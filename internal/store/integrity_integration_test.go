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
	"strings"
	"testing"

	"github.com/wso2/fhir-server/internal/store"
	"github.com/wso2/fhir-server/internal/testutil"
)

// newRIStore returns a store with both referential-integrity checks enabled —
// the production default wiring (config defaults both flags to on).
func newRIStore(t *testing.T) *store.Store {
	t.Helper()
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	return store.New(pool, reg, store.WithReferentialIntegrity(store.RefIntegrity{OnWrite: true, OnDelete: true}))
}

func patient(id string) map[string]any {
	return map[string]any{"resourceType": "Patient", "id": id}
}

func observation(id, subjectRef string) map[string]any {
	return map[string]any{
		"resourceType": "Observation",
		"id":           id,
		"status":       "final",
		"code":         map[string]any{"text": "test"},
		"subject":      map[string]any{"reference": subjectRef},
	}
}

func TestRefIntegrity_CreateDanglingRejected(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	_, err := s.Create(ctx, "Observation", observation("obs-1", "Patient/nope"))
	var ri store.ReferentialIntegrityError
	if !errors.As(err, &ri) {
		t.Fatalf("want ReferentialIntegrityError, got %v", err)
	}
	if !strings.Contains(err.Error(), "Patient/nope") || !strings.Contains(err.Error(), "Observation.subject") {
		t.Errorf("error should name the reference and its path: %v", err)
	}
	// The transaction must have rolled back: the observation does not exist.
	if _, err := s.Read(ctx, "Observation", "obs-1"); err == nil {
		t.Error("rejected create must not persist the resource")
	}
}

func TestRefIntegrity_CreateResolvedAccepted(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Patient", patient("p1")); err != nil {
		t.Fatalf("create patient: %v", err)
	}
	if _, err := s.Create(ctx, "Observation", observation("obs-1", "Patient/p1")); err != nil {
		t.Fatalf("create observation with resolvable reference: %v", err)
	}
}

func TestRefIntegrity_NonLocalReferencesSkipped(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	body := map[string]any{
		"resourceType": "Observation",
		"status":       "final",
		"code":         map[string]any{"text": "t"},
		"subject":      map[string]any{"reference": "https://other.example.org/fhir/Patient/remote"},
		"performer": []any{
			map[string]any{"reference": "#contained-practitioner"},
			map[string]any{"identifier": map[string]any{"system": "urn:mrn", "value": "42"}},
		},
	}
	if _, err := s.Create(ctx, "Observation", body); err != nil {
		t.Fatalf("absolute/fragment/logical references must not be existence-checked: %v", err)
	}
}

func TestRefIntegrity_UpdateToDanglingRejected(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Patient", patient("p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Observation", observation("obs-1", "Patient/p1")); err != nil {
		t.Fatal(err)
	}
	_, err := s.Update(ctx, "Observation", "obs-1", observation("obs-1", "Patient/gone"), -1)
	var ri store.ReferentialIntegrityError
	if !errors.As(err, &ri) {
		t.Fatalf("want ReferentialIntegrityError on update, got %v", err)
	}
}

func TestRefIntegrity_ReferenceToDeletedRejected(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Patient", patient("p1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "Patient", "p1"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Create(ctx, "Observation", observation("obs-1", "Patient/p1"))
	var ri store.ReferentialIntegrityError
	if !errors.As(err, &ri) {
		t.Fatalf("a soft-deleted target must not satisfy the check, got %v", err)
	}
}

func TestRefIntegrity_DeleteReferencedRejected(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Patient", patient("p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Observation", observation("obs-1", "Patient/p1")); err != nil {
		t.Fatal(err)
	}

	err := s.Delete(ctx, "Patient", "p1")
	var rb store.ReferencedByError
	if !errors.As(err, &rb) {
		t.Fatalf("want ReferencedByError, got %v", err)
	}
	if !strings.Contains(err.Error(), "Patient/p1") || !strings.Contains(err.Error(), "Observation/obs-1") {
		t.Errorf("error should name target and referrer: %v", err)
	}
	// Rollback: patient still readable.
	if _, err := s.Read(ctx, "Patient", "p1"); err != nil {
		t.Errorf("rejected delete must leave the resource live: %v", err)
	}

	// Deleting the referrer first unblocks the target.
	if err := s.Delete(ctx, "Observation", "obs-1"); err != nil {
		t.Fatalf("delete referrer: %v", err)
	}
	if err := s.Delete(ctx, "Patient", "p1"); err != nil {
		t.Fatalf("delete after referrer removed: %v", err)
	}
}

