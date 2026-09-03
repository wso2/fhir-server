---
title: Search
description: Search FHIR resources with typed parameters and modifiers.
---

# Search

Search with query parameters on a resource type:

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient?family=Smith" | jq
```

The response is a `searchset` Bundle. Matches carry `search.mode = "match"`:

```json title="Response"
{
  "resourceType": "Bundle",
  "type": "searchset",
  "link": [
    {"relation": "self",  "url": "http://localhost:9090/fhir/r4/Patient?_count=20&_page=1&family=Smith"},
    {"relation": "first", "url": "http://localhost:9090/fhir/r4/Patient?_count=20&_page=1&family=Smith"}
  ],
  "entry": [
    {
      "fullUrl": "http://localhost:9090/fhir/r4/Patient/5ea0f561-b771-4c9a-b4b1-5d75a8dd5227",
      "resource": {
        "resourceType": "Patient",
        "id": "5ea0f561-b771-4c9a-b4b1-5d75a8dd5227",
        "meta": {"versionId": "1", "lastUpdated": "2026-09-03T06:19:23Z"},
        "active": true,
        "name": [{"family": "Smith", "given": ["Alice"]}]
      },
      "search": {"mode": "match"}
    }
  ]
}
```

Note there is no `total` unless you ask for one — see
[`_total`](./search-results.md#totals). This page covers parameter types and modifiers; see also
[Search across references](./search-joins.md) for chaining and includes, and
[Search results and paging](./search-results.md) for `_count`, `_sort`, `_total`, and response
shaping.

## Combining parameters

- **Different parameters** are combined with AND — `?family=Smith&gender=female` requires both.
- **Repeated parameters** are also ANDed — `?code=a&code=b` requires both codes.
- **Comma-separated values** within one parameter are ORed — `?code=a,b` matches either.

## Parameter types

| Type | Matching behaviour |
| --- | --- |
| **String** | Case-insensitive **prefix** match by default. `:exact` matches the stored value exactly (case- and whitespace-sensitive); `:contains` matches a substring |
| **Token** | Exact, case-sensitive matching. Forms: `code`, `system\|code`, `system\|` (any code in that system), `\|code` (that code, any system). `:text` matches the coding's display text |
| **Date** | Matched at the precision you supply — `2026`, `2026-03`, `2026-03-14`, or a full instant. Supports the prefixes `eq`, `ne`, `gt`, `lt`, `ge`, `le`, `sa`, `eb`, `ap` |
| **Number** | Numeric comparison with an implicit precision band derived from the value. Same nine prefixes |
| **Quantity** | `[prefix]value\|system\|code`, for example `ge5.4\|http://unitsofmeasure.org\|mg`. Same nine prefixes |
| **URI** | Exact match by default. `:below` matches values beneath the given prefix; `:above` matches values that are prefixes of it |
| **Reference** | `Type/id`, a bare `id`, or an absolute URL. `:identifier` matches the reference's `identifier` instead of its literal id; a resource-type modifier such as `:Patient` restricts the target type |
| **Composite** | Two component values joined by `$`, for example `code-value-quantity=http://loinc.org\|8480-6$ge140` |

```bash title="Examples"
# string prefix — case-insensitive
curl -sS "http://localhost:9090/fhir/r4/Patient?family=ali"

# exact string — case- and whitespace-sensitive
curl -sS "http://localhost:9090/fhir/r4/Patient?family:exact=Smith"

# token with system and code
curl -sS "http://localhost:9090/fhir/r4/Observation?code=http://loinc.org%7C8867-4"

# date at month precision, on or after
curl -sS "http://localhost:9090/fhir/r4/Observation?date=ge2026-01"

# quantity with unit
curl -sS "http://localhost:9090/fhir/r4/Observation?value-quantity=ge140%7Chttp://unitsofmeasure.org%7Cmm%5BHg%5D"

# reference, restricted to a target type
curl -sS "http://localhost:9090/fhir/r4/Observation?subject:Patient=Patient/123"

# missing values
curl -sS "http://localhost:9090/fhir/r4/Patient?birthdate:missing=true"
```

