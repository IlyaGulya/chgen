//go:build fuzzoracle

package engine

// The wrapper grid.
//
// This test ENUMERATES a closed cell space and compares, cell by cell,
// what the SERVER answers with what chgen answers. It replaces the
// per-defect hand-built probe table that earlier changes used, and it does
// so without needing a correct human hypothesis first: the grid is built
// from the registry tables themselves, thus it measures the rules that
// exist rather than the rules that somebody suspected.
//
// Two commands:
//
//	go test -tags fuzzoracle ./internal/engine -run TestWrapperGrid \
//	    -chgen-grid-url http://localhost:18123
//
//	go test -tags fuzzoracle ./internal/engine -run TestWrapperGrid \
//	    -chgen-grid-url http://localhost:18123 -chgen-grid-regenerate
//
// The first form COMPARES against the committed golden and fails on any
// difference. The second form REWRITES the golden from the server.
//
// Regeneration is a deliberate act. It needs an explicit flag, thus no
// ordinary run and no CI run can quietly rewrite the expectation. The
// golden holds one line per cell, sorted by cell address, so a rewrite
// shows in review as the exact set of cells whose answer moved, together
// with the old and the new answer of the server.
//
// The golden holds the MEASURED answer of the server. It is never written
// by hand. A hand-written expectation would be a second copy of the type
// rules and it would drift away from the server, which is the defect that
// this instrument exists to remove.

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
	"github.com/IlyaGulya/chgen/internal/conformance"
)

var (
	gridURL = flag.String("chgen-grid-url", "",
		"ClickHouse HTTP endpoint for the wrapper grid; empty skips the test")
	gridRegenerate = flag.Bool("chgen-grid-regenerate", false,
		"rewrite the golden file from the server instead of comparing against it")
)

// gridRecord is one line of the golden file.
type gridRecord struct {
	// id is the stable cell address.
	id string
	// sql is the measured expression. It is stored so that a reviewer
	// can re-run one cell by hand without rebuilding the enumerator.
	sql string
	// server is the type that the server answered, or the empty string
	// when the server refused the cell.
	server string
	// serverCode is the ClickHouse error code of a refused cell, or the
	// empty string for an accepted cell. A refusal is DATA: the code
	// derives the argument domain of an entry that declares none, and
	// it is the only evidence that a typed cell is refused at execution
	// (the regression produced AggregateFunction(quantile, Bool), which the
	// server answers with Code 134 when it runs).
	serverCode string
	// chgen is the type that chgen answered, or the empty string when
	// chgen refused.
	chgen string
	// chgenRefused reports a chgen refusal. An explicit refusal is
	// always better than a silently wrong type, thus a refusal is
	// recorded as its own state and never as a missing answer.
	chgenRefused bool
	// verdict classifies the cell; see gridClassify.
	verdict string
}

// The verdicts. They are text in the golden file, so that a change of
// verdict for one cell is readable in a diff.
const (
	// gridAgree: both sides gave the same type, after the accepted
	// Bool/UInt8 equivalence.
	gridAgree = "AGREE"
	// gridBothRefuse: the server refused and chgen refused. This is the
	// wanted answer for an illegal cell.
	gridBothRefuse = "BOTH_REFUSE"
	// gridChgenRefusesServerAccepts: chgen is more strict than the
	// server. It is a GAP, not a defect: an explicit refusal never
	// produces a wrong type.
	gridChgenRefusesServerAccepts = "CHGEN_REFUSES_SERVER_ACCEPTS"
	// gridChgenTypesServerRefuses: chgen produced a type for a cell
	// that the server refuses. This is the dangerous class, because the
	// generated Go code would name a type that no query can return.
	gridChgenTypesServerRefuses = "CHGEN_TYPES_SERVER_REFUSES"
	// gridMismatch: both sides gave a type and the types differ. This
	// is the worst defect class, a silently wrong type.
	gridMismatch = "MISMATCH"
	// gridExcluded: the cell is illegal for a reason that is NOT about
	// typing; see gridExclusionReason.
	gridExcluded = "EXCLUDED"
	// gridChgenTypesServerRefusesPair: the server refuses the cell with
	// Code 386, NO_COMMON_TYPE, which is a refusal about the PAIR of
	// arguments and not about either argument's type on its own, AND
	// chgen produced a type for the same cell. The name says PAIR so a
	// reader cannot mistake this for gridChgenTypesServerRefuses, which
	// is a verdict about a TYPE. Conflating the two once fabricated five
	// findings: the grid banked a refusal about an enumerated pair as if
	// it were a domain finding about a type (the safarr element defect
	// fixed in the regression). A cell in this verdict asks a reviewer to
	// check the SQL first, before reading it as a type-rule gap.
	gridChgenTypesServerRefusesPair = "CHGEN_TYPES_SERVER_REFUSES_PAIR"
)

