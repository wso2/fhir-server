# Performance Tuning

This server's search layer is built around one architectural rule: every FHIR
search reduces to *find resources matching a predicate on an sp table, ordered by
recency, first page only*. Predicate order and recency order differ, and the right
plan depends on match density — which PostgreSQL's per-column statistics cannot
estimate for these tables. The tunables below control the mechanisms that work
around that; the defaults are calibrated for a ~500k-row-per-partition dataset and
change nothing until you override them.

All runtime tunables follow the standard precedence: **environment variable > config
file > built-in default**. Out-of-range values fail startup with a message naming
the offending key and its allowed range. The server logs the effective search
tuning at `info` on boot (`search tuning probeCap=… planCacheMode=…`) — check that
line to confirm an override took effect.

## 1. Runtime tunables (configurable)

| Key (`config.yaml`) | Env var | Default | Range | What it does |
|---|---|---|---|---|
| `search.probeCap` | `SEARCH_PROBE_CAP` | `5000` | 100–1,000,000 | Caps the density probe's counted matches for half-bounded range searches (quantity/date/number). Probe result **below** cap → sp-first seek (`MATERIALIZED`-pinned); probe **hits** cap → recency walk. The central knob. |
| `search.defaultPageSize` | `SEARCH_DEFAULT_PAGE_SIZE` | `20` | 1–1,000 | Page size when the request omits `_count`. |
| `search.maxPageSize` | `SEARCH_MAX_PAGE_SIZE` | `0` | 0 or 1–10,000 | Upper clamp on a client-supplied `_count`. `0` = unlimited (legacy behavior). Recommended `200` in production. |
| `search.maxChainDepth` | `SEARCH_MAX_CHAIN_DEPTH` | `5` | 1–10 | Bounds chained-parameter recursion depth. |
| `database.planCacheMode` | `DATABASE_PLAN_CACHE_MODE` | `force_custom_plan` | `force_custom_plan` \| `auto` \| `force_generic_plan` | Per-connection `plan_cache_mode`. **Load-bearing** — see the warning below. |
| `write.maxRowsPerStatement` | `WRITE_MAX_ROWS_PER_STATEMENT` | `1000` | 100–20,000 | Rows per multi-row `INSERT` in the bundle writer. Also clamped by Postgres's 65,535-parameter protocol limit at write time. Smaller → smaller per-statement parse tree (less backend memory). |
| `write.maxRowsPerBundle` | `WRITE_MAX_ROWS_PER_BUNDLE` | `100000` | 1,000–100,000,000 | Max index rows a single write transaction may buffer. A write that would exceed it is rejected with **HTTP 413** and rolled back — the safety valve that stops a pathological bundle from OOM-ing the database. Raise for trusted bulk-import windows. |

### Sizing `probeCap`: the √ rule

The probe makes the sparse/dense plan choice cheap in both directions:

- **Sparse branch** (probe < cap): the sp-first seek's cost is bounded by `cap`
  narrow index-only rows (a top-N sort over at most `cap` rows), pinned with a
  `MATERIALIZED` CTE so the planner cannot revert to the walk.
- **Dense branch** (probe hits cap): the recency walk's cost is bounded by roughly
  `pageSize × partitionRows / matchCount` index entries before a page fills.

Both branch costs are equal, and the whole thing is minimized, at:

```
probeCap ≈ sqrt(pageSize × P)
```

where `P` is the number of sp-table rows for the hot resource type / param. For
`pageSize = 20` and `P ≈ 500,000`, that is `sqrt(20 × 500000) ≈ 3162`, and `5000`
sits comfortably in the flat region of the curve — hence the default. **Re-derive
when the dataset grows ~10x** (P → 5M pushes the optimum to ~10,000).

### Why `plan_cache_mode` must stay `force_custom_plan`

