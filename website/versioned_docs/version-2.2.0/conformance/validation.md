---
title: Validation
description: Understand structural and profile validation behavior.
---

# Validate FHIR resources

The server validates basic FHIR resource structure and can apply loaded profile constraints when profile validation is enabled.

## Use `$validate`

Validate a resource without storing it:

```bash title="Request"
curl -sS -X POST 'http://localhost:9090/fhir/r4/Observation/$validate' \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Observation",
    "status": "final",
    "code": {"text": "Heart rate"}
  }' | jq
```

```json title="Response"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "warning",
      "code": "invariant",
      "diagnostics": "A resource should have narrative for robust management",
      "expression": ["Observation"]
    }
  ]
}
```

A passing resource still reports FHIRPath `invariant` findings as warnings, so check for
`severity: "error"` rather than an empty outcome — see
[Operations](../api/operations.md#validate).

The response is an OperationOutcome:

- `200 OK` when validation succeeds.
- `422 Unprocessable Entity` when the resource violates an applicable rule.

## Validation on write

Base resource checks protect fundamental structure. Profile validation is deployment-controlled and applies to profiles declared in `meta.profile` when their StructureDefinitions are available.

The toggles live in the `validation:` YAML block, and each can be overridden by an environment
variable:

| YAML key | Environment variable | Default | Effect |
| --- | --- | --- | --- |
| `validation.base` | `FHIR_VALIDATION_BASE` | `true` | Validates every write against the base R4 StructureDefinition. Set `false` to disable. |
| `validation.profile` | `FHIR_VALIDATION_PROFILE` | `false` | Set `true` to additionally enforce declared profiles on create and update. |
| `validation.referentialIntegrityOnWrite` | `FHIR_VALIDATION_REFERENTIAL_INTEGRITY_ON_WRITE` | `true` | Rejects writes whose local literal references do not resolve — see below. |
| `validation.referentialIntegrityOnDelete` | `FHIR_VALIDATION_REFERENTIAL_INTEGRITY_ON_DELETE` | `true` | Rejects deletes of resources that are still referenced — see below. |

:::note
The default behavior favors FHIR interoperability. Load the required Implementation Guides and set `FHIR_VALIDATION_PROFILE=true` when a deployment requires profile enforcement.
:::

Independent of these toggles, writes always enforce a small set of required fields whose absence
breaks core workflows — for example `Observation.code`, `Encounter.status` and `Encounter.class`,
`Condition.subject`, `DiagnosticReport.status` and `DiagnosticReport.code`, and
`AllergyIntolerance.patient`. A present-but-empty value (`null`, `""`, `{}`, `[]`) counts as
missing and is rejected with `422 Unprocessable Entity`.

## Referential integrity

At the end of every write transaction — after all entries are flushed, inside the same
transaction — the store verifies that the resulting database state is referentially consistent:

- **On write** (`validation.referentialIntegrityOnWrite`, default `true`): every local literal
  reference (`Type/id`) carried by a created, updated, or patched resource must resolve to a
  live (non-deleted) resource. A violation aborts the transaction with
  `422 Unprocessable Entity`.
- **On delete** (`validation.referentialIntegrityOnDelete`, default `true`): a resource cannot
  be deleted while live resources still reference it through an indexed reference search
  parameter. A violation aborts the transaction with `409 Conflict`.

Because the checks run after the flush, transaction Bundles are order-independent: a Bundle that
creates both an Observation and the Patient it points at passes regardless of entry order, and a
violation rolls the whole Bundle back.

Scope and exemptions:

- Only local literal references are existence-checked. Absolute URLs, `urn:` values, internal
  fragments (`#contained`), conditional references (`Type?query`), and logical
  (identifier-only) references are never checked.
- The delete-side check consults the reference search index (`sp_reference`), so it sees exactly
  the references indexed by a reference-type SearchParameter.
- `Bundle`-typed resources (stored documents or collections) are exempt from the write-side
  check: their entry-local references resolve against the bundle itself, not this server.

`$validate` does not check referential integrity, so a resource can validate clean and still be
rejected on write. When both checks are enabled, the CapabilityStatement advertises
`referencePolicy: ["literal", "logical", "enforced"]`. Bulk loads whose data arrives out of
order (for example Synthea exports imported file by file) may need
`FHIR_VALIDATION_REFERENTIAL_INTEGRITY_ON_WRITE=false` for the duration of the import.

## Profile availability

Use `/metadata` to confirm that the expected packages and profiles loaded successfully before sending profile-constrained traffic.

## Application behavior

Clients should parse OperationOutcome issues instead of relying only on HTTP status text. Preserve the issue severity, code, diagnostics, and expression fields when presenting validation failures.
