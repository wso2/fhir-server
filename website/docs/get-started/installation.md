---
title: Installation
description: Build and run WSO2 FHIR Server as a binary or container.
---

# Install and run the server

All commands run from a clone of the repository:

```bash
git clone https://github.com/wso2/fhir-server.git
cd fhir-server
```

## Build a binary

**Prerequisite:** Go 1.25 or later.

```bash
go build -o fhir-server ./cmd/server
```

## Build a container image

```bash
docker build -t fhir-server:latest .
```

## Prepare PostgreSQL

The server supports PostgreSQL 14 through 18. The commands below set up a **local development** database; the `fhir`/`fhir` credentials and `sslmode=disable` must not be reused outside a local environment. Production deployments need unique credentials injected from a secret manager and TLS on the database connection.

Create the runtime role and database as a PostgreSQL superuser:

```bash
psql -U postgres -c "CREATE ROLE fhir LOGIN PASSWORD 'fhir'"
psql -U postgres -c "CREATE DATABASE fhirdb OWNER fhir"
```

Then apply the idempotent schema with a role that holds DDL privileges (for local use the owner created above is fine; production should use a separate DDL role):

```bash
psql "postgres://fhir:fhir@localhost:5432/fhirdb?sslmode=disable" \
  -f internal/db/schema.sql
```

Alternatively, set `FHIR_CREATE_TABLES=true` for a one-time startup using a role with DDL privileges.

:::warning
Keep DDL privileges out of the steady-state runtime role. Provision the schema separately for production deployments.
:::

## Run with a configuration file

[`config.example.yaml`](https://github.com/wso2/fhir-server/blob/main/config.example.yaml) ships in the repository root and documents every available key:

```bash
cp config.example.yaml config.yaml
./fhir-server --config ./config.yaml
```

## Run with environment variables

```bash
export DATABASE_URL="postgres://fhir:fhir@localhost:5432/fhirdb?sslmode=disable"
export SERVER_PORT=9090
export BASE_URL="http://localhost:9090/fhir/r4"
./fhir-server
```

Configuration precedence is:

```text
environment variable > configuration file > built-in default
```

See the complete [configuration reference](../administration/configuration.md) before deploying the server.
