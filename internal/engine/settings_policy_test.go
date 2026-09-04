package engine

import (
	"reflect"
	"strings"
	"testing"
)

func TestUncheckedSettingsPreserveSQLAndTypesAcrossPlacements(t *testing.T) {
	for _, test := range []struct {
		name, command, sql string
	}{
		{"select", "many", "SELECT value FROM settings_source SETTINGS future_read_control = 1"},
		{"one", "one", "SELECT value FROM settings_source ORDER BY id LIMIT 1 SETTINGS future_read_control = 1"},
		{"derived", "many", "SELECT value FROM (SELECT value FROM settings_source SETTINGS future_read_control = 1)"},
		{"scalar", "one", "SELECT (SELECT min(value) FROM settings_source SETTINGS future_read_control = 1) AS v"},
		{"cte", "many", "WITH q AS (SELECT value FROM settings_source SETTINGS future_read_control = 1) SELECT value FROM q"},
		{"union", "many", "SELECT value FROM settings_source UNION ALL SELECT value FROM settings_source SETTINGS future_read_control = 1"},
		{"insert_select", "exec", "INSERT INTO settings_sink (grp, total) SELECT grp, toInt64(value) FROM settings_source SETTINGS future_read_control = 1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			schema := catalogsFromDDL(t, settingsRosterSchema)
			header := "-- name: Read :" + test.command + "\n"
			queries, err := parseQueriesWithCatalogs(t, header+uncheckedSettingDirective+" future_read_control\n"+test.sql, schema)
			if err != nil {
				t.Fatal(err)
			}
			if queries[0].SQL != test.sql {
				t.Fatalf("runtime SQL changed: %q", queries[0].SQL)
			}
			baselineSQL := strings.ReplaceAll(test.sql, " SETTINGS future_read_control = 1", "")
			baseline, err := parseQueriesWithCatalogs(t, header+baselineSQL, schema)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(queries[0].Results, baseline[0].Results) {
				t.Fatalf("opt-in changed inferred result types: %#v vs %#v", queries[0].Results, baseline[0].Results)
			}
			generated, err := Generate("settingsgen", queries)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(generated), test.sql) {
				t.Fatalf("generated SQL lost settings:\n%s", generated)
			}
			// The shared catalogs and global roster must remain strict.
			if _, err := parseQueriesWithCatalogs(t, header+test.sql, schema); err == nil || !strings.Contains(err.Error(), "measured resolver roster") {
				t.Fatalf("opt-in leaked into another parse: %v", err)
			}
		})
	}
}

func TestUncheckedSettingsLiteralDomainsAndBuiltInPrecedence(t *testing.T) {
	for _, test := range []struct {
		name, settings, want string
	}{
		{"future", "future = 0", ""},
		{"future", "future = 18446744073709551615", ""},
		{"future", "future = true", ""},
		{"future", "future = false", ""},
		{"future", "future = 'custom mode'", ""},
		{"future", "future = 1, future = 2", ""},
		{"future", "future = 1, future = -1", "unchecked SETTINGS future must be"},
		{"future", "future = -1", "unchecked SETTINGS future must be"},
		{"future", "future = 18446744073709551616", "unchecked SETTINGS future must be"},
		{"future", "future = chgen.arg('Value')", "expected <number>, <bool> or <string>"},
		{"future", "future = 1, another = 1", `SETTINGS name "another"`},
		{"future", "future = 1, optimize_read_in_order = 2", "must be 0, 1, true, or false"},
		{"optimize_read_in_order", "optimize_read_in_order = 2", "must be 0, 1, true, or false"},
		{"max_block_size", "max_block_size = 0", "positive unsigned integer literal"},
		{"max_threads", "max_threads = '1'", "unsigned integer literal"},
		{"max_threads", "max_threads = 1", ""},
		{"optimize_read_in_order", "optimize_read_in_order = 1", ""},
		{"future", "Future = 1", "does not match a SETTINGS item"},
	} {
		t.Run(test.settings, func(t *testing.T) {
			input := "-- name: Read :many\n" + uncheckedSettingDirective + " " + test.name + "\nSELECT value FROM settings_source SETTINGS " + test.settings
			_, err := parseQueriesWithDDL(t, settingsRosterSchema, input)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestUncheckedSettingsAnnotationsAreExactAndQueryLocal(t *testing.T) {
	for _, input := range []string{
		uncheckedSettingDirective + " future\n-- name: Read :many\nSELECT value FROM settings_source",
		"-- name: Read :many\n" + uncheckedSettingDirective + "\nSELECT value FROM settings_source",
		"-- name: Read :many\n" + uncheckedSettingDirective + " *\nSELECT value FROM settings_source",
		"-- name: Read :many\n" + uncheckedSettingDirective + " future another\nSELECT value FROM settings_source",
		"-- name: Read :many\n" + uncheckedSettingDirective + " Future\nSELECT value FROM settings_source",
		"-- name: Read :many\n" + uncheckedSettingDirective + " future\n" + uncheckedSettingDirective + " future\nSELECT value FROM settings_source SETTINGS future = 1",
		"-- name: Read :many\n" + uncheckedSettingDirective + " future\nSELECT value FROM settings_source",
		"-- name: First :many\n" + uncheckedSettingDirective + " future\nSELECT value FROM settings_source SETTINGS future = 1\n-- name: Second :many\nSELECT value FROM settings_source SETTINGS future = 1",
	} {
		if _, err := parseQueriesWithDDL(t, settingsRosterSchema, input); err == nil {
			t.Errorf("unexpectedly accepted:\n%s", input)
		}
	}
	input := "-- name: Read :many\n" + uncheckedSettingDirective + "\tfuture\n" + uncheckedSettingDirective + " another\nSELECT value FROM settings_source SETTINGS future = 1, another = 'mode'"
	if _, err := parseQueriesWithDDL(t, settingsRosterSchema, input); err != nil {
		t.Fatal(err)
	}
}

func TestUncheckedSettingsDoNotBypassOtherResolutionErrors(t *testing.T) {
	for _, sql := range []string{
		"SELECT missing FROM settings_source SETTINGS future = 1",
		"SELECT id FROM missing_table SETTINGS future = 1",
		"SELECT unknown_function(value) AS v FROM settings_source SETTINGS future = 1",
	} {
		input := "-- name: Read :many\n" + uncheckedSettingDirective + " future\n" + sql
		if _, err := parseQueriesWithDDL(t, settingsRosterSchema, input); err == nil {
			t.Errorf("unchecked setting hid a resolution error: %s", sql)
		}
	}
}
