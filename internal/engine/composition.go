package engine

import (
	"fmt"
	"go/ast"
	"slices"
	"strings"

	"github.com/IlyaGulya/chgen/internal/diagnostic"
)

// QueryComposition contains only fully resolved, finite SQL variants. No
// caller-provided SQL fragment or identifier is interpolated at runtime.
type QueryComposition struct {
	Options  []string
	Tables   []TableChoice
	Owners   map[string]string
	Variants []Query
}

type TableChoice struct {
	Name   string
	Tables []string
}

func isCompositionControlLine(line string) bool {
	for _, prefix := range []string{"-- chgen:if", "-- chgen:end", "-- chgen:else"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func parseTableChoice(line string) (TableChoice, error) {
	fields := strings.Fields(line)
	if len(fields) < 4 || fields[1] != "chgen:table" || !isGoIdentifier(fields[2]) || !ast.IsExported(fields[2]) {
		return TableChoice{}, fmt.Errorf("expected -- chgen:table ExportedChoiceName table [table...]")
	}
	seen := make(map[string]bool)
	for _, table := range fields[3:] {
		if !isGoIdentifier(table) || seen[table] {
			return TableChoice{}, fmt.Errorf("table choice %s requires distinct simple table names, got %q", fields[2], table)
		}
		seen[table] = true
	}
	return TableChoice{Name: fields[2], Tables: fields[3:]}, nil
}

func compositionCommentLines(sql string) map[int]bool {
	lines := make(map[int]bool)
	for i := 0; i < len(sql); {
		end := sqlDataEnd(sql, i)
		start := strings.LastIndexByte(sql[:i], '\n') + 1
		if strings.HasPrefix(sql[i:], "-- chgen:") && strings.TrimSpace(sql[start:i]) == "" {
			lines[lineOfOffset(sql, i)] = true
		}
		if end > i {
			i = end
		} else {
			i++
		}
	}
	return lines
}

func selectedTables(composition *QueryComposition, variant int) map[string]string {
	index := variant >> len(composition.Options)
	selected := make(map[string]string)
	for _, choice := range composition.Tables {
		selected[choice.Name] = choice.Tables[index%len(choice.Tables)]
		index /= len(choice.Tables)
	}
	return selected
}

func replaceTableChoices(sql string, selected map[string]string) (string, map[string]bool, error) {
	var out strings.Builder
	used := make(map[string]bool)
	for i := 0; i < len(sql); {
		if end := sqlDataEnd(sql, i); end > i {
			out.WriteString(sql[i:end])
			i = end
			continue
		}
		name, end, matched, err := parseNamedMacroAt(sql, i, "chgen.table")
		if err != nil {
			return "", nil, err
		}
		if !matched {
			out.WriteByte(sql[i])
			i++
			continue
		}
		table, ok := selected[name]
		if !ok {
			return "", nil, fmt.Errorf("chgen.table(%q) has no chgen:table declaration", name)
		}
		out.WriteString(table)
		used[name] = true
		i = end
	}
	return out.String(), used, nil
}

type conditionalSQL struct {
	text   string
	option string
}

func compositionError(query Query, err error) error {
	return diagnostic.With(fmt.Errorf("%s:%d: query %s composition: %w", query.File, query.Line, query.Name, err), diagnostic.Detail{
		Code: "composition-invalid", Status: diagnostic.Invalid, Stage: "composition",
		File: query.File, Line: query.Line, Query: query.Name,
		Hint: "Every finite variant must have the same result and shared parameter types. Correct the declared composition; unchecked SQL fragments are not accepted.",
	})
}

func parseConditionalSQL(sql string) ([]conditionalSQL, []string, error) {
	var parts []conditionalSQL
	var options []string
	start, active := 0, ""
	for i := 0; i < len(sql); {
		end := sqlDataEnd(sql, i)
		lineStart := strings.LastIndexByte(sql[:i], '\n') + 1
		if strings.HasPrefix(sql[i:], "-- chgen:") && strings.TrimSpace(sql[lineStart:i]) == "" {
			line := strings.TrimSpace(sql[i:end])
			if strings.HasPrefix(line, "-- chgen:else") {
				return nil, nil, fmt.Errorf("chgen:else is not supported; use independent optional blocks")
			}
			if strings.HasPrefix(line, "-- chgen:if") || strings.HasPrefix(line, "-- chgen:end") {
				parts = append(parts, conditionalSQL{text: sql[start:i], option: active})
				if line == "-- chgen:end" {
					if active == "" {
						return nil, nil, fmt.Errorf("chgen:end has no matching chgen:if")
					}
					active = ""
				} else {
					fields := strings.Fields(line)
					if len(fields) != 3 || fields[1] != "chgen:if" || !isGoIdentifier(fields[2]) || !ast.IsExported(fields[2]) {
						return nil, nil, fmt.Errorf("expected -- chgen:if ExportedOptionName")
					}
					if active != "" {
						return nil, nil, fmt.Errorf("nested chgen:if blocks are not supported; use independent blocks")
					}
					active = fields[2]
					if !slices.Contains(options, active) {
						options = append(options, active)
					}
				}
				start = end
			}
		}
		if end > i {
			i = end
		} else {
			i++
		}
	}
	if active != "" {
		return nil, nil, fmt.Errorf("chgen:if %s is missing chgen:end", active)
	}
	parts = append(parts, conditionalSQL{text: sql[start:]})
	return parts, options, nil
}

func resolveComposedQuery(builder *queryBuilder, schema, external *Schema) (Query, error) {
	parts, options, err := parseConditionalSQL(strings.TrimSpace(strings.Join(builder.sqlLines, "\n")))
	if err != nil {
		return Query{}, compositionError(builder.query, err)
	}
	if len(options) == 0 && len(builder.tableChoices) == 0 {
		if _, _, err := replaceTableChoices(builder.query.SQL, nil); err != nil {
			return Query{}, compositionError(builder.query, err)
		}
		return resolveBuiltQuery(builder, schema, external)
	}
	if builder.query.Command == CommandExec {
		return Query{}, compositionError(builder.query, fmt.Errorf("composition is supported only in :one and :many queries"))
	}
	if len(options) > 5 {
		return Query{}, compositionError(builder.query, fmt.Errorf("composition exceeds the limit of 32 fully checked variants"))
	}
	count := 1 << len(options)
	names := slices.Clone(options)
	for _, choice := range builder.tableChoices {
		if slices.Contains(names, choice.Name) {
			return Query{}, compositionError(builder.query, fmt.Errorf("duplicate composition option %s", choice.Name))
		}
		names = append(names, choice.Name)
		if count > 32/len(choice.Tables) {
			return Query{}, compositionError(builder.query, fmt.Errorf("composition exceeds the limit of 32 fully checked variants"))
		}
		count *= len(choice.Tables)
		for _, table := range choice.Tables {
			if _, ok := schema.Tables[table]; !ok {
				return Query{}, compositionError(builder.query, fmt.Errorf("table choice %s names %q, which is not a physical catalog table", choice.Name, table))
			}
		}
	}
	composition := &QueryComposition{Options: options, Tables: builder.tableChoices, Owners: make(map[string]string)}
	for mask := range count {
		variant := *builder
		variant.query = builder.query
		variant.query.Params = slices.Clone(builder.query.Params)
		variant.query.Results = slices.Clone(builder.query.Results)
		var sql strings.Builder
		for _, part := range parts {
			if part.option == "" || mask&(1<<slices.Index(options, part.option)) != 0 {
				sql.WriteString(part.text)
			} else {
				sql.WriteString(strings.Repeat("\n", strings.Count(part.text, "\n")))
			}
		}
		variant.query.SQL = strings.TrimSpace(sql.String())
		var used map[string]bool
		variant.query.SQL, used, err = replaceTableChoices(variant.query.SQL, selectedTables(composition, mask))
		if err != nil {
			return Query{}, compositionError(builder.query, err)
		}
		// Require each declared table slot in every variant. This keeps the
		// descriptor choice meaningful, even when an optional block is absent.
		for _, choice := range composition.Tables {
			if !used[choice.Name] {
				return Query{}, compositionError(builder.query, fmt.Errorf("table choice %s is unused in variant %d", choice.Name, mask+1))
			}
		}
		// An optional block owns its annotations only when it is present.
		_, names, err := normalizeNamedArgs(variant.query.SQL)
		if err != nil {
			return Query{}, compositionError(builder.query, err)
		}
		variant.query.Params = slices.DeleteFunc(variant.query.Params, func(param Param) bool { return !slices.Contains(names, param.GoName) })
		resolved, err := resolveBuiltQuery(&variant, schema, external)
		if err != nil {
			return Query{}, fmt.Errorf("composition variant %d (%s): %w", mask+1, strings.Join(enabledOptions(options, mask), ", "), err)
		}
		composition.Variants = append(composition.Variants, resolved)
	}
	query, err := mergeQueryVariants(composition)
	if err != nil {
		return Query{}, compositionError(builder.query, err)
	}
	for _, annotation := range builder.query.Params {
		if !slices.ContainsFunc(query.Params, func(param Param) bool { return param.GoName == annotation.GoName }) {
			return Query{}, compositionError(builder.query, fmt.Errorf("unused parameter annotation %s", annotation.GoName))
		}
	}
	return query, nil
}

func enabledOptions(options []string, mask int) []string {
	var enabled []string
	for i, name := range options {
		if mask&(1<<i) != 0 {
			enabled = append(enabled, name)
		}
	}
	return enabled
}

func mergeQueryVariants(composition *QueryComposition) (Query, error) {
	query := composition.Variants[0]
	query.Params, query.ExternalParams = nil, nil
	query.unverifiedExpressions, query.unverifiedResults, query.uncheckedSettings = nil, nil, nil
	query.Results = slices.Clone(query.Results)
	query.Composition = composition
	paramTypes := make(map[string]string)
	presence := make(map[string]map[int]bool)
	for index, variant := range composition.Variants {
		if len(variant.Results) != len(query.Results) {
			return Query{}, fmt.Errorf("variant %d changes the result column count", index+1)
		}
		for i, result := range variant.Results {
			want := query.Results[i]
			if result.SQLName != want.SQLName || result.GoName != want.GoName || result.GoType != want.GoType || result.CHType.String() != want.CHType.String() {
				return Query{}, fmt.Errorf("variant %d changes result column %d (%s)", index+1, i+1, want.SQLName)
			}
			query.Results[i].Asserted = query.Results[i].Asserted || result.Asserted
		}
		add := func(name, shape string) (bool, error) {
			previous, exists := paramTypes[name]
			if exists && previous != shape {
				return false, fmt.Errorf("variant %d changes parameter %s from %s to %s", index+1, name, previous, shape)
			}
			if !exists {
				paramTypes[name] = shape
				presence[name] = make(map[int]bool)
			}
			presence[name][index] = true
			return !exists, nil
		}
		for _, param := range variant.Params {
			added, err := add(param.GoName, param.GoType+":"+param.CHType.String())
			if err != nil {
				return Query{}, err
			}
			if added {
				query.Params = append(query.Params, param)
			}
		}
		for _, param := range variant.ExternalParams {
			added, err := add(param.GoName, fmt.Sprintf("external:%#v", param))
			if err != nil {
				return Query{}, err
			}
			if added {
				query.ExternalParams = append(query.ExternalParams, param)
			}
		}
		query.unverifiedExpressions = appendUnique(query.unverifiedExpressions, variant.unverifiedExpressions)
		query.unverifiedResults = appendUnique(query.unverifiedResults, variant.unverifiedResults)
		query.uncheckedSettings = appendUnique(query.uncheckedSettings, variant.uncheckedSettings)
	}
	for bit, option := range composition.Options {
		if _, exists := paramTypes[option]; exists {
			return Query{}, fmt.Errorf("option %s collides with a parameter name", option)
		}
		for name, variants := range presence {
			owned := true
			for index := range composition.Variants {
				if variants[index] != (index&(1<<bit) != 0) {
					owned = false
					break
				}
			}
			if owned {
				composition.Owners[name] = option
			}
		}
	}
	for _, table := range composition.Tables {
		if _, exists := paramTypes[table.Name]; exists {
			return Query{}, fmt.Errorf("table choice %s collides with a parameter name", table.Name)
		}
	}
	return query, nil
}

func appendUnique(values, more []string) []string {
	for _, value := range more {
		if !slices.Contains(values, value) {
			values = append(values, value)
		}
	}
	return values
}
