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

// Integration tests for parallel transaction bundle execution
// (x-bundle-processing-logic: parallel / bundle.transactionConcurrency).

package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2/fhir-server/internal/store"
	"github.com/wso2/fhir-server/internal/tenant"
	"github.com/wso2/fhir-server/internal/testutil"
)

// syncBuffer is a goroutine-safe bytes.Buffer: parallel shards log concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs replaces the default slog logger with a Debug-level JSON capture
// for the duration of the test, so tests can assert which execution path ran
// (the parallel executor logs mode=parallel; the fallback logs an Info).
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// ranParallel reports whether the captured logs show the parallel executor
// completed a transaction bundle.
func ranParallel(logs string) bool {
	return strings.Contains(logs, `"msg":"processed transaction bundle"`) &&
		strings.Contains(logs, `"mode":"parallel"`)
}

// parallelStore builds a Store with the parallel capability enabled at
// concurrency k (and the header-absent default set by parallelDefault).
func parallelStore(t *testing.T, pool *pgxpool.Pool, k int, parallelDefault bool) *store.Store {
	t.Helper()
	reg := testutil.MustRegistry(t, pool)
	return store.New(pool, reg, store.WithBundleTuning(store.BundleTuning{
		TransactionConcurrency:     k,
		TransactionParallelDefault: parallelDefault,
	}))
}

func parallelCtx() context.Context {
	return store.WithBundleProcessing(context.Background(), store.BundleProcessingParallel)
}

// syntheaEntries builds a Synthea-shaped transaction bundle: Patients created
// by POST with urn:uuid fullUrls, Observations/Encounters/Conditions referencing
// them by urn:uuid, plus conditional creates (If-None-Exist). All ids are
// client-assigned so two runs of the same bundle are comparable byte-for-byte
// (modulo meta.lastUpdated).
func syntheaEntries(prefix string, nPatients int) []store.BundleEntryRequest {
	var entries []store.BundleEntryRequest
	for i := 0; i < nPatients; i++ {
		pid := fmt.Sprintf("%s-pat-%d", prefix, i)
		pURN := fmt.Sprintf("urn:uuid:00000000-0000-4000-8000-%012d", i)
		entries = append(entries, store.BundleEntryRequest{
			FullURL:  pURN,
			Method:   "POST",
			URL:      "Patient",
			Resource: patientBody(pid, fmt.Sprintf("Fam%d", i), "Alex", "1974-12-25"),
		})
		entries = append(entries, store.BundleEntryRequest{
			Method: "POST",
			URL:    "Observation",
			Resource: map[string]any{
				"resourceType": "Observation",
				"id":           fmt.Sprintf("%s-obs-%d", prefix, i),
				"status":       "final",
				"code": map[string]any{"coding": []any{map[string]any{
					"system": "http://loinc.org", "code": "29463-7", "display": "Body Weight",
				}}},
				"subject":           map[string]any{"reference": pURN},
				"effectiveDateTime": "2020-03-14T09:00:00Z",
				"valueQuantity": map[string]any{
					"value": 70.5 + float64(i), "unit": "kg",
					"system": "http://unitsofmeasure.org", "code": "kg",
				},
			},
		})
		entries = append(entries, store.BundleEntryRequest{
			Method: "POST",
			URL:    "Encounter",
			Resource: map[string]any{
				"resourceType": "Encounter",
				"id":           fmt.Sprintf("%s-enc-%d", prefix, i),
				"status":       "finished",
				"class":        map[string]any{"system": "http://terminology.hl7.org/CodeSystem/v3-ActCode", "code": "AMB"},
				"subject":      map[string]any{"reference": pURN},
				"period":       map[string]any{"start": "2020-03-14T09:00:00Z", "end": "2020-03-14T09:30:00Z"},
			},
		})
		// Conditional create: no match on a fresh database, so it creates — and
		// both the serial and parallel runs resolve it identically at plan time.
		entries = append(entries, store.BundleEntryRequest{
			Method:      "POST",
			URL:         "Condition",
			IfNoneExist: fmt.Sprintf("code=http://snomed.info/sct|%s-44054006-%d", prefix, i),
			Resource: map[string]any{
				"resourceType": "Condition",
				"id":           fmt.Sprintf("%s-cond-%d", prefix, i),
				"code": map[string]any{"coding": []any{map[string]any{
					"system": "http://snomed.info/sct", "code": fmt.Sprintf("%s-44054006-%d", prefix, i),
				}}},
				"subject":       map[string]any{"reference": pURN},
				"onsetDateTime": "2018-06-01",
			},
		})
	}
	return entries
}

