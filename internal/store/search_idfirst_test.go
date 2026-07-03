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
