package engine

import (
	"bytes"
	"fmt"
	"go/format"
	"sort"
	"strconv"
	"strings"
	"text/template"
)

// Generate returns gofmt-formatted generated Go source for queries.
func Generate(packageName string, queries []Query) ([]byte, error) {
	if !isGoIdentifier(packageName) {
		return nil, fmt.Errorf("invalid package name %q", packageName)
	}
	if len(queries) == 0 {
		return nil, fmt.Errorf("no queries to generate")
	}
	for _, query := range queries {
		if err := validateQuery(query); err != nil {
			return nil, fmt.Errorf("query %s: %w", query.Name, err)
		}
		if err := validateResultCapacity(query); err != nil {
			return nil, fmt.Errorf("query %s: %w", query.Name, err)
		}
		if strings.Contains(query.SQL, "`") {
			return nil, fmt.Errorf("query %s contains a backtick and cannot be embedded as a raw Go string", query.Name)
		}
		scalarNames := make(map[string]struct{}, len(query.Params))
		for _, param := range query.Params {
			scalarNames[param.GoName] = struct{}{}
		}
		for _, param := range query.ExternalParams {
			if _, exists := scalarNames[param.GoName]; exists {
				return nil, fmt.Errorf(
					"query %s has both scalar and external parameters named %s",
					query.Name,
					param.GoName,
				)
			}
		}
	}
	externalTables, err := collectExternalTableTypes(queries)
	if err != nil {
		return nil, err
	}

	goTypes := collectGoTypeUse(queries)

	data := templateData{
		MeasuredCHVersion:    MeasuredCHVersion,
		Package:              packageName,
		Queries:              queries,
		ExternalTables:       externalTables,
		NeedsExternalTables:  len(externalTables) > 0,
		NeedsNullableParam:   hasNullableParams(queries),
		NeedsOne:             hasCommand(queries, CommandOne),
		NeedsResultContracts: hasResultContracts(queries),
		// A result or an external-table column can need any of these types.
		// A parameter can carry every one of them except json.RawMessage,
		// which is a result-only shape.
		NeedsJSON:    goTypes.inResults("json.RawMessage"),
		NeedsTime:    goTypes.inParamsOrResults("time.Time"),
		NeedsDecimal: goTypes.inParamsOrResults("decimal.Decimal"),
		NeedsNet:     goTypes.inParamsOrResults("net.IP"),
		NeedsUUID:    goTypes.inParamsOrResults("uuid.UUID"),
		NeedsTemporalGuards: func() bool {
			for _, query := range queries {
				for _, param := range query.Params {
					if _, ok := param.temporalPlan(); ok {
						return true
					}
				}
			}
			return false
		}(),
		NeedsTemporalScanCheck: func() bool {
			for _, query := range queries {
				for _, result := range query.Results {
					if shape, ok := result.temporalPlan(); ok && shapeNeedsScanCheck(shape) {
						return true
					}
				}
			}
			return false
		}(),

		TemporalDateMin:                     chgenDateMinUnix,
		TemporalDateMax:                     chgenDateMaxUnix,
		TemporalDate32Min:                   chgenDate32MinUnix,
		TemporalDate32Max:                   chgenDate32MaxUnix,
		TemporalDateTimeMin:                 chgenDateTimeMinUnix,
		TemporalDateTimeMax:                 chgenDateTimeMaxUnix,
		TemporalDateTime64Min:               chgenDateTime64MinUnix,
		TemporalDateTime64Max:               chgenDateTime64MaxUnix,
		TemporalDateTime64MaxNanosecond:     chgenDateTime64MaxNanosecond,
		TemporalDateTime64NanoMin:           chgenDateTime64NanoMinUnix,
		TemporalDateTime64NanoMax:           chgenDateTime64NanoMaxUnix,
		TemporalDateTime64NanoMaxNanosecond: chgenDateTime64NanoMaxNanosecond,
		TemporalReadableMin:                 chgenReadableMinUnix,
		TemporalReadableMax:                 chgenReadableMaxUnix,
		TemporalReadableMaxNanosecond:       chgenReadableMaxNanosecond,
	}

	var raw bytes.Buffer
	if err := generatedTemplate.Execute(&raw, data); err != nil {
		return nil, fmt.Errorf("execute generation template: %w", err)
	}
	formatted, err := format.Source(raw.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gofmt generated source: %w\n%s", err, raw.String())
	}
	return formatted, nil
}

