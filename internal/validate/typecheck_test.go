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

package validate

import (
	"strings"
	"testing"
)

// sd builds a minimal StructureDefinition snapshot for tests.
func sd(rootType string, elements ...map[string]any) map[string]any {
	els := []any{map[string]any{"path": rootType, "min": float64(0), "max": "*"}}
	for _, e := range elements {
		els = append(els, e)
	}
	return map[string]any{
		"resourceType": "StructureDefinition",
		"type":         rootType,
		"snapshot":     map[string]any{"element": els},
	}
}

func el(path, max string, types ...string) map[string]any {
	tl := make([]any, 0, len(types))
	for _, t := range types {
		tl = append(tl, map[string]any{"code": t})
	}
	return map[string]any{"path": path, "min": float64(0), "max": max, "type": tl}
}

// testPatient is a small Patient-like profile: scalar primitives, a repeating
// datatype, a complex scalar, and a choice element.
func testPatient() *Profile {
	return Compile(sd("Patient",
		el("Patient.active", "1", "boolean"),
		el("Patient.gender", "1", "code"),
		el("Patient.birthDate", "1", "date"),
		el("Patient.count", "1", "integer"),
		el("Patient.name", "*", "HumanName"),
		el("Patient.maritalStatus", "1", "CodeableConcept"),
		el("Patient.deceased[x]", "1", "boolean", "dateTime"),
	))
}

func testHumanName() *Profile {
	return Compile(sd("HumanName",
		el("HumanName.family", "1", "string"),
		el("HumanName.given", "*", "string"),
	))
}

func lookupHumanName(name string) *Profile {
	if name == "HumanName" {
		return testHumanName()
	}
	return nil
}

// expectIssueAt asserts that at least one issue anchors at the expression.
func expectIssueAt(t *testing.T, issues []Issue, expr string) {
	t.Helper()
	for _, iss := range issues {
		if iss.Expression == expr {
			if iss.Severity != "error" {
				t.Errorf("issue at %s: want severity error, got %s", expr, iss.Severity)
			}
			return
		}
	}
	var got []string
	for _, iss := range issues {
		got = append(got, iss.Expression)
	}
	t.Errorf("no issue at %q; issues at: %s", expr, strings.Join(got, ", "))
}

func check(body map[string]any) []Issue {
	return testPatient().CheckTypes(body, lookupHumanName)
}

func TestCheckTypes_ValidResource(t *testing.T) {
	issues := check(map[string]any{
		"resourceType":    "Patient",
		"active":          true,
		"gender":          "male",
		"birthDate":       "1974-12-25",
		"count":           float64(3),
		"name":            []any{map[string]any{"family": "Chalmers", "given": []any{"Peter", "James"}}},
		"maritalStatus":   map[string]any{"coding": []any{}}, // interior not described by the mini SDs
		"deceasedBoolean": false,
	})
	if len(issues) != 0 {
		t.Fatalf("want no issues, got %+v", issues)
	}
}

func TestCheckTypes_PrimitiveKinds(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		expr string
	}{
		{"boolean as number", map[string]any{"active": float64(1)}, "Patient.active"},
		{"boolean as string", map[string]any{"active": "true"}, "Patient.active"},
		{"boolean as object", map[string]any{"active": map[string]any{"id": "1"}}, "Patient.active"},
		{"integer with fraction", map[string]any{"count": 3.1}, "Patient.count"},
		{"integer as string", map[string]any{"count": "42"}, "Patient.count"},
		{"integer overflow", map[string]any{"count": float64(2147483648)}, "Patient.count"},
		{"empty date", map[string]any{"birthDate": ""}, "Patient.birthDate"},
		{"malformed date", map[string]any{"birthDate": "not-a-date"}, "Patient.birthDate"},
		{"month 13", map[string]any{"birthDate": "2020-13-01"}, "Patient.birthDate"},
		{"code with double space", map[string]any{"gender": "male  female"}, "Patient.gender"},
		{"null primitive", map[string]any{"birthDate": nil}, "Patient.birthDate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectIssueAt(t, check(tc.body), tc.expr)
		})
	}
}

