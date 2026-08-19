---
title: Terminology
description: Connect the FHIR server to an external terminology service.
---

# Terminology integration

WSO2 FHIR Server stores clinical resources but delegates terminology reasoning to a FHIR terminology service. This keeps ValueSet expansion, code validation, and subsumption outside the resource-store runtime.

## Configure the service

Set the terminology server base URL with the `FHIR_TERMINOLOGY_URL` environment variable. When it is unset, terminology-backed search modifiers are disabled:

```bash
export FHIR_TERMINOLOGY_URL="https://tx.example.org/fhir"
```

For local evaluation, use a non-production terminology endpoint and verify its licensing and availability constraints.

## Terminology-backed search

Token searches can use terminology modifiers when the external service is configured. Examples include ValueSet membership and CodeSystem hierarchy searches:

```bash
curl -sS --get "http://localhost:9090/fhir/r4/Observation" \
  --data-urlencode "code:in=http://example.org/fhir/ValueSet/lab-codes"
```

```bash
curl -sS --get "http://localhost:9090/fhir/r4/Condition" \
  --data-urlencode "code:below=http://snomed.info/sct|73211009"
```

## Operational considerations

- Treat the terminology service as a runtime dependency for terminology-backed queries.
- The server's terminology client uses a fixed 10-second request timeout; monitor error rates and latency, and size any gateway timeouts around it.
- Confirm CodeSystem and ValueSet licensing for every deployment.
- Cache only where the terminology server's versioning semantics make cached results safe.

:::warning
Do not assume the public terminology sandbox provides production availability, performance, or licenses for every code system.
:::
