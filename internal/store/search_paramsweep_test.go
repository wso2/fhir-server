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
	"fmt"
	"testing"

	"github.com/wso2/fhir-server/internal/searchparam"
)

// paramSweepRegistry covers every sp_* value type plus composite so the sweep
// exercises each value-predicate builder.
func paramSweepRegistry() *searchparam.Registry {
	reg := searchparam.NewRegistry()
	for _, d := range []searchparam.Definition{
		{ResourceType: "Observation", ParamName: "value-quantity", ParamType: "quantity", FHIRPath: "Observation.value.ofType(Quantity)"},
		{ResourceType: "Observation", ParamName: "combo-value-quantity", ParamType: "quantity", FHIRPath: "Observation.xx"},
		{ResourceType: "Observation", ParamName: "some-number", ParamType: "number", FHIRPath: "Observation.nn"},
		{ResourceType: "Observation", ParamName: "code", ParamType: "token", FHIRPath: "Observation.code"},
		{ResourceType: "Observation", ParamName: "category", ParamType: "token"},
		{ResourceType: "Observation", ParamName: "subject", ParamType: "reference", Targets: []string{"Patient"}},
		{ResourceType: "Observation", ParamName: "date", ParamType: "date"},
		{ResourceType: "Observation", ParamName: "some-uri", ParamType: "uri"},
		{ResourceType: "Observation", ParamName: "code-value-quantity", ParamType: "composite",
			Components: []searchparam.ComponentDef{{Expression: "Observation.code"}, {Expression: "Observation.value.ofType(Quantity)"}}},
		{ResourceType: "Patient", ParamName: "name", ParamType: "string"},
		{ResourceType: "Patient", ParamName: "birthdate", ParamType: "date"},
		{ResourceType: "Patient", ParamName: "pat-qty", ParamType: "quantity", FHIRPath: "Patient.patQty"},
	} {
		reg.Upsert(d)
	}
	return reg
}

