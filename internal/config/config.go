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

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the resolved server configuration consumed by the rest of the
// application. Values are merged from (in order of precedence, highest first):
//
//  1. Environment variables
//  2. The YAML configuration file (if one is specified)
//  3. Built-in defaults
type Config struct {
	DatabaseURL     string
	Port            int
	BaseURL         string
	LogLevel        string
	IGPackages      []string // e.g. ["hl7.fhir.us.core@6.1.0", "hl7.fhir.us.carin-bb@2.0.0"]
	IGRegistryURL   string   // default: https://packages.fhir.org
	IGForceReload   bool     // re-load IGs even if already recorded in ig_packages
	IGCacheDir      string   // local .tgz cache dir (default: .fhir-ig-cache)
	ValidateOnWrite bool     // enforce profile validation on create/update (default off)
	BaseValidation  bool     // validate writes against base FHIR R4 StructureDefinitions (default on)
	TerminologyURL  string   // base URL of the FHIR terminology server for :in/:not-in (empty = disabled)
	CreateTables    bool     // create database tables on startup (requires a DB role with DDL privileges; default off)

	// HTTP server timeouts. WriteTimeout bounds the WHOLE handler execution in
	// net/http, so it must accommodate the slowest legitimate request (e.g. a
	// large transaction bundle); 0 disables a timeout entirely.
	ReadTimeout  time.Duration // default 30s
	WriteTimeout time.Duration // default 60s
	IdleTimeout  time.Duration // default 120s

	// Search performance tunables. Defaults equal the store's historical
	// hardcoded constants — see docs/performance-tuning.md for the tuning rules.
	SearchProbeCap        int // density-probe cap; default 5000
	SearchDefaultPageSize int // page size when _count is omitted; default 20
	SearchMaxPageSize     int // upper clamp on client _count; 0 = unlimited; default 0
	SearchMaxChainDepth   int // chained-parameter recursion bound; default 5

	// PlanCacheMode is the per-connection plan_cache_mode. force_custom_plan is
	// load-bearing for the search probe architecture's per-bound plan choice.
	PlanCacheMode string // default force_custom_plan

	// Write-path batching limits. The bundle writer batches a whole transaction's
	// index rows into multi-row INSERTs; these bound how large one statement and
	// one transaction may grow so a pathological bundle fails with a 413 instead
	// of driving the database out of memory. See docs/performance-tuning.md.
	WriteMaxRowsPerStatement int // rows per multi-row INSERT; default 1000
	WriteMaxRowsPerBundle    int // total index rows one transaction may buffer; default 100000
}

// FileConfig is the on-disk YAML schema. Each field is optional — anything
// not specified falls through to the env var, then to the built-in default.
type FileConfig struct {
	Server struct {
		Port    int    `yaml:"port"`
		BaseURL string `yaml:"baseUrl"`
		// Go duration strings ("30s", "5m"); "0" disables that timeout.
		ReadTimeout  string `yaml:"readTimeout"`
		WriteTimeout string `yaml:"writeTimeout"`
		IdleTimeout  string `yaml:"idleTimeout"`
	} `yaml:"server"`

	Logging struct {
		Level string `yaml:"level"`
	} `yaml:"logging"`

	Database struct {
		URL      string `yaml:"url"`
		Host     string `yaml:"host"`
		Port     string `yaml:"port"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`
		Name     string `yaml:"name"`
		// CreateTables opts in to creating the schema's tables on startup.
		// Pointer so an absent key is distinguishable from an explicit `false`.
		CreateTables *bool `yaml:"createTables"`
		// PlanCacheMode overrides the per-connection plan_cache_mode.
		PlanCacheMode string `yaml:"planCacheMode"`
	} `yaml:"database"`

	IG struct {
		Packages    []string `yaml:"packages"`
		RegistryURL string   `yaml:"registryUrl"`
		ForceReload *bool    `yaml:"forceReload"` // pointer so absence is distinguishable from `false`
		CacheDir    string   `yaml:"cacheDir"`
	} `yaml:"ig"`

	// Search performance tunables. Pointers so an absent key is distinguishable
	// from an explicit value (matters for maxPageSize, where 0 is meaningful, and
	// for validation, which must reject an explicit out-of-range 0 for probeCap).
	Search struct {
		ProbeCap        *int `yaml:"probeCap"`
		DefaultPageSize *int `yaml:"defaultPageSize"`
		MaxPageSize     *int `yaml:"maxPageSize"`
		MaxChainDepth   *int `yaml:"maxChainDepth"`
	} `yaml:"search"`

	// Write-path batching limits. Pointers so an absent key falls through to the
	// default while an explicit value is honored and range-validated.
	Write struct {
		MaxRowsPerStatement *int `yaml:"maxRowsPerStatement"`
		MaxRowsPerBundle    *int `yaml:"maxRowsPerBundle"`
	} `yaml:"write"`

	// Bundle execution tunables. transactionConcurrency is a pointer so an
	// absent key falls through to the default (1 = off) while an explicit value
	// is range-validated.
}

