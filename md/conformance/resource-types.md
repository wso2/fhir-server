---
title: Supported resource types
description: Every FHIR R4 resource type you can store, search, and version on the server, and how to confirm support at runtime.
---

# Supported resource types

The server accepts every concrete FHIR R4 resource type. Storage is generic, so you can create,
read, update, delete, search, and version any of them through the same REST endpoints:

```bash title="Request"
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

## Three counts that differ

Three different numbers describe resource-type support, and they are not interchangeable:

| Count | What it means |
| --- | --- |
| **Every** R4 type | Can be stored, read, updated, deleted, searched, and versioned. Storage is schema-generic, so no type needs enabling |
| **147** | Base R4 StructureDefinitions the server loads at startup and validates writes against |
| **135** | Types advertised in `GET /metadata`, namely those that have search parameters in the registry |

A type outside the 135 still works for full CRUD and search — it simply has no type-specific search
parameters of its own, so only the universal ones (`_id`, `_lastUpdated`, `_tag`, …) apply:

```bash title="Request"
curl -sS -X POST http://localhost:9090/fhir/r4/SubstanceProtein \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType": "SubstanceProtein", "numberOfSubunits": 2}'
```

```json title="Response"
{
  "resourceType": "SubstanceProtein",
  "id": "0a2b8c31-…",
  "meta": {"versionId": "1", "lastUpdated": "2026-09-03T06:37:10Z"},
  "numberOfSubunits": 2
}
```

:::note
A `422` on first use of an unfamiliar type is almost always base validation reporting a missing
required element, not the type being unsupported. Read the `OperationOutcome` — it names the
element, for example `MolecularSequence.coordinateSystem: minimum cardinality is 1 but element is
absent`. See [Validation](./validation.md).
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

- **Search parameters.** The base R4 search parameters are seeded at startup for all types. Heavily used clinical types (Patient, Observation, Encounter, Condition, MedicationRequest, and others) have the deepest coverage; any type can be extended with custom [SearchParameters](../api/search.md#custom-search-parameters). To see exactly which parameters a running server supports for a given type, read that type's `searchParam` list in `GET /metadata`.
- **Compartments.** `GET /Patient/{id}/*` style compartment search is available for the Patient, Encounter, and Practitioner compartments.
- **Profiles.** Loading an [Implementation Guide](./implementation-guides.md) adds profile [validation](./validation.md) on top of base R4 validation for the types it constrains.

## Where to go next

- [FHIR API reference](../api/interactions.md) — the operations available on every resource endpoint.
- [Search](../api/search.md) — how to query what you have stored.
- [Quickstart](../get-started/quickstart.md) — a running server in one command.
- [Storage](../architecture/storage.md) — how resources are stored, for readers who want the details behind the migration-free design.
