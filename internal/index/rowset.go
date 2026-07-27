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

// maxInsertParams caps the bind parameters in one multi-row INSERT, safely under
// PostgreSQL's 65535-parameter protocol limit. The batched writer picks the
// largest whole number of rows per statement that fits, so a table's rows are
// inserted in as few statements as the limit allows.
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

// refKey identifies a resource whose existing sp_* rows must be cleared before
// its freshly extracted rows are inserted (the re-index on update/patch/delete).
type refKey struct {
	resourceID   string
	resourceType string
}

// RowSet accumulates the sp_* index rows for one database transaction so the
// whole transaction's index writes flush as a handful of COPY (and, for
// re-indexed resources, one batched DELETE per table) statements instead of one
// INSERT/DELETE per row. A single bundle transaction shares one RowSet across
// all its entries; the single-resource CRUD paths use one with a batch of one.
//
// The row slices hold values in the column order declared above, tenant_id
// first. Callers append via the extractor's append* helpers.
type RowSet struct {
	tenant string

	spString    [][]any
	spToken     [][]any
	spDate      [][]any
	spNumber    [][]any
	spQuantity  [][]any
	spURI       [][]any
	spReference [][]any
	spComposite [][]any

	// deletes is the ordered, de-duplicated set of resources to clear before
	// insert; deleteSeen guards the dedup.
	deletes    []refKey
	deleteSeen map[refKey]struct{}
}

// NewRowSet returns an empty RowSet stamped with the transaction's tenant, which
// is written into every row's tenant_id column.
func NewRowSet(tenant string) *RowSet {
	return &RowSet{tenant: tenant, deleteSeen: map[refKey]struct{}{}}
}

// AddDelete records that resource (resourceType, resourceID) must have its
// existing sp_* rows removed at flush (re-index), and drops any rows already
// accumulated for it earlier in this transaction. The purge keeps a
// create-then-update of the same resource within one bundle byte-identical to
// the old per-op delete-then-reinsert: only the final version's rows survive.
func (rs *RowSet) AddDelete(resourceType, resourceID string) {
	k := refKey{resourceID: resourceID, resourceType: resourceType}
	if _, ok := rs.deleteSeen[k]; !ok {
		rs.deleteSeen[k] = struct{}{}
		rs.deletes = append(rs.deletes, k)
	}
	rs.purge(k)
}

// purge removes any accumulated rows for k from every sp_* slice. Row layout is
// [tenant_id, resource_id, resource_type, ...], so indexes 1 and 2 identify the
// owning resource.
func (rs *RowSet) purge(k refKey) {
	filter := func(rows [][]any) [][]any {
		out := rows[:0]
		for _, r := range rows {
			if r[1] == k.resourceID && r[2] == k.resourceType {
				continue
			}
			out = append(out, r)
		}
		return out
	}
	rs.spString = filter(rs.spString)
	rs.spToken = filter(rs.spToken)
	rs.spDate = filter(rs.spDate)
	rs.spNumber = filter(rs.spNumber)
	rs.spQuantity = filter(rs.spQuantity)
	rs.spURI = filter(rs.spURI)
	rs.spReference = filter(rs.spReference)
	rs.spComposite = filter(rs.spComposite)
}

// Flush writes the accumulated index changes to tx: first the batched re-index
// DELETEs (one statement per sp_* table over every collected resource key), then
// chunked multi-row INSERTs per sp_* table. DELETEs precede INSERTs so a
// re-indexed resource's stale rows are gone before its fresh rows land. The
// caller runs this inside the bundle's transaction, after all entries are
// processed and after the parent resources rows exist (the sp_* FK), and before
// COMMIT.
func (rs *RowSet) Flush(ctx context.Context, tx pgx.Tx) error {
	if len(rs.deletes) > 0 {
		ids := make([]string, len(rs.deletes))
		types := make([]string, len(rs.deletes))
		for i, k := range rs.deletes {
			ids[i] = k.resourceID
			types[i] = k.resourceType
		}
		// One DELETE per table over all (resource_id, resource_type) pairs,
		// replacing the former per-resource DELETE storm. UNNEST pairs the two
		// parallel arrays positionally. The tenant predicate mirrors the original
		// per-row DELETE so exactly the same rows are removed.
		for _, tbl := range spDeleteTables {
			q := fmt.Sprintf(`DELETE FROM %s t
			                  USING (SELECT UNNEST($1::text[]) AS rid, UNNEST($2::text[]) AS rt) d
			                  WHERE t.tenant_id = current_setting('app.current_tenant', true)
			                    AND t.resource_id = d.rid AND t.resource_type = d.rt`, tbl)
			if _, err := tx.Exec(ctx, q, ids, types); err != nil {
				return fmt.Errorf("batch re-index delete from %s: %w", tbl, err)
			}
		}
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
		if err := InsertBatched(ctx, tx, t.name, t.cols, t.rows); err != nil {
			return err
		}
	}
	return nil
}

// InsertBatched inserts rows into table with multi-row INSERT ... VALUES
// statements, chunked so no single statement exceeds maxInsertParams bind
// parameters. It is a no-op for an empty slice. Exported so the store reuses it
// for the resources and resource_history batched writes, keeping one write
// mechanism. Every value is passed as a bind parameter, so the RLS WITH CHECK
// policy validates each row exactly as the former per-row INSERTs did.
func InsertBatched(ctx context.Context, tx pgx.Tx, table string, cols []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	ncol := len(cols)
	rowsPerChunk := maxInsertParams / ncol
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
		if _, err := tx.Exec(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("batch insert into %s: %w", table, err)
		}
	}
	return nil
}
