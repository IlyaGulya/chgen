//go:build fuzzoracle

package engine

import (
	"math/rand"
	"sort"
	"strings"
	"testing"
)

var samplingProfileSeeds = []int64{1, 7, 42, 99}

var curatedAcceptedLaneQuotas = map[string]int{
	"aggregate":   300,
	"arr":         50,
	"boolean":     190,
	"coverage":    400,
	"fallbackOps": 75,
	"num":         470,
	"str":         200,
	"temporal":    50,
	"tuple":       90,
	"window":      50,
}

// Each entry names one curated production and the kinds that the accepted
// population must contain.
var curatedProductionMutationKinds = map[string][]string{
	"curatedCoverage":               {"arith:%", "cmp-str:=", "cmp:=", "logic:AND", "curated-date-difference"},
	"curatedDateDifference":         {"curated-date-difference"},
	"curatedLCArithmetic":           {"arith:%", "arith:*", "arith:+", "arith:-", "arith:/"},
	"curatedLCStringComparison":     {"cmp-str:!=", "cmp-str:<", "cmp-str:<=", "cmp-str:<>", "cmp-str:=", "cmp-str:==", "cmp-str:>", "cmp-str:>="},
	"curatedPlainNumericComparison": {"cmp:!=", "cmp:<", "cmp:<=", "cmp:<>", "cmp:=", "cmp:==", "cmp:>", "cmp:>="},
	"curatedNullableLogic":          {"curated-nullable-guard", "logic:AND", "logic:OR"},
	"curatedFallbackOperators":      {"curated-op-eqeq", "curated-op-regexp", "curated-op-regexp-refused", "curated-op-cast", "curated-op-lambda-bare"},
	"curatedDoubleEquals":           {"curated-op-eqeq"},
	"curatedRegexp":                 {"curated-op-regexp", "curated-op-regexp-refused"},
	"curatedCastOperator":           {"curated-op-cast"},
	"curatedBareLambda":             {"curated-op-lambda-bare"},
	"curatedNumeric":                {"curated-decimal256-column", "curated-simpleagg-column", "curated-wide-column", "curated-case-searched", "curated-case-no-else", "curated-case-operand", "curated-tuple-element", "curated-tupleElement-fn"},
	"curatedString":                 {"curated-enum-column", "curated-case-searched-str", "curated-case-no-else-str", "curated-tuple-element-str", "curated-arrayStringConcat-lambda"},
	"curatedBoolean":                {"curated-in-subquery", "curated-in-list", "curated-enum-cmp", "curated-special-cmp", "curated-between"},
	"curatedTemporal":               {"curated-date32-column", "curated-interval:+", "curated-interval:-", "curated-tz-column", "curated-toDateTime-tz", "curated-toTimeZone", "curated-toStartOfInterval"},
	"curatedArray":                  {"curated-arrayMap-num", "curated-arrayMap-toString", "curated-arrayFilter", "curated-arrayMap-binary", "curated-arraySort-lambda"},
	"curatedAggregate":              {"curated-agg-special", "curated-uniqState", "curated-sumSimpleState", "curated-countIf"},
	"curatedWindow":                 {"curated-window-sum", "curated-window-count", "curated-window-rank", "curated-window-row_number", "curated-window-first_value"},
	"curatedTuple":                  {"curated-tuple-fn", "curated-tuple-column", "curated-tuple-single", "curated-untuple-cmp"},
}

func newCurrentOracleGenerator(t *testing.T, seed int64) *oracleGen {
	t.Helper()
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the oracle fixture: %v", err)
	}
	g := &oracleGen{r: rand.New(rand.NewSource(seed))}
	var columns []fixtureColumn
	for _, tableName := range sortedTableNames(schema.Tables) {
		table := schema.Tables[tableName]
		for _, columnName := range table.ColumnOrder {
			column := table.Columns[columnName]
			columns = append(columns, fixtureColumn{name: column.Name, columnType: column.Type})
		}
	}
	index, _ := buildComposedDrawIndex(columns, rand.New(rand.NewSource(seed)))
	g.drawIndex = index
	g.drawScalar = drawNamesOfPlace(index, placementScalar)
	g.drawAggregate = drawNamesOfPlace(index, placementAggregate)
	g.drawWindow = drawNamesOfPlace(index, placementWindow)
	g.drawIllegalScalar = drawNamesWithIllegal(index, placementScalar)
	g.drawIllegalAggregate = drawNamesWithIllegal(index, placementAggregate)
	return g
}

func walkSignalKinds(expr *fexpr, kinds map[string]int) {
	if expr == nil {
		return
	}
	kinds[expr.kind]++
	for _, child := range expr.children {
		walkSignalKinds(child, kinds)
	}
}

func sortedSignalKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestCurrentOraclePlanRefusesRetiredGrammarEnvironment(t *testing.T) {
	retiredKey := "CHGEN_ORACLE_" + "GRAMMAR"
	lookup := func(key string) (string, bool) {
		if key == retiredKey {
			return "v2", true
		}
		return "", false
	}
	_, err := currentOraclePlan(lookup)
	if err == nil || !strings.Contains(err.Error(), "retired") || !strings.Contains(err.Error(), "cannot replay") {
		t.Fatalf("retired environment key must get an explicit replay refusal: %v", err)
	}
}

func TestCurrentOraclePlanAcceptsOnlyTheCurrentProfile(t *testing.T) {
	if got, err := currentOraclePlan(func(string) (string, bool) { return "", false }); err != nil || got != currentSamplingPlanID {
		t.Fatalf("default plan: got %q, err %v", got, err)
	}
	lookup := func(key string) (string, bool) {
		if key == "CHGEN_ORACLE_PLAN" {
			return "unknown", true
		}
		return "", false
	}
	if _, err := currentOraclePlan(lookup); err == nil || !strings.Contains(err.Error(), "unknown value") {
		t.Fatalf("unknown plan must be refused: %v", err)
	}
}
