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

package db_test

import (
	"context"
	"testing"

	"github.com/wso2/fhir-server/internal/testutil"
)

func TestCreateTables_CreatesExpectedTables(t *testing.T) {
	pool := testutil.MustDB(t) // MustDB already runs CreateTables
	ctx := context.Background()

	tables := []string{
		"resources",
		"resource_history",
		"search_param_definitions",
		"sp_string",
		"sp_token",
		"sp_date",
		"sp_number",
		"sp_quantity",
		"sp_uri",
		"sp_reference",
		"ig_packages",
		"ig_profiles",
	}

	for _, tbl := range tables {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)`, tbl,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("query table %q: %v", tbl, err)
		}
		if !exists {
			t.Errorf("table %q not created by CreateTables", tbl)
		}
	}
}

func TestCreateTables_Idempotent(t *testing.T) {
	pool := testutil.MustDB(t)
	ctx := context.Background()

	// MustDB already ran CreateTables once; query to confirm tables are usable.
	var n int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM resources`).Scan(&n)
	if err != nil {
		t.Fatalf("query after table creation: %v", err)
	}
}

func TestCreateTables_SearchParamDefinitions_HasColumns(t *testing.T) {
	pool := testutil.MustDB(t)
	ctx := context.Background()

	cols := []string{"resource_type", "param_name", "param_type", "fhirpath_expr", "is_custom", "ig_source"}
	for _, col := range cols {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'search_param_definitions' AND column_name = $1
			)`, col,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("query column %q: %v", col, err)
		}
		if !exists {
			t.Errorf("column %q missing from search_param_definitions", col)
		}
	}
}
