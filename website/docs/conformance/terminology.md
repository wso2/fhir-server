---
title: Terminology
description: Connect the FHIR server to an external terminology service.
---

# Terminology integration

WSO2 FHIR Server supports terminology-backed searches — ValueSet membership and CodeSystem hierarchy filters — by connecting to an external FHIR terminology service. Point the server at a terminology endpoint and the `:in`, `:not-in`, `:below`, and `:above` token search modifiers become available.

## Configure the service

Set the terminology server base URL with the `FHIR_TERMINOLOGY_URL` environment variable (env only — there is no YAML key). When it is unset, the server runs normally but terminology-backed search modifiers return an error instead of failing silently:

```bash
export FHIR_TERMINOLOGY_URL="https://tx.example.org/fhir"
```

For local evaluation, use a non-production terminology endpoint and verify its licensing and availability constraints.

## Terminology-backed search

Token searches can use terminology modifiers when the external service is configured. Examples include ValueSet membership and CodeSystem hierarchy searches:

```bash title="Request"
curl -sS --get "http://localhost:9090/fhir/r4/Observation" \
  --data-urlencode "code:in=http://example.org/fhir/ValueSet/lab-codes"
```

```bash title="Request"
curl -sS --get "http://localhost:9090/fhir/r4/Condition" \
  --data-urlencode "code:below=http://snomed.info/sct|73211009"
```

When no terminology server is configured, these modifiers fail loudly rather than returning a
misleading result set:

```json title="Response"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "error",
      "code": "not-supported",
      "diagnostics": "modifier :in on param \"code\" requires FHIR_TERMINOLOGY_URL to be configured"
    }
  ]
}
```

## Operational considerations

- Treat the terminology service as a runtime dependency for terminology-backed queries.
- The server's terminology client uses a fixed 10-second request timeout; monitor error rates and latency, and size any gateway timeouts around it.
- Confirm CodeSystem and ValueSet licensing for every deployment.
- Cache only where the terminology server's versioning semantics make cached results safe.

:::warning
Do not assume the public terminology sandbox provides production availability, performance, or licenses for every code system.
:::
