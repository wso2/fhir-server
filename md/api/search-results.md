---
title: Search results and paging
description: Control page size, ordering, totals, response shape, and filtering of search results.
---

# Search results and paging

Result parameters change how matches are counted, ordered, paged, and shaped. They never change
which resources match — for that see [Search](./search.md) and
[Search across references](./search-joins.md).

## Paging

Paging is offset-based, using `_count` for the page size and `_page` for a 1-based page number.

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient?_count=50&_page=2" | jq '.link'
```

| Parameter | Behaviour |
| --- | --- |
| `_count` | Page size. Defaults to `search.defaultPageSize` (20) and is clamped by `search.maxPageSize` when that is set |
| `_page` | Page number, 1-based. This is a server extension, not a FHIR-defined parameter |

The response Bundle carries navigation links:

| Link | When present |
| --- | --- |
| `self`, `first` | Always |
| `previous` | When `_page` is greater than 1 |
| `next` | When a total is known and more pages remain; otherwise when the current page came back full |
| `last` | Only when a total was computed — see [`_total`](#totals) |

```json title="Response"
{
  "link": [
    {"relation": "self",     "url": ".../Patient?_count=2&_page=2"},
    {"relation": "first",    "url": ".../Patient?_count=2&_page=1"},
    {"relation": "previous", "url": ".../Patient?_count=2&_page=1"},
    {"relation": "next",     "url": ".../Patient?_count=2&_page=3"}
  ]
}
```

:::note
`_count=0` does **not** mean "count only" on this server; it falls back to the default page size.
Use `_summary=count` to get a count without entries.
:::

## Totals

Computing an exact total requires counting every match, so it is **opt-in**.

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient?_total=accurate" | jq '.total'
```

| `_total` | Behaviour |
| --- | --- |
| absent (**default**) | `Bundle.total` is **omitted** and no count query runs |
| `accurate` | Exact `COUNT(*)` over the full match set |
| `estimate` | Currently identical to `accurate` — an exact count |

The difference is visible in the links — without a total there is no `last`:

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient?_count=2" | jq '{total, links:[.link[].relation]}'
```

```json title="Response"
{ "total": null, "links": ["self", "first", "next"] }
```

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient?_count=2&_total=accurate" | jq '{total, links:[.link[].relation]}'
```

```json title="Response"
{ "total": 23, "links": ["self", "first", "last", "next"] }
```

Because the default omits the total, a client that needs `last` links or a match count must ask for
`_total=accurate` (or use `_summary=count`).

## Ordering

`_sort` takes a comma-separated list of keys, applied in order. Prefix a key with `-` for
descending.

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient?_sort=family,-_lastUpdated" | jq '[.entry[].resource.id]'
```

Sortable keys are `_id`, `_lastUpdated`, and any search parameter backed by a sortable indexed
value — string, token, date, number, quantity, uri, and reference parameters. Without `_sort`,
results are ordered by last-updated descending.

:::note
A `_sort` key that cannot be resolved — an unknown parameter, a composite parameter, or `_score` —
is skipped silently rather than rejected. Check that ordering is what you expect rather than
assuming an unsupported key errored.
:::

## Shaping the response

Two parameters reduce the payload. Both add a `SUBSETTED` tag to `meta.tag` so clients know the
resources are incomplete, and `_elements` wins when both are supplied.

```bash title="Request"
# only these top-level elements
curl -sS "http://localhost:9090/fhir/r4/Patient?_elements=name,birthDate" | jq '.entry[0].resource'

