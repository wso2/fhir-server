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

package db

import (
	"context"
	"embed"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaFS embed.FS

func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return pool, nil
}

// CreateTables applies the embedded schema.sql to the database, creating the
// tables and indexes the server needs if they do not already exist.
//
// This is NOT a migration system: schema.sql is purely additive (CREATE TABLE
// IF NOT EXISTS / ADD COLUMN IF NOT EXISTS), so it cannot perform destructive
// or altering schema changes. Applying it requires a database role with DDL
// privileges, so the server only calls it when explicitly opted in (see the
// CreateTables config / FHIR_CREATE_TABLES env var).
func CreateTables(ctx context.Context, pool *pgxpool.Pool) error {
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read embedded schema: %w", err)
	}

	_, err = pool.Exec(ctx, string(schema))
	if err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	slog.Info("database tables created")
	return nil
}
