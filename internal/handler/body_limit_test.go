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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// oversized is one byte past the server's 32 MiB request-body cap.
const oversized = (32 << 20) + 1

// fillReader streams n copies of b without materializing them, so oversized
// bodies cost a fixed buffer rather than tens of megabytes of test allocation.
type fillReader struct {
	b byte
	n int
}

func (f *fillReader) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, io.EOF
	}
	m := len(p)
	if m > f.n {
		m = f.n
	}
	for i := 0; i < m; i++ {
		p[i] = f.b
	}
	f.n -= m
	return m, nil
}

func fill(n int) io.Reader { return &fillReader{b: 'a', n: n} }

func sendRaw(t *testing.T, h http.Handler, method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestRequestBody_OversizedReturns413 covers every body-reading format: an
// oversized body must be rejected as 413, not read into memory in full.
func TestRequestBody_OversizedReturns413(t *testing.T) {
	h := newRouter(&mockStore{})

	cases := []struct {
		name, method, path, contentType string
		body                            io.Reader
	}{
		{
			name:        "json",
			method:      http.MethodPost,
			path:        "/fhir/r4/Patient",
			contentType: "application/fhir+json",
			// Valid JSON whose string value streams past the limit, so the
			// decoder hits MaxBytesReader rather than a syntax error.
			body: io.MultiReader(
				strings.NewReader(`{"resourceType":"Patient","name":[{"family":"`),
				fill(oversized),
				strings.NewReader(`"}]}`),
			),
		},
		{
			name:        "xml",
			method:      http.MethodPost,
			path:        "/fhir/r4/Patient",
			contentType: "application/fhir+xml",
			body:        fill(oversized),
		},
		{
			name:        "turtle",
			method:      http.MethodPost,
			path:        "/fhir/r4/Patient",
			contentType: "application/fhir+turtle",
			body:        fill(oversized),
		},
		{
			name:        "xml-patch",
			method:      http.MethodPatch,
			path:        "/fhir/r4/Patient/p1",
			contentType: "application/xml-patch+xml",
			body:        fill(oversized),
		},
		{
			name:        "bundle-json",
			method:      http.MethodPost,
			path:        "/fhir/r4",
			contentType: "application/fhir+json",
			body: io.MultiReader(
				strings.NewReader(`{"resourceType":"Bundle","type":"batch","entry":[{"request":{"method":"POST","url":"Patient"},"resource":{"resourceType":"Patient","name":[{"family":"`),
				fill(oversized),
				strings.NewReader(`"}]}}]}`),
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := sendRaw(t, h, tc.method, tc.path, tc.contentType, tc.body)
			if resp.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("%s: status = %d, want 413; body=%s", tc.name, resp.Code, resp.Body.String())
			}
		})
	}
}

// TestRequestBody_MalformedStays400 confirms non-oversized parse failures are
// still 400, not 413.
func TestRequestBody_MalformedStays400(t *testing.T) {
	h := newRouter(&mockStore{})
	resp := sendRaw(t, h, http.MethodPost, "/fhir/r4/Patient", "application/fhir+json",
		strings.NewReader(`{"resourceType":`)) // truncated JSON
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON: status = %d, want 400; body=%s", resp.Code, resp.Body.String())
	}
}