func TestCheckTypes_Shapes(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		expr string
	}{
		{"object at repeating element", map[string]any{"name": map[string]any{"family": "X"}}, "Patient.name"},
		{"array at scalar element", map[string]any{"gender": []any{"male"}}, "Patient.gender"},
		{"empty array", map[string]any{"name": []any{}}, "Patient.name"},
		{"empty object entry", map[string]any{"name": []any{map[string]any{}}}, "Patient.name[0]"},
		{"null entry in complex array", map[string]any{"name": []any{nil}}, "Patient.name[0]"},
		{"empty complex object", map[string]any{"maritalStatus": map[string]any{}}, "Patient.maritalStatus"},
		{"null complex", map[string]any{"maritalStatus": nil}, "Patient.maritalStatus"},
		{"primitive at complex element", map[string]any{"maritalStatus": "married"}, "Patient.maritalStatus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectIssueAt(t, check(tc.body), tc.expr)
		})
	}
}

func TestCheckTypes_DatatypeInterior(t *testing.T) {
	issues := check(map[string]any{
		"name": []any{map[string]any{"given": []any{map[string]any{"id": "1"}}}},
	})
	expectIssueAt(t, issues, "Patient.name[0].given[0]")
}

func TestCheckTypes_ChoiceElement(t *testing.T) {
	issues := check(map[string]any{"deceasedBoolean": "yes"})
	expectIssueAt(t, issues, "Patient.deceased.ofType(boolean)")

	if got := check(map[string]any{"deceasedDateTime": "2020-01-01"}); len(got) != 0 {
		t.Errorf("valid deceasedDateTime: want no issues, got %+v", got)
	}
	issues = check(map[string]any{"deceasedDateTime": "01/02/2020"})
	expectIssueAt(t, issues, "Patient.deceased.ofType(dateTime)")
}

