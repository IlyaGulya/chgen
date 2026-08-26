package typeboundary

import (
	"fmt"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

const (
	PinnedBoundaryVersion    = "25.8.29.51"
	CandidateBoundaryVersion = "24.8.14.39"
)

type boundaryWitness struct {
	id                string
	expression        string
	pinnedAnalysis    string
	pinnedVerdict     Verdict
	candidateAnalysis string
	candidateVerdict  Verdict
	candidateExecCode int
}

var knownVersionBoundary = []boundaryWitness{
	{"v24_8_trim_fixedstring", "trim(fs)", "String", Verdict{TypeName: "String"}, "FixedString(4)", Verdict{ErrorCode: 43}, 43},
	{"v24_8_trimleft_fixedstring", "trimLeft(fs)", "String", Verdict{TypeName: "String"}, "FixedString(4)", Verdict{ErrorCode: 43}, 43},
	{"v24_8_trimright_fixedstring", "trimRight(fs)", "String", Verdict{TypeName: "String"}, "FixedString(4)", Verdict{ErrorCode: 43}, 43},
	{"v24_8_greatest_safn_i32", "greatest(safn_i32)", "SimpleAggregateFunction(anyLast, Nullable(Int32))", Verdict{TypeName: "SimpleAggregateFunction(anyLast, Nullable(Int32))"}, "Nullable(Int32)", Verdict{TypeName: "Nullable(Int32)"}, 0},
	{"v24_8_greatest_safn_u64", "greatest(safn_u64)", "SimpleAggregateFunction(anyLast, Nullable(UInt64))", Verdict{TypeName: "SimpleAggregateFunction(anyLast, Nullable(UInt64))"}, "Nullable(UInt64)", Verdict{TypeName: "Nullable(UInt64)"}, 0},
	{"v24_8_least_safn_i32", "least(safn_i32)", "SimpleAggregateFunction(anyLast, Nullable(Int32))", Verdict{TypeName: "SimpleAggregateFunction(anyLast, Nullable(Int32))"}, "Nullable(Int32)", Verdict{TypeName: "Nullable(Int32)"}, 0},
	{"v24_8_least_safn_u64", "least(safn_u64)", "SimpleAggregateFunction(anyLast, Nullable(UInt64))", Verdict{TypeName: "SimpleAggregateFunction(anyLast, Nullable(UInt64))"}, "Nullable(UInt64)", Verdict{TypeName: "Nullable(UInt64)"}, 0},
	{"v24_8_cityhash64_array_nullable", "cityHash64(arr_n_i32)", "UInt64", Verdict{TypeName: "UInt64"}, "UInt64", Verdict{ErrorCode: 48}, 48},
	{"v24_8_cityhash64_saf_array_nullable", "cityHash64(saf_narr)", "UInt64", Verdict{TypeName: "UInt64"}, "UInt64", Verdict{ErrorCode: 48}, 48},
	{"v24_8_cityhash64_saf_array_array_nullable", "cityHash64(safarr_narr)", "UInt64", Verdict{TypeName: "UInt64"}, "UInt64", Verdict{ErrorCode: 48}, 48},
}

// VerifyKnownVersionBoundary checks the fixed, executed server witnesses.
// It refuses an artifact with a different server, fixture, seed, matrix, or
// required cell. Other probe cells can grow without a contract update.
func VerifyKnownVersionBoundary(pinned, candidate *Artifact) error {
	if pinned.Server.Version != PinnedBoundaryVersion {
		return fmt.Errorf("pinned server version is %q, want %q", pinned.Server.Version, PinnedBoundaryVersion)
	}
	if candidate.Server.Version != CandidateBoundaryVersion {
		return fmt.Errorf("candidate server version is %q, want %q", candidate.Server.Version, CandidateBoundaryVersion)
	}
	if err := pinned.ValidateConformance(); err != nil {
		return fmt.Errorf("pinned artifact: %w", err)
	}
	if err := candidate.ValidateConformance(); err != nil {
		return fmt.Errorf("candidate artifact: %w", err)
	}
	if pinned.FixtureHash != candidate.FixtureHash || pinned.SeedHash != candidate.SeedHash || pinned.MatrixHash != candidate.MatrixHash {
		return fmt.Errorf("probe identities differ: pinned fixture=%s seed=%s matrix=%s; candidate fixture=%s seed=%s matrix=%s",
			pinned.FixtureHash, pinned.SeedHash, pinned.MatrixHash,
			candidate.FixtureHash, candidate.SeedHash, candidate.MatrixHash)
	}
	pinnedCells, err := conformanceByID(pinned.Conformance)
	if err != nil {
		return fmt.Errorf("pinned artifact: %w", err)
	}
	candidateCells, err := conformanceByID(candidate.Conformance)
	if err != nil {
		return fmt.Errorf("candidate artifact: %w", err)
	}
	for _, witness := range knownVersionBoundary {
		if err := verifyBoundarySide("pinned", pinned, pinnedCells, witness.id, witness.expression, witness.pinnedAnalysis, witness.pinnedVerdict, 0); err != nil {
			return err
		}
		if err := verifyBoundarySide("candidate", candidate, candidateCells, witness.id, witness.expression, witness.candidateAnalysis, witness.candidateVerdict, witness.candidateExecCode); err != nil {
			return err
		}
	}
	return nil
}

func conformanceByID(cells []conformance.Cell) (map[string]conformance.Cell, error) {
	byID := make(map[string]conformance.Cell, len(cells))
	for _, cell := range cells {
		if _, exists := byID[cell.ID]; exists {
			return nil, fmt.Errorf("conformance has duplicate cell %q", cell.ID)
		}
		byID[cell.ID] = cell
	}
	return byID, nil
}

func verifyBoundarySide(side string, artifact *Artifact, cells map[string]conformance.Cell, id, expression, analysis string, verdict Verdict, execCode int) error {
	gotVerdict, ok := artifact.Cells[id]
	if !ok {
		return fmt.Errorf("%s artifact has no required cell %q", side, id)
	}
	if !gotVerdict.Equal(verdict) {
		return fmt.Errorf("%s cell %q verdict is %s, want %s", side, id, gotVerdict, verdict)
	}
	cell, ok := cells[id]
	if !ok {
		return fmt.Errorf("%s conformance has no required cell %q", side, id)
	}
	if cell.Expression != expression {
		return fmt.Errorf("%s cell %q expression is %q, want %q", side, id, cell.Expression, expression)
	}
	if cell.Analysis.Raw != analysis || cell.Analysis.ErrorCode != 0 || cell.Analysis.Error != "" {
		return fmt.Errorf("%s cell %q analysis is raw=%q code=%d error=%q, want raw=%q",
			side, id, cell.Analysis.Raw, cell.Analysis.ErrorCode, cell.Analysis.Error, analysis)
	}
	if execCode == 0 {
		if !cell.Execution.Ran || cell.Execution.ErrorCode != 0 || cell.Execution.Error != "" {
			return fmt.Errorf("%s cell %q did not execute successfully", side, id)
		}
		return nil
	}
	if cell.Execution.ErrorCode != execCode || cell.Execution.Error == "" {
		return fmt.Errorf("%s cell %q execution code is %d with error %q, want code %d",
			side, id, cell.Execution.ErrorCode, cell.Execution.Error, execCode)
	}
	return nil
}
