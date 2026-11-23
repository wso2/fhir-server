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
	"strings"
	"testing"

	"github.com/wso2/fhir-server/internal/config"
)

func TestReindex_DefaultOff(t *testing.T) {
	clearIGEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Reindex.Enabled {
		t.Error("default Reindex.Enabled should be false")
	}
}

func TestReindex_YAMLEnables(t *testing.T) {
	clearIGEnv(t)
	path := writeConfigFile(t, `
reindex:
  enabled: true
`)
	cfg, err := config.LoadFromPath(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Reindex.Enabled {
		t.Error("YAML reindex.enabled=true should enable reindexing")
	}
}

func TestReindex_EnvOverridesYAML(t *testing.T) {
	clearIGEnv(t)
	path := writeConfigFile(t, `
reindex:
  enabled: false
`)
	t.Setenv("REINDEX_ENABLED", "true")
	cfg, err := config.LoadFromPath(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Reindex.Enabled {
		t.Error("env REINDEX_ENABLED should override the YAML value")
	}
}

func TestReindex_InvalidBoolFailsFast(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("REINDEX_ENABLED", "banana")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error for an unparseable boolean")
	}
	if !strings.Contains(err.Error(), "REINDEX_ENABLED") {
		t.Errorf("error should name the offending variable, got: %v", err)
	}
}
