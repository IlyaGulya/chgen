//go:build fuzzoracle

package engine

// Differential type-inference fuzzer. It compares the type that chgen infers
// for an expression with the type that a real ClickHouse server reports
// through toTypeName. The file is behind the fuzzoracle build tag, so a
// normal `go test ./...` does not need Docker.
//
// Run:
//
//	docker run -d --rm --name chgen-fuzzoracle -p 18123:8123 \
//	    clickhouse/clickhouse-server:25.8
//	CHGEN_ORACLE_URL=http://localhost:18123 \
//	    go test -tags fuzzoracle -run TestTypeOracle -v ./internal/engine
//
// Each run makes its own ClickHouse database and drops it at the end, thus
// two runs against ONE server do not destroy the fixture of each other.
//
// Optional environment keys:
//
//	CHGEN_ORACLE_DATABASE name of the run database (default: a unique name
//	                      from the process id and a nanosecond stamp). Give
//	                      a name only to inspect the fixture after the run;
//	                      the run refuses a name that is already present.
//	CHGEN_ORACLE_SEED     random seed (default 1); the run is reproducible
//	CHGEN_ORACLE_N        number of generated expressions (default 5000)
//	CHGEN_ORACLE_OUT      path for the machine-readable JSON result
//	CHGEN_ORACLE_PLAN     sampling profile; the only accepted value is
//	                      current-combined-v1, which is also the default
//	CHGEN_ORACLE_FIXTURE_ID
//	                      fixture identity; current-fixture is the default,
//	                      and cross-version-common-v1 is for the explicit
//	                      version-boundary comparison
//
// Result classes:
//
//	MISMATCH     chgen inferred a type, ClickHouse reports a different type.
//	             Every entry is a REAL wrong type, thus a non-zero count is
//	             a hard signal. There is no accepted equivalence any more:
//	             the earlier Bool-against-UInt8 rewrite was unmeasured and
//	             hid a real defect (chgen invented Bool for the whole
//	             predicate family, where ClickHouse always answers UInt8).
//	             The predicate class in inference now answers UInt8, so the
//	             two sides agree as text there and no rewrite is needed.
//	CHGEN_ERROR  chgen refused (parse or inference error), ClickHouse answered
//	CH_ERROR     ClickHouse rejected the expression (generator waste)
//	OK           both sides parse to the same canonical type tree
//
//	CH_ERROR_43_CHGEN_TYPED
//	             the blindness class, and a SUBSET of CH_ERROR, not a
//	             fourth exclusive class: chgen gave a type while the
//	             server refused with ILLEGAL_TYPE_OF_ARGUMENT (Code: 43).
//	             Such an expression raises counts["CH_ERROR"] and
//	             counts["CH_ERROR_43_CHGEN_TYPED"] together, and it makes
//	             an uncapped findings entry of this class that carries
//	             chgen_type, the wrong type that chgen gave. The plain
//	             CH_ERROR entries have no chgen_type, because there chgen
//	             refused too, thus the field separates a silently wrong
//	             answer from a correct refusal.
//
// What may be compared between two runs:
//
//	YES  counts, mismatch_signatures, outer_kind_chains,
//	     blind_code43_by_kind, ch_error_codes, ch_error_by_kind,
//	     ch_error_digest, and the MISMATCH, CHGEN_ERROR and
//	     CH_ERROR_43_CHGEN_TYPED entries of findings. The last class is
//	     never capped, thus it is complete and it can carry a rate.
//	NO   the CH_ERROR entries of findings. The list stops at
//	     findings_ch_error_cap while the count continues, thus a rate or a
//	     diff taken from the list is wrong as soon as the count passes the
//	     cap. Use ch_error_digest to compare the full CH_ERROR set and
//	     ch_error_codes to compare its shape.
//
// ONLY between two runs of ONE server run. The report carries server_run, the
// boot moment of the instance. The server-side CH_ERROR class moves with the
// age of the instance on one and the same commit, thus a difference between
// two reports with a different server_run says nothing about the code. Do not
// compare such reports by hand; use the comparator, which refuses them:
//
//	go run ./internal/tooling/cmd/oraclediff old.json new.json
//
// See package internal/oraclereport.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
	"github.com/IlyaGulya/chgen/internal/conformance"
	"github.com/IlyaGulya/chgen/internal/oraclereport"
)

// fexpr is one generated expression with its derivation tree, kept for
// shrinking a mismatch to a minimal reproduction.
type fexpr struct {
	sql      string
	kind     string
	agg      bool
	children []*fexpr
}

type oracleGen struct {
	r *rand.Rand
	// drawIndex is the candidate index of the run. It is built from the
	// columns of the schema that the RUN created, thus a lane can never
	// name a column that the live table does not hold.
	drawIndex map[string]genDrawCandidate
	// drawScalar, drawAggregate and drawWindow hold the sorted names of
	// the writable candidates of each placement. The order is sorted and
	// not map order, because a Go map walk is randomised and the
	// generator must stay reproducible from the seed alone.
	drawScalar    []string
	drawAggregate []string
	drawWindow    []string
	// drawIllegalScalar and drawIllegalAggregate hold the names that can
	// take an argument from the ILLEGAL half of their own domain.
	drawIllegalScalar    []string
	drawIllegalAggregate []string
	// laneDraws counts, for EVERY top-level lane declared in
	// topLevelLanes (registry, coverage, window, tuple, fallbackOps,
	// num, boolean, str, temporal, arr, aggregate) and for every v3
	// sub-lane inside registryDraw (registry-scalar, registry-aggregate,
	// registry-window, combinator), how many expressions the lane
	// actually PRODUCED from its own pool versus how many draws fell
	// through to a FALLBACK generator instead. The fallback count is
	// kept apart from the produced count under a "-fallback" suffix key,
	// because a lane whose pool is empty or whose renderCall always
	// fails looks, from the outside, exactly like a lane with a healthy
	// pool that simply drew rarely — the same blind spot
	// outer_kind_chains has, recorded for a different reason (see the
	// related note). A top-level lane that never falls
	// back (num, boolean, str, temporal, arr, aggregate, coverage,
	// window, tuple, fallbackOps: none of these has a pool that can run
	// dry) is only ever recorded as produced. Allocated on the FIRST
	// call to topLevel, so it is populated on both a v2 run and a v3
	// run; it is no longer v3-only.
	laneDraws map[string]int
}

// countLaneDraw records one draw of a lane, either a produced
// expression or a fallback. The key is the lane's own name; a fallback
// draw is recorded under name+"-fallback" so the two never collide and a
// reader can compare them side by side.
func (g *oracleGen) countLaneDraw(name string, fellBack bool) {
	if g.laneDraws == nil {
		g.laneDraws = make(map[string]int)
	}
	if fellBack {
		name += "-fallback"
	}
	g.laneDraws[name]++
}

func lit(kind, sql string) *fexpr { return &fexpr{sql: sql, kind: kind} }

func node(kind, sql string, agg bool, children ...*fexpr) *fexpr {
	return &fexpr{sql: sql, kind: kind, agg: agg, children: children}
}

