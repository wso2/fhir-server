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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/wso2/fhir-server/internal/store"
)

// bundle handles POST /fhir/r4 — a system-level transaction or batch Bundle.
func (h *fhirHandler) bundle(w http.ResponseWriter, r *http.Request) {
	if !requireFHIRContent(w, r) {
		return
	}

	body, err := readBody(r)
	if err != nil {
		writeBodyError(w, "invalid JSON: ", err)
		return
	}

	if rt, _ := body["resourceType"].(string); rt != "Bundle" {
		operationOutcome(w, http.StatusBadRequest, "error", "invalid",
			"request body must be a Bundle resource")
		return
	}

	bundleType, _ := body["type"].(string)
	if bundleType != "transaction" && bundleType != "batch" {
		operationOutcome(w, http.StatusBadRequest, "error", "value",
			fmt.Sprintf("Bundle.type must be 'transaction' or 'batch', got %q", bundleType))
		return
	}

	entries, perr := parseBundleEntries(body)
	if perr != "" {
		operationOutcome(w, http.StatusBadRequest, "error", "value", perr)
		return
	}

	ctx := r.Context()
	base := h.tenantBaseURL(ctx)

	// Content validation mirrors the single-resource write path. A transaction is
	// atomic, so any invalid entry rejects the whole bundle; a batch processes
	// entries independently, so an invalid entry yields a per-entry outcome while
	// the valid entries still execute.
	execEntries := entries
	var invalid map[int]string
	if bundleType == "batch" {
		invalid = map[int]string{}
		execEntries = make([]store.BundleEntryRequest, 0, len(entries))
		for i, e := range entries {
			if msg := h.validateBundleEntry(r, base, e); msg != "" {
				invalid[i] = msg
				continue
			}
			execEntries = append(execEntries, e)
		}
	} else {
		for i, e := range entries {
			if msg := h.validateBundleEntry(r, base, e); msg != "" {
				operationOutcome(w, http.StatusUnprocessableEntity, "error", "invalid",
					fmt.Sprintf("entry[%d]: %s", i, msg))
				return
			}
		}
	}

	results, err := h.store.ExecuteBundle(ctx, bundleType, base, execEntries)
	if err != nil {
		var be *store.BundleError
		if errors.As(err, &be) {
			slog.Error("bundle execution failed", "bundleType", bundleType,
				"entryIndex", be.EntryIndex, "status", be.HTTPStatus, "err", be.Diagnostics)
			diag := be.Diagnostics
			if be.EntryIndex >= 0 {
				diag = fmt.Sprintf("entry[%d]: %s", be.EntryIndex, be.Diagnostics)
			}
			operationOutcome(w, be.HTTPStatus, "error", be.Code, diag)
			return
		}
		slog.Error("bundle execution failed", "bundleType", bundleType, "err", err)
		operationOutcome(w, http.StatusInternalServerError, "error", "exception",
			"unexpected error processing the bundle; see server logs")
		return
	}

	// Re-insert the held-back batch validation failures as per-entry outcomes,
	// in the original request order, so every request entry has a response entry.
	if bundleType == "batch" && len(invalid) > 0 {
		merged := make([]store.BundleEntryResult, len(entries))
		vi := 0
		for i := range entries {
			if msg, bad := invalid[i]; bad {
				merged[i] = store.BundleEntryResult{
					Status:  "422 Unprocessable Entity",
					Outcome: bundleValidationOutcome(msg),
				}
				continue
			}
			merged[i] = results[vi]
			vi++
		}
		results = merged
	}

	// Keep the in-memory SearchParameter registry in sync with any custom
	// SearchParameters written by the Bundle, mirroring the single-resource path:
	// create/update re-sync the definition, delete removes it. A sync failure
	// here means the persisted resource and the in-memory registry are out of
	// step — log loudly so an operator can spot it; we still return success
	// because the Bundle itself committed.
	for _, res := range results {
		if res.ResourceType != "SearchParameter" {
			continue
		}
		switch res.Method {
		case "POST", "PUT", "PATCH":
			if res.Resource != nil {
				if serr := h.store.SyncSearchParameter(r.Context(), res.Resource); serr != nil {
					slog.Error("bundle SearchParameter sync failed; registry may be stale",
						"method", res.Method, "resourceID", res.ID, "err", serr)
				}
			}
		case "DELETE":
			if res.ID != "" {
				if serr := h.store.DeleteSearchParameter(r.Context(), res.ID); serr != nil {
					slog.Error("bundle SearchParameter delete-sync failed; registry may be stale",
						"resourceID", res.ID, "err", serr)
				}
			}
		}
	}

	responseType := "transaction-response"
	if bundleType == "batch" {
		responseType = "batch-response"
	}
	writeJSON(w, http.StatusOK, h.buildBundleResponse(h.tenantBaseURL(r.Context()), responseType, results))
}

