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

package basedef

import "testing"

func TestDatatype_ShipsCoreComplexTypes(t *testing.T) {
	for _, name := range []string{
		"HumanName", "CodeableConcept", "Coding", "Extension", "Identifier",
		"Quantity", "Period", "Reference", "Meta", "Narrative", "Address",
		"ContactPoint", "Attachment", "Annotation", "Range", "Ratio", "Timing",
	} {
		if Datatype(name) == nil {
			t.Errorf("Datatype(%q) = nil, want compiled profile", name)
		}
	}
}

func TestDatatype_UnknownAndPrimitiveNamesAreAbsent(t *testing.T) {
	for _, name := range []string{"NoSuchType", "boolean", "string", "Patient"} {
		if Datatype(name) != nil {
			t.Errorf("Datatype(%q) should be nil", name)
		}
	}
}

func TestDatatype_HumanNameDrivesTypeChecking(t *testing.T) {
	hn := Datatype("HumanName")
	if hn == nil {
		t.Fatal("HumanName profile missing")
	}
	// given is a repeating string element: a JSON object inside it must be
	// flagged, proving the compiled datatype snapshot carries usable type info.
	issues := hn.CheckTypes(map[string]any{
		"given": []any{map[string]any{"id": "1"}},
	}, nil)
	if len(issues) == 0 {
		t.Fatal("expected an issue for object inside HumanName.given")
	}
	if issues[0].Expression != "HumanName.given[0]" {
		t.Errorf("want expression HumanName.given[0], got %q", issues[0].Expression)
	}
}
