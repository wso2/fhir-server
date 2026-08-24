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
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ─── $meta (system / type / instance) ──────────────────────────────────────────

// metaSystem handles GET [base]/$meta — the union of all meta tags/security/
// profiles in use across every resource.
func (h *fhirHandler) metaSystem(w http.ResponseWriter, r *http.Request) {
	meta, err := h.store.AggregateMeta(r.Context(), "")
	if err != nil {
		operationOutcome(w, http.StatusInternalServerError, "error", "exception", "meta aggregation failed: "+err.Error())
		return
	}
	writeFHIR(w, r, http.StatusOK, metaParameters(meta))
}

// metaType handles GET [base]/{type}/$meta — meta in use across one type.
func (h *fhirHandler) metaType(w http.ResponseWriter, r *http.Request) {
	rt := chi.URLParam(r, "resourceType")
	meta, err := h.store.AggregateMeta(r.Context(), rt)
	if err != nil {
		operationOutcome(w, http.StatusInternalServerError, "error", "exception", "meta aggregation failed: "+err.Error())
		return
	}
	writeFHIR(w, r, http.StatusOK, metaParameters(meta))
}

// metaInstance handles GET [base]/{type}/{id}/$meta — the resource's own meta.
func (h *fhirHandler) metaInstance(w http.ResponseWriter, r *http.Request) {
	rt := chi.URLParam(r, "resourceType")
	id := chi.URLParam(r, "id")
	resource, err := h.store.Read(r.Context(), rt, id)
	if err != nil {
		handleError(w, err)
		return
	}
	meta, _ := resource["meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	writeFHIR(w, r, http.StatusOK, metaParameters(meta))
}

// metaAdd handles POST [base]/{type}/{id}/$meta-add — union the supplied
// tag/security/profile entries into the resource's meta.
func (h *fhirHandler) metaAdd(w http.ResponseWriter, r *http.Request) {
	h.metaMutate(w, r, true)
}

// metaDelete handles POST [base]/{type}/{id}/$meta-delete — remove the supplied
// tag/security/profile entries from the resource's meta.
func (h *fhirHandler) metaDelete(w http.ResponseWriter, r *http.Request) {
	h.metaMutate(w, r, false)
}

func (h *fhirHandler) metaMutate(w http.ResponseWriter, r *http.Request, add bool) {
	rt := chi.URLParam(r, "resourceType")
	id := chi.URLParam(r, "id")

	body, err := readFHIRBody(r)
	if err != nil {
		operationOutcome(w, http.StatusBadRequest, "error", "invalid", "invalid Parameters body: "+err.Error())
		return
	}
	inMeta := metaFromParameters(body)
	if inMeta == nil {
		operationOutcome(w, http.StatusBadRequest, "error", "invalid", "expected a Parameters resource with a 'meta' parameter")
		return
	}

	resource, err := h.store.Read(r.Context(), rt, id)
	if err != nil {
		handleError(w, err)
		return
	}
	meta, _ := resource["meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}

	for _, field := range []string{"tag", "security"} {
		meta[field] = mutateCodingList(toList(meta[field]), toList(inMeta[field]), add)
	}
	meta["profile"] = mutateStringList(toList(meta["profile"]), toList(inMeta["profile"]), add)

	// Drop now-empty arrays so meta stays clean.
	for _, field := range []string{"tag", "security", "profile"} {
		if arr, ok := meta[field].([]any); ok && len(arr) == 0 {
			delete(meta, field)
		}
	}
	resource["meta"] = meta

	updated, err := h.store.Update(r.Context(), rt, id, resource, -1)
	if err != nil {
		handleError(w, err)
		return
	}
	outMeta, _ := updated["meta"].(map[string]any)
	writeFHIR(w, r, http.StatusOK, metaParameters(outMeta))
}

// ─── Helpers ───────────────────────────────────────────────────────────────────

// metaParameters wraps a meta object in a Parameters resource (out param
// "return", type Meta), per the $meta family's OperationDefinition.
func metaParameters(meta map[string]any) map[string]any {
	return map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{map[string]any{
			"name":      "return",
			"valueMeta": meta,
		}},
	}
}

// metaFromParameters extracts the input Meta from a Parameters body
// (parameter name="meta", valueMeta=...). Returns nil unless the body is a
// Parameters resource, per the $meta-add / $meta-delete operation contract.
func metaFromParameters(body map[string]any) map[string]any {
	if rt, _ := body["resourceType"].(string); rt != "Parameters" {
		return nil
	}
	params, _ := body["parameter"].([]any)
	for _, raw := range params {
		p, _ := raw.(map[string]any)
		if p == nil {
			continue
		}
		if p["name"] == "meta" {
			if m, ok := p["valueMeta"].(map[string]any); ok {
				return m
			}
		}
	}
	return nil
}

func toList(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	return nil
}

// mutateCodingList adds or removes Coding entries (matched by system+code).
func mutateCodingList(existing, delta []any, add bool) []any {
	key := func(v any) string {
		m, _ := v.(map[string]any)
		s, _ := m["system"].(string)
		c, _ := m["code"].(string)
		return s + "|" + c
	}
	if add {
		seen := map[string]bool{}
		for _, e := range existing {
			seen[key(e)] = true
		}
		for _, d := range delta {
			if !seen[key(d)] {
				existing = append(existing, d)
				seen[key(d)] = true
			}
		}
		return existing
	}
	remove := map[string]bool{}
	for _, d := range delta {
		remove[key(d)] = true
	}
	out := make([]any, 0, len(existing))
	for _, e := range existing {
		if !remove[key(e)] {
			out = append(out, e)
		}
	}
	return out
}

func mutateStringList(existing, delta []any, add bool) []any {
	if add {
		seen := map[string]bool{}
		for _, e := range existing {
			if s, ok := e.(string); ok {
				seen[s] = true
			}
		}
		for _, d := range delta {
			if s, ok := d.(string); ok && !seen[s] {
				existing = append(existing, s)
				seen[s] = true
			}
		}
		return existing
	}
	remove := map[string]bool{}
	for _, d := range delta {
		if s, ok := d.(string); ok {
			remove[s] = true
		}
	}
	out := make([]any, 0, len(existing))
	for _, e := range existing {
		if s, ok := e.(string); ok && remove[s] {
			continue
		}
		out = append(out, e)
	}
	return out
}
