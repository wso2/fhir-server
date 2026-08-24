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

package validate

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// Lookup resolves a type name (a complex datatype like "HumanName" or a
// resource type for Resource-valued elements) to its compiled base profile.
// Returning nil skips descent into values of that type.
type Lookup func(typeName string) *Profile

// maxCheckDepth bounds recursion so a pathologically nested body (contained
// resources in contained resources, …) cannot exhaust the stack.
const maxCheckDepth = 64

// CheckTypes walks a resource instance against the profile's element type
// metadata and reports FHIR JSON representation violations as error issues:
//
//   - primitive values whose JSON kind or lexical form does not match the
//     declared type (boolean as 0/"true", integer as 3.1, malformed dates, …)
//   - shape mismatches: arrays where scalars belong and vice versa, objects at
//     primitive elements, primitives at complex elements
//   - empty values: "", null, {}, [], and [null] without an extension fill
//   - primitive-extension (_field) pairing: unknown fields, non-primitive
//     fields, wrong container shape, and non-Element keys
//
// Issue expressions are instance paths with array indices, choice elements
// rendered as ofType() — e.g. Patient.name[0].given[0] and
// Parameters.parameter[0].value.ofType(boolean) — matching how validators
// conventionally report locations.
//
// lookup provides datatype and resource profiles for descent; a nil lookup
// checks only elements described by this profile (root and BackboneElements).
func (p *Profile) CheckTypes(resource map[string]any, lookup Lookup) []Issue {
	if p == nil {
		return nil
	}
	c := &typeChecker{lookup: lookup}
	c.walkObject(resource, p, p.rootType, p.rootType, 0)
	return c.issues
}

type typeChecker struct {
	lookup Lookup
	issues []Issue
}

func (c *typeChecker) issue(expr, format string, args ...any) {
	c.issues = append(c.issues, Issue{
		Severity:    "error",
		Code:        "value",
		Expression:  expr,
		Diagnostics: fmt.Sprintf(format, args...),
	})
}

// walkObject checks every key of obj against the elements of profile at sdPath.
// expr is the instance-path prefix for issue locations.
func (c *typeChecker) walkObject(obj map[string]any, p *Profile, sdPath, expr string, depth int) {
	if depth > maxCheckDepth {
		return
	}
	for key, val := range obj {
		if key == "resourceType" {
			continue
		}
		if strings.HasPrefix(key, "_") {
			c.checkPrimitiveExtension(obj, key, val, p, sdPath, expr, depth)
			continue
		}

		info, choiceType, choiceBase := resolveElement(p, sdPath, key)
		if info == nil {
			// Unknown plain keys are not policed: the walker cannot tell a bad
			// key from an element it lacks the definition for.
			continue
		}
		childExpr := expr + "." + key
		if choiceType != "" {
			childExpr = expr + "." + choiceBase + ".ofType(" + choiceType + ")"
		}
		childSD := sdPath + "." + key
		if choiceType != "" {
			childSD = sdPath + "." + choiceBase + "[x]"
		}

		if info.repeats {
			arr, ok := val.([]any)
			if !ok {
				c.issue(childExpr, "%s: element repeats and must be a JSON array", childExpr)
				continue
			}
			if len(arr) == 0 {
				c.issue(childExpr, "%s: array must not be empty", childExpr)
				continue
			}
			counterpart, _ := obj["_"+key].([]any)
			for i, item := range arr {
				itemExpr := fmt.Sprintf("%s[%d]", childExpr, i)
				if item == nil {
					// A null entry in a primitive array is a placeholder and is
					// only valid when the paired _field carries an extension at
					// the same position.
					if !isPrimitiveType(firstType(info)) || !hasExtensionAt(counterpart, i) {
						c.issue(itemExpr, "%s: null is only allowed in a primitive array alongside an extension at the same position", itemExpr)
					}
					continue
				}
				c.checkValue(item, info, choiceType, p, childSD, itemExpr, depth)
			}
			continue
		}

		if _, isArr := val.([]any); isArr {
			c.issue(childExpr, "%s: element does not repeat and must not be a JSON array", childExpr)
			continue
		}
		if val == nil {
			c.issue(childExpr, "%s: null is not a valid element value", childExpr)
			continue
		}
		c.checkValue(val, info, choiceType, p, childSD, childExpr, depth)
	}
}