func TestWrapperGrid(t *testing.T) {
	if *gridURL == "" {
		t.Skip("-chgen-grid-url is not set; start a disposable ClickHouse to run the wrapper grid")
	}
	oracle := &chOracle{url: *gridURL, client: &http.Client{Timeout: 120 * time.Second}}
	version, err := oracle.adminExec("SELECT version()")
	if err != nil {
		t.Fatalf("ClickHouse is not reachable: %v", err)
	}
	version = strings.TrimSpace(version)

	// The run owns a private database and drops it at the end. The name
	// is fixed, not random, because the ticket assigns exactly one probe
	// database to this work while another change owns the other names.
	const runDatabase = "chgen_probe_6xl"
	if _, err := oracle.adminExec("DROP DATABASE IF EXISTS " + runDatabase); err != nil {
		t.Fatalf("drop stale run database: %v", err)
	}
	if _, err := oracle.adminExec("CREATE DATABASE " + runDatabase); err != nil {
		t.Fatalf("create run database: %v", err)
	}
	oracle.database = runDatabase
	t.Cleanup(func() {
		if _, err := oracle.adminExec("DROP DATABASE IF EXISTS " + runDatabase); err != nil {
			t.Logf("drop run database: %v", err)
		}
	})

	// The fixture. The DDL is derived from the same cell list that
	// addresses the probes, thus a column cannot exist without a cell.
	//
	// allow_suspicious_low_cardinality_types is ON. Code 455 is a POLICY
	// guard and not type evidence: the server refuses
	// LowCardinality(Int32) by policy while it types the expression
	// perfectly well. Leaving the guard on would record a policy opinion
	// as a type rule.
	settings := "allow_suspicious_low_cardinality_types=1"
	oracle.url = gridURLWithSettings(*gridURL, settings)
	if _, err := oracle.exec(gridSchemaDDL()); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if _, err := oracle.exec(gridSeedRow()); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	schema, err := schemaFromDDLErr(t, gridSchemaDDL())
	if err != nil {
		t.Fatalf("chgen schema parse: %v", err)
	}

	probes := gridBuildProbes(schema)
	if len(probes) == 0 {
		t.Fatal("the grid enumerated no cells")
	}

	// The measurement. The batch is grouped by the aggregate flag,
	// because a batch that mixes an aggregate with a bare column fails
	// as a whole with Code 215 and that failure belongs to the batch.
	records := gridMeasure(t, oracle, schema, probes)

	sort.Slice(records, func(i, j int) bool { return records[i].id < records[j].id })

	if *gridRegenerate {
		if err := gridCheckWriteVersion(*gridExpectVersion, version); err != nil {
			t.Fatalf("%v", err)
		}
		gridWriteGolden(t, probes, records, version)
		t.Logf("golden rewritten from the server: %d cells, version %s", len(records), version)
		return
	}
	gridCompareGolden(t, records, version)
}

// gridURLWithSettings appends the settings of the run to the base URL.
func gridURLWithSettings(base, settings string) string {
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	return base + separator + settings
}

// gridMeasure asks both sides for every cell.
func gridMeasure(t *testing.T, oracle *chOracle, schema *Schema, probes []gridProbe) []gridRecord {
	t.Helper()
	var aggregates, scalars []gridProbe
	for _, probe := range probes {
		if probe.aggregate {
			aggregates = append(aggregates, probe)
		} else {
			scalars = append(scalars, probe)
		}
	}
	records := make([]gridRecord, 0, len(probes))
	records = append(records, gridMeasureGroup(oracle, schema, scalars)...)
	records = append(records, gridMeasureGroup(oracle, schema, aggregates)...)
	return records
}

// gridMeasureGroup measures one aggregate-homogeneous group.
func gridMeasureGroup(oracle *chOracle, schema *Schema, probes []gridProbe) []gridRecord {
	if len(probes) == 0 {
		return nil
	}
	inputs := make([]conformance.Input, len(probes))
	for i, probe := range probes {
		inputs[i] = conformance.Input{ID: probe.id, Expression: probe.sql, Table: "g"}
	}
	report, err := conformance.RunBatched(context.Background(), gridSchemaDDL(), gridSeedRow(), inputs, conformance.BatchLanes{
		Info: func(context.Context) (conformance.ServerInfo, error) {
			version, infoErr := oracle.adminExec("SELECT version()")
			if infoErr != nil {
				return conformance.ServerInfo{}, infoErr
			}
			serverRun, uptime, infoErr := readServerRun(oracle)
			return conformance.ServerInfo{Version: strings.TrimSpace(version), ServerRun: serverRun, UptimeS: uptime}, infoErr
		},
		Infer: func(ordered []conformance.Input) []conformance.TypeResult {
			results := make([]conformance.TypeResult, len(ordered))
			for index, input := range ordered {
				inferred, inferErr := gridChgenType(schema, input.Expression)
				if inferErr != nil {
					results[index].Error = inferErr.Error()
				} else {
					results[index] = conformance.CanonicalResult(inferred)
				}
			}
			return results
		},
		Analyze: func(_ context.Context, ordered []conformance.Input) []conformance.TypeResult {
			return gridAnalysisLane(oracle, ordered)
		},
		Execute: func(_ context.Context, ordered []conformance.Input) []conformance.ExecutionResult {
			return gridExecutionLane(oracle, ordered)
		},
	})
	if err != nil {
		panic(fmt.Sprintf("wrapper grid conformance runner: %v", err))
	}
	records := make([]gridRecord, len(report.Cells))
	for i, cell := range report.Cells {
		record := gridRecord{id: cell.ID, sql: cell.Expression}
		if cell.Execution.Error != "" {
			record.serverCode = strconv.Itoa(cell.Execution.ErrorCode)
		} else if cell.Analysis.Error != "" {
			record.serverCode = strconv.Itoa(cell.Analysis.ErrorCode)
		} else {
			record.server = gridStableTypeName(cell.Analysis.Raw)
		}
		if cell.Chgen.Error != "" {
			record.chgenRefused = true
		} else {
			record.chgen = gridStableTypeName(cell.Chgen.Raw)
		}
		record.verdict = gridClassify(record)
		records[i] = record
	}
	return records
}

