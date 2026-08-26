package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var windowMatrixPath = moduleRootPath("testdata", "clickhouse-window-matrix.json")

type windowMatrixCell struct {
	ID             string `json:"id"`
	Axis           string `json:"axis"`
	SQL            string `json:"sql"`
	Chgen          string `json:"chgen"`
	Analysis       string `json:"analysis"`
	Execution      string `json:"execution"`
	Type           string `json:"type,omitempty"`
	GeneratedValue bool   `json:"generated_value,omitempty"`
}

type windowMatrixArtifact struct {
	Version           int                `json:"version"`
	ClickHouseVersion string             `json:"clickhouse_version"`
	Cells             []windowMatrixCell `json:"cells"`
	ExecProbeLinks    map[string]string  `json:"exec_probe_links"`
}

func currentWindowMatrix() windowMatrixArtifact {
	return windowMatrixArtifact{Version: 1, ClickHouseVersion: MeasuredCHVersion, ExecProbeLinks: map[string]string{
		"placement-row-number-over": "probe_window_row_number",
		"frame-rows":                "probe_window_sum_rows",
		"policy-lag-in-frame":       "probe_window_lag_in_frame",
		"result-aggregate-window":   "probe_window_sum_rows",
		"roster-lag":                "probe_window_lag_frame_difference",
		"roster-lead":               "probe_window_lead_frame_difference",
	}, Cells: []windowMatrixCell{
		{"placement-row-number-bare", "placement", "row_number()", "refuse", "refuse", "refuse", "", false},
		{"placement-row-number-over", "placement", "row_number() OVER (ORDER BY i32)", "accept", "accept", "accept", "UInt64", true},
		{"placement-first-value-bare", "placement", "first_value(i32)", "accept", "accept", "accept", "Int32", false},
		{"scope-known-columns", "scope", "sum(i32) OVER (PARTITION BY u8 ORDER BY i32)", "accept", "accept", "accept", "Int64", false},
		{"scope-unknown-partition", "scope", "sum(i32) OVER (PARTITION BY missing ORDER BY i32)", "refuse", "may_accept", "refuse", "", false},
		{"scope-unknown-order", "scope", "sum(i32) OVER (PARTITION BY u8 ORDER BY missing)", "refuse", "may_accept", "refuse", "", false},
		{"scope-named-derived", "scope", "sum(i32) OVER w2 WINDOW w1 AS (PARTITION BY u8), w2 AS (w1 ORDER BY i32)", "accept", "accept", "accept", "Int64", false},
		{"scope-named-missing", "scope", "sum(i32) OVER missing", "refuse", "refuse", "refuse", "", false},
		{"scope-named-wrong-case", "scope", "sum(i32) OVER W WINDOW w AS (ORDER BY i32)", "refuse", "refuse", "refuse", "", false},
		{"scope-named-distinct-case", "scope", "sum(i32) OVER W WINDOW W AS (PARTITION BY u8), w AS (ORDER BY i32)", "accept", "accept", "accept", "Int64", false},
		{"frame-rows", "frame", "sum(i32) OVER (ORDER BY i32 ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)", "accept", "accept", "accept", "Int64", true},
		{"frame-range", "frame", "sum(i32) OVER (ORDER BY i32 RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", "accept", "accept", "accept", "Int64", false},
		{"frame-inverted", "frame", "sum(i32) OVER (ORDER BY i32 ROWS BETWEEN 2 FOLLOWING AND 1 FOLLOWING)", "refuse", "may_accept", "refuse", "", false},
		{"frame-range-without-order", "frame", "sum(i32) OVER (RANGE BETWEEN 1 PRECEDING AND CURRENT ROW)", "refuse", "may_accept", "refuse", "", false},
		{"frame-groups", "frame", "sum(i32) OVER (ORDER BY i32 GROUPS BETWEEN 1 PRECEDING AND CURRENT ROW)", "refuse", "may_accept", "refuse", "", false},
		{"policy-lag-frame", "policy", "lag(i32) OVER (ORDER BY i32 ROWS BETWEEN CURRENT ROW AND CURRENT ROW)", "refuse", "may_accept", "refuse", "", false},
		{"policy-lag-in-frame", "policy", "lagInFrame(i32, 1) OVER (ORDER BY i32 ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)", "accept", "accept", "accept", "Int32", true},
		{"policy-ntile-no-order", "policy", "ntile(2) OVER ()", "refuse", "may_accept", "refuse", "", false},
		{"result-nullable", "result", "lagInFrame(ni32, 1) OVER (ORDER BY i32)", "accept", "accept", "accept", "Nullable(Int32)", false},
		{"result-geo", "result", "lagInFrame(api_point, 1) OVER (ORDER BY i32)", "accept", "accept", "accept", "Tuple(Float64, Float64)", false},
		{"result-aggregate-window", "result", "sum(i32) OVER (ORDER BY i32 ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)", "accept", "accept", "accept", "Int64", true},
		{"roster-lag", "policy", "lag(i32, 1) OVER (ORDER BY i32)", "accept", "accept", "accept", "Int32", true},
		{"roster-lead", "policy", "lead(i32, 1) OVER (ORDER BY i32)", "accept", "accept", "accept", "Int32", true},
		{"roster-nth-value", "policy", "nth_value(i32, 2) OVER (ORDER BY i32 ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)", "accept", "accept", "accept", "Int32", false},
		{"roster-ntile", "policy", "ntile(2) OVER (ORDER BY i32)", "accept", "accept", "accept", "UInt64", false},
		{"roster-first-respect-nulls", "result", "firstValueRespectNulls(ni32) OVER (ORDER BY i32)", "accept", "accept", "accept", "Nullable(Int32)", false},
		{"roster-last-respect-nulls-bare", "placement", "last_value_respect_nulls(ni32)", "accept", "accept", "accept", "Nullable(Int32)", false},
	}}
}

