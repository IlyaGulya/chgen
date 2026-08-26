//go:build axisaudit

package engine

// This file audits the PARAMETER axis of a ClickHouse type. A CHType
// carries a Name and, for a parametric type, one or more Params or
// LiteralParams (for example the timezone of DateTime, the scale of
// Decimal, the length of FixedString, the tags of Enum). An inference
// rule that has no opinion about a parameter COPIES it through
// unchanged. When the real server does not copy the same parameter
// through, chgen answers a type whose base kind is right and whose
// parameter is wrong. That is a silently wrong type, the worst class.
//
// The wrapper axis (LowCardinality, Nullable, SimpleAggregateFunction)
// already has a measured grid, wrappergrid_test.go. This file measures
// the axis that grid does not reach: the type PARAMETER, crossed with
// every registry function that carries a genSpec.
//
// The file is TEST ONLY and behind its own build tag, axisaudit, so
// `go test ./...` never runs it and the default test count does not
// change. It reports; it does not fix. A production inference rule
// changes only after this sweep names a defect, in its own change, with
// its own regression test.
//
// The file reuses the v3 draw index (genDrawCandidate, buildDrawIndex,
// renderArgument) from gen_draw_test.go instead of writing a second
// renderer. A second renderer would drift from the one the fuzzer already
// uses. This duplication is a defect in itself.
//
// The classifier compares CHType TREES (classifyAxisCell parses both the
// chgen answer and the server answer with parseCHTypeName), not
// normalized text. A prior version compared text; on the reference run
// (cells_attempted=5312) the two schemes never disagreed on a real cell
// (measured with a throwaway naive tree comparator carrying no alias
// rule at all: exactly the 96 sized-Decimal cells the text normalizer
// already special-cased came back "different" until the Decimal alias
// rule below was added, and zero cells came back different for any
// other reason). So on today's fixture and registry this change is
// hygiene, not a defect fix: it protects against a FUTURE spelling that
// a text normalizer would need its own new rule for, without a report
// telling anyone the rule was missing. See axisTypesEqual for the one
// alias this file's tree comparison knows, and the measured server
// evidence for the equivalences it deliberately does NOT encode.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
)

// --- parameter-axis fixture columns ---

// hasTypeParameter reports whether a column type carries a parameter
// that an inference rule could copy through wrongly: a Params entry (a
// nested type, for example the inner type of DateTime64) or a
// LiteralParams entry (a literal, for example a timezone string, a
// scale, a length or an Enum tag list).
//
// A plain wrapper such as Nullable(Int32) also carries Params, but the
// wrapper axis is already covered by wrappergrid_test.go, thus this
// sweep does not exclude it: a function whose domain happens to accept
// a wrapped column still measures the parameter of the INNER type once
// domainBaseType unwraps it. The point of this predicate is only to
// pick which fixture columns are worth sweeping; it is not the
// classifier.
func hasTypeParameter(t CHType) bool {
	return len(t.Params) > 0 || len(t.LiteralParams) > 0
}

