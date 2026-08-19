---
title: Configuration
description: Configure the server, database, search, write path, and Implementation Guide loader.
---

# Configuration reference

The server reads YAML, environment variables, or both. Higher-priority sources override lower-priority sources:

```text
environment variable > configuration file > built-in default
```

Pass a file explicitly:

```bash
fhir-server --config /etc/fhir-server/config.yaml
```

Or set:

```bash
FHIR_SERVER_CONFIG=/etc/fhir-server/config.yaml fhir-server
```

Unknown keys, invalid values, and an explicitly configured missing file fail startup.

## Example

```yaml
server:
  port: 9090
  baseUrl: http://localhost:9090/fhir/r4
  readTimeout: 30s
  writeTimeout: 60s
  idleTimeout: 120s

logging:
  level: info

database:
  url: postgres://fhir:fhir@localhost:5432/fhirdb?sslmode=disable
  createTables: false
  planCacheMode: force_custom_plan

search:
  probeCap: 5000
  defaultPageSize: 20
  maxPageSize: 200
  maxChainDepth: 5

write:
  maxRowsPerStatement: 1000
  maxRowsPerBundle: 100000

ig:
  packages:
    - hl7.fhir.us.core@6.1.0
  registryUrl: https://packages.fhir.org
  forceReload: false
  cacheDir: .fhir-ig-cache
```

## Server and logging

| YAML key | Environment variable | Default |
| --- | --- | --- |
| `server.port` | `SERVER_PORT` | `9090` |
| `server.baseUrl` | `BASE_URL` | `http://localhost:{port}/fhir/r4` |
| `server.readTimeout` | `SERVER_READ_TIMEOUT` | `30s` |
| `server.writeTimeout` | `SERVER_WRITE_TIMEOUT` | `60s` |
| `server.idleTimeout` | `SERVER_IDLE_TIMEOUT` | `120s` |
| `logging.level` | `LOG_LEVEL` | `info` |

## Database

| YAML key | Environment variable | Default |
| --- | --- | --- |
| `database.url` | `DATABASE_URL` | Derived from individual fields |
| `database.host` | `DB_HOST` | `localhost` |
| `database.port` | `DB_PORT` | `5432` |
| `database.user` | `DB_USER` | `fhir` |
| `database.password` | `DB_PASSWORD` | `fhir` |
| `database.name` | `DB_NAME` | `fhirdb` |
| `database.createTables` | `FHIR_CREATE_TABLES` | `false` |
| `database.planCacheMode` | `DATABASE_PLAN_CACHE_MODE` | `force_custom_plan` |

When `database.url` is set, it overrides the individual database fields.

## Search and write path

| YAML key | Environment variable | Default |
| --- | --- | --- |
| `search.probeCap` | `SEARCH_PROBE_CAP` | `5000` |
| `search.defaultPageSize` | `SEARCH_DEFAULT_PAGE_SIZE` | `20` |
| `search.maxPageSize` | `SEARCH_MAX_PAGE_SIZE` | `0` |
| `search.maxChainDepth` | `SEARCH_MAX_CHAIN_DEPTH` | `5` |
| `write.maxRowsPerStatement` | `WRITE_MAX_ROWS_PER_STATEMENT` | `1000` |
| `write.maxRowsPerBundle` | `WRITE_MAX_ROWS_PER_BUNDLE` | `100000` |

`search.maxPageSize=0` preserves unlimited legacy behavior. Set an explicit production limit based on client needs.

## Implementation Guides

| YAML key | Environment variable | Default |
| --- | --- | --- |
| `ig.packages` | `IG_PACKAGES` | Empty |
| `ig.registryUrl` | `IG_REGISTRY_URL` | `https://packages.fhir.org` |
| `ig.forceReload` | `IG_FORCE_RELOAD` | `false` |
| `ig.cacheDir` | `IG_CACHE_DIR` | `.fhir-ig-cache` |

## Validation and terminology

These controls are environment-variable only; they have no YAML key:

| Environment variable | Default | Effect |
| --- | --- | --- |
| `FHIR_BASE_VALIDATION` | `true` | Base R4 structural validation on writes; set `false` to disable. |
| `FHIR_VALIDATE_ON_WRITE` | `false` | Set `true` to enforce declared profiles on create and update. |
| `FHIR_TERMINOLOGY_URL` | Empty (disabled) | Base URL of the external FHIR terminology server used by terminology-backed search modifiers. |

:::tip
Keep secrets in environment variables or a secret manager. Use YAML for non-secret, reviewable deployment defaults.
:::

See [Performance tuning](https://github.com/wso2/fhir-server/blob/main/docs/performance-tuning.md) before changing search-plan or write-path controls.
