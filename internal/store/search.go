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
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wso2/fhir-server/internal/searchparam"
	"github.com/wso2/fhir-server/internal/terminology"
)

// SearchParams are the parsed query parameters from the HTTP request.
type SearchParams struct {
	ResourceType string
	Params       map[string][]string // raw query params
	Page         int
	PageSize     int
	// Total is the _total mode: "none" skips the (potentially expensive) count
	// query. Any other value (including "") computes an accurate count.
	Total string
	// CountOnly is set for _summary=count: compute the total but skip fetching
	// and including the matching resources.
	CountOnly bool
}

type SearchResult struct {
	// Total is the number of matches, or -1 when not computed (_total=none).
	Total    int
	Entries  []map[string]any
	Included []map[string]any // _include / _revinclude results
}

// UnsupportedParamError is returned when a search request names a registry-known
// param whose type the query builder can't translate (composite, special).
// The HTTP layer should map this to a 400 OperationOutcome rather than execute
// a query that silently ignores the predicate.
type UnsupportedParamError struct{ Msg string }

func (e *UnsupportedParamError) Error() string { return e.Msg }

// Search executes a FHIR search against the resources + sp_* tables,
// and resolves _include / _revinclude parameters.
func (s *Store) Search(ctx context.Context, sp SearchParams) (SearchResult, error) {
	if sp.PageSize <= 0 {
		sp.PageSize = 20
	}
	if sp.Page <= 0 {
		sp.Page = 1
	}
	offset := (sp.Page - 1) * sp.PageSize

	// _list=<id>: resolve the named List resource and inject its entry IDs as
	// additional _id filters so only listed resources are returned.
	if listIDs, ok := sp.Params["_list"]; ok && len(listIDs) > 0 {
		ids, err := s.resolveListIDs(ctx, listIDs)
		if err != nil {
			return SearchResult{}, err
		}
		if len(ids) == 0 {
			return SearchResult{Total: 0}, nil
		}
		params := make(map[string][]string, len(sp.Params))
		for k, v := range sp.Params {
			if k != "_list" {
				params[k] = v
			}
		}
		// Inject as a single comma-joined value so _id's comma-OR logic applies.
		params["_id"] = []string{strings.Join(ids, ",")}
		sp.Params = params
	}

	b := &queryBuilder{rt: sp.ResourceType, reg: s.registry, terminology: s.terminology, ctx: ctx}
	b.writeBase()

	for rawKey, values := range sp.Params {
		if len(values) == 0 {
			continue
		}
		for _, v := range values {
			b.applyParam(rawKey, v)
		}
	}

	if b.err != nil {
		return SearchResult{}, b.err
	}

	// Acquire a tenant-scoped connection for the search query. _include /
	// _revinclude below resolve on their own connections (FetchReferences),
	// so this one is released as soon as the primary result set is fetched.
	c, err := s.tenantConn(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	defer c.Release()

	// _summary=count: only the total is needed, no rows to fetch.
	if sp.CountOnly {
		n, err := b.count(ctx, c)
		if err != nil {
			slog.Error("search count failed", "resourceType", sp.ResourceType, "err", err)
			return SearchResult{}, err
		}
		return SearchResult{Total: n}, nil
	}

	total := -1
	var entries []map[string]any
	if sp.Total == "none" {
		// _total=none: skip the count entirely, just fetch rows. This is the
		// default for API search requests (see handler.totalMode); an accurate
		// count is opt-in because it scans the whole match set.
		var err error
		entries, err = b.fetch(ctx, c, sp.PageSize, offset)
		if err != nil {
			slog.Error("search failed", "resourceType", sp.ResourceType, "err", err)
			return SearchResult{}, err
		}
		slog.Debug("search completed", "resourceType", sp.ResourceType, "returned", len(entries))
	} else {
		// _total=accurate|estimate (and internal callers that leave Total unset):
		// compute the exact count. fetchWithCount runs a dedicated COUNT(*) plus
		// an early-terminating page fetch.
		var err error
		total, entries, err = b.fetchWithCount(ctx, c, sp.PageSize, offset)
		if err != nil {
			slog.Error("search failed", "resourceType", sp.ResourceType, "err", err)
			return SearchResult{}, err
		}
		slog.Debug("search completed", "resourceType", sp.ResourceType, "total", total, "returned", len(entries))
	}

	result := SearchResult{Total: total, Entries: entries}

	// _include / _revinclude
	if incl := sp.Params["_include"]; len(incl) > 0 {
		included, err := s.resolveIncludes(ctx, entries, sp.ResourceType, false)
		if err != nil {
			return result, err
		}
		result.Included = append(result.Included, included...)
	}
	if rIncl := sp.Params["_revinclude"]; len(rIncl) > 0 {
		included, err := s.resolveIncludes(ctx, entries, sp.ResourceType, true)
		if err != nil {
			return result, err
		}
		result.Included = append(result.Included, included...)
	}

	return result, nil
}

// resolveListIDs fetches the List resources named by listIDs and returns the
// set of resource IDs they reference via entry[].item.reference. The returned
// IDs are the bare resource IDs (e.g. "abc-123", not "Patient/abc-123").
func (s *Store) resolveListIDs(ctx context.Context, listIDs []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, listID := range listIDs {
		list, err := s.Read(ctx, "List", listID)
		if err != nil {
			return nil, fmt.Errorf("_list: read List/%s: %w", listID, err)
		}
		entries, _ := list["entry"].([]any)
		for _, raw := range entries {
			entry, _ := raw.(map[string]any)
			if entry == nil {
				continue
			}
			item, _ := entry["item"].(map[string]any)
			if item == nil {
				continue
			}
			ref, _ := item["reference"].(string)
			if ref == "" {
				continue
			}
			// Strip resource type prefix: "Patient/123" → "123", bare "123" stays.
			if idx := strings.LastIndex(ref, "/"); idx >= 0 {
				ref = ref[idx+1:]
			}
			if ref != "" && !seen[ref] {
				seen[ref] = true
				out = append(out, ref)
			}
		}
	}
	return out, nil
}

// resolveIncludes fetches include/revinclude resources for a set of matched entries.
func (s *Store) resolveIncludes(ctx context.Context, entries []map[string]any, resourceType string, reverse bool) ([]map[string]any, error) {
	seen := make(map[string]bool)
	var results []map[string]any

	for _, entry := range entries {
		id, _ := entry["id"].(string)
		if id == "" {
			continue
		}
		refs, err := s.FetchReferences(ctx, resourceType, id, reverse)
		if err != nil {
			continue
		}
		for _, ref := range refs {
			refID, _ := ref["id"].(string)
			key := ref["resourceType"].(string) + "/" + refID
			if !seen[key] {
				seen[key] = true
				results = append(results, ref)
			}
		}
	}
	return results, nil
}

// ─── Query builder ────────────────────────────────────────────────────────────

type queryBuilder struct {
	rt          string
	reg         *searchparam.Registry
	terminology *terminology.Client // nil if no terminology server configured
	ctx         context.Context     // for terminology expansion calls
	where       strings.Builder
	args        []any
	argN        int
	// rtParam is the placeholder writeBase bound for the resource_type ($1). The
	// direct-drive fetch reuses it in its resources JOIN so that placeholder stays
	// referenced — a parameter bound but never used trips Postgres' type inference
	// (SQLSTATE 42P18), which the direct-drive shape would otherwise hit because it
	// builds its WHERE from numericBodies rather than b.where.
	rtParam string
	sort    []sortKey
	// usesSP is set when a positive value predicate against a numeric,
	// selectivity-mis-estimated sp_* table (quantity/number, or composite which
	// embeds one; see paramUsesIdFirst) is added. It selects the id-first fetch
	// strategy in fetchSQL, which avoids the full-table scan the ordered-scan plan
	// degrades to for sparse result sets on those tables.
	usesSP bool
	// predicateCount counts the top-level value predicates added (one per and()).
	// With numericTable/numericBodies it gates the direct-drive id-first fetch (see
	// fetchSQL): a search whose sole predicate (predicateCount == 1) is a numeric
	// value match can drive the candidate CTE straight off the sp_* value index.
	predicateCount int
	// numericTable / numericBodies capture the non-correlated candidate source for
	// a numeric (quantity/number) predicate — the sp_* table and the predicate on
	// alias s with the s.resource_id = r.fhir_id correlation stripped (comma-OR
	// values append one body each). When the search turns out to be a lone numeric
	// predicate sorted by a resources column, fetchSQL drives the id-first CTE off
	// these instead of scanning resources with a correlated EXISTS. Since sp_* rows
	// carry a denormalised last_updated in the covering index, both the sparse and
	// the dense case resolve index-only — no per-match resources lookup, and no
	// density probe.
	numericTable  string
	numericBodies []string
	// comp captures a composite search (e.g. code-value-quantity) as a two-table
	// drive: resolve candidate resource_ids from the selective token component's
	// sp_* table, filtered by the other component as a nested EXISTS, before
	// touching resources. Set only when the composite is the sole predicate, one
	// component is a token (a selective equality driver), and the sort maps to a
	// resources column. compositeDriveSQL uses it instead of the correlated
	// id-first CTE, which otherwise joins resources for every single-component
	// match before intersecting (measured 255ms → 88ms on the perf dataset).
	comp *compositeDrive
	// suppressDirectDrive is set while a nested value predicate is being built —
	// a composite component (buildCompositeExists), a chained target
	// (buildChainedCondition), or a _has value (applyHas). In those contexts the
	// numeric builders would otherwise capture numericTable/numericBodies, and a
	// lone such predicate would then wrongly satisfy directDrive() — emitting only
	// that embedded numeric body and dropping the surrounding structure (wrong
	// results, plus orphaned params → SQLSTATE 42P18). Capture is skipped while
	// this is set, so those searches keep the correlated id-first / single-scan
	// shape built from b.where.
	suppressDirectDrive bool
	// err is set when the request can't be satisfied (e.g. a registry-known
	// param of unsupported type like composite/special). Search() returns it
	// as an UnsupportedParamError rather than silently widening the result set.
	err error
}

// sortKey is one component of a _sort directive: the search param name and
// whether the order is descending (the param was prefixed with '-').
type sortKey struct {
	param string
	desc  bool
}

func (b *queryBuilder) next(v any) string {
	b.args = append(b.args, v)
	b.argN++
	return fmt.Sprintf("$%d", b.argN)
}

func (b *queryBuilder) writeBase() {
	rtP := b.next(b.rt)
	b.rtParam = rtP
	// Tenant scope (defence in depth alongside Row-Level Security): restrict to
	// the request's tenant via the app.current_tenant setting the store applies
	// to the connection/transaction. Holds even if the DB role bypasses RLS.
	b.where.WriteString(fmt.Sprintf(
		"r.tenant_id = current_setting('app.current_tenant', true) AND r.resource_type = %s AND r.is_deleted = FALSE", rtP,
	))
}

func (b *queryBuilder) and(cond string) {
	b.where.WriteString(" AND ")
	b.where.WriteString(cond)
	b.predicateCount++
}

func (b *queryBuilder) applyParam(rawKey, value string) {
	paramName, modifier := splitModifier(rawKey)

	// _has reverse chaining: _has:SourceType:refParam:valueParam=value
	// "give me resources of b.rt referenced by a SourceType resource via refParam
	// whose valueParam matches value".
	if paramName == "_has" {
		b.applyHas(modifier, value)
		return
	}

	// Chained search: organization.name=… or subject:Patient.name=… — a dot in
	// the param name (or in a type modifier) walks a reference to the target
	// resource's own search params.
	if ref, targetType, targetParam, targetMod, ok := parseChain(paramName, modifier); ok {
		b.applyChained(ref, targetType, targetParam, targetMod, value)
		return
	}

	switch paramName {
	case "_id":
		parts := strings.Split(value, ",")
		if len(parts) == 1 {
			p := b.next(strings.TrimSpace(value))
			b.and(fmt.Sprintf("r.fhir_id = %s", p))
		} else {
			var ors []string
			for _, part := range parts {
				p := b.next(strings.TrimSpace(part))
				ors = append(ors, fmt.Sprintf("r.fhir_id = %s", p))
			}
			b.and("(" + strings.Join(ors, " OR ") + ")")
		}
	case "_lastUpdated":
		b.applyLastUpdated(value)
	case "_text", "_content":
		p := b.next(value)
		b.and(fmt.Sprintf("r.search_text @@ plainto_tsquery('english', %s)", p))
	case "_sort":
		b.addSort(value)
	case "_filter":
		b.applyFilter(value)
	case "_count", "_page", "_include", "_revinclude", "_format", "_summary", "_elements", "_total", "_list":
		// control params — handled at the HTTP layer
	default:
		b.applySearchParam(paramName, modifier, value)
	}
}

func (b *queryBuilder) applySearchParam(param, modifier, value string) {
	if modifier == "missing" {
		// :missing must look in the table the param was actually indexed into,
		// otherwise typed params (reference, token, date, quantity, uri) are all
		// reported as missing. Fall back to sp_string for params the registry
		// doesn't know.
		table := "sp_string"
		if pt, ok := universalParamType[param]; ok {
			table = tableForType(pt)
		} else if b.reg != nil {
			if def, ok := b.reg.Lookup(b.rt, param); ok {
				t := tableForType(def.ParamType)
				if t == "" {
					// composite / special — we can't tell from registry alone where
					// this param indexes, so :missing is unanswerable. Fail closed
					// rather than skipping the predicate (which widens the result
					// set unexpectedly).
					b.err = &UnsupportedParamError{Msg: fmt.Sprintf("param %q on %s has type %q which is not yet supported for :missing", param, b.rt, def.ParamType)}
					return
				}
				table = t
			}
		}
		exists := b.spExists(table, param, "")
		if value == "true" {
			b.and(fmt.Sprintf("NOT EXISTS (%s)", exists))
		} else {
			b.and(fmt.Sprintf("EXISTS (%s)", exists))
		}
		return
	}

	// :not negates the match. :not-in is also negated (handled in buildTypedExists
	// but needs to be NOT EXISTS at the applyParam level).
	// suppressDirectDrive around the negated branches: a :not / :not-in predicate
	// is a NOT EXISTS over the positive body, not a bare value match, so the inner
	// numeric builder must not register it as a direct-drive candidate (which would
	// let a lone negated numeric search drive the id-first fetch off the *positive*
	// predicate). Mirrors applyHas / buildCompositeExists / buildChainedCondition.
	if modifier == "not" {
		prev := b.suppressDirectDrive
		b.suppressDirectDrive = true
		expr := b.combinedExists(param, "", value)
		b.suppressDirectDrive = prev
		if expr != "" {
			b.and("NOT " + expr)
		}
		return
	}
	if modifier == "not-in" {
		prev := b.suppressDirectDrive
		b.suppressDirectDrive = true
		expr := b.combinedExists(param, "not-in", value)
		b.suppressDirectDrive = prev
		if expr != "" {
			b.and("NOT " + expr)
		}
		return
	}

	if expr := b.combinedExists(param, modifier, value); expr != "" {
		b.and(expr)
		// Route this search through the id-first fetch strategy only for the
		// numeric sp_* predicates the planner mis-estimates (quantity/number, and
		// composite which embeds one — see idFirstType). token, reference, date,
		// string and uri params keep the early-terminating ordered scan, where the
		// planner has good enough statistics to pick the right plan itself.
		if b.paramUsesIdFirst(param) {
			b.usesSP = true
		}
	}
}

// applyHas implements _has reverse chaining.
// rawKey form: "_has:SourceType:refParam:valueParam", value = search value.
// The modifier contains "SourceType:refParam:valueParam".
// Result: add a predicate that the current resource is referenced by a
// SourceType resource (via refParam) that also satisfies valueParam=value.
func (b *queryBuilder) applyHas(modifier, value string) {
	parts := strings.SplitN(modifier, ":", 3)
	if len(parts) != 3 {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("_has modifier must be SourceType:refParam:valueParam, got %q", modifier)}
		return
	}
	sourceType, refParam, valueParam := parts[0], parts[1], parts[2]

	// Build the inner predicate for valueParam=value on sourceType, shadowing
	// the outer 'r' alias with the source resource row. suppressDirectDrive so a
	// numeric valueParam is not mistaken for a direct-drive candidate (this is a
	// reverse-chained predicate on the source type, not a bare value match).
	saved := b.rt
	b.rt = sourceType
	prevSuppress := b.suppressDirectDrive
	b.suppressDirectDrive = true
	inner := b.combinedExists(valueParam, "", value)
	b.suppressDirectDrive = prevSuppress
	b.rt = saved
	if inner == "" {
		return
	}

	rtP := b.next(b.rt)
	srcP := b.next(sourceType)
	refP := b.next(refParam)

	// The source resource references the current resource via sp_reference.
	b.and(fmt.Sprintf(
		"EXISTS (SELECT 1 FROM sp_reference sr WHERE sr.target_id = r.fhir_id AND sr.target_type = %s AND sr.resource_type = %s AND sr.param_name = %s AND EXISTS (SELECT 1 FROM resources r WHERE r.fhir_id = sr.resource_id AND r.resource_type = %s AND r.is_deleted = FALSE AND %s))",
		rtP, srcP, refP, srcP, inner,
	))
}

