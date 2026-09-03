---
title: Interactions
description: Create, read, update, patch, delete, versioned read, history, and Bundles.
---

# Interactions

The default FHIR base URL is:

```text
http://localhost:9090/fhir/r4
```

Examples below use `application/fhir+json`. The server also reads and writes
`application/fhir+xml` and `application/fhir+turtle` (or `text/turtle`), selected with the
`Content-Type` and `Accept` headers, or with `?_format=json|xml|turtle`.

## The interaction set

| Interaction | Method and path | See also |
| --- | --- | --- |
| Create | `POST /{type}` | [Conditional create](./conditional.md#conditional-create) |
| Read | `GET /{type}/{id}` | |
| Versioned read | `GET /{type}/{id}/_history/{version}` | |
| Update | `PUT /{type}/{id}` | [Optimistic locking](./conditional.md#optimistic-locking-with-if-match) |
| Patch | `PATCH /{type}/{id}` | [Patch formats](#patch) |
| Delete | `DELETE /{type}/{id}` | [Conditional delete](./conditional.md#conditional-delete) |
| Search | `GET /{type}?{parameters}` | [Search](./search.md) |
| Search via form body | `POST /{type}/_search` | [Search](./search.md) |
| Instance history | `GET /{type}/{id}/_history` | |
| Type history | `GET /{type}/_history` | |
| System history | `GET /_history` | |
| Transaction or batch | `POST /` | [Bundles](#transaction-and-batch-bundles) |
| Compartment search | `GET /{ownerType}/{id}/{targetType}` | [Compartments](#compartment-search) |
| CapabilityStatement | `GET /metadata` | [CapabilityStatement](./capability-statement.md) |
| Operations | `$validate`, `$everything`, `$lastn`, `$document`, `$convert`, `$meta*` | [Operations](./operations.md) |

## Wire formats

Any resource response can be returned as JSON, XML, or RDF Turtle. Turtle support is unusual among
FHIR servers and works for both reads and writes:

```bash title="Request"
curl -sS -H 'Accept: application/fhir+turtle' "http://localhost:9090/fhir/r4/Patient/{id}"
```

```turtle title="Response"
@prefix fhir: <http://hl7.org/fhir/> .

[ a fhir:Patient ;
  fhir:id "041a98d5-d1f0-432d-9cb1-48847b05af9a" ;
  fhir:meta [ ;
    fhir:lastUpdated "2026-09-03T06:57:48Z" ;
    fhir:versionId "1"
  ] ;
  fhir:name ( [ ;
      fhir:family "Cohort-A"
    ] )
] .
```

## Create

```bash title="Request"
curl -i -X POST "http://localhost:9090/fhir/r4/Patient" \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Patient",
    "active": true,
    "name": [{"family": "Smith", "given": ["Alice"]}]
  }'
```

```text title="Response headers"
HTTP/1.1 201 Created
Content-Type: application/fhir+json
ETag: W/"1"
Location: http://localhost:9090/fhir/r4/Patient/5ea0f561-b771-4c9a-b4b1-5d75a8dd5227/_history/1
```

```json title="Response body"
{
  "resourceType": "Patient",
  "id": "5ea0f561-b771-4c9a-b4b1-5d75a8dd5227",
  "meta": {"versionId": "1", "lastUpdated": "2026-09-03T06:19:23Z"},
  "active": true,
  "name": [{"family": "Smith", "given": ["Alice"]}]
}
```

The server assigns an `id` when the body has none, and stamps `meta.versionId` and
`meta.lastUpdated`. Supplying your own `id` in the body is honoured.

## Read

```bash title="Request"
curl -sS -D- "http://localhost:9090/fhir/r4/Patient/{id}" | jq
```

```text title="Response headers"
HTTP/1.1 200 OK
Content-Type: application/fhir+json
ETag: W/"1"
```

```json title="Response body"
{
  "resourceType": "Patient",
  "id": "5ea0f561-b771-4c9a-b4b1-5d75a8dd5227",
  "meta": {"versionId": "1", "lastUpdated": "2026-09-03T06:19:23Z"},
  "active": true,
  "name": [{"family": "Smith", "given": ["Alice"]}]
}
```

A resource that never existed returns `404 Not Found`. One that has been deleted returns
`410 Gone`, which is a distinct signal — the id was valid and its history is still readable:

```json title="Response"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "error",
      "code": "deleted",
      "diagnostics": "Patient/5ea0f561-b771-4c9a-b4b1-5d75a8dd5227 has been deleted"
    }
  ]
}
```

## Versioned read

Read an exact historical version:

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient/{id}/_history/1" | jq
```

Every version ever written stays readable, including the version produced by a delete.

## Update

```bash title="Request"
curl -i -X PUT "http://localhost:9090/fhir/r4/Patient/{id}" \
  -H "Content-Type: application/fhir+json" \
  -H 'If-Match: W/"1"' \
  -d '{
    "resourceType": "Patient",
    "id": "{id}",
    "active": false
  }'
```

```text title="Response headers"
HTTP/1.1 200 OK
Content-Type: application/fhir+json
ETag: W/"2"
```

```json title="Response body"
{
  "resourceType": "Patient",
  "id": "5ea0f561-b771-4c9a-b4b1-5d75a8dd5227",
  "meta": {"versionId": "2", "lastUpdated": "2026-09-03T06:19:23Z"},
  "active": false
}
```

A stale `If-Match` fails the write instead of overwriting:

```text title="Response headers"
HTTP/1.1 412 Precondition Failed
```

```json title="Response body"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "error",
      "code": "conflict",
      "diagnostics": "version conflict: current=2, if-match=1"
    }
  ]
}
```

Notes:

- `If-Match` is optional but recommended — see
  [optimistic locking](./conditional.md#optimistic-locking-with-if-match).
- A body `id` that disagrees with the URL is rejected with `400 Bad Request`.
- `PUT` to an id that does not exist returns `404 Not Found`. The server does **not** do
  update-as-create, and advertises `updateCreate: false`.
- `PUT` to a *deleted* id restores the resource at a new version.

## Patch

Four patch formats are supported, selected by `Content-Type`:

| Format | `Content-Type` |
| --- | --- |
| JSON Merge Patch (RFC 7386) — **default** | `application/merge-patch+json`, or omitted |
| JSON Patch (RFC 6902) | `application/json-patch+json` |
| FHIR Patch (`Parameters` resource) | `application/fhir+json`, `application/fhir+xml` |
| XML Patch | `application/xml-patch+xml` |

JSON Merge Patch — send only the fields to change:

```bash title="Request"
curl -i -X PATCH "http://localhost:9090/fhir/r4/Patient/{id}" \
  -H "Content-Type: application/merge-patch+json" \
  -d '{"active": false}'
```

JSON Patch — send explicit operations:

```bash title="Request"
curl -i -X PATCH "http://localhost:9090/fhir/r4/Patient/{id}" \
  -H "Content-Type: application/json-patch+json" \
  -d '[{"op": "replace", "path": "/active", "value": false}]'
```

FHIR Patch — a `Parameters` resource, useful when the client already speaks FHIR:

```bash title="Request"
curl -i -X PATCH "http://localhost:9090/fhir/r4/Patient/{id}" \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Parameters",
    "parameter": [{
      "name": "operation",
      "part": [
        {"name": "type", "valueCode": "replace"},
        {"name": "path", "valueString": "Patient.active"},
        {"name": "value", "valueBoolean": false}
      ]
    }]
  }'
```

All four return `200 OK` with the patched resource and its new `ETag`:

```json title="Response"
{
  "resourceType": "Patient",
  "id": "c8f1d869-a363-48d5-bdb4-e9178da29d69",
  "meta": {"versionId": "2", "lastUpdated": "2026-09-03T06:19:51Z"},
  "active": false,
  "name": [{"family": "Patchy"}]
}
```

A patch that cannot be applied returns `422 Unprocessable Entity`.

:::warning
XML Patch writes the supplied value as a **string** — `<replace sel="…/active/@value">false</replace>`
stores `"active": "false"`, not `false`. Prefer JSON Merge Patch or FHIR Patch when the element is a
boolean or a number.
:::

:::note
`If-Match` is not evaluated on patch requests. When you need a version precondition, read the
resource and use `PUT` with `If-Match`.
:::

## Delete

```bash title="Request"
curl -i -X DELETE "http://localhost:9090/fhir/r4/Patient/{id}"
```

Returns `204 No Content`. Deletes are **soft**:

- The resource version is incremented and a history entry recorded, so the pre-delete content stays
  readable through [versioned read](#versioned-read) and history.
- A subsequent read returns `410 Gone`.
- Deleting an already-deleted resource returns `204` again and writes nothing further.
- Deleting an id that never existed returns `404 Not Found`.
- A later `PUT` to the same id restores it.

## History

History is available at three scopes and returns a `history` Bundle:

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient/{id}/_history" | jq   # one resource
curl -sS "http://localhost:9090/fhir/r4/Patient/_history" | jq        # one type
curl -sS "http://localhost:9090/fhir/r4/_history" | jq                # whole server
```

A resource that was created then deleted has two versions, newest first:

```json title="Response"
{
  "resourceType": "Bundle",
  "type": "history",
  "total": 2,
  "entry": [
    {
      "resource": {
        "resourceType": "Patient",
        "id": "e7f4d572-7d50-4435-9931-c40bed82b6c3",
        "meta": {"versionId": "2", "lastUpdated": "2026-09-03T06:19:51Z"},
        "name": [{"family": "Patchy"}]
      },
      "request": {"method": "DELETE", "url": "http://localhost:9090/fhir/r4/Patient/e7f4d572-…"}
    },
    {
      "resource": {
        "resourceType": "Patient",
        "id": "e7f4d572-7d50-4435-9931-c40bed82b6c3",
        "meta": {"versionId": "1", "lastUpdated": "2026-09-03T06:19:51Z"},
        "name": [{"family": "Patchy"}]
      },
      "request": {"method": "POST", "url": "http://localhost:9090/fhir/r4/Patient/e7f4d572-…"}
    }
  ]
}
```

Each entry reports the interaction that produced it in `entry.request.method` — including `DELETE`,
whose entry carries the content as it was immediately before deletion.

## Compartment search

Return resources of one type that belong to a compartment owner. The Patient, Encounter, and
Practitioner compartments are supported:

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient/{id}/Observation" | jq '{type, total}'
```

```json title="Response"
{
  "type": "searchset",
  "total": 1
}
```

## Transaction and batch Bundles

`POST /` at the base accepts a Bundle whose `type` is `transaction` or `batch`.

```bash title="Request"
curl -sS -X POST "http://localhost:9090/fhir/r4" \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Bundle",
    "type": "transaction",
    "entry": [
      {
        "fullUrl": "urn:uuid:patient-1",
        "resource": {"resourceType": "Patient", "name": [{"family": "Smith"}]},
        "request": {"method": "POST", "url": "Patient"}
      },
      {
        "resource": {
          "resourceType": "Observation",
          "status": "final",
          "code": {"text": "Heart rate"},
          "subject": {"reference": "urn:uuid:patient-1"}
        },
        "request": {"method": "POST", "url": "Observation"}
      }
    ]
  }' | jq '.type, [.entry[].response.status]'
