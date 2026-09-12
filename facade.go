package chgen

import (
	"github.com/IlyaGulya/chgen/internal/diagnostic"
	"github.com/IlyaGulya/chgen/internal/engine"
	"github.com/IlyaGulya/chgen/internal/project"
)

// MeasuredCHVersion is the ClickHouse version used to verify the type rules.
// Generation is offline and does not check a live server version.
const MeasuredCHVersion = "25.8.29.51"

// Command is the generated query operation.
type Command string

const (
	CommandMany Command = "many"
	CommandOne  Command = "one"
	CommandExec Command = "exec"
)

// CHType is one ClickHouse type and its nested arguments.
type CHType struct {
	Name          string
	Params        []CHType
	LiteralParams []string

	// ParamNames contains Tuple element names in the order of Params.
	// An unnamed element has an empty name.
	ParamNames []string
}

func (t CHType) String() string {
	return toEngineCHType(t).String()
}

// Column is a ClickHouse column and its parsed type.
type Column struct {
	Name       string
	Type       CHType
	Insertable bool
}

// TableEngine is the part of an ENGINE clause that controls row replacement.
type TableEngine struct {
	// Name is the engine name, such as ReplacingMergeTree.
	Name string
	// Params contains the engine arguments as written.
	Params []string
	// OrderBy contains the ORDER BY expressions as written.
	OrderBy []string
}

// Table is a ClickHouse table known to the generator.
type Table struct {
	Name string
	// File and Line locate the CREATE TABLE statement for this table.
	File        string
	Line        int
	Columns     map[string]Column
	ColumnOrder []string
	// Engine is the ENGINE clause. It is nil when the DDL has no engine.
	Engine *TableEngine
}

// Schema is a catalog of ClickHouse tables.
type Schema struct {
	Tables map[string]Table
}

// SchemaCatalogs holds the two catalogs that one ordered schema input stream
// produces. Physical tables answer FROM references; External tables answer
// chgen.external(...) references. The catalogs never mix.
type SchemaCatalogs struct {
	Physical *Schema
	External *Schema
}

// ExternalColumn is one typed column in an external-table row.
type ExternalColumn struct {
	GoName         string
	SQLName        string
	GoType         string
	ClickHouseType string
}

// ExternalParam describes one request-scoped ClickHouse external table used
// by a generated query. SchemaName identifies the reusable row schema, while
// WireName is the table identifier embedded in the runtime SQL and sent with
// the native query protocol.
type ExternalParam struct {
	GoName     string
	SchemaName string
	WireName   string
	RowType    string
	Columns    []ExternalColumn
}

// Param describes one positional ClickHouse placeholder. GoType is optional
// when a schema-aware parse can infer it.
type Param struct {
	GoName string
	GoType string

	// CHType is the inferred ClickHouse type. It is empty when an explicit
	// parameter annotation supplies the Go type.
	CHType CHType
}

// Result describes one selected result column. SQLName must match the SELECT
// alias exactly; GoName is the generated struct field name and GoType can be
// inferred from a schema-aware parse.
type Result struct {
	GoName  string
	SQLName string
	GoType  string

	// CHType is the inferred ClickHouse type of the selected expression.
	CHType CHType
}

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

	batchInsert      bool
	batchInsertTable string
	assertedResults  map[string]bool
}

// Config is the parsed chgen.yaml file. All paths are resolved against the
// directory that holds the configuration file.
type Config struct {
	Path     string
	Version  int
	Packages []PackageConfig
}

// PackageConfig is one generation unit from the configuration file.
type PackageConfig struct {
	Name    string
	Output  string
	Queries []InputEntry
	Schema  []InputEntry
}

// InputEntry is one queries or schema input as written in the configuration
// file. Entry keeps the original text for error messages; Path is the entry
// resolved against the configuration file directory.
type InputEntry struct {
	Entry string
	Path  string
}

// LoadConfig reads and validates a chgen.yaml file. Relative paths in the file
// resolve against the directory that holds the file.
func LoadConfig(path string) (*Config, error) {
	config, err := project.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return fromProjectConfig(config), nil
}

