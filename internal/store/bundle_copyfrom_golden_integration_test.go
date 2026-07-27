//go:build integration

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

package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2/fhir-server/internal/store"
	"github.com/wso2/fhir-server/internal/testutil"
)

// ─── W3: Golden equivalence test (write-path optimization) ─────────────────────
//
// This test imports one transaction Bundle exercising ≥3 resource types, an
// update (delete-then-reindex), a conditional create, and quantity + date +
// token + reference + composite params, then snapshots the ordered contents of
// resources, resource_history, and every sp_* table into a golden file.
//
// The snapshot is the write-path analogue of a default-equivalence test: it is
// recorded against the current (per-row INSERT) implementation and must stay
// byte-identical through the CopyFrom batching refactor (W1) and the
// conditional-create LIMIT-2 change (W2).
//
// Two sources of run-to-run nondeterminism are normalized so the golden is
// stable and still meaningful:
//
//   - Surrogate BIGSERIAL ids are excluded — they are sequence-assigned and
//     differ across runs and across insertion strategies (per-row INSERT vs
//     CopyFrom). Rows are ordered by their semantic columns instead.
//   - Wallclock timestamps set from time.Now() at write time (resources.last_updated,
//     resource_history.recorded_at, sp_*.last_updated, and the embedded
//     meta.lastUpdated inside resource_json) are replaced with the literal token
//     <TS>. Input-derived timestamps that encode data (sp_date.value_low/high)
//     are kept verbatim, because those must match exactly.
//
// The timestamp normalization is paired with explicit invariant checks
// (assertRecencyMirrors) that every sp_*.last_updated equals its owning
// resource's resources.last_updated — the exact property the recency-mirror
// columns rely on and the one most at risk if batched extraction mis-assigns a
// row's timestamp. Together the golden (content) and the invariants (timestamp
// correctness) pin the write path's observable output.
//
// Regenerate the golden after an intentional change with:
//
//	UPDATE_GOLDEN=1 go test -tags integration ./internal/store -run TestBundleWritePath_GoldenSnapshot

func TestBundleWritePath_GoldenSnapshot(t *testing.T) {
	pool := testutil.MustSeededDB(t)
	reg := testutil.MustRegistry(t, pool)
	s := store.New(pool, reg)
	ctx := context.Background()

	// A pre-existing Patient the Bundle updates (PUT), so the snapshot covers the
	// delete-then-reindex path, not just fresh inserts.
	if _, err := s.Create(ctx, "Patient", patientBody("pat-update", "Original", "OG", "1980-01-01")); err != nil {
		t.Fatalf("seed Patient/pat-update: %v", err)
	}
	// A pre-existing Organization the Bundle's conditional create should match
	// (If-None-Exist identifier=...), resolving to a skip rather than a new write.
	if _, err := s.Create(ctx, "Organization", map[string]any{
		"resourceType": "Organization",
		"id":           "org-existing",
		"name":         "Existing Org",
		"identifier": []any{map[string]any{
			"system": "http://example.org/orgs",
			"value":  "ORG-1",
		}},
	}); err != nil {
		t.Fatalf("seed Organization/org-existing: %v", err)
	}

	if _, err := s.ExecuteBundle(ctx, "transaction", "", goldenBundleEntries()); err != nil {
		t.Fatalf("ExecuteBundle: %v", err)
	}

	got := snapshotWritePath(t, pool)

	goldenPath := filepath.Join("testdata", "golden_write_path_snapshot.txt")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote golden snapshot to %s (%d bytes)", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (regenerate with UPDATE_GOLDEN=1): %v", err)
	}
	if got != string(want) {
		t.Errorf("write-path snapshot differs from golden.\n%s", firstDiff(string(want), got))
	}

	assertRecencyMirrors(t, pool)
}

