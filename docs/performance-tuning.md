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

## 5. Reserved keys (roadmap — not yet implemented)

Reserved so future work does not collide with these names:

| Key | Intent |
|---|---|
| `search.walkScanBudget` | Adaptive fallback: give the dense-branch walk an entry-scan budget; if a page isn't filled within budget, re-run as the sparse shape. Guards against temporally clustered matches (e.g. `date=lt2015` on recent-heavy data) — the one distribution the two-branch rule doesn't bound. |
| `search.probeCapAuto` | Derive `probeCap` per resource type from cached `pg_class.reltuples` via the √ rule, instead of a single global value. |
