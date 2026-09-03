---
title: WSO2 FHIR Server
sidebar_label: Introduction
description: A production-oriented FHIR R4 REST server built in Go and backed by PostgreSQL.
slug: /
---

# WSO2 FHIR Server

A blazing-fast, lightweight FHIR server written in Go. It is an open-source FHIR R4 REST server
backed by PostgreSQL, built for a compact operational footprint: a single binary, one database, and
no per-resource schema migrations.

  - [**Run it locally**](./get-started/quickstart.md) — Start PostgreSQL and the server with Docker Compose, then create your first Patient.
  - [**Understand the architecture**](./architecture/architecture.md) — See how the server, database, and search fit together in a deployment.
  - [**Use the FHIR API**](./api/interactions.md) — Work with CRUD, history, transactions, validation, search, and FHIR operations.
  - [**See what you can store**](./conformance/resource-types.md) — Every FHIR R4 resource type is supported out of the box; browse them by clinical purpose.

## At a glance

| Capability | Implementation |
| --- | --- |
| FHIR version | R4 (4.0.1) |
| Runtime | Go 1.25 |
| Database | PostgreSQL 14 through 18 |
| API formats | FHIR JSON, XML, and RDF Turtle |
| Tenancy | Dedicated deployments or logical tenant isolation |
| Extensibility | Custom SearchParameters and FHIR Implementation Guides |

## What it provides

- FHIR create, read, update, patch, delete, versioned read, and history
  [interactions](./api/interactions.md), with four patch formats.
- [Search](./api/search.md) across string, token, date, number, quantity, URI, reference, and
  composite parameters, plus [chaining, `_has`, and includes](./api/search-joins.md) and
  [paging and result controls](./api/search-results.md).
- Transaction and batch Bundle processing, with
  [conditional create, update, and delete](./api/conditional.md) and optimistic locking.
- Eight [FHIR operations](./api/operations.md): `$validate`, `$everything`, `$lastn`,
  `$document`, `$convert`, and the `$meta` family.
- Base FHIR R4 structural [validation](./conformance/validation.md) on every write, with optional
  profile enforcement.
- [Implementation Guide](./conformance/implementation-guides.md) package loading — additional
  SearchParameters and profiles without code changes.
- Code-aware search filters (`:in`, `:not-in`, `:below`, `:above`) via an external
  [terminology server](./conformance/terminology.md).
- Dedicated and shared [multi-tenant](./administration/multi-tenancy.md) deployment models, with
  logical isolation enforced by PostgreSQL Row-Level Security.

## Choose your path

  - **Application developer** — Start with the [quickstart](./get-started/quickstart.md), then use the [API reference](./api/interactions.md) and [search guide](./api/search.md).
  - **Platform operator** — Review [configuration](./administration/configuration.md), [deployment](./administration/deployment.md) and [performance tuning](https://github.com/wso2/fhir-server/blob/main/docs/performance-tuning.md).
  - **FHIR implementer** — Learn how [Implementation Guides](./conformance/implementation-guides.md), [validation](./conformance/validation.md), and [terminology](./conformance/terminology.md) work.
  - **Contributor** — Read the [contribution](./contributing/contributing.md) and [testing](./contributing/testing.md) guides.

:::note
This project implements FHIR server capabilities. Always validate deployment-specific security, consent, audit, retention, and compliance requirements before handling health information.
:::