func validateResultCapacity(query Query) error {
	if query.ResultCapacity == "" {
		return nil
	}
	if query.Command != CommandMany {
		return fmt.Errorf("-- result-capacity is valid only for :many queries")
	}
	for _, param := range query.Params {
		if param.GoName == query.ResultCapacity && strings.HasPrefix(param.GoType, "[]") {
			return nil
		}
	}
	for _, param := range query.ExternalParams {
		if param.GoName == query.ResultCapacity {
			return nil
		}
	}
	return fmt.Errorf(
		"-- result-capacity parameter %s is not a slice parameter",
		query.ResultCapacity,
	)
}

type templateData struct {
	// MeasuredCHVersion travels into the header of the generated file, so
	// that a reader of the OUTPUT sees the version that the type rules
	// were measured on without a read of chgen. The value has one source,
	// the MeasuredCHVersion constant.
	MeasuredCHVersion string

	Package              string
	Queries              []Query
	ExternalTables       []ExternalParam
	NeedsExternalTables  bool
	NeedsNullableParam   bool
	NeedsOne             bool
	NeedsResultContracts bool
	NeedsJSON            bool
	NeedsTime            bool
	NeedsNet             bool
	NeedsUUID            bool
	NeedsDecimal         bool

	// NeedsTemporalGuards emits the write-side range guards, and
	// NeedsTemporalScanCheck emits the read-side wrap detector. The measured
	// limits travel into the generated file as named constants, so a reader of
	// the output sees the numbers without opening chgen.
	NeedsTemporalGuards    bool
	NeedsTemporalScanCheck bool

	TemporalDateMin                     int64
	TemporalDateMax                     int64
	TemporalDate32Min                   int64
	TemporalDate32Max                   int64
	TemporalDateTimeMin                 int64
	TemporalDateTimeMax                 int64
	TemporalDateTime64Min               int64
	TemporalDateTime64Max               int64
	TemporalDateTime64MaxNanosecond     int64
	TemporalDateTime64NanoMin           int64
	TemporalDateTime64NanoMax           int64
	TemporalDateTime64NanoMaxNanosecond int64
	TemporalReadableMin                 int64
	TemporalReadableMax                 int64
	TemporalReadableMaxNanosecond       int64
}

func collectExternalTableTypes(queries []Query) ([]ExternalParam, error) {
	bySchema := make(map[string]ExternalParam)
	rowTypes := make(map[string]string)
	for _, query := range queries {
		for _, external := range query.ExternalParams {
			if previousSchema, exists := rowTypes[external.RowType]; exists && previousSchema != external.SchemaName {
				return nil, fmt.Errorf(
					"external schemas %q and %q both generate Go row type %s",
					previousSchema,
					external.SchemaName,
					external.RowType,
				)
			}
			rowTypes[external.RowType] = external.SchemaName
			if previous, exists := bySchema[external.SchemaName]; exists {
				if !equalExternalColumns(previous.Columns, external.Columns) {
					return nil, fmt.Errorf("external schema %q has inconsistent column definitions", external.SchemaName)
				}
				continue
			}
			bySchema[external.SchemaName] = external
		}
	}
	result := make([]ExternalParam, 0, len(bySchema))
	for _, external := range bySchema {
		result = append(result, external)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].SchemaName < result[j].SchemaName
	})
	return result, nil
}

func equalExternalColumns(left, right []ExternalColumn) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// hasNullableParams reports whether some generated call site calls
// chgenNullableParam. Only the text path wraps a parameter: generatedParamArg
// wraps each Nullable pointer that paramArgs renders.
// path gives the pointer to batch.Append without a wrap, thus a query that
// takes that path calls the helper for no parameter. A pointer parameter that
// no call site renders also calls nothing, thus the walk follows the same
// index order that paramArgs uses.
func hasNullableParams(queries []Query) bool {
	for _, query := range queries {
		if query.batchInsert {
			continue
		}
		for _, param := range textPathParams(query) {
			if strings.HasPrefix(param.GoType, "*") {
				return true
			}
		}
	}
	return false
}