// ParseSchemaCatalogs reads ordered schema files into a physical catalog and
// an external catalog. A CREATE TABLE directly after a "-- chgen:external"
// marker line declares an external row schema. Between the marker and the
// CREATE keyword only blank lines and comment lines are permitted.
func ParseSchemaCatalogs(paths []string) (*SchemaCatalogs, error) {
	catalogs, err := engine.ParseSchemaCatalogs(paths)
	if err != nil {
		return nil, err
	}
	return fromEngineSchemaCatalogs(catalogs), nil
}

// ParseQueryFiles parses ordered annotated query files against the physical
// and external catalogs of one package. Every query records the file and the
// line of its -- name annotation; a duplicate query name across the files of
// one package is an error.
func ParseQueryFiles(paths []string, catalogs *SchemaCatalogs) ([]Query, error) {
	queries, err := engine.ParseQueryFiles(paths, toEngineSchemaCatalogs(catalogs))
	if err != nil {
		return nil, err
	}
	return fromEngineQueries(queries), nil
}

// Generate returns gofmt-formatted generated Go source for queries.
func Generate(packageName string, queries []Query) ([]byte, error) {
	return engine.Generate(packageName, toEngineQueries(queries))
}

// Run generates every package declared in the configuration file at
// configPath. One run generates all packages; there is no partial mode.
func Run(configPath string) error {
	return project.Run(configPath)
}

