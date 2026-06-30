//go:build integration

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
	"sync/atomic"
	"testing"

	"net/http/httptest"

	"github.com/wso2/fhir-server/internal/basedef"
	"github.com/wso2/fhir-server/internal/handler"
	"github.com/wso2/fhir-server/internal/store"
	"github.com/wso2/fhir-server/internal/testutil"
)

// baseValidationServer builds a server with the base R4 definitions loaded.
func baseValidationServer(t *testing.T, opts ...handler.Options) *httptest.Server {
	t.Helper()
	pool := testutil.MustSeededDB(t)
	if _, err := basedef.Load(context.Background(), pool, true); err != nil {
		t.Fatalf("load base definitions: %v", err)
	}
	reg := testutil.MustRegistry(t, pool)
	s := store.New(pool, reg)
	var ready atomic.Int32
	ready.Store(1)
	srv := httptest.NewServer(handler.NewRouter(s, pool, reg, "http://test-server/fhir/r4", &ready, opts...))
	t.Cleanup(srv.Close)
	return srv
}

// An Observation missing the required status is rejected by base validation
// even though no profile is supplied. code is present so this isolates base
// validation from the legacy validateRequiredFields check (which only requires
// Observation.code).
func TestIntegration_BaseValidation_RejectsMissingRequired(t *testing.T) {
	srv := baseValidationServer(t)

	resp := iDo(t, srv, http.MethodPost, "/fhir/r4/Observation",
		map[string]any{
			"resourceType": "Observation",
			"code":         map[string]any{"text": "heart rate"},
		})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		body := iJSON(t, resp)
		t.Fatalf("want 422 for Observation without status, got %d: %v", resp.StatusCode, body)
	}
	resp.Body.Close()
}

// A structurally valid Observation (status + code present) is accepted.
func TestIntegration_BaseValidation_AcceptsValid(t *testing.T) {
	srv := baseValidationServer(t)

	resp := iDo(t, srv, http.MethodPost, "/fhir/r4/Observation",
		map[string]any{
			"resourceType": "Observation",
			"status":       "final",
			"code":         map[string]any{"text": "heart rate"},
		})
	if resp.StatusCode != http.StatusCreated {
		body := iJSON(t, resp)
		t.Fatalf("want 201 for valid Observation, got %d: %v", resp.StatusCode, body)
	}
	resp.Body.Close()
}

// A choice-type element (Immunization.occurrence[x]) satisfies its required
// cardinality through a concrete variant, so a valid Immunization is accepted.
func TestIntegration_BaseValidation_ChoiceTypeAccepted(t *testing.T) {
	srv := baseValidationServer(t)

	resp := iDo(t, srv, http.MethodPost, "/fhir/r4/Immunization",
		map[string]any{
			"resourceType":       "Immunization",
			"status":             "completed",
			"vaccineCode":        map[string]any{"text": "COVID-19"},
			"patient":            map[string]any{"reference": "Patient/x"},
			"occurrenceDateTime": "2026-01-01",
		})
	if resp.StatusCode != http.StatusCreated {
		body := iJSON(t, resp)
		t.Fatalf("want 201 for valid Immunization with occurrenceDateTime, got %d: %v", resp.StatusCode, body)
	}
	resp.Body.Close()
}

// With base validation disabled, an Observation missing only status (code
// present, so the legacy required-field check passes) is accepted.
func TestIntegration_BaseValidation_Disabled(t *testing.T) {
	srv := baseValidationServer(t, handler.Options{DisableBaseValidation: true})

	resp := iDo(t, srv, http.MethodPost, "/fhir/r4/Observation",
		map[string]any{
			"resourceType": "Observation",
			"code":         map[string]any{"text": "heart rate"},
		})
	if resp.StatusCode != http.StatusCreated {
		body := iJSON(t, resp)
		t.Fatalf("want 201 when base validation disabled, got %d: %v", resp.StatusCode, body)
	}
	resp.Body.Close()

	// $validate must also honor the disable flag: a bare Observation (missing
	// status) is reported valid because base validation is off.
	resp = iDo(t, srv, http.MethodPost, "/fhir/r4/Observation/$validate",
		map[string]any{"resourceType": "Observation"})
	if resp.StatusCode != http.StatusOK {
		body := iJSON(t, resp)
		t.Fatalf("want 200 from $validate when base validation disabled, got %d: %v", resp.StatusCode, body)
	}
	resp.Body.Close()
}

// $validate reports base structural problems without a profile.
func TestIntegration_BaseValidation_ValidateOperation(t *testing.T) {
	srv := baseValidationServer(t)

	resp := iDo(t, srv, http.MethodPost, "/fhir/r4/Observation/$validate",
		map[string]any{"resourceType": "Observation"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		body := iJSON(t, resp)
		t.Fatalf("want 422 from $validate for invalid Observation, got %d: %v", resp.StatusCode, body)
	}
	resp.Body.Close()
}