// goldenBundleEntries builds the fixture transaction Bundle.
func goldenBundleEntries() []store.BundleEntryRequest {
	return []store.BundleEntryRequest{
		// POST Patient — token (identifier), string (name), date (birthDate).
		{
			FullURL:  "urn:uuid:patient-new",
			Method:   "POST",
			URL:      "Patient",
			Resource: patientBody("pat-new", "Newman", "Alfred", "1991-06-15"),
		},
		// POST Observation — reference (subject → the new Patient), token (code,
		// category), quantity (valueQuantity), date (effectiveDateTime), and a
		// component carrying a code + valueQuantity (token+quantity composite).
		{
			FullURL: "urn:uuid:obs-new",
			Method:  "POST",
			URL:     "Observation",
			Resource: map[string]any{
				"resourceType": "Observation",
				"id":           "obs-new",
				"status":       "final",
				"category": []any{map[string]any{
					"coding": []any{map[string]any{
						"system": "http://terminology.hl7.org/CodeSystem/observation-category",
						"code":   "vital-signs",
					}},
				}},
				"code": map[string]any{
					"coding": []any{map[string]any{
						"system":  "http://loinc.org",
						"code":    "8867-4",
						"display": "Heart rate",
					}},
				},
				"subject":           map[string]any{"reference": "urn:uuid:patient-new"},
				"effectiveDateTime": "2015-02-07T13:28:17-05:00",
				"valueQuantity": map[string]any{
					"value":  72,
					"unit":   "beats/minute",
					"system": "http://unitsofmeasure.org",
					"code":   "/min",
				},
				"component": []any{map[string]any{
					"code": map[string]any{
						"coding": []any{map[string]any{
							"system": "http://loinc.org",
							"code":   "8480-6",
						}},
					},
					"valueQuantity": map[string]any{
						"value":  107,
						"system": "http://unitsofmeasure.org",
						"code":   "mm[Hg]",
					},
				}},
			},
		},
		// POST Organization with If-None-Exist that matches the seeded org →
		// resolves to a skip (no new write), exercising the conditional-create path.
		{
			Method:      "POST",
			URL:         "Organization",
			IfNoneExist: "identifier=http://example.org/orgs|ORG-1",
			Resource: map[string]any{
				"resourceType": "Organization",
				"name":         "Should Not Be Created",
				"identifier": []any{map[string]any{
					"system": "http://example.org/orgs",
					"value":  "ORG-1",
				}},
			},
		},
		// POST Organization with If-None-Exist that matches nothing → a real create.
		{
			FullURL:     "urn:uuid:org-new",
			Method:      "POST",
			URL:         "Organization",
			IfNoneExist: "identifier=http://example.org/orgs|ORG-2",
			Resource: map[string]any{
				"resourceType": "Organization",
				"id":           "org-new",
				"name":         "Brand New Org",
				"identifier": []any{map[string]any{
					"system": "http://example.org/orgs",
					"value":  "ORG-2",
				}},
			},
		},
		// PUT — update the pre-existing Patient (delete-then-reindex path).
		{
			Method:   "PUT",
			URL:      "Patient/pat-update",
			Resource: patientBody("pat-update", "Updated", "Newname", "1980-01-02"),
		},
	}
}

// patientBody builds a Patient with a token (identifier), a string (name), and a
// date (birthDate) search parameter populated.
func patientBody(id, family, given, birthDate string) map[string]any {
	body := map[string]any{
		"resourceType": "Patient",
		"gender":       "other",
		"birthDate":    birthDate,
		"identifier": []any{map[string]any{
			"system": "http://example.org/mrn",
			"value":  "MRN-" + family,
		}},
		"name": []any{map[string]any{
			"family": family,
			"given":  []any{given},
		}},
		"telecom": []any{map[string]any{
			"system": "phone",
			"value":  "555-0100",
		}},
	}
	if id != "" {
		body["id"] = id
	}
	return body
}

// snapshotTable is one table's projection into the golden snapshot: a label, and
// a query that yields exactly one text column ("line") per row, already
// normalized for surrogate ids and wallclock timestamps, ordered deterministically.
type snapshotTable struct {
	label string
	query string
}

// tsExpr replaces a wallclock timestamp column with the literal token so the
// golden is stable across runs. NULLs collapse to the same token.
func tsExpr(col string) string { return "'<TS>'" }

// jsonExpr strips the injected meta.lastUpdated (the only nondeterministic field
// in resource_json) while keeping meta.versionId and all input data verbatim.
func jsonExpr(col string) string {
	return "regexp_replace(" + col + ", '\"lastUpdated\":\"[^\"]*\"', '\"lastUpdated\":\"<TS>\"')"
}

