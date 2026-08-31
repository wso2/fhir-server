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

package config_test

import (
	"path/filepath"
	"testing"

	"github.com/wso2/fhir-server/internal/config"
)

// TestLoad_ExampleConfigParses keeps config.example.yaml honest: every key in
// the shipped example must be a known key (LoadFromPath rejects unknown YAML
// keys), and the commented-out defaults must match the resolved defaults.
func TestLoad_ExampleConfigParses(t *testing.T) {
	clearIGEnv(t)
	cfg, err := config.LoadFromPath(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("config.example.yaml must parse: %v", err)
	}
	v := cfg.Validation
	if !v.Base || v.Profile || !v.ReferentialIntegrityOnWrite || !v.ReferentialIntegrityOnDelete {
		t.Errorf("example config must resolve to the defaults, got %+v", v)
	}
}
