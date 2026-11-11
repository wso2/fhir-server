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

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wso2/fhir-server/internal/handler"
	"github.com/wso2/fhir-server/internal/store"
	"github.com/wso2/fhir-server/internal/testutil"
)

func TestIntegration_ReindexHTTPBackfillsTenant(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	registry := testutil.MustRegistry(t, pool)
	s := store.New(pool, registry)
	var ready atomic.Int32
	ready.Store(1)
	srv := httptest.NewServer(handler.NewRouter(s, pool, registry, "http://test-server/fhir/r4", &ready, handler.Options{DisableBaseValidation: true}))
	t.Cleanup(srv.Close)
	base := "/t/hospital-a/fhir/r4"
	otherBase := "/t/hospital-b/fhir/r4"
	var original map[string]any
	var patientID string
	for _, tenantBase := range []string{base, base, otherBase} {
		resp := iDo(t, srv, http.MethodPost, tenantBase+"/Patient", map[string]any{"resourceType": "Patient", "name": []any{map[string]any{"family": "Before"}}})
		body := iJSON(t, resp)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create Patient: %d: %v", resp.StatusCode, body)
		}
		if original == nil {
			original = body
			patientID = body["id"].(string)
		}
	}
	resp := iDo(t, srv, http.MethodPost, base+"/SearchParameter", map[string]any{
		"resourceType": "SearchParameter", "url": "http://example.com/SearchParameter/reindex-family", "name": "ReindexFamily", "status": "active", "code": "reindex-family", "base": []any{"Patient"}, "type": "string", "expression": "Patient.name.family",
	})
	body := iJSON(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create SearchParameter: %d: %v", resp.StatusCode, body)
	}
	assertReindexMatches(t, srv, base, 0)
	resp = iDo(t, srv, http.MethodPost, base+"/$reindex", map[string]any{"resourceType": "Parameters", "parameter": []any{map[string]any{"name": "resourceType", "valueCode": "Patient"}, map[string]any{"name": "batchSize", "valueInteger": 1}}})
	body = iJSON(t, resp)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start reindex: %d: %v", resp.StatusCode, body)
	}
	values := reindexValues(t, body)
	if values["status"]["valueString"] != "queued" {
		t.Fatalf("initial state: %v", values)
	}
	statusPath := base + "/_operations/reindex/" + values["id"]["valueString"].(string)
	if resp.Header.Get("Location") != "http://test-server"+statusPath || resp.Header.Get("Content-Location") != resp.Header.Get("Location") {
		t.Fatalf("headers: %v", resp.Header)
	}
	resp = iDo(t, srv, http.MethodPost, base+"/$reindex", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second job: %d: %v", resp.StatusCode, iJSON(t, resp))
	}
	resp.Body.Close()
	resp = iDo(t, srv, http.MethodGet, strings.Replace(statusPath, base, otherBase, 1), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other tenant job: %d: %v", resp.StatusCode, iJSON(t, resp))
	}
	resp.Body.Close()
	workerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.RunReindexJobs(workerCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp = iDo(t, srv, http.MethodGet, statusPath, nil)
		body = iJSON(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("poll reindex: %d: %v", resp.StatusCode, body)
		}
		values = reindexValues(t, body)
		status := values["status"]["valueString"]
		if status == "completed" {
			break
		}
		if status == "failed" || time.Now().After(deadline) {
			t.Fatalf("reindex did not complete: %v", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if values["resourcesReindexed"]["valueInteger"] != float64(2) || values["startTime"] == nil || values["endTime"] == nil {
		t.Fatalf("completed status: %v", body)
	}
	assertReindexMatches(t, srv, base, 2)
	assertReindexMatches(t, srv, otherBase, 0)
	resp = iDo(t, srv, http.MethodGet, base+"/Patient/"+patientID, nil)
	after := iJSON(t, resp)
	beforeMeta := original["meta"].(map[string]any)
	afterMeta := after["meta"].(map[string]any)
	beforeTime, beforeErr := time.Parse(time.RFC3339Nano, beforeMeta["lastUpdated"].(string))
	afterTime, afterErr := time.Parse(time.RFC3339Nano, afterMeta["lastUpdated"].(string))
	if beforeMeta["versionId"] != afterMeta["versionId"] || beforeErr != nil || afterErr != nil || !beforeTime.Equal(afterTime) {
		t.Fatalf("resource metadata changed: before=%v after=%v", beforeMeta, afterMeta)
	}
}

func assertReindexMatches(t *testing.T, srv *httptest.Server, base string, want int) {
	t.Helper()
	resp := iDo(t, srv, http.MethodGet, base+"/Patient?reindex-family=Before", nil)
	body := iJSON(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search: %d: %v", resp.StatusCode, body)
	}
	entries, _ := body["entry"].([]any)
	if len(entries) != want {
		t.Fatalf("%s matches = %d, want %d: %v", base, len(entries), want, body)
	}
}
