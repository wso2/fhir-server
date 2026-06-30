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

// elemConstraint is the per-element constraint data extracted from a
// StructureDefinition element: cardinality, forbidden flag, and fixed/pattern.
type elemConstraint struct {
	min     int
	maxZero bool
	fixed   any // fixed[x] value
	pattern any // pattern[x] value
}

// invariant is a FHIRPath constraint declared on an element.
type invariant struct {
	path     string
	key      string
	severity string
	human    string
	expr     string
}

// sliceEntry describes one named slice of a sliced element.
type sliceEntry struct {
	name    string
	pattern any // patternX value if set
	min     int
}

// Profile is a compiled StructureDefinition. Everything derived solely from the
// SD — the constraint map, invariants and slice groups — is extracted once by
// Compile, so validating many resources of the same type does not re-parse the
// (large) snapshot on every call. Only the per-resource checks run in Validate.
type Profile struct {
	rootType    string
	constraints map[string]elemConstraint
	invariants  []invariant
	sliceGroups map[string][]sliceEntry
}

// Compile extracts the SD-derived validation data from a StructureDefinition.
// It returns nil when the SD carries no usable element list (neither snapshot
// nor differential); a nil *Profile validates as a no-op.
func Compile(sd map[string]any) *Profile {
	snapshot, _ := sd["snapshot"].(map[string]any)
	if snapshot == nil {
		// No snapshot — fall back to differential (less complete, but better than nothing).
		snapshot, _ = sd["differential"].(map[string]any)
	}
	if snapshot == nil {
		return nil
	}
	elements, _ := snapshot["element"].([]any)
	if len(elements) == 0 {
		return nil
	}

	rootType, _ := sd["type"].(string)
	p := &Profile{
		rootType:    rootType,
		constraints: make(map[string]elemConstraint, len(elements)),
		sliceGroups: map[string][]sliceEntry{},
	}

	for _, raw := range elements {
		el, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path, _ := el["path"].(string)
		if path == "" {
			continue
		}

		// Cardinality, forbidden flag, fixed[x], pattern[x].
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
		p.constraints[path] = c

		// FHIRPath invariants declared on this element.
		constArr, _ := el["constraint"].([]any)
		for _, cr := range constArr {
			cm, _ := cr.(map[string]any)
			if cm == nil {
				continue
			}
			expr, _ := cm["expression"].(string)
			if expr == "" {
				continue
			}
			severity, _ := cm["severity"].(string)
			if severity == "" {
				severity = "error"
			}
			key, _ := cm["key"].(string)
			human, _ := cm["human"].(string)
			p.invariants = append(p.invariants, invariant{
				path: path, key: key, severity: severity, human: human, expr: expr,
			})
		}

		// Named slices share their parent element's path; group them by it.
		if sliceName, _ := el["sliceName"].(string); sliceName != "" {
			se := sliceEntry{name: sliceName}
			if m, ok := el["min"].(float64); ok {
				se.min = int(m)
			}
			for k, v := range el {
				if strings.HasPrefix(k, "pattern") {
					se.pattern = v
					break
				}
			}
			p.sliceGroups[path] = append(p.sliceGroups[path], se)
		}
	}
	return p
}

// AgainstProfile validates resource against a StructureDefinition. It compiles
// the SD on every call; callers that validate many resources against the same
// SD should Compile once and reuse the returned *Profile.
func AgainstProfile(resource, sd map[string]any) []Issue {
	return Compile(sd).Validate(resource)
}