// buildCompositeExists builds a nested-EXISTS predicate for a composite search
// param (e.g. code-value-quantity=8480-6$gt110): the resource must have both
// component values (matched independently, correlated on resource_id).
// The value is split on "$" to get the two component values. Each component's
// expression maps to a sub-param name in the registry.
func (b *queryBuilder) buildCompositeExists(def searchparam.Definition, param, value string) (string, bool) {
	if len(def.Components) < 2 {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("composite param %q has no component definitions — cannot execute", param)}
		return "", false
	}
	// Split on "$" to separate component values. Only two-component composites
	// are supported.
	dollarIdx := strings.IndexByte(value, '$')
	if dollarIdx < 0 {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("composite param %q value %q must contain '$' separating the two component values", param, value)}
		return "", false
	}
	val1, val2 := value[:dollarIdx], value[dollarIdx+1:]

	// Resolve component expressions to param names in the registry.
	comp1Name := resolveComponentName(b.rt, def.Components[0].Expression, b.reg)
	comp2Name := resolveComponentName(b.rt, def.Components[1].Expression, b.reg)
	if comp1Name == "" || comp2Name == "" {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("composite param %q: cannot resolve component params from expressions %q / %q", param, def.Components[0].Expression, def.Components[1].Expression)}
		return "", false
	}

	// Token+quantity composites route to the dedicated single table
	// sp_composite_token_quantity, where the code equality and value range live in
	// one index scan (see buildCompositeTokenQuantityExists). The materialised rows
	// pair the two components per element, so this is also the spec-correct path:
	// the legacy fall-through below correlates the components only on resource_id,
	// which false-matches multi-component resources. Other component-type pairs keep
	// the legacy path.
	type1 := b.resolvedParamType(comp1Name)
	type2 := b.resolvedParamType(comp2Name)
	if isTokenQuantityPair(type1, type2) {
		tokVal, qtyVal := val1, val2
		if type1 == "quantity" { // components in (quantity, token) order
			tokVal, qtyVal = val2, val1
		}
		return b.buildCompositeTokenQuantityExists(param, tokVal, qtyVal)
	}

	// Build the two component SELECT bodies. Each is a fully-formed
	// "SELECT 1 FROM sp_<type> s WHERE s.resource_id = r.fhir_id AND …"
	// correlated to the outer resource row. suppressDirectDrive stops a numeric
	// component from being captured as a direct-drive candidate: the composite's
	// full predicate is both components, so it must keep the correlated id-first shape.
	prev := b.suppressDirectDrive
	b.suppressDirectDrive = true
	cond1, ok1 := b.buildExistsForValue(comp1Name, "", val1)
	cond2, ok2 := b.buildExistsForValue(comp2Name, "", val2)
	b.suppressDirectDrive = prev
	if !ok1 || !ok2 || cond1 == "" || cond2 == "" {
		return "", false
	}
	// At the top level (not nested inside a chained/_has/composite context),
	// capture a two-table drive so fetchSQL can resolve candidates from the
	// selective token component instead of the resources recency-walk / correlated
	// CTE. Nested composites keep the correlated shape (prev == true).
	if !prev {
		b.captureCompositeDrive(comp1Name, cond1, comp2Name, cond2)
	}
	// Nest cond2 as a correlated EXISTS inside cond1's WHERE. The caller
	// (combinedExists) wraps our return value in a single EXISTS(...), producing
	//   EXISTS (cond1 … AND EXISTS (cond2 …))
	// Both components are matched independently and correlated only on
	// resource_id, so the resource matches when it has both component values.
	// This nested-EXISTS shape is flattenable: Postgres turns it into two
	// semi-joins driven by the more selective component's sp_* index, rather
	// than a correlated subplan run once per candidate resource row.
	return cond1 + " AND EXISTS (" + cond2 + ")", true
}

// isTokenQuantityPair reports whether two component types are exactly one token
// and one quantity (in either order) — the pair materialised into
// sp_composite_token_quantity.
func isTokenQuantityPair(t1, t2 string) bool {
	return (t1 == "token" && t2 == "quantity") || (t1 == "quantity" && t2 == "token")
}