// CheckReport describes offline generation readiness. Confirmed means the
// project passes chgen's model, not that every SQL behavior is proven or that
// the queries have been executed against a server.
type CheckReport struct {
	Status      string       `json:"status"`
	CanGenerate bool         `json:"can_generate"`
	Packages    int          `json:"packages"`
	Queries     int          `json:"queries"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Diagnostic identifies a refusal or an explicit trust boundary. Status is
// confirmed, invalid, or unknown within the stated Stage, never a claim about
// all ClickHouse semantics. An unclassified error is conservatively unknown.
type Diagnostic struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Stage   string `json:"stage"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Package string `json:"package,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Query   string `json:"query,omitempty"`
}

// ExplainError extracts structured context without parsing error text. It
// returns an empty diagnostic for nil. The original error remains usable with
// errors.Is and errors.As.
func ExplainError(err error) Diagnostic {
	return fromDiagnostic(diagnostic.Describe(err))
}

func fromDiagnostic(d diagnostic.Detail) Diagnostic {
	return Diagnostic{Code: d.Code, Status: d.Status, Stage: d.Stage, Message: d.Message, Hint: d.Hint,
		Package: d.Package, File: d.File, Line: d.Line, Query: d.Query}
}

// Check validates every input and generates every package in memory, using
// the same validation as Run. It never writes outputs or creates directories.
func Check(configPath string) (CheckReport, error) {
	report, err := project.Check(configPath)
	result := CheckReport{
		Status: report.Status, CanGenerate: report.CanGenerate,
		Packages: report.Packages, Queries: report.Queries,
		Diagnostics: make([]Diagnostic, 0, len(report.Diagnostics)),
	}
	for _, d := range report.Diagnostics {
		result.Diagnostics = append(result.Diagnostics, fromDiagnostic(d))
	}
	return result, err
}

// InferExpressionType infers one expression against one fixture DDL.
func InferExpressionType(ddl, tableName, expression string) (CHType, error) {
	value, err := engine.InferExpressionType(ddl, tableName, expression)
	if err != nil {
		return CHType{}, err
	}
	return fromEngineCHType(value), nil
}

// InferQueryResultType resolves one result from a complete SELECT statement.
func InferQueryResultType(ddl, statement string) (CHType, error) {
	value, err := engine.InferQueryResultType(ddl, statement)
	if err != nil {
		return CHType{}, err
	}
	return fromEngineCHType(value), nil
}

func fromProjectConfig(value *project.Config) *Config {
	if value == nil {
		return nil
	}
	result := &Config{Path: value.Path, Version: value.Version}
	if value.Packages != nil {
		result.Packages = make([]PackageConfig, len(value.Packages))
		for index, item := range value.Packages {
			result.Packages[index] = PackageConfig{
				Name:    item.Name,
				Output:  item.Output,
				Queries: fromProjectInputEntries(item.Queries),
				Schema:  fromProjectInputEntries(item.Schema),
			}
		}
	}
	return result
}

func fromProjectInputEntries(values []project.InputEntry) []InputEntry {
	if values == nil {
		return nil
	}
	result := make([]InputEntry, len(values))
	for index, value := range values {
		result[index] = InputEntry{Entry: value.Entry, Path: value.Path}
	}
	return result
}

func toEngineCHType(value CHType) engine.CHType {
	result := engine.CHType{
		Name:          value.Name,
		LiteralParams: cloneStrings(value.LiteralParams),
		ParamNames:    cloneStrings(value.ParamNames),
	}
	if value.Params != nil {
		result.Params = make([]engine.CHType, len(value.Params))
		for index, item := range value.Params {
			result.Params[index] = toEngineCHType(item)
		}
	}
	return result
}

func fromEngineCHType(value engine.CHType) CHType {
	result := CHType{
		Name:          value.Name,
		LiteralParams: cloneStrings(value.LiteralParams),
		ParamNames:    cloneStrings(value.ParamNames),
	}
	if value.Params != nil {
		result.Params = make([]CHType, len(value.Params))
		for index, item := range value.Params {
			result.Params[index] = fromEngineCHType(item)
		}
	}
	return result
}

func fromEngineSchemaCatalogs(value *engine.SchemaCatalogs) *SchemaCatalogs {
	if value == nil {
		return nil
	}
	return &SchemaCatalogs{
		Physical: fromEngineSchema(value.Physical),
		External: fromEngineSchema(value.External),
	}
}

func toEngineSchemaCatalogs(value *SchemaCatalogs) *engine.SchemaCatalogs {
	if value == nil {
		return nil
	}
	return &engine.SchemaCatalogs{
		Physical: toEngineSchema(value.Physical),
		External: toEngineSchema(value.External),
	}
}

func fromEngineSchema(value *engine.Schema) *Schema {
	if value == nil {
		return nil
	}
	result := &Schema{}
	if value.Tables != nil {
		result.Tables = make(map[string]Table, len(value.Tables))
		for name, table := range value.Tables {
			result.Tables[name] = fromEngineTable(table)
		}
	}
	return result
}

func toEngineSchema(value *Schema) *engine.Schema {
	if value == nil {
		return nil
	}
	result := &engine.Schema{}
	if value.Tables != nil {
		result.Tables = make(map[string]engine.Table, len(value.Tables))
		for name, table := range value.Tables {
			result.Tables[name] = toEngineTable(table)
		}
	}
	return result
}

func fromEngineTable(value engine.Table) Table {
	result := Table{
		Name:        value.Name,
		File:        value.File,
		Line:        value.Line,
		ColumnOrder: cloneStrings(value.ColumnOrder),
	}
	if value.Columns != nil {
		result.Columns = make(map[string]Column, len(value.Columns))
		for name, column := range value.Columns {
			result.Columns[name] = Column{Name: column.Name, Type: fromEngineCHType(column.Type), Insertable: column.Insertable}
		}
	}
	if value.Engine != nil {
		result.Engine = &TableEngine{Name: value.Engine.Name, Params: cloneStrings(value.Engine.Params), OrderBy: cloneStrings(value.Engine.OrderBy)}
	}
	return result
}

func toEngineTable(value Table) engine.Table {
	result := engine.Table{
		Name:        value.Name,
		File:        value.File,
		Line:        value.Line,
		ColumnOrder: cloneStrings(value.ColumnOrder),
	}
	if value.Columns != nil {
		result.Columns = make(map[string]engine.Column, len(value.Columns))
		for name, column := range value.Columns {
			result.Columns[name] = engine.Column{Name: column.Name, Type: toEngineCHType(column.Type), Insertable: column.Insertable}
		}
	}
	if value.Engine != nil {
		result.Engine = &engine.TableEngine{Name: value.Engine.Name, Params: cloneStrings(value.Engine.Params), OrderBy: cloneStrings(value.Engine.OrderBy)}
	}
	return result
}

func fromEngineQueries(values []engine.Query) []Query {
	if values == nil {
		return nil
	}
	result := make([]Query, len(values))
	for index, value := range values {
		result[index] = fromEngineQuery(value)
	}
	return result
}

func fromEngineQuery(value engine.Query) Query {
	result := Query{
		Name:            value.Name,
		File:            value.File,
		Line:            value.Line,
		Command:         Command(value.Command),
		ParamIndexes:    cloneInts(value.ParamIndexes),
		ResultCapacity:  value.ResultCapacity,
		SQL:             value.SQL,
		NamedParamNames: cloneStrings(value.NamedParamNames),
	}
	result.batchInsert, result.batchInsertTable = engine.QueryBatchState(value)
	if value.Params != nil {
		result.Params = make([]Param, len(value.Params))
		for index, item := range value.Params {
			result.Params[index] = Param{GoName: item.GoName, GoType: item.GoType, CHType: fromEngineCHType(item.CHType)}
		}
	}
	if value.Results != nil {
		result.Results = make([]Result, len(value.Results))
		for index, item := range value.Results {
			result.Results[index] = Result{GoName: item.GoName, SQLName: item.SQLName, GoType: item.GoType, CHType: fromEngineCHType(item.CHType)}
			if item.Asserted {
				if result.assertedResults == nil {
					result.assertedResults = make(map[string]bool)
				}
				result.assertedResults[item.SQLName] = true
			}
		}
	}
	if value.ExternalParams != nil {
		result.ExternalParams = make([]ExternalParam, len(value.ExternalParams))
		for index, item := range value.ExternalParams {
			result.ExternalParams[index] = fromEngineExternalParam(item)
		}
	}
	return result
}

func toEngineQueries(values []Query) []engine.Query {
	if values == nil {
		return nil
	}
	result := make([]engine.Query, len(values))
	for index, value := range values {
		result[index] = toEngineQuery(value)
	}
	return result
}

func toEngineQuery(value Query) engine.Query {
	result := engine.Query{
		Name:            value.Name,
		File:            value.File,
		Line:            value.Line,
		Command:         engine.Command(value.Command),
		ParamIndexes:    cloneInts(value.ParamIndexes),
		ResultCapacity:  value.ResultCapacity,
		SQL:             value.SQL,
		NamedParamNames: cloneStrings(value.NamedParamNames),
	}
	if value.Params != nil {
		result.Params = make([]engine.Param, len(value.Params))
		for index, item := range value.Params {
			result.Params[index] = engine.Param{GoName: item.GoName, GoType: item.GoType, CHType: toEngineCHType(item.CHType)}
		}
	}
	if value.Results != nil {
		result.Results = make([]engine.Result, len(value.Results))
		for index, item := range value.Results {
			result.Results[index] = engine.Result{
				GoName:   item.GoName,
				SQLName:  item.SQLName,
				GoType:   item.GoType,
				CHType:   toEngineCHType(item.CHType),
				Asserted: value.assertedResults[item.SQLName],
			}
		}
	}
	if value.ExternalParams != nil {
		result.ExternalParams = make([]engine.ExternalParam, len(value.ExternalParams))
		for index, item := range value.ExternalParams {
			result.ExternalParams[index] = toEngineExternalParam(item)
		}
	}
	engine.SetQueryBatchState(&result, value.batchInsert, value.batchInsertTable)
	return result
}

func fromEngineExternalParam(value engine.ExternalParam) ExternalParam {
	result := ExternalParam{GoName: value.GoName, SchemaName: value.SchemaName, WireName: value.WireName, RowType: value.RowType}
	if value.Columns != nil {
		result.Columns = make([]ExternalColumn, len(value.Columns))
		for index, column := range value.Columns {
			result.Columns[index] = ExternalColumn(column)
		}
	}
	return result
}

func toEngineExternalParam(value ExternalParam) engine.ExternalParam {
	result := engine.ExternalParam{GoName: value.GoName, SchemaName: value.SchemaName, WireName: value.WireName, RowType: value.RowType}
	if value.Columns != nil {
		result.Columns = make([]engine.ExternalColumn, len(value.Columns))
		for index, column := range value.Columns {
			result.Columns[index] = engine.ExternalColumn(column)
		}
	}
	return result
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneInts(values []int) []int {
	if values == nil {
		return nil
	}
	return append([]int(nil), values...)
}
