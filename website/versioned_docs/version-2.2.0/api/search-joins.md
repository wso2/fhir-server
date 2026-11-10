---
title: Search across references
description: Chained search, reverse chaining with _has, and pulling related resources with _include.
---

# Search across references

Three mechanisms follow references during a search: **chaining** searches through a reference,
**`_has`** searches backwards through one, and **`_include`** returns related resources alongside the
matches without changing which resources match.

## Chained search

Put a dot after a reference parameter to apply the filter to the resource it points at.

```bash title="Request"
# Observations whose subject is a Patient named Smith
curl -sS "http://localhost:9090/fhir/r4/Observation?subject:Patient.family=Smith" | jq
```

Two forms are accepted:

| Form | Example | Notes |
| --- | --- | --- |
| `reference.param` | `organization.name=Acme` | Target type is inferred. Modifiers on the target parameter work: `organization.name:exact=Acme` |
| `reference:Type.param` | `subject:Patient.family=Smith` | Target type is explicit. Use this whenever the reference can point at several types |

When the type is not stated, it is inferred by capitalising the reference parameter name
(`organization` → `Organization`), and failing that, by using the reference's single declared target.
A reference with several possible targets and no explicit type is rejected with `400`
(`code=not-supported`) rather than guessing.

Chains can traverse more than one reference:

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Observation?subject:Patient.organization.name=Acme" | jq
```

Depth is bounded by `search.maxChainDepth` (`SEARCH_MAX_CHAIN_DEPTH`, default 5) and exceeding it
returns `400`. See [Configuration](../administration/configuration.md).

:::note
Two limits apply to multi-hop chains: an explicit `:Type` is only honoured on the **first** hop, so
every deeper hop must be inferrable; and in the `reference:Type.param:modifier` form the trailing
modifier is not applied. Prefer `reference.param:modifier` when you need a modifier on the target.
:::

## Reverse chaining with `_has`

`_has` selects resources based on *other* resources that point at them.

```bash title="Request"
# Patients that have an Observation with this LOINC code
curl -sS "http://localhost:9090/fhir/r4/Patient?_has:Observation:patient:code=http://loinc.org%7C8867-4" | jq
```

```json title="Response"
{
  "resourceType": "Bundle",
  "type": "searchset",
  "entry": [
    {
      "resource": {"resourceType": "Patient", "id": "b74d6c5a-9dd4-4016-8648-ae0a73c3a3a9"},
      "search": {"mode": "match"}
    }
  ]
}
```

The syntax has exactly three parts after `_has`:

```text
_has:{SourceType}:{referenceParam}:{searchParam}={value}
```

| Part | Meaning |
| --- | --- |
| `SourceType` | The resource type doing the pointing (`Observation`) |
| `referenceParam` | The reference parameter on that type which points back (`patient`) |
| `searchParam` | The parameter to test on the source resource (`code`) |

Anything other than three parts returns `400`, and the message tells you the expected shape:

```json title="Response"
{
  "resourceType": "OperationOutcome",
  "issue": [
    {
      "severity": "error",
      "code": "not-supported",
      "diagnostics": "_has modifier must be SourceType:refParam:valueParam, got \"Observation:patient\""
    }
  ]
}
```

:::warning
`_has` has three restrictions: the inner parameter takes **no modifiers**, `_has` cannot be
**nested** inside another `_has`, and the inner parameter cannot itself be a chain. If the inner
condition cannot be built, the whole `_has` is dropped rather than erroring — which *widens* the
result set. Verify a `_has` query returns what you expect before relying on it.
:::

## Including related resources

`_include` adds the resources your matches point at; `_revinclude` adds the resources that point at
your matches. Neither changes which resources match.

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Observation?code=http://loinc.org%7C8867-4&_include=Observation:subject" | jq \
  '[.entry[] | {type: .resource.resourceType, mode: .search.mode}]'
```

```json title="Response"
[
  {"type": "Observation", "mode": "match"},
  {"type": "Patient",     "mode": "include"}
]
```

Included resources appear after the matches with `search.mode = "include"`; matches carry
`search.mode = "match"`.

:::warning
`_include` and `_revinclude` are currently **coarse**: the parameter *value* is not interpreted, so
any value pulls in **every** forward reference (`_include`) or **every** reverse reference
(`_revinclude`) of the matched resources, one hop deep. You cannot restrict an include to a single
reference parameter or target type, and `_include=Observation:subject` behaves exactly like
`_include=*`.

The `:iterate` and `:recurse` modifiers are not supported, and using them
(`_include:iterate=…`) silently resolves **no includes at all**. Omit the modifier.
:::

Other behaviour to expect:

- Includes are one hop only; there is no transitive expansion. For a resource graph use
  [`$everything`](./operations.md#everything) or [`$document`](./operations.md#document).
- Included resources are de-duplicated among themselves, but a resource can appear both as a match
  and as an include.
- Includes are **not** counted in `Bundle.total`, which reflects matches only.
- `_summary` and `_elements` projections apply to included resources too.

## Compartment search

A compartment search returns resources of one type belonging to a compartment owner. Patient,
Encounter, and Practitioner compartments are supported.

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Patient/{id}/Observation" | jq
```

:::note
Compartment search does not emit pagination links and reads up to 1000 resources per underlying
reference parameter. For large compartments use an ordinary search with a reference filter, which
pages properly:

```bash title="Request"
curl -sS "http://localhost:9090/fhir/r4/Observation?subject=Patient/{id}&_count=50&_page=1"
```
:::

## Verify on your server

Reference parameters advertised as `_include` targets for a type:

```bash title="Request"
curl -sS http://localhost:9090/fhir/r4/metadata \
  | jq '.rest[0].resource[] | select(.type=="Observation") | .searchInclude'
```