// buildCompositeTokenQuantityExists builds the correlated EXISTS body for a
// token+quantity composite against the single sp_composite_token_quantity table,
// where each row is a per-element token+quantity pairing. tokVal is the token
// component value ("system|code" or bare code); qtyVal is the quantity component
// value ("[prefix]number|system|unit"). Placeholders are appended to b.args in
// left-to-right order so the same body serves both the WHERE/count form and the
// direct-drive candidate scan (captured below), exactly as buildQuantityExists
// does for sp_quantity.
func (b *queryBuilder) buildCompositeTokenQuantityExists(param, tokVal, qtyVal string) (string, bool) {
	rtP := b.next(b.rt)
	pP := b.next(param)

	// Token component: system|code, bare system|, |code, or bare code. code is
	// NOT NULL in the table, so a bare code always resolves to an equality.
	var tokCond string
	if parts := strings.SplitN(tokVal, "|", 2); len(parts) == 2 {
		sys, code := parts[0], parts[1]
		switch {
		case sys != "" && code != "":
			sP := b.next(sys)
			cP := b.next(code)
			tokCond = fmt.Sprintf("s.system = %s AND s.code = %s", sP, cP)
		case sys != "":
			tokCond = fmt.Sprintf("s.system = %s", b.next(sys))
		default:
			tokCond = fmt.Sprintf("s.code = %s", b.next(code))
		}
	} else {
		tokCond = fmt.Sprintf("s.code = %s", b.next(tokVal))
	}

	qtyCond := b.compositeQtyCond(qtyVal)
	if b.err != nil {
		return "", false
	}

	body := fmt.Sprintf("s.resource_type = %s AND s.param_name = %s AND %s AND %s", rtP, pP, tokCond, qtyCond)

	// Capture the correlation-free body so fetchSQL can drive the id-first
	// candidate scan straight off sp_composite_token_quantity (planner picks the
	// value-driven code_value index or the recency walk). Skipped inside a nested
	// context (chained / _has), whose full predicate is more than this one body.
	if !b.suppressDirectDrive {
		b.numericTable = "sp_composite_token_quantity"
		b.numericBodies = append(b.numericBodies, body)
	}
	return "SELECT 1 FROM sp_composite_token_quantity s WHERE s.resource_id = r.fhir_id AND " + body, true
}

// compositeQtyCond builds the quantity-component predicate on the
// sp_composite_token_quantity value columns. It mirrors buildQuantityExists
// byte-for-byte for the numrange overlap (eq/ne/ge/le) and scalar (gt/lt) forms
// so the emitted "numrange(s.value_low, s.value_high, '[]')" matches
// idx_sp_comp_tokqty_range_gist exactly and the planner can use it. A unit-scoped
// search ("…|system|unit") adds qty_system / qty_code equality, resolved from the
// index INCLUDE columns without a heap fetch.
func (b *queryBuilder) compositeQtyCond(value string) string {
	numPart := value
	var qsys, qcode string
	if parts := strings.SplitN(value, "|", 3); len(parts) > 1 {
		numPart = parts[0]
		qsys = parts[1]
		if len(parts) == 3 {
			qcode = parts[2]
		}
	}
	prefix, numStr := extractComparatorPrefix(numPart)
	// Fail closed on a non-numeric or non-finite value rather than centring the
	// range on zero (which would silently match value≈0 rows). Validate before
	// binding any placeholder so nothing is left orphaned.
	f, err := strconv.ParseFloat(numStr, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("composite quantity component %q must be a finite number", value)}
		return ""
	}
	eps := math.Abs(f) * 1e-7
	if eps == 0 {
		eps = 1e-7
	}
	low, high := f-eps, f+eps

	const stored = "numrange(s.value_low, s.value_high, '[]')"
	var cond string
	switch prefix {
	case "gt":
		cond = fmt.Sprintf("s.value_low > %s", b.next(high))
	case "lt":
		cond = fmt.Sprintf("s.value_high < %s", b.next(low))
	case "ge":
		cond = fmt.Sprintf("%s && numrange(%s, NULL, '[]')", stored, b.next(low))
	case "le":
		cond = fmt.Sprintf("%s && numrange(NULL, %s, '[]')", stored, b.next(high))
	case "ne":
		lP := b.next(low)
		hP := b.next(high)
		cond = fmt.Sprintf("NOT (%s && numrange(%s, %s, '[]'))", stored, lP, hP)
	default: // eq
		lP := b.next(low)
		hP := b.next(high)
		cond = fmt.Sprintf("%s && numrange(%s, %s, '[]')", stored, lP, hP)
	}
	if qsys != "" {
		cond += fmt.Sprintf(" AND s.qty_system = %s", b.next(qsys))
	}
	if qcode != "" {
		cond += fmt.Sprintf(" AND s.qty_code = %s", b.next(qcode))
	}
	return cond
}

// compositeDrive holds a composite search decomposed into a candidate driver
// (the selective token component's sp_* table) and a filter (the other
// component, applied as a nested EXISTS correlated to the driver row). Both
// bodies are correlation-free WHERE fragments; driverBody references alias s and
// filterBody references alias s2. The $N placeholders they contain were bound
// while the components were built, so compositeDriveSQL reuses them verbatim.
type compositeDrive struct {
	driverTable string
	driverBody  string
	filterTable string
	filterBody  string
}

// captureCompositeDrive records a two-table drive for the composite when one
// component is a token (a selective equality driver). cond1/cond2 are the
// fully-formed correlated component subqueries. If neither component is a token,
// or either body is too complex to safely re-alias, capture is skipped and the
// search keeps the correlated id-first CTE.
func (b *queryBuilder) captureCompositeDrive(name1, cond1, name2, cond2 string) {
	t1, body1, ok1 := splitSimpleExists(cond1)
	t2, body2, ok2 := splitSimpleExists(cond2)
	if !ok1 || !ok2 {
		return
	}
	tok1 := b.resolvedParamType(name1) == "token"
	tok2 := b.resolvedParamType(name2) == "token"
	var drvTable, drvBody, fltTable, fltBody string
	switch {
	case tok1:
		drvTable, drvBody, fltTable, fltBody = t1, body1, t2, body2
	case tok2:
		drvTable, drvBody, fltTable, fltBody = t2, body2, t1, body1
	default:
		return // no selective equality driver — keep the correlated id-first CTE
	}
	b.comp = &compositeDrive{
		driverTable: drvTable,
		driverBody:  drvBody,
		filterTable: fltTable,
		// The filter runs as a nested EXISTS under alias s2 correlated to the
		// driver's resource_id; re-alias its column references from s to s2. The
		// body only ever references s.<column> (never a literal "s." — values are
		// $N placeholders), so a plain replace is safe.
		filterBody: strings.ReplaceAll(fltBody, "s.", "s2."),
	}
}

// splitSimpleExists decomposes a component subquery of the exact shape
// "SELECT 1 FROM <table> s WHERE s.resource_id = r.fhir_id AND <body>" into its
// table and correlation-free body. Returns ok=false for any other shape (token
// :in / heuristic UNION, terminology hierarchy, etc.) that embeds a further
// SELECT and cannot be safely re-aliased into a driver row.
func splitSimpleExists(cond string) (table, body string, ok bool) {
	const pfx = "SELECT 1 FROM "
	const mid = " s WHERE s.resource_id = r.fhir_id AND "
	if !strings.HasPrefix(cond, pfx) {
		return "", "", false
	}
	rest := cond[len(pfx):]
	i := strings.Index(rest, mid)
	if i < 0 {
		return "", "", false
	}
	table = rest[:i]
	body = rest[i+len(mid):]
	if strings.Contains(body, "SELECT") || strings.Contains(body, "UNION") {
		return "", "", false
	}
	return table, body, true
}

// resolveComponentName converts a component expression like "code",
// "value.as(Quantity)", or "interpretation" to the search param name
// registered in the registry for the given resource type.
// It tries: exact match, then strips ".as(Type)" suffix to get the base field,
// then looks for a param whose FHIRPath starts with the expression.
func resolveComponentName(rt, expr string, reg *searchparam.Registry) string {
	if expr == "" || reg == nil {
		return ""
	}
	// Exact match by FHIRPath.
	for _, d := range reg.ForResource(rt) {
		if d.FHIRPath == expr {
			return d.ParamName
		}
	}

	// Extract the type hint from "value.as(Quantity)" → typeHint="Quantity"
	// and the base field: "value".
	typeHint := ""
	plain := expr
	if i := strings.Index(plain, ".as("); i >= 0 {
		end := strings.IndexByte(plain[i:], ')')
		if end >= 0 {
			typeHint = plain[i+4 : i+end]
		}
		plain = plain[:i]
	}
	// Strip leading resource-type prefix: "Observation.code" → "code".
	if dot := strings.IndexByte(plain, '.'); dot >= 0 {
		plain = plain[dot+1:]
	}
	if plain == "" {
		plain = expr
	}

	// Type hint → expected search param type mapping.
	expectedType := ""
	switch typeHint {
	case "Quantity", "SampledData":
		expectedType = "quantity"
	case "CodeableConcept":
		expectedType = "token"
	case "dateTime", "Period", "Date", "Instant":
		expectedType = "date"
	case "string", "string+":
		expectedType = "string"
	case "Reference":
		expectedType = "reference"
	}

	// 1. Exact name match (possibly filtered by type hint).
	for _, d := range reg.ForResource(rt) {
		if d.ParamName == plain {
			if expectedType == "" || d.ParamType == expectedType {
				return d.ParamName
			}
		}
	}
	// 2. FHIRPath contains the plain segment, filtered by type hint if available.
	for _, d := range reg.ForResource(rt) {
		pathMatch := strings.Contains(d.FHIRPath, "."+plain+".") ||
			strings.HasSuffix(d.FHIRPath, "."+plain) ||
			strings.HasPrefix(d.FHIRPath, plain+".") ||
			d.FHIRPath == plain
		if pathMatch {
			if expectedType == "" || d.ParamType == expectedType {
				return d.ParamName
			}
		}
	}
	return ""
}

// parseChain detects a chained-search parameter and splits it into the
// reference param on the current resource, an optional explicit target type,
// and the search param on the target resource. Two forms are recognised:
//
//	organization.name      → ref=organization, type="",      target=name   (modifier applies to target)
//	subject:Patient.name   → ref=subject,      type=Patient, target=name
//
// Returns ok=false when the key is not a chain.
func parseChain(paramName, modifier string) (ref, targetType, targetParam, targetModifier string, ok bool) {
	if i := strings.IndexByte(paramName, '.'); i >= 0 {
		return paramName[:i], "", paramName[i+1:], modifier, true
	}
	if i := strings.IndexByte(modifier, '.'); i >= 0 {
		return paramName, modifier[:i], modifier[i+1:], "", true
	}
	return "", "", "", "", false
}

// applyChained builds the predicate for a single-hop chained search: the
// resource has a `ref` reference to a `targetType` resource that itself matches
// `targetParam`=value. The inner match reuses the normal value builders by
// shadowing the `r` alias with the target resource inside an IN-subquery.
func (b *queryBuilder) applyChained(ref, targetType, targetParam, targetModifier, value string) {
	cond := b.buildChainedCondition(b.rt, ref, targetType, targetParam, targetModifier, value, 0)
	if cond != "" {
		b.and(cond)
	}
}

const maxChainDepth = 5 // prevent pathological queries

