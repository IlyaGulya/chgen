//go:build fuzzoracle

package engine

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestAggregateSignatureBoundariesAgainstClickHouse uses two independent
// server witnesses for each boundary. DESCRIBE checks analysis. SELECT checks
// execution against the same real fixture columns.
func TestAggregateSignatureBoundariesAgainstClickHouse(t *testing.T) {
	if *gridURL == "" {
		t.Skip("-chgen-grid-url is not set; start ClickHouse to run this test")
	}
	oracle := &chOracle{url: *gridURL, client: &http.Client{Timeout: 120 * time.Second}}
	database := fmt.Sprintf("chgen_aggregate_signature_%d", time.Now().UnixNano())
	if _, err := oracle.adminExec("CREATE DATABASE " + database); err != nil {
		t.Fatalf("create run database: %v", err)
	}
	oracle.database = database
	t.Cleanup(func() {
		if _, err := oracle.adminExec("DROP DATABASE IF EXISTS " + database); err != nil {
			t.Logf("drop run database: %v", err)
		}
	})

	const ddl = `CREATE TABLE aggregate_signature_fixture (
	    i32 Int32,
	    u8 UInt8,
	    nu8 Nullable(UInt8),
	    b Bool,
    s String,
    f32 Float32,
    arr_i Array(Int32),
    sum_state AggregateFunction(sum, Int32),
    uniq_state AggregateFunction(uniq, UInt64),
    sum_if_state AggregateFunction(sumIf, Int32, UInt8)
) ENGINE = AggregatingMergeTree ORDER BY tuple()`
	if _, err := oracle.exec(ddl); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	const seed = `INSERT INTO aggregate_signature_fixture SELECT
	    1, 1, toNullable(toUInt8(1)), true, 'x', 1.0, [1, 2],
    CAST(sumState(toInt32(1)), 'AggregateFunction(sum, Int32)'),
    CAST(uniqState(toUInt64(1)), 'AggregateFunction(uniq, UInt64)'),
    CAST(sumIfState(toInt32(1), toUInt8(1)), 'AggregateFunction(sumIf, Int32, UInt8)')`
	if _, err := oracle.exec(seed); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	testCases := []struct {
		name        string
		expression  string
		wantType    string
		wantCode    string
		chgenRefuse bool
	}{
		{name: "count UInt8", expression: "countIf(u8)", wantType: "UInt64"},
		{name: "count Nullable UInt8", expression: "countIf(nu8)", wantType: "UInt64"},
		{name: "count Bool", expression: "countIf(b)", wantType: "UInt64"},
		{name: "count String", expression: "countIf(s)", wantCode: "43", chgenRefuse: true},
		{name: "count Int32", expression: "countIf(i32)", wantCode: "43", chgenRefuse: true},
		{name: "count Array", expression: "countIf(arr_i)", wantCode: "43", chgenRefuse: true},
		{name: "registered Distinct", expression: "countDistinct(i32)", wantType: "UInt64"},
		{name: "state positive", expression: "sumState(i32)", wantType: "AggregateFunction(sum, Int32)"},
		{name: "state arity", expression: "sumState(i32, u8)", wantCode: "42", chgenRefuse: true},
		{name: "array positive", expression: "sumArray(arr_i)", wantType: "Int64"},
		{name: "array role", expression: "sumArray(i32)", wantCode: "43", chgenRefuse: true},
		{name: "array arity", expression: "sumArray(arr_i, arr_i)", wantCode: "42", chgenRefuse: true},
		{name: "or null positive", expression: "sumOrNull(i32)", wantType: "Nullable(Int64)"},
		{name: "or null arity", expression: "sumOrNull(i32, u8)", wantCode: "42", chgenRefuse: true},
		{name: "merge positive", expression: "uniqMerge(uniq_state)", wantType: "UInt64"},
		{name: "merge arity", expression: "uniqMerge(uniq_state, uniq_state)", wantCode: "42", chgenRefuse: true},
		{name: "merge identity", expression: "sumIfMerge(sum_state)", wantCode: "43", chgenRefuse: true},
		{name: "if merge positive", expression: "sumIfMerge(sum_if_state)", wantType: "Int64"},
		{name: "resample positive", expression: "sumResample(0, 10, 1)(i32, i32)", wantType: "Array(Int64)"},
		{name: "resample nullable key", expression: "sumResample(0, 10, 1)(i32, nu8)", wantType: "Array(Int64)"},
		{name: "resample key String", expression: "sumResample(0, 10, 1)(i32, s)", wantCode: "43", chgenRefuse: true},
		{name: "resample key Float32", expression: "sumResample(0, 10, 1)(i32, f32)", wantCode: "43", chgenRefuse: true},
		{name: "resample zero step", expression: "sumResample(0, 10, 0)(i32, i32)", wantCode: "69", chgenRefuse: true},
		{name: "resample bucket limit", expression: "sumResample(0, 1048576, 1)(i32, i32)", wantType: "Array(Int64)"},
		{name: "resample bucket overflow", expression: "sumResample(0, 1048577, 1)(i32, i32)", wantCode: "69", chgenRefuse: true},
		{name: "group array positive size", expression: "groupArray(1)(i32)", wantType: "Array(Int32)"},
		{name: "group array zero size", expression: "groupArray(0)(i32)", wantCode: "36", chgenRefuse: true},
		{name: "group array negative size", expression: "groupArray(-1)(i32)", wantCode: "36", chgenRefuse: true},
		{name: "group array If positive size", expression: "groupArrayIf(1)(i32, b)", wantType: "Array(Int32)"},
		{name: "group array If zero size", expression: "groupArrayIf(0)(i32, b)", wantCode: "36", chgenRefuse: true},
		{name: "group uniq array positive size", expression: "groupUniqArray(1)(i32)", wantType: "Array(Int32)"},
		{name: "group uniq array zero size", expression: "groupUniqArray(0)(i32)", wantCode: "36", chgenRefuse: true},
		{name: "group uniq array negative size", expression: "groupUniqArray(-1)(i32)", wantCode: "36", chgenRefuse: true},
		{name: "group uniq array If positive size", expression: "groupUniqArrayIf(1)(i32, b)", wantType: "Array(Int32)"},
		{name: "group uniq array If zero size", expression: "groupUniqArrayIf(0)(i32, b)", wantCode: "36", chgenRefuse: true},
		{name: "unmodeled Array State", expression: "sumArrayState(arr_i)", chgenRefuse: true},
		{name: "unmodeled Distinct", expression: "sumDistinct(i32)", chgenRefuse: true},
		{name: "unmodeled For Each", expression: "sumForEach(arr_i)", chgenRefuse: true},
		{name: "unmodeled Arg Max", expression: "sumArgMax(i32, i32)", chgenRefuse: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			analysis, analysisErr := oracle.exec("DESCRIBE TABLE (SELECT " + testCase.expression + " AS result FROM aggregate_signature_fixture) FORMAT TabSeparatedRaw")
			executionErr := func() error {
				_, err := oracle.exec("SELECT ignore(" + testCase.expression + ") FROM aggregate_signature_fixture FORMAT Null")
				return err
			}()
			if testCase.wantCode != "" {
				assertAggregateLiveCode(t, "analysis", analysisErr, testCase.wantCode)
				assertAggregateLiveCode(t, "execution", executionErr, testCase.wantCode)
			} else {
				if analysisErr != nil {
					t.Fatalf("analysis: %v", analysisErr)
				}
				if executionErr != nil {
					t.Fatalf("execution: %v", executionErr)
				}
				if testCase.wantType != "" {
					fields := strings.Split(analysis, "\t")
					if len(fields) < 2 || normalizeTypeName(fields[1]) != normalizeTypeName(testCase.wantType) {
						t.Fatalf("analysis type = %q, want %s", analysis, testCase.wantType)
					}
				}
			}

			got, inferErr := InferExpressionType(ddl, "aggregate_signature_fixture", testCase.expression)
			if testCase.chgenRefuse {
				if inferErr == nil {
					t.Fatalf("chgen type = %s, want a refusal", got.String())
				}
				return
			}
			if inferErr != nil {
				t.Fatalf("chgen: %v", inferErr)
			}
			if normalizeTypeName(got.String()) != normalizeTypeName(testCase.wantType) {
				t.Fatalf("chgen type = %s, want %s", got.String(), testCase.wantType)
			}
		})
	}
}

func assertAggregateLiveCode(t *testing.T, witness string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s accepted the expression, want Code %s", witness, want)
	}
	if got := clickHouseErrorCode(err.Error()); got != want {
		t.Fatalf("%s code = %s, want %s: %v", witness, got, want, err)
	}
}
