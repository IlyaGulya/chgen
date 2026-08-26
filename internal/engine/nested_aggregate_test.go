package engine

import (
	"strings"
	"testing"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// nestedAggregateSchema holds the columns that the Code 184 cells use.
// Every measurement behind this file was made over these COLUMNS and
// never over literals, because ClickHouse folds a constant and a folded
// call does not prove what a real query does.
const nestedAggregateSchema = `
CREATE TABLE probe (
    k UInt8,
    x Int32,
    y Float64,
    s String,
    arr_i32 Array(Int32)
) ENGINE = MergeTree ORDER BY k
`

// parseNestedAggregateExpr parses one select expression and gives back
// its AST node, so a test can drive findNestedAggregate directly.
func parseNestedAggregateExpr(t *testing.T, exprSQL string) clickhouse.Expr {
	t.Helper()
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM probe").ParseStmts()
	if err != nil {
		t.Fatalf("parse %q: %v", exprSQL, err)
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		t.Fatalf("parse %q: not a SELECT", exprSQL)
	}
	return selectQuery.SelectItems[0].Expr
}

func nestedAggregateTestSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, nestedAggregateSchema)
	if err != nil {
		t.Fatalf("parse probe schema: %v", err)
	}
	return schema
}

// TestAggregateRefusesAnAggregateArgument is the defect of the regression.
// ClickHouse ALWAYS answers Code: 184 (ILLEGAL_AGGREGATION) when an
// aggregate function appears in the argument subtree of another
// aggregate function. chgen used to give such a call a type, so the
// generated query failed at run time and not at generation time.
//
// Measured on ClickHouse 25.8.29.51 with DESCRIBE over the columns of a
// real table. DESCRIBE and not toTypeName, because toTypeName reports
// the type the server WOULD assign and says nothing about whether the
// query runs.
//
//	DESCRIBE (SELECT any(any(x)) FROM t184)
//	-- Code: 184. Aggregate function any(x) is found inside another
//	-- aggregate function in query. (ILLEGAL_AGGREGATION)
//
// This test is the NARROW half. Its partner
// TestAggregateAcceptsANonAggregateArgument is the WIDE half: a refusal
// wider than the server's breaks a query that runs and is just as much a
// defect as a missing refusal.
func TestAggregateRefusesAnAggregateArgument(t *testing.T) {
	schema := nestedAggregateTestSchema(t)
	refused := []string{
		// Direct nesting. The 108 cells of the axisaudit nesting
		// sweep all have this shape: outer any or anyLast, inner
		// one of 21 aggregates.
		"any(any(x))",
		"any(anyLast(x))",
		"anyLast(sum(x))",
		"any(avg(x))",
		"any(min(x))",
		"any(max(x))",
		"any(uniq(x))",
		"any(uniqExact(x))",
		"any(uniqCombined(x))",
		"any(uniqCombined64(x))",
		"any(countDistinct(x))",
		"any(groupArray(x))",
		"any(groupUniqArray(x))",
		"any(groupBitAnd(x))",
		"any(groupBitOr(x))",
		"any(groupBitXor(x))",
		"any(median(x))",
		// The registry does not carry these with class
		// wrapperAggregate. They are in knownAggregateNames, and each
		// was measured: "any(uniqHLL12(x))" and "any(uniqTheta(x))"
		// both answer Code 184.
		"any(uniqHLL12(x))",
		"any(uniqTheta(x))",
		"any(stddevPop(x))",
		"any(corr(x, y))",
		"any(sumKahan(x))",
		"any(deltaSum(x))",
		"any(avgWeighted(x, y))",
		// Through a scalar function. The server looks at the whole
		// subtree, not only at the direct argument.
		"any(abs(sum(x)))",
		"any(toString(sum(x)))",
		// Through arithmetic INSIDE the aggregate. Note the
		// contrast with "sum(x) + sum(y)", which runs.
		"any(x + sum(x))",
		// Through a branch.
		"any(if(x > 1, sum(x), 0))",
		// Through a lambda body of a higher-order function.
		"any(arrayMap(v -> v + 1, groupArray(x)))",
		// Combinators. -If, -State and -Merge are all still
		// aggregates on both sides of the nesting.
		"any(sumIf(x, x > 1))",
		"any(sumState(x))",
		"sumMerge(sumState(x))",
		"sumIf(sum(x), x > 1)",
		// Parametric aggregates, on the inside and on the outside.
		// The data arguments of a parametric call live in the
		// ColumnArgList and not in the parameter list, thus the
		// walk must read both.
		"any(quantile(0.5)(x))",
		"quantile(0.5)(sum(x))",
	}
	for _, exprSQL := range refused {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err == nil {
			t.Errorf("%s: chgen answered %s, but the server refuses the call with Code: 184 (ILLEGAL_AGGREGATION)", exprSQL, inferred)
			continue
		}
		if !strings.Contains(err.Error(), "184") {
			t.Errorf("%s: chgen refused with %q, which does not name the server error Code: 184", exprSQL, err)
		}
	}
}

