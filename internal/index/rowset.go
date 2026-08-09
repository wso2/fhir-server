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

package index

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// maxInsertParams is the hard ceiling on bind parameters in one multi-row INSERT,
// safely under PostgreSQL's 65535-parameter protocol limit. It clamps the
// caller-supplied rows-per-statement so a large (or misconfigured) value can
// never produce a statement the wire protocol rejects.
//
// The write path uses multi-row INSERT rather than binary COPY because the sp_*
// (and resources / resource_history) tables run under FORCE ROW LEVEL SECURITY,
// and PostgreSQL rejects COPY FROM on an RLS-enforced table for the ordinary
// (non-owner) application role a real deployment connects as
// ("COPY FROM not supported with row-level security"). Multi-row INSERT is the
// design's stated fallback: it still collapses the per-row round-trip storm into
// a handful of statements (one parse/plan per chunk of rows) while the RLS
// WITH CHECK policy validates every row.
const maxInsertParams = 60000

// defaultMaxRowsPerStmt is the built-in rows-per-statement chunk used by the
// standalone indexer and as the store's default (config key
// write.maxRowsPerStatement). Kept well under maxInsertParams for the widest
// (12-column) table so each statement's parse tree stays small.
const defaultMaxRowsPerStmt = 1000

// Column lists for every batched sp_* INSERT. Each MUST match the value order the
// append* helpers build, and each begins with tenant_id, supplied explicitly and
// validated by the RLS WITH CHECK policy. Columns omitted here (e.g. the BIGSERIAL
// id, sp_date.value_precision, sp_quantity.canonical_*) intentionally fall back
// to their table defaults, exactly as the previous per-row INSERTs did.
var (
	spStringCols    = []string{"tenant_id", "resource_id", "resource_type", "param_name", "value_exact", "value_lower"}
	spTokenCols     = []string{"tenant_id", "resource_id", "resource_type", "param_name", "system", "code", "display", "last_updated"}
	spDateCols      = []string{"tenant_id", "resource_id", "resource_type", "param_name", "value_low", "value_high", "last_updated"}
	spNumberCols    = []string{"tenant_id", "resource_id", "resource_type", "param_name", "value", "value_low", "value_high", "last_updated"}
	spQuantityCols  = []string{"tenant_id", "resource_id", "resource_type", "param_name", "value", "value_low", "value_high", "system", "code", "last_updated"}
	spURICols       = []string{"tenant_id", "resource_id", "resource_type", "param_name", "value"}
	spReferenceCols = []string{"tenant_id", "resource_id", "resource_type", "param_name", "target_type", "target_id", "identifier_system", "identifier_value", "last_updated"}
	spCompositeCols = []string{"tenant_id", "resource_id", "resource_type", "param_name", "system", "code", "value", "value_low", "value_high", "qty_system", "qty_code", "last_updated"}
)

// spDeleteTables is the set of sp_* tables re-indexed on update/patch/delete,
// identical to the tables the former per-resource QueueDelete cleared.
var spDeleteTables = []string{
	"sp_string", "sp_token", "sp_date", "sp_number",
	"sp_quantity", "sp_uri", "sp_reference", "sp_composite_token_quantity",
}

// noRecencyTables are the sp_* tables without a denormalised last_updated
// column. Their rows are a pure function of the resource body, so a re-index
// that would write value-identical rows can skip the table entirely. Every
// other table's rows embed last_updated (served back by the covering recency
// scans in store/search.go in place of resources.last_updated), so those rows
// must be rewritten on every update to stay fresh even when the indexed values
// did not change.
var noRecencyTables = map[string]bool{"sp_string": true, "sp_uri": true}

// refKey identifies a resource whose existing sp_* rows must be cleared before
// its freshly extracted rows are inserted (the re-index on update/patch/delete).
type refKey struct {
	resourceID   string
	resourceType string
}

