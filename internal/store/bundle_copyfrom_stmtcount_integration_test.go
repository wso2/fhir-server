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

package store_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2/fhir-server/internal/store"
	"github.com/wso2/fhir-server/internal/testutil"
)

// stmtTracer records the SQL of every query issued on its pool while enabled. It
// backs the statement-count assertion below (verification protocol §3): a bundle
// import must issue a handful of statements, not one round trip per sp_* row.
type stmtTracer struct {
	mu      sync.Mutex
	enabled bool
	sqls    []string
}

func (t *stmtTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.mu.Lock()
	if t.enabled {
		t.sqls = append(t.sqls, data.SQL)
	}
	t.mu.Unlock()
	return ctx
}

func (t *stmtTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Batch statements travel through the batch tracer hooks, not TraceQueryStart,
// so the pipelined flush's statements are recorded here.
func (t *stmtTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}

func (t *stmtTracer) TraceBatchQuery(_ context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	t.mu.Lock()
	if t.enabled {
		t.sqls = append(t.sqls, data.SQL)
	}
	t.mu.Unlock()
}

func (t *stmtTracer) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

func (t *stmtTracer) start() {
	t.mu.Lock()
	t.enabled = true
	t.sqls = nil
	t.mu.Unlock()
}

func (t *stmtTracer) stop() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.enabled = false
	out := make([]string, len(t.sqls))
	copy(out, t.sqls)
	return out
}

// TestBundleWritePath_StatementCount proves the round-trip collapse: importing a
// 30-entry transaction bundle whose resources carry token/string/date/quantity/
// reference/composite params issues ≤ 20 statements in the whole transaction —
// not the ~30 × ~13 = ~390 single-row INSERTs the per-row path would have — with
// at most one INSERT per sp_* table (no per-row sp storm).
func TestBundleWritePath_StatementCount(t *testing.T) {
	admin := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, admin)

	// A second pool to the same database, carrying a query tracer. Copy() inherits
	// the container's connection settings (including the AfterConnect that pins the
	// default tenant), so only the tracer is added.
	tracer := &stmtTracer{}
	cfg := admin.Config().Copy()
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("traced pool: %v", err)
	}
	t.Cleanup(pool.Close)

	s := store.New(pool, reg)
	ctx := context.Background()

	const nPatients, nObservations = 20, 10
	entries := make([]store.BundleEntryRequest, 0, nPatients+nObservations)
	for i := 0; i < nPatients; i++ {
		pid := fmt.Sprintf("stmt-pat-%02d", i)
		entries = append(entries, store.BundleEntryRequest{
			FullURL:  "urn:uuid:" + pid,
			Method:   "POST",
			URL:      "Patient",
			Resource: patientBody(pid, fmt.Sprintf("Fam%02d", i), fmt.Sprintf("Giv%02d", i), "1990-03-14"),
		})
	}
	for i := 0; i < nObservations; i++ {
		oid := fmt.Sprintf("stmt-obs-%02d", i)
		entries = append(entries, store.BundleEntryRequest{
			FullURL: "urn:uuid:" + oid,
			Method:  "POST",
			URL:     "Observation",
			Resource: map[string]any{
				"resourceType":      "Observation",
				"id":                oid,
				"status":            "final",
				"code":              map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4"}}},
				"subject":           map[string]any{"reference": fmt.Sprintf("urn:uuid:stmt-pat-%02d", i)},
				"effectiveDateTime": "2015-02-07T13:28:17-05:00",
				"valueQuantity":     map[string]any{"value": 70 + i, "system": "http://unitsofmeasure.org", "code": "/min"},
				"component": []any{map[string]any{
					"code":          map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8480-6"}}},
					"valueQuantity": map[string]any{"value": 100 + i, "system": "http://unitsofmeasure.org", "code": "mm[Hg]"},
				}},
			},
		})
	}

	tracer.start()
	if _, err := s.ExecuteBundle(ctx, "transaction", "", entries); err != nil {
		t.Fatalf("ExecuteBundle: %v", err)
	}
	sqls := tracer.stop()

	total := len(sqls)
	var spInserts, resourceInserts, historyInserts int
	for _, q := range sqls {
		norm := strings.ToLower(strings.Join(strings.Fields(q), " "))
		switch {
		case strings.HasPrefix(norm, "insert into sp_"):
			spInserts++
		case strings.HasPrefix(norm, "insert into resources"):
			resourceInserts++
		case strings.HasPrefix(norm, "insert into resource_history"):
			historyInserts++
		}
	}

	nEntries := nPatients + nObservations
	t.Logf("bundle of %d entries → %d statements (%d sp INSERT, %d resources INSERT, %d history INSERT)",
		nEntries, total, spInserts, resourceInserts, historyInserts)

	// The whole transaction must stay within the design's ~20-statement budget.
	if total > 20 {
		t.Errorf("bundle of %d entries issued %d statements, want ≤ 20 (per-row path would be ~%d)\nstatements:\n%s",
			nEntries, total, nEntries*13, strings.Join(sqls, "\n"))
	}
	// At most one INSERT per sp_* table (8 tables) — decisive proof there is no
	// per-row sp INSERT storm (which would be dozens to hundreds here).
	if spInserts > 8 {
		t.Errorf("sp_* INSERT statements = %d, want ≤ 8 (one batched INSERT per table)", spInserts)
	}
	// All 30 resources' rows land in a single batched INSERT each (deferred creates
	// and one history INSERT), not one per resource.
	if resourceInserts != 1 {
		t.Errorf("resources INSERT statements = %d, want exactly 1 batched INSERT", resourceInserts)
	}
	if historyInserts != 1 {
		t.Errorf("resource_history INSERT statements = %d, want exactly 1 batched INSERT", historyInserts)
	}
}
