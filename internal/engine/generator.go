package engine

import (
	"go/token"
	"unicode"
	"unicode/utf8"
)

// Command is the generated query operation.
type Command string

const (
	CommandMany Command = "many"
	CommandOne  Command = "one"
	CommandExec Command = "exec"
)

// Param describes one positional ClickHouse placeholder. GoType is optional
// when a schema-aware parse can infer it.
type Param struct {
	GoName string
	GoType string

	// CHType is the inferred ClickHouse type. It is empty when an explicit
	// parameter annotation supplies the Go type.
	CHType CHType
}

// temporalPlan gives the range-guard walk plan of this parameter. It reports
// false when the ClickHouse type holds no temporal value.
func (p Param) temporalPlan() (temporalShape, bool) {
	shape, ok := temporalShapeOf(p.CHType)
	if !ok {
		return temporalShape{}, false
	}
	if temporalParamPlanUsesDeclaredPointerShape && shape.Nullable && p.GoType == "time.Time" {
		shape.Nullable = false
	}
	return shape, true
}

var temporalParamPlanUsesDeclaredPointerShape = true

// Result describes one selected result column. SQLName must match the SELECT
// alias exactly; GoName is the generated struct field name and GoType can be
// inferred from a schema-aware parse.
type Result struct {
	GoName  string
	SQLName string
	GoType  string

	// CHType is the inferred ClickHouse type of the selected expression.
	CHType CHType
	// Asserted marks a client result contract, checked against server metadata.
	Asserted bool
}

// temporalPlan gives the read-side check plan of this column. It reports
// false when the ClickHouse type holds no temporal value.
func (r Result) temporalPlan() (temporalShape, bool) { return temporalShapeOf(r.CHType) }

// Query is one annotated SQL statement.
type Query struct {
	Name string
	// File and Line locate the -- name annotation for this query.
	File           string
	Line           int
	Command        Command
	Params         []Param
	ExternalParams []ExternalParam
	// ParamIndexes contains one parameter index for each SQL placeholder.
	ParamIndexes   []int
	Results        []Result
	ResultCapacity string
	SQL            string
	// NamedParamNames contains one source chgen.arg name for each placeholder.
	NamedParamNames []string
	resultContracts map[string]resultTypeContract

	// batchInsert marks a fixed INSERT ... VALUES whose tuple is bare
	// placeholders only. Such a statement goes through the native
	// PrepareBatch/Append path, which keeps the sub-second fraction of a
	// time.Time that the client-side text interpolation of conn.Exec drops.
	// batchInsertTable is the "INSERT INTO <table> (<columns>)" prefix that
	// PrepareBatch needs.
	batchInsert      bool
	batchInsertTable string
}

// QueryBatchState returns the hidden batch state of a query.
func QueryBatchState(query Query) (bool, string) {
	return query.batchInsert, query.batchInsertTable
}

// SetQueryBatchState restores hidden batch state after a facade conversion.
func SetQueryBatchState(query *Query, batchInsert bool, batchInsertTable string) {
	query.batchInsert = batchInsert
	query.batchInsertTable = batchInsertTable
}

type queryBuilder struct {
	query             Query
	sqlLines          []string
	bodyStarted       bool
	uncheckedSettings []string
	// sqlLine is the 1-based line number of the first SQL body line. The raw
	// placeholder check uses it to point at the offending source line.
	sqlLine int
}

func isGoIdentifier(name string) bool {
	return name != "_" && token.IsIdentifier(name)
}

func lowerFirstIdentifier(name string) string {
	first, size := utf8.DecodeRuneInString(name)
	if size == 0 {
		return name
	}
	return string(unicode.ToLower(first)) + name[size:]
}