// TestAggregateAcceptsANonAggregateArgument is the WIDE half of the
// Code 184 rule. Every expression here RUNS on ClickHouse 25.8.29.51,
// thus a refusal would break a working query.
//
// The two exits of the rule are here and both were measured:
//   - a window function opens a new frame, so "sum(sum(x)) OVER ()" is
//     legal and gives Int64;
//   - a subquery opens a new scope, so "any((SELECT sum(x) FROM t184))"
//     is legal and gives Nullable(Int64).
//
// The six scalars that carry the class wrapperAggregate are here too:
// arraySlice, arraySort, arrayDistinct, arrayResize, greatest and least.
// A name test built on that class alone would refuse them, but each one
// RUNS: "SELECT any(greatest(x, toInt32(y))), any(least(x, 1)) FROM t184"
// gives 1 and 1, and "SELECT any(arraySlice(arr, 1, 2)) FROM t3" gives
// [1,2].
func TestAggregateAcceptsANonAggregateArgument(t *testing.T) {
	schema := nestedAggregateTestSchema(t)
	accepted := map[string]string{
		// Arithmetic OVER aggregates. Legal: the aggregates are
		// siblings, not nested.
		"sum(x) + sum(x)":  "Int64",
		"sum(x) / count()": "Float64",
		// A scalar function OVER an aggregate. Legal.
		"abs(sum(x))":      "UInt64",
		"toString(sum(x))": "String",
		// greatest and least INSIDE an aggregate. Legal, they are
		// not aggregates.
		"any(greatest(x, toInt32(y)))": "Int32",
		"any(least(x, 1))":             "Int32",
		// A plain aggregate over a plain column stays untouched.
		"any(x)":        "Int32",
		"sum(x)":        "Int64",
		"sumIf(x, k)":   "Int64",
		"groupArray(x)": "Array(Int32)",
		// A higher-order call with no aggregate in the lambda body.
		"any(arrayMap(v -> v + 1, arr_i32))": "Array(Int64)",
		// The array functions that carry the class wrapperAggregate.
		// All six RUN inside an aggregate and OVER an aggregate.
		"any(arraySlice(arr_i32, 1, 2))":  "Array(Int32)",
		"any(arraySort(arr_i32))":         "Array(Int32)",
		"any(arrayDistinct(arr_i32))":     "Array(Int32)",
		"any(arrayResize(arr_i32, 2))":    "Array(Int32)",
		"arraySort(groupUniqArray(x))":    "Array(Int32)",
		"arraySlice(groupArray(x), 1, 2)": "Array(Int32)",
	}
	for exprSQL, want := range accepted {
		inferred, err := inferTestExprType(t, schema, exprSQL)
		if err != nil {
			t.Errorf("%s: chgen refused with %v, but the server runs the call", exprSQL, err)
			continue
		}
		if inferred != want {
			t.Errorf("%s: chgen answered %s, want %s", exprSQL, inferred, want)
		}
	}
}

