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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wso2/fhir-server/internal/store"
)

func TestBundle_RoutesAndShapesTransactionResponse(t *testing.T) {
	ms := &mockStore{
		executeBundleFn: func(_ context.Context, bundleType, baseURL string, entries []store.BundleEntryRequest) ([]store.BundleEntryResult, error) {
			if bundleType != "transaction" {
				t.Errorf("bundleType = %q, want transaction", bundleType)
			}
			if len(entries) != 1 || entries[0].Method != "POST" {
				t.Errorf("entries not parsed: %+v", entries)
			}
			return []store.BundleEntryResult{{
				Status:   "201 Created",
				Location: "Patient/123/_history/1",
				ETag:     `W/"1"`,
				Resource: map[string]any{"resourceType": "Patient", "id": "123"},
			}}, nil
		},
	}
	h := newRouter(ms)

	resp := do(t, h, http.MethodPost, "/fhir/r4", map[string]any{
		"resourceType": "Bundle",
		"type":         "transaction",
		"entry": []any{map[string]any{
			"resource": map[string]any{"resourceType": "Patient"},
			"request":  map[string]any{"method": "POST", "url": "Patient"},
		}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	body := decodeJSON(t, resp)
	if body["type"] != "transaction-response" {
		t.Errorf("type = %v, want transaction-response", body["type"])
	}
	entries, _ := body["entry"].([]any)
	if len(entries) != 1 {
		t.Fatalf("want 1 response entry, got %d", len(entries))
	}
	respObj := entries[0].(map[string]any)["response"].(map[string]any)
	if respObj["status"] != "201 Created" {
		t.Errorf("status = %v, want 201 Created", respObj["status"])
	}
	// Location must be made absolute under the server base.
	if respObj["location"] != "http://localhost:9090/fhir/r4/Patient/123/_history/1" {
		t.Errorf("location = %v, want absolute", respObj["location"])
	}
}

func TestBundle_RejectsNonBundle(t *testing.T) {
	h := newRouter(&mockStore{})
	resp := do(t, h, http.MethodPost, "/fhir/r4", map[string]any{"resourceType": "Patient"})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.Code)
	}
}

func TestBundle_RejectsBadType(t *testing.T) {
	h := newRouter(&mockStore{})
	resp := do(t, h, http.MethodPost, "/fhir/r4", map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
	})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for non-transaction/batch type", resp.Code)
	}
}

func TestBundle_TransactionErrorMapsStatus(t *testing.T) {
	ms := &mockStore{
		executeBundleFn: func(_ context.Context, _, _ string, _ []store.BundleEntryRequest) ([]store.BundleEntryResult, error) {
			return nil, &store.BundleError{HTTPStatus: 404, Code: "not-found", EntryIndex: 0, Diagnostics: "Patient/x not found"}
		},
	}
	h := newRouter(ms)
	resp := do(t, h, http.MethodPost, "/fhir/r4", map[string]any{
		"resourceType": "Bundle",
		"type":         "transaction",
		"entry": []any{map[string]any{
			"request": map[string]any{"method": "DELETE", "url": "Patient/x"},
		}},
	})
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.Code)
	}
	body := decodeJSON(t, resp)
	if body["resourceType"] != "OperationOutcome" {
		t.Errorf("want OperationOutcome, got %v", body["resourceType"])
	}
}

func TestBundle_TrailingSlashAlsoRoutes(t *testing.T) {
	called := false
	ms := &mockStore{
		executeBundleFn: func(_ context.Context, _, _ string, _ []store.BundleEntryRequest) ([]store.BundleEntryResult, error) {
			called = true
			return []store.BundleEntryResult{}, nil
		},
	}
	h := newRouter(ms)
	resp := do(t, h, http.MethodPost, "/fhir/r4/", map[string]any{
		"resourceType": "Bundle", "type": "batch",
	})
	if resp.Code != http.StatusOK || !called {
		t.Fatalf("trailing-slash POST did not route to bundle handler: status=%d called=%v", resp.Code, called)
	}
}

// TestBundle_ProcessingLogicHeader verifies x-bundle-processing-logic plumbing:
// a recognised value (case-insensitive) reaches the store via the context, an
// unrecognised value or absent header leaves the context unset, so the store's
// configured default applies.
func TestBundle_ProcessingLogicHeader(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string // expected store.BundleProcessingFrom(ctx)
	}{
		{"parallel", "parallel", store.BundleProcessingParallel},
		{"case-insensitive", "PaRaLLeL", store.BundleProcessingParallel},
		{"sequential", "Sequential", store.BundleProcessingSequential},
		{"unrecognised ignored", "bogus", ""},
		{"absent", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			ms := &mockStore{
				executeBundleFn: func(ctx context.Context, _, _ string, _ []store.BundleEntryRequest) ([]store.BundleEntryResult, error) {
					got = store.BundleProcessingFrom(ctx)
					return nil, nil
				},
			}
			h := newRouter(ms)

			body, _ := json.Marshal(map[string]any{
				"resourceType": "Bundle",
				"type":         "transaction",
				"entry": []any{map[string]any{
					"resource": map[string]any{"resourceType": "Patient"},
					"request":  map[string]any{"method": "POST", "url": "Patient"},
				}},
			})
			req := httptest.NewRequest(http.MethodPost, "/fhir/r4", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/fhir+json")
			if tc.header != "" {
				req.Header.Set("x-bundle-processing-logic", tc.header)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
			}
			if got != tc.want {
				t.Errorf("store saw processing logic %q, want %q", got, tc.want)
			}
		})
	}
}
