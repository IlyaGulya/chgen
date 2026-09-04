package engine

import (
	"fmt"
	"strings"
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

const settingsRosterSchema = `
CREATE TABLE settings_source (
    id UInt64,
    grp UInt8,
    value Int32
) ENGINE = Memory;
CREATE TABLE settings_sink (
    grp UInt8,
    total Int64
) ENGINE = Memory;
`

func TestSettingsRosterAcceptsMeasuredLiteralDomains(t *testing.T) {
	tests := []string{
		"SELECT value FROM settings_source SETTINGS max_bytes_before_external_group_by = 17",
		"SELECT value FROM settings_source SETTINGS max_bytes_before_external_sort = 19",
		"SELECT value FROM settings_source SETTINGS memory_overcommit_ratio_denominator = 0",
		"SELECT value FROM settings_source SETTINGS memory_overcommit_ratio_denominator_for_user = 0",
		"SELECT value FROM settings_source SETTINGS preferred_block_size_bytes = 23",
		"SELECT value FROM settings_source SETTINGS use_uncompressed_cache = 0",
		"SELECT value FROM settings_source SETTINGS use_uncompressed_cache = true",
		"SELECT value FROM settings_source SETTINGS do_not_merge_across_partitions_select_final = false",
		"SELECT value FROM settings_source SETTINGS max_execution_time = 29",
		"SELECT value FROM settings_source SETTINGS max_execution_time = 9223372036854",
		"SELECT value FROM settings_source SETTINGS max_memory_usage = 31",
		"SELECT value FROM settings_source SETTINGS max_block_size = 37",
		"SELECT value FROM settings_source ORDER BY id LIMIT 1 SETTINGS optimize_read_in_order = 1, max_threads = 1",
		"SELECT value FROM settings_source SETTINGS optimize_read_in_order = 0",
		"SELECT value FROM settings_source SETTINGS optimize_read_in_order = true",
		"SELECT value FROM settings_source SETTINGS optimize_read_in_order = false",
		"SELECT value FROM settings_source SETTINGS max_threads = 8",
		"SELECT value FROM settings_source SETTINGS max_threads = 0",
		"SELECT value FROM settings_source SETTINGS log_comment = 'statement_probe'",
		"SELECT value FROM settings_source SETTINGS max_memory_usage = 41, max_memory_usage = 43",
		"SELECT value FROM settings_source SETTINGS max_memory_usage = 47, max_block_size = 53, log_comment = 'combined', max_bytes_before_external_group_by = 59",
	}
	for _, query := range tests {
		if _, err := inferQueryResults(settingsRosterSchema, query); err != nil {
			t.Errorf("%s: %v", query, err)
		}
	}
}

func TestSettingsRosterRefusesOutsideMeasuredDomains(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{query: "SELECT value FROM settings_source SETTINGS max_bytes_before_external_group_by = 'x'", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS max_bytes_before_external_sort = 'x'", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS memory_overcommit_ratio_denominator = -1", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS memory_overcommit_ratio_denominator_for_user = 'x'", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS preferred_block_size_bytes = true", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS use_uncompressed_cache = 2", want: "must be 0, 1, true, or false"},
		{query: "SELECT value FROM settings_source SETTINGS do_not_merge_across_partitions_select_final = 'x'", want: "must be 0, 1, true, or false"},
		{query: "SELECT value FROM settings_source SETTINGS max_execution_time = 'x'", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS max_execution_time = 9223372036855", want: "must be at most 9223372036854"},
		{query: "SELECT value FROM settings_source SETTINGS max_execution_time = 18446744073709551615", want: "must be at most 9223372036854"},
		{query: "SELECT value FROM settings_source SETTINGS max_memory_usage = 'x'", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS max_memory_usage = '61'", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS max_memory_usage = true", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS max_memory_usage = 1.5", want: "unexpected token"},
		{query: "SELECT value FROM settings_source SETTINGS max_memory_usage = 18446744073709551616", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS max_block_size = 0", want: "positive unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS optimize_read_in_order = 2", want: "must be 0, 1, true, or false"},
		{query: "SELECT value FROM settings_source SETTINGS optimize_read_in_order = '1'", want: "must be 0, 1, true, or false"},
		{query: "SELECT value FROM settings_source SETTINGS optimize_read_in_order = -1", want: "must be 0, 1, true, or false"},
		{query: "SELECT value FROM settings_source SETTINGS max_threads = -1", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS max_threads = '1'", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS max_threads = true", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS max_threads = 18446744073709551616", want: "unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS max_block_size = 'x'", want: "positive unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS max_block_size = false", want: "positive unsigned integer literal"},
		{query: "SELECT value FROM settings_source SETTINGS log_comment = 1", want: "string literal"},
		{query: "SELECT value FROM settings_source SETTINGS MAX_MEMORY_USAGE = 1", want: "not in the measured resolver roster"},
		{query: "SELECT value FROM settings_source SETTINGS unknown_setting = 1", want: "not in the measured resolver roster"},
	}
	for _, test := range tests {
		if _, err := inferQueryResults(settingsRosterSchema, test.query); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.query, err, test.want)
		}
	}
}

func TestSettingsRosterKeepsSetAndInsertSelectPlacement(t *testing.T) {
	selectQueries := []string{
		"SELECT value FROM settings_source SETTINGS max_memory_usage = 67 UNION ALL SELECT value FROM settings_source",
		"SELECT value FROM settings_source UNION ALL SELECT value FROM settings_source SETTINGS log_comment = 'last_branch'",
		"(SELECT value FROM settings_source SETTINGS max_block_size = 71) UNION ALL SELECT value FROM settings_source",
	}
	for _, query := range selectQueries {
		if _, err := inferQueryResults(settingsRosterSchema, query); err != nil {
			t.Errorf("%s: %v", query, err)
		}
	}

	schema, err := schemaFromDDLErr(t, settingsRosterSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query   string
		wantErr string
	}{
		{query: "-- name: InsertWithSettings :exec\nINSERT INTO settings_sink (grp, total) SELECT grp, sum(value) FROM settings_source GROUP BY grp SETTINGS max_memory_usage = 73"},
		{query: "-- name: InsertSetWithSettings :exec\nINSERT INTO settings_sink (grp, total) SELECT grp, toInt64(value) FROM settings_source UNION ALL SELECT grp, toInt64(value) FROM settings_source SETTINGS log_comment = 'insert_set'"},
		{query: "-- name: InsertWrongSetting :exec\nINSERT INTO settings_sink (grp, total) SELECT grp, toInt64(value) FROM settings_source SETTINGS max_memory_usage = 'x'", wantErr: "unsigned integer literal"},
		{query: "-- name: InsertUnknownSetting :exec\nINSERT INTO settings_sink (grp, total) SELECT grp, toInt64(value) FROM settings_source SETTINGS unknown_setting = 1", wantErr: "not in the measured resolver roster"},
	} {
		_, gotErr := parseQueriesWithSchema(t, test.query, schema)
		if test.wantErr == "" && gotErr != nil {
			t.Errorf("%s: %v", test.query, gotErr)
		}
		if test.wantErr != "" && (gotErr == nil || !strings.Contains(gotErr.Error(), test.wantErr)) {
			t.Errorf("%s: error = %v, want %q", test.query, gotErr, test.wantErr)
		}
	}
}

func TestSettingsRosterRejectsParametersBeforeGeneration(t *testing.T) {
	schema, err := schemaFromDDLErr(t, settingsRosterSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"-- name: MemorySetting :many\n-- param: Memory uint64\nSELECT value FROM settings_source SETTINGS max_memory_usage = chgen.arg('Memory')",
		"-- name: CommentSetting :many\n-- param: Comment string\nSELECT value FROM settings_source SETTINGS log_comment = chgen.arg('Comment')",
	} {
		if _, err := parseQueriesWithSchema(t, query, schema); err == nil || !strings.Contains(err.Error(), "expected <number>, <bool> or <string>") {
			t.Fatalf("error = %v, want constant SETTINGS refusal", err)
		}
	}
}

func TestSettingsStatementShapesParseAndGenerate(t *testing.T) {
	input := `-- name: SummarizeValues :many
SELECT grp, sum(value) AS total
FROM settings_source
GROUP BY grp
SETTINGS max_bytes_before_external_group_by = 79,
         max_bytes_before_external_sort = 83,
         max_memory_usage = 89,
         memory_overcommit_ratio_denominator = 0,
         memory_overcommit_ratio_denominator_for_user = 0,
         max_threads = 1,
         log_comment = 'summary_read'

-- name: ListValues :many
SELECT value
FROM settings_source
ORDER BY id
LIMIT 1 BY grp
SETTINGS max_memory_usage = 97,
         optimize_read_in_order = 1,
         max_block_size = 101,
         preferred_block_size_bytes = 103,
         use_uncompressed_cache = 0,
         max_threads = 1,
         log_comment = 'value_read'

-- name: CountRows :one
SELECT count() AS count
FROM settings_source
SETTINGS log_comment = 'row_count'

-- name: HasRows :one
SELECT count() AS count
FROM (SELECT 1 AS present FROM settings_source LIMIT 1)
SETTINGS max_threads = 1,
         max_block_size = 107,
         log_comment = 'row_exists'

-- name: CompareTables :many
SELECT 'left' AS table_name, count() AS row_count FROM settings_source
UNION ALL
SELECT 'right' AS table_name, count() AS row_count FROM settings_source
SETTINGS log_comment = 'table_compare'

-- name: RefreshSummary :exec
INSERT INTO settings_sink (grp, total)
SELECT grp, sum(value) AS total
FROM settings_source
GROUP BY grp
SETTINGS max_threads = 1,
         max_memory_usage = 109,
         max_bytes_before_external_group_by = 113,
         memory_overcommit_ratio_denominator = 0,
         do_not_merge_across_partitions_select_final = 0,
         max_execution_time = 127`
	queries, err := parseQueriesWithDDL(t, settingsRosterSchema, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 6 {
		t.Fatalf("query count = %d, want 6", len(queries))
	}
	generated, err := Generate("settingsgen", queries)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	for _, want := range []string{
		"max_bytes_before_external_group_by = 79",
		"max_bytes_before_external_sort = 83",
		"memory_overcommit_ratio_denominator_for_user = 0",
		"max_memory_usage = 89",
		"max_block_size = 101",
		"optimize_read_in_order = 1",
		"preferred_block_size_bytes = 103",
		"use_uncompressed_cache = 0",
		"do_not_merge_across_partitions_select_final = 0",
		"max_execution_time = 127",
		"log_comment = 'value_read'",
		"log_comment = 'table_compare'",
		"max_memory_usage = 109",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated SQL has no %q", want)
		}
	}
}

func TestSettingRosterMutationsFailClosed(t *testing.T) {
	want := map[string]selectSettingRule{
		"do_not_merge_across_partitions_select_final": {kind: selectSettingBooleanLiteral},
		"log_comment":                                  {kind: selectSettingStringLiteral},
		"max_block_size":                               {kind: selectSettingUnsignedLiteral, nonZero: true},
		"max_bytes_before_external_group_by":           {kind: selectSettingUnsignedLiteral},
		"max_bytes_before_external_sort":               {kind: selectSettingUnsignedLiteral},
		"max_execution_time":                           {kind: selectSettingUnsignedLiteral, hasMax: true, max: maxExecutionTimeWholeSeconds},
		"max_memory_usage":                             {kind: selectSettingUnsignedLiteral},
		"max_threads":                                  {kind: selectSettingUnsignedLiteral},
		"memory_overcommit_ratio_denominator":          {kind: selectSettingUnsignedLiteral},
		"memory_overcommit_ratio_denominator_for_user": {kind: selectSettingUnsignedLiteral},
		"optimize_read_in_order":                       {kind: selectSettingBooleanLiteral},
		"preferred_block_size_bytes":                   {kind: selectSettingUnsignedLiteral},
		"use_uncompressed_cache":                       {kind: selectSettingBooleanLiteral},
	}
	if err := validateSettingRosterIdentity(selectSettingRoster, want); err != nil {
		t.Fatal(err)
	}
	for name := range want {
		mutated := cloneSelectSettingRoster(selectSettingRoster)
		delete(mutated, name)
		if err := validateSettingRosterIdentity(mutated, want); err == nil {
			t.Errorf("deletion of %s passed", name)
		}
	}
	mutations := []func(map[string]selectSettingRule){
		func(value map[string]selectSettingRule) {
			value["unknown_setting"] = selectSettingRule{kind: selectSettingUnsignedLiteral}
		},
		func(value map[string]selectSettingRule) {
			value["log_comment"] = selectSettingRule{kind: selectSettingUnsignedLiteral}
		},
		func(value map[string]selectSettingRule) {
			value["max_memory_usage"] = selectSettingRule{kind: selectSettingStringLiteral}
		},
		func(value map[string]selectSettingRule) {
			value["max_block_size"] = selectSettingRule{kind: selectSettingUnsignedLiteral}
		},
		func(value map[string]selectSettingRule) {
			value["use_uncompressed_cache"] = selectSettingRule{kind: selectSettingUnsignedLiteral}
		},
		func(value map[string]selectSettingRule) {
			value["max_execution_time"] = selectSettingRule{kind: selectSettingUnsignedLiteral}
		},
		func(value map[string]selectSettingRule) {
			value["max_execution_time"] = selectSettingRule{kind: selectSettingUnsignedLiteral, hasMax: true, max: maxExecutionTimeWholeSeconds + 1}
		},
	}
	for index, mutate := range mutations {
		mutated := cloneSelectSettingRoster(selectSettingRoster)
		mutate(mutated)
		if err := validateSettingRosterIdentity(mutated, want); err == nil {
			t.Errorf("roster mutation %d passed", index)
		}
	}
}

func TestSettingDeletionChangesResolution(t *testing.T) {
	queries := map[string]string{
		"do_not_merge_across_partitions_select_final": "SELECT value FROM settings_source SETTINGS do_not_merge_across_partitions_select_final = 0",
		"log_comment":                                  "SELECT value FROM settings_source SETTINGS log_comment = 'roster_probe'",
		"max_block_size":                               "SELECT value FROM settings_source SETTINGS max_block_size = 131",
		"max_bytes_before_external_group_by":           "SELECT value FROM settings_source SETTINGS max_bytes_before_external_group_by = 137",
		"max_bytes_before_external_sort":               "SELECT value FROM settings_source SETTINGS max_bytes_before_external_sort = 139",
		"max_execution_time":                           "SELECT value FROM settings_source SETTINGS max_execution_time = 149",
		"max_memory_usage":                             "SELECT value FROM settings_source SETTINGS max_memory_usage = 151",
		"max_threads":                                  "SELECT value FROM settings_source SETTINGS max_threads = 1",
		"memory_overcommit_ratio_denominator":          "SELECT value FROM settings_source SETTINGS memory_overcommit_ratio_denominator = 0",
		"memory_overcommit_ratio_denominator_for_user": "SELECT value FROM settings_source SETTINGS memory_overcommit_ratio_denominator_for_user = 0",
		"optimize_read_in_order":                       "SELECT value FROM settings_source SETTINGS optimize_read_in_order = 1",
		"preferred_block_size_bytes":                   "SELECT value FROM settings_source SETTINGS preferred_block_size_bytes = 157",
		"use_uncompressed_cache":                       "SELECT value FROM settings_source SETTINGS use_uncompressed_cache = false",
	}
	for name, query := range queries {
		settings := parsedSelectSettings(t, query)
		if err := validateSettingsClauseWithRoster(settings, selectSettingRoster); err != nil {
			t.Fatalf("%s baseline: %v", name, err)
		}
		mutated := cloneSelectSettingRoster(selectSettingRoster)
		delete(mutated, name)
		if err := validateSettingsClauseWithRoster(settings, mutated); err == nil {
			t.Errorf("resolution passed after deletion of %s", name)
		}
	}
}

func parsedSelectSettings(t *testing.T, query string) *clickhouse.SettingsClause {
	t.Helper()
	statements, err := parseChgenStatements(query, CommandMany)
	if err != nil {
		t.Fatal(err)
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok || selectQuery.Settings == nil {
		t.Fatalf("query has no SETTINGS AST: %s", query)
	}
	return selectQuery.Settings
}

func cloneSelectSettingRoster(source map[string]selectSettingRule) map[string]selectSettingRule {
	result := make(map[string]selectSettingRule, len(source))
	for name, rule := range source {
		result[name] = rule
	}
	return result
}

func validateSettingRosterIdentity(got, want map[string]selectSettingRule) error {
	if len(got) != len(want) {
		return fmt.Errorf("SETTINGS roster has %d names, want %d", len(got), len(want))
	}
	for name, wantRule := range want {
		if gotRule, ok := got[name]; !ok || gotRule != wantRule {
			return fmt.Errorf("SETTINGS roster has a wrong rule for %s", name)
		}
	}
	return nil
}
