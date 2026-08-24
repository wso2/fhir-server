// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied. See the License for the
// specific language governing permissions and limitations
// under the License.

// Package store implements FHIR CRUD operations against the normalized schema.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2/fhir-server/internal/index"
	"github.com/wso2/fhir-server/internal/searchparam"
	"github.com/wso2/fhir-server/internal/tenant"
	"github.com/wso2/fhir-server/internal/terminology"
)

// SearchTuning carries the search performance knobs the store reads at query
// time. They are configurable (see internal/config and docs/performance-tuning.md);
// New fills any zero field with its built-in default, so callers that don't pass
// WithSearchTuning keep the historical hardcoded behavior.
type SearchTuning struct {
	ProbeCap        int // density-probe cap (defaultProbeCap when 0)
	DefaultPageSize int // page size when _count omitted (defaultSearchPageSize when 0)
	MaxPageSize     int // clamp on client _count; 0 = unlimited
	MaxChainDepth   int // chained-parameter recursion bound (defaultMaxChainDepth when 0)
}

// defaultSearchTuning returns the built-in tunable values, identical to the
// store's historical hardcoded constants.
func defaultSearchTuning() SearchTuning {
	return SearchTuning{
		ProbeCap:        defaultProbeCap,
		DefaultPageSize: defaultSearchPageSize,
		MaxPageSize:     defaultMaxPageSize,
		MaxChainDepth:   defaultMaxChainDepth,
	}
}

// WriteTuning bounds the bundle writer's batched inserts so a single transaction
// cannot drive the database out of memory. MaxRowsPerStatement caps one multi-row
// INSERT; MaxRowsPerBundle caps the total index rows one transaction may buffer
// before it is rejected with a bounded 413. Both are configurable — see
// internal/config and docs/performance-tuning.md.
type WriteTuning struct {
	MaxRowsPerStatement int // rows per multi-row INSERT (defaultWriteMaxRowsPerStatement when 0)
	MaxRowsPerBundle    int // total index rows per transaction; 0 = unlimited (unsafe)
}

const (
	defaultWriteMaxRowsPerStatement = 1000
	defaultWriteMaxRowsPerBundle    = 100_000
)

// defaultWriteTuning returns the built-in write-path limits.
func defaultWriteTuning() WriteTuning {
	return WriteTuning{
		MaxRowsPerStatement: defaultWriteMaxRowsPerStatement,
		MaxRowsPerBundle:    defaultWriteMaxRowsPerBundle,
	}
}

type Store struct {
	pool         *pgxpool.Pool
	extractor    *index.Extractor
	registry     *searchparam.Registry
	terminology  *terminology.Client // may be nil if FHIR_TERMINOLOGY_URL is unset
	tuning       SearchTuning
	writeTuning  WriteTuning
	refIntegrity RefIntegrity
}

