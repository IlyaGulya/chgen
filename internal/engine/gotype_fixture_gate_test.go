package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// This gate holds the fixture of the type oracle against goType.
//
// goType decides which ClickHouse types chgen can give a Go type to. The
// oracle fixture decides which types a fuzz case can reach. A type that
// goType maps and the fixture has no column for is UNREACHABLE: no fuzz
// case can touch it, thus a wrong mapping of that type stays silent.
//
// The gate reads the case set of goType with go/parser, NOT from a hand
// written list. A second list would drift, and that duplication is the
// defect the gate removes. A new case in goType with no fixture column
// fails this test, and no other file needs an edit.
//
// The gate runs in the DEFAULT go test run. The fuzzer is behind the
// fuzzoracle build tag, thus a gate inside the tagged file would be
// invisible where it matters.

// structuralGoTypeCases are the cases of goType that are wrappers or a
// deliberate refusal. A wrapper has no column of its own: it always
// carries an inner type, and the inner type is what the gate checks.
var structuralGoTypeCases = map[string]string{
	"nullable":       "a wrapper; the fixture carries ni32, nf64, ns and lcn",
	"lowcardinality": "a wrapper; the fixture carries lc and lcn",
	"array":          "a wrapper; the fixture carries arr_i and arr_s",
	"map":            "a wrapper; the fixture carries m",
	"aggregatefunction": "a measured refusal: the value has no Go representation, " +
		"thus a column would only assert the refusal that goType already documents",
}

// goTypeFixtureExclusions are the mapped types that the fixture does not
// reach yet. Every entry needs a reason. An entry that IS reachable
// fails the test, thus the list shrinks and does not rot.
var goTypeFixtureExclusions = map[string]string{
	"enum": "an alias that the server resolves: measured on 25.8.29.51, " +
		"CREATE TABLE (x Enum('a'=1,'b'=2)) gives the column type Enum8, thus a " +
		"fixture column spelled Enum could never carry the bare name; e8 measures it",
}

// goTypeCaseNames reads the case labels of the switch in goType.
func goTypeCaseNames(t *testing.T) []string {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "gotype.go", nil, 0)
	if err != nil {
		t.Fatalf("parse gotype.go: %v", err)
	}
	var names []string
	ast.Inspect(file, func(node ast.Node) bool {
		clause, ok := node.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range clause.List {
			literal, ok := expr.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				continue
			}
			names = append(names, value)
		}
		return true
	})
	if len(names) == 0 {
		t.Fatal("no case label was read from gotype.go; the extraction is broken, " +
			"thus the gate would pass without measuring anything")
	}
	return names
}

// fixtureBaseNames gives every base type name in the fixture, including
// the names inside a wrapper.
func fixtureBaseNames(t *testing.T) map[string]bool {
	t.Helper()
	schema, err := schemaFromDDLErr(t, oracleSchemaDDL)
	if err != nil {
		t.Fatalf("parse the oracle fixture: %v", err)
	}
	found := make(map[string]bool)
	var walk func(CHType)
	walk = func(columnType CHType) {
		found[columnType.normalizedName()] = true
		for _, param := range columnType.Params {
			walk(param)
		}
	}
	for _, table := range schema.Tables {
		for _, column := range table.Columns {
			walk(column.Type)
		}
	}
	return found
}

// TestEveryGoMappedTypeHasAFixtureColumn fails when goType maps a type
// that no fuzz case can reach.
func TestEveryGoMappedTypeHasAFixtureColumn(t *testing.T) {
	present := fixtureBaseNames(t)
	for _, name := range goTypeCaseNames(t) {
		if _, structural := structuralGoTypeCases[name]; structural {
			continue
		}
		if reason, excluded := goTypeFixtureExclusions[name]; excluded {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("goTypeFixtureExclusions[%q] has an empty reason; "+
					"an exclusion without a reason hides the gap it names", name)
			}
			continue
		}
		if !present[name] {
			t.Errorf("goType maps %q, and the oracle fixture has no column of that type; "+
				"add a column to oracleSchemaDDL, or add an exclusion with a reason",
				name)
		}
	}
}

// TestGoTypeFixtureExclusionsAreStillNeeded keeps the exclusion list
// honest. An entry that is reachable, or that names no case of goType,
// is dead weight, and it would hide the gap it claims to record.
func TestGoTypeFixtureExclusionsAreStillNeeded(t *testing.T) {
	present := fixtureBaseNames(t)
	cases := make(map[string]bool)
	for _, name := range goTypeCaseNames(t) {
		cases[name] = true
	}
	for name, reason := range goTypeFixtureExclusions {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("goTypeFixtureExclusions[%q] has an empty reason", name)
		}
		if !cases[name] {
			t.Errorf("goTypeFixtureExclusions has %q, which goType does not map any more; "+
				"remove the entry", name)
			continue
		}
		if present[name] {
			t.Errorf("goTypeFixtureExclusions has %q, but the fixture now HAS such a column; "+
				"remove the exclusion, because the gap it records is closed", name)
		}
	}
}