// Load reads configuration using the env-var-based discovery path. The
// optional config file location is taken from FHIR_SERVER_CONFIG.
//
// Callers that parse CLI flags should use LoadFromPath instead.
func Load() (*Config, error) {
	return LoadFromPath(os.Getenv("FHIR_SERVER_CONFIG"))
}

// LoadFromPath reads configuration, optionally seeded from a YAML file at the
// given path. An empty path means "no config file" — only env vars + defaults
// are applied. A non-empty path that cannot be read or parsed returns an
// error; unknown YAML keys are also rejected so typos surface loudly.
func LoadFromPath(path string) (*Config, error) {
	var fc FileConfig
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config file %q: %w", path, err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&fc); err != nil && !errors.Is(err, io.EOF) {
			// io.EOF means the file was empty / whitespace-only — that's fine,
			// we just fall through to env vars + defaults.
			return nil, fmt.Errorf("parse config file %q: %w", path, err)
		}
	}
	return resolve(&fc)
}

// resolve materializes a Config from a (possibly empty) FileConfig, layering
// env vars on top and falling back to defaults.
func resolve(fc *FileConfig) (*Config, error) {
	dbURL, err := resolveDatabaseURL(fc)
	if err != nil {
		return nil, err
	}

	serverPort, err := resolveServerPort(fc)
	if err != nil {
		return nil, err
	}

	baseURL := pick(os.Getenv("BASE_URL"), fc.Server.BaseURL, fmt.Sprintf("http://localhost:%d/fhir/r4", serverPort))
	logLevel := pick(os.Getenv("LOG_LEVEL"), fc.Logging.Level, "info")

	igPackages := resolveIGPackages(fc)
	igRegistry := pick(os.Getenv("IG_REGISTRY_URL"), fc.IG.RegistryURL, "https://packages.fhir.org")
	igCacheDir := pick(os.Getenv("IG_CACHE_DIR"), fc.IG.CacheDir, ".fhir-ig-cache")

	igForceReload := false
	if fc.IG.ForceReload != nil {
		igForceReload = *fc.IG.ForceReload
	}
	if v := os.Getenv("IG_FORCE_RELOAD"); v != "" {
		igForceReload = strings.EqualFold(v, "true")
	}

	validateOnWrite := strings.EqualFold(os.Getenv("FHIR_VALIDATE_ON_WRITE"), "true")
	// Base validation is on by default; set FHIR_BASE_VALIDATION=false to disable.
	baseValidation := !strings.EqualFold(os.Getenv("FHIR_BASE_VALIDATION"), "false")
	terminologyURL := os.Getenv("FHIR_TERMINOLOGY_URL")

	createTables := false
	if fc.Database.CreateTables != nil {
		createTables = *fc.Database.CreateTables
	}
	if v := os.Getenv("FHIR_CREATE_TABLES"); v != "" {
		createTables = strings.EqualFold(v, "true")
	}

	readTimeout, err := resolveTimeout("SERVER_READ_TIMEOUT", "server.readTimeout", fc.Server.ReadTimeout, 30*time.Second)
	if err != nil {
		return nil, err
	}
	writeTimeout, err := resolveTimeout("SERVER_WRITE_TIMEOUT", "server.writeTimeout", fc.Server.WriteTimeout, 60*time.Second)
	if err != nil {
		return nil, err
	}
	idleTimeout, err := resolveTimeout("SERVER_IDLE_TIMEOUT", "server.idleTimeout", fc.Server.IdleTimeout, 120*time.Second)
	if err != nil {
		return nil, err
	}

	// Search tunables. Defaults equal the store's historical constants, so an
	// empty config reproduces current behavior exactly (see the default-equivalence
	// test). Ranges per docs/performance-tuning.md; validation fails fast, naming
	// the offending source (env var or config key) and the allowed range.
	probeCap, err := resolveIntTunable("SEARCH_PROBE_CAP", "search.probeCap", fc.Search.ProbeCap, 5000, 100, 1_000_000)
	if err != nil {
		return nil, err
	}
	defaultPageSize, err := resolveIntTunable("SEARCH_DEFAULT_PAGE_SIZE", "search.defaultPageSize", fc.Search.DefaultPageSize, 20, 1, 1000)
	if err != nil {
		return nil, err
	}
	// maxPageSize: 0 = unlimited (legacy behavior), else a clamp in [1, 10000].
	maxPageSize, err := resolveIntTunable("SEARCH_MAX_PAGE_SIZE", "search.maxPageSize", fc.Search.MaxPageSize, 0, 0, 10_000)
	if err != nil {
		return nil, err
	}
	maxChainDepth, err := resolveIntTunable("SEARCH_MAX_CHAIN_DEPTH", "search.maxChainDepth", fc.Search.MaxChainDepth, 5, 1, 10)
	if err != nil {
		return nil, err
	}
	planCacheMode, err := resolveEnumTunable("DATABASE_PLAN_CACHE_MODE", "database.planCacheMode", fc.Database.PlanCacheMode,
		"force_custom_plan", []string{"force_custom_plan", "auto", "force_generic_plan"})
	if err != nil {
		return nil, err
	}

	// Write-path batching limits. Defaults are conservative; raise maxRowsPerBundle
	// for trusted bulk-import environments (see docs/performance-tuning.md). The
	// per-statement value is additionally clamped by the 65535-parameter protocol
	// ceiling at write time, so a large value can never produce an invalid statement.
	writeMaxRowsPerStatement, err := resolveIntTunable("WRITE_MAX_ROWS_PER_STATEMENT", "write.maxRowsPerStatement", fc.Write.MaxRowsPerStatement, 1000, 100, 20_000)
	if err != nil {
		return nil, err
	}
	writeMaxRowsPerBundle, err := resolveIntTunable("WRITE_MAX_ROWS_PER_BUNDLE", "write.maxRowsPerBundle", fc.Write.MaxRowsPerBundle, 100_000, 1000, 100_000_000)
	if err != nil {
		return nil, err
	}

	// Parallel transaction bundle tunables. Concurrency 1 (the default) keeps the
	// capability off entirely; the processing default exists so a deployment can
	// opt whole workloads into parallel mode without per-request headers.

	return &Config{
		DatabaseURL:     dbURL,
		Port:            serverPort,
		BaseURL:         baseURL,
		LogLevel:        logLevel,
		IGPackages:      igPackages,
		IGRegistryURL:   igRegistry,
		IGForceReload:   igForceReload,
		IGCacheDir:      igCacheDir,
		ValidateOnWrite: validateOnWrite,
		BaseValidation:  baseValidation,
		TerminologyURL:  terminologyURL,
		CreateTables:    createTables,
		ReadTimeout:     readTimeout,
		WriteTimeout:    writeTimeout,
		IdleTimeout:     idleTimeout,

		SearchProbeCap:        probeCap,
		SearchDefaultPageSize: defaultPageSize,
		SearchMaxPageSize:     maxPageSize,
		SearchMaxChainDepth:   maxChainDepth,
		PlanCacheMode:         planCacheMode,

		WriteMaxRowsPerStatement: writeMaxRowsPerStatement,
		WriteMaxRowsPerBundle:    writeMaxRowsPerBundle,
	}, nil
}

