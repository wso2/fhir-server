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

// fetchSQL produces two id-first shapes, both distinct from the plain
// ordered-scan (which is a bare "FROM resources r WHERE …"):
//   - correlated id-first (composite / multi-predicate): a MATERIALIZED CTE.
//   - direct-drive (a lone numeric predicate sorted by a resources column): a
//     DISTINCT candidate subquery driven straight off the sp_* index, with the
//     ORDER BY + LIMIT pushed in so a dense predicate early-exits.
const idFirstMarker = "WITH candidates AS MATERIALIZED"
const directDriveMarker = "SELECT DISTINCT"

// usesIdFirst reports whether the fetch SQL is one of the id-first shapes
// (rather than the plain ordered scan over resources).
func usesIdFirst(sql string) bool {
	return strings.Contains(sql, idFirstMarker) || strings.Contains(sql, directDriveMarker)
}

func idFirstTestRegistry() *searchparam.Registry {
	reg := searchparam.NewRegistry()
	for _, d := range []searchparam.Definition{
		{ResourceType: "Observation", ParamName: "value-quantity", ParamType: "quantity", FHIRPath: "Observation.value.ofType(Quantity)"},
		{ResourceType: "Observation", ParamName: "code", ParamType: "token", FHIRPath: "Observation.code"},
		{ResourceType: "Observation", ParamName: "subject", ParamType: "reference", Targets: []string{"Patient"}},
		{ResourceType: "Observation", ParamName: "date", ParamType: "date"},
		{ResourceType: "Patient", ParamName: "name", ParamType: "string"},
		// A numeric Patient param, reached only via a chained search (subject.pat-qty).
		{ResourceType: "Patient", ParamName: "pat-qty", ParamType: "quantity", FHIRPath: "Patient.patQty"},
		// A composite embedding a token + quantity component — exercises the
		// direct-drive suppression inside buildCompositeExists.
		{ResourceType: "Observation", ParamName: "code-value-quantity", ParamType: "composite",
			Components: []searchparam.ComponentDef{
				{Expression: "Observation.code"},
				{Expression: "Observation.value.ofType(Quantity)"},
			}},
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
		// token/reference equality now drive the sp-first ordered walk off their
		// recency indexes (plan-selection standard §3.2).
		{"token uses id-first", "Observation", "code", "8302-2", true},
		{"reference uses id-first", "Observation", "subject", "Patient/123", true},
		// sp_date joined the id-first family (design-addendum Phase 3.1) once it
		// gained a last_updated recency column.
		{"date uses id-first", "Observation", "date", "gt2020", true},
		{"string keeps ordered scan", "Patient", "name", "Smith", false},
		{"negated quantity keeps ordered scan", "Observation", "value-quantity:not", "gt170", false},
		// :not token stays on the correlated path (negation cannot use the walk).
		{"negated token keeps ordered scan", "Observation", "code:not", "8302-2", false},
		{"plain browse keeps ordered scan", "Observation", "_lastUpdated", "gt2020", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := buildSQL(t, tc.rt, tc.key, tc.value)
			got := usesIdFirst(sql)
			if got != tc.wantIdFirst {
				t.Fatalf("id-first=%v, want %v\nSQL:\n%s", got, tc.wantIdFirst, sql)
			}
		})
	}
}

