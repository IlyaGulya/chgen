package conformance

import (
	"context"
	"errors"
	"testing"
)

const testDDL = "CREATE TABLE t (i Int32, d Decimal64(2), n Tuple(x Int32, y String)) ENGINE = Memory"

type fakeServer struct {
	analysis string
	anErr    error
	execErr  error
}

func (s fakeServer) Info(context.Context) (ServerInfo, error) {
	return ServerInfo{Version: "test", ServerRun: 7, UptimeS: 3}, nil
}

func (s fakeServer) Analyze(context.Context, Input) (string, error) {
	return s.analysis, s.anErr
}

func (s fakeServer) Execute(context.Context, Input) error { return s.execErr }

func TestRunnerRecordsAllIndependentLanes(t *testing.T) {
	runner := Runner{
		Server: fakeServer{analysis: "Decimal(18, 2)"},
		Infer:  func(Input) (string, error) { return "Decimal64(2)", nil },
		Native: func(context.Context, Input) NativeResult { return NativeResult{Supported: true, Value: "3.25"} },
	}
	report, err := runner.Run(context.Background(), testDDL, "INSERT INTO t VALUES (1, 3.25, (1, 'a'))", []Input{{ID: "b", Expression: "d", Table: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	cell := report.Cells[0]
	if cell.Chgen.Canonical == nil || cell.Analysis.Canonical == nil || !cell.Chgen.Canonical.Equal(*cell.Analysis.Canonical) {
		t.Fatal("canonical type trees do not agree")
	}
	if !cell.Execution.Ran || !cell.Native.Supported || cell.Native.Value != "3.25" {
		t.Fatalf("cell did not record execution and native lanes: %#v", cell)
	}
	if len(report.Metadata.FixtureColumns) != 3 || report.Metadata.Server.ServerRun != 7 || report.Metadata.MatrixHash == "" {
		t.Fatalf("metadata is incomplete: %#v", report.Metadata)
	}
}

func TestRunnerMutationFailsEachLane(t *testing.T) {
	tests := []struct {
		name   string
		runner Runner
		check  func(Cell) bool
	}{
		{
			name: "chgen",
			runner: Runner{Server: fakeServer{analysis: "Int32"}, Infer: func(Input) (string, error) {
				return "", errors.New("mutated inference")
			}},
			check: func(cell Cell) bool { return cell.Chgen.Error != "" },
		},
		{
			name: "analysis",
			runner: Runner{Server: fakeServer{anErr: &HTTPError{ClickHouseCode: 43, Message: "mutated analysis"}}, Infer: func(Input) (string, error) {
				return "Int32", nil
			}},
			check: func(cell Cell) bool { return cell.Analysis.ErrorCode == 43 },
		},
		{
			name: "execution",
			runner: Runner{Server: fakeServer{analysis: "Int32", execErr: &HTTPError{ClickHouseCode: 44, Message: "mutated execution"}}, Infer: func(Input) (string, error) {
				return "Int32", nil
			}},
			check: func(cell Cell) bool { return !cell.Execution.Ran && cell.Execution.ErrorCode == 44 },
		},
		{
			name: "native",
			runner: Runner{Server: fakeServer{analysis: "Int32"}, Infer: func(Input) (string, error) { return "Int32", nil }, Native: func(context.Context, Input) NativeResult {
				return NativeResult{Supported: true, Error: "mutated native value"}
			}},
			check: func(cell Cell) bool { return cell.Native.Error != "" },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := test.runner.Run(context.Background(), testDDL, "seed", []Input{{ID: "cell", Expression: "i", Table: "t"}})
			if err != nil {
				t.Fatal(err)
			}
			if !test.check(report.Cells[0]) {
				t.Fatalf("mutation was not visible: %#v", report.Cells[0])
			}
		})
	}
}

func TestRunnerRefusesZeroCellsAndExecutionLoss(t *testing.T) {
	validLanes := BatchLanes{
		Info:    func(context.Context) (ServerInfo, error) { return ServerInfo{Version: "test", ServerRun: 1}, nil },
		Infer:   func([]Input) []TypeResult { return []TypeResult{CanonicalResult("Int32")} },
		Analyze: func(context.Context, []Input) []TypeResult { return []TypeResult{CanonicalResult("Int32")} },
		Execute: func(context.Context, []Input) []ExecutionResult { return []ExecutionResult{{Ran: true}} },
	}
	if _, err := RunBatched(context.Background(), testDDL, "seed", nil, validLanes); err == nil {
		t.Fatal("runner accepted zero measured cells")
	}
	validLanes.Execute = func(context.Context, []Input) []ExecutionResult { return []ExecutionResult{{}} }
	if _, err := RunBatched(context.Background(), testDDL, "seed", []Input{{ID: "cell", Expression: "i", Table: "t"}}, validLanes); err == nil {
		t.Fatal("runner accepted a missing execution witness")
	}
}

func TestMatrixAndFixtureHashesAreStable(t *testing.T) {
	left := []Input{{ID: "b", Expression: "d", Table: "t"}, {ID: "a", Expression: "i", Table: "t"}}
	right := []Input{{ID: "a", Expression: "i", Table: "t"}, {ID: "b", Expression: "d", Table: "t"}}
	if MatrixHash(left) == MatrixHash(right) {
		t.Fatal("MatrixHash must preserve input order before Runner sorts the matrix")
	}
	if got := StableHash(testDDL); got != "a27ec003d6774c4a6139f1887f1e46226f1e91c2fcf6224e2ff6a68fc974a592" {
		t.Fatalf("StableHash changed: %s", got)
	}
}

func TestFixtureColumnsIgnoreCommentsBeforeCommaSplit(t *testing.T) {
	ddl := `CREATE TABLE t (
		d32 Decimal32(4), -- Do not add d32: d32 above is already Decimal32(4).
		-- Do not use this comment as a column, even when it has a comma.
		dt32 Date32
	) ENGINE = Memory`
	columns, err := FixtureColumnsFromDDL(ddl)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 || columns[0].Name != "d32" || columns[1].Name != "dt32" {
		t.Fatalf("fixture column roster includes comment text: %#v", columns)
	}
}

func TestFixtureColumnsReadEveryCreateTable(t *testing.T) {
	ddl := "CREATE TABLE one (a Int32) ENGINE = Memory; CREATE TABLE IF NOT EXISTS two (b String) ENGINE = Memory"
	columns, err := FixtureColumnsFromDDL(ddl)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 || columns[0].Table != "one" || columns[1].Table != "two" {
		t.Fatalf("fixture column roster does not cover all tables: %#v", columns)
	}
}
