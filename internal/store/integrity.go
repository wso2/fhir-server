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
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Referential integrity enforcement.
//
// When enabled (see RefIntegrity), the store verifies at the end of every
// write transaction — after the batched flush, inside the same transaction —
// that the final database state is referentially consistent:
//
//   - OnWrite: every local literal reference ("Type/id") carried by a
//     created/updated/patched resource must resolve to a live (non-deleted)
//     resource. Violations abort the transaction with a
//     ReferentialIntegrityError (HTTP 422).
//   - OnDelete: a resource may not be deleted while live resources still
//     reference it through an indexed reference search parameter. Violations
//     abort the transaction with a ReferencedByError (HTTP 409).
//
// Verifying after the flush makes transaction Bundles order-independent: a
// Bundle that creates both an Observation and the Patient it points at
// passes, and one that deletes a Patient while re-pointing its referrers in
// the same transaction also passes, regardless of FHIR verb ordering.
//
// Scope:
//   - Only local literal references are resolved. Absolute URLs, urn: values,
//     internal fragments ("#..."), conditional references ("Type?query") and
//     logical (identifier-only) references are never existence-checked.
//   - The delete-side check consults sp_reference, so it sees exactly the
//     references indexed by a reference-type SearchParameter.
//   - Bundle-typed resources (stored documents/collections) are exempt from
//     the write-side check: their entry-local references are resolved against
//     the bundle itself, not this server.

// RefIntegrity toggles referential-integrity enforcement on the store. The
// zero value disables both checks, preserving the store's historical behavior
// for callers that never opt in; the server wires it from config, where both
// default to on (config key `validation.referentialIntegrityOnWrite` /
// `...OnDelete`).
type RefIntegrity struct {
	OnWrite  bool // reject writes whose local references do not resolve
	OnDelete bool // reject deletes of resources that are still referenced
}

// WithReferentialIntegrity sets the referential-integrity enforcement flags
// (resolved from config).
func WithReferentialIntegrity(ri RefIntegrity) func(*Store) {
	return func(s *Store) { s.refIntegrity = ri }
}

// MissingReference is one write-side violation: a local literal reference
// whose target does not exist (or is deleted).
type MissingReference struct {
	Source    string // referencing resource, "Observation/obs-1"
	Path      string // element path carrying the reference, "Observation.subject"
	Reference string // the reference value, "Patient/p-1"
}

// ReferentialIntegrityError aborts a write whose references do not resolve.
// Mapped to HTTP 422.
type ReferentialIntegrityError struct {
	Missing []MissingReference
}

func (e ReferentialIntegrityError) Error() string {
	if len(e.Missing) == 0 {
		return "referential integrity violation"
	}
	m := e.Missing[0]
	msg := fmt.Sprintf("reference %q at %s in %s targets a resource that does not exist", m.Reference, m.Path, m.Source)
	if len(e.Missing) > 1 {
		msg += fmt.Sprintf(" (and %d more unresolved references)", len(e.Missing)-1)
	}
	return msg
}

// ReferencedByError aborts a delete whose target is still referenced by live
// resources. Mapped to HTTP 409.
type ReferencedByError struct {
	Target       string   // "Patient/p-1"
	ReferencedBy []string // sample of referrers, "Observation/obs-1 (subject)"
}

func (e ReferencedByError) Error() string {
	return fmt.Sprintf("%s cannot be deleted because it is referenced by %s; delete or update the referencing resources first, or disable validation.referentialIntegrityOnDelete",
		e.Target, strings.Join(e.ReferencedBy, ", "))
}

// pendingRef is one local literal reference collected from a written resource,
// verified against the resources table at the end of the transaction.
type pendingRef struct {
	source     string // "Observation/obs-1"
	path       string // "Observation.subject"
	ref        string // original reference value
	targetType string
	targetID   string
}

// localRefRe matches a local literal reference: "Type/id" with a FHIR id
// ([A-Za-z0-9\-\.]{1,64}) and a resource-type-shaped first segment. Anything
// else — absolute URLs, urn: values, "#" fragments, conditional references —
// is not existence-checked.
var localRefRe = regexp.MustCompile(`^[A-Z][A-Za-z]*/[A-Za-z0-9\-\.]{1,64}$`)

// collectLocalRefs walks body and returns every local literal reference it
// carries. path tracks the element location for error messages; array indices
// are omitted, matching the FHIRPath-style locations used by validation
// issues. Bundle resources are exempt (entry-local reference semantics).
func collectLocalRefs(resourceType, resourceID string, body map[string]any) []pendingRef {
	if resourceType == "Bundle" {
		return nil
	}
	var out []pendingRef
	source := resourceType + "/" + resourceID
	walkRefs(body, resourceType, source, &out)
	return out
}

func walkRefs(node any, path, source string, out *[]pendingRef) {
	switch v := node.(type) {
	case map[string]any:
		if ref, ok := v["reference"].(string); ok {
			// Versioned references ("Type/id/_history/n") resolve against the
			// current version of the target, so the suffix is stripped first.
			base := ref
			if i := strings.Index(base, "/_history/"); i >= 0 {
				base = base[:i]
			}
			if localRefRe.MatchString(base) {
				slash := strings.IndexByte(base, '/')
				*out = append(*out, pendingRef{
					source:     source,
					path:       path,
					ref:        ref,
					targetType: base[:slash],
					targetID:   base[slash+1:],
				})
			}
		}
		for k, child := range v {
			if k == "reference" {
				continue
			}
			walkRefs(child, path+"."+k, source, out)
		}
	case []any:
		for _, child := range v {
			walkRefs(child, path, source, out)
		}
	}
}

