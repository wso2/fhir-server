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

// Package index extracts search parameter values from a FHIR resource JSON
// and writes them to the sp_* tables.
package index

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2/fhir-server/internal/fhirpath"
	"github.com/wso2/fhir-server/internal/searchparam"
)

// Extractor extracts and persists search index rows for a resource.
type Extractor struct {
	registry *searchparam.Registry
}

func New(registry *searchparam.Registry) *Extractor {
	return &Extractor{registry: registry}
}

// Index extracts all search parameter values from resource and inserts them
// into the sp_* tables within tx using a single batched round-trip.
func (e *Extractor) Index(ctx context.Context, tx pgx.Tx, resourceType, resourceID string, resource map[string]any, lastUpdated time.Time) error {
	slog.Debug("Starting index extraction", "resourceType", resourceType, "resourceID", resourceID)
	batch := &pgx.Batch{}

	defs := e.registry.ForResource(resourceType)
	for _, d := range defs {
		e.queueParam(batch, resourceType, resourceID, resource, d, lastUpdated)
	}
	// Universal meta.* params (_tag/_security/_profile/_source) and the
	// resource-level language. These live on Resource/DomainResource and so
	// aren't in the per-resource registry; index them uniformly for every type.
	queueMeta(batch, resourceType, resourceID, resource, lastUpdated)

	if batch.Len() == 0 {
		return nil
	}

	br := tx.SendBatch(ctx, batch)
	n := batch.Len()
	slog.Debug("sending index batch", "batchSize", n, "resourceType", resourceType)
	for i := 0; i < n; i++ {
		if _, err := br.Exec(); err != nil {
			slog.Warn("index batch exec failed", "type", resourceType, "i", i, "err", err)
			// Non-fatal — continue draining the batch
		}
	}
	return br.Close()
}

// queueMeta queues sp_* rows for the universal meta search params:
//
//	_tag, _security  → sp_token  (Codings from meta.tag / meta.security)
//	_profile, _source → sp_uri   (meta.profile URLs, meta.source URI)
//	_language        → sp_token  (top-level language code)
func queueMeta(batch *pgx.Batch, rt, rid string, resource map[string]any, lastUpdated time.Time) {
	if meta, ok := resource["meta"].(map[string]any); ok {
		for _, m := range []struct{ field, param string }{{"tag", "_tag"}, {"security", "_security"}} {
			if arr, ok := meta[m.field].([]any); ok {
				for _, c := range arr {
					queueToken(batch, rt, rid, m.param, c, lastUpdated)
				}
			}
		}
		if arr, ok := meta["profile"].([]any); ok {
			for _, p := range arr {
				queueURIValue(batch, rt, rid, "_profile", asString(p))
			}
		}
		queueURIValue(batch, rt, rid, "_source", asString(meta["source"]))
	}
	if lang := asString(resource["language"]); lang != "" {
		batch.Queue(
			`INSERT INTO sp_token (resource_id, resource_type, param_name, system, code, display, last_updated)
			 VALUES ($1, $2, '_language', '', $3, '', $4)`,
			rid, rt, lang, lastUpdated,
		)
	}
}

// queueURIValue queues a single sp_uri row, skipping empty values.
func queueURIValue(batch *pgx.Batch, rt, rid, param, value string) {
	if value == "" {
		return
	}
	batch.Queue(
		`INSERT INTO sp_uri (resource_id, resource_type, param_name, value)
		 VALUES ($1, $2, $3, $4)`,
		rid, rt, param, value,
	)
}

// Delete removes all sp_* rows for a resource in a single batched round-trip.
func Delete(ctx context.Context, tx pgx.Tx, resourceType, resourceID string) error {
	tables := []string{"sp_string", "sp_token", "sp_date", "sp_number", "sp_quantity", "sp_uri", "sp_reference", "sp_composite_token_quantity"}
	batch := &pgx.Batch{}
	for _, tbl := range tables {
		batch.Queue(
			fmt.Sprintf(`DELETE FROM %s WHERE resource_id = $1 AND resource_type = $2 AND tenant_id = current_setting('app.current_tenant', true)`, tbl),
			resourceID, resourceType,
		)
	}
	br := tx.SendBatch(ctx, batch)
	for _, tbl := range tables {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("delete from %s: %w", tbl, err)
		}
	}
	return br.Close()
}

