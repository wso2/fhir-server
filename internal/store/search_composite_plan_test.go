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

// TestSearch_CompositeTokenQuantity_Plan is the design's plan verification (§6.2):
// the value-driven scan must ride a composite index (no seq scan), and a bounded
// ge/le search must reach the GiST range-overlap index via the && operator.
func TestSearch_CompositeTokenQuantity_Plan(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	s := New(pool, reg)
	ctx := context.Background()

	// Seed enough rows across several codes and a value spread that lt60 is
	// selective, so the planner has a reason to prefer an index over a seq scan.
	codes := []string{"8867-4", "8480-6", "8462-4", "2708-6", "9279-1"}
	for i := 0; i < 400; i++ {
		code := codes[i%len(codes)]
		val := float64(40 + (i % 120)) // 40..159
		if _, err := s.Create(ctx, "Observation", map[string]any{
			"resourceType": "Observation", "status": "final",
			"code":          map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": code}}},
			"valueQuantity": map[string]any{"value": val, "system": "http://unitsofmeasure.org", "code": "/min"},
		}); err != nil {
			t.Fatalf("seed create %d: %v", i, err)
		}
	}
	if _, err := pool.Exec(ctx, "ANALYZE sp_composite_token_quantity"); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	plan := func(value string) string {
		b := &queryBuilder{rt: "Observation", reg: reg}
		b.writeBase()
		b.applyParam("code-value-quantity", value)
		if b.err != nil {
			t.Fatalf("build %q: %v", value, b.err)
		}
		sql := b.fetchSQL(20, 0)
		rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, b.args...)
		if err != nil {
			t.Fatalf("explain %q: %v", value, err)
		}
		defer rows.Close()
		var sb strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatalf("scan plan: %v", err)
			}
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("plan rows: %v", err)
		}
		return sb.String()
	}

	// Sparse lt drive: an index on the composite table, never a seq scan of it.
	ltPlan := plan("8867-4$lt60")
	t.Logf("lt plan:\n%s", ltPlan)
	if strings.Contains(ltPlan, "Seq Scan on sp_composite_token_quantity") {
		t.Errorf("lt: unexpected seq scan on sp_composite_token_quantity:\n%s", ltPlan)
	}
	if !strings.Contains(ltPlan, "idx_sp_comp_tokqty") {
		t.Errorf("lt: expected a composite index scan, got:\n%s", ltPlan)
	}

	// Bounded ge search: served by a composite index, and the range-overlap
	// operator (&&, the GiST-eligible predicate) must be what's applied — not a
	// scalar-bound fallback that drops the overlap. We don't pin a specific index:
	// depending on selectivity the planner may satisfy the sort via the recency
	// index or drive off the range-GiST, and both are composite indexes, so
	// pinning one would be flaky on small data.
	gePlan := plan("8867-4$ge60")
	t.Logf("ge plan:\n%s", gePlan)
	if !strings.Contains(gePlan, "idx_sp_comp_tokqty") {
		t.Errorf("ge: expected a composite index scan, got:\n%s", gePlan)
	}
	if !strings.Contains(gePlan, "&&") {
		t.Errorf("ge: expected the numrange overlap (&&) predicate, not a scalar fallback, got:\n%s", gePlan)
	}
}