func TestRefIntegrity_DisabledKeepsLegacyBehavior(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	s := store.New(pool, reg) // zero-value RefIntegrity: both checks off
	ctx := context.Background()

	if _, err := s.Create(ctx, "Observation", observation("obs-1", "Patient/nope")); err != nil {
		t.Fatalf("dangling reference must be accepted when disabled: %v", err)
	}
	if _, err := s.Create(ctx, "Patient", patient("p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Observation", observation("obs-2", "Patient/p1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "Patient", "p1"); err != nil {
		t.Fatalf("delete of a referenced resource must succeed when disabled: %v", err)
	}
}

func TestRefIntegrity_TransactionBundleInternalRefs(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	// POST Patient + POST Observation referencing it via urn:uuid — the classic
	// ingest shape. The write-side check runs post-flush, so the in-bundle
	// target satisfies it.
	entries := []store.BundleEntryRequest{
		{FullURL: "urn:uuid:pat-1", Method: "POST", URL: "Patient", Resource: patient("")},
		{FullURL: "urn:uuid:obs-1", Method: "POST", URL: "Observation", Resource: observation("", "urn:uuid:pat-1")},
	}
	results, err := s.ExecuteBundle(ctx, "transaction", "http://localhost:9090/fhir/r4", entries)
	if err != nil {
		t.Fatalf("transaction with in-bundle references must pass: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
}

func TestRefIntegrity_TransactionBundleDanglingRollsBack(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	entries := []store.BundleEntryRequest{
		{FullURL: "urn:uuid:pat-1", Method: "POST", URL: "Patient", Resource: patient("tx-p1")},
		{FullURL: "urn:uuid:obs-1", Method: "POST", URL: "Observation", Resource: observation("", "Patient/absent")},
	}
	_, err := s.ExecuteBundle(ctx, "transaction", "http://localhost:9090/fhir/r4", entries)
	var be *store.BundleError
	if !errors.As(err, &be) {
		t.Fatalf("want *BundleError, got %v", err)
	}
	if be.HTTPStatus != 422 {
		t.Errorf("want 422, got %d (%s)", be.HTTPStatus, be.Diagnostics)
	}
	// Atomicity: the valid patient entry must have rolled back too.
	if _, err := s.Read(ctx, "Patient", "tx-p1"); err == nil {
		t.Error("failed transaction must roll back all entries")
	}
}

func TestRefIntegrity_TransactionDeleteWithRepointing(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Patient", patient("p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Patient", patient("p2")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Observation", observation("obs-1", "Patient/p1")); err != nil {
		t.Fatal(err)
	}

	// One transaction: delete p1 AND re-point its referrer at p2. FHIR verb
	// order runs the DELETE first; post-flush verification must still pass
	// because the final state is consistent.
	entries := []store.BundleEntryRequest{
		{Method: "DELETE", URL: "Patient/p1"},
		{Method: "PUT", URL: "Observation/obs-1", Resource: observation("obs-1", "Patient/p2")},
	}
	if _, err := s.ExecuteBundle(ctx, "transaction", "http://localhost:9090/fhir/r4", entries); err != nil {
		t.Fatalf("delete + re-point in one transaction must pass: %v", err)
	}

	// And the inverse: deleting a target while a referrer keeps pointing at it fails 409.
	if _, err := s.Create(ctx, "Patient", patient("p3")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Observation", observation("obs-2", "Patient/p3")); err != nil {
		t.Fatal(err)
	}
	_, err := s.ExecuteBundle(ctx, "transaction", "http://localhost:9090/fhir/r4", []store.BundleEntryRequest{
		{Method: "DELETE", URL: "Patient/p3"},
	})
	var be *store.BundleError
	if !errors.As(err, &be) || be.HTTPStatus != 409 {
		t.Fatalf("want *BundleError 409, got %v", err)
	}
}

func TestRefIntegrity_PatchToDanglingRejected(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Patient", patient("p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Observation", observation("obs-1", "Patient/p1")); err != nil {
		t.Fatal(err)
	}
	_, err := s.Patch(ctx, "Observation", "obs-1", map[string]any{
		"subject": map[string]any{"reference": "Patient/gone"},
	})
	var ri store.ReferentialIntegrityError
	if !errors.As(err, &ri) {
		t.Fatalf("want ReferentialIntegrityError on patch, got %v", err)
	}
	// Rollback: the stored observation still points at p1.
	got, err := s.Read(ctx, "Observation", "obs-1")
	if err != nil {
		t.Fatal(err)
	}
	if ref := got["subject"].(map[string]any)["reference"]; ref != "Patient/p1" {
		t.Errorf("rejected patch must roll back, subject is %v", ref)
	}
}

func TestRefIntegrity_BundleRewriteSameResourceFinalStateWins(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Patient", patient("p1")); err != nil {
		t.Fatal(err)
	}
	// Two PUTs to the same Observation in one transaction: the first version
	// carries a dangling reference, the final version a resolvable one. Only
	// the final state may be verified.
	entries := []store.BundleEntryRequest{
		{Method: "PUT", URL: "Observation/obs-1", Resource: observation("obs-1", "Patient/dangling")},
		{Method: "PUT", URL: "Observation/obs-1", Resource: observation("obs-1", "Patient/p1")},
	}
	if _, err := s.ExecuteBundle(ctx, "transaction", "http://localhost:9090/fhir/r4", entries); err != nil {
		t.Fatalf("only the final version's references must be verified: %v", err)
	}
}

func TestRefIntegrity_BundleDeleteThenResurrect(t *testing.T) {
	s := newRIStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, "Patient", patient("p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Observation", observation("obs-1", "Patient/p1")); err != nil {
		t.Fatal(err)
	}
	// DELETE + PUT of the same Patient in one transaction resurrects it (FHIR
	// verb order runs the DELETE first). The final state is alive, so the
	// delete-side check must not fire even though obs-1 still references it.
	entries := []store.BundleEntryRequest{
		{Method: "DELETE", URL: "Patient/p1"},
		{Method: "PUT", URL: "Patient/p1", Resource: patient("p1")},
	}
	if _, err := s.ExecuteBundle(ctx, "transaction", "http://localhost:9090/fhir/r4", entries); err != nil {
		t.Fatalf("delete-then-resurrect must not trip the delete-side check: %v", err)
	}
	if _, err := s.Read(ctx, "Patient", "p1"); err != nil {
		t.Errorf("patient must be alive after resurrection: %v", err)
	}
}

func TestRefIntegrity_OnWriteOnly_AllowsReferencedDelete(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	s := store.New(pool, reg, store.WithReferentialIntegrity(store.RefIntegrity{OnWrite: true, OnDelete: false}))
	ctx := context.Background()

	if _, err := s.Create(ctx, "Patient", patient("p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Observation", observation("obs-1", "Patient/p1")); err != nil {
		t.Fatal(err)
	}
	// The write-side check must still fire...
	if _, err := s.Create(ctx, "Observation", observation("obs-2", "Patient/nope")); err == nil {
		t.Error("OnWrite must still reject dangling references")
	}
	// ...but deleting a referenced resource must be permitted with OnDelete off.
	if err := s.Delete(ctx, "Patient", "p1"); err != nil {
		t.Fatalf("OnDelete=false must permit deleting a referenced resource: %v", err)
	}
}

func TestRefIntegrity_OnDeleteOnly_AllowsDanglingWrite(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	s := store.New(pool, reg, store.WithReferentialIntegrity(store.RefIntegrity{OnWrite: false, OnDelete: true}))
	ctx := context.Background()

	// Dangling references must be accepted with OnWrite off...
	if _, err := s.Create(ctx, "Observation", observation("obs-1", "Patient/nope")); err != nil {
		t.Fatalf("OnWrite=false must accept dangling references: %v", err)
	}
	// ...while the delete-side check still fires.
	if _, err := s.Create(ctx, "Patient", patient("p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Observation", observation("obs-2", "Patient/p1")); err != nil {
		t.Fatal(err)
	}
	err := s.Delete(ctx, "Patient", "p1")
	var rb store.ReferencedByError
	if !errors.As(err, &rb) {
		t.Fatalf("OnDelete must still reject deleting a referenced resource, got %v", err)
	}
}
