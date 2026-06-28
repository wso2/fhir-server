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

import "testing"

func minSD(resourceType string, elements []map[string]any) map[string]any {
	rawEls := make([]any, len(elements))
	for i, e := range elements {
		rawEls[i] = e
	}
	return map[string]any{
		"resourceType": "StructureDefinition",
		"type":         resourceType,
		"snapshot":     map[string]any{"element": rawEls},
	}
}

func hasError(issues []Issue, code string) bool {
	for _, i := range issues {
		if i.Severity == "error" && i.Code == code {
			return true
		}
	}
	return false
}

func TestValidate_RequiredPresent(t *testing.T) {
	sd := minSD("Patient", []map[string]any{
		{"path": "Patient", "min": float64(1), "max": "*"},
		{"path": "Patient.name", "min": float64(1), "max": "*"},
	})
	resource := map[string]any{"resourceType": "Patient", "name": []any{map[string]any{"family": "Smith"}}}
	if issues := AgainstProfile(resource, sd); len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestValidate_RequiredMissing(t *testing.T) {
	sd := minSD("Patient", []map[string]any{
		{"path": "Patient.name", "min": float64(1), "max": "*"},
	})
	resource := map[string]any{"resourceType": "Patient"}
	issues := AgainstProfile(resource, sd)
	if !hasError(issues, "required") {
		t.Errorf("expected required error, got %v", issues)
	}
}

func TestValidate_RequiredChoicePresent(t *testing.T) {
	// Immunization.occurrence[x] is required; a concrete variant satisfies it.
	sd := minSD("Immunization", []map[string]any{
		{"path": "Immunization.occurrence[x]", "min": float64(1), "max": "1"},
	})
	resource := map[string]any{"resourceType": "Immunization", "occurrenceDateTime": "2026-01-01"}
	if issues := AgainstProfile(resource, sd); hasError(issues, "required") {
		t.Errorf("choice variant present should satisfy required, got %v", issues)
	}
}

func TestValidate_RequiredChoiceMissing(t *testing.T) {
	sd := minSD("Immunization", []map[string]any{
		{"path": "Immunization.occurrence[x]", "min": float64(1), "max": "1"},
	})
	resource := map[string]any{"resourceType": "Immunization"}
	if issues := AgainstProfile(resource, sd); !hasError(issues, "required") {
		t.Errorf("missing required choice should error, got %v", issues)
	}
}

func TestValidate_RequiredUnderAbsentParentNotEnforced(t *testing.T) {
	// doseNumber[x] is required, but only when its optional parent
	// protocolApplied is present. With protocolApplied absent, no error.
	sd := minSD("Immunization", []map[string]any{
		{"path": "Immunization.protocolApplied", "min": float64(0), "max": "*"},
		{"path": "Immunization.protocolApplied.doseNumber[x]", "min": float64(1), "max": "1"},
	})
	resource := map[string]any{"resourceType": "Immunization", "occurrenceDateTime": "2026-01-01"}
	if issues := AgainstProfile(resource, sd); hasError(issues, "required") {
		t.Errorf("required child under absent optional parent must not fire, got %v", issues)
	}
}

func TestValidate_RequiredUnderPresentParentEnforced(t *testing.T) {
	sd := minSD("Immunization", []map[string]any{
		{"path": "Immunization.protocolApplied", "min": float64(0), "max": "*"},
		{"path": "Immunization.protocolApplied.doseNumber[x]", "min": float64(1), "max": "1"},
	})
	resource := map[string]any{
		"resourceType":    "Immunization",
		"protocolApplied": []any{map[string]any{"series": "A"}},
	}
	if issues := AgainstProfile(resource, sd); !hasError(issues, "required") {
		t.Errorf("required child of a present parent should error, got %v", issues)
	}
}

func TestValidate_Forbidden(t *testing.T) {
	sd := minSD("Patient", []map[string]any{
		{"path": "Patient.multipleBirthBoolean", "min": float64(0), "max": "0"},
	})
	resource := map[string]any{"resourceType": "Patient", "multipleBirthBoolean": true}
	issues := AgainstProfile(resource, sd)
	if !hasError(issues, "structure") {
		t.Errorf("expected structure error for max=0, got %v", issues)
	}
}

func TestValidate_FixedValue(t *testing.T) {
	sd := minSD("Observation", []map[string]any{
		{"path": "Observation.status", "min": float64(1), "max": "1", "fixedCode": "final"},
	})
	valid := map[string]any{"resourceType": "Observation", "status": "final"}
	if issues := AgainstProfile(valid, sd); len(issues) != 0 {
		t.Errorf("expected no issues for correct fixed value, got %v", issues)
	}
	invalid := map[string]any{"resourceType": "Observation", "status": "preliminary"}
	if issues := AgainstProfile(invalid, sd); !hasError(issues, "value") {
		t.Errorf("expected value error for wrong fixed value, got %v", issues)
	}
}

func TestValidate_PatternValue(t *testing.T) {
	sd := minSD("Observation", []map[string]any{
		{"path": "Observation.category", "min": float64(0), "max": "*",
			"patternCodeableConcept": map[string]any{
				"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/observation-category", "code": "vital-signs"}},
			},
		},
	})
	valid := map[string]any{
		"resourceType": "Observation",
		"category": []any{map[string]any{
			"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/observation-category", "code": "vital-signs"}},
		}},
	}
	if issues := AgainstProfile(valid, sd); len(issues) != 0 {
		t.Errorf("expected no issues for matching pattern, got %v", issues)
	}
	invalid := map[string]any{
		"resourceType": "Observation",
		"category":     []any{map[string]any{"coding": []any{map[string]any{"code": "wrong"}}}},
	}
	if issues := AgainstProfile(invalid, sd); !hasError(issues, "value") {
		t.Errorf("expected value error for non-matching pattern, got %v", issues)
	}
}

