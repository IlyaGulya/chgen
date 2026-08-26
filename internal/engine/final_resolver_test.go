package engine

import (
	"strings"
	"testing"
)

const finalSchema = `
CREATE TABLE final_rows (
    id UInt64,
    value Int32,
    version UInt64
) ENGINE = ReplacingMergeTree(version) ORDER BY id;
CREATE TABLE final_peers (
    id UInt64,
    label String
) ENGINE = MergeTree ORDER BY id;
CREATE TABLE final_memory (
    id UInt64,
    value Int32
) ENGINE = Memory;
CREATE TABLE final_sink (
    id UInt64,
    value Int32
) ENGINE = Memory;
`

func TestFinalReadsMeasuredReplacingMergeTreeColumns(t *testing.T) {
	results, err := inferQueryResults(finalSchema, "SELECT id, value FROM final_rows FINAL")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].CHType.String() != "UInt64" || results[1].CHType.String() != "Int32" {
		t.Fatalf("results = %#v", results)
	}
}

func TestFinalKeepsBranchScopesAndInsertSelectTypes(t *testing.T) {
	tests := []string{
		"SELECT r.id FROM final_rows AS r FINAL INNER JOIN final_peers AS p ON r.id = p.id",
		"SELECT value FROM (SELECT value FROM final_rows FINAL) AS q",
		"WITH q AS (SELECT value FROM final_rows FINAL) SELECT value FROM q",
		"SELECT value FROM final_rows FINAL UNION ALL SELECT toInt32(7) AS value",
		"SELECT (SELECT max(value) FROM final_rows FINAL) AS value",
	}
	for _, sql := range tests {
		if _, err := inferQueryResults(finalSchema, sql); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
	schema, err := schemaFromDDLErr(t, finalSchema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseQueriesWithSchema(t, "-- name: InsertFinal :exec\nINSERT INTO final_sink (id, value) SELECT id, value FROM final_rows FINAL", schema); err != nil {
		t.Fatal(err)
	}
	if _, err := parseQueriesWithSchema(t, "-- name: InsertBadFinal :exec\nINSERT INTO final_sink (id, value) SELECT id, value FROM final_memory FINAL", schema); err == nil || !strings.Contains(err.Error(), "engine Memory") {
		t.Fatalf("invalid INSERT SELECT error = %v", err)
	}
}

func TestFinalJoinAndCTEQueryShapes(t *testing.T) {
	const schemaSQL = `
CREATE TABLE versioned_values (
    id UUID,
    group_key String,
    amount UInt64,
    version UInt64
) ENGINE = ReplacingMergeTree(version) ORDER BY (group_key, id);
CREATE TABLE versioned_groups (
    group_key String,
    sequence UInt32,
    version UInt64
) ENGINE = ReplacingMergeTree(version) ORDER BY (group_key, sequence);
`
	queries := []string{
		"SELECT group_key, amount FROM versioned_values FINAL WHERE group_key IN (?)",
		"SELECT v.group_key, g.sequence FROM versioned_values AS v FINAL INNER JOIN versioned_groups AS g FINAL ON v.group_key = g.group_key WHERE v.group_key = ?",
		"WITH selected_groups AS (SELECT group_key FROM versioned_groups FINAL WHERE sequence > ?) SELECT v.amount FROM versioned_values AS v FINAL INNER JOIN selected_groups AS g ON v.group_key = g.group_key",
	}
	for _, sql := range queries {
		query := Query{Name: "final_shape", Command: CommandMany, SQL: sql}
		schema, err := conformanceSchema(schemaSQL)
		if err != nil {
			t.Fatal(err)
		}
		if err := resolveQuery(&query, schema); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
}
