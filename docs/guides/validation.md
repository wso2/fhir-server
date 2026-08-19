---
title: Validation
description: Understand structural and profile validation behavior.
---

# Validate FHIR resources

The server validates basic FHIR resource structure and can apply loaded profile constraints when profile validation is enabled.

## Use `$validate`

Validate a resource without storing it:

```bash
curl -sS -X POST "http://localhost:9090/fhir/r4/Observation/$validate" \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Observation",
    "status": "final",
    "code": {"text": "Heart rate"}
  }' | jq
```

The response is an OperationOutcome:

- `200 OK` when validation succeeds.
- `422 Unprocessable Entity` when the resource violates an applicable rule.

## Validation on write

Base resource checks protect fundamental structure. Profile validation is deployment-controlled and applies to profiles declared in `meta.profile` when their StructureDefinitions are available.

Two environment variables control the behavior:

| Variable | Default | Effect |
| --- | --- | --- |
| `FHIR_BASE_VALIDATION` | `true` | Validates every write against the base R4 StructureDefinition. Set `false` to disable. |
| `FHIR_VALIDATE_ON_WRITE` | `false` | Set `true` to additionally enforce declared profiles on create and update. |

:::note
The default behavior favors FHIR interoperability. Load the required Implementation Guides and set `FHIR_VALIDATE_ON_WRITE=true` when a deployment requires profile enforcement.
:::

## Profile availability

Use `/metadata` to confirm that the expected packages and profiles loaded successfully before sending profile-constrained traffic.

## Application behavior

Clients should parse OperationOutcome issues instead of relying only on HTTP status text. Preserve the issue severity, code, diagnostics, and expression fields when presenting validation failures.