// checkValue checks one non-null, non-array instance value against its
// element's declared type and descends into complex values.
func (c *typeChecker) checkValue(val any, info *elemInfo, choiceType string, p *Profile, childSD, expr string, depth int) {
	// contentReference elements (BackboneElement reuse) carry no type; their
	// children live under the referenced path of the same profile.
	if len(info.types) == 0 && info.contentRef != "" {
		if m, ok := val.(map[string]any); ok {
			if len(m) == 0 {
				c.issue(expr, "%s: object must not be empty", expr)
				return
			}
			c.walkObject(m, p, info.contentRef, expr, depth+1)
		} else {
			c.issue(expr, "%s: expected a JSON object", expr)
		}
		return
	}

	typeCode := choiceType
	if typeCode == "" {
		typeCode = firstType(info)
	}
	if typeCode == "" {
		return
	}

	if check, prim := primitiveChecks[typeCode]; prim {
		if !check(val) {
			c.issue(expr, "%s: value is not a valid %s", expr, typeCode)
		}
		return
	}

	m, ok := val.(map[string]any)
	if !ok {
		c.issue(expr, "%s: expected a JSON object for type %s", expr, typeCode)
		return
	}
	if len(m) == 0 {
		c.issue(expr, "%s: object must not be empty", expr)
		return
	}

	switch typeCode {
	case "BackboneElement", "Element":
		c.walkObject(m, p, childSD, expr, depth+1)
	case "Resource", "DomainResource":
		rt, _ := m["resourceType"].(string)
		if rt == "" {
			c.issue(expr, "%s: nested resource is missing resourceType", expr)
			return
		}
		if c.lookup != nil {
			if sub := c.lookup(rt); sub != nil {
				c.walkObject(m, sub, rt, expr, depth+1)
			}
		}
	default:
		if c.lookup != nil {
			if dt := c.lookup(typeCode); dt != nil {
				c.walkObject(m, dt, typeCode, expr, depth+1)
			}
		}
	}
}

// checkPrimitiveExtension validates a "_field" key: the JSON sibling that
// carries id/extension for a primitive element. The reported expression points
// at the base element (Patient.gender for _gender), matching convention.
func (c *typeChecker) checkPrimitiveExtension(obj map[string]any, key string, val any, p *Profile, sdPath, expr string, depth int) {
	base := key[1:]
	info, choiceType, choiceBase := resolveElement(p, sdPath, base)
	baseExpr := expr + "." + base
	if choiceType != "" {
		baseExpr = expr + "." + choiceBase + ".ofType(" + choiceType + ")"
	}
	if info == nil {
		c.issue(baseExpr, "%s: unknown element %q", expr, key)
		return
	}
	typeCode := choiceType
	if typeCode == "" {
		typeCode = firstType(info)
	}
	if !isPrimitiveType(typeCode) {
		c.issue(baseExpr, "%s: %q is only valid for primitive elements, and %s is of type %s", expr, key, base, typeCode)
		return
	}

	if info.repeats {
		arr, ok := val.([]any)
		if !ok {
			c.issue(baseExpr, "%s: %q must be a JSON array because %s repeats", expr, key, base)
			return
		}
		for i, item := range arr {
			if item == nil {
				continue // null = no id/extension for the element at this position
			}
			c.checkElementObject(item, fmt.Sprintf("%s[%d]", baseExpr, i), depth)
		}
		return
	}
	c.checkElementObject(val, baseExpr, depth)
}

