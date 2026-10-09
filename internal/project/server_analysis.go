package project

import (
	"context"
	"fmt"
	"strings"

	"github.com/IlyaGulya/chgen/internal/describe"
	"github.com/IlyaGulya/chgen/internal/engine"
)

func analyzeServerQuery(ctx context.Context, options describe.Options, input engine.Query, examples map[string]string, version *string) (engine.Query, error) {
	if input.Composition != nil {
		used := make(map[string]bool)
		variantExamples := make([]map[string]string, len(input.Composition.Variants))
		for i, variant := range input.Composition.Variants {
			_, parameters, err := engine.PrepareServerSelect(variant.SQL)
			if err != nil {
				return engine.Query{}, err
			}
			variantExamples[i] = make(map[string]string, len(parameters))
			for _, param := range parameters {
				value, exists := examples[param.Name]
				if !exists {
					return engine.Query{}, fmt.Errorf("missing example for native parameter %s", param.Name)
				}
				used[param.Name] = true
				variantExamples[i][param.Name] = value
			}
		}
		for name := range examples {
			if !used[name] {
				return engine.Query{}, fmt.Errorf("unused example for native parameter %s", name)
			}
		}
		variants := make([]engine.Query, len(input.Composition.Variants))
		for i, variant := range input.Composition.Variants {
			resolved, err := analyzeServerQuery(ctx, options, variant, variantExamples[i], version)
			if err != nil {
				return engine.Query{}, fmt.Errorf("composition variant %d: %w", i+1, err)
			}
			variants[i] = resolved
		}
		return engine.WithServerCompositionResults(input, variants)
	}
	options.Parameters = examples
	options.ExternalTables = nil
	for _, external := range input.ExternalParams {
		var columns []string
		for _, column := range external.Columns {
			columns = append(columns, "`"+strings.ReplaceAll(column.SQLName, "`", "``")+"` "+column.ClickHouseType)
		}
		options.ExternalTables = append(options.ExternalTables, describe.ExternalTable{Name: external.WireName, Structure: strings.Join(columns, ", ")})
	}
	report, err := describe.NativeQuery(ctx, options, input.SQL)
	if err != nil {
		return engine.Query{}, err
	}
	if *version != "" && *version != report.ServerVersion {
		return engine.Query{}, fmt.Errorf("server version changed during generation")
	}
	*version = report.ServerVersion
	columns := make([]engine.ServerColumn, len(report.Columns))
	for i, column := range report.Columns {
		columns[i] = engine.ServerColumn{Name: column.Name, Type: column.Type}
	}
	return engine.WithServerResults(input, columns, report.ServerVersion)
}
