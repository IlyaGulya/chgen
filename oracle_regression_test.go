package chgen_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IlyaGulya/chgen"
)

func TestOracleRegressionBoundaryAgainstClickHouse(t *testing.T) {
	endpoint := os.Getenv("CHGEN_ORACLE_URL")
	if endpoint == "" {
		t.Skip("CHGEN_ORACLE_URL is not set")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	query := func(ctx context.Context, sql string) (string, bool) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(sql))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(data)), response.StatusCode == http.StatusOK
	}
	database := fmt.Sprintf("chgen_boundary_%d", time.Now().UnixNano())
	if body, ok := query(t.Context(), "CREATE DATABASE "+database); !ok {
		t.Fatal(body)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if body, ok := query(ctx, "DROP DATABASE "+database); !ok {
			t.Error(body)
		}
	})
	const columns = `(a Array(Int32), n Nullable(UInt64), f Float64, i Int64,
iv IntervalWeek, ih IntervalHour, d Decimal(18,4), day Date32, dt DateTime64(3),
multi MultiLineString, poly Polygon, ring Ring, m Map(String,Array(Int32))) ENGINE=Memory`
	ddl := "CREATE TABLE events " + columns
	if body, ok := query(t.Context(), "CREATE TABLE "+database+".events "+columns); !ok {
		t.Fatal(body)
	}
	if body, ok := query(t.Context(), "INSERT INTO "+database+`.events VALUES
([42],NULL,1,1,1,1,1,'2024-01-01','2024-01-01 00:00:00', [[(1,2)]], [[(1,2)]], [(1,2)], {'a':[1]})`); !ok {
		t.Fatal(body)
	}
	for _, tc := range []struct{ expression, want string }{
		{"arrayElement(arrayDistinct(a),n)", "Nullable(Int32)"},
		{"arrayElement([a],n)", ""},
		{"arraySlice(a,f)", ""},
		{"arraySlice(a,1,f)", ""},
		{"arraySlice(a,n)", "Array(Int32)"},
		{"assumeNotNull(multi)", "Array(Array(Tuple(Float64, Float64)))"},
		{"greatest(poly)", "Array(Array(Tuple(Float64, Float64)))"},
		{"least(ring)", "Array(Tuple(Float64, Float64))"},
		{"equals(d,iv)", ""},
		{"greater(dt,iv)", ""},
		{"nullIf(iv,day)", ""},
		{"nullIf(iv,poly)", ""},
		{"equals(iv,i)", "UInt8"},
		{"equals(iv,ih)", "UInt8"},
		{"notEquals(n,multi)", ""},
		{"notEquals(multi,m)", ""},
		{"equals(multi,poly)", "UInt8"},
		{"arrayCumSum(x -> x, array(iv))", ""},
		{"toDateTime64(poly,3)", ""},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			inferred, err := chgen.InferExpressionType(ddl, "events", tc.expression)
			body, executed := query(t.Context(), "SELECT "+tc.expression+" FROM "+database+".events FORMAT Null")
			if tc.want == "" {
				if err == nil || executed {
					t.Fatalf("expected both sides to refuse; chgen=%s error=%v; server executed=%t: %s", inferred.String(), err, executed, body)
				}
				return
			}
			if err != nil || inferred.String() != tc.want || !executed {
				t.Fatalf("chgen=%s error=%v; server executed=%t: %s; want %s", inferred.String(), err, executed, body, tc.want)
			}
			body, ok := query(t.Context(), "SELECT toTypeName("+tc.expression+") FROM "+database+".events FORMAT TabSeparatedRaw")
			if !ok || body != tc.want {
				t.Fatalf("server type=%s; want %s", body, tc.want)
			}
		})
	}
}

func TestNullableArrayIndexProducesNullableResult(t *testing.T) {
	queries, err := parsePublicQuery(t,
		"CREATE TABLE events (a Array(Int32), n Nullable(UInt64)) ENGINE=Memory",
		"-- name: Read :many\nSELECT arrayElement(arrayDistinct(a), n) AS value FROM events;")
	if err != nil {
		t.Fatal(err)
	}
	result := queries[0].Results[0]
	if result.CHType.String() != "Nullable(Int32)" || result.GoType != "*int32" {
		t.Fatalf("nullable index result = %+v; want Nullable(Int32) / *int32", result)
	}
}

func TestNullableArrayIndexKeepsOneNullableLayer(t *testing.T) {
	queries, err := parsePublicQuery(t,
		"CREATE TABLE events (a Array(Nullable(Int32)), n Nullable(UInt64)) ENGINE=Memory",
		"-- name: Read :many\nSELECT arrayElement(a, n) AS value FROM events;")
	if err != nil {
		t.Fatal(err)
	}
	if got := queries[0].Results[0].CHType.String(); got != "Nullable(Int32)" {
		t.Fatalf("type = %s", got)
	}
}

