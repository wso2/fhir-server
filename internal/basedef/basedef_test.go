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

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"testing"
)

// TestDecode verifies the embedded bundle decompresses and yields exactly the
// base resource StructureDefinitions it declares, each carrying a snapshot the
// validator needs. The expected set is derived from the bundle itself, so the
// test does not hardcode a count and survives a FHIR version change.
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
	// and a non-empty snapshot.
	got := make(map[string]bool, len(defs))
	for _, d := range defs {
		if d.resourceType == "" {
			t.Errorf("definition with empty resource type: %+v", d)
			continue
		}
		snap, _ := d.sd["snapshot"].(map[string]any)
		if snap == nil {
			t.Errorf("%s: StructureDefinition has no snapshot", d.resourceType)
			continue
		}
		if els, _ := snap["element"].([]any); len(els) == 0 {
			t.Errorf("%s: snapshot has no elements", d.resourceType)
		}
		got[d.resourceType] = true
	}

	// Independently parse the raw bundle and confirm decode() returned exactly
	// the base resource types it contains — no silent drops or extras.
	want := baseTypesInBundle(t)
	if len(want) == 0 {
		t.Fatal("no base resource types found in raw bundle")
	}
	for rt := range want {
		if !got[rt] {
			t.Errorf("decode() dropped base resource type %q", rt)
		}
	}
	for rt := range got {
		if !want[rt] {
			t.Errorf("decode() returned unexpected resource type %q", rt)
		}
	}
}

// baseTypesInBundle parses the embedded bundle directly (independently of
// decode) and returns the set of base resource types it declares
// (resourceType=StructureDefinition, kind=resource, derivation=specialization).
func baseTypesInBundle(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := bundleFS.ReadFile(bundleFile)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("open gzip reader: %v", err)
	}
	defer gz.Close()
	data, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("decompress bundle: %v", err)
	}
	var bundle struct {
		Entry []struct {
			Resource map[string]any `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatalf("parse bundle JSON: %v", err)
	}
	want := make(map[string]bool)
	for _, e := range bundle.Entry {
		r := e.Resource
		if r == nil {
			continue
		}
		if s, _ := r["resourceType"].(string); s != "StructureDefinition" {
			continue
		}
		if k, _ := r["kind"].(string); k != "resource" {
			continue
		}
		if d, _ := r["derivation"].(string); d != "specialization" {
			continue
		}
		if rt, _ := r["type"].(string); rt != "" {
			want[rt] = true
		}
	}
	return want
}
