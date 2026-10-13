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

//go:build integration

package searchparam_test

import (
	"context"
	"sync"
	"testing"

	"github.com/wso2/fhir-server/internal/searchparam"
	"github.com/wso2/fhir-server/internal/testutil"
)

// A write that commits and calls Upsert between a reload's query and its swap
// must survive the swap.
func TestRegistryReload_ConcurrentUpsertSurvives(t *testing.T) {
	ctx := context.Background()
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)

	calls := 0
	reg.SetAfterQuery(func() {
		calls++
		if calls > 1 {
			return
		}
		insertCustomParam(t, ctx, pool, "raced-upsert")
		reg.Upsert(searchparam.Definition{
			ResourceType: "Patient", ParamName: "raced-upsert", ParamType: "string",
			FHIRPath: "Patient.name.given", IsCustom: true,
		})
	})

	if err := reg.ReloadForTest(ctx, pool); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reg.Lookup("Patient", "raced-upsert"); !ok {
		t.Error("Upsert made during reload was lost")
	}
	if calls != 2 {
		t.Errorf("want 2 attempts, got %d", calls)
	}
}

// A delete that commits and calls Remove between a reload's query and its swap
// must not be resurrected by the older snapshot.
func TestRegistryReload_ConcurrentRemoveSurvives(t *testing.T) {
	ctx := context.Background()
	pool := testutil.MustSeededDB(t)
	insertCustomParam(t, ctx, pool, "raced-remove")
	reg := testutil.MustRegistry(t, pool)
	if _, ok := reg.Lookup("Patient", "raced-remove"); !ok {
		t.Fatal("precondition: param should be loaded")
	}

	calls := 0
	reg.SetAfterQuery(func() {
		calls++
		if calls > 1 {
			return
		}
		if _, err := pool.Exec(ctx, `DELETE FROM search_param_definitions WHERE param_name = 'raced-remove'`); err != nil {
			t.Errorf("delete: %v", err)
		}
		reg.Remove("Patient", "raced-remove")
	})

	if err := reg.ReloadForTest(ctx, pool); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reg.Lookup("Patient", "raced-remove"); ok {
		t.Error("Remove made during reload was undone")
	}
}

// If a mutation lands on every attempt the reload gives up with an error and
// keeps the previous snapshot.
func TestRegistryReload_GivesUpAfterBoundedAttempts(t *testing.T) {
	ctx := context.Background()
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	insertCustomParam(t, ctx, pool, "never-published")

	calls := 0
	reg.SetAfterQuery(func() {
		calls++
		reg.Upsert(searchparam.Definition{ResourceType: "Patient", ParamName: "noise", ParamType: "string", FHIRPath: "Patient.name"})
	})

	if err := reg.ReloadForTest(ctx, pool); err == nil {
		t.Fatal("expected an error after repeated interference")
	}
	if calls != 3 {
		t.Errorf("want 3 attempts, got %d", calls)
	}
	if _, ok := reg.Lookup("Patient", "never-published"); ok {
		t.Error("a discarded snapshot was published")
	}
}

// Concurrent Upserts and reloads must be race-free and leave a persisted
// definition visible.
func TestRegistryReload_StressWithUpserts(t *testing.T) {
	ctx := context.Background()
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	// Persist first, as a real write does, so a reload from the DB includes it.
	insertCustomParam(t, ctx, pool, "stress")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			reg.Upsert(searchparam.Definition{ResourceType: "Patient", ParamName: "stress", ParamType: "string", FHIRPath: "Patient.name.given", IsCustom: true})
		}
	}()
	for i := 0; i < 20; i++ {
		_ = reg.ReloadForTest(ctx, pool) // may give up under contention; that is allowed
	}
	wg.Wait()
	if _, ok := reg.Lookup("Patient", "stress"); !ok {
		t.Error("completed Upsert not visible")
	}
}
