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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wso2/fhir-server/internal/index"
	"github.com/wso2/fhir-server/internal/tenant"
)

const (
	reindexPollInterval = time.Second
	reindexBatchTimeout = 30 * time.Second
)

type reindexCursor struct {
	resourceType string
	id           string
}

type reindexFailure struct{ reason string }

func (e reindexFailure) Error() string { return e.reason }

// RunReindexJobs resumes durable jobs; only the current batch rolls back on shutdown.
func (s *Store) RunReindexJobs(ctx context.Context) {
	for ctx.Err() == nil {
		batchCtx, cancel := context.WithTimeout(ctx, reindexBatchTimeout)
		worked, err := s.processReindexBatch(batchCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("reindex batch will be retried", "err", err)
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(reindexPollInterval):
		}
	}
}

func (s *Store) processReindexBatch(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var jobID, tenantID string
	err = tx.QueryRow(ctx, `SELECT job_id::text, tenant_id FROM reindex_queue
		ORDER BY scheduled_at, job_id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&jobID, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim reindex batch: %w", err)
	}
	ctx = tenant.WithTenant(ctx, tenantID)
	if err := setTenantTx(ctx, tx); err != nil {
		return true, err
	}
	if err := lockReindexDefinitions(ctx, tx); err != nil {
		return true, err
	}
	var job ReindexJob
	var cursor, end reindexCursor
	var hash string
	err = tx.QueryRow(ctx, `SELECT `+reindexJobColumns+`, cursor_type, cursor_id, end_type, end_id, definition_hash
		FROM reindex_jobs WHERE tenant_id = $1 AND job_id = $2 FOR UPDATE`, tenantID, jobID).Scan(
		&job.ID, &job.ResourceType, &job.BatchSize, &job.Status, &job.ResourcesReindexed,
		&job.QueuedTime, &job.LastModified, &job.StartTime, &job.EndTime, &job.Error,
		&cursor.resourceType, &cursor.id, &end.resourceType, &end.id, &hash)
	if err != nil {
		return true, fmt.Errorf("lock reindex job: %w", err)
	}
	// Keep the queue claim while rolling back failed index writes.
	batch, err := tx.Begin(ctx)
	if err != nil {
		return true, err
	}
	count, next, completed, batchErr := s.rebuildReindexBatch(ctx, batch, job, cursor, end, hash)
	if batchErr != nil {
		if err := batch.Rollback(ctx); err != nil {
			return true, fmt.Errorf("rollback reindex batch: %w", err)
		}
		var failure reindexFailure
		if !errors.As(batchErr, &failure) {
			return true, batchErr
		}
		if err := finishReindex(ctx, tx, job.ID, "failed", failure.reason); err != nil {
			return true, err
		}
		return true, tx.Commit(ctx)
	}
	if err := batch.Commit(ctx); err != nil {
		return true, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reindex_jobs SET status = 'running',
		started_at = COALESCE(started_at, NOW()), updated_at = NOW(),
		resources_reindexed = resources_reindexed + $1, cursor_type = $2, cursor_id = $3
		WHERE tenant_id = current_setting('app.current_tenant', true) AND job_id = $4`,
		count, next.resourceType, next.id, job.ID); err != nil {
		return true, fmt.Errorf("record reindex progress: %w", err)
	}
	if completed {
		if err := finishReindex(ctx, tx, job.ID, "completed", ""); err != nil {
			return true, err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE reindex_queue SET scheduled_at = clock_timestamp() WHERE job_id = $1`, job.ID); err != nil {
		return true, fmt.Errorf("reschedule reindex batch: %w", err)
	}
	return true, tx.Commit(ctx)
}

func (s *Store) rebuildReindexBatch(ctx context.Context, tx pgx.Tx, job ReindexJob, cursor, end reindexCursor, hash string) (int64, reindexCursor, bool, error) {
	registry, currentHash, err := reindexRegistry(ctx, tx, job.ResourceType)
	if err != nil {
		return 0, cursor, false, err
	}
	if currentHash != hash {
		return 0, cursor, false, reindexFailure{reason: "search parameter definitions changed; start a new reindex job"}
	}
	rows, err := tx.Query(ctx, `SELECT resource_type, fhir_id, resource_json, last_updated, is_deleted FROM resources
		WHERE tenant_id = current_setting('app.current_tenant', true)
		AND ($1::text = '' OR resource_type = $1)
		AND (resource_type, fhir_id) > ($2, $3) AND (resource_type, fhir_id) <= ($4, $5)
		ORDER BY resource_type, fhir_id LIMIT $6 FOR UPDATE`,
		job.ResourceType, cursor.resourceType, cursor.id, end.resourceType, end.id, job.BatchSize)
	if err != nil {
		return 0, cursor, false, fmt.Errorf("read reindex resources: %w", err)
	}
	defer rows.Close()
	rs := index.NewRowSet(tenant.From(ctx), s.writeTuning.MaxRowsPerBundle)
	extractor := index.New(registry)
	var scanned int
	var reindexed int64
	for rows.Next() {
		var raw []byte
		var lastUpdated time.Time
		var deleted bool
		if err := rows.Scan(&cursor.resourceType, &cursor.id, &raw, &lastUpdated, &deleted); err != nil {
			return 0, cursor, false, err
		}
		scanned++
		rs.AddDelete(cursor.resourceType, cursor.id)
		if deleted {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			return 0, cursor, false, reindexFailure{reason: "stored resource is not valid JSON; repair it and start a new reindex job"}
		}
		extractor.Extract(rs, cursor.resourceType, cursor.id, body, lastUpdated)
		if rs.LimitHit {
			return 0, cursor, false, reindexFailure{reason: "batch exceeds index-row limit; reduce batchSize or raise write.maxRowsPerBundle and start a new reindex job"}
		}
		reindexed++
	}
	if err := rows.Err(); err != nil {
		return 0, cursor, false, err
	}
	rows.Close()
	if err := rs.Flush(ctx, tx, s.writeTuning.MaxRowsPerStatement); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23")) {
			return 0, cursor, false, reindexFailure{reason: "index data violates database limits or constraints; repair the resource or definition and start a new reindex job"}
		}
		return 0, cursor, false, fmt.Errorf("write reindex rows: %w", err)
	}
	return reindexed, cursor, scanned < job.BatchSize, nil
}

func finishReindex(ctx context.Context, tx pgx.Tx, id, status, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE reindex_jobs SET status = $1, error = $2,
		started_at = COALESCE(started_at, NOW()), ended_at = NOW(), updated_at = NOW()
		WHERE tenant_id = current_setting('app.current_tenant', true) AND job_id = $3`, status, reason, id); err != nil {
		return fmt.Errorf("finish reindex job: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM reindex_queue WHERE job_id = $1`, id); err != nil {
		return fmt.Errorf("remove finished reindex job from queue: %w", err)
	}
	return nil
}
