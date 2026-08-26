package conformance

import (
	"fmt"
	"reflect"
)

// SetOperationResult records one output column in source order.
type SetOperationResult struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// SetOperationCell records one set-query or CTE dependency boundary.
type SetOperationCell struct {
	ID            string               `json:"id"`
	Role          string               `json:"role"`
	Query         string               `json:"query"`
	Chgen         string               `json:"chgen"`
	ChgenError    string               `json:"chgen_error,omitempty"`
	Results       []SetOperationResult `json:"results,omitempty"`
	Analysis      string               `json:"analysis"`
	AnalysisCode  int                  `json:"analysis_code,omitempty"`
	Execution     string               `json:"execution"`
	ExecutionCode int                  `json:"execution_code,omitempty"`
	Owner         string               `json:"owner,omitempty"`
	ValueProbe    string               `json:"value_probe,omitempty"`
}

// SetOperationArtifact records the executable set-query matrix.
type SetOperationArtifact struct {
	Version           int                `json:"version"`
	ClickHouseVersion string             `json:"clickhouse_version"`
	Fixture           string             `json:"fixture"`
	Cells             []SetOperationCell `json:"cells"`
}

// SetOperationFixtureDDL creates real columns for set-query checks.
const SetOperationFixtureDDL = `
CREATE TABLE set_left (
    id UInt8,
    num Int16,
    text String,
    n Nullable(Int32),
    dec Decimal(9, 2),
    dt DateTime('UTC'),
    arr Array(Int16),
    lc LowCardinality(String),
    dt_alt DateTime64(3, 'UTC')
) ENGINE = Memory;
CREATE TABLE set_right (
    id UInt16,
    num UInt32,
    text FixedString(8),
    n Int32,
    dec Decimal(18, 4),
    dt DateTime64(3, 'UTC'),
    arr Array(UInt32),
    lc LowCardinality(String),
    dt_alt DateTime64(3, 'Europe/Berlin')
) ENGINE = Memory;
CREATE TABLE q (v UInt64) ENGINE = Memory;
`

// SetOperationFixtureSeed gives each operation a distinct value witness.
const SetOperationFixtureSeed = `
INSERT INTO set_left VALUES
    (1, -2, 'left', NULL, 1.25, '2024-01-01 00:00:00', [-2], 'left-lc', '2024-01-01 00:00:00.123'),
    (2, 3, 'same', 4, 2.50, '2024-01-02 00:00:00', [3], 'same-lc', '2024-01-02 00:00:00.456');
INSERT INTO set_right VALUES
    (1, 4, 'right', 5, 3.7500, '2024-01-03 00:00:00.123', [4], 'right-lc', '2024-01-03 00:00:00.123'),
    (3, 6, 'same', 7, 4.0000, '2024-01-04 00:00:00.456', [6], 'same-lc', '2024-01-04 00:00:00.456');
INSERT INTO q VALUES (100);
`

func setResult(name, typeName string) []SetOperationResult {
	return []SetOperationResult{{Name: name, Type: typeName}}
}