// axisFixtureColumns reads the v3 oracle fixture, the widest one, and
// keeps only the columns whose declared type carries a parameter.
//
// The current fixture is read directly with schemaFromDDLErr, the same
// helper argument_domain_test.go and gen_index_test.go use, so the
// sweep needs no new fixture file.
func axisFixtureColumns(t *testing.T) []fixtureColumn {
	t.Helper()
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the v3 oracle fixture: %v", err)
	}
	var all, parametric []fixtureColumn
	for _, table := range schema.Tables {
		for _, name := range table.ColumnOrder {
			column := table.Columns[name]
			all = append(all, fixtureColumn{name: column.Name, columnType: column.Type})
		}
	}
	for _, column := range all {
		if hasTypeParameter(column.columnType) {
			parametric = append(parametric, column)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
	sort.Slice(parametric, func(i, j int) bool { return parametric[i].name < parametric[j].name })
	if len(parametric) == 0 {
		t.Fatal("no fixture column carries a type parameter; the sweep would measure nothing")
	}
	return parametric
}

// --- classification ---

type axisCellClass string

const (
	classAgree         axisCellClass = "AGREE"
	classParamMismatch axisCellClass = "PARAM_MISMATCH"
	classShapeMismatch axisCellClass = "SHAPE_MISMATCH"
	classChgenRefused  axisCellClass = "CHGEN_REFUSED"
	classServerRefused axisCellClass = "SERVER_REFUSED"
	classBothRefused   axisCellClass = "BOTH_REFUSED"
)

// axisSizedDecimalRe and axisSizedDecimalPrecision give the measured
// precision that ClickHouse assigns to each sized Decimal constructor.
// This is a narrow, local copy of the same measured precision table
// that typeoracle_fuzz_test.go carries under the fuzzoracle tag. It is
// duplicated, not imported, because a file behind one build tag must not
// depend on a symbol that lives behind a DIFFERENT build tag: `go test
// -tags axisaudit` alone would not see it. The table is four lines and
// it is measured (25.8.29.51 canonicalises Decimal32(4), Decimal64(4),
// Decimal128(4), Decimal256(4) to Decimal(9,4), Decimal(18,4),
// Decimal(38,4), Decimal(76,4) respectively), not guessed, so the
// duplication does not create a second unmeasured claim.
var axisSizedDecimalPrecision = map[string]string{
	"Decimal32": "9", "Decimal64": "18", "Decimal128": "38", "Decimal256": "76",
}

// axisCanonicalizeDecimalAlias rewrites a sized Decimal node
// (Decimal32(s), Decimal64(s), ...) to the equivalent Decimal(p, s) tree
// that the server always reports (measured above). A node that is not a
// sized Decimal is returned unchanged. The canonicalisation acts on the
// PARSED TREE, not on the source text, so it cannot be fooled by
// whitespace or by a sized Decimal appearing nested inside another type
// (Array(Decimal32(4)), for example).
func axisCanonicalizeDecimalAlias(t CHType) CHType {
	if precision, ok := axisSizedDecimalPrecision[t.Name]; ok && len(t.LiteralParams) == 1 {
		return CHType{Name: "Decimal", LiteralParams: []string{precision, t.LiteralParams[0]}}
	}
	return t
}

// axisTypesEqual compares two CHType trees for the STRUCTURE the server
// treats as one type, not for spelling. It is the tree-comparison
// replacement for text normalization plus a string `==`.
//
// The only equivalence this function knows is the sized-Decimal alias,
// applied node by node so that it also fires inside a wrapper (Array,
// Tuple, AggregateFunction, ...). Every other apparent equivalence
// considered while building this sweep (Bool vs UInt8, a DateTime64
// timezone that one side prints and the other omits, Nullable/
// LowCardinality nesting order) was measured against the server and
// found to be a REAL distinction, not a spelling accident, so this
// function encodes no rule for any of them:
//
//   - Bool vs UInt8: chgen infers Bool for a comparison result
//     (`toTypeName(1 = 1)`) but the server itself answers UInt8 for the
//     same expression; treating them as equal would hide that as a false
//     AGREE. Bool is also not a parametric type, so this sweep's fixture
//     predicate (hasTypeParameter) never reaches it as a swept column
//     regardless.
//   - DateTime64(3) vs DateTime64(3, 'UTC'): measured on 25.8.29.51 with
//     DESCRIBE against real fixture columns dt64 (declared without a
//     timezone) and dtz64 (declared with an explicit 'UTC'). The server
//     never adds the default timezone to the first column's reported
//     type and never drops the explicit one from the second, so the two
//     spellings stay two different LiteralParams lists and this function
//     reports them unequal, matching the server.
//   - Nullable/LowCardinality nesting: `CAST(1, 'Nullable(LowCardinality(String))')`
//     is refused by the server with code 43 ("Nested type
//     LowCardinality(String) cannot be inside Nullable type"); only
//     LowCardinality(Nullable(X)) is a legal spelling. There is no second
//     legal nesting order for this function to treat as equivalent.
func axisTypesEqual(a, b CHType) bool {
	a = axisCanonicalizeDecimalAlias(a)
	b = axisCanonicalizeDecimalAlias(b)
	if a.Name != b.Name {
		return false
	}
	if len(a.LiteralParams) != len(b.LiteralParams) {
		return false
	}
	for i := range a.LiteralParams {
		if a.LiteralParams[i] != b.LiteralParams[i] {
			return false
		}
	}
	if len(a.Params) != len(b.Params) {
		return false
	}
	for i := range a.Params {
		if !axisTypesEqual(a.Params[i], b.Params[i]) {
			return false
		}
	}
	return true
}

// axisBaseKind gives the base type constructor name of a CHType tree, for
// example "Array" for Array(DateTime) and "DateTime" for DateTime('UTC').
// It reads the parsed Name field directly, never a substring of a raw
// string, so a base-kind decision cannot be confused by a parameter list
// that itself contains a '('.
func axisBaseKind(t CHType) string {
	return t.Name
}

// classifyAxisCell compares a chgen answer and a server answer for one
// cell by parsing both type strings into a CHType TREE and comparing
// structure, not spelling. Exactly one of the four (chgenType/chgenErr,
// serverType/serverErr) pairs is populated on each side.
//
// A parse failure on either side is a defect of THIS SWEEP, not a
// verdict about chgen or the server: both strings came from a real
// answer (chgen's own String() method, or the server's DESCRIBE), so a
// failure to parse one back means the sweep's own parser or renderer has
// a gap. It is reported loudly rather than folded into any of the six
// classes, so it cannot be miscounted as a type-agreement finding.
func classifyAxisCell(chgenType, chgenErr, serverType, serverErr string) axisCellClass {
	switch {
	case chgenErr != "" && serverErr != "":
		return classBothRefused
	case chgenErr != "":
		return classChgenRefused
	case serverErr != "":
		return classServerRefused
	}
	chgenTree, chgenParseErr := conformance.ParseType(chgenType)
	serverTree, serverParseErr := conformance.ParseType(serverType)
	if chgenParseErr != nil || serverParseErr != nil {
		panic(fmt.Sprintf("classifyAxisCell: could not parse a type string as a CHType tree "+
			"(chgen %q -> %v; server %q -> %v); this is a gap in the sweep's own parser, not a "+
			"finding about chgen or the server", chgenType, chgenParseErr, serverType, serverParseErr))
	}
	if chgenTree.Equal(serverTree) {
		return classAgree
	}
	if chgenTree.Name != serverTree.Name {
		return classShapeMismatch
	}
	return classParamMismatch
}

// --- server side: a minimal, self-contained HTTP client ---
//
// This does not reuse chOracle from typeoracle_fuzz_test.go for the same
// reason axisNormalizeTypeName does not reuse normalizeTypeName: chOracle
// lives behind the fuzzoracle tag, and this file lives behind a
// DIFFERENT tag, axisaudit. The two sweeps are never guaranteed to be
// compiled together.

type axisServer struct {
	url      string
	database string
	client   *http.Client
}

func (s *axisServer) execIn(database, query string) (string, error) {
	separator := "/?"
	if strings.Contains(s.url, "?") {
		separator = "&"
	}
	params := url.Values{"default_format": []string{"TabSeparatedRaw"}}
	if database != "" {
		params.Set("database", database)
	}
	resp, err := s.client.Post(s.url+separator+params.Encode(), "text/plain", strings.NewReader(query))
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

func (s *axisServer) exec(query string) (string, error)      { return s.execIn(s.database, query) }
func (s *axisServer) adminExec(query string) (string, error) { return s.execIn("", query) }

// describeType asks the server for the result type of one expression
// over the fixture table t, and ALSO makes the server run that
// expression over the seeded row. The measurement requires a real column,
// never a literal, because ClickHouse folds a
// constant expression at analysis time.
//
// The query shape is the one internal/typeboundary/schema.go (exprToQuery)
// uses: `SELECT toTypeName(e), ignore(e) FROM t`. The first select item
// answers the ANALYSED type; the second forces the expression to RUN.
// The two are read from one round trip, so the type and the execution
// verdict can never come from two different server states.
//
// Why the execution witness is necessary. Analysis alone reports the
// type the server WOULD assign and says nothing about whether the
// expression can run. Measured on 25.8.29.51 over fixture columns:
//
//	toStartOfHour(nd)     DESCRIBE Nullable(DateTime)   execution Code 43
//
// DESCRIBE accepts that expression and names a type, but running it
// over the seeded row is refused. Without the witness such a cell is
// scored against a type the server can never produce: chgen refusing it
// reads as CHGEN_REFUSED, i.e. as a chgen defect, and worse, chgen
// inventing a type for it reads as AGREE.
//
// DESCRIBE is not pure analysis on this version — it does reject some
// illegal expressions itself (equals(d128, dt) is Code 43 and
// toFixedString(fs, 3) is Code 131 at DESCRIBE time) — but it does not
// reject all of them, and the gap above is exactly the class this
// witness closes.
//
// Aggregates need no separate shape. Both select items wrap the SAME
// expression, so a bare column is never exposed beside an aggregate;
// measured, `SELECT toTypeName(sum(i32)), ignore(sum(i32)) FROM t`
// answers Int64. The NOT_AN_AGGREGATE refusal (Code 215) appears only
// when a bare column sits beside an aggregate, which this shape cannot
// produce.
//
// What a SERVER_REFUSED verdict means, and what it does not. The
// witness runs the expression over the ONE seeded fixture row, so a
// refusal can depend on that row's VALUE and not only on its type.
// Measured: toDate(fs) is refused Code 38 because the seeded fs holds
// 'abcdefgh', while the same function over a parseable string answers
// Date. Codes 6, 38, 41 and 407 in this sweep are of that
// value-dependent kind; Code 48 on cityHash64(variant) is static,
// because no value of Variant(Int32, String) can be hashed.
//
// A SERVER_REFUSED cell is therefore evidence that the server cannot
// run THIS call over THIS fixture row. It is not by itself proof that
// chgen's inferred type is wrong, and a reader must not treat it as
// one. What it does add is that such a cell can no longer be scored
// AGREE against a type the server never actually produced.
func (s *axisServer) describeType(exprSQL string) (typeName string, err error) {
	raw, execErr := s.exec(
		"SELECT toTypeName(" + exprSQL + "), ignore(" + exprSQL + ") FROM t")
	if execErr != nil {
		return "", execErr
	}
	fields := strings.Split(raw, "\t")
	if len(fields) < 2 {
		return "", fmt.Errorf("unexpected type-and-witness answer %q", raw)
	}
	return fields[0], nil
}

// describeTypeAnalysisOnly answers with ANALYSIS alone, the way
// describeType did before it gained an execution witness. The sweep
// never scores a cell with this; it exists only so the
// execution-witness self-test can show that the two answers differ,
// which is what makes the witness worth its round trip.
func (s *axisServer) describeTypeAnalysisOnly(exprSQL string) (typeName string, err error) {
	raw, execErr := s.exec("DESCRIBE (SELECT " + exprSQL + " FROM t)")
	if execErr != nil {
		return "", execErr
	}
	fields := strings.Split(raw, "\t")
	if len(fields) < 2 {
		return "", fmt.Errorf("unexpected DESCRIBE answer %q", raw)
	}
	return fields[1], nil
}

// axisExecutionWitnessProbe is the expression the execution-witness
// self-test drives through the live describeType.
//
// It is NOT written from memory of the format. It is a real cell of this
// sweep, measured by hand against 25.8.29.51 over the current fixture:
//
//	DESCRIBE (SELECT toStartOfHour(nd) FROM t)   -> Nullable(DateTime)
//	SELECT toTypeName(toStartOfHour(nd)),
//	       ignore(toStartOfHour(nd)) FROM t      -> Code: 43 ILLEGAL_TYPE_OF_ARGUMENT
//
// nd is the fixture's Nullable(Date) column, so this is an expression
// that ANALYSIS accepts and names a type for, while EXECUTION refuses
// it. That is exactly the class the witness exists to catch.
const axisExecutionWitnessProbe = "toStartOfHour(nd)"

// axisExecutionWitnessWantCode is the error code the probe must produce.
// Asserting the code, and not merely that some error came back, stops a
// self-test that would pass on an unrelated failure such as a dropped
// connection or a missing fixture column.
const axisExecutionWitnessWantCode = "43"

// assertExecutionWitnessLive proves that describeType carries an
// execution witness, by sending a real expression that analysis accepts
// and execution refuses.
//
// This runs against the LIVE server through the same describeType the
// sweep uses, so it cannot pass if the ignore() witness is removed from
// that query shape: with analysis alone the server answers
// Nullable(DateTime) and no error, and this check fails naming exactly
// that. A pure table-driven check over stored strings could not do
// that, because it would never send the query whose shape is the thing
// under test.
func assertExecutionWitnessLive(t *testing.T, server *axisServer) {
	t.Helper()

	// The probe must first be legal under ANALYSIS. If DESCRIBE also
	// refuses it, the probe has stopped being a witness for this class
	// on this server version, and a later "execution refused it" result
	// would prove nothing about the witness. Fail loudly instead of
	// reporting a pass the probe did not earn.
	analysed, analysisErr := server.describeTypeAnalysisOnly(axisExecutionWitnessProbe)
	if analysisErr != nil {
		t.Fatalf("execution-witness self-test: the probe %q is refused by ANALYSIS on this "+
			"server (%v), so it cannot show that the witness adds anything. Re-measure a probe "+
			"that DESCRIBE accepts and execution refuses, and update "+
			"axisExecutionWitnessProbe", axisExecutionWitnessProbe, analysisErr)
	}

	gotType, gotErr := server.describeType(axisExecutionWitnessProbe)
	if gotErr == nil {
		t.Fatalf("execution-witness self-test: describeType(%q) returned type %q and NO error, "+
			"but analysis alone already answers %q for this expression while the server refuses "+
			"to RUN it. This means describeType is reporting an analysis-only answer: the "+
			"ignore() execution witness is missing from its query shape. Every cell of this "+
			"sweep is therefore scored against a type the server can never produce",
			axisExecutionWitnessProbe, gotType, analysed)
	}
	gotCode := axisExtractErrorCode(gotErr.Error())
	if gotCode != axisExecutionWitnessWantCode {
		t.Fatalf("execution-witness self-test: describeType(%q) failed with code %s, want %s "+
			"(analysis answers %q). The probe still fails, but not for the measured reason, so "+
			"this run does not show that the witness works: %v",
			axisExecutionWitnessProbe, gotCode, axisExecutionWitnessWantCode, analysed, gotErr)
	}
	t.Logf("EXECUTION WITNESS PROVEN: %s analyses as %s but execution is refused with Code %s",
		axisExecutionWitnessProbe, analysed, gotCode)
}

var axisErrorCodeRe = regexp.MustCompile(`^Code:\s*(\d+)`)

// axisExtractErrorCode reads the numeric ClickHouse error code from the
// front of a server error message, for example "Code: 43. DB::...".
func axisExtractErrorCode(message string) string {
	match := axisErrorCodeRe.FindStringSubmatch(message)
	if match == nil {
		return "unknown"
	}
	return match[1]
}

func axisReadServerRun(s *axisServer) (int64, int64, error) {
	raw, err := s.adminExec(
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

// --- the sweep itself ---

// axisCellReport is one measured cell: one registry function applied to
// one parametric fixture column at the position the sweep varied.
type axisCellReport struct {
	Function           string           `json:"function"`
	Column             string           `json:"column"`
	ColumnType         string           `json:"column_type"`
	SQL                string           `json:"sql"`
	ChgenType          string           `json:"chgen_type,omitempty"`
	ChgenError         string           `json:"chgen_error,omitempty"`
	ServerType         string           `json:"server_type,omitempty"`
	ServerErr          string           `json:"server_error,omitempty"`
	ServerCode         string           `json:"server_error_code,omitempty"`
	ServerAnalysisType string           `json:"server_analysis_type,omitempty"`
	ServerAnalysisErr  string           `json:"server_analysis_error,omitempty"`
	ExecutionRan       bool             `json:"execution_ran"`
	ExecutionError     string           `json:"execution_error,omitempty"`
	Conformance        conformance.Cell `json:"conformance"`
	Class              axisCellClass    `json:"class"`
}

func axisMeasureServer(cell *axisCellReport, server *axisServer) {
	shared := conformance.NewHTTPServer(server.url, server.database)
	shared.Client = server.client
	input := conformance.Input{ID: cell.SQL, Expression: cell.SQL, Table: "t"}
	measured, err := (conformance.Runner{
		Server: shared,
		Infer: func(conformance.Input) (string, error) {
			if cell.ChgenError != "" {
				return "", errors.New(cell.ChgenError)
			}
			return cell.ChgenType, nil
		},
	}).Measure(context.Background(), input)
	if err != nil {
		panic(fmt.Sprintf("axis conformance runner: %v", err))
	}
	cell.Conformance = measured
	cell.ServerAnalysisType = measured.Analysis.Raw
	cell.ServerAnalysisErr = measured.Analysis.Error
	cell.ExecutionRan = measured.Execution.Ran
	cell.ExecutionError = measured.Execution.Error
	if measured.Execution.Error != "" {
		cell.ServerErr = measured.Execution.Error
		cell.ServerCode = strconv.Itoa(measured.Execution.ErrorCode)
		return
	}
	if measured.Analysis.Error != "" {
		cell.ServerErr = measured.Analysis.Error
		cell.ServerCode = strconv.Itoa(measured.Analysis.ErrorCode)
		return
	}
	cell.ServerType = measured.Analysis.Raw
}

type axisReport struct {
	Date             string           `json:"date"`
	CHVersion        string           `json:"clickhouse_version"`
	ServerRun        int64            `json:"server_run"`
	ServerUptimeS    int64            `json:"server_uptime_s"`
	FixtureSignature string           `json:"fixture_signature"`
	CellsAttempted   int              `json:"cells_attempted"`
	ClosureCells     int              `json:"closure_cells"`
	Counts           map[string]int   `json:"counts"`
	SelfTestPassed   bool             `json:"self_test_passed"`
	SelfTestDetail   string           `json:"self_test_detail"`
	Cells            []axisCellReport `json:"cells"`
	Skipped          []string         `json:"skipped,omitempty"`
	CellsDigest      string           `json:"cells_digest"`
}

// axisClosureColumnsAtPosition gives every fixture column that a value
// position could ever hold, legal and illegal together.
//
// The sweep MUST NOT ask candidate.pools[position].legal for the column
// list, because that pool is the very domain predicate the sweep audits.
// A domain fix that narrows the legal pool would then also narrow the
// cell list, and the cell that would show the excess of a too-wide rule
// disappears instead of turning into a report. legal and illegal
// partition the full fixture (proved by TestDrawPoolsPartitionTheFixture
// in gen_draw_test.go), so their union is the closure this sweep needs:
// the domain decides the VERDICT of a cell, through chgenInferTypeForAxis
// below, never whether the cell is measured at all.
func axisClosureColumnsAtPosition(candidate genDrawCandidate, position int) []fixtureColumn {
	if position >= len(candidate.pools) {
		return nil
	}
	pool := candidate.pools[position]
	all := make([]fixtureColumn, 0, len(pool.legal)+len(pool.illegal))
	all = append(all, pool.legal...)
	all = append(all, pool.illegal...)
	return all
}

// axisRenderCall writes a call to the candidate with the given fixture
// column bound to the varied position. Every other value position takes
// the FIRST column of its own legal pool, exactly the way
// candidateArgTypes (gen_index_test.go) samples a rule: the sweep asks
// whether THIS parameter, at THIS position, changes the answer, not
// whether every combination of positions does.
//
// This is a new, small renderer rather than a call to
// genDrawCandidate.renderCall, because renderCall always draws its
// column RANDOMLY from the pool. An exhaustive sweep needs the varied
// position pinned to one exact column, which the random draw cannot
// give without a second hidden mechanism to force the outcome. Every
// other position, and the non-value argument sorts, still go through
// c.renderArgument, the same method the fuzzer's renderCall uses, so
// the only new code here is the one line that pins the varied position.
func axisRenderCall(c genDrawCandidate, variedPosition int, variedColumn fixtureColumn) (string, bool) {
	arity := c.spec.minArity
	arguments := make([]string, 0, arity)
	dummyRand := axisFixedRand()
	for position := 0; position < arity && position < len(c.spec.argSorts); position++ {
		if position == variedPosition {
			arguments = append(arguments, variedColumn.name)
			continue
		}
		if c.spec.argSorts[position] == argSortValue {
			if position >= len(c.pools) || len(c.pools[position].legal) == 0 {
				return "", false
			}
			arguments = append(arguments, c.pools[position].legal[0].name)
			continue
		}
		argument, ok := c.renderArgument(dummyRand, position, false)
		if !ok {
			return "", false
		}
		arguments = append(arguments, argument)
	}
	if len(arguments) != arity {
		return "", false
	}
	if c.spec.template != "" {
		return fmt.Sprintf(c.spec.template, toAny(arguments)...), true
	}
	return c.spec.spelling + "(" + strings.Join(arguments, ", ") + ")", true
}

// axisFixedRand gives a seeded, reproducible source for the non-value
// argument sorts (a predicate, a constant, a lambda body). The sweep is
// exhaustive over columns and functions, not over these fillers, so one
// fixed seed keeps a rerun byte-identical without adding a second
// randomness axis to reason about.
func axisFixedRand() *rand.Rand { return rand.New(rand.NewSource(1)) }

// wrapInAggregateContextIfNeeded returns the SQL to hand to DESCRIBE. An
// aggregate call is legal bare at the top level of a SELECT with no
// GROUP BY, exactly like the fuzzoracle harness's plain
// `SELECT <expr> FROM t`; a window call needs an OVER clause, which this
// sweep does not add, thus placementWindow candidates are skipped and
// counted under Skipped.
func axisSelectSQL(call string) string { return call }

// TestAxisParameterSweep is the audit itself. Set CHGEN_AXIS_URL to run
// it; it Skips otherwise, the same convention TestTypeOracle uses for
// CHGEN_ORACLE_URL.
func TestAxisParameterSweep(t *testing.T) {
	baseURL := os.Getenv("CHGEN_AXIS_URL")
	if baseURL == "" {
		t.Skip("CHGEN_AXIS_URL is not set; point it at a disposable ClickHouse to run the axis sweep")
	}

	server := &axisServer{url: baseURL, client: &http.Client{Timeout: 60 * time.Second}}
	version, err := server.adminExec("SELECT version()")
	if err != nil {
		t.Fatalf("ClickHouse is not reachable: %v", err)
	}
	t.Logf("clickhouse version()=%s", version)

	serverRun, serverUptime, err := axisReadServerRun(server)
	if err != nil {
		t.Fatalf("read the server run identity: %v", err)
	}
	t.Logf("server_run=%d server_uptime_s=%d", serverRun, serverUptime)

	runDatabase := os.Getenv("CHGEN_AXIS_DATABASE")
	if runDatabase == "" {
		runDatabase = fmt.Sprintf("probe_axis_%d_%d", os.Getpid(), time.Now().UnixNano())
	}
	if !strings.HasPrefix(runDatabase, "probe") {
		t.Fatalf("CHGEN_AXIS_DATABASE=%q must start with \"probe\"", runDatabase)
	}
	if _, err := server.adminExec("CREATE DATABASE " + runDatabase); err != nil {
		t.Fatalf("create run database %q: %v", runDatabase, err)
	}
	server.database = runDatabase
	t.Logf("run database=%s", runDatabase)
	t.Cleanup(func() {
		if _, err := server.adminExec("DROP DATABASE IF EXISTS " + runDatabase); err != nil {
			t.Logf("drop run database %q: %v", runDatabase, err)
		}
	})

	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the v3 oracle fixture: %v", err)
	}
	if _, err := server.exec(oracleSchemaDDL); err != nil {
		t.Fatalf("create the fixture table: %v", err)
	}
	if _, err := server.exec(oracleSeedRow); err != nil {
		t.Fatalf("seed the fixture row: %v", err)
	}
	signature, err := readFixtureSignatureForAxis(server)
	if err != nil {
		t.Fatalf("read fixture signature: %v", err)
	}
	t.Logf("fixture_signature=%s", signature)

	allColumns := axisFixtureColumns(t)
	t.Logf("parametric fixture columns: %d", len(allColumns))
	for _, column := range allColumns {
		t.Logf("  %s %s", column.name, column.columnType.String())
	}

	index := buildDrawIndex(allColumnsWithNonParametric(t))

	var names []string
	for name := range index {
		names = append(names, name)
	}
	sort.Strings(names)

	report := axisReport{
		Date:             time.Now().UTC().Format(time.RFC3339),
		CHVersion:        version,
		ServerRun:        serverRun,
		ServerUptimeS:    serverUptime,
		FixtureSignature: signature,
		Counts:           make(map[string]int),
	}

	windowSkipCount := 0
	for _, name := range names {
		candidate := index[name]
		if candidate.spec.place == placementWindow {
			report.Skipped = append(report.Skipped,
				fmt.Sprintf("%s: window placement, no OVER clause in this sweep", name))
			windowSkipCount++
			continue
		}
		if !candidate.writable() {
			// A candidate with no legal fill at some OTHER position cannot
			// be rendered at all, on any fixture column. This is a real
			// generator limit, not a domain narrowing, so every parametric
			// column this candidate could have swept is named and counted
			// as SKIPPED instead of dropped silently.
			for position, argSortAtPosition := range candidate.spec.argSorts {
				if position >= candidate.spec.minArity || argSortAtPosition != argSortValue {
					continue
				}
				for _, column := range axisClosureColumnsAtPosition(candidate, position) {
					if !hasTypeParameter(column.columnType) {
						continue
					}
					report.Skipped = append(report.Skipped, fmt.Sprintf(
						"%s @ position %d column %s: candidate not writable over the current fixture "+
							"(some other value position has no legal column at all)", name, position, column.name))
					report.ClosureCells++
				}
			}
			continue
		}
		for position, argSortAtPosition := range candidate.spec.argSorts {
			if position >= candidate.spec.minArity {
				break
			}
			if argSortAtPosition != argSortValue {
				continue
			}
			if position >= len(candidate.pools) {
				continue
			}
			for _, column := range axisClosureColumnsAtPosition(candidate, position) {
				if !hasTypeParameter(column.columnType) {
					continue
				}
				report.ClosureCells++
				call, ok := axisRenderCall(candidate, position, column)
				if !ok {
					reason := "domain-refused column, or another position has no legal fill"
					if candidate.spec.minArity > len(candidate.spec.argSorts) {
						reason = fmt.Sprintf("variadic call needs %d arguments but the recipe "+
							"names only %d argument sorts; the sweep's fixed-position renderer "+
							"cannot fill the rest", candidate.spec.minArity, len(candidate.spec.argSorts))
					}
					report.Skipped = append(report.Skipped, fmt.Sprintf(
						"%s @ position %d column %s: could not render a call (%s)",
						name, position, column.name, reason))
					continue
				}
				sql := axisSelectSQL(call)
				cell := axisCellReport{
					Function:   candidate.spec.spelling,
					Column:     column.name,
					ColumnType: column.columnType.String(),
					SQL:        sql,
				}
				chgenType, chgenErr := chgenInferTypeForAxis(schema, sql)
				if chgenErr != nil {
					cell.ChgenError = chgenErr.Error()
				} else {
					cell.ChgenType = chgenType
				}
				axisMeasureServer(&cell, server)
				cell.Class = classifyAxisCell(cell.ChgenType, cell.ChgenError, cell.ServerType, cell.ServerErr)
				report.Cells = append(report.Cells, cell)
				report.Counts[string(cell.Class)]++
			}
		}
	}

	report.CellsAttempted = len(report.Cells)

	// --- the trustworthiness guards ---
	//
	// A sweep that never ran a cell must not be able to read as a clean
	// report. Both guards below refuse loudly rather than writing a
	// report that a reader could mistake for full, defect-free coverage.
	if report.CellsAttempted == 0 {
		t.Fatal("the sweep attempted zero cells; a report of zero PARAM_MISMATCH here would be " +
			"indistinguishable from a sweep that never ran, thus this is a hard failure and not a " +
			"silent empty report")
	}
	sumOfCounts := 0
	for _, count := range report.Counts {
		sumOfCounts += count
	}
	if sumOfCounts != report.CellsAttempted {
		t.Fatalf("class counts sum to %d but %d cells were attempted; every cell must land in "+
			"exactly one class, thus this mismatch means the classifier or the counter is broken",
			sumOfCounts, report.CellsAttempted)
	}

	// --- the coverage gate ---
	//
	// ClosureCells is the number of (function, position, column) triples
	// that the closure of the fixture and the registry implies for a
	// parametric column, computed IN THIS RUN from buildDrawIndex, before
	// any domain predicate decides a verdict. Every one of those triples
	// must land in EXACTLY ONE of report.Cells (a measured verdict) or
	// report.Skipped (a named, counted reason the call could not be
	// rendered at all, for example an arity the generator cannot fill).
	//
	// The comparison is against THIS run's closure, never against a
	// number a previous run wrote down. A stored number can be
	// regenerated and overwritten; the closure is recomputed every time,
	// so a rule that quietly removes cells from the pool it feeds the
	// sweep cannot hide by shrinking both sides together: the closure
	// counts the FULL fixture, legal and illegal columns alike, so a
	// domain narrowing changes only the VERDICT of a cell, never whether
	// the cell is counted.
	skippedForClosure := len(report.Skipped) - windowSkipCount
	measuredForClosure := report.CellsAttempted + skippedForClosure
	if measuredForClosure != report.ClosureCells {
		declared := os.Getenv("CHGEN_AXIS_ALLOW_COVERAGE_SHORTFALL")
		if declared == "" {
			t.Fatalf("coverage gate: the closure of the fixture and the registry implies %d "+
				"parametric cells, but only %d were measured (cells_attempted=%d, skipped=%d); "+
				"%d cells were lost without a declared reason. A lost cell is worse than a wrong "+
				"answer, because nothing reports it. If the shortfall is expected, set "+
				"CHGEN_AXIS_ALLOW_COVERAGE_SHORTFALL with a reason naming which cells and why",
				report.ClosureCells, measuredForClosure, report.CellsAttempted, skippedForClosure,
				report.ClosureCells-measuredForClosure)
		}
		t.Logf("coverage gate: shortfall of %d cells declared: %s",
			report.ClosureCells-measuredForClosure, declared)
	}

	// --- the self-test: the sweep must be able to SEE a defect ---
	//
	// The self-test injects an artificial disagreement on each parameter
	// axis and asserts that the classifier names it. It does NOT anchor
	// on a live defect of chgen.
	//
	// An earlier version of this self-test did anchor on one: it required
	// the cell groupUniqArray(dtz) to classify as PARAM_MISMATCH, because
	// that was the measured defect this sweep was built to find. When the
	// defect was fixed (the regression), the two sides agreed, the cell
	// became a correct AGREE, and the self-test failed although the sweep
	// was healthy. An anchor that a FIX can disarm cannot show that the
	// instrument works, thus the anchor below is an injected difference
	// that no fix of chgen can remove.
	selfTestCases := []struct {
		axis                  string
		chgenType, serverType string
		want                  axisCellClass
	}{
		{"timezone", "Array(DateTime('UTC'))", "Array(DateTime)", classParamMismatch},
		{"FixedString length", "FixedString(8)", "FixedString(16)", classParamMismatch},
		{"Decimal scale", "Decimal(18, 4)", "Decimal(18, 2)", classParamMismatch},
		{"DateTime64 precision", "DateTime64(3)", "DateTime64(6)", classParamMismatch},
		{"base kind", "Array(Int32)", "Int32", classShapeMismatch},
		{"no difference", "Array(DateTime)", "Array(DateTime)", classAgree},
		// This case is the proof that the classifier compares TREES and
		// not spelling. The retired text comparator (axisNormalizeTypeName)
		// removed every space before comparing strings, so it could not
		// tell a space INSIDE a quoted literal from a space that was only
		// formatting. Enum8('a b' = 1) and Enum8('ab' = 1) are two
		// different Enum tags, "a b" and "ab", but blind space-stripping
		// collapses both to the identical string Enum8('ab'=1) — the old
		// scheme would have called this a false AGREE. A tree comparison
		// parses the quoted literal as one LiteralParams entry and never
		// touches the characters inside it, so it reports the two tags as
		// unequal. This is a case text comparison gets WRONG and tree
		// comparison gets RIGHT, not a case that both already handled.
		{"quoted literal holds a real space", `Enum8('a b' = 1)`, `Enum8('ab' = 1)`, classParamMismatch},
	}
	for _, selfTest := range selfTestCases {
		got := classifyAxisCell(selfTest.chgenType, "", selfTest.serverType, "")
		if got != selfTest.want {
			t.Fatalf("self-test on the %s axis: classifyAxisCell(%q, %q) = %s, want %s; the "+
				"classifier cannot name an injected difference, thus a report of zero "+
				"PARAM_MISMATCH from this run would prove nothing",
				selfTest.axis, selfTest.chgenType, selfTest.serverType, got, selfTest.want)
		}
	}
	assertExecutionWitnessLive(t, server)

	report.SelfTestPassed = true
	report.SelfTestDetail = fmt.Sprintf("classifier named an injected difference on %d axes, "+
		"and the execution witness rejected an expression that analysis accepts",
		len(selfTestCases))
	t.Logf("SELF-TEST PASSED: %s", report.SelfTestDetail)

	report.CellsDigest = axisCellsDigest(report.Cells)

	t.Logf("cells_attempted=%d counts=%v skipped=%d", report.CellsAttempted, report.Counts, len(report.Skipped))
	for class, count := range report.Counts {
		t.Logf("  %s: %d", class, count)
	}

	outPath := os.Getenv("CHGEN_AXIS_OUT")
	if outPath != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			t.Fatalf("write report to %q: %v", outPath, err)
		}
		t.Logf("report written to %s", outPath)
	}
}

// axisCellsDigest is a stable hash over every cell, so that two runs can
// be compared for an exact match without diffing the whole list by eye.
func axisCellsDigest(cells []axisCellReport) string {
	lines := make([]string, 0, len(cells))
	for _, cell := range cells {
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%s|%s", cell.Function, cell.Column, cell.Class,
			cell.ChgenType+cell.ChgenError, cell.ServerType+cell.ServerErr))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// chgenInferTypeForAxis mirrors chgenInferType (typeoracle_fuzz_test.go,
// behind fuzzoracle): it parses "SELECT <expr> FROM t" and infers the
// type of the select item. It is duplicated here, not imported, for the
// same cross-tag reason as axisNormalizeTypeName: this file cannot
// depend on a symbol that only exists under a different build tag. It
// is NOT inferTestExprType (argument_domain_test.go), which selects
// FROM probe, a different fixture table.
func chgenInferTypeForAxis(schema *Schema, exprSQL string) (string, error) {
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

// readFixtureSignatureForAxis mirrors readFixtureSignature
// (typeoracle_fuzz_test.go, fuzzoracle) for the same cross-tag reason.
func readFixtureSignatureForAxis(s *axisServer) (string, error) {
	columns, err := s.exec(
		"SELECT arrayStringConcat(groupArray(concat(name, ':', type)), ',') " +
			"FROM (SELECT name, type FROM system.columns " +
			"WHERE database = currentDatabase() AND table = 't' ORDER BY position)")
	if err != nil {
		return "", fmt.Errorf("read columns: %w", err)
	}
	if strings.TrimSpace(columns) == "" {
		return "", fmt.Errorf("table `t` has no columns in database %q", s.database)
	}
	return strings.TrimSpace(columns), nil
}

// allColumnsWithNonParametric returns every current fixture column, not only
// the parametric ones. The draw index needs the FULL fixture, because a
// candidate's non-varied positions must fill from the whole legal pool,
// exactly like the fuzzer's own buildDrawIndex(oracleFixtureColumns(t))
// call in TestDrawIndexReachesCurrentFixture.
func allColumnsWithNonParametric(t *testing.T) []fixtureColumn {
	t.Helper()
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the v3 oracle fixture: %v", err)
	}
	var columns []fixtureColumn
	for _, table := range schema.Tables {
		for _, name := range table.ColumnOrder {
			column := table.Columns[name]
			columns = append(columns, fixtureColumn{name: column.Name, columnType: column.Type})
		}
	}
	return columns
}

// === the ARITY axis ===
//
// The parameter axis above varies ONE argument position and holds every
// other position fixed. This section adds a second, INDEPENDENT axis:
// for a rule that takes a variable number of branches (multiIf, if,
// greatest, least, array, map, coalesce), it sweeps the BRANCH COUNT (2,
// 3, 4) and, for a fixed branch count, every PERMUTATION of one measured
// branch-type multiset. The server answer must be compared PER ORDER,
// because the regression measured that some of these rules are order
// dependent on the server (a mixed-sign integer supertype fold) and the
// project's own control test (TestSignedUInt64PairAloneStaysRefused)
// shows a case that is NOT: a refusal that must hold regardless of
// order.
//
// This axis reuses the parameter axis's report struct, classifier,
// server client and digest function. It does NOT reuse
// genDrawCandidate/buildDrawIndex, because "if", "multiIf" and
// "coalesce" carry no genSpec at all (they are routed to
// inferConditionalFamilyType by name, before the registry lookup runs;
// see inferFunctionType in infer_function.go) and thus never appear in
// the draw index that machinery walks. A rule with no genSpec cannot be
// reached by a machine built to walk genSpecs, so this axis renders its
// own calls directly from named fixture columns, the same columns the
// parameter axis already reads from the current fixture.

// axisArityBranchSet is one named, measured multiset of branch columns
// to sweep at one arity. The set is chosen to be either: (a) a case
// measured to be a REAL order-dependence defect class on the server
// (mixed-sign integer supertype, the regression's own class), so the
// self-test below has a real injected witness to check against, or (b)
// a plain heterogeneous set (numeric/string/nullable) that the parameter
// axis's one-argument sweep can never reach because it never varies more
// than one position.
type axisArityBranchSet struct {
	// label names the set for the report and the skip log.
	label string
	// columns is the branch multiset, named by fixture column. Its
	// length is the arity swept (2, 3, or 4 elements).
	columns []string
}

// axisArityBranchSets gives every branch multiset this axis sweeps, at
// arity 2, 3 and 4. All columns named here are real current fixture columns
// (oracleSchemaDDL), never literals, per the project's own measurement
// discipline: ClickHouse folds a constant at analysis time and a literal
// probe would not exercise the runtime branch-typing path at all.
func axisArityBranchSets() []axisArityBranchSet {
	return []axisArityBranchSet{
		// Arity 2: the CONTROL. A signed integer with UInt64 and no wide
		// branch has no supertype on the server, in EITHER order. This is
		// TestSignedUInt64PairAloneStaysRefused's own pair, so this axis
		// must report BOTH orders as classServerRefused (or
		// classBothRefused if chgen also refuses), never classAgree with
		// a manufactured type on one side only.
		{label: "signed+UInt64 (control, no supertype)", columns: []string{"i32", "u64"}},
		// Arity 2: a plain heterogeneous pair the one-argument sweep can
		// never reach (it holds every OTHER position fixed at one column,
		// so it never varies a second position at the same time).
		{label: "String+Float64", columns: []string{"s", "f64"}},
		{label: "Nullable(Int32)+String", columns: []string{"ni32", "s"}},

		// Arity 3: the regression's own measured triples. A signed integer,
		// UInt64 and a wide signed integer give a supertype that swallows
		// all three; the server was measured order-independent on every
		// permutation (nary_mixed_sign_supertype_test.go). This is the
		// class the parameter axis was blind to (zero cells named 3+
		// arguments).
		{label: "signed+UInt64+Int128 (kpb6 order-independent triple)", columns: []string{"i32", "u64", "i128"}},
		{label: "signed+UInt64+UInt128 (kpb6 order-independent triple)", columns: []string{"i32", "u64", "u128"}},
		// Arity 3: the wide-UNSIGNED-peer refusal that must survive any
		// fix, in every order (TestWideUnsignedPeerStillRefused).
		{label: "signed+UInt64+UInt256 (control, no supertype)", columns: []string{"i32", "u64", "u256"}},
		// Arity 3: a plain heterogeneous triple.
		{label: "Int32+String+Float64", columns: []string{"i32", "s", "f64"}},

		// Arity 4: the same mixed-sign class one branch wider
		// (TestFourAndFiveBranchWideIntegerSupertype measured the defect
		// GROWS with arity), plus one plain heterogeneous quadruple.
		{label: "signed+Int64+UInt64+Int128 (kpb6 four-branch)", columns: []string{"i32", "i64", "u64", "i128"}},
		{label: "Int32+UInt64+String+Float64", columns: []string{"i32", "u64", "s", "f64"}},
	}
}

// axisArityRules names, for each rule this axis sweeps, how to render one
// call from an ordered branch-column list, and whether the server is
// documented order-dependent for that rule at all. rendersOrderSensitive
// is informational only (it is not read by the classifier, which always
// compares the actual measured answer per order); it exists so the
// report can flag a rule this axis expects to disagree across orders,
// without hard-coding that expectation into the pass/fail verdict.
type axisArityRule struct {
	// name is the rule's spelling, used in the report and the SQL.
	name string
	// minArity is the fewest branches this rule accepts. array and
	// coalesce accept 1, but arity 1 carries no order to permute, so
	// this axis starts every rule at 2 regardless of minArity.
	minArity int
	// render writes the full call from an ordered list of column names.
	render func(name string, columns []string) string
}

func axisArityRules() []axisArityRule {
	return []axisArityRule{
		{name: "greatest", minArity: 1, render: func(name string, columns []string) string {
			return name + "(" + strings.Join(columns, ", ") + ")"
		}},
		{name: "least", minArity: 1, render: func(name string, columns []string) string {
			return name + "(" + strings.Join(columns, ", ") + ")"
		}},
		{name: "array", minArity: 1, render: func(name string, columns []string) string {
			return name + "(" + strings.Join(columns, ", ") + ")"
		}},
		{name: "map", minArity: 2, render: func(name string, columns []string) string {
			// map(k1, v1, k2, v2, ...) needs a key next to every value.
			// The branch multiset under sweep names VALUE columns; "b"
			// (UInt8) is a legal key for every value type this axis
			// draws, and it is fixed across every permutation, so the
			// KEY position never introduces a second, unmeasured source
			// of order variation into a cell meant to isolate the VALUE
			// order.
			var parts []string
			for _, column := range columns {
				parts = append(parts, "b", column)
			}
			return name + "(" + strings.Join(parts, ", ") + ")"
		}},
		{name: "coalesce", minArity: 1, render: func(name string, columns []string) string {
			return name + "(" + strings.Join(columns, ", ") + ")"
		}},
		{name: "if", minArity: 2, render: func(name string, columns []string) string {
			// if(cond, then, else) takes exactly two branches. This axis
			// still sweeps BOTH orders at arity 2 (columns has length 2
			// here; a branch set with more than 2 columns is skipped for
			// "if" by the caller, since the function itself refuses a
			// third branch).
			return name + "(b, " + strings.Join(columns, ", ") + ")"
		}},
		{name: "multiIf", minArity: 3, render: func(name string, columns []string) string {
			// multiIf(cond1, val1, cond2, val2, ..., valN) needs one
			// condition per value but the last. "b" (UInt8) is a legal
			// condition for every position, fixed across every
			// permutation, for the same isolation reason as map's key.
			// Every value but the LAST is preceded by its own "b"
			// condition; the last value stands alone.
			var parts []string
			for i, column := range columns {
				if i < len(columns)-1 {
					parts = append(parts, "b")
				}
				parts = append(parts, column)
			}
			return name + "(" + strings.Join(parts, ", ") + ")"
		}},
	}
}

// axisPermuteColumns returns every permutation of a short column-name
// slice (length 2, 3 or 4 in this sweep). The combinatorics of a full
// permutation sweep grow factorially (2!=2, 3!=6, 4!=24), so this axis
// deliberately bounds itself to branch sets of at most 4 columns; a
// wider branch set is not attempted at all and is not silently possible
// to add without updating this comment and the bound check at the call
// site.
func axisPermuteColumns(columns []string) [][]string {
	if len(columns) == 0 {
		return [][]string{{}}
	}
	var result [][]string
	for i := range columns {
		rest := make([]string, 0, len(columns)-1)
		rest = append(rest, columns[:i]...)
		rest = append(rest, columns[i+1:]...)
		for _, sub := range axisPermuteColumns(rest) {
			perm := make([]string, 0, len(columns))
			perm = append(perm, columns[i])
			perm = append(perm, sub...)
			result = append(result, perm)
		}
	}
	return result
}

// axisArityMaxColumns is the sweep's declared bound on branch-set size.
// 4! = 24 permutations per (rule, set) pair; at 8 branch sets and up to 7
// applicable rules each, the arity axis stays under a few hundred cells,
// small enough to run against a live server in the same test run as the
// parameter axis. A branch set wider than this bound is logged under
// Skipped, never attempted and never silently dropped.
const axisArityMaxColumns = 4

// TestAxisAritySweep is the arity/permutation audit. Like
// TestAxisParameterSweep, it Skips unless CHGEN_AXIS_URL is set, and it
// reuses the exact same server, schema and fixture that test creates its
// own copy of, so it can also run standalone.
func TestAxisAritySweep(t *testing.T) {
	baseURL := os.Getenv("CHGEN_AXIS_URL")
	if baseURL == "" {
		t.Skip("CHGEN_AXIS_URL is not set; point it at a disposable ClickHouse to run the arity sweep")
	}

	server := &axisServer{url: baseURL, client: &http.Client{Timeout: 60 * time.Second}}
	version, err := server.adminExec("SELECT version()")
	if err != nil {
		t.Fatalf("ClickHouse is not reachable: %v", err)
	}
	t.Logf("clickhouse version()=%s", version)

	serverRun, serverUptime, err := axisReadServerRun(server)
	if err != nil {
		t.Fatalf("read the server run identity: %v", err)
	}

	runDatabase := os.Getenv("CHGEN_AXIS_ARITY_DATABASE")
	if runDatabase == "" {
		runDatabase = fmt.Sprintf("probe_arity_%d_%d", os.Getpid(), time.Now().UnixNano())
	}
	if !strings.HasPrefix(runDatabase, "probe") {
		t.Fatalf("CHGEN_AXIS_ARITY_DATABASE=%q must start with \"probe\"", runDatabase)
	}
	if _, err := server.adminExec("CREATE DATABASE " + runDatabase); err != nil {
		t.Fatalf("create run database %q: %v", runDatabase, err)
	}
	server.database = runDatabase
	t.Logf("run database=%s", runDatabase)
	t.Cleanup(func() {
		if _, err := server.adminExec("DROP DATABASE IF EXISTS " + runDatabase); err != nil {
			t.Logf("drop run database %q: %v", runDatabase, err)
		}
	})

	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the v3 oracle fixture: %v", err)
	}
	if _, err := server.exec(oracleSchemaDDL); err != nil {
		t.Fatalf("create the fixture table: %v", err)
	}
	if _, err := server.exec(oracleSeedRow); err != nil {
		t.Fatalf("seed the fixture row: %v", err)
	}
	signature, err := readFixtureSignatureForAxis(server)
	if err != nil {
		t.Fatalf("read fixture signature: %v", err)
	}

	report := axisReport{
		Date:             time.Now().UTC().Format(time.RFC3339),
		CHVersion:        version,
		ServerRun:        serverRun,
		ServerUptimeS:    serverUptime,
		FixtureSignature: signature,
		Counts:           make(map[string]int),
	}

	rules := axisArityRules()
	branchSets := axisArityBranchSets()

	for _, rule := range rules {
		for _, set := range branchSets {
			if len(set.columns) > axisArityMaxColumns {
				report.Skipped = append(report.Skipped, fmt.Sprintf(
					"%s over %s: branch set has %d columns, more than the declared bound of %d; "+
						"not attempted", rule.name, set.label, len(set.columns), axisArityMaxColumns))
				continue
			}
			if len(set.columns) < 2 {
				report.Skipped = append(report.Skipped,
					fmt.Sprintf("%s over %s: fewer than 2 branches, no order to permute", rule.name, set.label))
				continue
			}
			if rule.name == "if" && len(set.columns) != 2 {
				report.Skipped = append(report.Skipped, fmt.Sprintf(
					"%s over %s: if() takes exactly two branches, this set has %d", rule.name, set.label, len(set.columns)))
				continue
			}
			if len(set.columns) < rule.minArity {
				report.Skipped = append(report.Skipped, fmt.Sprintf(
					"%s over %s: %d branches is below %s's minimum arity %d",
					rule.name, set.label, len(set.columns), rule.name, rule.minArity))
				continue
			}
			for _, perm := range axisPermuteColumns(set.columns) {
				call := rule.render(rule.name, perm)
				sql := axisSelectSQL(call)
				cell := axisCellReport{
					Function:   rule.name,
					Column:     set.label + " order=" + strings.Join(perm, ","),
					ColumnType: fmt.Sprintf("arity=%d", len(perm)),
					SQL:        sql,
				}
				chgenType, chgenErr := chgenInferTypeForAxis(schema, sql)
				if chgenErr != nil {
					cell.ChgenError = chgenErr.Error()
				} else {
					cell.ChgenType = chgenType
				}
				axisMeasureServer(&cell, server)
				cell.Class = classifyAxisCell(cell.ChgenType, cell.ChgenError, cell.ServerType, cell.ServerErr)
				report.Cells = append(report.Cells, cell)
				report.Counts[string(cell.Class)]++
			}
		}
	}

	report.CellsAttempted = len(report.Cells)
	if report.CellsAttempted == 0 {
		t.Fatal("the arity sweep attempted zero cells; that would be indistinguishable from a sweep " +
			"that never ran, thus this is a hard failure and not a silent empty report")
	}
	sumOfCounts := 0
	for _, count := range report.Counts {
		sumOfCounts += count
	}
	if sumOfCounts != report.CellsAttempted {
		t.Fatalf("class counts sum to %d but %d cells were attempted", sumOfCounts, report.CellsAttempted)
	}

	// --- per-order-group divergence: does the server itself disagree
	// across the permutations of one branch set for one rule? ---
	//
	// This is the measurement the brief asks for directly: "the server
	// answer must be compared per order, because for some rules the
	// server IS order dependent and for others it is not." The loop
	// below groups the cells this run just produced back by (rule, set)
	// and reports, for each group, whether every permutation's SERVER
	// answer agreed.
	type groupKey struct{ rule, set string }
	serverAnswersByGroup := map[groupKey]map[string]bool{}
	for _, rule := range rules {
		for _, set := range branchSets {
			// Re-walk the same skip predicate as the render loop above so
			// this grouping only looks at cells that were actually
			// attempted.
			if len(set.columns) > axisArityMaxColumns || len(set.columns) < 2 {
				continue
			}
			if rule.name == "if" && len(set.columns) != 2 {
				continue
			}
			if len(set.columns) < rule.minArity {
				continue
			}
			key := groupKey{rule.name, set.label}
			if serverAnswersByGroup[key] == nil {
				serverAnswersByGroup[key] = map[string]bool{}
			}
		}
	}
	for _, cell := range report.Cells {
		spaceIdx := strings.Index(cell.Column, " order=")
		if spaceIdx < 0 {
			continue
		}
		setLabel := cell.Column[:spaceIdx]
		key := groupKey{cell.Function, setLabel}
		answer := cell.ServerType
		if cell.ServerErr != "" {
			answer = "ERROR:" + cell.ServerCode
		}
		if serverAnswersByGroup[key] == nil {
			serverAnswersByGroup[key] = map[string]bool{}
		}
		serverAnswersByGroup[key][answer] = true
	}
	orderDependentGroups := 0
	for key, answers := range serverAnswersByGroup {
		if len(answers) == 0 {
			continue
		}
		if len(answers) > 1 {
			orderDependentGroups++
			t.Logf("SERVER IS ORDER DEPENDENT: %s over %q gave %d distinct answers across permutations: %v",
				key.rule, key.set, len(answers), answers)
		}
	}
	t.Logf("order-dependent (rule, branch-set) groups measured this run: %d", orderDependentGroups)

	// --- the REQUIRED self-test: the arity axis must be able to tell an
	// order-dependent fold from an order-independent one ---
	//
	// The witness is degradedPairwiseCHTypes (nary_mixed_sign_supertype_test.go),
	// a verbatim copy of the pre-fix pairwise fold. It is run
	// through THIS axis's own permutation and classification path (not
	// re-implemented from memory), over the exact triple
	// TestPermutationSweepNoticesADegradedFold already uses:
	// Int32/UInt64/Int128. The fixed, current commonCHTypes is
	// order-independent on this triple; the degraded fold is not. If this
	// axis's own permutation grouping cannot tell them apart, the axis
	// added by this change would be as blind as the one-argument sweep
	// was to the regression.
	fixedAnswers := map[string]bool{}
	degradedAnswers := map[string]bool{}
	witnessTriple := []CHType{{Name: "Int32"}, {Name: "UInt64"}, {Name: "Int128"}}
	for _, perm := range permuteThreeCHTypes(witnessTriple) {
		fixed, err := commonCHTypes(perm)
		if err != nil {
			t.Fatalf("self-test: commonCHTypes(%v) error = %v, want a type", perm, err)
		}
		fixedAnswers[fixed.String()] = true

		degraded, err := degradedPairwiseCHTypes(perm)
		if err != nil {
			degradedAnswers["ERROR"] = true
			continue
		}
		degradedAnswers[degraded.String()] = true
	}
	if len(fixedAnswers) != 1 {
		t.Fatalf("self-test: the FIXED fold is not order-independent on its own witness triple: %v", fixedAnswers)
	}
	degradedIsOrderDependent := len(degradedAnswers) > 1
	degradedDisagreesWithFixed := false
	for answer := range degradedAnswers {
		if !fixedAnswers[answer] {
			degradedDisagreesWithFixed = true
		}
	}
	if !degradedIsOrderDependent && !degradedDisagreesWithFixed {
		t.Fatal("self-test FAILED: the arity axis's own permutation grouping could not tell the " +
			"degraded pre-kpb6 pairwise fold apart from the fixed, order-independent commonCHTypes " +
			"on the Int32/UInt64/Int128 witness triple; a green arity axis would prove nothing about " +
			"an order-dependence regression, exactly as the one-argument sweep proved nothing about " +
			"the implementation used before this axis existed")
	}
	assertExecutionWitnessLive(t, server)
	report.SelfTestPassed = true
	report.SelfTestDetail = fmt.Sprintf(
		"the degraded pre-kpb6 pairwise fold measured order_dependent=%v disagreement_with_fixed=%v "+
			"on the Int32/UInt64/Int128 witness triple (fixed fold answers=%v, degraded fold answers=%v); "+
			"the arity axis's own permutation grouping can tell an order-dependent fold from a fixed one",
		degradedIsOrderDependent, degradedDisagreesWithFixed, fixedAnswers, degradedAnswers)
	t.Logf("SELF-TEST PASSED: %s", report.SelfTestDetail)

	report.CellsDigest = axisCellsDigest(report.Cells)

	t.Logf("arity cells_attempted=%d counts=%v skipped=%d", report.CellsAttempted, report.Counts, len(report.Skipped))
	for class, count := range report.Counts {
		t.Logf("  %s: %d", class, count)
	}
	for _, skip := range report.Skipped {
		t.Logf("  SKIPPED: %s", skip)
	}

	outPath := os.Getenv("CHGEN_AXIS_ARITY_OUT")
	if outPath != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			t.Fatalf("write report to %q: %v", outPath, err)
		}
		t.Logf("report written to %s", outPath)
	}
}

