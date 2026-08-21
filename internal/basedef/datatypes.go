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
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/wso2/fhir-server/internal/validate"
)

//go:embed profiles-types.min.json.gz
var typesBundleFS embed.FS

const typesBundleFile = "profiles-types.min.json.gz"

var (
	datatypesOnce sync.Once
	datatypes     map[string]*validate.Profile
)

// Datatype returns the compiled base StructureDefinition for a complex FHIR R4
// datatype (HumanName, CodeableConcept, Extension, …), or nil when the name is
// not a shipped datatype. Unlike the resource definitions, the datatype bundle
// is small and never customized, so it is decoded from the embedded file and
// compiled in memory on first use — no database involved.
func Datatype(name string) *validate.Profile {
	datatypesOnce.Do(func() {
		m, err := decodeDatatypes()
		if err != nil {
			// A broken embedded bundle is a build defect, not a runtime
			// condition; log and degrade to no-datatype-knowledge (type
			// checking then skips datatype interiors).
			slog.Error("decode embedded datatype bundle", "err", err)
			m = map[string]*validate.Profile{}
		}
		datatypes = m
	})
	return datatypes[name]
}

// decodeDatatypes reads the embedded profiles-types bundle and compiles each
// complex-type base StructureDefinition, keyed by datatype name.
func decodeDatatypes() (map[string]*validate.Profile, error) {
	raw, err := typesBundleFS.ReadFile(typesBundleFile)
	if err != nil {
		return nil, fmt.Errorf("read embedded types bundle: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("open gzip reader: %w", err)
	}
	defer gz.Close()
	data, err := io.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("decompress types bundle: %w", err)
	}

	var bundle struct {
		Entry []struct {
			Resource map[string]any `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("parse types bundle JSON: %w", err)
	}

	out := make(map[string]*validate.Profile, len(bundle.Entry))
	for _, e := range bundle.Entry {
		sd := e.Resource
		if sd == nil {
			continue
		}
		if t, _ := sd["resourceType"].(string); t != "StructureDefinition" {
			continue
		}
		if k, _ := sd["kind"].(string); k != "complex-type" {
			continue
		}
		if d, _ := sd["derivation"].(string); d != "specialization" {
			continue
		}
		name, _ := sd["type"].(string)
		if name == "" {
			continue
		}
		if p := validate.Compile(sd); p != nil {
			out[name] = p
		}
	}
	return out, nil
}
