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

package ig

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/wso2/fhir-server/internal/searchparam"
	"github.com/wso2/fhir-server/internal/testutil"
)

// writeTestIG writes a package with n Patient search parameters and returns its path.
func writeTestIG(t *testing.T, n int) string {
	t.Helper()
	files := map[string][]byte{
		"package/package.json": mustJSON(map[string]any{
			"name": "test.ig", "version": "1.0.0", "fhirVersion": "4.0.1",
		}),
	}
	for i := 0; i < n; i++ {
		code := fmt.Sprintf("race-param-%d", i)
		files["package/SearchParameter-"+code+".json"] = mustJSON(map[string]any{
			"resourceType": "SearchParameter",
			"code":         code,
			"type":         "string",
			"base":         []string{"Patient"},
			"expression":   "Patient.name",
		})
	}
	path := filepath.Join(t.TempDir(), "test-ig.tgz")
	if err := os.WriteFile(path, buildTestTgz(t, files), 0o644); err != nil {
		t.Fatalf("write ig: %v", err)
	}
	return path
}

func countIGParams(r *searchparam.Registry) int {
	n := 0
	for _, d := range r.ForResource("Patient") {
		if d.IGSource != "" {
			n++
		}
	}
	return n
}

// Replicas starting together must each end up with every IG search parameter,
// whichever of them wins the insert race.
func TestLoadPackage_ConcurrentReplicasConvergeRegistry(t *testing.T) {
	const params, replicas = 20, 4
	ctx := context.Background()
	pool := testutil.MustSeededDB(t)
	spec := writeTestIG(t, params)

	registries := make([]*searchparam.Registry, replicas)
	for i := range registries {
		registries[i] = testutil.MustRegistry(t, pool)
	}

	var wg sync.WaitGroup
	errs := make([]error, replicas)
	results := make([]*PackageResult, replicas)
	for i := range registries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = LoadPackage(ctx, pool, registries[i], spec, LoadOptions{})
		}()
	}
	wg.Wait()

	loaders := 0
	for i := range registries {
		if errs[i] != nil {
			t.Fatalf("replica %d: %v", i, errs[i])
		}
		if !results[i].AlreadyLoaded {
			loaders++
		}
		if got := countIGParams(registries[i]); got != params {
			t.Errorf("replica %d registry has %d IG params, want %d", i, got, params)
		}
	}
	if loaders != 1 {
		t.Errorf("want exactly 1 replica to load the package, got %d", loaders)
	}
}

// A replica whose startup registry load predates another replica's IG load must
// catch up when it finds the package already loaded.
func TestLoadPackage_SkipPathRefreshesStaleRegistry(t *testing.T) {
	const params = 5
	ctx := context.Background()
	pool := testutil.MustSeededDB(t)
	spec := writeTestIG(t, params)

	stale := testutil.MustRegistry(t, pool)
	if _, err := LoadPackage(ctx, pool, testutil.MustRegistry(t, pool), spec, LoadOptions{}); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if got := countIGParams(stale); got != 0 {
		t.Fatalf("stale registry should have no IG params yet, got %d", got)
	}

	res, err := LoadPackage(ctx, pool, stale, spec, LoadOptions{})
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if !res.AlreadyLoaded {
		t.Fatal("expected AlreadyLoaded")
	}
	if got := countIGParams(stale); got != params {
		t.Errorf("registry has %d IG params after skip, want %d", got, params)
	}
}