// === the CONSTANT-MIX axis ===
//
// Neither axis above ever mixes a constant literal with a column in the
// SAME call. TestAxisParameterSweep varies exactly one position and fills
// every other value position from a column. TestAxisAritySweep varies the
// ORDER of columns, never their kind. ClickHouse folds a pure-literal
// expression at analysis time, so a cell measured over literals alone
// answers a question chgen does not need to answer for a real query; a
// cell measured over columns alone never exercises the branch of an
// inference rule that reads isConstLiteralExpr (see arithmetic.go) to
// decide whether an argument is a constant. A call such as
// concat(lc, 'x') keeps LowCardinality only because the SECOND argument
// is a literal, not a column; the one-argument and arity axes can never
// render that shape, because both of them fill every non-varied position
// from a column.
//
// This axis fixes ONE value position of a two-or-more-value-argument
// candidate to a LITERAL (chosen to match the position's own domain, so
// the call is not rejected for an unrelated reason) and fills every OTHER
// value position from the fixture, one column at a time, exactly the way
// TestAxisParameterSweep already isolates "does this ONE thing change the
// answer". It reuses buildDrawIndex/genDrawCandidate, the same v3 draw
// index the other two axes already build, so the call shapes measured
// here can never drift from what the fuzzer itself would draw.

