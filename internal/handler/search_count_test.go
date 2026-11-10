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
	"testing"

	"github.com/wso2/fhir-server/internal/store"
)

func TestSearch_CountZeroIsCountOnly(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, form, wantSelf string
	}{
		{"GET _count=0", http.MethodGet, "/fhir/r4/Patient?name=Smith&_count=0", "", "_count=0"},
		{"POST _count=0", http.MethodPost, "/fhir/r4/Patient/_search", "name=Smith&_count=0", "_count=0"},
		{"GET _summary=count", http.MethodGet, "/fhir/r4/Patient?name=Smith&_summary=count", "", "_summary=count"},
	} {
		ms := &mockStore{}
		var countOnly bool
		ms.searchFn = func(_ context.Context, sp store.SearchParams) (store.SearchResult, error) {
			countOnly = sp.CountOnly
			if sp.CountOnly {
				return store.SearchResult{Total: 45}, nil
			}
			return store.SearchResult{Total: -1, Entries: []map[string]any{{"resourceType": "Patient", "id": "p1"}}}, nil
		}

		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.form))
		if tc.form != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		w := httptest.NewRecorder()
		newRouter(ms).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d: %s", tc.name, w.Code, w.Body.String())
		}
		body := decodeJSON(t, w)

		if !countOnly {
			t.Errorf("%s: store was not asked for a count-only search", tc.name)
		}
		if body["total"] != float64(45) {
			t.Errorf("%s: total = %v, want 45", tc.name, body["total"])
		}
		if entries, _ := body["entry"].([]any); len(entries) != 0 {
			t.Errorf("%s: want no entries, got %d", tc.name, len(entries))
		}
		links, _ := body["link"].([]any)
		if len(links) != 1 {
			t.Fatalf("%s: want only a self link, got %v", tc.name, links)
		}
		self, _ := links[0].(map[string]any)
		if self["relation"] != "self" || !strings.Contains(self["url"].(string), tc.wantSelf) {
			t.Errorf("%s: self link = %v, want it to keep %s", tc.name, self, tc.wantSelf)
		}
	}
}
