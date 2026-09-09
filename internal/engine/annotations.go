package engine

import (
	"errors"
	"fmt"
	"strings"
)

// parseQueriesInFile parses the annotated queries of one file against the
// physical catalog and the external catalog of its package. schema is never
// nil: ParseQueryFiles, the only caller, refuses a call without a catalog.
func parseQueriesInFile(file, input string, schema *Schema, externalSchema *Schema) ([]Query, error) {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	lines := strings.Split(input, "\n")
	contractLines := resultContractCommentLines(input)
	var builders []*queryBuilder
	var current *queryBuilder

	finish := func() {
		if current == nil {
			return
		}
		current.query.SQL = strings.TrimSpace(strings.Join(trimTrailingCommentLines(current.sqlLines), "\n"))
		builders = append(builders, current)
		current = nil
	}

	for lineNumber, line := range lines {
		trimmed := strings.TrimSpace(line)
		if contractLines[lineNumber+1] {
			if current == nil || current.bodyStarted {
				return nil, fmt.Errorf("%s:%d: %s must appear in a query header", file, lineNumber+1, resultCHTypeDirective)
			}
			if current.query.Command == CommandExec {
				return nil, fmt.Errorf("%s:%d: %s is not allowed on :exec", file, lineNumber+1, resultCHTypeDirective)
			}
			alias, contract, err := parseResultTypeContract(trimmed, lineNumber+1)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", file, lineNumber+1, err)
			}
			if current.query.resultContracts == nil {
				current.query.resultContracts = make(map[string]resultTypeContract)
			}
			if _, exists := current.query.resultContracts[alias]; exists {
				return nil, fmt.Errorf("%s:%d: duplicate result-chtype for %q", file, lineNumber+1, alias)
			}
			current.query.resultContracts[alias] = contract
			continue
		}
		if strings.HasPrefix(trimmed, "-- name:") {
			finish()
			query, err := parseNameAnnotation(trimmed)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNumber+1, err)
			}
			query.File = file
			query.Line = lineNumber + 1
			current = &queryBuilder{query: query}
			continue
		}
		if current == nil {
			if strings.HasPrefix(trimmed, uncheckedSettingDirective) {
				return nil, fmt.Errorf("%s:%d: %s must follow a -- name annotation", file, lineNumber+1, uncheckedSettingDirective)
			}
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			return nil, fmt.Errorf("line %d: SQL appears before a -- name annotation", lineNumber+1)
		}

		if !current.bodyStarted && strings.HasPrefix(trimmed, uncheckedSettingDirective) {
			name, err := parseUncheckedSettingAnnotation(trimmed, current.uncheckedSettings)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", file, lineNumber+1, err)
			}
			current.uncheckedSettings = append(current.uncheckedSettings, name)
			continue
		}
		if !current.bodyStarted && strings.HasPrefix(trimmed, "-- param:") {
			param, err := parseParamAnnotation(trimmed)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNumber+1, err)
			}
			current.query.Params = append(current.query.Params, param)
			continue
		}
		if !current.bodyStarted && strings.HasPrefix(trimmed, "-- result:") {
			result, err := parseResultAnnotation(trimmed)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNumber+1, err)
			}
			current.query.Results = append(current.query.Results, result)
			continue
		}
		if !current.bodyStarted && strings.HasPrefix(trimmed, "-- result-capacity:") {
			if current.query.ResultCapacity != "" {
				return nil, fmt.Errorf("line %d: duplicate -- result-capacity annotation", lineNumber+1)
			}
			capacity, err := parseResultCapacityAnnotation(trimmed)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNumber+1, err)
			}
			current.query.ResultCapacity = capacity
			continue
		}
		if !current.bodyStarted && (trimmed == "" || strings.HasPrefix(trimmed, "--")) {
			continue
		}
		if !current.bodyStarted {
			current.sqlLine = lineNumber + 1
		}
		current.bodyStarted = true
		current.sqlLines = append(current.sqlLines, line)
	}
	finish()

	if len(builders) == 0 {
		return nil, fmt.Errorf("no -- name annotations found")
	}

	queries := make([]Query, 0, len(builders))
	for _, builder := range builders {
		wrap := func(err error) error {
			var externalErr *externalReferenceError
			if errors.As(err, &externalErr) {
				return fmt.Errorf("%s:%d: %w", builder.query.File, builder.query.Line, err)
			}
			return fmt.Errorf("%s:%d: query %s: %w", builder.query.File, builder.query.Line, builder.query.Name, err)
		}
		if err := rejectRawPlaceholders(builder.query, builder.sqlLine); err != nil {
			return nil, err
		}
		normalizedSQL, externalParams, err := normalizeExternalTables(builder.query.SQL, externalSchema, schema)
		if err != nil {
			return nil, wrap(err)
		}
		builder.query.SQL = normalizedSQL
		builder.query.ExternalParams = externalParams
		normalizedSQL, namedParamNames, err := normalizeNamedArgs(builder.query.SQL)
		if err != nil {
			return nil, wrap(err)
		}
		builder.query.SQL = normalizedSQL
		builder.query.NamedParamNames = namedParamNames
		if err := validateQueryFields(builder.query); err != nil {
			return nil, wrap(err)
		}
		querySchema, err := schemaForQuery(schema, externalParams, externalSchema)
		if err != nil {
			return nil, wrap(err)
		}
		if err := resolveQueryWithUncheckedSettings(&builder.query, querySchema, builder.uncheckedSettings); err != nil {
			return nil, wrap(err)
		}
		queries = append(queries, builder.query)
	}
	return queries, nil
}

