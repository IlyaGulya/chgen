package functionrules_test

import (
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen"
)

func TestBuildDependentMathInputsAreRefusedWithoutLosingPortableCalls(t *testing.T) {
	ddl := `CREATE TABLE t (i Int32, f Float64, f32 Float32, n Nullable(Float32),
		lc LowCardinality(Float32), lcn LowCardinality(Nullable(Float32)),
		a32 SimpleAggregateFunction(anyLast, Float32), an32 SimpleAggregateFunction(anyLast, Nullable(Float32)),
		a SimpleAggregateFunction(anyLast, Float64),
		s SimpleAggregateFunction(sum, Float64), an SimpleAggregateFunction(anyLast, Nullable(Float64))) ENGINE=Memory`
	for _, name := range []string{"exp", "log", "tanh"} {
		for _, column := range []string{"f32", "n", "lc", "lcn", "a32", "an32", "a", "s"} {
			_, err := chgen.InferExpressionType(ddl, "t", name+"("+column+")")
			if err == nil || !strings.Contains(err.Error(), "build-dependent") || !strings.Contains(err.Error(), "Float64") {
				t.Errorf("%s(%s) must explain build-dependent type and explicit cast: %v", name, column, err)
			}
		}
		for _, expression := range []string{name + "(i)", name + "(f)", name + "(?)", name + "(CAST(f32 AS Float64))", name + "(CAST(a AS Float64))"} {
			got, err := chgen.InferExpressionType(ddl, "t", expression)
			if err != nil || got.String() != "Float64" {
				t.Errorf("portable %s: %s, %v", expression, got.String(), err)
			}
		}
		for _, expression := range []string{name + "(an)", name + "(CAST(n AS Nullable(Float64)))"} {
			got, err := chgen.InferExpressionType(ddl, "t", expression)
			if err != nil || got.String() != "Nullable(Float64)" {
				t.Errorf("portable nullable %s: %s, %v", expression, got.String(), err)
			}
		}
	}
}

func TestMeasuredScalarSpelling(t *testing.T) {
	ddl := "CREATE TABLE t (i Int32) ENGINE=Memory"
	for _, name := range []string{"cbrt", "cosh", "erf", "erfc", "exp10", "exp2", "lgamma", "sinh", "tgamma"} {
		got, err := chgen.InferExpressionType(ddl, "t", name+"(i)")
		if err != nil || got.String() != "Float64" {
			t.Errorf("canonical %s: got %s, %v", name, got.String(), err)
		}
		for _, spelling := range []string{strings.ToUpper(name), strings.ToUpper(name[:1]) + name[1:]} {
			if _, err := chgen.InferExpressionType(ddl, "t", spelling+"(i)"); err == nil || !strings.Contains(err.Error(), "case-sensitive spelling "+name) {
				t.Errorf("case-sensitive function %s: expected canonical spelling diagnostic, got %v", spelling, err)
			}
		}
	}
	for _, name := range []string{"SIN", "Sin"} {
		if _, err := chgen.InferExpressionType(ddl, "t", name+"(i)"); err != nil {
			t.Errorf("case-insensitive %s: %v", name, err)
		}
	}
}

func TestGeneratedStringRuleIsUsedByPublicInference(t *testing.T) {
	ddl := "CREATE TABLE t (s String, n Nullable(String), lc LowCardinality(String), f FixedString(8), i Int32, a SimpleAggregateFunction(anyLast, String), an SimpleAggregateFunction(anyLast, Nullable(String))) ENGINE=Memory"
	for _, test := range []struct{ expression, want string }{
		{"lowerUTF8(s)", "String"}, {"lowerUTF8(n)", "Nullable(String)"},
		{"lowerUTF8(lc)", "LowCardinality(String)"},
		{"lowerUTF8(a)", "SimpleAggregateFunction(anyLast, String)"}, {"lowerUTF8(an)", "Nullable(String)"},
	} {
		got, err := chgen.InferExpressionType(ddl, "t", test.expression)
		if err != nil || got.String() != test.want {
			t.Errorf("%s: %s, %v; want %s", test.expression, got.String(), err, test.want)
		}
	}
	for _, expression := range []string{"lowerutf8(s)", "LOWERUTF8(s)", "lowerUTF8(i)", "lowerUTF8(f)", "lowerUTF8(s,s)", "lowerUTF8()"} {
		if _, err := chgen.InferExpressionType(ddl, "t", expression); err == nil {
			t.Errorf("unproved string call accepted: %s", expression)
		}
	}
}