func gridStableTypeName(raw string) string {
	name := normalizeTypeName(raw)
	var stable strings.Builder
	quote := byte(0)
	for index := 0; index < len(name); index++ {
		char := name[index]
		if quote != 0 {
			stable.WriteByte(char)
			if char == quote && (index == 0 || name[index-1] != '\\') {
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			stable.WriteByte(char)
			continue
		}
		if char == ' ' && index > 0 && name[index-1] == ',' {
			continue
		}
		stable.WriteByte(char)
	}
	return stable.String()
}

func gridAnalysisLane(oracle *chOracle, inputs []conformance.Input) []conformance.TypeResult {
	expressions := make([]string, len(inputs))
	for index, input := range inputs {
		expressions[index] = "toTypeName(" + input.Expression + ")"
	}
	answers := gridServerProjection(oracle, expressions)
	results := make([]conformance.TypeResult, len(answers))
	for index, answer := range answers {
		if answer.err != "" {
			results[index] = conformance.TypeResult{Error: answer.err, ErrorCode: gridErrorCode(answer.err)}
		} else {
			results[index] = conformance.CanonicalResult(answer.typeName)
		}
	}
	return results
}

func gridExecutionLane(oracle *chOracle, inputs []conformance.Input) []conformance.ExecutionResult {
	expressions := make([]string, len(inputs))
	for index, input := range inputs {
		expressions[index] = "ignore(" + input.Expression + ")"
	}
	answers := gridServerProjection(oracle, expressions)
	results := make([]conformance.ExecutionResult, len(answers))
	for index, answer := range answers {
		if answer.err != "" {
			results[index] = conformance.ExecutionResult{Error: answer.err, ErrorCode: gridErrorCode(answer.err)}
		} else {
			results[index].Ran = true
		}
	}
	return results
}

func gridErrorCode(message string) int {
	code, _ := strconv.Atoi(clickHouseErrorCode(message))
	return code
}

func gridServerProjection(oracle *chOracle, expressions []string) []chResult {
	return conformanceServerProjection(oracle, "g", expressions)
}

func conformanceServerProjection(oracle *chOracle, table string, expressions []string) []chResult {
	results := make([]chResult, len(expressions))
	var run func(int, int)
	run = func(low, high int) {
		out, err := oracle.exec("SELECT " + strings.Join(expressions[low:high], ", ") + " FROM " + table)
		if err == nil {
			fields := strings.Split(out, "\t")
			if len(fields) == high-low {
				for index, field := range fields {
					results[low+index] = chResult{typeName: field}
				}
				return
			}
			err = fmt.Errorf("expected %d fields, got %d", high-low, len(fields))
		}
		if high-low == 1 {
			results[low] = chResult{err: err.Error()}
			return
		}
		middle := (low + high) / 2
		run(low, middle)
		run(middle, high)
	}
	if len(expressions) > 0 {
		run(0, len(expressions))
	}
	return results
}

// gridServerTypes resolves a batch of expressions against the fixture
// table `g`.
//
// It reuses the batching and the bisection of the oracle harness, but it
// must send `FROM g`, thus it cannot call chOracle.typeNames, which names
// `t`. The bisection rule is the load-bearing one and is kept exactly: a
// refusal may only be written from a query that held ONE expression, so a
// batching artifact can never be recorded as a server refusal.
func gridServerTypes(oracle *chOracle, exprs []string) []chResult {
	results := make([]chResult, len(exprs))
	var run func(lo, hi int)
	run = func(lo, hi int) {
		parts := make([]string, 0, 2*(hi-lo))
		for _, e := range exprs[lo:hi] {
			// toTypeName names the type; ignore() makes the server RUN
			// the expression over the seeded row. Without the
			// execution witness a cell such as
			// quantileState over a Bool column is reported as a type,
			// although the server answers Code 134 when it runs.
			parts = append(parts, "toTypeName("+e+")", "ignore("+e+")")
		}
		out, err := oracle.exec("SELECT " + strings.Join(parts, ", ") + " FROM g")
		if err == nil {
			fields := strings.Split(out, "\t")
			if len(fields) == 2*(hi-lo) {
				for i := 0; i < hi-lo; i++ {
					results[lo+i] = chResult{typeName: fields[2*i]}
				}
				return
			}
			err = fmt.Errorf("expected %d fields, got %d", 2*(hi-lo), len(fields))
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

// gridChgenType asks chgen for the type of one expression over table `g`.
func gridChgenType(schema *Schema, exprSQL string) (string, error) {
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM g").ParseStmts()
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

// gridClassify gives the verdict of one cell.
//
// The EXCLUSION runs first. A cell that is illegal for a reason unrelated
// to types is not evidence about typing, and counting it would bank noise
// as coverage.
func gridClassify(record gridRecord) string {
	if reason := gridExclusionReason(record); reason != "" {
		return gridExcluded
	}
	serverRefused := record.serverCode != ""
	switch {
	case serverRefused && record.chgenRefused:
		return gridBothRefuse
	case serverRefused && !record.chgenRefused && record.serverCode == "386":
		// Code 386, NO_COMMON_TYPE, is a refusal about the enumerated
		// PAIR of arguments, not a refusal about either argument's type
		// on its own. Banking it as gridChgenTypesServerRefuses would
		// read a real, enumerated pair (has(arr_i32, arr_i32) really is
		// Code 386, and chgen really would type it if it typed the
		// pair) as a domain finding about a type. It is not: it is
		// evidence that this ONE spelled pair is incompatible, which is
		// a fact about the enumerator's choice of pair, not about the
		// entry's type rule.
		return gridChgenTypesServerRefusesPair
	case serverRefused && !record.chgenRefused:
		return gridChgenTypesServerRefuses
	case !serverRefused && record.chgenRefused:
		return gridChgenRefusesServerAccepts
	}
	if record.server == record.chgen {
		return gridAgree
	}
	return gridMismatch
}

// gridExclusionReason names why a cell carries no type evidence, or the
// empty string when the cell is evidence.
//
// Each exclusion below is a MEASURED class of server error that is about
// something other than the type of the expression. The list is closed and
// short on purpose: a wide exclusion list would let a real defect hide
// behind an error code.
func gridExclusionReason(record gridRecord) string {
	switch record.serverCode {
	case "":
		return ""
	case "184":
		// ILLEGAL_AGGREGATION. The server refuses an aggregate inside
		// an aggregate, and it refuses a state that is built in the
		// same SELECT that merges it. Measured:
		// sumMerge(sumState(i32)) is Code 184. The error is about the
		// aggregation context, not about a type.
		return "illegal aggregation context"
	case "215":
		// NOT_AN_AGGREGATE. The expression mixes an aggregate with a
		// bare column. That is a query-shape error.
		return "not an aggregate: query shape"
	case "36":
		// BAD_ARGUMENTS with a non-constant argument. A function whose
		// argument must be a literal cannot take a column, and the
		// grid puts columns in the value positions by design.
		return "argument must be a constant"
	case "6":
		// CANNOT_PARSE_TEXT. A parse function that receives a probe
		// value outside its range answers with a VALUE error, not a
		// type refusal: toInt32('abc') is Code 6 although the type
		// rule is perfectly defined. This is the exclusion that the
		// ticket names explicitly.
		return "value does not parse: a value error, not a type refusal"
	case "62":
		// SYNTAX_ERROR. The cell is not valid SQL at all, thus the
		// server never reached a type rule and the answer says nothing
		// about typing.
		//
		// Measured cause on this grid: the argSortConstString renderer
		// writes a quoted unit, which suits dateDiff('day', a, b) but
		// is wrong for toStartOfInterval, whose second argument is an
		// INTERVAL literal and not a string. The cell is an artifact
		// of the enumerator, not a finding, and it is excluded rather
		// than counted. It stays enumerated so that the count does not
		// shrink silently.
		return "syntax error: the enumerator cannot spell this call"
	case "131":
		// TOO_LARGE_STRING_SIZE. Measured on the grid fixture:
		//
		//	toFixedString(c_bare_s, 2)
		//
		// answers "String too long for type FixedString(2)". The seed
		// of the String column is 'abcdefgh', which is 8 bytes, and
		// the grid puts the constant 2 in every size position. The
		// refusal is therefore about the LENGTH of the probe value
		// and not about the type of the argument: the very same call
		// with a size of 8 is accepted and types as FixedString(8).
		// Counting it would bank a property of the seed row as
		// evidence about a type rule.
		return "probe value too long for the size constant: a value error"
	}
	return ""
}

// gridWriteGolden writes the golden file.
//
// The format is one line per cell, sorted by cell address, with tab
// separated fields. A line format keeps a diff readable: a reviewer sees
// exactly which cells moved and what the server now says about them.
//
// The header carries a census: one count per verdict, one count per
// wrapper letter and a legal_but_unenumerated count. Every number is
// DERIVED from records and probes at write time, thus byte
// reproducibility holds and a reader never has to recount by hand or
// trust a number restated from memory. The regression exists because a commit
// message of an earlier session stated a census of 4862 cells while the
// golden held 4622: the number was restated instead of quoted from the
// file. The rule this ticket sets: a commit message QUOTES the header,
// it never restates it.
func gridWriteGolden(t *testing.T, probes []gridProbe, records []gridRecord, version string) {
	t.Helper()
	var builder strings.Builder
	builder.WriteString("# chgen wrapper grid, measured golden.\n")
	builder.WriteString("# DO NOT EDIT BY HAND. Regenerate with:\n")
	builder.WriteString("#   go test -tags fuzzoracle ./internal/engine -run TestWrapperGrid \\\n")
	builder.WriteString("#       -chgen-grid-url http://localhost:18123 -chgen-grid-regenerate \\\n")
	builder.WriteString("#       -chgen-grid-i-measured-this <the server's own SELECT version()>\n")
	builder.WriteString("# Every value below is the answer of a live ClickHouse server or of\n")
	builder.WriteString("# chgen. A hand-written value is a second copy of a rule and it drifts.\n")
	builder.WriteString("# Fields: id, verdict, server_type, server_code, chgen_type, sql\n")
	builder.WriteString(fmt.Sprintf("# clickhouse_version\t%s\n", version))
	builder.WriteString(fmt.Sprintf("# cells\t%d\n", len(records)))
	for _, family := range gridFamilyCounts(records) {
		builder.WriteString(fmt.Sprintf("# family\t%s\t%d\n", family.name, family.count))
	}
	for _, verdict := range gridVerdictCounts(records) {
		builder.WriteString(fmt.Sprintf("# verdict\t%s\t%d\n", verdict.name, verdict.count))
	}
	for _, wrapper := range gridWrapperLetterCounts(records) {
		builder.WriteString(fmt.Sprintf("# wrapper\t%s\t%d\n", wrapper.name, wrapper.count))
	}
	builder.WriteString(fmt.Sprintf("# legal_but_unenumerated\t%d\n",
		gridLegalButUnenumeratedCount(probes)))
	for _, record := range records {
		chgenField := record.chgen
		if record.chgenRefused {
			chgenField = "<refused>"
		}
		serverField := record.server
		if record.serverCode != "" {
			serverField = "<refused>"
		}
		builder.WriteString(strings.Join([]string{
			record.id, record.verdict, serverField, record.serverCode,
			chgenField, record.sql,
		}, "\t"))
		builder.WriteString("\n")
	}
	path := filepath.FromSlash(gridGoldenPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create testdata directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write golden: %v", err)
	}
}

// gridFamilyCount is one per-family cell count of the golden header.
type gridFamilyCount struct {
	name  string
	count int
}

// gridFamilyCounts counts the cells per family, in sorted family order.
// The counts are in the golden header so that a grid which SHRINKS is
// visible in the diff as a smaller number, and not only as a set of
// removed lines that a reviewer must count by hand.
func gridFamilyCounts(records []gridRecord) []gridFamilyCount {
	counts := map[string]int{}
	for _, record := range records {
		counts[strings.SplitN(record.id, "/", 2)[0]]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]gridFamilyCount, 0, len(names))
	for _, name := range names {
		out = append(out, gridFamilyCount{name: name, count: counts[name]})
	}
	return out
}

// gridNamedCount is one named count of the golden header. It is reused
// for the per-verdict and the per-wrapper-letter counts: both are a tally
// over a fixed, closed name set, sorted by name for a stable header.
type gridNamedCount struct {
	name  string
	count int
}

// gridVerdictCounts counts the cells per verdict, in sorted verdict order.
//
// the regression: a census restated from memory once claimed 4862 cells while
// the golden held 4622. Putting one count per verdict in the header, next
// to the family counts that were already there, means a reader QUOTES
// the number from the file and never recounts or restates it.
func gridVerdictCounts(records []gridRecord) []gridNamedCount {
	counts := map[string]int{}
	for _, record := range records {
		counts[record.verdict]++
	}
	return gridSortedNamedCounts(counts)
}

// gridWrapperOther names the remainder line of the wrapper-letter census:
// a cell whose id does not carry exactly one wrapper letter.
const gridWrapperOther = "(no single wrapper)"

// gridWrapperLetterCounts partitions the cells by wrapper letter, in
// sorted wrapper order, plus one named remainder line so the counts
// always sum to the census.
//
// The wrapper letter is read from the cell's own ADDRESS, never from the
// SQL. An earlier version of this function counted a COLUMN MENTION in
// the SQL instead: it read gridColumnName("c_<wrapper>_<base>") out of
// the expression text and counted a cell once per DISTINCT wrapper it
// mentioned. That double-counted any cell whose SQL happens to touch a
// second column of a different wrapper for a reason that has nothing to
// do with the cell's own wrapper, for example the condition argument of
// argMaxIf(c_lc_i32, c_lc_i32, c_bare_i32 > 1): the cell is address-owned
// by "lc", not by "bare", yet the mention count added it to both. The
// mention sum came to 7394 against a census of 7072, a header that could
// not be quoted without a footnote, which is exactly the failure this
// ticket exists to end.
//
// Three id shapes carry the wrapper letter positionally at index 2 of
// the address, one wrapper per cell: "fn", "op", "simplestate" and
// "sized" (for example fn/any/bare/arr, op/AND/lc/i32). The same index
// also carries a "<wrapper>-const" spelling for a cell with one
// operand column and one constant operand (for example
// op/AND/lc-const/i32 is c_lc_i32 AND 3); that cell carries exactly the
// one wrapper named before the "-const" suffix. The "hof" family
// carries the letter inside the trailing "c_<wrapper>_<base>" segment
// of the id itself, still one wrapper per cell (for example
// hof/arrayall/predicate/c_saf_arr). All of these are read from the id
// text, not from the SQL, and all attribute exactly one wrapper per
// cell.
//
// Two shapes cannot honestly take a single wrapper letter, and their
// cells go to gridWrapperOther instead of being forced into a wrong
// bucket:
//
//   - "var" cells spell TWO operands, each carrying its own wrapper
//     letter (var/array/bare:i32+n:i32). 56 of the 126 var cells in the
//     current grid name two DIFFERENT wrappers; assigning such a cell to
//     one of the two would misattribute it to the other.
//   - a nullary cell (today(), row_number(), ...) reads no fixture
//     column of its own and its id ends in "/nullary"; it has no wrapper
//     letter to report, whether or not its SQL happens to touch a
//     column for an unrelated reason such as a predicate.
//
// A partition is what a reader assumes of a histogram, thus the counts
// here are guaranteed to sum to len(records): every record lands in
// exactly one bucket, named or gridWrapperOther.
func gridWrapperLetterCounts(records []gridRecord) []gridNamedCount {
	wrapperKeys := map[string]bool{}
	for _, wrapper := range gridWrappers {
		wrapperKeys[wrapper.key] = true
	}
	counts := map[string]int{}
	for _, record := range records {
		counts[gridWrapperLetterOfCell(record.id, wrapperKeys)]++
	}
	return gridSortedNamedCounts(counts)
}

// gridWrapperLetterOfCell reads the single wrapper letter that a cell's
// own address carries, or gridWrapperOther when the id shape carries
// none or carries more than one. See gridWrapperLetterCounts for why
// each family is handled the way it is.
func gridWrapperLetterOfCell(id string, wrapperKeys map[string]bool) string {
	if strings.HasSuffix(id, "/nullary") {
		return gridWrapperOther
	}
	parts := strings.Split(id, "/")
	switch {
	case len(parts) == 0:
		return gridWrapperOther
	case parts[0] == "var":
		// var/<entry>/<wrapper>:<base>+<wrapper>:<base>
		if len(parts) < 3 {
			return gridWrapperOther
		}
		operands := strings.SplitN(parts[2], "+", 2)
		if len(operands) != 2 {
			return gridWrapperOther
		}
		first := strings.SplitN(operands[0], ":", 2)[0]
		second := strings.SplitN(operands[1], ":", 2)[0]
		if first != second {
			return gridWrapperOther
		}
		if wrapperKeys[first] {
			return first
		}
		return gridWrapperOther
	case parts[0] == "hof":
		// hof/<entry>/<role>/c_<wrapper>_<base>
		if len(parts) < 4 {
			return gridWrapperOther
		}
		column := parts[3]
		if !strings.HasPrefix(column, "c_") {
			return gridWrapperOther
		}
		rest := strings.TrimPrefix(column, "c_")
		for _, wrapper := range gridWrappers {
			if rest == wrapper.key || strings.HasPrefix(rest, wrapper.key+"_") {
				return wrapper.key
			}
		}
		return gridWrapperOther
	case parts[0] == "fn" || parts[0] == "op" || parts[0] == "simplestate" || parts[0] == "sized":
		// <family>/<entry>/<wrapper>/<base>
		if len(parts) < 3 {
			return gridWrapperOther
		}
		if wrapperKeys[parts[2]] {
			return parts[2]
		}
		// <family>/<entry>/<wrapper>-const/<base>: one operand carries
		// the wrapper, the other is a constant. The cell still has
		// exactly one wrapper, so it is not "no single wrapper" — see
		// the package-level comment on gridWrapperLetterCounts for the
		// 32-cell defect this shape used to fall into.
		//
		// The match is against the FULL segment, "<key>-const", never
		// against a prefix: the alphabet has "saf" as a prefix of
		// "safarr", "saflc" and "safn", so trimming a trailing
		// "-const" and testing wrapperKeys[trimmed] is the only safe
		// form. Matching by "segment starts with key+\"-const\"" would
		// let "safarr-const" satisfy the "saf" prefix test too, and a
		// Go map walk order is randomised per process, so which key
		// wins would differ from run to run.
		if trimmed, ok := strings.CutSuffix(parts[2], "-const"); ok && wrapperKeys[trimmed] {
			return trimmed
		}
		return gridWrapperOther
	default:
		return gridWrapperOther
	}
}

// gridSortedNamedCounts turns a name->count map into a slice sorted by
// name, so the header is written in a fixed, reproducible order.
func gridSortedNamedCounts(counts map[string]int) []gridNamedCount {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]gridNamedCount, 0, len(names))
	for _, name := range names {
		out = append(out, gridNamedCount{name: name, count: counts[name]})
	}
	return out
}

// gridLegalButUnenumeratedCount counts the (entry, wrapper, base kind)
// triples that the closed alphabet spells but that no cell of the current
// enumeration addresses and that gridClosureExclusions does not name.
//
// This calls gridAlphabetClosure in wrappergrid_shape_test.go, the ONE
// walk that also backs TestGridAddressesTheAlphabet. Before this change
// the two files ran two separate copies of the same loop; now the golden
// header count and the always-on shape-test check read the same closure
// computation, so they cannot silently drift apart.
func gridLegalButUnenumeratedCount(probes []gridProbe) int {
	return len(gridAlphabetClosure(probes))
}

// gridCompareGolden compares the measured run against the committed
// golden and fails on any difference.
//
// liveVersion is the server version this run just measured with
// "SELECT version()" in TestWrapperGrid. gridCheckVersionGate compares it
// against the golden's own "# clickhouse_version" header BEFORE the cell
// diff runs, so a golden re-measured on the wrong server is refused even
// when every cell happens to still agree (see the regression and
// grid_version_gate_test.go).
func gridCompareGolden(t *testing.T, records []gridRecord, liveVersion string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(gridGoldenPath))
	if err != nil {
		t.Fatalf("read the golden: %v\nRegenerate it with -chgen-grid-regenerate.", err)
	}
	gridCheckVersionGate(t.Errorf, gridGoldenPath, raw, liveVersion)
	golden := map[string]string{}
	var goldenOrder []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, "\t", 6)
		if len(fields) != 6 {
			t.Fatalf("malformed golden line: %q", line)
		}
		// fields[1:6] is verdict, server_type, server_code, chgen_type,
		// sql. The SQL field MUST be part of the compared value: a cell
		// whose SPELLING drifts under a stable id must fail compare
		// mode, the gate that runs without a server and that a reviewer
		// trusts first. Without it, a drifted SQL string would pass
		// compare mode and only the regeneration diff would catch it.
		golden[fields[0]] = strings.Join(fields[1:6], "\t")
		goldenOrder = append(goldenOrder, fields[0])
	}
	measured := map[string]string{}
	for _, record := range records {
		chgenField := record.chgen
		if record.chgenRefused {
			chgenField = "<refused>"
		}
		serverField := record.server
		if record.serverCode != "" {
			serverField = "<refused>"
		}
		measured[record.id] = strings.Join([]string{
			record.verdict, serverField, record.serverCode, chgenField, record.sql,
		}, "\t")
	}
	differences := 0
	for _, id := range goldenOrder {
		got, present := measured[id]
		if !present {
			t.Errorf("cell %s is in the golden but the enumeration no longer makes it.\n"+
				"The grid SHRANK. A cell that disappears without a reason is how coverage dies.", id)
			differences++
			continue
		}
		if got != golden[id] {
			t.Errorf("cell %s changed.\n  golden:   %s\n  measured: %s", id, golden[id], got)
			differences++
		}
	}
	for _, record := range records {
		if _, present := golden[record.id]; !present {
			t.Errorf("cell %s is new and is not in the golden.\n  measured: %s",
				record.id, measured[record.id])
			differences++
		}
	}
	if differences > 0 {
		t.Errorf("%d cells differ from the golden. If the change is intended, "+
			"regenerate with -chgen-grid-regenerate and review the diff.", differences)
	}
}

