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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wso2/fhir-server/internal/config"
)

func writeConfigFile(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidation_Defaults(t *testing.T) {
	clearIGEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v := cfg.Validation
	if !v.Base {
		t.Error("default Validation.Base should be true")
	}
	if v.Profile {
		t.Error("default Validation.Profile should be false")
	}
	if !v.ReferentialIntegrityOnWrite {
		t.Error("default Validation.ReferentialIntegrityOnWrite should be true")
	}
	if !v.ReferentialIntegrityOnDelete {
		t.Error("default Validation.ReferentialIntegrityOnDelete should be true")
	}
}

func TestValidation_YAMLBlock(t *testing.T) {
	clearIGEnv(t)
	path := writeConfigFile(t, `
validation:
  base: false
  profile: true
  referentialIntegrityOnWrite: false
  referentialIntegrityOnDelete: false
`)
	cfg, err := config.LoadFromPath(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v := cfg.Validation
	if v.Base || !v.Profile || v.ReferentialIntegrityOnWrite || v.ReferentialIntegrityOnDelete {
		t.Errorf("YAML validation block not honored: %+v", v)
	}
}

func TestValidation_EnvOverridesYAML(t *testing.T) {
	clearIGEnv(t)
	path := writeConfigFile(t, `
validation:
  referentialIntegrityOnWrite: true
`)
	t.Setenv("FHIR_VALIDATION_REFERENTIAL_INTEGRITY_ON_WRITE", "false")
	cfg, err := config.LoadFromPath(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Validation.ReferentialIntegrityOnWrite {
		t.Error("env var should override the YAML value")
	}
}

func TestValidation_LegacyEnvNames(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("FHIR_BASE_VALIDATION", "false")
	t.Setenv("FHIR_VALIDATE_ON_WRITE", "true")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Validation.Base {
		t.Error("legacy FHIR_BASE_VALIDATION=false should disable base validation")
	}
	if !cfg.Validation.Profile {
		t.Error("legacy FHIR_VALIDATE_ON_WRITE=true should enable profile validation")
	}
}

func TestValidation_CanonicalEnvBeatsLegacy(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("FHIR_VALIDATE_ON_WRITE", "true")
	t.Setenv("FHIR_VALIDATION_PROFILE", "false")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Validation.Profile {
		t.Error("FHIR_VALIDATION_PROFILE must win over legacy FHIR_VALIDATE_ON_WRITE")
	}
}

func TestValidation_InvalidBoolFailsFast(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("FHIR_VALIDATION_REFERENTIAL_INTEGRITY_ON_DELETE", "banana")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error for an unparseable boolean")
	}
	if !strings.Contains(err.Error(), "FHIR_VALIDATION_REFERENTIAL_INTEGRITY_ON_DELETE") {
		t.Errorf("error should name the offending variable, got: %v", err)
	}
}
