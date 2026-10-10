package functionrules_test

import (
	"strings"
	"testing"

	"github.com/IlyaGulya/chgen"
)

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
