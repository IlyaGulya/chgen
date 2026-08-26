//go:build fuzzoracle

package engine

// The combinator grid.
//
// This test promotes the SAMPLED combinator lane of the tagged fuzz
// oracle (typeoracle_fuzz_test.go: combinatorDraw) to a COMMITTED,
// byte-reproducible measured golden, in the same mechanism as the
// wrapper grid (wrappergrid_test.go). It enumerates a closed cell space
// -- the suffix-by-base product of inferAggregateCombinatorType plus the
// negative dispatch cells that the regression adds -- and compares, cell by
// cell, what the SERVER answers with what chgen answers.
//
// It reuses chOracle, clickHouseErrorCode and normalizeTypeName from
// typeoracle_fuzz_test.go, which carries the same build tag, and it
// reuses the four verdict constants and gridURL/gridRegenerate flags of
// wrappergrid_test.go. It does not reuse gridMeasure, gridClassify,
// gridColumns or any other wrappergrid_* enumeration helper: this grid
// owns its own alphabet, its own fixture and its own golden file, so it
// never edits a file that another change owns.
//
// Two commands:
//
//	go test -tags fuzzoracle ./internal/engine -run TestCombinatorGrid \
//	    -chgen-grid-url http://localhost:18123
//
//	go test -tags fuzzoracle ./internal/engine -run TestCombinatorGrid \
//	    -chgen-grid-url http://localhost:18123 -chgen-grid-regenerate
//
// The first form COMPARES against the committed golden and fails on any
// difference. The second form REWRITES the golden from the server.

import (
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
)

// combGridRecord is one line of the golden file. The shape mirrors
// gridRecord in wrappergrid_test.go on purpose, so a reviewer who already
// knows that format reads this one for free.
type combGridRecord struct {
	id            string
	sql           string
	analysis      string
	analysisCode  string
	executionRan  bool
	executionCode string
	chgen         string
	chgenRefused  bool
	verdict       string
}

// combGridClassify gives the verdict of one cell. It reuses the wrapper
// grid's own verdict constants (gridAgree, gridBothRefuse,
// gridChgenRefusesServerAccepts, gridChgenTypesServerRefuses, gridMismatch)
// rather than declaring a second set of the same four spellings, so the
// two grids cannot drift into different names for the same idea.
//
// This grid has no exclusion pass and no Code-386 pair-evidence split:
// the cells are hand-designed, not enumerated from a domain closure, so
// every cell is either real type evidence or a deliberately spelled
// negative-dispatch cell, and none of the wrapper grid's batching or
// pair-enumeration artifacts can occur here.
func combGridClassify(record combGridRecord) string {
	serverRefused := record.analysisCode != "" || record.executionCode != "" || !record.executionRan
	switch {
	case serverRefused && record.chgenRefused:
		return gridBothRefuse
	case serverRefused && !record.chgenRefused:
		return gridChgenTypesServerRefuses
	case !serverRefused && record.chgenRefused:
		return gridChgenRefusesServerAccepts
	}
	if record.analysis == record.chgen {
		return gridAgree
	}
	return gridMismatch
}

func TestCombinatorGrid(t *testing.T) {
	if *gridURL == "" {
		t.Skip("-chgen-grid-url is not set; start a disposable ClickHouse to run the combinator grid")
	}
	oracle := &chOracle{url: *gridURL, client: &http.Client{Timeout: 120 * time.Second}}
	version, err := oracle.adminExec("SELECT version()")
	if err != nil {
		t.Fatalf("ClickHouse is not reachable: %v", err)
	}
	version = strings.TrimSpace(version)

	// The run owns a private database and drops it at the end. The name
	// is fixed and distinct from chgen_probe_6xl (the wrapper grid's own
	// run database), because the two grids can run in the same session.
	const runDatabase = "chgen_probe_80a"
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

	if _, err := oracle.exec(combGridSchemaDDL()); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if _, err := oracle.exec(combGridSeedRow()); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	schema, err := schemaFromDDLErr(t, combGridSchemaDDL())
	if err != nil {
		t.Fatalf("chgen schema parse: %v", err)
	}

	probes := combGridBuildProbes()
	if len(probes) == 0 {
		t.Fatal("the combinator grid enumerated no cells")
	}

	records := combGridMeasure(oracle, schema, probes)
	sort.Slice(records, func(i, j int) bool { return records[i].id < records[j].id })

	if *gridRegenerate {
		if err := gridCheckWriteVersion(*gridExpectVersion, version); err != nil {
			t.Fatalf("%v", err)
		}
		combGridWriteGolden(t, probes, records, version)
		t.Logf("combinator golden rewritten from the server: %d cells, version %s", len(records), version)
		return
	}
	combGridCompareGolden(t, records, version)
}