// stripVolatile removes the timestamp that legitimately differs between two
// runs of the same bundle; everything else must match exactly.
func stripVolatile(res map[string]any) map[string]any {
	if res == nil {
		return nil
	}
	raw, _ := json.Marshal(res)
	var cp map[string]any
	_ = json.Unmarshal(raw, &cp)
	if meta, ok := cp["meta"].(map[string]any); ok {
		delete(meta, "lastUpdated")
	}
	return cp
}

// tableCounts snapshots the row count of every table the write path touches.
func tableCounts(t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, tbl := range []string{
		"resources", "resource_history",
		"sp_string", "sp_token", "sp_date", "sp_number",
		"sp_quantity", "sp_uri", "sp_reference", "sp_composite_token_quantity",
	} {
		var n int
		if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+tbl).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		counts[tbl] = n
	}
	return counts
}

// ─── 1. Equivalence: serial vs K=8 ────────────────────────────────────────────

// TestParallelBundle_EquivalenceWithSerial runs the same Synthea-shaped bundle
// (POSTs with urn:uuid references + conditional creates) serially on one
// database and at K=8 on another, and requires identical per-entry results
// (modulo meta.lastUpdated) and identical row counts in every sp_* table.
func TestParallelBundle_EquivalenceWithSerial(t *testing.T) {
	logs := captureLogs(t)

	serialPool := testutil.MustSeededDB(t)
	serialStore := store.New(serialPool, testutil.MustRegistry(t, serialPool))
	parPool := testutil.MustSeededDB(t)
	parStore := parallelStore(t, parPool, 8, false)

	entries := syntheaEntries("eq", 5) // 20 entries → shards of 2–3 ops at K=8

	serialRes, err := serialStore.ExecuteBundle(context.Background(), "transaction", "", entries)
	if err != nil {
		t.Fatalf("serial ExecuteBundle: %v", err)
	}
	parRes, err := parStore.ExecuteBundle(parallelCtx(), "transaction", "", entries)
	if err != nil {
		t.Fatalf("parallel ExecuteBundle: %v", err)
	}
	if !ranParallel(logs.String()) {
		t.Fatal("parallel run did not take the parallel executor (no mode=parallel log)")
	}

	if len(serialRes) != len(parRes) {
		t.Fatalf("result lengths differ: serial=%d parallel=%d", len(serialRes), len(parRes))
	}
	for i := range serialRes {
		s, p := serialRes[i], parRes[i]
		if s.Status != p.Status || s.Location != p.Location || s.ETag != p.ETag {
			t.Errorf("entry %d envelope differs: serial={%s %s %s} parallel={%s %s %s}",
				i, s.Status, s.Location, s.ETag, p.Status, p.Location, p.ETag)
		}
		if !reflect.DeepEqual(stripVolatile(s.Resource), stripVolatile(p.Resource)) {
			t.Errorf("entry %d resource differs (modulo lastUpdated)", i)
		}
	}

	sc, pc := tableCounts(t, serialPool), tableCounts(t, parPool)
	if !reflect.DeepEqual(sc, pc) {
		t.Errorf("table row counts differ:\nserial:   %v\nparallel: %v", sc, pc)
	}
}

// ─── 2. Atomic rollback ───────────────────────────────────────────────────────

// TestParallelBundle_AtomicRollback poisons one entry of a K=8 bundle (a plain
// PUT to a missing id → 404) and requires the whole bundle to roll back: zero
// rows persisted anywhere, and the error naming the poisoned entry.
func TestParallelBundle_AtomicRollback(t *testing.T) {
	logs := captureLogs(t)
	pool := testutil.MustSeededDB(t)
	s := parallelStore(t, pool, 8, false)

	entries := syntheaEntries("rb", 4)
	poisonedIndex := len(entries)
	entries = append(entries, store.BundleEntryRequest{
		Method:   "PUT", // plain PUT to a missing id: no update-as-create → 404
		URL:      "Patient/rb-missing",
		Resource: patientBody("rb-missing", "Ghost", "Gone", "1900-01-01"),
	})

	before := tableCounts(t, pool)
	_, err := s.ExecuteBundle(parallelCtx(), "transaction", "", entries)
	if err == nil {
		t.Fatal("expected the poisoned bundle to fail, got nil error")
	}
	var be *store.BundleError
	if !errors.As(err, &be) {
		t.Fatalf("expected *store.BundleError, got %T: %v", err, err)
	}
	if be.HTTPStatus != 404 {
		t.Errorf("HTTPStatus = %d, want 404", be.HTTPStatus)
	}
	if be.EntryIndex != poisonedIndex {
		t.Errorf("EntryIndex = %d, want %d (the poisoned entry)", be.EntryIndex, poisonedIndex)
	}
	// The failure must have gone through the parallel executor, not the serial
	// fallback, for this to prove parallel rollback.
	if strings.Contains(logs.String(), "parallel bundle fell back to serial") {
		t.Error("bundle unexpectedly fell back to the serial path")
	}

	if after := tableCounts(t, pool); !reflect.DeepEqual(before, after) {
		t.Errorf("rows persisted despite rollback:\nbefore: %v\nafter:  %v", before, after)
	}
}