// RowSet accumulates the sp_* index rows for one database transaction so the
// whole transaction's index writes flush as a handful of batched DELETE and
// multi-row INSERT statements instead of one INSERT/DELETE per row. A single
// bundle transaction shares one RowSet across all its entries; the
// single-resource CRUD paths use one with a batch of one.
//
// The row slices hold values in the column order declared above, tenant_id
// first. Callers append via the extractor's append* helpers.
type RowSet struct {
	tenant string

	// maxRows bounds the total sp_* rows one transaction may buffer (0 = unlimited).
	// Extraction stops appending once the count reaches it and sets LimitHit, so a
	// pathological resource cannot balloon the in-memory set (and, via the writer's
	// pre-flush check, cannot drive the database out of memory with a giant insert).
	maxRows int
	// LimitHit is set (sticky) when extraction was stopped by maxRows. The writer
	// reads it to abort the transaction with a bounded "payload too large" error.
	LimitHit bool

	spString    [][]any
	spToken     [][]any
	spDate      [][]any
	spNumber    [][]any
	spQuantity  [][]any
	spURI       [][]any
	spReference [][]any
	spComposite [][]any

	// deletes is the ordered, de-duplicated set of (table, resource) re-index
	// DELETEs to run before insert; deleteSeen guards the dedup. Registration is
	// per table so an update that provably left a table untouched (see
	// MergeReindex) issues no DELETE against it at all.
	deletes    map[string][]refKey
	deleteSeen map[string]map[refKey]struct{}
}

// NewRowSet returns an empty RowSet stamped with the transaction's tenant (written
// into every row's tenant_id column) and bounded to maxRows total sp_* rows
// (0 = unlimited).
func NewRowSet(tenant string, maxRows int) *RowSet {
	return &RowSet{
		tenant:     tenant,
		maxRows:    maxRows,
		deletes:    map[string][]refKey{},
		deleteSeen: map[string]map[refKey]struct{}{},
	}
}

// spSlice maps a table name to the RowSet slice holding its buffered rows, in
// spDeleteTables order. Kept as a method (not a stored field) so the returned
// pointers always address the current slices.
func (rs *RowSet) spSlice(table string) *[][]any {
	switch table {
	case "sp_string":
		return &rs.spString
	case "sp_token":
		return &rs.spToken
	case "sp_date":
		return &rs.spDate
	case "sp_number":
		return &rs.spNumber
	case "sp_quantity":
		return &rs.spQuantity
	case "sp_uri":
		return &rs.spURI
	case "sp_reference":
		return &rs.spReference
	case "sp_composite_token_quantity":
		return &rs.spComposite
	}
	return nil
}

// Count returns the total number of sp_* rows currently buffered across all tables.
func (rs *RowSet) Count() int {
	return len(rs.spString) + len(rs.spToken) + len(rs.spDate) + len(rs.spNumber) +
		len(rs.spQuantity) + len(rs.spURI) + len(rs.spReference) + len(rs.spComposite)
}

// atLimit reports whether the buffered row count has reached the configured cap.
// Extraction checks this at its loop boundaries to stop early and set LimitHit.
func (rs *RowSet) atLimit() bool {
	return rs.maxRows > 0 && rs.Count() >= rs.maxRows
}

// TableCounts returns the per-table buffered row counts. Used to diagnose which
// sp_* table dominates when a write is rejected for exceeding the row cap — a
// disproportionate sp_composite_token_quantity count points at a composite
// extraction explosion rather than a merely large bundle.
func (rs *RowSet) TableCounts() map[string]int {
	return map[string]int{
		"sp_string":                   len(rs.spString),
		"sp_token":                    len(rs.spToken),
		"sp_date":                     len(rs.spDate),
		"sp_number":                   len(rs.spNumber),
		"sp_quantity":                 len(rs.spQuantity),
		"sp_uri":                      len(rs.spURI),
		"sp_reference":                len(rs.spReference),
		"sp_composite_token_quantity": len(rs.spComposite),
	}
}

// AddDelete records that resource (resourceType, resourceID) must have its
// existing sp_* rows removed from every table at flush (full re-index), and
// drops any rows already accumulated for it earlier in this transaction. The
// purge keeps a create-then-update of the same resource within one bundle
// byte-identical to the old per-op delete-then-reinsert: only the final
// version's rows survive.
func (rs *RowSet) AddDelete(resourceType, resourceID string) {
	k := refKey{resourceID: resourceID, resourceType: resourceType}
	for _, tbl := range spDeleteTables {
		rs.addTableDelete(tbl, k)
	}
}

// addTableDelete registers the re-index DELETE for one (table, resource) pair
// and purges that table's buffered rows for the resource, so only the final
// version's rows survive when one transaction touches a resource repeatedly.
func (rs *RowSet) addTableDelete(table string, k refKey) {
	seen := rs.deleteSeen[table]
	if seen == nil {
		seen = map[refKey]struct{}{}
		rs.deleteSeen[table] = seen
	}
	if _, ok := seen[k]; !ok {
		seen[k] = struct{}{}
		rs.deletes[table] = append(rs.deletes[table], k)
	}
	rs.purgeTable(table, k)
}

