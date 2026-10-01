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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wso2/fhir-server/internal/store"
	"github.com/wso2/fhir-server/internal/tenant"
)

const reindexJobID = "be1e7718-8c65-4343-a7e2-cb227bc6ff28"

func TestReindexStart(t *testing.T) {
	for _, tc := range []struct {
		name, path, tenantID, location string
		body                           any
		want                           store.ReindexOptions
	}{
		{"whole default tenant", "/fhir/r4/$reindex", tenant.Default, "http://localhost:9090/fhir/r4", nil, store.ReindexOptions{}},
		{"whole explicit tenant", "/t/hospital-a/fhir/r4/$reindex", "hospital-a", "http://localhost:9090/t/hospital-a/fhir/r4", map[string]any{"resourceType": "Parameters"}, store.ReindexOptions{}},
		{"one resource type", "/t/hospital-a/fhir/r4/$reindex", "hospital-a", "http://localhost:9090/t/hospital-a/fhir/r4", map[string]any{"resourceType": "Parameters", "parameter": []any{map[string]any{"name": "resourceType", "valueCode": "Patient"}, map[string]any{"name": "batchSize", "valueInteger": 25}}}, store.ReindexOptions{ResourceType: "Patient", BatchSize: 25}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queued := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
			s := &mockStore{startReindexFn: func(ctx context.Context, options store.ReindexOptions) (store.ReindexJob, error) {
				if tenant.From(ctx) != tc.tenantID {
					t.Fatalf("tenant = %q", tenant.From(ctx))
				}
				if options != tc.want {
					t.Fatalf("options = %+v, want %+v", options, tc.want)
				}
				return store.ReindexJob{ID: reindexJobID, Status: "queued", ResourceType: options.ResourceType, BatchSize: 100, QueuedTime: queued, LastModified: queued}, nil
			}}
			rr := do(t, newRouter(s), http.MethodPost, tc.path, tc.body)
			if rr.Code != http.StatusAccepted {
				t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
			}
			wantURL := tc.location + "/_operations/reindex/" + reindexJobID
			if rr.Header().Get("Location") != wantURL || rr.Header().Get("Content-Location") != wantURL {
				t.Fatalf("headers = %v", rr.Header())
			}
			body := decodeJSON(t, rr)
			parameters := reindexValues(t, body)
			if body["resourceType"] != "Parameters" || parameters["id"]["valueString"] != reindexJobID || parameters["status"]["valueString"] != "queued" || parameters["queuedTime"]["valueInstant"] != queued.Format(time.RFC3339) {
				t.Fatalf("body = %v", body)
			}
		})
	}
}