// TestGridWriteGoldenHeaderCarriesTheCensus guards the regression.
//
// The golden header must carry one count per verdict, one count per
// wrapper letter and a legal_but_unenumerated count, all DERIVED from
// the records at write time. Before the fix, gridWriteGolden wrote only
// the clickhouse_version, the total cell count and the per-family
// counts: a reader who wanted the count of any one verdict had to open
// the file and count lines by hand, or trust a number restated from
// memory, which is the exact failure this ticket records (a commit
// message once stated 4862 cells while the golden held 4622).
//
// The test writes to the real gridGoldenPath, because gridGoldenPath is
// a const declared in a file this ticket must not touch. The original
// bytes are read first and restored via t.Cleanup, so the committed
// golden is never left changed by this test.
func TestGridWriteGoldenHeaderCarriesTheCensus(t *testing.T) {
	path := filepath.FromSlash(gridGoldenPath)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the committed golden before swapping it: %v", err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(path, original, 0o644); err != nil {
			t.Fatalf("restore the committed golden: %v", err)
		}
	})

	probes := []gridProbe{
		{id: "fn/probe1/bare/i32", family: "fn", entry: "probe1", sql: "toInt32(c_bare_i32)"},
		{id: "fn/probe2/lc/i32", family: "fn", entry: "probe2", sql: "toInt32(c_lc_i32)"},
	}
	records := []gridRecord{
		{id: "fn/probe1/bare/i32", sql: "toInt32(c_bare_i32)", server: "Int32", chgen: "Int32", verdict: gridAgree},
		{id: "fn/probe2/lc/i32", sql: "toInt32(c_lc_i32)", chgenRefused: true, serverCode: "43", verdict: gridBothRefuse},
	}
	gridWriteGolden(t, probes, records, "test")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the golden that gridWriteGolden just wrote: %v", err)
	}
	header := string(raw)
	for _, want := range []string{
		"# verdict\tAGREE\t1\n",
		"# verdict\tBOTH_REFUSE\t1\n",
		"# wrapper\tbare\t1\n",
		"# wrapper\tlc\t1\n",
		"# legal_but_unenumerated\t",
	} {
		if !strings.Contains(header, want) {
			t.Errorf("golden header is missing %q.\n"+
				"The header must carry a verdict count, a wrapper-letter count and "+
				"a legal_but_unenumerated count, so a reader QUOTES the census "+
				"from the file and never restates it from memory.\nheader:\n%s",
				want, header)
		}
	}
}