func TestNullableArrayIndexRefusesContainerResult(t *testing.T) {
	_, err := parsePublicQuery(t,
		"CREATE TABLE events (a Array(Array(Int32)), n Nullable(UInt64)) ENGINE=Memory",
		"-- name: Read :many\nSELECT arrayElement(a, n) AS value FROM events;")
	if err == nil {
		t.Fatal("accepted a nullable container result")
	}
}

func TestScalarGeometryResultsUseStructuralTypes(t *testing.T) {
	const ddl = "CREATE TABLE events (multi MultiLineString, poly Polygon, ring Ring) ENGINE=Memory"
	for _, tc := range []struct{ expression, want string }{
		{"assumeNotNull(multi)", "Array(Array(Tuple(Float64, Float64)))"},
		{"greatest(multi)", "Array(Array(Tuple(Float64, Float64)))"},
		{"greatest(poly)", "Array(Array(Tuple(Float64, Float64)))"},
		{"least(poly)", "Array(Array(Tuple(Float64, Float64)))"},
		{"least(ring)", "Array(Tuple(Float64, Float64))"},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			got, err := chgen.InferExpressionType(ddl, "events", tc.expression)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Fatalf("type = %s; want %s", got.String(), tc.want)
			}
		})
	}
}

func TestArraySliceRejectsFloatingPointOffsets(t *testing.T) {
	const ddl = "CREATE TABLE events (a Array(Int32), f Float64) ENGINE=Memory"
	for _, expression := range []string{"arraySlice(a, f)", "arraySlice(a, 1, f)"} {
		_, err := parsePublicQuery(t, ddl, "-- name: Read :many\nSELECT "+expression+" AS value FROM events;")
		if err == nil {
			t.Fatalf("accepted %s", expression)
		}
	}
}

func TestIntervalComparisonsRefuseIncompatibleValueTypes(t *testing.T) {
	const ddl = `CREATE TABLE events (iv IntervalWeek, d Decimal(18,4), dt DateTime64(3), day Date32) ENGINE=Memory`
	for _, expression := range []string{
		"equals(d,iv)", "equals(iv,d)", "greater(dt,iv)", "greater(iv,dt)",
		"nullIf(iv,day)", "sumIf(equals(d,iv),1)", "floor(equals(d,iv))",
	} {
		t.Run(expression, func(t *testing.T) {
			if _, err := parsePublicQuery(t, ddl, "-- name: Read :many\nSELECT "+expression+" AS value FROM events;"); err == nil {
				t.Fatal("accepted incompatible comparison")
			}
		})
	}
}

func TestGeometryComparisonsUseStructuralDomains(t *testing.T) {
	const ddl = `CREATE TABLE events (multi MultiLineString, poly Polygon, nu Nullable(UInt64), m Map(String,Array(Int32))) ENGINE=Memory`
	for _, expression := range []string{"equals(nu,multi)", "equals(multi,nu)", "notEquals(multi,m)", "notEquals(m,multi)"} {
		if _, err := chgen.InferExpressionType(ddl, "events", expression); err == nil {
			t.Fatalf("accepted %s", expression)
		}
	}
	if _, err := chgen.InferExpressionType(ddl, "events", "equals(multi,poly)"); err != nil {
		t.Fatal(err)
	}
}

func TestIntervalComparisonsRejectContainersAndKeepCounts(t *testing.T) {
	const ddl = `CREATE TABLE events (iv IntervalWeek, ih IntervalHour, p MultiPolygon, i Int64, f Float64, dt DateTime) ENGINE=Memory`
	if _, err := chgen.InferExpressionType(ddl, "events", "nullIf(iv,p)"); err == nil {
		t.Fatal("accepted interval/container comparison")
	}
	for _, expression := range []string{"equals(iv,ih)", "equals(iv,i)", "equals(iv,f)", "equals(iv,dt)"} {
		if _, err := chgen.InferExpressionType(ddl, "events", expression); err != nil {
			t.Fatalf("%s: %v", expression, err)
		}
	}
}

func TestArrayReductionDoesNotTreatIntervalAsAnInteger(t *testing.T) {
	const ddl = "CREATE TABLE events (iv IntervalWeek) ENGINE=Memory"
	for _, expression := range []string{"arrayCumSum(x -> x, array(iv))", "arrayCumSum(array(iv))", "arraySum(x -> x, array(iv))"} {
		if _, err := chgen.InferExpressionType(ddl, "events", expression); err == nil {
			t.Fatalf("accepted %s", expression)
		}
	}
}

func TestTimestampConversionRefusesGeometryContainers(t *testing.T) {
	const ddl = "CREATE TABLE events (line LineString, poly MultiPolygon) ENGINE=Memory"
	for _, expression := range []string{"toDateTime64(line,2)", "toDateTime64(poly,3)"} {
		if _, err := chgen.InferExpressionType(ddl, "events", expression); err == nil {
			t.Fatalf("accepted %s", expression)
		}
	}
}
