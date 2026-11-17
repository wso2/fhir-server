//go:build integration

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

package seed_test

import (
	"context"
	"testing"

	"github.com/wso2/fhir-server/internal/seed"
	"github.com/wso2/fhir-server/internal/testutil"
)

func TestSeedSearchParams_InsertsRows(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	ctx := context.Background()

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM search_param_definitions WHERE is_custom = FALSE`,
	).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	// The CSV contains 1707 rows for base FHIR R4 search params
	if count < 100 {
		t.Errorf("expected ≥100 base search params, got %d", count)
	}
	t.Logf("seeded %d base FHIR R4 search parameters", count)
}

func TestSeedSearchParams_Idempotent(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	ctx := context.Background()

	var countBefore int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM search_param_definitions`).Scan(&countBefore); err != nil {
		t.Fatalf("count before: %v", err)
	}

	// Re-run seed — the base rows are refreshed in place, never duplicated.
	if err := seed.SeedSearchParams(ctx, pool); err != nil {
		t.Fatalf("re-seed: %v", err)
	}

	var countAfter int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM search_param_definitions`).Scan(&countAfter); err != nil {
		t.Fatalf("count after: %v", err)
	}

	if countAfter != countBefore {
		t.Errorf("seed not idempotent: before=%d, after=%d", countBefore, countAfter)
	}
}

// TestSeedSearchParams_RefreshesStaleBaseRow covers databases seeded by an
// older CSV: Condition.code once carried AllergyIntolerance.reaction.substance,
// so every Condition?code search returned nothing. A re-seed must repair the
// existing base row rather than skip it.
func TestSeedSearchParams_RefreshesStaleBaseRow(t *testing.T) {
	pool := testutil.MustDB(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `INSERT INTO search_param_definitions
		(resource_type, param_name, param_type, fhirpath_expr, is_custom, ig_source)
		VALUES ('Condition', 'code', 'token', 'AllergyIntolerance.reaction.substance', FALSE, '')`); err != nil {
		t.Fatalf("insert stale row: %v", err)
	}
	if err := seed.SeedSearchParams(ctx, pool); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var expr string
	if err := pool.QueryRow(ctx,
		`SELECT fhirpath_expr FROM search_param_definitions
		 WHERE resource_type = 'Condition' AND param_name = 'code'`).Scan(&expr); err != nil {
		t.Fatalf("query expression: %v", err)
	}
	if expr != "Condition.code" {
		t.Errorf("stale base row not refreshed: got %q, want %q", expr, "Condition.code")
	}
}

// TestSeedSearchParams_PreservesOverrides verifies the refresh is scoped to
// base rows: custom and IG-sourced definitions keep their expressions.
func TestSeedSearchParams_PreservesOverrides(t *testing.T) {
	pool := testutil.MustDB(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `INSERT INTO search_param_definitions
		(resource_type, param_name, param_type, fhirpath_expr, is_custom, ig_source) VALUES
		('Condition', 'code', 'token', 'Condition.overrideCustom', TRUE, ''),
		('Observation', 'code', 'token', 'Observation.overrideIG', FALSE, 'hl7.fhir.us.core@7.0.0')`); err != nil {
		t.Fatalf("insert overrides: %v", err)
	}
	if err := seed.SeedSearchParams(ctx, pool); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, tc := range []struct{ rt, param, want string }{
		{"Condition", "code", "Condition.overrideCustom"},
		{"Observation", "code", "Observation.overrideIG"},
	} {
		var expr string
		if err := pool.QueryRow(ctx,
			`SELECT fhirpath_expr FROM search_param_definitions
			 WHERE resource_type = $1 AND param_name = $2`, tc.rt, tc.param).Scan(&expr); err != nil {
			t.Fatalf("query %s.%s: %v", tc.rt, tc.param, err)
		}
		if expr != tc.want {
			t.Errorf("%s.%s override was overwritten: got %q, want %q", tc.rt, tc.param, expr, tc.want)
		}
	}
}

func TestSeedSearchParams_KnownParams(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	ctx := context.Background()

	knownParams := []struct {
		resource string
		param    string
	}{
		{"Patient", "name"},
		{"Patient", "birthdate"},
		{"Patient", "identifier"},
		{"Observation", "code"},
		{"Observation", "date"},
		{"Condition", "code"},
		{"MedicationRequest", "status"},
		{"Encounter", "subject"},
	}

	for _, kp := range knownParams {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS(
				SELECT 1 FROM search_param_definitions
				WHERE resource_type = $1 AND param_name = $2
			)`, kp.resource, kp.param,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("query %s.%s: %v", kp.resource, kp.param, err)
		}
		if !exists {
			t.Errorf("missing expected param %s.%s", kp.resource, kp.param)
		}
	}
}

func TestSeedSearchParams_ParamTypes(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	ctx := context.Background()

	// Verify we have all expected param types
	types := []string{"string", "token", "date", "reference", "uri"}
	for _, pt := range types {
		var count int
		pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM search_param_definitions WHERE param_type = $1`, pt,
		).Scan(&count)
		if count == 0 {
			t.Errorf("no params of type %q found after seeding", pt)
		}
		t.Logf("type %q: %d params", pt, count)
	}
}
