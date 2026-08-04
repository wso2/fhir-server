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

package store

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// ─── Tuning & request-mode plumbing ───────────────────────────────────────────

// BundleTuning carries the bundle-execution knobs (resolved from config; see
// internal/config and docs/performance-tuning.md). TransactionConcurrency is
// the shard count for parallel transaction bundles — 1 keeps the capability
// off and every transaction on the serial path. TransactionParallelDefault is
// the mode applied when a request carries no x-bundle-processing-logic header.
type BundleTuning struct {
	TransactionConcurrency     int  // shard count; 1 = parallel execution off
	TransactionParallelDefault bool // header absent → parallel when true, sequential when false
}

// WithBundleTuning sets the bundle execution tunables (resolved from config).
// Non-positive concurrency is clamped to 1 (off).
func WithBundleTuning(t BundleTuning) func(*Store) {
	return func(s *Store) {
		if t.TransactionConcurrency < 1 {
			t.TransactionConcurrency = 1
		}
		s.bundleTuning = t
	}
}

// Bundle processing modes carried from the HTTP layer. They mirror the values
// of Microsoft FHIR server's x-bundle-processing-logic request header.
const (
	BundleProcessingParallel   = "parallel"
	BundleProcessingSequential = "sequential"
)

type bundleProcessingCtxKey struct{}

// WithBundleProcessing returns a copy of ctx carrying the client's requested
// bundle processing logic (BundleProcessingParallel or BundleProcessingSequential).
// The handler sets it only when the request carried a recognised header value;
// an absent value falls back to the configured default.
func WithBundleProcessing(ctx context.Context, mode string) context.Context {
	return context.WithValue(ctx, bundleProcessingCtxKey{}, mode)
}

