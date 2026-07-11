-- FHIR Server PostgreSQL Schema
-- One resources table holds all FHIR resource types. Separate sp_* tables
-- store extracted search parameter values so searches never touch resource_json.
-- Requires PostgreSQL 13+. For Location near-search, install PostGIS.

-- ─── Schema version ──────────────────────────────────────────────────────────
-- Tracks the schema revision applied to this database.

CREATE TABLE IF NOT EXISTS schema_version (
    version     INT         NOT NULL PRIMARY KEY,
    upgraded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── Master resource table ────────────────────────────────────────────────────
-- Stores every FHIR resource. resource_json holds the full FHIR document.
-- search_text is a pre-built tsvector used for _text / _content full-text search.
-- No GIN index is created on resource_json because all searches go through the
-- sp_* tables; indexing the entire document would cost ~2.4x on writes with no
-- benefit to the query patterns used here.

CREATE TABLE IF NOT EXISTS resources (
    tenant_id     TEXT         NOT NULL DEFAULT current_setting('app.current_tenant', true),
    fhir_id       VARCHAR(64)  NOT NULL,
    resource_type VARCHAR(100) NOT NULL,
    version_id    INT          NOT NULL DEFAULT 1,
    last_updated  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    is_deleted    BOOLEAN      NOT NULL DEFAULT FALSE,
    resource_json JSONB        NOT NULL,
    search_text   TSVECTOR,
    PRIMARY KEY (tenant_id, resource_type, fhir_id)
);

-- List all resources of a type ordered by recency (used by GET /{type}).
CREATE INDEX IF NOT EXISTS idx_res_type_updated ON resources (tenant_id, resource_type, last_updated DESC);
-- Same as above but skips soft-deleted resources (used by most searches).
CREATE INDEX IF NOT EXISTS idx_res_active       ON resources (tenant_id, resource_type, last_updated DESC) WHERE is_deleted = FALSE;
-- Full-text search over search_text (_text / _content search parameters).
CREATE INDEX IF NOT EXISTS idx_res_search_text  ON resources USING GIN (search_text);

-- ─── Version history ──────────────────────────────────────────────────────────
-- Append-only log of every create, update, and delete. Each row is a full
-- snapshot of resource_json at that version, enabling vread and audit trails.

CREATE TABLE IF NOT EXISTS resource_history (
    id            BIGSERIAL    PRIMARY KEY,
    tenant_id     TEXT         NOT NULL DEFAULT current_setting('app.current_tenant', true),
    fhir_id       VARCHAR(64)  NOT NULL,
    resource_type VARCHAR(100) NOT NULL,
    version_id    INT          NOT NULL,
    operation     VARCHAR(10)  NOT NULL,   -- CREATE | UPDATE | DELETE
    recorded_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    resource_json JSONB,
    UNIQUE (tenant_id, fhir_id, resource_type, version_id)
);

-- Fetch a specific version of a resource (GET /{type}/{id}/_history/{vid}).
CREATE INDEX IF NOT EXISTS idx_hist_resource  ON resource_history (tenant_id, resource_type, fhir_id, version_id DESC);
-- Global history feed ordered by time (GET /_history).
CREATE INDEX IF NOT EXISTS idx_hist_time      ON resource_history (tenant_id, recorded_at DESC);
-- History feed for a single resource type ordered by time (GET /{type}/_history).
CREATE INDEX IF NOT EXISTS idx_hist_type_time ON resource_history (tenant_id, resource_type, recorded_at DESC);

-- ─── String search index ─────────────────────────────────────────────────────
-- Stores extracted values for FHIR string search parameters (name, address, etc.).
-- value_exact keeps the original casing for the :exact modifier.
-- value_lower stores the downcased value for the default case-insensitive prefix match.

CREATE TABLE IF NOT EXISTS sp_string (
    id            BIGSERIAL    PRIMARY KEY,
    tenant_id     TEXT         NOT NULL DEFAULT current_setting('app.current_tenant', true),
    resource_id   VARCHAR(64)  NOT NULL,
    resource_type VARCHAR(100) NOT NULL,
    param_name    VARCHAR(191) NOT NULL,
    value_exact   VARCHAR(512),
    value_lower   VARCHAR(512),
    FOREIGN KEY (tenant_id, resource_id, resource_type) REFERENCES resources (tenant_id, fhir_id, resource_type) ON DELETE CASCADE
);

-- text_pattern_ops is required for LIKE 'prefix%' to use a btree index under
-- non-C collations (e.g. en_US.utf8). Without it, prefix scans fall back to
-- a sequential scan. The operator class also serves equality lookups.
CREATE INDEX IF NOT EXISTS idx_sp_str_lower_pattern ON sp_string (tenant_id, resource_type, param_name, value_lower text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_sp_str_exact         ON sp_string (tenant_id, resource_type, param_name, value_exact);
-- Leading on resource_id serves the per-resource EXISTS probe of multi-parameter
-- searches (correlated on resource_id/resource_type) and per-resource re-index
-- DELETEs / FK ON DELETE CASCADE. param_name + value_lower let the probe narrow
-- to the parameter and resolve its value predicate index-only, instead of heap-
-- fetching every sp_string row of the candidate resource. (The v5 diet slimmed
-- this to (resource_id, resource_type); that regressed multi-param search — see
-- the schema_version v8 note below.)
CREATE INDEX IF NOT EXISTS idx_sp_str_source        ON sp_string (tenant_id, resource_id, resource_type, param_name, value_lower);
-- Uncomment for :contains support (requires pg_trgm extension):
-- CREATE EXTENSION IF NOT EXISTS pg_trgm;
-- CREATE INDEX idx_sp_str_trgm ON sp_string USING GIN (value_lower gin_trgm_ops);

-- ─── Token search index ───────────────────────────────────────────────────────
-- Stores extracted values for FHIR token search parameters
-- (CodeableConcept, Coding, Identifier, code, boolean).
-- display is stored to support the :text modifier (match on the human label).

CREATE TABLE IF NOT EXISTS sp_token (
    id            BIGSERIAL    PRIMARY KEY,
    tenant_id     TEXT         NOT NULL DEFAULT current_setting('app.current_tenant', true),
    resource_id   VARCHAR(64)  NOT NULL,
    resource_type VARCHAR(100) NOT NULL,
    param_name    VARCHAR(191) NOT NULL,
    system        VARCHAR(512),
    code          VARCHAR(191),
    display       VARCHAR(512),
    -- Denormalised copy of resources.last_updated, set at index time. Lets the
    -- composite early-exit drive (fetchSQL's composite shape) walk sp_token
    -- newest-first for a code and stop after one page, without a resources lookup.
    last_updated  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    FOREIGN KEY (tenant_id, resource_id, resource_type) REFERENCES resources (tenant_id, fhir_id, resource_type) ON DELETE CASCADE
);

-- Primary lookup: system|code pairs (the most common token search pattern).
CREATE INDEX IF NOT EXISTS idx_sp_tok_sys_code ON sp_token (tenant_id, resource_type, param_name, system, code);
-- (idx_sp_tok_system dropped: it was a strict prefix of idx_sp_tok_sys_code
--  above, which the planner already uses for system-only lookups — the separate
--  index only added write cost on the heaviest sp_* table.)
-- Lookup by code alone when the search omits system.
CREATE INDEX IF NOT EXISTS idx_sp_tok_code ON sp_token (tenant_id, resource_type, param_name, code) WHERE code IS NOT NULL;
-- Leading on resource_id serves the per-resource EXISTS probe of multi-parameter
-- searches and re-index deletes; param_name + system + code let the probe filter
-- the token value index-only rather than heap-fetching every sp_token row of the
-- candidate resource. (Restored from the v5 diet's (resource_id, resource_type) —
-- see the schema_version v8 note below. idx_sp_tok_system stays dropped: it is a
-- strict prefix of idx_sp_tok_sys_code and genuinely redundant for reads.)
CREATE INDEX IF NOT EXISTS idx_sp_tok_source ON sp_token (tenant_id, resource_id, resource_type, param_name, system, code);

-- ─── Date search index ────────────────────────────────────────────────────────
-- Stores extracted values for FHIR date / dateTime / Period / instant parameters.
-- Partial-precision dates (e.g. "2000", "2000-04") are expanded into a
-- [value_low, value_high] range at write time so all 8 FHIR date comparators
-- (eq, ne, lt, gt, le, ge, sa, eb) work correctly without special casing.
-- value_precision records the original granularity (YEAR|MONTH|DAY|SECOND).

CREATE TABLE IF NOT EXISTS sp_date (
    id              BIGSERIAL    PRIMARY KEY,
    tenant_id     TEXT         NOT NULL DEFAULT current_setting('app.current_tenant', true),
    resource_id     VARCHAR(64)  NOT NULL,
    resource_type   VARCHAR(100) NOT NULL,
    param_name      VARCHAR(191) NOT NULL,
    value_low       TIMESTAMPTZ  NOT NULL,
    value_high      TIMESTAMPTZ  NOT NULL,
    value_precision VARCHAR(10)  NOT NULL DEFAULT 'SECOND',
    FOREIGN KEY (tenant_id, resource_id, resource_type) REFERENCES resources (tenant_id, fhir_id, resource_type) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_sp_date_range  ON sp_date (tenant_id, resource_type, param_name, value_low, value_high);
-- Serves the per-resource EXISTS probe of multi-parameter searches and re-index
-- deletes; param_name + range columns keep the probe index-only. (Restored from
-- the v5 diet's (resource_id, resource_type) — see the schema_version v8 note.)
CREATE INDEX IF NOT EXISTS idx_sp_date_source ON sp_date (tenant_id, resource_id, resource_type, param_name, value_low, value_high);

-- ─── Number search index ──────────────────────────────────────────────────────
-- Stores extracted values for FHIR number search parameters.
-- value_low / value_high encode the implicit precision range around value so
-- that FHIR's "approximately equal" (eq) semantics work: e.g. searching for
-- 100 matches 100.4 but not 100.5.

CREATE TABLE IF NOT EXISTS sp_number (
    id            BIGSERIAL     PRIMARY KEY,
    tenant_id     TEXT         NOT NULL DEFAULT current_setting('app.current_tenant', true),
    resource_id   VARCHAR(64)   NOT NULL,
    resource_type VARCHAR(100)  NOT NULL,
    param_name    VARCHAR(191)  NOT NULL,
    value         DECIMAL(20,6) NOT NULL,
    value_low     DECIMAL(20,6) NOT NULL,
    value_high    DECIMAL(20,6) NOT NULL,
    -- Denormalised copy of resources.last_updated, set at index time to the exact
    -- value written to resources. Lets the id-first fetch sort candidates straight
    -- from idx_sp_num_range without a per-match resources lookup — see fetchSQL's
    -- direct-drive shape.
    last_updated  TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    FOREIGN KEY (tenant_id, resource_id, resource_type) REFERENCES resources (tenant_id, fhir_id, resource_type) ON DELETE CASCADE
);

-- Migration for pre-existing deployments: CREATE TABLE IF NOT EXISTS above does
-- NOT add last_updated to an already-created sp_number, and the covering /
-- recency indexes below reference it (idx_sp_num_recent keys on it). ADD COLUMN
-- IF NOT EXISTS is a no-op on fresh installs and backfills existing rows with the
-- DEFAULT, so both paths end up with the column before the indexes are built.
ALTER TABLE sp_number ADD COLUMN IF NOT EXISTS last_updated TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- INCLUDE (resource_id, last_updated) makes the range scan covering: the id-first
-- candidate resolve is index-only, yielding the fhir_id to join and the sort key
-- to order by without touching the heap.
CREATE INDEX IF NOT EXISTS idx_sp_num_range  ON sp_number (tenant_id, resource_type, param_name, value_low, value_high) INCLUDE (resource_id, last_updated);
-- Serves the per-resource EXISTS probe of multi-parameter searches and re-index
-- deletes; param_name + range columns keep the probe index-only. (Restored from
-- the v5 diet's (resource_id, resource_type) — see the schema_version v8 note.)
CREATE INDEX IF NOT EXISTS idx_sp_num_source ON sp_number (tenant_id, resource_id, resource_type, param_name, value_low, value_high);

-- ─── Quantity search index ────────────────────────────────────────────────────
-- Stores extracted values for FHIR quantity search parameters.
-- value / value_low / value_high hold the raw value with its precision range.
-- canonical_value / canonical_units hold the UCUM-normalised equivalent so
-- that cross-unit comparisons work (e.g. searching "1g" matches "1000mg").

CREATE TABLE IF NOT EXISTS sp_quantity (
    id               BIGSERIAL     PRIMARY KEY,
    tenant_id     TEXT         NOT NULL DEFAULT current_setting('app.current_tenant', true),
    resource_id      VARCHAR(64)   NOT NULL,
    resource_type    VARCHAR(100)  NOT NULL,
    param_name       VARCHAR(191)  NOT NULL,
    value            DECIMAL(20,6) NOT NULL,
    value_low        DECIMAL(20,6) NOT NULL,
    value_high       DECIMAL(20,6) NOT NULL,
    system           VARCHAR(255),
    code             VARCHAR(64),
    canonical_value  DECIMAL(20,6),
    canonical_units  VARCHAR(64),
    -- Denormalised copy of resources.last_updated, set at index time to the exact
    -- value written to resources. Lets the id-first fetch sort candidates straight
    -- from idx_sp_qty_raw without a per-match resources lookup — see fetchSQL's
    -- direct-drive shape.
    last_updated     TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    FOREIGN KEY (tenant_id, resource_id, resource_type) REFERENCES resources (tenant_id, fhir_id, resource_type) ON DELETE CASCADE
);

-- Migration for pre-existing deployments (see the sp_number note above): backfill
-- last_updated before the covering / recency indexes that reference it are built.
ALTER TABLE sp_quantity ADD COLUMN IF NOT EXISTS last_updated TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- Raw value range search (same system+code, no unit conversion needed).
-- INCLUDE (resource_id, last_updated) makes the range scan covering so the
-- id-first candidate resolve is index-only (fhir_id to join + sort key to order).
CREATE INDEX IF NOT EXISTS idx_sp_qty_raw       ON sp_quantity (tenant_id, resource_type, param_name, value_low, value_high, system, code) INCLUDE (resource_id, last_updated);
-- Serves the per-resource EXISTS probe of multi-parameter searches and re-index
-- deletes. buildQuantityExists filters on value_low/value_high plus optional
-- system/code, so those trail param_name to keep the probe index-only — matching
-- the date/number _source indexes. (The pre-v5 form was (resource_id,
-- resource_type, param_name) with no value columns; widened here for index-only
-- parity. See the schema_version v8 note.)
CREATE INDEX IF NOT EXISTS idx_sp_qty_source    ON sp_quantity (tenant_id, resource_id, resource_type, param_name, value_low, value_high, system, code);
-- Canonical search (cross-unit comparison via UCUM normalisation).
CREATE INDEX IF NOT EXISTS idx_sp_qty_canonical ON sp_quantity (tenant_id, resource_type, param_name, canonical_value, canonical_units)
    WHERE canonical_value IS NOT NULL;

-- ─── URI search index ─────────────────────────────────────────────────────────
-- Stores extracted values for FHIR uri search parameters (url, profile, etc.).
-- Supports exact match and the :below modifier (prefix / hierarchy match).

CREATE TABLE IF NOT EXISTS sp_uri (
    id            BIGSERIAL    PRIMARY KEY,
    tenant_id     TEXT         NOT NULL DEFAULT current_setting('app.current_tenant', true),
    resource_id   VARCHAR(64)  NOT NULL,
    resource_type VARCHAR(100) NOT NULL,
    param_name    VARCHAR(191) NOT NULL,
    value         VARCHAR(512) NOT NULL,
    FOREIGN KEY (tenant_id, resource_id, resource_type) REFERENCES resources (tenant_id, fhir_id, resource_type) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_sp_uri_exact  ON sp_uri (tenant_id, resource_type, param_name, value);
-- text_pattern_ops enables efficient LIKE 'prefix%' for the :below modifier.
CREATE INDEX IF NOT EXISTS idx_sp_uri_prefix ON sp_uri (tenant_id, resource_type, param_name, value text_pattern_ops);
-- Serves the per-resource EXISTS probe of multi-parameter searches and re-index
-- deletes; param_name + value keep the probe index-only. (Restored from the v5
-- diet's (resource_id, resource_type) — see the schema_version v8 note.)
CREATE INDEX IF NOT EXISTS idx_sp_uri_source ON sp_uri (tenant_id, resource_id, resource_type, param_name, value);

-- ─── Reference search index ───────────────────────────────────────────────────
-- Stores extracted values for FHIR reference search parameters.
-- Also used for _include / _revinclude and $everything traversal.
-- target_url holds the literal URL when the reference is external (not local).
-- identifier_* columns support the :identifier modifier (search by Identifier
-- instead of resource id).

CREATE TABLE IF NOT EXISTS sp_reference (
    id                BIGSERIAL    PRIMARY KEY,
    tenant_id     TEXT         NOT NULL DEFAULT current_setting('app.current_tenant', true),
    resource_id       VARCHAR(64)  NOT NULL,
    resource_type     VARCHAR(100) NOT NULL,
    param_name        VARCHAR(191) NOT NULL,
    target_type       VARCHAR(100),
    target_id         VARCHAR(64),
    target_version_id INT,
    target_url        VARCHAR(512),
    identifier_system VARCHAR(512),
    identifier_value  VARCHAR(255),
    display           VARCHAR(255),
    FOREIGN KEY (tenant_id, resource_id, resource_type) REFERENCES resources (tenant_id, fhir_id, resource_type) ON DELETE CASCADE
);

-- Serves the per-resource EXISTS probe of multi-parameter searches and re-index
-- deletes; param_name + target_id let the probe resolve reference-by-source
-- predicates index-only. (Restored from the v5 diet's (resource_id, resource_type)
-- — see the schema_version v8 note below.)
CREATE INDEX IF NOT EXISTS idx_sp_ref_source      ON sp_reference (tenant_id, resource_id, resource_type, param_name, target_id);
-- Used when searching by target (e.g. ?patient=123): leading on target_id
-- serves bare-id lookups; extra columns allow the predicate to resolve index-only.
CREATE INDEX IF NOT EXISTS idx_sp_ref_target_full ON sp_reference (tenant_id, target_id, target_type, param_name, resource_type, resource_id);
-- Used for the :identifier modifier (find references by Identifier value).
CREATE INDEX IF NOT EXISTS idx_sp_ref_ident       ON sp_reference (tenant_id, target_type, identifier_system, identifier_value)
    WHERE identifier_value IS NOT NULL;

-- ─── Coordinates search index ─────────────────────────────────────────────────
-- Stores lat/lng for the Location.near search parameter.
-- For heavy geospatial workloads, consider replacing lat/lng with a
-- PostGIS geometry(Point,4326) column and a GIST index.

CREATE TABLE IF NOT EXISTS sp_coords (
    id            BIGSERIAL    PRIMARY KEY,
    tenant_id     TEXT         NOT NULL DEFAULT current_setting('app.current_tenant', true),
    resource_id   VARCHAR(64)  NOT NULL,
    resource_type VARCHAR(100) NOT NULL,
    param_name    VARCHAR(191) NOT NULL,
    latitude      DECIMAL(9,6) NOT NULL,
    longitude     DECIMAL(9,6) NOT NULL,
    FOREIGN KEY (tenant_id, resource_id, resource_type) REFERENCES resources (tenant_id, fhir_id, resource_type) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_sp_coords ON sp_coords (tenant_id, resource_type, param_name, latitude, longitude);

-- ─── Search parameter definitions ────────────────────────────────────────────
-- Registry of all known search parameters for each resource type.
-- Populated at server startup from the embedded FHIR R4 base spec (CSV) and
-- any loaded Implementation Guide packages.
-- ig_source: '' = base FHIR R4, 'user' = custom SearchParameter resource,
--            'name@version' = sourced from a specific IG package.
-- components_json: composite search parameter component expressions (JSON array).

CREATE TABLE IF NOT EXISTS search_param_definitions (
    id               SERIAL       PRIMARY KEY,
    resource_type    VARCHAR(191) NOT NULL,
    param_name       VARCHAR(191) NOT NULL,
    param_type       VARCHAR(32)  NOT NULL,
    fhirpath_expr    TEXT         NOT NULL,
    is_custom        BOOLEAN      NOT NULL DEFAULT FALSE,
    ig_source        TEXT         NOT NULL DEFAULT '',
    target_types     TEXT         NOT NULL DEFAULT '',
    components_json  TEXT         NOT NULL DEFAULT '',
    UNIQUE (resource_type, param_name)
);

CREATE INDEX IF NOT EXISTS idx_spd_resource ON search_param_definitions (resource_type);
CREATE INDEX IF NOT EXISTS idx_spd_custom   ON search_param_definitions (resource_type) WHERE is_custom = TRUE;
CREATE INDEX IF NOT EXISTS idx_spd_ig       ON search_param_definitions (ig_source) WHERE ig_source != '';

-- ─── Implementation Guide tracking ───────────────────────────────────────────
-- ig_packages records each loaded IG package so the server can skip re-downloading
-- it on restart. ig_profiles stores the StructureDefinition profiles declared by
-- each IG, used when building the CapabilityStatement.

CREATE TABLE IF NOT EXISTS ig_packages (
    id              SERIAL      PRIMARY KEY,
    package_name    TEXT        NOT NULL,
    package_version TEXT        NOT NULL,
    fhir_version    TEXT        NOT NULL DEFAULT '',
    loaded_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (package_name, package_version)
);

CREATE TABLE IF NOT EXISTS ig_profiles (
    id            SERIAL  PRIMARY KEY,
    package_name  TEXT    NOT NULL,
    profile_url   TEXT    NOT NULL,
    resource_type TEXT    NOT NULL DEFAULT '',
    sd_json       JSONB,
    UNIQUE (profile_url)
);

-- ─── Base FHIR R4 StructureDefinitions ────────────────────────────────────────
-- The core FHIR R4 resource StructureDefinitions (kind=resource,
-- derivation=specialization) shipped with the server and loaded at startup (see
-- internal/basedef). They let the server validate resources against the base
-- spec even when no profile is supplied. Like ig_profiles this is reference data,
-- not PHI, and is identical across tenants — so it carries no tenant_id and is
-- intentionally excluded from the Row-Level Security policies below.
CREATE TABLE IF NOT EXISTS base_definitions (
    resource_type TEXT        NOT NULL PRIMARY KEY,
    sd_url        TEXT        NOT NULL DEFAULT '',
    sd_json       JSONB       NOT NULL,
    loaded_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── FHIR Terminology: closure tables ─────────────────────────────────────────
-- Support the $closure operation, which maintains a transitive closure table of
-- subsumption relationships between coded concepts. A closure context groups
-- related concepts; ClosureDeltaTable records each subsumes/subsumed-by pair.

CREATE TABLE IF NOT EXISTS "ClosureContextTable" (
    "ID"           SERIAL       PRIMARY KEY,
    "NAME"         VARCHAR(191) NOT NULL UNIQUE,
    "LAST_UPDATED" TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS "ClosureConceptTable" (
    "ID"           SERIAL       PRIMARY KEY,
    "CONTEXT_ID"   INT          NOT NULL,
    "SYSTEM"       VARCHAR(512) NOT NULL,
    "CODE"         VARCHAR(191) NOT NULL,
    "LAST_UPDATED" TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    FOREIGN KEY ("CONTEXT_ID") REFERENCES "ClosureContextTable"("ID") ON DELETE CASCADE,
    UNIQUE ("CONTEXT_ID", "SYSTEM", "CODE")
);

CREATE INDEX IF NOT EXISTS idx_closure_concept ON "ClosureConceptTable" ("CONTEXT_ID", "SYSTEM", "CODE");

CREATE TABLE IF NOT EXISTS "ClosureDeltaTable" (
    "ID"           SERIAL       PRIMARY KEY,
    "CONTEXT_ID"   INT          NOT NULL,
    "SUBSUMES_ID"  INT          NOT NULL,
    "SUBSUMED_ID"  INT          NOT NULL,
    "LAST_UPDATED" TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    FOREIGN KEY ("CONTEXT_ID")  REFERENCES "ClosureContextTable"("ID")  ON DELETE CASCADE,
    FOREIGN KEY ("SUBSUMES_ID") REFERENCES "ClosureConceptTable"("ID")  ON DELETE CASCADE,
    FOREIGN KEY ("SUBSUMED_ID") REFERENCES "ClosureConceptTable"("ID")  ON DELETE CASCADE,
    UNIQUE ("CONTEXT_ID", "SUBSUMES_ID", "SUBSUMED_ID")
);

-- ─── Planner statistics ───────────────────────────────────────────────────────
-- Raise statistics targets for high-cardinality columns so the planner
-- produces accurate row-count estimates for multi-param searches.
ALTER TABLE sp_token     ALTER COLUMN code          SET STATISTICS 1000;
ALTER TABLE sp_token     ALTER COLUMN system        SET STATISTICS 1000;
ALTER TABLE sp_token     ALTER COLUMN resource_type SET STATISTICS 1000;
ALTER TABLE sp_token     ALTER COLUMN param_name    SET STATISTICS 1000;
ALTER TABLE sp_reference ALTER COLUMN target_id     SET STATISTICS 1000;
ALTER TABLE sp_reference ALTER COLUMN param_name    SET STATISTICS 1000;

-- Multivariate statistics: the per-column targets above still let the planner
-- assume resource_type / param_name / code are independent, so it badly
-- under-estimates a common (resource_type, param_name, code) combination — e.g.
-- Observation category=vital-signs (~20% of Observations) was estimated at <2%.
-- That misestimate made it materialize + sort the entire match set (135k rows,
-- one resources lookup each) instead of the abort-early ordered scan that stops
-- at the first page: ~966ms vs ~15ms on the perf dataset. MCV/dependencies over
-- the correlated columns correct the estimate so the planner picks the ordered
-- scan for dense codes and the id-first-style materialize for sparse ones.
-- Populated by ANALYZE (autoanalyze covers this after the bulk import).
CREATE STATISTICS IF NOT EXISTS stx_sp_token_rt_param_code (dependencies, ndistinct, mcv)
    ON resource_type, param_name, code FROM sp_token;

-- system|code searches (the common token form, e.g.
-- class=http://terminology.hl7.org/CodeSystem/v3-ActCode|AMB) filter on system
-- AND code, which are strongly correlated — a code like AMB only ever appears
-- under its own system. Without system in the stats the planner treats them as
-- independent and badly UNDER-estimates a dense combination: Encounter class=AMB
-- (62k rows) was estimated at 593, so it materialised + sorted every match and
-- joined resources per row (~2.7s) instead of the abort-early ordered scan
-- (~0.7ms). Adding system to the multivariate stat corrects the estimate so the
-- planner takes the ordered early-exit for dense system|code tokens and keeps
-- the id-first materialize for sparse ones. Populated by ANALYZE.
CREATE STATISTICS IF NOT EXISTS stx_sp_token_rt_param_sys_code (dependencies, ndistinct, mcv)
    ON resource_type, param_name, system, code FROM sp_token;

-- ─── Autovacuum tuning for high-churn tables ─────────────────────────────────
-- Default autovacuum_vacuum_scale_factor=0.20 means PostgreSQL waits until 20%
-- of a table is dead before cleaning up. On tables with millions of rows that
-- is millions of dead tuples — causing index bloat, planner misstimation, and
-- the throughput degradation visible after ~500 s in all load test runs.
-- Tighten to 2% so autovacuum stays ahead of write-heavy workloads.
--
-- These ALTER TABLE SET (...) statements are idempotent: safe to re-run on
-- an existing database.

ALTER TABLE resources SET (
    autovacuum_vacuum_scale_factor  = 0.02,
    autovacuum_analyze_scale_factor = 0.01
);

ALTER TABLE resource_history SET (
    autovacuum_vacuum_scale_factor  = 0.02,
    autovacuum_analyze_scale_factor = 0.01
);

-- sp_* tables are DELETE+INSERT heavy on every UPDATE; keep them lean so
-- index bloat does not accumulate between vacuum cycles.
ALTER TABLE sp_string    SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE sp_token     SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE sp_date      SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE sp_number    SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE sp_quantity  SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE sp_uri       SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE sp_reference SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE sp_coords    SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.01);

-- ─── Stamp schema version ─────────────────────────────────────────────────────

-- v5: ingest write-amplification diet — slimmed the 7 sp_* _source indexes to
-- (resource_id, resource_type) and dropped redundant idx_sp_tok_system. Fresh
-- installs get the lean form; existing DBs need a migration to DROP+recreate
-- (CREATE INDEX IF NOT EXISTS won't alter an index that already exists).
INSERT INTO schema_version (version) VALUES (5) ON CONFLICT DO NOTHING;

-- ─── Multi-tenancy: Row-Level Security ────────────────────────────────────────
-- Every PHI-bearing table (resources, resource_history, sp_*) carries a
-- tenant_id column (declared inline in the definitions above) that defaults to
-- the app.current_tenant runtime setting the server applies per request. Their
-- primary / foreign keys and the resource_history version-uniqueness all lead
-- with tenant_id, so two tenants may hold resources with the same id. The
-- global configuration tables (search_param_definitions, ig_*, the closure
-- tables, schema_version) are intentionally SHARED across tenants and carry no
-- tenant_id.
--
-- Isolation is enforced by Row-Level Security: each policy restricts rows to
-- the current tenant. FORCE makes the policy apply to the table owner too. An
-- unset tenant fails CLOSED — NULL matches no rows and violates the NOT NULL
-- tenant_id on write.
--
-- NOTE: the server (and any tooling that touches these tables at runtime) MUST
-- connect as a NON-superuser role — superusers and roles with BYPASSRLS ignore
-- Row-Level Security entirely.
DO $rls$
DECLARE
    t             text;
    tenant_tables text[] := ARRAY['resources','resource_history','sp_string','sp_token',
                                  'sp_date','sp_number','sp_quantity','sp_uri',
                                  'sp_reference','sp_coords'];
BEGIN
    FOREACH t IN ARRAY tenant_tables LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE  ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I '
            || 'USING (tenant_id = current_setting(''app.current_tenant'', true)) '
            || 'WITH CHECK (tenant_id = current_setting(''app.current_tenant'', true))',
            t);
    END LOOP;
END
$rls$;

INSERT INTO schema_version (version) VALUES (6) ON CONFLICT DO NOTHING;
-- v7: add base_definitions (core FHIR R4 StructureDefinitions for base validation)
INSERT INTO schema_version (version) VALUES (7) ON CONFLICT DO NOTHING;

-- v8: revert the v5 _source-index diet that regressed multi-parameter search.
-- Multi-param searches are built as correlated `EXISTS (SELECT 1 FROM sp_* s
-- WHERE s.resource_id = r.fhir_id AND s.resource_type = … AND s.param_name = …
-- AND <value predicate>)` (see internal/store/search.go). v5 slimmed the 7 sp_*
-- _source indexes to (tenant_id, resource_id, resource_type), dropping param_name
-- and the value columns. That forced the per-resource EXISTS probe to scan every
-- sp_* row of a candidate resource and heap-fetch each to re-check param_name and
-- value — the drastic search slowdown reported after 2671286. Restored above to
-- the pre-v5 composite forms (param_name + value columns, tenant_id-led) so the
-- probe narrows to the parameter and resolves its value predicate index-only.
-- idx_sp_tok_system stays dropped (a redundant strict prefix of idx_sp_tok_sys_code).
--
-- Fresh installs get the restored indexes above. EXISTING databases are NOT
-- altered by CREATE INDEX IF NOT EXISTS (it won't rebuild an index that already
-- exists), so each idx_sp_*_source must be dropped and recreated, e.g.:
--   DROP  INDEX CONCURRENTLY IF EXISTS idx_sp_str_source;
--   CREATE INDEX CONCURRENTLY idx_sp_str_source
--       ON sp_string (tenant_id, resource_id, resource_type, param_name, value_lower);
-- (repeat for tok/date/num/qty/uri/ref with the column lists above).
INSERT INTO schema_version (version) VALUES (8) ON CONFLICT DO NOTHING;

-- v9: mark numeric comparison operators LEAKPROOF so quantity/number range and
-- equality predicates push into the sp_quantity/sp_number indexes under RLS.
--
-- With Row-Level Security the sp_* tables are security barriers: a user WHERE
-- qual is evaluated *inside* the index scan (as an index cond) only if its
-- operator is LEAKPROOF; otherwise it is held back and applied as a post-filter
-- above the barrier. PostgreSQL ships text and date/timestamptz comparisons
-- LEAKPROOF, but NOT the numeric ones. So `value_low > $1` on sp_quantity
-- (numeric(20,6)) could not use the (tenant_id, resource_type, param_name,
-- value_low, …) index bound — every quantity/number search scanned the entire
-- (tenant, type, param) partition and filtered value_low in memory (~50ms/query
-- on the perf dataset, and far worse under concurrency where it spawned parallel
-- seq scans). numeric comparison operators are pure arithmetic and do not leak
-- argument values via errors or side channels, so marking them LEAKPROOF is safe.
--
-- Requires superuser: only a superuser may set LEAKPROOF. Wrapped so a non-super
-- DDL role (or managed Postgres with no superuser, e.g. Azure Flexible Server)
-- still applies the rest of the schema — the optimization is skipped with a
-- notice and those searches fall back to the correct, slower post-filter path.
DO $leakproof$
BEGIN
    ALTER FUNCTION numeric_eq(numeric, numeric) LEAKPROOF;
    ALTER FUNCTION numeric_gt(numeric, numeric) LEAKPROOF;
    ALTER FUNCTION numeric_ge(numeric, numeric) LEAKPROOF;
    ALTER FUNCTION numeric_lt(numeric, numeric) LEAKPROOF;
    ALTER FUNCTION numeric_le(numeric, numeric) LEAKPROOF;
EXCEPTION
    WHEN insufficient_privilege THEN
        RAISE NOTICE 'skipping LEAKPROOF on numeric comparison operators (requires superuser); quantity/number range searches under RLS will use a slower post-filter';
END
$leakproof$;

INSERT INTO schema_version (version) VALUES (9) ON CONFLICT DO NOTHING;

-- v10: recency covering indexes on the numeric sp_* tables, ordered by
-- last_updated DESC, for the early-exit id-first fetch (directDriveSQL).
--
-- The value indexes above (idx_sp_qty_raw / idx_sp_num_range) are ordered by
-- value, so a search sorted by recency had to read the entire match set and
-- top-N sort it — fine for a sparse predicate, but for a DENSE one (e.g.
-- value-quantity=le140 matching ~500k rows) that was a multi-hundred-ms
-- materialise-and-sort. Ordering the index by last_updated DESC lets the
-- planner walk it newest-first, apply the value predicate from the INCLUDE'd
-- columns, and stop after one page (~1ms), while a sparse/empty predicate still
-- uses the value index. The planner chooses between the two per bound value
-- (force_custom_plan) from the single sp_* table's own statistics — no
-- application-level plan forcing.
CREATE INDEX IF NOT EXISTS idx_sp_qty_recent ON sp_quantity (tenant_id, resource_type, param_name, last_updated DESC) INCLUDE (value_low, value_high, resource_id, system, code);
CREATE INDEX IF NOT EXISTS idx_sp_num_recent ON sp_number   (tenant_id, resource_type, param_name, last_updated DESC) INCLUDE (value_low, value_high, resource_id);

INSERT INTO schema_version (version) VALUES (10) ON CONFLICT DO NOTHING;

-- v11: recency covering index on sp_token, ordered by last_updated DESC, for the
-- composite early-exit drive (fetchSQL's composite shape, see search.go).
--
-- A composite token+quantity search (e.g. code-value-quantity) drives candidate
-- resolution from the selective token component, then filters by the quantity
-- component. Without a recency-ordered token index the drive had to resolve the
-- whole intersection and top-N sort it — cheap for a selective code, but for a
-- common code with a loose value bound (e.g. body-weight code + value>80, an
-- 8k-row intersection) that was a multi-second materialise-and-sort that also
-- tripped a parallel hash join. Ordering the index by last_updated DESC lets the
-- drive walk sp_token newest-first for the code, probe the quantity component
-- per row, and stop after one page. INCLUDE (resource_id, system) keeps the walk
-- (and an optional system filter) index-only.
--
-- Migration for pre-existing deployments (mirrors the sp_number / sp_quantity
-- v10 note): ADD COLUMN before the recency index that references it. Existing
-- rows take the migration-time default; the exact resources.last_updated is
-- written on the next re-index, matching how the numeric tables were populated.
ALTER TABLE sp_token ADD COLUMN IF NOT EXISTS last_updated TIMESTAMPTZ NOT NULL DEFAULT NOW();
CREATE INDEX IF NOT EXISTS idx_sp_tok_recent ON sp_token (tenant_id, resource_type, param_name, code, last_updated DESC) INCLUDE (resource_id, system) WHERE code IS NOT NULL;

INSERT INTO schema_version (version) VALUES (11) ON CONFLICT DO NOTHING;

-- v12: range-overlap GiST index on sp_quantity for the bounded quantity searches
-- (eq / ne / ge / le), reachable only through the numrange && operator.
--
-- Those prefixes are interval overlap — value_low <= searchHigh AND value_high >=
-- searchLow — but expressed as two independent numeric bounds the predicate can
-- only ride the btree value indexes (idx_sp_qty_raw / idx_sp_qty_recent). A dense
-- bounded window (e.g. value-quantity=ge10&le140 covering a large slice of one
-- (tenant, type, param) partition) still had to scan-and-filter that whole
-- partition. A GiST index over the stored interval answers the same overlap with
-- a range probe. buildQuantityExists now emits the predicate as
-- numrange(s.value_low, s.value_high, '[]') && numrange(searchLow, searchHigh, '[]');
-- the index expression below must stay byte-for-byte identical to that stored
-- numrange, or the planner will not match it. gt/lt stay scalar (strict, not
-- overlap) and keep using the btree value indexes.
--
-- The leading (tenant_id, resource_type, param_name) equality columns are varchar,
-- which have no default gist opclass, so the multicolumn GiST needs btree_gist.
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE INDEX IF NOT EXISTS idx_sp_qty_range_gist ON sp_quantity
    USING gist (tenant_id, resource_type, param_name, numrange(value_low, value_high, '[]'));
-- Production build note: on an existing large sp_quantity, build this out of band
-- with CREATE INDEX CONCURRENTLY before deploying the paired search.go change —
-- CONCURRENTLY cannot run inside schema.sql's single implicit transaction:
--   CREATE INDEX CONCURRENTLY idx_sp_qty_range_gist ON sp_quantity
--       USING gist (tenant_id, resource_type, param_name,
--                   numrange(value_low, value_high, '[]'));

-- LEAKPROOF the two functions the overlap predicate is built from, so it reaches
-- the GiST index under RLS. Under the FORCE ROW LEVEL SECURITY barrier on the
-- sp_* tables (see the tenant_isolation policy above) a qual is only pushed below
-- the barrier — and thus eligible to become an index cond — if EVERY function it
-- calls is LEAKPROOF; otherwise it is held back as a post-filter above the
-- barrier. This is the same barrier the v9 numeric-operator marking addressed. The
-- predicate is  numrange(s.value_low, s.value_high, '[]') && numrange($lo, $hi, '[]'),
-- so both the && operator (range_overlaps) AND the numrange constructor that runs
-- per row on value_low/value_high must be leakproof. Verified: with only
-- range_overlaps marked, the && stays a recheck filter under RLS and the GiST
-- index is never used to narrow by range (measured on PostgreSQL 15); marking both
-- pushes && into the GiST Index Cond. Leaving it half-done would also strand the
-- v10 recency early-exit, whose value filter over idx_sp_qty_recent would no longer
-- push into the scan.
--
-- Safety. range_overlaps is pure bound arithmetic and cannot leak. numrange is
-- less obvious: its 3-arg constructor raises "range lower bound must be less than
-- or equal to range upper bound" when lower > upper, and an argument-dependent
-- error is normally a side channel that disqualifies LEAKPROOF. It is safe HERE
-- because the expression index above computes the very same
-- numrange(value_low, value_high, '[]') for every row at write time — a row that
-- would make the constructor throw cannot be inserted while the index exists — so
-- the query-time constructor never errors on stored data. Caveat: LEAKPROOF is a
-- global property of the function, so it relaxes numrange() everywhere; this schema
-- uses numrange only for this index, but a future RLS table that constructs
-- numrange over unconstrained numerics would inherit the relaxed barrier. Same
-- superuser caveat / defensive wrapper as v9: a non-super DDL role still applies
-- the rest of the schema, and these searches fall back to the correct, slower
-- post-filter path.
DO $leakproof_range$
BEGIN
    ALTER FUNCTION range_overlaps(anyrange, anyrange) LEAKPROOF;
    ALTER FUNCTION numrange(numeric, numeric, text) LEAKPROOF;
EXCEPTION
    WHEN insufficient_privilege THEN
        RAISE NOTICE 'skipping LEAKPROOF on range_overlaps/numrange (requires superuser); bounded quantity searches under RLS will use a slower post-filter';
END
$leakproof_range$;

INSERT INTO schema_version (version) VALUES (12) ON CONFLICT DO NOTHING;
