package project

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/IlyaGulya/chgen/internal/describe"
	"github.com/IlyaGulya/chgen/internal/diagnostic"
	"github.com/IlyaGulya/chgen/internal/engine"
)

type serverDescribe func(context.Context, describe.Options, string) (describe.Report, error)

// Parameter examples are deliberately supplied by the caller: inventing values
// can select a different result type or change the validity of a query.
func validateServerExamples(input engine.Query, key string, examples map[string]string) error {
	queries := []engine.Query{input}
	if input.Composition != nil {
		queries = input.Composition.Variants
	}
	required := make(map[string]string)
	missing := false
	for _, query := range queries {
		_, parameters, err := engine.PrepareServerSelect(query.SQL)
		if err != nil {
			return err
		}
		for _, param := range parameters {
			required[param.Name] = "<" + param.Type.String() + ">"
			if _, supplied := examples[param.Name]; !supplied {
				missing = true
			}
		}
	}
	if missing {
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(map[string]map[string]string{key: required}); err != nil {
			return err
		}
		return diagnostic.With(fmt.Errorf("parameter examples are missing for query %s", input.Name), diagnostic.Detail{
			Code: "server-parameter-examples-missing", Status: diagnostic.Unknown, Stage: "analysis",
			Hint: "Create examples.json with " + strings.TrimSpace(buffer.String()) + ". Replace each <ClickHouseType> with a representative value, then pass -params examples.json. Examples are sent to your test server; they are not stored in generated code or contracts.",
		})
	}
	for name := range examples {
		if _, used := required[name]; !used {
			return diagnostic.With(fmt.Errorf("unused example for native parameter %s", name), diagnostic.Detail{
				Code: "server-parameter-example-unused", Status: diagnostic.Invalid, Stage: "analysis",
				Hint: "Remove unused entries from this query's examples. Supply exactly one value for each declared parameter.",
			})
		}
	}
	return nil
}

func analyzeServerQuery(ctx context.Context, options describe.Options, input engine.Query, examples map[string]string, version *string, analyze serverDescribe) (engine.Query, error) {
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
			resolved, err := analyzeServerQuery(ctx, options, variant, variantExamples[i], version, analyze)
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
	report, err := analyze(ctx, options, input.SQL)
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
