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
	"time"

	"github.com/wso2/fhir-server/internal/config"
)

func TestLoad_Defaults(t *testing.T) {
	clearIGEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 9090 {
		t.Errorf("default Port: got %d, want 9090", cfg.Port)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("default LogLevel: got %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.IGRegistryURL != "https://packages.fhir.org" {
		t.Errorf("default IGRegistryURL: got %q", cfg.IGRegistryURL)
	}
	if cfg.IGCacheDir != ".fhir-ig-cache" {
		t.Errorf("default IGCacheDir: got %q, want %q", cfg.IGCacheDir, ".fhir-ig-cache")
	}
	if cfg.IGForceReload {
		t.Error("default IGForceReload should be false")
	}
	if len(cfg.IGPackages) != 0 {
		t.Errorf("default IGPackages should be empty, got %v", cfg.IGPackages)
	}
}

// TestLoad_SearchTuning_DefaultEquivalence is the §0.1 proof: with no config,
// every performance tunable resolves to the store's / db's historical hardcoded
// value. Asserted against the literals (5000 / 20 / 0 / 5 / force_custom_plan),
// not against a round-trip, so threading the config through the store (T2) and
// db (T3) cannot silently move an effective value without failing here.
func TestLoad_SearchTuning_DefaultEquivalence(t *testing.T) {
	clearIGEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SearchProbeCap != 5000 {
		t.Errorf("default SearchProbeCap: got %d, want 5000", cfg.SearchProbeCap)
	}
	if cfg.SearchDefaultPageSize != 20 {
		t.Errorf("default SearchDefaultPageSize: got %d, want 20", cfg.SearchDefaultPageSize)
	}
	if cfg.SearchMaxPageSize != 0 {
		t.Errorf("default SearchMaxPageSize: got %d, want 0 (unlimited)", cfg.SearchMaxPageSize)
	}
	if cfg.SearchMaxChainDepth != 5 {
		t.Errorf("default SearchMaxChainDepth: got %d, want 5", cfg.SearchMaxChainDepth)
	}
	if cfg.PlanCacheMode != "force_custom_plan" {
		t.Errorf("default PlanCacheMode: got %q, want force_custom_plan", cfg.PlanCacheMode)
	}
}

func TestLoad_SearchProbeCap_EnvOverride(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("SEARCH_PROBE_CAP", "4999")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SearchProbeCap != 4999 {
		t.Errorf("SearchProbeCap: got %d, want 4999", cfg.SearchProbeCap)
	}
}

func TestLoad_InvalidProbeCap_OutOfRange(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("SEARCH_PROBE_CAP", "50") // below the 100 floor

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for out-of-range SEARCH_PROBE_CAP, got nil")
	}
	if !strings.Contains(err.Error(), "SEARCH_PROBE_CAP") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}

func TestLoad_InvalidProbeCap_NotInteger(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("SEARCH_PROBE_CAP", "lots")

	if _, err := config.Load(); err == nil {
		t.Fatal("expected error for non-integer SEARCH_PROBE_CAP, got nil")
	}
}

func TestLoad_InvalidPlanCacheMode(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("DATABASE_PLAN_CACHE_MODE", "turbo")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid DATABASE_PLAN_CACHE_MODE, got nil")
	}
	if !strings.Contains(err.Error(), "DATABASE_PLAN_CACHE_MODE") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}

func TestLoad_PlanCacheMode_ValidVariants(t *testing.T) {
	for _, mode := range []string{"force_custom_plan", "auto", "force_generic_plan"} {
		t.Run(mode, func(t *testing.T) {
			clearIGEnv(t)
			t.Setenv("DATABASE_PLAN_CACHE_MODE", mode)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.PlanCacheMode != mode {
				t.Errorf("PlanCacheMode: got %q, want %q", cfg.PlanCacheMode, mode)
			}
		})
	}
}