// Validate checks resource against the compiled profile. A nil profile (an SD
// with no usable elements) yields no issues.
func (p *Profile) Validate(resource map[string]any) []Issue {
	if p == nil {
		return nil
	}

	var issues []Issue
	for path, c := range p.constraints {
		// Skip the root element itself (e.g. "Patient" — cardinality is meaningless there).
		if path == p.rootType || !strings.Contains(path, ".") {
			continue
		}
		// Convert SD path (Patient.name.family) to a relative key path within
		// the resource (name.family) by stripping the resource type prefix.
		relPath := strings.TrimPrefix(path, p.rootType+".")

		// Collect every value at this path, descending into all repeated
		// (array) ancestors — not just the first — so constraints are checked
		// against each occurrence.
		vals := collectValues(resource, relPath)
		present := len(vals) > 0

		// A required element only applies where its parent is present: a min>=1
		// element nested under an absent optional element (e.g. doseNumber[x]
		// inside an omitted protocolApplied) is not actually required. When the
		// parent repeats, every entry must carry the required child.
		if c.min >= 1 && missingRequired(resource, relPath) {
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
		if c.fixed != nil && present && anyNotEqual(vals, c.fixed) {
			issues = append(issues, Issue{
				Severity:    "error",
				Code:        "value",
				Expression:  path,
				Diagnostics: fmt.Sprintf("%s: value does not match fixed value", path),
			})
		}
		if c.pattern != nil && present && anyNotMatching(vals, c.pattern) {
			issues = append(issues, Issue{
				Severity:    "error",
				Code:        "value",
				Expression:  path,
				Diagnostics: fmt.Sprintf("%s: value does not match required pattern", path),
			})
		}
	}

	// FHIRPath invariants, evaluated against the resource.
	for _, inv := range p.invariants {
		ok, err := fhirpath.EvaluateBool(inv.expr, resource)
		if err != nil {
			slog.Debug("invariant eval error", "path", inv.path, "key", inv.key, "err", err)
			continue
		}
		if !ok {
			msg := inv.human
			if msg == "" {
				msg = fmt.Sprintf("invariant %s failed: %s", inv.key, inv.expr)
			}
			issues = append(issues, Issue{
				Severity:    inv.severity,
				Code:        "invariant",
				Expression:  inv.path,
				Diagnostics: msg,
			})
		}
	}

	issues = append(issues, p.checkSlicing(resource)...)
	return issues
}

// checkSlicing validates the resource's sliced elements against the slice
// discriminators precomputed in the profile. For each slice group, an element
// in the resource's corresponding array must satisfy the slice's pattern; a
// required slice (min>=1) with no matching element is an error.
func (p *Profile) checkSlicing(resource map[string]any) []Issue {
	var issues []Issue

	for slicedPath, slices := range p.sliceGroups {
		relPath := strings.TrimPrefix(slicedPath, p.rootType+".")
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

// collectValues returns every value at a relative element path, descending into
// all entries of any repeated (array) ancestor — not just the first — and
// resolving FHIR choice-type segments ("value[x]") to the concrete variant
// present. The result is flat across array branches, so a path like
// "component.code" yields one entry per component that has a code.
func collectValues(resource map[string]any, relPath string) []any {
	return collectAt(resource, strings.Split(relPath, "."))
}

// collectAt walks parts from node, fanning out across array elements at every
// level and returning all terminal values reached.
func collectAt(node any, parts []string) []any {
	if len(parts) == 0 {
		if node == nil {
			return nil
		}
		return []any{node}
	}
	switch v := node.(type) {
	case map[string]any:
		return collectAt(resolveLeaf(v, parts[0]), parts[1:])
	case []any:
		var out []any
		for _, e := range v {
			out = append(out, collectAt(e, parts)...)
		}
		return out
	default:
		return nil
	}
}

// resolveLeaf returns the value of a single path segment within m, handling a
// trailing choice-type marker ("value[x]" → "valueQuantity", …).
func resolveLeaf(m map[string]any, segment string) any {
	if strings.HasSuffix(segment, "[x]") {
		return matchChoice(m, strings.TrimSuffix(segment, "[x]"))
	}
	return m[segment]
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

// missingRequired reports whether a required element at relPath is absent where
// it applies. A top-level element is required outright; a nested element is
// required only when its parent is present, and when the parent repeats it must
// be present in every entry.
func missingRequired(resource map[string]any, relPath string) bool {
	i := strings.LastIndex(relPath, ".")
	if i < 0 {
		return len(collectValues(resource, relPath)) == 0
	}
	parentPath, leaf := relPath[:i], relPath[i+1:]
	parents := mapsAt(resource, parentPath)
	if len(parents) == 0 {
		return false // parent absent → not applicable
	}
	for _, p := range parents {
		if resolveLeaf(p, leaf) == nil {
			return true
		}
	}
	return false
}

// mapsAt returns every object reached at parentPath, flattening repeated
// (array) ancestors into their individual element maps.
func mapsAt(resource map[string]any, parentPath string) []map[string]any {
	var out []map[string]any
	for _, v := range collectAt(resource, strings.Split(parentPath, ".")) {
		switch t := v.(type) {
		case map[string]any:
			out = append(out, t)
		case []any:
			for _, e := range t {
				if m, ok := e.(map[string]any); ok {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

// anyNotEqual reports whether any value fails to equal the fixed value.
func anyNotEqual(vals []any, fixed any) bool {
	for _, v := range vals {
		if !deepEqual(v, fixed) {
			return true
		}
	}
	return false
}

// anyNotMatching reports whether any value fails to satisfy the pattern.
func anyNotMatching(vals []any, pattern any) bool {
	for _, v := range vals {
		if !matchesPattern(v, pattern) {
			return true
		}
	}
	return false
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
