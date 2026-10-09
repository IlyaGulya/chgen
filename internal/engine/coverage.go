package engine

import (
	"errors"
	"fmt"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
	"github.com/IlyaGulya/chgen/internal/diagnostic"
	"github.com/IlyaGulya/chgen/internal/sqlir"
)

// CoverageStage records an observation, not a claim about unvisited stages.
type CoverageStage struct {
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type CoverageColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type SQLCoverage struct {
	Binding *sqlir.BindingReport     `json:"binding,omitempty"`
	IR      *sqlir.Document          `json:"ir,omitempty"`
	Stages  map[string]CoverageStage `json:"stages"`
	Columns []CoverageColumn         `json:"columns,omitempty"`
}

// InspectSQL measures parsing separately from catalog replay, resolution
// (binding plus inference), and Go generation. It does not write or execute SQL.
// Parser-only cases may contain complete upstream scripts, including DDL.
func InspectSQL(source, schema, sql string, parserOnly bool) SQLCoverage {
	report := SQLCoverage{Stages: make(map[string]CoverageStage)}
	for _, stage := range []string{"parse", "lower", "catalog", "bind", "resolve", "generate", "server_analysis", "type_comparison", "execution"} {
		report.Stages[stage] = CoverageStage{Status: "not_run"}
	}
	record := func(stage string, err error) bool {
		if err == nil {
			report.Stages[stage] = CoverageStage{Status: "passed"}
			return true
		}
		d := diagnostic.Describe(err)
		report.Stages[stage] = CoverageStage{Status: d.Status, Code: d.Code, Message: d.Message}
		return false
	}
	statements, err := parseChgenStatements(sql, CommandMany)
	if !record("parse", err) || parserOnly {
		return report
	}
	if len(statements) != 1 {
		record("resolve", fmt.Errorf("query coverage requires exactly one SELECT"))
		return report
	}
	query, ok := statements[0].(*clickhouse.SelectQuery)
	if !ok {
		record("resolve", fmt.Errorf("query coverage requires SELECT; use scope parse for scripts or DDL"))
		return report
	}
	ir, lowerErr := lowerSQLIR(query, sql)
	if record("lower", lowerErr) {
		report.IR = ir
	}
	catalogs := &SchemaCatalogs{Physical: &Schema{Tables: make(map[string]Table)}, External: &Schema{Tables: make(map[string]Table)}}
	if !record("catalog", applySchemaSource(catalogs, source+"/schema", schema)) {
		return report
	}
	bound, bindErr := bindIRSelect(query, catalogs.Physical, sql)
	if errors.Is(bindErr, sqlir.ErrBindingUnmodeled) {
		report.Stages["bind"] = CoverageStage{Status: "unknown", Code: "ir-binding-unmodeled", Message: bindErr.Error()}
	} else if record("bind", bindErr) {
		binding := bound.Report()
		report.Binding = &binding
	}
	queries, err := parseQueriesInFile(source, "-- name: Read :many\n"+sql, catalogs.Physical, catalogs.External)
	if !record("resolve", err) {
		return report
	}
	if len(queries) != 1 {
		record("resolve", fmt.Errorf("query coverage requires one query; annotations are not corpus SQL"))
		return report
	}
	for _, column := range queries[0].Results {
		report.Columns = append(report.Columns, CoverageColumn{Name: column.SQLName, Type: column.CHType.String()})
	}
	for _, d := range QueryDiagnostics(queries[0]) {
		if d.Status == diagnostic.Unknown {
			report.Stages["resolve"] = CoverageStage{Status: d.Status, Code: d.Code, Message: d.Message}
		}
	}
	_, err = Generate("queries", queries)
	record("generate", err)
	return report
}
