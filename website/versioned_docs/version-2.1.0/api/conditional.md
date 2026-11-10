---
title: Conditional operations
description: Address resources by search criteria, and guard writes with version preconditions.
---

# Conditional operations

A conditional request tells the server to act only if some condition holds. Two kinds are
supported, and this page covers both:

- **Conditional on search criteria** — address a resource by a business identifier instead of a
  server-assigned id, with `If-None-Exist` on create and a query on update or delete.
- **Conditional on version** — guard a write with `If-Match`, so a stale update fails instead of
  silently overwriting a concurrent one.

All four conditional forms are available at the REST layer and inside
[transaction and batch Bundles](./interactions.md#transaction-and-batch-bundles).

## Conditional create

Send `If-None-Exist` with a search query on a normal create. The server creates the resource only
when nothing matches.

```bash title="Request"
curl -i -X POST "http://localhost:9090/fhir/r4/Patient" \
  -H "Content-Type: application/fhir+json" \
  -H 'If-None-Exist: identifier=http://hospital.example|MRN-123' \
  -d '{
    "resourceType": "Patient",
    "identifier": [{"system": "http://hospital.example", "value": "MRN-123"}],
    "name": [{"family": "Smith"}]
  }'
```

| Matches | Status | Response |
| --- | --- | --- |
| none | `201 Created` | the new resource, `Location: …/Patient/{id}/_history/1`, `ETag: W/"1"` |
| exactly one | `200 OK` | the **existing** resource unchanged, `Location: …/Patient/{id}` (no version segment), `ETag` of that resource's current version |
| two or more | `412 Precondition Failed` | `OperationOutcome`, `code=conflict` |

On a **single match** the response is the resource already stored — note the `Location` has no
version segment and the body is the *existing* content, not what you sent:

```text title="Response headers"
HTTP/1.1 200 OK
ETag: W/"1"
Location: http://localhost:9090/fhir/r4/Patient/1dc5f63e-6b16-407a-a436-5ff9265141ef
```

On **two or more matches** nothing is written:

```json title="Response body"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "error",
      "code": "conflict",
      "diagnostics": "If-None-Exist matched 2 resources"
    }
  ]
}
```

## Conditional update

`PUT /{type}?<query>` updates the single resource matching the query.

```bash title="Request"
curl -i -X PUT "http://localhost:9090/fhir/r4/Patient?identifier=http://hospital.example%7CMRN-123" \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Patient",
    "identifier": [{"system": "http://hospital.example", "value": "MRN-123"}],
    "active": true
  }'
```

| Matches | Status | Response |
| --- | --- | --- |
| none | `201 Created` | the resource is created — id taken from the request body when present, otherwise server-assigned. `Location: …/_history/1`, `ETag: W/"1"` |
| exactly one | `200 OK` | the updated resource with its new version in `ETag`. No `Location` header |
| two or more | `412 Precondition Failed` | `OperationOutcome`, `code=conflict` |

On a single match the update returns the new version:

```text title="Response headers"
HTTP/1.1 200 OK
ETag: W/"2"
```

```json title="Response body"
{
  "resourceType": "Patient",
  "id": "1dc5f63e-6b16-407a-a436-5ff9265141ef",
  "meta": {"versionId": "2", "lastUpdated": "2026-09-03T06:20:24Z"},
  "identifier": [{"system": "http://hospital.example", "value": "MRN-123"}],
  "active": true
}
```

A request with no search criteria is rejected with `400 Bad Request` — the server never interprets
an empty query as "match everything".

:::warning
`If-Match` is **not** evaluated on the conditional-update path. Send `If-Match` only with
`PUT /{type}/{id}`, where it is enforced.
:::

## Conditional delete

`DELETE /{type}?<query>` deletes at most one resource. The CapabilityStatement advertises
`conditionalDelete: "single"`.

```bash title="Request"
curl -i -X DELETE "http://localhost:9090/fhir/r4/Patient?identifier=http://hospital.example%7CMRN-123"
```

| Matches | Status | Notes |
| --- | --- | --- |
| none | `204 No Content` | treated as already-absent, not an error |
| exactly one | `204 No Content` | soft delete — history is preserved |
| two or more | `412 Precondition Failed` | multiple delete is not supported |

Two or more matches refuse the delete:

```json title="Response"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "error",
      "code": "conflict",
      "diagnostics": "conditional delete matched 2 resources; multiple delete is not supported"
    }
  ]
}
```

An empty query is rejected with `400 Bad Request`, so there is no accidental mass delete.

## Optimistic locking with If-Match

Send the version you believe is current. The write proceeds only if the server agrees.

```bash title="Request"
curl -i -X PUT "http://localhost:9090/fhir/r4/Patient/{id}" \
  -H "Content-Type: application/fhir+json" \
  -H 'If-Match: W/"1"' \
  -d '{"resourceType": "Patient", "id": "{id}", "active": false}'
```

| Condition | Status |
| --- | --- |
| version matches | `200 OK`, resource written at version + 1 |
| version does not match | `412 Precondition Failed`, `code=conflict`, diagnostics naming both versions |
| malformed header value | `412 Precondition Failed` |
| header absent | write proceeds — last write wins |

```json title="Response"
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

Accepted value forms, all verified against the server:

| `If-Match` value | Result on a version-1 resource |
| --- | --- |
| `W/"1"` | accepted |
| `"1"` | accepted |
| `1` | accepted |
| `W/1` | accepted |
| `w/"1"` | `412` — the `W/` prefix is case-sensitive |
| `*` | `412` — not supported |
| a list, e.g. `W/"1", W/"2"` | `412` — not supported |

Even without `If-Match`, concurrent writers to the same resource are serialized by a row lock taken
inside the write transaction, so no update is ever lost — `If-Match` is what turns a silent
overwrite into an explicit `412`.

:::note
`If-Match` is honoured on `PUT /{type}/{id}` only. It is accepted but not enforced on `PATCH`,
`DELETE`, and conditional update.
:::

## ETag and version semantics

Every write increments `meta.versionId`, starting at 1. Deletes increment it too, so the delete
itself is a retrievable version.

- ETags are always **weak**: `W/"{versionId}"`.
- `ETag` is returned on read, create, update, all patch formats, and conditional create/update.
- `ETag` is **not** returned on versioned read, search Bundles, history Bundles, or operation
  responses.
- `Last-Modified` is not sent, and `If-None-Match` / `If-Modified-Since` are not evaluated — there
  is no `304 Not Modified` path. Use `meta.lastUpdated` or `_lastUpdated` search instead.

## Inside Bundles

The same four forms work as Bundle entries, with a few deliberate differences.

| Entry form | Field | Behaviour difference vs REST |
| --- | --- | --- |
| Conditional create | `request.ifNoneExist` | On a single match the entry reports `200 OK` and a location, but carries no `resource` and no `etag` |
| Conditional update | query in `request.url` | Same as REST, including create-on-zero-matches |
| Conditional delete | query in `request.url` | Same status codes as REST |
| Plain `DELETE Type/id` | — | A missing id yields `204 No Content`, whereas the REST route returns `404 Not Found` |
| Any entry | `request.ifMatch` | A malformed value fails the entry with `400`, whereas the REST route returns `412` |

In a **transaction**, any entry failure aborts and rolls back the whole Bundle, and the failing
status becomes the HTTP status of the response. In a **batch**, each entry succeeds or fails
independently and carries its own `response.outcome`.

:::warning
Conditional criteria in a transaction are resolved **before** the transaction opens, and are not
re-evaluated inside it. An entry's conditional match therefore does not see resources created by
earlier entries of the same Bundle.
:::

## Known limitations

- Match counting stops at two, so every "two or more" diagnostic reports exactly two resources
  regardless of how many actually match.
- Conditional update does not check a body `id` against the matched resource; the matched id wins.
- `If-Match` combined with a conditional Bundle `PUT` that matches nothing is ignored — the entry
  creates the resource at version 1 rather than failing the precondition.

## Verify on your server

```bash title="Request"
curl -sS http://localhost:9090/fhir/r4/metadata \
  | jq '.rest[0].resource[] | select(.type=="Patient") |
        {conditionalCreate, conditionalUpdate, conditionalDelete, updateCreate, versioning}'
```
