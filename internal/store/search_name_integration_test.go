//go:build integration

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

package store_test

import (
	"context"
	"testing"

	"github.com/wso2/fhir-server/internal/store"
)

// Regression for issue #82: string search params over HumanName/Address must
// match on the individual text parts, not on a rendering of the whole object.
func TestSearch_HumanNameAndAddressParts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, "Patient", map[string]any{
		"resourceType": "Patient",
		"name": []any{map[string]any{
			"family": "Reyes",
			"given":  []any{"Daniel", nil},
			"prefix": []any{"Dr."},
			"suffix": []any{"Jr."},
		}},
		"address": []any{map[string]any{
			"text":       "1 Main St, Springfield",
			"line":       []any{"1 Main St"},
			"city":       "Springfield",
			"district":   "Hampden",
			"state":      "MA",
			"postalCode": "01103",
			"country":    "USA",
		}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := created["id"].(string)
	// A second patient that must never match any of the searches below.
	if _, err := s.Create(ctx, "Patient", map[string]any{
		"resourceType": "Patient",
		"name":         []any{map[string]any{"family": "Other", "given": []any{"Someone"}}},
	}); err != nil {
		t.Fatalf("Create other: %v", err)
	}

	search := func(param, value string) int {
		t.Helper()
		res, err := s.Search(ctx, store.SearchParams{
			ResourceType: "Patient",
			Params:       map[string][]string{param: {value}},
		})
		if err != nil {
			t.Fatalf("Search %s=%s: %v", param, value, err)
		}
		for _, e := range res.Entries {
			if e["id"] != id {
				t.Errorf("%s=%s returned unexpected patient %v", param, value, e["id"])
			}
		}
		return len(res.Entries)
	}

	cases := []struct {
		param, value string
		want         int
	}{
		// HumanName parts (issue #82).
		{"name", "reyes", 1},
		{"name", "Daniel", 1},
		{"name", "rey", 1},
		{"name:exact", "Reyes", 1},
		{"name:exact", "reyes", 0},
		{"name:exact", "Rey", 0},
		{"name", "Dr.", 1},
		{"name", "Jr.", 1},
		{"name:contains", "anie", 1},
		{"name", "nobody", 0},
		// Address parts.
		{"address", "1 Main", 1}, // text and line
		{"address", "Springfield", 1},
		{"address", "Hampden", 1}, // district
		{"address", "MA", 1},      // state
		{"address", "01103", 1},   // postalCode
		{"address", "USA", 1},     // country
		{"address-city", "Springfield", 1},
		{"address-state", "MA", 1},
		{"address-postalcode", "01103", 1},
		{"address-country", "USA", 1},
		{"address", "Nowhere", 0},
	}
	for _, c := range cases {
		if got := search(c.param, c.value); got != c.want {
			t.Errorf("%s=%s: got %d results, want %d", c.param, c.value, got, c.want)
		}
	}
}

// _sort=name must order people by family name, not by whichever text part of
// the name (prefix, given) happens to sort lowest.
func TestSearch_SortByNameUsesFamily(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	for _, n := range []map[string]any{
		{"family": "Zulu", "given": []any{"Amy"}},
		{"family": "Young", "given": []any{"Zack"}},
		{"family": "Xray", "given": []any{"Bob"}, "prefix": []any{"Dr."}},
	} {
		if _, err := s.Create(ctx, "Patient", map[string]any{
			"resourceType": "Patient",
			"name":         []any{n},
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	families := func(sort string) []string {
		res, err := s.Search(ctx, store.SearchParams{
			ResourceType: "Patient",
			Params:       map[string][]string{"_sort": {sort}},
		})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		var out []string
		for _, e := range res.Entries {
			n := e["name"].([]any)[0].(map[string]any)
			out = append(out, n["family"].(string))
		}
		return out
	}

	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	if got, want := families("name"), []string{"Xray", "Young", "Zulu"}; !eq(got, want) {
		t.Errorf("_sort=name: got %v, want %v", got, want)
	}
	if got, want := families("-name"), []string{"Zulu", "Young", "Xray"}; !eq(got, want) {
		t.Errorf("_sort=-name: got %v, want %v", got, want)
	}
}
