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

// Package basedef ships the core FHIR R4 resource StructureDefinitions and loads
// them into the base_definitions table, so the server can validate resources
// against the base spec even when no profile is supplied.
//
// The definitions come from the official R4 `profiles-resources` bundle, stored
// compressed in this package (profiles-resources.min.json.gz) and decompressed
// at load time. Only base resource definitions are kept — entries with
// kind=="resource" and derivation=="specialization" (i.e. the 147 base resource
// types, not datatypes or constraint profiles).
package basedef

import (
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/wso2/fhir-server/internal/validate"
)

//go:embed profiles-resources.min.json.gz
var bundleFS embed.FS

const bundleFile = "profiles-resources.min.json.gz"

// Load decompresses the embedded base R4 StructureDefinition bundle and upserts
// each base resource definition into base_definitions, keyed by resource type.
//
// It is idempotent. The embedded bundle is the source of truth: when force is
// false, Load skips the upserts only when the table already contains every base
// resource type the bundle ships (a set derived from the bundle, not a hardcoded
// count). A partial earlier load (missing types) or a changed bundle (new types)
// therefore triggers a full reload. Pass force=true to re-apply unconditionally.
func Load(ctx context.Context, pool *pgxpool.Pool, force bool) (int, error) {
	defs, err := decode()
	if err != nil {
		return 0, err
	}

	if !force {
		have, err := loadedTypes(ctx, pool)
		// Skip only when the table matches the bundle exactly. Requiring equal
		// sizes (not just a superset) means a shrunk bundle — one that dropped a
		// type — still triggers a reconciling reload that prunes the stale rows.
		if err == nil && len(have) == len(defs) && containsAll(have, defs) {
			return len(defs), nil
		}
	}

	// Reconcile the table to exactly the embedded set in one transaction, so it
	// is never left partially loaded and never retains types the bundle dropped.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // no-op once committed

	shipped := make([]string, 0, len(defs))
	for _, d := range defs {
		shipped = append(shipped, d.resourceType)
		sdJSON, err := json.Marshal(d.sd)
		if err != nil {
			return 0, fmt.Errorf("marshal base SD (%s): %w", d.resourceType, err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO base_definitions (resource_type, sd_url, sd_json)
			VALUES ($1, $2, $3)
			ON CONFLICT (resource_type)
			DO UPDATE SET sd_url = EXCLUDED.sd_url, sd_json = EXCLUDED.sd_json, loaded_at = NOW()`,
			d.resourceType, d.url, sdJSON)
		if err != nil {
			return 0, fmt.Errorf("upsert base_definitions (%s): %w", d.resourceType, err)
		}
	}
	// Drop any rows for types the bundle no longer ships so the table mirrors
	// the embedded set exactly.
	if _, err := tx.Exec(ctx,
		`DELETE FROM base_definitions WHERE NOT (resource_type = ANY($1))`, shipped); err != nil {
		return 0, fmt.Errorf("prune stale base_definitions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit base definitions: %w", err)
	}
	return len(defs), nil
}

// loadedTypes returns the set of resource types currently in base_definitions.
func loadedTypes(ctx context.Context, pool *pgxpool.Pool) (map[string]struct{}, error) {
	rows, err := pool.Query(ctx, `SELECT resource_type FROM base_definitions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := make(map[string]struct{})
	for rows.Next() {
		var rt string
		if err := rows.Scan(&rt); err != nil {
			return nil, err
		}
		have[rt] = struct{}{}
	}
	return have, rows.Err()
}

// containsAll reports whether have includes every resource type in defs.
func containsAll(have map[string]struct{}, defs []def) bool {
	for _, d := range defs {
		if _, ok := have[d.resourceType]; !ok {
			return false
		}
	}
	return true
}

type def struct {
	resourceType string
	url          string
	sd           map[string]any
}

// decode reads and parses the embedded bundle, returning the base resource
// StructureDefinitions it contains.
func decode() ([]def, error) {
	raw, err := bundleFS.ReadFile(bundleFile)
	if err != nil {
		return nil, fmt.Errorf("read embedded bundle: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("open gzip reader: %w", err)
	}
	defer gz.Close()
	data, err := io.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("decompress bundle: %w", err)
	}

	var bundle struct {
		Entry []struct {
			Resource map[string]any `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("parse bundle JSON: %w", err)
	}

	defs := make([]def, 0, len(bundle.Entry))
	for _, e := range bundle.Entry {
		sd := e.Resource
		if sd == nil {
			continue
		}
		if t, _ := sd["resourceType"].(string); t != "StructureDefinition" {
			continue
		}
		// Base resource definitions only: kind=resource excludes datatypes;
		// derivation=specialization excludes constraint profiles.
		if k, _ := sd["kind"].(string); k != "resource" {
			continue
		}
		if d, _ := sd["derivation"].(string); d != "specialization" {
			continue
		}
		rt, _ := sd["type"].(string)
		if rt == "" {
			continue
		}
		url, _ := sd["url"].(string)
		defs = append(defs, def{resourceType: rt, url: url, sd: sd})
	}
	if len(defs) == 0 {
		return nil, errors.New("embedded bundle contained no base resource definitions")
	}
	return defs, nil
}

// Cache provides concurrency-safe, memoized lookup of base StructureDefinitions
// by resource type, backed by the base_definitions table. Each definition is
// compiled (see validate.Compile) once on first use and the compiled *Profile
// is cached, so repeated validation of the same resource type does not re-parse
// the snapshot or re-read it from the database. A negative result (no base
// definition for a type) is cached too, so unknown/custom resource types cost
// at most one query.
type Cache struct {
	pool *pgxpool.Pool
	mu   sync.RWMutex
	m    map[string]*validate.Profile // resourceType -> compiled profile (nil = known-absent)
}

// NewCache returns a Cache backed by pool. A nil pool yields a Cache whose
// Lookup always returns (nil, nil), which disables base validation cleanly.
func NewCache(pool *pgxpool.Pool) *Cache {
	return &Cache{pool: pool, m: make(map[string]*validate.Profile)}
}

// Lookup returns the compiled base StructureDefinition for resourceType, or nil
// when none is loaded.
func (c *Cache) Lookup(ctx context.Context, resourceType string) (*validate.Profile, error) {
	if c == nil || c.pool == nil || resourceType == "" {
		return nil, nil
	}

	c.mu.RLock()
	prof, cached := c.m[resourceType]
	c.mu.RUnlock()
	if cached {
		return prof, nil
	}

	var raw []byte
	err := c.pool.QueryRow(ctx,
		`SELECT sd_json FROM base_definitions WHERE resource_type = $1`, resourceType).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.put(resourceType, nil)
			return nil, nil
		}
		return nil, err
	}

	var sdMap map[string]any
	if err := json.Unmarshal(raw, &sdMap); err != nil {
		return nil, fmt.Errorf("unmarshal base SD (%s): %w", resourceType, err)
	}
	prof = validate.Compile(sdMap)
	c.put(resourceType, prof)
	return prof, nil
}

func (c *Cache) put(resourceType string, prof *validate.Profile) {
	c.mu.Lock()
	c.m[resourceType] = prof
	c.mu.Unlock()
}
