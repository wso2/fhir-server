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
	"strings"
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

func TestBundle_RejectsEntryMissingRequiredField(t *testing.T) {
	called := false
	ms := &mockStore{
		executeBundleFn: func(_ context.Context, _, _ string, _ []store.BundleEntryRequest) ([]store.BundleEntryResult, error) {
			called = true
			return nil, nil
		},
	}
	h := newRouter(ms)
	resp := do(t, h, http.MethodPost, "/fhir/r4", map[string]any{
		"resourceType": "Bundle",
		"type":         "transaction",
		"entry": []any{map[string]any{
			"resource": map[string]any{"resourceType": "Observation", "status": "final"}, // missing code
			"request":  map[string]any{"method": "POST", "url": "Observation"},
		}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	if called {
		t.Error("ExecuteBundle must not run when an entry fails validation")
	}
}

func TestBundle_RejectsEntryResourceTypeMismatch(t *testing.T) {
	called := false
	ms := &mockStore{
		executeBundleFn: func(_ context.Context, _, _ string, _ []store.BundleEntryRequest) ([]store.BundleEntryResult, error) {
			called = true
			return nil, nil
		},
	}
	h := newRouter(ms)
	resp := do(t, h, http.MethodPost, "/fhir/r4", map[string]any{
		"resourceType": "Bundle",
		"type":         "transaction",
		"entry": []any{map[string]any{
			"resource": map[string]any{"resourceType": "Observation", "status": "final", "code": map[string]any{"text": "x"}},
			"request":  map[string]any{"method": "POST", "url": "Patient"}, // URL says Patient
		}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	if called {
		t.Error("ExecuteBundle must not run when an entry's resourceType disagrees with its URL")
	}
}

func TestBundle_BatchInvalidEntryYieldsPerEntryOutcome(t *testing.T) {
	gotEntries := -1
	ms := &mockStore{
		executeBundleFn: func(_ context.Context, bundleType, _ string, entries []store.BundleEntryRequest) ([]store.BundleEntryResult, error) {
			if bundleType != "batch" {
				t.Errorf("bundleType = %q, want batch", bundleType)
			}
			gotEntries = len(entries)
			res := make([]store.BundleEntryResult, len(entries))
			for i := range entries {
				res[i] = store.BundleEntryResult{Status: "201 Created", Resource: map[string]any{"resourceType": "Patient", "id": "p1"}}
			}
			return res, nil
		},
	}
	h := newRouter(ms)
	resp := do(t, h, http.MethodPost, "/fhir/r4", map[string]any{
		"resourceType": "Bundle",
		"type":         "batch",
		"entry": []any{
			map[string]any{ // invalid: Observation missing the required code
				"resource": map[string]any{"resourceType": "Observation", "status": "final"},
				"request":  map[string]any{"method": "POST", "url": "Observation"},
			},
			map[string]any{ // valid Patient
				"resource": map[string]any{"resourceType": "Patient", "name": []any{map[string]any{"family": "OK"}}},
				"request":  map[string]any{"method": "POST", "url": "Patient"},
			},
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("batch status = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	if gotEntries != 1 {
		t.Errorf("ExecuteBundle should receive only the 1 valid entry, got %d", gotEntries)
	}

	body := decodeJSON(t, resp)
	if body["type"] != "batch-response" {
		t.Errorf("type = %v, want batch-response", body["type"])
	}
	entries, _ := body["entry"].([]any)
	if len(entries) != 2 {
		t.Fatalf("want a response entry per request entry (2), got %d", len(entries))
	}

	// entry[0]: the invalid entry → 4xx with an OperationOutcome, and it did NOT execute.
	resp0 := entries[0].(map[string]any)["response"].(map[string]any)
	if s, _ := resp0["status"].(string); !strings.HasPrefix(s, "422") {
		t.Errorf("entry[0] status = %v, want 422", resp0["status"])
	}
	if _, ok := resp0["outcome"]; !ok {
		t.Errorf("entry[0] must carry an OperationOutcome, got %v", resp0)
	}

	// entry[1]: the valid entry → executed normally.
	resp1 := entries[1].(map[string]any)["response"].(map[string]any)
	if resp1["status"] != "201 Created" {
		t.Errorf("entry[1] status = %v, want 201 Created", resp1["status"])
	}
}

func TestBundle_AcceptsValidEntries(t *testing.T) {
	called := false
	ms := &mockStore{
		executeBundleFn: func(_ context.Context, _, _ string, entries []store.BundleEntryRequest) ([]store.BundleEntryResult, error) {
			called = true
			return make([]store.BundleEntryResult, len(entries)), nil
		},
	}
	h := newRouter(ms)
	resp := do(t, h, http.MethodPost, "/fhir/r4", map[string]any{
		"resourceType": "Bundle",
		"type":         "transaction",
		"entry": []any{
			map[string]any{
				"resource": map[string]any{"resourceType": "Patient", "name": []any{map[string]any{"family": "Smith"}}},
				"request":  map[string]any{"method": "POST", "url": "Patient"},
			},
			map[string]any{
				"resource": map[string]any{"resourceType": "Observation", "status": "final", "code": map[string]any{"text": "x"}},
				"request":  map[string]any{"method": "POST", "url": "Observation"},
			},
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("valid bundle status = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	if !called {
		t.Error("a fully valid bundle must reach ExecuteBundle")
	}
}

func TestBundle_EmptyResourceTypeNormalizedToURLType(t *testing.T) {
	// An entry whose resourceType is an empty string must be normalized to the
	// URL-derived type before validation/execution, so it is base-validated like
	// any other entry rather than slipping through unchecked.
	for _, bundleType := range []string{"transaction", "batch"} {
		t.Run(bundleType, func(t *testing.T) {
			var gotRT string
			ms := &mockStore{
				executeBundleFn: func(_ context.Context, _, _ string, entries []store.BundleEntryRequest) ([]store.BundleEntryResult, error) {
					if len(entries) == 1 {
						gotRT, _ = entries[0].Resource["resourceType"].(string)
					}
					return make([]store.BundleEntryResult, len(entries)), nil
				},
			}
			h := newRouter(ms)
			resp := do(t, h, http.MethodPost, "/fhir/r4", map[string]any{
				"resourceType": "Bundle",
				"type":         bundleType,
				"entry": []any{map[string]any{
					"resource": map[string]any{"resourceType": "", "name": []any{map[string]any{"family": "X"}}},
					"request":  map[string]any{"method": "POST", "url": "Patient"},
				}},
			})
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", resp.Code, resp.Body.String())
			}
			if gotRT != "Patient" {
				t.Errorf("empty resourceType should be normalized to %q before execution, got %q", "Patient", gotRT)
			}
		})
	}
}

func TestBundle_BatchResultCountMismatchIsDiagnosable(t *testing.T) {
	// One invalid + one valid entry so the batch merge indexes into results;
	// the store returns fewer results than executed entries with a nil error.
	// The guard must turn that into a clean 500 OperationOutcome, not a panic.
	ms := &mockStore{
		executeBundleFn: func(_ context.Context, _, _ string, _ []store.BundleEntryRequest) ([]store.BundleEntryResult, error) {
			return nil, nil // 0 results for 1 executed entry
		},
	}
	h := newRouter(ms)
	resp := do(t, h, http.MethodPost, "/fhir/r4", map[string]any{
		"resourceType": "Bundle",
		"type":         "batch",
		"entry": []any{
			map[string]any{ // invalid: Observation missing the required code
				"resource": map[string]any{"resourceType": "Observation", "status": "final"},
				"request":  map[string]any{"method": "POST", "url": "Observation"},
			},
			map[string]any{ // valid Patient — the one executed entry
				"resource": map[string]any{"resourceType": "Patient", "name": []any{map[string]any{"family": "OK"}}},
				"request":  map[string]any{"method": "POST", "url": "Patient"},
			},
		},
	})
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", resp.Code, resp.Body.String())
	}
	body := decodeJSON(t, resp)
	if body["resourceType"] != "OperationOutcome" {
		t.Errorf("want a diagnosable OperationOutcome, got %v", body["resourceType"])
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
