//go:build fuzzoracle

package engine

import (
	"strings"
	"testing"
)

const nullLiteralLiveDDL = `CREATE TABLE probe (
    flag UInt8,
    value Float64,
    ` + "`NULL`" + ` Int32,
    arr Array(Int32),
    map_value Map(String, Int32),
    tuple_value Tuple(Int32, String)
) ENGINE = Memory`

const nullLiteralLiveSeed = `INSERT INTO probe VALUES
(1, 7.5, 9, [1, 2], map('a', 1), (3, 'x')),
(0, 2.5, 11, [4], map('b', 2), (5, 'y'))`

func TestBareNullLiteralAgainstClickHouse(t *testing.T) {
	oracle := execWitnessFixtureWithDDL(t, nullLiteralLiveDDL, nullLiteralLiveSeed)
	tests := []struct {
		expression string
		wantType   string
		wantValues string
	}{
		{expression: "CAST(NULL, 'Nullable(Float64)')", wantType: "Nullable(Float64)", wantValues: "\\N\n\\N"},
		{expression: "CAST(NULL AS Nullable(Float64))", wantType: "Nullable(Float64)", wantValues: "\\N\n\\N"},
		{expression: "NULL IS NULL", wantType: "UInt8", wantValues: "1\n1"},
		{expression: "NULL IS NOT NULL", wantType: "UInt8", wantValues: "0\n0"},
		{expression: "if(flag = 1, NULL, value)", wantType: "Nullable(Float64)", wantValues: "\\N\n2.5"},
		{expression: "ifNull(NULL, value)", wantType: "Float64", wantValues: "7.5\n2.5"},
		{expression: "coalesce(NULL, value)", wantType: "Float64", wantValues: "7.5\n2.5"},
		{expression: "ifNull(NULL, arr)", wantType: "Array(Int32)", wantValues: "[1,2]\n[4]"},
		{expression: "coalesce(NULL, map_value)", wantType: "Map(String, Int32)", wantValues: "{'a':1}\n{'b':2}"},
		{expression: "ifNull(NULL, tuple_value)", wantType: "Tuple(Int32, String)", wantValues: "(3,'x')\n(5,'y')"},
		{expression: "CAST(if(flag = 1, NULL, NULL) AS Nullable(Int32))", wantType: "Nullable(Int32)", wantValues: "\\N\n\\N"},
		{expression: "CAST(ifNull(NULL, NULL), 'Nullable(Float64)')", wantType: "Nullable(Float64)", wantValues: "\\N\n\\N"},
		{expression: "CAST(coalesce(NULL, NULL) AS Nullable(Int32))", wantType: "Nullable(Int32)", wantValues: "\\N\n\\N"},
		{expression: "CAST(arrayElement([NULL], 1) AS Nullable(Int32))", wantType: "Nullable(Int32)", wantValues: "\\N\n\\N"},
		{expression: "CAST((NULL, 1) AS Tuple(Nullable(Int32), UInt8))", wantType: "Tuple(Nullable(Int32), UInt8)", wantValues: "(NULL,1)\n(NULL,1)"},
		{expression: "'NULL'", wantType: "String", wantValues: "NULL\nNULL"},
		{expression: "\"NULL\"", wantType: "Int32", wantValues: "9\n11"},
		{expression: "probe.NULL", wantType: "Int32", wantValues: "9\n11"},
	}
	for _, test := range tests {
		analysis, analysisErr := oracle.exec("SELECT DISTINCT toTypeName(" + test.expression + ") FROM probe FORMAT TabSeparatedRaw")
		if analysisErr != nil {
			t.Errorf("analysis of %s: %v", test.expression, analysisErr)
		} else if got := strings.TrimSpace(analysis); got != test.wantType {
			t.Errorf("analysis type of %s = %s, want %s", test.expression, got, test.wantType)
		}
		execution, executionErr := oracle.exec("SELECT " + test.expression + " FROM probe ORDER BY flag DESC FORMAT TabSeparatedRaw")
		if executionErr != nil {
			t.Errorf("execution of %s: %v", test.expression, executionErr)
		} else if got := strings.TrimSpace(execution); got != test.wantValues {
			t.Errorf("execution values of %s = %q, want %q", test.expression, got, test.wantValues)
		}
		gotType, inferErr := InferExpressionType(nullLiteralLiveDDL, "probe", test.expression)
		if inferErr != nil {
			t.Errorf("chgen inference of %s: %v", test.expression, inferErr)
		} else if got := gotType.String(); got != test.wantType {
			t.Errorf("chgen type of %s = %s, want %s", test.expression, got, test.wantType)
		}
	}
	for _, expression := range []string{
		"if(flag = 1, NULL, arr)",
		"multiIf(flag = 1, NULL, arr)",
		"CASE WHEN flag = 1 THEN NULL ELSE arr END",
		"if(flag = 1, NULL, map_value)",
		"if(flag = 1, NULL, tuple_value)",
		"[NULL, arr]",
		"CAST(NULL, 'Float64')",
		"CAST(NULL AS Int32)",
		"CAST(NULL, 'Array(Int32)')",
		"CAST(NULL, 'Nullable(Array(Int32))')",
		"CAST((NULL), 'Float64')",
		"CAST((NULL) AS Int32)",
		"CAST((((NULL))) AS Float64)",
		"CAST(if(flag = 1, NULL, NULL) AS Int32)",
		"CAST(ifNull(NULL, NULL), 'Float64')",
		"CAST(coalesce(NULL, NULL) AS Int32)",
		"CAST(arrayElement([NULL], 1) AS Int32)",
	} {
		if output, err := oracle.exec("SELECT DISTINCT toTypeName(" + expression + ") FROM probe FORMAT TabSeparatedRaw"); err == nil {
			t.Errorf("analysis of %s = %q, want a refusal", expression, output)
		}
		if output, err := oracle.exec("SELECT " + expression + " FROM probe FORMAT TabSeparatedRaw"); err == nil {
			t.Errorf("execution of %s = %q, want a refusal", expression, output)
		}
		if got, err := InferExpressionType(nullLiteralLiveDDL, "probe", expression); err == nil {
			t.Errorf("chgen type of %s = %s, want a refusal", expression, got.String())
		}
	}

	if _, err := oracle.exec("CREATE TABLE sink (value Nullable(Float64)) ENGINE = Memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := oracle.exec("INSERT INTO sink SELECT CAST(NULL, 'Nullable(Float64)') FROM probe"); err != nil {
		t.Fatalf("INSERT SELECT execution: %v", err)
	}
	witness, err := oracle.exec("SELECT toTypeName(value), isNull(value) FROM sink ORDER BY isNull(value) FORMAT TabSeparatedRaw")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(witness); got != "Nullable(Float64)\t1\nNullable(Float64)\t1" {
		t.Fatalf("INSERT SELECT witness = %q", got)
	}
}
