package engine

import (
	"fmt"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

var goScalarTypes = map[string]struct{}{
	"string": {}, "bool": {},
	"int8": {}, "int16": {}, "int32": {}, "int64": {},
	"uint8": {}, "uint16": {}, "uint32": {}, "uint64": {},
	"float32": {}, "float64": {}, "time.Time": {}, "decimal.Decimal": {},
	"net.IP": {},
	// uuid.UUID is a [16]byte array, thus it is a valid Go map key as well
	// as a valid value. This is the difference from net.IP, which is a
	// byte slice and cannot be a map key.
	"uuid.UUID": {},
}

// goTypeUse records which leaf Go types the generated code mentions. It is
// the input of the import decision.
//
// The decision follows the EMITTED Go type, not the ClickHouse type. A
// `-- result:` or `-- param:` annotation can pin a Go type that the CHType
// does not imply: the example project exposes a String column as
// json.RawMessage. An import decision taken from the CHType would drop the
// encoding/json import and break the build.
type goTypeUse struct {
	// params holds the leaves that reach a query parameter. results holds the
	// leaves of a result column or of an external-table column. The split
	// exists because a parameter cannot carry json.RawMessage, which is a
	// result-only shape.
	params  map[string]struct{}
	results map[string]struct{}
}

func (u goTypeUse) inResults(goTypeName string) bool {
	_, used := u.results[goTypeName]
	return used
}

func (u goTypeUse) inParamsOrResults(goTypeName string) bool {
	if _, used := u.params[goTypeName]; used {
		return true
	}
	return u.inResults(goTypeName)
}

// collectGoTypeUse walks every generated Go type once and records its leaves.
//
// It replaces a per-import substring scan of the whole type text. The two
// agree exactly: validGoValueType admits only the forms
// `leaf`, `*leaf`, `[]T` and `map[key]T`, so every name in a generated type
// is a leaf of that type; and no leaf name that the import decision asks
// about is a substring of another leaf name or of the `[]`, `map[`, `]` and
// `*` syntax. A substring hit and a leaf hit are therefore the same event.
func collectGoTypeUse(queries []Query) goTypeUse {
	use := goTypeUse{
		params:  make(map[string]struct{}),
		results: make(map[string]struct{}),
	}
	for _, query := range queries {
		for _, param := range query.Params {
			addGoTypeLeaves(use.params, param.GoType)
		}
		for _, result := range query.Results {
			addGoTypeLeaves(use.results, result.GoType)
		}
		for _, external := range query.ExternalParams {
			for _, column := range external.Columns {
				addGoTypeLeaves(use.results, column.GoType)
			}
		}
	}
	return use
}

// addGoTypeLeaves records the leaf type names of one generated Go type. It
// walks the same grammar that validGoValueType accepts, thus an unexpected
// spelling contributes its whole text as one leaf and cannot silently drop an
// import.
func addGoTypeLeaves(leaves map[string]struct{}, goType string) {
	if goType == "" {
		return
	}
	if inner, ok := strings.CutPrefix(goType, "*"); ok {
		addGoTypeLeaves(leaves, inner)
		return
	}
	// []byte is a leaf, not a slice of the leaf `byte`.
	if goType != "[]byte" {
		if inner, ok := strings.CutPrefix(goType, "[]"); ok {
			addGoTypeLeaves(leaves, inner)
			return
		}
	}
	if rest, ok := strings.CutPrefix(goType, "map["); ok {
		if closing := strings.Index(rest, "]"); closing >= 0 {
			addGoTypeLeaves(leaves, rest[:closing])
			addGoTypeLeaves(leaves, rest[closing+1:])
			return
		}
	}
	leaves[goType] = struct{}{}
}

func validateGoType(goType string) error {
	if !validGoValueType(goType) {
		return fmt.Errorf("unsupported Go type %q", goType)
	}
	return nil
}

// validGoValueType accepts the forms that clickhouse-go can scan or bind: the
// scalar set, a pointer to a scalar for Nullable(T), slices and maps of those
// forms for Array and Map (a map key is never a pointer, because ClickHouse
// forbids a Nullable map key), plus []byte and json.RawMessage.
func validGoValueType(goType string) bool {
	switch goType {
	case "[]byte", "json.RawMessage":
		return true
	}
	if inner, ok := strings.CutPrefix(goType, "*"); ok {
		_, scalar := goScalarTypes[inner]
		return scalar
	}
	if inner, ok := strings.CutPrefix(goType, "[]"); ok {
		return validGoValueType(inner)
	}
	if rest, ok := strings.CutPrefix(goType, "map["); ok {
		closing := strings.Index(rest, "]")
		if closing < 0 {
			return false
		}
		// net.IP is a byte slice, thus it is not a valid Go map key.
		if key := rest[:closing]; key == "net.IP" {
			return false
		} else if _, scalar := goScalarTypes[key]; !scalar {
			return false
		}
		return validGoValueType(rest[closing+1:])
	}
	_, scalar := goScalarTypes[goType]
	return scalar
}

func validateQuery(query Query) error {
	return validateQueryFields(query)
}

func validateQueryFields(query Query) error {
	if query.SQL == "" {
		return fmt.Errorf("SQL body is empty")
	}
	statements, err := parseChgenStatements(query.SQL, query.Command)
	if err != nil {
		return fmt.Errorf("parse SQL: %w", err)
	}
	if len(statements) != 1 {
		return fmt.Errorf("expected exactly one SQL statement, got %d", len(statements))
	}

	if err := checkQualifiedReferences(statements[0]); err != nil {
		return err
	}

	placeholderCount := countPlaceholders(statements[0])
	if len(query.ParamIndexes) > 0 {
		if placeholderCount != len(query.ParamIndexes) {
			return fmt.Errorf("SQL has %d positional placeholders but %d parameter mappings", placeholderCount, len(query.ParamIndexes))
		}
	} else if len(query.Params) > placeholderCount {
		return fmt.Errorf("SQL has %d positional placeholders but %d -- param annotations", placeholderCount, len(query.Params))
	}

	if query.Command == CommandExec {
		return nil
	}
	selectQuery, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		return fmt.Errorf("%s query must be a SELECT, got %T", query.Command, statements[0])
	}
	if len(query.Results) > 0 {
		if len(query.Results) > len(selectQuery.SelectItems) {
			return fmt.Errorf("SELECT has %d items but %d -- result annotations", len(selectQuery.SelectItems), len(query.Results))
		}
	}

	aliases := make(map[string]struct{}, len(selectQuery.SelectItems))
	for _, item := range selectQuery.SelectItems {
		alias, err := selectItemSQLName(item)
		if err != nil {
			return err
		}
		if _, exists := aliases[alias]; exists {
			return fmt.Errorf("duplicate SELECT alias %q", alias)
		}
		aliases[alias] = struct{}{}
	}
	for _, result := range query.Results {
		if _, ok := aliases[result.SQLName]; !ok {
			return fmt.Errorf("result annotation %s references missing SELECT alias %q", result.GoName, result.SQLName)
		}
	}
	return validateResultGoNames(query.Results)
}