// buildChainedCondition builds the EXISTS…IN SQL fragment for one hop of a
// chained search, recursing for multi-hop chains.
// sourceType is the type of the resource at the current hop (the one we're
// filtering by the sp_reference table).
func (b *queryBuilder) buildChainedCondition(sourceType, ref, targetType, targetParam, targetModifier, value string, depth int) string {
	if depth > maxChainDepth {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("chained search exceeds maximum depth %d", maxChainDepth)}
		return ""
	}

	// Resolve the target type for this hop.
	if targetType == "" {
		guess := strings.ToUpper(ref[:1]) + ref[1:]
		if b.reg != nil && len(b.reg.ForResource(guess)) > 0 {
			targetType = guess
		}
	}
	if targetType == "" {
		// Try to infer from the registry Targets of the ref param.
		if b.reg != nil {
			if def, ok := b.reg.Lookup(sourceType, ref); ok && len(def.Targets) == 1 {
				targetType = def.Targets[0]
			}
		}
	}
	if targetType == "" {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("chained search: cannot infer target type for %s.%s — use explicit Type, e.g. %s:Type.%s", sourceType, ref, ref, targetParam)}
		return ""
	}

	refP := b.next(ref)
	stP := b.next(sourceType)
	ttP := b.next(targetType)

	// If targetParam still contains a dot, this is a further hop.
	if dot := strings.IndexByte(targetParam, '.'); dot >= 0 {
		nextRef := targetParam[:dot]
		rest := targetParam[dot+1:]
		// Determine next explicit type from the modifier if present.
		nextType, nextParam := "", rest
		if i := strings.IndexByte(rest, '.'); i >= 0 {
			// rest could be "nextType.finalParam" if we were given explicit types
			// but that's handled by the outer parseChain — here rest is just the
			// remaining chain without type qualifiers.
			_ = i
		}
		inner := b.buildChainedCondition(targetType, nextRef, nextType, nextParam, targetModifier, value, depth+1)
		if inner == "" {
			return ""
		}
		return fmt.Sprintf(
			"EXISTS (SELECT 1 FROM sp_reference sr WHERE sr.resource_id = r.fhir_id AND sr.resource_type = %s AND sr.param_name = %s AND sr.target_type = %s AND sr.target_id IN (SELECT r.fhir_id FROM resources r WHERE r.is_deleted = FALSE AND r.resource_type = %s AND %s))",
			stP, refP, ttP, ttP, inner,
		)
	}

	// Leaf hop: build the value predicate on the final target type.
	// suppressDirectDrive so a numeric targetParam is not captured as a direct-drive
	// candidate — it is a chained predicate on the target type, not a bare match.
	saved := b.rt
	b.rt = targetType
	prevSuppress := b.suppressDirectDrive
	b.suppressDirectDrive = true
	inner := b.combinedExists(targetParam, targetModifier, value)
	b.suppressDirectDrive = prevSuppress
	b.rt = saved
	if inner == "" {
		return ""
	}

	return fmt.Sprintf(
		"EXISTS (SELECT 1 FROM sp_reference sr WHERE sr.resource_id = r.fhir_id AND sr.resource_type = %s AND sr.param_name = %s AND sr.target_type = %s AND sr.target_id IN (SELECT r.fhir_id FROM resources r WHERE r.is_deleted = FALSE AND r.resource_type = %s AND %s))",
		stP, refP, ttP, ttP, inner,
	)
}

// combinedExists builds the EXISTS predicate for a (possibly comma-separated)
// value, OR-joining the parts. Returns "" when no part produced a condition.
func (b *queryBuilder) combinedExists(param, modifier, value string) string {
	// Multi-value reference searches (comma = logical OR): coalesce the targets
	// into a single EXISTS with target_id = ANY(...) rather than OR-ing separate
	// EXISTS subqueries. A lone EXISTS lets Postgres invert the plan and drive
	// from the selective sp_reference target index; OR-of-EXISTS defeats that
	// inversion and collapses to a full recency-walk over resources — measured on
	// the perf dataset at 360ms / 672k buffers for an empty match set, vs
	// 0.1ms / 19 buffers for the ANY form. :identifier is excluded (it matches on
	// identifier_system/value, not target_id).
	if modifier != "identifier" && strings.IndexByte(value, ',') >= 0 && b.resolvedParamType(param) == "reference" {
		if expr, ok := b.buildReferenceAnyExists(param, modifier, value); ok {
			return expr
		}
		// Fall through to the generic OR form if the values could not be coalesced.
	}
	var ors []string
	for _, p := range strings.Split(value, ",") {
		cond, ok := b.buildExistsForValue(param, modifier, strings.TrimSpace(p))
		if ok {
			ors = append(ors, fmt.Sprintf("EXISTS (%s)", cond))
		}
	}
	switch len(ors) {
	case 0:
		return ""
	case 1:
		return ors[0]
	default:
		return "(" + strings.Join(ors, " OR ") + ")"
	}
}

// resolvedParamType returns the FHIR search param type for param on the current
// resource type, resolving universal meta params first, then the registry.
// Returns "" when the type is unknown (heuristic path).
func (b *queryBuilder) resolvedParamType(param string) string {
	if pt, ok := universalParamType[param]; ok {
		return pt
	}
	if b.reg != nil {
		if def, ok := b.reg.Lookup(b.rt, param); ok {
			return def.ParamType
		}
	}
	return ""
}

// buildReferenceAnyExists builds a single EXISTS predicate for a multi-value
// reference search, matching any of the comma-separated targets via
// target_id = ANY(...). Values are grouped by target_type so a mixed-type list
// (e.g. subject=Patient/1,Group/2) stays correct; each group contributes one
// ANY clause OR-ed inside the single EXISTS. Bare ids (no Type/ prefix and no
// type modifier) form an untyped group. Returns ok=false if any value has no
// id, so the caller falls back to the generic OR-of-EXISTS form.
func (b *queryBuilder) buildReferenceAnyExists(param, modifier, value string) (string, bool) {
	rtP := b.next(b.rt)
	pP := b.next(param)

	typed := map[string][]string{} // target_type -> ids
	var bare []string              // ids with no explicit type
	for _, part := range strings.Split(value, ",") {
		typ, id := parseSearchReference(strings.TrimSpace(part))
		if typ == "" && modifier != "" {
			// e.g. subject:Patient=1,2 — the modifier names the target type.
			typ = modifier
		}
		if id == "" {
			return "", false
		}
		if typ != "" {
			typed[typ] = append(typed[typ], id)
		} else {
			bare = append(bare, id)
		}
	}

	var clauses []string
	types := make([]string, 0, len(typed))
	for t := range typed {
		types = append(types, t)
	}
	sort.Strings(types) // deterministic SQL for stable plans and tests
	for _, typ := range types {
		tP := b.next(typ)
		idsP := b.next(typed[typ])
		clauses = append(clauses, fmt.Sprintf("(s.target_type = %s AND s.target_id = ANY(%s))", tP, idsP))
	}
	if len(bare) > 0 {
		idsP := b.next(bare)
		clauses = append(clauses, fmt.Sprintf("s.target_id = ANY(%s)", idsP))
	}
	if len(clauses) == 0 {
		return "", false
	}
	targetClause := clauses[0]
	if len(clauses) > 1 {
		targetClause = "(" + strings.Join(clauses, " OR ") + ")"
	}
	body := fmt.Sprintf(
		"SELECT 1 FROM sp_reference s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND %s",
		rtP, pP, targetClause,
	)
	return "EXISTS (" + body + ")", true
}

// buildExistsForValue builds an EXISTS subquery for a single value, routing to
// the correct sp_* table by the param's declared type in the registry. When the
// param is unknown to the registry (e.g. a custom param not yet loaded) it falls
// back to a best-effort guess from the value format.
func (b *queryBuilder) buildExistsForValue(param, modifier, value string) (string, bool) {
	// Universal meta params have a fixed type and aren't in the per-resource
	// registry; resolve them first so they route to the right sp_* table.
	if pt, ok := universalParamType[param]; ok {
		return b.buildTypedExists(searchparam.Definition{ParamType: pt, ParamName: param}, param, modifier, value)
	}
	if b.reg != nil {
		if def, ok := b.reg.Lookup(b.rt, param); ok {
			return b.buildTypedExists(def, param, modifier, value)
		}
	}
	return b.buildHeuristicExists(param, modifier, value)
}

// universalParamType maps the meta.* search params (indexed for every resource
// type by index.indexMeta) to their FHIR search param type. _id/_lastUpdated/
// _text/_content are handled separately in applyParam.
var universalParamType = map[string]string{
	"_tag":      "token",
	"_security": "token",
	"_profile":  "uri",
	"_source":   "uri",
	"_language": "token",
}

// buildTypedExists routes a value match to the sp_* table for the given FHIR
// search param type. Returns (subquery, false) for types we don't yet support
// (composite, special) so the caller can skip the filter rather than misroute it.
func (b *queryBuilder) buildTypedExists(def searchparam.Definition, param, modifier, value string) (string, bool) {
	paramType := def.ParamType
	switch paramType {
	case "composite":
		sub, ok := b.buildCompositeExists(def, param, value)
		return sub, ok
	case "string":
		return b.buildStringExists(param, modifier, value), true
	case "token":
		// :in/:not-in expand a ValueSet via the terminology server.
		// :of-type matches Identifier.type + value (indexed under <param>:of-type).
		// :above/:below need code-system subsumption (terminology).
		switch modifier {
		case "in", "not-in":
			return b.buildTokenInExists(param, modifier, value)
		case "of-type":
			return b.buildOfTypeExists(param, value), true
		case "above", "below":
			return b.buildTokenHierarchyExists(param, modifier, value)
		}
		return b.buildTokenExists(param, modifier, value), true
	case "date", "dateTime", "instant", "Period":
		return b.buildDateExists(param, value), true
	case "number":
		return b.buildNumberExists(param, value), true
	case "quantity":
		return b.buildQuantityExists(param, value), true
	case "uri":
		return b.buildURIExists(param, modifier, value), true
	case "reference":
		return b.buildReferenceExists(param, modifier, value), true
	default:
		// special (e.g. Location.near) — not supported. Fail closed rather
		// than silently dropping the predicate (which would broaden results).
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("param %q on %s has type %q which is not yet supported", param, b.rt, paramType)}
		slog.Warn("unsupported search param type; failing request",
			"resourceType", b.rt, "param", param, "paramType", paramType)
		return "", false
	}
}

// paramUsesIdFirst reports whether a positive value predicate on this search
// param should select the id-first fetch strategy (see fetchSQL). It is enabled
// only for the numeric, selectivity-mis-estimated params — quantity, number, and
// composites built from them (see idFirstType) — where the ordered-scan plan
// collapses to a full-table scan-with-probe when the result set is sparse or
// empty (the source of the multi-second query tail on the perf dataset).
//
// token is deliberately excluded (idFirstType): its equality has reliable MCV
// stats, so the planner already resolves id-first for selective/empty codes and
// takes the ordered early-exit for dense ones. reference is excluded too —
// Postgres already plans it id-first from the sp_reference target index — as are
// date, string and uri, which live in small tables where a full scan is cheap.
func (b *queryBuilder) paramUsesIdFirst(param string) bool {
	if pt, ok := universalParamType[param]; ok {
		return idFirstType(pt)
	}
	if b.reg != nil {
		if def, ok := b.reg.Lookup(b.rt, param); ok {
			return idFirstType(def.ParamType)
		}
	}
	return false
}

