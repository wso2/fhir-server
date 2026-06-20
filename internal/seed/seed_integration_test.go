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

	// Re-run seed — ON CONFLICT DO NOTHING should prevent duplicate inserts.
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