// TestGridWrapperLetterCountsReconcileWithTheCensus guards the regression
// against the defect the session's independent verification found: the
// wrapper-letter counts summed to 7394 against a census of 7072, a
// header that could not be quoted without a footnote.
//
// This test needs no server. It builds a small synthetic record set that
// covers every shape gridWrapperLetterOfCell must handle: a plain
// fn/op/simplestate/sized id, a hof id (wrapper inside the trailing
// column segment), a var id whose two operands SHARE a wrapper, a var id
// whose two operands carry DIFFERENT wrappers, and a nullary id. The
// property under test is that the counts always form a PARTITION: they
// sum to exactly the number of records, with no cell counted twice and
// no cell dropped.
func TestGridWrapperLetterCountsReconcileWithTheCensus(t *testing.T) {
	records := []gridRecord{
		{id: "fn/any/bare/i32", sql: "any(c_bare_i32)", verdict: gridAgree},
		{id: "op/AND/lc/i32", sql: "c_lc_i32 AND c_lc_i32", verdict: gridAgree},
		{id: "hof/arrayall/predicate/c_saf_arr", sql: "arrayAll(x -> x > 1, c_saf_arr)", verdict: gridAgree},
		// Same wrapper on both operands: attributable to that wrapper.
		{id: "var/array/n:i32+n:i32", sql: "array(c_n_i32, c_n_i32)", verdict: gridAgree},
		// Different wrappers on the two operands: no single wrapper.
		{id: "var/array/bare:i32+n:i32", sql: "array(c_bare_i32, c_n_i32)", verdict: gridAgree},
		// The condition argument brings a SECOND, unrelated wrapper's
		// column into the SQL of a cell that is address-owned by "lc":
		// the exact argMaxIf(c_lc_i32, c_lc_i32, c_bare_i32 > 1) shape
		// that the mention-based count double-booked under "lc" AND
		// "bare". This cell must count under "lc" alone.
		{id: "fn/argmaxif/lc/i32", sql: "argMaxIf(c_lc_i32, c_lc_i32, c_bare_i32 > 1)", verdict: gridAgree},
		// One operand column, one constant operand: exactly ONE
		// wrapper, not "no single wrapper". This is the regression
		// defect: before the fix this id fell into gridWrapperOther.
		{id: "op/AND/lc-const/i32", sql: "c_lc_i32 AND 3", verdict: gridAgree},
		// No fixture column at all.
		{id: "fn/today/nullary", sql: "today()", verdict: gridAgree},
	}
	counts := gridWrapperLetterCounts(records)
	sum := 0
	for _, c := range counts {
		sum += c.count
	}
	if sum != len(records) {
		t.Errorf("wrapper-letter counts sum to %d, want %d (the census).\n"+
			"The histogram must be a PARTITION: every cell lands in exactly one "+
			"bucket, named or %q, or the header cannot be quoted without a footnote.",
			sum, len(records), gridWrapperOther)
	}
	want := map[string]int{
		"bare":           1, // fn/any/bare/i32
		"lc":             3, // op/AND/lc/i32, fn/argmaxif/lc/i32, op/AND/lc-const/i32
		"saf":            1, // hof/arrayall/predicate/c_saf_arr
		"n":              1, // var/array/n:i32+n:i32 (both operands agree)
		gridWrapperOther: 2, // the mismatched var cell + the nullary cell
	}
	got := map[string]int{}
	for _, c := range counts {
		got[c.name] = c.count
	}
	for name, count := range want {
		if got[name] != count {
			t.Errorf("wrapper %q: got %d cells, want %d\nfull counts: %+v", name, got[name], count, got)
		}
	}
	for name, count := range got {
		if _, expected := want[name]; !expected && count != 0 {
			t.Errorf("unexpected wrapper bucket %q with %d cells\nfull counts: %+v", name, count, got)
		}
	}
}

