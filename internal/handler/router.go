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

package handler

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/wso2/fhir-server/internal/obs"
	"github.com/wso2/fhir-server/internal/searchparam"
	"github.com/wso2/fhir-server/internal/tenant"
)

// NewRouter constructs the chi router. validateOnWrite enables profile
// validation on create/update (default off in production; controlled by
// FHIR_VALIDATE_ON_WRITE).
func NewRouter(s StoreAPI, pool *pgxpool.Pool, registry *searchparam.Registry, baseURL string, igReady *atomic.Int32, validateOnWrite ...bool) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(obs.Middleware)

	// Prometheus metrics endpoint — outside the FHIR base path so it can be
	// scraped without traversing the FHIR middleware stack.
	r.Get("/metrics", obs.MetricsHandler().ServeHTTP)

	vow := len(validateOnWrite) > 0 && validateOnWrite[0]
	h := &fhirHandler{store: s, pool: pool, registry: registry, baseURL: baseURL, igReady: igReady, validateOnWrite: vow}

	// Health probes
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/health/ready", func(w http.ResponseWriter, _ *http.Request) {
		if igReady != nil && igReady.Load() == 1 {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})

	// mountFHIR registers the full FHIR REST surface. It is mounted twice (see
	// below): once at the bare base path and once under a /t/{tenant} prefix, so
	// the routing tree is defined in exactly one place.
	mountFHIR := func(r chi.Router) {
		// System-level transaction / batch Bundle, posted to the FHIR base.
		// Registered without a trailing slash here and with one below so both
		// /fhir/r4 and /fhir/r4/ are accepted.
		r.Post("/fhir/r4", h.bundle)

		r.Route("/fhir/r4", func(r chi.Router) {
			// Capability statement
			r.Get("/metadata", h.metadata)

			// System-level history
			r.Get("/_history", h.systemHistory)

			// System-level operations
			r.Post("/$validate", h.validateSystem) // POST [base]/$validate
			r.Post("/$convert", h.convert)         // POST [base]/$convert
			r.Get("/$meta", h.metaSystem)          // GET  [base]/$meta

			// System-level transaction / batch Bundle (trailing-slash form)
			r.Post("/", h.bundle)

			// Per-resource-type routes
			r.Route("/{resourceType}", func(r chi.Router) {
				r.Get("/", h.search)
				r.Post("/", h.create)
				r.Put("/", h.conditionalUpdate)    // PUT /{type}?<search>
				r.Delete("/", h.conditionalDelete) // DELETE /{type}?<search>
				r.Post("/_search", h.searchPost)
				r.Post("/$validate", h.validate)        // type-level $validate
				r.Get("/$meta", h.metaType)             // GET /{type}/$meta
				r.Get("/$everything", h.everythingType) // type-level $everything
				r.Get("/$lastn", h.lastN)               // GET /Observation/$lastn
				r.Get("/_history", h.typeHistory)

				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.read)
					r.Put("/", h.update)
					r.Patch("/", h.patch)
					r.Delete("/", h.delete)
					r.Get("/_history", h.history)
					r.Get("/_history/{vid}", h.vread)
					r.Get("/$everything", h.everything)
					r.Post("/$validate", h.validateInstance) // instance-level $validate
					r.Get("/$meta", h.metaInstance)          // GET /{type}/{id}/$meta
					r.Post("/$meta-add", h.metaAdd)          // POST /{type}/{id}/$meta-add
					r.Post("/$meta-delete", h.metaDelete)    // POST /{type}/{id}/$meta-delete
					r.Get("/$document", h.document)          // GET /Composition/{id}/$document

					// Compartment search: /Patient/{id}/Observation etc.
					// Determined at runtime by checking if the URL's resourceType
					// is a known compartment type.
					r.Get("/{targetResourceType}", h.compartmentSearch)
				})
			})
		})
	}

	// Logical multi-tenancy (Option 2): the same FHIR surface is served at the
	// bare base path (→ the "default" tenant, so single-tenant deployments are
	// unaffected) and under an explicit /t/{tenant} prefix. resolveTenant places
	// the active tenant in the request context; the store turns it into the
	// Postgres app.current_tenant setting that Row-Level Security enforces.
	r.Group(func(r chi.Router) {
		r.Use(h.resolveTenant)
		mountFHIR(r)
	})
	r.Route("/t/{tenant}", func(r chi.Router) {
		r.Use(h.resolveTenant)
		mountFHIR(r)
	})

	return r
}

// tenantBaseURL returns the FHIR service base URL for the request's tenant.
// For the default tenant it is the configured base URL unchanged; for an
// explicit tenant it inserts the /t/{tenant} prefix ahead of the /fhir/r4
// path segment so that generated absolute URLs (Location headers, Bundle
// fullUrl and pagination links) address the same tenant the client used.
func (h *fhirHandler) tenantBaseURL(ctx context.Context) string {
	t := tenant.From(ctx)
	if t == tenant.Default {
		return h.baseURL
	}
	if i := strings.LastIndex(h.baseURL, "/fhir/r4"); i >= 0 {
		return h.baseURL[:i] + "/t/" + t + h.baseURL[i:]
	}
	return strings.TrimRight(h.baseURL, "/") + "/t/" + t
}

// resolveTenant derives the active tenant from the {tenant} URL segment (empty
// on the bare base path → the default tenant) and stores it in the request
// context. Malformed tenant identifiers are rejected with 404 so they can never
// reach a query.
func (h *fhirHandler) resolveTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "tenant")
		if id == "" {
			id = tenant.Default
		} else if !tenant.Valid(id) {
			operationOutcome(w, http.StatusNotFound, "error", "not-found", "unknown tenant")
			return
		}
		ctx := tenant.WithTenant(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type fhirHandler struct {
	store           StoreAPI
	pool            *pgxpool.Pool
	registry        *searchparam.Registry
	baseURL         string
	igReady         *atomic.Int32
	validateOnWrite bool // enforce profile validation on create/update when true
}
