package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Input is one deterministic conformance cell.
type Input struct {
	ID                     string
	Expression             string
	Table                  string
	Query                  string
	StatementTemplate      string
	Scope                  string
	StatementShape         string
	Fixture                string
	Expectation            Expectation
	KnownGap               string
	ExpectedChgenError     string
	ExpectedChgenResult    string
	ExpectedAnalysisCode   int
	ExpectedAnalysisError  string
	ExpectedAnalysisResult string
	ExpectedExecutionCode  int
	ExpectedExecutionError string
}

// TypeResult is one inference or analysis answer.
type TypeResult struct {
	Raw       string       `json:"raw,omitempty"`
	Canonical *Type        `json:"canonical,omitempty"`
	Results   []ResultType `json:"results,omitempty"`
	Error     string       `json:"error,omitempty"`
	ErrorCode int          `json:"error_code,omitempty"`
}

// ResultType records one named result in statement projection order.
type ResultType struct {
	Name      string `json:"name"`
	Raw       string `json:"raw"`
	Canonical Type   `json:"canonical"`
}

// RawResultType is one result before canonical type parsing.
type RawResultType struct {
	Name string
	Type string
}

// ExecutionResult records the independent value execution witness.
type ExecutionResult struct {
	Ran       bool   `json:"ran"`
	Error     string `json:"error,omitempty"`
	ErrorCode int    `json:"error_code,omitempty"`
}