// CurrentSetOperationArtifact returns the pinned set-query matrix.
func CurrentSetOperationArtifact(clickHouseVersion string) SetOperationArtifact {
	const owner = "set-operation-known-gap"
	return SetOperationArtifact{
		Version: 1, ClickHouseVersion: clickHouseVersion, Fixture: "set-operation-v1",
		Cells: []SetOperationCell{
			{ID: "union-all/numeric", Role: "union_all", Query: "SELECT id AS result FROM set_left UNION ALL SELECT id AS result FROM set_right ORDER BY result", Chgen: "accept", Results: setResult("result", "UInt16"), Analysis: "accept", Execution: "accept", ValueProbe: "probe_set_union_all"},
			{ID: "union-distinct/numeric", Role: "union_distinct", Query: "SELECT id AS result FROM set_left UNION DISTINCT SELECT id AS result FROM set_right ORDER BY result", Chgen: "accept", Results: setResult("result", "UInt16"), Analysis: "accept", Execution: "accept", ValueProbe: "probe_set_union_distinct"},
			{ID: "intersect/numeric", Role: "intersect", Query: "SELECT id AS result FROM set_left INTERSECT SELECT id AS result FROM set_right ORDER BY result", Chgen: "accept", Results: setResult("result", "UInt16"), Analysis: "accept", Execution: "accept", ValueProbe: "probe_set_intersect"},
			{ID: "except/numeric", Role: "except", Query: "SELECT id AS result FROM set_left EXCEPT SELECT id AS result FROM set_right ORDER BY result", Chgen: "accept", Results: setResult("result", "UInt16"), Analysis: "accept", Execution: "accept", ValueProbe: "probe_set_except"},
			{ID: "result/first-names-order", Role: "result_identity", Query: "SELECT id AS first_id, num AS first_num FROM set_left UNION ALL SELECT num AS ignored_num, id AS ignored_id FROM set_right", Chgen: "accept", Results: []SetOperationResult{{Name: "first_id", Type: "UInt32"}, {Name: "first_num", Type: "Int32"}}, Analysis: "accept", Execution: "accept", ValueProbe: "probe_set_result_vector"},
			{ID: "result/width-refused", Role: "width", Query: "SELECT id AS result FROM set_left UNION ALL SELECT id, num FROM set_right", Chgen: "refuse", ChgenError: "column count 1", Analysis: "refuse", AnalysisCode: 258, Execution: "refuse", ExecutionCode: 258},
			{ID: "result/type-refused", Role: "type_join", Query: "SELECT text AS result FROM set_left UNION ALL SELECT arr FROM set_right", Chgen: "refuse", ChgenError: "no common type", Analysis: "refuse", AnalysisCode: 386, Execution: "refuse", ExecutionCode: 386},
			{ID: "join/nary-numeric", Role: "type_join", Query: "SELECT id AS result FROM set_left UNION ALL SELECT num FROM set_left UNION ALL SELECT num FROM set_right", Chgen: "accept", Results: setResult("result", "Int64"), Analysis: "accept", Execution: "accept"},
			{ID: "join/nullable", Role: "type_join", Query: "SELECT n AS result FROM set_left UNION ALL SELECT n FROM set_right", Chgen: "accept", Results: setResult("result", "Nullable(Int32)"), Analysis: "accept", Execution: "accept"},
			{ID: "join/string", Role: "type_join", Query: "SELECT text AS result FROM set_left UNION ALL SELECT text FROM set_right", Chgen: "accept", Results: setResult("result", "String"), Analysis: "accept", Execution: "accept"},
			{ID: "join/decimal", Role: "type_join", Query: "SELECT dec AS result FROM set_left UNION ALL SELECT dec FROM set_right", Chgen: "accept", Results: setResult("result", "Decimal(18, 4)"), Analysis: "accept", Execution: "accept"},
			{ID: "join/datetime", Role: "type_join", Query: "SELECT dt AS result FROM set_left UNION ALL SELECT dt FROM set_right", Chgen: "accept", Results: setResult("result", "DateTime64(3, 'UTC')"), Analysis: "accept", Execution: "accept"},
			{ID: "join/array", Role: "type_join", Query: "SELECT arr AS result FROM set_left UNION ALL SELECT arr FROM set_right", Chgen: "accept", Results: setResult("result", "Array(Int64)"), Analysis: "accept", Execution: "accept"},
			{ID: "join/low-cardinality", Role: "type_join", Query: "SELECT lc AS result FROM set_left UNION ALL SELECT lc FROM set_right", Chgen: "accept", Results: setResult("result", "LowCardinality(String)"), Analysis: "accept", Execution: "accept", ValueProbe: "probe_set_low_cardinality"},
			{ID: "join/low-cardinality-mixed", Role: "type_join", Query: "SELECT lc AS result FROM set_left UNION ALL SELECT text FROM set_right", Chgen: "accept", Results: setResult("result", "String"), Analysis: "accept", Execution: "accept"},
			{ID: "join/timezone-first", Role: "type_join", Query: "SELECT dt_alt AS result FROM set_left UNION ALL SELECT dt_alt FROM set_right", Chgen: "accept", Results: setResult("result", "DateTime64(3, 'UTC')"), Analysis: "accept", Execution: "accept"},
			{ID: "join/timezone-reversed", Role: "type_join", Query: "SELECT dt_alt AS result FROM set_right UNION ALL SELECT dt_alt FROM set_left", Chgen: "accept", Results: setResult("result", "DateTime64(3, 'Europe/Berlin')"), Analysis: "accept", Execution: "accept"},
			{ID: "parentheses/left", Role: "parentheses", Query: "(SELECT id AS result FROM set_left UNION ALL SELECT num FROM set_left) UNION ALL SELECT num FROM set_right", Chgen: "accept", Results: setResult("result", "Int64"), Analysis: "accept", Execution: "accept"},
			{ID: "parentheses/right", Role: "parentheses", Query: "SELECT id AS result FROM set_left UNION ALL (SELECT num FROM set_left INTERSECT SELECT num FROM set_right)", Chgen: "accept", Results: setResult("result", "Int64"), Analysis: "accept", Execution: "accept"},
			{ID: "precedence/intersect", Role: "precedence", Query: "SELECT toUInt8(1) AS result UNION ALL SELECT toUInt16(2) INTERSECT SELECT toUInt32(2) AS result ORDER BY result", Chgen: "accept", Results: setResult("result", "UInt32"), Analysis: "accept", Execution: "accept", ValueProbe: "probe_set_precedence"},
			{ID: "associativity/except", Role: "associativity", Query: "SELECT id AS result FROM set_left EXCEPT SELECT id FROM set_right EXCEPT SELECT toUInt8(2) AS result ORDER BY result", Chgen: "accept", Results: setResult("result", "UInt16"), Analysis: "accept", Execution: "accept", ValueProbe: "probe_set_associativity"},
			{ID: "scope/right-missing", Role: "branch_scope", Query: "SELECT id AS result FROM set_left UNION ALL SELECT missing FROM set_right", Chgen: "refuse", ChgenError: "set branch 2", Analysis: "refuse", AnalysisCode: 47, Execution: "refuse", ExecutionCode: 47},
			{ID: "scope/first-scalar-with", Role: "branch_scope", Query: "WITH toInt32(7) AS shared SELECT shared AS result UNION ALL SELECT shared", Chgen: "accept", Results: setResult("result", "Int32"), Analysis: "accept", Execution: "accept"},
			{ID: "scope/first-relation-with", Role: "branch_scope", Query: "WITH shared AS (SELECT toInt32(7) AS value) SELECT value AS result FROM shared UNION ALL SELECT value FROM shared", Chgen: "accept", Results: setResult("result", "Int32"), Analysis: "accept", Execution: "accept"},
			{ID: "scope/later-with-local", Role: "branch_scope", Query: "SELECT toInt32(1) AS result UNION ALL WITH toInt32(2) AS local SELECT local", Chgen: "accept", Results: setResult("result", "Int32"), Analysis: "accept", Execution: "accept"},
			{ID: "scope/later-with-hidden", Role: "branch_scope", Query: "SELECT toInt32(1) AS result UNION ALL WITH toInt32(2) AS local SELECT local UNION ALL SELECT local", Chgen: "refuse", ChgenError: "set branch 3", Analysis: "refuse", AnalysisCode: 47, Execution: "refuse", ExecutionCode: 47},
			{ID: "scope/parenthesized-first-with-hidden", Role: "branch_scope", Query: "(WITH toInt32(7) AS local SELECT local AS result) UNION ALL SELECT local", Chgen: "refuse", ChgenError: "set branch 2", Analysis: "refuse", AnalysisCode: 47, Execution: "refuse", ExecutionCode: 47},
			{ID: "cte/forward-scalar", Role: "cte_dependency", Query: "WITH later AS q, toInt32(7) AS later SELECT q AS result", Chgen: "accept", Results: setResult("result", "Int32"), Analysis: "accept", Execution: "accept"},
			{ID: "cte/forward-relation", Role: "cte_dependency", Query: "WITH first AS (SELECT value FROM second), second AS (SELECT toInt32(7) AS value) SELECT value AS result FROM first", Chgen: "accept", Results: setResult("result", "Int32"), Analysis: "accept", Execution: "accept"},
			{ID: "cte/forward-cross-namespace", Role: "cte_dependency", Query: "WITH first AS (SELECT q AS value), toInt32(7) AS q SELECT value AS result FROM first", Chgen: "accept", Results: setResult("result", "Int32"), Analysis: "accept", Execution: "accept"},
			{ID: "cte/duplicate-scalar", Role: "cte_identity", Query: "WITH toInt32(1) AS q, toInt32(2) AS q SELECT q AS result", Chgen: "refuse", ChgenError: "scalar CTE \"q\" is defined more than once", Analysis: "refuse", AnalysisCode: 179, Execution: "refuse", ExecutionCode: 179},
			{ID: "cte/duplicate-relation", Role: "cte_identity", Query: "WITH q AS (SELECT toInt32(1) AS value), q AS (SELECT toInt32(2) AS value) SELECT value AS result FROM q", Chgen: "refuse", ChgenError: "relation CTE \"q\" is defined more than once", Analysis: "refuse", AnalysisCode: 179, Execution: "refuse", ExecutionCode: 179},
			{ID: "cte/scalar-case", Role: "cte_identity", Query: "WITH toInt16(1) AS q, toInt32(2) AS Q SELECT q AS lower, Q AS upper", Chgen: "accept", Results: []SetOperationResult{{Name: "lower", Type: "Int16"}, {Name: "upper", Type: "Int32"}}, Analysis: "accept", Execution: "accept"},
			{ID: "cte/relation-case", Role: "cte_identity", Query: "WITH q AS (SELECT toInt16(1) AS value), Q AS (SELECT toInt32(2) AS value) SELECT q.value AS lower, Q.value AS upper FROM q CROSS JOIN Q", Chgen: "accept", Results: []SetOperationResult{{Name: "lower", Type: "Int16"}, {Name: "upper", Type: "Int32"}}, Analysis: "accept", Execution: "accept"},
			{ID: "cte/scalar-cycle", Role: "cte_dependency", Query: "WITH r AS q, q AS r SELECT q AS result", Chgen: "refuse", ChgenError: "not resolved", Analysis: "refuse", AnalysisCode: 47, Execution: "refuse", ExecutionCode: 47},
			{ID: "cte/relation-cycle", Role: "cte_dependency", Query: "WITH a AS (SELECT value FROM b), b AS (SELECT value FROM a) SELECT value AS result FROM a", Chgen: "refuse", ChgenError: "not present in the schema or the query scope", Analysis: "refuse", AnalysisCode: 60, Execution: "refuse", ExecutionCode: 60},
			{ID: "cte/catalog-shadow", Role: "cte_shadow", Query: "WITH a AS (SELECT v FROM q), q AS (SELECT toInt16(7) AS v) SELECT v AS result FROM a", Chgen: "accept", Results: setResult("result", "Int16"), Analysis: "accept", Execution: "accept", ValueProbe: "probe_cte_catalog_shadow"},
			{ID: "cte/outer-scalar-shadow", Role: "cte_shadow", Query: "WITH toUInt64(100) AS x, inner_q AS (WITH x AS y, toInt16(7) AS x SELECT y AS v) SELECT v AS result FROM inner_q", Chgen: "accept", Results: setResult("result", "Int16"), Analysis: "accept", Execution: "accept", ValueProbe: "probe_cte_scalar_shadow"},
			{ID: "cte/outer-relation-shadow", Role: "cte_shadow", Query: "WITH q AS (SELECT toUInt64(100) AS v), inner_q AS (WITH a AS (SELECT v FROM q), q AS (SELECT toInt16(7) AS v) SELECT v FROM a) SELECT v AS result FROM inner_q", Chgen: "accept", Results: setResult("result", "Int16"), Analysis: "accept", Execution: "accept", ValueProbe: "probe_cte_relation_shadow"},
			{ID: "nested/derived", Role: "derived", Query: "SELECT nested.result FROM (SELECT id AS result FROM set_left UNION ALL SELECT id FROM set_right) AS nested ORDER BY result", Chgen: "accept", Results: setResult("result", "UInt16"), Analysis: "accept", Execution: "accept"},
			{ID: "nested/in", Role: "in", Query: "SELECT id, id IN (SELECT id FROM set_left UNION ALL SELECT id FROM set_right) AS result FROM set_left ORDER BY id", Chgen: "accept", Results: []SetOperationResult{{Name: "id", Type: "UInt8"}, {Name: "result", Type: "UInt8"}}, Analysis: "accept", Execution: "accept"},
			{ID: "nested/exists", Role: "exists", Query: "SELECT EXISTS(SELECT id FROM set_left UNION ALL SELECT id FROM set_right) AS result", Chgen: "accept", Results: setResult("result", "UInt8"), Analysis: "accept", Execution: "accept"},
			{ID: "nested/scalar-refused", Role: "scalar", Query: "SELECT (SELECT id FROM set_left UNION ALL SELECT id FROM set_right) AS result", Chgen: "refuse", ChgenError: "row cardinality is not statically bounded", Analysis: "refuse", AnalysisCode: 125, Execution: "refuse", ExecutionCode: 125},
			{ID: "recursive/refused", Role: "recursive", Query: "WITH RECURSIVE nums AS (SELECT toUInt8(1) AS n UNION ALL SELECT n + 1 FROM nums WHERE n < 3) SELECT n AS result FROM nums ORDER BY result", Chgen: "refuse", ChgenError: "parse SQL for type resolution", Analysis: "accept", Execution: "accept", Results: setResult("result", "UInt8"), Owner: owner},
		},
	}
}

