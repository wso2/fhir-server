---
title: Storage model
description: How FHIR resources, history, and search values live in your PostgreSQL database.
---

# Storage model

Everything the server stores lives in a small, fixed set of PostgreSQL tables — around a dozen, no matter how many resource types you use. Adding a new resource type, profile, or search parameter never requires a schema migration.

Every write is atomic: the resource, its history snapshot, and its search-index entries all commit
in one transaction, so a resource is never searchable in a state that was not stored. Creates,
updates, and deletes each append an immutable snapshot, which is what powers
[versioned reads](../api/interactions.md#versioned-read), `If-Match`
[optimistic locking](../api/conditional.md#optimistic-locking-with-if-match), and audit trails.
Deletes are soft, so history remains readable after one.

## The tables you will see

Operators looking at the database will find:

| Table | Purpose |
| --- | --- |
| `resources` | Current JSON representation and metadata for every resource |
| `resource_history` | Immutable version snapshots |
| `sp_string`, `sp_token`, `sp_date`, `sp_number`, `sp_quantity`, `sp_uri`, `sp_reference`, `sp_coords` | Typed search values extracted at write time |
| `search_param_definitions` | Base, IG-provided, and custom SearchParameter definitions |
| `ig_packages`, `ig_profiles`, `base_definitions` | Loaded FHIR packages and StructureDefinitions |
| `schema_version` | Applied schema revision |

Search never scans raw JSON: queries resolve against the typed `sp_*` indexes first and load the matching documents last, which keeps search latency predictable as data grows.

## Schema management

The canonical schema is [`internal/db/schema.sql`](https://github.com/wso2/fhir-server/blob/main/internal/db/schema.sql). It is idempotent; production deployments should apply schema changes out of band with a controlled database role rather than granting the runtime role DDL privileges.