func hasCommand(queries []Query, command Command) bool {
	for _, query := range queries {
		if query.Command == command {
			return true
		}
	}
	return false
}

// textPathParams returns the parameters that paramArgs renders for one query,
// in the same order. It shares the index rule of paramArgs: an empty
// ParamIndexes means the natural order of Params, and an index out of range
// makes paramArgs render nothing at all.
func textPathParams(query Query) []Param {
	indexes := query.ParamIndexes
	if len(indexes) == 0 {
		return query.Params
	}
	params := make([]Param, 0, len(indexes))
	for _, index := range indexes {
		if index < 0 || index >= len(query.Params) {
			return nil
		}
		params = append(params, query.Params[index])
	}
	return params
}

func generatedParamArg(param Param) string {
	if strings.HasPrefix(param.GoType, "*") {
		return fmt.Sprintf("chgenNullableParam(arg.%s)", param.GoName)
	}
	return "arg." + param.GoName
}

var generatedTemplate = template.Must(template.New("chgen").Funcs(template.FuncMap{
	"lowerFirst": func(name string) string {
		return lowerFirstIdentifier(name)
	},
	"quoteSQL": func(sql string) string {
		return strings.TrimSpace(sql)
	},
	"quoteGo": strconv.Quote,
	"batchInsert": func(query Query) bool {
		return query.batchInsert
	},
	"batchInsertTable": func(query Query) string {
		return query.batchInsertTable
	},
	"paramArg": generatedParamArg,
	"paramArgs": func(query Query) []string {
		indexes := query.ParamIndexes
		if len(indexes) == 0 {
			indexes = make([]int, len(query.Params))
			for index := range indexes {
				indexes[index] = index
			}
		}
		args := make([]string, 0, len(indexes))
		for _, index := range indexes {
			if index < 0 || index >= len(query.Params) {
				return nil
			}
			args = append(args, generatedParamArg(query.Params[index]))
		}
		return args
	},
	// paramGuards renders the temporal range guards for one query. Every
	// parameter that reaches a temporal column is checked before the value
	// leaves the process, because the driver reports no error for a value that
	// the column cannot hold.
	"paramGuards": func(query Query) string {
		errReturn := func(name string) string {
			return fmt.Sprintf("return fmt.Errorf(\"%s: %%w\", %s)", query.Name, name)
		}
		if query.Command != CommandExec {
			errReturn = func(name string) string {
				if query.Command == CommandOne {
					return fmt.Sprintf("return result, fmt.Errorf(\"%s: %%w\", %s)", query.Name, name)
				}
				return fmt.Sprintf("return nil, fmt.Errorf(\"%s: %%w\", %s)", query.Name, name)
			}
		}
		var lines []string
		for _, param := range query.Params {
			shape, ok := param.temporalPlan()
			if !ok {
				continue
			}
			lines = append(lines, chgenGuardStatements(shape, "arg."+param.GoName, param.GoName, 0, "\t", errReturn)...)
		}
		return strings.Join(lines, "\n")
	},
	// resultChecks renders the read-side checks for one scanned row.
	"resultChecks": func(query Query, target string) string {
		errReturn := func(name string) string {
			if query.Command == CommandOne {
				return fmt.Sprintf("return result, fmt.Errorf(\"%s: %%w\", %s)", query.Name, name)
			}
			return fmt.Sprintf("return nil, fmt.Errorf(\"%s: %%w\", %s)", query.Name, name)
		}
		indent := "\t"
		if query.Command == CommandMany {
			indent = "\t\t"
		}
		var lines []string
		for _, result := range query.Results {
			shape, ok := result.temporalPlan()
			if !ok || !shapeNeedsScanCheck(shape) {
				continue
			}
			lines = append(lines, chgenScanCheckStatements(shape, target+"."+result.GoName, result.GoName, 0, indent, errReturn)...)
		}
		return strings.Join(lines, "\n")
	},
	// batchAppendArgs renders the Append arguments of a native batch INSERT.
	// The batch path takes the pointer of a Nullable column directly, so it
	// must not go through chgenNullableParam.
	"batchAppendArgs": func(query Query) []string {
		indexes := query.ParamIndexes
		if len(indexes) == 0 {
			indexes = make([]int, len(query.Params))
			for index := range indexes {
				indexes[index] = index
			}
		}
		args := make([]string, 0, len(indexes))
		for _, index := range indexes {
			if index < 0 || index >= len(query.Params) {
				return nil
			}
			args = append(args, "arg."+query.Params[index].GoName)
		}
		return args
	},
	"sortedImports": func(data templateData) []string {
		imports := []string{"context", "fmt"}
		if data.NeedsOne {
			imports = append(imports, "errors")
		}
		if data.NeedsResultContracts {
			imports = append(imports, "strings")
		}
		if data.NeedsJSON {
			imports = append(imports, "encoding/json")
		}
		if data.NeedsTime || data.NeedsTemporalGuards || data.NeedsTemporalScanCheck {
			imports = append(imports, "time")
		}
		if data.NeedsNet {
			imports = append(imports, "net")
		}
		sort.Strings(imports)
		return imports
	},
	"contractHelpers": func() string { return resultContractHelpers },
	"contractCheck":   renderResultContractCheck,
}).Parse(`// Code generated by chgen; DO NOT EDIT.
//
// The ClickHouse type rules that made this file were measured on
// ClickHouse {{.MeasuredCHVersion}}. Another major version can answer a
// different type. chgen works offline and cannot read the version of
// your server, thus this line tells you and enforces nothing.

package {{.Package}}

import (
{{- range sortedImports .}}
	"{{.}}"
{{- end}}

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
{{- if .NeedsDecimal}}
	"github.com/shopspring/decimal"
{{- end}}
{{- if .NeedsUUID}}
	"github.com/google/uuid"
{{- end}}
{{- if .NeedsExternalTables}}
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/ext"
{{- end}}
)

{{- if .NeedsOne}}
// ErrNoRows is returned by a :one query when ClickHouse returns no row.
// An aggregate over empty input can still return a row. Use -OrNull aggregates
// for NULL values, or HAVING count() > 0 when absence should produce ErrNoRows.
var ErrNoRows = errors.New("no rows")
{{- end}}

{{- if .NeedsResultContracts}}
{{contractHelpers}}
{{- end}}

// Queries is the generated ClickHouse query set. The target database is a
// connection property (clickhouse Auth.Database); the SQL never names it.
type Queries struct {
	conn driver.Conn
}

// Querier is the generated query-set contract used by services and tests.
type Querier interface {
{{- range .Queries}}
	{{.Name}}(ctx context.Context, arg {{.Name}}Params) {{if eq .Command "exec"}}error{{else if eq .Command "one"}}({{.Name}}Row, error){{else}}([]{{.Name}}Row, error){{end}}
{{- end}}
}

// MockQuerier is a dependency-free test double for the generated query set.
// Set only the Func fields needed by a test; an unset field returns an error.
type MockQuerier struct {
{{- range .Queries}}
	{{.Name}}Func func(ctx context.Context, arg {{.Name}}Params) {{if eq .Command "exec"}}error{{else if eq .Command "one"}}({{.Name}}Row, error){{else}}([]{{.Name}}Row, error){{end}}
{{- end}}
}

var _ Querier = (*Queries)(nil)
var _ Querier = (*MockQuerier)(nil)

{{range .Queries}}
func (m *MockQuerier) {{.Name}}(ctx context.Context, arg {{.Name}}Params) {{if eq .Command "exec"}}error{{else if eq .Command "one"}}({{.Name}}Row, error){{else}}([]{{.Name}}Row, error){{end}} {
	if m == nil || m.{{.Name}}Func == nil {
		{{- if eq .Command "exec"}}
		return fmt.Errorf("MockQuerier.{{.Name}}Func is nil")
		{{- else if eq .Command "one"}}
		var zero {{.Name}}Row
		return zero, fmt.Errorf("MockQuerier.{{.Name}}Func is nil")
		{{- else}}
		return nil, fmt.Errorf("MockQuerier.{{.Name}}Func is nil")
		{{- end}}
	}
	return m.{{.Name}}Func(ctx, arg)
}
{{end}}

// New constructs the generated query set. It panics on a nil connection so a
// wiring mistake fails at start time, not on the first query.
func New(conn driver.Conn) *Queries {
	if conn == nil {
		panic("{{.Package}}.New: ClickHouse connection is nil")
	}
	return &Queries{conn: conn}
}

{{- if .NeedsNullableParam}}
func chgenNullableParam[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}
{{- end}}

{{- if .NeedsTemporalGuards}}

// The constants below are the MEASURED range limits of the ClickHouse
// temporal types, taken from ClickHouse 25.8.29.51 through the HTTP
// interface, which does not involve the Go driver. Each limit is the last
// value that the server stores unchanged.
//
// The guards exist because clickhouse-go reports NO error for a value that
// the column cannot hold. Measured with driver v2.47.0, a DateTime64(3) write
// of 2299-12-31 stored 2106-02-07 through conn.Exec and 1900-01-01 through
// the native batch path. Both calls returned nil. The project rule is: never
// silently wrong, thus an unrepresentable value becomes an explicit error.
const (
	chgenDateMinUnix   = int64({{.TemporalDateMin}})
	chgenDateMaxUnix   = int64({{.TemporalDateMax}})
	chgenDate32MinUnix = int64({{.TemporalDate32Min}})
	chgenDate32MaxUnix = int64({{.TemporalDate32Max}})
	chgenDateTimeMinUnix = int64({{.TemporalDateTimeMin}})
	chgenDateTimeMaxUnix = int64({{.TemporalDateTimeMax}})
	chgenDateTime64MinUnix = int64({{.TemporalDateTime64Min}})
	chgenDateTime64MaxUnix = int64({{.TemporalDateTime64Max}})
	chgenDateTime64MaxNanosecond = int64({{.TemporalDateTime64MaxNanosecond}})
	chgenDateTime64NanoMinUnix = int64({{.TemporalDateTime64NanoMin}})
	chgenDateTime64NanoMaxUnix = int64({{.TemporalDateTime64NanoMax}})
	chgenDateTime64NanoMaxNanosecond = int64({{.TemporalDateTime64NanoMaxNanosecond}})
)

func chgenGuardRange(value time.Time, label, typeName string, minUnix, maxUnix int64) error {
	seconds := value.Unix()
	if seconds < minUnix || seconds > maxUnix {
		return fmt.Errorf(
			"%s: %s cannot hold %s; the representable range is %s to %s",
			label,
			typeName,
			value.UTC().Format(time.RFC3339Nano),
			time.Unix(minUnix, 0).UTC().Format(time.RFC3339),
			time.Unix(maxUnix, 0).UTC().Format(time.RFC3339),
		)
	}
	return nil
}

func chgenGuardDateTime64Range(value time.Time, label, typeName string, minUnix, maxUnix, maxNanosecond int64) error {
	seconds := value.Unix()
	nanosecond := int64(value.Nanosecond())
	if seconds < minUnix || seconds > maxUnix || (seconds == maxUnix && nanosecond > maxNanosecond) {
		return fmt.Errorf(
			"%s: %s cannot hold %s; the representable range is %s to %s",
			label,
			typeName,
			value.UTC().Format(time.RFC3339Nano),
			time.Unix(minUnix, 0).UTC().Format(time.RFC3339),
			time.Unix(maxUnix, maxNanosecond).UTC().Format(time.RFC3339Nano),
		)
	}
	return nil
}

func chgenGuardDate(value time.Time, label string) error {
	return chgenGuardRange(value, label, "Date", chgenDateMinUnix, chgenDateMaxUnix)
}

func chgenGuardDate32(value time.Time, label string) error {
	return chgenGuardRange(value, label, "Date32", chgenDate32MinUnix, chgenDate32MaxUnix)
}

func chgenGuardDateTime(value time.Time, label string) error {
	return chgenGuardRange(value, label, "DateTime", chgenDateTimeMinUnix, chgenDateTimeMaxUnix)
}

// chgenGuardDateTime64 uses the DRIVER limit, not the wider column calendar.
// A DateTime64(3) column holds up to 2299-12-31, but clickhouse-go carries
// every DateTime64 through int64 nanoseconds, so a write of 2299-12-31 stored
// 1900-01-01 00:00:00.291 with err = nil (measured, v2.47.0). Guarding at the
// column limit would pass exactly the values that corrupt.
func chgenGuardDateTime64(value time.Time, label string) error {
	return chgenGuardDateTime64Range(value, label, "DateTime64 through clickhouse-go", chgenDateTime64MinUnix, chgenDateTime64MaxUnix, chgenDateTime64MaxNanosecond)
}

// chgenGuardDateTime64Nano is precision 9, where the server itself raises
// DECIMAL_OVERFLOW past the same nanosecond limit.
func chgenGuardDateTime64Nano(value time.Time, label string) error {
	return chgenGuardDateTime64Range(value, label, "DateTime64(9)", chgenDateTime64NanoMinUnix, chgenDateTime64NanoMaxUnix, chgenDateTime64NanoMaxNanosecond)
}
{{- end}}
{{- if .NeedsTemporalScanCheck}}

// chgenCheckDateTime64ScanRange detects an out-of-range DateTime64 scan.
// clickhouse-go converts every DateTime64 through int64 nanoseconds. A stored
// instant after 2262-04-11T23:47:16.854775807Z can arrive as an unrelated
// earlier time with no error. Measured: a stored 2299-12-31 arrives as
// 1715-06-12.
//
// The arriving value does not identify the original instant. This check can
// only refuse the invalid scan. It cannot repair the value.
// It does not detect empty aggregates or missing source rows. Unix epoch is
// valid, including the default returned by min/max over empty non-null input.
// Use minOrNull/maxOrNull in SQL when an empty aggregate must become nil.
func chgenCheckDateTime64ScanRange(value time.Time, label string) error {
	if value.Unix() < chgenReadableMinUnix {
		return fmt.Errorf(
			"%s: the scanned DateTime64 %s is before %s, which means the driver wrapped a stored value that is beyond %s; the value cannot be recovered",
			label,
			value.UTC().Format(time.RFC3339Nano),
			time.Unix(chgenReadableMinUnix, 0).UTC().Format(time.RFC3339),
			time.Unix(chgenReadableMaxUnix, chgenReadableMaxNanosecond).UTC().Format(time.RFC3339Nano),
		)
	}
	return nil
}

const (
	chgenReadableMinUnix = int64({{.TemporalReadableMin}})
	chgenReadableMaxUnix = int64({{.TemporalReadableMax}})
	chgenReadableMaxNanosecond = int64({{.TemporalReadableMaxNanosecond}})
)
{{- end}}

{{range .ExternalTables}}
// {{.RowType}} is one row supplied to the {{.SchemaName}} external schema.
type {{.RowType}} struct {
{{- range .Columns}}
	{{.GoName}} {{.GoType}}
{{- end}}
}

func new{{.RowType}}ExternalTable(name string, rows []{{.RowType}}) (*ext.Table, error) {
	table, err := ext.NewTable(name,
		{{- range .Columns}}
		ext.Column({{quoteGo .SQLName}}, {{quoteGo .ClickHouseType}}),
		{{- end}}
	)
	if err != nil {
		return nil, fmt.Errorf("create external table %s: %w", name, err)
	}
	for rowIndex, row := range rows {
		if err := table.Append({{range $index, $column := .Columns}}{{if $index}}, {{end}}row.{{$column.GoName}}{{end}}); err != nil {
			return nil, fmt.Errorf("append external table %s row %d: %w", name, rowIndex, err)
		}
	}
	return table, nil
}
{{end}}

{{range .Queries}}
{{- $query := .}}
{{- $sqlName := lowerFirst .Name}}
{{- if ne .Command "exec"}}
// {{.Name}}Row is one row returned by {{.Name}}.
type {{.Name}}Row struct {
{{- range .Results}}
	{{.GoName}} {{.GoType}}
{{- end}}
}
{{- end}}

// {{.Name}}Params contains the positional arguments for {{.Name}}.
type {{.Name}}Params struct {
{{- range .Params}}
	{{.GoName}} {{.GoType}}
{{- end}}
{{- range .ExternalParams}}
	{{.GoName}} []{{.RowType}}
{{- end}}
}

const {{$sqlName}}SQL = {{printf "%c" 96}}{{quoteSQL .SQL}}{{printf "%c" 96}}
{{- if batchInsert .}}

// {{$sqlName}}BatchSQL is the prefix that PrepareBatch takes. The native batch
// path keeps the sub-second fraction of a time.Time that the client-side text
// interpolation of conn.Exec drops.
const {{$sqlName}}BatchSQL = {{quoteGo (batchInsertTable .)}}
{{- end}}

{{- if eq .Command "exec"}}
// {{.Name}} executes the annotated ClickHouse statement.
func (q *Queries) {{.Name}}(ctx context.Context, arg {{.Name}}Params) error {
	if ctx == nil {
		ctx = context.Background()
	}
	{{- if .ExternalParams}}
	externalTables := make([]*ext.Table, 0, {{len .ExternalParams}})
	{{- range $index, $external := .ExternalParams}}
	externalTable{{$index}}, err := new{{$external.RowType}}ExternalTable({{quoteGo $external.WireName}}, arg.{{$external.GoName}})
	if err != nil {
		return fmt.Errorf("{{$query.Name}}: %w", err)
	}
	externalTables = append(externalTables, externalTable{{$index}})
	{{- end}}
	ctx = clickhouse.Context(ctx, clickhouse.WithExternalTable(externalTables...))
	{{- end}}
{{- with paramGuards .}}
{{.}}
{{- end}}
	{{- if batchInsert .}}
	batch, err := q.conn.PrepareBatch(ctx, {{$sqlName}}BatchSQL)
	if err != nil {
		return fmt.Errorf("{{.Name}} prepare batch: %w", err)
	}
	if err := batch.Append({{range $index, $arg := batchAppendArgs .}}{{if $index}}, {{end}}{{$arg}}{{end}}); err != nil {
		return fmt.Errorf("{{.Name}} append: %w", err)
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("{{.Name}} send: %w", err)
	}
	return nil
	{{- else if eq (len (paramArgs .)) 0}}
	return q.conn.Exec(ctx, {{$sqlName}}SQL)
	{{- else}}
	return q.conn.Exec(ctx, {{$sqlName}}SQL{{range paramArgs .}}, {{.}}{{end}})
	{{- end}}
}
{{- else if eq .Command "one"}}
// {{.Name}} returns one typed ClickHouse row.
func (q *Queries) {{.Name}}(ctx context.Context, arg {{.Name}}Params) ({{.Name}}Row, error) {
	var result {{.Name}}Row
	if ctx == nil {
		ctx = context.Background()
	}
	{{- if .ExternalParams}}
	externalTables := make([]*ext.Table, 0, {{len .ExternalParams}})
	{{- range $index, $external := .ExternalParams}}
	externalTable{{$index}}, err := new{{$external.RowType}}ExternalTable({{quoteGo $external.WireName}}, arg.{{$external.GoName}})
	if err != nil {
		return result, fmt.Errorf("{{$query.Name}}: %w", err)
	}
	externalTables = append(externalTables, externalTable{{$index}})
	{{- end}}
	ctx = clickhouse.Context(ctx, clickhouse.WithExternalTable(externalTables...))
	{{- end}}
{{- with paramGuards .}}
{{.}}
{{- end}}
	rows, err := q.conn.Query(ctx, {{$sqlName}}SQL{{range paramArgs .}}, {{.}}{{end}})
	if err != nil {
		return result, fmt.Errorf("{{.Name}} query: %w", err)
	}
	defer rows.Close()
	{{- with contractCheck . "result"}}
{{.}}
	{{- end}}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return result, fmt.Errorf("{{.Name}} rows: %w", err)
		}
		return result, fmt.Errorf("{{.Name}}: %w", ErrNoRows)
	}
	if err := rows.Scan({{range $index, $result := .Results}}{{if $index}}, {{end}}&result.{{$result.GoName}}{{end}}); err != nil {
		return result, fmt.Errorf("{{.Name}} scan: %w", err)
	}
{{- with resultChecks . "result"}}
{{.}}
{{- end}}
	return result, nil
}
{{- else}}
// {{.Name}} returns typed ClickHouse rows.
func (q *Queries) {{.Name}}(ctx context.Context, arg {{.Name}}Params) ([]{{.Name}}Row, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	{{- if .ExternalParams}}
	externalTables := make([]*ext.Table, 0, {{len .ExternalParams}})
	{{- range $index, $external := .ExternalParams}}
	externalTable{{$index}}, err := new{{$external.RowType}}ExternalTable({{quoteGo $external.WireName}}, arg.{{$external.GoName}})
	if err != nil {
		return nil, fmt.Errorf("{{$query.Name}}: %w", err)
	}
	externalTables = append(externalTables, externalTable{{$index}})
	{{- end}}
	ctx = clickhouse.Context(ctx, clickhouse.WithExternalTable(externalTables...))
	{{- end}}
{{- with paramGuards .}}
{{.}}
{{- end}}
	rows, err := q.conn.Query(ctx, {{$sqlName}}SQL{{range paramArgs .}}, {{.}}{{end}})
	if err != nil {
		return nil, fmt.Errorf("{{.Name}} query: %w", err)
	}
	defer rows.Close()
	{{- with contractCheck . "nil"}}
{{.}}
	{{- end}}
	result := make([]{{.Name}}Row, 0{{if .ResultCapacity}}, len(arg.{{.ResultCapacity}}){{end}})
	var row {{.Name}}Row
	for rows.Next() {
		row = {{.Name}}Row{}
		if err := rows.Scan({{range $index, $result := .Results}}{{if $index}}, {{end}}&row.{{$result.GoName}}{{end}}); err != nil {
			return nil, fmt.Errorf("{{.Name}} scan: %w", err)
		}
{{- with resultChecks . "row"}}
{{.}}
{{- end}}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("{{.Name}} rows: %w", err)
	}
	return result, nil
}
{{- end}}
{{end}}
`))

