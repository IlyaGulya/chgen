package conformance

import (
	"fmt"
	"reflect"
)

// ArrayJoinResult records one output column in source order.
type ArrayJoinResult struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ArrayJoinCell records one ARRAY JOIN scope boundary.
type ArrayJoinCell struct {
	ID            string            `json:"id"`
	Role          string            `json:"role"`
	Query         string            `json:"query"`
	Chgen         string            `json:"chgen"`
	ChgenError    string            `json:"chgen_error,omitempty"`
	Results       []ArrayJoinResult `json:"results,omitempty"`
	Analysis      string            `json:"analysis"`
	AnalysisCode  int               `json:"analysis_code,omitempty"`
	Execution     string            `json:"execution"`
	ExecutionCode int               `json:"execution_code,omitempty"`
	Owner         string            `json:"owner,omitempty"`
	ValueProbe    string            `json:"value_probe,omitempty"`
}

// ArrayJoinArtifact records the executable ARRAY JOIN matrix.
type ArrayJoinArtifact struct {
	Version           int             `json:"version"`
	ClickHouseVersion string          `json:"clickhouse_version"`
	Fixture           string          `json:"fixture"`
	Cells             []ArrayJoinCell `json:"cells"`
}

// ArrayJoinFixtureDDL creates real columns for ARRAY JOIN checks.
const ArrayJoinFixtureDDL = `
CREATE TABLE array_join_rows (
    id UInt8,
    scalar Int64,
    values Array(Int32),
    peers Array(UInt16),
    short_values Array(UInt16),
    nullable_values Array(Nullable(Int16)),
    low_values Array(LowCardinality(String)),
    tuple_values Array(Tuple(x UInt8, y Nullable(String))),
    nested_values Nested(k UInt16, v String),
    empty_values Array(UInt32),
    mapped Map(String, UInt16)
) ENGINE = MergeTree ORDER BY id;
`

// ArrayJoinFixtureSeed gives each ARRAY JOIN shape a value witness.
const ArrayJoinFixtureSeed = `
INSERT INTO array_join_rows
    (id, scalar, values, peers, short_values, nullable_values, low_values,
     tuple_values, nested_values.k, nested_values.v, empty_values, mapped)
VALUES
    (1, 99, [10, 20], [1, 2], [7], [NULL, 3], ['a', 'b'],
     [(1, 'x'), (2, NULL)], [3, 4], ['n3', 'n4'], [], map('x', toUInt16(7))),
    (2, 100, [30, 40], [3, 4], [8], [5, NULL], ['c', 'd'],
     [(5, 'y'), (6, 'z')], [7, 8], ['n7', 'n8'], [9], map('y', toUInt16(8)));
`

func arrayJoinResult(name, typeName string) []ArrayJoinResult {
	return []ArrayJoinResult{{Name: name, Type: typeName}}
}