```

- **`transaction`** applies every entry atomically. Entries are processed in FHIR verb order
  (`DELETE`, `POST`, `PUT`, `PATCH`, `GET`), `urn:uuid` references between entries are resolved to
  the ids actually assigned, and any failure rolls the whole Bundle back — the failing entry's
  status becomes the HTTP status of the response.
- **`batch`** applies each entry independently. A failing entry carries its own
  `response.outcome` and does not affect the others.

```json title="Response"
{
  "resourceType": "Bundle",
  "type": "transaction-response",
  "entry": [
    {
      "resource": {"resourceType": "Patient", "id": "1dc5f63e-…", "meta": {"versionId": "1"}},
      "response": {
        "status": "201 Created",
        "location": "http://localhost:9090/fhir/r4/Patient/1dc5f63e-…/_history/1",
        "etag": "W/\"1\""
      }
    },
    {
      "resource": {"resourceType": "Observation", "id": "8bbfa5c5-…", "meta": {"versionId": "1"}},
      "response": {
        "status": "201 Created",
        "location": "http://localhost:9090/fhir/r4/Observation/8bbfa5c5-…/_history/1",
        "etag": "W/\"1\""
      }
    }
  ]
}
```

The response is a `transaction-response` or `batch-response` Bundle whose entries mirror the request
order, each with `response.status`, and `response.location`/`response.etag` where applicable.

Entries may also carry `request.ifNoneExist`, `request.ifMatch`, and conditional URLs — see
[Conditional operations](./conditional.md#inside-bundles) for the differences from the REST forms.

## Response metadata

| Header | Behaviour |
| --- | --- |
| `Content-Type` | The negotiated FHIR media type |
| `ETag` | Weak validator, `W/"{versionId}"`. Returned on read, create, update, patch, and conditional create/update — not on versioned read, search, or history |
| `Location` | On create, the new resource's versioned URL |

`Last-Modified` is not sent, and conditional reads (`If-None-Match`, `If-Modified-Since`) are not
evaluated — every read returns a full body. Use `meta.lastUpdated` or the `_lastUpdated` search
parameter to detect change.

Errors are returned as an `OperationOutcome` whenever the response can be represented as FHIR.
