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

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/wso2/fhir-server/internal/testutil"
)

// TestSearch_DensityProbe_Plan is the design-addendum §3.2 verification: the
// density probe measures the half-bounded match count and pins the fetch plan.
//   - sparse bound (probe < cap) → sp-first value-index seek pinned with a
//     MATERIALIZED CTE, never a seq scan or the recency walk it mis-prefers.
//   - dense bound (probe hits the cap) → the recency walk, unpinned.
//
// It exercises both sp_date and sp_quantity. The dense case is forced cheaply by
// injecting > defaultProbeCap sp_date rows for one resource (the probe counts rows, not
// distinct resources), avoiding a multi-thousand-resource seed.
func TestSearch_DensityProbe_Plan(t *testing.T) {
	ctx := context.Background()
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	s := New(pool, reg)

	enc, err := s.Create(ctx, "Encounter", map[string]any{
		"resourceType": "Encounter", "status": "finished",
		"class":  map[string]any{"system": "http://terminology.hl7.org/CodeSystem/v3-ActCode", "code": "AMB"},
		"period": map[string]any{"start": "2020-01-01", "end": "2020-12-31"},
	})
	if err != nil {
		t.Fatalf("seed encounter: %v", err)
	}
	encID := enc["id"].(string)

	// Inflate the 'date' partition past defaultProbeCap for this one resource so a
	// matching bound trips the dense branch without seeding thousands of rows.
	if _, err := pool.Exec(ctx, `
		INSERT INTO sp_date (resource_id, resource_type, param_name, value_low, value_high, last_updated)
		SELECT $1, 'Encounter', 'date', '2020-01-01'::timestamptz, '2020-12-31'::timestamptz, now()
		FROM generate_series(1, $2)`, encID, defaultProbeCap+1000); err != nil {
		t.Fatalf("inflate date partition: %v", err)
	}

	// A body of sparse-tail quantity rows plus a dense low tail.
	for i := 0; i < 400; i++ {
		s.Create(ctx, "Observation", map[string]any{
			"resourceType": "Observation", "status": "final",
			"code":          map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8480-6"}}},
			"valueQuantity": map[string]any{"value": float64(i % 200), "system": "http://unitsofmeasure.org", "code": "mm[Hg]"},
		})
	}
	if _, err := pool.Exec(ctx, "ANALYZE sp_date, sp_quantity"); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	// build runs the same flow as Search: build predicate, probe, then fetchSQL.
	build := func(rt, param, value string) (*queryBuilder, string) {
		b := &queryBuilder{rt: rt, reg: reg}
		b.writeBase()
		b.applyParam(param, value)
		if b.err != nil {
			t.Fatalf("build %s=%s: %v", param, value, b.err)
		}
		if err := b.decidePlan(ctx, pool); err != nil {
			t.Fatalf("probe %s=%s: %v", param, value, err)
		}
		return b, b.fetchSQL(20, 0)
	}

	explain := func(b *queryBuilder, sql string) string {
		rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, b.args...)
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		defer rows.Close()
		var sb strings.Builder
		for rows.Next() {
			var line string
			rows.Scan(&line)
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
		return sb.String()
	}

	t.Run("date sparse pins value_high seek", func(t *testing.T) {
		b, sql := build("Encounter", "date", "ge2099-01-01")
		if b.planDense != planSparse {
			t.Fatalf("expected planSparse, got %v", b.planDense)
		}
		if !strings.Contains(sql, "MATERIALIZED") {
			t.Errorf("sparse date fetch must pin with a MATERIALIZED CTE, got:\n%s", sql)
		}
		plan := explain(b, sql)
		if strings.Contains(plan, "Seq Scan on sp_date") {
			t.Errorf("sparse date: unexpected seq scan:\n%s", plan)
		}
		if !strings.Contains(plan, "idx_sp_date_high") {
			t.Errorf("sparse date: expected idx_sp_date_high seek, got:\n%s", plan)
		}
	})

	t.Run("hitting the probe cap always walks, never materializes", func(t *testing.T) {
		// Regression guard: the cap is a lower bound only, so a predicate matching
		// most of the partition (here ~6001 of ~6002 date rows) hits it exactly like
		// a borderline one. At the cap the verdict must be the recency walk
		// unconditionally — materializing there would pull the whole match set
		// (measured ~870ms / 41k buffers vs. ~1.6ms for the walk). We assert the
		// scale-independent guarantees — dense verdict, no MATERIALIZED pin — not a
		// specific index: on this tiny all-matching table a seq scan is legitimately
		// cheaper than the walk, so pinning an index name would be flaky.
		b, sql := build("Encounter", "date", "ge2019-01-01")
		if b.planDense != planDenseWalk {
			t.Fatalf("expected planDenseWalk, got %v", b.planDense)
		}
		if strings.Contains(sql, "MATERIALIZED") {
			t.Errorf("dense date fetch must not pin, got:\n%s", sql)
		}
		// Sanity: the emitted plan executes.
		_ = explain(b, sql)
	})

	t.Run("quantity sparse pins value_high seek", func(t *testing.T) {
		b, sql := build("Observation", "value-quantity", "ge99999")
		if b.planDense != planSparse {
			t.Fatalf("expected planSparse, got %v", b.planDense)
		}
		if !strings.Contains(sql, "MATERIALIZED") {
			t.Errorf("sparse quantity fetch must pin with a MATERIALIZED CTE, got:\n%s", sql)
		}
		plan := explain(b, sql)
		if strings.Contains(plan, "Seq Scan on sp_quantity") {
			t.Errorf("sparse quantity: unexpected seq scan:\n%s", plan)
		}
		if !strings.Contains(plan, "idx_sp_qty_high") {
			t.Errorf("sparse quantity: expected idx_sp_qty_high seek, got:\n%s", plan)
		}
	})
}
