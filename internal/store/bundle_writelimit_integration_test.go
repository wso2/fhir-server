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
	"errors"
	"testing"

	"github.com/wso2/fhir-server/internal/store"
	"github.com/wso2/fhir-server/internal/testutil"
)

// TestWriteLimit_RejectsOversizedWrite verifies the per-transaction row cap: a
// write that would buffer more index rows than WriteTuning.MaxRowsPerBundle is
// rejected with a WriteLimitError and rolled back — nothing is persisted. This is
// the guard that keeps a pathological bundle from driving the database out of
// memory; it fails the request (413) instead of the cluster.
func TestWriteLimit_RejectsOversizedWrite(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	// A deliberately tiny per-bundle cap; a normal Patient extracts well more than
	// 5 sp_* rows (identifier token, name/family/given strings, birthDate, telecom).
	s := store.New(pool, reg, store.WithWriteTuning(store.WriteTuning{
		MaxRowsPerStatement: 1000,
		MaxRowsPerBundle:    5,
	}))
	ctx := context.Background()

	// Single create → WriteLimitError, and the resources row is rolled back.
	_, err := s.Create(ctx, "Patient", patientBody("wl-pat", "Overflow", "Olivia", "1970-01-01"))
	if err == nil {
		t.Fatal("expected WriteLimitError for an oversized create, got nil")
	}
	var wl store.WriteLimitError
	if !errors.As(err, &wl) {
		t.Fatalf("expected store.WriteLimitError, got %T: %v", err, err)
	}
	if wl.Limit != 5 {
		t.Errorf("WriteLimitError.Limit = %d, want 5", wl.Limit)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM resources WHERE fhir_id = 'wl-pat'`).Scan(&n); err != nil {
		t.Fatalf("count resources: %v", err)
	}
	if n != 0 {
		t.Errorf("resources rows for the rejected write = %d, want 0 (must roll back)", n)
	}

	// A transaction bundle over the cap fails atomically with HTTP 413.
	entries := []store.BundleEntryRequest{{
		Method:   "POST",
		URL:      "Patient",
		Resource: patientBody("wl-bundle-pat", "BundleOverflow", "Bob", "1971-02-02"),
	}}
	_, berr := s.ExecuteBundle(ctx, "transaction", "", entries)
	if berr == nil {
		t.Fatal("expected a bundle error for an oversized transaction, got nil")
	}
	var be *store.BundleError
	if !errors.As(berr, &be) {
		t.Fatalf("expected *store.BundleError, got %T: %v", berr, berr)
	}
	if be.HTTPStatus != 413 {
		t.Errorf("bundle error status = %d, want 413", be.HTTPStatus)
	}

	if err := pool.QueryRow(ctx, `SELECT count(*) FROM resources WHERE fhir_id = 'wl-bundle-pat'`).Scan(&n); err != nil {
		t.Fatalf("count bundle resources: %v", err)
	}
	if n != 0 {
		t.Errorf("resources rows for the rejected bundle = %d, want 0 (must roll back)", n)
	}
}

// TestWriteLimit_DefaultAllowsNormalWrites is a guardrail: the default cap
// (100k rows) is far above any ordinary resource, so normal writes are unaffected.
func TestWriteLimit_DefaultAllowsNormalWrites(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	s := store.New(pool, reg) // default WriteTuning
	ctx := context.Background()

	if _, err := s.Create(ctx, "Patient", patientBody("wl-ok", "Normal", "Nora", "1980-01-01")); err != nil {
		t.Fatalf("normal create under default cap should succeed: %v", err)
	}
}
