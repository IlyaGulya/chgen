package coverage

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/IlyaGulya/chgen/internal/describe"
	"github.com/IlyaGulya/chgen/internal/engine"
)

// Analyze adds server metadata observations for query cases only. Upstream
// scripts are never submitted: they can contain mutations, expected errors,
// external services, and unbounded workloads. No fixture DDL is applied.
func Analyze(ctx context.Context, data []byte, options describe.Options) (Report, error) {
	report, err := Measure(data)
	if err != nil {
		return Report{}, err
	}
	identity, err := describe.NativeQuery(ctx, options, "SELECT 1 AS __chgen_coverage_identity")
	if err != nil {
		return Report{}, fmt.Errorf("cannot establish coverage server identity: %w", err)
	}
	if identity.ServerVersion != report.ClickHouseVersion {
		return Report{}, fmt.Errorf("corpus targets ClickHouse %s; server answers %s", report.ClickHouseVersion, identity.ServerVersion)
	}
	report.ServerVersion = identity.ServerVersion
	corpus, err := Read(data)
	if err != nil {
		return Report{}, err
	}
	report.Warning = "Server analysis is not execution evidence. Only query cases are submitted to the explicit test database; parser-only scripts and schema inputs are never executed. Unmatched fixture schemas can cause server refusals. Expected outcomes are declarations, not live proof."
	for i, cell := range corpus.Cases {
		if cell.Scope == "parse" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		observation := &report.Cases[i]
		analysis, err := describe.NativeQuery(ctx, options, cell.SQL)
		if err != nil {
			stage := engine.CoverageStage{Status: "unknown", Message: err.Error()}
			var refusal *describe.ServerRefusal
			if errors.As(err, &refusal) && refusal.Code != "" {
				stage.Status, stage.Code = "refused", refusal.Code
				switch refusal.Code {
				case "164", "497", "516":
					stage.Status = "blocked"
				}
			}
			observation.Stages["server_analysis"] = stage
			continue
		}
		if analysis.ServerVersion != corpus.ClickHouseVersion {
			return Report{}, fmt.Errorf("corpus targets ClickHouse %s; server answers %s", corpus.ClickHouseVersion, analysis.ServerVersion)
		}
		observation.Stages["server_analysis"] = engine.CoverageStage{Status: "passed"}
		for _, column := range analysis.Columns {
			observation.ServerColumns = append(observation.ServerColumns, engine.CoverageColumn{Name: column.Name, Type: column.Type})
		}
		if len(observation.Columns) > 0 {
			status := "mismatch"
			if slices.Equal(observation.Columns, observation.ServerColumns) {
				status = "matched"
			}
			observation.Stages["type_comparison"] = engine.CoverageStage{Status: status}
		}
	}
	report.recount()
	return report, nil
}
