package project

import (
	"github.com/IlyaGulya/chgen/internal/diagnostic"
	"github.com/IlyaGulya/chgen/internal/engine"
)

// CheckReport describes offline generation readiness, not SQL execution safety.
type CheckReport struct {
	Status      string
	CanGenerate bool
	Packages    int
	Queries     int
	Diagnostics []diagnostic.Detail
}

// Check runs exactly the generation planning phase, without committing outputs.
func Check(configPath string) (CheckReport, error) {
	report := CheckReport{Status: "unknown"}
	config, err := LoadConfig(configPath)
	if err != nil {
		return failedCheck(err)
	}
	plan, err := buildExecutionPlan(config)
	if err != nil {
		return failedCheck(err)
	}
	report.Status = "confirmed"
	report.CanGenerate = true
	report.Packages = len(plan.packages)
	for _, pkg := range plan.packages {
		report.Queries += len(pkg.resolvedQueries)
		for _, query := range pkg.resolvedQueries {
			for _, d := range engine.QueryDiagnostics(query) {
				d.Package = pkg.name
				report.Diagnostics = append(report.Diagnostics, d)
				if d.Status == diagnostic.Unknown {
					report.Status = diagnostic.Unknown
				}
			}
		}
	}
	return report, nil
}

func failedCheck(err error) (CheckReport, error) {
	d := diagnostic.Describe(err)
	return CheckReport{Status: d.Status, Diagnostics: []diagnostic.Detail{d}}, err
}
