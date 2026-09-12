package engine

import (
	"fmt"
	"strings"
)

func validateCompositionSymbols(queries []Query, external []ExternalParam) error {
	// Existing query declarations and newly generated option/choice types share
	// one Go namespace. Refuse collisions before producing an unusable package.
	names := map[string]string{"Queries": "runtime", "Querier": "runtime", "MockQuerier": "runtime", "New": "runtime", "ErrNoRows": "runtime"}
	for _, table := range external {
		names[table.RowType] = "external schema " + table.SchemaName
	}
	for _, query := range queries {
		symbols := []string{query.Name + "Params"}
		if query.Command != CommandExec {
			symbols = append(symbols, query.Name+"Row")
		}
		if query.Composition == nil {
			symbols = append(symbols, lowerFirstIdentifier(query.Name)+"SQL")
		} else {
			for _, option := range query.Composition.Options {
				symbols = append(symbols, query.Name+option+"Params")
			}
			for _, choice := range query.Composition.Tables {
				symbols = append(symbols, query.Name+choice.Name)
				for _, table := range choice.Tables {
					symbols = append(symbols, query.Name+choice.Name+exportedIdentifier(table))
				}
			}
			for i := range query.Composition.Variants {
				symbols = append(symbols, fmt.Sprintf("%sVariant%dSQL", lowerFirstIdentifier(query.Name), i))
			}
		}
		for _, name := range symbols {
			if previous, exists := names[name]; exists {
				return compositionError(query, fmt.Errorf("generated name %s collides with %s", name, previous))
			}
			names[name] = "query " + query.Name
		}
	}
	return nil
}

func compositionAccess(query Query, name string) string {
	if owner := query.Composition.Owners[name]; owner != "" {
		return "arg." + owner + "." + name
	}
	return "arg." + name
}

func renderCompositionParams(query Query) string {
	var out strings.Builder
	renderFields := func(owner string) {
		for _, param := range query.Params {
			if query.Composition.Owners[param.GoName] == owner {
				fmt.Fprintf(&out, "%s %s\n", param.GoName, param.GoType)
			}
		}
		for _, param := range query.ExternalParams {
			if query.Composition.Owners[param.GoName] == owner {
				fmt.Fprintf(&out, "%s []%s\n", param.GoName, param.RowType)
			}
		}
	}
	fmt.Fprintf(&out, "// %sParams chooses one fully checked SQL variant.\ntype %sParams struct {\n", query.Name, query.Name)
	renderFields("")
	for _, table := range query.Composition.Tables {
		fmt.Fprintf(&out, "%s %s%s\n", table.Name, query.Name, table.Name)
	}
	for _, option := range query.Composition.Options {
		fmt.Fprintf(&out, "// %s enables its SQL blocks when non-nil.\n%s *%s%sParams\n", option, option, query.Name, option)
	}
	out.WriteString("}\n")
	for _, choice := range query.Composition.Tables {
		typeName := query.Name + choice.Name
		fmt.Fprintf(&out, "// %s selects a declared physical table; the zero value is invalid.\ntype %s string\nconst (\n", typeName, typeName)
		for _, table := range choice.Tables {
			fmt.Fprintf(&out, "%s%s %s = %q\n", typeName, exportedIdentifier(table), typeName, table)
		}
		out.WriteString(")\n")
	}
	for _, option := range query.Composition.Options {
		fmt.Fprintf(&out, "type %s%sParams struct {\n", query.Name, option)
		renderFields(option)
		out.WriteString("}\n")
	}
	for i, variant := range query.Composition.Variants {
		fmt.Fprintf(&out, "const %sVariant%dSQL = `%s`\n", lowerFirstIdentifier(query.Name), i, variant.SQL)
	}
	return out.String()
}

func renderCompositionSetup(query Query, empty string) string {
	var out strings.Builder
	out.WriteString("var querySQL string\nvar queryArgs []any\n")
	if query.ResultCapacity != "" {
		out.WriteString("var queryCapacity int\n")
	}
	out.WriteString("switch {\n")
	for mask, variant := range query.Composition.Variants {
		var conditions []string
		for bit, option := range query.Composition.Options {
			op := "=="
			if mask&(1<<bit) != 0 {
				op = "!="
			}
			conditions = append(conditions, fmt.Sprintf("arg.%s %s nil", option, op))
		}
		selected := selectedTables(query.Composition, mask)
		for _, choice := range query.Composition.Tables {
			conditions = append(conditions, fmt.Sprintf("arg.%s == %s%s%s", choice.Name, query.Name, choice.Name, exportedIdentifier(selected[choice.Name])))
		}
		fmt.Fprintf(&out, "case %s:\n", strings.Join(conditions, " && "))
		fmt.Fprintf(&out, "querySQL = %sVariant%dSQL\n", lowerFirstIdentifier(query.Name), mask)
		var args []string
		for _, param := range textPathParams(variant) {
			param.GoName = strings.TrimPrefix(compositionAccess(query, param.GoName), "arg.")
			args = append(args, generatedParamArg(param))
		}
		fmt.Fprintf(&out, "queryArgs = []any{%s}\n", strings.Join(args, ", "))
		for _, param := range variant.Params {
			if shape, ok := param.temporalPlan(); ok {
				errReturn := func(name string) string {
					return fmt.Sprintf("return %s, fmt.Errorf(%q, %s)", empty, query.Name+": %w", name)
				}
				out.WriteString(strings.Join(chgenGuardStatements(shape, compositionAccess(query, param.GoName), param.GoName, 0, "\t", errReturn), "\n"))
				out.WriteByte('\n')
			}
		}
		if len(variant.ExternalParams) > 0 {
			fmt.Fprintf(&out, "externalTables := make([]*ext.Table, 0, %d)\n", len(variant.ExternalParams))
			for i, param := range variant.ExternalParams {
				fmt.Fprintf(&out, "externalTable%d, err := new%sExternalTable(%q, %s)\n", i, param.RowType, param.WireName, compositionAccess(query, param.GoName))
				fmt.Fprintf(&out, "if err != nil { return %s, fmt.Errorf(%q, err) }\n", empty, query.Name+": %w")
				fmt.Fprintf(&out, "externalTables = append(externalTables, externalTable%d)\n", i)
			}
			out.WriteString("ctx = clickhouse.Context(ctx, clickhouse.WithExternalTable(externalTables...))\n")
		}
		if query.ResultCapacity != "" {
			for _, param := range variant.Params {
				if param.GoName == query.ResultCapacity {
					fmt.Fprintf(&out, "queryCapacity = len(%s)\n", compositionAccess(query, param.GoName))
				}
			}
			for _, param := range variant.ExternalParams {
				if param.GoName == query.ResultCapacity {
					fmt.Fprintf(&out, "queryCapacity = len(%s)\n", compositionAccess(query, param.GoName))
				}
			}
		}
	}
	fmt.Fprintf(&out, "default: return %s, fmt.Errorf(%q)\n}\n", empty, query.Name+": invalid query variant")
	return out.String()
}
