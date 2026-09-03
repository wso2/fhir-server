---
title: CapabilityStatement
description: Read the server's machine-readable conformance contract at /metadata.
---

# CapabilityStatement

`GET /metadata` returns a FHIR `CapabilityStatement` describing what the running server actually
supports. It is generated at request time from the live search-parameter registry and the
Implementation Guide packages loaded at startup — not from a static file — so it always reflects
that deployment.

```bash title="Request"
curl -sS http://localhost:9090/fhir/r4/metadata | jq
```

:::tip
Treat `/metadata` as authoritative over any documentation, including this site. A deployment that
loads Implementation Guides or defines custom `SearchParameter` resources supports more than the
shipped defaults.
:::

## Server-level fields

| Field | Value on a default deployment |
| --- | --- |
| `fhirVersion` | `4.0.1` |
| `status` / `kind` | `active` / `instance` |
| `format` | `application/fhir+json`, `application/fhir+xml`, `application/fhir+turtle` |
| `patchFormat` | `application/json-patch+json`, `application/merge-patch+json`, `application/xml-patch+xml`, `application/fhir+json`, `application/fhir+xml` |
| `rest[0].mode` | `server` |
| `rest[0].interaction` | `transaction`, `batch`, `history-system` |
| `rest[0].operation` | the server's [operations](./operations.md) |
| `implementationGuide` | canonical URL per loaded package |
| `software` | name and version of the build |

## Per-resource entries

`rest[0].resource` carries one entry per resource type the registry knows, which is the full FHIR R4
base set plus anything contributed by loaded IG packages.

```bash title="Request"
# how many resource types this server exposes
curl -sS http://localhost:9090/fhir/r4/metadata | jq '.rest[0].resource | length'
```

```json title="Response"
135
```

Every R4 resource type can be stored, read, and searched. This count is the number of types the
server advertises **with search parameters**; see [Resource types](../conformance/resource-types.md).

Each entry reports:

| Field | Meaning |
| --- | --- |
| `interaction` | `read`, `vread`, `update`, `patch`, `delete`, `create`, `search-type` |
| `versioning` | `versioned` — every write appends a retrievable version |
| `readHistory` | `true` — past versions are readable via [history and vread](./interactions.md) |
| `conditionalCreate` / `conditionalUpdate` | `true` — see [Conditional operations](./conditional.md) |
| `conditionalDelete` | `single` — a conditional delete may match at most one resource |
| `updateCreate` | `false` — `PUT` to an unknown id does not create the resource |
| `referencePolicy` | `literal`, `logical` — both `Type/id` and identifier-based references are indexed |
| `searchParam` | every parameter available for the type, base plus IG plus custom |
| `searchInclude` | reference parameters usable as `_include` targets |
| `searchRevInclude` | parameters usable as `_revinclude` targets |
| `supportedProfile` | profiles contributed by loaded IGs, when present |

```bash title="Request"
curl -sS http://localhost:9090/fhir/r4/metadata \
  | jq '.rest[0].resource[] | select(.type=="Patient") |
        {conditionalCreate, conditionalUpdate, conditionalDelete, updateCreate, versioning, readHistory, referencePolicy}'
```

```json title="Response"
{
  "conditionalCreate": true,
  "conditionalUpdate": true,
  "conditionalDelete": "single",
  "updateCreate": false,
  "versioning": "versioned",
  "readHistory": true,
  "referencePolicy": ["literal", "logical"]
}
```

## Useful queries

Confirm a specific search parameter is available before a client relies on it:

```bash title="Request"
curl -sS http://localhost:9090/fhir/r4/metadata \
  | jq '.rest[0].resource[] | select(.type=="Observation") | [.searchParam[].name]'
```

```json title="Response"
[
  "_id", "_lastUpdated", "_text", "_content", "_tag", "_profile", "_security",
  "_source", "_language", "_list", "based-on", "category", "code", "date",
  "patient", "subject", "value-quantity"
]
```

List the operations this server advertises:

```bash title="Request"
curl -sS http://localhost:9090/fhir/r4/metadata | jq '[.rest[0].operation[].name] | unique'
```

```json title="Response"
["convert", "document", "everything", "lastn", "meta", "meta-add", "meta-delete", "validate"]
```

Verify which Implementation Guides loaded successfully:

```bash title="Request"
curl -sS http://localhost:9090/fhir/r4/metadata | jq '.implementationGuide'
```

```json title="Response"
[]
```

An empty array means no Implementation Guides are loaded. See
[Implementation Guides](../conformance/implementation-guides.md).

Check the profiles a type is constrained by:

```bash title="Request"
curl -sS http://localhost:9090/fhir/r4/metadata \
  | jq '.rest[0].resource[] | select(.type=="Patient") | .supportedProfile'
```

```json title="Response"
null
```

`supportedProfile` is absent until an IG contributing profiles for that type is loaded.

## Format negotiation

`/metadata` honours the same content negotiation as every other endpoint:

```bash title="Request"
curl -sS -H 'Accept: application/fhir+xml' http://localhost:9090/fhir/r4/metadata
```

```xml title="Response"
<?xml version="1.0" encoding="UTF-8"?>
<CapabilityStatement xmlns="http://hl7.org/fhir">
  <status value="active"></status>
  <kind value="instance"></kind>
  <fhirVersion value="4.0.1"></fhirVersion>
  ...
</CapabilityStatement>
```

## In multi-tenant deployments

Requesting `/metadata` under a tenant prefix returns the same capability set with tenant-aware URLs:

```bash title="Request"
curl -sS http://localhost:9090/t/acme/fhir/r4/metadata | jq '.rest[0].resource | length'
```

```json title="Response"
135
```

See [Multi-tenancy](../administration/multi-tenancy.md).
