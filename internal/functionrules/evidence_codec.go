package functionrules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
)

// The wire plan is independently versioned. Its digest also binds every
// expression, input type and row value in the ordered measurement recipes.
const evidencePlanVersion = "functionrules-plan-v1"

type measurementOutcome struct {
	Analysis      string `json:"analysis,omitempty"`
	Execution     string `json:"execution,omitempty"`
	AnalysisCode  int    `json:"analysis_code,omitzero"`
	ExecutionCode int    `json:"execution_code,omitzero"`
	ExecutionRows int    `json:"execution_rows,omitzero"`
}

type compactFunction struct {
	Name            string            `json:"name"`
	CaseInsensitive bool              `json:"case_insensitive"`
	Outcomes        outcomeReferences `json:"outcomes"`
}

type outcomeReferences []int

func (references *outcomeReferences) UnmarshalJSON(data []byte) error {
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	result := make(outcomeReferences, len(entries))
	for i, entry := range entries {
		if bytes.Equal(bytes.TrimSpace(entry), []byte("null")) {
			return fmt.Errorf("null outcome reference at position %d", i)
		}
		if err := json.Unmarshal(entry, &result[i]); err != nil {
			return fmt.Errorf("invalid outcome reference at position %d: %w", i, err)
		}
	}
	*references = result
	return nil
}

type compactPayload struct {
	Format      int                  `json:"format"`
	Profile     string               `json:"profile"`
	Source      apiinventory.Source  `json:"source"`
	PlanVersion string               `json:"plan_version"`
	PlanDigest  string               `json:"plan_digest"`
	Outcomes    []measurementOutcome `json:"outcomes"`
	Functions   []compactFunction    `json:"functions"`
}

type compactEvidence struct {
	compactPayload
	Checksum string `json:"checksum"`
}

// EncodeCompact losslessly stores independently measured analysis and execution
// witnesses. A checksum detects corruption; it is not a signature or proof that
// the measurements came from a trusted server.
func EncodeCompact(report Report) ([]byte, error) {
	expanded, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	if _, err := decodeExpanded(expanded); err != nil {
		return nil, err
	}
	payload := compactPayload{Format: 2, Profile: report.Profile, Source: report.Source,
		PlanVersion: evidencePlanVersion, PlanDigest: identity(report).PlanDigest}
	indexes := make(map[measurementOutcome]int)
	for _, function := range report.Functions {
		entry := compactFunction{Name: function.Name, CaseInsensitive: function.CaseInsensitive}
		for _, cell := range function.Cells {
			outcome := measurementOutcome{cell.Analysis, cell.Execution, cell.AnalysisCode, cell.ExecutionCode, cell.ExecutionRows}
			index, found := indexes[outcome]
			if !found {
				index = len(payload.Outcomes)
				indexes[outcome] = index
				payload.Outcomes = append(payload.Outcomes, outcome)
			}
			entry.Outcomes = append(entry.Outcomes, index)
		}
		payload.Functions = append(payload.Functions, entry)
	}
	return json.Marshal(compactEvidence{compactPayload: payload, Checksum: digest(payload)})
}

func decodeCompact(data []byte) (Report, error) {
	var saved compactEvidence
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return Report{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Report{}, fmt.Errorf("evidence must contain exactly one JSON document")
	}
	if saved.Checksum == "" || saved.Checksum != digest(saved.compactPayload) {
		return Report{}, fmt.Errorf("compact evidence checksum mismatch")
	}
	if saved.PlanVersion != evidencePlanVersion {
		return Report{}, fmt.Errorf("unsupported measurement plan version %q", saved.PlanVersion)
	}
	allowed, err := profileNames(saved.Profile)
	if err != nil {
		return Report{}, err
	}
	if len(saved.Functions) == 0 || len(saved.Functions) > len(allowed) || len(saved.Outcomes) == 0 {
		return Report{}, fmt.Errorf("incomplete compact evidence")
	}
	report := Report{Format: 1, Profile: saved.Profile, Source: saved.Source}
	seen := make(map[string]bool)
	used := make([]bool, len(saved.Outcomes))
	for _, function := range saved.Functions {
		if !slices.Contains(allowed, function.Name) || seen[function.Name] {
			return Report{}, fmt.Errorf("unknown or duplicate function recipe %q", function.Name)
		}
		seen[function.Name] = true
		plan := profilePlan(saved.Profile, function.Name)
		if len(function.Outcomes) != len(plan) {
			return Report{}, fmt.Errorf("%s has an incomplete probe matrix", function.Name)
		}
		entry := Function{Name: function.Name, CaseInsensitive: function.CaseInsensitive}
		for i, index := range function.Outcomes {
			if index < 0 || index >= len(saved.Outcomes) {
				return Report{}, fmt.Errorf("%s probe %s has invalid outcome index %d", function.Name, plan[i].id, index)
			}
			used[index] = true
			probe, outcome := plan[i], saved.Outcomes[index]
			entry.Cells = append(entry.Cells, Cell{ID: probe.id, Input: probe.input, Expression: probe.expression, Values: slices.Clone(probe.values),
				Analysis: outcome.Analysis, Execution: outcome.Execution, AnalysisCode: outcome.AnalysisCode, ExecutionCode: outcome.ExecutionCode, ExecutionRows: outcome.ExecutionRows})
		}
		report.Functions = append(report.Functions, entry)
	}
	if saved.PlanDigest != identity(report).PlanDigest {
		return Report{}, fmt.Errorf("compact evidence measurement plan digest mismatch; the saved plan cannot be reinterpreted")
	}
	unique := make(map[measurementOutcome]bool)
	for i, outcome := range saved.Outcomes {
		if !used[i] || unique[outcome] {
			return Report{}, fmt.Errorf("unused or duplicate compact evidence outcome %d", i)
		}
		unique[outcome] = true
	}
	expanded, err := json.Marshal(report)
	if err != nil {
		return Report{}, err
	}
	return decodeExpanded(expanded)
}