// Queue adds all search parameter insert statements for the given resource to
// an external batch without sending it. The caller is responsible for sending
// and draining the batch. This allows callers to merge index inserts with other
// write operations into a single round-trip.
func (e *Extractor) Queue(batch *pgx.Batch, resourceType, resourceID string, resource map[string]any, lastUpdated time.Time) {
	defs := e.registry.ForResource(resourceType)
	for _, d := range defs {
		e.queueParam(batch, resourceType, resourceID, resource, d, lastUpdated)
	}
	queueMeta(batch, resourceType, resourceID, resource, lastUpdated)
}

// QueueDelete adds one DELETE statement per sp_* table for the given resource
// to an external batch without sending it. Returns the number of statements queued.
func QueueDelete(batch *pgx.Batch, resourceType, resourceID string) int {
	tables := []string{"sp_string", "sp_token", "sp_date", "sp_number", "sp_quantity", "sp_uri", "sp_reference", "sp_composite_token_quantity"}
	for _, tbl := range tables {
		batch.Queue(fmt.Sprintf(`DELETE FROM %s WHERE resource_id = $1 AND resource_type = $2 AND tenant_id = current_setting('app.current_tenant', true)`, tbl), resourceID, resourceType)
	}
	return len(tables)
}

// DeleteWithPool removes all sp_* rows using a pool (for soft-delete paths
// where no transaction is provided yet).
func DeleteWithPool(ctx context.Context, pool *pgxpool.Pool, resourceType, resourceID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := Delete(ctx, tx, resourceType, resourceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (e *Extractor) queueParam(batch *pgx.Batch, resourceType, resourceID string, resource map[string]any, d searchparam.Definition, lastUpdated time.Time) {
	vals, err := fhirpath.EvaluatePolymorphic(d.FHIRPath, resource)
	if err != nil || len(vals) == 0 {
		return
	}

	switch d.ParamType {
	case "string":
		queueString(batch, resourceType, resourceID, d.ParamName, vals)
	case "token":
		queueTokenValues(batch, resourceType, resourceID, d.ParamName, vals, lastUpdated)
	case "date", "dateTime", "instant", "Period":
		queueDate(batch, resourceType, resourceID, d.ParamName, vals)
	case "number":
		queueNumber(batch, resourceType, resourceID, d.ParamName, vals, lastUpdated)
	case "quantity":
		queueQuantity(batch, resourceType, resourceID, d.ParamName, vals, lastUpdated)
	case "uri":
		queueURI(batch, resourceType, resourceID, d.ParamName, vals)
	case "reference":
		queueReference(batch, resourceType, resourceID, d.ParamName, vals)
	case "composite":
		// vals are the composite's element instances: the FHIRPath root expression
		// (e.g. Observation, or Observation.component) evaluated against the
		// resource. queueComposite pairs the two component values WITHIN each
		// element so multi-component resources don't cross-match (FHIR composite
		// semantics). Only token+quantity composites are materialised here; other
		// component-type pairs keep the legacy query-time EXISTS path.
		e.queueComposite(batch, resourceType, resourceID, d, vals, lastUpdated)
	}
}

// ─── sp_string ────────────────────────────────────────────────────────────────

func queueString(batch *pgx.Batch, rt, rid, param string, vals []any) {
	for _, v := range vals {
		s := asString(v)
		if s == "" {
			continue
		}
		batch.Queue(
			`INSERT INTO sp_string (resource_id, resource_type, param_name, value_exact, value_lower)
			 VALUES ($1, $2, $3, $4, $5)`,
			rid, rt, param, s, strings.ToLower(s),
		)
	}
}

// ─── sp_token ─────────────────────────────────────────────────────────────────

func queueTokenValues(batch *pgx.Batch, rt, rid, param string, vals []any, lastUpdated time.Time) {
	for _, v := range vals {
		switch val := v.(type) {
		case map[string]any: // Coding or CodeableConcept
			if codings, ok := val["coding"].([]any); ok {
				for _, c := range codings {
					queueToken(batch, rt, rid, param, c, lastUpdated)
				}
			} else {
				// Plain Coding
				queueToken(batch, rt, rid, param, val, lastUpdated)
			}
		case bool:
			code := "false"
			if val {
				code = "true"
			}
			batch.Queue(
				`INSERT INTO sp_token (resource_id, resource_type, param_name, system, code, display, last_updated)
				 VALUES ($1, $2, $3, '', $4, '', $5)`,
				rid, rt, param, code, lastUpdated,
			)
		case string:
			batch.Queue(
				`INSERT INTO sp_token (resource_id, resource_type, param_name, system, code, display, last_updated)
				 VALUES ($1, $2, $3, '', $4, '', $5)`,
				rid, rt, param, val, lastUpdated,
			)
		}
	}
}

// OfTypeSuffix is appended to a token param name to store the auxiliary index
// row used by the :of-type modifier. The row carries the Identifier.type
// coding (system, code) plus the identifier value in the display column.
const OfTypeSuffix = ":of-type"

func queueToken(batch *pgx.Batch, rt, rid, param string, v any, lastUpdated time.Time) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	sys := asString(m["system"])
	code := asString(m["code"])
	display := asString(m["display"])
	value := asString(m["value"])
	// Identifier and ContactPoint carry their token in "value" rather than
	// "code"; fall back to it so identifier/telecom token searches match.
	if code == "" {
		code = value
	}
	if code == "" {
		return
	}
	batch.Queue(
		`INSERT INTO sp_token (resource_id, resource_type, param_name, system, code, display, last_updated)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		rid, rt, param, sys, code, display, lastUpdated,
	)

	// :of-type support — only for Identifiers that carry a type.coding and a value.
	// Store an auxiliary row keyed by "<param>:of-type" with the type's
	// system/code and the identifier value in display.
	if value != "" {
		if typ, ok := m["type"].(map[string]any); ok {
			if codings, ok := typ["coding"].([]any); ok {
				for _, c := range codings {
					cm, _ := c.(map[string]any)
					if cm == nil {
						continue
					}
					tSys := asString(cm["system"])
					tCode := asString(cm["code"])
					if tCode == "" {
						continue
					}
					batch.Queue(
						`INSERT INTO sp_token (resource_id, resource_type, param_name, system, code, display, last_updated)
						 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
						rid, rt, param+OfTypeSuffix, tSys, tCode, value, lastUpdated,
					)
				}
			}
		}
	}
}