func validateWindowMatrix(artifact windowMatrixArtifact) error {
	required := map[string]bool{"placement": false, "scope": false, "frame": false, "policy": false, "result": false}
	seen := make(map[string]bool)
	generated := 0
	complements := make(map[string]map[string]bool)
	for _, cell := range artifact.Cells {
		if cell.ID == "" || seen[cell.ID] {
			return fmt.Errorf("window matrix has an empty or duplicate cell ID %q", cell.ID)
		}
		seen[cell.ID] = true
		if _, found := required[cell.Axis]; !found {
			return fmt.Errorf("window matrix cell %s has unknown axis %q", cell.ID, cell.Axis)
		}
		required[cell.Axis] = true
		if complements[cell.Axis] == nil {
			complements[cell.Axis] = make(map[string]bool)
		}
		complements[cell.Axis][cell.Chgen] = true
		if cell.GeneratedValue {
			generated++
			if artifact.ExecProbeLinks[cell.ID] == "" {
				return fmt.Errorf("window matrix generated cell %s has no exec probe link", cell.ID)
			}
		}
	}
	for axis, found := range required {
		if !found {
			return fmt.Errorf("window matrix has no %s axis", axis)
		}
	}
	for _, axis := range []string{"placement", "scope", "frame", "policy"} {
		if !complements[axis]["accept"] || !complements[axis]["refuse"] {
			return fmt.Errorf("window matrix axis %s has no accept/refuse complement", axis)
		}
	}
	if generated < 3 {
		return fmt.Errorf("window matrix has %d generated value cells, want at least 3", generated)
	}
	for cellID := range artifact.ExecProbeLinks {
		if !seen[cellID] {
			return fmt.Errorf("window matrix exec probe link names unknown cell %s", cellID)
		}
	}
	return nil
}

func validateWindowMatrixAgainstChgen(t *testing.T, artifact windowMatrixArtifact, schema *Schema) error {
	for _, cell := range artifact.Cells {
		expression, clause := cell.SQL, ""
		if index := strings.Index(expression, " WINDOW "); index >= 0 {
			clause = expression[index:]
			expression = expression[:index]
		}
		actual, err := inferWindowQueryType(t, schema, "SELECT "+expression+" AS x FROM probe"+clause)
		if cell.Chgen == "refuse" {
			if err == nil {
				return fmt.Errorf("window matrix cell %s expected chgen refusal", cell.ID)
			}
			continue
		}
		if err != nil || actual.String() != cell.Type {
			return fmt.Errorf("window matrix cell %s inferred %s, %v; want %s", cell.ID, actual.String(), err, cell.Type)
		}
	}
	return nil
}

func windowMatrixSchema(t *testing.T) *Schema {
	return schemaFromDDL(t, "CREATE TABLE probe (i32 Int32, u8 UInt8, ni32 Nullable(Int32), s String, dec Decimal(18,4), dt64 DateTime64(3), api_point Point) ENGINE = Memory")
}

func TestWindowMatrixExecutesAgainstChgen(t *testing.T) {
	if err := validateWindowMatrixAgainstChgen(t, currentWindowMatrix(), windowMatrixSchema(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedWindowMatrixIsCurrent(t *testing.T) {
	artifact := currentWindowMatrix()
	if err := validateWindowMatrix(artifact); err != nil {
		t.Fatal(err)
	}
	sort.Slice(artifact.Cells, func(i, j int) bool { return artifact.Cells[i].ID < artifact.Cells[j].ID })
	want, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("CHGEN_UPDATE_WINDOW_MATRIX") == "1" {
		if err := os.WriteFile(windowMatrixPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(windowMatrixPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("window matrix artifact is stale; run with CHGEN_UPDATE_WINDOW_MATRIX=1")
	}
}

func TestWindowMatrixCoverageGateDetectsMissingAxis(t *testing.T) {
	artifact := currentWindowMatrix()
	filtered := artifact.Cells[:0]
	for _, cell := range artifact.Cells {
		if cell.Axis != "scope" {
			filtered = append(filtered, cell)
		}
	}
	artifact.Cells = filtered
	if err := validateWindowMatrix(artifact); err == nil {
		t.Fatal("window matrix accepted a missing scope axis")
	}
}

func TestWindowMatrixMutationGates(t *testing.T) {
	schema := windowMatrixSchema(t)
	t.Run("expectation", func(t *testing.T) {
		a := currentWindowMatrix()
		a.Cells[0].Chgen = "accept"
		if err := validateWindowMatrixAgainstChgen(t, a, schema); err == nil {
			t.Fatal("changed expectation passed")
		}
	})
	t.Run("sql", func(t *testing.T) {
		a := currentWindowMatrix()
		a.Cells[1].SQL = "missing() OVER ()"
		if err := validateWindowMatrixAgainstChgen(t, a, schema); err == nil {
			t.Fatal("changed SQL passed")
		}
	})
	t.Run("complement", func(t *testing.T) {
		a := currentWindowMatrix()
		for i := range a.Cells {
			if a.Cells[i].Axis == "frame" && a.Cells[i].Chgen == "refuse" {
				a.Cells[i].Chgen = "accept"
			}
		}
		if err := validateWindowMatrix(a); err == nil {
			t.Fatal("missing complement passed")
		}
	})
	t.Run("probe-link", func(t *testing.T) {
		a := currentWindowMatrix()
		delete(a.ExecProbeLinks, "roster-lag")
		if err := validateWindowMatrix(a); err == nil {
			t.Fatal("missing probe link passed")
		}
	})
}