// validateResultGoNames applies the same uniqueness rule as the external-table
// columnGoNames check: two SQL aliases must not generate one Go field, or the
// generated file does not compile.
func validateResultGoNames(results []Result) error {
	goNames := make(map[string]string, len(results))
	for _, result := range results {
		if result.GoName == "" {
			continue
		}
		if previous, exists := goNames[result.GoName]; exists {
			return fmt.Errorf(
				"result columns %q and %q both generate Go field %s",
				previous, result.SQLName, result.GoName,
			)
		}
		goNames[result.GoName] = result.SQLName
	}
	return nil
}

func countPlaceholders(statement clickhouse.Expr) int {
	count := 0
	// clickhouse.Walk descends into InsertStmt.Values as of
	// clickhouse-sql-parser v0.5.2 (PR #284), so VALUES-tuple placeholders are
	// counted by the generic walk alongside every other placeholder.
	clickhouse.Walk(statement, func(node clickhouse.Expr) bool {
		if _, ok := node.(*clickhouse.PlaceHolder); ok {
			count++
		}
		return true
	})
	return count
}

func selectItemSQLName(item *clickhouse.SelectItem) (string, error) {
	if item.Alias != nil {
		return item.Alias.Name, nil
	}
	name, ok := directColumnName(item.Expr)
	if !ok {
		return "", fmt.Errorf("SELECT expression %q requires an explicit AS alias", clickhouse.Format(item.Expr))
	}
	return name, nil
}

func directColumnName(expression clickhouse.Expr) (string, bool) {
	switch expr := expression.(type) {
	case *clickhouse.ColumnExpr:
		return directColumnName(expr.Expr)
	case *clickhouse.Ident:
		return expr.Name, true
	case *clickhouse.Path:
		if len(expr.Fields) == 0 {
			return "", false
		}
		return expr.Fields[len(expr.Fields)-1].Name, true
	default:
		return "", false
	}
}
