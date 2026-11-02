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
	"errors"
	"testing"

	"github.com/wso2/fhir-server/internal/searchparam"
)

// TestBuildNumericExists_InvalidValueSetsError pins that a number or quantity
// value that doesn't parse, or isn't finite, surfaces as an InvalidParamError
// (HTTP 400) instead of being searched.
func TestBuildNumericExists_InvalidValueSetsError(t *testing.T) {
	for _, value := range []string{"abc", "gtabc", "NaN", "Inf", "-Inf", "gtInfinity"} {
		b := &queryBuilder{rt: "RiskAssessment"}
		b.buildNumberExists("probability", value)
		var invalid *InvalidParamError
		if !errors.As(b.err, &invalid) {
			t.Errorf("number %q: b.err = %v, want *InvalidParamError", value, b.err)
		}
	}
	for _, value := range []string{"abc|http://unitsofmeasure.org|mg", "leabc", "NaN", "Inf|http://unitsofmeasure.org|mg"} {
		b := &queryBuilder{rt: "Observation"}
		b.buildQuantityExists("value-quantity", value)
		var invalid *InvalidParamError
		if !errors.As(b.err, &invalid) {
			t.Errorf("quantity %q: b.err = %v, want *InvalidParamError", value, b.err)
		}
	}
}

// TestApplyParam_EmptyNumericValueIgnored pins that an empty number or quantity
// value adds no filter and no error, as the FHIR R4 search spec requires.
func TestApplyParam_EmptyNumericValueIgnored(t *testing.T) {
	reg := searchparam.NewRegistry()
	reg.Upsert(searchparam.Definition{ResourceType: "RiskAssessment", ParamName: "probability", ParamType: "number"})
	reg.Upsert(searchparam.Definition{ResourceType: "Observation", ParamName: "value-quantity", ParamType: "quantity"})

	for _, tc := range []struct{ rt, key string }{
		{"RiskAssessment", "probability"},
		{"Observation", "value-quantity"},
		{"Observation", "value-quantity:not"},
	} {
		b := &queryBuilder{rt: tc.rt, reg: reg}
		b.applyParam(tc.key, "")
		if b.err != nil || len(b.args) != 0 {
			t.Errorf("%s?%s=: b.err = %v, args = %v, want no error and no filter", tc.rt, tc.key, b.err, b.args)
		}
	}
}

// TestApplyParam_UnknownParamNonFiniteValueNotRejected pins that an unknown
// param with a value like "Nan" stays on the heuristic path instead of being
// taken for a number and rejected.
func TestApplyParam_UnknownParamNonFiniteValueNotRejected(t *testing.T) {
	for _, value := range []string{"Nan", "inf", "Infinity"} {
		b := &queryBuilder{rt: "Patient", reg: searchparam.NewRegistry()}
		b.applyParam("nickname", value)
		if b.err != nil {
			t.Errorf("nickname=%s: b.err = %v, want nil", value, b.err)
		}
	}
}
