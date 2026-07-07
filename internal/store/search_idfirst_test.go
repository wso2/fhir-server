// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied. See the License for the
// specific language governing permissions and limitations
// under the License.

package store

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/wso2/fhir-server/internal/searchparam"
)

// idFirstMarker uniquely identifies the id-first fetch shape produced by
// fetchSQL. The ordered-scan shape never contains a CTE.
const idFirstMarker = "WITH candidates AS MATERIALIZED"

func idFirstTestRegistry() *searchparam.Registry {
	reg := searchparam.NewRegistry()
	for _, d := range []searchparam.Definition{
		{ResourceType: "Observation", ParamName: "value-quantity", ParamType: "quantity"},
		{ResourceType: "Observation", ParamName: "code", ParamType: "token"},
		{ResourceType: "Observation", ParamName: "subject", ParamType: "reference", Targets: []string{"Patient"}},
		{ResourceType: "Observation", ParamName: "date", ParamType: "date"},
		{ResourceType: "Patient", ParamName: "name", ParamType: "string"},
	} {
		reg.Upsert(d)
	}
	return reg
}

// buildSQL runs a single-param search through the builder and returns the fetch
// SQL it would execute for the first page.
func buildSQL(t *testing.T, rt, rawKey, value string) string {
	t.Helper()
	b := &queryBuilder{rt: rt, reg: idFirstTestRegistry()}
	b.writeBase()
	b.applyParam(rawKey, value)
	if b.err != nil {
		t.Fatalf("applyParam(%q=%q): %v", rawKey, value, b.err)
	}
	return b.fetchSQL(20, 0)
}

func TestFetchSQL_IdFirstGating(t *testing.T) {
	cases := []struct {
		name        string
		rt          string
		key, value  string
		wantIdFirst bool
	}{
		{"quantity uses id-first", "Observation", "value-quantity", "gt170", true},
		{"token keeps ordered scan", "Observation", "code", "8302-2", false},
		{"reference keeps ordered scan", "Observation", "subject", "Patient/123", false},
		{"date keeps ordered scan", "Observation", "date", "gt2020", false},
		{"string keeps ordered scan", "Patient", "name", "Smith", false},
		{"negated quantity keeps ordered scan", "Observation", "value-quantity:not", "gt170", false},
		{"plain browse keeps ordered scan", "Observation", "_lastUpdated", "gt2020", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := buildSQL(t, tc.rt, tc.key, tc.value)
			got := strings.Contains(sql, idFirstMarker)
			if got != tc.wantIdFirst {
				t.Fatalf("id-first=%v, want %v\nSQL:\n%s", got, tc.wantIdFirst, sql)
			}
		})
	}
}

// A search that mixes a reference filter (ordered-scan-friendly) with a quantity
// filter must still use id-first: the quantity predicate is the one that can
// degrade into a full scan.
func TestFetchSQL_MixedParamsUseIdFirst(t *testing.T) {
	b := &queryBuilder{rt: "Observation", reg: idFirstTestRegistry()}
	b.writeBase()
	b.applyParam("subject", "Patient/123")
	b.applyParam("value-quantity", "gt170")
	if b.err != nil {
		t.Fatal(b.err)
	}
	if sql := b.fetchSQL(20, 0); !strings.Contains(sql, idFirstMarker) {
		t.Fatalf("expected id-first for mixed reference+quantity search\nSQL:\n%s", sql)
	}
}

// The id-first query must carry the sort key through the candidate CTE and
// re-apply it after the resource_json join, so ordering survives pagination.
func TestFetchSQL_IdFirstCarriesSortKey(t *testing.T) {
	b := &queryBuilder{rt: "Observation", reg: idFirstTestRegistry()}
	b.writeBase()
	b.applyParam("value-quantity", "gt170")
	b.addSort("-date")
	if b.err != nil {
		t.Fatal(b.err)
	}
	sql := b.fetchSQL(20, 0)
	for _, want := range []string{idFirstMarker, "AS sort0", "ORDER BY sort0", "ORDER BY c.sort0"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("id-first sort SQL missing %q\nSQL:\n%s", want, sql)
		}
	}
}

// A sole numeric predicate sorted by a resources column uses the direct-drive
// id-first shape: the candidate CTE resolves straight off the sp_* value index
// (FROM sp_quantity s) using the denormalised last_updated, rather than scanning
// resources with a correlated EXISTS.
func TestFetchSQL_SoleNumericUsesDirectDrive(t *testing.T) {
	b := &queryBuilder{rt: "Observation", reg: idFirstTestRegistry()}
	b.writeBase()
	b.applyParam("value-quantity", "gt170")
	if b.err != nil {
		t.Fatal(b.err)
	}
	sql := b.fetchSQL(20, 0)
	for _, want := range []string{idFirstMarker, "FROM sp_quantity s", "s.resource_id AS fhir_id", "s.last_updated"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("direct-drive SQL missing %q\nSQL:\n%s", want, sql)
		}
	}
	// The candidate CTE resolves off sp_quantity, not a correlated EXISTS over resources.
	if strings.Contains(sql, "EXISTS (SELECT 1 FROM sp_quantity") {
		t.Fatalf("direct-drive should not use a correlated EXISTS\nSQL:\n%s", sql)
	}
}