// ─── sp_date ──────────────────────────────────────────────────────────────────

func queueDate(batch *pgx.Batch, rt, rid, param string, vals []any) {
	for _, v := range vals {
		low, high, err := parseDateRange(v)
		if err != nil {
			continue
		}
		batch.Queue(
			`INSERT INTO sp_date (resource_id, resource_type, param_name, value_low, value_high)
			 VALUES ($1, $2, $3, $4, $5)`,
			rid, rt, param, low, high,
		)
	}
}

func parseDateRange(v any) (low, high time.Time, err error) {
	switch val := v.(type) {
	case string:
		return expandDateString(val)
	case map[string]any:
		// Period: {start, end}
		startStr := asString(val["start"])
		endStr := asString(val["end"])
		if startStr == "" && endStr == "" {
			return time.Time{}, time.Time{}, fmt.Errorf("empty period")
		}
		if startStr != "" {
			low, _, err = expandDateString(startStr)
			if err != nil {
				return
			}
		} else {
			low = time.Time{}
		}
		if endStr != "" {
			_, high, err = expandDateString(endStr)
			if err != nil {
				return
			}
		} else {
			high = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
		}
		return
	}
	return time.Time{}, time.Time{}, fmt.Errorf("unsupported date type %T", v)
}

var dateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02",
	"2006-01",
	"2006",
}

func expandDateString(s string) (low, high time.Time, err error) {
	s = strings.TrimSpace(s)
	switch len(s) {
	case 4: // YYYY
		y, e := strconv.Atoi(s[0:4])
		if e != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid year %q: %w", s, e)
		}
		low = time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		high = time.Date(y, 12, 31, 23, 59, 59, 0, time.UTC)
	case 7: // YYYY-MM
		if s[4] != '-' {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid year-month %q", s)
		}
		y, e1 := strconv.Atoi(s[0:4])
		mi, e2 := strconv.Atoi(s[5:7])
		if e1 != nil || e2 != nil || mi < 1 || mi > 12 {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid year-month %q", s)
		}
		m := time.Month(mi)
		low = time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		high = time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Second)
	case 10: // YYYY-MM-DD
		t, e := time.ParseInLocation("2006-01-02", s, time.UTC)
		if e != nil {
			return time.Time{}, time.Time{}, e
		}
		low = t
		high = t.Add(24*time.Hour - time.Second)
	default:
		// Full datetime
		for _, layout := range dateLayouts {
			t, e := time.Parse(layout, s)
			if e == nil {
				return t, t, nil
			}
		}
		return time.Time{}, time.Time{}, fmt.Errorf("cannot parse date %q", s)
	}
	return
}

// ─── sp_number ────────────────────────────────────────────────────────────────

