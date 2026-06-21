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

package compartment

// PatientCompartment is the FHIR R4 Patient compartment definition.
// Source: https://hl7.org/fhir/R4/compartmentdefinition-patient.html
// Only resource types with defined inclusions are listed; types not included
// here are not in the Patient compartment.
var PatientCompartment = &Definition{
	CompartmentType: "Patient",
	Inclusions: map[string][]string{
		"Account":                     {"subject"},
		"AdverseEvent":                {"subject"},
		"AllergyIntolerance":          {"patient", "recorder", "asserter"},
		"Appointment":                 {"actor"},
		"AppointmentResponse":         {"actor"},
		"AuditEvent":                  {"patient"},
		"Basic":                       {"patient", "author"},
		"BodyStructure":               {"patient"},
		"CarePlan":                    {"patient", "performer"},
		"CareTeam":                    {"patient", "participant"},
		"ChargeItem":                  {"subject"},
		"Claim":                       {"patient", "payee"},
		"ClaimResponse":               {"patient"},
		"ClinicalImpression":          {"subject"},
		"Communication":               {"subject", "sender", "recipient"},
		"CommunicationRequest":        {"subject", "sender", "recipient", "requester"},
		"Composition":                 {"subject", "author", "attester"},
		"Condition":                   {"patient", "asserter"},
		"Consent":                     {"patient"},
		"Coverage":                    {"policy-holder", "subscriber", "beneficiary", "payor"},
		"CoverageEligibilityRequest":  {"patient"},
		"CoverageEligibilityResponse": {"patient"},
		"DetectedIssue":               {"patient"},
		"DeviceRequest":               {"subject", "performer"},
		"DeviceUseStatement":          {"subject"},
		"DiagnosticReport":            {"subject"},
		"DocumentManifest":            {"subject", "author", "recipient"},
		"DocumentReference":           {"subject", "author"},
		"Encounter":                   {"patient"},
		"EnrollmentRequest":           {"subject"},
		"EpisodeOfCare":               {"patient"},
		"ExplanationOfBenefit":        {"patient", "payee"},
		"FamilyMemberHistory":         {"patient"},
		"Flag":                        {"patient"},
		"Goal":                        {"patient"},
		"Group":                       {"member"},
		"ImagingStudy":                {"patient"},
		"Immunization":                {"patient"},
		"ImmunizationEvaluation":      {"patient"},
		"ImmunizationRecommendation":  {"patient"},
		"Invoice":                     {"subject", "patient", "recipient"},
		"List":                        {"subject", "source"},
		"MeasureReport":               {"patient"},
		"Media":                       {"subject"},
		"MedicationAdministration":    {"patient", "performer", "subject"},
		"MedicationDispense":          {"subject", "patient", "receiver"},
		"MedicationRequest":           {"subject"},
		"MedicationStatement":         {"subject"},
		"MolecularSequence":           {"patient"},
		"NutritionOrder":              {"patient"},
		"Observation":                 {"subject", "performer"},
		"Patient":                     {"link"},
		"Person":                      {"patient"},
		"Procedure":                   {"patient", "performer"},
		"Provenance":                  {"patient"},
		"QuestionnaireResponse":       {"subject", "author"},
		"RelatedPerson":               {"patient"},
		"RequestGroup":                {"subject", "participant"},
		"ResearchSubject":             {"individual"},
		"RiskAssessment":              {"subject"},
		"Schedule":                    {"actor"},
		"ServiceRequest":              {"subject", "performer"},
		"Specimen":                    {"subject"},
		"SupplyDelivery":              {"patient"},
		"SupplyRequest":               {"subject"},
		"Task":                        {"patient"},
		"VisionPrescription":          {"patient"},
	},
}