Several predicates have plan quality that depends on the specific bound value's
selectivity — most notably token equality, where a generic plan cannot tell a
sparse code (id-first resolve) from a dense one (ordered early-exit). Under a
generic plan a dense token like `category=laboratory` (~70% of rows) degrades to a
HashAggregate over the whole match set (~2.9s); with a custom plan the planner sees
the value's MCV stats and takes the ordered scan (~6ms). `auto` and
`force_generic_plan` re-expose exactly the misestimate pathologies the probe
architecture exists to avoid — change this only for controlled experiments.

## 2. The two-branch probe rule (do not add a third branch)

The density probe has **exactly two** branches, keyed strictly on whether the
capped count hit the cap:

- `count < cap` → **sparse**: sp-first value-index seek, `MATERIALIZED`-pinned.
- `count == cap` → **dense**: recency walk, unconditionally.

> ⚠️ **Do not add a "materialize when capped but the partition is large" third
> branch.** A capped probe gives a *lower* bound only — it never bounds the true
> match count. A predicate matching most of the partition (e.g.
> `value-quantity=le99999`) hits the cap exactly like one matching precisely `cap`
> rows. Materializing at the cap therefore pulls the *entire* match set: measured
> **~870ms / 41k buffers** for `le99999`, versus **~1.6ms** for the walk — a ~4x+
> regression on the hot path. At the cap, the walk is the only safe choice.

## 3. Deploy-time DDL tunables (documented, not config-wired)

These live in `internal/db/schema.sql` and take effect at provisioning. Runtime
config cannot meaningfully own them, and templating DDL from config adds risk for
no benefit, so they are fixed in the schema — but an operator can retune a live
database with the recipes below (no re-provisioning required).

| Value | Where | Why this value |
|---|---|---|
| `SET STATISTICS 1000` on sp bound/code columns | schema.sql, statistics section | Histogram resolution for extreme-bound selectivity; the default 100 buckets are too coarse for values spanning orders of magnitude. |
| `autovacuum_vacuum_scale_factor = 0.02`, `autovacuum_analyze_scale_factor = 0.01` per sp table + `resources` | schema.sql, autovacuum section | Index-only scans (every fast path) need a fresh visibility map; a stale one was measured at **2x** walk cost. |
| Extended statistics (`stx_sp_token_*`, `stx_sp_comp_*`) | schema.sql | Cross-column equality estimates (resource_type × param_name × code). |

### Post-deploy retune recipes

```sql
-- Raise autovacuum aggressiveness on a hot sp table:
ALTER TABLE sp_quantity SET (autovacuum_vacuum_scale_factor = 0.01,
                             autovacuum_analyze_scale_factor = 0.005);

-- Raise histogram resolution on a bound column, then re-analyze:
ALTER TABLE sp_quantity ALTER COLUMN value_high SET STATISTICS 2000;
ANALYZE sp_quantity;

-- One-time cleanup on databases provisioned before the sp_date recency work:
-- idx_sp_date_low (covering) supersedes the old non-covering idx_sp_date_range.
-- A fresh schema never creates idx_sp_date_range; existing installs can drop it.
DROP INDEX IF EXISTS idx_sp_date_range;
```

`VACUUM`/`ANALYZE` must each run as their own statement (they cannot run inside a
multi-statement transaction). After any bulk data load, run `VACUUM (ANALYZE)` on
`resources` and every sp table before measuring.

## 4. Regression gates

After **any** dataset change or `probeCap` change, both canonical extremes must
return in single-digit milliseconds. They are the sparse-extreme and dense-extreme
probes of the same predicate class:

```
GET /Observation?value-quantity=ge99999    # sparse extreme → sp-first seek
GET /Observation?value-quantity=le99999    # dense extreme  → recency walk
```

If `le99999` regresses to hundreds of ms, a third probe branch has almost certainly
been reintroduced (see §2). Confirm plan shape with `EXPLAIN (ANALYZE, BUFFERS)` and
the tenant GUC set: the sparse case should ride `idx_sp_qty_high` under a
`MATERIALIZED` CTE; the dense case should be a recency walk with no `MATERIALIZED`.

## 5. Bulk import & the write path