// BundleProcessingFrom returns the processing logic carried by ctx, or "" when
// the request did not specify one.
func BundleProcessingFrom(ctx context.Context) string {
	if v, ok := ctx.Value(bundleProcessingCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// parallelTransactionRequested resolves the effective mode for one transaction:
// the request header wins; when absent the configured default applies; and
// parallel is effective only when TransactionConcurrency > 1 (silently ignored
// otherwise, so a lone server with the capability off never changes behavior).
func (s *Store) parallelTransactionRequested(ctx context.Context) bool {
	if s.bundleTuning.TransactionConcurrency <= 1 {
		return false
	}
	switch BundleProcessingFrom(ctx) {
	case BundleProcessingParallel:
		return true
	case BundleProcessingSequential:
		return false
	}
	return s.bundleTuning.TransactionParallelDefault
}

// ─── Overlap guard ─────────────────────────────────────────────────────────────

// overlappingWriteTarget returns a "Type/id" key that two or more non-skipped
// write ops (DELETE/POST/PUT/PATCH) target, or "" when every write target is
// unique. Parallel execution requires unique targets — the client's side of the
// x-bundle-processing-logic: parallel contract — so any overlap sends the
// bundle down the serial path (correct result, no speedup). Skipped ops are
// excluded: a conditional create that resolved to an existing resource writes
// nothing.
func overlappingWriteTarget(ops []bundleOp) string {
	seen := make(map[string]bool, len(ops))
	for i := range ops {
		o := &ops[i]
		if o.skip || o.id == "" {
			continue
		}
		switch o.method {
		case "POST", "PUT", "PATCH", "DELETE":
			key := o.resourceType + "/" + o.id
			if seen[key] {
				return key
			}
			seen[key] = true
		}
	}
	return ""
}

// ─── Parallel executor ─────────────────────────────────────────────────────────

// executeTransactionParallel executes a planned transaction Bundle across K
// concurrent database transactions (shards). Called from executeTransaction
// only when parallel mode is resolved, the overlap guard passed, and the
// bundle has at least two write ops; the serial body remains the default and
// the fallback.
//
// Execution shape (see docs/performance-tuning.md for the client contract):
//
//	DELETE ops   — K contiguous chunks, concurrently, then a barrier
//	POST/PUT/PATCH — K contiguous chunks, concurrently, then a barrier
//	row cap      — bundle-total check before anything is flushed
//	flush        — per shard, concurrently, then a barrier
//	commit       — fast serial loop (the only non-atomic window)
//	GET ops      — sequentially on the pool, reading committed data
//
// Any error before the commit loop cancels the remaining shards and rolls
// every shard back, preserving all-or-nothing semantics. A failure during the
// commit loop itself is reported as a 500 naming how many shards committed.
func (s *Store) executeTransactionParallel(ctx context.Context, ops []bundleOp) ([]BundleEntryResult, error) {
	// Verb-ordered indices (as serial), split into the delete phase, the write
	// phase, and the post-commit reads. Skipped ops write nothing and join the
	// write phase for result placement only.
	var deletes, writes, gets []int
	for _, idx := range verbOrder(ops) {
		switch {
		case ops[idx].method == "GET":
			gets = append(gets, idx)
		case ops[idx].method == "DELETE" && !ops[idx].skip:
			deletes = append(deletes, idx)
		default:
			writes = append(writes, idx)
		}
	}

	k := s.bundleTuning.TransactionConcurrency
	if n := len(deletes) + len(writes); k > n {
		k = n
	}

	// Same deferred-resources read-set as serial: a POSTed row buffered for the
	// batched insert must not be one a later entry reads back inside the shard
	// transaction. (GETs run post-commit here, but PUT/PATCH/DELETE version bumps
	// still read through the transaction, and the guard already forbids those on
	// POSTed ids — keeping the serial computation keeps the two paths identical.)
	readSet := make(map[string]bool)
	for i := range ops {
		o := ops[i]
		if o.skip || o.id == "" {
			continue
		}
		switch o.method {
		case "GET", "PUT", "PATCH", "DELETE":
			readSet[o.resourceType+"/"+o.id] = true
		}
	}

	// One transaction + writer per shard, opened up front so a Begin failure
	// aborts before any work starts. Rollback of a committed tx is a no-op error.
	type shard struct {
		tx pgx.Tx
		w  *bundleWriter
	}
	shards := make([]*shard, 0, k)
	defer func() {
		for _, sh := range shards {
			_ = sh.tx.Rollback(ctx)
		}
	}()
	for i := 0; i < k; i++ {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, &BundleError{HTTPStatus: 500, Code: "exception", EntryIndex: -1, Diagnostics: err.Error()}
		}
		shards = append(shards, &shard{tx: tx, w: s.newBundleWriter(ctx)})
		if err := setTenantTx(ctx, tx); err != nil {
			return nil, &BundleError{HTTPStatus: 500, Code: "exception", EntryIndex: -1, Diagnostics: err.Error()}
		}
	}

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]BundleEntryResult, len(ops))
	errCh := make(chan *BundleError, k)

	// runWave executes fn(shard i, chunk i) on every shard concurrently and
	// waits for all of them; fn reports failure through errCh + cancel. A nil
	// chunks (the flush wave) runs fn once per shard.
	runWave := func(chunks [][]int, fn func(sh *shard, chunk []int) *BundleError) *BundleError {
		var wg sync.WaitGroup
		for i, sh := range shards {
			var chunk []int
			if chunks != nil {
				if i < len(chunks) {
					chunk = chunks[i]
				}
				if len(chunk) == 0 {
					continue
				}
			}
			wg.Add(1)
			go func(sh *shard, chunk []int) {
				defer wg.Done()
				if be := fn(sh, chunk); be != nil {
					errCh <- be
					cancel()
				}
			}(sh, chunk)
		}
		wg.Wait()
		return drainBundleErrs(errCh)
	}

	execChunk := func(sh *shard, chunk []int) *BundleError {
		for _, idx := range chunk {
			if pctx.Err() != nil {
				return nil // a sibling shard failed; its error is already reported
			}
			op := ops[idx]
			deferResource := op.method == "POST" && op.id != "" && !readSet[op.resourceType+"/"+op.id]
			res, berr := s.execOpInTx(pctx, sh.tx, op, sh.w, deferResource)
			if berr != nil {
				berr.EntryIndex = op.origIndex
				return berr
			}
			results[op.origIndex] = res
		}
		return nil
	}

	// Phase 1: deletes. One extra barrier, free on all-POST import bundles.
	start := time.Now()
	if len(deletes) > 0 {
		if be := runWave(chunkContiguous(deletes, k), execChunk); be != nil {
			return nil, be
		}
		slog.Debug("parallel bundle phase", "phase", "delete", "ops", len(deletes), "shards", k, "elapsed", time.Since(start))
	}

	// Phase 2: POST/PUT/PATCH (and skipped-op result placement).
	start = time.Now()
	if be := runWave(chunkContiguous(writes, k), execChunk); be != nil {
		return nil, be
	}
	slog.Debug("parallel bundle phase", "phase", "execute", "ops", len(writes), "shards", k, "elapsed", time.Since(start))

	// Bundle-total row cap, checked before anything is flushed so the limit
	// stays a per-bundle contract (per-shard flush would only catch a single
	// shard exceeding it) and an oversized bundle sends nothing to the database.
	if limit := s.writeTuning.MaxRowsPerBundle; limit > 0 {
		total, limitHit := 0, false
		tableTotals := map[string]int{}
		for _, sh := range shards {
			total += sh.w.totalRows()
			limitHit = limitHit || sh.w.rs.LimitHit
			for tbl, n := range sh.w.rs.TableCounts() {
				tableTotals[tbl] += n
			}
		}
		if limitHit || total > limit {
			slog.Warn("write exceeded per-transaction row limit",
				"limit", limit,
				"indexRows", total,
				"shards", k,
				"sp_composite_token_quantity", tableTotals["sp_composite_token_quantity"],
				"sp_token", tableTotals["sp_token"],
				"sp_string", tableTotals["sp_string"],
				"sp_reference", tableTotals["sp_reference"],
				"sp_quantity", tableTotals["sp_quantity"],
				"sp_date", tableTotals["sp_date"],
				"sp_number", tableTotals["sp_number"],
				"sp_uri", tableTotals["sp_uri"],
			)
			be := storeErrToBundleErr(WriteLimitError{Rows: total, Limit: limit})
			be.EntryIndex = -1
			return nil, be
		}
	}

	// Flush every shard's buffered writes concurrently, then the barrier the
	// commit loop requires: past this point every shard is fully written and
	// only COMMIT remains.
	start = time.Now()
	if be := runWave(nil, func(sh *shard, _ []int) *BundleError {
		if err := sh.w.flush(pctx, sh.tx); err != nil {
			be := storeErrToBundleErr(err)
			be.EntryIndex = -1
			return be
		}
		return nil
	}); be != nil {
		return nil, be
	}
	slog.Debug("parallel bundle phase", "phase", "flush", "shards", k, "elapsed", time.Since(start))

	// Commit barrier passed — fast serial commit loop. A failure here is the
	// documented non-atomic window: earlier shards are already durable, the
	// failing and remaining ones roll back via the deferred Rollback.
	start = time.Now()
	for committed, sh := range shards {
		if err := sh.tx.Commit(ctx); err != nil {
			return nil, &BundleError{
				HTTPStatus: 500, Code: "exception", EntryIndex: -1,
				Diagnostics: fmt.Sprintf("parallel bundle commit failed after %d of %d shards committed; the bundle is partially applied: %v", committed, k, err),
			}
		}
	}
	slog.Debug("parallel bundle phase", "phase", "commit", "shards", k, "elapsed", time.Since(start))

	// GET entries read committed data from the pool — the same visible results
	// as serial's end-of-transaction reads. A GET failure here surfaces as the
	// bundle's error, but the writes above are already committed (documented
	// parallel-mode semantics).
	for _, idx := range gets {
		op := ops[idx]
		res, berr := s.execGetPostCommit(ctx, op)
		if berr != nil {
			berr.EntryIndex = op.origIndex
			return nil, berr
		}
		res.Method = op.method
		res.ResourceType = op.resourceType
		if res.ID == "" {
			res.ID = op.id
		}
		results[op.origIndex] = res
	}

	slog.Debug("processed transaction bundle", "entries", len(ops), "mode", "parallel", "shards", k)
	return results, nil
}

