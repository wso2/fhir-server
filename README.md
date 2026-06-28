# FHIR Server — Developer Guide

A FHIR R4 REST server written in Go, backed by PostgreSQL. It replaces a legacy architecture of 150+ per-resource tables with a normalized 11-table schema, reducing write amplification and enabling cross-resource search without schema changes.

**FHIR version:** R4 (4.0.1)  
**Language:** Go 1.25  
**Database:** PostgreSQL 13+

---

## Table of Contents

1. [Quick Start (Docker Compose)](#1-quick-start-docker-compose)
2. [Running Without Docker](#2-running-without-docker)
3. [Configuration Reference](#3-configuration-reference)
4. [Architecture](#4-architecture)
5. [Multi-Tenancy](#5-multi-tenancy)
6. [Database Schema](#6-database-schema)
7. [API Reference](#7-api-reference)
8. [Search Parameters](#8-search-parameters)
9. [Terminology](#9-terminology)
10. [Implementation Guides](#10-implementation-guides)
11. [Testing](#11-testing)
12. [Extending the Server](#12-extending-the-server)

---

## 1. Quick Start (Docker Compose)

**Prerequisites:** Docker Desktop (or Colima on macOS), `curl`

```bash
# 1. Start PostgreSQL + server
docker-compose up

# 2. Wait for the server to report healthy (watch the container logs or poll):
curl -sv http://localhost:9090/health/ready   # → look for "< HTTP/1.1 200 OK" when ready (body is empty)

# 3. Smoke test — create a Patient. The server assigns an id and returns the
#    created resource; piping to `jq .id` prints just the new Patient's id, e.g.:
#      "abeab77a-34e5-4fd5-b5df-8c2ce4691682"
curl -s -X POST http://localhost:9090/fhir/r4/Patient \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType":"Patient","name":[{"family":"Smith","given":["Alice"]}]}' \
  | jq .id
```

The server is available at **`http://localhost:9090/fhir/r4`**.  
PostgreSQL is exposed on `localhost:5432` (user `fhir`, password `fhir`, database `fhirdb`).

**Explore the database:** the compose stack includes [Adminer](https://www.adminer.org/),
a lightweight web UI, at **`http://localhost:8080`**. Log in with:

| Field    | Value        |
|----------|--------------|
| System   | `PostgreSQL` |
| Server   | `db`         |
| Username | `fhir`       |
| Password | `fhir`       |
| Database | `fhirdb`     |

To stop and remove all data:
```bash
docker-compose down -v
```

---

## 2. Running Without Docker

**Prerequisites:** Go 1.25+, PostgreSQL 13+ running locally

```bash
# Create the role and database as a PostgreSQL superuser.
# Pick the block that matches how you installed PostgreSQL:

# --- macOS (Homebrew) ---
# Your OS user is the superuser; there is no "postgres" role, so connect
# with `psql postgres`. (Using `-U postgres` fails: role "postgres" does not exist.)
psql postgres -c "CREATE USER fhir WITH PASSWORD 'fhir';"
psql postgres -c "CREATE DATABASE fhirdb OWNER fhir;"

# --- Debian / Ubuntu / RHEL (apt/yum packages) ---
# The superuser is the "postgres" OS/DB role; run psql via sudo -u postgres.
sudo -u postgres psql -c "CREATE USER fhir WITH PASSWORD 'fhir';"
sudo -u postgres psql -c "CREATE DATABASE fhirdb OWNER fhir;"

# Option A — point the server at a YAML config file
cp config.example.yaml config.yaml      # then edit as needed
go run ./cmd/server --config ./config.yaml

# Option B — drive everything from env vars (no file)
export DATABASE_URL="postgres://fhir:fhir@localhost:5432/fhirdb?sslmode=disable"
export SERVER_PORT=9090
export BASE_URL=http://localhost:9090/fhir/r4
go run ./cmd/server

# Option C — both: file for non-secrets, env for secrets
export DB_PASSWORD="$(cat ~/.fhir-db-password)"
go run ./cmd/server --config ./config.yaml
```

The server does **not** create database tables by default — that needs a role
with DDL privileges, which the runtime DB role usually should not have. To
create the tables (e.g. on first start, when the role owns the database), opt
in with `FHIR_CREATE_TABLES=true`:

```bash
FHIR_CREATE_TABLES=true go run ./cmd/server      # creates tables, then serves
```

Otherwise the server expects the tables to already exist and logs that it is
skipping table creation. (The `docker-compose` setup sets this for you.)

The server logs a JSON line to stdout when listening:
```json
{"level":"INFO","msg":"server listening","addr":":9090","baseURL":"http://localhost:9090/fhir/r4"}
```

### Build a binary

```bash
go build -o fhir-server ./cmd/server
./fhir-server
```

### Build a Docker image

```bash
docker build -t fhir-server:latest .
docker run --rm \
  -e DATABASE_URL=postgres://fhir:fhir@host.docker.internal:5432/fhirdb?sslmode=disable \
  -p 9090:9090 \
  fhir-server:latest
```

---

## 3. Configuration Reference

The server reads configuration from a YAML file, environment variables, or both. When the same key is set in multiple places, the higher-priority source wins:

```
env var   >   config file   >   built-in default
```

This lets you keep non-secret defaults in a checked-in `config.yaml` and inject secrets (like `DB_PASSWORD`) via environment variables at deploy time.

### Specifying the config file

Pass the path explicitly — there is no implicit search of the working directory, so behavior is the same on every host.

```bash
# Via CLI flag (either form):
fhir-server --config /etc/fhir-server/config.yaml
fhir-server -c       /etc/fhir-server/config.yaml

# Or via env var (useful in containers):
FHIR_SERVER_CONFIG=/etc/fhir-server/config.yaml fhir-server
```

If the path is set but the file is missing, malformed, or contains an unknown key, the server fails to start with a clear error.

### File format

YAML, with the structure below. Every key is optional — omit anything you don't need to override. See [`config.example.yaml`](config.example.yaml) for a copy-paste starting point.

```yaml
server:
  port: 9090                                  # SERVER_PORT
  baseUrl: http://localhost:9090/fhir/r4      # BASE_URL

logging:
  level: info                                 # LOG_LEVEL — debug | info | warn | error

database:
  # Either a full DSN ...
  url: postgres://fhir:fhir@localhost:5432/fhirdb?sslmode=disable   # DATABASE_URL
  # ... or individual components (ignored when `url` is set):
  host:     localhost   # DB_HOST
  port:     "5432"      # DB_PORT (string, in YAML)
  user:     fhir        # DB_USER
  password: fhir        # DB_PASSWORD
  name:     fhirdb      # DB_NAME

ig:
  packages:                                   # IG_PACKAGES (comma-separated in env)
    - hl7.fhir.us.core@6.1.0
    - hl7.fhir.us.carin-bb@2.0.0
  registryUrl: https://packages.fhir.org      # IG_REGISTRY_URL
  forceReload: false                          # IG_FORCE_RELOAD
  cacheDir:    .fhir-ig-cache                 # IG_CACHE_DIR
```

### Settings table

| YAML key | Env var | Default | Description |
|---|---|---|---|
| `server.port` | `SERVER_PORT` | `9090` | HTTP listen port |
| `server.baseUrl` | `BASE_URL` | `http://localhost:{port}/fhir/r4` | Canonical server base URL. Written into bundle `link` URLs and the CapabilityStatement. Must match the address clients use. For multi-tenant requests the `/t/{tenant}` prefix is inserted automatically (see [Multi-Tenancy](#5-multi-tenancy)), so set this to the bare base path. |
| `logging.level` | `LOG_LEVEL` | `info` | Log verbosity: `debug`, `info`, `warn`, `error`. Logs are JSON (structured). |
| `database.url` | `DATABASE_URL` | *(derived)* | Full PostgreSQL DSN. When set, overrides every other `database.*` field. |
| `database.host` | `DB_HOST` | `localhost` | PostgreSQL host (only used when `database.url` is empty) |
| `database.port` | `DB_PORT` | `5432` | PostgreSQL port |
| `database.user` | `DB_USER` | `fhir` | PostgreSQL user |
| `database.password` | `DB_PASSWORD` | `fhir` | PostgreSQL password |
| `database.name` | `DB_NAME` | `fhirdb` | PostgreSQL database name |
| `database.createTables` | `FHIR_CREATE_TABLES` | `false` | Create the server's tables on startup. Requires a DB role with DDL privileges, so it is off by default; enable it for a one-off first start, or create tables out-of-band with a privileged role. |
| `ig.packages` | `IG_PACKAGES` | *(empty)* | List of IG package specs to load at startup. In env vars, comma-separated. See [Implementation Guides](#10-implementation-guides). |
| `ig.registryUrl` | `IG_REGISTRY_URL` | `https://packages.fhir.org` | FHIR package registry for resolving `name@version` specs. |
| `ig.forceReload` | `IG_FORCE_RELOAD` | `false` | Set to `true` to re-download and re-process IGs even if already recorded in the database. |
| `ig.cacheDir` | `IG_CACHE_DIR` | `.fhir-ig-cache` | Directory for caching downloaded `.tgz` packages between restarts. |
| *(env only)* | `FHIR_TERMINOLOGY_URL` | *(empty)* | Base URL of an external FHIR terminology server used for ValueSet `$expand` (e.g. `https://tx.fhir.org/r4`). Empty disables the `:in` / `:not-in` / `:below` / `:above` search filters. See [Terminology](#9-terminology). |
| *(env only)* | `FHIR_VALIDATE_ON_WRITE` | `false` | Enforce **profile** validation (against `meta.profile`) on create/update. Off by default. See [Validation rules](#validation-rules). |
| *(env only)* | `FHIR_BASE_VALIDATION` | `true` | Validate writes against the **base FHIR R4** StructureDefinitions (cardinality, fixed/pattern, slicing). On by default; set to `false` to disable. See [Validation rules](#validation-rules). |

> **Secrets:** Prefer environment variables (or a secret-manager-backed env) for `DB_PASSWORD` and any other sensitive value rather than committing them to the YAML file.

---

## 4. Architecture

### Package overview

```
cmd/server/main.go           Entry point: wires all packages, starts HTTP
│
├── internal/config          Reads env vars, validates, provides typed Config struct
├── internal/db              Opens pgxpool, creates schema tables on opt-in (idempotent)
├── internal/seed            Inserts 100+ base FHIR R4 search param definitions (idempotent)
├── internal/searchparam     Thread-safe registry: resource type + param name → FHIRPath + type
├── internal/fhirpath        FHIRPath evaluator (path chains, where(), ofType(), arrays)
├── internal/index           Extracts SP values from resource JSON and writes to sp_* tables
├── internal/store           CRUD + Search + History against the normalized schema
├── internal/ig              Downloads IG .tgz packages and registers their SearchParameters
├── internal/handler         chi router, HTTP handlers, OperationOutcome serialization
└── internal/testutil        Integration test helpers (testcontainers-go, build tag: integration)
```

### Request lifecycle

```
HTTP Request
     │
     ▼
handler (chi router)
     │  validates: Content-Type, body resourceType, required fields, If-Match
     │  (Content-Type not validated on PATCH)
     ▼
store.Create / Read / Update / Patch / Delete / Search
     │
     ├── BEGIN transaction
     ├── resources table  — upsert JSON + bump version_id
     ├── resource_history — append snapshot (Create/Update) or tombstone (Delete)
     ├── index.Delete     — remove stale sp_* rows        [Create / Update / Delete]
     ├── index.Index      — FHIRPath extract → sp_* rows  [Create / Update only]
     └── COMMIT
     │
     ▼
HTTP Response (application/fhir+json)
```

### Search flow

```
GET /fhir/r4/Patient?family=Smith&gender=female
     │
     ▼
handler.search — collects query params, calls store.Search
     │
     ▼
store.Search
     │  for each param:
     │    searchparam.Registry.Lookup("Patient", "family")
     │         returns → type=string, expr="Patient.name.family"
     │
     ├── queryBuilder.applyParam (per query param)
     │     type=string  → EXISTS(SELECT 1 FROM sp_string WHERE ...)
     │     type=token   → EXISTS(SELECT 1 FROM sp_token  WHERE ...)
     │     type=date    → EXISTS(SELECT 1 FROM sp_date   WHERE ...)
     │     ...
     │
     └── SELECT r.resource_json FROM resources r
         WHERE r.resource_type = $1
           AND r.is_deleted = FALSE
           AND <EXISTS clause per param>
         ORDER BY r.last_updated DESC
         LIMIT $N OFFSET $M
```

### Startup sequence

```
1. Load config from env
2. Connect to PostgreSQL (pgxpool)
3. Create schema tables if FHIR_CREATE_TABLES=true (idempotent CREATE TABLE IF NOT EXISTS); otherwise skip
4. Seed base FHIR R4 search params (ON CONFLICT DO NOTHING)
5. Load search param registry from DB
6. Create store + HTTP router
7. Start HTTP listener  ← liveness probe passes here
8. Load IG packages in background (goroutine per package)
9. Set igReady=1           ← readiness probe passes here
```

If `IG_PACKAGES` is empty, steps 8–9 are skipped and the server is ready immediately.

---

## 5. Multi-Tenancy

The server supports two ways to serve multiple tenants from one deployment. They can be used independently or together (a gateway can route some tenants to dedicated instances and the rest to a shared one).

### Option 1 — Physical separation (a dedicated server **and** database per tenant)

Give each tenant its own full deployment — a server instance **and** its own database — and put a gateway in front that routes each tenant to its backend:

```text
                          ┌────────────────┐   ┌─────────────┐
   /t/acme/...    ─────▶  │ fhir-server    │──▶│ acme  DB    │
                          └────────────────┘   └─────────────┘
                          ┌────────────────┐   ┌─────────────┐
   /t/globex/...  ─────▶  │ fhir-server    │──▶│ globex DB   │
                          └────────────────┘   └─────────────┘
```

This needs **no application configuration** — each instance is a normal single-tenant server pointed at its own `DATABASE_URL`. It gives the strongest isolation (separate data, backups, and blast radius) and is the recommended model for a small number of larger tenants or strict compliance/data-residency requirements. The trade-off is operational overhead that grows with the number of tenants.

> A **single server fronting one database per tenant** (a connection pool per tenant inside one process) is intentionally **not** supported: each tenant's pool consumes connections, so one process can only serve a small, fixed number of tenants before exhausting them. For many tenants on shared infrastructure, use Option 2 instead.

### Option 2 — Logical separation (shared server and database)

All tenants share one server and one database; every row is tagged with a `tenant_id` and isolation is enforced by PostgreSQL **Row-Level Security**. The active tenant is taken from the **URL**:

```text
POST /t/{tenant}/fhir/r4/Patient        # tenant "{tenant}"
GET  /t/acme/fhir/r4/Patient?name=smith # tenant "acme"
GET  /fhir/r4/Patient/123               # the "default" tenant (no prefix)
```

- **Tenant in the URL.** Requests under `/t/{tenant}/…` act on that tenant; requests on the bare `/fhir/r4/…` base act on the `default` tenant, so **existing single-tenant deployments keep working unchanged**. Each tenant therefore has its own FHIR base URL (`…/t/{tenant}/fhir/r4`), and generated absolute URLs (`Location`, Bundle `fullUrl`, pagination links) carry the prefix so clients stay within their tenant.
- **Enforced by the database, not just the query.** On every request the server sets the `app.current_tenant` Postgres setting (via `SET LOCAL` inside write transactions; per-connection for reads). RLS policies on `resources`, `resource_history`, and the `sp_*` index tables restrict every read and write to the matching `tenant_id`. A query that forgets to filter — or a bug in the application layer — still cannot cross tenants. New rows derive their `tenant_id` from the same setting, and an unset tenant fails closed.
- **Tenant identifiers** must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`; anything else is rejected with `404`.

> **⚠️ The database role must NOT be a superuser.** PostgreSQL superusers — and any role with `BYPASSRLS` — ignore Row-Level Security, which would silently disable tenant isolation. Create a dedicated least-privilege role for the server and connect as it:
>
> ```sql
> CREATE ROLE fhir_app LOGIN PASSWORD '…';            -- NOT a superuser
> GRANT USAGE ON SCHEMA public TO fhir_app;
> GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO fhir_app;
> GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO fhir_app;
> ```
>
> (Run migrations as the owner/admin role; run the server as `fhir_app`.) Single-tenant deployments that never use the tenant routes are unaffected either way.
>
> **Security:** the server trusts the tenant in the URL — it performs no authentication of its own. Deploy it behind a gateway or auth proxy (e.g. WSO2 API Manager) that authenticates the caller and authorizes them for the `{tenant}` they address. Do not expose the tenant routes directly to untrusted clients.

**What is shared vs. isolated.** PHI lives in tenant-scoped tables (`resources`, `resource_history`, `sp_*`) and is isolated per tenant. Server-wide *configuration* is intentionally shared across tenants: the search-parameter registry (including custom `SearchParameter`s) and loaded Implementation Guides. If you need per-tenant search parameters or IGs, use Option 1.

The `resources`, `resource_history`, and `sp_*` indexes lead with `tenant_id`, so tenant-scoped reads and writes stay selective as the number of tenants grows. For a single-tenant (`default`) deployment the leading column is constant and effectively free.

---


## 6. Database Schema

The schema is embedded in the binary (`internal/db/schema.sql`, schema version 7). It describes the database from scratch — every PHI table carries a `tenant_id` column and tenant-leading primary/foreign keys, and Row-Level Security is declared on each (see [Multi-Tenancy](#5-multi-tenancy)). It is applied at startup by `db.CreateTables()` **only when `FHIR_CREATE_TABLES=true`** (off by default — see [Running Without Docker](#2-running-without-docker)). This is table creation, not a migration system: statements use `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`, so a fresh database can be (re)initialised safely but it can only add tables/columns — it cannot perform destructive or altering changes. Upgrading a pre-existing database to a new schema version is handled by a separate migration step.

### Core tables

#### `resources` — master resource store

| Column | Type | Notes |
|---|---|---|
| `tenant_id` | `TEXT` | Owning tenant; defaults to `current_setting('app.current_tenant')` (see [Multi-Tenancy](#5-multi-tenancy)) |
| `fhir_id` | `VARCHAR(64)` | FHIR logical id (UUID or server-assigned) |
| `resource_type` | `VARCHAR(100)` | e.g. `Patient`, `Observation` |
| `version_id` | `INT` | Monotonically increasing per resource |
| `last_updated` | `TIMESTAMPTZ` | Timestamp of last write |
| `is_deleted` | `BOOLEAN` | Soft-delete flag; deleted resources return HTTP 410 |
| `resource_json` | `JSONB` | Full resource body |
| `search_text` | `TSVECTOR` | Reserved for `_text`/`_content` full-text search — column exists but is not currently populated by the server |

Primary key: `(tenant_id, resource_type, fhir_id)`.

#### `resource_history` — append-only audit trail

Every create, update, and delete appends a row here. VRead (`GET /{type}/{id}/_history/{vid}`) reads directly from this table.

| Column | Type | Notes |
|---|---|---|
| `tenant_id` | `TEXT` | Owning tenant (see [Multi-Tenancy](#5-multi-tenancy)) |
| `fhir_id` | `VARCHAR(64)` | |
| `resource_type` | `VARCHAR(100)` | |
| `version_id` | `INT` | |
| `operation` | `VARCHAR(10)` | `POST` (create), `PUT` (update), or `DELETE` |
| `recorded_at` | `TIMESTAMPTZ` | |
| `resource_json` | `JSONB` | Full snapshot at this version |

Unique key: `(tenant_id, fhir_id, resource_type, version_id)`.

#### `sp_*` — search index tables

One table per FHIR search parameter type. Rows are deleted and re-inserted on every write (inside the same transaction as the resource update).

| Table | Param type | Key columns |
|---|---|---|
| `sp_string` | `string` | `value_exact`, `value_lower` (downcased for prefix match) |
| `sp_token` | `token` | `system`, `code`, `display` |
| `sp_date` | `date` | `value_low`, `value_high`, `value_precision` (YEAR/MONTH/DAY/SECOND) |
| `sp_number` | `number` | `value`, `value_low`, `value_high` (implicit-precision range) |
| `sp_quantity` | `quantity` | `value`, `system`, `code`, `canonical_value`, `canonical_units` |
| `sp_uri` | `uri` | `value` (prefix index for `:below`) |
| `sp_reference` | `reference` | `target_type`, `target_id` + identifier columns for `:identifier` modifier |
| `sp_coords` | `special` | `latitude`, `longitude` (Location.near) |

All `sp_*` tables carry a `tenant_id` column and have `FOREIGN KEY (tenant_id, resource_id, resource_type) REFERENCES resources ON DELETE CASCADE`. Their indexes also lead with `tenant_id`.

#### `search_param_definitions` — search parameter registry

| Column | Notes |
|---|---|
| `resource_type` | e.g. `Patient` |
| `param_name` | e.g. `family` |
| `param_type` | `string`, `token`, `date`, `number`, `quantity`, `uri`, `reference`, `special` |
| `fhirpath_expr` | FHIRPath expression evaluated against the resource JSON |
| `is_custom` | `true` for user-registered SearchParameter resources |
| `ig_source` | `''` = base R4 spec; `'name@version'` = from an IG package |

#### `ig_packages` / `ig_profiles`

Track which IG packages have been loaded (for skip-on-restart) and which profiles they declare (for CapabilityStatement).

#### `base_definitions` — base FHIR R4 StructureDefinitions

Holds the core FHIR R4 resource StructureDefinitions (one row per resource type), shipped embedded in the binary and loaded at startup by `internal/basedef`. They drive base validation (see [Validation rules](#validation-rules)). Like `ig_profiles` this is reference data, not PHI, so it carries no `tenant_id` and is excluded from Row-Level Security.

---

## 7. API Reference

**Base path:** `/fhir/r4`  
**Content-Type:** All request and response bodies use `application/fhir+json`.  
**Errors:** All error responses return an `OperationOutcome` resource.

### Endpoint table

| Method | Path | Status | Description |
|---|---|---|---|
| `GET` | `/metadata` | 200 | CapabilityStatement |
| `POST` | `/` (FHIR base) | 200, 400, 4xx, 500 | Process a `transaction` / `batch` Bundle |
| `GET` | `/{type}/{id}` | 200, 404, 410 | Read resource (410 if soft-deleted) |
| `GET` | `/{type}/{id}/_history/{vid}` | 200, 400, 404 | Read specific version |
| `POST` | `/{type}` | 201 | Create resource |
| `PUT` | `/{type}/{id}` | 200, 400, 404, 412, 422 | Update resource |
| `PATCH` | `/{type}/{id}` | 200, 400, 404 | JSON Merge Patch (RFC 7396) |
| `DELETE` | `/{type}/{id}` | 204, 404 | Soft delete |
| `GET` | `/{type}` | 200 | Search |
| `POST` | `/{type}/_search` | 200 | Search (form-encoded body) |
| `GET` | `/{type}/{id}/_history` | 200 | Instance history |
| `GET` | `/{type}/_history` | 200 | Type-level history |
| `GET` | `/{type}/{id}/$everything` | 200, 404 | Patient/resource graph |
| `POST` | `/{type}/$validate` | 200, 415, 422 | Validate without persisting |
| `GET` | `/health/live` | 200 | Liveness probe |
| `GET` | `/health/ready` | 200, 503 | Readiness probe (503 while IGs loading) |

### Response headers

| Header | Set on | Value |
|---|---|---|
| `ETag` | Read, Create, Update, Patch | `W/"<version_id>"` e.g. `W/"3"` |
| `Location` | Create | `{baseURL}/{type}/{id}/_history/1` |
| `Content-Type` | All responses | `application/fhir+json` |

### If-Match (optimistic locking)

Send `If-Match: W/"<version>"` on `PUT` to enforce that you're updating the version you last read. Returns **412** if the current version differs.

```bash
# Read current version
curl -si http://localhost:9090/fhir/r4/Patient/abc123 | grep ETag
# ETag: W/"2"

# Update only if version is still 2
curl -X PUT http://localhost:9090/fhir/r4/Patient/abc123 \
  -H "Content-Type: application/fhir+json" \
  -H "If-Match: W/\"2\"" \
  -d '{"resourceType":"Patient","id":"abc123","active":false}'
```

---

### Examples

#### Create a Patient

```bash
curl -X POST http://localhost:9090/fhir/r4/Patient \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Patient",
    "name": [{"family": "Smith", "given": ["Alice"]}],
    "birthDate": "1990-05-15",
    "gender": "female"
  }'
```

Response `201 Created`:
```json
{
  "resourceType": "Patient",
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "meta": { "versionId": "1", "lastUpdated": "2024-01-15T10:30:00Z" },
  "name": [{"family": "Smith", "given": ["Alice"]}],
  "birthDate": "1990-05-15",
  "gender": "female"
}
```

#### Read a Resource

```bash
curl http://localhost:9090/fhir/r4/Patient/550e8400-e29b-41d4-a716-446655440000
```

Returns **410 Gone** if the resource has been deleted (body is OperationOutcome).

#### Update a Resource

```bash
curl -X PUT http://localhost:9090/fhir/r4/Patient/550e8400-e29b-41d4-a716-446655440000 \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Patient",
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "name": [{"family": "Smith-Jones", "given": ["Alice"]}],
    "birthDate": "1990-05-15",
    "gender": "female"
  }'
```

The `id` in the body must match the URL id, or the server returns **400**.

#### Partial Update (Patch)

```bash
curl -X PATCH http://localhost:9090/fhir/r4/Patient/550e8400-e29b-41d4-a716-446655440000 \
  -H "Content-Type: application/fhir+json" \
  -d '{"active": true}'
```

Uses [JSON Merge Patch (RFC 7396)](https://tools.ietf.org/html/rfc7396): set a key to `null` to delete it. PATCH does not enforce `Content-Type` — a wrong type will fail with 400 when the body cannot be parsed as JSON.

#### Delete a Resource

```bash
curl -X DELETE http://localhost:9090/fhir/r4/Patient/550e8400-e29b-41d4-a716-446655440000
# 204 No Content
```

The resource row is soft-deleted (`is_deleted = TRUE`). Subsequent reads return **410 Gone**.

#### Transaction / Batch Bundle

`POST` a `Bundle` to the FHIR base (`/fhir/r4`). Each `entry.request` carries the
`method` and `url` the entry would have used as a standalone interaction.

```bash
curl -X POST http://localhost:9090/fhir/r4 \
  -H 'Content-Type: application/fhir+json' \
  -d '{
    "resourceType": "Bundle",
    "type": "transaction",
    "entry": [
      {
        "fullUrl": "urn:uuid:pat-1",
        "resource": { "resourceType": "Patient", "name": [{"family": "Smith"}] },
        "request": { "method": "POST", "url": "Patient" }
      },
      {
        "resource": {
          "resourceType": "Observation", "status": "final",
          "code": { "text": "heart-rate" },
          "subject": { "reference": "urn:uuid:pat-1" }
        },
        "request": { "method": "POST", "url": "Observation" }
      }
    ]
  }'
```

The response is a `transaction-response` Bundle whose entries carry
`response.status` / `response.location` / `response.etag`.

**Semantics**

| Bundle type | Atomicity | On entry failure |
|---|---|---|
| `transaction` | All entries commit in a **single DB transaction** | Whole Bundle rolls back; a single `OperationOutcome` is returned with the failing entry's status |
| `batch` | Each entry runs **independently** | Only that entry fails (its `response` carries an `OperationOutcome`); siblings are unaffected; overall status is `200` |

Supported per-entry methods: `POST`, `PUT`, `PATCH` (JSON Merge Patch), `DELETE`, `GET`.

- **Reference resolution** — within a `transaction`, `urn:uuid:` (and absolute-URL)
  references between entries are rewritten to the server-assigned `Type/id` before
  persisting. Entries are processed in FHIR verb order (DELETE → POST →
  PUT/PATCH → GET) so references resolve regardless of entry order.
- **Conditional create** — `entry.request.ifNoneExist` (a search query). If it
  matches one existing resource the create is skipped and the entry resolves to it;
  more than one match is a `412`.
- **Conditional update / delete** — a `PUT`/`DELETE` whose `request.url` is a search
  query (e.g. `Patient?identifier=urn:cond|abc`). One match updates/deletes it;
  zero matches creates (PUT) or no-ops (DELETE); multiple matches is a `412`.
- **Optimistic locking** — `entry.request.ifMatch` (e.g. `W/"2"`) is honoured on `PUT`.

> **Note:** `GET` search entries inside a `transaction` read the *committed* snapshot
> and do not observe not-yet-committed writes from earlier entries in the same Bundle.
> Instance reads (`GET Type/id`) do observe them.

#### Search (GET)

```bash
# By name (prefix, case-insensitive)
curl "http://localhost:9090/fhir/r4/Patient?family=smith"

# By gender token
curl "http://localhost:9090/fhir/r4/Patient?gender=female"

# Multiple params (AND logic)
curl "http://localhost:9090/fhir/r4/Patient?family=smith&birthdate=ge1980"

# Pagination
curl "http://localhost:9090/fhir/r4/Patient?_count=10&_page=2"
```

Response is a `Bundle` (type `searchset`) with `link` entries: `self`, `first`, `last`, `next` (if more pages exist), `previous` (if not on page 1).

#### Search (POST)

Use when query parameters would be too long, or to avoid logging sensitive params in URL access logs:

```bash
curl -X POST http://localhost:9090/fhir/r4/Patient/_search \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "family=smith&gender=female&_count=10"
```

#### Read a Specific Version (VRead)

```bash
curl http://localhost:9090/fhir/r4/Patient/550e8400-e29b-41d4-a716-446655440000/_history/1
```

#### Instance History

```bash
curl "http://localhost:9090/fhir/r4/Patient/550e8400-e29b-41d4-a716-446655440000/_history"
```

Response is a `Bundle` (type `history`) with entries in reverse chronological order. Each entry has a `request.method` field whose value is `POST` (create), `PUT` (update), or `DELETE`.

#### Type-Level History

```bash
# All history for Patient (paginated)
curl "http://localhost:9090/fhir/r4/Patient/_history?_count=20&_page=1"

# Only changes since a given timestamp
curl "http://localhost:9090/fhir/r4/Patient/_history?_since=2024-01-01T00:00:00Z"
```

Response is a `Bundle` (type `history`) with pagination links.

#### $everything (Resource Graph)

Fetches the anchor resource plus all resources it references (forward) and all resources that reference it (reverse):

```bash
curl http://localhost:9090/fhir/r4/Patient/550e8400-e29b-41d4-a716-446655440000/\$everything

# Filter by type
curl "http://localhost:9090/fhir/r4/Patient/550e8400-e29b-41d4-a716-446655440000/\$everything?_type=Observation,Condition"

# Only include referenced resources with lastUpdated strictly after this timestamp
curl "http://localhost:9090/fhir/r4/Patient/550e8400-e29b-41d4-a716-446655440000/\$everything?_since=2024-01-01T00:00:00Z"
```

#### $validate

Validates a resource body against required-field rules without persisting it:

```bash
# Valid resource → 200 OperationOutcome (severity: information)
curl -X POST http://localhost:9090/fhir/r4/Patient/\$validate \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType":"Patient","name":[{"family":"Test"}]}'

# Invalid resource → 422 OperationOutcome
curl -X POST http://localhost:9090/fhir/r4/Observation/\$validate \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType":"Observation","status":"final"}'
# → 422: missing required field "code" for Observation
```

#### Capability Statement

```bash
curl http://localhost:9090/fhir/r4/metadata | jq '{fhirVersion: .fhirVersion, status: .status}'
```

---

### Validation rules

These checks apply to both `POST /{type}` (create), `PUT /{type}/{id}` (update), and `POST /{type}/$validate`:

| Check | Status | Condition |
|---|---|---|
| Content-Type must be `application/fhir+json` or `application/json` | 415 | Wrong or unsupported `Content-Type` header |
| `resourceType` in body must match URL resource type | 422 | e.g. sending `{"resourceType":"Observation"}` to `/Patient` |
| Required fields present | 422 | Observation requires `code`; Encounter requires `status` and `class` |
| Base FHIR R4 structure | 422 | Cardinality, `fixed[x]`, `pattern[x]`, and slicing from the base spec (e.g. missing `Observation.status`). On by default; see below |
| `id` in body must match URL id | 400 | PUT only; body `id` ≠ URL id segment |

**Base validation.** The server ships the core FHIR R4 resource StructureDefinitions (embedded, loaded into `base_definitions` at startup — see [Database Schema](#6-database-schema)) and validates every write against the base definition for its resource type. This catches structural problems — missing required elements, `fixed[x]`/`pattern[x]` mismatches, forbidden (`max=0`) elements, and required slices — even when the client supplies no profile. Choice elements (`value[x]`) and elements nested under absent optional parents are handled correctly, so valid resources are not falsely rejected. FHIRPath invariant failures are reported as **warnings** (they never block a write), because the engine implements a subset of FHIRPath. Disable the whole feature with `FHIR_BASE_VALIDATION=false`.

**Profile validation** (`FHIR_VALIDATE_ON_WRITE=true`) additionally validates writes against the profiles named in `meta.profile`, using StructureDefinitions loaded from [Implementation Guides](#10-implementation-guides). It is off by default and is independent of base validation.

---

## 8. Search Parameters

### Built-in parameters

100+ FHIR R4 base search parameters are seeded from `internal/seed/fhir-r4-search-params.csv` at every startup. These cover all common parameters for Patient, Observation, Encounter, Condition, MedicationRequest, and other core resource types.

### Supported parameter types and modifiers

| Type | Example | Modifiers | Notes |
|---|---|---|---|
| `string` | `family=smith` | `:exact`, `:contains`, `:missing` | Default is case-insensitive prefix match |
| `token` | `gender=female`, `code=http://loinc.org\|8310-5` | `:missing`, `:in`, `:not-in`, `:below`, `:above` | `system\|code`, `\|code` (any system), `system\|` (any code with that system). The `:in`/`:not-in`/`:below`/`:above` modifiers require an external terminology server — see [Terminology](#9-terminology). |
| `date` | `birthdate=ge1980`, `date=2024-01-15` | `eq`, `ne`, `lt`, `gt`, `le`, `ge` | `sa`/`eb` parse but fall back to `eq` |
| `number` | `probability=gt0.8` | `eq`, `lt`, `gt` | |
| `reference` | `subject=Patient/abc123` | — | |

**Not yet queryable** — these types are indexed (rows written to their `sp_*` tables) but the query builder does not read from them:

| Type | Table | Status |
|---|---|---|
| `quantity` | `sp_quantity` | Indexed only |
| `uri` | `sp_uri` | Indexed only |
| `special` (Location.near) | `sp_coords` | Indexed only |

Special parameters handled without `sp_*` tables:

| Parameter | Behaviour |
|---|---|
| `_id` | Matches `resources.fhir_id` directly |
| `_lastUpdated` | Matches `resources.last_updated`; supports `eq`, `ne`, `lt`, `gt`, `le`, `ge` |
| `_text` / `_content` | Queries `resources.search_text` tsvector — **not currently functional** (column is never populated) |
| `_include` | Fetches all forward references for matched resources |
| `_revinclude` | Fetches all reverse references for matched resources |
| `_sort` | **Silently ignored** — results always ordered by `last_updated DESC` |
| `_count`, `_page` | Pagination |

### Registering a custom SearchParameter

Create a `SearchParameter` resource via `POST`. The server automatically syncs it to the registry:

```bash
curl -X POST http://localhost:9090/fhir/r4/SearchParameter \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "SearchParameter",
    "code": "my-extension",
    "type": "string",
    "base": ["Patient"],
    "expression": "Patient.extension('"'"'http://example.com/my-ext'"'"').value"
  }'
```

The parameter is available for searching immediately and persists across restarts.

---

## 9. Terminology

The server is **not a terminology server**. It does not host `CodeSystem`, `ValueSet`, or `ConceptMap` resources, it does not expose terminology operations (`$validate-code`, `$lookup`, `$translate`), and resource validation does **not** check coded values against their bound value sets (see [Validation rules](#validation-rules) — validation is structural only: cardinality, fixed values, patterns, FHIRPath invariants, and slicing).

What it *does* provide is a thin **client to an external FHIR terminology server**, used purely to support a handful of code-aware search filters. When a search uses one of these token modifiers, the server calls `ValueSet/$expand` on the configured terminology server and filters matched resources against the returned code list:

| Modifier | Meaning | How it expands |
|---|---|---|
| `:in` | Code is a member of the named ValueSet | `GET /ValueSet/$expand?url={valueSetUrl}` |
| `:not-in` | Code is **not** a member of the named ValueSet | Same as `:in`, negated |
| `:below` | Code is a descendant of the given concept | `POST /ValueSet/$expand` with an `is-a` filter |
| `:above` | Code is an ancestor of the given concept | `POST /ValueSet/$expand` with a `generalizes` filter |

### Configuration

Set the terminology server base URL via the `FHIR_TERMINOLOGY_URL` environment variable (env only — there is no YAML key):

```bash
# Point at the public sandbox terminology server
export FHIR_TERMINOLOGY_URL=https://tx.fhir.org/r4
```

- **Disabled by default.** If `FHIR_TERMINOLOGY_URL` is empty, the server starts normally and all other features work — but a search using `:in` / `:not-in` / `:below` / `:above` returns an `UnsupportedParamError` rather than failing silently.
- **Caching.** Expansion results are cached in-memory for 5 minutes per `(ValueSet)` or `(system, op, value)` key to avoid calling the terminology server on every search. The cache is per-instance and not shared across replicas.
- **No write-time enforcement.** Creating or updating a resource with a code outside its bound value set is **not** rejected; terminology is consulted only during search.

### Example

```bash
# Find Observations whose code is in a given ValueSet
curl "http://localhost:9090/fhir/r4/Observation?code:in=http://hl7.org/fhir/ValueSet/observation-vitalsignresult"

# Find Conditions coded below a SNOMED CT concept (descendants)
curl "http://localhost:9090/fhir/r4/Condition?code:below=http://snomed.info/sct|73211009"
```

If you need full terminology capabilities — hosting your own code systems and value sets, `$validate-code`/`$lookup`/`$translate`, or binding enforcement on write — run a dedicated terminology server (e.g. HAPI FHIR, Ontoserver, Firely, or `tx.fhir.org`) and point `FHIR_TERMINOLOGY_URL` at it.

---

## 10. Implementation Guides

IGs extend the server with additional SearchParameters and profiles without code changes.

### Loading IGs at startup

Set `IG_PACKAGES` to a comma-separated list of package specs:

```bash
# Format: name@version or a direct .tgz URL
export IG_PACKAGES="hl7.fhir.us.core@6.1.0,hl7.fhir.us.carin-bb@2.0.0"
```

On startup the server:
1. Downloads `.tgz` packages from the FHIR package registry (or `IG_REGISTRY_URL`)
2. Caches them to `IG_CACHE_DIR` for subsequent restarts
3. Extracts all `SearchParameter` resources and registers them
4. Records package metadata in `ig_packages` / `ig_profiles` tables
5. Marks readiness (so `GET /health/ready` returns 200)

Packages already recorded in `ig_packages` are skipped on restart unless `IG_FORCE_RELOAD=true`.

### Startup behavior

The HTTP listener starts before IGs finish loading. This means:

- `GET /health/live` → **200 immediately** (liveness OK)
- `GET /health/ready` → **503** while IGs are loading, **200** when done

In Kubernetes, set both probes and use `readinessProbe` to gate traffic.

### Verifying loaded IGs

```bash
# CapabilityStatement lists loaded IGs and supported profiles
curl http://localhost:9090/fhir/r4/metadata | jq '.implementationGuide'
```

---

## 11. Testing

See [TESTING.md](TESTING.md) for the full test inventory. Quick reference:

### Unit tests (no database, no Docker)

```bash
go test ./...                         # All unit tests (~107 tests, <5s)
go test ./... -race                   # With race detector
go test ./... -run TestEvaluate       # Filter by test name
go test ./internal/store/... -v       # Single package, verbose
```

### Integration tests (requires Docker)

Integration tests spin up a real PostgreSQL container via [testcontainers-go](https://testcontainers.com/). Each test function gets its own isolated database.

```bash
# Ensure Docker is running first
go test -tags integration ./...                      # All integration tests
go test -tags integration -v -timeout 300s ./...     # Verbose, 5-minute timeout
go test -tags integration ./internal/store/... -v    # Store tests only
go test -tags integration ./internal/handler/... -v  # HTTP handler tests only
```

First run takes 30–90 seconds (container image pull). Subsequent runs take 10–30 seconds.

**On macOS with Colima**, set the Docker socket before running:
```bash
export DOCKER_HOST=unix://${HOME}/.colima/default/docker.sock
go test -tags integration ./...
```

### What the integration tests cover

| Package | Tests | What they verify |
|---|---|---|
| `internal/store` | ~22 | CRUD, soft-delete/410, If-Match conflicts, history, VRead, search by type/token/date, FetchReferences, custom SearchParameter sync |
| `internal/handler` | ~21 | Full HTTP round-trips: CRUD + 410, VRead, If-Match 412/200, 415 Content-Type, 422 validation, 400 body-id mismatch, GET/POST search, pagination links, $validate, type-level history with `_since`/`_count`/`_page`, $everything |

---

## 12. Extending the Server

### Adding a required-field validation rule

Edit `validateRequiredFields` in `internal/handler/handlers.go`. Add the resource type and its required fields to the map:

```go
required := map[string][]string{
    "Observation": {"code"},
    "Encounter":   {"status", "class"},
    "YourType":    {"fieldOne", "fieldTwo"},   // ← add here
}
```

Add a corresponding test case in `internal/handler/handler_test.go`.

### Adding a new search parameter type

1. Add a table to `internal/db/schema.sql` following the `sp_*` pattern.
2. Add an indexer case in `internal/index/extractor.go` (the `Extractor.indexParam` method dispatches on `d.ParamType`).
3. Add a query builder case in `internal/store/search.go`: add a `build<Type>Exists` method on `queryBuilder`, then wire it into `buildExistsForValue` (value-format heuristic) or `applyParam` (named special params).
4. Add integration tests in `internal/store/store_integration_test.go`.

### Adding a new FHIR operation

1. Implement the handler method on `*fhirHandler` in `internal/handler/handlers.go`.
2. Register the route in `internal/handler/router.go`.
3. Add the method signature to the `StoreAPI` interface in `internal/handler/store.go` if the handler needs a new store method.
4. Add unit tests in `internal/handler/handler_test.go` (mock store) and integration tests in `internal/handler/handler_integration_test.go` (real DB).

### Updating the schema

Add new statements to `internal/db/schema.sql`. Use `CREATE TABLE IF NOT EXISTS` and `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` so table creation stays idempotent. Bump the version number in the final `INSERT INTO schema_version` statement.