# a count with no entries
curl -sS "http://localhost:9090/fhir/r4/Patient?_summary=count" | jq '{total, entries: (.entry | length)}'
```

| `_summary` | Behaviour |
| --- | --- |
| `count` | Returns the total only, with no entries |
| `true` | Keeps the R4 summary element set for the type |
| `text` | Keeps `text`, `id`, `meta`, and `resourceType` |
| `data` | Drops `text` |
| `false` | Full resources — the default |

A reduced resource is tagged `SUBSETTED` so clients can tell it is incomplete:

```json title="Response"
{
  "resourceType": "Patient",
  "id": "25178ae2-f6f2-429d-a540-3b44ef510a05",
  "meta": {
    "versionId": "1",
    "lastUpdated": "2026-09-03T06:22:16Z",
    "tag": [
      {"system": "http://terminology.hl7.org/CodeSystem/v3-ObservationValue", "code": "SUBSETTED"}
    ]
  },
  "name": [{"family": "Alison"}]
}
```

`_elements` accepts a comma-separated list of **top-level** element names. `resourceType`, `id`, and
`meta` are always retained. Dotted paths such as `name.family` are not supported.

## Filtering by metadata

These parameters work on every resource type:

| Parameter | Type | Example |
| --- | --- | --- |
| `_id` | id | `_id=8ccfcd49-…` (comma-separated values are OR'd) |
| `_lastUpdated` | date | `_lastUpdated=ge2026-01-01` |
| `_tag` | token | `_tag=http://example.org/tags%7Creviewed` |
| `_profile` | uri | `_profile=http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient` |
| `_security` | token | `_security=http://terminology.hl7.org/CodeSystem/v3-Confidentiality%7CR` |
| `_source` | uri | `_source=http://example.org/feed` |
| `_language` | token | `_language=en` |

:::note
`_lastUpdated` fully supports the `gt`, `lt`, `ge`, and `le` prefixes. The `eq`, `ne`, `sa`, `eb`,
and `ap` prefixes all behave as a plain range match at the stated precision, so `ne` does not negate.
:::

## Searching a List

`_list` restricts a search to the members of a `List` resource.

First create the `List`, then search against its id:

```bash title="Request"
LIST_ID=$(curl -sS -X POST "http://localhost:9090/fhir/r4/List" \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "List",
    "status": "current",
    "mode": "working",
    "entry": [
      {"item": {"reference": "Patient/<id-1>"}},
      {"item": {"reference": "Patient/<id-2>"}}
    ]
  }' | jq -r .id)

curl -sS "http://localhost:9090/fhir/r4/Patient?_list=$LIST_ID" \
  | jq '[.entry[].resource.name[].family]'
```

```json title="Response"
["Cohort-B", "Cohort-A"]
```

The list's `entry[].item.reference` values become the candidate ids.

:::warning
`_list` **replaces** any `_id` you also supply rather than intersecting with it, and a `_list`
naming a List that does not exist fails the request with `500`. Confirm the List exists first.
:::

## `_filter`

`_filter` expresses conditions the plain parameter syntax cannot, combining them with `and`/`or`.

```bash title="Request"
curl -sS --get "http://localhost:9090/fhir/r4/Patient" \
  --data-urlencode '_filter=family co "smi" and birthdate ge 1980-01-01' | jq '.entry | length'
```

Supported operators:

| Operator | Meaning |
| --- | --- |
| `eq` | Equals — the parameter's default match |
| `ne` | Not equal |
| `co` | Contains |
| `sw` | Starts with |
| `gt`, `lt`, `ge`, `le` | Ordered comparison for date, number, and quantity parameters |

An unsupported operator is rejected with `400`:

```json title="Response"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "error",
      "code": "not-supported",
      "diagnostics": "_filter operator ew is not supported"
    }
  ]
}
```

:::warning
`_filter` is partially implemented. Known gaps: the `pr` (present) operator does not work as
specified; `ew` (ends with) is rejected; `re`, `in`, `ni`, `ap`, `sa`, and `eb` are not implemented;
and `and`/`or` are combined left to right rather than with `and` binding tighter than `or`. Group
conditions into separate parameters, or express them with ordinary search parameters, when precedence
matters.
:::

## Not currently functional

Two advertised parameters do not return results on this release. They appear in the
CapabilityStatement but should not be relied on:

| Parameter | Behaviour |
| --- | --- |
| `_text`, `_content` | Full-text search matches **nothing** — the underlying text index is not populated. Use `:contains` on a string parameter instead, for example `name:contains=smi` |
| `_score` | Relevance scores are never returned, and `_score` is not usable as a `_sort` key |

## Verify on your server

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient?_total=accurate&_count=1" \
  | jq '{total, links: [.link[].relation]}'
```