var (
	numCols  = []string{"i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "f32", "f64", "dec", "ni32", "nf64"}
	intLits  = []string{"1", "-1", "255", "256", "300", "-129", "70000", "4000000000", "10000000000", "9223372036854775807", "18446744073709551615"}
	fltLits  = []string{"1.5", "-2.5", "0.5", "1e10", "1.7976931348623157e308"}
	strCols  = []string{"s", "fs", "lc", "lcn", "ns"}
	strLits  = []string{"'abc'", "'%a%'", "''"}
	dateCols = []string{"d"}
	dtCols   = []string{"dt", "dt64"}
	arrCols  = []string{"arr_i", "arr_s"}
	castTys  = []string{"Int8", "Int32", "Int64", "UInt8", "UInt16", "UInt64", "Float32", "Float64", "String", "Nullable(Int64)", "LowCardinality(String)", "Decimal(10, 2)"}
)

func (g *oracleGen) num(depth int) *fexpr {
	if depth > 0 && g.r.Intn(5) == 0 {
		return g.curatedNumeric(depth)
	}
	if depth <= 0 || g.r.Intn(4) == 0 {
		if g.r.Intn(3) == 0 {
			if g.r.Intn(3) == 0 {
				return lit("float-literal", pick(g.r, fltLits))
			}
			return lit("int-literal", pick(g.r, intLits))
		}
		return lit("num-column", pick(g.r, numCols))
	}
	switch g.r.Intn(12) {
	case 0, 1, 2:
		a, b := g.num(depth-1), g.num(depth-1)
		op := pick(g.r, arithTokens())
		return node("arith:"+op, fmt.Sprintf("(%s %s %s)", a.sql, op, b.sql), false, a, b)
	case 3:
		a := g.num(depth - 1)
		return node("unary-minus", fmt.Sprintf("-(%s)", a.sql), false, a)
	case 4:
		c, a, b := g.boolean(depth-1), g.num(depth-1), g.num(depth-1)
		return node("if", fmt.Sprintf("if(%s, %s, %s)", c.sql, a.sql, b.sql), false, c, a, b)
	case 5:
		c1, a, c2, b, e := g.boolean(depth-1), g.num(depth-1), g.boolean(depth-1), g.num(depth-1), g.num(depth-1)
		return node("multiIf", fmt.Sprintf("multiIf(%s, %s, %s, %s, %s)", c1.sql, a.sql, c2.sql, b.sql, e.sql), false, c1, a, c2, b, e)
	case 6:
		a, b := g.num(depth-1), g.num(depth-1)
		return node("nullIf", fmt.Sprintf("nullIf(%s, %s)", a.sql, b.sql), false, a, b)
	case 7:
		a, b := g.num(depth-1), g.num(depth-1)
		fn := pick(g.r, []string{"ifNull", "coalesce"})
		return node(fn, fmt.Sprintf("%s(nullIf(%s, %s), %s)", fn, a.sql, b.sql, b.sql), false, a, b)
	case 8:
		a := g.num(depth - 1)
		return node("cast", fmt.Sprintf("CAST(%s AS %s)", a.sql, pick(g.r, castTys)), false, a)
	case 9:
		a := g.num(depth - 1)
		fn := pick(g.r, conversionFunctionNames())
		return node(fn, fmt.Sprintf("%s(%s)", fn, a.sql), false, a)
	case 10:
		s := g.str(depth - 1)
		return node("length", fmt.Sprintf("length(%s)", s.sql), false, s)
	case 11:
		if g.r.Intn(2) == 0 {
			return node("map-access", "m['k']", false)
		}
		return node("array-access", "arr_i[1]", false)
	}
	panic("unreachable")
}

func (g *oracleGen) str(depth int) *fexpr {
	if depth > 0 && g.r.Intn(5) == 0 {
		return g.curatedString(depth)
	}
	if depth <= 0 || g.r.Intn(3) == 0 {
		if g.r.Intn(4) == 0 {
			return lit("str-literal", pick(g.r, strLits))
		}
		return lit("str-column", pick(g.r, strCols))
	}
	switch g.r.Intn(5) {
	case 0:
		a, b := g.str(depth-1), g.str(depth-1)
		return node("concat-op", fmt.Sprintf("(%s || %s)", a.sql, b.sql), false, a, b)
	case 1:
		a, b := g.str(depth-1), g.str(depth-1)
		return node("concat-fn", fmt.Sprintf("concat(%s, %s)", a.sql, b.sql), false, a, b)
	case 2:
		a := g.str(depth - 1)
		fn := pick(g.r, []string{"lower", "upper", "trim"})
		return node(fn, fmt.Sprintf("%s(%s)", fn, a.sql), false, a)
	case 3:
		a := g.num(depth - 1)
		return node("toString", fmt.Sprintf("toString(%s)", a.sql), false, a)
	case 4:
		a := g.str(depth - 1)
		return node("substring", fmt.Sprintf("substring(%s, 1, 2)", a.sql), false, a)
	}
	panic("unreachable")
}

func (g *oracleGen) boolean(depth int) *fexpr {
	if depth > 0 && g.r.Intn(5) == 0 {
		return g.curatedBoolean(depth)
	}
	if depth <= 0 || g.r.Intn(4) == 0 {
		return lit("bool-leaf", pick(g.r, []string{"b", "true", "false"}))
	}
	switch g.r.Intn(7) {
	case 0:
		a, b := g.num(depth-1), g.num(depth-1)
		op := pick(g.r, comparisonTokens())
		return node("cmp:"+op, fmt.Sprintf("(%s %s %s)", a.sql, op, b.sql), false, a, b)
	case 1:
		a, b := g.str(depth-1), g.str(depth-1)
		op := pick(g.r, comparisonTokens())
		return node("cmp-str:"+op, fmt.Sprintf("(%s %s %s)", a.sql, op, b.sql), false, a, b)
	case 2:
		a, b := g.boolean(depth-1), g.boolean(depth-1)
		op := pick(g.r, []string{"AND", "OR"})
		return node("logic:"+op, fmt.Sprintf("(%s %s %s)", a.sql, op, b.sql), false, a, b)
	case 3:
		a := g.boolean(depth - 1)
		return node("not", fmt.Sprintf("(NOT %s)", a.sql), false, a)
	case 4:
		a := g.str(depth - 1)
		op := pick(g.r, []string{"LIKE", "ILIKE", "NOT LIKE"})
		return node("like:"+op, fmt.Sprintf("(%s %s '%%a%%')", a.sql, op), false, a)
	case 5:
		return node("has", "has(arr_i, 1)", false)
	case 6:
		a := g.str(depth - 1)
		return node("empty", fmt.Sprintf("empty(%s)", a.sql), false, a)
	}
	panic("unreachable")
}

func (g *oracleGen) temporal(depth int) *fexpr {
	if depth > 0 && g.r.Intn(4) == 0 {
		return g.curatedTemporal(depth)
	}
	if depth <= 0 || g.r.Intn(3) == 0 {
		return lit("temporal-column", pick(g.r, append(append([]string{}, dateCols...), dtCols...)))
	}
	switch g.r.Intn(5) {
	case 0:
		return node("now", pick(g.r, []string{"now()", "today()", "now64()"}), false)
	case 1:
		return node("toDate", fmt.Sprintf("toDate(%s)", pick(g.r, dtCols)), false)
	case 2:
		return node("toDateTime", fmt.Sprintf("toDateTime(%s)", pick(g.r, dateCols)), false)
	case 3:
		a := g.temporal(depth - 1)
		return node("date-plus-int", fmt.Sprintf("(%s + 1)", a.sql), false, a)
	case 4:
		a := g.temporal(depth - 1)
		return node("toStartOfDay", fmt.Sprintf("toStartOfDay(%s)", a.sql), false, a)
	}
	panic("unreachable")
}

func (g *oracleGen) arr(depth int) *fexpr {
	if depth > 0 && g.r.Intn(3) == 0 {
		return g.curatedArray(depth)
	}
	if depth <= 0 || g.r.Intn(2) == 0 {
		if g.r.Intn(3) == 0 {
			return lit("arr-literal", pick(g.r, []string{"[1, 2, 3]", "['a', 'b']", "[1.5, 2.5]"}))
		}
		return lit("arr-column", pick(g.r, arrCols))
	}
	a := g.arr(depth - 1)
	fn := pick(g.r, []string{"arraySort", "arrayDistinct"})
	return node(fn, fmt.Sprintf("%s(%s)", fn, a.sql), false, a)
}

// aggregate builds one aggregate-lane expression. All column references stay
// inside aggregate calls, so the SELECT is valid with an implicit full-table
// group.
func (g *oracleGen) aggregate(depth int) *fexpr {
	if g.r.Intn(6) == 0 {
		return g.curatedAggregate(depth)
	}
	inner := func() *fexpr {
		switch g.r.Intn(4) {
		case 0:
			return g.num(depth)
		case 1:
			return g.str(depth)
		case 2:
			return g.temporal(depth)
		default:
			return g.num(depth)
		}
	}
	simple := func() *fexpr {
		switch g.r.Intn(10) {
		case 0:
			return node("count", "count()", true)
		case 1:
			a := g.num(depth)
			return node("sum", fmt.Sprintf("sum(%s)", a.sql), true, a)
		case 2:
			a := g.num(depth)
			return node("avg", fmt.Sprintf("avg(%s)", a.sql), true, a)
		case 3:
			a := inner()
			fn := pick(g.r, []string{"min", "max", "any"})
			return node(fn, fmt.Sprintf("%s(%s)", fn, a.sql), true, a)
		case 4:
			a := inner()
			return node("uniq", fmt.Sprintf("uniq(%s)", a.sql), true, a)
		case 5:
			a := inner()
			return node("groupArray", fmt.Sprintf("groupArray(%s)", a.sql), true, a)
		case 6:
			a, c := g.num(depth), g.boolean(depth)
			return node("sumIf", fmt.Sprintf("sumIf(%s, %s)", a.sql, c.sql), true, a, c)
		case 7:
			a, b := inner(), g.num(depth)
			return node("argMax", fmt.Sprintf("argMax(%s, %s)", a.sql, b.sql), true, a, b)
		case 8:
			a := g.num(depth)
			return node("quantile", fmt.Sprintf("quantile(0.5)(%s)", a.sql), true, a)
		default:
			a := g.num(depth)
			return node("median", fmt.Sprintf("median(%s)", a.sql), true, a)
		}
	}
	first := simple()
	if g.r.Intn(3) == 0 {
		second := simple()
		op := pick(g.r, arithTokens())
		return node("agg-arith:"+op, fmt.Sprintf("(%s %s %s)", first.sql, op, second.sql), true, first, second)
	}
	return first
}

// topLevelLane names one production that topLevel can pick, together
// with its weight and its eligibility test.
//
// eligible reports whether the lane can run at all under the current
// grammar. A lane that is not eligible must not consume randomness and
// must not change any other lane's share: topLevel filters the table to
// the eligible lanes FIRST, and only then draws one number over the sum
// of THEIR weights.
type topLevelLane struct {
	name   string
	weight int
	build  func(g *oracleGen, depth int) *fexpr
}

// topLevelLanes is the single declared weight table for topLevel. Every
// top-level production and its share of the draw is listed here, so a
// reader sees the whole distribution in one place instead of following
// a chain of independent `if` draws.
//
// The v3 weights are chosen to match, as closely as an integer table
// allows, the shares the OLD sequential-if cascade produced when
// current registry grammar was on. That cascade drew:
//
//	registry:     g.r.Intn(5) < 2                               -> 2/5
//	coverage:     (not registry) && g.r.Intn(6) == 0             -> 3/5 * 1/6   = 1/10
//	window/tuple: (not the above) && g.r.Intn(7) == 0, split 50/50 among window and tuple
//	                                                              -> 3/5 * 5/6 * 1/7 = 1/14, so 1/28 each
//	fallbackOps:  (not the above) && g.r.Intn(7) == 0             -> 3/5 * 5/6 * 6/7 * 1/7 = 3/49
//	switch(12):   whatever is left                                -> 3/5 * 5/6 * 6/7 * 6/7 = 18/49
//	  of which:   num 4/12, boolean 2/12, str 2/12, temporal 1/12, arr 1/12, aggregate 2/12
//	              of the 18/49 remainder.
//
// Converting every fraction to a common denominator of 980 (the LCM of
// the fractions above) gives an exact integer weight per lane with no
// rounding:
//
//	registry      392   (2/5)
//	coverage       98   (1/10)
//	window         35   (1/28)
//	tuple          35   (1/28)
//	fallbackOps    60   (3/49)
//	num           120   (6/49, i.e. 18/49 * 4/12)
//	boolean        60   (3/49, i.e. 18/49 * 2/12)
//	str            60   (3/49, i.e. 18/49 * 2/12)
//	temporal       30   (3/98, i.e. 18/49 * 1/12)
//	arr            30   (3/98, i.e. 18/49 * 1/12)
//	aggregate      60   (3/49, i.e. 18/49 * 2/12)
//	total         980
//
// This is a REFACTOR, not a retuning: the point is that the shares
// become a number a reader can see and change in one place, not that
// they move. When grammar is v2, the registry lane is not eligible and
// drops out of the sum, so the seven remaining v2/switch lanes share
// the same 588 (=980-392) total weight in the same relative
// proportions they always had among themselves.
//
// IMPORTANT: this table draws ONE random number over the sum of the
// ELIGIBLE lanes' weights, unlike the old cascade, which drew a
// separate random number per `if`. This means a v2 run under this
// table is NOT byte-identical to a v2 run under the old cascade or to
// any earlier recorded artifact, even though the grammar and the
// relative shares are unchanged. The header comment of this file and
// the old per-lane comments promised byte-identity across grammars;
// that promise is retired starting with this change. See the commit
// message for why the loss is accepted.
var topLevelLanes = []topLevelLane{
	{
		name:   "registry",
		weight: 392,
		build:  func(g *oracleGen, depth int) *fexpr { return g.registryDraw(depth) },
	},
	{
		// The shallow coverage lane. It emits the LowCardinality and
		// plain-Bool productions with a SMALL depth, directly at the
		// top level.
		//
		// The depth is the point of the lane. The same productions also
		// sit inside curatedNumeric and curatedBoolean, but there a deep parent
		// usually wraps them, and a wrapped LowCardinality result
		// either loses the wrapper or makes the whole expression
		// overflow into the CH_ERROR class, where no type is compared.
		// Measured: with the productions reachable only from the deep
		// lanes, 37 expressions held length(lc...) and NOT ONE of them
		// reported an arith mismatch. At the top level the same
		// production reports the class.
		name:   "coverage",
		weight: 98,
		build:  func(g *oracleGen, depth int) *fexpr { return g.curatedCoverage(1) },
	},
	{
		name:   "window",
		weight: 35,
		build:  func(g *oracleGen, depth int) *fexpr { return g.curatedWindow(depth) },
	},
	{
		name:   "tuple",
		weight: 35,
		build:  func(g *oracleGen, depth int) *fexpr { return g.curatedTuple(depth) },
	},
	{
		// The fallback-operator lane. It sits at the TOP level and not
		// inside a deep production, for the same reason as the shallow
		// coverage lane above: a wrapped result loses the class, and
		// these four tokens have no other way into the grammar at all.
		name:   "fallbackOps",
		weight: 60,
		build:  func(g *oracleGen, depth int) *fexpr { return g.curatedFallbackOperators() },
	},
	{
		name:   "num",
		weight: 120,
		build:  func(g *oracleGen, depth int) *fexpr { return g.num(depth) },
	},
	{
		name:   "boolean",
		weight: 60,
		build:  func(g *oracleGen, depth int) *fexpr { return g.boolean(depth) },
	},
	{
		name:   "str",
		weight: 60,
		build:  func(g *oracleGen, depth int) *fexpr { return g.str(depth) },
	},
	{
		name:   "temporal",
		weight: 30,
		build:  func(g *oracleGen, depth int) *fexpr { return g.temporal(depth) },
	},
	{
		name:   "arr",
		weight: 30,
		build:  func(g *oracleGen, depth int) *fexpr { return g.arr(depth) },
	},
	{
		name:   "aggregate",
		weight: 60,
		build:  func(g *oracleGen, depth int) *fexpr { return g.aggregate(depth - 1) },
	},
}

// topLevel picks exactly one lane from topLevelLanes, over the sum of
// the weights of the lanes ELIGIBLE for the current grammar, and builds
// it. Every top-level production draw funnels through this ONE random
// choice, so a lane's share is visible in the table above and cannot be
// silently diluted by a lane declared after it, unlike the old
// sequential-if cascade this replaces.
func (g *oracleGen) topLevel(depth int) *fexpr {
	total := 0
	for _, lane := range topLevelLanes {
		total += lane.weight
	}
	pick := g.r.Intn(total)
	for _, lane := range topLevelLanes {
		if pick < lane.weight {
			g.countLaneDraw(lane.name, false)
			return lane.build(g, depth)
		}
		pick -= lane.weight
	}
	panic("unreachable: topLevelLanes weights did not cover the draw")
}

// --- current registry grammar: the registry lane ---
//
// The current registry grammar draws its call names from the candidate index and not from a
// list in this file. The lists that the earlier grammars held here, toFuncs,
// arith and cmps, are gone: the operator tokens come from operatorCatalog and
// the function names come from the registry, thus a new rule is fuzzed as
// soon as it carries a genSpec.
//
// The lane has two halves and BOTH read one domain.
//
// The legal half draws every argument from the columns that
// spec.domain.accepts accepted. The illegal half changes exactly ONE operand
// and draws it from the complement, that is the columns that the very same
// predicate rejected. There is no second list of bad arguments. A hand
// written reject list would drift away from the domain it claims to
// contradict, and it would re-create the duplication that this epic removes.
//
// The illegal share exists to feed CH_ERROR_43_CHGEN_TYPED, the blindness
// counter. That class counts an expression that the SERVER refuses with code
// 43 while chgen still answered a type. Such a cell can only appear when the
// generator writes a call that breaks the domain, thus without this share the
// counter can only ever read zero and the refusal side of every domain stays
// untested.
//
// The share is FIXED at one draw in six and not tuned per function. A share
// that followed the size of the illegal pool would put nearly all of the
// illegal calls on the few functions with the narrowest domain, and the wide
// domains would stay unexercised.
const registryIllegalShare = 6

// registryDraw emits one call to a candidate of the index.
func (g *oracleGen) registryDraw(depth int) *fexpr {
	// The placement decides which names are legal in the position. A
	// window call needs an OVER clause and an aggregate call needs an
	// aggregate context, thus the three sets cannot be mixed.
	switch g.r.Intn(8) {
	case 0:
		return g.registryWindowDraw()
	case 1, 2:
		return g.registryAggregateDraw(depth)
	case 3:
		return g.combinatorDraw()
	default:
		return g.registryScalarDraw(depth)
	}
}

// combinatorDraw emits one cell of the suffix-by-base product of
// inferAggregateCombinatorType.
//
// The lane exists because that dispatch composes ANY suffix over ANY
// base, and only two cells of the product had a production by hand:
// uniqMerge and sumSimpleState. The rest was unfuzzed, thus the regression, a
// combinator over a Nullable inner type, had to be found by hand.
//
// The suffix comes from aggregateCombinators, the inference table
// itself, so a new suffix is fuzzed as soon as inference knows it.
//
// The node carries agg=true, because every form here is an aggregate
// call and needs the aggregate context of the harness.
func (g *oracleGen) combinatorDraw() *fexpr {
	base := pick(g.r, combinatorBaseSpellings())
	combinator := pick(g.r, aggregateCombinators)
	argument := pick(g.r, combinatorArgumentColumns())
	form, ok := renderCombinatorForm(base, combinator.suffix, argument, "arr_i")
	if !ok {
		g.countLaneDraw("combinator", true)
		return g.aggregate(0)
	}
	g.countLaneDraw("combinator", false)
	return node(form.kind, form.sql, true)
}

// registryScalarDraw writes one scalar call from the index.
func (g *oracleGen) registryScalarDraw(depth int) *fexpr {
	illegal := g.r.Intn(registryIllegalShare) == 0
	names := g.drawScalar
	if illegal {
		names = g.drawIllegalScalar
	}
	if len(names) == 0 {
		g.countLaneDraw("registry-scalar", true)
		return g.num(depth)
	}
	return g.renderRegistryCall("registry-scalar", pick(g.r, names), illegal, false, depth)
}

// registryAggregateDraw writes one aggregate call from the index. The node
// carries agg=true, so the harness puts the expression in an aggregate
// context.
func (g *oracleGen) registryAggregateDraw(depth int) *fexpr {
	illegal := g.r.Intn(registryIllegalShare) == 0
	names := g.drawAggregate
	if illegal {
		names = g.drawIllegalAggregate
	}
	if len(names) == 0 {
		g.countLaneDraw("registry-aggregate", true)
		return g.aggregate(depth - 1)
	}
	return g.renderRegistryCall("registry-aggregate", pick(g.r, names), illegal, true, depth)
}

// registryWindowDraw writes one window call from the index. Every window
// call gets the same ORDER BY frame: the frame is not what this lane
// measures, and a varying frame would only spread the draws.
func (g *oracleGen) registryWindowDraw() *fexpr {
	if len(g.drawWindow) == 0 {
		g.countLaneDraw("registry-window", true)
		return lit("num-column", "i32")
	}
	name := pick(g.r, g.drawWindow)
	candidate := g.drawIndex[name]
	call, ok := candidate.renderCall(g.r, -1)
	if !ok {
		g.countLaneDraw("registry-window", true)
		return lit("num-column", "i32")
	}
	g.countLaneDraw("registry-window", false)
	return node("registry-window-"+candidate.spec.spelling,
		call+" OVER (ORDER BY i32)", true)
}

// renderRegistryCall writes the call and wraps it in a node.
//
// The kind names the spelling and says whether the call is the legal or
// the illegal form. The two forms must NOT share a kind: an illegal call
// is expected to be refused, thus one shared kind would let a real
// defect in the legal form read as the intended refusal of the illegal
// one.
func (g *oracleGen) renderRegistryCall(lane, name string, illegal, aggregate bool, depth int) *fexpr {
	candidate := g.drawIndex[name]
	position := -1
	if illegal {
		position = candidate.pickIllegalPosition(g.r)
	}
	call, ok := candidate.renderCall(g.r, position)
	if !ok {
		g.countLaneDraw(lane, true)
		if aggregate {
			return g.aggregate(depth - 1)
		}
		return g.num(depth)
	}
	g.countLaneDraw(lane, false)
	kind := "registry-fn-" + candidate.spec.spelling
	if illegal {
		kind = "registry-illegal-" + candidate.spec.spelling
	}
	return node(kind, call, aggregate)
}

// --- Curated productions ---
//
// These productions put measured boundary shapes directly in the current
// population. They cover CASE, INTERVAL arithmetic, lambdas, tuples, wide
// numeric types, window functions, combinators, and subqueries.

var (
	curatedWideCols  = []string{"i128", "u128", "i256", "u256", "d32", "d64s", "d128"}
	curatedIntervals = []string{"SECOND", "MINUTE", "HOUR", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR"}

	// The three lists below feed the coverage productions that bring the
	// LowCardinality and plain-Bool classes of earlier grammar into curated grammar.
	//
	// The cause of the loss was dilution, not a missing production. Both
	// grammars hold lc and lcn in strCols, thus the walk of curated grammar can
	// reach every one of these classes. But every v2 hook takes a share of
	// the draws away from the plain string and number lanes, so at this
	// sample size the LowCardinality operand and the pair of non-Nullable
	// numbers arrive in the compared position too seldom for the class to
	// appear. The productions put the operand in the position directly, so
	// the class no longer depends on the luck of the walk.
	curatedLCCols = []string{"lc", "lcn"}
	// curatedPlainNumCols holds the numeric columns that are NOT Nullable. A
	// comparison of two of them gives the non-Nullable Bool-against-UInt8
	// class; ni32 and nf64 would make the result Nullable.
	curatedPlainNumCols = []string{"i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "f32", "f64", "dec"}
	// curatedNullableNumCols holds the Nullable numeric columns. A logical AND
	// or OR over a comparison on one of them gives the Nullable(Bool)
	// class of the logic lane.
	curatedNullableNumCols = []string{"ni32", "nf64"}
)

// curatedCoverage emits one of the productions that bring the grammar-v1 classes
// into curated grammar. Both the deep lanes (curatedNumeric, curatedBoolean) and the shallow
// top-level lane call the same helpers, so the two lanes cannot drift apart.
// The shares are not uniform. They are set from the measured yield of each
// production at N=2000, so that every class it must cover appears at more than
// one seed. The logic and the arithmetic lanes get the larger shares, because
// their classes survive only in a narrow shape: the class is reported only
// while shrink() cannot walk into a mismatching child, and most of the
// generated instances lose that race. The comparison lanes yield about ten
// findings each per run and need no help.
func (g *oracleGen) curatedCoverage(depth int) *fexpr {
	// The draw is out of 11, not out of 10, so that the date-difference
	// class is ADDED to this lane instead of taking its share from
	// curatedNullableLogic. Every earlier arm keeps the same number of shares.
	switch g.r.Intn(11) {
	case 0, 1, 2:
		return g.curatedLCArithmetic()
	case 3, 4:
		return g.curatedLCStringComparison()
	case 5:
		return g.curatedPlainNumericComparison()
	case 10:
		return g.curatedDateDifference()
	default:
		return g.curatedNullableLogic(depth)
	}
}

// curatedDateDifference builds the difference of two date-like columns.
//
// The production lives in the coverage lane, not only in the temporal lane,
// for the reason that curatedLCArithmetic above records: reaching a class through the
// walk is a matter of luck. The temporal lane sits behind three nested
// probability gates, and a measured pre-fix run showed the cost of that: with
// the production in the temporal lane alone, seed 99 never generated the
// shape at ALL over 2000 expressions, so that seed could not have found the
// defect whatever the rules said. The coverage lane puts the operands in the
// position directly.
//
// The SAME-type pairs carry the information, because each one has a definite
// server answer, measured on 25.8.29.51 against real columns:
//
//	d    - d      Int32
//	dt   - dt     Int32
//	dt32 - dt32   code 43, ILLEGAL_TYPE_OF_ARGUMENT
//
// A MIXED pair is code 43 for every combination, thus all of them together
// say only one thing. The draw therefore takes a same-type pair two times out
// of three, and the mixed pairs keep a share so that the refusal side stays
// exercised too.
func (g *oracleGen) curatedDateDifference() *fexpr {
	cols := []string{"d", "dt", "dt32"}
	left := pick(g.r, cols)
	right := left
	if g.r.Intn(3) == 0 {
		right = pick(g.r, cols)
	}
	return node("curated-date-difference", fmt.Sprintf("(%s - %s)", left, right), false)
}

// curatedLCArithmetic builds arithmetic between an if() over a small negative literal
// and length() over a LowCardinality string column.
//
// Every part of the left operand is load-bearing. Measured on 25.8.29.51 with
// the resolver at this commit:
//
//   - The other operand must not be a plain column. length() over a
//     LowCardinality string keeps the wrapper, but a column on the other side
//     drops it: `i8 % length(lcn)` gives plain Nullable(Int64) on the server
//     as well, thus there is no class to compare.
//   - The literal must be NEGATIVE. length() is unsigned, so a positive
//     literal gives the UInt64 form: `255 % length(lcn)` gives
//     LowCardinality(Nullable(UInt64)), not the Int64 form under test.
//   - The literal must be SMALL. A value such as 18446744073709551615 makes
//     the expression overflow into the CH_ERROR class, where no type is
//     compared at all.
//   - The literal must be wrapped in a NON-CONSTANT if(). This is the
//     surprising part, and the measurement below shows it.
//
// The two measured rows for the last rule, where the left column gives the
// expression and the right column gives the answer:
//
//	-129                % length(lcn)  ->  chgen and the server AGREE
//	if(false, i16, -129) % length(lcn)  ->  chgen Nullable(Int64), but the
//	                                        server LowCardinality(Nullable(Int64))
//
// With a bare literal chgen keeps the LowCardinality wrapper and the
// expression is OK. The wrapper is lost only once the left operand is a
// non-constant expression, and that is the defect the class names. A
// production with a bare literal generates the shape and reports NOTHING,
// which reads exactly like coverage while proving none.
//
// The kind stays "arith:<op>", the kind that earlier grammar reports, because a
// class is the pair of the kind and the two types. A new kind name would make
// a new class and would not prove that the v1 class is covered.
func (g *oracleGen) curatedLCArithmetic() *fexpr {
	small := pick(g.r, []string{"-1", "-129", "-2"})
	a := node("if", fmt.Sprintf("if(%s, i16, %s)", pick(g.r, []string{"true", "false", "b"}), small), false)
	lc := node("length", fmt.Sprintf("length(%s)", pick(g.r, curatedLCCols)), false)
	op := pick(g.r, arithTokens())
	return node("arith:"+op, fmt.Sprintf("(%s %s %s)", a.sql, op, lc.sql), false, a, lc)
}

// curatedLCStringComparison compares a LowCardinality string against a string LITERAL.
//
// The literal side is load-bearing. Measured on 25.8.29.51:
//
//	'abc' != lc  -> LowCardinality(UInt8)
//	lc    != s   ->                UInt8
//
// A comparison of two columns drops the wrapper, thus a column on the other
// side would not produce the class under test at all. lc gives the
// non-Nullable form of the class and lcn the Nullable one. A string function
// or a concatenation over the column keeps the wrapper, so the production also
// reaches the deeper v1 shapes such as upper(lc) and (lcn || '%a%').
func (g *oracleGen) curatedLCStringComparison() *fexpr {
	a := lit("str-column", pick(g.r, curatedLCCols))
	switch g.r.Intn(3) {
	case 0:
		fn := pick(g.r, []string{"lower", "upper", "trim"})
		a = node(fn, fmt.Sprintf("%s(%s)", fn, a.sql), false, a)
	case 1:
		a = node("concat-op", fmt.Sprintf("(%s || %s)", a.sql, pick(g.r, strLits)), false, a)
	}
	b := lit("str-literal", pick(g.r, strLits))
	if g.r.Intn(2) == 0 {
		a, b = b, a
	}
	op := pick(g.r, comparisonTokens())
	return node("cmp-str:"+op, fmt.Sprintf("(%s %s %s)", a.sql, op, b.sql), false, a, b)
}

// curatedPlainNumericComparison compares two non-Nullable numeric columns. This is the
// cmp:<op> class with a plain Bool against a plain UInt8. ni32 and nf64 stay
// out of the list, because either of them would make the result Nullable.
func (g *oracleGen) curatedPlainNumericComparison() *fexpr {
	a := lit("num-column", pick(g.r, curatedPlainNumCols))
	b := lit("num-column", pick(g.r, curatedPlainNumCols))
	op := pick(g.r, comparisonTokens())
	return node("cmp:"+op, fmt.Sprintf("(%s %s %s)", a.sql, op, b.sql), false, a, b)
}

// curatedNullableLogic builds AND or OR whose Nullable half is a comparison of a
// Nullable numeric column against an integer literal, wrapped in a constant
// `false OR (...)`. This is the logic:<op> class with Nullable(Bool) against
// Nullable(UInt8).
//
// The `false OR` wrapper is the whole trick, and it is a property of the
// HARNESS, not of ClickHouse. shrink() reports the smallest child that still
// disagrees, so a mismatching child hides the parent. Measured on 25.8.29.51
// at this commit:
//
//	256 < ni32                -> MISMATCH: Nullable(Bool) vs Nullable(UInt8)
//	false OR (256 < ni32)     -> OK: chgen and the server agree
//
// The bare comparison disagrees, thus a logic node built directly over it is
// always shrunk down to cmp:<op> and the logic class is never named. Measured:
// two earlier revisions of this production generated the shape many times and
// left both logic classes at ZERO for exactly this reason. `false OR (...)`
// restores agreement on the child, so shrink stops at the logic node above it
// and the class is reported.
//
// The yield of the two logic classes is therefore a property of the reporter,
// not a measure of how often chgen is wrong under a logic node. Measured on
// curated grammar, seed 42, N=2000: the two logic signatures count 2 findings
// between them, while outer_kind_chains shows 23 mismatches whose path passes
// through a logic node. Read outer_kind_chains before you read a low count in
// this class as evidence that chgen is correct here.
//
// The two grammar-v1 shapes have this same form:
//
//	('' LIKE '%a%') AND (false OR (256 < ni32))
//	((NOT false) AND (nf64 > 256)) OR (trim(lcn) ILIKE '%a%')
//
// The other side of the operator is a shallow boolean, so the Nullable half
// decides the result type. It must not be a bare bool leaf: `b AND (false OR
// (256 < ni32))` reported nothing in the probe.
func (g *oracleGen) curatedNullableLogic(depth int) *fexpr {
	na := lit("num-column", pick(g.r, curatedNullableNumCols))
	nb := lit("int-literal", pick(g.r, []string{"1", "-1", "255", "256", "300"}))
	cmpOp := pick(g.r, comparisonTokens())
	a, b := na, nb
	if g.r.Intn(2) == 0 {
		a, b = b, a
	}
	// The guarded node carries NO children, so shrink cannot walk into the
	// bare comparison under it and re-file the finding as cmp:<op>.
	guarded := node("curated-nullable-guard",
		fmt.Sprintf("(false OR (%s %s %s))", a.sql, cmpOp, b.sql), false)
	var other *fexpr
	if depth > 0 {
		other = g.boolean(depth - 1)
	} else {
		other = node("like:LIKE", fmt.Sprintf("(%s LIKE '%%a%%')", pick(g.r, strCols)), false)
	}
	op := pick(g.r, []string{"AND", "OR"})
	if g.r.Intn(2) == 0 {
		return node("logic:"+op, fmt.Sprintf("(%s %s %s)", other.sql, op, guarded.sql), false, other, guarded)
	}
	return node("logic:"+op, fmt.Sprintf("(%s %s %s)", guarded.sql, op, other.sql), false, guarded, other)
}

// --- the operators of the historic binary-operator fallback ---
//
// Four operator tokens reached the historic fallback of
// inferBinaryOperationType: `==`, `REGEXP`, `::` and a bare `->`. The grammar
// made none of them. Measured at seed 42 with N=2000, the findings of both
// grammars held zero `==`, zero `REGEXP` and zero `::`, and every `->` was a
// lambda inside a higher-order array call, where the call consumes the lambda
// and the operator never reaches the operand inference. The oracle was
// therefore blind to the whole family, and four wrong answers lived in it
// while every run stayed green. See docs/binary-operator-fallback-survey.md.
//
// The productions below put each token in the compared position directly.
// They do not depend on the luck of the walk, for the same reason that
// curatedCoverage does not: a token that only a deep walk can reach arrives too
// seldom at this sample size for its class to appear.
//
// Each production is shaped from the measured survey, not guessed:
//
//   - curatedDoubleEquals must MIX the operand types. `==` took the LEFT operand type from
//     the fallback, thus `i32 == i32` answers Int32 while the server answers
//     UInt8; but `i32 = i32` also disagrees with the server under the
//     accepted Bool-against-UInt8 divergence, so that pair cannot tell the
//     defect from the known divergence. An Enum or a Float on the left gives
//     a left type that no rule of `=` could ever produce, thus the class is
//     unambiguous.
//   - curatedRegexp draws its left operand from BOTH halves of the measured table.
//     A legal operand (String, Enum, the LowCardinality and Nullable
//     wrappers) proves the result base. A refused operand (a number, a Date,
//     a DateTime) proves that chgen does not type an expression that the
//     server refuses with Code: 43. The second half cannot appear in the
//     MISMATCH class by construction, because the server gives no type to
//     compare; it appears in CH_ERROR_43_CHGEN_TYPED, which is exactly the
//     blindness counter, and that counter is uncapped.
//   - curatedCastOperator pairs a column with a type name that the survey measured. The
//     right operand is a TYPE, not a column, thus the question the cell asks
//     is whether chgen refuses cleanly or invents a type.
//   - curatedBareLambda emits a lambda OUTSIDE any higher-order call. The server
//     has no result type for it either, thus the compared cell is a refusal
//     on both sides; the answer it must prevent is chgen reporting the type
//     of the lambda PARAMETER as the type of the lambda.
var (
	// curatedEqEqPairs holds operand pairs whose LEFT type is not a plausible
	// answer for a comparison. Both `==` defects of the survey are here:
	// `e8 == s` (the fallback said Enum8) and `f64 == i32` (Float64).
	curatedEqEqPairs = [][2]string{
		{"e8", "s"}, {"e8", "e8"}, {"e16", "s"},
		{"f64", "i32"}, {"f32", "i64"}, {"dec", "dec"},
		{"d", "dt"}, {"dt64", "dt"}, {"uid", "uid"},
		{"ip4", "ip4"}, {"arr_i", "arr_i"}, {"tup", "tup"},
		{"lc", "'a'"}, {"lcn", "'a'"}, {"ns", "s"}, {"i128", "i128"},
	}
	// curatedRegexpLegal holds the left operand types that the server accepts
	// for match(). The base result is UInt8 and the operand wrappers move
	// into it.
	curatedRegexpLegal = []string{"s", "fs", "e8", "e16", "lc", "ns", "lcn"}
	// curatedRegexpRefused holds the left operand types that the server refuses
	// with Code: 43. `d REGEXP 'a'` is the survey defect: the fallback
	// answered Date for an expression that the server has no type for.
	curatedRegexpRefused = []string{"d", "dt", "i32", "u8", "f64", "dec", "b", "uid", "ip4", "arr_s"}
	// curatedCastOpPairs are the cast-operator cells of the survey.
	curatedCastOpPairs = [][2]string{
		{"i32", "String"}, {"e8", "String"}, {"lc", "String"},
		{"uid", "String"}, {"d", "DateTime"}, {"dec", "Float64"},
		{"f64", "Int32"}, {"arr_i", "Array(Int64)"}, {"s", "Int32"},
	}
)

// curatedFallbackOperators emits one expression that carries an operator of the historic
// fallback. The four shares are equal, so a defect in any one token cannot
// hide behind the others.
func (g *oracleGen) curatedFallbackOperators() *fexpr {
	switch g.r.Intn(4) {
	case 0:
		return g.curatedDoubleEquals()
	case 1:
		return g.curatedRegexp()
	case 2:
		return g.curatedCastOperator()
	default:
		return g.curatedBareLambda()
	}
}

// curatedDoubleEquals builds `a == b`. The kind is its own, "curated-op-eqeq", and NOT the
// "cmp:=" kind of the `=` spelling. The two spellings took different paths
// through the resolver before the fix, thus one shared kind would let a
// defect of `==` read as the known Bool-against-UInt8 divergence of `=`.
func (g *oracleGen) curatedDoubleEquals() *fexpr {
	pair := pick(g.r, curatedEqEqPairs)
	a, b := pair[0], pair[1]
	if g.r.Intn(2) == 0 {
		a, b = b, a
	}
	return node("curated-op-eqeq", fmt.Sprintf("(%s == %s)", a, b), false)
}

// curatedRegexp builds `a REGEXP '<pattern>'`. Two of three draws take a legal
// operand and one takes a refused one: the legal half carries the comparable
// type, and the refused half only has to appear often enough for the
// blindness counter to see it.
func (g *oracleGen) curatedRegexp() *fexpr {
	pattern := pick(g.r, []string{"'a'", "'^a'", "'a.*'"})
	if g.r.Intn(3) == 0 {
		return node("curated-op-regexp-refused",
			fmt.Sprintf("(%s REGEXP %s)", pick(g.r, curatedRegexpRefused), pattern), false)
	}
	return node("curated-op-regexp",
		fmt.Sprintf("(%s REGEXP %s)", pick(g.r, curatedRegexpLegal), pattern), false)
}

// curatedCastOperator builds `a :: T`, the cast operator.
func (g *oracleGen) curatedCastOperator() *fexpr {
	pair := pick(g.r, curatedCastOpPairs)
	return node("curated-op-cast", fmt.Sprintf("(%s :: %s)", pair[0], pair[1]), false)
}

// curatedBareLambda builds a lambda that stands alone, outside any higher-order
// call. The server refuses it too, thus the cell proves only that chgen
// refuses it instead of answering with the type of the lambda parameter.
func (g *oracleGen) curatedBareLambda() *fexpr {
	body := pick(g.r, []string{"(x + 1)", "(x * 2)", "toString(x)", "(x > 1)"})
	if g.r.Intn(4) == 0 {
		return node("curated-op-lambda-bare", fmt.Sprintf("((x, y) -> %s)", body), false)
	}
	return node("curated-op-lambda-bare", fmt.Sprintf("(x -> %s)", body), false)
}

func (g *oracleGen) curatedNumeric(depth int) *fexpr {
	switch g.r.Intn(12) {
	case 10:
		// The Decimal256 lane. dec256 alone is Decimal256(4); every
		// arithmetic form of it is Decimal(76, 4).
		if g.r.Intn(2) == 0 {
			return lit("curated-decimal256-column", "dec256")
		}
		op := pick(g.r, arithTokens())
		return node("curated-decimal256-arith:"+op, fmt.Sprintf("(dec256 %s dec256)", op), false)
	case 11:
		// The aggregate-state lane.
		//
		// sagg is put bare and under arithmetic. A bare sagg keeps the
		// SimpleAggregateFunction wrapper; under arithmetic the server
		// drops it and answers the inner type.
		//
		// HISTORY. This production once made CHGEN_ERROR rise from 8
		// to about 22 to 25 per 2000 expressions, and every added
		// finding had this one cause: chgen had no arithmetic rule
		// for a SimpleAggregateFunction operand, while the server
		// answers a type. The refusal then propagated through
		// composition, so the same missing rule appeared under many
		// outer kinds. Measured on 25.8.29.51 against real columns:
		//
		//	sagg + 1     Int64      sagg / 2      Float64
		//	sagg * i64   Int64      sagg - sagg   Int64
		//	sagg32 + 1   Int64      saggf * 2     Float64
		//	nsagg + 1    Nullable(Int64)
		//
		// Every one of these equals the answer for the INNER type
		// alone, thus the rule is "unwrap SimpleAggregateFunction(f, T)
		// to T". That was a MISSING RULE, not a correct refusal.
		//
		// The rule is now in arithmetic.go as simpleAggregateInnerType,
		// and this family of findings is gone. The unwrap is LOCAL to
		// the binary arithmetic operators, because it is not universal
		// and a blanket rule would be a widening. Measured
		// counter-examples where the server KEEPS the wrapper:
		//
		//	if(1, sagg, i64)   SimpleAggregateFunction(sum, Int64)
		//	max(sagg)          SimpleAggregateFunction(sum, Int64)
		//
		// Those two are pinned by
		// TestSimpleAggregateKeepsWrapperOutsideArithmetic. The lane
		// stays, because it now proves the rule instead of the gap.
		//
		// agg is put under uniqMerge ONLY. A bare AggregateFunction
		// column has no Go mapping, and gotype.go refuses it on
		// purpose, thus generating it bare would only manufacture a
		// finding for a refusal that is already the intended answer.
		// The arithmetic arm keeps a SMALL share, one in six. It must
		// stay, because dropping it would hide the family again, which
		// is the very blindness this fixture removes. But it reports a
		// cause that is already known and it propagates, so a larger
		// share would fill the report with one finding wearing many
		// kinds and could mask a NEW defect. The bare column and the
		// merge cover the family's types on their own.
		switch g.r.Intn(6) {
		case 0:
			op := pick(g.r, arithTokens())
			return node("curated-simpleagg-arith:"+op, fmt.Sprintf("(sagg %s i64)", op), false)
		case 1, 2:
			return lit("curated-simpleagg-column", "sagg")
		default:
			return node("curated-aggregate-merge", "uniqMerge(agg)", true)
		}
	case 9:
		return g.curatedLCArithmetic()
	case 0:
		return lit("curated-wide-column", pick(g.r, curatedWideCols))
	case 1:
		a := g.num(depth - 1)
		fn := pick(g.r, []string{"toInt128", "toInt256", "toUInt128", "toUInt256"})
		return node("curated-"+fn, fmt.Sprintf("%s(%s)", fn, a.sql), false, a)
	case 2:
		a := g.num(depth - 1)
		fn := pick(g.r, []string{"toDecimal32", "toDecimal64", "toDecimal128"})
		return node("curated-"+fn, fmt.Sprintf("%s(%s, 2)", fn, a.sql), false, a)
	case 3:
		c, a, b := g.boolean(depth-1), g.num(depth-1), g.num(depth-1)
		return node("curated-case-searched", fmt.Sprintf("(CASE WHEN %s THEN %s ELSE %s END)", c.sql, a.sql, b.sql), false, c, a, b)
	case 4:
		c, a := g.boolean(depth-1), g.num(depth-1)
		return node("curated-case-no-else", fmt.Sprintf("(CASE WHEN %s THEN %s END)", c.sql, a.sql), false, c, a)
	case 5:
		x, a, b := g.num(depth-1), g.num(depth-1), g.num(depth-1)
		return node("curated-case-operand", fmt.Sprintf("(CASE %s WHEN 1 THEN %s ELSE %s END)", x.sql, a.sql, b.sql), false, x, a, b)
	case 6:
		fn := pick(g.r, []string{"arraySum", "arrayMin", "arrayMax", "arrayCount"})
		return node("curated-"+fn+"-lambda", fmt.Sprintf("%s(x -> (x + 1), arr_i)", fn), false)
	case 7:
		if g.r.Intn(2) == 0 {
			return node("curated-tuple-element", "tup.1", false)
		}
		return node("curated-tupleElement-fn", "tupleElement(tup, 1)", false)
	case 8:
		unit := pick(g.r, []string{"'day'", "'hour'", "'second'"})
		return node("curated-dateDiff", fmt.Sprintf("dateDiff(%s, d, dt)", unit), false)
	}
	panic("unreachable")
}

func (g *oracleGen) curatedString(depth int) *fexpr {
	switch g.r.Intn(6) {
	case 0:
		return lit("curated-enum-column", pick(g.r, []string{"e8", "e16"}))
	case 1:
		c, a, b := g.boolean(depth-1), g.str(depth-1), g.str(depth-1)
		return node("curated-case-searched-str", fmt.Sprintf("(CASE WHEN %s THEN %s ELSE %s END)", c.sql, a.sql, b.sql), false, c, a, b)
	case 2:
		c, a := g.boolean(depth-1), g.str(depth-1)
		return node("curated-case-no-else-str", fmt.Sprintf("(CASE WHEN %s THEN %s END)", c.sql, a.sql), false, c, a)
	case 3:
		col := pick(g.r, []string{"e8", "e16", "uid", "ip4", "ip6"})
		return node("curated-toString-special", fmt.Sprintf("toString(%s)", col), false)
	case 4:
		return node("curated-tuple-element-str", "tup.2", false)
	case 5:
		return node("curated-arrayStringConcat-lambda", "arrayStringConcat(arrayMap(x -> toString(x), arr_i), ',')", false)
	}
	panic("unreachable")
}

func (g *oracleGen) curatedBoolean(depth int) *fexpr {
	switch g.r.Intn(9) {
	case 6:
		return g.curatedLCStringComparison()
	case 7:
		return g.curatedPlainNumericComparison()
	case 8:
		return g.curatedNullableLogic(depth)
	case 0:
		op := pick(g.r, []string{"IN", "NOT IN"})
		col := pick(g.r, []string{"i32", "u8", "s"})
		return node("curated-in-subquery", fmt.Sprintf("(%s %s (SELECT %s FROM t))", col, op, col), false)
	case 1:
		return node("curated-in-list", "(i32 IN (1, 2, 3))", false)
	case 2:
		fn := pick(g.r, []string{"arrayExists", "arrayAll"})
		return node("curated-"+fn+"-lambda", fmt.Sprintf("%s(x -> (x > 1), arr_i)", fn), false)
	case 3:
		e := pick(g.r, []string{"e8", "e16"})
		v := "'a'"
		if e == "e16" {
			v = "'x'"
		}
		return node("curated-enum-cmp", fmt.Sprintf("(%s = %s)", e, v), false)
	case 4:
		col := pick(g.r, []string{"uid", "ip4", "ip6"})
		return node("curated-special-cmp", fmt.Sprintf("(%s = %s)", col, col), false)
	case 5:
		a := g.num(depth - 1)
		return node("curated-between", fmt.Sprintf("(%s BETWEEN 0 AND 100)", a.sql), false, a)
	}
	panic("unreachable")
}

func (g *oracleGen) curatedTemporal(depth int) *fexpr {
	switch g.r.Intn(7) {
	case 5:
		// The Date32 lane. Before this production no fixture held a
		// Date32 column, so the oracle could not reach the type at all.
		return lit("curated-date32-column", "dt32")
	case 6:
		// The difference of two date-like columns. This is the shape
		// that the earlier grammar never built: temporal() only ever
		// added an integer to a date, thus "<date> - <date>" was
		// unreachable and the wrong Date32 answer stayed invisible.
		//
		return g.curatedDateDifference()
	case 0:
		a := g.temporal(depth - 1)
		op := pick(g.r, []string{"+", "-"})
		unit := pick(g.r, curatedIntervals)
		n := g.r.Intn(3) + 1
		return node("curated-interval:"+op, fmt.Sprintf("(%s %s INTERVAL %d %s)", a.sql, op, n, unit), false, a)
	case 1:
		return lit("curated-tz-column", pick(g.r, []string{"dtz", "dtz64"}))
	case 2:
		col := pick(g.r, []string{"dt", "d", "dtz"})
		return node("curated-toDateTime-tz", fmt.Sprintf("toDateTime(%s, 'Europe/Berlin')", col), false)
	case 3:
		col := pick(g.r, []string{"dt", "dt64", "dtz", "dtz64"})
		return node("curated-toTimeZone", fmt.Sprintf("toTimeZone(%s, 'UTC')", col), false)
	case 4:
		a := g.temporal(depth - 1)
		unit := pick(g.r, []string{"HOUR", "DAY"})
		return node("curated-toStartOfInterval", fmt.Sprintf("toStartOfInterval(%s, INTERVAL 1 %s)", a.sql, unit), false, a)
	}
	panic("unreachable")
}

func (g *oracleGen) curatedArray(depth int) *fexpr {
	switch g.r.Intn(5) {
	case 0:
		return node("curated-arrayMap-num", "arrayMap(x -> (x * 2), arr_i)", false)
	case 1:
		return node("curated-arrayMap-toString", "arrayMap(x -> toString(x), arr_i)", false)
	case 2:
		a := g.arr(depth - 1)
		return node("curated-arrayFilter", fmt.Sprintf("arrayFilter(x -> (NOT empty(toString(x))), %s)", a.sql), false, a)
	case 3:
		return node("curated-arrayMap-binary", "arrayMap((x, y) -> (x + y), arr_i, arr_i)", false)
	case 4:
		return node("curated-arraySort-lambda", "arraySort(x -> -(x), arr_i)", false)
	}
	panic("unreachable")
}

func (g *oracleGen) curatedAggregate(depth int) *fexpr {
	switch g.r.Intn(5) {
	case 0:
		a := g.num(depth)
		fn := pick(g.r, []string{"sumState", "avgState", "minState"})
		return node("curated-"+fn, fmt.Sprintf("%s(%s)", fn, a.sql), true, a)
	case 1:
		a := g.num(depth)
		return node("curated-uniqState", fmt.Sprintf("uniqState(%s)", a.sql), true, a)
	case 2:
		a := g.num(depth)
		return node("curated-sumSimpleState", fmt.Sprintf("sumSimpleState(%s)", a.sql), true, a)
	case 3:
		e := pick(g.r, []string{"e8", "e16", "uid"})
		fn := pick(g.r, []string{"min", "max", "any", "uniq"})
		return node("curated-agg-special", fmt.Sprintf("%s(%s)", fn, e), true)
	case 4:
		a := g.num(depth)
		return node("curated-countIf", fmt.Sprintf("countIf(%s > 0)", a.sql), true, a)
	}
	panic("unreachable")
}

func (g *oracleGen) curatedWindow(depth int) *fexpr {
	frame := pick(g.r, []string{"()", "(PARTITION BY u8)", "(ORDER BY i32)", "(PARTITION BY u8 ORDER BY i32)"})
	switch g.r.Intn(5) {
	case 0:
		a := g.num(depth - 1)
		fn := pick(g.r, []string{"sum", "avg", "min", "max"})
		return node("curated-window-"+fn, fmt.Sprintf("%s(%s) OVER %s", fn, a.sql, frame), true, a)
	case 1:
		return node("curated-window-count", fmt.Sprintf("count() OVER %s", frame), true)
	case 2:
		fn := pick(g.r, []string{"row_number", "rank", "dense_rank"})
		return node("curated-window-"+fn, fmt.Sprintf("%s() OVER (ORDER BY i32)", fn), true)
	case 3:
		fn := pick(g.r, []string{"lagInFrame", "leadInFrame"})
		col := pick(g.r, []string{"i32", "s", "ni32"})
		return node("curated-window-"+fn, fmt.Sprintf("%s(%s, 1) OVER (ORDER BY i32)", fn, col), true)
	case 4:
		return node("curated-window-first_value", fmt.Sprintf("first_value(%s) OVER (ORDER BY i32)", pick(g.r, []string{"i32", "ns"})), true)
	}
	panic("unreachable")
}

func (g *oracleGen) curatedTuple(depth int) *fexpr {
	switch g.r.Intn(4) {
	case 0:
		a, b := g.num(depth-1), g.str(depth-1)
		return node("curated-tuple-fn", fmt.Sprintf("tuple(%s, %s)", a.sql, b.sql), false, a, b)
	case 1:
		return lit("curated-tuple-column", "tup")
	case 2:
		a := g.num(depth - 1)
		return node("curated-tuple-single", fmt.Sprintf("tuple(%s)", a.sql), false, a)
	case 3:
		return node("curated-untuple-cmp", "(tup = (1, 'a'))", false)
	}
	panic("unreachable")
}

// stripOuterParens removes one balanced outer parenthesis layer. Generated
// composite nodes always carry outer parentheses so they nest safely, but at
// the top level the parentheses parse as *parser.ParamExprList and chgen
// refuses every such expression. Stripping the outer layer lets the fuzzer
// observe the inference rule under the parentheses. The ParamExprList gap
// itself stays visible through dedicated canaries.
func stripOuterParens(sql string) string {
	for strings.HasPrefix(sql, "(") && strings.HasSuffix(sql, ")") {
		depth := 0
		balanced := true
		for i, c := range sql[:len(sql)-1] {
			switch c {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth == 0 && i < len(sql)-2 {
				balanced = false
				break
			}
		}
		if !balanced {
			return sql
		}
		sql = sql[1 : len(sql)-1]
	}
	return sql
}

// canaryExprs are fixed probes for the three known discrepancies, plus two
// probes that keep the parenthesized-expression gap on record. The fuzzer is
// blind if the known discrepancies do not surface as findings.
var canaryExprs = []*fexpr{
	lit("paren", "(1 + 1)"),
	lit("paren", "(s LIKE '%a%')"),
	lit("int-literal", "1"),
	node("unary-minus", "-1", false, lit("int-literal", "1")),
	lit("float-literal", "1.5"),
	node("like:LIKE", "s LIKE '%a%'", false, lit("str-column", "s")),
	node("like:ILIKE", "s ILIKE '%a%'", false, lit("str-column", "s")),
	node("like:NOT LIKE", "s NOT LIKE '%a%'", false, lit("str-column", "s")),
	lit("int-literal", "9223372036854775807"),
	lit("int-literal", "18446744073709551615"),
	// The blindness probe. The random walk can also make this
	// expression, but a probe that depends on chance is not a guard:
	// a grammar change that stops making it would disable the check
	// quietly. Thus give it to every run.
	node("empty", "empty(s)", false, lit("str-column", "s")),
}

// --- chgen side ---

func chgenInferType(schema *Schema, exprSQL string) (string, error) {
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM t").ParseStmts()
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if len(statements) != 1 {
		return "", fmt.Errorf("parse: got %d statements", len(statements))
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		return "", fmt.Errorf("parse: not a SELECT")
	}
	scope, _, err := resolveScope(selectQuery, schema)
	if err != nil {
		return "", err
	}
	inferred, err := inferExprType(selectQuery.SelectItems[0].Expr, scope)
	if err != nil {
		return "", err
	}
	return inferred.String(), nil
}

// --- ClickHouse side ---

// chOracle talks to the ClickHouse HTTP interface. Every run owns a private
// database (field database). The name goes into the `database` query key, so
// the unqualified table name `t` in the schema DDL and in every generated
// expression resolves inside that database only. Two runs against one server
// therefore cannot see, drop or re-create the fixture of the other run.
//
// A per-run database is the isolation unit, not a per-run table name, for two
// reasons. First, the literal name `t` is in the schema DDL, in the seed row,
// in every `FROM t` that the harness builds and inside the IN subqueries of
// curated grammar. A per-run table name would need a rewrite of all that SQL on
// the ClickHouse side, while the chgen catalog keeps the name it parsed from
// the same DDL; the two sides would then no longer read identical text and
// the measurement itself would change. Second, the `database` key is a
// transport detail: no byte of the measured SQL changes, thus the findings of
// an isolated run stay comparable with the earlier run series.
type chOracle struct {
	url      string
	database string
	client   *http.Client
}

// exec sends one query into the database of the run. adminExec sends a query
// with no database bound, which is necessary to create and to drop the run
// database itself.
func (o *chOracle) exec(query string) (string, error) {
	return o.execIn(o.database, query)
}

func (o *chOracle) adminExec(query string) (string, error) {
	return o.execIn("", query)
}

func (o *chOracle) execIn(database, query string) (string, error) {
	separator := "/?"
	if strings.Contains(o.url, "?") {
		separator = "&"
	}
	params := url.Values{"default_format": []string{"TabSeparatedRaw"}}
	if database != "" {
		params.Set("database", database)
	}
	resp, err := o.client.Post(o.url+separator+params.Encode(), "text/plain", strings.NewReader(query))
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	return strings.TrimRight(string(body), "\n"), nil
}

// readFixtureSignature returns a short text that identifies the shape of the
// fixture table `t` in the database of the run: the ordered column list with
// the types, and the number of rows. It is deliberately read with one query
// against system.columns, so a table that another run dropped gives an error
// or an empty answer instead of a plausible-looking result.
func readFixtureSignature(o *chOracle) (string, error) {
	columns, err := o.exec(
		"SELECT arrayStringConcat(groupArray(concat(name, ':', type)), ',') " +
			"FROM (SELECT name, type FROM system.columns " +
			"WHERE database = currentDatabase() AND table = 't' ORDER BY position)")
	if err != nil {
		return "", fmt.Errorf("read columns: %w", err)
	}
	if strings.TrimSpace(columns) == "" {
		return "", fmt.Errorf("table `t` has no columns in database %q", o.database)
	}
	rows, err := o.exec("SELECT count() FROM t")
	if err != nil {
		return "", fmt.Errorf("count rows: %w", err)
	}
	return fmt.Sprintf("rows=%s columns=%s", strings.TrimSpace(rows), columns), nil
}

// readServerRun returns the boot moment of the server in Unix seconds and the
// uptime in seconds at the moment of the call. The boot moment identifies the
// instance run: it is stable to the second while the server lives and it
// changes on every restart. ClickHouse 25.8 has no getServerUUID (it answers
// with code 46), and bare uptime is useless as an identity, because it moves
// every second.
// sortedTableNames gives the table names of a schema in a fixed order.
//
// schema.Tables is a Go map, and a map walk in Go is randomised per
// process. A caller that feeds the walk order into a seeded random
// stream, as the v3 draw index does, would then draw a different column
// on the same random index every run, even with one seed and one server.
// Sorting removes that source of drift.
func sortedTableNames(tables map[string]Table) []string {
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readServerRun(o *chOracle) (int64, int64, error) {
	raw, err := o.adminExec(
		"SELECT toUnixTimestamp(now() - toIntervalSecond(toUInt32(uptime()))), toUInt32(uptime())")
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected answer %q", raw)
	}
	boot, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse boot moment %q: %w", fields[0], err)
	}
	uptime, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse uptime %q: %w", fields[1], err)
	}
	return boot, uptime, nil
}

type chResult struct {
	typeName string
	err      string
}

// typeNames resolves the type of a batch of expressions with one query, and
// it is an EXECUTION witness: it asks for
//
//	toTypeName(e), ignore(e)
//
// per expression, not toTypeName(e) alone.
//
// Why the ignore() half is not decoration. ClickHouse answers toTypeName from
// ANALYSIS and never runs the function under it. Measured on 25.8.29.51 with
// a real Date column and a seeded row:
//
//	SELECT toTypeName(toStartOfInterval(d, INTERVAL 1 HOUR)) FROM t  -> DateTime
//	SELECT           toStartOfInterval(d, INTERVAL 1 HOUR)  FROM t  -> Code: 43,
//	    Illegal interval kind for argument data type Date
//
// With an analysis-only witness the server reports a type for an expression
// that it refuses to run. chgen refuses that expression on purpose (see the
// comment on checkToStartOfIntervalDomain in argument_domain.go), so the
// correct refusal was recorded as CHGEN_ERROR, the class that the report
// documents as "a defect by construction". That guarantee holds only while
// the server side executes. ignore() forces evaluation over the seeded row
// and returns a constant 0, so the refusal arrives while the type string
// stays exactly the one the earlier series compared.
//
// toTypeName is kept, and not replaced by reading the value back, because the
// measurement is a TYPE comparison: the printed value of a DateTime64 or a
// Decimal does not name its type, and an empty result set would name nothing
// at all. The type therefore still comes from analysis; execution is added
// beside it as a separate witness rather than in place of it.
//
// Batching and the one artifact it creates. Under toTypeName alone an
// aggregate and a bare column may share one SELECT, because neither is run.
// Once the witness executes they may not: `SELECT toTypeName(sum(i32)),
// ignore(sum(i32)), toTypeName(i32), ignore(i32) FROM t` fails with code 215,
// NOT_AN_AGGREGATE. That failure belongs to the BATCH, not to either
// expression, and recording it would be a harness fault disguised as a server
// refusal. Two mechanisms keep it out:
//
//   - the caller groups a batch by the aggregate flag of the generated
//     expression, so the mixed shape is not normally sent at all; and
//   - a failing batch is bisected down to a single expression, and only a
//     SINGLE-expression query may write a refusal into the result. A query
//     with one expression has no batch mates, thus its failure cannot be a
//     batching artifact.
//
// The second mechanism is the load-bearing one and it does not trust the
// first: a mislabelled aggregate costs extra queries, never a wrong verdict.
// The genuine form of the same error survives, because it fails on its own:
// `sum(i32) + i32` is refused as code 215 as a lone expression, under plain
// toTypeName as well, and it stays a refusal.
// chIgnoreConstant is the value that ignore() answers. Measured on
// 25.8.29.51: `SELECT toTypeName(1), ignore(1) FORMAT TSV` gives
// "UInt8\t0". typeNames reads the batch response by position, thus this
// constant is what shows that a field holds the ignore() column and not a
// type name.
const chIgnoreConstant = "0"

func (o *chOracle) typeNames(exprs []string) []chResult {
	results := make([]chResult, len(exprs))
	var run func(lo, hi int)
	run = func(lo, hi int) {
		parts := make([]string, 0, 2*(hi-lo))
		for _, e := range exprs[lo:hi] {
			// toTypeName names the type; ignore() makes the server
			// run the expression over the seeded row.
			parts = append(parts, "toTypeName("+e+")", "ignore("+e+")")
		}
		out, err := o.exec("SELECT " + strings.Join(parts, ", ") + " FROM t")
		if err == nil {
			fields := strings.Split(out, "\t")
			// Two fields per expression: the type name and the
			// constant that ignore() returns.
			if len(fields) == 2*(hi-lo) {
				// The read is POSITIONAL, thus a field count
				// alone does not show that field 2i holds the
				// type of expression i. Every odd field must
				// hold the constant that ignore() answers, so
				// check it. A misaligned read would move each
				// type one column and give a batch of wrong
				// comparisons that look like inference defects.
				//
				// A tab inside a type name cannot cause this.
				// Measured on 25.8.29.51: an Enum member that
				// holds a tab comes back as the two characters
				// \t, because TSV escapes a tab in a value,
				// while the separator is a real tab. The guard
				// is kept for the alignment of the two
				// expressions, which is code in this function
				// and can drift, and not for the escaping,
				// which the format guarantees.
				misaligned := -1
				for i := 0; i < hi-lo; i++ {
					if fields[2*i+1] != chIgnoreConstant {
						misaligned = i
						break
					}
				}
				if misaligned < 0 {
					for i := 0; i < hi-lo; i++ {
						results[lo+i] = chResult{typeName: fields[2*i]}
					}
					return
				}
				err = fmt.Errorf(
					"the batch response is misaligned: field %d must hold the ignore() constant %q and holds %q. "+
						"The response was NOT read as types; do not look for an inference defect",
					2*misaligned+1, chIgnoreConstant, fields[2*misaligned+1])
			} else {
				err = fmt.Errorf("expected %d fields, got %d", 2*(hi-lo), len(fields))
			}
		}
		if hi-lo == 1 {
			results[lo] = chResult{err: err.Error()}
			return
		}
		mid := (lo + hi) / 2
		run(lo, mid)
		run(mid, hi)
	}
	if len(exprs) > 0 {
		run(0, len(exprs))
	}
	return results
}

// sizedDecimalRe matches a sized Decimal spelling with its scale, for example
// Decimal64(4). It is anchored on a word boundary, so that it also finds the
// name inside a wrapper such as Nullable(Decimal64(4)).
var sizedDecimalRe = regexp.MustCompile(`\bDecimal(32|64|128|256)\((\d+)\)`)

// sizedDecimalPrecision gives the precision that each sized Decimal name
// fixes. Decimal32 holds 9 digits, Decimal64 18, Decimal128 38 and Decimal256
// 76. The same table is in canonicalDecimalType in infer_function.go.
var sizedDecimalPrecision = map[string]string{
	"32": "9", "64": "18", "128": "38", "256": "76",
}

// canonicalDecimalSpelling rewrites a sized Decimal name into the canonical
// Decimal(P, S) form.
//
// The two spellings name ONE type. Measured on ClickHouse 25.8.29.51:
//
//	SELECT toTypeName(toDecimal128(1, 4))                       -> Decimal(38, 4)
//	SELECT toDecimal128(1,4)::Decimal128(4)
//	     = toDecimal128(1,4)::Decimal(38, 4)                    -> 1
//
// The server always prints the canonical form, while chgen keeps the spelling
// of the DDL for a column, which is deliberate and is pinned by a generator
// test. Without this step a bare column reference such as `d128` is reported
// as a MISMATCH although no rule of inference took part and both sides mean
// the same type.
func canonicalDecimalSpelling(name string) string {
	return sizedDecimalRe.ReplaceAllStringFunc(name, func(match string) string {
		parts := sizedDecimalRe.FindStringSubmatch(match)
		return "Decimal(" + sizedDecimalPrecision[parts[1]] + ", " + parts[2] + ")"
	})
}

func normalizeTypeName(name string) string {
	name = canonicalDecimalSpelling(strings.TrimSpace(name))
	parsed, err := parseCHTypeName(name)
	if err != nil {
		// Keep an invalid spelling visible. A text cleanup here could make
		// two invalid type names look equal and hide a harness defect.
		return name
	}
	return parsed.String()
}

// There is no Bool-against-UInt8 equivalence here any more. That rewrite was
// never measured (it silenced 571 wrapper-grid cells sight unseen) and it
// hid a real defect: ClickHouse answers plain UInt8 for the whole predicate
// family (equals, less, empty, has, isNull, arrayExists, the LIKE family and
// so on), even over a Bool operand, while chgen invented Bool for that same
// family. The predicate class in the inference code now answers UInt8, so
// the oracle needs no equivalence to agree with the server there. See
// TestPredicateFamilyAnswersUInt8EvenOverBool and
// TestLogicOperatorsPreserveBool below for the measurement.

// The sized Decimal names and the canonical Decimal(P, S) name one type, thus
// the oracle must not report the pair as a mismatch. The precisions are
// measured: Decimal32 holds 9 digits, Decimal64 18, Decimal128 38 and
// Decimal256 76.
func TestNormalizeTypeNameCanonicalisesTheDecimalSpelling(t *testing.T) {
	for _, tt := range []struct{ sized, canonical string }{
		{"Decimal32(4)", "Decimal(9, 4)"},
		{"Decimal64(4)", "Decimal(18, 4)"},
		{"Decimal128(4)", "Decimal(38, 4)"},
		{"Decimal256(4)", "Decimal(76, 4)"},
		{"Decimal64(0)", "Decimal(18, 0)"},
		// The name also appears inside a wrapper.
		{"Nullable(Decimal64(4))", "Nullable(Decimal(18, 4))"},
		{"LowCardinality(Nullable(Decimal32(2)))", "LowCardinality(Nullable(Decimal(9, 2)))"},
		{"Array(Decimal128(6))", "Array(Decimal(38, 6))"},
	} {
		t.Run(tt.sized, func(t *testing.T) {
			if got, want := normalizeTypeName(tt.sized), normalizeTypeName(tt.canonical); got != want {
				t.Errorf("normalizeTypeName(%q) = %q, want %q", tt.sized, got, want)
			}
		})
	}
}

// A type that is not a sized Decimal must pass through unchanged, so that the
// rewrite cannot hide a real difference.
func TestNormalizeTypeNameLeavesTheOtherNames(t *testing.T) {
	for _, tt := range []struct{ name, want string }{
		{"Decimal(38, 4)", "Decimal(38, 4)"},
		{"Int64", "Int64"},
		{"Nullable(Int64)", "Nullable(Int64)"},
		{"Decimal32", "Decimal32"},
		{"Decimal64(4, 2)", "Decimal64(4, 2)"},
		{"MyDecimal64(4)", "MyDecimal64(4)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeTypeName(tt.name); got != tt.want {
				t.Errorf("normalizeTypeName(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// A named Tuple can use the compact chgen form or the server pretty-print
// form. Both forms must parse to one type tree. Element names and element
// types stay in the tree, so the comparison cannot hide a real difference.
func TestNormalizeTypeNameParsesNamedTuplePrettyPrint(t *testing.T) {
	compact := "Array(Tuple(x Int32, y String))"
	pretty := "Array(Tuple(\n    x Int32,\n    y String\n))"
	if got, want := normalizeTypeName(pretty), normalizeTypeName(compact); got != want {
		t.Fatalf("named Tuple forms differ: pretty=%q compact=%q", got, want)
	}

	for _, different := range []string{
		"Array(Tuple(x Int64, y String))",
		"Array(Tuple(a Int32, y String))",
		"Array(Tuple(x Int32, y FixedString(1)))",
		"Array(Tuple(x Int32, y Enum8('a b' = 1)))",
	} {
		if normalizeTypeName(different) == normalizeTypeName(compact) {
			t.Errorf("a different named Tuple became equal: %q", different)
		}
	}
	if normalizeTypeName("Enum8('a b' = 1)") == normalizeTypeName("Enum8('ab' = 1)") {
		t.Fatal("normalization removed a significant space from an Enum element")
	}
}

// predicateFamilyProbeDDL is a minimal fixture for the tests below. It
// holds a bare Bool column, a Nullable(Bool) column, an Array(Bool)
// column and a plain UInt64 column, plus the columns that the AND/OR
// pair-quirk rule needs: three DISTINCT Nullable(Bool) columns, three
// DISTINCT SimpleAggregateFunction(anyLast, Bool) columns (their marker
// SURVIVES a value read, see simpleAggregateMarkerSurvives) and two
// DISTINCT LowCardinality(Bool) columns. None of the frozen oracle
// fixtures carry this set together (oracleSchemaDDL has `b Bool` but no
// Nullable(Bool), Array(Bool) or SimpleAggregateFunction(Bool) column;
// A type that no fixture column holds cannot be reached by any generated
// expression. The table needs AggregatingMergeTree,
// because a SimpleAggregateFunction column is illegal on any other
// engine.
const predicateFamilyProbeDDL = `
CREATE TABLE t (
    b    Bool,
    b2   Bool,
    nb   Nullable(Bool),
    n1   Nullable(Bool),
    n2   Nullable(Bool),
    n3   Nullable(Bool),
    arrb Array(Bool),
    u64  UInt64,
    saf  SimpleAggregateFunction(anyLast, Bool),
    saf2 SimpleAggregateFunction(anyLast, Bool),
    saf3 SimpleAggregateFunction(anyLast, Bool),
    lb   LowCardinality(Bool),
    lb2  LowCardinality(Bool)
) ENGINE = AggregatingMergeTree ORDER BY tuple()
`

// inferSQLTypeOverT infers the type of one SELECT-list expression against
// predicateFamilyProbeDDL, which is aliased `t`. It is a thin wrapper
// around inferExprType, the same production entry point that
// gridChgenType and the oracle both call. It gives the bare type NAME;
// use inferSQLTypeOverTFull where a wrapper (Nullable, LowCardinality)
// is part of what the test checks.
func inferSQLTypeOverT(t *testing.T, schema *Schema, exprSQL string) string {
	t.Helper()
	return inferSQLTypeOverTFull(t, schema, exprSQL).Name
}

// inferSQLTypeOverTFull is inferSQLTypeOverT, but it gives the full CHType
// instead of only its bare name, so a caller can read a wrapper such as
// Nullable or LowCardinality as well as the base.
func inferSQLTypeOverTFull(t *testing.T, schema *Schema, exprSQL string) CHType {
	t.Helper()
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM t").ParseStmts()
	if err != nil {
		t.Fatalf("parse %q: %v", exprSQL, err)
	}
	if len(statements) != 1 {
		t.Fatalf("parse %q: got %d statements", exprSQL, len(statements))
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		t.Fatalf("parse %q: not a SELECT", exprSQL)
	}
	scope, _, err := resolveScope(selectQuery, schema)
	if err != nil {
		t.Fatalf("resolve scope for %q: %v", exprSQL, err)
	}
	inferred, err := inferExprType(selectQuery.SelectItems[0].Expr, scope)
	if err != nil {
		t.Fatalf("infer %q: %v", exprSQL, err)
	}
	return inferred
}

// TestPredicateFamilyAnswersUInt8EvenOverBool pins the measured rule that
// replaced the unmeasured Bool-against-UInt8 normaliser: every PREDICATE
// answers plain UInt8, even when it reads a Bool operand. Measured on
// ClickHouse 25.8.29.51 over real columns of a probe table: empty(Array
// (Bool)), notEmpty(Array(Bool)), has(Array(Bool), true), isNull(Nullable
// (Bool)), isNotNull(Nullable(Bool)), equals(Bool, Bool), less(Bool, Bool),
// arrayExists(x -> x, Array(Bool)) and arrayAll(x -> x, Array(Bool)) all
// give UInt8, never Bool.
//
// This is one direction of the family rule. The other direction,
// TestLogicOperatorsPreserveBool below, must also hold: a test that can
// only fail in one direction is not enough.
func TestPredicateFamilyAnswersUInt8EvenOverBool(t *testing.T) {
	schema, err := schemaFromDDLErr(t, predicateFamilyProbeDDL)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	for _, tt := range []struct {
		name string
		expr string
	}{
		{"equals operator", "b = b"},
		{"notequals operator", "b != b"},
		{"less operator", "b < b"},
		{"lessorequals operator", "b <= b"},
		{"greater operator", "b > b"},
		{"greaterorequals operator", "b >= b"},
		{"isnull", "isNull(nb)"},
		{"isnotnull", "isNotNull(nb)"},
		{"empty function", "empty(arrb)"},
		{"notempty function", "notEmpty(arrb)"},
		{"has", "has(arrb, true)"},
		{"arrayexists", "arrayExists(x -> x, arrb)"},
		{"arrayall", "arrayAll(x -> x, arrb)"},
		{"equals function form", "equals(b, b)"},
		{"less function form", "less(b, b)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := inferSQLTypeOverT(t, schema, tt.expr)
			if !strings.EqualFold(got, "UInt8") {
				t.Errorf("inferred type of %q = %q, want UInt8; the predicate class must never answer Bool", tt.expr, got)
			}
		})
	}
}

// TestLogicOperatorsPreserveBool pins the other direction of the family
// rule: AND, OR and NOT are not predicates, they are operations ON Bool,
// and they PRESERVE Bool under ONE rule: the base is Bool if AT LEAST
// ONE operand carries a Bool base OUTSIDE a Nullable wrapper and OUTSIDE
// a SimpleAggregateFunction marker that SURVIVES a value read; otherwise
// the base is UInt8. The Nullable wrapper on the RESULT is unchanged: it
// still applies whenever any operand is Nullable.
//
// This single predicate, and NOT a per-arity counter, is the fix for a
// defect the coordinator found by probing the live server: an earlier
// version of this rule counted Nullable operands and separately counted
// SimpleAggregateFunction operands, and flipped the base to UInt8 at a
// count of two of either kind. That counting rule answered
// "b AND n1 AND n2" as Nullable(UInt8), because it saw two Nullable
// operands (n1, n2) and ignored that a BARE Bool operand (b) was also
// present; the server answers Nullable(Bool) there. It also answered
// "saf AND n1" as Nullable(Bool), because neither counter alone reached
// two; the server answers Nullable(UInt8), because no operand carries a
// bare Bool. Both were silently wrong types outside the wrapper grid's
// own coverage. The single predicate below gets every one of these cells
// right, because it asks the same question of every operand and never
// counts.
//
// Measured on ClickHouse 25.8.29.51 (b, b2 bare Bool; n1, n2, n3
// distinct Nullable(Bool); saf, saf2, saf3 distinct
// SimpleAggregateFunction(anyLast, Bool), whose marker SURVIVES a value
// read; lb, lb2 distinct LowCardinality(Bool), which does NOT survive
// unwrapping and so counts as carrying Bool; never over a literal, which
// the server folds):
//
//	NOT Bool                    Bool               NOT UInt8              UInt8
//	Bool AND Bool                Bool               UInt8 AND UInt8        UInt8
//	n1 AND n2                    Nullable(UInt8)    n1 AND b               Nullable(Bool)
//	b AND n1 AND n2               Nullable(Bool)     n1 AND b AND n2        Nullable(Bool)
//	saf AND saf2                  UInt8              saf AND b              Bool
//	saf AND n1                    Nullable(UInt8)    saf AND saf2 AND n1    Nullable(UInt8)
//	lb AND lb2                    Bool               lb AND n1              Nullable(Bool)
//	(b AND n1) AND n2 (sealed)     Nullable(UInt8)    b AND (n1 AND n2)      Nullable(Bool)
//
// A test that only checked the predicate direction would pass even if a
// later change made AND/OR fall into the predicate rule and answer UInt8
// for a Bool operand, which would be a regression on measurement 4. This
// test exists so that regression fails here instead of going unnoticed.
func TestLogicOperatorsPreserveBool(t *testing.T) {
	schema, err := schemaFromDDLErr(t, predicateFamilyProbeDDL)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	for _, tt := range []struct {
		name string
		expr string
		want string
	}{
		{"not bool", "NOT b", "Bool"},
		{"and bool", "b AND b", "Bool"},
		{"or bool", "b OR b", "Bool"},
		{"not uint8", "NOT u64", "UInt8"},
		{"and uint8", "u64 AND u64", "UInt8"},

		// The pair-quirk cells the coordinator measured: two Nullable
		// operands flip the base, but a bare Bool ANYWHERE in the
		// list overrides that, regardless of order.
		{"two nullable, no bare bool", "n1 AND n2", "Nullable(UInt8)"},
		{"nullable plus bare bool", "n1 AND b", "Nullable(Bool)"},
		{"three operands, bare bool first", "b AND n1 AND n2", "Nullable(Bool)"},
		{"three operands, bare bool middle", "n1 AND b AND n2", "Nullable(Bool)"},
		{"three operands, bare bool last", "n1 AND n2 AND b", "Nullable(Bool)"},
		{"three nullable, no bare bool", "n1 AND n2 AND n3", "Nullable(UInt8)"},

		// The SimpleAggregateFunction marker mirrors the Nullable
		// pair-quirk when its OWN marker survives, but a bare Bool (or
		// LowCardinality(Bool), which is not excluded) still overrides
		// it, and mixing the two families with no bare Bool present
		// still gives UInt8: this is the defect the coordinator found,
		// because neither counter alone reached two.
		{"two surviving markers, no bare bool", "saf AND saf2", "UInt8"},
		{"surviving marker plus bare bool", "saf AND b", "Bool"},
		{"bare bool plus surviving marker", "b AND saf", "Bool"},
		{"three surviving markers", "saf AND saf2 AND saf3", "UInt8"},
		{"marker family mixed with nullable, no bare bool", "saf AND n1", "Nullable(UInt8)"},
		{"nullable mixed with marker, no bare bool", "n1 AND saf", "Nullable(UInt8)"},
		{"two markers plus nullable, no bare bool", "saf AND saf2 AND n1", "Nullable(UInt8)"},
		{"two nullable plus marker, no bare bool", "n1 AND n2 AND saf", "Nullable(UInt8)"},

		// LowCardinality(Bool) is NOT excluded by the rule: it counts
		// as carrying a bare Bool, exactly like a plain Bool column.
		{"two lowcardinality bool", "lb AND lb2", "Bool"},
		{"lowcardinality bool plus bare bool", "lb AND b", "Bool"},
		{"lowcardinality bool plus nullable", "lb AND n1", "Nullable(Bool)"},

		// Parenthesisation is part of the rule. An EXPLICIT paren
		// seals its sub-expression at its own computed type, so the
		// outer AND/OR can no longer see the bare Bool that the seal
		// hides; the unparenthesised spelling has no seal, so the
		// bare Bool stays visible to the outer AND, although both
		// spellings parse to the same nested BinaryOperation shape.
		{"sealed left operand hides the bare bool", "(b AND n1) AND n2", "Nullable(UInt8)"},
		{"sealed right operand still exposes the bare bool", "b AND (n1 AND n2)", "Nullable(Bool)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := inferSQLTypeOverTFull(t, schema, tt.expr).String()
			if got != tt.want {
				t.Errorf("inferred type of %q = %q, want %s", tt.expr, got, tt.want)
			}
		})
	}
}

// --- findings ---

type finding struct {
	Class     string `json:"class"`
	Expr      string `json:"expr"`
	Kind      string `json:"kind"`
	ChgenType string `json:"chgen_type,omitempty"`
	CHType    string `json:"ch_type,omitempty"`
	ChgenErr  string `json:"chgen_error,omitempty"`
	CHErr     string `json:"ch_error,omitempty"`
	MinExpr   string `json:"min_expr,omitempty"`
	MinKind   string `json:"min_kind,omitempty"`
	MinChgen  string `json:"min_chgen_type,omitempty"`
	MinCH     string `json:"min_ch_type,omitempty"`
	// KindChain is the path of kinds from the outermost expression to the
	// node that names the finding, joined with ">". It shows which outer
	// shapes carried this mismatch, which the Kind and MinKind pair alone
	// cannot show for a chain of more than two steps.
	KindChain string `json:"kind_chain,omitempty"`
}

type oracleReport struct {
	Version     int            `json:"version"`
	Population  any            `json:"population"`
	Date        string         `json:"date"`
	CHVersion   string         `json:"clickhouse_version"`
	Seed        int64          `json:"seed"`
	Generated   int            `json:"generated_expressions"`
	Canaries    int            `json:"canary_expressions"`
	Counts      map[string]int `json:"counts"`
	MismatchSig map[string]int `json:"mismatch_signatures"`
	// KindChains counts, per path of kinds from the outermost expression to
	// the node that names the finding, the mismatches that walked that path.
	//
	// It exists because shrink() names a finding after the DEEPEST
	// mismatching node, thus an outer shape can never name a class of its
	// own. A reader who counts only mismatch_signatures cannot tell a shape
	// that chgen types correctly from a shape that the reporter cannot
	// name. The chains make the second case visible.
	//
	// Read it beside mismatch_signatures, not in place of it: one chain
	// covers many signatures and one signature appears in many chains.
	KindChains map[string]int `json:"outer_kind_chains"`
	// Blind43 counts, per expression kind, the expressions that chgen
	// typed while the server answered ILLEGAL_TYPE_OF_ARGUMENT. It is
	// the direct measure of a missing domain rule.
	Blind43 map[string]int `json:"blind_code43_by_kind"`
	// RunDatabase is the private database that this run made and used.
	RunDatabase string `json:"run_database"`
	FixtureHash string `json:"fixture_hash"`
	// FixtureSignature is the shape of the fixture table that the run
	// created and verified. Two runs that report a different signature did
	// not measure the same fixture.
	FixtureSignature string `json:"fixture_signature"`
	// ServerRun is the boot moment of the ClickHouse instance in Unix
	// seconds. The oracle is deterministic in its own part, but the server
	// is not: on one and the same commit a fresh container and a container
	// that has run for two hours give different server-side CH_ERROR
	// numbers, and each state is deterministic in itself. Thus two runs in
	// a row that agree show only that the instance did not change between
	// them. This field names the instance, so that a difference in the
	// conditions cannot be read as a difference in the code.
	//
	// It is read as
	//
	//	SELECT toUnixTimestamp(now() - toIntervalSecond(toUInt32(uptime())))
	//
	// which is stable to the second inside one instance and changes on
	// every restart. ClickHouse 25.8 has no getServerUUID (code 46), and
	// bare uptime cannot be compared, because it moves every second.
	//
	// The value carries about a second of jitter, because now() and
	// uptime() truncate independently, thus the comparator matches it with
	// a tolerance. See oraclereport.ServerRunToleranceS.
	ServerRun int64 `json:"server_run"`
	// ServerUptimeS is the uptime in seconds at the moment of the run. It
	// tells the reader how warm the server was. It is never a comparison
	// key, because it moves every second.
	ServerUptimeS int64 `json:"server_uptime_s"`
	// CHErrorCodes counts the CH_ERROR findings per ClickHouse error code.
	// It is UNCAPPED, unlike Findings, thus it can be compared between
	// runs and it can carry a rate.
	CHErrorCodes map[string]int `json:"ch_error_codes"`
	// CHErrorByKind counts the CH_ERROR expressions per expression kind.
	// Also uncapped.
	CHErrorByKind map[string]int `json:"ch_error_by_kind"`
	// CHErrorDigest is a sha256 over the sorted, newline-joined text of
	// EVERY CH_ERROR expression with its error code, not only over the
	// listed 200. Two runs with an equal digest saw exactly the same
	// CH_ERROR set. This is the value to diff between runs.
	CHErrorDigest string `json:"ch_error_digest"`
	// FindingsCHErrorCap is the number of CH_ERROR findings that Findings
	// holds at most. Read the note on Findings before you diff the list.
	FindingsCHErrorCap int `json:"findings_ch_error_cap"`
	// Findings holds the full MISMATCH, CHGEN_ERROR and
	// CH_ERROR_43_CHGEN_TYPED lists, but only the first
	// FindingsCHErrorCap CH_ERROR entries in generation order. Thus:
	//
	//   MAY be compared between runs: every MISMATCH entry, every
	//   CHGEN_ERROR entry, every CH_ERROR_43_CHGEN_TYPED entry,
	//   MismatchSig, Counts, Blind43, CHErrorCodes, CHErrorByKind and
	//   CHErrorDigest.
	//
	// A CH_ERROR_43_CHGEN_TYPED entry is a second entry for an expression
	// that ALSO raised counts["CH_ERROR"]. It is never capped, because it
	// is the blindness class: chgen gave the type in its ChgenType field
	// while the server refused the expression. The cap on CH_ERROR is
	// correct for generator waste, and the blindness class must not live
	// inside it, thus the two lists are separate.
	//
	//   MUST NOT be compared between runs, and MUST NOT be used to
	//   compute a rate: the CH_ERROR entries of this list. The list is
	//   truncated while Counts["CH_ERROR"] is not, so a rate taken from
	//   the list is wrong as soon as the count passes the cap.
	//
	// The cap stays. Raising it would only move the same trap to a higher
	// number, and the full list is large sample noise, not a finding: the
	// CH_ERROR class is generator waste. The examples in the list are for
	// reading; the uncapped digest and the per-code counts carry the
	// comparison.
	Findings []finding `json:"findings"`
	// RoundTripChecked is the number of DISTINCT type strings, among the
	// run's own OK cells, that held an AggregateFunction or a parametric
	// spelling and so were sent through roundTripColumnType (CREATE TABLE
	// with that exact type string). This field is the guard against the
	// anti-pattern the project keeps rediscovering: a check that examined
	// no cell and a check that examined many both look like a green run
	// unless the count itself is on record. Zero here on a non-empty OK
	// population is a broken run, not a clean one.
	RoundTripChecked int `json:"round_trip_checked"`
	// RoundTripFailures lists every OK-cell type string, unique, for which
	// CREATE TABLE failed. An entry here is chgen inferring a type string
	// that names no legal ClickHouse type at all, which the recorded defect
	// proved happens and is a worse defect than a wrong type (a wrong type
	// is still a type). The run does not fail on a non-empty list, for the
	// same reason TestTypeOracle does not fail on MISMATCH: the failing
	// count is read from here, not inferred from the run's exit status.
	RoundTripFailures []roundTripFailure `json:"round_trip_failures,omitempty"`
	// LaneDraws counts, for EVERY top-level lane in topLevelLanes and for
	// every v3 sub-lane inside registryDraw, how many draws the lane
	// turned into a real produced expression versus how many draws fell
	// through to a fallback generator instead. A fallback draw is
	// recorded under the lane's own name with a "-fallback" suffix.
	//
	// This is the positive reachability witness for every top-level
	// lane, in the same spirit as RoundTripChecked: outer_kind_chains is
	// a mismatch-path histogram, written at one site only inside the
	// MISMATCH branch, so a run with zero mismatches reports an empty
	// map there regardless of whether every lane fired or none did. That
	// field cannot answer "did this lane ever run", and reading it as if
	// it could is the exact trap the recorded defect already recorded.
	// Populated on BOTH a v2 run and a v3 run, because topLevel counts
	// every lane it draws and not only the v3 registry lanes; it is
	// nil only if topLevel was never called at all.
	LaneDraws map[string]int `json:"lane_draws,omitempty"`
	// SamplingPlanID and SamplingPlanHash identify the scheduler contract.
	// The three lane maps keep attempts, accepted unique expressions, and
	// fallbacks separate. An attempt cannot satisfy an accepted quota.
	SamplingPlanID   string         `json:"sampling_plan_id,omitempty"`
	SamplingPlanHash string         `json:"sampling_plan_hash,omitempty"`
	LaneAttempts     map[string]int `json:"lane_attempts,omitempty"`
	LaneAccepted     map[string]int `json:"lane_accepted,omitempty"`
	LaneFallback     map[string]int `json:"lane_fallback,omitempty"`
}

// roundTripFailure names one OK-cell type string that CREATE TABLE refused.
type roundTripFailure struct {
	TypeName string `json:"type_name"`
	CHErr    string `json:"ch_error"`
}

// shrink walks the derivation tree and returns the smallest child whose types
// still disagree in the same way (any disagreement counts; the root cause of a
// parent mismatch is usually a child mismatch).
//
// The descent means a finding is ALWAYS named after the deepest mismatching
// node, thus the node above it can never name a class. `256 < ni32` disagrees
// on its own, so `false OR (256 < ni32)` is filed as a comparison and the logic
// node is never named. A low count in a signature class therefore does not
// prove that the shape is rare, and a class at zero cannot be read as "chgen is
// correct here": the class can be unreachable for the reporter.
//
// The descent stays as it is, because the mismatch signature counts are
// compared between runs and a new naming rule would break that comparison.
// shrink therefore also returns the chain of kinds that it walked, outermost
// first. The chain goes to the KindChains field, which makes a shape that
// cannot name a finding visible without a change to the existing counts.
func shrink(schema *Schema, o *chOracle, e *fexpr) (min *fexpr, chgenType, chType string, chain []string) {
	for _, child := range e.children {
		child.sql = stripOuterParens(child.sql)
		got, err := chgenInferType(schema, child.sql)
		if err != nil {
			continue
		}
		res := o.typeNames([]string{child.sql})[0]
		if res.err != "" {
			continue
		}
		if normalizeTypeName(got) != normalizeTypeName(res.typeName) {
			min, minChgen, minCH, inner := shrink(schema, o, child)
			return min, minChgen, minCH, append([]string{e.kind}, inner...)
		}
	}
	got, err := chgenInferType(schema, e.sql)
	if err != nil {
		return e, "<error>", "", []string{e.kind}
	}
	res := o.typeNames([]string{e.sql})[0]
	return e, got, res.typeName, []string{e.kind}
}

func currentOraclePlan(lookup func(string) (string, bool)) (string, error) {
	retiredKey := "CHGEN_ORACLE_" + "GRAMMAR"
	if value, present := lookup(retiredKey); present {
		return "", fmt.Errorf("%s=%q is retired and cannot replay a historical population; remove it and use CHGEN_ORACLE_PLAN=%s", retiredKey, value, currentSamplingPlanID)
	}
	plan, present := lookup("CHGEN_ORACLE_PLAN")
	if !present || plan == "" {
		return currentSamplingPlanID, nil
	}
	if plan != currentSamplingPlanID {
		return "", fmt.Errorf("CHGEN_ORACLE_PLAN: unknown value %q (use %s)", plan, currentSamplingPlanID)
	}
	return plan, nil
}

func TestTypeOracle(t *testing.T) {
	baseURL := os.Getenv("CHGEN_ORACLE_URL")
	if baseURL == "" {
		t.Skip("CHGEN_ORACLE_URL is not set; start a disposable ClickHouse and set the URL to run the oracle")
	}
	seed := int64(1)
	if v := os.Getenv("CHGEN_ORACLE_SEED"); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("CHGEN_ORACLE_SEED: %v", err)
		}
		seed = parsed
	}
	count := 5000
	if v := os.Getenv("CHGEN_ORACLE_N"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("CHGEN_ORACLE_N: %v", err)
		}
		count = parsed
	}
	plan, err := currentOraclePlan(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := selectOracleFixture(os.Getenv("CHGEN_ORACLE_FIXTURE_ID"))
	if err != nil {
		t.Fatal(err)
	}
	schemaDDL, seedRow := fixture.schema, fixture.seedRow
	t.Logf("seed=%d n=%d url=%s plan=%s fixture=%s", seed, count, baseURL, plan, fixture.id)

	oracle := &chOracle{url: baseURL, client: &http.Client{Timeout: 60 * time.Second}}
	version, err := oracle.adminExec("SELECT version()")
	if err != nil {
		t.Fatalf("ClickHouse is not reachable: %v", err)
	}
	t.Logf("clickhouse version()=%s", version)

	// The identity of the server run. Without it a stored report cannot be
	// compared with a new one, because the server-side CH_ERROR class moves
	// with the age of the instance on one and the same commit.
	serverRun, serverUptime, err := readServerRun(oracle)
	if err != nil {
		t.Fatalf("read the server run identity: %v", err)
	}
	t.Logf("server_run=%d server_uptime_s=%d", serverRun, serverUptime)

	// Isolation. The run makes its own database and works only in it. The
	// name holds the process id and a nanosecond stamp, thus two runs on
	// one server, and also two runs on one machine, get different names.
	// CHGEN_ORACLE_DATABASE overrides the name for a manual inspection.
	runDatabase := os.Getenv("CHGEN_ORACLE_DATABASE")
	createdDatabase := false
	if runDatabase == "" {
		runDatabase = fmt.Sprintf("chgen_oracle_%d_%d", os.Getpid(), time.Now().UnixNano())
	}
	if _, err := oracle.adminExec("CREATE DATABASE " + runDatabase); err != nil {
		// A database that is already present is not ours to own. Refuse,
		// because a silent reuse is exactly the shared fixture that this
		// harness must not have.
		t.Fatalf("create run database %q: %v", runDatabase, err)
	}
	createdDatabase = true
	oracle.database = runDatabase
	t.Logf("run database=%s", runDatabase)
	t.Cleanup(func() {
		// Clean up only a database that this run created. A database
		// given from the outside stays, because the run does not own it.
		if !createdDatabase {
			return
		}
		if _, err := oracle.adminExec("DROP DATABASE IF EXISTS " + runDatabase); err != nil {
			t.Logf("drop run database %q: %v", runDatabase, err)
		}
	})

	if _, err := oracle.exec(schemaDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := oracle.exec(seedRow); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	// The fixture signature. It is the exact shape that the run created:
	// the ordered column-name/column-type list of table `t` plus the row
	// count. checkFixture compares the live shape against it. A collision
	// with another run, or any other loss of the fixture, then stops the
	// run instead of turning into confident but meaningless numbers.
	fixtureSignature, err := readFixtureSignature(oracle)
	if err != nil {
		t.Fatalf("read fixture signature: %v", err)
	}
	checkFixture := func(stage string) {
		got, err := readFixtureSignature(oracle)
		if err != nil {
			t.Fatalf("fixture check (%s): the fixture of this run is gone: %v", stage, err)
		}
		if got != fixtureSignature {
			t.Fatalf("fixture check (%s): the fixture of this run changed shape.\n"+
				"  expected: %s\n  observed: %s\n"+
				"Another run probably wrote into database %q. The numbers of this run are void.",
				stage, fixtureSignature, got, runDatabase)
		}
	}
	checkFixture("after setup")

	schema, err := schemaFromDDLErr(t, schemaDDL)
	if err != nil {
		t.Fatalf("chgen schema parse: %v", err)
	}

	// The current generator combines curated and registry lanes.
	gen := &oracleGen{r: rand.New(rand.NewSource(seed))}
	{
		// The index is built from the columns of the schema that THIS run
		// created. A lane can therefore never name a column that the live
		// table does not hold.
		//
		// The walk below reads sortedTableNames and table.ColumnOrder, and
		// NEVER ranges over schema.Tables or table.Columns directly. Both
		// are Go maps, and a map walk in Go is randomised per process, not
		// per seed. gen.r and the composed-member rand.Rand both draw from
		// this columns slice by INDEX, thus a randomised slice order fed a
		// different column to the same draw on every run, even with a
		// pinned seed and a pinned server. Measured: the regression found the
		// signature set (MISMATCH, blindness, ch_error_digest) changing
		// between two runs of identical code, same seed, same live server.
		// A standalone dump of just this columns slice, with no server
		// call at all, showed a different column order every run; once the
		// walk used table.ColumnOrder instead, two dumps came out
		// byte-identical.
		var columns []fixtureColumn
		for _, tableName := range sortedTableNames(schema.Tables) {
			table := schema.Tables[tableName]
			for _, columnName := range table.ColumnOrder {
				column := table.Columns[columnName]
				columns = append(columns, fixtureColumn{name: column.Name, columnType: column.Type})
			}
		}
		// The index is built over the fixture columns AND one level of
		// composed call results, so that the registry lanes can put an
		// aggregate over an arithmetic result and a function over a
		// function. Drawing from the columns alone gave depth-1 calls
		// only, and the wrapper defects live at the composition
		// boundary.
		//
		// The composed members are drawn with an OWN random source and
		// not with gen.r. The build happens once at setup while gen.r
		// feeds every expression of the run, thus taking the members
		// from gen.r would shift the whole stream and no stored seed
		// would reproduce its expressions again.
		composedIndex, composedMembers := buildComposedDrawIndex(columns, rand.New(rand.NewSource(seed)))
		gen.drawIndex = composedIndex
		gen.drawScalar = drawNamesOfPlace(gen.drawIndex, placementScalar)
		gen.drawAggregate = drawNamesOfPlace(gen.drawIndex, placementAggregate)
		gen.drawWindow = drawNamesOfPlace(gen.drawIndex, placementWindow)
		gen.drawIllegalScalar = drawNamesWithIllegal(gen.drawIndex, placementScalar)
		gen.drawIllegalAggregate = drawNamesWithIllegal(gen.drawIndex, placementAggregate)
		t.Logf("registry draw index: %d candidates, scalar=%d aggregate=%d window=%d, "+
			"with an illegal half: scalar=%d aggregate=%d",
			len(gen.drawIndex), len(gen.drawScalar), len(gen.drawAggregate), len(gen.drawWindow),
			len(gen.drawIllegalScalar), len(gen.drawIllegalAggregate))
		t.Logf("registry composition: %d composed members returned to the pools", len(composedMembers))
	}
	exprs, planStats, err := buildCurrentSamplingPlan(seed, count, gen, canaryExprs)
	if err != nil {
		t.Fatalf("build sampling plan: %v", err)
	}
	planHash := currentSamplingPlanHash(gen)

	report := oracleReport{
		Version:     oraclereport.ReportVersion,
		Population:  oraclereport.CurrentPopulation(planHash),
		Date:        time.Now().UTC().Format(time.RFC3339),
		CHVersion:   version,
		Seed:        seed,
		Generated:   len(exprs) - len(canaryExprs),
		Canaries:    len(canaryExprs),
		Counts:      map[string]int{},
		MismatchSig: map[string]int{},
		KindChains:  map[string]int{},
		Blind43:     map[string]int{},

		RunDatabase:        runDatabase,
		FixtureHash:        conformance.StableHash(schemaDDL),
		FixtureSignature:   fixtureSignature,
		ServerRun:          serverRun,
		ServerUptimeS:      serverUptime,
		CHErrorCodes:       map[string]int{},
		CHErrorByKind:      map[string]int{},
		FindingsCHErrorCap: chErrorFindingCap,
	}
	// chErrorDigestLines holds one line per CH_ERROR expression. It is
	// uncapped, and it feeds report.CHErrorDigest.
	chErrorDigestLines := make([]string, 0, 1024)
	lastCHErrorCount := 0

	// okRoundTripTypes collects, from this run's own OK population, the
	// DISTINCT type strings that hold an AggregateFunction or a parametric
	// spelling. See the regression: the regression proved the round-trip
	// mechanism catches chgen inferring a type string with no real
	// referent, but only ran it over a curated 15-expression probe list,
	// never over a real fuzz run's OK cells. This closes that gap.
	okRoundTripTypes := map[string]bool{}

	// The witness executes the expressions (see typeNames), thus an
	// aggregate and a bare column can no longer share one query: together
	// they raise code 215, NOT_AN_AGGREGATE, which is a property of the
	// batch and not of any member. Group each batch by the aggregate flag
	// so that shape is not sent. This is a SPEED measure only. The
	// correctness guarantee is in typeNames, which bisects a failing batch
	// to a single expression before it records any refusal, so a
	// mislabelled flag costs queries and never a wrong verdict.
	batches := make([][]*fexpr, 0, 2*(len(exprs)/50+1))
	const batchSize = 50
	for _, group := range [2]bool{false, true} {
		members := make([]*fexpr, 0, len(exprs))
		for _, e := range exprs {
			if e.agg == group {
				members = append(members, e)
			}
		}
		for lo := 0; lo < len(members); lo += batchSize {
			hi := lo + batchSize
			if hi > len(members) {
				hi = len(members)
			}
			batches = append(batches, members[lo:hi])
		}
	}
	for batchIndex, batch := range batches {
		inputs := make([]conformance.Input, len(batch))
		for i, e := range batch {
			inputs[i] = conformance.Input{ID: fmt.Sprintf("%08d", batchIndex*batchSize+i), Expression: e.sql, Table: "t"}
		}
		conformanceReport, runErr := conformance.RunBatched(context.Background(), schemaDDL, seedRow, inputs, conformance.BatchLanes{
			Info: func(context.Context) (conformance.ServerInfo, error) {
				return conformance.ServerInfo{Version: version, ServerRun: serverRun, UptimeS: serverUptime}, nil
			},
			Infer: func(ordered []conformance.Input) []conformance.TypeResult {
				results := make([]conformance.TypeResult, len(ordered))
				for index, input := range ordered {
					inferred, inferErr := chgenInferType(schema, input.Expression)
					if inferErr != nil {
						results[index].Error = inferErr.Error()
					} else {
						results[index] = conformance.CanonicalResult(inferred)
					}
				}
				return results
			},
			Analyze: func(_ context.Context, ordered []conformance.Input) []conformance.TypeResult {
				expressions := make([]string, len(ordered))
				for index, input := range ordered {
					expressions[index] = "toTypeName(" + input.Expression + ")"
				}
				answers := conformanceServerProjection(oracle, "t", expressions)
				results := make([]conformance.TypeResult, len(answers))
				for index, answer := range answers {
					if answer.err != "" {
						results[index] = conformance.TypeResult{Error: answer.err, ErrorCode: gridErrorCode(answer.err)}
					} else {
						results[index] = conformance.CanonicalResult(answer.typeName)
					}
				}
				return results
			},
			Execute: func(_ context.Context, ordered []conformance.Input) []conformance.ExecutionResult {
				expressions := make([]string, len(ordered))
				for index, input := range ordered {
					expressions[index] = "ignore(" + input.Expression + ")"
				}
				answers := conformanceServerProjection(oracle, "t", expressions)
				results := make([]conformance.ExecutionResult, len(answers))
				for index, answer := range answers {
					if answer.err != "" {
						results[index] = conformance.ExecutionResult{Error: answer.err, ErrorCode: gridErrorCode(answer.err)}
					} else {
						results[index].Ran = true
					}
				}
				return results
			},
		})
		if runErr != nil {
			t.Fatalf("shared conformance batch %d: %v", batchIndex, runErr)
		}
		chResults := make([]chResult, len(conformanceReport.Cells))
		for index, cell := range conformanceReport.Cells {
			if cell.Execution.Error != "" {
				chResults[index].err = cell.Execution.Error
			} else if cell.Analysis.Error != "" {
				chResults[index].err = cell.Analysis.Error
			} else {
				chResults[index].typeName = cell.Analysis.Raw
			}
		}
		for i, e := range batch {
			chgenType, chgenErr := chgenInferType(schema, e.sql)
			ch := chResults[i]
			switch {
			case ch.err != "":
				report.Counts["CH_ERROR"]++
				code := clickHouseErrorCode(ch.err)
				report.CHErrorCodes[code]++
				report.CHErrorByKind[e.kind]++
				chErrorDigestLines = append(chErrorDigestLines, code+"\t"+e.sql)
				// The blindness count. chgen gave a type to an
				// expression that the server refuses with
				// ILLEGAL_TYPE_OF_ARGUMENT. This is the silently
				// wrong answer that a domain rule must remove.
				// The count is not capped, unlike the findings
				// list, thus it stays comparable between runs.
				if chgenErr == nil && strings.Contains(ch.err, "Code: 43.") {
					report.Counts["CH_ERROR_43_CHGEN_TYPED"]++
					report.Blind43[e.kind]++
					// The blindness class also gets its OWN
					// findings entry, and that entry is NEVER
					// capped. A count that tells a reader that
					// a defect exists, while it gives no way
					// to reach one instance, is the same
					// failure as a test that cannot show what
					// it looks for.
					//
					// The entry carries chgen_type, the type
					// that chgen gave. That field is what
					// separates a blind answer from a correct
					// refusal: in a correct refusal chgen also
					// refused, thus the pair is a CH_ERROR
					// entry with no chgen_type.
					report.Findings = append(report.Findings, finding{
						Class: "CH_ERROR_43_CHGEN_TYPED", Expr: e.sql, Kind: e.kind,
						ChgenType: chgenType, CHErr: firstLine(ch.err),
					})
				}
				if report.Counts["CH_ERROR"] <= chErrorFindingCap {
					report.Findings = append(report.Findings, finding{
						Class: "CH_ERROR", Expr: e.sql, Kind: e.kind, CHErr: firstLine(ch.err),
					})
				}
			case chgenErr != nil:
				report.Counts["CHGEN_ERROR"]++
				report.Findings = append(report.Findings, finding{
					Class: "CHGEN_ERROR", Expr: e.sql, Kind: e.kind, CHType: ch.typeName, ChgenErr: chgenErr.Error(),
				})
			case normalizeTypeName(chgenType) != normalizeTypeName(ch.typeName):
				report.Counts["MISMATCH"]++
				min, minChgen, minCH, chain := shrink(schema, oracle, e)
				sig := fmt.Sprintf("%s: chgen=%s ch=%s", min.kind, normalizeTypeName(minChgen), normalizeTypeName(minCH))
				report.MismatchSig[sig]++
				report.KindChains[strings.Join(chain, ">")]++
				report.Findings = append(report.Findings, finding{
					Class: "MISMATCH", Expr: e.sql, Kind: e.kind,
					ChgenType: chgenType, CHType: ch.typeName,
					MinExpr: min.sql, MinKind: min.kind, MinChgen: minChgen, MinCH: minCH,
					KindChain: strings.Join(chain, ">"),
				})
			default:
				report.Counts["OK"]++
				// Round-trip candidate. Only a type string that holds an
				// AggregateFunction or a parametric spelling is worth the
				// CREATE TABLE probe; the rest are shapes toTypeName
				// already witnesses correctly (see the regression). Dedupe by
				// type string here, in the collection step, so the probe
				// itself runs once per DISTINCT type, not once per cell.
				if holdsAggregateOrParametricType(ch.typeName) {
					okRoundTripTypes[ch.typeName] = true
				}
			}
		}
		// Defence in depth. A batch that fails wholesale looks the same
		// as a batch of bad expressions, thus a lost fixture would hide
		// inside the CH_ERROR class. Verify the fixture after every
		// batch that produced any CH_ERROR, so the run stops at the
		// collision instead of reporting numbers from a dead table.
		if report.Counts["CH_ERROR"] > lastCHErrorCount {
			checkFixture(fmt.Sprintf("after batch at offset %d", batchIndex*batchSize))
			lastCHErrorCount = report.Counts["CH_ERROR"]
		}
	}
	checkFixture("before the report")

	// Round-trip the OK population's aggregate/parametric type strings
	// through CREATE TABLE. This is the same mechanism
	// TestOKAggregateAndParametricTypesRoundTrip already runs over a
	// curated probe list; here it runs over what THIS run actually
	// generated. The count is recorded, not merely logged, because
	// t.Logf output is swallowed by this project's test harness and a
	// check that leaves no durable trace of having run is the very
	// anti-pattern this ticket exists to close.
	report.SamplingPlanID = currentSamplingPlanID
	report.SamplingPlanHash = planHash
	report.LaneAttempts = planStats.Attempts
	report.LaneAccepted = planStats.Accepted
	report.LaneFallback = planStats.Fallback

	report.RoundTripChecked = len(okRoundTripTypes)
	for typeName := range okRoundTripTypes {
		roundTripErr, cleanupWarn := roundTripColumnType(oracle, typeName)
		if cleanupWarn != nil {
			// The cleanup warning is not a finding: the type
			// round-tripped. It goes to the log so a leaked
			// throwaway table stays visible.
			t.Logf("round-trip cleanup warning for type %q, not a finding: %s",
				typeName, firstLine(cleanupWarn.Error()))
		}
		if roundTripErr != nil {
			report.RoundTripFailures = append(report.RoundTripFailures, roundTripFailure{
				TypeName: typeName,
				CHErr:    firstLine(roundTripErr.Error()),
			})
		}
	}
	sort.Slice(report.RoundTripFailures, func(i, j int) bool {
		return report.RoundTripFailures[i].TypeName < report.RoundTripFailures[j].TypeName
	})

	sort.Strings(chErrorDigestLines)
	sum := sha256.Sum256([]byte(strings.Join(chErrorDigestLines, "\n")))
	report.CHErrorDigest = hex.EncodeToString(sum[:])

	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Class != report.Findings[j].Class {
			return report.Findings[i].Class < report.Findings[j].Class
		}
		return report.Findings[i].Expr < report.Findings[j].Expr
	})

	outPath := os.Getenv("CHGEN_ORACLE_OUT")
	if outPath == "" {
		outPath = "/tmp/chgen-typeoracle.json"
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Logf("counts: %v", report.Counts)
	t.Logf("distinct mismatch signatures: %d", len(report.MismatchSig))
	t.Logf("distinct outer kind chains: %d", len(report.KindChains))
	t.Logf("ch_error codes: %v", report.CHErrorCodes)
	t.Logf("ch_error digest (uncapped): %s", report.CHErrorDigest)
	t.Logf("report: %s", outPath)
	t.Logf("round_trip_checked (distinct OK-population aggregate/parametric types): %d", report.RoundTripChecked)
	t.Logf("lane_draws: %v", report.LaneDraws)
	t.Logf("sampling_plan: id=%s hash=%s", report.SamplingPlanID, report.SamplingPlanHash)
	t.Logf("lane_attempts: %v", report.LaneAttempts)
	t.Logf("lane_accepted: %v", report.LaneAccepted)
	t.Logf("lane_fallback: %v", report.LaneFallback)

	// Self-defence, mirroring TestOKAggregateAndParametricTypesRoundTrip's
	// own guard: a check that examines zero cells is indistinguishable
	// from a check that never ran, and this project has re-shipped that
	// exact anti-pattern more than once.
	//
	// The condition is a fact about the FIXTURE, and never the name of
	// the grammar. The round-trip filter can only match a type that
	// holds an aggregate or a parametric spelling, and the fixture is
	// what puts such a type in reach: the v2 and current fixtures declare the
	// `agg` and `sagg` columns. An earlier form of this guard read only
	// the OK count and failed a healthy run on the earlier grammar fixture,
	// which held neither column, of 1653 OK cells; that fixture is now
	// removed, but the fix stays: the signature comes from the live
	// server, thus a fixture that loses the columns is still caught
	// here and does not quietly disarm the guard.
	//
	// Both v2 and v3 declare an AggregateFunction column today, so the
	// `!fixtureHoldsAggregateColumn` branch below has no live fixture to
	// take it. It stays as drift protection: a future grammar or a
	// change to schemaDDL selection above that ever picks a fixture
	// WITHOUT the column would hit this exact check, the same one that
	// caught the v1 case, instead of shipping a filter that measures
	// nothing while still reporting a positive count.
	fixtureHoldsAggregateColumn := strings.Contains(fixtureSignature, "AggregateFunction(")
	if fixtureHoldsAggregateColumn && report.Counts["OK"] > 0 && report.RoundTripChecked == 0 {
		t.Fatalf("round_trip_checked is 0 although the run produced %d OK cells and the fixture "+
			"declares an AggregateFunction column; the round-trip filter did not fire on a real "+
			"fuzz run, which is the exact coverage gap that this test must detect", report.Counts["OK"])
	}
	if !fixtureHoldsAggregateColumn && report.RoundTripChecked > 0 {
		t.Fatalf("round_trip_checked is %d although the fixture declares NO AggregateFunction "+
			"column; the filter matched something it cannot reach, thus the check no longer "+
			"measures what it names", report.RoundTripChecked)
	}

	// A round-trip failure here is a NEW, more serious finding than
	// MISMATCH: chgen printed a type STRING that names no legal
	// ClickHouse type at all, so no comparison with the server's answer
	// could ever have caught it (the recorded defect). It is reported, not
	// failed, for the same reason MISMATCH is reported: this harness is a
	// measurement of chgen's current defect surface, not a release gate,
	// and turning a real, live finding into a hard failure would only
	// teach the next contributor to silence the harness instead of
	// reading it. The report.RoundTripFailures list carries the finding
	// forward for anyone who does treat it as a gate.
	if n := len(report.RoundTripFailures); n > 0 {
		t.Logf("round trip is not empty: %d OK-population type strings do not exist. "+
			"Every one is chgen inferring a type with no real referent.", n)
		for _, f := range report.RoundTripFailures {
			t.Logf("  round trip failure: type=%s ch_error=%s", f.TypeName, f.CHErr)
		}
	}

	// Canary check. The run 3 fixes closed the literal canaries, so `1` and
	// `-1` must now be OK (no finding at all). The run 4 fixes also closed
	// the LIKE canary. `empty(s)` used to be the Bool-against-UInt8
	// blindness canary (measured, see TestPredicateFamilyAnswersUInt8EvenOverBool
	// below); now that the predicate class answers UInt8 for chgen too,
	// the pair agrees as text and `empty(s)` must report no finding at
	// all, exactly like the other canaries.
	assertNoFinding := func(expr string) {
		for _, f := range report.Findings {
			if f.Expr == expr {
				t.Errorf("canary %q regressed to %s; it must be OK", expr, f.Class)
			}
		}
	}
	assertNoFinding("1")
	assertNoFinding("-1")
	assertNoFinding("s LIKE '%a%'")
	// `s < s` was the earlier canary, but curated grammar never emits it (the
	// harness skips a repeated SQL string), so the canary was stale there
	// and the v2 run failed on it at the base commit already. `empty(s)`
	// is the predicate-family canary and both grammars produce it, so it
	// holds the check for every grammar.
	assertNoFinding("empty(s)")

	// MISMATCH is a hard signal. A non-zero count names a type that
	// chgen gets WRONG. The run does not fail on it, because the class
	// still holds a small residue of real, previously hidden findings and
	// this harness is a measurement, not a gate. It names them instead, so
	// no reader has to diff signatures by hand to see that the class is
	// not empty.
	if n := report.Counts["MISMATCH"]; n > 0 {
		t.Logf("MISMATCH is not empty: %d findings. Every one is a REAL wrong type.", n)
		for signature, count := range report.MismatchSig {
			t.Logf("  mismatch signature x%d: %s", count, signature)
		}
	}
}

// chErrorFindingCap is the number of CH_ERROR examples that the findings list
// holds. Read the note on oracleReport.Findings: the cap stays on purpose and
// the uncapped CHErrorCodes, CHErrorByKind and CHErrorDigest carry every
// comparison between runs.
const chErrorFindingCap = 200

// clickHouseErrorCode extracts the numeric ClickHouse error code from a server
// message, for example "Code: 47. DB::Exception: ..." gives "47". A message
// with no code gives "unknown".
func clickHouseErrorCode(message string) string {
	const marker = "Code: "
	i := strings.Index(message, marker)
	if i < 0 {
		return "unknown"
	}
	rest := message[i+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return "unknown"
	}
	return rest[:end]
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