// NativeResult records a generated native-protocol value when the lane
// supports one.
type NativeResult struct {
	Supported bool   `json:"supported"`
	Value     any    `json:"value,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Cell records all four independent conformance lanes.
type Cell struct {
	ID                     string          `json:"id"`
	Expression             string          `json:"expression"`
	Table                  string          `json:"table,omitempty"`
	Query                  string          `json:"query,omitempty"`
	StatementTemplate      string          `json:"statement_template,omitempty"`
	Scope                  string          `json:"scope,omitempty"`
	StatementShape         string          `json:"statement_shape,omitempty"`
	Fixture                string          `json:"fixture,omitempty"`
	Expectation            Expectation     `json:"expectation,omitempty"`
	KnownGap               string          `json:"known_gap,omitempty"`
	ExpectedChgenError     string          `json:"expected_chgen_error,omitempty"`
	ExpectedChgenResult    string          `json:"expected_chgen_result,omitempty"`
	ExpectedAnalysisCode   int             `json:"expected_analysis_code,omitempty"`
	ExpectedAnalysisError  string          `json:"expected_analysis_error,omitempty"`
	ExpectedAnalysisResult string          `json:"expected_analysis_result,omitempty"`
	ExpectedExecutionCode  int             `json:"expected_execution_code,omitempty"`
	ExpectedExecutionError string          `json:"expected_execution_error,omitempty"`
	Chgen                  TypeResult      `json:"chgen"`
	Analysis               TypeResult      `json:"server_analysis"`
	Execution              ExecutionResult `json:"server_execution"`
	Native                 NativeResult    `json:"native"`
}

// ServerInfo identifies one ClickHouse process.
type ServerInfo struct {
	Version   string `json:"version"`
	ServerRun int64  `json:"server_run"`
	UptimeS   int64  `json:"server_uptime_s"`
}

// Metadata makes two deterministic runs comparable.
type Metadata struct {
	Server         ServerInfo      `json:"server"`
	FixtureHash    string          `json:"fixture_hash"`
	SeedHash       string          `json:"seed_hash"`
	MatrixHash     string          `json:"matrix_hash"`
	FixtureColumns []FixtureColumn `json:"fixture_columns"`
}

// Report is one deterministic matrix run.
type Report struct {
	Metadata Metadata `json:"metadata"`
	Cells    []Cell   `json:"cells"`
}

// InferLane computes the chgen type for one expression.
type InferLane func(Input) (string, error)

// InferResultsLane computes all statement results in projection order.
type InferResultsLane func(Input) ([]RawResultType, error)

// NativeLane reads a value through generated native code.
type NativeLane func(context.Context, Input) NativeResult

// Server provides independent analysis and execution witnesses.
type Server interface {
	Info(context.Context) (ServerInfo, error)
	Analyze(context.Context, Input) (string, error)
	Execute(context.Context, Input) error
}

type resultServer interface {
	AnalyzeResults(context.Context, Input) ([]RawResultType, error)
}

// Runner executes one deterministic matrix.
type Runner struct {
	Server       Server
	Infer        InferLane
	InferResults InferResultsLane
	Native       NativeLane
}

// Measure executes all available lanes for one cell. It does not read
// metadata. A harness can use it inside a larger server session.
func (r Runner) Measure(ctx context.Context, input Input) (Cell, error) {
	if r.Server == nil || r.Infer == nil && r.InferResults == nil {
		return Cell{}, fmt.Errorf("conformance runner needs server and inference lanes")
	}
	cell := cellFromInput(input)
	if input.Query != "" && r.InferResults != nil {
		results, err := r.InferResults(input)
		if err != nil {
			cell.Chgen.Error = err.Error()
		} else {
			cell.Chgen = canonicalResults(results)
		}
	} else if raw, err := r.Infer(input); err != nil {
		cell.Chgen.Error = err.Error()
	} else {
		cell.Chgen = canonicalResult(raw)
	}
	if input.Query != "" {
		server, ok := r.Server.(resultServer)
		if !ok {
			return Cell{}, fmt.Errorf("statement conformance server has no result-vector analysis")
		}
		results, err := server.AnalyzeResults(ctx, input)
		if err != nil {
			cell.Analysis = TypeResult{Error: err.Error(), ErrorCode: ErrorCode(err)}
		} else {
			cell.Analysis = canonicalResults(results)
		}
	} else if raw, err := r.Server.Analyze(ctx, input); err != nil {
		cell.Analysis = TypeResult{Error: err.Error(), ErrorCode: ErrorCode(err)}
	} else {
		cell.Analysis = canonicalResult(raw)
	}
	if err := r.Server.Execute(ctx, input); err != nil {
		cell.Execution = ExecutionResult{Error: err.Error(), ErrorCode: ErrorCode(err)}
	} else {
		cell.Execution.Ran = true
	}
	if r.Native != nil {
		cell.Native = r.Native(ctx, input)
	}
	return cell, nil
}

// BatchLanes lets a harness keep its safe batching and bisection while the
// shared runner owns the cell shape, canonicalization, metadata, and ordering.
type BatchLanes struct {
	Info    func(context.Context) (ServerInfo, error)
	Infer   func([]Input) []TypeResult
	Analyze func(context.Context, []Input) []TypeResult
	Execute func(context.Context, []Input) []ExecutionResult
	Native  func(context.Context, []Input) []NativeResult
}

// Run executes every lane for every cell in stable ID order.
func (r Runner) Run(ctx context.Context, ddl, seed string, inputs []Input) (Report, error) {
	if r.Server == nil || r.Infer == nil && r.InferResults == nil {
		return Report{}, fmt.Errorf("conformance runner needs server and inference lanes")
	}
	lanes := BatchLanes{
		Info: r.Server.Info,
		Infer: func(ordered []Input) []TypeResult {
			results := make([]TypeResult, len(ordered))
			for index, input := range ordered {
				if input.Query != "" && r.InferResults != nil {
					raw, laneErr := r.InferResults(input)
					if laneErr != nil {
						results[index].Error = laneErr.Error()
					} else {
						results[index] = canonicalResults(raw)
					}
					continue
				}
				if raw, laneErr := r.Infer(input); laneErr != nil {
					results[index].Error = laneErr.Error()
				} else {
					results[index] = canonicalResult(raw)
				}
			}
			return results
		},
		Analyze: func(ctx context.Context, ordered []Input) []TypeResult {
			results := make([]TypeResult, len(ordered))
			for index, input := range ordered {
				if input.Query != "" {
					server, ok := r.Server.(resultServer)
					if !ok {
						results[index].Error = "statement conformance server has no result-vector analysis"
						continue
					}
					raw, laneErr := server.AnalyzeResults(ctx, input)
					if laneErr != nil {
						results[index] = TypeResult{Error: laneErr.Error(), ErrorCode: ErrorCode(laneErr)}
					} else {
						results[index] = canonicalResults(raw)
					}
					continue
				}
				if raw, laneErr := r.Server.Analyze(ctx, input); laneErr != nil {
					results[index] = TypeResult{Error: laneErr.Error(), ErrorCode: ErrorCode(laneErr)}
				} else {
					results[index] = canonicalResult(raw)
				}
			}
			return results
		},
		Execute: func(ctx context.Context, ordered []Input) []ExecutionResult {
			results := make([]ExecutionResult, len(ordered))
			for index, input := range ordered {
				if laneErr := r.Server.Execute(ctx, input); laneErr != nil {
					results[index] = ExecutionResult{Error: laneErr.Error(), ErrorCode: ErrorCode(laneErr)}
				} else {
					results[index].Ran = true
				}
			}
			return results
		},
	}
	if r.Native != nil {
		lanes.Native = func(ctx context.Context, ordered []Input) []NativeResult {
			results := make([]NativeResult, len(ordered))
			for index, input := range ordered {
				results[index] = r.Native(ctx, input)
			}
			return results
		}
	}
	return RunBatched(ctx, ddl, seed, inputs, lanes)
}

// RunBatched executes the shared conformance model through harness-owned
// batch lanes.
func RunBatched(ctx context.Context, ddl, seed string, inputs []Input, lanes BatchLanes) (Report, error) {
	if lanes.Info == nil || lanes.Infer == nil || lanes.Analyze == nil || lanes.Execute == nil {
		return Report{}, fmt.Errorf("conformance batch runner has an incomplete lane set")
	}
	if len(inputs) == 0 {
		return Report{}, fmt.Errorf("conformance matrix has no cells; zero measured cells are not coverage")
	}
	columns, err := FixtureColumnsFromDDL(ddl)
	if err != nil {
		return Report{}, fmt.Errorf("read fixture columns: %w", err)
	}
	info, err := lanes.Info(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("read server identity: %w", err)
	}
	ordered := append([]Input(nil), inputs...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].ID < ordered[right].ID })
	chgenResults := lanes.Infer(ordered)
	analysisResults := lanes.Analyze(ctx, ordered)
	executionResults := lanes.Execute(ctx, ordered)
	nativeResults := make([]NativeResult, len(ordered))
	if lanes.Native != nil {
		nativeResults = lanes.Native(ctx, ordered)
	}
	if len(chgenResults) != len(ordered) || len(analysisResults) != len(ordered) || len(executionResults) != len(ordered) || len(nativeResults) != len(ordered) {
		return Report{}, fmt.Errorf("conformance batch lane result count differs from the matrix")
	}
	report := Report{Metadata: Metadata{
		Server: info, FixtureHash: StableHash(ddl), SeedHash: StableHash(seed),
		MatrixHash: MatrixHash(ordered), FixtureColumns: columns,
	}}
	for index, input := range ordered {
		cell := cellFromInput(input)
		cell.Chgen = chgenResults[index]
		cell.Analysis = analysisResults[index]
		cell.Execution = executionResults[index]
		cell.Native = nativeResults[index]
		report.Cells = append(report.Cells, cell)
	}
	if err := ValidateReport(report); err != nil {
		return Report{}, err
	}
	return report, nil
}

// ValidateReport checks that each measured cell has all mandatory witnesses.
func ValidateReport(report Report) error {
	if len(report.Cells) == 0 {
		return fmt.Errorf("conformance report has no cells; zero measured cells are not coverage")
	}
	if report.Metadata.FixtureHash == "" || report.Metadata.SeedHash == "" || report.Metadata.MatrixHash == "" {
		return fmt.Errorf("conformance report has incomplete identity")
	}
	seen := make(map[string]struct{}, len(report.Cells))
	inputs := make([]Input, 0, len(report.Cells))
	for _, cell := range report.Cells {
		if cell.ID == "" || cell.Expression == "" && cell.Query == "" {
			return fmt.Errorf("conformance report has an unnamed cell")
		}
		if _, duplicate := seen[cell.ID]; duplicate {
			return fmt.Errorf("conformance report repeats cell %q", cell.ID)
		}
		seen[cell.ID] = struct{}{}
		if !cell.Chgen.hasAnswer() {
			return fmt.Errorf("conformance cell %q has no chgen result", cell.ID)
		}
		if !cell.Analysis.hasAnswer() {
			return fmt.Errorf("conformance cell %q has no analysis witness", cell.ID)
		}
		if !cell.Execution.Ran && cell.Execution.Error == "" {
			return fmt.Errorf("conformance cell %q has no execution witness", cell.ID)
		}
		inputs = append(inputs, Input{ID: cell.ID, Expression: cell.Expression, Table: cell.Table,
			Query: cell.Query, StatementTemplate: cell.StatementTemplate, Scope: cell.Scope, StatementShape: cell.StatementShape,
			Fixture: cell.Fixture, Expectation: cell.Expectation, KnownGap: cell.KnownGap,
			ExpectedChgenError: cell.ExpectedChgenError, ExpectedAnalysisCode: cell.ExpectedAnalysisCode,
			ExpectedChgenResult: cell.ExpectedChgenResult, ExpectedAnalysisError: cell.ExpectedAnalysisError,
			ExpectedAnalysisResult: cell.ExpectedAnalysisResult, ExpectedExecutionCode: cell.ExpectedExecutionCode,
			ExpectedExecutionError: cell.ExpectedExecutionError})
	}
	if got := MatrixHash(inputs); got != report.Metadata.MatrixHash {
		return fmt.Errorf("conformance report matrix hash is %s, want %s", report.Metadata.MatrixHash, got)
	}
	if hasStatementCells(report.Cells) {
		return ValidateStatementCoverage(report)
	}
	return nil
}

func cellFromInput(input Input) Cell {
	return Cell{ID: input.ID, Expression: input.Expression, Table: input.Table,
		Query: input.Query, StatementTemplate: input.StatementTemplate, Scope: input.Scope, StatementShape: input.StatementShape,
		Fixture: input.Fixture, Expectation: input.Expectation, KnownGap: input.KnownGap,
		ExpectedChgenError: input.ExpectedChgenError, ExpectedAnalysisCode: input.ExpectedAnalysisCode,
		ExpectedChgenResult: input.ExpectedChgenResult, ExpectedAnalysisError: input.ExpectedAnalysisError,
		ExpectedAnalysisResult: input.ExpectedAnalysisResult, ExpectedExecutionCode: input.ExpectedExecutionCode,
		ExpectedExecutionError: input.ExpectedExecutionError}
}

func canonicalResult(raw string) TypeResult {
	parsed, err := ParseType(raw)
	if err != nil {
		return TypeResult{Raw: raw, Error: err.Error()}
	}
	return TypeResult{Raw: raw, Canonical: &parsed}
}

func canonicalResults(raw []RawResultType) TypeResult {
	result := TypeResult{Results: make([]ResultType, 0, len(raw))}
	for _, field := range raw {
		parsed, err := ParseType(field.Type)
		if err != nil {
			return TypeResult{Error: fmt.Sprintf("result %q type %q: %v", field.Name, field.Type, err)}
		}
		result.Results = append(result.Results, ResultType{Name: field.Name, Raw: field.Type, Canonical: parsed})
	}
	if len(result.Results) == 0 {
		result.Error = "statement result vector is empty"
	}
	return result
}

func (result TypeResult) hasAnswer() bool {
	return result.Error != "" || result.Canonical != nil || len(result.Results) > 0
}

// CanonicalResult parses one raw type answer for a harness batch lane.
func CanonicalResult(raw string) TypeResult { return canonicalResult(raw) }

// StableHash gives the stable SHA-256 identity of one source string.
func StableHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// MatrixHash identifies the exact ordered cell matrix.
func MatrixHash(inputs []Input) string {
	var text strings.Builder
	for _, input := range inputs {
		fmt.Fprintf(&text, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
			input.ID, input.Table, input.Expression, input.Query, input.StatementTemplate, input.Scope,
			input.StatementShape, input.Fixture, input.Expectation)
		if input.Query != "" {
			fmt.Fprintf(&text, "\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%d\x00%s", input.KnownGap,
				input.ExpectedChgenError, input.ExpectedChgenResult, input.ExpectedAnalysisCode, input.ExpectedAnalysisError,
				input.ExpectedAnalysisResult, input.ExpectedExecutionCode, input.ExpectedExecutionError)
		}
		text.WriteByte('\n')
	}
	return StableHash(text.String())
}

type codedError interface{ Code() int }

// ErrorCode reads a numeric ClickHouse code when the error exposes one.
func ErrorCode(err error) int {
	if coded, ok := err.(codedError); ok {
		return coded.Code()
	}
	return 0
}
