---
title: Operations
description: Reference for $reindex, $validate, $everything, $lastn, $document, $convert, and the $meta family.
---

# Operations

Alongside the REST interactions, the server supports nine FHIR operations out of the box. Each one
is advertised in the [CapabilityStatement](./capability-statement.md) under `rest[0].operation`.

| Operation | Scopes | Method |
| --- | --- | --- |
| [`$reindex`](#reindex) | system (selected tenant) | `POST` |
| [`$validate`](#validate) | system · type · instance | `POST` |
| [`$everything`](#everything) | type (Patient, Encounter, Group) · instance (any type) | `GET` |
| [`$lastn`](#lastn) | type (`Observation` only) | `GET` |
| [`$document`](#document) | instance (`Composition` only) | `GET` |
| [`$convert`](#convert) | system | `POST` |
| [`$meta`](#meta) | system · type · instance | `GET` |
| [`$meta-add`](#meta-add-and-meta-delete) | instance | `POST` |
| [`$meta-delete`](#meta-add-and-meta-delete) | instance | `POST` |

Only the method shown is registered for each path; another method on the same path returns
`405 Method Not Allowed`. Every operation is also available under a tenant prefix
(`/t/{tenant}/fhir/r4/…`) and honours the usual [format negotiation](./interactions.md).

## $reindex

[OperationDefinition](https://wso2.github.io/fhir-server/OperationDefinition/reindex.json).

Start a background job to rebuild search indexes for the selected tenant. Use it after adding or
changing a custom or IG search parameter to make existing resources searchable with that definition.
The server rebuilds all search parameters for each selected resource and preserves its content,
`meta.versionId`, `meta.lastUpdated`, and history.

| Request | Path |
| --- | --- |
| Start a job | `POST /t/{tenant}/fhir/r4/$reindex` |
| Read its status | `GET /t/{tenant}/fhir/r4/_operations/reindex/{jobId}` |

The bare paths under `/fhir/r4` select the default tenant. A job covers that tenant alone; its status
returns `404` from another tenant's path. The server uses the deployment's existing external
authorization layer for these routes. It has no built-in admin role.

### Start a job

An empty body selects all resource types with a batch size of 100. To restrict the pass or choose a
batch size, send a FHIR `Parameters` resource:

| Input | FHIR value | Meaning |
| --- | --- | --- |
| `resourceType` | `valueCode` | Optional single FHIR R4 resource type; omit for all types |
| `batchSize` | `valueInteger` | Resources selected per transaction; defaults to `100`, range `1`–`1000` |

```bash title="Request"
curl -i -X POST 'http://localhost:9090/t/acme/fhir/r4/$reindex' \
  -H 'Content-Type: application/fhir+json' \
  -d '{
    "resourceType": "Parameters",
    "parameter": [
      {"name": "resourceType", "valueCode": "Patient"},
      {"name": "batchSize", "valueInteger": 100}
    ]
  }'
```

The server returns `202 Accepted` with a status resource and both `Location` and `Content-Location`
pointing to its polling URL:

```text title="Response headers"
HTTP/1.1 202 Accepted
Location: http://localhost:9090/t/acme/fhir/r4/_operations/reindex/9eea24ec-2469-4e09-b9da-493c9f441122
Content-Location: http://localhost:9090/t/acme/fhir/r4/_operations/reindex/9eea24ec-2469-4e09-b9da-493c9f441122
```

```json title="Response"
{
  "resourceType": "Parameters",
  "parameter": [
    {"name": "id", "valueString": "9eea24ec-2469-4e09-b9da-493c9f441122"},
    {"name": "status", "valueString": "queued"},
    {"name": "resourceType", "valueCode": "Patient"},
    {"name": "batchSize", "valueInteger": 100},
    {"name": "resourcesReindexed", "valueInteger": 0},
    {"name": "queuedTime", "valueInstant": "2026-09-30T10:00:00Z"},
    {"name": "lastModified", "valueInstant": "2026-09-30T10:00:00Z"}
  ]
}
```

The server accepts one queued or running job per tenant. Starting another returns `409 Conflict`;
jobs in other tenants can proceed. Invalid inputs return `400 Bad Request` with an `OperationOutcome`.

### Read status

Poll the URL from the response headers:

```bash title="Request"
curl -sS \
  'http://localhost:9090/t/acme/fhir/r4/_operations/reindex/9eea24ec-2469-4e09-b9da-493c9f441122' | jq
```

A known job returns `200 OK`, including when it has failed. Its FHIR `Parameters` resource contains:

| Output | FHIR value | Meaning |
| --- | --- | --- |
| `id` | `valueString` | Job identifier |
| `status` | `valueString` | `queued`, `running`, `completed`, or `failed` |
| `resourceType` | `valueCode` | Present when the request selected one type |
| `batchSize` | `valueInteger` | Requested or default batch size |
| `resourcesReindexed` | `valueInteger` | Active resources reindexed in committed batches |
| `queuedTime` | `valueInstant` | Time the server accepted the job |
| `lastModified` | `valueInstant` | Time of the latest committed job update |
| `startTime` | `valueInstant` | Present after the worker starts the job |
| `endTime` | `valueInstant` | Present after completion or failure |
| `error` | `valueString` | Failure details, present for a failed job |

For example, a completed pass includes:

```json title="Response"
{
  "resourceType": "Parameters",
  "parameter": [
    {"name": "id", "valueString": "9eea24ec-2469-4e09-b9da-493c9f441122"},
    {"name": "status", "valueString": "completed"},
    {"name": "resourceType", "valueCode": "Patient"},
    {"name": "batchSize", "valueInteger": 100},
    {"name": "resourcesReindexed", "valueInteger": 250},
    {"name": "queuedTime", "valueInstant": "2026-09-30T10:00:00Z"},
    {"name": "lastModified", "valueInstant": "2026-09-30T10:00:04Z"},
    {"name": "startTime", "valueInstant": "2026-09-30T10:00:01Z"},
    {"name": "endTime", "valueInstant": "2026-09-30T10:00:04Z"}
  ]
}
```

`resourcesReindexed` counts completed work, not a percentage. The worker scans stored resource rows
up to the highest key captured when you start the job, skipping deleted resources. Concurrent creates
and deletes can change the number of active rows it encounters.

### Execution and recovery

Each batch locks its selected resource rows, replaces their index rows, and commits the index changes
with the job's progress. Concurrent writes to those resources wait for that transaction; other
requests remain available. Searches using an affected parameter can return incomplete results until
the pass completes.

The server persists jobs in PostgreSQL. After a restart, a worker resumes unfinished jobs from the
last committed batch. Workers across replicas coordinate through database locks, so two workers
cannot process the same job batch concurrently. A failed batch rolls back while earlier batches
remain committed. Inspect `error`, correct the cause, and start a new job to retry a failed pass.

The worker reloads definitions from the database for each batch. If definitions change after you
start the job, it marks the job `failed`; start a new pass once the definitions have settled. In a
multiple-replica deployment, first let the write replicas converge through `SEARCH_PARAM_WATCH=true`,
or restart replicas with stale definitions. Reindexing cannot prevent later writes from a stale
replica from omitting new indexes. See [Deployment](../administration/deployment.md#reindex-jobs).

This operation uses the existing search extractor and its supported FHIRPath subset. Reindexing does
not extend expression support. The server starts jobs only when you call `$reindex`; registering a
search parameter does not start one.

## $validate

Validate a resource without storing it.

```bash title="Request"
curl -sS -X POST 'http://localhost:9090/fhir/r4/Observation/$validate' \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType": "Observation", "status": "final", "code": {"text": "Heart rate"}}' | jq
```

| Scope | Path |
| --- | --- |
| System | `POST /$validate` — resource type is taken from the body |
| Type | `POST /{type}/$validate` — a body `resourceType` that disagrees with the URL is rejected with `422` |
| Instance | `POST /{type}/{id}/$validate` — **with an empty body the stored resource is validated** |

Responses are always an `OperationOutcome`:

| Outcome | Status |
| --- | --- |
| No `error`-severity issues | `200 OK` — the resource is acceptable |
| Any `error`-severity issue | `422 Unprocessable Entity` with all issues |

A resource that passes returns `200`, but the outcome is **not empty**: base validation reports
every unevaluated FHIRPath invariant as a `warning`, so expect a few dozen of them. Only
`severity: "error"` matters for the verdict.

```json title="Response"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "warning",
      "code": "invariant",
      "diagnostics": "A resource should have narrative for robust management",
      "expression": ["Observation"]
    },
    {
      "severity": "warning",
      "code": "invariant",
      "diagnostics": "All FHIR elements must have a @value or children",
      "expression": ["Observation.meta"]
    }
  ]
}
```

A failure puts the `error` issue first, with the precise element in `expression`:

```json title="Response"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "error",
      "code": "required",
      "diagnostics": "Observation.status: minimum cardinality is 1 but element is absent",
      "expression": ["Observation.status"]
    }
  ]
}
```

:::tip
Filter for what matters rather than reading the whole outcome:

```bash title="Request"
curl -sS -X POST 'http://localhost:9090/fhir/r4/Observation/$validate' \
  -H "Content-Type: application/fhir+json" -d @observation.json \
  | jq '[.issue[] | select(.severity=="error")]'
```
:::

Choose the profiles to validate against with `?profile=`, which takes precedence over the resource's
own `meta.profile`:

```bash title="Request"
curl -sS -X POST 'http://localhost:9090/fhir/r4/Patient/$validate?profile=http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient' \
  -H "Content-Type: application/fhir+json" -d @patient.json | jq
```

Behaviour worth knowing:

- The body may be the **resource itself** or a `Parameters` envelope whose parameter named
  `resource` carries it — the envelope is unwrapped at every scope. An envelope without a
  `resource` parameter is rejected with `400 Bad Request`, except `mode=delete`, which returns
  a valid outcome without checking anything (deletes carry no resource to validate). Other
  `mode` values do not change what is validated.
- Only the first `?profile=` value is read.
- Profile resolution is **soft-fail**: a profile that is not loaded is skipped silently, so an
  unrecognised `?profile=` returns `200 OK` rather than an error. Confirm packages loaded with
  [`/metadata`](./capability-statement.md).
- Base R4 validation is included unless `FHIR_VALIDATION_BASE=false`. FHIRPath `invariant` failures
  are reported as warnings — see [Validation](../conformance/validation.md).

## $everything

Return a resource together with the resources around it, as a `searchset` Bundle.

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient/{id}/\$everything?_type=Observation,Condition&_since=2026-01-01T00:00:00Z" | jq
```

**Instance level** — `GET /{type}/{id}/$everything`, available for any resource type.

| Parameter | Effect |
| --- | --- |
| `_type` | Comma-separated or repeated resource types to keep. Applies to the related resources only |
| `_since` | RFC 3339 instant; keeps related resources updated strictly after it. A malformed value returns `400` |

```json title="Response"
{
  "resourceType": "Bundle",
  "type": "searchset",
  "total": 3,
  "entry": [
    {"resource": {"resourceType": "Patient",     "id": "b74d6c5a-…"}, "search": {"mode": "match"}},
    {"resource": {"resourceType": "Observation", "id": "3f1a…"},      "search": {"mode": "include"}},
    {"resource": {"resourceType": "Condition",   "id": "9c22…"},      "search": {"mode": "include"}}
  ]
}
```

The graph is **one hop** in each direction: references *from* the anchor and references *to* it.
Traversal is not transitive, so a reference two steps away is not included. The anchor is always
present with `search.mode = match`; everything else is `include`. `Bundle.total` is the number of
entries returned, and there is no paging.

**Type level** — `GET /{type}/$everything`, supported only for `Patient`, `Encounter`, and `Group`.
Any other type returns `404` with `code=not-supported`.

:::warning
Type-level `$everything` reads at most **1000** anchor resources and ignores `_type`, `_since`, and
`_count`. Treat it as a convenience for small datasets, not an export mechanism.
:::

## $lastn

Return the most recent `Observation` resources, grouped by code.

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Observation/\$lastn?patient=Patient/{id}&max=3" | jq
```

Defined for `Observation` only; any other type returns `404` `not-supported`.

| Parameter | Effect |
| --- | --- |
| `max` | Number of observations to keep **per distinct code**. Defaults to `1`; a non-numeric or non-positive value silently falls back to `1` |
| `patient`, `subject`, `category`, `code` | Filters applied to the candidate set |

```json title="Response"
{
  "resourceType": "Bundle",
  "type": "searchset",
  "total": 1,
  "entry": [
    {
      "resource": {
        "resourceType": "Observation",
        "id": "3f1a…",
        "status": "final",
        "code": {"coding": [{"system": "http://loinc.org", "code": "8867-4"}]},
        "effectiveDateTime": "2026-03-01"
      },
      "search": {"mode": "match"}
    }
  ]
}
```

Grouping is by each `code` coding (`system` + `code`), ordered by the observation date descending,
falling back to the resource's last-updated time. An observation carrying several codings can rank
in more than one group but is returned once. All entries have `search.mode = match`.

:::note
Filters other than the four listed — including `_count`, `_sort`, `_include`, and `date` — are
ignored on this operation. Use ordinary [search](./search.md) when you need them.
:::

## $document

Assemble a `Composition` and everything it references into a `document` Bundle.

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Composition/{id}/\$document" \
  | jq '{type, identifier, entries: [.entry[].resource.resourceType]}'
```

Defined for `Composition` only; any other type returns `404` `not-supported`.

- Traversal follows **forward references transitively** (breadth-first) from the Composition, so
  sections, subjects, authors, and their onward references are pulled in. Reverse references are not
  followed.
- Assembly stops at **500** entries.
- The Composition is always the first entry.
- The response is `Bundle.type = document` with a freshly generated `identifier`
  (`urn:uuid:…`) and a `timestamp`. Entries carry `fullUrl` and `resource` but no `search` element,
  and there is no `total`.

```json title="Response"
{
  "resourceType": "Bundle",
  "type": "document",
  "identifier": {"system": "urn:ietf:rfc:3986", "value": "urn:uuid:5f2c…"},
  "timestamp": "2026-09-03T06:21:13Z",
  "entry": [
    {"fullUrl": "http://localhost:9090/fhir/r4/Composition/…", "resource": {"resourceType": "Composition"}},
    {"fullUrl": "http://localhost:9090/fhir/r4/Patient/…",     "resource": {"resourceType": "Patient"}}
  ]
}
```

The document is generated per request. It is not persisted and not signed, so a repeated call
produces a new identifier.

## $convert

Re-serialize a resource from one FHIR wire format to another.

```bash title="Request"
# XML in, JSON out
curl -sS -X POST 'http://localhost:9090/fhir/r4/$convert' \
  -H "Content-Type: application/fhir+xml" \
  -H "Accept: application/fhir+json" \
  --data-binary @patient.xml | jq
```

```xml title="Response"
<?xml version="1.0" encoding="UTF-8"?>
<Patient xmlns="http://hl7.org/fhir">
  <active value="true"></active>
  <name>
    <family value="Smith"></family>
  </name>
</Patient>
```

- Input format comes from `Content-Type`; output format from `?_format=` (highest priority) or
  `Accept`. Supported formats are `application/fhir+json`, `application/fhir+xml`, and
  `application/fhir+turtle`.
- Any resource type is accepted, including Bundles.
- It is a pure format conversion: the resource is parsed and re-serialized unchanged. There is no
  validation and no FHIR version conversion.
- A body that cannot be parsed returns `400`.

## $meta

Read the metadata in use — `tag`, `security`, and `profile`.

```bash title="Request"
# across every resource type
curl -sS "http://localhost:9090/fhir/r4/\$meta" | jq '.parameter[0].valueMeta'

# for one type
curl -sS "http://localhost:9090/fhir/r4/Observation/\$meta" | jq '.parameter[0].valueMeta'

# for one resource
curl -sS "http://localhost:9090/fhir/r4/Patient/{id}/\$meta" | jq '.parameter[0].valueMeta'
```

```json title="Response"
{
  "resourceType": "Parameters",
  "parameter": [
    {
      "name": "return",
      "valueMeta": {
        "tag": [{"system": "http://example.org/tags", "code": "reviewed"}]
      }
    }
  ]
}
```

All three scopes return `200 OK` with a `Parameters` resource holding a single parameter named
`return` whose `valueMeta` is the result. When nothing is in use, `valueMeta` is `{}`.

- **System and type scope** aggregate the *distinct* values found across stored resources. Codings
  are identified by `system` + `code`; `display` is deliberately excluded, so the same code with
  different display text collapses to one entry.
- **Instance scope** returns that resource's own `meta`, and returns `404`/`410` for a missing or
  deleted resource.

:::warning[PROD]
System- and type-scope `$meta` aggregate across the whole database without tenant scoping. In a
multi-tenant deployment do not expose these two scopes to tenants — restrict them at the gateway and
use instance scope instead. See [Multi-tenancy](../administration/multi-tenancy.md).
:::

## $meta-add and $meta-delete

Add or remove `meta` entries on one resource. Both are instance-scope `POST` only.

```bash title="Request"
curl -sS -X POST "http://localhost:9090/fhir/r4/Patient/{id}/\$meta-add" \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Parameters",
    "parameter": [{
      "name": "meta",
      "valueMeta": {
        "tag": [{"system": "http://example.org/tags", "code": "reviewed"}]
      }
    }]
  }' | jq '.parameter[0].valueMeta'
```

```json title="Response"
{
  "resourceType": "Parameters",
  "parameter": [
    {
      "name": "return",
      "valueMeta": {
        "versionId": "2",
        "lastUpdated": "2026-09-03T06:21:13Z",
        "tag": [{"system": "http://example.org/tags", "code": "reviewed"}]
      }
    }
  ]
}
```

The body must be a `Parameters` resource containing a parameter named `meta` with a `valueMeta`
value; anything else returns `400`. The response is the resource's `meta` after the change — note
`versionId` has advanced, because the change is applied as a normal update.

| Field | Behaviour |
| --- | --- |
| `tag`, `security` | Coding lists. Identity is `system` + `code` — `display` is ignored, so a delete removes a matching code regardless of its display text |
| `profile` | String list matched exactly |
| Any other `meta` field | Ignored — `versionId`, `lastUpdated`, `source`, and extensions cannot be set this way |

`$meta-add` unions the supplied entries into the existing list, keeping existing entries as they
are. `$meta-delete` removes matching entries. A list left empty is dropped from `meta` entirely.

:::note
Both operations are applied as an ordinary **versioned update**: `meta.versionId` is incremented,
`meta.lastUpdated` is refreshed, and a history entry is written. `If-Match` is not evaluated on this
path, and neither base nor profile validation runs.
:::

## Verify on your server

```bash title="Request"
curl -sS http://localhost:9090/fhir/r4/metadata \
  | jq '[.rest[0].operation[] | {name, definition}] | unique_by(.definition)'
```
