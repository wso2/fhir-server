---
title: FHIR REST API
description: Reference for resource interactions, history, Bundles, and server operations.
---

# Interact with FHIR resources

The default FHIR base URL is:

```text
http://localhost:9090/fhir/r4
```

Use `application/fhir+json` for the examples below. The server also negotiates `application/fhir+xml` and `application/fhir+turtle` (or `text/turtle`) through the `Content-Type` and `Accept` headers.

## Interactions

| Interaction | Method and path |
| --- | --- |
| Create | `POST /{type}` |
| Read | `GET /{type}/{id}` |
| Update | `PUT /{type}/{id}` |
| Patch | `PATCH /{type}/{id}` |
| Delete | `DELETE /{type}/{id}` |
| Search | `GET /{type}?{parameters}` |
| Search with form body | `POST /{type}/_search` |
| Versioned read | `GET /{type}/{id}/_history/{version}` |
| Instance history | `GET /{type}/{id}/_history` |
| Type history | `GET /{type}/_history` |
| System transaction or batch | `POST /` |
| CapabilityStatement | `GET /metadata` |
| Validate | `POST /{type}/$validate` |
| Resource graph (instance) | `GET /{type}/{id}/$everything` |
| Resource graph (type; Patient, Encounter, and Group only) | `GET /{type}/$everything` |
| Compartment search | `GET /{ownerType}/{id}/{targetType}` |

## Compartment search

`{ownerType}` is the compartment owner's resource type; Patient, Encounter, and Practitioner are supported. A compartment search returns resources of the target type that belong to that owner instance:

```bash
curl -sS "http://localhost:9090/fhir/r4/Patient/{id}/Observation" | jq
```

## $everything

`$everything` returns the anchor resource and the resources referenced from its graph as a Bundle. It accepts `_type` (comma-separated resource types to include) and `_since` (RFC 3339 timestamp; only resources updated since then):

```bash
curl -sS "http://localhost:9090/fhir/r4/Patient/{id}/\$everything?_type=Observation,Condition&_since=2026-01-01T00:00:00Z" | jq
```

## Create

```bash
curl -i -X POST "http://localhost:9090/fhir/r4/Patient" \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Patient",
    "active": true,
    "name": [{"family": "Smith", "given": ["Alice"]}]
  }'
```

A successful create returns `201 Created`, the created resource, a `Location` header, and a weak ETag.

## Read and update

```bash
curl -sS "http://localhost:9090/fhir/r4/Patient/{id}" | jq
```

```bash
curl -i -X PUT "http://localhost:9090/fhir/r4/Patient/{id}" \
  -H "Content-Type: application/fhir+json" \
  -H 'If-Match: W/"1"' \
  -d '{
    "resourceType": "Patient",
    "id": "{id}",
    "active": true
  }'
```

`If-Match` enables optimistic locking. A stale version fails instead of overwriting a concurrent update.

## Patch

Three patch formats are supported, selected by `Content-Type`:

| Format | Content-Type |
| --- | --- |
| JSON Patch (RFC 6902) | `application/json-patch+json` |
| FHIR Patch (`Parameters` resource) | `application/fhir+json` or `application/fhir+xml` |
| XML Patch | `application/xml-patch+xml` |

```bash
curl -i -X PATCH "http://localhost:9090/fhir/r4/Patient/{id}" \
  -H "Content-Type: application/json-patch+json" \
  -d '[{"op":"replace","path":"/active","value":false}]'
```

## Delete

```bash
curl -i -X DELETE "http://localhost:9090/fhir/r4/Patient/{id}"
```

A successful delete returns `204 No Content`. The resource is soft-deleted and the version remains available through history.

## Transaction Bundle

```bash
curl -sS -X POST "http://localhost:9090/fhir/r4" \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Bundle",
    "type": "transaction",
    "entry": [
      {
        "fullUrl": "urn:uuid:patient-1",
        "resource": {
          "resourceType": "Patient",
          "name": [{"family": "Smith"}]
        },
        "request": {"method": "POST", "url": "Patient"}
      }
    ]
  }' | jq
```

Transaction entries commit atomically. Batch entries produce independent outcomes.

## Response metadata

Resource responses can include:

- `Content-Type` with the negotiated FHIR media type.
- `ETag` containing the weak resource version.
- `Last-Modified` containing the resource update time.
- `Location` identifying a newly created version.

Errors use a FHIR OperationOutcome body whenever the response can be represented as FHIR.