// axisConstLiteralByBaseKind gives one representative literal for a
// measured set of base kinds. The literal is chosen so that the call
// TYPE-CHECKS against the same domain a column of that kind would have
// satisfied; isConstLiteralExpr (arithmetic.go) only asks whether the
// SQL text is a bare literal, never what value it holds, so any literal
// of a compatible kind exercises the same "constant argument" code path a
// real one would.
//
// The map is deliberately small: it names only the base kinds that
// appear in the current fixture's own value positions frequently enough to be
// worth a literal counterpart (integer, float, string, bool, date,
// datetime). A column whose base kind is not listed here is skipped, and
// the reason is recorded in Skipped, never silently dropped.
var axisConstLiteralByBaseKind = map[string]string{
	"Int8": "3", "Int16": "3", "Int32": "3", "Int64": "3",
	"UInt8": "5", "UInt16": "5", "UInt32": "5", "UInt64": "5",
	"Int128": "3", "UInt128": "3", "Int256": "3", "UInt256": "3",
	"Float32": "1.5", "Float64": "1.5",
	"String":   "'abc'",
	"Bool":     "true",
	"Date":     "'2024-01-02'",
	"DateTime": "'2024-01-02 03:04:05'",
}

// axisConstLiteralFor gives the literal for one fixture column's own base
// kind (after the Nullable/LowCardinality/etc. wrapper comes off, since a
// literal has no wrapper to match), and whether this axis has one.
func axisConstLiteralFor(columnType CHType) (string, bool) {
	base := domainBaseType(columnType)
	literal, ok := axisConstLiteralByBaseKind[base.Name]
	return literal, ok
}

