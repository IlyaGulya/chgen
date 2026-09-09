package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResultContractGeneratedRuntime(t *testing.T) {
	var sql strings.Builder
	for _, query := range []struct {
		name, command, expression, typeName, tail string
	}{
		{"ReadOne", "one", "materialize(toUInt32(7))", "UInt32", ""},
		{"ReadMany", "many", "materialize(toUInt32(7))", "UInt32", ""},
		{"EmptyOne", "one", "materialize(toUInt32(7))", "UInt32", " WHERE 0"},
		{"EmptyMany", "many", "materialize(toUInt32(7))", "UInt32", " WHERE 0"},
		{"WrongOne", "one", "materialize(toUInt64(7))", "UInt32", ""},
		{"WrongMany", "many", "materialize(toUInt64(7))", "UInt32", " WHERE 0"},
		{"ReadNull", "one", "materialize(CAST(NULL AS Nullable(UInt32)))", "Nullable(UInt32)", ""},
		{"ReadTime", "one", "materialize(toDateTime('2024-02-03 12:00:00'))", "DateTime", ""},
		{"ReadLowCard", "one", "materialize(toLowCardinality('ok'))", "LowCardinality(String)", ""},
		{"ReadDecimal", "one", "materialize(toDecimal32('1.23', 2))", "Decimal(9, 2)", ""},
		{"ReadArray", "one", "materialize([toUInt32(7)])", "Array(UInt32)", ""},
	} {
		fmt.Fprintf(&sql, "-- name: %s :%s\n-- result-chtype: value %s\nSELECT %s AS value%s;\n",
			query.name, query.command, query.typeName, query.expression, query.tail)
	}
	queries, err := parseQueriesWithSchema(t, sql.String(), &Schema{Tables: map[string]Table{}})
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Generate("contracts", queries)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(moduleRootPath("internal", "engine", "testdata", "resultcontracts", "runtime_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"v2.42.0", "v2.47.0"} {
		t.Run(version, func(t *testing.T) {
			dir := newGeneratedCompileModule(t)
			for name, data := range map[string][]byte{"queries.go": generated, "runtime_test.go": fixture} {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, args := range [][]string{
				{"mod", "edit", "-require=github.com/ClickHouse/clickhouse-go/v2@" + version},
				{"test", "-mod=mod", "-count=1", "-v", "."},
			} {
				command := exec.Command("go", args...)
				command.Dir = dir
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("go %v: %v\n%s", args, err, output)
				}
				t.Logf("%s", output)
			}
		})
	}
}