// execGetPostCommit serves a transaction Bundle GET entry after the commit
// loop, reading committed data via the pool. Mirrors execGetInTx: instance
// reads (Read), version reads (GetVersion), and searches (Search) — the latter
// two already use the pool on the serial path too.
func (s *Store) execGetPostCommit(ctx context.Context, op bundleOp) (BundleEntryResult, *BundleError) {
	if !op.isSearch {
		if op.versionID != "" {
			vid, _ := strconv.Atoi(op.versionID)
			res, err := s.GetVersion(ctx, op.resourceType, op.id, vid)
			if err != nil {
				return BundleEntryResult{}, storeErrToBundleErr(err)
			}
			return BundleEntryResult{Status: "200 OK", Resource: res, ETag: etag(res)}, nil
		}
		res, err := s.Read(ctx, op.resourceType, op.id)
		if err != nil {
			return BundleEntryResult{}, storeErrToBundleErr(err)
		}
		return BundleEntryResult{Status: "200 OK", Resource: res, ETag: etag(res)}, nil
	}
	result, err := s.Search(ctx, SearchParams{ResourceType: op.resourceType, Params: valuesToMap(op.query)})
	if err != nil {
		return BundleEntryResult{}, &BundleError{HTTPStatus: 500, Code: "exception", Diagnostics: err.Error()}
	}
	return BundleEntryResult{Status: "200 OK", Resource: searchsetBundle(result)}, nil
}