// purgeTable removes any accumulated rows for k from one sp_* slice. Row layout
// is [tenant_id, resource_id, resource_type, ...], so indexes 1 and 2 identify
// the owning resource.
func (rs *RowSet) purgeTable(table string, k refKey) {
	sl := rs.spSlice(table)
	out := (*sl)[:0]
	for _, r := range *sl {
		if r[1] == k.resourceID && r[2] == k.resourceType {
			continue
		}
		out = append(out, r)
	}
	*sl = out
}

// MergeReindex merges the re-index of one resource into rs, given the rows
// extracted from the stored version (oldRS; empty when the resource was absent
// or soft-deleted) and from the incoming version (newRS; empty for a delete).
// Per table it decides between three outcomes:
//
//   - both versions have no rows → nothing at all (the former unconditional
//     8-table DELETE storm ran mostly against tables like this);
//   - the table carries no denormalised last_updated (noRecencyTables) and the
//     extracted rows are value-identical → leave the stored rows untouched;
//   - otherwise → register the table-scoped DELETE and buffer the new rows.
//
// Both temporary RowSets must share rs's tenant. The buffered-row cap and
// LimitHit stay in force: appends stop at rs.maxRows, and a LimitHit on either
// temporary set propagates so the writer still aborts oversized transactions.
func (rs *RowSet) MergeReindex(oldRS, newRS *RowSet, resourceType, resourceID string) {
	k := refKey{resourceID: resourceID, resourceType: resourceType}
	for _, tbl := range spDeleteTables {
		oldRows := *oldRS.spSlice(tbl)
		newRows := *newRS.spSlice(tbl)
		if len(oldRows) == 0 && len(newRows) == 0 {
			continue
		}
		if noRecencyTables[tbl] && rowsEqual(oldRows, newRows) {
			continue
		}
		rs.addTableDelete(tbl, k)
		dst := rs.spSlice(tbl)
		for _, r := range newRows {
			if rs.atLimit() {
				rs.LimitHit = true
				break
			}
			*dst = append(*dst, r)
		}
	}
	if oldRS.LimitHit || newRS.LimitHit {
		rs.LimitHit = true
	}
}

// rowsEqual reports whether two buffered row slices are identical position by
// position. Extraction is deterministic for a given body and registry, so an
// unchanged resource subtree yields the same rows in the same order; any
// mismatch (including reordering) just falls back to a full re-index of the
// table. Only called for noRecencyTables, whose values are strings/nil and
// therefore directly comparable.
func rowsEqual(a, b [][]any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

// Batch collects statements for one pipelined round trip, wrapping pgx.Batch
// with a parallel label per statement so a failure still names the table it
// targeted, exactly as the former one-Exec-per-statement writes did.
type Batch struct {
	b      pgx.Batch
	labels []string
}

// Queue adds one statement to the batch under a diagnostic label.
func (qb *Batch) Queue(label, sql string, args ...any) {
	qb.b.Queue(sql, args...)
	qb.labels = append(qb.labels, label)
}

// Len returns the number of queued statements.
func (qb *Batch) Len() int { return qb.b.Len() }

// Send runs every queued statement in one pipelined round trip on tx and
// surfaces the first failure with its statement's label. A no-op when empty.
// Statements execute in queue order, so callers preserve FK ordering (parent
// resources rows before sp_* rows) simply by queueing in that order.
func (qb *Batch) Send(ctx context.Context, tx pgx.Tx) error {
	if qb.b.Len() == 0 {
		return nil
	}
	br := tx.SendBatch(ctx, &qb.b)
	var execErr error
	for i := 0; i < qb.b.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			execErr = fmt.Errorf("%s: %w", qb.labels[i], err)
			break
		}
	}
	if cerr := br.Close(); execErr == nil && cerr != nil {
		execErr = cerr
	}
	return execErr
}