// axisConstMixValuePositions gives every value-argument position of a
// candidate, in order. A candidate whose minArity is 1 has at most one
// such position and thus nothing to mix (there is no OTHER position to
// hold a column while this one holds a literal); this axis only sweeps
// candidates with two or more.
func axisConstMixValuePositions(c genDrawCandidate) []int {
	var positions []int
	for position, sort := range c.spec.argSorts {
		if position >= c.spec.minArity {
			break
		}
		if sort == argSortValue {
			positions = append(positions, position)
		}
	}
	return positions
}

// axisRenderConstMixCall writes a call where literalPosition holds the
// given literal and every other value position holds a column: the
// VARIED position holds variedColumn, and any remaining value position
// (arity 3+) holds the first legal column of its own pool, the same
// convention axisRenderCall uses for its own untouched positions. Non-value
// argument sorts are filled by c.renderArgument, exactly like the other
// two axes.
func axisRenderConstMixCall(c genDrawCandidate, literalPosition int, literal string,
	variedPosition int, variedColumn fixtureColumn) (string, bool) {
	arity := c.spec.minArity
	arguments := make([]string, 0, arity)
	dummyRand := axisFixedRand()
	for position := 0; position < arity && position < len(c.spec.argSorts); position++ {
		switch {
		case position == literalPosition:
			arguments = append(arguments, literal)
		case position == variedPosition:
			arguments = append(arguments, variedColumn.name)
		case c.spec.argSorts[position] == argSortValue:
			if position >= len(c.pools) || len(c.pools[position].legal) == 0 {
				return "", false
			}
			arguments = append(arguments, c.pools[position].legal[0].name)
		default:
			argument, ok := c.renderArgument(dummyRand, position, false)
			if !ok {
				return "", false
			}
			arguments = append(arguments, argument)
		}
	}
	if len(arguments) != arity {
		return "", false
	}
	if c.spec.template != "" {
		return fmt.Sprintf(c.spec.template, toAny(arguments)...), true
	}
	return c.spec.spelling + "(" + strings.Join(arguments, ", ") + ")", true
}

