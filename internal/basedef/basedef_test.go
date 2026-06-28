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
		t.Fatalf("decode: %v", err)
	}

	// R4 has 140+ base resource types; guard against a truncated/empty asset.
	if len(defs) < 140 {
		t.Fatalf("expected 140+ base definitions, got %d", len(defs))
	}

	byType := make(map[string]def, len(defs))
	for _, d := range defs {
		if d.resourceType == "" {
			t.Errorf("definition with empty resource type: %+v", d)
		}
		if d.sd == nil {
			t.Errorf("%s: nil StructureDefinition", d.resourceType)
		}
		byType[d.resourceType] = d
	}

	for _, rt := range []string{"Patient", "Observation", "Immunization"} {
		d, ok := byType[rt]
		if !ok {
			t.Fatalf("missing base definition for %s", rt)
		}
		snap, _ := d.sd["snapshot"].(map[string]any)
		if snap == nil {
			t.Fatalf("%s: StructureDefinition has no snapshot", rt)
		}
		if els, _ := snap["element"].([]any); len(els) == 0 {
			t.Fatalf("%s: snapshot has no elements", rt)
		}
	}
}