// trimTrailingCommentLines removes the blank and whole-line comment lines at
// the end of a query body. Such lines usually introduce the NEXT query, and
// the parser reaches them before it sees its -- name annotation. Without this
// the prose of one query becomes part of the SQL text of the query before it.
//
// Only the tail is trimmed. A comment between two SQL lines belongs to the
// statement and stays.
//
// A line that begins with "--" inside a multi-line string literal is data, not
// a comment. A line whose PREVIOUS line ends inside an open literal is
// therefore kept, so trimming can never truncate a literal.
func trimTrailingCommentLines(lines []string) []string {
	openQuoted := openQuotedLineSet(strings.Join(lines, "\n"))
	end := len(lines)
	for end > 0 {
		index := end - 1
		if openQuoted[index-1] {
			break
		}
		trimmed := strings.TrimSpace(lines[index])
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			end--
			continue
		}
		break
	}
	return lines[:end]
}

// openQuotedLineSet reports the line indexes where a string literal or a
// quoted identifier is still OPEN at the end of the line. Such a line
// continues into the next one, so its text is data and the trailing-comment
// scan must not trim it.
//
// A literal that opens and closes on one line does not mark that line: the
// line ends outside the quotes, so a real comment after it is still a comment.
//
// The scan reuses skipSQLQuoted, the scanner that normalizeNamedArgs uses, so
// both agree on what counts as quoted text.
func openQuotedLineSet(sql string) map[int]bool {
	open := make(map[int]bool)
	line := 0
	for index := 0; index < len(sql); {
		character := sql[index]
		if character == '\'' || character == '"' || character == '`' {
			end := skipSQLQuoted(sql, index, character)
			for offset := index; offset < end; offset++ {
				if sql[offset] == '\n' {
					open[line] = true
					line++
				}
			}
			index = end
			continue
		}
		if character == '\n' {
			line++
		}
		index++
	}
	return open
}