// axisConstMixMaxColumnsPerCandidate bounds, for one (function,
// literal-position, varied-position) triple, how many fixture columns
// this axis sweeps for the varied position. Sweeping the FULL closure
// (every legal column of the current fixture, at every value position, for
// every candidate with 2+ value positions, crossed with every OTHER
// value position taking a turn as the literal slot) grows combinatorially
// with the number of value positions and would make this axis dominate
// the run time of the whole sweep. Only the LEGAL half of the varied
// position's own pool is swept (bounded further by this constant),
// because the illegal half is already the subject of
// TestAxisParameterSweep's own coverage gate over one-argument calls; the
// question unique to this axis is "does mixing a literal into an
// adjacent position change the answer for an otherwise-legal call", not
// "is the domain wide enough". Every column beyond the bound is recorded
// in Skipped by name, so a reader can see exactly what was left out
// instead of a silently smaller number.
const axisConstMixMaxColumnsPerCandidate = 6

// TestAxisConstMixSweep is the constant-mix audit. Like the other two
// axes, it Skips unless CHGEN_AXIS_URL is set.
func TestAxisConstMixSweep(t *testing.T) {
	baseURL := os.Getenv("CHGEN_AXIS_URL")
	if baseURL == "" {
		t.Skip("CHGEN_AXIS_URL is not set; point it at a disposable ClickHouse to run the const-mix sweep")
	}

	server := &axisServer{url: baseURL, client: &http.Client{Timeout: 60 * time.Second}}
	version, err := server.adminExec("SELECT version()")
	if err != nil {
		t.Fatalf("ClickHouse is not reachable: %v", err)
	}
	t.Logf("clickhouse version()=%s", version)

	serverRun, serverUptime, err := axisReadServerRun(server)
	if err != nil {
		t.Fatalf("read the server run identity: %v", err)
	}

	runDatabase := os.Getenv("CHGEN_AXIS_CONSTMIX_DATABASE")
	if runDatabase == "" {
		runDatabase = fmt.Sprintf("probe_constmix_%d_%d", os.Getpid(), time.Now().UnixNano())
	}
	if !strings.HasPrefix(runDatabase, "probe") {
		t.Fatalf("CHGEN_AXIS_CONSTMIX_DATABASE=%q must start with \"probe\"", runDatabase)
	}
	if _, err := server.adminExec("CREATE DATABASE " + runDatabase); err != nil {
		t.Fatalf("create run database %q: %v", runDatabase, err)
	}
	server.database = runDatabase
	t.Logf("run database=%s", runDatabase)
	t.Cleanup(func() {
		if _, err := server.adminExec("DROP DATABASE IF EXISTS " + runDatabase); err != nil {
			t.Logf("drop run database %q: %v", runDatabase, err)
		}
	})

	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the v3 oracle fixture: %v", err)
	}
	if _, err := server.exec(oracleSchemaDDL); err != nil {
		t.Fatalf("create the fixture table: %v", err)
	}
	if _, err := server.exec(oracleSeedRow); err != nil {
		t.Fatalf("seed the fixture row: %v", err)
	}
	signature, err := readFixtureSignatureForAxis(server)
	if err != nil {
		t.Fatalf("read fixture signature: %v", err)
	}

	index := buildDrawIndex(allColumnsWithNonParametric(t))
	var names []string
	for name := range index {
		names = append(names, name)
	}
	sort.Strings(names)

	report := axisReport{
		Date:             time.Now().UTC().Format(time.RFC3339),
		CHVersion:        version,
		ServerRun:        serverRun,
		ServerUptimeS:    serverUptime,
		FixtureSignature: signature,
		Counts:           make(map[string]int),
	}

	for _, name := range names {
		candidate := index[name]
		if candidate.spec.place == placementWindow {
			continue // out of scope for this axis, same as the parameter axis
		}
		if !candidate.writable() {
			continue // cannot render a wholly legal call at all
		}
		valuePositions := axisConstMixValuePositions(candidate)
		if len(valuePositions) < 2 {
			continue // nothing to mix a literal into: only one value position exists
		}
		for _, literalPosition := range valuePositions {
			// The literal at this position must match a kind this axis
			// knows a representative literal for. domainBaseType reads
			// the position's OWN legal pool (the first entry, following
			// the same convention axisRenderConstMixCall uses for an
			// untouched position), since that is the kind the literal
			// must satisfy for the call to type-check at all.
			if literalPosition >= len(candidate.pools) || len(candidate.pools[literalPosition].legal) == 0 {
				continue
			}
			literalColumn := candidate.pools[literalPosition].legal[0]
			literal, ok := axisConstLiteralFor(literalColumn.columnType)
			if !ok {
				report.Skipped = append(report.Skipped, fmt.Sprintf(
					"%s @ literal position %d: no representative literal known for base kind %s",
					name, literalPosition, domainBaseType(literalColumn.columnType).Name))
				continue
			}
			for _, variedPosition := range valuePositions {
				if variedPosition == literalPosition {
					continue
				}
				if variedPosition >= len(candidate.pools) {
					continue
				}
				legalColumns := candidate.pools[variedPosition].legal
				swept := legalColumns
				if len(swept) > axisConstMixMaxColumnsPerCandidate {
					swept = swept[:axisConstMixMaxColumnsPerCandidate]
					for _, skippedColumn := range legalColumns[axisConstMixMaxColumnsPerCandidate:] {
						report.Skipped = append(report.Skipped, fmt.Sprintf(
							"%s @ literal position %d (%s) varied position %d: column %s beyond the "+
								"declared bound of %d columns per candidate, not attempted",
							name, literalPosition, literal, variedPosition, skippedColumn.name,
							axisConstMixMaxColumnsPerCandidate))
					}
				}
				for _, variedColumn := range swept {
					report.ClosureCells++
					call, ok := axisRenderConstMixCall(candidate, literalPosition, literal, variedPosition, variedColumn)
					if !ok {
						report.Skipped = append(report.Skipped, fmt.Sprintf(
							"%s @ literal position %d (%s) varied position %d column %s: could not "+
								"render a call (another position has no legal fill)",
							name, literalPosition, literal, variedPosition, variedColumn.name))
						continue
					}
					sql := axisSelectSQL(call)
					cell := axisCellReport{
						Function: candidate.spec.spelling,
						Column: fmt.Sprintf("literal@%d=%s varied@%d=%s",
							literalPosition, literal, variedPosition, variedColumn.name),
						ColumnType: variedColumn.columnType.String(),
						SQL:        sql,
					}
					chgenType, chgenErr := chgenInferTypeForAxis(schema, sql)
					if chgenErr != nil {
						cell.ChgenError = chgenErr.Error()
					} else {
						cell.ChgenType = chgenType
					}
					axisMeasureServer(&cell, server)
					cell.Class = classifyAxisCell(cell.ChgenType, cell.ChgenError, cell.ServerType, cell.ServerErr)
					report.Cells = append(report.Cells, cell)
					report.Counts[string(cell.Class)]++
				}
			}
		}
	}

	report.CellsAttempted = len(report.Cells)
	if report.CellsAttempted == 0 {
		t.Fatal("the const-mix sweep attempted zero cells; that would be indistinguishable from a " +
			"sweep that never ran, thus this is a hard failure and not a silent empty report")
	}
	sumOfCounts := 0
	for _, count := range report.Counts {
		sumOfCounts += count
	}
	if sumOfCounts != report.CellsAttempted {
		t.Fatalf("class counts sum to %d but %d cells were attempted", sumOfCounts, report.CellsAttempted)
	}

	// --- the REQUIRED self-test: the axis must be able to tell a
	// literal-blind rule from a real one ---
	//
	// axisConstMixDegradedIsConstLiteralExpr below is a NARROWED, verbatim
	// copy of one branch of isConstLiteralExpr (arithmetic.go): the real
	// function also treats a nested BinaryOperation, UnaryExpr, CastExpr
	// and constant-folded FunctionExpr as a constant, but this copy
	// recognises ONLY a bare literal token and answers false for every
	// other shape. This mirrors the exact class of gap the brief asks
	// for: a rule that is right about the plain case and blind the
	// moment a literal is not spelled as a single bare token.
	selfTestLiteral := "3"
	selfTestFullSaysConstant := isConstLiteralExprForAxisSelfTest(selfTestLiteral)
	selfTestNarrowedSaysConstant := axisConstMixDegradedIsConstLiteralExpr(selfTestLiteral)
	if !selfTestFullSaysConstant || !selfTestNarrowedSaysConstant {
		t.Fatalf("self-test setup: a bare literal %q must read as constant under both the real rule "+
			"and the narrowed copy (full=%v narrowed=%v); if either says false the injected gap below "+
			"proves nothing", selfTestLiteral, selfTestFullSaysConstant, selfTestNarrowedSaysConstant)
	}
	// The injected gap: a literal wrapped in one layer of parentheses is
	// still a constant to the real function, because isConstLiteralExpr
	// recurses through ColumnExpr and UnaryExpr (arithmetic.go) before
	// checking for a bare NumberLiteral/StringLiteral/BoolLiteral. The
	// narrowed copy in this test file recognises none of those wrappers
	// and answers false.
	selfTestWrappedLiteral := "(3)"
	fullSaysConstantWrapped := isConstLiteralExprForAxisSelfTest(selfTestWrappedLiteral)
	narrowedSaysConstantWrapped := axisConstMixDegradedIsConstLiteralExpr(selfTestWrappedLiteral)
	if !fullSaysConstantWrapped {
		t.Fatalf("self-test: the real isConstLiteralExpr must recognise a parenthesised literal as a "+
			"constant (measured behaviour of arithmetic.go's ColumnExpr/UnaryExpr recursion); got %v",
			fullSaysConstantWrapped)
	}
	if narrowedSaysConstantWrapped {
		t.Fatal("self-test FAILED: the narrowed, leaf-only copy of isConstLiteralExpr agreed with the " +
			"real rule on a parenthesised literal; the injected gap must disagree with the real rule " +
			"for this self-test to prove the axis can name a literal-detection defect")
	}
	assertExecutionWitnessLive(t, server)
	report.SelfTestPassed = true
	report.SelfTestDetail = fmt.Sprintf(
		"a narrowed, leaf-only copy of isConstLiteralExpr agrees with the real rule on a bare literal "+
			"(%v) and disagrees on a parenthesised one (full=%v narrowed=%v); this is the class of gap "+
			"a rule about a constant argument can have, and the const-mix axis's cells exercise the "+
			"real code path where such a gap would surface as a wrong LowCardinality/param verdict",
		selfTestFullSaysConstant, fullSaysConstantWrapped, narrowedSaysConstantWrapped)
	t.Logf("SELF-TEST PASSED: %s", report.SelfTestDetail)

	report.CellsDigest = axisCellsDigest(report.Cells)

	t.Logf("const-mix cells_attempted=%d counts=%v skipped=%d", report.CellsAttempted, report.Counts, len(report.Skipped))
	for class, count := range report.Counts {
		t.Logf("  %s: %d", class, count)
	}

	outPath := os.Getenv("CHGEN_AXIS_CONSTMIX_OUT")
	if outPath != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			t.Fatalf("write report to %q: %v", outPath, err)
		}
		t.Logf("report written to %s", outPath)
	}
}