// CurrentArrayJoinArtifact returns the pinned ARRAY JOIN matrix.
func CurrentArrayJoinArtifact(clickHouseVersion string) ArrayJoinArtifact {
	accept := func(id, role, query string, results []ArrayJoinResult) ArrayJoinCell {
		return ArrayJoinCell{ID: id, Role: role, Query: query, Chgen: "accept", Results: results, Analysis: "accept", Execution: "accept"}
	}
	refuse := func(id, role, query, chgenError string, code int) ArrayJoinCell {
		return ArrayJoinCell{ID: id, Role: role, Query: query, Chgen: "refuse", ChgenError: chgenError, Analysis: "refuse", AnalysisCode: code, Execution: "refuse", ExecutionCode: code}
	}
	cells := []ArrayJoinCell{
		accept("binding/bare", "binding", "SELECT values AS item FROM array_join_rows ARRAY JOIN values ORDER BY id, item", arrayJoinResult("item", "Int32")),
		accept("binding/alias-preserves-source", "binding", "SELECT values AS original, item FROM array_join_rows ARRAY JOIN values AS item ORDER BY id, item", []ArrayJoinResult{{Name: "original", Type: "Array(Int32)"}, {Name: "item", Type: "Int32"}}),
		accept("binding/qualified-bare", "binding", "SELECT r.values AS item FROM array_join_rows AS r ARRAY JOIN values ORDER BY id, item", arrayJoinResult("item", "Int32")),
		accept("binding/qualified-alias-source", "binding", "SELECT r.values AS original, item FROM array_join_rows AS r ARRAY JOIN values AS item ORDER BY id, item", []ArrayJoinResult{{Name: "original", Type: "Array(Int32)"}, {Name: "item", Type: "Int32"}}),
		accept("kind/left-empty", "join_kind", "SELECT id, item FROM array_join_rows LEFT ARRAY JOIN empty_values AS item ORDER BY id", []ArrayJoinResult{{Name: "id", Type: "UInt8"}, {Name: "item", Type: "UInt32"}}),
		accept("kind/inner", "join_kind", "SELECT item FROM array_join_rows INNER ARRAY JOIN values AS item ORDER BY item", arrayJoinResult("item", "Int32")),
		accept("binding/multiple", "binding", "SELECT a, b FROM array_join_rows ARRAY JOIN values AS a, peers AS b ORDER BY id, a", []ArrayJoinResult{{Name: "a", Type: "Int32"}, {Name: "b", Type: "UInt16"}}),
		accept("expression/array-map", "expression", "SELECT item FROM array_join_rows ARRAY JOIN arrayMap(x -> x + toInt32(id), values) AS item ORDER BY item", arrayJoinResult("item", "Int64")),
		accept("expression/anonymous", "expression", "SELECT id AS result FROM array_join_rows ARRAY JOIN arrayMap(x -> x + 1, values) ORDER BY id", arrayJoinResult("result", "UInt8")),
		accept("wrapper/nullable", "wrapper", "SELECT item FROM array_join_rows ARRAY JOIN nullable_values AS item ORDER BY id", arrayJoinResult("item", "Nullable(Int16)")),
		accept("wrapper/low-cardinality", "wrapper", "SELECT item FROM array_join_rows ARRAY JOIN low_values AS item ORDER BY item", arrayJoinResult("item", "LowCardinality(String)")),
		accept("shape/tuple", "shape", "SELECT item.x AS x, item.y AS y FROM array_join_rows ARRAY JOIN tuple_values AS item ORDER BY id, x", []ArrayJoinResult{{Name: "x", Type: "UInt8"}, {Name: "y", Type: "Nullable(String)"}}),
		accept("shape/nested", "shape", "SELECT nested_values.k AS k, nested_values.v AS v FROM array_join_rows ARRAY JOIN nested_values ORDER BY id, k", []ArrayJoinResult{{Name: "k", Type: "UInt16"}, {Name: "v", Type: "String"}}),
		accept("shape/map", "shape", "SELECT item.1 AS key, item.2 AS value FROM array_join_rows ARRAY JOIN mapped AS item ORDER BY id, key", []ArrayJoinResult{{Name: "key", Type: "String"}, {Name: "value", Type: "UInt16"}}),
		accept("shadow/unqualified", "shadow", "SELECT scalar AS exploded FROM array_join_rows ARRAY JOIN values AS scalar ORDER BY exploded", arrayJoinResult("exploded", "Int32")),
		accept("shadow/qualified-source", "shadow", "SELECT scalar AS exploded, array_join_rows.scalar AS original FROM array_join_rows ARRAY JOIN values AS scalar ORDER BY exploded", []ArrayJoinResult{{Name: "exploded", Type: "Int32"}, {Name: "original", Type: "Int64"}}),
		accept("shadow/scalar-cte", "shadow", "WITH toUInt64(7) AS item SELECT item AS result FROM array_join_rows ARRAY JOIN values AS item ORDER BY id", arrayJoinResult("result", "UInt64")),
		refuse("identity/alias-case", "identity", "SELECT Item AS value FROM array_join_rows ARRAY JOIN values AS item", "Item", 47),
		refuse("identity/duplicate-alias", "identity", "SELECT item AS value FROM array_join_rows ARRAY JOIN values AS item, peers AS item", "defined more than once", 36),
		refuse("expression/missing", "expression", "SELECT item AS value FROM array_join_rows ARRAY JOIN missing AS item", "missing", 47),
		refuse("expression/non-array", "expression", "SELECT item AS value FROM array_join_rows ARRAY JOIN scalar AS item", "requires an Array or Map", 53),
		accept("expression/empty-anonymous", "expression", "SELECT id AS result FROM array_join_rows ARRAY JOIN [] ORDER BY id", arrayJoinResult("result", "UInt8")),
		refuse("shape/nested-missing", "shape", "SELECT nested_values.missing AS value FROM array_join_rows ARRAY JOIN nested_values", "missing", 47),
		refuse("shape/tuple-missing", "shape", "SELECT item.missing AS value FROM array_join_rows ARRAY JOIN tuple_values AS item", "missing", 47),
		accept("clause/where-output", "clause", "SELECT item FROM array_join_rows ARRAY JOIN values AS item WHERE item > 10 ORDER BY item", arrayJoinResult("item", "Int32")),
		refuse("clause/prewhere-output", "clause", "SELECT item FROM array_join_rows ARRAY JOIN values AS item PREWHERE item > 10", "PREWHERE cannot use", 182),
		accept("clause/prewhere-source", "clause", "SELECT item FROM array_join_rows ARRAY JOIN values AS item PREWHERE id = 1 ORDER BY item", arrayJoinResult("item", "Int32")),
		accept("clause/group-having-order", "clause", "SELECT item, count() AS n FROM array_join_rows ARRAY JOIN values AS item GROUP BY item HAVING item > 10 ORDER BY item", []ArrayJoinResult{{Name: "item", Type: "Int32"}, {Name: "n", Type: "UInt64"}}),
		accept("clause/source-array", "clause", "SELECT values AS original FROM array_join_rows ARRAY JOIN values AS item WHERE values[1] > 0 ORDER BY id", arrayJoinResult("original", "Array(Int32)")),
		accept("runtime/equal-sizes", "runtime", "SELECT a, b FROM array_join_rows ARRAY JOIN values AS a, peers AS b ORDER BY id, a", []ArrayJoinResult{{Name: "a", Type: "Int32"}, {Name: "b", Type: "UInt16"}}),
		{ID: "runtime/unequal-sizes", Role: "runtime", Query: "SELECT a, b FROM array_join_rows ARRAY JOIN values AS a, short_values AS b", Chgen: "accept", Results: []ArrayJoinResult{{Name: "a", Type: "Int32"}, {Name: "b", Type: "UInt16"}}, Analysis: "accept", Execution: "refuse", ExecutionCode: 190},
		accept("scope/derived", "scope", "SELECT item FROM (SELECT id, values FROM array_join_rows) AS d ARRAY JOIN values AS item ORDER BY id, item", arrayJoinResult("item", "Int32")),
		accept("scope/cte", "scope", "WITH q AS (SELECT id, values FROM array_join_rows) SELECT item FROM q ARRAY JOIN values AS item ORDER BY id, item", arrayJoinResult("item", "Int32")),
		accept("scope/set-operation", "scope", "SELECT item FROM array_join_rows ARRAY JOIN values AS item UNION ALL SELECT toInt64(7) AS item ORDER BY item", arrayJoinResult("item", "Int64")),
		accept("identity/qualified-exact", "identity", "SELECT item FROM array_join_rows AS R ARRAY JOIN R.values AS item ORDER BY item", arrayJoinResult("item", "Int32")),
		refuse("identity/qualified-case", "identity", "SELECT item FROM array_join_rows AS R ARRAY JOIN r.values AS item", "column \"values\"", 47),
		refuse("scope/same-node-alias", "scope", "SELECT a, b FROM array_join_rows ARRAY JOIN values AS a, [a] AS b", "column \"a\"", 47),
		refuse("scope/same-node-expression-alias", "scope", "SELECT a, b FROM array_join_rows ARRAY JOIN values AS a, arrayMap(x -> x + a, peers) AS b", "column \"a\"", 47),
		{ID: "scope/same-node-bare-rebinding", Role: "scope", Query: "SELECT values, b FROM array_join_rows ARRAY JOIN values, [values] AS b", Chgen: "accept", Results: []ArrayJoinResult{{Name: "values", Type: "Int32"}, {Name: "b", Type: "Array(Int32)"}}, Analysis: "accept", Execution: "refuse", ExecutionCode: 190},
		accept("scope/sequential-visibility", "scope", "SELECT a, b FROM array_join_rows ARRAY JOIN values AS a ARRAY JOIN [a] AS b ORDER BY id, a", []ArrayJoinResult{{Name: "a", Type: "Int32"}, {Name: "b", Type: "Int32"}}),
		accept("runtime/sequential-cardinality", "runtime", "SELECT a, b FROM array_join_rows ARRAY JOIN values AS a ARRAY JOIN peers AS b ORDER BY id, a, b", []ArrayJoinResult{{Name: "a", Type: "Int32"}, {Name: "b", Type: "UInt16"}}),
	}
	probes := map[string]string{
		"binding/bare": "probe_array_join_bare", "binding/alias-preserves-source": "probe_array_join_alias",
		"kind/left-empty": "probe_array_join_left", "binding/multiple": "probe_array_join_multiple",
		"wrapper/nullable": "probe_array_join_nullable", "wrapper/low-cardinality": "probe_array_join_low_cardinality",
		"shape/tuple": "probe_array_join_tuple", "shape/nested": "probe_array_join_nested",
		"shape/map": "probe_array_join_map", "clause/where-output": "probe_array_join_param",
		"scope/set-operation": "probe_array_join_set", "expression/empty-anonymous": "probe_array_join_empty_anonymous",
		"scope/sequential-visibility": "probe_array_join_sequential_visibility", "runtime/sequential-cardinality": "probe_array_join_sequential_cardinality",
	}
	for index := range cells {
		cells[index].ValueProbe = probes[cells[index].ID]
	}
	return ArrayJoinArtifact{Version: 1, ClickHouseVersion: clickHouseVersion, Fixture: "array-join-v1", Cells: cells}
}

