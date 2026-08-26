package conformance

import (
	"fmt"
	"reflect"
)

// FinalResult records one output column in source order.
type FinalResult struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// FinalCell records one FINAL scope or engine boundary.
type FinalCell struct {
	ID            string        `json:"id"`
	Role          string        `json:"role"`
	Query         string        `json:"query"`
	Chgen         string        `json:"chgen"`
	ChgenError    string        `json:"chgen_error,omitempty"`
	Results       []FinalResult `json:"results,omitempty"`
	Analysis      string        `json:"analysis"`
	AnalysisCode  int           `json:"analysis_code,omitempty"`
	Execution     string        `json:"execution"`
	ExecutionCode int           `json:"execution_code,omitempty"`
	Owner         string        `json:"owner,omitempty"`
	ValueProbe    string        `json:"value_probe,omitempty"`
}

// FinalArtifact records the executable FINAL matrix.
type FinalArtifact struct {
	Version           int         `json:"version"`
	ClickHouseVersion string      `json:"clickhouse_version"`
	Fixture           string      `json:"fixture"`
	Cells             []FinalCell `json:"cells"`
}

// FinalFixtureDDL creates real MergeTree-family columns for FINAL checks.
const FinalFixtureDDL = `
CREATE TABLE final_rows (id UInt64, value Int32, version UInt64)
ENGINE = ReplacingMergeTree(version) ORDER BY id;
CREATE TABLE final_other (id UInt64, value Int16, version UInt64)
ENGINE = ReplacingMergeTree(version) ORDER BY id;
CREATE TABLE final_summing (id UInt64, value Int32)
ENGINE = SummingMergeTree ORDER BY id;
CREATE TABLE final_aggregating (id UInt64, value SimpleAggregateFunction(sum, Int64))
ENGINE = AggregatingMergeTree ORDER BY id;
CREATE TABLE final_collapsing (id UInt64, value Int32, sign Int8)
ENGINE = CollapsingMergeTree(sign) ORDER BY id;
CREATE TABLE final_versioned (id UInt64, value Int32, sign Int8, version UInt64)
ENGINE = VersionedCollapsingMergeTree(sign, version) ORDER BY id;
CREATE TABLE final_plain (id UInt64, value Int32)
ENGINE = MergeTree ORDER BY id;
CREATE TABLE final_memory (id UInt64, value Int32) ENGINE = Memory;
CREATE TABLE final_log (id UInt64, value Int32) ENGINE = Log;
CREATE TABLE final_sink (id UInt64, value Int32) ENGINE = Memory;
`

// FinalFixtureSeed gives FINAL a row replacement and collapse witness.
const FinalFixtureSeed = `
INSERT INTO final_rows VALUES (1, 10, 1), (1, 20, 2), (2, 30, 1);
INSERT INTO final_other VALUES (1, 4, 1), (1, 5, 2), (3, 6, 1);
INSERT INTO final_summing VALUES (1, 2), (1, 3), (2, 4);
INSERT INTO final_aggregating VALUES (1, 2), (1, 3), (2, 4);
INSERT INTO final_collapsing VALUES (1, 10, 1), (1, 10, -1), (2, 20, 1);
INSERT INTO final_versioned VALUES (1, 10, 1, 1), (1, 10, -1, 2), (2, 20, 1, 1);
INSERT INTO final_plain VALUES (1, 10);
INSERT INTO final_memory VALUES (1, 10);
INSERT INTO final_log VALUES (1, 10);
`

func finalResult(name, typeName string) []FinalResult {
	return []FinalResult{{Name: name, Type: typeName}}
}