// ─── 3. Overlap fallback ──────────────────────────────────────────────────────

// TestParallelBundle_OverlapFallback sends a bundle whose POST and DELETE
// target the same id under parallel mode: the server must detect the overlap,
// fall back to the serial path (logged at Info), and produce the serial result.
func TestParallelBundle_OverlapFallback(t *testing.T) {
	logs := captureLogs(t)
	pool := testutil.MustSeededDB(t)
	s := parallelStore(t, pool, 8, false)
	ctx := context.Background()

	entries := []store.BundleEntryRequest{
		{Method: "POST", URL: "Patient", Resource: patientBody("ov-pat", "Overlap", "Once", "1980-05-05")},
		{Method: "DELETE", URL: "Patient/ov-pat"},
		{Method: "POST", URL: "Patient", Resource: patientBody("ov-other", "Bystander", "Beth", "1981-06-06")},
	}

	results, err := s.ExecuteBundle(parallelCtx(), "transaction", "", entries)
	if err != nil {
		t.Fatalf("ExecuteBundle: %v", err)
	}

	if !strings.Contains(logs.String(), "parallel bundle fell back to serial") {
		t.Error("expected the Info fallback log, not found")
	}
	if ranParallel(logs.String()) {
		t.Error("bundle with overlapping targets must not run on the parallel executor")
	}

	// Serial semantics: DELETE runs first (no-op on the missing id), then the
	// POST creates — the resource must exist afterwards.
	if results[0].Status != "201 Created" {
		t.Errorf("POST status = %q, want 201 Created", results[0].Status)
	}
	if results[1].Status != "204 No Content" {
		t.Errorf("DELETE status = %q, want 204 No Content", results[1].Status)
	}
	if _, err := s.Read(ctx, "Patient", "ov-pat"); err != nil {
		t.Errorf("Patient/ov-pat should exist after the serial-ordered bundle: %v", err)
	}
}

// ─── 4. Header / config matrix ────────────────────────────────────────────────

// TestParallelBundle_ModeMatrix drives the four header × config combinations
// from the plan and asserts which executor ran via the captured logs.
func TestParallelBundle_ModeMatrix(t *testing.T) {
	pool := testutil.MustSeededDB(t)

	cases := []struct {
		name         string
		k            int
		parallelDflt bool
		headerMode   string // "" = header absent
		wantParallel bool
	}{
		{"header absent, default sequential", 8, false, "", false},
		{"header absent, default parallel", 8, true, "", true},
		{"header parallel, K=1 → ignored", 1, false, store.BundleProcessingParallel, false},
		{"header sequential overrides default parallel", 8, true, store.BundleProcessingSequential, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			s := parallelStore(t, pool, tc.k, tc.parallelDflt)
			ctx := context.Background()
			if tc.headerMode != "" {
				ctx = store.WithBundleProcessing(ctx, tc.headerMode)
			}
			_, err := s.ExecuteBundle(ctx, "transaction", "", syntheaEntries(fmt.Sprintf("mx%d", i), 2))
			if err != nil {
				t.Fatalf("ExecuteBundle: %v", err)
			}
			if got := ranParallel(logs.String()); got != tc.wantParallel {
				t.Errorf("parallel executor ran = %v, want %v", got, tc.wantParallel)
			}
		})
	}
}

// ─── 5. Row cap across shards ─────────────────────────────────────────────────

// TestParallelBundle_RowCapRejected verifies WriteMaxRowsPerBundle stays a
// per-bundle contract under K=8: shards individually under the cap must still
// be rejected when the bundle total exceeds it — 413, nothing committed.
func TestParallelBundle_RowCapRejected(t *testing.T) {
	captureLogs(t) // silence the expected row-limit Warn from test output
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	// Each Patient extracts ~8+ index rows; 16 Patients across 8 shards keeps
	// every shard (~2 Patients ≈ 20 rows) under the 60-row cap while the bundle
	// total (~160) exceeds it — the case only the bundle-level sum catches.
	s := store.New(pool, reg,
		store.WithWriteTuning(store.WriteTuning{MaxRowsPerStatement: 1000, MaxRowsPerBundle: 60}),
		store.WithBundleTuning(store.BundleTuning{TransactionConcurrency: 8}),
	)

	var entries []store.BundleEntryRequest
	for i := 0; i < 16; i++ {
		entries = append(entries, store.BundleEntryRequest{
			Method:   "POST",
			URL:      "Patient",
			Resource: patientBody(fmt.Sprintf("cap-pat-%d", i), fmt.Sprintf("Cap%d", i), "Carl", "1970-01-01"),
		})
	}

	before := tableCounts(t, pool)
	_, err := s.ExecuteBundle(parallelCtx(), "transaction", "", entries)
	if err == nil {
		t.Fatal("expected the over-cap bundle to be rejected, got nil error")
	}
	var be *store.BundleError
	if !errors.As(err, &be) {
		t.Fatalf("expected *store.BundleError, got %T: %v", err, err)
	}
	if be.HTTPStatus != 413 {
		t.Errorf("HTTPStatus = %d, want 413", be.HTTPStatus)
	}

	if after := tableCounts(t, pool); !reflect.DeepEqual(before, after) {
		t.Errorf("rows persisted despite the cap rejection:\nbefore: %v\nafter:  %v", before, after)
	}
}