// TestNestedAggregateWalkStopsAtANewScope pins the two exits of the walk
// on their own, because they are the shapes that a careless widening
// would break first. Both were measured and both RUN:
//
//	DESCRIBE (SELECT sum(sum(x)) OVER () FROM t184)      -> Int64
//	DESCRIBE (SELECT any((SELECT sum(x) FROM t184)) ...) -> Nullable(Int64)
func TestNestedAggregateWalkStopsAtANewScope(t *testing.T) {
	// A window function below an aggregate argument: the walk must
	// give back nothing, although a sum sits inside it.
	windowed := parseNestedAggregateExpr(t, "sum(x) OVER ()")
	if inner, found, err := findNestedAggregate(windowed); err != nil {
		t.Fatalf("walk window function: %v", err)
	} else if found {
		t.Errorf("the walk found %s under a window function; the server runs sum(sum(x)) OVER ()", inner)
	}
	// A subquery below an aggregate argument: same rule.
	subquery := parseNestedAggregateExpr(t, "(SELECT sum(x) FROM probe)")
	if inner, found, err := findNestedAggregate(subquery); err != nil {
		t.Fatalf("walk subquery: %v", err)
	} else if found {
		t.Errorf("the walk found %s under a subquery; the server runs any((SELECT sum(x) FROM t))", inner)
	}
	// The control. Without a new scope the same sum MUST be found,
	// otherwise this test cannot tell "no difference" from "never
	// ran".
	plain := parseNestedAggregateExpr(t, "abs(sum(x))")
	if _, found, err := findNestedAggregate(plain); err != nil {
		t.Fatalf("walk control: %v", err)
	} else if !found {
		t.Error("the walk found no aggregate under abs(sum(x)); the control proves the walk runs at all")
	}
	// A scope exit must prune one branch and continue with its siblings.
	// The parser Walk function propagates false to all remaining branches,
	// so these cases guard the local prune rule.
	for _, exprSQL := range []string{
		"tuple(sum(x) OVER (), sum(y))",
		"tuple((SELECT sum(x) FROM probe), sum(y))",
	} {
		expression := parseNestedAggregateExpr(t, exprSQL)
		if _, found, err := findNestedAggregate(expression); err != nil {
			t.Errorf("%s: walk: %v", exprSQL, err)
		} else if !found {
			t.Errorf("%s: the scope exit stopped the walk before its sibling", exprSQL)
		}
	}
}

// TestNestedAggregateWalkReadsExpressionContainers is the mutation guard
// for the AST walk. Each case puts an aggregate below a different parser
// container. A walk that skips one container gives a false safe result.
func TestNestedAggregateWalkReadsExpressionContainers(t *testing.T) {
	tests := []string{
		"abs(sum(x))",
		"x + sum(x)",
		"-sum(x)",
		"NOT sum(x)",
		"sum(x) IS NULL",
		"sum(x) IS NOT NULL",
		"CAST(sum(x) AS Int64)",
		"[x, sum(x)]",
		"[x, sum(x)][1]",
		"sum(x) BETWEEN 0 AND 1",
		"CASE x WHEN 1 THEN sum(x) ELSE 0 END",
		"if(x > 0, sum(x), 0)",
		"quantile(0.5)(sum(x))",
	}
	for _, exprSQL := range tests {
		expression := parseNestedAggregateExpr(t, exprSQL)
		inner, found, err := findNestedAggregate(expression)
		if err != nil {
			t.Errorf("%s: walk: %v", exprSQL, err)
			continue
		}
		if !found {
			t.Errorf("%s: the walk did not find the aggregate", exprSQL)
			continue
		}
		if !strings.Contains(strings.ToLower(inner), "sum") {
			t.Errorf("%s: the walk found %q, want sum", exprSQL, inner)
		}
	}
}