// ValidateArrayJoinArtifact checks the complete cell and probe roster.
func ValidateArrayJoinArtifact(artifact ArrayJoinArtifact) error {
	if artifact.Version != 1 || artifact.ClickHouseVersion == "" || artifact.Fixture == "" {
		return fmt.Errorf("ARRAY JOIN matrix identity is incomplete")
	}
	required := CurrentArrayJoinArtifact(artifact.ClickHouseVersion).Cells
	if len(artifact.Cells) != len(required) {
		return fmt.Errorf("ARRAY JOIN matrix has %d cells, want %d", len(artifact.Cells), len(required))
	}
	seen := make(map[string]bool, len(artifact.Cells))
	probes := make(map[string]bool)
	for index, cell := range artifact.Cells {
		if cell.ID == "" || cell.ID != required[index].ID || seen[cell.ID] || cell.Query == "" || cell.Role == "" || !reflect.DeepEqual(cell, required[index]) {
			return fmt.Errorf("ARRAY JOIN cell %d has a wrong identity", index)
		}
		seen[cell.ID] = true
		if !arrayJoinLane(cell.Chgen) || !arrayJoinLane(cell.Analysis) || !arrayJoinLane(cell.Execution) {
			return fmt.Errorf("ARRAY JOIN cell %s has an invalid lane", cell.ID)
		}
		if cell.Chgen == "accept" && len(cell.Results) == 0 || cell.Chgen == "refuse" && cell.ChgenError == "" {
			return fmt.Errorf("ARRAY JOIN cell %s has no chgen boundary", cell.ID)
		}
		if cell.Analysis == "accept" && len(cell.Results) == 0 || cell.Analysis == "refuse" && cell.AnalysisCode == 0 {
			return fmt.Errorf("ARRAY JOIN cell %s has no analysis boundary", cell.ID)
		}
		if cell.Execution == "refuse" && cell.ExecutionCode == 0 {
			return fmt.Errorf("ARRAY JOIN cell %s has no execution boundary", cell.ID)
		}
		if (cell.Chgen != cell.Analysis) != (cell.Owner != "") {
			return fmt.Errorf("ARRAY JOIN cell %s has a wrong owner", cell.ID)
		}
		if cell.ValueProbe != "" {
			if probes[cell.ValueProbe] || cell.Execution != "accept" {
				return fmt.Errorf("ARRAY JOIN cell %s has an invalid value probe", cell.ID)
			}
			probes[cell.ValueProbe] = true
		}
	}
	for _, probe := range []string{"probe_array_join_bare", "probe_array_join_alias", "probe_array_join_left", "probe_array_join_multiple", "probe_array_join_nullable", "probe_array_join_low_cardinality", "probe_array_join_tuple", "probe_array_join_nested", "probe_array_join_map", "probe_array_join_param", "probe_array_join_set", "probe_array_join_empty_anonymous", "probe_array_join_sequential_visibility", "probe_array_join_sequential_cardinality"} {
		if !probes[probe] {
			return fmt.Errorf("ARRAY JOIN matrix has no value probe %s", probe)
		}
	}
	return nil
}

func arrayJoinLane(value string) bool { return value == "accept" || value == "refuse" }
