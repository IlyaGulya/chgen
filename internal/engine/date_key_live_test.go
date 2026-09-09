//go:build fuzzoracle

package engine

import (
	"fmt"
	"strings"
	"testing"
)

func TestNumericDateKeysAgainstClickHouse(t *testing.T) {
	oracle := execWitnessFixtureWithDDL(t, toYYYYMMDDLiveDDL, toYYYYMMDDLiveSeed)
	schema := schemaFromDDL(t, toYYYYMMDDLiveDDL)
	for _, function := range numericDateKeys {
		for _, column := range []string{"d", "d32", "dt", "dt64", "nd", "lcd", "lcnd"} {
			expression := function.name + "(" + column + ")"
			got, err := chgenInferType(schema, expression)
			if err != nil {
				t.Fatal(err)
			}
			server, err := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
			if err != nil || strings.TrimSpace(server) != got {
				t.Fatalf("%s: chgen %s, server %q, %v", expression, got, server, err)
			}
			if _, err := oracle.exec("SELECT " + expression + " FROM t"); err != nil {
				t.Fatal(err)
			}
		}
		for _, arg := range []string{"s", "i32", "dec", "dates", "uid"} {
			if _, err := oracle.exec("SELECT " + function.name + "(" + arg + ") FROM t"); err == nil {
				t.Fatalf("server accepted invalid %s argument %s", function.name, arg)
			}
		}
		// NULL has no materialized Go representation; do not invent UInt32/64.
		server, err := oracle.exec("SELECT toTypeName(" + function.name + "(NULL))")
		if err != nil || strings.TrimSpace(server) != "Nullable(Nothing)" {
			t.Fatalf("literal NULL: %q, %v", server, err)
		}
		// Keep the optional-timezone boundary explicit, rather than implying
		// that the one-argument rule models arbitrary timezone values.
		for _, column := range []string{"d", "d32", "dt", "dt64"} {
			for _, zone := range []string{"UTC", "Asia/Tokyo", "Europe/Berlin", "Europe/Moscow"} {
				expression := fmt.Sprintf("%s(%s, '%s')", function.name, column, zone)
				if _, err := oracle.exec("SELECT " + expression + " FROM t"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, test := range []struct{ expression, want string }{
		{"toYYYYMM(toDateTime('2024-03-01 00:30:00', 'UTC'))", "202403"},
		{"toYYYYMMDD(toDateTime('2024-03-01 00:30:00', 'UTC'))", "20240301"},
		{"toYYYYMMDDhhmmss(toDateTime('2024-03-01 00:30:00', 'UTC'))", "20240301003000"},
		{"toYYYYMM(CAST(NULL AS Nullable(Date))) IS NULL", "1"},
		{"toYYYYMMDDhhmmss(CAST(NULL AS Nullable(DateTime64(3)))) IS NULL", "1"},
	} {
		got, err := oracle.exec("SELECT " + test.expression)
		if err != nil || strings.TrimSpace(got) != test.want {
			t.Fatalf("%s: %q, %v; want %s", test.expression, got, err, test.want)
		}
	}
}
