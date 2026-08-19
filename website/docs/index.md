---
title: WSO2 FHIR Server
description: A production-oriented FHIR R4 REST server built in Go and backed by PostgreSQL.
slug: /
---

# WSO2 FHIR Server

A blazing-fast, lightweight FHIR server written in Go. It stores, validates, searches, and versions FHIR R4 resources through a standards-aligned REST API, running as a single binary backed by PostgreSQL.

  - [**Run it locally**](./get-started/quickstart.md) — Start PostgreSQL and the server with Docker Compose, then create your first Patient.
  - [**Understand the architecture**](./concepts/architecture.md) — See how the server, database, and search fit together in a deployment.
  - [**Use the FHIR API**](./reference/api.md) — Work with CRUD, history, transactions, validation, search, and FHIR operations.
  - [**See what you can store**](./reference/resource-types.md) — Every FHIR R4 resource type is supported out of the box; browse them by clinical purpose.

## At a glance

| Capability | Implementation |
| --- | --- |
| FHIR version | R4 (4.0.1) |
| Runtime | Go 1.25 |
| Database | PostgreSQL 14 through 18 |
| API formats | FHIR JSON, XML, and Turtle where supported |
| Tenancy | Dedicated deployments or logical tenant isolation |
| Extensibility | Custom SearchParameters and FHIR Implementation Guides |

## Choose your path

  - **Application developer** — Start with the [quickstart](./get-started/quickstart.md), then use the [API reference](./reference/api.md) and [search guide](./reference/search.md).
  - **Platform operator** — Review [configuration](./reference/configuration.md), [deployment](./operations/deployment.md) and [performance tuning](https://github.com/wso2/fhir-server/blob/main/docs/performance-tuning.md).
  - **FHIR implementer** — Learn how [Implementation Guides](./guides/implementation-guides.md), [validation](./guides/validation.md), and [terminology](./guides/terminology.md) work.
  - **Contributor** — Read the [testing](./development/testing.md), [extension](./development/extending.md), and [contribution](./development/contributing.md) guides.

:::note
This project implements FHIR server capabilities. Always validate deployment-specific security, consent, audit, retention, and compliance requirements before handling health information.
:::
