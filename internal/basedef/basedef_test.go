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

// TestDecode verifies the embedded bundle decompresses and yields the base
// resource StructureDefinitions, each carrying a snapshot the validator needs.
func TestDecode(t *testing.T) {
	defs, err := decode()
	if err != nil {
		// A truncated or corrupt embedded asset fails here on the gzip checksum.
		t.Fatalf("decode: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("decode returned no base definitions")
	}

	// Every base definition must be usable by the validator: a resource type
	// and a non-empty snapshot. This holds regardless of the bundle's size, so
	// it does not need to be revisited when the FHIR version changes.
	byType := make(map[string]def, len(defs))
	for _, d := range defs {
		if d.resourceType == "" {
			t.Errorf("definition with empty resource type: %+v", d)
		}
		snap, _ := d.sd["snapshot"].(map[string]any)
		if snap == nil {
			t.Errorf("%s: StructureDefinition has no snapshot", d.resourceType)
			continue
		}
		if els, _ := snap["element"].([]any); len(els) == 0 {
			t.Errorf("%s: snapshot has no elements", d.resourceType)
		}
		byType[d.resourceType] = d
	}

	// A representative set of core resource types must be present — a strong
	// integrity check on the shipped asset without hardcoding its exact size.
	for _, rt := range []string{
		"Patient", "Observation", "Immunization", "Encounter", "Condition",
		"Procedure", "MedicationRequest", "Bundle", "Organization", "Practitioner",
	} {
		if _, ok := byType[rt]; !ok {
			t.Errorf("missing base definition for %s", rt)
		}
	}
}