func TestValidate_FHIRPathInvariant(t *testing.T) {
	// Invariant: name.exists() implies name.family.exists()
	sd := minSD("Patient", []map[string]any{
		{"path": "Patient", "min": float64(1), "max": "*", "constraint": []any{
			map[string]any{
				"key":        "pat-1",
				"severity":   "error",
				"human":      "If name exists, family must be present",
				"expression": "name.exists() implies name.family.exists()",
			},
		}},
	})
	// Valid: name with family.
	valid := map[string]any{"resourceType": "Patient", "name": []any{map[string]any{"family": "Smith"}}}
	if issues := AgainstProfile(valid, sd); len(issues) != 0 {
		t.Errorf("valid resource should have no issues, got %v", issues)
	}
	// Invalid: name without family.
	invalid := map[string]any{"resourceType": "Patient", "name": []any{map[string]any{"given": []any{"Alice"}}}}
	issues := AgainstProfile(invalid, sd)
	if !hasError(issues, "invariant") {
		t.Errorf("expected invariant error, got %v", issues)
	}
}

func TestValidate_Slicing(t *testing.T) {
	// Category slice: requires at least one element matching the vital-signs pattern.
	sd := minSD("Observation", []map[string]any{
		{"path": "Observation", "min": float64(1), "max": "*"},
		// The slicing parent element.
		{"path": "Observation.category", "min": float64(0), "max": "*",
			"slicing": map[string]any{
				"discriminator": []any{map[string]any{"type": "pattern", "path": "$this"}},
				"ordered":       false, "rules": "open",
			}},
		// The named slice with a required pattern.
		{"path": "Observation.category", "sliceName": "VSCat", "min": float64(1), "max": "1",
			"patternCodeableConcept": map[string]any{
				"coding": []any{map[string]any{
					"system": "http://terminology.hl7.org/CodeSystem/observation-category",
					"code":   "vital-signs",
				}},
			}},
	})

	// Valid: category has the required vital-signs coding.
	valid := map[string]any{
		"resourceType": "Observation",
		"category": []any{map[string]any{
			"coding": []any{map[string]any{
				"system": "http://terminology.hl7.org/CodeSystem/observation-category",
				"code":   "vital-signs",
			}},
		}},
	}
	if issues := AgainstProfile(valid, sd); len(issues) != 0 {
		t.Errorf("valid resource should have no issues, got %v", issues)
	}

	// Invalid: category present but doesn't match the pattern.
	invalid := map[string]any{
		"resourceType": "Observation",
		"category": []any{map[string]any{
			"coding": []any{map[string]any{"code": "laboratory"}},
		}},
	}
	issues := AgainstProfile(invalid, sd)
	if !hasError(issues, "required") {
		t.Errorf("expected required error for missing slice, got %v", issues)
	}
}

func TestValidate_NoSnapshot(t *testing.T) {
	sd := map[string]any{"resourceType": "StructureDefinition", "type": "Patient"}
	if issues := AgainstProfile(map[string]any{"resourceType": "Patient"}, sd); len(issues) != 0 {
		t.Errorf("SD with no snapshot should pass (no constraints to check), got %v", issues)
	}
}