// TestGridWrapperLetterOfCellRecognisesEveryConstCell guards the regression.
//
// It loops over gridWrappers itself, not over a hand-written list of the
// eight current keys. A hand-written list would duplicate the alphabet
// and would not grow when a ninth wrapper is added, so it would silently
// stop proving anything for the new key. Looping over gridWrappers means
// the day a ninth wrapper lands, this test starts covering it for free.
//
// For every key, a synthetic "op/AND/<key>-const/i32" id must resolve
// to that key, never to gridWrapperOther and never to a different key.
// The alphabet has "saf" as a literal prefix of "safarr", "saflc" and
// "safn", so this also proves the match is against the FULL segment
// ("<key>-const") and not merely a HasPrefix test: a prefix test would
// let "safarr-const" satisfy the "saf" branch too.
func TestGridWrapperLetterOfCellRecognisesEveryConstCell(t *testing.T) {
	wrapperKeys := map[string]bool{}
	for _, w := range gridWrappers {
		wrapperKeys[w.key] = true
	}
	for _, w := range gridWrappers {
		id := "op/AND/" + w.key + "-const/i32"
		got := gridWrapperLetterOfCell(id, wrapperKeys)
		if got != w.key {
			t.Errorf("id %q: got wrapper %q, want %q", id, got, w.key)
		}
	}
}

