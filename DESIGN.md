# DESIGN — WSO2 FHIR Server

This document explains **how the WSO2 FHIR Server is built and *why* it is built that
way** — the architecture, the design decisions behind each subsystem, the trade-offs
that were consciously accepted, and the things the server deliberately does *not* do.

It is meant to be read on its own. Where it describes *how to operate* a feature
(config keys, endpoints, curl examples), it defers to the [README](README.md), which is
the user-facing developer guide. This document owns the *rationale*.

- **FHIR version:** R4 (4.0.1)
- **Language:** Go 1.25
- **Database:** PostgreSQL 13+ (PostGIS optional, only for `Location.near`)

---

## Table of contents

1. [Design principles](#1-design-principles)
2. [Architecture overview](#2-architecture-overview)
3. [Storage model](#3-storage-model)
4. [Search architecture](#4-search-architecture)
5. [Multi-tenancy](#5-multi-tenancy)
6. [Search parameters & the registry](#6-search-parameters--the-registry)
7. [Implementation Guides](#7-implementation-guides)
8. [Validation](#8-validation)
9. [Terminology](#9-terminology)
10. [FHIRPath engine](#10-fhirpath-engine)
11. [HTTP API semantics](#11-http-api-semantics)
12. [Concurrency & versioning](#12-concurrency--versioning)
13. [Configuration](#13-configuration)
14. [Startup & lifecycle](#14-startup--lifecycle)
15. [Performance engineering](#15-performance-engineering)
16. [Schema versioning & migrations](#16-schema-versioning--migrations)
17. [Deliberate non-goals & known limitations](#17-deliberate-non-goals--known-limitations)

---

## 1. Design principles

A handful of cross-cutting principles recur throughout the codebase. Understanding
them upfront makes the per-subsystem decisions below feel consistent rather than
arbitrary.

- **One schema for all resource types.** FHIR has 140+ resource types. Rather than a
  table per type (the legacy approach), every resource lives in a single `resources`
  table as a JSONB document, with search values projected out into normalized index
  tables. New resource types and Implementation Guides require *no schema changes*.

- **Search reads never touch the resource JSON.** All query predicates resolve against
  narrow, purpose-built `sp_*` index tables. The full document is fetched only to
  return it. This keeps the write cost of indexing bounded and the read path index-only.

- **Fail closed, not open.** When the server cannot answer a query *correctly* (an
  unsupported parameter type, an unresolvable chain, a `:missing` on a param it can't
  locate), it returns an explicit error rather than silently dropping the predicate and
  returning a *wider* result set. Returning too many records is a correctness/safety
  bug; an honest error is not.

- **Strictness is opt-in; interoperability is the default.** Profile validation on write
  is off unless enabled, and even then only applies to resources that *declare* a
  profile. The server accepts well-formed FHIR by default and lets deployments tighten.

- **Idempotent startup.** Schema creation, base search-parameter seeding, and IG loading
  are all safe to run repeatedly (`CREATE ... IF NOT EXISTS`, `ON CONFLICT DO NOTHING`,
  skip-if-already-loaded). Restarts and re-deploys converge instead of corrupting.

- **Terminology is someone else's job.** The server is a resource store, not a
  terminology server. Code-system expansion, subsumption, and value-set membership are
  delegated to an external terminology service.

- **Availability over completeness at startup.** The HTTP listener comes up before
  Implementation Guides finish loading, so liveness probes pass immediately; readiness
  gates traffic until the registry is fully populated.

---

## 2. Architecture overview

The server is a single Go binary fronting a PostgreSQL database.

```
                 ┌─────────────────────────────────────────────┐
   HTTP client → │  cmd/server  (main: config, wiring, lifecycle)│
                 └───────────────────────┬─────────────────────┘
                                         │
                 ┌───────────────────────▼─────────────────────┐
                 │  internal/handler  (chi router)              │
                 │   • content negotiation (JSON/XML/Turtle)    │
                 │   • OperationOutcome errors                  │
                 │   • CRUD, search, history, $operations       │
                 │   • conditional create/update/delete         │
                 │   • transaction / batch bundles              │
                 │   • profile validation (opt-in)              │
                 └───┬───────────────┬───────────────┬──────────┘
                     │               │               │
        ┌────────────▼───┐   ┌───────▼────────┐  ┌───▼───────────────┐
        │ internal/store │   │ internal/search│  │ internal/validate │
        │  CRUD + tx +   │   │  param registry│  │  structural +     │
        │  version/hist  │   │  query builder │  │  profile checks   │
        └───────┬────────┘   └───────┬────────┘  └───────────────────┘
                │                    │
        ┌───────▼────────┐   ┌───────▼────────┐   ┌──────────────────┐
        │ internal/index │   │internal/fhirpath│  │ internal/terminology
        │ write-time     │   │ expression eval │  │ external TX server │
        │ sp_* extraction│   │ + parse cache   │  │ (ValueSet $expand) │
        └───────┬────────┘   └────────────────┘   └──────────────────┘
                │
   internal/seed (base R4 CSV)   internal/ig (IG packages)
                │                         │
                └────────────┬────────────┘
                             ▼
                     ┌───────────────┐
                     │  PostgreSQL   │  resources, resource_history,
                     │               │  sp_*, search_param_definitions,
                     │               │  ig_packages, ig_profiles, closure*
                     └───────────────┘
```

**Request lifecycle (write):** router resolves tenant → decodes & negotiates →
(optional) profile validation → `store` opens a transaction, sets the tenant scope,
upserts the resource, bumps `version_id`, appends a `resource_history` snapshot, and
calls the `index` extractor to refresh that resource's `sp_*` rows — all atomically.

**Request lifecycle (search):** router resolves tenant → `store` builds a SQL query
from the parsed parameters, consulting the in-memory search-parameter `registry` to
type each param and route it to the correct `sp_*` table → results are paged → full
documents are fetched from `resources` and assembled into a `Bundle`.

---

## 3. Storage model

### One `resources` table, JSONB document

Every FHIR resource of every type is stored as a row in `resources`, keyed by
`(tenant_id, resource_type, fhir_id)`, with the full document in a `resource_json`
JSONB column (`internal/db/schema.sql`).

- **Decision:** replace the legacy "150+ per-resource tables" model with one table.
- **Why:** FHIR's resource set is large and evolves; per-type tables mean a migration
  for every new type or profile. A single document table makes resource types and IGs a
  *data* concern, not a *schema* concern.
- **Trade-off:** queries can't lean on per-type columns — hence the separate `sp_*`
  index tables (§4).

### No GIN index on `resource_json`

The schema deliberately does **not** index the JSONB document
(`internal/db/schema.sql` header comment).

- **Why:** all search predicates resolve through `sp_*` tables, so a document-wide GIN
  index would buy nothing for the actual query patterns while costing roughly **2.4×**
  on write throughput. The only GIN index is on `search_text` (a `tsvector`) for the
  `_text`/`_content` full-text parameters.

### Append-only history

Every create, update, and delete writes a full snapshot row into `resource_history`
with an immutable `version_id` and an `operation` tag (`CREATE|UPDATE|DELETE`).

- **Why:** FHIR mandates versioning (`vread`, `_history`) and healthcare deployments
  need audit trails.
- **Trade-off:** storage grows with every update (full snapshot per version). Accepted —
  versioning is non-negotiable and compliance generally requires retention anyway.

### Soft delete

`DELETE` sets `is_deleted = TRUE` rather than removing the row; the deletion is also
recorded in history.

- **Why:** preserves history and referential context, and lets a subsequent `PUT`
  resurrect the id with a new version. A partial index `WHERE is_deleted = FALSE` keeps
  the common "active resources only" search path fast.

### What lives where

| Table(s) | Purpose |
|---|---|
| `resources` | Current version of every resource (JSONB) |
| `resource_history` | Immutable per-version snapshots |
| `sp_string`, `sp_token`, `sp_date`, `sp_number`, `sp_quantity`, `sp_uri`, `sp_reference`, `sp_coords` | Extracted search values, one table per param type |
| `search_param_definitions` | The search-parameter registry (base + IG + custom) |
| `ig_packages`, `ig_profiles` | Loaded IG tracking + profile StructureDefinitions |
| `ClosureContextTable`, `ClosureConceptTable`, `ClosureDeltaTable` | `$closure` subsumption bookkeeping |
| `schema_version` | Applied schema revision |

---

## 4. Search architecture

Search is the most involved subsystem. The core idea: **project searchable values out
of each resource at write time into typed index tables, and resolve queries entirely
against those tables.**

### Write-time extraction

On every create/update, `internal/index` evaluates each applicable search parameter's
FHIRPath expression against the resource and writes the resulting values into the
matching `sp_*` table (inside the same transaction as the resource write). On update,
a resource's `sp_*` rows are deleted and re-inserted.

- **Decision:** extract eagerly at write, not lazily at read.
- **Trade-off:** writes do more work (and `sp_*` tables are DELETE+INSERT-heavy), but
  reads become simple indexed lookups. For a FHIR store, reads/searches dominate and
  must be fast and predictable.

### One table per parameter type

Each FHIR search-parameter type has its own table with columns shaped for its matching
semantics (`internal/db/schema.sql`):

- **`sp_string`** — keeps both `value_exact` (original casing, for `:exact`) and
  `value_lower` (for the default case-insensitive prefix match). `text_pattern_ops`
  indexes make `LIKE 'prefix%'` use a btree even under non-C collations.
- **`sp_token`** — stores `system | code` pairs plus `display` (for `:text`). The
  primary index leads with `system, code`; a code-only partial index handles searches
  that omit the system.
- **`sp_date`** — partial-precision dates (`2000`, `2000-04`) are **expanded into a
  `[value_low, value_high]` range at write time**, so all eight FHIR date comparators
  (`eq/ne/lt/gt/le/ge/sa/eb`) work without special-casing precision at query time.
- **`sp_number`** — stores a precision range so FHIR's "approximately equal" `eq`
  semantics hold (searching `100` matches `100.4` but not `100.5`).
- **`sp_quantity`** — stores the raw value *and* a UCUM-canonicalized value/units, so
  cross-unit comparisons work (`1g` matches `1000mg`).
- **`sp_uri`** — exact match plus `:below` prefix/hierarchy match via `text_pattern_ops`.
- **`sp_reference`** — target type/id/version, external `target_url`, and identifier
  columns for the `:identifier` modifier. Also the backbone of `_include`/`_revinclude`
  and `$everything` traversal.
- **`sp_coords`** — lat/lng for `Location.near` (swap for PostGIS for heavy geospatial).

### Registry-driven query building

The query builder (`internal/store/search.go`) consults the in-memory search-parameter
`registry` (§6) to look up each parameter's type, then routes it to the right `sp_*`
table.

- **Unknown parameter → heuristic, not rejection.** If a parameter isn't in the registry
  (e.g. a custom param that hasn't been loaded), the builder falls back to a best-effort
  guess from the value's format (`buildHeuristicExists`). It does **not** reject the
  request. The failure mode is therefore *imperfect typing*, not an error.
- **Unsupported-but-known → fail closed.** A *registry-known* parameter whose type the
  engine doesn't implement (composite, special like `Location.near` without support),
  an over-deep chain, or an unanswerable `:missing`, returns an `UnsupportedParamError`
  (surfaced as an `OperationOutcome`) rather than silently widening results.

### Chaining, `_include`, `_revinclude`

Chained searches (`subject.name=...`) resolve by walking the reference parameter to its
target type and applying the filter there; `_include`/`_revinclude` are resolved in a
separate fetch pass over `sp_reference`. These are intentionally implemented as extra
queries rather than giant JOINs — reference traversal is comparatively rare, and keeping
the base search path simple matters more than micro-optimizing the traversal case.

---

## 5. Multi-tenancy

The server supports two deployment models (`internal/tenant`, README §5):

1. **One database (or server) per tenant** — ignore tenancy; everything uses the
   `default` tenant. Simplest, strongest isolation.
2. **Logical multi-tenancy** — a single server/database, with requests routed under a
   `/t/{tenant}` prefix and isolation enforced in the database.

### Row-Level Security (RLS)

For the logical model, every PHI-bearing table (`resources`, `resource_history`, all
`sp_*`) carries a `tenant_id` that defaults to the `app.current_tenant` runtime setting,
and an RLS policy restricts every read/write to the current tenant
(`internal/db/schema.sql`, the `$rls$` block).

- **`FORCE ROW LEVEL SECURITY`** so the policy applies even to the table owner.
- **Fail closed:** an unset tenant matches *no* rows, and the `NOT NULL tenant_id`
  rejects writes — so a missing scope can never silently read/write across tenants.
- **Operational requirement:** the server must connect as a **non-superuser** role.
  Superusers and `BYPASSRLS` roles ignore RLS entirely. This is called out explicitly in
  the schema because it's a security-critical deployment constraint.

### Defense in depth

Beyond RLS, the store sets the tenant scope on every transaction (`SET LOCAL` for
writes, scoped to the tx; `SET` for reads). So even a misconfigured DB role gets tenant
scoping from the setting itself, not only from the RLS policy.

### Shared vs isolated

PHI is per-tenant. **Configuration is intentionally shared:** `search_param_definitions`,
`ig_packages`, `ig_profiles`, the closure tables, and `schema_version` carry no
`tenant_id`. The search-parameter registry and loaded IGs are server-wide. If you need
per-tenant search params or IGs, use deployment model #1.

---

## 6. Search parameters & the registry

### Base spec: embedded and idempotent

The base FHIR R4 search parameters ship **embedded in the binary** as a CSV
(`internal/seed`, `//go:embed`). At startup they're inserted into
`search_param_definitions` with `ON CONFLICT (resource_type, param_name) DO NOTHING`.

- **Why embedded:** no network dependency for the base spec; the server is fully
  functional offline with the standard parameter set (~1,700 definitions across all R4
  resource types).
- **Why idempotent:** safe to re-run on every boot; never clobbers IG-sourced or custom
  parameters.
- The CSV reader detects both the WSO2 Ballerina-format header and a canonical header,
  and supports loading from an external file (for testing / overrides).

### The in-memory registry

After seeding, the entire table is loaded into an in-memory `Registry`
(`internal/searchparam`) that the index extractor and query builder consult on the hot
path. It also builds a reverse-include index (`targetType → [SourceType:param]`) so
`_revinclude` and the CapabilityStatement can be answered without a query.

The `ig_source` column distinguishes provenance: `''` = base R4, `'user'` = a custom
`SearchParameter` resource written via the API, `'name@version'` = from an IG package.

### Custom `SearchParameter` resources

Writing a `SearchParameter` resource updates both the DB and the in-memory registry
(`internal/store`), and deleting one removes it — the DB write commits *before* the
in-memory registry changes, so the two never diverge in the dangerous direction
("registry has it, DB doesn't").

---

## 7. Implementation Guides

IGs are the server's **extension mechanism**: they add search parameters and validation
profiles for a specification (e.g. US Core) without any code change. None are loaded by
default — the server is a plain base-R4 store until you opt in.

### Load lifecycle (`internal/ig`)

For each configured `name@version` (or direct `.tgz` URL):

1. **Skip-if-loaded:** if it's already in `ig_packages` (and `forceReload` is off), skip.
2. **Fetch** the package `.tgz` from the registry (`packages.fhir.org` by default),
   using a local cache directory to avoid re-downloading across restarts.
3. **Extract two artifact kinds only:**
   - `SearchParameter` resources → `search_param_definitions` (`ig_source =
     name@version`) and `registry.Upsert` into the live registry. `ON CONFLICT DO
     NOTHING` means base-spec definitions win over IG ones on a name collision.
   - `StructureDefinition` **profiles** that are `kind = resource` and `derivation =
     constraint` → `ig_profiles`, storing the **full SD JSON** for the validator.
3. **Record** the package in `ig_packages` and commit — all in one transaction.

- **Why only those two artifact kinds:** they're what changes server *behavior* (search
  and validation). ValueSets/CodeSystems are terminology (delegated, §9); examples and
  non-resource profiles aren't actionable here.

### What "loaded" changes

- **Search:** the IG's params become first-class (correctly typed routing, `_revinclude`,
  chaining) instead of heuristic guesses.
- **Validation:** resources that *declare* one of the IG's profiles in `meta.profile` can
  be validated against it (§8).
- **CapabilityStatement:** `/metadata` advertises the loaded packages, supported
  profiles, and the IG's search parameters.

### Known limitation: no reindex of existing data

Indexing happens at write time against the registry as it stood *then*. Loading an IG (or
adding a custom param) **after** data already exists does not reindex pre-existing
resources, so searches by a newly added parameter only match resources written since the
load — until those rows are rewritten. This is tracked as
[wso2/fhir-server#11](https://github.com/wso2/fhir-server/issues/11); a `$reindex`-style
operation is the intended fix.

---

## 8. Validation

The server has two layers, and the profile layer is intentionally narrow.

### Structural validation (always on)

Basic structural checks apply to create, update, and `$validate`
(`internal/handler`, README §"Validation rules"): correct `Content-Type`, body
`resourceType` matches the URL, required fields present, and body `id` matches URL `id`
on `PUT`. These guard the store from obviously malformed input.

### Profile validation (opt-in, declaration-gated)

Validation against IG `StructureDefinition` profiles (`internal/validate`,
`AgainstProfile`) checks required/forbidden elements, fixed/pattern values, FHIRPath
invariants, and slicing discriminators. It is gated by **two** conditions:

1. The server was started with **`validateOnWrite`** enabled
   (`FHIR_VALIDATE_ON_WRITE` / `server`-side config; **off by default**), **and**
2. the resource itself **declares** a profile in `meta.profile`.

If a resource declares no profile, nothing is validated. If a declared profile isn't
loaded (its IG isn't present), it is **soft-skipped** with a debug log — *not* a 422.
When a loaded profile is violated, the write fails with **422** and an `OperationOutcome`.

- **Why opt-in + declaration-gated:** maximizes interoperability by default (accept
  well-formed FHIR), and avoids forcing every `Patient` to conform to, say, US Core just
  because that IG is loaded. Deployments that want strictness turn it on and use
  `meta.profile` to say what each resource claims to be.
- **Why soft-skip unknown profiles:** a resource may legitimately reference a profile
  from an IG this server hasn't loaded; rejecting it would be wrong. The trade-off is
  that a typo'd profile URL is silently ignored rather than flagged.

### `$validate`

The `$validate` operation validates an arbitrary resource against profiles named by a
`?profile=` query parameter or by `meta.profile`, returning an `OperationOutcome`
(200 informational when valid, 422 with issues otherwise) — same soft-skip rules.

---

## 9. Terminology

**The server is not a terminology server, by design** (`internal/terminology`, README §9).

- It does **not** host `CodeSystem`/`ValueSet`/`ConceptMap`, expose `$lookup`/`$translate`,
  or validate coded values against their bound value sets. Structural validation is
  exactly that — structural.
- For the token modifiers that *need* terminology — `:in` / `:not-in` (value-set
  membership) and `:above` / `:below` (subsumption) — the server calls an **external**
  terminology server's `$expand`, configured via `FHIR_TERMINOLOGY_URL` (a FHIR base
  such as `https://tx.fhir.org/r4`). **It is unset by default**, and when unset those
  modifiers return `UnsupportedParamError` rather than guessing.
- Expansions are **cached briefly (≈5 minutes)** to avoid hammering the external server
  on repeated searches, accepting a small staleness window.

- **Why delegate:** correct terminology is a large, separately-maintained problem
  (huge code systems, frequent updates). Re-implementing it inside the resource store
  would be costly and perpetually out of date. The `$closure` tables exist to support
  the closure operation's bookkeeping, but subsumption logic itself is the external
  server's responsibility.

---

## 10. FHIRPath engine

A purpose-built, pure-Go FHIRPath evaluator (`internal/fhirpath`) powers write-time
search extraction.

- **Scoped subset, not a full implementation.** It supports what search-parameter
  expressions actually use: path traversal, array flattening, union (`|`), `ofType()`,
  `where()`, `exists()`, `extension('url')`. It intentionally omits arithmetic, string
  manipulation, and other general-purpose FHIRPath features.
  - **Why:** the engine exists to evaluate the FHIRPath in `search_param_definitions`,
    not to be a general FHIRPath interpreter. Scoping it keeps it small, fast, and
    dependency-free.
- **Parse cache.** Compiled expression ASTs are cached by expression string in a
  `sync.Map`. The AST is immutable, so reads are lock-free.
  - **Why:** the same expressions are evaluated against every ingested resource —
    parsing once and reusing keeps the ingest hot path cheap.

---

## 11. HTTP API semantics

The HTTP layer (`internal/handler`, chi router) aims for spec-faithful FHIR REST.

- **`OperationOutcome` for every error.** No bare HTTP error bodies — clients get a
  consistent FHIR error contract with `severity`/`code`/`diagnostics`.
- **Content negotiation.** `_format` query param (highest priority) then `Accept`
  header; defaults to JSON. **JSON is first-class; XML and Turtle are best-effort.**
- **Optimistic concurrency.** Updates honor `If-Match` against the weak ETag
  (`W/"versionId"`); a mismatch is **412**. There is no pessimistic row locking on the
  normal path.
- **Conditional interactions.**
  - *Conditional create* (`If-None-Exist`): 0 matches → create; 1 → return existing
    (200); >1 → 412.
  - *Conditional update/delete* by search params resolve a single match; **multiple
    conditional delete is intentionally not supported** (the CapabilityStatement
    advertises `single`).
- **Bundles.** *Transaction* bundles are all-or-nothing in a single DB transaction with
  `urn:uuid` reference resolution; *batch* bundles process entries independently, a
  failed entry yielding an `OperationOutcome` without aborting the others.

---

## 12. Concurrency & versioning

- **Integer `version_id`**, starting at 1 and bumped on each update; surfaced as
  `meta.versionId` and the ETag.
- **Lost-update protection** comes from optimistic concurrency (`If-Match` → 412), not
  locks, on the standard update path. Operations that must read-modify-write atomically
  (e.g. PATCH) take a row lock *within* their transaction.
- **History is append-only and immutable**, uniquely keyed by
  `(tenant_id, fhir_id, resource_type, version_id)` — concurrent writers can't collide on
  a version number, and `vread` always sees a stable snapshot.

---

## 13. Configuration

`internal/config`. Precedence is **environment variable > YAML file > built-in default**,
so a checked-in `config.yaml` can hold non-secret defaults while secrets and per-env
overrides come from the environment.

- **Strict YAML parsing** (`KnownFields(true)`): an unrecognized config key is a loud
  error, not a silent no-op — typos surface immediately.
- **Everything is optional**: pure env-var configuration works (container-friendly), and
  every key has a default.
- **HTTP timeouts are configurable** (read/write/idle). `WriteTimeout` bounds the *entire*
  handler, so deployments ingesting large transaction bundles must raise it (or set `0`)
  to avoid cutting legitimate long requests after they've already committed.
- **IGs are opt-in** (`ig.packages` / `IG_PACKAGES`); empty by default.

---

## 14. Startup & lifecycle

Ordered initialization in `cmd/server`:

1. Load config → set up structured JSON logging.
2. Connect to PostgreSQL.
3. Create tables if requested / expected (idempotent schema).
4. Seed base FHIR R4 search parameters (idempotent; non-fatal on failure).
5. Load the search-parameter registry from the DB.
6. Build the store and router.
7. **Start listening immediately.**
8. Load IG packages in **background goroutines** (one per package); set the `igReady`
   flag when all finish.

- **Liveness before readiness.** `/health/live` is 200 as soon as the process is up;
  `/health/ready` is 503 while IGs load and 200 once `igReady` is set. In Kubernetes,
  the readiness probe gates traffic so clients never hit a half-loaded registry, while
  the liveness probe doesn't kill a server that's merely still loading IGs.
- **IG failures are non-fatal.** A bad package logs a warning and the others continue —
  one broken IG can't take down the server. The trade-off: if a package never succeeds,
  `igReady` never flips, so readiness stays 503 (surfacing the problem rather than
  pretending all is well).
- **Graceful shutdown.** SIGINT/SIGTERM trigger a bounded (15s) drain of in-flight
  requests.

---

## 15. Performance engineering

The schema encodes lessons from load testing — these are deliberate, evidence-driven
choices (`internal/db/schema.sql`):

- **Narrow "source" indexes on every `sp_*` table.** `idx_sp_*_source` was slimmed to
  `(tenant_id, resource_id, resource_type)` — exactly what the per-resource re-index
  `DELETE` and the FK `ON DELETE CASCADE` need — dropping wider forms that only helped
  rare index-only multi-param joins. This cuts write amplification on the ingest path.
- **Redundant indexes removed.** e.g. `idx_sp_tok_system` was dropped because it was a
  strict prefix of the `system, code` index the planner already uses — it only added
  write cost on the heaviest `sp_*` table.
- **Raised planner statistics targets** (`SET STATISTICS 1000`) on high-cardinality
  columns (`sp_token.code/system`, `sp_reference.target_id`, etc.) so multi-parameter
  searches get accurate row estimates and good plans.
- **Aggressive autovacuum** on `resources`, `resource_history`, and all `sp_*` tables
  (`autovacuum_vacuum_scale_factor = 0.02` vs PostgreSQL's `0.20` default). Load tests
  showed throughput degrading after ~500s as dead tuples accumulated under the default;
  2% keeps autovacuum ahead of write-heavy workloads.

The throughline: writes are the expensive path here (document + history + N `sp_*`
rows), so the indexing strategy is tuned to keep write amplification down while
preserving index-only reads.

---

## 16. Schema versioning & migrations

A `schema_version` table records the applied revision; the schema stamps versions as it
evolves (currently up to v6: the write-amplification index diet at v5, RLS at v6).

- The schema is written with `CREATE ... IF NOT EXISTS` and idempotent `ALTER ... SET`,
  so applying it to a fresh or existing database converges.
- **Caveat:** `CREATE INDEX IF NOT EXISTS` won't *alter* an index that already exists
  under the same name. Structural changes to existing indexes (like the v5 diet) require
  an explicit DROP+recreate migration on already-provisioned databases — fresh installs
  get the lean form directly.

---

## 17. Deliberate non-goals & known limitations

Consolidated here so they're easy to find. Each is a conscious choice, not an oversight.

| Area | Decision | Rationale |
|---|---|---|
| Terminology | No local code-system/value-set logic; delegate to external TX server | Terminology is a large, separately-maintained problem |
| JSON indexing | No GIN index on `resource_json` | ~2.4× write cost for no benefit to the query patterns used |
| Validation | Profile validation off by default, and only for resources declaring `meta.profile` | Interoperability first; strictness is opt-in |
| Unknown profile URL | Soft-skip, not 422 | A resource may reference an IG this server hasn't loaded |
| Reindex | No reindex of existing data when params change ([#11](https://github.com/wso2/fhir-server/issues/11)) | Indexing is write-time; bulk reindex is future work |
| Search params | Composite & special (e.g. `Location.near` without support) fail closed | Don't silently widen result sets |
| Unknown param | Heuristic typing, not rejection | Graceful degradation for not-yet-loaded custom params |
| FHIRPath | Scoped subset (no arithmetic/string ops) | Engine exists for search extraction, not general evaluation |
| Serialization | XML/Turtle best-effort; JSON first-class | JSON is the dominant FHIR wire format |
| Conditional delete | Single match only | Per FHIR `single` conditional-delete capability |
| Concurrency | Optimistic (`If-Match`), no pessimistic locking on the normal path | Lost-update safety without lock contention |
| Geospatial | lat/lng columns, not PostGIS, by default | PostGIS is opt-in for heavy geospatial workloads |

---

*This document describes design intent and rationale. For exact APIs, config keys, and
operational steps, see the [README](README.md). When behavior and this document
disagree, the code is the source of truth — please update this file in the same PR.*