// combGridMeasure asks both sides for every cell. Every cell of this grid
// is an aggregate call, so unlike the wrapper grid there is no scalar
// group to keep apart: the whole batch is homogeneous.
func combGridMeasure(oracle *chOracle, schema *Schema, probes []combGridProbe) []combGridRecord {
	records := make([]combGridRecord, len(probes))
	for i, probe := range probes {
		record := combGridRecord{id: probe.id, sql: probe.sql}
		analysis, analysisErr := combGridAnalysisWitness(oracle, probe.sql)
		if analysisErr != nil {
			record.analysisCode = clickHouseErrorCode(analysisErr.Error())
		} else {
			record.analysis = normalizeTypeName(analysis)
		}
		if _, executionErr := oracle.exec("SELECT ignore(" + probe.sql + ") FROM cg FORMAT Null"); executionErr != nil {
			record.executionCode = clickHouseErrorCode(executionErr.Error())
		} else {
			record.executionRan = true
		}
		inferred, err := combGridChgenType(schema, probe.sql)
		if err != nil {
			record.chgenRefused = true
		} else {
			record.chgen = normalizeTypeName(inferred)
		}
		record.verdict = combGridClassify(record)
		records[i] = record
	}
	return records
}

// combGridAnalysisWitness asks only for query analysis. The execution witness
// uses a separate request in combGridMeasure, so one channel can disagree with
// the other and the golden keeps that disagreement.
func combGridAnalysisWitness(oracle *chOracle, expression string) (string, error) {
	out, err := oracle.exec("DESCRIBE TABLE (SELECT " + expression + " AS result FROM cg) FORMAT TabSeparatedRaw")
	if err != nil {
		return "", err
	}
	fields := strings.Split(out, "\t")
	if len(fields) < 2 {
		return "", fmt.Errorf("analysis witness returned %q", out)
	}
	return fields[1], nil
}