// TestGridWrapperOtherHoldsOnlyGenuinelyWrapperlessCells guards the regression.
//
// gridWrapperOther is a remainder bucket, and a remainder bucket can
// hide a labelling bug by silently absorbing any id shape the caller
// forgot to classify. This test pins the ONLY two id shapes allowed to
// land there: a "var/..." id whose two operands name DIFFERENT
// wrappers, and an id ending in "/nullary". Both are checked from the
// id's own shape, never from a hardcoded count, so a future lane that
// lands in the remainder for a new, unreviewed reason fails the build
// instead of quietly inflating "(no single wrapper)".
func TestGridWrapperOtherHoldsOnlyGenuinelyWrapperlessCells(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(gridGoldenPath))
	if err != nil {
		t.Fatalf("read the committed golden: %v", err)
	}
	wrapperKeys := map[string]bool{}
	for _, w := range gridWrappers {
		wrapperKeys[w.key] = true
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id := strings.SplitN(line, "\t", 2)[0]
		if gridWrapperLetterOfCell(id, wrapperKeys) != gridWrapperOther {
			continue
		}
		if strings.HasSuffix(id, "/nullary") {
			continue
		}
		parts := strings.Split(id, "/")
		if parts[0] == "var" && len(parts) >= 3 {
			operands := strings.SplitN(parts[2], "+", 2)
			if len(operands) == 2 {
				first := strings.SplitN(operands[0], ":", 2)[0]
				second := strings.SplitN(operands[1], ":", 2)[0]
				if first != second {
					continue
				}
			}
		}
		t.Errorf("cell %q landed in gridWrapperOther for a shape this test does not "+
			"recognise as genuinely wrapper-less. Either the id shape needs a real "+
			"branch in gridWrapperLetterOfCell, or this guard's allow-list needs to "+
			"name the new genuinely-wrapper-less shape explicitly.", id)
	}
}

