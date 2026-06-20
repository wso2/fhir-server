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

package fhirxml

import (
	"strings"
	"testing"
)

func TestToXML_Patient(t *testing.T) {
	resource := map[string]any{
		"resourceType": "Patient",
		"id":           "p1",
		"active":       true,
		"gender":       "female",
		"name":         []any{map[string]any{"family": "Smith", "given": []any{"Alice"}}},
	}
	out, err := ToXML(resource)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `<Patient xmlns="http://hl7.org/fhir">`) {
		t.Errorf("missing Patient root with xmlns: %s", s[:min(len(s), 200)])
	}
	if !strings.Contains(s, `<id value="p1"`) {
		t.Errorf("missing id element: %s", s[:min(len(s), 200)])
	}
	if !strings.Contains(s, `<active value="true"`) {
		t.Errorf("missing active element: %s", s)
	}
	if !strings.Contains(s, `<gender value="female"`) {
		t.Errorf("missing gender element: %s", s)
	}
	if !strings.Contains(s, `<family value="Smith"`) {
		t.Errorf("missing family element: %s", s)
	}
}

func TestFromXML_Patient(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<Patient xmlns="http://hl7.org/fhir">
  <id value="p1"/>
  <active value="true"/>
  <gender value="female"/>
  <name>
    <family value="Smith"/>
    <given value="Alice"/>
  </name>
</Patient>`
	m, err := FromXML([]byte(xmlData))
	if err != nil {
		t.Fatal(err)
	}
	if m["resourceType"] != "Patient" {
		t.Errorf("resourceType: got %v", m["resourceType"])
	}
	if m["id"] != "p1" {
		t.Errorf("id: got %v", m["id"])
	}
	// active and gender come from value attributes
	if m["active"] != "true" {
		t.Errorf("active: got %v", m["active"])
	}
	if m["gender"] != "female" {
		t.Errorf("gender: got %v", m["gender"])
	}
	name, _ := m["name"].(map[string]any)
	if name == nil {
		t.Fatal("name not parsed as map")
	}
	if name["family"] != "Smith" {
		t.Errorf("name.family: got %v", name["family"])
	}
}

func TestFromXML_NoSyntheticResourceTypeOnChildren(t *testing.T) {
	// Nested complex elements must NOT receive a synthetic resourceType — only
	// the root resource carries it. Regression for the decodeStarted bug.
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<Patient xmlns="http://hl7.org/fhir">
  <name>
    <family value="Smith"/>
  </name>
</Patient>`
	m, err := FromXML([]byte(xmlData))
	if err != nil {
		t.Fatal(err)
	}
	if m["resourceType"] != "Patient" {
		t.Errorf("root resourceType: got %v", m["resourceType"])
	}
	name, _ := m["name"].(map[string]any)
	if name == nil {
		t.Fatalf("name not parsed as object: %T", m["name"])
	}
	if _, ok := name["resourceType"]; ok {
		t.Errorf("nested name element must not have a synthetic resourceType, got %v", name["resourceType"])
	}
	if name["family"] != "Smith" {
		t.Errorf("name.family: got %v", name["family"])
	}
}

func TestRoundTrip(t *testing.T) {
	original := map[string]any{
		"resourceType": "Observation",
		"id":           "obs1",
		"status":       "final",
		"code": map[string]any{
			"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8480-6", "display": "Systolic BP"}},
			"text":   "Systolic blood pressure",
		},
		"valueQuantity": map[string]any{
			"value":  float64(120),
			"unit":   "mmHg",
			"system": "http://unitsofmeasure.org",
			"code":   "mm[Hg]",
		},
	}
	xmlBytes, err := ToXML(original)
	if err != nil {
		t.Fatal("ToXML:", err)
	}
	back, err := FromXML(xmlBytes)
	if err != nil {
		t.Fatal("FromXML:", err)
	}
	if back["resourceType"] != "Observation" {
		t.Errorf("roundtrip resourceType: got %v", back["resourceType"])
	}
	if back["status"] != "final" {
		t.Errorf("roundtrip status: got %v", back["status"])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
