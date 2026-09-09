package engine

import (
	"fmt"
	"testing"
)

var numericDateKeys = []struct{ name, base string }{
	{"toYYYYMM", "UInt32"},
	{"toYYYYMMDD", "UInt32"},
	{"toYYYYMMDDhhmmss", "UInt64"},
}

func TestNumericDateKeyFamily(t *testing.T) {
	schema := schemaFromDDL(t, toYYYYMMDDTestDDL)
	for _, function := range numericDateKeys {
		for _, column := range []struct{ name, wrapper string }{
			{"d", "%s"}, {"d32", "%s"}, {"dt", "%s"}, {"dt64", "%s"},
			{"nd", "Nullable(%s)"}, {"lcd", "LowCardinality(%s)"},
			{"lcnd", "LowCardinality(Nullable(%s))"},
		} {
			expression := function.name + "(" + column.name + ")"
			got, err := inferTestExprType(t, schema, expression)
			want := fmt.Sprintf(column.wrapper, function.base)
			if err != nil || got != want {
				t.Errorf("%s = %s, %v; want %s", expression, got, err, want)
			}
		}
		for _, arg := range []string{"s", "i32", "dec", "dates", "uid", "NULL", "", "d, 'UTC'", "d, s", "d, 'UTC', 'UTC'"} {
			if _, err := inferTestExprType(t, schema, function.name+"("+arg+")"); err == nil {
				t.Errorf("accepted unmeasured/invalid %s(%s)", function.name, arg)
			}
		}
		queries, err := parseQueriesWithSchema(t,
			"-- name: Read :many\nSELECT "+function.name+"(d) AS value FROM probe", schema)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Generate("datekeys", queries); err != nil {
			t.Fatal(err)
		}
	}
}