// CurrentFinalArtifact returns the pinned FINAL matrix.
func CurrentFinalArtifact(clickHouseVersion string) FinalArtifact {
	accept := func(id, role, query string, results []FinalResult) FinalCell {
		return FinalCell{ID: id, Role: role, Query: query, Chgen: "accept", Results: results, Analysis: "accept", Execution: "accept"}
	}
	refuse := func(id, role, query, chgenError string, code int) FinalCell {
		return FinalCell{ID: id, Role: role, Query: query, Chgen: "refuse", ChgenError: chgenError, Analysis: "refuse", AnalysisCode: code, Execution: "refuse", ExecutionCode: code}
	}
	cells := []FinalCell{
		accept("from/direct", "from", "SELECT id, value FROM final_rows FINAL ORDER BY id", []FinalResult{{Name: "id", Type: "UInt64"}, {Name: "value", Type: "Int32"}}),
		accept("from/alias-as", "from", "SELECT r.id, r.value FROM final_rows AS r FINAL ORDER BY r.id", []FinalResult{{Name: "id", Type: "UInt64"}, {Name: "value", Type: "Int32"}}),
		accept("from/alias-bare", "from", "SELECT r.id AS id FROM final_rows r FINAL ORDER BY r.id", finalResult("id", "UInt64")),
		accept("from/where", "from", "SELECT value FROM final_rows FINAL WHERE id = 1", finalResult("value", "Int32")),
		refuse("position/alias-after-final", "position", "SELECT r.id AS id FROM final_rows FINAL AS r", "parse SQL", 62),
		accept("engine/replacing", "engine", "SELECT value FROM final_rows FINAL ORDER BY id", finalResult("value", "Int32")),
		accept("engine/summing", "engine", "SELECT value FROM final_summing FINAL ORDER BY id", finalResult("value", "Int32")),
		accept("engine/aggregating", "engine", "SELECT id FROM final_aggregating FINAL ORDER BY id", finalResult("id", "UInt64")),
		accept("engine/collapsing", "engine", "SELECT value FROM final_collapsing FINAL ORDER BY id", finalResult("value", "Int32")),
		accept("engine/versioned-collapsing", "engine", "SELECT value FROM final_versioned FINAL ORDER BY id", finalResult("value", "Int32")),
		refuse("engine/plain-merge-tree", "engine", "SELECT value FROM final_plain FINAL", "engine MergeTree", 181),
		refuse("engine/memory", "engine", "SELECT value FROM final_memory FINAL", "engine Memory", 181),
		refuse("engine/log", "engine", "SELECT value FROM final_log FINAL", "engine Log", 181),
		accept("join/left-final", "join", "SELECT r.id AS id, p.value AS peer_value FROM final_rows AS r FINAL INNER JOIN final_plain AS p ON r.id = p.id", []FinalResult{{Name: "id", Type: "UInt64"}, {Name: "peer_value", Type: "Int32"}}),
		accept("join/both-final", "join", "SELECT r.id AS id, o.value AS other_value FROM final_rows AS r FINAL INNER JOIN final_other AS o FINAL ON r.id = o.id", []FinalResult{{Name: "id", Type: "UInt64"}, {Name: "other_value", Type: "Int16"}}),
		accept("join/comma-first-final", "join", "SELECT r.id AS left_id, p.id AS right_id FROM final_rows AS r FINAL, final_plain AS p", []FinalResult{{Name: "left_id", Type: "UInt64"}, {Name: "right_id", Type: "UInt64"}}),
		refuse("join/plain-final", "join", "SELECT r.id AS left_id, p.id AS right_id FROM final_rows AS r FINAL, final_plain AS p FINAL", "engine MergeTree", 181),
		accept("scope/derived-inner", "scope", "SELECT d.value FROM (SELECT value FROM final_rows FINAL) AS d", finalResult("value", "Int32")),
		refuse("scope/derived-modifier", "scope", "SELECT d.value FROM (SELECT value FROM final_rows) AS d FINAL", "derived table", 1),
		accept("scope/cte-inner", "scope", "WITH q AS (SELECT value FROM final_rows FINAL) SELECT value FROM q", finalResult("value", "Int32")),
		refuse("scope/cte-modifier", "scope", "WITH q AS (SELECT value FROM final_rows) SELECT value FROM q FINAL", "has no engine", 1),
		accept("scope/set-branch", "scope", "SELECT value FROM final_rows FINAL UNION ALL SELECT value FROM final_memory ORDER BY value", finalResult("value", "Int32")),
		refuse("scope/set-invalid-engine", "scope", "SELECT value FROM final_rows FINAL UNION ALL SELECT value FROM final_memory FINAL", "engine Memory", 181),
		accept("subquery/scalar", "subquery", "SELECT (SELECT max(value) FROM final_rows FINAL) AS value", finalResult("value", "Nullable(Int32)")),
		accept("subquery/in", "subquery", "SELECT id IN (SELECT id FROM final_rows FINAL) AS value FROM final_plain", finalResult("value", "UInt8")),
		accept("subquery/exists", "subquery", "SELECT EXISTS(SELECT id FROM final_rows FINAL WHERE value > 0) AS value", finalResult("value", "UInt8")),
		accept("scope/derived-set", "scope", "SELECT d.value FROM (SELECT value FROM final_rows FINAL UNION ALL SELECT toInt32(7) AS value) AS d ORDER BY value", finalResult("value", "Int32")),
	}
	probes := map[string]string{
		"from/direct": "probe_final_replacing", "engine/summing": "probe_final_summing",
		"engine/collapsing": "probe_final_collapsing", "join/both-final": "probe_final_join",
		"scope/cte-inner": "probe_final_cte", "scope/set-branch": "probe_final_set",
	}
	for index := range cells {
		cells[index].ValueProbe = probes[cells[index].ID]
	}
	return FinalArtifact{Version: 1, ClickHouseVersion: clickHouseVersion, Fixture: "final-v1", Cells: cells}
}