func TestCheckTypes_PrimitiveExtensions(t *testing.T) {
	t.Run("valid pairing", func(t *testing.T) {
		issues := check(map[string]any{
			"birthDate":  "1974-12-25",
			"_birthDate": map[string]any{"extension": []any{map[string]any{"url": "http://x", "valueDateTime": "2020"}}},
		})
		if len(issues) != 0 {
			t.Fatalf("want no issues, got %+v", issues)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		expectIssueAt(t, check(map[string]any{"_unknown": map[string]any{"id": "1"}}), "Patient.unknown")
	})
	t.Run("non-primitive field", func(t *testing.T) {
		expectIssueAt(t, check(map[string]any{"_name": map[string]any{"id": "1"}}), "Patient.name")
	})
	t.Run("scalar subpart as string", func(t *testing.T) {
		expectIssueAt(t, check(map[string]any{"_active": "test"}), "Patient.active")
	})
	t.Run("scalar subpart as array", func(t *testing.T) {
		expectIssueAt(t, check(map[string]any{"_active": []any{map[string]any{"id": "1"}}}), "Patient.active")
	})
	t.Run("empty subpart object", func(t *testing.T) {
		expectIssueAt(t, check(map[string]any{"_gender": map[string]any{}}), "Patient.gender")
	})
	t.Run("null subpart", func(t *testing.T) {
		expectIssueAt(t, check(map[string]any{"_gender": nil}), "Patient.gender")
	})
	t.Run("repeated subpart must be array", func(t *testing.T) {
		issues := check(map[string]any{
			"name": []any{map[string]any{"given": []any{"a"}, "_given": map[string]any{"id": "1"}}},
		})
		expectIssueAt(t, issues, "Patient.name[0].given")
	})
	t.Run("unknown key inside Element", func(t *testing.T) {
		issues := check(map[string]any{
			"name": []any{map[string]any{"given": []any{"a"}, "_given": []any{map[string]any{"foo": "x"}}}},
		})
		expectIssueAt(t, issues, "Patient.name[0].given[0]")
	})
}

func TestCheckTypes_NullFillsInPrimitiveArrays(t *testing.T) {
	t.Run("null filled by extension is valid", func(t *testing.T) {
		issues := check(map[string]any{
			"name": []any{map[string]any{
				"given":  []any{nil, "test"},
				"_given": []any{map[string]any{"extension": []any{map[string]any{"url": "http://x", "valueString": "v"}}}, nil},
			}},
		})
		if len(issues) != 0 {
			t.Fatalf("want no issues, got %+v", issues)
		}
	})
	t.Run("null with id-only counterpart is invalid", func(t *testing.T) {
		issues := check(map[string]any{
			"name": []any{map[string]any{
				"given":  []any{nil, "test"},
				"_given": []any{map[string]any{"id": "x"}, nil},
			}},
		})
		expectIssueAt(t, issues, "Patient.name[0].given[0]")
	})
	t.Run("null without counterpart is invalid", func(t *testing.T) {
		issues := check(map[string]any{
			"name": []any{map[string]any{"given": []any{nil}}},
		})
		expectIssueAt(t, issues, "Patient.name[0].given[0]")
	})
	t.Run("all-null subpart array is valid", func(t *testing.T) {
		issues := check(map[string]any{
			"name": []any{map[string]any{"given": []any{"a"}, "_given": []any{nil, nil}}},
		})
		if len(issues) != 0 {
			t.Fatalf("want no issues, got %+v", issues)
		}
	})
}

func TestCheckTypes_UnknownPlainKeysAreNotPoliced(t *testing.T) {
	issues := check(map[string]any{"customField": "whatever"})
	if len(issues) != 0 {
		t.Fatalf("unknown plain keys must be skipped, got %+v", issues)
	}
}

func TestCheckTypes_NilProfileAndLookup(t *testing.T) {
	var p *Profile
	if got := p.CheckTypes(map[string]any{"x": 1}, nil); got != nil {
		t.Fatalf("nil profile: want nil, got %+v", got)
	}
	// nil lookup: datatype interiors are skipped, own elements still checked.
	issues := testPatient().CheckTypes(map[string]any{"active": "true"}, nil)
	expectIssueAt(t, issues, "Patient.active")
}

func TestPrimitiveChecks_Lexical(t *testing.T) {
	valid := map[string][]any{
		"integer":      {float64(0), float64(-2147483648), float64(2147483647)},
		"positiveInt":  {float64(1), float64(2147483647)},
		"unsignedInt":  {float64(0)},
		"decimal":      {3.14, float64(2)},
		"date":         {"2020", "2020-06", "2020-06-15", "2024-02-29", "2000-02-29"},
		"dateTime":     {"2020", "2020-06-15", "1974-12-25T14:35:45-05:00", "2015-02-07T13:28:17Z"},
		"instant":      {"2015-02-07T13:28:17.239+02:00", "2017-01-01T00:00:00Z"},
		"time":         {"13:28:17", "00:00:00.000"},
		"code":         {"male", "two tokens"},
		"id":           {"abc-123.DEF"},
		"oid":          {"urn:oid:1.2.3.4"},
		"uuid":         {"urn:uuid:53fefa32-fcbb-4ff8-8a92-55ee120877b7"},
		"uri":          {"http://example.org/x"},
		"base64Binary": {"QmFzZTY0"},
	}
	invalid := map[string][]any{
		"integer":      {3.1, "42", true, float64(2147483648), float64(-2147483649)},
		"positiveInt":  {float64(0), float64(-1), 9.2},
		"unsignedInt":  {float64(-1), true},
		"decimal":      {"3.14", true},
		"date":         {"", "20200615", "2020-6-15", "2020-00-10", "2020-01-32", "2023-02-29", "2024-02-31", "1900-02-29"},
		"dateTime":     {"2020-06-15T13:28:17", "01/02/2020", "2020-06-15T25:00:00Z", "2023-02-29", "2017-01-01T00:00:00.0000000000Z"},
		"instant":      {"2020", "2020-06-15", "2015-02-07T13:28:17", "2017-01-01T00:00:00.0000000000Z"},
		"time":         {"24:00:00", "13:28", "", "12:00:00.0000000000"},
		"code":         {" male", "male ", "a  b", ""},
		"id":           {"", "has space", strings.Repeat("a", 65)},
		"oid":          {"1.2.3", "urn:oid:"},
		"uuid":         {"53fefa32-fcbb-4ff8-8a92-55ee120877b7", "urn:uuid:XYZ"},
		"uri":          {"has space", ""},
		"base64Binary": {"abc", ""},
	}
	for typ, vals := range valid {
		for _, v := range vals {
			if !primitiveChecks[typ](v) {
				t.Errorf("%s: %v should be valid", typ, v)
			}
		}
	}
	for typ, vals := range invalid {
		for _, v := range vals {
			if primitiveChecks[typ](v) {
				t.Errorf("%s: %v should be invalid", typ, v)
			}
		}
	}
}