type nestedAggregateTransparentExpr struct {
	Child clickhouse.Expr
}

func (*nestedAggregateTransparentExpr) Pos() clickhouse.Pos                { return 0 }
func (*nestedAggregateTransparentExpr) End() clickhouse.Pos                { return 0 }
func (*nestedAggregateTransparentExpr) FormatSQL(*clickhouse.Formatter)    {}
func (*nestedAggregateTransparentExpr) Accept(clickhouse.ASTVisitor) error { return nil }

type nestedAggregateOpaqueExpr struct {
	child clickhouse.Expr
}

func (*nestedAggregateOpaqueExpr) Pos() clickhouse.Pos                { return 0 }
func (*nestedAggregateOpaqueExpr) End() clickhouse.Pos                { return 0 }
func (*nestedAggregateOpaqueExpr) FormatSQL(*clickhouse.Formatter)    {}
func (*nestedAggregateOpaqueExpr) Accept(clickhouse.ASTVisitor) error { return nil }

// TestNestedAggregateWalkHandlesNewNodeForms proves both fail-closed
// outcomes for a new parser node. An exported expression field is read
// without a roster update. An inaccessible expression field gives an
// explicit error and cannot give a false safe result.
func TestNestedAggregateWalkHandlesNewNodeForms(t *testing.T) {
	aggregate := parseNestedAggregateExpr(t, "sum(x)")
	transparent := &nestedAggregateTransparentExpr{Child: aggregate}
	if inner, found, err := findNestedAggregate(transparent); err != nil {
		t.Fatalf("walk transparent node: %v", err)
	} else if !found {
		t.Error("the walk did not find an aggregate in a new transparent node")
	} else if !strings.Contains(strings.ToLower(inner), "sum") {
		t.Errorf("the walk found %q, want sum", inner)
	}

	opaque := &nestedAggregateOpaqueExpr{child: aggregate}
	if _, found, err := findNestedAggregate(opaque); err == nil {
		t.Errorf("opaque node: found=%v, want an explicit traversal error", found)
	} else if !strings.Contains(err.Error(), "can hide an expression") {
		t.Errorf("opaque node: error %q does not explain the unsafe field", err)
	}
}

// TestIsAggregateCallNameBoundary pins the name test on its own. It is
// the half of the rule that decides whether a call is judged at all.
func TestIsAggregateCallNameBoundary(t *testing.T) {
	aggregates := []string{
		"any", "anylast", "sum", "avg", "min", "max", "count",
		"uniq", "uniqexact", "uniqhll12", "uniqtheta", "topk",
		"quantile", "quantilestate", "median", "grouparray",
		"groupuniqarray", "groupbitand", "groupbitor", "groupbitxor",
		"countdistinct", "uniqcombined", "uniqcombined64",
		"sumif", "sumstate", "summerge", "sumornull", "sumordefault",
		"stddevpop", "corr", "sumkahan", "deltasum", "avgweighted",
	}
	for _, name := range aggregates {
		if !isAggregateCallName(name) {
			t.Errorf("%s: not reported as an aggregate, but the server answers Code: 184 for any(%s(...))", name, name)
		}
	}
	// These six carry the class wrapperAggregate, yet each one RUNS
	// inside an aggregate (measured: any(arraySlice(arr, 1, 2)) is
	// Array(Int32) and gives [1,2]). They must NOT be reported, or the
	// refusal is wider than the server and breaks a working query.
	scalars := []string{
		"arraydistinct", "arrayresize", "arrayslice", "arraysort",
		"greatest", "least",
		"abs", "tostring", "arraymap", "if", "plus", "length",
		"toint8", "tuple", "coalesce",
	}
	for _, name := range scalars {
		if isAggregateCallName(name) {
			t.Errorf("%s: reported as an aggregate, but the server runs it inside an aggregate", name)
		}
	}
}
