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

package basedef

import (
	"context"
	"testing"

	"github.com/wso2/fhir-server/internal/testutil"
)

// A second load is a no-op skip (the shipped set is already complete) and
// reports the same count.
func TestLoad_Idempotent(t *testing.T) {
	pool := testutil.MustDB(t)
	ctx := context.Background()

	n1, err := Load(ctx, pool, false)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if n1 == 0 {
		t.Fatal("first load inserted nothing")
	}

	n2, err := Load(ctx, pool, false)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if n2 != n1 {
		t.Fatalf("idempotent load changed count: %d -> %d", n1, n2)
	}
}

// When a previously-loaded type goes missing (a partial load), the next
// non-forced load detects the gap against the bundle and reloads.
func TestLoad_ReloadsWhenIncomplete(t *testing.T) {
	pool := testutil.MustDB(t)
	ctx := context.Background()

	if _, err := Load(ctx, pool, false); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM base_definitions WHERE resource_type = 'Patient'`); err != nil {
		t.Fatalf("delete Patient: %v", err)
	}

	if _, err := Load(ctx, pool, false); err != nil {
		t.Fatalf("reload: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM base_definitions WHERE resource_type = 'Patient'`).Scan(&n); err != nil {
		t.Fatalf("count Patient: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected Patient to be reloaded, got count %d", n)
	}
}
