package conformance

import (
	"strings"
	"testing"
)

func TestStatementMatrixHasAcceptedAndRefusedCellsForEveryContext(t *testing.T) {
	inputs := StatementMatrix()
	seen := make(map[string]map[Expectation]bool)
	for _, input := range inputs {
		if seen[input.StatementShape] == nil {
			seen[input.StatementShape] = make(map[Expectation]bool)
		}
		seen[input.StatementShape][input.Expectation] = true
		if input.Query == "" || input.Scope == "" || input.Fixture == "" || input.StatementTemplate == "" {
			t.Fatalf("statement cell has incomplete identity: %#v", input)
		}
	}
	for _, context := range StatementContexts() {
		accepted := seen[context][ExpectSupported] || seen[context][ExpectExecutionOnly] || seen[context][ExpectKnownChgenRefusal]
		rejected := seen[context][ExpectRefused] || seen[context][ExpectKnownChgenAcceptance]
		if !accepted || !rejected {
			t.Errorf("context %q does not have accepted and refused cells", context)
		}
	}
}

func TestStatementShrinkKeepsTheCausalContext(t *testing.T) {
	input := Input{ID: "window-finding", Expression: "missing.deep", Query: "SELECT row_number() OVER (ORDER BY missing.deep) AS value FROM stmt_left",
		StatementTemplate: "SELECT row_number() OVER (ORDER BY {{expr}}) AS value FROM stmt_left",
		StatementShape:    "window", Scope: "stmt_left", Fixture: "statement-v1", Expectation: ExpectRefused}
	shrunk := ShrinkStatement(input, []string{"missing"}, func(trial Input) bool {
		return trial.Expression == "missing"
	})
	if shrunk.Expression != "missing" || shrunk.StatementShape != "window" || shrunk.StatementTemplate != input.StatementTemplate {
		t.Fatalf("shrink removed the causal context: %#v", shrunk)
	}
	if shrunk.Query != "SELECT row_number() OVER (ORDER BY missing) AS value FROM stmt_left" {
		t.Fatalf("shrink made an unexpected query: %s", shrunk.Query)
	}
}

func TestStatementCoverageRefusesAContextWithoutAnAcceptedCell(t *testing.T) {
	report := statementReportForTest()
	if err := ValidateStatementCoverage(report); err != nil {
		t.Fatal(err)
	}
	for index := range report.Cells {
		if report.Cells[index].StatementShape == "join_on" {
			report.Cells[index].Execution = ExecutionResult{Error: "rejected"}
		}
	}
	if err := ValidateStatementCoverage(report); err == nil {
		t.Fatal("statement coverage accepted an empty context lane")
	}
}

func TestStatementLaneMutationsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Report)
		want   string
	}{
		{name: "supported type", mutate: func(report *Report) {
			cell := statementCell(report, "result-vector-supported")
			cell.Chgen.Results[1] = ResultType{Name: "second", Raw: "String", Canonical: Type{Name: "String"}}
		}, want: "result 1 type"},
		{name: "supported name", mutate: func(report *Report) {
			statementCell(report, "result-vector-supported").Chgen.Results[0].Name = "mutated"
		}, want: "result 0 name"},
		{name: "supported width", mutate: func(report *Report) {
			cell := statementCell(report, "result-vector-supported")
			cell.Chgen.Results = cell.Chgen.Results[:1]
		}, want: "chgen has 1 results"},
		{name: "refused typed", mutate: func(report *Report) {
			cell := statementCell(report, "where-unknown-refused")
			cell.Chgen.Error = ""
			cell.Chgen.Results = oneTestResult()
		}, want: "typed by chgen"},
		{name: "refusal boundary", mutate: func(report *Report) {
			statementCell(report, "union-refused").Chgen.Error = "unrelated refusal"
		}, want: "want boundary"},
		{name: "refusal analysis code", mutate: func(report *Report) {
			statementCell(report, "union-refused").Analysis.ErrorCode = 47
		}, want: "analysis code is 47, want 386"},
		{name: "refusal execution code", mutate: func(report *Report) {
			statementCell(report, "asof-join-refused").Execution.ErrorCode = 47
		}, want: "execution code is 47, want 403"},
		{name: "execution only ran", mutate: func(report *Report) {
			cell := statementCell(report, "execution-only-runtime")
			cell.Execution = ExecutionResult{Ran: true}
		}, want: "did not fail only at execution"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := statementReportForTest()
			test.mutate(&report)
			err := ValidateStatementCoverage(report)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestStatementKnownGapRosterIsExact(t *testing.T) {
	if len(statementKnownGaps) != 0 {
		t.Fatalf("known gap count = %d, want 0", len(statementKnownGaps))
	}
	seen := make(map[string]bool, len(statementKnownGaps))
	for _, input := range StatementMatrix() {
		if input.Expectation != ExpectKnownChgenRefusal && input.Expectation != ExpectKnownChgenAcceptance {
			if input.KnownGap != "" {
				t.Fatalf("cell %q has unexpected gap %q", input.ID, input.KnownGap)
			}
			continue
		}
		if input.Expectation == ExpectKnownChgenRefusal {
			owner, ok := statementKnownGaps[input.ID]
			if !ok || owner != input.KnownGap {
				t.Fatalf("cell %q has gap %q, refusal roster has %q", input.ID, input.KnownGap, owner)
			}
			seen[input.ID] = true
		}
	}
	if len(seen) != len(statementKnownGaps) {
		t.Fatalf("matrix has %d known gaps, roster has %d", len(seen), len(statementKnownGaps))
	}
	if len(statementKnownAcceptances) != 0 {
		t.Fatalf("known acceptance count = %d, want 0", len(statementKnownAcceptances))
	}
	seenAcceptances := make(map[string]bool, len(statementKnownAcceptances))
	for _, input := range StatementMatrix() {
		if input.Expectation != ExpectKnownChgenAcceptance {
			continue
		}
		owner, ok := statementKnownAcceptances[input.ID]
		if !ok || owner != input.KnownGap {
			t.Fatalf("cell %q has gap %q, acceptance roster has %q", input.ID, input.KnownGap, owner)
		}
		seenAcceptances[input.ID] = true
	}
	if len(seenAcceptances) != len(statementKnownAcceptances) {
		t.Fatalf("matrix has %d known acceptances, roster has %d", len(seenAcceptances), len(statementKnownAcceptances))
	}
	for id := range statementKnownGaps {
		if statementRefusalBoundaries[id].chgenError == "" {
			t.Fatalf("known refusal %q has no chgen boundary", id)
		}
	}
	for id := range statementKnownAcceptances {
		boundary := statementRefusalBoundaries[id]
		if boundary.chgenResult == "" {
			t.Fatalf("known acceptance %q has no chgen result boundary", id)
		}
		if boundary.analysisResult == "" && (boundary.analysisCode == 0 || boundary.analysisError == "" ||
			boundary.executionCode == 0 || boundary.executionError == "") {
			t.Fatalf("known acceptance %q has an incomplete server boundary", id)
		}
	}
}

func TestEveryKnownStatementBoundaryHasAMutationGate(t *testing.T) {
	for id := range statementKnownGaps {
		t.Run(id, func(t *testing.T) {
			report := statementReportForTest()
			statementCell(&report, id).Chgen.Error = "unrelated refusal"
			if err := ValidateStatementCoverage(report); err == nil || !strings.Contains(err.Error(), "want boundary") {
				t.Fatalf("error = %v, want the exact chgen boundary gate", err)
			}
		})
	}
	for id := range statementKnownAcceptances {
		t.Run(id, func(t *testing.T) {
			report := statementReportForTest()
			cell := statementCell(&report, id)
			if cell.ExpectedAnalysisCode != 0 {
				cell.Analysis.ErrorCode++
			} else {
				cell.Analysis.Results[0].Raw = "Int8"
			}
			if err := ValidateStatementCoverage(report); err == nil {
				t.Fatal("known acceptance mutation passed")
			}
		})
	}
}

func TestEveryPinnedStatementFieldHasAMutationGate(t *testing.T) {
	for id, boundary := range statementRefusalBoundaries {
		mutations := []struct {
			name   string
			active bool
			mutate func(*Cell)
		}{
			{name: "chgen_error", active: boundary.chgenError != "", mutate: func(cell *Cell) { cell.Chgen.Error = "unrelated" }},
			{name: "chgen_result", active: boundary.chgenResult != "", mutate: func(cell *Cell) { cell.Chgen.Results[0].Raw = "Int8" }},
			{name: "analysis_code", active: boundary.analysisCode != 0, mutate: func(cell *Cell) { cell.Analysis.ErrorCode++ }},
			{name: "analysis_error", active: boundary.analysisError != "", mutate: func(cell *Cell) { cell.Analysis.Error = "unrelated" }},
			{name: "analysis_result", active: boundary.analysisResult != "", mutate: func(cell *Cell) { cell.Analysis.Results[0].Raw = "Int8" }},
			{name: "execution_code", active: boundary.executionCode != 0, mutate: func(cell *Cell) { cell.Execution.ErrorCode++ }},
			{name: "execution_error", active: boundary.executionError != "", mutate: func(cell *Cell) { cell.Execution.Error = "unrelated" }},
		}
		for _, mutation := range mutations {
			if !mutation.active {
				continue
			}
			t.Run(id+"/"+mutation.name, func(t *testing.T) {
				report := statementReportForTest()
				mutation.mutate(statementCell(&report, id))
				if err := ValidateStatementCoverage(report); err == nil {
					t.Fatal("pinned statement mutation passed")
				}
			})
		}
	}
}

func TestStatementMatrixIdentityIsPinned(t *testing.T) {
	inputs := StatementMatrix()
	if len(inputs) != 139 {
		t.Fatalf("statement matrix has %d cells, want 139", len(inputs))
	}
	const wantHash = "571df32463c8a0d4d7cd87b350999e791d9a37e3bd8eaadf95aeca3e00d44fe5"
	if got := MatrixHash(inputs); got != wantHash {
		t.Fatalf("statement matrix hash is %s, want %s", got, wantHash)
	}
}

func statementReportForTest() Report {
	report := Report{}
	for _, input := range StatementMatrix() {
		cell := Cell{
			ID: input.ID, Query: input.Query, StatementTemplate: input.StatementTemplate, Scope: input.Scope,
			StatementShape: input.StatementShape, Fixture: input.Fixture,
			Expectation: input.Expectation, KnownGap: input.KnownGap,
			ExpectedChgenError: input.ExpectedChgenError, ExpectedAnalysisCode: input.ExpectedAnalysisCode,
			ExpectedChgenResult: input.ExpectedChgenResult, ExpectedAnalysisError: input.ExpectedAnalysisError,
			ExpectedAnalysisResult: input.ExpectedAnalysisResult, ExpectedExecutionCode: input.ExpectedExecutionCode,
			ExpectedExecutionError: input.ExpectedExecutionError,
		}
		switch input.Expectation {
		case ExpectSupported:
			cell.Execution.Ran = true
			cell.Chgen.Results = oneTestResult()
			cell.Analysis.Results = oneTestResult()
			if input.ID == "result-vector-supported" {
				cell.Chgen.Results = vectorTestResult()
				cell.Analysis.Results = vectorTestResult()
			}
		case ExpectExecutionOnly:
			cell.Execution.Error = "expected execution failure"
			cell.Chgen.Results = []ResultType{{Name: "value", Raw: "Int32", Canonical: Type{Name: "Int32"}}}
			cell.Analysis.Results = []ResultType{{Name: "value", Raw: "Int32", Canonical: Type{Name: "Int32"}}}
		case ExpectKnownChgenRefusal:
			cell.Execution.Ran = true
			cell.Chgen.Error = "expected known refusal: " + input.ExpectedChgenError
			cell.Analysis.Results = []ResultType{{Name: "value", Raw: "Int32", Canonical: Type{Name: "Int32"}}}
		case ExpectKnownChgenAcceptance:
			cell.Chgen.Results = []ResultType{{Name: "value", Raw: input.ExpectedChgenResult, Canonical: mustTestType(input.ExpectedChgenResult)}}
			if input.ID == "scalar-wrapper-known-acceptance" {
				cell.Execution.Ran = true
				cell.Analysis.Results = []ResultType{{Name: "value", Raw: input.ExpectedAnalysisResult, Canonical: mustTestType(input.ExpectedAnalysisResult)}}
			} else {
				cell.Execution.Error = "expected server refusal: " + input.ExpectedExecutionError
				cell.Analysis.Error = "expected server refusal: " + input.ExpectedAnalysisError
			}
		case ExpectRefused:
			cell.Execution.Error = "expected rejection"
			cell.Chgen.Error = "expected refusal: " + input.ExpectedChgenError
			if input.ExpectedAnalysisResult != "" {
				cell.Analysis.Results = []ResultType{{Name: "value", Raw: input.ExpectedAnalysisResult, Canonical: mustTestType(input.ExpectedAnalysisResult)}}
			} else {
				cell.Analysis.Error = "expected rejection"
				if input.ExpectedAnalysisError != "" {
					cell.Analysis.Error += ": " + input.ExpectedAnalysisError
				}
			}
			if input.ExpectedExecutionError != "" {
				cell.Execution.Error += ": " + input.ExpectedExecutionError
			}
		}
		cell.Analysis.ErrorCode = input.ExpectedAnalysisCode
		cell.Execution.ErrorCode = input.ExpectedExecutionCode
		report.Cells = append(report.Cells, cell)
	}
	return report
}

func mustTestType(raw string) Type {
	value, err := ParseType(raw)
	if err != nil {
		panic(err)
	}
	return value
}

func oneTestResult() []ResultType {
	return []ResultType{{Name: "value", Raw: "Int32", Canonical: Type{Name: "Int32"}}}
}

func vectorTestResult() []ResultType {
	return []ResultType{
		{Name: "first", Raw: "UInt64", Canonical: Type{Name: "UInt64"}},
		{Name: "second", Raw: "Int32", Canonical: Type{Name: "Int32"}},
	}
}

func statementCell(report *Report, id string) *Cell {
	for index := range report.Cells {
		if report.Cells[index].ID == id {
			return &report.Cells[index]
		}
	}
	panic("statement cell not found: " + id)
}
