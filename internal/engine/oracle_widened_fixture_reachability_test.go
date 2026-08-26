//go:build fuzzoracle

package engine

// This file proves that the eleven columns the regression added to
// oracleSchemaDDL are REACHABLE by the generator, and not merely present
// in the DDL text.
//
// A type that no fixture column holds cannot be reached by any generated
// expression. Thus, the oracle is blind to it by construction. A column that
// sits in the DDL but that no lane
// ever draws would recreate the same blindness under a different name: the
// fixture would widen, the report would stay green, and nobody would learn
// that the green report still proves nothing about Variant, Dynamic, JSON,
// Nested, a named Tuple, a Map with a container value, or an extreme-scale
// Decimal.
//
// The proof runs the REAL generator, g.topLevel, over many seeds and many
// draws per seed, and checks that every new column name appears at least
// once in the rendered SQL of a generated expression. It builds the
// oracleGen the same way TestTypeOracle does (buildDrawIndex over the v3
// fixture columns), but it makes no network call: topLevel, registryDraw and
// every lane they reach are pure functions of *rand.Rand and the in-memory
// index, so this test needs no live ClickHouse server.

import (
	"math/rand"
	"strings"
	"testing"
)

// newColumnsUnderTest names the columns that the regression added to
// oracleSchemaDDL, by the name chgen's OWN schema model gives them, not
// by the name the server's system.columns reports.
//
// nst_a is the one column where the two names differ. The DDL writes
// nst_a Nested(a Int32, b String), and the server SPLITS that into two
// physical sub-columns, nst_a.a and nst_a.b (both plain Array columns;
// measured on 25.8.29.51 through system.columns). chgen's schema parser
// does not perform that split: parseCreateTable reads one ColumnDef per
// DDL entry, thus nst_a stays ONE column of type Nested in chgen's model
// (schema.go, parseCHType's NestedType branch for a non-Tuple name). This
// test therefore checks nst_a, the name chgen's own catalog holds, and
// the mismatch between the two names is recorded as a finding in the
// column's own DDL comment, not fixed here.
func newColumnsUnderTest() []string {
	return []string{
		"variant",
		"dyn",
		"js",
		"nst_a",
		"ntup",
		"m_arr",
		"m_tup",
		"dec76_0",
		"dec76_76",
	}
}

// walkExprSQL collects the rendered SQL of a *fexpr and every descendant,
// so a column reference nested inside a call is still found.
func walkExprSQL(e *fexpr, out *[]string) {
	if e == nil {
		return
	}
	*out = append(*out, e.sql)
	for _, child := range e.children {
		walkExprSQL(child, out)
	}
}

// buildCurrentGenerator constructs an oracleGen the same way TestTypeOracle does for
// current registry grammar, over the widened current fixture, with NO network call. topLevel
// and every lane it reaches read only g.r and the in-memory index built
// here, thus this is the exact draw mechanism the live fuzzer runs, minus
// the HTTP round trip that checks the answer.
func buildCurrentGenerator(t *testing.T, seed int64) *oracleGen {
	t.Helper()
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the current fixture: %v", err)
	}
	var columns []fixtureColumn
	for _, tableName := range sortedTableNames(schema.Tables) {
		table := schema.Tables[tableName]
		for _, columnName := range table.ColumnOrder {
			column := table.Columns[columnName]
			columns = append(columns, fixtureColumn{name: column.Name, columnType: column.Type})
		}
	}
	index, _ := buildComposedDrawIndex(columns, rand.New(rand.NewSource(seed)))
	return &oracleGen{
		r:                    rand.New(rand.NewSource(seed)),
		drawIndex:            index,
		drawScalar:           drawNamesOfPlace(index, placementScalar),
		drawAggregate:        drawNamesOfPlace(index, placementAggregate),
		drawWindow:           drawNamesOfPlace(index, placementWindow),
		drawIllegalScalar:    drawNamesWithIllegal(index, placementScalar),
		drawIllegalAggregate: drawNamesWithIllegal(index, placementAggregate),
	}
}

// TestWidenedFixtureColumnsAreReachable draws a large sample of top-level
// expressions over many seeds and asserts that every new column name is
// named by at least one generated expression.
//
// A miss here is itself a finding: it means the column widens the DDL but
// not the population the oracle actually samples, thus a green sweep over
// that family would still be blindness wearing the widened fixture's
// clothes.
func TestWidenedFixtureColumnsAreReachable(t *testing.T) {
	const (
		seeds        = 40
		drawsPerSeed = 4000
		exprDepth    = 3
	)
	seen := make(map[string]bool, len(newColumnsUnderTest()))
	sampleExpr := make(map[string]string)
	for seed := int64(1); seed <= seeds; seed++ {
		g := buildCurrentGenerator(t, seed)
		for i := 0; i < drawsPerSeed; i++ {
			expr := g.topLevel(exprDepth)
			var texts []string
			walkExprSQL(expr, &texts)
			for _, text := range texts {
				for _, column := range newColumnsUnderTest() {
					if seen[column] {
						continue
					}
					if referencesColumn(text, column) {
						seen[column] = true
						sampleExpr[column] = text
					}
				}
			}
		}
	}
	for _, column := range newColumnsUnderTest() {
		if !seen[column] {
			t.Errorf("column %q was never named by a generated expression over %d seeds x %d draws; "+
				"the fixture holds it but the generator never reaches it, which is the same "+
				"blindness the widened fixture was meant to remove", column, seeds, drawsPerSeed)
			continue
		}
		t.Logf("column %q reached, for example: %s", column, sampleExpr[column])
	}
}

// referencesColumn reports whether sql names column as a whole identifier,
// not as a substring of a longer name. A plain strings.Contains would let
// "m_arr" match inside a longer identifier that happens to embed it; the
// boundary check below requires that the character before and after the
// match, if any, is not an identifier character or a dot-continuation that
// would make the match part of a bigger name.
func referencesColumn(sql, column string) bool {
	idx := 0
	for {
		at := strings.Index(sql[idx:], column)
		if at < 0 {
			return false
		}
		start := idx + at
		end := start + len(column)
		beforeOK := start == 0 || !isIdentByte(sql[start-1])
		afterOK := end == len(sql) || !isIdentByte(sql[end])
		if beforeOK && afterOK {
			return true
		}
		idx = start + 1
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '.' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
