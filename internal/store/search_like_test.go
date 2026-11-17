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

import "testing"

func TestLikeSearchesEscapeWildcards(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(b *queryBuilder)
		want  string
	}{
		{"string prefix", func(b *queryBuilder) { b.buildStringExists("name", "", `50%_A\`) }, `50\%\_a\\%`},
		{"string :contains", func(b *queryBuilder) { b.buildStringExists("name", "contains", "50%") }, `%50\%%`},
		{"token :text", func(b *queryBuilder) { b.buildTokenExists("code", "text", "a_b") }, `%a\_b%`},
	} {
		b := &queryBuilder{rt: "Patient"}
		tc.build(b)
		if got := b.args[len(b.args)-1]; got != tc.want {
			t.Errorf("%s: pattern = %q, want %q", tc.name, got, tc.want)
		}
	}
}