// buildBundleResponse assembles the transaction-response / batch-response Bundle.
func (h *fhirHandler) buildBundleResponse(base, responseType string, results []store.BundleEntryResult) map[string]any {
	entries := make([]any, 0, len(results))
	for _, res := range results {
		response := map[string]any{"status": res.Status}
		if res.Location != "" {
			response["location"] = h.absoluteLocation(base, res.Location)
		}
		if res.ETag != "" {
			response["etag"] = res.ETag
		}
		if res.Outcome != nil {
			response["outcome"] = res.Outcome
		}

		entry := map[string]any{"response": response}
		if res.Resource != nil {
			entry["resource"] = res.Resource
		}
		entries = append(entries, entry)
	}

	return map[string]any{
		"resourceType": "Bundle",
		"type":         responseType,
		"entry":        entries,
	}
}

// absoluteLocation turns a relative "Type/id/_history/v" location into an
// absolute URL under the server base.
func (h *fhirHandler) absoluteLocation(base, loc string) string {
	if strings.Contains(loc, "://") {
		return loc
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(loc, "/")
}

// parseBundleEntries converts the raw Bundle.entry array into typed store
// requests. It returns a non-empty error string on a malformed Bundle.
func parseBundleEntries(bundle map[string]any) ([]store.BundleEntryRequest, string) {
	rawEntries, ok := bundle["entry"].([]any)
	if !ok {
		// An empty Bundle is valid — nothing to process.
		return nil, ""
	}

	entries := make([]store.BundleEntryRequest, 0, len(rawEntries))
	for i, raw := range rawEntries {
		entryMap, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Sprintf("entry[%d] is not an object", i)
		}

		req, ok := entryMap["request"].(map[string]any)
		if !ok {
			return nil, fmt.Sprintf("entry[%d].request is required for transaction/batch", i)
		}

		method, _ := req["method"].(string)
		url, _ := req["url"].(string)
		if method == "" || url == "" {
			return nil, fmt.Sprintf("entry[%d].request.method and request.url are required", i)
		}

		entry := store.BundleEntryRequest{
			Method:      method,
			URL:         url,
			IfMatch:     stringField(req, "ifMatch"),
			IfNoneExist: stringField(req, "ifNoneExist"),
			FullURL:     stringField(entryMap, "fullUrl"),
		}
		if resource, ok := entryMap["resource"].(map[string]any); ok {
			entry.Resource = resource
		}
		entries = append(entries, entry)
	}
	slog.Debug("parsed bundle entries", "count", len(entries))
	return entries, ""
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// validateBundleEntry applies the same content validation the single-resource
// write path enforces to one resource-bearing Bundle entry: a resourceType that
// disagrees with the entry's request.url, a missing base-required field, or a
// base/profile validation error. It returns an empty string when the entry
// passes. Callers decide what a failure means per Bundle type — a transaction
// rejects the whole bundle, a batch reports the entry individually.
func (h *fhirHandler) validateBundleEntry(r *http.Request, baseURL string, e store.BundleEntryRequest) string {
	method := strings.ToUpper(strings.TrimSpace(e.Method))
	if method != "POST" && method != "PUT" {
		return ""
	}
	if e.Resource == nil {
		return ""
	}
	rt, _, _, _, perr := store.ParseEntryURL(baseURL, e.URL)
	if perr != "" || rt == "" {
		return ""
	}
	if bodyRT, ok := e.Resource["resourceType"].(string); ok && bodyRT != "" && bodyRT != rt {
		return fmt.Sprintf("body resourceType %q does not match request.url resource type %q", bodyRT, rt)
	}
	if msg := validateRequiredFields(rt, e.Resource); msg != "" {
		return msg
	}
	if _, ok := e.Resource["resourceType"].(string); !ok {
		e.Resource["resourceType"] = rt
	}
	for _, iss := range h.writeValidationIssues(r, e.Resource) {
		if iss.Severity == "error" {
			return iss.Diagnostics
		}
	}
	return ""
}

// bundleValidationOutcome builds the OperationOutcome carried by a batch response
// entry that failed handler-side content validation before execution.
func bundleValidationOutcome(msg string) map[string]any {
	return map[string]any{
		"resourceType": "OperationOutcome",
		"issue": []any{map[string]any{
			"severity":    "error",
			"code":        "invalid",
			"diagnostics": msg,
		}},
	}
}