func idFirstType(paramType string) bool {
	switch paramType {
	// quantity/number range predicates (and composite, which embeds one) are
	// mis-estimated by the planner — it cannot judge the selectivity of an
	// unbounded numeric range, so without the id-first barrier it may choose the
	// ordered scan and walk the whole table for a sparse match set. token is
	// deliberately NOT here: token equality has reliable MCV statistics, so the
	// planner already resolves id-first for selective/empty codes and takes the
	// ordered early-exit for dense ones. Forcing id-first on tokens instead
	// pessimises the common dense case (e.g. a category code matching most rows),
	// which then has to materialize and sort the entire match set.
	case "quantity", "number", "composite":
		return true
	default:
		return false
	}
}

// tableForType maps a FHIR search param type to its sp_* index table. Returns ""
// for types without a dedicated table (composite, special).
func tableForType(paramType string) string {
	switch paramType {
	case "string":
		return "sp_string"
	case "token":
		return "sp_token"
	case "date", "dateTime", "instant", "Period":
		return "sp_date"
	case "number":
		return "sp_number"
	case "quantity":
		return "sp_quantity"
	case "uri":
		return "sp_uri"
	case "reference":
		return "sp_reference"
	default:
		return ""
	}
}

// buildHeuristicExists is the legacy value-format guess, used only when the
// param type is unknown. Reference/quantity/uri params are never reachable here
// once the registry is loaded.
func (b *queryBuilder) buildHeuristicExists(param, modifier, value string) (string, bool) {
	switch {
	case looksLikeDate(value):
		return b.buildDateExists(param, value), true
	case looksLikeNumber(value):
		return b.buildNumberExists(param, value), true
	case strings.Contains(value, "|"):
		return b.buildTokenExists(param, modifier, value), true
	default:
		// Match against both sp_string and sp_token so plain-code token searches
		// (e.g. gender=female) work alongside string params.
		strQ := b.buildStringExists(param, modifier, value)
		tokQ := b.buildTokenExists(param, modifier, value)
		return strQ + " UNION ALL " + tokQ, true
	}
}

func (b *queryBuilder) buildStringExists(param, modifier, value string) string {
	rtP := b.next(b.rt)
	pP := b.next(param)
	switch modifier {
	case "exact":
		vP := b.next(value)
		return fmt.Sprintf("SELECT 1 FROM sp_string s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.value_exact = %s", rtP, pP, vP)
	case "contains":
		vP := b.next("%" + strings.ToLower(value) + "%")
		return fmt.Sprintf("SELECT 1 FROM sp_string s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.value_lower LIKE %s", rtP, pP, vP)
	default:
		vP := b.next(strings.ToLower(value) + "%")
		return fmt.Sprintf("SELECT 1 FROM sp_string s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.value_lower LIKE %s", rtP, pP, vP)
	}
}

func (b *queryBuilder) buildTokenExists(param, modifier, value string) string {
	rtP := b.next(b.rt)
	pP := b.next(param)
	// :text matches the human-readable display/text of the token (case-insensitive
	// substring), not its code.
	if modifier == "text" {
		vP := b.next("%" + strings.ToLower(value) + "%")
		return fmt.Sprintf("SELECT 1 FROM sp_token s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND LOWER(s.display) LIKE %s", rtP, pP, vP)
	}
	parts := strings.SplitN(value, "|", 2)
	if len(parts) == 2 {
		sys, code := parts[0], parts[1]
		if sys == "" {
			cP := b.next(code)
			return fmt.Sprintf("SELECT 1 FROM sp_token s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.code = %s", rtP, pP, cP)
		}
		if code == "" {
			sP := b.next(sys)
			return fmt.Sprintf("SELECT 1 FROM sp_token s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.system = %s", rtP, pP, sP)
		}
		sP := b.next(sys)
		cP := b.next(code)
		return fmt.Sprintf("SELECT 1 FROM sp_token s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.system = %s AND s.code = %s", rtP, pP, sP, cP)
	}
	vP := b.next(value)
	return fmt.Sprintf("SELECT 1 FROM sp_token s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.code = %s", rtP, pP, vP)
}

// buildTokenInExists expands a ValueSet URL and builds an IN/NOT IN subquery
// against sp_token. Requires b.terminology to be set.
func (b *queryBuilder) buildTokenInExists(param, modifier, vsURL string) (string, bool) {
	if b.terminology == nil {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("modifier :%s on param %q requires FHIR_TERMINOLOGY_URL to be configured", modifier, param)}
		return "", false
	}
	codes, err := b.terminology.Expand(b.ctx, vsURL)
	if err != nil {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("ValueSet $expand %s failed: %v", vsURL, err)}
		return "", false
	}
	if len(codes) == 0 {
		// Empty ValueSet: return a subquery that yields no rows. The caller wraps
		// it in EXISTS(...), so :in → EXISTS(∅) = false (match none), and :not-in
		// → NOT EXISTS(∅) = true (match all). Must be a real subquery, not a bare
		// boolean, because EXISTS requires a SELECT.
		return emptyRowSubquery, true
	}

	sub := b.tokenCodeSetExists(param, codes)
	// caller wraps :not-in in NOT EXISTS at the applyParam level.
	return sub, true
}

// emptyRowSubquery is a valid SELECT that returns no rows, used as the body of
// an EXISTS(...) when a token-set helper resolves to no codes.
const emptyRowSubquery = "SELECT 1 WHERE false"

// tokenCodeSetExists builds a SELECT-1 subquery matching sp_token rows for
// param whose (system,code) is in the given code set (OR-joined pairs).
func (b *queryBuilder) tokenCodeSetExists(param string, codes []terminology.CodeEntry) string {
	rtP := b.next(b.rt)
	pP := b.next(param)
	var pairOrs []string
	for _, c := range codes {
		if c.System != "" {
			sP := b.next(c.System)
			cP := b.next(c.Code)
			pairOrs = append(pairOrs, fmt.Sprintf("(s.system = %s AND s.code = %s)", sP, cP))
		} else {
			cP := b.next(c.Code)
			pairOrs = append(pairOrs, fmt.Sprintf("s.code = %s", cP))
		}
	}
	codeFilter := "(" + strings.Join(pairOrs, " OR ") + ")"
	return fmt.Sprintf("SELECT 1 FROM sp_token s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND %s",
		rtP, pP, codeFilter)
}

// buildOfTypeExists implements the token :of-type modifier for Identifiers.
// value is "typeSystem|typeCode|idValue" (system optional). It matches the
// auxiliary "<param>:of-type" rows written by the indexer, which carry the
// Identifier.type coding in system/code and the identifier value in display.
func (b *queryBuilder) buildOfTypeExists(param, value string) string {
	parts := strings.SplitN(value, "|", 3)
	var typeSys, typeCode, idValue string
	switch len(parts) {
	case 3:
		typeSys, typeCode, idValue = parts[0], parts[1], parts[2]
	case 2:
		typeCode, idValue = parts[0], parts[1]
	default:
		idValue = value
	}
	rtP := b.next(b.rt)
	pP := b.next(param + ":of-type") // matches index.OfTypeSuffix
	conds := []string{
		fmt.Sprintf("s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s", rtP, pP),
	}
	if typeSys != "" {
		conds = append(conds, fmt.Sprintf("s.system = %s", b.next(typeSys)))
	}
	if typeCode != "" {
		conds = append(conds, fmt.Sprintf("s.code = %s", b.next(typeCode)))
	}
	if idValue != "" {
		conds = append(conds, fmt.Sprintf("s.display = %s", b.next(idValue)))
	}
	return "SELECT 1 FROM sp_token s WHERE " + strings.Join(conds, " AND ")
}

// buildTokenHierarchyExists implements token :above / :below via the
// terminology server's subsumption filters (:below = is-a descendants,
// :above = generalizes ancestors). value is "system|code".
func (b *queryBuilder) buildTokenHierarchyExists(param, modifier, value string) (string, bool) {
	if b.terminology == nil {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("modifier :%s on param %q requires FHIR_TERMINOLOGY_URL (code-system subsumption)", modifier, param)}
		return "", false
	}
	sys, code := value, ""
	if i := strings.Index(value, "|"); i >= 0 {
		sys, code = value[:i], value[i+1:]
	}
	if sys == "" || code == "" {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("modifier :%s requires a system|code value, got %q", modifier, value)}
		return "", false
	}
	op := "is-a" // :below — the given code and its descendants
	if modifier == "above" {
		op = "generalizes" // the given code and its ancestors
	}
	codes, err := b.terminology.ExpandFilter(b.ctx, sys, op, code)
	if err != nil {
		b.err = &UnsupportedParamError{Msg: fmt.Sprintf("terminology subsumption (:%s) for %s|%s failed: %v", modifier, sys, code, err)}
		return "", false
	}
	if len(codes) == 0 {
		// No codes in the hierarchy → match nothing (valid no-rows subquery for
		// the EXISTS(...) wrapper).
		return emptyRowSubquery, true
	}
	return b.tokenCodeSetExists(param, codes), true
}

func (b *queryBuilder) buildDateExists(param, value string) string {
	prefix, dateStr := extractComparatorPrefix(value)
	low, high := expandDateRange(dateStr)
	rtP := b.next(b.rt)
	pP := b.next(param)
	// Only bind the args actually referenced by each operator to avoid PG arg-count mismatch.
	switch prefix {
	case "gt":
		highP := b.next(high)
		return fmt.Sprintf("SELECT 1 FROM sp_date s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.value_low > %s", rtP, pP, highP)
	case "lt":
		lowP := b.next(low)
		return fmt.Sprintf("SELECT 1 FROM sp_date s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.value_high < %s", rtP, pP, lowP)
	case "ge":
		lowP := b.next(low)
		return fmt.Sprintf("SELECT 1 FROM sp_date s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.value_high >= %s", rtP, pP, lowP)
	case "le":
		highP := b.next(high)
		return fmt.Sprintf("SELECT 1 FROM sp_date s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.value_low <= %s", rtP, pP, highP)
	case "ne":
		highP := b.next(high)
		lowP := b.next(low)
		return fmt.Sprintf("SELECT 1 FROM sp_date s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND NOT (s.value_low <= %s AND s.value_high >= %s)", rtP, pP, highP, lowP)
	default: // eq
		highP := b.next(high)
		lowP := b.next(low)
		return fmt.Sprintf("SELECT 1 FROM sp_date s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.value_low <= %s AND s.value_high >= %s", rtP, pP, highP, lowP)
	}
}

func (b *queryBuilder) buildNumberExists(param, value string) string {
	prefix, numStr := extractComparatorPrefix(value)
	f, _ := strconv.ParseFloat(numStr, 64)
	eps := math.Abs(f) * 1e-7
	if eps == 0 {
		eps = 1e-7
	}
	rtP := b.next(b.rt)
	pP := b.next(param)
	var cond string
	switch prefix {
	case "gt":
		cond = fmt.Sprintf("s.value_high > %s", b.next(f))
	case "lt":
		cond = fmt.Sprintf("s.value_low < %s", b.next(f))
	default: // eq
		highP := b.next(f + eps)
		lowP := b.next(f - eps)
		cond = fmt.Sprintf("s.value_low <= %s AND s.value_high >= %s", highP, lowP)
	}
	body := fmt.Sprintf("s.resource_type = %s AND s.param_name = %s AND %s", rtP, pP, cond)
	// Capture the correlation-free body so a lone number predicate can drive the
	// id-first CTE straight off sp_number (see fetchSQL). Skipped inside a nested
	// context (composite / chained / _has), whose full predicate is more than this.
	if !b.suppressDirectDrive {
		b.numericTable = "sp_number"
		b.numericBodies = append(b.numericBodies, body)
	}
	return "SELECT 1 FROM sp_number s WHERE s.resource_id = r.fhir_id AND " + body
}