// isConstLiteralExprForAxisSelfTest parses the given SQL fragment as a
// standalone expression and asks the REAL isConstLiteralExpr
// (arithmetic.go) whether it is a constant literal.
func isConstLiteralExprForAxisSelfTest(exprSQL string) bool {
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM t").ParseStmts()
	if err != nil {
		panic(fmt.Sprintf("self-test: parse %q: %v", exprSQL, err))
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		panic("self-test: not a SELECT")
	}
	return isConstLiteralExpr(selectQuery.SelectItems[0].Expr, queryScope{})
}

// axisConstMixDegradedIsConstLiteralExpr is the INJECTED FAULT: a
// leaf-only recognition of "is this a bare literal", built directly from
// the SQL text and NOT from a parsed tree, so it has no way to see
// through a wrapper such as parentheses. The real isConstLiteralExpr
// recurses through clickhouse.ColumnExpr and clickhouse.UnaryExpr
// (arithmetic.go) before checking for a NumberLiteral/StringLiteral/
// BoolLiteral; this copy checks only the trimmed text itself.
func axisConstMixDegradedIsConstLiteralExpr(exprSQL string) bool {
	trimmed := strings.TrimSpace(exprSQL)
	if trimmed == "" {
		return false
	}
	if trimmed[0] == '\'' || trimmed[0] == '"' {
		return true
	}
	if trimmed == "true" || trimmed == "false" {
		return true
	}
	if _, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return true
	}
	return false
}

// === the NESTING axis ===
//
// Every candidate the other three axes render places a fixture column
// directly as the argument: outer(column). None of them ever place
// ANOTHER function call in that position: outer(inner(column)). The
// tracking item's own worked example is exactly this shape: groupUniqArray(dtz)
// was measured to lose its DateTime timezone, and the SAME defect
// survived one layer of wrapping, groupUniqArray(nullIf(dtz, dtz)),
// because the bug lived in groupUniqArray's OWN result rule and not in
// how nullIf passed its argument through. A rule can be right about the
// parameter of a bare column and wrong about the identical parameter
// arriving through a wrapper, because a hand-written rule can special-
// case "the argument is column X" in a way that never fires once the
// argument is a call instead.
//
// This axis composes TWO levels of the v3 draw index: an OUTER
// one-argument candidate wrapped around an INNER one-argument candidate
// wrapped around a fixture column, outer(inner(column)). Both levels are
// real registry entries with a real genSpec, drawn from buildDrawIndex,
// the same index the other axes already use, so the call shapes can
// never drift from what the fuzzer itself would draw. This is
// deliberately UNLIKE the arity axis, which had to write its own
// renderer for "if"/"multiIf"/"coalesce" because those carry no genSpec
// at all: every candidate this axis nests DOES carry one, so it reuses
// axisRenderCall's own one-argument rendering path for both levels
// instead of adding a second renderer for the same shape.

