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
	"strings"
	"testing"
)

// TestEqualityWalkEmission asserts the sp-first ordered-walk emission for token
// and reference equality searches (plan-selection standard §3.2/§3.3), and that
// modifiers that cannot use it stay on the correlated path.
func TestEqualityWalkEmission(t *testing.T) {
	// Single-value token → sp-first walk off sp_token, ordered by last_updated,
	// not a resources-first correlated EXISTS driver.
	t.Run("single token sp-first", func(t *testing.T) {
		sql := buildSQL(t, "Observation", "code", "8302-2")
		if !strings.Contains(sql, "FROM sp_token s") {
			t.Errorf("expected sp-first scan of sp_token, got:\n%s", sql)
		}
		if !strings.Contains(sql, "ORDER BY sort0 DESC") {
			t.Errorf("expected ordered walk by sort0, got:\n%s", sql)
		}
		if strings.Contains(sql, "UNION ALL") {
			t.Errorf("single value must not UNION ALL, got:\n%s", sql)
		}
	})

	// system|code single value still equality-shaped.
	t.Run("single sys|code sp-first", func(t *testing.T) {
		sql := buildSQL(t, "Observation", "code", "http://loinc.org|8302-2")
		if !strings.Contains(sql, "s.system =") || !strings.Contains(sql, "s.code =") {
			t.Errorf("expected system+code equality, got:\n%s", sql)
		}
		if !strings.Contains(sql, "FROM sp_token s") || strings.Contains(sql, "UNION ALL") {
			t.Errorf("expected single sp_token walk, got:\n%s", sql)
		}
	})

	// Multi-value token → UNION ALL per value, deduped by fhir_id, no OR-of-EXISTS.
	t.Run("multi token UNION ALL", func(t *testing.T) {
		sql := buildSQL(t, "Observation", "code", "a,b,c")
		if n := strings.Count(sql, "UNION ALL"); n != 2 {
			t.Errorf("expected 2 UNION ALL joining 3 branches, got %d:\n%s", n, sql)
		}
		if n := strings.Count(sql, "FROM sp_token s"); n != 3 {
			t.Errorf("expected 3 per-value sp_token scans, got %d:\n%s", n, sql)
		}
		if !strings.Contains(sql, "DISTINCT ON (fhir_id)") {
			t.Errorf("expected dedupe by fhir_id, got:\n%s", sql)
		}
		if strings.Contains(sql, "OR EXISTS") || strings.Contains(sql, ") OR (") {
			t.Errorf("multi-value equality must not OR bodies, got:\n%s", sql)
		}
	})

	// Single-value reference → sp-first walk off sp_reference (target_type/id).
	t.Run("single reference sp-first", func(t *testing.T) {
		sql := buildSQL(t, "Observation", "subject", "Patient/123")
		if !strings.Contains(sql, "FROM sp_reference s") {
			t.Errorf("expected sp-first scan of sp_reference, got:\n%s", sql)
		}
		if !strings.Contains(sql, "s.target_type =") || !strings.Contains(sql, "s.target_id =") {
			t.Errorf("expected target_type+target_id equality, got:\n%s", sql)
		}
	})

	// :not token cannot use the walk — stays on the correlated (NOT EXISTS) path.
	t.Run("token :not stays correlated", func(t *testing.T) {
		sql := buildSQL(t, "Observation", "code:not", "8302-2")
		if usesIdFirst(sql) {
			t.Errorf(":not must not drive the sp-first walk, got:\n%s", sql)
		}
	})
}