// ─── 6. Pool smaller than K: clamp + contention fallback ─────────────────────

// TestParallelBundle_PoolSmallerThanK reproduces the CI deadlock: K=8 against
// a pool of MaxConns=2. The shard count must clamp to the pool size (never
// self-deadlock waiting for connections this bundle already holds), and when
// the pool is actively contended the bundle must fall back to the serial path
// after the acquisition timeout instead of blocking forever.
func TestParallelBundle_PoolSmallerThanK(t *testing.T) {
	logs := captureLogs(t)
	admin := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, admin)

	cfg := admin.Config()
	cfg.MaxConns = 2
	small, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("small pool: %v", err)
	}
	t.Cleanup(small.Close)

	s := store.New(small, reg, store.WithBundleTuning(store.BundleTuning{TransactionConcurrency: 8}))

	// Uncontended: K clamps from 8 to the pool's 2 and the bundle still runs
	// on the parallel executor. Pre-fix this call never returned.
	if _, err := s.ExecuteBundle(parallelCtx(), "transaction", "", syntheaEntries("cl", 2)); err != nil {
		t.Fatalf("clamped parallel ExecuteBundle: %v", err)
	}
	if !ranParallel(logs.String()) {
		t.Error("clamped bundle should still run on the parallel executor")
	}
	if !strings.Contains(logs.String(), `"shards":2`) {
		t.Error("expected the shard count clamped to the pool size (shards=2)")
	}

	// Contended: hold one of the two connections for the duration. Shard
	// acquisition times out, every held shard is released, and the bundle
	// completes on the serial path.
	conn, err := small.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire blocker conn: %v", err)
	}
	defer conn.Release()

	results, err := s.ExecuteBundle(parallelCtx(), "transaction", "", syntheaEntries("ct", 2))
	if err != nil {
		t.Fatalf("contended ExecuteBundle should fall back to serial and succeed: %v", err)
	}
	for i, r := range results {
		if !strings.HasPrefix(r.Status, "20") {
			t.Errorf("entry %d status = %q, want 2xx", i, r.Status)
		}
	}
	if !strings.Contains(logs.String(), "shard connections unavailable") {
		t.Error("expected the Info fallback log for shard-connection contention")
	}
}

// ─── 7. Tenant stamping ───────────────────────────────────────────────────────

// TestParallelBundle_TenantStamping runs a K=8 bundle under an explicit tenant
// and verifies every row every shard wrote carries that tenant.
func TestParallelBundle_TenantStamping(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	s := parallelStore(t, pool, 8, false)

	ctx := store.WithBundleProcessing(tenant.WithTenant(context.Background(), "tenant-par"), store.BundleProcessingParallel)
	if _, err := s.ExecuteBundle(ctx, "transaction", "", syntheaEntries("tn", 4)); err != nil {
		t.Fatalf("ExecuteBundle: %v", err)
	}

	// The seeded test pool connects as a superuser (bypasses RLS), so it can
	// audit tenant_id across all tenants directly.
	for _, tbl := range []string{"resources", "resource_history", "sp_token", "sp_string", "sp_reference", "sp_quantity", "sp_date"} {
		idCol := "resource_id" // sp_* tables
		if tbl == "resources" || tbl == "resource_history" {
			idCol = "fhir_id"
		}
		var total, tenantRows int
		if err := pool.QueryRow(context.Background(),
			fmt.Sprintf(`SELECT count(*), count(*) FILTER (WHERE tenant_id = 'tenant-par') FROM %s WHERE %s LIKE 'tn-%%'`, tbl, idCol),
		).Scan(&total, &tenantRows); err != nil {
			t.Fatalf("audit %s: %v", tbl, err)
		}
		if total == 0 {
			t.Errorf("%s: expected rows from the bundle, found none", tbl)
		}
		if total != tenantRows {
			t.Errorf("%s: %d of %d rows missing tenant_id='tenant-par'", tbl, total-tenantRows, total)
		}
	}
}
