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

// Package validate walks a FHIR StructureDefinition's snapshot.element list
// and checks a resource map against the constraints it encodes:
//
//   - Required elements (min >= 1) must be present and non-null.
//   - Forbidden elements (max = "0") must be absent.
//   - fixed[x] values — the resource must carry exactly that value.
//   - pattern[x] values — the resource value must include every key/value
//     present in the pattern (deep partial match).
//   - constraint[].expression — FHIRPath boolean invariants (via EvaluateBool).
//   - Slicing: value and pattern discriminators on sliced elements.
package validate

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/wso2/fhir-server/internal/fhirpath"
)

// Issue is one validation finding.
type Issue struct {
	Severity    string // "error" | "warning"
	Code        string // "required" | "value" | "structure"
	Expression  string // FHIRPath-style location, e.g. "Patient.name"
	Diagnostics string
}

// AgainstProfile validates resource against a loaded StructureDefinition sd.
// Returns a slice of Issues (empty → valid).
func AgainstProfile(resource, sd map[string]any) []Issue {
	snapshot, _ := sd["snapshot"].(map[string]any)
	if snapshot == nil {
		// No snapshot — try differential (less complete, but better than nothing)
		snapshot, _ = sd["differential"].(map[string]any)
	}
	if snapshot == nil {
		return nil
	}
	elements, _ := snapshot["element"].([]any)
	if len(elements) == 0 {
		return nil
	}

	// Build a flat constraint map keyed by element path.
	type elemConstraint struct {
		min     int
		maxZero bool
		fixed   any // fixed[x] value
		pattern any // pattern[x] value
	}
	constraints := make(map[string]elemConstraint, len(elements))
	for _, raw := range elements {
		el, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path, _ := el["path"].(string)
		if path == "" {
			continue
		}
		var c elemConstraint

		if minV, ok := el["min"].(float64); ok {
			c.min = int(minV)
		}
		if maxV, _ := el["max"].(string); maxV == "0" {
			c.maxZero = true
		}
		for k, v := range el {
			if strings.HasPrefix(k, "fixed") {
				c.fixed = v
				break
			}
		}
		for k, v := range el {
			if strings.HasPrefix(k, "pattern") {
				c.pattern = v
				break
			}
		}
		constraints[path] = c
	}

	// Check FHIRPath invariants from constraint[].expression on each element.
	var invariantIssues []Issue
	for _, raw := range elements {
		el, _ := raw.(map[string]any)
		if el == nil {
			continue
		}
		path, _ := el["path"].(string)
		constArr, _ := el["constraint"].([]any)
		for _, cr := range constArr {
			c, _ := cr.(map[string]any)
			if c == nil {
				continue
			}
			severity, _ := c["severity"].(string)
			if severity == "" {
				severity = "error"
			}
			expr, _ := c["expression"].(string)
			key, _ := c["key"].(string)
			human, _ := c["human"].(string)
			if expr == "" {
				continue
			}
			ok, err := fhirpath.EvaluateBool(expr, resource)
			if err != nil {
				slog.Debug("invariant eval error", "path", path, "key", key, "err", err)
				continue
			}
			if !ok {
				msg := human
				if msg == "" {
					msg = fmt.Sprintf("invariant %s failed: %s", key, expr)
				}
				invariantIssues = append(invariantIssues, Issue{
					Severity:    severity,
					Code:        "invariant",
					Expression:  path,
					Diagnostics: msg,
				})
			}
		}
	}

	// Slicing: for elements with a slicing definition, validate that each slice
	// entry in the resource matches its discriminator. Currently supports
	// discriminator.type = "value" and "pattern".
	slicingIssues := checkSlicing(resource, elements, sd)

	var issues []Issue
	rootType, _ := sd["type"].(string)

	for path, c := range constraints {
		// Skip the root element itself (e.g. "Patient" — cardinality is meaningless there).
		if path == rootType || !strings.Contains(path, ".") {
			continue
		}
		// Convert SD path (Patient.name.family) to a relative key path within
		// the resource (name.family) by stripping the resource type prefix.
		relPath := strings.TrimPrefix(path, rootType+".")

		val := resolveElement(resource, relPath)
		present := val != nil

		// A required element only applies when its parent is present: a min>=1
		// element nested under an absent optional element (e.g. doseNumber[x]
		// inside an omitted protocolApplied) is not actually required.
		if c.min >= 1 && !present && parentPresent(resource, relPath) {
			issues = append(issues, Issue{
				Severity:    "error",
				Code:        "required",
				Expression:  path,
				Diagnostics: fmt.Sprintf("%s: minimum cardinality is %d but element is absent", path, c.min),
			})
		}
		if c.maxZero && present {
			issues = append(issues, Issue{
				Severity:    "error",
				Code:        "structure",
				Expression:  path,
				Diagnostics: fmt.Sprintf("%s: element is not permitted (max=0)", path),
			})
		}
		if c.fixed != nil && present && !deepEqual(val, c.fixed) {
			issues = append(issues, Issue{
				Severity:    "error",
				Code:        "value",
				Expression:  path,
				Diagnostics: fmt.Sprintf("%s: value does not match fixed value", path),
			})
		}
		if c.pattern != nil && present && !matchesPattern(val, c.pattern) {
			issues = append(issues, Issue{
				Severity:    "error",
				Code:        "value",
				Expression:  path,
				Diagnostics: fmt.Sprintf("%s: value does not match required pattern", path),
			})
		}
	}
	issues = append(issues, invariantIssues...)
	issues = append(issues, slicingIssues...)
	return issues
}

