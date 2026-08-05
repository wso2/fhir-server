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

// TestEqualityWalk_MultiValueDedupe verifies the §3.3 UNION ALL shape returns
// each matching resource exactly once even when it matches several comma values
// (a resource with two of the searched codes must not appear twice).
func TestEqualityWalk_MultiValueDedupe(t *testing.T) {
	ctx := context.Background()
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	s := New(pool, reg)

	// One Observation carrying BOTH codes a and b.
	both, err := s.Create(ctx, "Observation", map[string]any{
		"resourceType": "Observation", "status": "final",
		"code": map[string]any{"coding": []any{
			map[string]any{"system": "http://loinc.org", "code": "aaaaa-1"},
			map[string]any{"system": "http://loinc.org", "code": "bbbbb-2"},
		}},
	})
	if err != nil {
		t.Fatalf("create both: %v", err)
	}
	bothID := both["id"].(string)

	// One with only code c.
	only, _ := s.Create(ctx, "Observation", map[string]any{
		"resourceType": "Observation", "status": "final",
		"code": map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "ccccc-3"}}},
	})
	onlyID := only["id"].(string)

	res, err := s.Search(ctx, SearchParams{
		ResourceType: "Observation",
		Params:       map[string][]string{"code": {"aaaaa-1,bbbbb-2,ccccc-3"}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	seen := map[string]int{}
	for _, e := range res.Entries {
		seen[e["id"].(string)]++
	}
	if seen[bothID] != 1 {
		t.Errorf("resource matching two codes must appear exactly once, got %d", seen[bothID])
	}
	if seen[onlyID] != 1 {
		t.Errorf("resource matching one code must appear once, got %d", seen[onlyID])
	}
}

// TestEqualityWalk_Plan is the §3.2 plan verification: a single-value token /
// reference search rides its recency index (idx_sp_tok_recent / idx_sp_ref_recent)
// as an sp-first walk, never a resources-first seq scan. Values are selective so
// the index seek is unambiguously the planner's choice on small data.
func TestEqualityWalk_Plan(t *testing.T) {
	ctx := context.Background()
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	s := New(pool, reg)

	pat, _ := s.Create(ctx, "Patient", map[string]any{"resourceType": "Patient"})
	patID := pat["id"].(string)

	// Bulk of common rows, plus a single row carrying the selective code/subject.
	for i := 0; i < 400; i++ {
		s.Create(ctx, "Observation", map[string]any{
			"resourceType": "Observation", "status": "final",
			"code":    map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "common-0"}}},
			"subject": map[string]any{"reference": "Patient/someone-" + string(rune('a'+i%5))},
		})
	}
	s.Create(ctx, "Observation", map[string]any{
		"resourceType": "Observation", "status": "final",
		"code":    map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "rare-9"}}},
		"subject": map[string]any{"reference": "Patient/" + patID},
	})
	if _, err := pool.Exec(ctx, "ANALYZE sp_token, sp_reference"); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	explain := func(param, value string) string {
		b := &queryBuilder{rt: "Observation", reg: reg}
		b.writeBase()
		b.applyParam(param, value)
		if b.err != nil {
			t.Fatalf("build %s=%s: %v", param, value, b.err)
		}
		sql := b.fetchSQL(20, 0)
		rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, b.args...)
		if err != nil {
			t.Fatalf("explain %s=%s: %v", param, value, err)
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

	// The scale-independent guarantee is sp-first and index-driven (never a
	// resources-first seq scan). Which token index the planner picks depends on
	// selectivity — a rare code is cheapest via idx_sp_tok_code (seek + sort), a
	// dense one via idx_sp_tok_recent (recency walk) — so we assert the family, not
	// a specific index (cf. TestSearch_CompositeTokenQuantity_Plan).
	t.Run("token walk is sp-first and index-driven", func(t *testing.T) {
		plan := explain("code", "rare-9")
		if strings.Contains(plan, "Seq Scan on sp_token") {
			t.Errorf("token: unexpected seq scan on sp_token:\n%s", plan)
		}
		if !strings.Contains(plan, "idx_sp_tok") {
			t.Errorf("token: expected an sp_token index scan, got:\n%s", plan)
		}
	})

	t.Run("reference walk is sp-first and index-driven", func(t *testing.T) {
		plan := explain("subject", "Patient/"+patID)
		if strings.Contains(plan, "Seq Scan on sp_reference") {
			t.Errorf("reference: unexpected seq scan on sp_reference:\n%s", plan)
		}
		if !strings.Contains(plan, "idx_sp_ref") {
			t.Errorf("reference: expected an sp_reference index scan, got:\n%s", plan)
		}
	})
}
