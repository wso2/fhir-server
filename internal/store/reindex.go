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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wso2/fhir-server/internal/searchparam"
	"github.com/wso2/fhir-server/internal/tenant"
)

const (
	DefaultReindexBatchSize = 100
	MaxReindexBatchSize     = 1000
	reindexJobColumns       = `job_id::text, resource_type, batch_size, status, resources_reindexed,
		queued_at, updated_at, started_at, ended_at, error`
)

var reindexResourceType = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

type ReindexOptions struct {
	ResourceType string
	BatchSize    int
}

type ReindexJob struct {
	ID                 string
	ResourceType       string
	BatchSize          int
	Status             string
	ResourcesReindexed int64
	QueuedTime         time.Time
	LastModified       time.Time
	StartTime          *time.Time
	EndTime            *time.Time
	Error              string
}

func (s *Store) StartReindex(ctx context.Context, opts ReindexOptions) (ReindexJob, error) {
	if opts.BatchSize == 0 {
		opts.BatchSize = DefaultReindexBatchSize
	}
	if opts.BatchSize < 1 || opts.BatchSize > MaxReindexBatchSize {
		return ReindexJob{}, &InvalidParamError{Msg: fmt.Sprintf("batchSize must be between 1 and %d", MaxReindexBatchSize)}
	}
	if opts.ResourceType != "" && (len(opts.ResourceType) > 100 || !reindexResourceType.MatchString(opts.ResourceType)) {
		return ReindexJob{}, &InvalidParamError{Msg: "invalid resourceType"}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReindexJob{}, fmt.Errorf("begin reindex job: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := setTenantTx(ctx, tx); err != nil {
		return ReindexJob{}, err
	}
	if err := lockReindexDefinitions(ctx, tx); err != nil {
		return ReindexJob{}, err
	}
	_, hash, err := reindexRegistry(ctx, tx, opts.ResourceType)
	if err != nil {
		return ReindexJob{}, err
	}
	var endType, endID string
	err = tx.QueryRow(ctx, `SELECT resource_type, fhir_id FROM resources
		WHERE tenant_id = current_setting('app.current_tenant', true)
		AND ($1 = '' OR resource_type = $1)
		ORDER BY resource_type DESC, fhir_id DESC LIMIT 1`, opts.ResourceType).Scan(&endType, &endID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ReindexJob{}, fmt.Errorf("find reindex upper bound: %w", err)
	}
	job, err := scanReindexJob(tx.QueryRow(ctx, `INSERT INTO reindex_jobs
		(tenant_id, job_id, resource_type, batch_size, status, end_type, end_id, definition_hash)
		VALUES ($1, $2, $3, $4, 'queued', $5, $6, $7) RETURNING `+reindexJobColumns,
		tenant.From(ctx), uuid.NewString(), opts.ResourceType, opts.BatchSize, endType, endID, hash))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "idx_reindex_active_tenant" {
			return ReindexJob{}, ConflictError{Message: "a reindex job is already queued or running for this tenant"}
		}
		return ReindexJob{}, fmt.Errorf("create reindex job: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO reindex_queue (tenant_id, job_id) VALUES ($1, $2)`, tenant.From(ctx), job.ID); err != nil {
		return ReindexJob{}, fmt.Errorf("enqueue reindex job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ReindexJob{}, fmt.Errorf("commit reindex job: %w", err)
	}
	return job, nil
}

func (s *Store) GetReindex(ctx context.Context, id string) (ReindexJob, error) {
	if _, err := uuid.Parse(id); err != nil {
		return ReindexJob{}, NotFoundError{ResourceType: "reindex job", ResourceID: id}
	}
	c, err := s.tenantConn(ctx)
	if err != nil {
		return ReindexJob{}, err
	}
	defer c.Release()
	job, err := scanReindexJob(c.QueryRow(ctx, `SELECT `+reindexJobColumns+` FROM reindex_jobs
		WHERE tenant_id = current_setting('app.current_tenant', true) AND job_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReindexJob{}, NotFoundError{ResourceType: "reindex job", ResourceID: id}
	}
	if err != nil {
		return ReindexJob{}, fmt.Errorf("read reindex job: %w", err)
	}
	return job, nil
}

func scanReindexJob(row pgx.Row) (ReindexJob, error) {
	var job ReindexJob
	err := row.Scan(&job.ID, &job.ResourceType, &job.BatchSize, &job.Status, &job.ResourcesReindexed,
		&job.QueuedTime, &job.LastModified, &job.StartTime, &job.EndTime, &job.Error)
	return job, err
}

func lockReindexDefinitions(ctx context.Context, tx pgx.Tx) error {
	// Freeze definitions for this batch so a config change cannot race index replacement.
	if _, err := tx.Exec(ctx, `LOCK TABLE search_param_definitions IN SHARE MODE`); err != nil {
		return fmt.Errorf("lock reindex definitions: %w", err)
	}
	return nil
}

func reindexRegistry(ctx context.Context, tx pgx.Tx, resourceType string) (*searchparam.Registry, string, error) {
	registry := searchparam.NewRegistry()
	if err := registry.Load(ctx, tx); err != nil {
		return nil, "", err
	}
	resourceTypes := registry.ResourceTypes()
	if resourceType != "" {
		resourceTypes = []string{resourceType}
	}
	var definitions []searchparam.Definition
	for _, rt := range resourceTypes {
		definitions = append(definitions, registry.ForResource(rt)...)
	}
	raw, err := json.Marshal(definitions)
	if err != nil {
		return nil, "", fmt.Errorf("fingerprint reindex definitions: %w", err)
	}
	return registry, fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}