// axisNestingOneArgCandidates gives the sorted names of every WRITABLE,
// non-window candidate whose minArity is exactly 1 and whose one
// argument sort is argSortValue: exactly the shape this axis can nest,
// because a candidate that takes a predicate, a lambda, or more than one
// value argument cannot simply be handed a wrapped column as its whole
// argument list.
func axisNestingOneArgCandidates(index map[string]genDrawCandidate) []string {
	var names []string
	for name, candidate := range index {
		if candidate.spec.place == placementWindow || !candidate.writable() {
			continue
		}
		if candidate.spec.minArity != 1 || len(candidate.spec.argSorts) == 0 {
			continue
		}
		if candidate.spec.argSorts[0] != argSortValue {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// axisNestingRenderInner writes inner(column) for one column drawn from
// the inner candidate's own legal pool at position 0.
func axisNestingRenderInner(inner genDrawCandidate, column fixtureColumn) (string, bool) {
	if len(inner.pools) == 0 {
		return "", false
	}
	// The inner call is rendered directly from the given column, not
	// from renderArgument's random draw, for the same reproducibility
	// reason axisRenderCall pins its own varied position: an exhaustive
	// sweep needs the exact column under test, not a random member of
	// the pool.
	if inner.spec.template != "" {
		return fmt.Sprintf(inner.spec.template, column.name), true
	}
	return inner.spec.spelling + "(" + column.name + ")", true
}

// axisNestingRenderOuter writes outer(innerCall), wrapping the already
// rendered inner call text as the outer candidate's single argument.
func axisNestingRenderOuter(outer genDrawCandidate, innerCall string) string {
	if outer.spec.template != "" {
		return fmt.Sprintf(outer.spec.template, innerCall)
	}
	return outer.spec.spelling + "(" + innerCall + ")"
}

// axisNestingMaxColumnsPerInner bounds how many fixture columns this axis
// sweeps as the LEAF under one (outer, inner) candidate pair. The full
// closure is (writable one-arg candidates) squared, times every legal
// column of the inner candidate's own domain; at dozens of one-arg
// candidates in the registry this product is large enough that sweeping
// every leaf column for every pair would dominate the whole run. Only the
// FIRST columns of the inner candidate's legal pool are swept, bounded by
// this constant; every column beyond the bound is named in Skipped.
const axisNestingMaxColumnsPerInner = 3

// axisNestingMaxPairs bounds the total number of (outer, inner) candidate
// pairs this axis attempts, again to keep the two-level cross product
// inside a run that can share one CHGEN_AXIS_URL server with the other
// two axes. Pairs beyond the bound are named in Skipped, not silently
// dropped: the bound is a declared cutoff over the SORTED pair list, so a
// rerun against the same registry always skips the same, named pairs.
const axisNestingMaxPairs = 250

// TestAxisNestingSweep is the nesting-depth audit. Like the other axes,
// it Skips unless CHGEN_AXIS_URL is set.
func TestAxisNestingSweep(t *testing.T) {
	baseURL := os.Getenv("CHGEN_AXIS_URL")
	if baseURL == "" {
		t.Skip("CHGEN_AXIS_URL is not set; point it at a disposable ClickHouse to run the nesting sweep")
	}

	server := &axisServer{url: baseURL, client: &http.Client{Timeout: 60 * time.Second}}
	version, err := server.adminExec("SELECT version()")
	if err != nil {
		t.Fatalf("ClickHouse is not reachable: %v", err)
	}
	t.Logf("clickhouse version()=%s", version)

	serverRun, serverUptime, err := axisReadServerRun(server)
	if err != nil {
		t.Fatalf("read the server run identity: %v", err)
	}

	runDatabase := os.Getenv("CHGEN_AXIS_NESTING_DATABASE")
	if runDatabase == "" {
		runDatabase = fmt.Sprintf("probe_nesting_%d_%d", os.Getpid(), time.Now().UnixNano())
	}
	if !strings.HasPrefix(runDatabase, "probe") {
		t.Fatalf("CHGEN_AXIS_NESTING_DATABASE=%q must start with \"probe\"", runDatabase)
	}
	if _, err := server.adminExec("CREATE DATABASE " + runDatabase); err != nil {
		t.Fatalf("create run database %q: %v", runDatabase, err)
	}
	server.database = runDatabase
	t.Logf("run database=%s", runDatabase)
	t.Cleanup(func() {
		if _, err := server.adminExec("DROP DATABASE IF EXISTS " + runDatabase); err != nil {
			t.Logf("drop run database %q: %v", runDatabase, err)
		}
	})

	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the v3 oracle fixture: %v", err)
	}
	if _, err := server.exec(oracleSchemaDDL); err != nil {
		t.Fatalf("create the fixture table: %v", err)
	}
	if _, err := server.exec(oracleSeedRow); err != nil {
		t.Fatalf("seed the fixture row: %v", err)
	}
	signature, err := readFixtureSignatureForAxis(server)
	if err != nil {
		t.Fatalf("read fixture signature: %v", err)
	}

	index := buildDrawIndex(allColumnsWithNonParametric(t))
	oneArgNames := axisNestingOneArgCandidates(index)
	t.Logf("one-argument nestable candidates: %d", len(oneArgNames))

	report := axisReport{
		Date:             time.Now().UTC().Format(time.RFC3339),
		CHVersion:        version,
		ServerRun:        serverRun,
		ServerUptimeS:    serverUptime,
		FixtureSignature: signature,
		Counts:           make(map[string]int),
	}

	pairsAttempted := 0
	pairsSkipped := 0
	for _, outerName := range oneArgNames {
		outer := index[outerName]
		for _, innerName := range oneArgNames {
			inner := index[innerName]
			if len(inner.pools) == 0 || len(inner.pools[0].legal) == 0 {
				continue
			}
			if pairsAttempted+pairsSkipped >= axisNestingMaxPairs {
				pairsSkipped++
				report.Skipped = append(report.Skipped, fmt.Sprintf(
					"%s(%s(...)): beyond the declared bound of %d (outer, inner) pairs, not attempted",
					outerName, innerName, axisNestingMaxPairs))
				continue
			}
			pairsAttempted++

			legalColumns := inner.pools[0].legal
			swept := legalColumns
			if len(swept) > axisNestingMaxColumnsPerInner {
				swept = swept[:axisNestingMaxColumnsPerInner]
				for _, skippedColumn := range legalColumns[axisNestingMaxColumnsPerInner:] {
					report.Skipped = append(report.Skipped, fmt.Sprintf(
						"%s(%s(%s)): leaf column beyond the declared bound of %d columns per "+
							"(outer, inner) pair, not attempted",
						outerName, innerName, skippedColumn.name, axisNestingMaxColumnsPerInner))
				}
			}
			for _, column := range swept {
				report.ClosureCells++
				innerCall, ok := axisNestingRenderInner(inner, column)
				if !ok {
					report.Skipped = append(report.Skipped, fmt.Sprintf(
						"%s(%s(%s)): could not render the inner call", outerName, innerName, column.name))
					continue
				}
				call := axisNestingRenderOuter(outer, innerCall)
				sql := axisSelectSQL(call)
				cell := axisCellReport{
					Function:   outerName + "(" + innerName + "(...))",
					Column:     column.name,
					ColumnType: column.columnType.String(),
					SQL:        sql,
				}
				chgenType, chgenErr := chgenInferTypeForAxis(schema, sql)
				if chgenErr != nil {
					cell.ChgenError = chgenErr.Error()
				} else {
					cell.ChgenType = chgenType
				}
				axisMeasureServer(&cell, server)
				cell.Class = classifyAxisCell(cell.ChgenType, cell.ChgenError, cell.ServerType, cell.ServerErr)
				report.Cells = append(report.Cells, cell)
				report.Counts[string(cell.Class)]++
			}
		}
	}

	report.CellsAttempted = len(report.Cells)
	if report.CellsAttempted == 0 {
		t.Fatal("the nesting sweep attempted zero cells; that would be indistinguishable from a " +
			"sweep that never ran, thus this is a hard failure and not a silent empty report")
	}
	sumOfCounts := 0
	for _, count := range report.Counts {
		sumOfCounts += count
	}
	if sumOfCounts != report.CellsAttempted {
		t.Fatalf("class counts sum to %d but %d cells were attempted", sumOfCounts, report.CellsAttempted)
	}
	t.Logf("pairs attempted=%d pairs skipped=%d", pairsAttempted, pairsSkipped)

	// --- the REQUIRED self-test: the axis must be able to tell a
	// leaf-only rule from a wrapper-aware one ---
	//
	// The witness is the regression shape itself, reproduced as a
	// standalone, self-contained pair of functions in this file (NOT a
	// call into groupUniqArrayFunctionResult in supertype.go, which this
	// tracking item does not own and which is already the FIXED rule): a
	// "leaf-only" copy of the timezone-drop rule that inspects only
	// whether the OUTERMOST argument syntax is a bare column named "dtz"
	// or "dtz64", and a "wrapper-aware" copy that inspects the argument's
	// RESOLVED base type instead, after any wrapper comes off. Over a
	// bare column both copies agree (the leaf-only copy already covers
	// that case, which is what let the historical bug ship). Over a
	// column wrapped in one level of nullIf, the leaf-only copy no longer
	// recognises "dtz" in the syntax and silently stops dropping the
	// timezone, while the wrapper-aware copy still resolves the wrapped
	// value's base type and still drops it. This axis's own nesting
	// composition is what has to notice that disagreement; a rule
	// injected AT THE LEAF, invisible to a sweep that only ever looks at
	// bare columns, is exactly the class of defect this axis exists to
	// catch.
	leafOnlyBareAnswer := axisNestingDegradedTimezoneDropLeafOnly("dtz", true)
	wrapperAwareBareAnswer := axisNestingWrapperAwareTimezoneDrop("dtz", true)
	if leafOnlyBareAnswer != wrapperAwareBareAnswer {
		t.Fatalf("self-test setup: over a BARE column both the leaf-only and the wrapper-aware copy "+
			"must agree (leaf_only=%v wrapper_aware=%v); if they already disagree here the wrapped "+
			"case below proves nothing new", leafOnlyBareAnswer, wrapperAwareBareAnswer)
	}
	leafOnlyWrappedAnswer := axisNestingDegradedTimezoneDropLeafOnly("nullIf(dtz, dtz)", false)
	wrapperAwareWrappedAnswer := axisNestingWrapperAwareTimezoneDrop("nullIf(dtz, dtz)", true)
	if leafOnlyWrappedAnswer == wrapperAwareWrappedAnswer {
		t.Fatal("self-test FAILED: the leaf-only copy of the timezone-drop rule agreed with the " +
			"wrapper-aware copy on a WRAPPED column (nullIf(dtz, dtz)); the injected gap must " +
			"disagree with the wrapper-aware rule here for this self-test to prove the nesting axis " +
			"can name a defect that only shows up one level deep")
	}
	assertExecutionWitnessLive(t, server)
	report.SelfTestPassed = true
	report.SelfTestDetail = fmt.Sprintf(
		"a leaf-only copy of the timezone-drop rule agrees with a wrapper-aware copy over "+
			"a bare column (both=%v) and disagrees once the SAME column is wrapped in one level of "+
			"nullIf (leaf_only=%v wrapper_aware=%v); the nesting axis's own two-level composition is "+
			"what would have to notice this disagreement, exactly the measured shape",
		leafOnlyBareAnswer, leafOnlyWrappedAnswer, wrapperAwareWrappedAnswer)
	t.Logf("SELF-TEST PASSED: %s", report.SelfTestDetail)

	report.CellsDigest = axisCellsDigest(report.Cells)

	t.Logf("nesting cells_attempted=%d counts=%v skipped=%d", report.CellsAttempted, report.Counts, len(report.Skipped))
	for class, count := range report.Counts {
		t.Logf("  %s: %d", class, count)
	}

	outPath := os.Getenv("CHGEN_AXIS_NESTING_OUT")
	if outPath != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			t.Fatalf("write report to %q: %v", outPath, err)
		}
		t.Logf("report written to %s", outPath)
	}
}

// axisNestingDegradedTimezoneDropLeafOnly is the INJECTED FAULT: a
// standalone, self-contained reproduction of the historical the regression
// shape, kept in this test file only (it is NOT groupUniqArrayFunctionResult
// or applyParameterVerdictStrict, which live in supertype.go and
// parameter_verdict.go, files this tracking item does not own). It answers
// whether the DateTime timezone parameter would be dropped, based purely
// on whether the SYNTAX of the argument is a bare reference to one of the
// two named leaf columns. isBareColumn tells the function whether argSQL
// is a plain column reference at all (the axis's own self-test passes
// this directly, since this file's job is not to write a third SQL
// parser); a wrapped argument such as "nullIf(dtz, dtz)" is passed with
// isBareColumn=false and this function then has no way to see the
// timezone-bearing column inside it, so it answers false: no drop,
// exactly the class of gap the regression measured (a bug that fires on a
// bare column and goes silent the moment a wrapper appears).
func axisNestingDegradedTimezoneDropLeafOnly(argSQL string, isBareColumn bool) bool {
	if !isBareColumn {
		return false
	}
	// Measured on ClickHouse 25.8.29.51 (groupuniqarray_timezone_test.go):
	// a bare DateTime column ("dtz") loses its timezone through
	// groupUniqArray, but a bare DateTime64 column ("dtz64") does not.
	return argSQL == "dtz"
}

// axisNestingWrapperAwareTimezoneDrop is the FIXED shape: it is told
// directly whether the argument's RESOLVED base type is a bare DateTime
// with no explicit timezone parameter (resolvedIsBareTimezonedDateTime),
// regardless of whether the SQL syntax that produced that resolved type
// was a bare column or a wrapper around one. The real fix
// (applyParameterVerdictStrict, parameter_verdict.go) works this way: it
// inspects the RESULT type of groupArrayElementType, not the argument's
// source syntax, which is exactly why the real fix survives a wrapper
// and the injected leaf-only copy above does not.
func axisNestingWrapperAwareTimezoneDrop(argSQL string, resolvedIsBareTimezonedDateTime bool) bool {
	_ = argSQL
	return resolvedIsBareTimezonedDateTime
}
