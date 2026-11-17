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

package index

import (
	"reflect"
	"testing"
)

// A string param over a complex type (Patient.name -> HumanName) must index its
// text parts, not fmt's "map[...]" rendering of the whole object.
func TestAppendString_ComplexType(t *testing.T) {
	rs := NewRowSet("t", 0)
	appendString(rs, "Patient", "p1", "name", []any{
		map[string]any{
			"use":    "official",
			"family": "Reyes",
			"given":  []any{"Daniel", "Jose"},
			"prefix": []any{"Dr."},
		},
	})
	appendString(rs, "Patient", "p1", "address", []any{
		map[string]any{"line": []any{"1 Main St"}, "city": "Boston", "use": "home"},
	})
	appendString(rs, "Patient", "p1", "family", []any{"Reyes"})

	var got [][2]string
	for _, r := range rs.spString {
		got = append(got, [2]string{r[3].(string), r[4].(string)})
	}
	want := [][2]string{
		{"name", "Reyes"}, {"name", "Daniel"}, {"name", "Jose"}, {"name", "Dr."},
		{"address", "1 Main St"}, {"address", "Boston"},
		{"family", "Reyes"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if lower := rs.spString[0][5]; lower != "reyes" {
		t.Errorf("value_lower = %v, want reyes", lower)
	}
}

// The same text under one param is stored once: a family shared by two names,
// or Address.text equal to city, must not produce duplicate rows.
func TestAppendString_DedupesValues(t *testing.T) {
	rs := NewRowSet("t", 0)
	appendString(rs, "Patient", "p1", "name", []any{
		map[string]any{"family": "Reyes", "given": []any{"Daniel"}},
		map[string]any{"family": "Reyes", "given": []any{"Dan", "Daniel"}},
	})
	appendString(rs, "Patient", "p1", "address", []any{
		map[string]any{"text": "Boston", "city": "Boston"},
	})

	var got [][2]string
	for _, r := range rs.spString {
		got = append(got, [2]string{r[3].(string), r[4].(string)})
	}
	want := [][2]string{
		{"name", "Reyes"}, {"name", "Daniel"}, {"name", "Dan"},
		{"address", "Boston"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// One param expanding into many rows must stop at maxRows and flag LimitHit,
// not only be checked between params.
func TestAppendString_EnforcesRowCap(t *testing.T) {
	rs := NewRowSet("t", 3)
	appendString(rs, "Patient", "p1", "name", []any{
		map[string]any{"family": "A", "given": []any{"B", "C", "D", "E"}},
		map[string]any{"family": "F"},
	})
	if got := len(rs.spString); got != 3 {
		t.Fatalf("rows = %d, want 3 (the cap)", got)
	}
	if !rs.LimitHit {
		t.Error("LimitHit not set after hitting the cap")
	}
}

// A null entry in a name's given array must not produce an empty or "<nil>" row.
func TestAppendString_NullInArray(t *testing.T) {
	rs := NewRowSet("t", 0)
	appendString(rs, "Patient", "p1", "name", []any{
		map[string]any{"family": "Reyes", "given": []any{"Daniel", nil}},
	})
	if got := len(rs.spString); got != 2 {
		t.Fatalf("rows = %v, want 2 (Reyes, Daniel)", rs.spString)
	}
}

// A complex value with none of the known text parts (e.g. an Identifier behind a
// custom string param) must still be indexed, as it was before parts were split.
func TestAppendString_UnknownMapFallsBack(t *testing.T) {
	rs := NewRowSet("t", 0)
	appendString(rs, "Patient", "p1", "custom", []any{
		map[string]any{"system": "http://example.org/mrn", "value": "MRN-1"},
	})
	if got := len(rs.spString); got != 1 {
		t.Fatalf("rows = %v, want 1 fallback row", rs.spString)
	}
	if v := rs.spString[0][4].(string); v != asString(map[string]any{"system": "http://example.org/mrn", "value": "MRN-1"}) {
		t.Errorf("value_exact = %q, want the asString rendering", v)
	}

	// A HumanName must NOT also get the fallback row.
	rs = NewRowSet("t", 0)
	appendString(rs, "Patient", "p1", "name", []any{map[string]any{"family": "Reyes", "use": "official"}})
	if got := len(rs.spString); got != 1 {
		t.Fatalf("HumanName rows = %v, want only the family row", rs.spString)
	}
}