// QueueFlush queues the accumulated index changes: first the batched re-index
// DELETEs (one statement per touched sp_* table over that table's collected
// resource keys), then chunked multi-row INSERTs per sp_* table. DELETEs
// precede INSERTs so a re-indexed resource's stale rows are gone before its
// fresh rows land. The caller sends the batch inside the transaction, after
// all entries are processed and after the parent resources rows are queued
// (the sp_* FK), and before COMMIT.
func (rs *RowSet) QueueFlush(qb *Batch, maxRowsPerStmt int) {
	for _, tbl := range spDeleteTables {
		keys := rs.deletes[tbl]
		if len(keys) == 0 {
			continue
		}
		ids := make([]string, len(keys))
		types := make([]string, len(keys))
		for i, k := range keys {
			ids[i] = k.resourceID
			types[i] = k.resourceType
		}
		// One DELETE per table over all (resource_id, resource_type) pairs,
		// replacing the former per-resource DELETE storm. UNNEST pairs the two
		// parallel arrays positionally. The tenant predicate mirrors the original
		// per-row DELETE so exactly the same rows are removed.
		q := fmt.Sprintf(`DELETE FROM %s t
		                  USING (SELECT UNNEST($1::text[]) AS rid, UNNEST($2::text[]) AS rt) d
		                  WHERE t.tenant_id = current_setting('app.current_tenant', true)
		                    AND t.resource_id = d.rid AND t.resource_type = d.rt`, tbl)
		qb.Queue("batch re-index delete from "+tbl, q, ids, types)
	}

	tables := []struct {
		name string
		cols []string
		rows [][]any
	}{
		{"sp_string", spStringCols, rs.spString},
		{"sp_token", spTokenCols, rs.spToken},
		{"sp_date", spDateCols, rs.spDate},
		{"sp_number", spNumberCols, rs.spNumber},
		{"sp_quantity", spQuantityCols, rs.spQuantity},
		{"sp_uri", spURICols, rs.spURI},
		{"sp_reference", spReferenceCols, rs.spReference},
		{"sp_composite_token_quantity", spCompositeCols, rs.spComposite},
	}
	for _, t := range tables {
		QueueInsertBatched(qb, t.name, t.cols, t.rows, maxRowsPerStmt)
	}
}

// Flush queues the accumulated index changes (see QueueFlush) and sends them in
// one pipelined round trip on tx. Retained for callers that flush a RowSet on
// its own, like the standalone Extractor.Index.
func (rs *RowSet) Flush(ctx context.Context, tx pgx.Tx, maxRowsPerStmt int) error {
	qb := &Batch{}
	rs.QueueFlush(qb, maxRowsPerStmt)
	return qb.Send(ctx, tx)
}

// QueueInsertBatched queues multi-row INSERT ... VALUES statements for rows.
// Each statement carries at most maxRowsPerStmt rows, additionally clamped so
// no statement exceeds maxInsertParams bind parameters (the wire protocol's
// hard limit). It is a no-op for an empty slice. Exported so the store reuses
// it for the resources and resource_history batched writes, keeping one write
// mechanism. Every value is passed as a bind parameter, so the RLS WITH CHECK
// policy validates each row exactly as the former per-row INSERTs did.
func QueueInsertBatched(qb *Batch, table string, cols []string, rows [][]any, maxRowsPerStmt int) {
	if len(rows) == 0 {
		return
	}
	ncol := len(cols)
	rowsPerChunk := maxRowsPerStmt
	if paramCap := maxInsertParams / ncol; rowsPerChunk > paramCap {
		rowsPerChunk = paramCap
	}
	if rowsPerChunk < 1 {
		rowsPerChunk = 1
	}
	colList := strings.Join(cols, ", ")

	for start := 0; start < len(rows); start += rowsPerChunk {
		end := start + rowsPerChunk
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[start:end]

		var sb strings.Builder
		sb.WriteString("INSERT INTO ")
		sb.WriteString(table)
		sb.WriteString(" (")
		sb.WriteString(colList)
		sb.WriteString(") VALUES ")
		args := make([]any, 0, len(chunk)*ncol)
		p := 1
		for i, r := range chunk {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('(')
			for j := 0; j < ncol; j++ {
				if j > 0 {
					sb.WriteByte(',')
				}
				sb.WriteByte('$')
				sb.WriteString(strconv.Itoa(p))
				p++
			}
			sb.WriteByte(')')
			args = append(args, r...)
		}
		qb.Queue("batch insert into "+table, sb.String(), args...)
	}
}

// InsertBatched queues rows (see QueueInsertBatched) and sends them immediately
// in one pipelined round trip on tx.
func InsertBatched(ctx context.Context, tx pgx.Tx, table string, cols []string, rows [][]any, maxRowsPerStmt int) error {
	qb := &Batch{}
	QueueInsertBatched(qb, table, cols, rows, maxRowsPerStmt)
	return qb.Send(ctx, tx)
}
