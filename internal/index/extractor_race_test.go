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

package index_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/wso2/fhir-server/internal/index"
	"github.com/wso2/fhir-server/internal/searchparam"
)

// TestExtract_ConcurrentShards proves the read path a parallel transaction
// bundle depends on is safe under concurrent use: K shard goroutines share one
// Extractor (and through it the searchparam.Registry and the fhirpath parse
// cache) while extracting into their own RowSets, exactly as the parallel
// bundle executor does. A registry writer runs alongside to model a concurrent
// SearchParameter sync. Run under -race (the default `make test` / CI unit
// pass), any lazy cache or unguarded map in the extraction path fails here
// before the parallel executor can ship it.
func TestExtract_ConcurrentShards(t *testing.T) {
	reg := raceRegistry()
	ex := index.New(reg)
	resources := raceResources(100)
	now := time.Now().UTC()

	const goroutines = 8
	var wg sync.WaitGroup
	rowCounts := make([]int, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			rs := index.NewRowSet("default", 0)
			for _, r := range resources {
				rt, _ := r["resourceType"].(string)
				id, _ := r["id"].(string)
				ex.Extract(rs, rt, id, r, now)
			}
			rowCounts[g] = rs.Count()
		}(g)
	}

	// Concurrent registry churn: a bundle writing a custom SearchParameter
	// triggers Upsert/Remove while other bundles extract.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			reg.Upsert(searchparam.Definition{
				ResourceType: "Patient",
				ParamName:    "race-custom",
				ParamType:    "string",
				FHIRPath:     "Patient.name.family",
				IsCustom:     true,
			})
			reg.Remove("Patient", "race-custom")
		}
	}()

	wg.Wait()
	<-done

	if rowCounts[0] == 0 {
		t.Fatal("extraction produced no rows; fixtures or registry are broken")
	}
	// The registry writer only churns a param the fixtures never rely on
	// exclusively, but its Upsert/Remove can transiently add rows for shard
	// extractions that observe it. All shards extract the same resources, so any
	// difference beyond that transient param signals a data race corrupted state.
	for g := 1; g < goroutines; g++ {
		if diff := rowCounts[g] - rowCounts[0]; diff < -100 || diff > 100 {
			t.Fatalf("shard %d extracted %d rows, shard 0 extracted %d — concurrent extraction diverged",
				g, rowCounts[g], rowCounts[0])
		}
	}
}

// raceRegistry builds an in-memory registry with representative definitions of
// every search param type the extractor routes, mirroring the base R4 set used
// on the Synthea import path.
func raceRegistry() *searchparam.Registry {
	reg := searchparam.NewRegistry()
	for _, d := range []searchparam.Definition{
		{ResourceType: "Patient", ParamName: "name", ParamType: "string", FHIRPath: "Patient.name.family | Patient.name.given"},
		{ResourceType: "Patient", ParamName: "identifier", ParamType: "token", FHIRPath: "Patient.identifier"},
		{ResourceType: "Patient", ParamName: "gender", ParamType: "token", FHIRPath: "Patient.gender"},
		{ResourceType: "Patient", ParamName: "birthdate", ParamType: "date", FHIRPath: "Patient.birthDate"},
		{ResourceType: "Patient", ParamName: "address-city", ParamType: "string", FHIRPath: "Patient.address.city"},
		{ResourceType: "Observation", ParamName: "code", ParamType: "token", FHIRPath: "Observation.code"},
		{ResourceType: "Observation", ParamName: "date", ParamType: "date", FHIRPath: "Observation.effective.ofType(dateTime) | Observation.effective.ofType(Period)"},
		{ResourceType: "Observation", ParamName: "subject", ParamType: "reference", FHIRPath: "Observation.subject", Targets: []string{"Patient"}},
		{ResourceType: "Observation", ParamName: "patient", ParamType: "reference", FHIRPath: "Observation.subject.where(resolve() is Patient)", Targets: []string{"Patient"}},
		{ResourceType: "Observation", ParamName: "value-quantity", ParamType: "quantity", FHIRPath: "Observation.value.ofType(Quantity)"},
		{ResourceType: "Observation", ParamName: "code-value-quantity", ParamType: "composite", FHIRPath: "Observation",
			Components: []searchparam.ComponentDef{
				{Expression: "code"},
				{Expression: "value.as(Quantity)"},
			}},
		{ResourceType: "Encounter", ParamName: "date", ParamType: "date", FHIRPath: "Encounter.period"},
		{ResourceType: "Encounter", ParamName: "class", ParamType: "token", FHIRPath: "Encounter.class"},
		{ResourceType: "Encounter", ParamName: "subject", ParamType: "reference", FHIRPath: "Encounter.subject", Targets: []string{"Patient"}},
		{ResourceType: "Condition", ParamName: "code", ParamType: "token", FHIRPath: "Condition.code"},
		{ResourceType: "Condition", ParamName: "onset-date", ParamType: "date", FHIRPath: "Condition.onset.ofType(dateTime)"},
		{ResourceType: "Condition", ParamName: "subject", ParamType: "reference", FHIRPath: "Condition.subject", Targets: []string{"Patient"}},
	} {
		reg.Upsert(d)
	}
	return reg
}

