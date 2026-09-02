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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func convertReq(t *testing.T, h http.Handler, resource map[string]any, accept string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(resource)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/fhir/r4/$convert", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("Accept", accept)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func assertConvertRejected(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	// A rejected convert yields an OperationOutcome error, never a serialized
	// document — so the crafted markup never reaches the output stream.
	if w.Code == http.StatusOK {
		t.Fatalf("crafted name must not produce a 200 document; body=%s", w.Body.String())
	}
	var oo map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &oo); err != nil {
		t.Fatalf("expected an OperationOutcome JSON error, got unparseable body: %s", w.Body.String())
	}
	if oo["resourceType"] != "OperationOutcome" {
		t.Errorf("want OperationOutcome, got %v", oo["resourceType"])
	}
}

func TestConvert_RejectsInjectedElementNameXML(t *testing.T) {
	h := newRouter(&mockStore{})
	w := convertReq(t, h, map[string]any{"resourceType": "Patient", "injected><forged": "x"}, "application/fhir+xml")
	assertConvertRejected(t, w)
}

func TestConvert_RejectsInjectedResourceTypeTurtle(t *testing.T) {
	h := newRouter(&mockStore{})
	w := convertReq(t, h, map[string]any{
		"resourceType": `Patient . fhir:injected "pwned"`,
		"name":         []any{map[string]any{"family": "x"}},
	}, "application/fhir+turtle")
	assertConvertRejected(t, w)
}

func TestConvert_NormalResourceStillConverts(t *testing.T) {
	h := newRouter(&mockStore{})
	xw := convertReq(t, h, map[string]any{"resourceType": "Patient", "active": true}, "application/fhir+xml")
	if xw.Code != http.StatusOK || !strings.Contains(xw.Body.String(), "<Patient") {
		t.Errorf("normal XML convert failed: status=%d body=%s", xw.Code, xw.Body.String())
	}
	tw := convertReq(t, h, map[string]any{"resourceType": "Patient", "active": true}, "application/fhir+turtle")
	if tw.Code != http.StatusOK || !strings.Contains(tw.Body.String(), "a fhir:Patient") {
		t.Errorf("normal Turtle convert failed: status=%d body=%s", tw.Code, tw.Body.String())
	}
}