var paramRefRE = regexp.MustCompile(`\$(\d+)`)

// assertParamsContiguous fails if the SQL does not reference exactly $1..$nargs.
// A bound-but-unreferenced placeholder (a gap) makes Postgres unable to infer the
// parameter's type at execute time — "could not determine data type of parameter
// $N" (SQLSTATE 42P18) — which substring assertions alone would miss.
func assertParamsContiguous(t *testing.T, sql string, nargs int) {
	t.Helper()
	seen := map[int]bool{}
	for _, m := range paramRefRE.FindAllStringSubmatch(sql, -1) {
		n, _ := strconv.Atoi(m[1])
		seen[n] = true
	}
	for i := 1; i <= nargs; i++ {
		if !seen[i] {
			t.Errorf("parameter $%d is bound but never referenced (orphan → SQLSTATE 42P18)\nSQL:\n%s", i, sql)
		}
	}
	for n := range seen {
		if n < 1 || n > nargs {
			t.Errorf("SQL references $%d but only %d args are bound\nSQL:\n%s", n, nargs, sql)
		}
	}
}

// Every fetch shape must reference exactly the parameters it binds. A gap orphans
// a placeholder and fails at execute time with SQLSTATE 42P18 — the direct-drive
// shape hit this by binding writeBase's $1 without referencing it.
func TestFetchSQL_NoOrphanParams(t *testing.T) {
	cases := []struct {
		name   string
		rt     string
		params [][2]string
		sort   string
	}{
		{"sole quantity (direct-drive)", "Observation", [][2]string{{"value-quantity", "gt170"}}, ""},
		{"quantity eq two-bound (direct-drive)", "Observation", [][2]string{{"value-quantity", "5"}}, ""},
		{"quantity OR list (direct-drive)", "Observation", [][2]string{{"value-quantity", "gt10,lt5"}}, ""},
		{"quantity system|code (direct-drive)", "Observation", [][2]string{{"value-quantity", "gt5|http://unitsofmeasure.org|mg"}}, ""},
		{"mixed ref+quantity (correlated id-first)", "Observation", [][2]string{{"subject", "Patient/123"}, {"value-quantity", "gt170"}}, ""},
		{"quantity sorted by sp_ param (correlated id-first)", "Observation", [][2]string{{"value-quantity", "gt170"}}, "-date"},
		{"token (single-scan)", "Observation", [][2]string{{"code", "8302-2"}}, ""},
		{"plain browse (single-scan)", "Observation", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &queryBuilder{rt: tc.rt, reg: idFirstTestRegistry()}
			b.writeBase()
			for _, p := range tc.params {
				b.applyParam(p[0], p[1])
			}
			if tc.sort != "" {
				b.addSort(tc.sort)
			}
			if b.err != nil {
				t.Fatal(b.err)
			}
			sql := b.fetchSQL(20, 0)
			assertParamsContiguous(t, sql, len(b.args))
		})
	}
}

// directDrive engages only for a lone numeric predicate sorted by a resources
// column. A token predicate (no id-first) and any multi-predicate search must not
// qualify — they fall back to the ordered scan or the correlated id-first shape.
func TestDirectDriveGating(t *testing.T) {
	cases := []struct {
		name       string
		params     [][2]string
		wantDirect bool
		wantCount  int
	}{
		{"sole quantity", [][2]string{{"value-quantity", "gt170"}}, true, 1},
		{"sole token", [][2]string{{"code", "8302-2"}}, false, 1},
		{"quantity plus token", [][2]string{{"value-quantity", "gt170"}, {"code", "8302-2"}}, false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &queryBuilder{rt: "Observation", reg: idFirstTestRegistry()}
			b.writeBase()
			for _, p := range tc.params {
				b.applyParam(p[0], p[1])
			}
			if b.err != nil {
				t.Fatal(b.err)
			}
			if b.predicateCount != tc.wantCount {
				t.Errorf("predicateCount=%d, want %d", b.predicateCount, tc.wantCount)
			}
			if got := b.directDrive(b.orderTerms()); got != tc.wantDirect {
				t.Errorf("directDrive()=%v, want %v", got, tc.wantDirect)
			}
		})
	}
}