The search sections above tune reads. Transaction-bundle import is the hot write
path, and it has its own rules. Index and history writes for a whole bundle are
batched: each entry's sp\_\* rows are accumulated in one transaction and flushed as
a handful of multi-row `INSERT`s (plus one re-index `DELETE` per sp table),
instead of one round trip per row. That collapses a ~1,000-entry bundle from
~13k single-row statements to a few dozen. Two consequences to tune for:

### Bounding batched writes so one bundle can't OOM the database

The writer buffers a transaction's index rows and emits them as multi-row
`INSERT`s. Two limits keep that bounded (see the table in §1):

- **`write.maxRowsPerStatement`** (default 1,000) bounds one statement's parse
  tree — the per-backend memory a runaway insert would otherwise inflate. It is
  additionally clamped to Postgres's 65,535-parameter protocol ceiling, so a
  large value can never produce an invalid statement.
- **`write.maxRowsPerBundle`** (default 100,000) is the safety valve: if a single
  write transaction would buffer more index rows than this, it is rejected with
  **HTTP 413** and rolled back before anything is sent to the database. This
  turns a pathological bundle (e.g. a resource that extracts a runaway number of
  composite rows) into a failed *request* instead of an out-of-memory *cluster*.
  A realistic large bundle is ~30–50k index rows, so the default leaves headroom;
  raise it for trusted bulk-import environments where you accept the larger
  transaction, and pair the raise with adequate database memory.

### `SERVER_WRITE_TIMEOUT` must exceed the worst-case bundle import

A transaction bundle is **one** DB transaction and **one** HTTP handler
invocation, so `SERVER_WRITE_TIMEOUT` (`server.writeTimeout`, default **60s**)
bounds the entire import. If a large bundle's import can exceed it, the server
kills the handler mid-flight — the client sees a connection reset (EOF) and the
transaction rolls back, wasting the whole attempt. Size the timeout above your
largest expected bundle's import time with headroom; for dedicated import windows
(multi-thousand-entry bundles, many concurrent loaders) `SERVER_WRITE_TIMEOUT=300s`
is a reasonable starting point. This is a ceiling, not a target: normal small
writes are unaffected.

### Autovacuum interacts with bulk import — post-load `VACUUM (ANALYZE)` is mandatory

The aggressive per-table autovacuum thresholds from §3
(`autovacuum_vacuum_scale_factor = 0.02` / `autovacuum_analyze_scale_factor = 0.01`)
keep the visibility map and statistics fresh for the index-only read fast paths.
A bulk import inserts far faster than autovacuum can keep up, so immediately after
a load the visibility map is stale (index-only scans degrade ~2x) and planner
statistics lag the new row counts (misestimated plans). **After any bulk import,
run `VACUUM (ANALYZE)` on `resources` and every sp table before measuring or
serving read traffic** — this is not optional, and it is the same requirement
called out in §3. Each `VACUUM`/`ANALYZE` runs as its own statement (never inside
a transaction).

### Optional: `synchronous_commit = off` for import-only environments

For a throwaway or reloadable import environment, setting
`synchronous_commit = off` lets commits return without waiting for WAL flush to
disk, which materially speeds up a write-heavy import. **The tradeoff is a
data-loss window:** on an OS/hardware crash the most recent committed transactions
can be lost (the database stays *consistent* — this is not corruption — but
acknowledged writes may vanish). Never default it, and never enable it where an
acknowledged write must survive a crash (i.e. any environment holding real
patient data). Scope it as tightly as possible — e.g. per-session
`SET synchronous_commit = off` on the import connection — and turn it off again
before the environment takes production traffic.

## 6. Reserved keys (roadmap — not yet implemented)

Reserved so future work does not collide with these names:

| Key | Intent |
|---|---|
| `search.walkScanBudget` | Adaptive fallback: give the dense-branch walk an entry-scan budget; if a page isn't filled within budget, re-run as the sparse shape. Guards against temporally clustered matches (e.g. `date=lt2015` on recent-heavy data) — the one distribution the two-branch rule doesn't bound. |
| `search.probeCapAuto` | Derive `probeCap` per resource type from cached `pg_class.reltuples` via the √ rule, instead of a single global value. |