func TestLoad_ServerPort(t *testing.T) {
	t.Setenv("SERVER_PORT", "8080")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("got Port %d, want 8080", cfg.Port)
	}
}

func TestLoad_InvalidServerPort(t *testing.T) {
	t.Setenv("SERVER_PORT", "not-a-number")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid SERVER_PORT")
	}
}

func TestLoad_DatabaseURL_Direct(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@host:5432/db")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DatabaseURL != "postgres://user:pass@host:5432/db" {
		t.Errorf("got DatabaseURL %q", cfg.DatabaseURL)
	}
}

func TestLoad_DatabaseURL_FromComponents(t *testing.T) {
	// Ensure DATABASE_URL is not set
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_HOST", "myhost")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("DB_USER", "myuser")
	t.Setenv("DB_PASSWORD", "mypass")
	t.Setenv("DB_NAME", "mydb")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "postgres://myuser:mypass@myhost:5433/mydb?sslmode=disable"
	if cfg.DatabaseURL != want {
		t.Errorf("got %q, want %q", cfg.DatabaseURL, want)
	}
}

func TestLoad_IGPackages_CommaSeparated(t *testing.T) {
	t.Setenv("IG_PACKAGES", "hl7.fhir.us.core@6.1.0, hl7.fhir.us.carin-bb@2.0.0, ")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.IGPackages) != 2 {
		t.Fatalf("want 2 packages, got %d: %v", len(cfg.IGPackages), cfg.IGPackages)
	}
	if cfg.IGPackages[0] != "hl7.fhir.us.core@6.1.0" {
		t.Errorf("pkg[0]: got %q", cfg.IGPackages[0])
	}
	if cfg.IGPackages[1] != "hl7.fhir.us.carin-bb@2.0.0" {
		t.Errorf("pkg[1]: got %q", cfg.IGPackages[1])
	}
}

func TestLoad_IGPackages_Empty(t *testing.T) {
	t.Setenv("IG_PACKAGES", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.IGPackages) != 0 {
		t.Errorf("expected empty IGPackages, got %v", cfg.IGPackages)
	}
}

func TestLoad_IGForceReload(t *testing.T) {
	t.Setenv("IG_FORCE_RELOAD", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.IGForceReload {
		t.Error("expected IGForceReload=true")
	}
}

func TestLoad_IGCacheDir_Custom(t *testing.T) {
	t.Setenv("IG_CACHE_DIR", "/data/my-ig-cache")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IGCacheDir != "/data/my-ig-cache" {
		t.Errorf("got IGCacheDir %q", cfg.IGCacheDir)
	}
}

func TestLoad_LogLevel_Variants(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", level)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.LogLevel != level {
				t.Errorf("got %q, want %q", cfg.LogLevel, level)
			}
		})
	}
}

func TestLoad_BaseURL_Custom(t *testing.T) {
	t.Setenv("BASE_URL", "https://fhir.example.com/r4")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseURL != "https://fhir.example.com/r4" {
		t.Errorf("got BaseURL %q", cfg.BaseURL)
	}
}

func TestLoad_BaseURL_DefaultIncludesPort(t *testing.T) {
	t.Setenv("BASE_URL", "")
	t.Setenv("SERVER_PORT", "8888")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "http://localhost:8888/fhir/r4"
	if cfg.BaseURL != want {
		t.Errorf("got %q, want %q", cfg.BaseURL, want)
	}
}

// clearIGEnv removes env vars that might be set from outer test runs.
func clearIGEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DATABASE_URL", "DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME",
		"SERVER_PORT", "BASE_URL", "LOG_LEVEL",
		"SERVER_READ_TIMEOUT", "SERVER_WRITE_TIMEOUT", "SERVER_IDLE_TIMEOUT",
		"IG_PACKAGES", "IG_REGISTRY_URL", "IG_FORCE_RELOAD", "IG_CACHE_DIR",
		"FHIR_SERVER_CONFIG",
		"SEARCH_PROBE_CAP", "SEARCH_DEFAULT_PAGE_SIZE", "SEARCH_MAX_PAGE_SIZE",
		"SEARCH_MAX_CHAIN_DEPTH", "DATABASE_PLAN_CACHE_MODE",
	} {
		t.Setenv(k, "")
	}
}