// buildReferenceExists matches a reference param against sp_reference. It accepts
// "Type/id", a bare "id", or an absolute URL, and supports the :identifier
// modifier (patient:identifier=system|value) and an explicit target-type
// modifier (subject:Patient=123).
func (b *queryBuilder) buildReferenceExists(param, modifier, value string) string {
	rtP := b.next(b.rt)
	pP := b.next(param)

	if modifier == "identifier" {
		system, val := "", value
		if i := strings.Index(value, "|"); i >= 0 {
			system, val = value[:i], value[i+1:]
		}
		if system != "" {
			sP := b.next(system)
			vP := b.next(val)
			return fmt.Sprintf("SELECT 1 FROM sp_reference s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.identifier_system = %s AND s.identifier_value = %s", rtP, pP, sP, vP)
		}
		vP := b.next(val)
		return fmt.Sprintf("SELECT 1 FROM sp_reference s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.identifier_value = %s", rtP, pP, vP)
	}

	typ, id := parseSearchReference(value)
	if typ == "" && modifier != "" {
		// e.g. subject:Patient=123 — the modifier names the target type.
		typ = modifier
	}
	if typ != "" {
		tP := b.next(typ)
		iP := b.next(id)
		return fmt.Sprintf("SELECT 1 FROM sp_reference s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.target_type = %s AND s.target_id = %s", rtP, pP, tP, iP)
	}
	iP := b.next(id)
	return fmt.Sprintf("SELECT 1 FROM sp_reference s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.target_id = %s", rtP, pP, iP)
}

// buildQuantityExists matches a quantity param against sp_quantity. The value is
// "[prefix]number|system|code"; system and code are optional. The third token is
// matched against the coded (UCUM) unit stored in sp_quantity.code.
func (b *queryBuilder) buildQuantityExists(param, value string) string {
	numPart := value
	var system, code string
	if parts := strings.SplitN(value, "|", 3); len(parts) > 1 {
		numPart = parts[0]
		system = parts[1]
		if len(parts) == 3 {
			code = parts[2]
		}
	}
	prefix, numStr := extractComparatorPrefix(numPart)
	f, _ := strconv.ParseFloat(numStr, 64)
	eps := math.Abs(f) * 1e-7
	if eps == 0 {
		eps = 1e-7
	}
	low, high := f-eps, f+eps
	rtP := b.next(b.rt)
	pP := b.next(param)

	// Compare the indexed range against the search range endpoints (mirrors
	// buildDateExists) so boundary values are not matched incorrectly — e.g. an
	// indexed 5 must not satisfy gt5.
	//
	// eq/ne/ge/le are interval overlap: the stored [value_low, value_high] band
	// and the search band match iff value_low <= searchHigh AND value_high >=
	// searchLow. Emitting that as a numrange && numrange (overlaps) lets the
	// planner reach idx_sp_qty_range_gist (schema v12) instead of only the btree
	// value indexes — the scalar two-bound form cannot touch the GiST expression
	// index. The stored numrange bracket must match the index expression exactly
	// (numrange(value_low, value_high, '[]')) or the planner won't recognise it.
	// A NULL numrange bound is unbounded, giving the one-sided ge/le prefixes the
	// half-open search band they need. gt/lt are strict — the stored band lies
	// entirely above/below the search point, which is not overlap — so they stay
	// as scalar bound comparisons (still served by idx_sp_qty_raw / _recent).
	const stored = "numrange(s.value_low, s.value_high, '[]')"
	var cond string
	switch prefix {
	case "gt":
		cond = fmt.Sprintf("s.value_low > %s", b.next(high))
	case "lt":
		cond = fmt.Sprintf("s.value_high < %s", b.next(low))
	case "ge":
		// [searchLow, ∞) — overlap iff value_high >= searchLow.
		cond = fmt.Sprintf("%s && numrange(%s, NULL, '[]')", stored, b.next(low))
	case "le":
		// (-∞, searchHigh] — overlap iff value_low <= searchHigh.
		cond = fmt.Sprintf("%s && numrange(NULL, %s, '[]')", stored, b.next(high))
	case "ne":
		lP := b.next(low)
		hP := b.next(high)
		cond = fmt.Sprintf("NOT (%s && numrange(%s, %s, '[]'))", stored, lP, hP)
	default: // eq
		lP := b.next(low)
		hP := b.next(high)
		cond = fmt.Sprintf("%s && numrange(%s, %s, '[]')", stored, lP, hP)
	}

	body := fmt.Sprintf("s.resource_type = %s AND s.param_name = %s AND %s", rtP, pP, cond)
	if system != "" {
		body += fmt.Sprintf(" AND s.system = %s", b.next(system))
	}
	if code != "" {
		body += fmt.Sprintf(" AND s.code = %s", b.next(code))
	}
	// Capture the correlation-free body so a lone quantity predicate can drive the
	// id-first CTE straight off sp_quantity (see fetchSQL). The same $N args are
	// shared with the EXISTS form below. Skipped inside a nested context (composite
	// / chained / _has), whose full predicate is more than this one body.
	if !b.suppressDirectDrive {
		b.numericTable = "sp_quantity"
		b.numericBodies = append(b.numericBodies, body)
	}
	return "SELECT 1 FROM sp_quantity s WHERE s.resource_id = r.fhir_id AND " + body
}

// buildURIExists matches a uri param against sp_uri. Default is an exact match.
// :below matches stored URIs at or beneath the search value in the path
// hierarchy (stored value has the search value as a prefix); :above matches
// stored URIs at or above it (the stored value is a prefix of the search value).
func (b *queryBuilder) buildURIExists(param, modifier, value string) string {
	rtP := b.next(b.rt)
	pP := b.next(param)
	switch modifier {
	case "below":
		// Stored value has the search value as a path prefix. LIKE 'prefix%'
		// uses the text_pattern_ops index; escape the literal's metacharacters.
		vP := b.next(escapeLike(value) + "%")
		return fmt.Sprintf("SELECT 1 FROM sp_uri s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.value LIKE %s", rtP, pP, vP)
	case "above":
		// Stored value is a prefix of the search value. Compared with left()/
		// length() so the per-row stored value needs no LIKE escaping.
		vP := b.next(value)
		return fmt.Sprintf("SELECT 1 FROM sp_uri s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND left(%s, length(s.value)) = s.value", rtP, pP, vP)
	default:
		vP := b.next(value)
		return fmt.Sprintf("SELECT 1 FROM sp_uri s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s AND s.value = %s", rtP, pP, vP)
	}
}

// escapeLike escapes the LIKE metacharacters %, _ and \ in a literal so it can
// be used as a prefix in a LIKE pattern without the value's own characters
// acting as wildcards.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// parseSearchReference splits a reference search value into (type, id). It
// accepts "Patient/123", a bare "123", or an absolute URL ending in Type/id,
// and strips any "/_history/x" version suffix. Mirrors index.parseRefString.
func parseSearchReference(value string) (resourceType, id string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if i := strings.Index(value, "/_history/"); i >= 0 {
		value = value[:i]
	}
	if idx := strings.LastIndex(value, "/"); idx >= 0 {
		pre := value[:idx]
		if slashIdx := strings.LastIndex(pre, "/"); slashIdx >= 0 {
			resourceType = pre[slashIdx+1:]
		} else {
			resourceType = pre
		}
		return resourceType, value[idx+1:]
	}
	return "", value
}

func (b *queryBuilder) applyLastUpdated(value string) {
	prefix, dateStr := extractComparatorPrefix(value)
	low, high := expandDateRange(dateStr)
	switch prefix {
	case "gt":
		highP := b.next(high)
		b.and(fmt.Sprintf("r.last_updated > %s", highP))
	case "lt":
		lowP := b.next(low)
		b.and(fmt.Sprintf("r.last_updated < %s", lowP))
	case "ge":
		lowP := b.next(low)
		b.and(fmt.Sprintf("r.last_updated >= %s", lowP))
	case "le":
		highP := b.next(high)
		b.and(fmt.Sprintf("r.last_updated <= %s", highP))
	default:
		lowP := b.next(low)
		highP := b.next(high)
		b.and(fmt.Sprintf("r.last_updated >= %s AND r.last_updated <= %s", lowP, highP))
	}
}

// addSort parses a _sort value (comma-separated; a leading '-' means
// descending) and appends each component to b.sort, preserving order.
func (b *queryBuilder) addSort(value string) {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		desc := false
		if strings.HasPrefix(part, "-") {
			desc = true
			part = part[1:]
		}
		b.sort = append(b.sort, sortKey{param: part, desc: desc})
	}
}

// orderTerm is one resolved ORDER BY component: a SQL expression (referencing
// the resources alias r) and its direction suffix (e.g. "DESC" or
// "ASC NULLS LAST").
type orderTerm struct {
	expr string
	dir  string
}

// orderTerms resolves b.sort into structured ORDER BY components. Each
// search-param key becomes a correlated subquery into its sp_* table — MIN(value)
// for ascending, MAX(value) for descending — so a resource with multiple values
// sorts by its lowest/highest, with NULLS LAST so unindexed resources sort to the
// end. _id and _lastUpdated sort directly off the resources table. Falls back to
// last_updated DESC when no usable key is supplied.
//
// Returning structured terms (rather than a pre-joined string) lets fetch build
// the id-first query, where the sort keys are computed once in the candidate CTE,
// carried through the LIMIT, and re-applied after the resource_json join.
func (b *queryBuilder) orderTerms() []orderTerm {
	var terms []orderTerm
	for _, k := range b.sort {
		dir := "ASC"
		if k.desc {
			dir = "DESC"
		}
		switch k.param {
		case "_id":
			terms = append(terms, orderTerm{"r.fhir_id", dir})
			continue
		case "_lastUpdated":
			terms = append(terms, orderTerm{"r.last_updated", dir})
			continue
		case "_score":
			// relevance scoring is not implemented; skip rather than error.
			continue
		}

		table, col := "", ""
		if b.reg != nil {
			if def, ok := b.reg.Lookup(b.rt, k.param); ok {
				table = tableForType(def.ParamType)
				col = sortColumnForTable(table)
			}
		}
		if table == "" || col == "" {
			// Unknown or unsortable param (composite/special): skip it rather
			// than fail the whole search.
			continue
		}
		agg := "MIN"
		if k.desc {
			agg = "MAX"
		}
		pP := b.next(k.param)
		expr := fmt.Sprintf(
			"(SELECT %s(s.%s) FROM %s s WHERE s.resource_id = r.fhir_id AND s.resource_type = r.resource_type AND s.param_name = %s)",
			agg, col, table, pP,
		)
		terms = append(terms, orderTerm{expr, dir + " NULLS LAST"})
	}
	if len(terms) == 0 {
		return []orderTerm{{"r.last_updated", "DESC"}}
	}
	return terms
}