// checkElementObject validates the Element carried by a _field entry: a
// non-empty object holding only id and/or extension.
func (c *typeChecker) checkElementObject(val any, expr string, depth int) {
	m, ok := val.(map[string]any)
	if !ok {
		c.issue(expr, "%s: primitive element id/extension must be a JSON object", expr)
		return
	}
	if len(m) == 0 {
		c.issue(expr, "%s: object must not be empty", expr)
		return
	}
	for k, v := range m {
		switch k {
		case "id":
			if _, ok := v.(string); !ok {
				c.issue(expr, "%s: element id must be a string", expr)
			}
		case "extension":
			arr, ok := v.([]any)
			if !ok {
				c.issue(expr, "%s: extension must be a JSON array", expr)
				continue
			}
			if c.lookup == nil {
				continue
			}
			ext := c.lookup("Extension")
			if ext == nil {
				continue
			}
			for i, e := range arr {
				em, ok := e.(map[string]any)
				if !ok {
					c.issue(fmt.Sprintf("%s.extension[%d]", expr, i), "%s: extension entry must be a JSON object", expr)
					continue
				}
				c.walkObject(em, ext, "Extension", fmt.Sprintf("%s.extension[%d]", expr, i), depth+1)
			}
		default:
			c.issue(expr, "%s: %q is not valid inside a primitive element's id/extension object", expr, k)
		}
	}
}

// resolveElement finds the element for an instance key at sdPath: a direct
// child, or a choice element ("value[x]") matched by type-suffix
// ("valueBoolean" → value[x] with type boolean).
func resolveElement(p *Profile, sdPath, key string) (info *elemInfo, choiceType, choiceBase string) {
	if e, ok := p.elements[sdPath+"."+key]; ok {
		return &e, "", ""
	}
	for _, ch := range p.choices[sdPath] {
		if !strings.HasPrefix(key, ch.base) || len(key) <= len(ch.base) {
			continue
		}
		rem := key[len(ch.base):]
		e, ok := p.elements[ch.path]
		if !ok {
			continue
		}
		for _, t := range e.types {
			if strings.EqualFold(t, rem) {
				return &e, t, ch.base
			}
		}
	}
	return nil, "", ""
}

func firstType(info *elemInfo) string {
	if len(info.types) == 0 {
		return ""
	}
	return info.types[0]
}

func isPrimitiveType(typeCode string) bool {
	_, ok := primitiveChecks[typeCode]
	return ok
}

// hasExtensionAt reports whether the _field counterpart array carries an
// extension object at position i — the only thing that legitimizes a null in
// the paired primitive array.
func hasExtensionAt(counterpart []any, i int) bool {
	if i >= len(counterpart) {
		return false
	}
	m, ok := counterpart[i].(map[string]any)
	if !ok {
		return false
	}
	ext, ok := m["extension"].([]any)
	return ok && len(ext) > 0
}

// normalizeTypeCode maps the FHIRPath system types that R4 snapshots use for
// primitive value elements (Extension.url, Element.id, …) to their FHIR
// primitive equivalents.
func normalizeTypeCode(code string) string {
	if mapped, ok := systemTypes[code]; ok {
		return mapped
	}
	return code
}

var systemTypes = map[string]string{
	"http://hl7.org/fhirpath/System.String":   "string",
	"http://hl7.org/fhirpath/System.Boolean":  "boolean",
	"http://hl7.org/fhirpath/System.Integer":  "integer",
	"http://hl7.org/fhirpath/System.Decimal":  "decimal",
	"http://hl7.org/fhirpath/System.Date":     "date",
	"http://hl7.org/fhirpath/System.DateTime": "dateTime",
	"http://hl7.org/fhirpath/System.Time":     "time",
}

// ─── Primitive lexical rules (FHIR R4 datatypes) ───────────────────────────────