// referencedBySampleLimit bounds how many referrers a ReferencedByError names;
// the point is a actionable message, not an exhaustive listing.
const referencedBySampleLimit = 5

// recordWriteIntegrity records a written resource's final state for the
// post-flush checks: its outgoing local references (only collected when the
// write-side check is on) and the cancellation of any pending delete of the
// same id. Recording runs when either check is enabled so a resurrecting
// write clears the delete-side bookkeeping even with the write check off.
func (s *Store) recordWriteIntegrity(w *bundleWriter, resourceType, resourceID string, body map[string]any) {
	if !s.refIntegrity.OnWrite && !s.refIntegrity.OnDelete {
		return
	}
	var refs []pendingRef
	if s.refIntegrity.OnWrite {
		refs = collectLocalRefs(resourceType, resourceID, body)
	}
	w.recordWrite(resourceType, resourceID, refs)
}

// verifyIntegrity runs the enabled referential-integrity checks against the
// transaction's post-flush state. Must be called after w.flush so deferred
// resources rows and all re-index deletes are visible to the queries.
func (s *Store) verifyIntegrity(ctx context.Context, tx pgx.Tx, w *bundleWriter) error {
	if len(w.refs) > 0 {
		var refs []pendingRef
		for _, rs := range w.refs {
			refs = append(refs, rs...)
		}
		if len(refs) > 0 {
			if err := s.verifyRefsResolve(ctx, tx, refs); err != nil {
				return err
			}
		}
	}
	// Deletes are recorded whenever either flag is on (the write-side
	// bookkeeping needs them to drop a deleted resource's outgoing refs), so
	// the delete-side check must gate on its own flag here.
	if s.refIntegrity.OnDelete && len(w.deletes) > 0 {
		deletes := make([][2]string, 0, len(w.deletes))
		for _, d := range w.deletes {
			deletes = append(deletes, d)
		}
		if err := s.verifyNotReferenced(ctx, tx, deletes); err != nil {
			return err
		}
	}
	return nil
}

// verifyRefsResolve checks that every collected reference target exists as a
// live resource, in batched lookups against the resources table.
func (s *Store) verifyRefsResolve(ctx context.Context, tx pgx.Tx, refs []pendingRef) error {
	// Dedupe targets: many refs may point at the same resource.
	type target struct{ rt, id string }
	seen := make(map[target]bool, len(refs))
	var types, ids []string
	for _, r := range refs {
		t := target{r.targetType, r.targetID}
		if !seen[t] {
			seen[t] = false
			types = append(types, r.targetType)
			ids = append(ids, r.targetID)
		}
	}

	const chunk = 5000
	for lo := 0; lo < len(types); lo += chunk {
		hi := min(lo+chunk, len(types))
		rows, err := tx.Query(ctx, `
			SELECT resource_type, fhir_id FROM resources
			WHERE tenant_id = current_setting('app.current_tenant', true)
			  AND is_deleted = FALSE
			  AND (resource_type, fhir_id) IN (SELECT * FROM unnest($1::text[], $2::text[]))`,
			types[lo:hi], ids[lo:hi])
		if err != nil {
			return fmt.Errorf("referential integrity target lookup: %w", err)
		}
		for rows.Next() {
			var rt, id string
			if err := rows.Scan(&rt, &id); err != nil {
				rows.Close()
				return err
			}
			seen[target{rt, id}] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("referential integrity target lookup: %w", err)
		}
	}

	var missing []MissingReference
	for _, r := range refs {
		if !seen[target{r.targetType, r.targetID}] {
			missing = append(missing, MissingReference{Source: r.source, Path: r.path, Reference: r.ref})
		}
	}
	if len(missing) > 0 {
		return ReferentialIntegrityError{Missing: missing}
	}
	return nil
}

// verifyNotReferenced checks that no live resource still references any of the
// deleted targets. Post-flush, sp_reference holds rows for live resources
// only — rows of resources deleted or re-pointed in this transaction are
// already cleared — so any hit is a genuine live referrer.
func (s *Store) verifyNotReferenced(ctx context.Context, tx pgx.Tx, deletes [][2]string) error {
	types := make([]string, len(deletes))
	ids := make([]string, len(deletes))
	for i, d := range deletes {
		types[i], ids[i] = d[0], d[1]
	}
	rows, err := tx.Query(ctx, `
		SELECT resource_type, resource_id, param_name, target_type, target_id FROM sp_reference
		WHERE tenant_id = current_setting('app.current_tenant', true)
		  AND (target_type, target_id) IN (SELECT * FROM unnest($1::text[], $2::text[]))
		LIMIT $3`,
		types, ids, referencedBySampleLimit+1)
	if err != nil {
		return fmt.Errorf("referential integrity referrer lookup: %w", err)
	}
	defer rows.Close()

	// Report the first violated target only, with a bounded sample of its
	// referrers; hits against other deleted targets just mark the "and others"
	// suffix. One actionable violation is enough to abort the transaction.
	var target string
	var referencedBy []string
	extra := false
	for rows.Next() {
		var srcType, srcID, param, tType, tID string
		if err := rows.Scan(&srcType, &srcID, &param, &tType, &tID); err != nil {
			return err
		}
		if target == "" {
			target = tType + "/" + tID
		}
		if tType+"/"+tID != target || len(referencedBy) >= referencedBySampleLimit {
			extra = true
			continue
		}
		referencedBy = append(referencedBy, fmt.Sprintf("%s/%s (%s)", srcType, srcID, param))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("referential integrity referrer lookup: %w", err)
	}
	if len(referencedBy) == 0 {
		return nil
	}
	if extra {
		referencedBy = append(referencedBy, "and others")
	}
	return ReferencedByError{Target: target, ReferencedBy: referencedBy}
}
