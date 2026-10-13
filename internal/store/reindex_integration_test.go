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
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2/fhir-server/internal/searchparam"
	"github.com/wso2/fhir-server/internal/tenant"
	"github.com/wso2/fhir-server/internal/testutil"
)

func reindexTestStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	admin := testutil.MustSeededDB(t)
	for _, sql := range []string{
		`CREATE ROLE reindex_app LOGIN PASSWORD 'test' NOSUPERUSER NOBYPASSRLS`,
		`GRANT USAGE ON SCHEMA public TO reindex_app`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO reindex_app`,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO reindex_app`,
	} {
		if _, err := admin.Exec(context.Background(), sql); err != nil {
			t.Fatal(err)
		}
	}
	cfg := admin.Config().Copy()
	cfg.ConnConfig.User = "reindex_app"
	cfg.ConnConfig.Password = "test"
	cfg.AfterConnect = nil
	app, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return New(app, testutil.MustRegistry(t, app)), admin
}

func reindexPatient(t *testing.T, s *Store, ctx context.Context, id, family string) map[string]any {
	t.Helper()
	body, err := s.Create(ctx, "Patient", map[string]any{
		"resourceType": "Patient", "id": id,
		"name": []any{map[string]any{"family": family}}, "gender": "female",
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func reindexDefinition(t *testing.T, admin *pgxpool.Pool, rt, name, kind, expression string) {
	t.Helper()
	_, err := admin.Exec(context.Background(), `INSERT INTO search_param_definitions
        (resource_type, param_name, param_type, fhirpath_expr, is_custom, ig_source)
        VALUES ($1,$2,$3,$4,TRUE,'user') ON CONFLICT (resource_type,param_name)
        DO UPDATE SET param_type=EXCLUDED.param_type, fhirpath_expr=EXCLUDED.fhirpath_expr`, rt, name, kind, expression)
	if err != nil {
		t.Fatal(err)
	}
}

func drainReindex(t *testing.T, s *Store) {
	t.Helper()
	for i := 0; i < 30; i++ {
		worked, err := s.processReindexBatch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			return
		}
	}
	t.Fatal("reindex queue did not drain")
}

func TestReindexBackfillsScopedResources(t *testing.T) {
	for _, scope := range []string{"Patient", ""} {
		t.Run("scope="+scope, func(t *testing.T) {
			s, admin := reindexTestStore(t)
			a := tenant.WithTenant(context.Background(), "hospital-a")
			b := tenant.WithTenant(context.Background(), "hospital-b")
			originals := make(map[string][]byte)
			for _, id := range []string{"a", "b", "c"} {
				resource := reindexPatient(t, s, a, id, "Smith")
				originals[id], _ = json.Marshal(resource)
			}
			reindexPatient(t, s, b, "a", "Smith")
			if _, err := s.Create(a, "Observation", map[string]any{"resourceType": "Observation", "id": "a", "status": "final"}); err != nil {
				t.Fatal(err)
			}
			reindexDefinition(t, admin, "Patient", "late-family", "string", "Patient.name.family")
			reindexDefinition(t, admin, "Observation", "late-status", "token", "Observation.status")
			job, err := s.StartReindex(a, ReindexOptions{ResourceType: scope, BatchSize: 2})
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != "queued" || job.BatchSize != 2 {
				t.Fatalf("job=%+v", job)
			}
			if _, err := s.StartReindex(a, ReindexOptions{}); err == nil {
				t.Fatal("expected active-job conflict")
			}
			if _, err := s.GetReindex(b, job.ID); err == nil {
				t.Fatal("another tenant read job")
			}
			drainReindex(t, s)
			job, err = s.GetReindex(a, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := int64(3)
			if scope == "" {
				want = 4
			}
			if job.Status != "completed" || job.ResourcesReindexed != want || job.StartTime == nil || job.EndTime == nil {
				t.Fatalf("job=%+v", job)
			}
			for _, id := range []string{"a", "b", "c"} {
				resource, err := s.Read(a, "Patient", id)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(resource)
				var original, current map[string]any
				_ = json.Unmarshal(originals[id], &original)
				_ = json.Unmarshal(raw, &current)
				om := original["meta"].(map[string]any)
				cm := current["meta"].(map[string]any)
				ot, _ := time.Parse(time.RFC3339Nano, om["lastUpdated"].(string))
				ct, _ := time.Parse(time.RFC3339Nano, cm["lastUpdated"].(string))
				if om["versionId"] != cm["versionId"] || !ot.Equal(ct) {
					t.Fatalf("metadata changed: %v -> %v", om, cm)
				}
				history, err := s.GetHistory(a, "Patient", id)
				if err != nil || len(history) != 1 {
					t.Fatalf("history=%v err=%v", history, err)
				}
			}
			var count int
			if err := admin.QueryRow(a, `SELECT count(*) FROM sp_string WHERE tenant_id='hospital-a' AND param_name='late-family'`).Scan(&count); err != nil || count != 3 {
				t.Fatalf("backfill count=%d err=%v", count, err)
			}
			if err := admin.QueryRow(b, `SELECT count(*) FROM sp_string WHERE tenant_id='hospital-b' AND param_name='late-family'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("foreign count=%d err=%v", count, err)
			}
			wantObs := 0
			if scope == "" {
				wantObs = 1
			}
			if err := admin.QueryRow(a, `SELECT count(*) FROM sp_token WHERE tenant_id='hospital-a' AND param_name='late-status'`).Scan(&count); err != nil || count != wantObs {
				t.Fatalf("other type count=%d err=%v", count, err)
			}
			if _, err := s.StartReindex(b, ReindexOptions{ResourceType: "Patient"}); err != nil {
				t.Fatal(err)
			}
			drainReindex(t, s)
			if err := admin.QueryRow(b, `SELECT count(*) FROM sp_string WHERE tenant_id='hospital-b' AND param_name='late-family'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("foreign job count=%d err=%v", count, err)
			}
		})
	}
}

func TestReindexReplacesChangedParameterAndIsIdempotent(t *testing.T) {
	s, admin := reindexTestStore(t)
	ctx := context.Background()
	reindexDefinition(t, admin, "Patient", "changing", "string", "Patient.name.family")
	if err := s.registry.Load(ctx, s.pool); err != nil {
		t.Fatal(err)
	}
	reindexPatient(t, s, ctx, "a", "Smith")
	reindexDefinition(t, admin, "Patient", "changing", "token", "Patient.gender")
	for i := 0; i < 2; i++ {
		if _, err := s.StartReindex(ctx, ReindexOptions{}); err != nil {
			t.Fatal(err)
		}
		drainReindex(t, s)
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM sp_string WHERE param_name='changing'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("stale string count=%d err=%v", n, err)
		}
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM sp_token WHERE param_name='changing' AND code='female'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("fresh token count=%d err=%v", n, err)
		}
	}
}

func TestReindexResumesCommittedCursor(t *testing.T) {
	s, _ := reindexTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		reindexPatient(t, s, ctx, id, "Smith")
	}
	job, err := s.StartReindex(ctx, ReindexOptions{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.processReindexBatch(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := s.pool.Config().Copy()
	s.pool.Close()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	resumed := New(pool, searchparam.NewRegistry())
	partial, err := resumed.GetReindex(ctx, job.ID)
	if err != nil || partial.Status != "running" || partial.ResourcesReindexed != 1 {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
	drainReindex(t, resumed)
	completed, err := resumed.GetReindex(ctx, job.ID)
	if err != nil || completed.Status != "completed" || completed.ResourcesReindexed != 3 {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestReindexFailsWhenDefinitionsChangeBeforeCompletion(t *testing.T) {
	s, admin := reindexTestStore(t)
	ctx := context.Background()
	reindexPatient(t, s, ctx, "a", "Smith")
	job, err := s.StartReindex(ctx, ReindexOptions{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.processReindexBatch(ctx); err != nil {
		t.Fatal(err)
	}
	reindexDefinition(t, admin, "Patient", "late", "string", "Patient.name.family")
	drainReindex(t, s)
	failed, err := s.GetReindex(ctx, job.ID)
	if err != nil || failed.Status != "failed" || failed.ResourcesReindexed != 1 || !strings.Contains(failed.Error, "definitions changed") {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	if _, err := s.StartReindex(ctx, ReindexOptions{}); err != nil {
		t.Fatal(err)
	}
	drainReindex(t, s)
}

func TestReindexFailureRetainsProgressAndIndexes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt bool
		limit   int
	}{
		{name: "invalid JSON", corrupt: true},
		{name: "row limit", limit: 1},
		{name: "SQL data exception"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, admin := reindexTestStore(t)
			ctx := context.Background()
			reindexPatient(t, s, ctx, "a", "Smith")
			reindexPatient(t, s, ctx, "b", "Jones")
			job, err := s.StartReindex(ctx, ReindexOptions{BatchSize: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.processReindexBatch(ctx); err != nil {
				t.Fatal(err)
			}
			var before int
			if err := admin.QueryRow(ctx, `SELECT count(*) FROM sp_string WHERE resource_id='b'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if tc.corrupt {
				if _, err := admin.Exec(ctx, `UPDATE resources SET resource_json='invalid' WHERE fhir_id='b'`); err != nil {
					t.Fatal(err)
				}
			} else if tc.limit > 0 {
				s.writeTuning.MaxRowsPerBundle = tc.limit
			} else {
				raw, err := json.Marshal(map[string]any{
					"resourceType": "Patient", "id": "b",
					"name": []any{map[string]any{"family": strings.Repeat("x", 513)}},
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := admin.Exec(ctx, `UPDATE resources SET resource_json=$1 WHERE fhir_id='b'`, string(raw)); err != nil {
					t.Fatal(err)
				}
			}
			drainReindex(t, s)
			failed, err := s.GetReindex(ctx, job.ID)
			if err != nil || failed.Status != "failed" || failed.ResourcesReindexed != 1 || failed.Error == "" {
				t.Fatalf("failed=%+v err=%v", failed, err)
			}
			var after int
			if err := admin.QueryRow(ctx, `SELECT count(*) FROM sp_string WHERE resource_id='b'`).Scan(&after); err != nil || before != after {
				t.Fatalf("indexes changed %d -> %d err=%v", before, after, err)
			}
		})
	}
}

func TestReindexWaitsForResourceAndExcludesOtherWorkers(t *testing.T) {
	s, admin := reindexTestStore(t)
	ctx := context.Background()
	reindexPatient(t, s, ctx, "a", "Smith")
	job, err := s.StartReindex(ctx, ReindexOptions{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE resources SET resource_json='{"resourceType":"Patient","id":"a","name":[{"family":"Changed"}]}' WHERE fhir_id='a'`); err != nil {
		t.Fatal(err)
	}
	batchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.processReindexBatch(batchCtx); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'SELECT resource_type, fhir_id, resource_json%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not wait for locked resource")
		}
		time.Sleep(10 * time.Millisecond)
	}
	worked, err := s.processReindexBatch(batchCtx)
	if err != nil || worked {
		t.Fatalf("other worker claimed job: worked=%v err=%v", worked, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	drainReindex(t, s)
	var n int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM sp_string WHERE resource_id='a' AND param_name='family' AND value_exact='Changed'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("latest body not indexed: n=%d err=%v", n, err)
	}
	complete, err := s.GetReindex(ctx, job.ID)
	if err != nil || complete.ResourcesReindexed != 1 {
		t.Fatalf("complete=%+v err=%v", complete, err)
	}
}

func TestReindexInterruptedBatchDoesNotAdvance(t *testing.T) {
	s, admin := reindexTestStore(t)
	ctx := context.Background()
	reindexPatient(t, s, ctx, "a", "Smith")
	job, err := s.StartReindex(ctx, ReindexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM resources WHERE fhir_id='a' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	batchCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := s.processReindexBatch(batchCtx); err == nil {
		t.Fatal("expected interrupted batch")
	}
	partial, err := s.GetReindex(ctx, job.ID)
	if err != nil || partial.ResourcesReindexed != 0 || partial.Status != "queued" {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	drainReindex(t, s)
	complete, err := s.GetReindex(ctx, job.ID)
	if err != nil || complete.Status != "completed" || complete.ResourcesReindexed != 1 {
		t.Fatalf("complete=%+v err=%v", complete, err)
	}
}

func TestReindexEmptyTenantAndDeletedResources(t *testing.T) {
	s, _ := reindexTestStore(t)
	ctx := context.Background()
	reindexPatient(t, s, ctx, "a", "Smith")
	if err := s.Delete(ctx, "Patient", "a"); err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{ctx, tenant.WithTenant(ctx, "empty")} {
		job, err := s.StartReindex(ctx, ReindexOptions{})
		if err != nil {
			t.Fatal(err)
		}
		drainReindex(t, s)
		complete, err := s.GetReindex(ctx, job.ID)
		if err != nil || complete.Status != "completed" || complete.ResourcesReindexed != 0 {
			t.Fatalf("complete=%+v err=%v", complete, err)
		}
	}
}

func TestReindexInvalidOptions(t *testing.T) {
	s := New(nil, searchparam.NewRegistry())
	for _, opts := range []ReindexOptions{{BatchSize: -1}, {BatchSize: 1001}, {ResourceType: "Patient?name=x"}} {
		_, err := s.StartReindex(context.Background(), opts)
		var invalid *InvalidParamError
		if !errors.As(err, &invalid) {
			t.Fatalf("opts=%+v err=%v", opts, err)
		}
	}
}