// combGridChgenType asks chgen for the type of one expression over table
// `cg`.
func combGridChgenType(schema *Schema, exprSQL string) (string, error) {
	statements, err := clickhouse.NewParser("SELECT " + exprSQL + " FROM cg").ParseStmts()
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

// combGridWriteGolden writes the golden file. The format mirrors
// gridWriteGolden in wrappergrid_test.go: one line per cell, sorted by
// cell address, tab separated, with a header that carries a census
// derived from the records at write time, so a reader QUOTES the numbers
// instead of restating them (the regression rule).
func combGridWriteGolden(t *testing.T, probes []combGridProbe, records []combGridRecord, version string) {
	t.Helper()
	var builder strings.Builder
	builder.WriteString("# chgen combinator grid, measured golden.\n")
	builder.WriteString("# DO NOT EDIT BY HAND. Regenerate with:\n")
	builder.WriteString("#   go test -tags fuzzoracle ./internal/engine -run TestCombinatorGrid \\\n")
	builder.WriteString("#       -chgen-grid-url http://localhost:18123 -chgen-grid-regenerate \\\n")
	builder.WriteString("#       -chgen-grid-i-measured-this <the server's own SELECT version()>\n")
	builder.WriteString("# Every value below is the answer of a live ClickHouse server or of\n")
	builder.WriteString("# chgen. A hand-written value is a second copy of a rule and it drifts.\n")
	builder.WriteString("# Fields: id, verdict, analysis_type, analysis_code, execution_ran, execution_code, chgen_type, sql\n")
	builder.WriteString(fmt.Sprintf("# clickhouse_version\t%s\n", version))
	builder.WriteString(fmt.Sprintf("# cells\t%d\n", len(records)))
	for _, family := range combGridFamilyCounts(records) {
		builder.WriteString(fmt.Sprintf("# family\t%s\t%d\n", family.name, family.count))
	}
	for _, verdict := range combGridVerdictCounts(records) {
		builder.WriteString(fmt.Sprintf("# verdict\t%s\t%d\n", verdict.name, verdict.count))
	}
	for _, skip := range combGridSortedSkippedCells() {
		builder.WriteString(fmt.Sprintf("# skipped\t%s\t%s\n", skip.name, skip.reason))
	}
	for _, record := range records {
		chgenField := record.chgen
		if record.chgenRefused {
			chgenField = "<refused>"
		}
		analysisField := record.analysis
		if record.analysisCode != "" {
			analysisField = "<refused>"
		}
		builder.WriteString(strings.Join([]string{
			record.id, record.verdict, analysisField, record.analysisCode,
			strconv.FormatBool(record.executionRan), record.executionCode, chgenField, record.sql,
		}, "\t"))
		builder.WriteString("\n")
	}
	path := filepath.FromSlash(combGridGoldenPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create testdata directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write golden: %v", err)
	}
}

// combGridFamilyCounts counts the cells per family (product, negative),
// in sorted family order.
func combGridFamilyCounts(records []combGridRecord) []gridNamedCount {
	counts := map[string]int{}
	for _, record := range records {
		counts[strings.SplitN(record.id, "/", 2)[0]]++
	}
	return gridSortedNamedCounts(counts)
}

// combGridVerdictCounts counts the cells per verdict, in sorted verdict
// order, so the header can be quoted rather than restated.
func combGridVerdictCounts(records []combGridRecord) []gridNamedCount {
	counts := map[string]int{}
	for _, record := range records {
		counts[record.verdict]++
	}
	return gridSortedNamedCounts(counts)
}

// combGridCompareGolden compares the measured run against the committed
// golden AND against the alphabet's closure, never against the golden's
// own header alone.
//
// The comparison happens in three parts:
//
//  1. Every id in the golden must still be produced by the live
//     enumeration, with the identical verdict/server/chgen/sql fields.
//  2. Every id the live enumeration produces must already be in the
//     golden.
//  3. combGridSkippedProductCells, which is INDEPENDENT of the golden
//     (it is a pure function of combGridBases and combGridMergeStateColumn,
//     read fresh on every run) must still explain every currently
//     unspelled cell of the closed suffix-by-base product. A blessed
//     regeneration of the golden CANNOT silence this check, because it
//     never reads the golden file: it is compiled against the live
//     product and the live skip list, which is exactly what the ticket's
//     "compare against the alphabet, not the golden's own header" rule
//     asks for.
//
// liveVersion is the server version this run just measured with
// "SELECT version()" in TestCombinatorGrid. The same gridCheckVersionGate
// that guards the wrapper grid guards this golden too, so the two grids
// share one version rule instead of drifting into two (see
// grid_version_gate_test.go and the regression).
func combGridCompareGolden(t *testing.T, records []combGridRecord, liveVersion string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(combGridGoldenPath))
	if err != nil {
		t.Fatalf("read the golden: %v\nRegenerate it with -chgen-grid-regenerate.", err)
	}
	gridCheckVersionGate(t.Errorf, combGridGoldenPath, raw, liveVersion)
	golden := map[string]string{}
	var goldenOrder []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, "\t", 8)
		if len(fields) != 8 {
			t.Fatalf("malformed golden line: %q", line)
		}
		golden[fields[0]] = strings.Join(fields[1:8], "\t")
		goldenOrder = append(goldenOrder, fields[0])
	}
	measured := map[string]string{}
	for _, record := range records {
		chgenField := record.chgen
		if record.chgenRefused {
			chgenField = "<refused>"
		}
		analysisField := record.analysis
		if record.analysisCode != "" {
			analysisField = "<refused>"
		}
		measured[record.id] = strings.Join([]string{
			record.verdict, analysisField, record.analysisCode,
			strconv.FormatBool(record.executionRan), record.executionCode, chgenField, record.sql,
		}, "\t")
	}
	differences := 0
	for _, id := range goldenOrder {
		got, present := measured[id]
		if !present {
			t.Errorf("cell %s is in the golden but the enumeration no longer makes it.\n"+
				"The combinator grid SHRANK.", id)
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
		t.Errorf("%d cells differ from the combinator golden. If the change is intended, "+
			"regenerate with -chgen-grid-regenerate and review the diff.", differences)
	}

	// Part 3: the alphabet closure check, computed live and never read
	// from the golden.
	live := map[string]bool{}
	for _, base := range combGridBases() {
		for _, suffix := range combGridSuffixes() {
			if _, ok := combGridRenderProduct(base, suffix); ok {
				live[base+"/"+suffix] = true
			}
		}
	}
	skipped := combGridSkippedProductCells()
	for pair := range skipped {
		if live[pair] {
			t.Errorf("combGridSkippedProductCells names %q as unspellable, "+
				"but the live product now spells it; the skip list is stale", pair)
		}
	}
	for _, base := range combGridBases() {
		for _, suffix := range combGridSuffixes() {
			pair := base + "/" + suffix
			if live[pair] {
				continue
			}
			if _, named := skipped[pair]; !named {
				t.Errorf("(%s, %s) is a legal cell of the closed suffix-by-base product "+
					"that the grid does not spell and combGridSkippedProductCells does not "+
					"name; a regeneration of the golden cannot fix this", base, suffix)
			}
		}
	}
}