func New(pool *pgxpool.Pool, registry *searchparam.Registry, opts ...func(*Store)) *Store {
	s := &Store{
		pool:        pool,
		extractor:   index.New(registry),
		registry:    registry,
		tuning:      defaultSearchTuning(),
		writeTuning: defaultWriteTuning(),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// WithWriteTuning sets the write-path batching limits (resolved from config).
// Any non-positive MaxRowsPerStatement falls back to its default; MaxRowsPerBundle
// is preserved as given (0 means unlimited — unsafe, use only for trusted paths).
func WithWriteTuning(t WriteTuning) func(*Store) {
	return func(s *Store) {
		if t.MaxRowsPerStatement <= 0 {
			t.MaxRowsPerStatement = defaultWriteMaxRowsPerStatement
		}
		s.writeTuning = t
	}
}

// WithSearchTuning sets the search performance tunables (resolved from config).
// Any non-positive field falls back to its built-in default, so a partially
// populated struct is safe; MaxPageSize 0 is preserved (it means "unlimited").
func WithSearchTuning(t SearchTuning) func(*Store) {
	return func(s *Store) {
		if t.ProbeCap <= 0 {
			t.ProbeCap = defaultProbeCap
		}
		if t.DefaultPageSize <= 0 {
			t.DefaultPageSize = defaultSearchPageSize
		}
		if t.MaxChainDepth <= 0 {
			t.MaxChainDepth = defaultMaxChainDepth
		}
		s.tuning = t
	}
}

// WithTerminology configures the store to call tc for :in/:not-in expansion.
func WithTerminology(tc *terminology.Client) func(*Store) {
	return func(s *Store) { s.terminology = tc }
}

// ─── Tenant scoping ─────────────────────────────────────────────────────────
// Every PHI table (resources, resource_history, sp_*) is guarded by Postgres
// Row-Level Security keyed on the `app.current_tenant` runtime setting. The
// store must set it before any such table is touched, otherwise RLS fails
// closed (reads return nothing; writes violate NOT NULL on tenant_id).
//
//   - writes run in a transaction, so they SET LOCAL (scoped to the tx);
//   - reads run on a pooled connection acquired via tenantConn.
//
// The tenant id comes from the request context (tenant.From), defaulting to
// the "default" tenant for single-tenant deployments.

const (
	setTenantLocalSQL   = `SELECT set_config('app.current_tenant', $1, true)`  // tx-scoped
	setTenantSessionSQL = `SELECT set_config('app.current_tenant', $1, false)` // connection-scoped
)

// setTenantTx applies the request's tenant to a write transaction.
func setTenantTx(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, setTenantLocalSQL, tenant.From(ctx))
	return err
}

// tenantConn acquires a pooled connection with the request's tenant applied,
// for read paths that run outside a transaction. The caller must Release it;
// the next acquirer overwrites app.current_tenant before use, so the value
// never leaks across tenants.
func (s *Store) tenantConn(ctx context.Context) (*pgxpool.Conn, error) {
	c, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := c.Exec(ctx, setTenantSessionSQL, tenant.From(ctx)); err != nil {
		c.Release()
		return nil, err
	}
	return c, nil
}

// querier is satisfied by both *pgxpool.Pool and *pgxpool.Conn, letting the
// search query builder run against a tenant-scoped connection.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ─── Batched write buffer ──────────────────────────────────────────────────────
//
// A whole transaction's index and history writes are accumulated in a
// bundleWriter and flushed as a handful of multi-row INSERT statements (plus one
// batched re-index DELETE per sp_* table) instead of one INSERT per row. A single
// bundleWriter is shared across every entry of a transaction Bundle; the
// single-resource CRUD paths use one with a batch of one, so there is exactly
// one write implementation. (Multi-row INSERT rather than binary COPY because the
// tables run under FORCE ROW LEVEL SECURITY, which PostgreSQL forbids COPY FROM
// against for the ordinary application role — see index.InsertBatched.)

// resourcesCols / historyCols are the INSERT column lists for the batched
// resources and resource_history writes. Both lead with tenant_id, supplied
// explicitly and validated by the RLS WITH CHECK policy. Columns omitted here
// take their table defaults, exactly as the former per-row INSERTs relied on:
// resources.search_text stays NULL and resource_history.id is the BIGSERIAL key.
var (
	resourcesCols = []string{"tenant_id", "fhir_id", "resource_type", "version_id", "last_updated", "is_deleted", "resource_json"}
	historyCols   = []string{"tenant_id", "fhir_id", "resource_type", "version_id", "operation", "resource_json", "recorded_at"}
)

// bundleWriter buffers the batched writes for one transaction: the index rows
// (index.RowSet), any deferred resources creates, and every resource_history row.
//
// resources rows are buffered for the batched INSERT only when nothing later in
// the same transaction reads them back; version bumps, instance reads, and
// updates run as immediate statements against the open transaction, so the buffer
// never hides a row a later entry must observe.
type bundleWriter struct {
	rs        *index.RowSet
	tenant    string
	resources [][]any // deferred resources creates
	history   [][]any // every create/update/patch/delete history row

	// Referential-integrity bookkeeping (populated only when the matching
	// RefIntegrity flag is on; verified post-flush by Store.verifyIntegrity).
	refs    []pendingRef // local literal references carried by written resources
	deletes [][2]string  // (resourceType, id) soft-deleted in this transaction

	maxRowsPerStmt   int
	maxRowsPerBundle int
}

func (s *Store) newBundleWriter(ctx context.Context) *bundleWriter {
	t := tenant.From(ctx)
	wt := s.writeTuning
	return &bundleWriter{
		rs:               index.NewRowSet(t, wt.MaxRowsPerBundle),
		tenant:           t,
		maxRowsPerStmt:   wt.MaxRowsPerStatement,
		maxRowsPerBundle: wt.MaxRowsPerBundle,
	}
}

// totalRows is the total number of rows this transaction would write across the
// index, resources, and history tables.
func (w *bundleWriter) totalRows() int {
	return w.rs.Count() + len(w.resources) + len(w.history)
}

// flush writes the buffer to tx in FK-safe order: parent resources first (sp_*
// rows carry a FK to resources), then the index rows (batched re-index DELETEs +
// INSERTs), then resource_history. Called once, after all entries are processed
// and before COMMIT.
//
// Before writing anything it enforces the per-transaction row cap: if extraction
// stopped at the limit, or the buffered total exceeds MaxRowsPerBundle, it aborts
// with a WriteLimitError (mapped to HTTP 413) so a pathological bundle fails
// cleanly instead of driving the database out of memory. Nothing is sent to the
// database in that case; the caller's deferred Rollback unwinds any immediate
// writes (version bumps, non-deferred resource inserts) the entries already made.
func (w *bundleWriter) flush(ctx context.Context, tx pgx.Tx) error {
	if w.maxRowsPerBundle > 0 && (w.rs.LimitHit || w.totalRows() > w.maxRowsPerBundle) {
		// Log the per-table breakdown so an operator can tell a genuinely large
		// bundle (counts spread proportionally) from an extraction explosion (one
		// table, typically sp_composite_token_quantity, dominating). resources is
		// the number of write ops in this transaction, so index-rows / resources
		// is the rows-per-resource ratio — a normal resource is ~10–40; hundreds+
		// signals over-extraction.
		tc := w.rs.TableCounts()
		slog.Warn("write exceeded per-transaction row limit",
			"limit", w.maxRowsPerBundle,
			"indexRows", w.rs.Count(),
			"resources", len(w.history),
			"sp_composite_token_quantity", tc["sp_composite_token_quantity"],
			"sp_token", tc["sp_token"],
			"sp_string", tc["sp_string"],
			"sp_reference", tc["sp_reference"],
			"sp_quantity", tc["sp_quantity"],
			"sp_date", tc["sp_date"],
			"sp_number", tc["sp_number"],
			"sp_uri", tc["sp_uri"],
		)
		return WriteLimitError{Rows: w.totalRows(), Limit: w.maxRowsPerBundle}
	}
	// Queue everything into one pipelined batch so the whole flush costs a
	// single round trip instead of one per statement. Queue order preserves the
	// FK ordering the sequential writes relied on: parent resources rows first,
	// then the index DELETEs + INSERTs, then resource_history.
	qb := &index.Batch{}
	index.QueueInsertBatched(qb, "resources", resourcesCols, w.resources, w.maxRowsPerStmt)
	w.rs.QueueFlush(qb, w.maxRowsPerStmt)
	index.QueueInsertBatched(qb, "resource_history", historyCols, w.history, w.maxRowsPerStmt)
	return qb.Send(ctx, tx)
}

// flushAndVerify flushes the buffered writes and then runs the enabled
// referential-integrity checks against the transaction's final state. Every
// write path (single CRUD and Bundle processing) funnels through this so no
// path can skip enforcement.
func (s *Store) flushAndVerify(ctx context.Context, tx pgx.Tx, w *bundleWriter) error {
	if err := w.flush(ctx, tx); err != nil {
		return err
	}
	return s.verifyIntegrity(ctx, tx, w)
}

// ─── Create ───────────────────────────────────────────────────────────────────

func (s *Store) Create(ctx context.Context, resourceType string, body map[string]any) (map[string]any, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if err := setTenantTx(ctx, tx); err != nil {
		return nil, err
	}

	w := s.newBundleWriter(ctx)
	// A single create writes its resources row immediately (deferResource=false),
	// keeping duplicate-id and other errors surfacing exactly as before; its index
	// and history rows still flush through the shared batched writer.
	result, err := s.createInTx(ctx, tx, resourceType, body, w, false)
	if err != nil {
		return nil, err
	}
	if err := s.flushAndVerify(ctx, tx, w); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	id, _ := result["id"].(string)
	// Per-resource success is Debug, not Info: at import throughput (thousands of
	// writes/sec) an Info line per write serializes every request on the log
	// handler's write mutex and adds a marshal + syscall to the hot path. Bundle
	// processing still logs one Info summary per transaction (see ExecuteBundle).
	slog.Debug("created resource", "type", resourceType, "id", id)
	return result, nil
}

// createInTx performs a create within an existing transaction. It is the shared
// implementation behind the public Create and behind transaction/batch Bundle
// processing, where many writes must commit or roll back together.
//
// The resources row is buffered for a batched COPY when deferResource is true
// (safe only when nothing later in the transaction reads it back) or inserted
// immediately otherwise. Either way it lands before the index COPY, satisfying
// the sp_* foreign key. Index and history rows always join the shared batched
// buffer, which the caller flushes once per transaction.
func (s *Store) createInTx(ctx context.Context, tx pgx.Tx, resourceType string, body map[string]any, w *bundleWriter, deferResource bool) (map[string]any, error) {
	resourceID, _ := body["id"].(string)
	if resourceID == "" {
		resourceID = uuid.NewString()
	}
	body["id"] = resourceID
	body["resourceType"] = resourceType

	now := time.Now().UTC()
	body = setMeta(body, 1, now)

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal resource: %w", err)
	}

	slog.Debug("creating resource", "type", resourceType, "id", resourceID)
	if deferResource {
		w.resources = append(w.resources, []any{w.tenant, resourceID, resourceType, 1, now, false, raw})
	} else if _, err := tx.Exec(ctx,
		`INSERT INTO resources (fhir_id, resource_type, version_id, last_updated, is_deleted, resource_json)
		 VALUES ($1, $2, 1, $3, FALSE, $4)`,
		resourceID, resourceType, now, raw,
	); err != nil {
		return nil, fmt.Errorf("insert resource: %w", err)
	}

	s.extractor.Extract(w.rs, resourceType, resourceID, body, now)
	if s.refIntegrity.OnWrite {
		w.refs = append(w.refs, collectLocalRefs(resourceType, resourceID, body)...)
	}
	w.history = append(w.history, []any{w.tenant, resourceID, resourceType, 1, "POST", raw, now})

	return body, nil
}

