package functionrules_test

import (
	"crypto/rand"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IlyaGulya/chgen"
)

// ClickHouse 25.8.29.51 accepts an aggregate state as the returned value:
// only the ordering key of argMin/argMax must be comparable.
func TestAggregateCombinatorsConstrainTheKeyNotTheReturnedValue(t *testing.T) {
	ddl := "CREATE TABLE t (v AggregateFunction(sum, Int32), k Int32, b UInt8) ENGINE=Memory"
	got, err := chgen.InferExpressionType(ddl, "t", "argMinOrDefault(v, k)")
	if err != nil || got.String() != "AggregateFunction(sum, Int32)" {
		t.Fatalf("argMinOrDefault: %s, %v", got.String(), err)
	}
}

func TestAggregateCallPathsKeepWrappersAndDiagnostics(t *testing.T) {
	ddl := `CREATE TABLE t (
		i Int32, n Nullable(Int32), b UInt8, nb Nullable(UInt8), s String,
		dt DateTime('UTC'), dt64 DateTime64(3, 'UTC'), dec Decimal(18, 4),
		a Array(Nullable(Int32)), saf SimpleAggregateFunction(sum, Int64),
		v AggregateFunction(sum, Int32)
	) ENGINE=Memory`
	for _, test := range []struct{ expression, want string }{
		{"sumIf(i, b)", "Int64"},
		{"sumIf(n, b)", "Nullable(Int64)"},
		{"sumIf(saf, b)", "Int64"},
		{"maxIf(saf, b)", "SimpleAggregateFunction(sum, Int64)"},
		{"avgIf(n, b)", "Nullable(Float64)"},
		{"maxIf(dt64, b)", "DateTime64(3, 'UTC')"},
		{"sumIf(dec, b)", "Decimal(38, 4)"},
		{"groupArrayIf(dt, b)", "Array(DateTime('UTC'))"},
		{"groupUniqArrayIf(dt, b)", "Array(DateTime)"},
		{"uniqExactIf(n, b)", "UInt64"},
		{"countIf(nb)", "UInt64"},
		{"argMinIf(v, i, b)", "AggregateFunction(sum, Int32)"},
		{"sumArrayIf(a, b)", "Nullable(Int64)"},
		{"sumIfOrNull(i, b)", "Nullable(Int64)"},
		{"quantileStateIf(0.5)(n, b)", "AggregateFunction(quantile(0.5), Int32)"},
		{"quantileIfState(0.5)(n, b)", "AggregateFunction(quantileIf(0.5), Nullable(Int32), UInt8)"},
	} {
		t.Run(test.expression, func(t *testing.T) {
			got, err := chgen.InferExpressionType(ddl, "t", test.expression)
			if err != nil || got.String() != test.want {
				t.Fatalf("got %s, %v; want %s", got.String(), err, test.want)
			}
		})
	}
	for _, test := range []struct{ expression, diagnostic string }{
		{"sumIf(i, missing)", `function sumIf condition: column "missing"`},
		{"sumArrayIf(a, missing)", `function sumArrayIf condition: column "missing"`},
		{"sumIf(i, s)", "function sumIf takes the last argument as the -If condition"},
		{"sumArrayIf(a, s)", "function sumArrayIf takes the last argument as the -If condition"},
		{"avgIf(s, ?)", "function avgIf does not accept an argument of type String"},
		{"sumIf(?, b)", "function sumIf first argument: positional placeholder has no result type"},
		{"quantileStateIf(0.5)(?, b)", "function quantileStateIf argument: positional placeholder has no result type"},
		{"sumIf(sum(i), b)", "aggregate function sum"},
		{"argMinIf(i, v, b)", "function argMinIf does not accept an argument of type AggregateFunction"},
		{"argMinOrDefault(i, v)", "function argMinOrDefault does not accept an argument of type AggregateFunction"},
		{"sumIf(i, b, b)", "function sumIf rejects argument position 4"},
		{"sumArrayIf(a, a, b)", "function sumArrayIf base aggregate rejects 2 data arguments"},
	} {
		t.Run(test.expression, func(t *testing.T) {
			_, err := chgen.InferExpressionType(ddl, "t", test.expression)
			if err == nil || !strings.Contains(err.Error(), test.diagnostic) {
				t.Fatalf("lost diagnostic %q: %v", test.diagnostic, err)
			}
		})
	}
}

func TestFixedAggregateCombinatorsKeepUnresolvedPlaceholderBehavior(t *testing.T) {
	ddl := "CREATE TABLE t (b UInt8, n Nullable(Int32)) ENGINE=Memory"
	for _, test := range []struct{ expression, want string }{
		{"avgIf(?, b)", "Float64"},
		{"avgIf(n, ?)", "Float64"},
		{"uniqExactIf(?, b)", "UInt64"},
		{"countIf(?)", "UInt64"},
	} {
		got, err := chgen.InferExpressionType(ddl, "t", test.expression)
		if err != nil || got.String() != test.want {
			t.Fatalf("%s: %s, %v; want %s", test.expression, got.String(), err, test.want)
		}
	}
}

func TestAggregateCombinatorKeyDomainAgainstClickHouse(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" {
		t.Skip("CHGEN_ORACLE_URL is not set")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	execute := func(sql string) (int, string) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(sql))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, strings.TrimSpace(string(body))
	}
	if status, version := execute("SELECT version()"); status != http.StatusOK || version != "25.8.29.51" {
		t.Fatalf("wrong server: %d, %s", status, version)
	}
	table := "chgen_combinator_" + strings.ToLower(rand.Text())
	ddl := "CREATE TABLE " + table + " (v AggregateFunction(sum, Int32), k Int32, b UInt8) ENGINE=Memory"
	if status, body := execute(ddl); status != http.StatusOK {
		t.Fatalf("create: %s", body)
	}
	defer func() {
		if status, body := execute("DROP TABLE " + table); status != http.StatusOK {
			t.Errorf("cleanup: %s", body)
		}
	}()
	if status, body := execute("INSERT INTO " + table + " SELECT sumState(toInt32(number)), toInt32(1), toUInt8(1) FROM numbers(3)"); status != http.StatusOK {
		t.Fatalf("seed: %s", body)
	}
	for _, name := range []string{"argMinOrDefault", "argMaxOrDefault", "argMinIf", "argMaxIf"} {
		t.Run(name, func(t *testing.T) {
			expression := name + "(v, k)"
			invalid := name + "(k, v)"
			if strings.HasSuffix(name, "If") {
				expression = name + "(v, k, b)"
				invalid = name + "(k, v, b)"
			}
			got, err := chgen.InferExpressionType(ddl, table, expression)
			if err != nil || got.String() != "AggregateFunction(sum, Int32)" {
				t.Fatalf("public inference: %s, %v", got.String(), err)
			}
			if status, body := execute("SELECT toTypeName(" + expression + ") FROM " + table); status != http.StatusOK || body != "AggregateFunction(sum, Int32)" {
				t.Fatalf("analysis: %d, %s", status, body)
			}
			if status, body := execute("SELECT ignore(" + expression + ") FROM " + table + " FORMAT Null"); status != http.StatusOK {
				t.Fatalf("execution: %s", body)
			}
			if _, err := chgen.InferExpressionType(ddl, table, invalid); err == nil {
				t.Fatal("public inference accepted a non-comparable key")
			}
			if status, body := execute("SELECT ignore(" + invalid + ") FROM " + table + " FORMAT Null"); status == http.StatusOK || !strings.Contains(body, "Code: 43.") {
				t.Fatalf("invalid key: %d, %s", status, body)
			}
		})
	}
}