// ─── Small helpers ─────────────────────────────────────────────────────────────

// chunkContiguous splits items into k contiguous chunks of near-equal size,
// preserving order within each chunk. Fewer than k non-empty chunks are
// returned when len(items) < k.
func chunkContiguous(items []int, k int) [][]int {
	chunks := make([][]int, 0, k)
	n := len(items)
	for i := 0; i < k; i++ {
		lo, hi := i*n/k, (i+1)*n/k
		chunks = append(chunks, items[lo:hi])
	}
	return chunks
}

// drainBundleErrs empties errCh and returns the error to surface: the
// lowest-EntryIndex genuine failure (matching serial determinism, where the
// earliest failing entry aborts the transaction). Errors produced by sibling
// cancellation ("context canceled" bubbling out of an in-flight op) rank below
// genuine ones so the root cause wins.
func drainBundleErrs(errCh chan *BundleError) *BundleError {
	var best *BundleError
	bestNoise := false
	for {
		select {
		case be := <-errCh:
			noise := strings.Contains(be.Diagnostics, context.Canceled.Error())
			switch {
			case best == nil,
				bestNoise && !noise,
				bestNoise == noise && entryIndexRank(be) < entryIndexRank(best):
				best, bestNoise = be, noise
			}
		default:
			return best
		}
	}
}

// entryIndexRank orders BundleErrors for drainBundleErrs: entry-scoped errors
// by index, bundle-scoped (-1, e.g. a flush failure) after them.
func entryIndexRank(be *BundleError) int {
	if be.EntryIndex < 0 {
		return int(^uint(0) >> 1) // max int
	}
	return be.EntryIndex
}

// countWriteOps counts the non-GET ops in a planned bundle. A parallel run
// needs at least two of them for sharding to have anything to overlap.
func countWriteOps(ops []bundleOp) int {
	n := 0
	for i := range ops {
		if ops[i].method != "GET" {
			n++
		}
	}
	return n
}

// verbOrder returns op indices sorted into FHIR transaction processing order
// (DELETE, POST, PUT/PATCH, GET), stable within each verb — the same order the
// serial path executes. Shared by the serial and parallel executors.
func verbOrder(ops []bundleOp) []int {
	order := make([]int, len(ops))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return methodOrder(ops[order[a]].method) < methodOrder(ops[order[b]].method)
	})
	return order
}