func queueNumber(batch *pgx.Batch, rt, rid, param string, vals []any, lastUpdated time.Time) {
	for _, v := range vals {
		f, ok := toFloat(v)
		if !ok {
			continue
		}
		// ±5 ULP as implicit precision range
		eps := math.Abs(f) * 1e-7
		// last_updated mirrors resources.last_updated so the id-first fetch can
		// sort candidates from idx_sp_num_range without a resources lookup.
		batch.Queue(
			`INSERT INTO sp_number (resource_id, resource_type, param_name, value, value_low, value_high, last_updated)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			rid, rt, param, f, f-eps, f+eps, lastUpdated,
		)
	}
}

// ─── sp_quantity ──────────────────────────────────────────────────────────────

func queueQuantity(batch *pgx.Batch, rt, rid, param string, vals []any, lastUpdated time.Time) {
	for _, v := range vals {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		f, ok := toFloat(m["value"])
		if !ok {
			continue
		}
		sys := asString(m["system"])
		code := asString(m["code"])
		low, high := precisionRange(f)
		// last_updated mirrors resources.last_updated so the id-first fetch can
		// sort candidates from idx_sp_qty_raw without a resources lookup.
		batch.Queue(
			`INSERT INTO sp_quantity (resource_id, resource_type, param_name, value, value_low, value_high, system, code, last_updated)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			rid, rt, param, f, low, high, sys, code, lastUpdated,
		)
	}
}

// precisionRange returns the implicit-precision [low, high] band around f
// (±~5 ULP), FHIR's "approximately equal" tolerance. sp_quantity and
// sp_composite_token_quantity share it so both encode the same range for a
// given quantity value; keep them identical or bounded composite searches
// would disagree with plain quantity searches at the range edges.
func precisionRange(f float64) (low, high float64) {
	eps := math.Abs(f) * 1e-7
	return f - eps, f + eps
}

// ─── sp_composite_token_quantity ────────────────────────────────────────────────

// compositeCoding is one token value (system+code) of a composite's token
// component, paired at write time with the quantity component of the same element.
type compositeCoding struct {
	system string
	code   string
}

