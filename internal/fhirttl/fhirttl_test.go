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

package fhirttl

import (
	"strings"
	"testing"
)

func TestToTurtle_Basic(t *testing.T) {
	r := map[string]any{
		"resourceType": "Patient",
		"id":           "p1",
		"active":       true,
		"gender":       "female",
		"name":         []any{map[string]any{"family": "Smith"}},
	}
	out, err := ToTurtle(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "a fhir:Patient") {
		t.Errorf("missing type: %s", s)
	}
	if !strings.Contains(s, `fhir:gender "female"`) {
		t.Errorf("missing gender: %s", s)
	}
	if !strings.Contains(s, "fhir:active true") {
		t.Errorf("missing active: %s", s)
	}
}

func TestTurtle_RoundTrip(t *testing.T) {
	original := map[string]any{
		"resourceType": "Observation",
		"id":           "obs1",
		"status":       "final",
		"code": map[string]any{
			"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8480-6"}},
			"text":   "Systolic BP",
		},
		"valueQuantity": map[string]any{"value": float64(120), "unit": "mmHg"},
	}
	ttl, err := ToTurtle(original)
	if err != nil {
		t.Fatal("ToTurtle:", err)
	}
	back, err := FromTurtle(ttl)
	if err != nil {
		t.Fatalf("FromTurtle: %v\n--- turtle ---\n%s", err, string(ttl))
	}
	if back["resourceType"] != "Observation" {
		t.Errorf("resourceType: got %v", back["resourceType"])
	}
	if back["status"] != "final" {
		t.Errorf("status: got %v", back["status"])
	}
	code, _ := back["code"].(map[string]any)
	if code == nil {
		t.Fatalf("code not parsed as object: %T", back["code"])
	}
	if code["text"] != "Systolic BP" {
		t.Errorf("code.text: got %v", code["text"])
	}
	coding, _ := code["coding"].([]any)
	if len(coding) != 1 {
		t.Fatalf("coding: got %d entries", len(coding))
	}
	c0, _ := coding[0].(map[string]any)
	if c0["code"] != "8480-6" {
		t.Errorf("coding[0].code: got %v", c0["code"])
	}
	vq, _ := back["valueQuantity"].(map[string]any)
	if vq == nil || vq["value"] != float64(120) {
		t.Errorf("valueQuantity.value: got %v", back["valueQuantity"])
	}
}