// A multi-value reference search (comma = OR) must coalesce into a single EXISTS
// with target_id = ANY(...), not OR-ed separate EXISTS subqueries. The lone
// EXISTS lets Postgres drive from the sp_reference target index; OR-of-EXISTS
// defeats that inversion and forces a full recency-walk over resources.
func TestMultiValueReferenceUsesAnyArray(t *testing.T) {
	cases := []struct {
		name       string
		key, value string
		wantTypes  []string // target_type literals expected in the SQL
	}{
		{"same type", "subject", "Patient/1,Patient/2", []string{"Patient"}},
		{"typed via modifier", "subject:Patient", "1,2,3", []string{"Patient"}},
		{"mixed types grouped", "subject", "Patient/1,Group/2,Patient/3", []string{"Patient", "Group"}},
		{"bare ids", "subject", "1,2", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &queryBuilder{rt: "Observation", reg: idFirstTestRegistry()}
			b.writeBase()
			b.applyParam(tc.key, tc.value)
			if b.err != nil {
				t.Fatal(b.err)
			}
			sql := b.fetchSQL(20, 0)
			if !strings.Contains(sql, "= ANY(") {
				t.Fatalf("multi-value reference should use = ANY(...)\nSQL:\n%s", sql)
			}
			if strings.Contains(sql, "OR EXISTS") {
				t.Fatalf("multi-value reference must not OR separate EXISTS subqueries\nSQL:\n%s", sql)
			}
			if n := strings.Count(sql, "FROM sp_reference s"); n != 1 {
				t.Fatalf("want exactly one sp_reference scan, got %d\nSQL:\n%s", n, sql)
			}
			for _, ty := range tc.wantTypes {
				if !strings.Contains(sql, "s.target_type = ") {
					t.Fatalf("expected a target_type predicate for %q\nSQL:\n%s", ty, sql)
				}
			}
			// Bare ids carry no target_type filter.
			if tc.wantTypes == nil && strings.Contains(sql, "s.target_type") {
				t.Fatalf("bare-id reference should not filter target_type\nSQL:\n%s", sql)
			}
			assertParamsContiguous(t, sql, len(b.args))
		})
	}
}

// A lone token+quantity composite resolves candidates by driving directly off
// the single sp_composite_token_quantity table (its rows already pair the two
// components per element), with an early-exit DISTINCT subquery — not the
// correlated CTE that scans resources first, and not the legacy two-table
// sp_token/sp_quantity drive.
func TestCompositeUsesSingleTableDrive(t *testing.T) {
	b := &queryBuilder{rt: "Observation", reg: idFirstTestRegistry()}
	b.writeBase()
	b.applyParam("code-value-quantity", "8480-6$gt110")
	if b.err != nil {
		t.Fatal(b.err)
	}
	if b.comp != nil {
		t.Fatal("token+quantity composite must not use the legacy two-table drive")
	}
	if b.numericTable != "sp_composite_token_quantity" {
		t.Fatalf("expected direct-drive off sp_composite_token_quantity, got numericTable=%q", b.numericTable)
	}
	if !b.directDrive(b.orderTerms()) {
		t.Fatal("a lone token+quantity composite must direct-drive")
	}
	sql := b.fetchSQL(20, 0)
	t.Logf("COMPOSITE_DRIVE_SQL_BEGIN\n%s\nCOMPOSITE_DRIVE_SQL_END", sql)
	for _, want := range []string{
		directDriveMarker, // early-exit DISTINCT subquery, not a MATERIALIZED CTE
		"FROM sp_composite_token_quantity s",
		"s.last_updated AS sort0",   // recency sort key from the table, mapped
		"ORDER BY sort0 DESC LIMIT", // ORDER BY + LIMIT pushed in for early-exit
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("composite drive SQL missing %q\nSQL:\n%s", want, sql)
		}
	}
	// One table: no correlated EXISTS into a second sp_* table, and no MATERIALIZED
	// barrier (that was the correlated / full-intersection shape).
	if strings.Contains(sql, "EXISTS (SELECT 1 FROM sp_quantity") {
		t.Fatalf("single-table drive should not correlate into sp_quantity\nSQL:\n%s", sql)
	}
	if strings.Contains(sql, idFirstMarker) {
		t.Fatalf("composite drive should be early-exit, not a MATERIALIZED CTE\nSQL:\n%s", sql)
	}
	assertParamsContiguous(t, sql, len(b.args))
}

// A composite embedded in a chained search must NOT be captured as a two-table
// drive (it is not the sole top-level predicate); it keeps the correlated shape.
func TestNestedCompositeNotDriven(t *testing.T) {
	b := &queryBuilder{rt: "Observation", reg: idFirstTestRegistry()}
	b.writeBase()
	// A composite plus another predicate: predicateCount > 1, drive must not engage.
	b.applyParam("code-value-quantity", "8480-6$gt110")
	b.applyParam("date", "gt2020")
	if b.err != nil {
		t.Fatal(b.err)
	}
	if b.compositeDriveOK(b.orderTerms()) {
		t.Fatal("composite drive must not engage when other predicates are present")
	}
	assertParamsContiguous(t, b.fetchSQL(20, 0), len(b.args))
}