// resolveIntTunable resolves one integer tunable: env var > config file > default,
// then range-validates. An unparseable or out-of-range value fails fast, naming
// the source that supplied it (env var or config key) and the allowed range. The
// default must be within [min, max]. fileVal is a pointer so an absent key falls
// through to the default while an explicit value (including 0) is honored and
// validated.
func resolveIntTunable(envVar, fileKey string, fileVal *int, def, min, max int) (int, error) {
	val, source := def, ""
	if raw := os.Getenv(envVar); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid %s %q: must be an integer", envVar, raw)
		}
		val, source = n, envVar
	} else if fileVal != nil {
		val, source = *fileVal, fileKey
	}
	if val < min || val > max {
		if source == "" {
			source = fileKey
		}
		return 0, fmt.Errorf("%s (%d) out of range: must be between %d and %d", source, val, min, max)
	}
	return val, nil
}

// resolveEnumTunable resolves one enumerated string tunable: env var > config
// file > default, validated against the allowed set. An out-of-set value fails
// fast, naming the source and the allowed values. The default must be in allowed.
func resolveEnumTunable(envVar, fileKey, fileVal, def string, allowed []string) (string, error) {
	val, source := def, ""
	if raw := os.Getenv(envVar); raw != "" {
		val, source = raw, envVar
	} else if fileVal != "" {
		val, source = fileVal, fileKey
	}
	for _, a := range allowed {
		if val == a {
			return val, nil
		}
	}
	if source == "" {
		source = fileKey
	}
	return "", fmt.Errorf("invalid %s %q: must be one of %s", source, val, strings.Join(allowed, ", "))
}