func TestGeneratedMeasuredRuleIsUsedByPublicInference(t *testing.T) {
	ddl := "CREATE TABLE t (i Int32, n Nullable(Int32), lc LowCardinality(Int32), d Decimal(9, 2), s String) ENGINE = Memory"
	for _, test := range []struct{ expression, want string }{
		{"sin(i)", "Float64"}, {"sin(n)", "Nullable(Float64)"}, {"sin(lc)", "LowCardinality(Float64)"},
		{"sin(d)", "Float64"},
	} {
		got, err := chgen.InferExpressionType(ddl, "t", test.expression)
		if err != nil || got.String() != test.want {
			t.Fatalf("%s: got %s, %v; want %s", test.expression, got.String(), err, test.want)
		}
	}
	for _, expression := range []string{"sin(s)", "sin()", "sin(i, i)", "sin(1)(i)"} {
		if _, err := chgen.InferExpressionType(ddl, "t", expression); err == nil {
			t.Fatalf("unproved call %s was accepted", expression)
		}
	}
}

func TestMeasuredFamiliesKeepTheirPublicDomainDiagnostics(t *testing.T) {
	ddl := "CREATE TABLE t (s String, i Int32) ENGINE=Memory"
	for _, test := range []struct{ expression, diagnostic string }{
		{"sin(s)", "function sin does not accept an argument of type String; ClickHouse needs bool, float32, float64, int128, int16, int256, int32, int64, int8, uint128, uint16, uint256, uint32, uint64, uint8, Decimal here;"},
		{"sign(s)", "function sign does not accept an argument of type String; ClickHouse needs bool, float32, float64, int128, int16, int256, int32, int64, int8, uint128, uint16, uint256, uint32, uint64, uint8, Decimal here;"},
		{"lowerUTF8(i)", "function lowerUTF8 does not accept an argument of type Int32; ClickHouse needs String here;"},
		{"regexpQuoteMeta(i)", "function regexpQuoteMeta does not accept an argument of type Int32; ClickHouse needs String here;"},
		{"base64Encode(i)", "function base64Encode does not accept an argument of type Int32; ClickHouse needs String, FixedString here;"},
	} {
		_, err := chgen.InferExpressionType(ddl, "t", test.expression)
		if err == nil || !strings.Contains(err.Error(), test.diagnostic) {
			t.Errorf("%s: expected unchanged diagnostic %q, got %v", test.expression, test.diagnostic, err)
		}
	}
	got, err := chgen.InferExpressionType(ddl, "t", "sign(i)")
	if err != nil || got.String() != "Int8" {
		t.Fatalf("sign keeps its distinct result: got %s, %v", got.String(), err)
	}
}

func TestMeasuredExpansionKeepsExistingArrayResultsHonest(t *testing.T) {
	ddl := "CREATE TABLE t (a Array(Polygon), p Point) ENGINE=Memory"
	for _, test := range []struct{ expression, want string }{
		{"arraySlice(a, 1, 2)", "Array(Array(Array(Tuple(Float64, Float64))))"},
		{"arrayResize(a, 1)", "Array(Array(Array(Tuple(Float64, Float64))))"},
		{"arrayReverseSort(a)", "Array(Array(Array(Tuple(Float64, Float64))))"},
		{"arrayMap(x -> x, a)", "Array(Array(Array(Tuple(Float64, Float64))))"},
		{"tuple(p)", "Tuple(Point)"},
		{"array(p)", "Array(Point)"},
	} {
		got, err := chgen.InferExpressionType(ddl, "t", test.expression)
		if err != nil || got.String() != test.want {
			t.Errorf("%s: got %s, %v; want %s", test.expression, got.String(), err, test.want)
		}
	}
}

func TestMeasuredExpansionRejectsInvalidArrayResizeSizes(t *testing.T) {
	ddl := `CREATE TABLE t (a Array(Int32), i Int32, f Float64, d Decimal(9, 2), u UInt128,
		n Nullable(Int32), nf Nullable(Float64), nd Nullable(Decimal(9, 2)), e Enum16('a'=1)) ENGINE=Memory`
	for _, size := range []string{"i", "f", "d", "u"} {
		got, err := chgen.InferExpressionType(ddl, "t", "arrayResize(a, "+size+")")
		if err != nil || got.String() != "Array(Int32)" {
			t.Errorf("legal size %s: %s, %v", size, got.String(), err)
		}
	}
	for _, size := range []string{"n", "nf", "nd", "e"} {
		if _, err := chgen.InferExpressionType(ddl, "t", "arrayResize(a, "+size+")"); err == nil {
			t.Errorf("illegal size %s was accepted", size)
		}
	}
}