// TestParamSweep_NoOrphanParams exhaustively builds the fetch AND count SQL for a
// broad matrix of search shapes — every value type, its modifiers, comma-OR
// lists, composite, chained (typed and untyped), _has, _sort variants, and
// multi-predicate combos — and asserts each references exactly $1..$N with no
// gaps. A gap orphans a bound parameter and fails at execute time with SQLSTATE
// 42P18 ("could not determine data type of parameter $N"). Count SQL is checked
// against the args bound before fetch appends order/limit (mirroring
// fetchWithCount, where count runs first). Every one of these was also verified
// to PREPARE against live PostgreSQL.
func TestParamSweep_NoOrphanParams(t *testing.T) {
	reg := paramSweepRegistry()
	cases := []struct {
		name   string
		rt     string
		params [][2]string
		sort   string
	}{
		{"string plain", "Patient", [][2]string{{"name", "Smith"}}, ""},
		{"string exact", "Patient", [][2]string{{"name:exact", "Smith"}}, ""},
		{"string contains", "Patient", [][2]string{{"name:contains", "mit"}}, ""},
		{"string missing", "Patient", [][2]string{{"name:missing", "true"}}, ""},
		{"token code", "Observation", [][2]string{{"code", "8302-2"}}, ""},
		{"token sys|code", "Observation", [][2]string{{"code", "http://loinc.org|8302-2"}}, ""},
		{"token OR", "Observation", [][2]string{{"code", "a,b,c"}}, ""},
		{"token not", "Observation", [][2]string{{"code:not", "x"}}, ""},
		{"token missing", "Observation", [][2]string{{"category:missing", "false"}}, ""},
		{"date eq", "Observation", [][2]string{{"date", "2020"}}, ""},
		{"date gt", "Observation", [][2]string{{"date", "gt2020-01-01"}}, ""},
		{"date lt", "Observation", [][2]string{{"date", "lt2020"}}, ""},
		{"date ge", "Observation", [][2]string{{"date", "ge2020"}}, ""},
		{"date le", "Observation", [][2]string{{"date", "le2020"}}, ""},
		{"date ne", "Observation", [][2]string{{"date", "ne2020"}}, ""},
		{"date OR", "Patient", [][2]string{{"birthdate", "gt1990,lt1980"}}, ""},
		{"number gt", "Observation", [][2]string{{"some-number", "gt5"}}, ""},
		{"number lt", "Observation", [][2]string{{"some-number", "lt5"}}, ""},
		{"number eq", "Observation", [][2]string{{"some-number", "5"}}, ""},
		{"number OR", "Observation", [][2]string{{"some-number", "gt1,lt9"}}, ""},
		{"qty gt", "Observation", [][2]string{{"value-quantity", "gt170"}}, ""},
		{"qty lt", "Observation", [][2]string{{"value-quantity", "lt170"}}, ""},
		{"qty ge", "Observation", [][2]string{{"value-quantity", "ge170"}}, ""},
		{"qty le", "Observation", [][2]string{{"value-quantity", "le170"}}, ""},
		{"qty ne", "Observation", [][2]string{{"value-quantity", "ne170"}}, ""},
		{"qty eq", "Observation", [][2]string{{"value-quantity", "170"}}, ""},
		{"qty sys|code", "Observation", [][2]string{{"value-quantity", "gt5|http://unitsofmeasure.org|mg"}}, ""},
		{"qty OR", "Observation", [][2]string{{"value-quantity", "gt10,lt5"}}, ""},
		{"qty combo", "Observation", [][2]string{{"combo-value-quantity", "gt140"}}, ""},
		{"qty missing", "Observation", [][2]string{{"value-quantity:missing", "true"}}, ""},
		{"qty not", "Observation", [][2]string{{"value-quantity:not", "gt5"}}, ""},
		{"uri exact", "Observation", [][2]string{{"some-uri", "http://x/y"}}, ""},
		{"uri below", "Observation", [][2]string{{"some-uri:below", "http://x"}}, ""},
		{"uri above", "Observation", [][2]string{{"some-uri:above", "http://x/y/z"}}, ""},
		{"ref typed", "Observation", [][2]string{{"subject", "Patient/123"}}, ""},
		{"ref bare", "Observation", [][2]string{{"subject", "123"}}, ""},
		{"ref OR", "Observation", [][2]string{{"subject", "Patient/1,Patient/2"}}, ""},
		{"ref identifier", "Observation", [][2]string{{"subject:identifier", "sys|val"}}, ""},
		{"composite", "Observation", [][2]string{{"code-value-quantity", "8480-6$gt110"}}, ""},
		{"chained typed string", "Observation", [][2]string{{"subject:Patient.name", "Smith"}}, ""},
		{"chained typed qty", "Observation", [][2]string{{"subject:Patient.pat-qty", "gt5"}}, ""},
		{"chained untyped", "Observation", [][2]string{{"subject.name", "Smith"}}, ""},
		{"_has qty", "Patient", [][2]string{{"_has:Observation:subject:value-quantity", "gt5"}}, ""},
		{"_has token", "Patient", [][2]string{{"_has:Observation:subject:code", "x"}}, ""},
		{"qty sort sp_", "Observation", [][2]string{{"value-quantity", "gt170"}}, "date"},
		{"qty sort lastUpdated", "Observation", [][2]string{{"value-quantity", "gt170"}}, "-_lastUpdated"},
		{"qty sort id", "Observation", [][2]string{{"value-quantity", "gt170"}}, "_id"},
		{"token sort sp_", "Observation", [][2]string{{"code", "x"}}, "date"},
		{"browse sort", "Observation", nil, "-date"},
		{"multi qty+token", "Observation", [][2]string{{"value-quantity", "gt170"}, {"code", "x"}}, ""},
		{"multi qty+ref+date", "Observation", [][2]string{{"value-quantity", "gt170"}, {"subject", "Patient/1"}, {"date", "gt2020"}}, ""},
		{"multi token+token", "Observation", [][2]string{{"code", "x"}, {"category", "y"}}, ""},
		{"multi num+qty", "Observation", [][2]string{{"some-number", "gt5"}, {"value-quantity", "lt9"}}, ""},
		{"_id single", "Observation", [][2]string{{"_id", "abc"}}, ""},
		{"_id OR", "Observation", [][2]string{{"_id", "a,b"}}, ""},
		{"_lastUpdated", "Observation", [][2]string{{"_lastUpdated", "gt2020"}}, ""},
		{"plain browse", "Observation", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &queryBuilder{rt: tc.rt, reg: reg}
			b.writeBase()
			for _, p := range tc.params {
				b.applyParam(p[0], p[1])
			}
			if tc.sort != "" {
				b.addSort(tc.sort)
			}
			if b.err != nil {
				t.Fatalf("build error: %v", b.err)
			}
			// count runs before fetch appends order/limit args.
			assertParamsContiguous(t, fmt.Sprintf("SELECT COUNT(*) FROM resources r WHERE %s", b.where.String()), len(b.args))
			assertParamsContiguous(t, b.fetchSQL(20, 0), len(b.args))
		})
	}
}
