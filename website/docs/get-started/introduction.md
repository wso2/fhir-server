---
title: Introduction
description: Learn what WSO2 FHIR Server provides and how its main components fit together.
---

# What is WSO2 FHIR Server?

WSO2 FHIR Server is a blazing-fast, lightweight FHIR server written in Go. It is an open-source FHIR R4 REST server backed by PostgreSQL, built for a compact operational footprint: a single binary, one database, and no per-resource schema migrations.

## What it provides

- FHIR create, read, update, patch, delete, versioned read, and history interactions.
- FHIR search across string, token, date, number, quantity, URI, reference, and composite parameters, including `_include`, `_revinclude`, and pagination.
- Transaction and batch Bundle processing, with conditional create, update, and delete.
- CapabilityStatement generation, `$everything`, and `$validate` support.
- Base FHIR R4 structural validation on every write, with optional profile validation.
- Implementation Guide package loading — additional SearchParameters and profiles without code changes.
- Code-aware search filters (`:in`, `:not-in`, `:below`, `:above`) via an external terminology server.
- Physical and logical multi-tenant deployment models, with logical isolation enforced by PostgreSQL Row-Level Security.

## Where to begin

1. Use the [Docker Compose quickstart](./quickstart.md) for the shortest path to a running server.
2. Use the [FHIR API reference](../reference/api.md) when integrating a client.
3. Review [Deployment](../operations/deployment.md) before running outside a local environment.
4. See [Architecture](../concepts/architecture.md) for an overview of how the server is put together.
