---
title: Introduction
description: Learn what WSO2 FHIR Server provides and how its main components fit together.
---

# What is WSO2 FHIR Server?

WSO2 FHIR Server is an open-source FHIR R4 REST server optimized for a compact operational footprint and a predictable PostgreSQL data model. It replaces per-resource relational tables with one JSONB resource store and a small set of typed search-index tables.

## What it provides

- FHIR create, read, update, patch, delete, versioned read, and history interactions.
- FHIR search across string, token, date, number, quantity, URI, reference, and composite parameters.
- Transaction and batch Bundle processing.
- CapabilityStatement generation and `$validate` support.
- Implementation Guide package loading and profile-aware validation.
- External terminology integration for ValueSet and CodeSystem operations.
- Physical and logical multi-tenant deployment models.

## Design priorities

  - **One schema** — All FHIR resource types share the same resource and history tables. New types and profiles do not require per-resource migrations.
  - **Indexed search** — Search predicates resolve through narrow typed indexes. Resource JSON is fetched only after matches are identified.
  - **Atomic writes** — Resource storage, version history, and search-index refreshes commit in the same PostgreSQL transaction.
  - **Explicit boundaries** — Terminology is delegated to a terminology server, while deployment-specific identity and policy remain infrastructure concerns.

## Where to begin

1. Use the [Docker Compose quickstart](./quickstart.md) for the shortest path to a running server.
2. Read [Architecture](../concepts/architecture.md) before changing shared behavior.
3. Use the [FHIR API reference](../reference/api.md) when integrating a client.
4. Review [Deployment](../operations/deployment.md) before running outside a local environment.
