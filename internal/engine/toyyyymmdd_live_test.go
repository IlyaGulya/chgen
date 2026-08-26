//go:build fuzzoracle

package engine

import (
	"strings"
	"testing"
)

const toYYYYMMDDLiveDDL = `CREATE TABLE t (
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
) ENGINE = Memory SETTINGS allow_suspicious_low_cardinality_types = 1`

const toYYYYMMDDLiveSeed = `INSERT INTO t VALUES (
    toDate('2024-02-03'),
    toDate32('1900-02-03'),
    toDateTime('2024-02-03 12:00:00'),
    toDateTime64('2024-02-03 12:00:00.123', 3),
    toNullable(toDate('2024-02-03')),
    toLowCardinality(toDate('2024-02-03')),
    toLowCardinality(toNullable(toDate('2024-02-03'))),
    '2024-02-03',
    1,
    toDecimal32(1, 2),
    [toDate('2024-02-03')],
    toUUID('00000000-0000-0000-0000-000000000001')
)`

func TestToYYYYMMDDAgainstClickHouse(t *testing.T) {
	oracle := execWitnessFixtureWithDDL(t, toYYYYMMDDLiveDDL, toYYYYMMDDLiveSeed)
	schema, err := schemaFromDDLErr(t, toYYYYMMDDLiveDDL)
	if err != nil {
		t.Fatal(err)
	}

	accepted := map[string]string{
		"toYYYYMMDD(d)":                    "UInt32",
		"toYYYYMMDD(d32)":                  "UInt32",
		"toYYYYMMDD(dt)":                   "UInt32",
		"toYYYYMMDD(dt64)":                 "UInt32",
		"toYYYYMMDD(nd)":                   "Nullable(UInt32)",
		"toYYYYMMDD(lcd)":                  "LowCardinality(UInt32)",
		"toYYYYMMDD(lcnd)":                 "LowCardinality(Nullable(UInt32))",
		"toYYYYMMDD(toDate('2024-02-03'))": "UInt32",
	}
	for expression, want := range accepted {
		analysis, analysisErr := oracle.exec("SELECT toTypeName(" + expression + ") FROM t")
		if analysisErr != nil {
			t.Errorf("analysis of %s: %v", expression, analysisErr)
			continue
		}
		if got := strings.TrimSpace(analysis); got != want {
			t.Errorf("analysis type of %s = %s, want %s", expression, got, want)
		}
		if _, executionErr := oracle.exec("SELECT ignore(" + expression + ") FROM t"); executionErr != nil {
			t.Errorf("execution of %s: %v", expression, executionErr)
		}
		got, inferErr := chgenInferType(schema, expression)
		if inferErr != nil {
			t.Errorf("chgen inference of %s: %v", expression, inferErr)
		} else if got != want {
			t.Errorf("chgen type of %s = %s, want %s", expression, got, want)
		}
	}

	// ClickHouse accepts the optional timezone. chgen refuses this form on
	// purpose until timezone value validation has its own measured rule.
	const timezoneForm = "toYYYYMMDD(dt, 'UTC')"
	if _, err := oracle.exec("SELECT toTypeName(" + timezoneForm + ") FROM t"); err != nil {
		t.Fatalf("analysis of the server-only timezone form: %v", err)
	}
	if _, err := oracle.exec("SELECT ignore(" + timezoneForm + ") FROM t"); err != nil {
		t.Fatalf("execution of the server-only timezone form: %v", err)
	}
	if _, err := chgenInferType(schema, timezoneForm); err == nil {
		t.Fatal("chgen accepted the unmodeled timezone form")
	}

	for _, expression := range []string{
		"toYYYYMMDD(s)",
		"toYYYYMMDD(i32)",
		"toYYYYMMDD(dec)",
		"toYYYYMMDD(dates)",
		"toYYYYMMDD(uid)",
	} {
		if _, err := oracle.exec("DESCRIBE TABLE (SELECT " + expression + " AS result FROM t) FORMAT TabSeparatedRaw"); err == nil || clickHouseErrorCode(err.Error()) != "43" {
			t.Errorf("analysis of %s = %v, want Code 43", expression, err)
		}
		if _, err := oracle.exec("SELECT ignore(" + expression + ") FROM t FORMAT Null"); err == nil || clickHouseErrorCode(err.Error()) != "43" {
			t.Errorf("execution of %s = %v, want Code 43", expression, err)
		}
		if _, err := chgenInferType(schema, expression); err == nil {
			t.Errorf("chgen accepted %s", expression)
		}
	}
}
