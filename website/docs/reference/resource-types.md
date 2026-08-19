---
title: Supported resource types
description: Every FHIR R4 resource type you can store, search, and version on the server, and how to confirm support at runtime.
---

# Supported resource types

The server accepts every concrete FHIR R4 resource type — all 146 of them. You can create, read, update, delete, search, and version any of them through the same REST endpoints:

```bash
# Store a Patient
curl -X POST http://localhost:9090/fhir/r4/Patient \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType": "Patient", "name": [{"family": "Perera"}]}'

# Store an Observation the same way — no setup needed for a new type
curl -X POST http://localhost:9090/fhir/r4/Observation \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType": "Observation", "status": "final", "code": {"text": "Heart rate"}}'
```

There is no per-type enablement step. Using a type for the first time requires no migration or configuration. By default, each incoming resource is validated against the base R4 StructureDefinition for its type, which the server ships and loads at startup (`FHIR_BASE_VALIDATION=false` disables this).

:::tip
The authoritative list for a running server is its CapabilityStatement: `GET /metadata`. Clients should prefer it over documentation, since deployments may add Implementation Guides and custom SearchParameters.
:::

## What you can put up

The R4 specification groups its resource types by purpose. All groups are supported equally; the ones most deployments start with are in bold.

| Group | Examples |
| --- | --- |
| Individuals and groups | **Patient**, **Practitioner**, PractitionerRole, RelatedPerson, Person, Group |
| Clinical summary | **Condition**, **AllergyIntolerance**, **Procedure**, FamilyMemberHistory, ClinicalImpression, DetectedIssue |
| Diagnostics | **Observation**, **DiagnosticReport**, Specimen, ImagingStudy, MolecularSequence, BodyStructure |
| Medications | **MedicationRequest**, Medication, MedicationAdministration, MedicationDispense, MedicationStatement, Immunization |
| Care provision and workflow | **Encounter**, **CarePlan**, CareTeam, Goal, ServiceRequest, Task, Appointment, Schedule, Slot |
| Organizations and infrastructure | **Organization**, Location, HealthcareService, Endpoint, Device |
| Documents and communication | DocumentReference, Composition, Communication, QuestionnaireResponse, Media |
| Financial | Coverage, Claim, ClaimResponse, ExplanationOfBenefit, Invoice, PaymentNotice |
| Terminology and conformance | CodeSystem, ValueSet, ConceptMap, StructureDefinition, SearchParameter, ImplementationGuide |
| Security and audit | AuditEvent, Provenance, Consent |

Every other R4 type (research, medicinal-product definitions, messaging, testing, and so on) works the same way.

## What varies by type

Support for storage and CRUD is uniform, but a few capabilities are richer for commonly used types:

- **Search parameters.** The base R4 search parameters are seeded at startup for all types. Heavily used clinical types (Patient, Observation, Encounter, Condition, MedicationRequest, and others) have the deepest coverage; any type can be extended with custom [SearchParameters](../development/extending.md). To see exactly which parameters a running server supports for a given type, read that type's `searchParam` list in `GET /metadata`.
- **Compartments.** `GET /Patient/{id}/*` style compartment search is available for the Patient, Encounter, and Practitioner compartments.
- **Profiles.** Loading an [Implementation Guide](../guides/implementation-guides.md) adds profile [validation](../guides/validation.md) on top of base R4 validation for the types it constrains.

## Where to go next

- [FHIR API reference](./api.md) — the operations available on every resource endpoint.
- [Search](./search.md) — how to query what you have stored.
- [Quickstart](../get-started/quickstart.md) — a running server in one command.
- [Storage](../concepts/storage.md) — how resources are stored, for readers who want the details behind the migration-free design.
