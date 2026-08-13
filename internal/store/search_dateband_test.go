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
	"strings"
	"testing"
	"time"
)

func TestSearchBandEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		high time.Time
		want time.Time
	}{
		{"day band high advances to the next day",
			time.Date(2020, 6, 15, 23, 59, 59, 0, time.UTC),
			time.Date(2020, 6, 16, 0, 0, 0, 0, time.UTC)},
		{"whole-second instant covers its second",
			time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC),
			time.Date(2020, 6, 15, 12, 0, 1, 0, time.UTC)},
		{"fractional-second instant stays a point at timestamptz resolution",
			time.Date(2020, 6, 15, 12, 0, 0, 500_000_000, time.UTC),
			time.Date(2020, 6, 15, 12, 0, 0, 500_001_000, time.UTC)},
	} {
		if got := searchBandEnd(tc.high); !got.Equal(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestBuildDateExists_HalfOpenBandOps pins the comparator-to-column mapping
// against the half-open search band: high-side comparators (gt/le/sa) bind the
// exclusive band end with the adjusted operator, low-side ones (ge/lt/eb) bind
// the band start unchanged, so no stored value can be simultaneously inside a
// day's eq band and above it.
func TestBuildDateExists_HalfOpenBandOps(t *testing.T) {
	dayStart := time.Date(2020, 6, 15, 0, 0, 0, 0, time.UTC)
	nextDay := time.Date(2020, 6, 16, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		frag  string
		bound time.Time
	}{
		{"gt2020-06-15", "s.value_high >= $3", nextDay},
		{"sa2020-06-15", "s.value_low >= $3", nextDay},
		{"le2020-06-15", "s.value_low < $3", nextDay},
		{"ge2020-06-15", "s.value_high >= $3", dayStart},
		{"lt2020-06-15", "s.value_low < $3", dayStart},
		{"eb2020-06-15", "s.value_high < $3", dayStart},
	} {
		b := &queryBuilder{rt: "Encounter"}
		sql := b.buildDateExists("date", tc.value)
		if !strings.Contains(sql, tc.frag) {
			t.Errorf("%s: missing %q in:\n%s", tc.value, tc.frag, sql)
		}
		if got := b.args[2].(time.Time); !got.Equal(tc.bound) {
			t.Errorf("%s: bound %v, want %v", tc.value, got, tc.bound)
		}
	}
}