// raceResources builds n mixed Synthea-shaped resources (Patient, Observation
// with valueQuantity, Encounter with a Period, Condition) with inter-resource
// references, cycling through the four shapes.
func raceResources(n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		patientRef := fmt.Sprintf("Patient/p%d", i/4*4)
		switch i % 4 {
		case 0:
			out = append(out, map[string]any{
				"resourceType": "Patient",
				"id":           fmt.Sprintf("p%d", i),
				"name": []any{map[string]any{
					"use": "official", "family": fmt.Sprintf("Fam%d", i), "given": []any{"Alex", "B"},
				}},
				"identifier": []any{map[string]any{
					"system": "https://github.com/synthetichealth/synthea", "value": fmt.Sprintf("mrn-%d", i),
				}},
				"gender":    "female",
				"birthDate": "1974-12-25",
				"address":   []any{map[string]any{"city": "Boston", "state": "MA"}},
				"meta":      map[string]any{"tag": []any{map[string]any{"system": "http://example.org/tags", "code": "synthea"}}},
			})
		case 1:
			out = append(out, map[string]any{
				"resourceType": "Observation",
				"id":           fmt.Sprintf("o%d", i),
				"status":       "final",
				"code": map[string]any{"coding": []any{map[string]any{
					"system": "http://loinc.org", "code": "29463-7", "display": "Body Weight",
				}}},
				"subject":           map[string]any{"reference": patientRef},
				"effectiveDateTime": "2020-03-14T09:00:00Z",
				"valueQuantity": map[string]any{
					"value": 70.5 + float64(i), "unit": "kg", "system": "http://unitsofmeasure.org", "code": "kg",
				},
			})
		case 2:
			out = append(out, map[string]any{
				"resourceType": "Encounter",
				"id":           fmt.Sprintf("e%d", i),
				"status":       "finished",
				"class":        map[string]any{"system": "http://terminology.hl7.org/CodeSystem/v3-ActCode", "code": "AMB"},
				"subject":      map[string]any{"reference": patientRef},
				"period":       map[string]any{"start": "2020-03-14T09:00:00Z", "end": "2020-03-14T09:30:00Z"},
			})
		default:
			out = append(out, map[string]any{
				"resourceType": "Condition",
				"id":           fmt.Sprintf("c%d", i),
				"code": map[string]any{"coding": []any{map[string]any{
					"system": "http://snomed.info/sct", "code": "44054006", "display": "Diabetes",
				}}},
				"subject":       map[string]any{"reference": patientRef},
				"onsetDateTime": "2018-06-01",
			})
		}
	}
	return out
}