func snapshotWritePath(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()

	tables := []snapshotTable{
		{"resources", `SELECT concat_ws('|', fhir_id, resource_type, version_id::text, is_deleted::text, ` + tsExpr("last_updated") + `, ` + jsonExpr("resource_json") + `) FROM resources`},
		{"resource_history", `SELECT concat_ws('|', fhir_id, resource_type, version_id::text, operation, ` + tsExpr("recorded_at") + `, ` + jsonExpr("resource_json") + `) FROM resource_history`},
		{"sp_string", `SELECT concat_ws('|', resource_id, resource_type, param_name, coalesce(value_exact,''), coalesce(value_lower,'')) FROM sp_string`},
		{"sp_token", `SELECT concat_ws('|', resource_id, resource_type, param_name, coalesce(system,''), coalesce(code,''), coalesce(display,''), ` + tsExpr("last_updated") + `) FROM sp_token`},
		{"sp_date", `SELECT concat_ws('|', resource_id, resource_type, param_name, value_low::text, value_high::text, value_precision, ` + tsExpr("last_updated") + `) FROM sp_date`},
		{"sp_number", `SELECT concat_ws('|', resource_id, resource_type, param_name, value::text, value_low::text, value_high::text, ` + tsExpr("last_updated") + `) FROM sp_number`},
		{"sp_quantity", `SELECT concat_ws('|', resource_id, resource_type, param_name, value::text, value_low::text, value_high::text, coalesce(system,''), coalesce(code,''), coalesce(canonical_value::text,''), coalesce(canonical_units,''), ` + tsExpr("last_updated") + `) FROM sp_quantity`},
		{"sp_uri", `SELECT concat_ws('|', resource_id, resource_type, param_name, value) FROM sp_uri`},
		{"sp_reference", `SELECT concat_ws('|', resource_id, resource_type, param_name, coalesce(target_type,''), coalesce(target_id,''), coalesce(target_version_id::text,''), coalesce(target_url,''), coalesce(identifier_system,''), coalesce(identifier_value,''), coalesce(display,''), ` + tsExpr("last_updated") + `) FROM sp_reference`},
		{"sp_composite_token_quantity", `SELECT concat_ws('|', resource_id, resource_type, param_name, coalesce(system,''), code, value::text, value_low::text, value_high::text, coalesce(qty_system,''), coalesce(qty_code,''), ` + tsExpr("last_updated") + `) FROM sp_composite_token_quantity`},
	}

	var b strings.Builder
	for _, tbl := range tables {
		rows, err := pool.Query(ctx, tbl.query)
		if err != nil {
			t.Fatalf("snapshot %s: %v", tbl.label, err)
		}
		var lines []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatalf("scan %s: %v", tbl.label, err)
			}
			lines = append(lines, line)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate %s: %v", tbl.label, err)
		}
		// Order by semantic content (not surrogate id) so the snapshot is stable
		// regardless of insertion strategy.
		sortStrings(lines)
		b.WriteString("=== ")
		b.WriteString(tbl.label)
		b.WriteString(" (")
		b.WriteString(itoa(len(lines)))
		b.WriteString(" rows) ===\n")
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// assertRecencyMirrors verifies every sp_*.last_updated equals its owning
// resource's resources.last_updated. This is the invariant the <TS> normalization
// deliberately hides from the golden, checked here directly so batched extraction
// cannot silently break the recency-mirror columns.
func assertRecencyMirrors(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	spTablesWithRecency := []string{"sp_token", "sp_date", "sp_number", "sp_quantity", "sp_reference", "sp_composite_token_quantity"}
	for _, tbl := range spTablesWithRecency {
		var mismatched int
		q := `SELECT count(*) FROM ` + tbl + ` s
		       JOIN resources r
		         ON r.tenant_id = s.tenant_id AND r.fhir_id = s.resource_id AND r.resource_type = s.resource_type
		      WHERE s.last_updated <> r.last_updated`
		if err := pool.QueryRow(ctx, q).Scan(&mismatched); err != nil {
			t.Fatalf("recency check %s: %v", tbl, err)
		}
		if mismatched != 0 {
			t.Errorf("%s: %d rows whose last_updated does not mirror resources.last_updated", tbl, mismatched)
		}
	}
}

// ─── tiny local helpers (avoid extra imports in the golden file) ───────────────

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// firstDiff returns a compact description of where want and got first diverge.
func firstDiff(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	n := len(wl)
	if len(gl) < n {
		n = len(gl)
	}
	for i := 0; i < n; i++ {
		if wl[i] != gl[i] {
			return "first diff at line " + itoa(i+1) + ":\n  want: " + wl[i] + "\n   got: " + gl[i]
		}
	}
	if len(wl) != len(gl) {
		return "snapshots differ in length: want " + itoa(len(wl)) + " lines, got " + itoa(len(gl)) + " lines"
	}
	return "snapshots differ (no line-level diff found)"
}