func TestReindexRejectsInvalidBody(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"null", `null`},
		{"wrong resource", `{"resourceType":"Patient"}`},
		{"wrong parameter shape", `{"resourceType":"Parameters","parameter":{}}`},
		{"nonobject parameter", `{"resourceType":"Parameters","parameter":[1]}`},
		{"unknown parameter", `{"resourceType":"Parameters","parameter":[{"name":"url","valueString":"Patient?"}]}`},
		{"duplicate parameter", `{"resourceType":"Parameters","parameter":[{"name":"batchSize","valueInteger":5},{"name":"batchSize","valueInteger":10}]}`},
		{"wrong resource value", `{"resourceType":"Parameters","parameter":[{"name":"resourceType","valueString":"Patient"}]}`},
		{"unknown resource", `{"resourceType":"Parameters","parameter":[{"name":"resourceType","valueCode":"Unknown"}]}`},
		{"empty resource", `{"resourceType":"Parameters","parameter":[{"name":"resourceType","valueCode":""}]}`},
		{"abstract resource", `{"resourceType":"Parameters","parameter":[{"name":"resourceType","valueCode":"Resource"}]}`},
		{"zero batch", `{"resourceType":"Parameters","parameter":[{"name":"batchSize","valueInteger":0}]}`},
		{"too large batch", `{"resourceType":"Parameters","parameter":[{"name":"batchSize","valueInteger":1001}]}`},
		{"fractional batch", `{"resourceType":"Parameters","parameter":[{"name":"batchSize","valueInteger":1.5}]}`},
		{"string batch", `{"resourceType":"Parameters","parameter":[{"name":"batchSize","valueInteger":"10"}]}`},
		{"multiple values", `{"resourceType":"Parameters","parameter":[{"name":"batchSize","valueInteger":10,"valueCode":"Patient"}]}`},
		{"malformed JSON", `{`},
		{"trailing JSON", `{"resourceType":"Parameters"}{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mockStore{startReindexFn: func(context.Context, store.ReindexOptions) (store.ReindexJob, error) {
				t.Fatal("invalid request reached store")
				return store.ReindexJob{}, nil
			}}
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/fhir/r4/$reindex", strings.NewReader(tc.body))
			newRouter(s).ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
			}
			if decodeJSON(t, rr)["resourceType"] != "OperationOutcome" {
				t.Fatal("expected OperationOutcome")
			}
		})
	}
}

func TestReindexStartErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"active job", store.ConflictError{Message: "a reindex job is already active"}, http.StatusConflict},
		{"database error", errors.New("database unavailable"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mockStore{startReindexFn: func(context.Context, store.ReindexOptions) (store.ReindexJob, error) {
				return store.ReindexJob{}, tc.err
			}}
			rr := do(t, newRouter(s), http.MethodPost, "/fhir/r4/$reindex", nil)
			if rr.Code != tc.status {
				t.Fatalf("status = %d", rr.Code)
			}
		})
	}
}

func TestReindexStatusTenantIsolation(t *testing.T) {
	started := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	s := &mockStore{getReindexFn: func(ctx context.Context, id string) (store.ReindexJob, error) {
		if id != reindexJobID || tenant.From(ctx) != "hospital-a" {
			return store.ReindexJob{}, store.NotFoundError{ResourceType: "reindex", ResourceID: id}
		}
		return store.ReindexJob{ID: id, Status: "failed", ResourceType: "Patient", BatchSize: 25, ResourcesReindexed: 50, QueuedTime: started, LastModified: ended, StartTime: &started, EndTime: &ended, Error: "search parameter definitions changed; start a new reindex job"}, nil
	}}
	router := newRouter(s)
	rr := do(t, router, http.MethodGet, "/t/hospital-a/fhir/r4/_operations/reindex/"+reindexJobID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	values := reindexValues(t, decodeJSON(t, rr))
	if values["status"]["valueString"] != "failed" || values["resourcesReindexed"]["valueInteger"] != float64(50) || values["resourceType"]["valueCode"] != "Patient" || values["startTime"]["valueInstant"] != started.Format(time.RFC3339) || values["endTime"]["valueInstant"] != ended.Format(time.RFC3339) || values["error"]["valueString"] == nil {
		t.Fatalf("parameters = %v", values)
	}
	for _, path := range []string{"/t/hospital-b/fhir/r4/_operations/reindex/" + reindexJobID, "/fhir/r4/_operations/reindex/" + reindexJobID, "/t/hospital-a/fhir/r4/_operations/reindex/not-a-uuid"} {
		rr := do(t, router, http.MethodGet, path, nil)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d", path, rr.Code)
		}
	}
}

func reindexValues(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()
	parameters, ok := body["parameter"].([]any)
	if !ok {
		t.Fatalf("missing Parameters.parameter: %v", body)
	}
	values := make(map[string]map[string]any)
	for _, raw := range parameters {
		p := raw.(map[string]any)
		values[p["name"].(string)] = p
	}
	return values
}

func TestReindexStructuredBody(t *testing.T) {
	s := &mockStore{startReindexFn: func(_ context.Context, options store.ReindexOptions) (store.ReindexJob, error) {
		if options != (store.ReindexOptions{ResourceType: "Patient", BatchSize: 25}) {
			t.Fatalf("options = %+v", options)
		}
		return store.ReindexJob{ID: reindexJobID, Status: "queued", BatchSize: 25}, nil
	}}
	req := httptest.NewRequest(http.MethodPost, "/fhir/r4/$reindex", strings.NewReader(`<Parameters xmlns="http://hl7.org/fhir"><parameter><name value="resourceType"/><valueCode value="Patient"/></parameter><parameter><name value="batchSize"/><valueInteger value="25"/></parameter></Parameters>`))
	req.Header.Set("Content-Type", "application/fhir+xml")
	rr := httptest.NewRecorder()
	newRouter(s).ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
}