// sortColumnForTable returns the value column to sort on for a given sp_* table.
func sortColumnForTable(table string) string {
	switch table {
	case "sp_string":
		return "value_lower"
	case "sp_token":
		return "code"
	case "sp_date":
		return "value_low"
	case "sp_number":
		return "value"
	case "sp_quantity":
		return "value"
	case "sp_uri":
		return "value"
	case "sp_reference":
		return "target_id"
	default:
		return ""
	}
}

// spExists returns a bare SELECT EXISTS subquery (without value filter) for
// the :missing modifier.
func (b *queryBuilder) spExists(table, param, _ string) string {
	rtP := b.next(b.rt)
	pP := b.next(param)
	return fmt.Sprintf("SELECT 1 FROM %s s WHERE s.resource_id = r.fhir_id AND s.resource_type = %s AND s.param_name = %s", table, rtP, pP)
}

func (b *queryBuilder) count(ctx context.Context, pool querier) (int, error) {
	q := fmt.Sprintf(`SELECT COUNT(*) FROM resources r WHERE %s`, b.where.String())
	var n int
	err := pool.QueryRow(ctx, q, b.args...).Scan(&n)
	return n, err
}

// LastN implements the Observation $lastn operation correctly at the store
// layer: it returns the most recent maxN observations per code group, using a
// window function so per-code recency does not depend on overall volume (a
// naive "fetch top-N globally then group" drops low-frequency codes when
// high-frequency ones fill the page). params may carry the supported filters
// (patient/subject/category/code), which are applied as the search WHERE.
// Observations are partitioned by each code coding (system, code) and ordered
// by the indexed observation date (falling back to last_updated).
func (s *Store) LastN(ctx context.Context, params map[string][]string, maxN int) (SearchResult, error) {
	if maxN <= 0 {
		maxN = 1
	}
	b := &queryBuilder{rt: "Observation", reg: s.registry, terminology: s.terminology, ctx: ctx}
	b.writeBase()
	for k, vals := range params {
		if k == "_sort" || k == "max" || k == "_count" || k == "_page" {
			continue
		}
		for _, v := range vals {
			b.applyParam(k, v)
		}
	}
	if b.err != nil {
		return SearchResult{}, b.err
	}

	nP := b.next(maxN)
	q := fmt.Sprintf(`
		SELECT resource_json, version_id, last_updated FROM (
			SELECT r.resource_json AS resource_json, r.version_id AS version_id, r.last_updated AS last_updated,
			       ROW_NUMBER() OVER (
			           PARTITION BY tok.system, tok.code
			           ORDER BY COALESCE(d.value_low, r.last_updated) DESC
			       ) AS rn
			FROM resources r
			JOIN sp_token tok ON tok.resource_id = r.fhir_id AND tok.resource_type = r.resource_type AND tok.param_name = 'code'
			LEFT JOIN sp_date d ON d.resource_id = r.fhir_id AND d.resource_type = r.resource_type AND d.param_name = 'date'
			WHERE %s
		) ranked
		WHERE rn <= %s
		ORDER BY rn`,
		b.where.String(), nP,
	)

	c, err := s.tenantConn(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	defer c.Release()

	rows, err := c.Query(ctx, q, b.args...)
	if err != nil {
		return SearchResult{}, err
	}
	defer rows.Close()

	seen := map[string]bool{}
	var entries []map[string]any
	for rows.Next() {
		var raw []byte
		var versionID int
		var lastUpdated time.Time
		if err := rows.Scan(&raw, &versionID, &lastUpdated); err != nil {
			return SearchResult{}, err
		}
		m, err := unmarshalWithMeta(raw, versionID, lastUpdated)
		if err != nil {
			return SearchResult{}, err
		}
		// An observation with multiple codings appears once per code partition;
		// dedupe so the Bundle lists each resource a single time.
		id, _ := m["id"].(string)
		if id != "" && seen[id] {
			continue
		}
		seen[id] = true
		entries = append(entries, m)
	}
	if err := rows.Err(); err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Total: len(entries), Entries: entries}, nil
}