func TestLoad_Timeouts_Defaults(t *testing.T) {
	clearIGEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReadTimeout != 30*time.Second {
		t.Errorf("default ReadTimeout: got %v, want 30s", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 60*time.Second {
		t.Errorf("default WriteTimeout: got %v, want 60s", cfg.WriteTimeout)
	}
	if cfg.IdleTimeout != 120*time.Second {
		t.Errorf("default IdleTimeout: got %v, want 120s", cfg.IdleTimeout)
	}
}

func TestLoad_Timeouts_EnvOverride(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("SERVER_READ_TIMEOUT", "45s")
	t.Setenv("SERVER_WRITE_TIMEOUT", "10m")
	t.Setenv("SERVER_IDLE_TIMEOUT", "0") // 0 disables

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReadTimeout != 45*time.Second {
		t.Errorf("ReadTimeout: got %v, want 45s", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 10*time.Minute {
		t.Errorf("WriteTimeout: got %v, want 10m", cfg.WriteTimeout)
	}
	if cfg.IdleTimeout != 0 {
		t.Errorf("IdleTimeout: got %v, want 0 (disabled)", cfg.IdleTimeout)
	}
}

func TestLoad_Timeouts_Invalid(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("SERVER_WRITE_TIMEOUT", "not-a-duration")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected error for invalid SERVER_WRITE_TIMEOUT, got nil")
	}
}

func TestLoad_Timeouts_NegativeRejected(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("SERVER_READ_TIMEOUT", "-5s")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected error for negative SERVER_READ_TIMEOUT, got nil")
	}
}

// ─── Bundle transaction tunables ─────────────────────────────────────────────

func TestLoad_BundleTransaction_Defaults(t *testing.T) {
	clearIGEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BundleTransactionConcurrency != 1 {
		t.Errorf("default BundleTransactionConcurrency: got %d, want 1 (off)", cfg.BundleTransactionConcurrency)
	}
	if cfg.BundleTransactionProcessingDefault != "sequential" {
		t.Errorf("default BundleTransactionProcessingDefault: got %q, want sequential", cfg.BundleTransactionProcessingDefault)
	}
}

func TestLoad_BundleTransaction_EnvOverride(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("BUNDLE_TRANSACTION_CONCURRENCY", "8")
	t.Setenv("BUNDLE_TRANSACTION_PROCESSING_DEFAULT", "parallel")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BundleTransactionConcurrency != 8 {
		t.Errorf("BundleTransactionConcurrency: got %d, want 8", cfg.BundleTransactionConcurrency)
	}
	if cfg.BundleTransactionProcessingDefault != "parallel" {
		t.Errorf("BundleTransactionProcessingDefault: got %q, want parallel", cfg.BundleTransactionProcessingDefault)
	}
}

func TestLoad_BundleTransactionConcurrency_OutOfRange(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("BUNDLE_TRANSACTION_CONCURRENCY", "0") // below the 1 floor

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for out-of-range BUNDLE_TRANSACTION_CONCURRENCY, got nil")
	}
	if !strings.Contains(err.Error(), "BUNDLE_TRANSACTION_CONCURRENCY") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}

func TestLoad_BundleTransactionProcessingDefault_Invalid(t *testing.T) {
	clearIGEnv(t)
	t.Setenv("BUNDLE_TRANSACTION_PROCESSING_DEFAULT", "concurrent")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid BUNDLE_TRANSACTION_PROCESSING_DEFAULT, got nil")
	}
	if !strings.Contains(err.Error(), "BUNDLE_TRANSACTION_PROCESSING_DEFAULT") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}