// Fractional seconds are capped at nanosecond precision (9 digits), tighter
// than the spec regexes' unbounded [0-9]+.
var (
	reDate     = regexp.MustCompile(`^([0-9]([0-9]([0-9][1-9]|[1-9]0)|[1-9]00)|[1-9]000)(-(0[1-9]|1[0-2])(-(0[1-9]|[1-2][0-9]|3[0-1]))?)?$`)
	reDateTime = regexp.MustCompile(`^([0-9]([0-9]([0-9][1-9]|[1-9]0)|[1-9]00)|[1-9]000)(-(0[1-9]|1[0-2])(-(0[1-9]|[1-2][0-9]|3[0-1])(T([01][0-9]|2[0-3]):[0-5][0-9]:([0-5][0-9]|60)(\.[0-9]{1,9})?(Z|(\+|-)((0[0-9]|1[0-3]):[0-5][0-9]|14:00)))?)?)?$`)
	reInstant  = regexp.MustCompile(`^([0-9]([0-9]([0-9][1-9]|[1-9]0)|[1-9]00)|[1-9]000)-(0[1-9]|1[0-2])-(0[1-9]|[1-2][0-9]|3[0-1])T([01][0-9]|2[0-3]):[0-5][0-9]:([0-5][0-9]|60)(\.[0-9]{1,9})?(Z|(\+|-)((0[0-9]|1[0-3]):[0-5][0-9]|14:00))$`)
	reTime     = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]:([0-5][0-9]|60)(\.[0-9]{1,9})?$`)
	reCode     = regexp.MustCompile(`^[^\s]+(\s[^\s]+)*$`)
	reID       = regexp.MustCompile(`^[A-Za-z0-9\-\.]{1,64}$`)
	reOID      = regexp.MustCompile(`^urn:oid:[0-2](\.(0|[1-9][0-9]*))+$`)
	reUUID     = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	reBase64   = regexp.MustCompile(`^(\s*[0-9a-zA-Z+/=]{4}\s*)+$`)
	reNoSpace  = regexp.MustCompile(`^\S+$`)
)

func stringMatching(re *regexp.Regexp) func(any) bool {
	return func(v any) bool {
		s, ok := v.(string)
		return ok && re.MatchString(s)
	}
}

// dateLike combines a date-bearing regex with a calendar check, so lexically
// well-formed but impossible dates (2023-02-29, 2024-02-31) are rejected.
func dateLike(re *regexp.Regexp) func(any) bool {
	return func(v any) bool {
		s, ok := v.(string)
		return ok && re.MatchString(s) && calendarDayValid(s)
	}
}

// calendarDayValid verifies the day-of-month of a YYYY-MM-DD prefix actually
// exists. Values without a day part (partial dates) pass through.
func calendarDayValid(s string) bool {
	if len(s) < 10 || s[4] != '-' || s[7] != '-' {
		return true
	}
	_, err := time.Parse("2006-01-02", s[:10])
	return err == nil
}

func integerInRange(min, max float64) func(any) bool {
	return func(v any) bool {
		f, ok := v.(float64)
		return ok && f == math.Trunc(f) && f >= min && f <= max
	}
}

// primitiveChecks maps every FHIR R4 primitive type to its JSON-kind and
// lexical validity check. Membership in this map is also what classifies a
// type code as primitive.
var primitiveChecks = map[string]func(any) bool{
	"boolean": func(v any) bool { _, ok := v.(bool); return ok },
	"decimal": func(v any) bool { _, ok := v.(float64); return ok },

	"integer":     integerInRange(math.MinInt32, math.MaxInt32),
	"unsignedInt": integerInRange(0, math.MaxInt32),
	"positiveInt": integerInRange(1, math.MaxInt32),

	"string":   func(v any) bool { s, ok := v.(string); return ok && len(s) > 0 },
	"markdown": func(v any) bool { s, ok := v.(string); return ok && len(s) > 0 },
	"xhtml":    func(v any) bool { s, ok := v.(string); return ok && len(s) > 0 },

	"uri":       stringMatching(reNoSpace),
	"url":       stringMatching(reNoSpace),
	"canonical": stringMatching(reNoSpace),
	"code":      stringMatching(reCode),
	"id":        stringMatching(reID),
	"oid":       stringMatching(reOID),
	"uuid":      stringMatching(reUUID),

	"date":         dateLike(reDate),
	"dateTime":     dateLike(reDateTime),
	"instant":      dateLike(reInstant),
	"time":         stringMatching(reTime),
	"base64Binary": stringMatching(reBase64),
}