func (b *queryBuilder) fetch(ctx context.Context, pool querier, limit, offset int) ([]map[string]any, error) {
	q := b.fetchSQL(limit, offset)

	rows, err := pool.Query(ctx, q, b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []map[string]any
	for rows.Next() {
		var raw []byte
		var versionID int
		var lastUpdated time.Time
		if err := rows.Scan(&raw, &versionID, &lastUpdated); err != nil {
			return nil, err
		}
		m, err := unmarshalWithMeta(raw, versionID, lastUpdated)
		if err != nil {
			return nil, err
		}
		entries = append(entries, m)
	}
	return entries, rows.Err()
}

// fetchSQL builds the paginated result query. It binds the ORDER BY parameters
// (for _sort on search params) before LIMIT/OFFSET, keeping b.args ordered as
// [where args…, order-by args…, limit, offset, …].
//
// Three query shapes are produced:
//
//   - Single-scan form (no id-first sp_* predicate): ORDER BY r.last_updated
//     served directly by idx_res_active, so the LIMIT terminates early after
//     `limit` rows without visiting the whole match set. Used for plain browse,
//     resources-column filters, and token/reference/date/string/uri predicates,
//     where the planner has good enough statistics to pick the right plan.
//
//   - Direct-drive id-first form (a lone numeric quantity/number predicate sorted
//     by a resources column — see directDrive): a MATERIALIZED CTE resolves the
//     matching (resource_id, sort keys) straight off the sp_* value index. Because
//     sp_* carries a denormalised last_updated in the covering index, the resolve
//     is index-only for both a sparse match set (returns in well under a
//     millisecond) and a dense one (a bounded index scan + top-N sort), so no
//     density probe is needed. The surviving page then joins resources for JSON.
//
//   - Correlated id-first form (composite, or a multi-predicate search embedding a
//     numeric one): a MATERIALIZED CTE resolves (fhir_id, sort keys) from resources
//     with the correlated EXISTS predicates, so the planner still drives the match
//     off the selective sp_* index rather than scanning resources in last_updated
//     order. Used when the candidate set cannot be expressed as a single sp_* scan.
//
// The MATERIALIZED barrier is load-bearing for a sparse numeric predicate: without
// it the planner (which cannot estimate an unbounded numeric range) collapses into
// the single-scan shape and does a full resources scan with a per-row sp_* probe
// that never reaches the LIMIT — multiple seconds per query on the perf dataset.
func (b *queryBuilder) fetchSQL(limit, offset int) string {
	terms := b.orderTerms()

	if !b.usesSP {
		var clauses []string
		for _, t := range terms {
			clauses = append(clauses, t.expr+" "+t.dir)
		}
		limitP := b.next(limit)
		offsetP := b.next(offset)
		return fmt.Sprintf(`
		SELECT r.resource_json, r.version_id, r.last_updated
		FROM resources r
		WHERE %s
		ORDER BY %s
		LIMIT %s OFFSET %s`,
			b.where.String(), strings.Join(clauses, ", "), limitP, offsetP,
		)
	}

	if b.directDrive(terms) {
		return b.directDriveSQL(terms, limit, offset)
	}

	if b.compositeDriveOK(terms) {
		return b.compositeDriveSQL(terms, limit, offset)
	}

	// Correlated id-first: compute fhir_id plus each sort key once in the candidate
	// CTE, carry them through the LIMIT, then re-apply the sort after the json join.
	selectList := []string{"r.fhir_id"}
	innerCols := []string{"fhir_id"}
	var innerOrder, outerOrder []string
	for i, t := range terms {
		alias := fmt.Sprintf("sort%d", i)
		selectList = append(selectList, t.expr+" AS "+alias)
		innerCols = append(innerCols, alias)
		innerOrder = append(innerOrder, alias+" "+t.dir)
		outerOrder = append(outerOrder, "c."+alias+" "+t.dir)
	}
	limitP := b.next(limit)
	offsetP := b.next(offset)
	rtP := b.next(b.rt)

	return fmt.Sprintf(`
		WITH candidates AS MATERIALIZED (
			SELECT %s
			FROM resources r
			WHERE %s
		)
		SELECT r.resource_json, r.version_id, r.last_updated
		FROM (SELECT %s FROM candidates ORDER BY %s LIMIT %s OFFSET %s) c
		JOIN resources r ON r.fhir_id = c.fhir_id
			AND r.resource_type = %s
			AND r.tenant_id = current_setting('app.current_tenant', true)
		ORDER BY %s`,
		strings.Join(selectList, ", "),
		b.where.String(),
		strings.Join(innerCols, ", "), strings.Join(innerOrder, ", "), limitP, offsetP,
		rtP,
		strings.Join(outerOrder, ", "),
	)
}

// directDrive reports whether the search is a lone numeric (quantity/number)
// value predicate sorted by a resources column, so the id-first candidate CTE can
// be driven straight off the sp_* value index (see directDriveSQL) instead of a
// correlated EXISTS over resources. numericTable/numericBodies hold the captured
// scan; predicateCount == 1 guarantees the bodies fully express the match (no
// other WHERE predicate); orderByResourceIndex keeps the sort mappable to the
// sp_* columns (last_updated / resource_id).
func (b *queryBuilder) directDrive(terms []orderTerm) bool {
	return b.numericTable != "" && len(b.numericBodies) > 0 &&
		b.predicateCount == 1 && orderByResourceIndex(terms)
}

// compositeDriveOK reports whether a captured composite drive can be used: the
// composite must be the sole predicate (predicateCount == 1, so its two
// components fully express the match) and the sort must map to a resources
// column (applied after the resources join, since the token driver table has no
// denormalised sort column). Otherwise the search keeps the correlated id-first
// CTE built from b.where.
func (b *queryBuilder) compositeDriveOK(terms []orderTerm) bool {
	return b.comp != nil && b.predicateCount == 1 && orderByResourceIndex(terms)
}

// compositeDriveSQL resolves candidate resource_ids from the composite's
// selective token component (the driver), filtered by the other component as a
// nested EXISTS, then joins resources once for the small candidate set and
// sorts/limits there. This avoids both the resources recency-walk and the
// correlated CTE's habit of joining resources for every single-component match
// before intersecting. The driver/filter bodies reuse the $N args already bound
// while the components were built; only limit/offset are appended here, and the
// resources_type placeholder reuses writeBase's $1 (b.rtParam) so no bound
// parameter is left unreferenced (SQLSTATE 42P18).
func (b *queryBuilder) compositeDriveSQL(terms []orderTerm, limit, offset int) string {
	selectList := []string{"s.resource_id AS fhir_id"}
	var innerOrder, outerOrder []string
	for i, t := range terms {
		alias := fmt.Sprintf("sort%d", i)
		selectList = append(selectList, directDriveSortExpr(t.expr)+" AS "+alias)
		innerOrder = append(innerOrder, alias+" "+t.dir)
		outerOrder = append(outerOrder, "c."+alias+" "+t.dir)
	}
	limitP := b.next(limit)
	offsetP := b.next(offset)

	// Early-exit composite drive. DISTINCT + ORDER BY + LIMIT are pushed into the
	// candidate subquery (no MATERIALIZED barrier) so the planner walks the
	// driver's recency index (idx_sp_tok_recent: …, code, last_updated DESC)
	// newest-first for the token component, probes the other component via EXISTS
	// per row, and stops as soon as `limit` distinct resources are found — instead
	// of resolving the whole intersection and top-N sorting it (which, for a common
	// code with a loose value bound, was a multi-second materialise-and-sort plus a
	// parallel hash join). The sort keys map from their resources columns to the
	// denormalised sp_token columns (r.last_updated → s.last_updated, r.fhir_id →
	// s.resource_id). DISTINCT dedupes a resource that carries the code more than
	// once. Driver/filter bodies reuse the $N args bound while the components were
	// built; the resource_type placeholder reuses writeBase's $1 (b.rtParam) so no
	// bound parameter is left unreferenced (SQLSTATE 42P18).
	return fmt.Sprintf(`
		SELECT r.resource_json, r.version_id, r.last_updated
		FROM (
			SELECT DISTINCT %s
			FROM %s s
			WHERE s.tenant_id = current_setting('app.current_tenant', true) AND %s
				AND EXISTS (SELECT 1 FROM %s s2 WHERE s2.resource_id = s.resource_id AND %s)
			ORDER BY %s LIMIT %s OFFSET %s
		) c
		JOIN resources r ON r.fhir_id = c.fhir_id
			AND r.resource_type = %s
			AND r.tenant_id = current_setting('app.current_tenant', true)
			AND r.is_deleted = FALSE
		ORDER BY %s`,
		strings.Join(selectList, ", "),
		b.comp.driverTable, b.comp.driverBody,
		b.comp.filterTable, b.comp.filterBody,
		strings.Join(innerOrder, ", "), limitP, offsetP,
		b.rtParam,
		strings.Join(outerOrder, ", "),
	)
}

// directDriveSQL builds the id-first fetch that resolves candidates directly from
// the numeric sp_* value index. The sort keys map from their resources columns to
// the denormalised sp_* columns (r.last_updated → s.last_updated, r.fhir_id →
// s.resource_id), both covered by the value index, so the candidate resolve is
// index-only. The numericBodies reuse the $N args already bound during predicate
// building; only limit/offset/rt are appended here.
func (b *queryBuilder) directDriveSQL(terms []orderTerm, limit, offset int) string {
	selectList := []string{"s.resource_id AS fhir_id"}
	var innerOrder, outerOrder []string
	for i, t := range terms {
		alias := fmt.Sprintf("sort%d", i)
		selectList = append(selectList, directDriveSortExpr(t.expr)+" AS "+alias)
		innerOrder = append(innerOrder, alias+" "+t.dir)
		outerOrder = append(outerOrder, "c."+alias+" "+t.dir)
	}
	match := b.numericBodies[0]
	if len(b.numericBodies) > 1 {
		match = "(" + strings.Join(b.numericBodies, " OR ") + ")"
	}
	limitP := b.next(limit)
	offsetP := b.next(offset)

	// Early-exit id-first fetch. DISTINCT + ORDER BY + LIMIT are pushed into the
	// candidate subquery (no MATERIALIZED barrier) so the planner drives it
	// straight off an sp_* index and stops as soon as `limit` distinct resources
	// are found:
	//
	//   - dense predicate sorted by last_updated → recency covering index
	//     (…, last_updated DESC) INCLUDE (value_low, …): the page resolves after
	//     scanning a few dozen index rows, instead of the older MATERIALIZED CTE's
	//     full materialise-and-sort of the whole match set (e.g. value-quantity=le140
	//     matched ~500k rows → multi-hundred-ms sort; now ~1ms early-exit).
	//   - sparse/empty predicate → value index: returns few/zero rows directly,
	//     never a resources full-scan.
	//
	// DISTINCT dedupes resources that have several matching sp_* rows so a page is
	// always `limit` distinct resources (the old shape could emit duplicates).
	// Reuse writeBase's resource_type placeholder (b.rtParam): b.where is not
	// emitted by this shape, so binding a fresh one would orphan $1 (SQLSTATE 42P18).
	return fmt.Sprintf(`
		SELECT r.resource_json, r.version_id, r.last_updated
		FROM (
			SELECT DISTINCT %s
			FROM %s s
			WHERE s.tenant_id = current_setting('app.current_tenant', true) AND %s
			ORDER BY %s LIMIT %s OFFSET %s
		) c
		JOIN resources r ON r.fhir_id = c.fhir_id
			AND r.resource_type = %s
			AND r.tenant_id = current_setting('app.current_tenant', true)
			AND r.is_deleted = FALSE
		ORDER BY %s`,
		strings.Join(selectList, ", "),
		b.numericTable, match,
		strings.Join(innerOrder, ", "), limitP, offsetP,
		b.rtParam,
		strings.Join(outerOrder, ", "),
	)
}

// directDriveSortExpr maps a resources-column ORDER BY expression to the
// equivalent denormalised sp_* column for the direct-drive candidate CTE. Only
// reached for the two columns orderByResourceIndex admits.
func directDriveSortExpr(resExpr string) string {
	switch resExpr {
	case "r.last_updated":
		return "s.last_updated"
	case "r.fhir_id":
		return "s.resource_id"
	default:
		return resExpr
	}
}

// orderByResourceIndex reports whether every ORDER BY term is a plain resources
// column (last_updated / fhir_id), so the sort maps onto the sp_* value index's
// denormalised columns (direct-drive). A _sort on an sp_* param orders by a
// correlated subquery that no such index provides, so it keeps the correlated
// id-first shape. It takes the already-resolved terms rather than calling
// orderTerms() itself: orderTerms binds a $N arg per _sort param, so calling it
// twice would orphan a placeholder (SQLSTATE 42P18).
func orderByResourceIndex(terms []orderTerm) bool {
	for _, t := range terms {
		if t.expr != "r.last_updated" && t.expr != "r.fhir_id" {
			return false
		}
	}
	return true
}

// fetchWithCount returns the page of matching rows plus the exact total match
// count, using two queries: a dedicated COUNT(*) and a paginated fetch.
//
// The count runs first, before fetch() appends the ORDER BY / LIMIT / OFFSET
// parameters to b.args, so the count query binds only the WHERE arguments.
//
// This deliberately avoids COUNT(*) OVER() in the fetch. A window count is
// evaluated over the entire result set before LIMIT applies, which forces the
// planner to materialize and sort every matching row even though only `limit`
// are returned. For a broad predicate (one matching a large fraction of the
// table) that is the dominant cost: the page fetch alone, ordered by an indexed
// column, can stop after `limit` rows via the ordered index, whereas the window
// count cannot early-terminate. Splitting the two lets the fetch early-terminate
// and keeps the count to a bare aggregate that need not carry resource_json or
// sort — measured ~5x faster on broad searches against the perf dataset, with
// the exact total preserved.
func (b *queryBuilder) fetchWithCount(ctx context.Context, pool querier, limit, offset int) (int, []map[string]any, error) {
	total, err := b.count(ctx, pool)
	if err != nil {
		return 0, nil, err
	}
	entries, err := b.fetch(ctx, pool, limit, offset)
	if err != nil {
		return 0, nil, err
	}
	return total, entries, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func splitModifier(key string) (param, modifier string) {
	if idx := strings.IndexByte(key, ':'); idx >= 0 {
		return key[:idx], key[idx+1:]
	}
	return key, ""
}

func extractComparatorPrefix(s string) (prefix, rest string) {
	for _, p := range []string{"eq", "ne", "gt", "lt", "ge", "le", "sa", "eb", "ap"} {
		if strings.HasPrefix(s, p) && len(s) > 2 {
			return p, s[2:]
		}
	}
	return "eq", s
}

func expandDateRange(s string) (low, high time.Time) {
	low, high, _ = expandDateStringForSearch(s)
	return
}

func expandDateStringForSearch(s string) (low, high time.Time, err error) {
	s = strings.TrimSpace(s)
	switch len(s) {
	case 4:
		y := mustParseInt(s)
		low = time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		high = time.Date(y, 12, 31, 23, 59, 59, 0, time.UTC)
	case 7:
		y, m := mustParseInt(s[:4]), time.Month(mustParseInt(s[5:7]))
		low = time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		high = time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Second)
	case 10:
		t, e := time.ParseInLocation("2006-01-02", s, time.UTC)
		if e != nil {
			return time.Time{}, time.Time{}, e
		}
		low = t
		high = t.Add(24*time.Hour - time.Second)
	default:
		t, e := time.Parse(time.RFC3339, s)
		if e != nil {
			t, e = time.Parse("2006-01-02T15:04:05", s)
		}
		if e != nil {
			return time.Time{}, time.Time{}, e
		}
		low, high = t, t
	}
	return
}

func mustParseInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func looksLikeDate(s string) bool {
	// Strip comparator prefix
	_, rest := extractComparatorPrefix(s)
	return len(rest) >= 4 && rest[0] >= '1' && rest[0] <= '9' &&
		(len(rest) == 4 || (len(rest) > 4 && rest[4] == '-'))
}

func looksLikeNumber(s string) bool {
	_, rest := extractComparatorPrefix(s)
	_, err := strconv.ParseFloat(rest, 64)
	return err == nil
}

// FetchReferences returns resources linked to/from resourceID via sp_reference.
func (s *Store) FetchReferences(ctx context.Context, resourceType, resourceID string, reverse bool) ([]map[string]any, error) {
	var q string
	var args []any
	if !reverse {
		q = `SELECT DISTINCT r.resource_json, r.version_id, r.last_updated
			 FROM sp_reference sr
			 JOIN resources r ON r.fhir_id = sr.target_id AND r.resource_type = sr.target_type
			 WHERE sr.resource_id = $1 AND sr.resource_type = $2 AND r.is_deleted = FALSE
			   AND r.tenant_id = current_setting('app.current_tenant', true)`
		args = []any{resourceID, resourceType}
	} else {
		q = `SELECT DISTINCT r.resource_json, r.version_id, r.last_updated
			 FROM sp_reference sr
			 JOIN resources r ON r.fhir_id = sr.resource_id AND r.resource_type = sr.resource_type
			 WHERE sr.target_id = $1 AND sr.target_type = $2 AND r.is_deleted = FALSE
			   AND r.tenant_id = current_setting('app.current_tenant', true)`
		args = []any{resourceID, resourceType}
	}

	c, err := s.tenantConn(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Release()

	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanResourceRows(rows)
}

func scanResourceRows(rows pgx.Rows) ([]map[string]any, error) {
	var results []map[string]any
	for rows.Next() {
		var raw []byte
		var versionID int
		var lastUpdated time.Time
		if err := rows.Scan(&raw, &versionID, &lastUpdated); err != nil {
			return nil, err
		}
		m, err := unmarshalWithMeta(raw, versionID, lastUpdated)
		if err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, rows.Err()
}
