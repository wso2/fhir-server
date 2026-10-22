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