:::warning
The aggregate string parameters **`name` and `address` return no matches** on this release, even
though `/metadata` advertises them. Use their component parameters instead — `family`, `given`, and
`address-city`, `address-postalcode`, `address-country`, which all work as documented.
:::

:::warning
Quantity searches match on the **coded unit as stored** — there is no UCUM conversion. A search for
`5000|http://unitsofmeasure.org|mg` will not match a value recorded as `5|http://unitsofmeasure.org|g`.
Search using the same unit the data was written with.
:::

## Modifier support

Only the combinations below are implemented. A modifier applied to a parameter type that does not
support it is **ignored rather than rejected**, so the search silently runs without it.

| Modifier | string | token | date | number | quantity | uri | reference |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `:missing` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| `:not` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| `:exact` | ✅ | — | — | — | — | — | — |
| `:contains` | ✅ | — | — | — | — | — | — |
| `:text` | — | ✅ | — | — | — | — | — |
| `:in` / `:not-in` | — | ✅ | — | — | — | — | — |
| `:above` / `:below` | — | ✅ | — | — | — | ✅ | — |
| `:of-type` | — | ✅ | — | — | — | — | — |
| `:identifier` | — | — | — | — | — | — | ✅ |
| `:{ResourceType}` | — | — | — | — | — | — | ✅ |

`:missing=true` selects resources where the parameter has no value. Any other value — including
`false` — selects resources where it *does* have a value.

`:in`, `:not-in`, `:above`, and `:below` on token parameters require a configured terminology
server; without one they return `400`. See [Terminology](../conformance/terminology.md).

Composite parameters support `:missing` and `:not` only.

## POST search

Send parameters in a form body when they should not appear in the URL — useful for long queries or
identifiers you would rather not log:

```bash title="Request"
curl -sS -X POST "http://localhost:9090/fhir/r4/Patient/_search" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "family=Smith" \
  --data-urlencode "_count=20"
```

## When a search is rejected

The server prefers to fail rather than silently return a wrong result set. These return `400` with
`code=not-supported`:

- A chained search whose target type cannot be determined, or that exceeds the depth limit.
- A malformed `_has` or composite value.
- Terminology modifiers with no terminology server configured.
- Parameters of FHIR's `special` type, such as `Location.near`.
- An unsupported `_filter` operator.

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Location?near=1.0%7C2.0" | jq
```

```json title="Response"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "error",
      "code": "not-supported",
      "diagnostics": "param \"near\" on Location has type \"special\" which is not yet supported"
    }
  ]
}
```

A chained search the server cannot resolve tells you how to fix it:

```json title="Response"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "error",
      "code": "not-supported",
      "diagnostics": "chained search: cannot infer target type for Observation.subject — use explicit Type, e.g. subject:Type.family"
    }
  ]
}
```

:::note
An **unknown** parameter name is not rejected. The server infers a type from the value's shape and
applies it, which in practice matches nothing and narrows your results. Confirm parameter names
against `/metadata` — see [CapabilityStatement](./capability-statement.md).
:::

## Custom search parameters

Create a FHIR `SearchParameter` resource and new writes are indexed against it immediately.

:::warning
Defining a `SearchParameter` does not backfill existing resources, and there is no `$reindex`
operation ([wso2/fhir-server#11](https://github.com/wso2/fhir-server/issues/11)). An existing
resource only becomes findable through the new parameter once it is rewritten — for example by a
no-op `PUT` of its current content. Plan that pass before exposing the parameter to clients.
:::

## Verify on your server

```bash title="Request"
curl -sS http://localhost:9090/fhir/r4/metadata \
  | jq '.rest[0].resource[] | select(.type=="Patient") | [.searchParam[] | {name, type}]'
```