// ─── Read ─────────────────────────────────────────────────────────────────────

func (s *Store) Read(ctx context.Context, resourceType, resourceID string) (map[string]any, error) {
	var raw []byte
	var versionID int
	var lastUpdated time.Time
	var isDeleted bool

	c, err := s.tenantConn(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Release()

	err = c.QueryRow(ctx, `
		SELECT resource_json, version_id, last_updated, is_deleted
		FROM resources
		WHERE fhir_id = $1 AND resource_type = $2 AND tenant_id = current_setting('app.current_tenant', true)`,
		resourceID, resourceType,
	).Scan(&raw, &versionID, &lastUpdated, &isDeleted)
	if err != nil {
		if isNoRows(err) {
			return nil, NotFoundError{resourceType, resourceID}
		}
		return nil, err
	}
	if isDeleted {
		return nil, GoneError{resourceType, resourceID}
	}

	return unmarshalWithMeta(raw, versionID, lastUpdated)
}

// ─── Update (PUT) ─────────────────────────────────────────────────────────────

// Update replaces a resource. ifMatchVersion = -1 means no version check;
// any value >= 0 is compared to the current version_id and a ConflictError
// (412) is returned if they differ.
func (s *Store) Update(ctx context.Context, resourceType, resourceID string, body map[string]any, ifMatchVersion int) (map[string]any, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if err := setTenantTx(ctx, tx); err != nil {
		return nil, err
	}

	w := s.newBundleWriter(ctx)
	result, err := s.updateInTx(ctx, tx, resourceType, resourceID, body, ifMatchVersion, w)
	if err != nil {
		return nil, err
	}
	if err := s.flushAndVerify(ctx, tx, w); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	slog.Debug("updated resource", "type", resourceType, "id", resourceID, "version", metaVersionID(result))
	return result, nil
}

// UpdateOrCreate replaces a resource, creating it at the given id when no
// resource exists there (FHIR "update as create"). The created return reports
// which of the two happened, so the handler can answer 201 instead of 200.
// A version precondition (ifMatchVersion >= 0) never creates: If-Match asserts
// the client has seen a specific version, so a missing target stays a
// NotFoundError exactly as in Update.
func (s *Store) UpdateOrCreate(ctx context.Context, resourceType, resourceID string, body map[string]any, ifMatchVersion int) (map[string]any, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	if err := setTenantTx(ctx, tx); err != nil {
		return nil, false, err
	}

	w := s.newBundleWriter(ctx)
	created := false
	result, err := s.updateInTx(ctx, tx, resourceType, resourceID, body, ifMatchVersion, w)
	if _, missing := err.(NotFoundError); missing && ifMatchVersion < 0 {
		created = true
		result, err = s.createInTx(ctx, tx, resourceType, body, w, false)
	}
	if err != nil {
		return nil, false, err
	}
	if err := s.flushAndVerify(ctx, tx, w); err != nil {
		return nil, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}

	slog.Debug("upserted resource", "type", resourceType, "id", resourceID, "created", created, "version", metaVersionID(result))
	return result, created, nil
}

// updateInTx performs an update within an existing transaction. Shared by the
// public Update and by transaction/batch Bundle processing.
func (s *Store) updateInTx(ctx context.Context, tx pgx.Tx, resourceType, resourceID string, body map[string]any, ifMatchVersion int, w *bundleWriter) (map[string]any, error) {
	body["id"] = resourceID
	body["resourceType"] = resourceType

	oldRaw, currentVersion, wasDeleted, err := lockForUpdate(ctx, tx, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	if ifMatchVersion >= 0 && currentVersion != ifMatchVersion {
		return nil, ConflictError{fmt.Sprintf("version conflict: current=%d, if-match=%d", currentVersion, ifMatchVersion)}
	}
	newVersion := currentVersion + 1
	lastUpdated := time.Now().UTC()

	body = setMeta(body, newVersion, lastUpdated)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	// Update the resources row immediately so a later entry (or a subsequent
	// version bump for the same id within this bundle) observes the new version.
	// The stale index rows are cleared and the fresh ones inserted by the batched
	// flush; mergeReindex diffs the stored version's rows against the incoming
	// version's so untouched sp_* tables cost nothing (see RowSet.MergeReindex).
	if _, err := tx.Exec(ctx,
		`UPDATE resources SET version_id = $1, last_updated = $2, resource_json = $3, is_deleted = FALSE
		 WHERE fhir_id = $4 AND resource_type = $5 AND tenant_id = current_setting('app.current_tenant', true)`,
		newVersion, lastUpdated, raw, resourceID, resourceType,
	); err != nil {
		return nil, err
	}
	// A soft-deleted resource has no sp_* rows left, so its stored body must not
	// contribute "old" rows — every table the new version touches gets a full
	// (re)insert, exactly as a fresh create would.
	if wasDeleted {
		oldRaw = nil
	}
	s.mergeReindex(w, resourceType, resourceID, oldRaw, body, lastUpdated)
	w.history = append(w.history, []any{w.tenant, resourceID, resourceType, newVersion, "PUT", raw, lastUpdated})

	return body, nil
}

// mergeReindex buffers the re-index for one updated resource by diffing the
// stored version's extracted rows (oldRaw; nil for none) against the incoming
// body's. The old rows are extracted with the zero time: only tables without a
// denormalised last_updated column are compared row-for-row, so the timestamp
// never participates in the comparison, and mere table emptiness drives the
// rest. If the stored JSON cannot be parsed it falls back to the full
// clear-and-reinsert of every table.
func (s *Store) mergeReindex(w *bundleWriter, resourceType, resourceID string, oldRaw []byte, body map[string]any, lastUpdated time.Time) {
	if s.refIntegrity.OnWrite {
		w.refs = append(w.refs, collectLocalRefs(resourceType, resourceID, body)...)
	}
	// Unbounded (cap 0): the stored version already passed the row cap when it
	// was written, and a LimitHit from re-extracting it must not abort this
	// transaction — only the incoming version's rows count against the cap.
	oldRS := index.NewRowSet(w.tenant, 0)
	if len(oldRaw) > 0 {
		var oldBody map[string]any
		if err := json.Unmarshal(oldRaw, &oldBody); err != nil {
			w.rs.AddDelete(resourceType, resourceID)
			s.extractor.Extract(w.rs, resourceType, resourceID, body, lastUpdated)
			return
		}
		s.extractor.Extract(oldRS, resourceType, resourceID, oldBody, time.Time{})
	}
	newRS := index.NewRowSet(w.tenant, w.maxRowsPerBundle)
	s.extractor.Extract(newRS, resourceType, resourceID, body, lastUpdated)
	w.rs.MergeReindex(oldRS, newRS, resourceType, resourceID)
}

// ─── Patch (JSON Merge Patch) ─────────────────────────────────────────────────

// Patch applies a JSON Merge Patch (RFC 7396) atomically. The read and write
// happen inside a single transaction with a FOR UPDATE lock so concurrent
// PATCHes to the same resource cannot produce a lost update.
func (s *Store) Patch(ctx context.Context, resourceType, resourceID string, patch map[string]any) (map[string]any, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if err := setTenantTx(ctx, tx); err != nil {
		return nil, err
	}

	w := s.newBundleWriter(ctx)
	merged, newVersion, err := s.patchInTx(ctx, tx, resourceType, resourceID, patch, w)
	if err != nil {
		return nil, err
	}
	if err := s.flushAndVerify(ctx, tx, w); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	slog.Debug("patched resource", "type", resourceType, "id", resourceID, "version", newVersion)
	return merged, nil
}

// patchInTx applies a JSON Merge Patch within an existing transaction, taking a
// FOR UPDATE lock on the row. Shared by the public Patch and by Bundle processing.
func (s *Store) patchInTx(ctx context.Context, tx pgx.Tx, resourceType, resourceID string, patch map[string]any, w *bundleWriter) (map[string]any, int, error) {
	var raw []byte
	var versionID int
	var lastUpdated time.Time
	var isDeleted bool
	if err := tx.QueryRow(ctx, `
		SELECT resource_json, version_id, last_updated, is_deleted
		FROM resources WHERE fhir_id = $1 AND resource_type = $2 AND tenant_id = current_setting('app.current_tenant', true) FOR UPDATE`,
		resourceID, resourceType,
	).Scan(&raw, &versionID, &lastUpdated, &isDeleted); err != nil {
		if isNoRows(err) {
			return nil, 0, NotFoundError{resourceType, resourceID}
		}
		return nil, 0, err
	}
	if isDeleted {
		return nil, 0, GoneError{resourceType, resourceID}
	}

	existing, err := unmarshalWithMeta(raw, versionID, lastUpdated)
	if err != nil {
		return nil, 0, err
	}
	// Extract the stored version's index rows before mergePatch/setMeta run:
	// both alias nested maps of existing, and the diff below must see the
	// pre-patch rows.
	oldRS := index.NewRowSet(w.tenant, 0)
	s.extractor.Extract(oldRS, resourceType, resourceID, existing, time.Time{})
	merged := mergePatch(existing, patch)
	merged["id"] = resourceID
	merged["resourceType"] = resourceType

	newVersion := versionID + 1
	now := time.Now().UTC()
	merged = setMeta(merged, newVersion, now)
	mergedRaw, err := json.Marshal(merged)
	if err != nil {
		return nil, 0, err
	}

	// Update immediately, then buffer the diffed re-index and the history row
	// for the batched flush — the same shape as updateInTx.
	if _, err := tx.Exec(ctx,
		`UPDATE resources SET version_id = $1, last_updated = $2, resource_json = $3, is_deleted = FALSE
		 WHERE fhir_id = $4 AND resource_type = $5 AND tenant_id = current_setting('app.current_tenant', true)`,
		newVersion, now, mergedRaw, resourceID, resourceType,
	); err != nil {
		return nil, 0, err
	}
	newRS := index.NewRowSet(w.tenant, w.maxRowsPerBundle)
	s.extractor.Extract(newRS, resourceType, resourceID, merged, now)
	w.rs.MergeReindex(oldRS, newRS, resourceType, resourceID)
	w.history = append(w.history, []any{w.tenant, resourceID, resourceType, newVersion, "PATCH", mergedRaw, now})

	return merged, newVersion, nil
}

// mergePatch applies a JSON Merge Patch (RFC 7396).
func mergePatch(target, patch map[string]any) map[string]any {
	result := make(map[string]any, len(target))
	for k, v := range target {
		result[k] = v
	}
	for k, v := range patch {
		if v == nil {
			delete(result, k)
		} else if subPatch, ok := v.(map[string]any); ok {
			if subTarget, ok := result[k].(map[string]any); ok {
				result[k] = mergePatch(subTarget, subPatch)
			} else {
				result[k] = v
			}
		} else {
			result[k] = v
		}
	}
	return result
}

// ─── Delete ───────────────────────────────────────────────────────────────────

func (s *Store) Delete(ctx context.Context, resourceType, resourceID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := setTenantTx(ctx, tx); err != nil {
		return err
	}

	w := s.newBundleWriter(ctx)
	if err := s.deleteInTx(ctx, tx, resourceType, resourceID, w); err != nil {
		return err
	}
	if err := s.flushAndVerify(ctx, tx, w); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	slog.Debug("deleted resource", "type", resourceType, "id", resourceID)
	return nil
}

// deleteInTx soft-deletes a resource within an existing transaction. Shared by
// the public Delete and by Bundle processing. Idempotent: deleting an already
// deleted or non-existent resource returns nil.
func (s *Store) deleteInTx(ctx context.Context, tx pgx.Tx, resourceType, resourceID string, w *bundleWriter) error {
	// Lock the row first to prevent a concurrent Update from bumping the version
	// between our read and our soft-delete write, which would produce a UNIQUE
	// constraint violation on resource_history(fhir_id, resource_type, version_id).
	var raw []byte
	var versionID int
	var lastUpdated time.Time
	var isDeleted bool
	if err := tx.QueryRow(ctx, `
		SELECT resource_json, version_id, last_updated, is_deleted
		FROM resources WHERE fhir_id = $1 AND resource_type = $2 AND tenant_id = current_setting('app.current_tenant', true) FOR UPDATE`,
		resourceID, resourceType,
	).Scan(&raw, &versionID, &lastUpdated, &isDeleted); err != nil {
		if isNoRows(err) {
			return NotFoundError{resourceType, resourceID}
		}
		return err
	}
	if isDeleted {
		return nil // idempotent: already deleted
	}
	if s.refIntegrity.OnDelete {
		w.deletes = append(w.deletes, [2]string{resourceType, resourceID})
	}

	// DELETE is a new version in FHIR — bump to avoid UNIQUE(fhir_id, resource_type, version_id) conflict.
	deleteVersion := versionID + 1
	now := time.Now().UTC()

	// Buffer the delete-history row and the re-index DELETEs, then soft-delete
	// the resources row immediately. The batched flush clears this resource's
	// sp_* rows and writes the history row; there are no fresh index rows for a
	// delete, so the diff (empty new side) scopes the DELETEs to just the tables
	// the stored version actually has rows in. An unparseable stored body falls
	// back to clearing every table.
	w.history = append(w.history, []any{w.tenant, resourceID, resourceType, deleteVersion, "DELETE", raw, now})
	var oldBody map[string]any
	if err := json.Unmarshal(raw, &oldBody); err != nil {
		w.rs.AddDelete(resourceType, resourceID)
	} else {
		oldRS := index.NewRowSet(w.tenant, 0)
		s.extractor.Extract(oldRS, resourceType, resourceID, oldBody, time.Time{})
		w.rs.MergeReindex(oldRS, index.NewRowSet(w.tenant, 0), resourceType, resourceID)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE resources SET is_deleted = TRUE, version_id = $1, last_updated = $2
		 WHERE fhir_id = $3 AND resource_type = $4 AND tenant_id = current_setting('app.current_tenant', true)`,
		deleteVersion, now, resourceID, resourceType,
	); err != nil {
		return err
	}
	return nil
}

// ─── History ──────────────────────────────────────────────────────────────────

type HistoryEntry struct {
	VersionID int
	Operation string
	Resource  map[string]any
}

func (s *Store) GetHistory(ctx context.Context, resourceType, resourceID string) ([]HistoryEntry, error) {
	c, err := s.tenantConn(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Release()

	rows, err := c.Query(ctx, `
		SELECT version_id, operation, resource_json, recorded_at
		FROM resource_history
		WHERE resource_type = $1 AND fhir_id = $2
		ORDER BY version_id DESC`,
		resourceType, resourceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHistoryRows(rows)
}

// HistoryParams controls pagination and filtering for type-level history.
type HistoryParams struct {
	ResourceType string
	Since        time.Time // zero ⇒ no lower bound
	Page         int       // 1-based; 0 treated as 1
	PageSize     int       // 0 treated as 20
}

// HistoryResult is the paged result of a type-level history query.
type HistoryResult struct {
	Total   int
	Entries []HistoryEntry
}

// GetTypeHistory returns paged history for a single resource type. When
// p.ResourceType is empty it returns cross-type (system-level) history.
func (s *Store) GetTypeHistory(ctx context.Context, p HistoryParams) (HistoryResult, error) {
	if p.PageSize <= 0 {
		p.PageSize = 20
	}
	if p.Page <= 0 {
		p.Page = 1
	}
	offset := (p.Page - 1) * p.PageSize

	var (
		total  int
		countQ string
		fetchQ string
		args   []any
	)

	system := p.ResourceType == ""
	switch {
	case system && p.Since.IsZero():
		countQ = `SELECT COUNT(*) FROM resource_history`
		fetchQ = `SELECT version_id, operation, resource_json, recorded_at
		           FROM resource_history ORDER BY recorded_at DESC LIMIT $1 OFFSET $2`
		args = []any{}
	case system && !p.Since.IsZero():
		countQ = `SELECT COUNT(*) FROM resource_history WHERE recorded_at > $1`
		fetchQ = `SELECT version_id, operation, resource_json, recorded_at
		           FROM resource_history WHERE recorded_at > $1
		           ORDER BY recorded_at DESC LIMIT $2 OFFSET $3`
		args = []any{p.Since}
	case !system && p.Since.IsZero():
		countQ = `SELECT COUNT(*) FROM resource_history WHERE resource_type = $1`
		fetchQ = `SELECT version_id, operation, resource_json, recorded_at
		           FROM resource_history WHERE resource_type = $1
		           ORDER BY recorded_at DESC LIMIT $2 OFFSET $3`
		args = []any{p.ResourceType}
	default:
		countQ = `SELECT COUNT(*) FROM resource_history WHERE resource_type = $1 AND recorded_at > $2`
		fetchQ = `SELECT version_id, operation, resource_json, recorded_at
		           FROM resource_history WHERE resource_type = $1 AND recorded_at > $2
		           ORDER BY recorded_at DESC LIMIT $3 OFFSET $4`
		args = []any{p.ResourceType, p.Since}
	}

	c, err := s.tenantConn(ctx)
	if err != nil {
		return HistoryResult{}, err
	}
	defer c.Release()

	if err := c.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return HistoryResult{}, err
	}

	// append makes a copy so countQ args are not mutated.
	fetchArgs := make([]any, len(args), len(args)+2)
	copy(fetchArgs, args)
	fetchArgs = append(fetchArgs, p.PageSize, offset)
	rows, err := c.Query(ctx, fetchQ, fetchArgs...)
	if err != nil {
		return HistoryResult{}, err
	}
	defer rows.Close()

	entries, err := scanHistoryRows(rows)
	if err != nil {
		return HistoryResult{}, err
	}
	return HistoryResult{Total: total, Entries: entries}, nil
}

func (s *Store) GetVersion(ctx context.Context, resourceType, resourceID string, versionID int) (map[string]any, error) {
	var raw []byte
	var recordedAt time.Time
	c, err := s.tenantConn(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Release()
	err = c.QueryRow(ctx, `
		SELECT resource_json, recorded_at FROM resource_history
		WHERE resource_type = $1 AND fhir_id = $2 AND version_id = $3`,
		resourceType, resourceID, versionID,
	).Scan(&raw, &recordedAt)
	if err != nil {
		if isNoRows(err) {
			return nil, NotFoundError{resourceType, fmt.Sprintf("%s/_history/%d", resourceID, versionID)}
		}
		return nil, err
	}
	return unmarshalWithMeta(raw, versionID, recordedAt)
}

// ─── Shared helpers ───────────────────────────────────────────────────────────

// metaVersionID returns the meta.versionId string of a resource, or "" if absent.
func metaVersionID(body map[string]any) string {
	if meta, ok := body["meta"].(map[string]any); ok {
		if v, ok := meta["versionId"].(string); ok {
			return v
		}
	}
	return ""
}

// readInTx reads a live (non-deleted) resource within an existing transaction,
// so a GET inside a transaction Bundle observes writes made earlier in the same
// Bundle. Mirrors the public Read but uses the supplied tx instead of the pool.
func (s *Store) readInTx(ctx context.Context, tx pgx.Tx, resourceType, resourceID string) (map[string]any, error) {
	var raw []byte
	var versionID int
	var lastUpdated time.Time
	var isDeleted bool
	if err := tx.QueryRow(ctx, `
		SELECT resource_json, version_id, last_updated, is_deleted
		FROM resources WHERE fhir_id = $1 AND resource_type = $2 AND tenant_id = current_setting('app.current_tenant', true)`,
		resourceID, resourceType,
	).Scan(&raw, &versionID, &lastUpdated, &isDeleted); err != nil {
		if isNoRows(err) {
			return nil, NotFoundError{resourceType, resourceID}
		}
		return nil, err
	}
	if isDeleted {
		return nil, GoneError{resourceType, resourceID}
	}
	return unmarshalWithMeta(raw, versionID, lastUpdated)
}

// lockForUpdate locks the resource row and returns its stored body, version and
// deletion flag. Fetching resource_json here costs nothing extra — the row is
// already read for the lock — and it feeds the diff-based re-index, which needs
// the stored version's extracted rows to decide which sp_* tables to touch.
func lockForUpdate(ctx context.Context, tx pgx.Tx, resourceType, resourceID string) (raw []byte, currentVersion int, isDeleted bool, err error) {
	if err = tx.QueryRow(ctx, `
		SELECT resource_json, version_id, is_deleted FROM resources WHERE fhir_id = $1 AND resource_type = $2 AND tenant_id = current_setting('app.current_tenant', true) FOR UPDATE`,
		resourceID, resourceType,
	).Scan(&raw, &currentVersion, &isDeleted); err != nil {
		if isNoRows(err) {
			err = NotFoundError{resourceType, resourceID}
		}
		return
	}
	return
}

func setMeta(body map[string]any, versionID int, lastUpdated time.Time) map[string]any {
	meta, _ := body["meta"].(map[string]any)
	if meta == nil {
		meta = make(map[string]any)
	}
	meta["versionId"] = fmt.Sprintf("%d", versionID)
	meta["lastUpdated"] = lastUpdated.Format(time.RFC3339)
	body["meta"] = meta
	return body
}

func unmarshalWithMeta(raw []byte, versionID int, lastUpdated time.Time) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return setMeta(m, versionID, lastUpdated), nil
}

func scanHistoryRows(rows pgx.Rows) ([]HistoryEntry, error) {
	var entries []HistoryEntry
	for rows.Next() {
		var versionID int
		var op string
		var raw []byte
		var recordedAt time.Time
		if err := rows.Scan(&versionID, &op, &raw, &recordedAt); err != nil {
			return nil, err
		}
		res, err := unmarshalWithMeta(raw, versionID, recordedAt)
		if err != nil {
			return nil, err
		}
		entries = append(entries, HistoryEntry{VersionID: versionID, Operation: op, Resource: res})
	}
	return entries, rows.Err()
}

func isNoRows(err error) bool {
	return err == pgx.ErrNoRows || strings.Contains(err.Error(), "no rows")
}

// ─── SearchParameter sync ─────────────────────────────────────────────────────

// SyncSearchParameter persists a custom SearchParameter into search_param_definitions
// and updates the in-memory registry. Called after Create/Update of SearchParameter.
func (s *Store) SyncSearchParameter(ctx context.Context, body map[string]any) error {
	code, _ := body["code"].(string)
	paramType, _ := body["type"].(string)
	expression, _ := body["expression"].(string)
	baseArr, _ := body["base"].([]any)
	if code == "" || len(baseArr) == 0 {
		return nil
	}

	// Build the new set of bases to keep.
	newBases := make(map[string]struct{}, len(baseArr))
	for _, b := range baseArr {
		if rt, ok := b.(string); ok && rt != "" {
			newBases[rt] = struct{}{}
		}
	}
	if len(newBases) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Read previously persisted resource_types for this code so we can drop
	// any that have been removed from base in this update. Without this,
	// narrowing a SearchParameter (e.g. [Patient, Observation] → [Patient])
	// would leave the Observation definition behind.
	rows, err := tx.Query(ctx,
		`SELECT resource_type FROM search_param_definitions
		 WHERE param_name = $1 AND is_custom = TRUE FOR UPDATE`,
		code,
	)
	if err != nil {
		return fmt.Errorf("read existing search param bases for %s: %w", code, err)
	}
	var oldBases []string
	for rows.Next() {
		var rt string
		if err := rows.Scan(&rt); err != nil {
			rows.Close()
			return err
		}
		oldBases = append(oldBases, rt)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Collect target types if this is a reference param.
	var targets []string
	if targetArr, ok := body["target"].([]any); ok {
		for _, t := range targetArr {
			if s, ok := t.(string); ok && s != "" {
				targets = append(targets, s)
			}
		}
	}
	targetTypes := strings.Join(targets, "|")

	var defs []searchparam.Definition
	for rt := range newBases {
		if _, err := tx.Exec(ctx, `
			INSERT INTO search_param_definitions (resource_type, param_name, param_type, fhirpath_expr, is_custom, target_types)
			VALUES ($1, $2, $3, $4, TRUE, $5)
			ON CONFLICT (resource_type, param_name)
			DO UPDATE SET param_type = EXCLUDED.param_type,
			              fhirpath_expr = EXCLUDED.fhirpath_expr,
			              target_types = EXCLUDED.target_types
			WHERE search_param_definitions.is_custom = TRUE`,
			rt, code, paramType, expression, targetTypes,
		); err != nil {
			return fmt.Errorf("upsert search param %s.%s: %w", rt, code, err)
		}
		defs = append(defs, searchparam.Definition{
			ResourceType: rt,
			ParamName:    code,
			ParamType:    paramType,
			FHIRPath:     expression,
			IsCustom:     true,
			Targets:      targets,
		})
	}

	// Remove definitions for resource_types that were previously persisted but
	// are no longer in base.
	var dropped []string
	for _, rt := range oldBases {
		if _, keep := newBases[rt]; !keep {
			dropped = append(dropped, rt)
		}
	}
	if len(dropped) > 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM search_param_definitions
			 WHERE param_name = $1 AND is_custom = TRUE AND resource_type = ANY($2)`,
			code, dropped,
		); err != nil {
			return fmt.Errorf("delete stale search param defs for %s: %w", code, err)
		}
	}

	// Commit DB changes before updating the in-memory registry so that a
	// failure or rollback never leaves the registry ahead of the database.
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, def := range defs {
		s.registry.Upsert(def)
	}
	for _, rt := range dropped {
		s.registry.Remove(rt, code)
	}
	return nil
}

// DeleteSearchParameter removes a custom SearchParameter by resource ID.
func (s *Store) DeleteSearchParameter(ctx context.Context, resourceID string) error {
	var raw []byte
	c, err := s.tenantConn(ctx)
	if err != nil {
		return err
	}
	defer c.Release()
	err = c.QueryRow(ctx,
		`SELECT resource_json FROM resources WHERE fhir_id = $1 AND resource_type = 'SearchParameter' AND tenant_id = current_setting('app.current_tenant', true)`,
		resourceID,
	).Scan(&raw)
	if err != nil {
		if isNoRows(err) {
			return nil // nothing to clean up
		}
		return err
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	code, _ := body["code"].(string)
	if code == "" {
		return nil
	}

	// Collect base resource types from the payload so we only delete custom
	// definitions whose base matches — a SearchParameter on Patient must not
	// remove a same-code custom definition on Observation.
	var bases []string
	if baseArr, ok := body["base"].([]any); ok {
		for _, b := range baseArr {
			if rt, ok := b.(string); ok && rt != "" {
				bases = append(bases, rt)
			}
		}
	}
	if len(bases) == 0 {
		return nil
	}

	if _, err := s.pool.Exec(ctx,
		`DELETE FROM search_param_definitions WHERE param_name = $1 AND is_custom = TRUE AND resource_type = ANY($2)`,
		code, bases,
	); err != nil {
		return err
	}

	// Update the in-memory registry only after the DB delete commits so the
	// two stores never diverge in the direction of "registry missing, DB has it."
	for _, rt := range bases {
		s.registry.Remove(rt, code)
	}
	slog.Info("removed custom search parameter", "code", code)
	return nil
}

// NotFoundError is returned when a resource does not exist.
type NotFoundError struct {
	ResourceType string
	ResourceID   string
}

func (e NotFoundError) Error() string {
	return fmt.Sprintf("%s/%s not found", e.ResourceType, e.ResourceID)
}

// GoneError is returned when a resource existed but has been deleted.
type GoneError struct {
	ResourceType string
	ResourceID   string
}

func (e GoneError) Error() string {
	return fmt.Sprintf("%s/%s has been deleted", e.ResourceType, e.ResourceID)
}

// ConflictError is returned when an If-Match version check fails.
type ConflictError struct {
	Message string
}

func (e ConflictError) Error() string {
	return e.Message
}

// WriteLimitError is returned when a single write transaction would buffer more
// index rows than the configured per-bundle cap (WriteTuning.MaxRowsPerBundle).
// It maps to HTTP 413 (Payload Too Large): the write is rejected and rolled back
// so one pathological bundle cannot exhaust database memory. Raise the cap
// (WRITE_MAX_ROWS_PER_BUNDLE) for trusted bulk-import environments.
type WriteLimitError struct {
	Rows  int // rows the transaction attempted (at least the cap; may undercount once extraction stops)
	Limit int // the configured MaxRowsPerBundle
}

func (e WriteLimitError) Error() string {
	return fmt.Sprintf("write exceeds the per-transaction index-row limit of %d (raise write.maxRowsPerBundle to allow larger bundles)", e.Limit)
}
