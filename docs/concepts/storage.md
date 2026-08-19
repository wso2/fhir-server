---
title: Storage model
description: Learn how FHIR resources, history, and search values are represented in PostgreSQL.
---

# Storage model

All FHIR resource types share one primary table. Searchable values are projected into typed relational indexes at write time.

## Core tables

| Table | Purpose |
| --- | --- |
| `resources` | Current JSONB representation and metadata for every resource |
| `resource_history` | Immutable snapshots for versioned reads and history |
| `sp_*` | Typed search values extracted from resources |
| `search_param_definitions` | Base, IG-provided, and custom SearchParameter definitions |
| `ig_packages` | Loaded FHIR package records |
| `ig_profiles` | StructureDefinitions available for profile validation |
| `base_definitions` | Base FHIR R4 StructureDefinitions |
| `schema_version` | Applied schema revision |

## Resource identity

The current representation is identified by tenant, FHIR resource type, and logical ID. Metadata tracks the current version and last-updated instant. Deletes are soft deletes so history remains available.

## Version history

Creates, updates, and deletes append a complete resource snapshot to `resource_history`. This supports:

- Version-specific reads with `/_history/{version}`.
- Instance and type history interactions.
- Optimistic concurrency with weak ETags and `If-Match`.
- Audit and reconciliation workflows at the resource-store level.

## Search indexes

The server does not scan arbitrary JSON for normal FHIR search. It stores extracted values in tables shaped for FHIR matching semantics:

- `sp_string`
- `sp_token`
- `sp_date`
- `sp_number`
- `sp_quantity`
- `sp_uri`
- `sp_reference`
- `sp_coords`

This shifts work to writes and produces bounded, index-oriented reads. Resource storage, history, and index refreshes share the same transaction.

:::note
A full-document JSONB GIN index is intentionally absent. Search is implemented through the typed indexes, while full-text behavior uses the dedicated search vector.
:::

## Schema management

The canonical schema is [`internal/db/schema.sql`](https://github.com/wso2/fhir-server/blob/main/internal/db/schema.sql). It is idempotent, but production deployments should apply schema changes out of band with a controlled database role.