// checkSlicing validates sliced elements (elements with a slicing definition).
// For each slice group, each element in the resource's corresponding array
// must satisfy the discriminator pattern — if a slice has a patternX, the
// corresponding resource element must match.
func checkSlicing(resource map[string]any, elements []any, sd map[string]any) []Issue {
	rootType, _ := sd["type"].(string)
	var issues []Issue

	// Collect slice definitions: path → map[sliceName]sliceElement
	type sliceEntry struct {
		name    string
		pattern any // patternX value if set
		min     int
	}
	sliceGroups := map[string][]sliceEntry{}

	for _, raw := range elements {
		el, _ := raw.(map[string]any)
		if el == nil {
			continue
		}
		path, _ := el["path"].(string)
		if path == "" {
			continue
		}
		// Element is a named slice (has sliceName).
		sliceName, _ := el["sliceName"].(string)
		if sliceName == "" {
			continue
		}
		// A slice shares the path with its parent element (e.g. a slice of
		// "Observation.category" also has path "Observation.category"), so we
		// group slices by that shared path.
		basePath := path
		se := sliceEntry{name: sliceName}
		if m, ok := el["min"].(float64); ok {
			se.min = int(m)
		}
		// Find the pattern value.
		for k, v := range el {
			if strings.HasPrefix(k, "pattern") {
				se.pattern = v
				break
			}
		}
		sliceGroups[basePath] = append(sliceGroups[basePath], se)
	}

	// For each sliced path that has required slices with a pattern, check that
	// at least one element in the resource array matches.
	for slicedPath, slices := range sliceGroups {
		relPath := strings.TrimPrefix(slicedPath, rootType+".")
		val := getPath(resource, relPath)
		if val == nil {
			// Check if any required slice is missing.
			for _, se := range slices {
				if se.min >= 1 {
					issues = append(issues, Issue{
						Severity:    "error",
						Code:        "required",
						Expression:  slicedPath,
						Diagnostics: fmt.Sprintf("slice %q (min=%d) is required but no element matches", se.name, se.min),
					})
				}
			}
			continue
		}
		arr, isArr := val.([]any)
		if !isArr {
			arr = []any{val}
		}
		for _, se := range slices {
			if se.pattern == nil {
				continue
			}
			// Count elements matching this slice's pattern.
			matchCount := 0
			for _, item := range arr {
				if matchesPattern(item, se.pattern) {
					matchCount++
				}
			}
			if se.min >= 1 && matchCount == 0 {
				issues = append(issues, Issue{
					Severity:    "error",
					Code:        "required",
					Expression:  slicedPath,
					Diagnostics: fmt.Sprintf("required slice %q has no matching element (min=%d)", se.name, se.min),
				})
			}
		}
	}
	return issues
}

