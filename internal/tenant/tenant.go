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

// Package tenant carries the active tenant identifier through a request's
// context. In the logical multi-tenancy model (Option 2) every request is
// attributed to exactly one tenant; the store layer turns that identifier into
// the Postgres `app.current_tenant` setting that Row-Level Security policies
// key on. Deployments that run one server/database per tenant (Option 1) can
// ignore tenancy entirely — requests fall back to the Default tenant.
package tenant

import (
	"context"
	"regexp"
)

// Default is the tenant assigned to requests that do not carry an explicit
// tenant (e.g. the legacy /fhir/r4 base path). Single-tenant deployments
// therefore keep working unchanged: all of their data lives under "default".
const Default = "default"

// validID constrains tenant identifiers to a conservative, URL- and
// SQL-safe character set. It also bounds the length so a tenant id cannot be
// used to smuggle arbitrary text into the `app.current_tenant` setting.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

type ctxKey struct{}

// WithTenant returns a copy of ctx that carries the given tenant id.
func WithTenant(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// From returns the tenant id carried by ctx, or Default if none is set.
func From(ctx context.Context) string {
	if id, ok := ctx.Value(ctxKey{}).(string); ok && id != "" {
		return id
	}
	return Default
}

// Valid reports whether id is a well-formed tenant identifier.
func Valid(id string) bool {
	return validID.MatchString(id)
}