// TestGridCompareGoldenCatchesSQLDrift guards the regression.
//
// gridCompareGolden must fail when a cell's SQL spelling drifts under a
// stable id, even though the verdict, the server answer, the server code
// and the chgen answer all stay the same. Before the fix, the compare
// only joined fields[1:5] (verdict, server_type, server_code, chgen_type)
// and dropped fields[5] (sql), so a drifted SQL string passed compare
// mode silently. That is a hole in the gate that runs without a server,
// which is the gate a reviewer trusts first: the regeneration diff would
// catch the drift, but compare mode would not.
//
// The test writes a synthetic one-cell golden to the real committed path,
// because gridGoldenPath is a const in a file this ticket must not touch.
// The original bytes are read first and restored via t.Cleanup, so the
// committed golden is never left changed by this test.
func TestGridCompareGoldenCatchesSQLDrift(t *testing.T) {
	path := filepath.FromSlash(gridGoldenPath)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the committed golden before swapping it: %v", err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(path, original, 0o644); err != nil {
			t.Fatalf("restore the committed golden: %v", err)
		}
	})

	synthetic := "# chgen wrapper grid, measured golden.\n" +
		"# DO NOT EDIT BY HAND. Regenerate with:\n" +
		"#   go test -tags fuzzoracle ./internal/engine -run TestWrapperGrid \\\n" +
		"#       -chgen-grid-url http://localhost:18123 -chgen-grid-regenerate\n" +
		"# clickhouse_version\ttest\n" +
		"# cells\t1\n" +
		"fn/probe/bare/i32\tAGREE\tInt32\t\tInt32\ttoInt32(c_bare_i32)\n"
	if err := os.WriteFile(path, []byte(synthetic), 0o644); err != nil {
		t.Fatalf("write synthetic golden: %v", err)
	}

	// Same id, same verdict, same server/chgen fields, DIFFERENT sql.
	drifted := []gridRecord{{
		id:     "fn/probe/bare/i32",
		sql:    "toInt32(c_bare_other)",
		server: "Int32",
		chgen:  "Int32",
	}}
	drifted[0].verdict = gridClassify(drifted[0])

	spy := &testing.T{}
	gridCompareGolden(spy, drifted, "test")
	if !spy.Failed() {
		t.Error("gridCompareGolden did not fail when the SQL field drifted under a stable id.\n" +
			"A cell whose spelling changes must fail compare mode, the gate that runs without a server.")
	}
}

// TestGridClassifyCode386IsPairEvidenceNotTypeVerdict guards the regression.
//
// It needs no server: gridClassify is a pure function of a gridRecord, and
// the defect this test proves does not need a live ClickHouse to show.
//
// Before the fix, a cell where the server refused with Code 386 and chgen
// typed the same expression was banked as gridChgenTypesServerRefuses,
// the same bucket as a real type-rule gap. That conflation fabricated
// five findings once (the safarr element defect fixed in the regression): a
// refusal about the enumerated PAIR of arguments was read as a domain
// finding about a TYPE.
func TestGridClassifyCode386IsPairEvidenceNotTypeVerdict(t *testing.T) {
	record := gridRecord{
		id:         "fn/has/bare/probe",
		sql:        "has(c_bare_arr, c_bare_arr)",
		serverCode: "386",
		chgen:      "Bool",
	}
	got := gridClassify(record)
	if got != gridChgenTypesServerRefusesPair {
		t.Errorf("Code 386 with chgen typing the cell must classify as %s, got %s.\n"+
			"A refusal about an enumerated PAIR of arguments is not a verdict about a TYPE.",
			gridChgenTypesServerRefusesPair, got)
	}

	// The exclusion list must stay closed. A blanket exclusion of 386
	// would hide a genuinely enumerated incompatible pair, which is real
	// evidence and must still classify as pair evidence, not vanish.
	if reason := gridExclusionReason(record); reason != "" {
		t.Errorf("Code 386 must not be blanket-excluded, got reason %q", reason)
	}

	// The BOTH_REFUSE path must be untouched: when chgen also refuses,
	// Code 386 stays ordinary agreement evidence between the two sides.
	bothRefuse := record
	bothRefuse.chgen = ""
	bothRefuse.chgenRefused = true
	if got := gridClassify(bothRefuse); got != gridBothRefuse {
		t.Errorf("Code 386 with chgen also refusing must stay %s, got %s", gridBothRefuse, got)
	}
}
