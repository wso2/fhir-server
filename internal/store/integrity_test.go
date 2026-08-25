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
)

func TestCollectLocalRefs(t *testing.T) {
	body := map[string]any{
		"resourceType": "Observation",
		"id":           "obs-1",
		"status":       "final",
		"subject":      map[string]any{"reference": "Patient/p1"},
		"encounter":    map[string]any{"reference": "Encounter/e1/_history/3"},
		"performer": []any{
			map[string]any{"reference": "Practitioner/pr1"},
			map[string]any{"reference": "https://other.example.org/fhir/Practitioner/ext1"}, // absolute: skipped
			map[string]any{"reference": "urn:uuid:0c38f28e-0a4e-4a9a-9a3b-1c1b3fa4a111"},    // urn: skipped
			map[string]any{"reference": "#contained-1"},                                     // fragment: skipped
			map[string]any{"reference": "Patient?identifier=mrn|1234"},                      // conditional: skipped
			map[string]any{"identifier": map[string]any{"system": "s", "value": "v"}},       // logical: skipped
			map[string]any{"display": "Someone"},                                            // display-only: skipped
		},
		"note": []any{
			map[string]any{"authorReference": map[string]any{"reference": "Patient/p1"}},
		},
	}

	refs := collectLocalRefs("Observation", "obs-1", body)

	got := map[string]string{} // ref -> path
	for _, r := range refs {
		got[r.ref] = r.path
		if r.source != "Observation/obs-1" {
			t.Errorf("source: got %q, want Observation/obs-1", r.source)
		}
	}
	if len(refs) != 4 {
		t.Fatalf("collected %d refs (%v), want 4", len(refs), got)
	}
	if got["Patient/p1"] == "" || got["Practitioner/pr1"] != "Observation.performer" {
		t.Errorf("unexpected refs/paths: %v", got)
	}
	if p := got["Encounter/e1/_history/3"]; p != "Observation.encounter" {
		t.Errorf("versioned ref path: got %q", p)
	}
	// Versioned ref must resolve to the unversioned target.
	for _, r := range refs {
		if r.ref == "Encounter/e1/_history/3" && (r.targetType != "Encounter" || r.targetID != "e1") {
			t.Errorf("versioned ref target: got %s/%s", r.targetType, r.targetID)
		}
	}
}

func TestCollectLocalRefs_BundleExempt(t *testing.T) {
	body := map[string]any{
		"resourceType": "Bundle",
		"type":         "document",
		"entry": []any{
			map[string]any{"resource": map[string]any{
				"resourceType": "Composition",
				"subject":      map[string]any{"reference": "Patient/p1"},
			}},
		},
	}
	if refs := collectLocalRefs("Bundle", "b1", body); len(refs) != 0 {
		t.Errorf("Bundle resources must be exempt, collected %d refs", len(refs))
	}
}

func TestCollectLocalRefs_InvalidShapes(t *testing.T) {
	body := map[string]any{
		"resourceType": "Basic",
		"a":            map[string]any{"reference": "lowercase/id"},   // type must start uppercase
		"b":            map[string]any{"reference": "Patient/bad id"}, // space not a valid id char
		"c":            map[string]any{"reference": "Patient/"},       // empty id
		"d":            map[string]any{"reference": ""},               // empty
		"e":            map[string]any{"reference": "JustAnId"},       // no slash
	}
	if refs := collectLocalRefs("Basic", "x", body); len(refs) != 0 {
		t.Errorf("invalid reference shapes must be skipped, collected %+v", refs)
	}
}

func TestReferentialIntegrityError_Message(t *testing.T) {
	err := ReferentialIntegrityError{Missing: []MissingReference{
		{Source: "Observation/o1", Path: "Observation.subject", Reference: "Patient/p9"},
		{Source: "Observation/o1", Path: "Observation.performer", Reference: "Practitioner/x"},
	}}
	msg := err.Error()
	for _, want := range []string{"Patient/p9", "Observation.subject", "Observation/o1", "1 more"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}
}

func TestReferencedByError_Message(t *testing.T) {
	err := ReferencedByError{Target: "Patient/p1", ReferencedBy: []string{"Observation/o1 (subject)"}}
	msg := err.Error()
	for _, want := range []string{"Patient/p1", "Observation/o1 (subject)", "referentialIntegrityOnDelete"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}
}