// queueComposite materialises token+quantity composite pairs for one composite
// search parameter. elements are the composite's element instances — the root
// FHIRPath (e.g. Observation, or Observation.component) already evaluated against
// the resource. For each element it pairs that element's token codings with that
// element's quantity value(s), never across elements, matching FHIR composite
// semantics (a multi-component Observation must not cross-match component A's code
// with component B's value). Only token+quantity composites are handled; other
// component-type pairs return without writing and keep the legacy query path.
func (e *Extractor) queueComposite(batch *pgx.Batch, rt, rid string, d searchparam.Definition, elements []any, lastUpdated time.Time) {
	if len(d.Components) < 2 {
		return
	}
	type0 := e.componentType(rt, d.Components[0].Expression)
	type1 := e.componentType(rt, d.Components[1].Expression)
	var tokenExpr, qtyExpr string
	switch {
	case type0 == "token" && type1 == "quantity":
		tokenExpr, qtyExpr = d.Components[0].Expression, d.Components[1].Expression
	case type0 == "quantity" && type1 == "token":
		tokenExpr, qtyExpr = d.Components[1].Expression, d.Components[0].Expression
	default:
		return
	}

	for _, el := range elements {
		em, ok := el.(map[string]any)
		if !ok {
			continue
		}
		tokenVals, err := fhirpath.EvaluatePolymorphic(normalizeAs(tokenExpr), em)
		if err != nil || len(tokenVals) == 0 {
			continue
		}
		qtyVals, err := fhirpath.EvaluatePolymorphic(normalizeAs(qtyExpr), em)
		if err != nil || len(qtyVals) == 0 {
			continue
		}
		codings := extractCompositeCodings(tokenVals)
		if len(codings) == 0 {
			continue
		}
		for _, qv := range qtyVals {
			qm, ok := qv.(map[string]any)
			if !ok {
				continue
			}
			f, ok := toFloat(qm["value"])
			if !ok {
				continue
			}
			low, high := precisionRange(f)
			qSys := asString(qm["system"])
			qCode := asString(qm["code"])
			for _, tc := range codings {
				batch.Queue(
					`INSERT INTO sp_composite_token_quantity
					 (resource_id, resource_type, param_name, system, code, value, value_low, value_high, qty_system, qty_code, last_updated)
					 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
					rid, rt, d.ParamName, tc.system, tc.code, f, low, high, qSys, qCode, lastUpdated,
				)
			}
		}
	}
}

// componentType resolves a composite component expression (relative FHIRPath such
// as "code" or "value.as(Quantity)") to the FHIR search param type it indexes,
// using the registry. Mirrors the resolution in internal/store (resolveComponentName)
// but returns only the type, which is all the indexer needs to route the pair.
func (e *Extractor) componentType(rt, expr string) string {
	if expr == "" || e.registry == nil {
		return ""
	}
	// Exact FHIRPath match.
	for _, d := range e.registry.ForResource(rt) {
		if d.FHIRPath == expr {
			return d.ParamType
		}
	}
	// Type hint from ".as(Type)".
	typeHint := ""
	plain := expr
	if i := strings.Index(plain, ".as("); i >= 0 {
		if end := strings.IndexByte(plain[i:], ')'); end >= 0 {
			typeHint = plain[i+4 : i+end]
		}
		plain = plain[:i]
	}
	switch typeHint {
	case "Quantity", "SampledData":
		return "quantity"
	case "CodeableConcept":
		return "token"
	}
	// Strip a leading resource-type prefix, then match by param name.
	if dot := strings.IndexByte(plain, '.'); dot >= 0 {
		plain = plain[dot+1:]
	}
	for _, d := range e.registry.ForResource(rt) {
		if d.ParamName == plain {
			return d.ParamType
		}
	}
	return ""
}

// normalizeAs rewrites the FHIRPath ".as(Type)" cast used in composite component
// expressions (e.g. "value.as(Quantity)") into the ".ofType(Type)" form the
// evaluator understands and resolves to the concrete polymorphic field.
func normalizeAs(expr string) string {
	return strings.ReplaceAll(expr, ".as(", ".ofType(")
}

// extractCompositeCodings flattens a token component's evaluated values
// (CodeableConcepts, Codings, or bare code strings) into system+code pairs,
// following the same fallback rules as queueToken (Identifier/ContactPoint carry
// their token in "value"). Values with no usable code are dropped.
func extractCompositeCodings(vals []any) []compositeCoding {
	var out []compositeCoding
	add := func(c any) {
		m, ok := c.(map[string]any)
		if !ok {
			return
		}
		code := asString(m["code"])
		if code == "" {
			code = asString(m["value"])
		}
		if code == "" {
			return
		}
		out = append(out, compositeCoding{system: asString(m["system"]), code: code})
	}
	for _, v := range vals {
		switch val := v.(type) {
		case map[string]any:
			if codings, ok := val["coding"].([]any); ok {
				for _, c := range codings {
					add(c)
				}
			} else {
				add(val)
			}
		case string:
			if val != "" {
				out = append(out, compositeCoding{code: val})
			}
		}
	}
	return out
}

// ─── sp_uri ───────────────────────────────────────────────────────────────────

func queueURI(batch *pgx.Batch, rt, rid, param string, vals []any) {
	for _, v := range vals {
		s := asString(v)
		if s == "" {
			continue
		}
		batch.Queue(
			`INSERT INTO sp_uri (resource_id, resource_type, param_name, value)
			 VALUES ($1, $2, $3, $4)`,
			rid, rt, param, s,
		)
	}
}

// ─── sp_reference ─────────────────────────────────────────────────────────────

func queueReference(batch *pgx.Batch, rt, rid, param string, vals []any) {
	for _, v := range vals {
		m, ok := v.(map[string]any)
		if !ok {
			// May be a plain reference string
			if s := asString(v); s != "" {
				tType, tID := parseRefString(s)
				batch.Queue(
					`INSERT INTO sp_reference (resource_id, resource_type, param_name, target_type, target_id, identifier_system, identifier_value)
					 VALUES ($1, $2, $3, $4, $5, '', '')`,
					rid, rt, param, tType, tID,
				)
			}
			continue
		}
		ref := asString(m["reference"])
		tType, tID := parseRefString(ref)

		var idSys, idVal string
		if id, ok := m["identifier"].(map[string]any); ok {
			idSys = asString(id["system"])
			idVal = asString(id["value"])
		}

		batch.Queue(
			`INSERT INTO sp_reference (resource_id, resource_type, param_name, target_type, target_id, identifier_system, identifier_value)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			rid, rt, param, tType, tID, idSys, idVal,
		)
	}
}

// parseRefString splits "Patient/123" into ("Patient", "123"). Versioned
// references like "Patient/123/_history/2" have the history suffix stripped
// before splitting so the parser doesn't treat "_history" as the id segment.
func parseRefString(ref string) (resourceType, id string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", ""
	}
	if i := strings.Index(ref, "/_history/"); i >= 0 {
		ref = ref[:i]
	}
	if idx := strings.LastIndex(ref, "/"); idx >= 0 {
		pre := ref[:idx]
		if slashIdx := strings.LastIndex(pre, "/"); slashIdx >= 0 {
			resourceType = pre[slashIdx+1:]
		} else {
			resourceType = pre
		}
		id = ref[idx+1:]
		return
	}
	return "", ref
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func asString(v any) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	default:
		return fmt.Sprintf("%v", v)
	}
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}
