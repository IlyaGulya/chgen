//go:build fuzzoracle

package engine

import (
	"fmt"
	"strings"
	"testing"
)

func TestNullableOracleRegressionsAgainstClickHouse(t *testing.T) {
	oracle := execWitnessFixtureWithDDL(t, nullableShiftDDL,
		`INSERT INTO t VALUES
    ('2024-02-03', '2024-02-03', '2024-02-03 12:00:00', '2024-02-03 12:00:00', 1),
    ('2024-02-03', '2024-02-03', '2024-02-03 12:00:00', '2024-02-03 12:00:00', NULL)`)
	schema := schemaFromDDL(t, nullableShiftDDL)
	for function := range temporalShiftFunctions {
		for _, column := range []string{"d", "d32", "dt", "dt64"} {
			expression := fmt.Sprintf("%s(%s, n)", functionRegistry[function].gen.spelling, column)
			got, err := chgenInferType(schema, expression)
			if err != nil {
				t.Fatal(err)
			}
			server, err := oracle.exec("SELECT DISTINCT toTypeName(" + expression + ") FROM t")
			if err != nil || strings.TrimSpace(server) != got {
				t.Fatalf("%s: chgen %s, server %q, %v", expression, got, server, err)
			}
			if _, err := oracle.exec("SELECT " + expression + " FROM t"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range nonNullableGeometryTypes {
		value := "[]"
		if name == "Point" {
			value = "(1., 2.)"
		}
		expression := fmt.Sprintf("nullIf(CAST(%s AS %s), CAST(%s AS %s))", value, name, value, name)
		if _, err := oracle.exec("SELECT " + expression); err == nil || !strings.Contains(err.Error(), "Code: 43") {
			t.Errorf("%s: expected Code: 43, got %v", expression, err)
		}
	}
}
