package engine

import (
	"fmt"
	"strings"
	"testing"
)

const toYYYYMMDDTestDDL = `CREATE TABLE probe (
    d Date,
    d32 Date32,
    dt DateTime,
    dt64 DateTime64(3),
    nd Nullable(Date),
    lcd LowCardinality(Date),
    lcnd LowCardinality(Nullable(Date)),
    s String,
    i32 Int32,
    dec Decimal(9, 2),
    dates Array(Date),
    uid UUID
) ENGINE = MergeTree ORDER BY tuple()`

func TestToYYYYMMDDMeasuredTypeDomain(t *testing.T) {
	schema := schemaFromDDL(t, toYYYYMMDDTestDDL)
	accepted := map[string]string{
		"toYYYYMMDD(d)":                                  "UInt32",
		"toYYYYMMDD(d32)":                                "UInt32",
		"toYYYYMMDD(dt)":                                 "UInt32",
		"toYYYYMMDD(dt64)":                               "UInt32",
		"toYYYYMMDD(nd)":                                 "Nullable(UInt32)",
		"toYYYYMMDD(lcd)":                                "LowCardinality(UInt32)",
		"toYYYYMMDD(lcnd)":                               "LowCardinality(Nullable(UInt32))",
		"toYYYYMMDD(toDate('2024-02-03'))":               "UInt32",
		"CAST(toYYYYMMDD(dt64), 'UInt32')":               "UInt32",
		"CAST(toYYYYMMDD(dt64) AS UInt32)":               "UInt32",
		"toUInt64(toYYYYMMDD(toDate('2024-02-03')) + 1)": "UInt64",
	}
	for expression, want := range accepted {
		got, err := inferTestExprType(t, schema, expression)
		if err != nil {
			t.Errorf("inference refused %s: %v", expression, err)
			continue
		}
		if got != want {
			t.Errorf("type of %s = %s, want %s", expression, got, want)
		}
	}

	for _, expression := range []string{
		"toYYYYMMDD(s)",
		"toYYYYMMDD(i32)",
		"toYYYYMMDD(dec)",
		"toYYYYMMDD(dates)",
		"toYYYYMMDD(uid)",
		"toYYYYMMDD()",
		"toYYYYMMDD(d, 'UTC')",
		"toYYYYMMDD(d, 'UTC', 'UTC')",
		"toYYYYMMDD(d, s)",
	} {
		if _, err := inferTestExprType(t, schema, expression); err == nil {
			t.Errorf("inference accepted %s", expression)
		}
	}
}

func TestToYYYYMMDDRepeatedPartitionQueryShape(t *testing.T) {
	schema := schemaFromDDL(t, `
CREATE TABLE daily_alpha (bucket_start DateTime) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE daily_beta (bucket_start DateTime) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE daily_gamma (bucket_start DateTime) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE daily_delta (bucket_start DateTime) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE daily_epsilon (bucket_start DateTime) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE daily_zeta (bucket_start DateTime) ENGINE = MergeTree ORDER BY tuple();
CREATE TABLE daily_eta (bucket_start DateTime) ENGINE = MergeTree ORDER BY tuple();`)

	tables := []string{
		"daily_alpha",
		"daily_beta",
		"daily_gamma",
		"daily_delta",
		"daily_epsilon",
		"daily_zeta",
		"daily_eta",
	}
	var source strings.Builder
	for index, table := range tables {
		fmt.Fprintf(&source, "-- name: ListPartition%d :many\n", index+1)
		source.WriteString("-- result: Partition partition\n")
		fmt.Fprintf(&source, "SELECT DISTINCT CAST(toYYYYMMDD(bucket_start), 'UInt32') AS partition\nFROM %s;\n", table)
	}
	queries, err := parseQueriesWithSchema(t, source.String(), schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != len(tables) {
		t.Fatalf("partition queries = %d, want %d", len(queries), len(tables))
	}
	for _, query := range queries {
		if len(query.Results) != 1 || query.Results[0].CHType.String() != "UInt32" || query.Results[0].GoType != "uint32" {
			t.Errorf("query %s results = %+v, want one UInt32/uint32 result", query.Name, query.Results)
		}
	}
}

func TestToYYYYMMDDRegistryMutationsBreakTheContract(t *testing.T) {
	schema := schemaFromDDL(t, toYYYYMMDDTestDDL)
	original := functionRegistry["toyyyymmdd"]
	check := func() error {
		got, err := inferTestExprType(t, schema, "toYYYYMMDD(dt)")
		if err != nil {
			return fmt.Errorf("positive call: %w", err)
		}
		if got != "UInt32" {
			return fmt.Errorf("positive type = %s, want UInt32", got)
		}
		if _, err := inferTestExprType(t, schema, "toYYYYMMDD(s)"); err == nil {
			return fmt.Errorf("String argument was accepted")
		}
		return nil
	}
	if err := check(); err != nil {
		t.Fatalf("production contract: %v", err)
	}

	mutations := map[string]func(){
		"missing rule": func() { delete(functionRegistry, "toyyyymmdd") },
		"wide domain": func() {
			mutated := original
			mutated.domain = &scalarArgumentDomain
			functionRegistry["toyyyymmdd"] = mutated
		},
		"wrong result": func() {
			mutated := original
			mutated.rule = fixedFunctionType("UInt64")
			functionRegistry["toyyyymmdd"] = mutated
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			functionRegistry["toyyyymmdd"] = original
			mutate()
			t.Cleanup(func() { functionRegistry["toyyyymmdd"] = original })
			if err := check(); err == nil {
				t.Fatal("contract accepted the registry mutation")
			}
		})
	}
}