// ValidateFinalArtifact checks the complete cell and value-probe roster.
func ValidateFinalArtifact(artifact FinalArtifact) error {
	if artifact.Version != 1 || artifact.ClickHouseVersion == "" || artifact.Fixture == "" {
		return fmt.Errorf("FINAL matrix identity is incomplete")
	}
	required := CurrentFinalArtifact(artifact.ClickHouseVersion).Cells
	if len(artifact.Cells) != len(required) {
		return fmt.Errorf("FINAL matrix has %d cells, want %d", len(artifact.Cells), len(required))
	}
	seen := make(map[string]bool, len(artifact.Cells))
	probes := make(map[string]bool)
	for index, cell := range artifact.Cells {
		if cell.ID == "" || cell.ID != required[index].ID || seen[cell.ID] || cell.Query == "" || cell.Role == "" || !reflect.DeepEqual(cell, required[index]) {
			return fmt.Errorf("FINAL cell %d has a wrong identity", index)
		}
		seen[cell.ID] = true
		if !finalLane(cell.Chgen) || !finalLane(cell.Analysis) || !finalLane(cell.Execution) {
			return fmt.Errorf("FINAL cell %s has an invalid lane", cell.ID)
		}
		if cell.Chgen == "accept" && len(cell.Results) == 0 || cell.Chgen == "refuse" && cell.ChgenError == "" {
			return fmt.Errorf("FINAL cell %s has no chgen boundary", cell.ID)
		}
		if cell.Analysis == "accept" && len(cell.Results) == 0 || cell.Analysis == "refuse" && cell.AnalysisCode == 0 {
			return fmt.Errorf("FINAL cell %s has no analysis boundary", cell.ID)
		}
		if cell.Execution == "refuse" && cell.ExecutionCode == 0 {
			return fmt.Errorf("FINAL cell %s has no execution boundary", cell.ID)
		}
		if (cell.Chgen != cell.Analysis) != (cell.Owner != "") {
			return fmt.Errorf("FINAL cell %s has a wrong owner", cell.ID)
		}
		if cell.ValueProbe != "" {
			if probes[cell.ValueProbe] || cell.Execution != "accept" {
				return fmt.Errorf("FINAL cell %s has an invalid value probe", cell.ID)
			}
			probes[cell.ValueProbe] = true
		}
	}
	for _, probe := range []string{"probe_final_replacing", "probe_final_summing", "probe_final_collapsing", "probe_final_join", "probe_final_cte", "probe_final_set"} {
		if !probes[probe] {
			return fmt.Errorf("FINAL matrix has no value probe %s", probe)
		}
	}
	return nil
}

func finalLane(value string) bool { return value == "accept" || value == "refuse" }
