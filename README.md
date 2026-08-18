# FHIR Server — Developer Guide

A FHIR R4 REST server written in Go, backed by PostgreSQL. It replaces a legacy architecture of 150+ per-resource tables with a compact normalized schema (19 tables), so new resource types and Implementation Guides require no schema changes and search works uniformly across resource types.

**FHIR version:** R4 (4.0.1)  
**Language:** Go 1.25  
**Database:** PostgreSQL 14+ (tested through 18)

---

## Table of Contents

1. [Quick Start (Docker Compose)](#1-quick-start-docker-compose)
2. [Building](#2-building)
3. [Running Locally](#3-running-locally)
4. [Configuration Reference](#4-configuration-reference)
5. [Architecture](#5-architecture)
6. [Multi-Tenancy](#6-multi-tenancy)
7. [Database Schema](#7-database-schema)
8. [API Reference](#8-api-reference)
9. [Search Parameters](#9-search-parameters)
10. [Terminology](#10-terminology)
11. [Implementation Guides](#11-implementation-guides)
12. [Testing](#12-testing)
13. [Extending the Server](#13-extending-the-server)
14. [Performance Tuning](#14-performance-tuning)

---

## 1. Quick Start (Docker Compose)

**Prerequisites:** Docker Desktop (or Colima on macOS), `curl`

```bash
# 1. Start PostgreSQL + server
docker compose up

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
docker compose down -v
```

---

## 2. Building

**Prerequisites:** Go 1.25+ (building from source only)

> **Don't want to build?** Every [GitHub release](https://github.com/wso2/fhir-server/releases)
> ships prebuilt `fhir-server` binaries for Linux and macOS (amd64 and arm64) as
> `.tar.gz` archives with a `SHA256SUMS` file, and publishes a multi-arch
> container image to `ghcr.io/wso2/fhir-server` (tagged `v<version>` and
> `latest`). Download and unpack one, then continue with
> [Running Locally](#3-running-locally).

### Binary

Compile a self-contained executable into the current directory:

```bash
go build -o fhir-server ./cmd/server
```

This produces a `fhir-server` binary you can run directly (see [Running Locally](#3-running-locally)) or copy to another host of the same OS/architecture.

### Docker image

Alternatively, build a container image:

```bash
docker build -t fhir-server:latest .
```

---

## 3. Running Locally

Run the server directly against a local PostgreSQL — handy for development without the [Docker Compose](#1-quick-start-docker-compose) stack. Build the binary first (see [Building](#2-building)) or download a prebuilt one from the [releases page](https://github.com/wso2/fhir-server/releases); the steps below invoke `./fhir-server`.

**Prerequisites:** PostgreSQL 14+ running locally, and a `fhir-server` binary (built or downloaded from a release)

### Create the database and role

Create the `fhir` role and `fhirdb` database as a PostgreSQL superuser. Use the block that matches how you installed PostgreSQL.

**macOS (Homebrew)**

Your OS user is the superuser and there is no `postgres` role, so connect with `psql postgres`. (Using `-U postgres` fails with *role "postgres" does not exist*.)

```bash
psql postgres -c "CREATE USER fhir WITH PASSWORD 'fhir';"
psql postgres -c "CREATE DATABASE fhirdb OWNER fhir;"
```

**Debian / Ubuntu / RHEL (apt/yum packages)**

The superuser is the `postgres` OS/DB role, so run `psql` via `sudo -u postgres`.

```bash
sudo -u postgres psql -c "CREATE USER fhir WITH PASSWORD 'fhir';"
sudo -u postgres psql -c "CREATE DATABASE fhirdb OWNER fhir;"
```

### Create the database tables

Create the tables **before** starting the server. The schema lives in
`internal/db/schema.sql` and is idempotent (`CREATE TABLE IF NOT EXISTS`), so it
is safe to re-run. Apply it to the database:

```bash
psql "postgres://fhir:fhir@localhost:5432/fhirdb?sslmode=disable" -f internal/db/schema.sql
```

The server does **not** create tables on its own by default, because that needs a
role with DDL privileges that the runtime role usually should not have. As an
alternative to running the schema manually, you can let the server create the
tables on first start by setting `FHIR_CREATE_TABLES=true` (it creates the
tables, then serves):

```bash
FHIR_CREATE_TABLES=true ./fhir-server      # creates tables, then serves
```

(The Docker Compose setup sets this for you.)

### Run the server

With the tables in place, start the server. Choose one of the following approaches.

**Option A — YAML config file**

```bash
cp config.example.yaml config.yaml      # then edit as needed
./fhir-server --config ./config.yaml
```

**Option B — environment variables only (no file)**

```bash
export DATABASE_URL="postgres://fhir:fhir@localhost:5432/fhirdb?sslmode=disable"
export SERVER_PORT=9090
export BASE_URL=http://localhost:9090/fhir/r4
./fhir-server
```

**Option C — file for non-secrets, env for secrets**

```bash
export DB_PASSWORD="$(cat ~/.fhir-db-password)"
./fhir-server --config ./config.yaml
```

> ⚠️ `DB_PASSWORD` (and the other `DB_*` component variables) are only consulted
> when no full DSN is configured. `config.example.yaml` ships with `database.url`
> set — comment it out (and use the `database.host`/`user`/`name` components
> instead) or the exported password is silently ignored. Only `DATABASE_URL`
> overrides a `database.url` from the file.

The server logs a JSON line to stdout when listening:

```json
{"level":"INFO","msg":"server listening","addr":":9090","baseURL":"http://localhost:9090/fhir/r4"}
```

---

## 4. Configuration Reference

The server reads configuration from a YAML file, environment variables, or both. When the same key is set in multiple places, the higher-priority source wins:

```
env var   >   config file   >   built-in default
```

This lets you keep non-secret defaults in a checked-in `config.yaml` and inject secrets (like `DB_PASSWORD`) via environment variables at deploy time.

> **Search performance tunables** (density-probe cap, page-size limits, chain
> depth, `plan_cache_mode`) and the deploy-time DDL knobs are documented, with
> sizing rules and regression gates, in
> [docs/performance-tuning.md](docs/performance-tuning.md).

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

The only other CLI flags are `--version` / `-v`, which print version information and exit.

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
| `server.baseUrl` | `BASE_URL` | `http://localhost:{port}/fhir/r4` | Canonical server base URL. Written into bundle `link` URLs and the CapabilityStatement. Must match the address clients use. For multi-tenant requests the `/t/{tenant}` prefix is inserted automatically (see [Multi-Tenancy](#6-multi-tenancy)), so set this to the bare base path. |
| `server.readTimeout` | `SERVER_READ_TIMEOUT` | `30s` | Max time to read a request **including its body** — a large transaction bundle upload must fit inside it. `0` disables. |
| `server.writeTimeout` | `SERVER_WRITE_TIMEOUT` | `60s` | In Go's `net/http` this bounds the **entire handler execution**, not just the response write, so it must exceed your slowest legitimate request. `0` disables. See the note below and [Bulk import & the write path](docs/performance-tuning.md#5-bulk-import--the-write-path). |
| `server.idleTimeout` | `SERVER_IDLE_TIMEOUT` | `120s` | Keep-alive idle timeout. `0` disables. |
| `logging.level` | `LOG_LEVEL` | `info` | Log verbosity: `debug`, `info`, `warn`, `error`. Logs are JSON (structured). |
| `database.url` | `DATABASE_URL` | *(derived)* | Full PostgreSQL DSN. When set, overrides every other `database.*` field. |
| `database.host` | `DB_HOST` | `localhost` | PostgreSQL host (only used when `database.url` is empty) |
| `database.port` | `DB_PORT` | `5432` | PostgreSQL port |
| `database.user` | `DB_USER` | `fhir` | PostgreSQL user |
| `database.password` | `DB_PASSWORD` | `fhir` | PostgreSQL password |
| `database.name` | `DB_NAME` | `fhirdb` | PostgreSQL database name |
| `database.createTables` | `FHIR_CREATE_TABLES` | `false` | Create the server's tables on startup. Requires a DB role with DDL privileges, so it is off by default; enable it for a one-off first start, or create tables out-of-band with a privileged role. |
| `ig.packages` | `IG_PACKAGES` | *(empty)* | List of IG package specs to load at startup. In env vars, comma-separated. See [Implementation Guides](#11-implementation-guides). |
| `ig.registryUrl` | `IG_REGISTRY_URL` | `https://packages.fhir.org` | FHIR package registry for resolving `name@version` specs. |
| `ig.forceReload` | `IG_FORCE_RELOAD` | `false` | Set to `true` to re-download and re-process IGs even if already recorded in the database. |
| `ig.cacheDir` | `IG_CACHE_DIR` | `.fhir-ig-cache` | Directory for caching downloaded `.tgz` packages between restarts. |
| *(env only)* | `FHIR_TERMINOLOGY_URL` | *(empty)* | Base URL of an external FHIR terminology server used for ValueSet `$expand` (e.g. `https://tx.fhir.org/r4`). Empty disables the `:in` / `:not-in` / `:below` / `:above` search filters. See [Terminology](#10-terminology). |
| *(env only)* | `FHIR_VALIDATE_ON_WRITE` | `false` | Enforce **profile** validation (against `meta.profile`) on create/update. Off by default. See [Validation rules](#validation-rules). |
| *(env only)* | `FHIR_BASE_VALIDATION` | `true` | Validate writes against the **base FHIR R4** StructureDefinitions (cardinality, fixed/pattern, slicing). On by default; set to `false` to disable. See [Validation rules](#validation-rules). |

> **Secrets:** Prefer environment variables (or a secret-manager-backed env) for `DB_PASSWORD` and any other sensitive value rather than committing them to the YAML file.

> ⚠️ **`SERVER_WRITE_TIMEOUT` does not fail the way you expect.** The deadline
> starts when the request headers are read, and expiry does **not** abort the
> handler — the handler runs to completion, then fails to write its response, so
> the connection is dropped and the client sees a bare `EOF` rather than a timeout
> status. Large transaction bundles failing with `EOF` while smaller ones succeed
> is the signature. Set it above your slowest bundle *and* above the client's own
> timeout, so the client decides when to give up.

> **Performance tunables.** The search (`search.*`, `database.planCacheMode`)
> and write (`write.*`) parameters are documented with their
> ranges and sizing rules in the
> **[search-layer tuning reference](docs/performance-tuning.md)**. For the hardware
> and PostgreSQL settings underneath them, see
> [Performance Tuning](#14-performance-tuning).

---

## 5. Architecture

### Package overview

```
cmd/server/main.go           Entry point: wires all packages, starts HTTP
│
├── internal/config          Reads YAML config file + env vars, validates, provides typed Config struct
├── internal/db              Opens pgxpool, creates schema tables on opt-in (idempotent)
├── internal/seed            Inserts ~1,700 base FHIR R4 search param definitions (idempotent)
├── internal/basedef         Embedded base FHIR R4 StructureDefinitions; loads base_definitions
├── internal/searchparam     Thread-safe registry: resource type + param name → FHIRPath + type
├── internal/fhirpath        FHIRPath evaluator (path chains, where(), ofType(), arrays)
├── internal/fhirxml         FHIR XML serialization (application/fhir+xml)
├── internal/fhirttl         FHIR Turtle/RDF serialization (application/fhir+turtle)
├── internal/index           Extracts SP values from resource JSON and writes to sp_* tables
├── internal/store           CRUD + Search + History + Bundles against the normalized schema
├── internal/patch           JSON Patch (RFC 6902) and XML Patch engines
├── internal/compartment     Compartment definitions (compartment search, $everything)
├── internal/tenant          Tenant id validation + request-context plumbing (multi-tenancy)
├── internal/terminology     Client for an external terminology server ($expand, cached)
├── internal/validate        StructureDefinition validation (base + profile)
├── internal/ig              Downloads IG .tgz packages and registers their SearchParameters
├── internal/handler         chi router, HTTP handlers, content negotiation, OperationOutcome
├── internal/obs             Observability: Prometheus /metrics + request middleware
├── internal/version         Build version info (--version, stamped via -ldflags)
├── internal/conformance     FHIR conformance test suite (build tag: conformance)
└── internal/testutil        Integration test helpers (testcontainers-go, build tag: integration)
```

### Request lifecycle

```
HTTP Request
     │
     ▼
handler (chi router)
     │  validates: Content-Type, body resourceType, required fields, If-Match
     │  (on PATCH, Content-Type selects the patch format instead)
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
 1. Load config (YAML file and/or env vars)
 2. Connect to PostgreSQL (pgxpool)
 3. Create schema tables if FHIR_CREATE_TABLES=true (idempotent CREATE TABLE IF NOT EXISTS); otherwise skip
 4. Seed base FHIR R4 search params (ON CONFLICT DO NOTHING)
 5. Load base FHIR R4 StructureDefinitions into base_definitions (skipped when
    FHIR_BASE_VALIDATION=false; a load failure is fatal when base validation is on)
 6. Load search param registry from DB
 7. Create store + HTTP router
 8. Start HTTP listener  ← liveness probe passes here
 9. Load IG packages in background (goroutine per package)
10. Set igReady=1           ← readiness probe passes here
```

If `IG_PACKAGES` is empty, steps 9–10 are skipped and the server is ready immediately.

---

## 6. Multi-Tenancy

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


## 7. Database Schema

The schema is embedded in the binary (`internal/db/schema.sql`; the version it creates is recorded in the `schema_version` table). It describes the database from scratch — every PHI table carries a `tenant_id` column and tenant-leading primary/foreign keys, and Row-Level Security is declared on each (see [Multi-Tenancy](#6-multi-tenancy)). It is applied at startup by `db.CreateTables()` **only when `FHIR_CREATE_TABLES=true`** (off by default — see [Running Locally](#3-running-locally)). This is table creation, not a migration system: statements use `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`, so a fresh database can be (re)initialised safely but it can only add tables/columns — it cannot perform destructive or altering changes. Upgrading a pre-existing database to a new schema version is handled by a separate migration step.

### Core tables

#### `resources` — master resource store

| Column | Type | Notes |
|---|---|---|
| `tenant_id` | `TEXT` | Owning tenant; defaults to `current_setting('app.current_tenant')` (see [Multi-Tenancy](#6-multi-tenancy)) |
| `fhir_id` | `VARCHAR(64)` | FHIR logical id (UUID or server-assigned) |
| `resource_type` | `VARCHAR(100)` | e.g. `Patient`, `Observation` |
| `version_id` | `INT` | Monotonically increasing per resource |
| `last_updated` | `TIMESTAMPTZ` | Timestamp of last write |
| `is_deleted` | `BOOLEAN` | Soft-delete flag; deleted resources return HTTP 410 |
| `resource_json` | `TEXT` (lz4-compressed) | Full resource body. Deliberately **not** `JSONB`: the document is always read and written whole, so `TEXT` avoids JSONB's parse-and-normalize cost on every write and preserves the exact marshalled bytes |
| `search_text` | `TSVECTOR` | Reserved for `_text`/`_content` full-text search — column exists but is not currently populated by the server |

Primary key: `(tenant_id, resource_type, fhir_id)`.

#### `resource_history` — append-only audit trail

Every create, update, and delete appends a row here. VRead (`GET /{type}/{id}/_history/{vid}`) reads directly from this table.

| Column | Type | Notes |
|---|---|---|
| `tenant_id` | `TEXT` | Owning tenant (see [Multi-Tenancy](#6-multi-tenancy)) |
| `fhir_id` | `VARCHAR(64)` | |
| `resource_type` | `VARCHAR(100)` | |
| `version_id` | `INT` | |
| `operation` | `VARCHAR(10)` | `POST` (create), `PUT` (update), or `DELETE` |
| `recorded_at` | `TIMESTAMPTZ` | |
| `resource_json` | `TEXT` (lz4-compressed) | Full snapshot at this version |

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
| `sp_composite_token_quantity` | `composite` (token + quantity) | component `system`/`code` + quantity `value`/`value_low`/`value_high` from the **same element** (e.g. `Observation?code-value-quantity=…$…`); written alongside the component `sp_token`/`sp_quantity` rows |
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

#### Other tables

- `schema_version` — records the schema revision `schema.sql` creates.
- `"ClosureContextTable"` / `"ClosureConceptTable"` / `"ClosureDeltaTable"` —
  storage for terminology closure contexts (transitive subsumption pairs). The
  `$closure` HTTP operation itself is not currently exposed; the tables are
  declared so the schema is ready for it.

---

## 8. API Reference

**Base path:** `/fhir/r4`  
**Content types:** JSON (`application/fhir+json`, the default), XML (`application/fhir+xml`), and Turtle (`application/fhir+turtle`) are supported for request **and** response bodies. The response format is negotiated from the `Accept` header, or forced with the `_format` query parameter (which wins over `Accept`). Examples below use JSON.  
**Errors:** All error responses return an `OperationOutcome` resource.

### Endpoint table

| Method | Path | Status | Description |
|---|---|---|---|
| `GET` | `/metadata` | 200 | CapabilityStatement |
| `POST` | `/` (FHIR base) | 200, 400, 4xx, 500 | Process a `transaction` / `batch` Bundle |
| `GET` | `/{type}/{id}` | 200, 404, 410 | Read resource (410 if soft-deleted) |
| `GET` | `/{type}/{id}/_history/{vid}` | 200, 400, 404 | Read specific version |
| `POST` | `/{type}` | 201, 400, 415, 422 | Create resource |
| `PUT` | `/{type}/{id}` | 200, 400, 404, 412, 422 | Update resource |
| `PUT` | `/{type}?{search}` | 200, 201, 412 | Conditional update (create on zero matches; 412 on multiple) |
| `PATCH` | `/{type}/{id}` | 200, 400, 404 | Patch — JSON Merge Patch, JSON Patch, XML Patch, or FHIR Patch, selected by `Content-Type` (see [Partial Update](#partial-update-patch)) |
| `DELETE` | `/{type}/{id}` | 204, 404 | Soft delete |
| `DELETE` | `/{type}?{search}` | 204, 412 | Conditional delete (no-op on zero matches; 412 on multiple) |
| `GET` | `/{type}` | 200 | Search |
| `POST` | `/{type}/_search` | 200 | Search (form-encoded body) |
| `GET` | `/{type}/{id}/_history` | 200 | Instance history |
| `GET` | `/{type}/_history` | 200 | Type-level history |
| `GET` | `/_history` | 200 | System-level history (all types) |
| `GET` | `/{type}/{id}/{compartmentType}` | 200, 404 | Compartment search (e.g. `/Patient/{id}/Observation`) |
| `GET` | `/{type}/{id}/$everything` | 200, 404 | Patient/resource graph (instance) |
| `GET` | `/{type}/$everything` | 200, 404 | Type-level `$everything` — unions the graphs of every instance (Patient, Encounter, and Group only) |
| `GET` | `/Observation/$lastn` | 200, 400 | Most recent N observations per code |
| `GET` | `/Composition/{id}/$document` | 200, 404 | Assemble a document Bundle from a Composition |
| `POST` | `/$validate`, `/{type}/$validate`, `/{type}/{id}/$validate` | 200, 415, 422 | Validate without persisting (system / type / instance level) |
| `POST` | `/$convert` | 200, 400, 415 | Convert a resource between JSON, XML, and Turtle |
| `GET` | `/$meta`, `/{type}/$meta`, `/{type}/{id}/$meta` | 200 | Retrieve meta (system / type / instance level) |
| `POST` | `/{type}/{id}/$meta-add` | 200, 400, 404 | Add profiles/tags/security labels to meta |
| `POST` | `/{type}/{id}/$meta-delete` | 200, 400, 404 | Remove profiles/tags/security labels from meta |
| `GET` | `/health/live` | 200 | Liveness probe (outside the FHIR base path) |
| `GET` | `/health/ready` | 200, 503 | Readiness probe (503 while IGs loading; outside the FHIR base path) |
| `GET` | `/metrics` | 200 | Prometheus metrics (outside the FHIR base path) |

### Response headers

| Header | Set on | Value |
|---|---|---|
| `ETag` | Read, Create, Update, Patch | `W/"<version_id>"` e.g. `W/"3"` |
| `Location` | Create | `{baseURL}/{type}/{id}/_history/1` |
| `Content-Type` | All responses | `application/fhir+json` by default; `application/fhir+xml` / `application/fhir+turtle` when negotiated via `Accept` or `_format` |

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
  -H "Content-Type: application/merge-patch+json" \
  -d '{"active": true}'
```

The `Content-Type` header selects the patch format:

| Content-Type | Format |
|---|---|
| `application/merge-patch+json` (or none) | [JSON Merge Patch (RFC 7396)](https://tools.ietf.org/html/rfc7396) — set a key to `null` to delete it |
| `application/json-patch+json` | [JSON Patch (RFC 6902)](https://tools.ietf.org/html/rfc6902) — an array of `add`/`remove`/`replace`/… operations |
| `application/xml-patch+xml` | [XML Patch (RFC 5261)](https://tools.ietf.org/html/rfc5261) |
| `application/fhir+json` / `application/fhir+xml` | [FHIRPath Patch](https://hl7.org/fhir/R4/fhirpatch.html) — a `Parameters` resource of patch operations |

An unrecognized `Content-Type` falls back to JSON Merge Patch and fails with 400 if the body cannot be parsed as JSON.

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

Response is a `Bundle` (type `searchset`) with `link` entries: `self`, `first`, `next` (if more pages exist), `previous` (if not on page 1), and `last` (only when the total match count is known — when it isn't, the server uses a full-page heuristic for `next` and omits `last`).

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

Validates a resource body against the base FHIR R4 structure (and any profiles) without persisting it:

```bash
# Valid resource → 200 OperationOutcome (severity: information)
curl -X POST http://localhost:9090/fhir/r4/Patient/\$validate \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType":"Patient","name":[{"family":"Test"}]}'

# Invalid resource → 422 OperationOutcome (base validation reports the missing element)
curl -X POST http://localhost:9090/fhir/r4/Observation/\$validate \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType":"Observation","status":"final"}'
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
| Content-Type must be a supported FHIR media type: `application/fhir+json`, `application/json`, `application/fhir+xml`, `application/xml`, or `application/fhir+turtle` | 415 | Wrong or unsupported `Content-Type` header |
| `resourceType` in body must match URL resource type | 422 | e.g. sending `{"resourceType":"Observation"}` to `/Patient` |
| Required fields present (create/update) | 422 | Observation requires `code`; Encounter requires `status` and `class`; Condition requires `subject`; DiagnosticReport requires `status` and `code`; AllergyIntolerance requires `patient`. A present-but-empty value (`null`, `""`, `{}`, `[]`) counts as missing |
| Base FHIR R4 structure | 422 | Cardinality, `fixed[x]`, `pattern[x]`, and slicing from the base spec (e.g. missing `Observation.status`). On by default; see below |
| `id` in body must match URL id | 400 | PUT only; body `id` ≠ URL id segment |

**Base validation.** The server ships the core FHIR R4 resource StructureDefinitions (embedded, loaded into `base_definitions` at startup — see [Database Schema](#7-database-schema)) and validates every write against the base definition for its resource type. This catches structural problems — missing required elements, `fixed[x]`/`pattern[x]` mismatches, forbidden (`max=0`) elements, and required slices — even when the client supplies no profile. Choice elements (`value[x]`) and elements nested under absent optional parents are handled correctly, so valid resources are not falsely rejected. FHIRPath invariant failures are reported as **warnings** (they never block a write), because the engine implements a subset of FHIRPath. Disable the whole feature with `FHIR_BASE_VALIDATION=false`.

**Profile validation** (`FHIR_VALIDATE_ON_WRITE=true`) additionally validates writes against the profiles named in `meta.profile`, using StructureDefinitions loaded from [Implementation Guides](#11-implementation-guides). It is off by default and is independent of base validation.

---

## 9. Search Parameters

### Built-in parameters

~1,700 FHIR R4 base search parameters are seeded from `internal/seed/fhir-r4-search-params.csv` at every startup, covering the base spec's parameters for all resource types (Patient, Observation, Encounter, Condition, MedicationRequest, …).

### Supported parameter types and modifiers

| Type | Example | Modifiers | Notes |
|---|---|---|---|
| `string` | `family=smith` | `:exact`, `:contains`, `:missing` | Default is case-insensitive prefix match |
| `token` | `gender=female`, `code=http://loinc.org\|8310-5` | `:text`, `:of-type`, `:missing`, `:in`, `:not-in`, `:below`, `:above` | `system\|code`, `\|code` (any system), `system\|` (any code with that system). `:text` matches the display text (case-insensitive substring); `:of-type` matches `Identifier.type` + value. The `:in`/`:not-in`/`:below`/`:above` modifiers require an external terminology server — see [Terminology](#10-terminology). |
| `date` | `birthdate=ge1980`, `date=2024-01-15` | `eq`, `ne`, `lt`, `gt`, `le`, `ge`, `sa`, `eb`, `ap` | `eq` follows R4 containment semantics (the search range must fully contain the stored range); `ne` matches when the search range does not fully contain the stored range; `ap` matches on range overlap; `sa` matches values that start after the search range and `eb` matches values that end before it. Matching is per indexed value, so a resource with multiple values for one param can match both `eq` and `ne` |
| `number` | `probability=gt0.8` | `eq`, `ne`, `lt`, `gt`, `le`, `ge`, `sa`, `eb`, `ap` | Values match with implicit-precision ranges per the spec |
| `quantity` | `value-quantity=gt5.4\|http://unitsofmeasure.org\|mg` | `eq`, `ne`, `lt`, `gt`, `le`, `ge`, `sa`, `eb`, `ap`, `:missing` | Value is `[prefix]number\|system\|code`; `system` and `code` are optional. The code is matched against the coded (UCUM) unit |
| `uri` | `url=http://example.com/fhir/ValueSet/x` | `:below`, `:above`, `:missing` | Default is exact match; `:below` is a prefix match, `:above` matches ancestor URIs |
| `reference` | `subject=Patient/abc123` | `:identifier`, `:{Type}`, `:missing` | Accepts `Type/id`, a bare `id`, or an absolute URL. `subject:Patient=123` names the target type; `patient:identifier=system\|value` matches the reference's logical identifier |
| `composite` | `code-value-quantity=8480-6$lt90` | — | Component values joined with `$`; both components must co-occur in the same element |

The only parameter type that is indexed but **not queryable** is `special`
(`Location.near`, indexed into `sp_coords`). A search using it fails closed with
an error `OperationOutcome` rather than silently dropping the predicate.

### Chaining, reverse chaining, and `_filter`

- **Chained parameters** — `GET /Observation?subject.name=smith` (optionally
  typed: `subject:Patient.name=smith`) follow reference parameters into the
  target resource. Chain depth is bounded by `SEARCH_MAX_CHAIN_DEPTH`
  (default 5 — see [docs/performance-tuning.md](docs/performance-tuning.md)).
- **`_has` (reverse chaining)** — `GET /Patient?_has:Observation:patient:code=1234-5`
  matches resources that are *referenced by* resources matching the nested query.
- **`_filter`** — a supported subset of the FHIR `_filter` grammar:
  `and` / `or` / parentheses, operators `eq`, `ne`, `co`, `sw`, `ew`, `gt`, `lt`,
  `ge`, `le`, and `pr` (present). Unsupported constructs fail closed with an error.

### Special and control parameters

| Parameter | Behaviour |
|---|---|
| `_id` | Matches `resources.fhir_id` directly (comma-separated values are OR-ed) |
| `_lastUpdated` | Matches `resources.last_updated`; supports `eq`, `ne`, `lt`, `gt`, `le`, `ge` |
| `_tag`, `_security`, `_profile`, `_source`, `_language` | Universal `Resource.meta`/`Resource.language` parameters, indexed for every resource type |
| `_text` / `_content` | Queries `resources.search_text` tsvector — **not currently functional** (column is never populated) |
| `_include` | Fetches all forward references for matched resources |
| `_revinclude` | Fetches all reverse references for matched resources |
| `_sort` | Comma-separated search params, leading `-` for descending (e.g. `_sort=-date,name`). Resources missing the sort value order last; without `_sort`, results are ordered `last_updated DESC` |
| `_count`, `_page` | Pagination |
| `_total` | Opt-in `Bundle.total`: defaults to `none` (no count is computed); `_total=accurate` (or `estimate`) requests the count |
| `_summary`, `_elements` | Response projection (e.g. `_summary=true`, `_elements=id,name`) |
| `_format` | Response format override — see [API Reference](#8-api-reference) |

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

## 10. Terminology

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

## 11. Implementation Guides

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

## 12. Testing

See [TESTING.md](TESTING.md) for the full test inventory. Quick reference:

### Unit tests (no database, no Docker)

```bash
go test ./...                         # All unit tests (~340 tests, a few seconds)
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
| `internal/store` | ~65 | CRUD, soft-delete/410, If-Match conflicts, history, VRead, search by type/token/date, FetchReferences, custom SearchParameter sync, bundle write batching, tenant isolation, search plan selection |
| `internal/handler` | ~55 | Full HTTP round-trips: CRUD + 410, VRead, If-Match 412/200, 415 Content-Type, 422 validation, 400 body-id mismatch, GET/POST search, pagination links, $validate, type-level history with `_since`/`_count`/`_page`, $everything, bundles, conditional operations, base validation |
| `internal/db`, `internal/seed`, `internal/basedef` | ~10 | Schema creation, search-param seeding, base StructureDefinition loading |

There is also a FHIR conformance suite (`internal/conformance`, build tag
`conformance`) that exercises the server against spec-level expectations:

```bash
make test-conformance        # or: go test -tags conformance -timeout 300s ./internal/conformance/...
```

---

## 13. Extending the Server

### Adding a required-field validation rule

Edit `requiredFieldsByType` in `internal/handler/handlers.go`. Add the resource type and its required fields to the map:

```go
var requiredFieldsByType = map[string][]string{
    "Observation":        {"code"},
    "Encounter":          {"status", "class"},
    "Condition":          {"subject"},
    "DiagnosticReport":   {"status", "code"},
    "AllergyIntolerance": {"patient"},
    "YourType":           {"fieldOne", "fieldTwo"},   // ← add here
}
```

Add a corresponding test case in `internal/handler/handler_test.go`.

### Adding a new search parameter type

1. Add a table to `internal/db/schema.sql` following the `sp_*` pattern.
2. Add an indexer case in `internal/index/extractor.go` (the `Extractor.appendParam` method dispatches on `d.ParamType`).
3. Add a query builder case in `internal/store/search.go`: add a `build<Type>Exists` method on `queryBuilder`, then wire it into `buildExistsForValue` (value-format heuristic) or `applyParam` (named special params).
4. Add integration tests in `internal/store/store_integration_test.go`.

### Adding a new FHIR operation

1. Implement the handler method on `*fhirHandler` in `internal/handler/handlers.go`.
2. Register the route in `internal/handler/router.go`.
3. Add the method signature to the `StoreAPI` interface in `internal/handler/store.go` if the handler needs a new store method.
4. Add unit tests in `internal/handler/handler_test.go` (mock store) and integration tests in `internal/handler/handler_integration_test.go` (real DB).

### Updating the schema

Add new statements to `internal/db/schema.sql`. Use `CREATE TABLE IF NOT EXISTS` and `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` so table creation stays idempotent. Bump the version number in the `INSERT INTO schema_version` statement near the top of the file.

---

## 14. Performance Tuning

The server delegates all data access to PostgreSQL, so deployment performance is
determined primarily by the database host. Address the following areas in order —
storage, PostgreSQL configuration, server configuration, verification — because
each constrains those that follow it. PostgreSQL settings cannot compensate for
saturated storage, and the search tunables described in the
[search-layer tuning reference](docs/performance-tuning.md) cannot compensate for
either.

### Storage

Write amplification is substantial. A single resource produces approximately 20
index rows across eight `sp_*` tables, each maintaining several B-tree indexes.
Including WAL and checkpoint activity, plan for approximately 50 KB of physical
writes per resource. Ingestion is therefore constrained by sustained write IOPS
well before it is constrained by CPU.

Requirements:

- Use SSD or NVMe storage. Rotational disks are not suitable.
- Avoid burst-credit volumes. Throughput degrades sharply once credits are
  exhausted, and measurements are not reproducible between runs.
- Where the primary volume cannot be changed, relocate `pg_wal` to the fastest
  available device.

Validate a candidate volume before deployment. Every commit incurs an `fsync`:

```bash
pg_test_fsync -s 3   # execute within the PGDATA filesystem
```

Suitable hardware sustains rates in the thousands per second. Results in the low
hundreds indicate that storage will limit write throughput irrespective of other
tuning.

Measure saturation under representative load:

```bash
iostat -x 5
```

Sustained `%util` approaching 100 together with double-digit `w_await` indicates a
storage-bound deployment, in which case PostgreSQL-level tuning provides no
benefit.

On cloud platforms, confirm that the device is locally attached. A device exposed
through an NVMe interface may still be network-attached storage; on Azure, for
example, `MSFT NVMe Accelerator` denotes a remote managed disk whereas
`Microsoft NVMe Direct Disk` is physically attached. Locally attached NVMe offers
substantially higher throughput but is typically ephemeral and does not survive a
stop or deallocate operation, which makes it appropriate for replicated clusters
and reproducible test environments rather than an unreplicated system of record.

### PostgreSQL configuration

The following is a starting point for write-heavy deployments. Adjust
`shared_buffers` and the connection pool according to the memory budget below.

```conf
shared_buffers = 8GB                  # approximately 25% of memory available to PostgreSQL
max_wal_size = 8GB                    # the 1GB default triggers checkpoints too frequently
min_wal_size = 2GB                    # permits segment recycling rather than recreation
wal_buffers = 256MB                   # the 16MB default is insufficient for concurrent bulk writes
checkpoint_timeout = 15min
checkpoint_completion_target = 0.9    # distributes checkpoint I/O rather than issuing it in bursts
track_io_timing = on                  # required for blk_read_time and blk_write_time
```

`max_wal_size` and `wal_buffers` have the greatest effect on import throughput.
Frequent checkpoints require PostgreSQL to emit a full-page image for every page
modified since the preceding checkpoint, which increases WAL volume
significantly, so increasing `max_wal_size` is generally the most effective
PostgreSQL-level change. Review the memory budget before doing so.

#### Memory budget

Under a container or cgroup memory limit, two of the largest consumers cannot be
reclaimed when memory is exhausted:

- `shared_buffers`, which is allocated as shared memory, with swap normally
  disabled.
- Per-backend private memory. Allow approximately 250 MB per connection under
  bulk-bundle load; backends parsing large multi-row `INSERT` statements consume
  considerably more than idle connections.

The remainder is page cache, which is reclaimable once written back. Dirty pages
awaiting writeback are not.

```
memory limit  >=  shared_buffers
                + (pool_max_conns x 250MB)
                + headroom for page cache
```

`max_wal_size` is intentionally excluded from this calculation: it bounds WAL disk
consumption rather than resident memory. It does affect memory indirectly, since
deferring checkpoints increases the volume of dirty pages awaiting writeback, but
that quantity is governed by the `vm.dirty_*` parameters rather than by
`max_wal_size` itself.

Exceeding the budget does not produce a clean error. A backend is terminated by
the OOM killer, after which the postmaster terminates the remaining backends and
initiates crash recovery. PostgreSQL itself normally continues running and the
container is not restarted, but all in-flight requests fail and the database is
unavailable until recovery completes. The most common cause is a connection pool
sized for target throughput rather than for available memory.

Note that `vm.dirty_ratio` and `vm.dirty_background_ratio` are expressed as
percentages of host memory, not of the container limit. On a large host running a
small container, the kernel permits more dirty page cache than the cgroup can
accommodate. Where this occurs, set `vm.dirty_background_bytes` and
`vm.dirty_bytes` to explicit values.

#### Settings with limited benefit for this workload

- `wal_compression` compresses full-page images only. Once `max_wal_size` is
  sufficiently large, few full-page images are produced and the setting yields
  little benefit.
- `synchronous_commit = off` benefits workloads with high commit rates.
  Transaction bundles commit infrequently, as a single commit covers an entire
  bundle, so commit `fsync` represents a small proportion of total write cost.
  Measure before enabling, and do not enable it where an acknowledged write must
  survive a crash. See the
  [search-layer tuning reference](docs/performance-tuning.md#5-bulk-import--the-write-path).

### Server configuration

**Timeouts.** `SERVER_WRITE_TIMEOUT` bounds the entire handler invocation, so a
transaction bundle must complete within it. Determine the import duration of the
largest bundle the deployment will accept, then set the timeout above that value
with margin and above the client timeout, so that the client governs when a
request is abandoned. Bundles containing tens of thousands of entries may require
several minutes under concurrent load. `SERVER_READ_TIMEOUT` must accommodate the
upload of the same bundle.

**Connection pool.** Set `pool_max_conns` in the `DATABASE_URL`, sized from the
memory budget above. Additional connections do not increase write throughput once
storage is saturated; they increase memory consumption and lock contention.

**Write batching.** `WRITE_MAX_ROWS_PER_STATEMENT` and `WRITE_MAX_ROWS_PER_BUNDLE`
bound how large one multi-row INSERT and one write transaction may grow, so a
pathological bundle fails with a 413 instead of driving the database out of
memory. Semantics, permitted values and the sizing rules are documented in
[the search-layer tuning reference, §5](docs/performance-tuning.md#5-bulk-import--the-write-path).

### Verification

Run the following after any bulk load, before measuring performance or serving
read traffic:

```sql
VACUUM (ANALYZE) resources;   -- repeat for each sp_* table
```

Bulk imports insert faster than autovacuum can process, which leaves a stale
visibility map and outdated planner statistics. All read fast paths depend on
index-only scans, which require both to be current. Measurements taken before
this completes are not representative.

To identify the prevailing constraint, sample wait events under load:

```sql
SELECT wait_event_type, wait_event, count(*)
FROM pg_stat_activity
WHERE state = 'active' AND datname = current_database()
GROUP BY 1, 2 ORDER BY 3 DESC;
```

| Predominant wait event | Interpretation |
|---|---|
| `LWLock/WALWrite`, `LWLock/WALInsert` | WAL-bound. Review storage first, then `wal_buffers` and `max_wal_size`. |
| `LWLock/BufferContent` | Index-page contention. Reduce the number of concurrent writers; increasing concurrency makes it worse. |
| Predominantly CPU | Storage and locking are not limiting. The [search-layer tunables](docs/performance-tuning.md) apply. |
