//go:build integration

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

// Package testutil provides shared helpers for integration tests.
// All helpers require Docker (via testcontainers-go).
package testutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/wso2/fhir-server/internal/db"
	"github.com/wso2/fhir-server/internal/searchparam"
	"github.com/wso2/fhir-server/internal/seed"
)

// MustDB starts a PostgreSQL 16 container, creates the schema tables, and
// returns a ready pool. The container is terminated when t completes.
func MustDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pgc, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := pgc.Terminate(ctx); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get connection string: %v", err)
	}

	// Default every connection to the "default" tenant. Tests that drive the
	// store set the tenant per request and override this; tests that write via
	// raw SQL still need a tenant for the tenant_id column default and the
	// Row-Level Security policies added in schema v6.
	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET app.current_tenant = 'default'")
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	if err := db.CreateTables(ctx, pool); err != nil {
		t.Fatalf("create tables: %v", err)
	}

	return pool
}

// MustSeededDB is like MustDB but also inserts the FHIR R4 base search params.
func MustSeededDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := MustDB(t)
	if err := seed.SeedSearchParams(context.Background(), pool); err != nil {
		t.Fatalf("seed search params: %v", err)
	}
	return pool
}

// MustRegistry loads a search param registry from an already-seeded pool.
func MustRegistry(t *testing.T, pool *pgxpool.Pool) *searchparam.Registry {
	t.Helper()
	reg := searchparam.NewRegistry()
	if err := reg.Load(context.Background(), pool); err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}