// rejectRawPlaceholders fails a schema-aware query whose source contains a
// raw positional `?`. The check runs on the source text, before chgen.arg
// markers are lowered to `?`, so only author-written placeholders can match.
// The scan is lexical: `?` inside strings, quoted identifiers, and comments is
// data and does not match. Annotation-only parsing (no schema) keeps raw `?`
// because its ordered -- param annotations name every placeholder explicitly.
func rejectRawPlaceholders(query Query, sqlLine int) error {
	sql := query.SQL
	line := sqlLine
	for index := 0; index < len(sql); {
		switch {
		case sql[index] == '\n':
			line++
			index++
		case sql[index] == '\'' || sql[index] == '"' || sql[index] == '`':
			end := skipSQLQuoted(sql, index, sql[index])
			line += strings.Count(sql[index:end], "\n")
			index = end
		case strings.HasPrefix(sql[index:], "--"):
			end := skipSQLLineComment(sql, index)
			line += strings.Count(sql[index:end], "\n")
			index = end
		case strings.HasPrefix(sql[index:], "/*"):
			end := skipSQLBlockComment(sql, index)
			line += strings.Count(sql[index:end], "\n")
			index = end
		case sql[index] == '?':
			location := fmt.Sprintf("line %d", line)
			if query.File != "" {
				location = fmt.Sprintf("%s:%d", query.File, line)
			}
			return fmt.Errorf(
				"%s: query %s: raw positional placeholder \"?\"; use chgen.arg('Name') so parameter names stay in the SQL source",
				location, query.Name)
		default:
			index++
		}
	}
	return nil
}

// normalizeNamedArgs lowers the source-only chgen.arg('Name') form to the
// positional placeholders expected by ClickHouse drivers. The names are kept
// separately so schema-aware resolution can build one Go parameter per unique
// source name while preserving the placeholder occurrence order.
func normalizeNamedArgs(sql string) (string, []string, error) {
	var normalized strings.Builder
	normalized.Grow(len(sql))
	names := make([]string, 0)
	for index := 0; index < len(sql); {
		if quote := sql[index]; quote == '\'' || quote == '"' || quote == '`' {
			end := skipSQLQuoted(sql, index, quote)
			normalized.WriteString(sql[index:end])
			index = end
			continue
		}
		if strings.HasPrefix(sql[index:], "--") {
			end := skipSQLLineComment(sql, index)
			normalized.WriteString(sql[index:end])
			index = end
			continue
		}
		if strings.HasPrefix(sql[index:], "/*") {
			end := skipSQLBlockComment(sql, index)
			normalized.WriteString(sql[index:end])
			index = end
			continue
		}

		name, end, matched, err := parseNamedArgAt(sql, index)
		if err != nil {
			return "", nil, err
		}
		if matched {
			normalized.WriteByte('?')
			names = append(names, name)
			index = end
			continue
		}

		normalized.WriteByte(sql[index])
		index++
	}
	return normalized.String(), names, nil
}

const namedArgPrefix = "chgen.arg"

