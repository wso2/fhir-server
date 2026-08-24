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

import "context"

// AggregateMeta returns the distinct meta in use for the caller's tenant —
// tags/security from sp_token, profiles from sp_uri. When resourceType is empty
// the scope is system-wide (but still tenant-scoped). Any backend read failure
// is propagated so callers fail rather than returning a partial result.
func (s *Store) AggregateMeta(ctx context.Context, resourceType string) (map[string]any, error) {
	meta := map[string]any{}

	tags, err := s.distinctCodings(ctx, "_tag", resourceType)
	if err != nil {
		return nil, err
	}
	if len(tags) > 0 {
		meta["tag"] = tags
	}
	sec, err := s.distinctCodings(ctx, "_security", resourceType)
	if err != nil {
		return nil, err
	}
	if len(sec) > 0 {
		meta["security"] = sec
	}
	profs, err := s.distinctURIs(ctx, "_profile", resourceType)
	if err != nil {
		return nil, err
	}
	if len(profs) > 0 {
		meta["profile"] = profs
	}
	return meta, nil
}

func (s *Store) distinctCodings(ctx context.Context, param, rt string) ([]any, error) {
	c, err := s.tenantConn(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Release()

	q := `SELECT DISTINCT system, code FROM sp_token
	      WHERE param_name = $1 AND tenant_id = current_setting('app.current_tenant', true)`
	args := []any{param}
	if rt != "" {
		q += ` AND resource_type = $2`
		args = append(args, rt)
	}
	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []any
	for rows.Next() {
		var system, code string
		if err := rows.Scan(&system, &code); err != nil {
			return nil, err
		}
		row := map[string]any{}
		if system != "" {
			row["system"] = system
		}
		if code != "" {
			row["code"] = code
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) distinctURIs(ctx context.Context, param, rt string) ([]any, error) {
	c, err := s.tenantConn(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Release()

	q := `SELECT DISTINCT value FROM sp_uri
	      WHERE param_name = $1 AND tenant_id = current_setting('app.current_tenant', true)`
	args := []any{param}
	if rt != "" {
		q += ` AND resource_type = $2`
		args = append(args, rt)
	}
	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []any
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}