// resolveTimeout resolves one HTTP server timeout: env var > config file >
// default. Values are Go duration strings ("30s", "5m"); "0" disables the
// timeout (net/http treats zero as no timeout). Negative values are rejected.
// Validation errors name the source that actually supplied the bad value
// (the env var or the config-file key), so startup failures point at the
// right place.
func resolveTimeout(envVar, fileKey, fileVal string, def time.Duration) (time.Duration, error) {
	raw, source := os.Getenv(envVar), envVar
	if raw == "" {
		raw, source = fileVal, fileKey
	}
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", source, raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid %s %q: must not be negative", source, raw)
	}
	return d, nil
}

func resolveDatabaseURL(fc *FileConfig) (string, error) {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v, nil
	}
	if fc.Database.URL != "" {
		return fc.Database.URL, nil
	}
	host := pick(os.Getenv("DB_HOST"), fc.Database.Host, "localhost")
	port := pick(os.Getenv("DB_PORT"), fc.Database.Port, "5432")
	user := pick(os.Getenv("DB_USER"), fc.Database.User, "fhir")
	pass := pick(os.Getenv("DB_PASSWORD"), fc.Database.Password, "fhir")
	name := pick(os.Getenv("DB_NAME"), fc.Database.Name, "fhirdb")
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, pass),
		Host:     net.JoinHostPort(host, port),
		Path:     "/" + name,
		RawQuery: "sslmode=disable",
	}
	return u.String(), nil
}

func resolveServerPort(fc *FileConfig) (int, error) {
	if v := os.Getenv("SERVER_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("invalid SERVER_PORT: %w", err)
		}
		return n, nil
	}
	if fc.Server.Port != 0 {
		return fc.Server.Port, nil
	}
	return 9090, nil
}

func resolveIGPackages(fc *FileConfig) []string {
	// IG_PACKAGES is comma-separated: "hl7.fhir.us.core@6.1.0,hl7.fhir.us.carin-bb@2.0.0".
	// A non-empty value fully replaces the file's list. Empty / unset → fall back to file.
	if raw := os.Getenv("IG_PACKAGES"); raw != "" {
		var pkgs []string
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				pkgs = append(pkgs, p)
			}
		}
		return pkgs
	}
	if len(fc.IG.Packages) > 0 {
		// Defensive copy + trim, so the resolved Config isn't aliased to FileConfig.
		out := make([]string, 0, len(fc.IG.Packages))
		for _, p := range fc.IG.Packages {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// pick returns the first non-empty value. Useful for env > file > default chains.
func pick(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
