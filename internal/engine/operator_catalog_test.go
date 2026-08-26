package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This is the pin of the operator catalog.
//
// The catalog moved the operator set out of the case labels of the switch
// statements in inferBinaryOperationType. The move has one dangerous
// failure mode: a token that lands in the WRONG FAMILY. A token that is
// LOST from the catalog is safe, because it reaches the refusal at the
// end of the function, and a refusal is an honest answer. But a token in
// the wrong family takes the wrong rule body and gives a WRONG TYPE. For
// example REGEXP filed as a plain predicate would lose its operand
// domain, thus "i64 REGEXP 'a'" would answer UInt8 where the server
// answers code 43.
//
// The pin reads the case labels from the source with go/parser and holds
// them against the catalog. Thus the two cannot drift apart in silence.

// operatorCaseLabels reads the string case labels of the switch
// statements inside inferBinaryOperationType.
func operatorCaseLabels(t *testing.T) map[string]bool {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "infer_expr.go", nil, 0)
	if err != nil {
		t.Fatalf("parse infer_expr.go: %v", err)
	}
	var target *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "inferBinaryOperationType" {
			target = function
			break
		}
	}
	if target == nil {
		t.Fatal("inferBinaryOperationType was not found in infer_expr.go; " +
			"the pin cannot measure anything, thus it would pass without proof")
	}
	labels := make(map[string]bool)
	ast.Inspect(target, func(node ast.Node) bool {
		clause, ok := node.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expression := range clause.List {
			literal, ok := expression.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				continue
			}
			// A switch on a base type name is not an operator
			// switch. An operator token is upper case or a
			// symbol; a base type name is lower case.
			if value != strings.ToUpper(value) {
				continue
			}
			labels[value] = true
		}
		return true
	})
	if len(labels) == 0 {
		t.Fatal("no operator case label was read; the extraction is broken")
	}
	return labels
}

func catalogTokens() map[string]bool {
	tokens := make(map[string]bool)
	for _, spec := range operatorCatalog {
		tokens[spec.token] = true
	}
	return tokens
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestOperatorCatalogMatchesTheSwitch fails when the catalog and the
// switch labels disagree. The concatenation operator is the one expected
// difference: it has its own branch and no case label.
func TestOperatorCatalogMatchesTheSwitch(t *testing.T) {
	labels := operatorCaseLabels(t)
	tokens := catalogTokens()

	// "||" reaches its rule through an if branch and not through a
	// case label, thus the source holds no label for it.
	labelExceptions := map[string]string{
		"||": "the concatenation operator has its own branch, not a case label",
	}

	for token := range tokens {
		if labels[token] {
			continue
		}
		if _, expected := labelExceptions[token]; expected {
			continue
		}
		t.Errorf("operatorCatalog holds %q, and inferBinaryOperationType has no case label for it; "+
			"the catalog and the switch drifted apart", token)
	}
	for label := range labels {
		if tokens[label] {
			continue
		}
		t.Errorf("inferBinaryOperationType has a case label %q that operatorCatalog does not hold; "+
			"add the token with its family, or the generator can never emit it", label)
	}
	t.Logf("catalog tokens: %v", sortedKeys(tokens))
}

// TestOperatorCatalogFamiliesAreMeasured checks the explicit facts that the
// semantic builder needs. The generated digest pins the full typed source, so
// this test does not keep a second token-to-family map.
func TestOperatorCatalogFamiliesAreMeasured(t *testing.T) {
	for _, spec := range operatorCatalog {
		if spec.family == opFamilyUnknown || spec.evidence == "" {
			t.Errorf("operatorCatalog has an incomplete measured entry for %q", spec.token)
		}
	}
}

// TestRegexpKeepsItsOperandDomain pins the one operator whose domain the
// catalog carries. REGEXP without its domain would answer UInt8 for an
// operand that the server refuses with code 43.
func TestRegexpKeepsItsOperandDomain(t *testing.T) {
	for _, spec := range operatorCatalog {
		if spec.token != "REGEXP" {
			continue
		}
		if spec.operandDomain == nil {
			t.Fatal("REGEXP lost its operand domain; the server refuses most operand types " +
				"with code 43, thus chgen would give a wrong type")
		}
		// Measured on ClickHouse 25.8.29.51.
		if !spec.operandDomain(CHType{Name: "String"}) {
			t.Error("REGEXP must accept String")
		}
		if spec.operandDomain(CHType{Name: "Int64"}) {
			t.Error("REGEXP must refuse Int64: the server answers code 43")
		}
		return
	}
	t.Fatal("operatorCatalog has no REGEXP entry")
}
