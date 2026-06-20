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

// Package compartment provides the FHIR R4 compartment definitions and
// compartment-filtered search helpers.
//
// A compartment search (e.g. GET /Patient/{id}/Observation) is equivalent
// to a type search with an OR-filter over the params that link the resource
// to the compartment owner. For example, Observation resources in the Patient
// compartment match on any of: subject, performer.
//
// Reference: https://hl7.org/fhir/R4/compartmentdefinition-patient.html
package compartment

// Definition maps a compartment type ("Patient", "Encounter", "Practitioner")
// and a resource type to the list of reference params that make a resource
// part of that compartment. Each param name is an OR-alternative.
type Definition struct {
	// CompartmentType is the type of the compartment owner, e.g. "Patient".
	CompartmentType string
	// Inclusions maps resourceType → []paramName (OR alternatives).
	Inclusions map[string][]string
}

// R4Definitions is the hard-coded FHIR R4 compartment inclusion list for
// the three commonly supported compartments.
var R4Definitions = []*Definition{PatientCompartment, EncounterCompartment, PractitionerCompartment}

// Lookup returns the compartment definition for the given compartment type,
// or nil if not supported.
func Lookup(compartmentType string) *Definition {
	for _, d := range R4Definitions {
		if d.CompartmentType == compartmentType {
			return d
		}
	}
	return nil
}

// ParamsFor returns the search param names that link resourceType into the
// compartment, or nil if the resource type is not in this compartment.
func (d *Definition) ParamsFor(resourceType string) []string {
	return d.Inclusions[resourceType]
}