// resolveElement returns the value at a relative element path, additionally
// resolving a trailing FHIR choice-type segment ("value[x]") to whichever
// concrete variant is present in the resource ("valueQuantity", "valueString",
// …). For non-choice paths it is equivalent to getPath.
func resolveElement(resource map[string]any, relPath string) any {
	if !strings.HasSuffix(relPath, "[x]") {
		return getPath(resource, relPath)
	}
	base := strings.TrimSuffix(relPath, "[x]")

	// Locate the map that holds the choice element, then match by prefix.
	parent := resource
	leaf := base
	if i := strings.LastIndex(base, "."); i >= 0 {
		switch p := getPath(resource, base[:i]).(type) {
		case map[string]any:
			parent = p
		case []any:
			if len(p) == 0 {
				return nil
			}
			m, ok := p[0].(map[string]any)
			if !ok {
				return nil
			}
			parent = m
		default:
			return nil
		}
		leaf = base[i+1:]
	}
	return matchChoice(parent, leaf)
}

// matchChoice returns the value of a choice element named leaf (e.g. "value")
// within m by finding a key of the form "value<Type>" — leaf followed by a
// type name whose first character is uppercase. Returns nil when absent.
func matchChoice(m map[string]any, leaf string) any {
	for k, v := range m {
		if len(k) > len(leaf) && strings.HasPrefix(k, leaf) {
			c := k[len(leaf)]
			if c >= 'A' && c <= 'Z' {
				return v
			}
		}
	}
	return nil
}

// parentPresent reports whether the parent of relPath is present in the
// resource (top-level elements always qualify). Used to suppress required-
// cardinality errors for elements nested under an absent optional element.
func parentPresent(resource map[string]any, relPath string) bool {
	i := strings.LastIndex(relPath, ".")
	if i < 0 {
		return true
	}
	return getPath(resource, relPath[:i]) != nil
}

// getPath navigates a dot-delimited relative path into a resource map.
// Returns the value at the path (may be a scalar, map, or []any).
// For intermediate array segments it descends into the first element.
// Returns nil when any segment is absent.
func getPath(resource map[string]any, path string) any {
	parts := strings.SplitN(path, ".", 2)
	key := parts[0]
	val, ok := resource[key]
	if !ok || val == nil {
		return nil
	}
	// Terminal segment — return the raw value (array, map, or scalar).
	if len(parts) == 1 {
		return val
	}
	// Intermediate segment: unwrap a single-element array to descend.
	switch v := val.(type) {
	case map[string]any:
		return getPath(v, parts[1])
	case []any:
		if len(v) == 0 {
			return nil
		}
		if m, ok := v[0].(map[string]any); ok {
			return getPath(m, parts[1])
		}
		return nil
	}
	return nil
}

// deepEqual compares two values for equality (handles maps, slices, scalars).
func deepEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bm, ok := b.(map[string]any)
		if !ok || len(av) != len(bm) {
			return false
		}
		for k, v := range av {
			if !deepEqual(v, bm[k]) {
				return false
			}
		}
		return true
	case []any:
		bl, ok := b.([]any)
		if !ok || len(av) != len(bl) {
			return false
		}
		for i := range av {
			if !deepEqual(av[i], bl[i]) {
				return false
			}
		}
		return true
	default:
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
	}
}

// matchesPattern checks that val satisfies the pattern constraint:
//   - Pattern map → every key in the pattern must be present and match in val.
//   - Val is an array → at least one element must satisfy the pattern.
//   - Otherwise → deep equality.
func matchesPattern(val, pattern any) bool {
	pm, ok := pattern.(map[string]any)
	if !ok {
		return deepEqual(val, pattern)
	}
	// If the actual value is an array (e.g. category is []any), at least one
	// element must contain all the pattern keys.
	if arr, ok := val.([]any); ok {
		for _, elem := range arr {
			if matchesPattern(elem, pm) {
				return true
			}
		}
		return false
	}
	vm, ok := val.(map[string]any)
	if !ok {
		return false
	}
	for k, pv := range pm {
		vv, exists := vm[k]
		if !exists {
			return false
		}
		if !matchesPattern(vv, pv) {
			return false
		}
	}
	return true
}