// ValidateSetOperationArtifact checks the complete cell and probe roster.
func ValidateSetOperationArtifact(artifact SetOperationArtifact) error {
	if artifact.Version != 1 || artifact.ClickHouseVersion == "" || artifact.Fixture == "" {
		return fmt.Errorf("set-operation matrix identity is incomplete")
	}
	required := CurrentSetOperationArtifact(artifact.ClickHouseVersion).Cells
	if len(artifact.Cells) != len(required) {
		return fmt.Errorf("set-operation matrix has %d cells, want %d", len(artifact.Cells), len(required))
	}
	seen := make(map[string]bool, len(artifact.Cells))
	probes := make(map[string]bool)
	for index, cell := range artifact.Cells {
		if cell.ID == "" || cell.ID != required[index].ID || seen[cell.ID] || cell.Query == "" || cell.Role == "" || !reflect.DeepEqual(cell, required[index]) {
			return fmt.Errorf("set-operation cell %d has a wrong identity", index)
		}
		seen[cell.ID] = true
		if !setLane(cell.Chgen) || !setLane(cell.Analysis) || !setLane(cell.Execution) {
			return fmt.Errorf("set-operation cell %s has an invalid lane", cell.ID)
		}
		if cell.Chgen == "accept" && len(cell.Results) == 0 || cell.Chgen == "refuse" && cell.ChgenError == "" {
			return fmt.Errorf("set-operation cell %s has no chgen boundary", cell.ID)
		}
		if cell.Analysis == "accept" && len(cell.Results) == 0 || cell.Analysis == "refuse" && cell.AnalysisCode == 0 {
			return fmt.Errorf("set-operation cell %s has no analysis boundary", cell.ID)
		}
		if cell.Execution == "refuse" && cell.ExecutionCode == 0 {
			return fmt.Errorf("set-operation cell %s has no execution boundary", cell.ID)
		}
		disagrees := cell.Chgen != cell.Analysis
		if disagrees != (cell.Owner != "") {
			return fmt.Errorf("set-operation cell %s has a wrong owner", cell.ID)
		}
		for _, result := range cell.Results {
			if result.Name == "" || result.Type == "" {
				return fmt.Errorf("set-operation cell %s has an incomplete result", cell.ID)
			}
		}
		if cell.ValueProbe != "" {
			if probes[cell.ValueProbe] || cell.Execution != "accept" {
				return fmt.Errorf("set-operation cell %s has an invalid value probe", cell.ID)
			}
			probes[cell.ValueProbe] = true
		}
	}
	for _, probe := range []string{"probe_set_union_all", "probe_set_union_distinct", "probe_set_intersect", "probe_set_except", "probe_set_result_vector", "probe_set_precedence", "probe_set_associativity", "probe_set_low_cardinality", "probe_cte_catalog_shadow", "probe_cte_scalar_shadow", "probe_cte_relation_shadow"} {
		if !probes[probe] {
			return fmt.Errorf("set-operation matrix has no value probe %s", probe)
		}
	}
	return nil
}

func setLane(value string) bool { return value == "accept" || value == "refuse" }
