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
	"fmt"
	"testing"

	"github.com/wso2/fhir-server/internal/db"
	"github.com/wso2/fhir-server/internal/store"
	"github.com/wso2/fhir-server/internal/tenant"
	"github.com/wso2/fhir-server/internal/testutil"
)

// appRoleStore returns a Store backed by a NON-superuser role. Tenant isolation
// is enforced by PostgreSQL Row-Level Security, which superusers (and the
// default testcontainers role) bypass — so a faithful isolation test must
// connect as an ordinary role, exactly as a production deployment should.
func appRoleStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	admin := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, admin)

	for _, stmt := range []string{
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'fhir_app') THEN
				CREATE ROLE fhir_app LOGIN PASSWORD 'app';
			END IF;
		END $$;`,
		`GRANT USAGE ON SCHEMA public TO fhir_app`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO fhir_app`,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO fhir_app`,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("provision app role (%q): %v", stmt, err)
		}
	}

	cc := admin.Config().ConnConfig
	dsn := fmt.Sprintf("postgres://fhir_app:app@%s:%d/%s?sslmode=disable", cc.Host, cc.Port, cc.Database)
	appPool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as app role: %v", err)
	}
	t.Cleanup(appPool.Close)
	return store.New(appPool, reg)
}

// TestTenantIsolation verifies the Option 2 logical-separation guarantees: two
// tenants can hold resources with the same id, neither can read the other's
// data, and searches are scoped to the calling tenant.
func TestTenantIsolation(t *testing.T) {
	s := appRoleStore(t)

	ctxA := tenant.WithTenant(context.Background(), "tenant-a")
	ctxB := tenant.WithTenant(context.Background(), "tenant-b")

	// Both tenants create a Patient with the SAME client-assigned id.
	const id = "shared-id-1"
	mkPatient := func(family string) map[string]any {
		return map[string]any{
			"resourceType": "Patient",
			"id":           id,
			"name":         []any{map[string]any{"family": family}},
		}
	}
	if _, err := s.Create(ctxA, "Patient", mkPatient("Anderson")); err != nil {
		t.Fatalf("tenant-a create: %v", err)
	}
	if _, err := s.Create(ctxB, "Patient", mkPatient("Becker")); err != nil {
		t.Fatalf("tenant-b create (same id must be allowed across tenants): %v", err)
	}

	// Each tenant reads back its OWN resource.
	a, err := s.Read(ctxA, "Patient", id)
	if err != nil {
		t.Fatalf("tenant-a read: %v", err)
	}
	if got := familyName(a); got != "Anderson" {
		t.Fatalf("tenant-a read returned family %q, want Anderson", got)
	}
	b, err := s.Read(ctxB, "Patient", id)
	if err != nil {
		t.Fatalf("tenant-b read: %v", err)
	}
	if got := familyName(b); got != "Becker" {
		t.Fatalf("tenant-b read returned family %q, want Becker", got)
	}

	// A third tenant sees nothing.
	ctxC := tenant.WithTenant(context.Background(), "tenant-c")
	if _, err := s.Read(ctxC, "Patient", id); !errors.As(err, &store.NotFoundError{}) {
		t.Fatalf("tenant-c read: want NotFoundError, got %v", err)
	}

	// Search is tenant-scoped: tenant-a matches only its own Patient.
	res, err := s.Search(ctxA, store.SearchParams{
		ResourceType: "Patient",
		Params:       map[string][]string{"family": {"Anderson"}},
	})
	if err != nil {
		t.Fatalf("tenant-a search: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("tenant-a search total = %d, want 1", res.Total)
	}
	// tenant-b must not see tenant-a's "Anderson".
	resB, err := s.Search(ctxB, store.SearchParams{
		ResourceType: "Patient",
		Params:       map[string][]string{"family": {"Anderson"}},
	})
	if err != nil {
		t.Fatalf("tenant-b search: %v", err)
	}
	if resB.Total != 0 {
		t.Fatalf("tenant-b search for tenant-a data total = %d, want 0 (cross-tenant leak)", resB.Total)
	}

	// Deleting in tenant-a must not affect tenant-b's identically-keyed resource.
	if err := s.Delete(ctxA, "Patient", id); err != nil {
		t.Fatalf("tenant-a delete: %v", err)
	}
	if _, err := s.Read(ctxA, "Patient", id); !errors.As(err, &store.GoneError{}) {
		t.Fatalf("tenant-a read after delete: want GoneError, got %v", err)
	}
	if _, err := s.Read(ctxB, "Patient", id); err != nil {
		t.Fatalf("tenant-b read after tenant-a delete must still succeed: %v", err)
	}
}

func familyName(resource map[string]any) string {
	names, _ := resource["name"].([]any)
	if len(names) == 0 {
		return ""
	}
	n, _ := names[0].(map[string]any)
	fam, _ := n["family"].(string)
	return fam
}