func parseNamedArgAt(sql string, start int) (string, int, bool, error) {
	if len(sql)-start < len(namedArgPrefix) || !strings.EqualFold(sql[start:start+len(namedArgPrefix)], namedArgPrefix) {
		return "", start, false, nil
	}
	if start > 0 && (isSQLIdentifierByte(sql[start-1]) || sql[start-1] == '.') {
		return "", start, false, nil
	}

	index := start + len(namedArgPrefix)
	index = skipSQLWhitespace(sql, index)
	if index >= len(sql) || sql[index] != '(' {
		return "", start, true, fmt.Errorf("invalid chgen.arg at byte %d: expected opening parenthesis", start)
	}
	index = skipSQLWhitespace(sql, index+1)
	if index >= len(sql) || sql[index] != '\'' {
		return "", start, true, fmt.Errorf("invalid chgen.arg at byte %d: expected a single-quoted name", start)
	}
	nameStart := index + 1
	index++
	closing := strings.IndexByte(sql[index:], '\'')
	if closing < 0 {
		return "", start, true, fmt.Errorf("invalid chgen.arg at byte %d: expected closing quote", start)
	}
	index += closing
	name := sql[nameStart:index]
	if !isGoIdentifier(name) {
		return "", start, true, fmt.Errorf("invalid chgen.arg name %q", name)
	}
	if index >= len(sql) || sql[index] != '\'' {
		return "", start, true, fmt.Errorf("invalid chgen.arg(%q): expected closing quote", name)
	}
	index = skipSQLWhitespace(sql, index+1)
	if index >= len(sql) || sql[index] != ')' {
		return "", start, true, fmt.Errorf("invalid chgen.arg(%q): expected closing parenthesis", name)
	}
	return name, index + 1, true, nil
}

func skipSQLQuoted(sql string, start int, quote byte) int {
	for index := start + 1; index < len(sql); index++ {
		if sql[index] == '\\' {
			index++
			continue
		}
		if sql[index] != quote {
			continue
		}
		if index+1 < len(sql) && sql[index+1] == quote {
			index++
			continue
		}
		return index + 1
	}
	return len(sql)
}

func skipSQLLineComment(sql string, start int) int {
	if newline := strings.IndexByte(sql[start:], '\n'); newline >= 0 {
		return start + newline + 1
	}
	return len(sql)
}

func skipSQLBlockComment(sql string, start int) int {
	if end := strings.Index(sql[start+2:], "*/"); end >= 0 {
		return start + 2 + end + 2
	}
	return len(sql)
}

func skipSQLWhitespace(sql string, start int) int {
	for start < len(sql) {
		switch sql[start] {
		case ' ', '\t', '\r', '\n':
			start++
		default:
			return start
		}
	}
	return start
}

func isSQLIdentifierByte(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func parseNameAnnotation(line string) (Query, error) {
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "-- name:")))
	if len(fields) != 2 {
		return Query{}, fmt.Errorf("expected -- name: QueryName :many|:one|:exec")
	}
	if !isGoIdentifier(fields[0]) {
		return Query{}, fmt.Errorf("invalid query name %q", fields[0])
	}
	command := Command(strings.TrimPrefix(fields[1], ":"))
	switch command {
	case CommandMany, CommandOne, CommandExec:
	default:
		return Query{}, fmt.Errorf("unsupported query command %q", fields[1])
	}
	return Query{Name: fields[0], Command: command}, nil
}

func parseParamAnnotation(line string) (Param, error) {
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "-- param:")))
	if len(fields) != 1 && len(fields) != 2 {
		return Param{}, fmt.Errorf("expected -- param: GoName [GoType]")
	}
	if !isGoIdentifier(fields[0]) {
		return Param{}, fmt.Errorf("invalid parameter name %q", fields[0])
	}
	if len(fields) == 1 {
		return Param{GoName: fields[0]}, nil
	}
	if err := validateGoType(fields[1]); err != nil {
		return Param{}, fmt.Errorf("parameter %s: %w", fields[0], err)
	}
	return Param{GoName: fields[0], GoType: fields[1]}, nil
}

func parseResultAnnotation(line string) (Result, error) {
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "-- result:")))
	if len(fields) != 2 && len(fields) != 3 {
		return Result{}, fmt.Errorf("expected -- result: GoName SQLAlias [GoType]")
	}
	if !isGoIdentifier(fields[0]) {
		return Result{}, fmt.Errorf("invalid result field name %q", fields[0])
	}
	if !isGoIdentifier(fields[1]) {
		return Result{}, fmt.Errorf("invalid result SQL alias %q", fields[1])
	}
	if len(fields) == 2 {
		return Result{GoName: fields[0], SQLName: fields[1]}, nil
	}
	if err := validateGoType(fields[2]); err != nil {
		return Result{}, fmt.Errorf("result %s: %w", fields[0], err)
	}
	return Result{GoName: fields[0], SQLName: fields[1], GoType: fields[2]}, nil
}

func parseResultCapacityAnnotation(line string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "-- result-capacity:")))
	if len(fields) != 1 || !isGoIdentifier(fields[0]) {
		return "", fmt.Errorf("expected -- result-capacity: SliceParameter")
	}
	return fields[0], nil
}