// A single-value reference keeps the plain equality form (no ANY), so we do not
// regress the common case into an array bind.
func TestSingleValueReferenceKeepsEquality(t *testing.T) {
	sql := buildSQL(t, "Observation", "subject", "Patient/123")
	if strings.Contains(sql, "= ANY(") {
		t.Fatalf("single-value reference should use plain equality, not ANY(...)\nSQL:\n%s", sql)
	}
	if !strings.Contains(sql, "s.target_id = $") {
		t.Fatalf("single-value reference should match target_id by equality\nSQL:\n%s", sql)
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
// id-first shape: a DISTINCT candidate subquery resolves straight off the sp_*
// index (FROM sp_quantity s) with the ORDER BY + LIMIT pushed in so a dense
// predicate early-exits, rather than scanning resources with a correlated
// EXISTS or materialising the whole match set in a CTE.
func TestFetchSQL_SoleNumericUsesDirectDrive(t *testing.T) {
	b := &queryBuilder{rt: "Observation", reg: idFirstTestRegistry()}
	b.writeBase()
	b.applyParam("value-quantity", "gt170")
	if b.err != nil {
		t.Fatal(b.err)
	}
	sql := b.fetchSQL(20, 0)
	for _, want := range []string{directDriveMarker, "FROM sp_quantity s", "s.resource_id AS fhir_id", "s.last_updated", "LIMIT"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("direct-drive SQL missing %q\nSQL:\n%s", want, sql)
		}
	}
	// Early-exit shape: no MATERIALIZED barrier (which would force resolving the
	// whole match set before the LIMIT) and no correlated EXISTS over resources.
	if strings.Contains(sql, idFirstMarker) {
		t.Fatalf("direct-drive must not use a MATERIALIZED CTE (defeats early-exit)\nSQL:\n%s", sql)
	}
	if strings.Contains(sql, "EXISTS (SELECT 1 FROM sp_quantity") {
		t.Fatalf("direct-drive should not use a correlated EXISTS\nSQL:\n%s", sql)
	}
}

// A numeric value embedded in a nested context (composite component, chained
// target, or _has value) must never be captured as a direct-drive candidate —
// only a bare top-level numeric predicate qualifies. Capturing it would drive the
// fetch off just that embedded body, dropping the surrounding structure (wrong
// results, orphaned params → SQLSTATE 42P18).
func TestNestedNumericNotDirectDrive(t *testing.T) {
	// A lone top-level token+quantity composite is NOT nested — it legitimately
	// direct-drives off sp_composite_token_quantity (see TestCompositeUsesSingleTableDrive).
	// Only a numeric embedded in a chained target or a _has value is nested.
	cases := []struct{ name, rt, key, val string }{
		{"chained target", "Observation", "subject:Patient.pat-qty", "gt5"},
		{"_has value", "Patient", "_has:Observation:subject:value-quantity", "gt5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &queryBuilder{rt: tc.rt, reg: idFirstTestRegistry()}
			b.writeBase()
			b.applyParam(tc.key, tc.val)
			if b.err != nil {
				t.Fatal(b.err)
			}
			if b.numericTable != "" {
				t.Errorf("numericTable=%q; a nested numeric must not be captured for direct-drive", b.numericTable)
			}
			if b.directDrive(b.orderTerms()) {
				t.Error("directDrive()=true; a nested numeric must not direct-drive")
			}
			assertParamsContiguous(t, b.fetchSQL(20, 0), len(b.args))
		})
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
		{"composite token+quantity (single-table drive)", "Observation", [][2]string{{"code-value-quantity", "8480-6$gt110"}}, ""},
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

// directDrive engages for a lone sp-drivable predicate sorted by a resources
// column — numeric (quantity/number/date) or equality (token/reference). Any
// multi-predicate search must not qualify — it falls back to the correlated
// id-first shape.
func TestDirectDriveGating(t *testing.T) {
	cases := []struct {
		name       string
		params     [][2]string
		wantDirect bool
		wantCount  int
	}{
		{"sole quantity", [][2]string{{"value-quantity", "gt170"}}, true, 1},
		{"sole token", [][2]string{{"code", "8302-2"}}, true, 1},
		{"quantity plus token", [][2]string{{"value-quantity", "gt170"}, {"code", "8302-2"}}, false, 2},
		{"token+quantity composite (single-table drive)", [][2]string{{"code-value-quantity", "8480-6$gt110"}}, true, 1},
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
