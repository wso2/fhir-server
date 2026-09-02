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

// TestRequestBody_RejectsTrailingJSON covers every JSON body path: content after
// the single JSON value (trailing garbage or a second value) must be rejected,
// not silently ignored.
func TestRequestBody_RejectsTrailingJSON(t *testing.T) {
	h := newRouter(&mockStore{})
	cases := []struct{ name, method, path, ct, body string }{
		{"create-garbage", http.MethodPost, "/fhir/r4/Patient", "application/fhir+json",
			`{"resourceType":"Patient","name":[{"family":"X"}]} GARBAGE`},
		{"create-second-value", http.MethodPost, "/fhir/r4/Patient", "application/fhir+json",
			`{"resourceType":"Patient"}{"resourceType":"Patient"}`},
		{"bundle-trailing", http.MethodPost, "/fhir/r4", "application/fhir+json",
			`{"resourceType":"Bundle","type":"batch"} trailing`},
		{"json-patch-trailing", http.MethodPatch, "/fhir/r4/Patient/p1", "application/json-patch+json",
			`[{"op":"remove","path":"/x"}] extra`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := sendRaw(t, h, tc.method, tc.path, tc.ct, strings.NewReader(tc.body))
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d, want 400; body=%s", tc.name, resp.Code, resp.Body.String())
			}
		})
	}
}

// TestRequestBody_AcceptsTrailingWhitespace ensures the strict single-value check
// does not reject an ordinary trailing newline/whitespace, which real clients send.
func TestRequestBody_AcceptsTrailingWhitespace(t *testing.T) {
	called := false
	ms := &mockStore{
		executeBundleFn: func(_ context.Context, _, _ string, _ []store.BundleEntryRequest) ([]store.BundleEntryResult, error) {
			called = true
			return []store.BundleEntryResult{}, nil
		},
	}
	h := newRouter(ms)
	resp := sendRaw(t, h, http.MethodPost, "/fhir/r4", "application/fhir+json",
		strings.NewReader("{\"resourceType\":\"Bundle\",\"type\":\"batch\",\"entry\":[]}\n  \n"))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	if !called {
		t.Error("a valid bundle with a trailing newline should still execute")
	}
}
