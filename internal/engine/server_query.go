package engine

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"unicode"
)

// ServerColumn is a result column described by ClickHouse during analysis.
type ServerColumn struct {
	Name string
	Type string
}

// ServerParameter uses ClickHouse's native query-parameter syntax. Identifier
// parameters are deliberately excluded: they would make the result schema vary.
type ServerParameter struct {
	Name string
	Type CHType
}

var nativeStringEscapes = []string{"\\", "\\\\", "\x00", "\\0", "\b", "\\b", "\f", "\\f", "\n", "\\n", "\r", "\\r", "\t", "\\t"}
var nativeStringEscaper = strings.NewReplacer(nativeStringEscapes...)

// EncodeServerString uses the escaped-string format of ClickHouse native query
// parameters, not SQL string quoting. The generated encoder uses this same list.
func EncodeServerString(value string) string { return nativeStringEscaper.Replace(value) }

func nativeStringEncoder(queries []Query) string {
	for _, query := range queries {
		if query.serverVersion == "" || query.serverBindingSQL != "" {
			continue
		}
		for _, param := range query.Params {
			if param.CHType.Name == "String" {
				var args []string
				for i, text := range nativeStringEscapes {
					if i%2 == 1 {
						// The native driver serializes parameters as quoted fields.
						// That layer consumes one escape before ClickHouse reads
						// the parameter's escaped String value. HTTP has no such
						// layer and uses EncodeServerString directly.
						text = strings.ReplaceAll(text, "\\", "\\\\")
					}
					args = append(args, strconv.Quote(text))
				}
				return "var chgenNativeStringEscaper = strings.NewReplacer(" + strings.Join(args, ",") + ")"
			}
		}
	}
	return ""
}

type sqlBoundaryToken struct {
	start, end int
	text       string
	comment    bool
	quoted     bool
}

// scanSQLBoundary is a lexer, not a substitute SQL grammar. The server remains
// responsible for parsing. Only statement boundaries, headers, and parameters
// are interpreted here. Unhandled lexical forms are explicitly refused.
func scanSQLBoundary(sql string) ([]sqlBoundaryToken, error) {
	var tokens []sqlBoundaryToken
	for i := 0; i < len(sql); {
		if strings.ContainsRune(" \t\r\n", rune(sql[i])) {
			i++
			continue
		}
		start := i
		token := sqlBoundaryToken{start: start}
		switch {
		case strings.HasPrefix(sql[i:], "--") || sql[i] == '#':
			token.comment = true
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case strings.HasPrefix(sql[i:], "/*"):
			token.comment = true
			i += 2
			depth := 1
			for i < len(sql) && depth > 0 {
				switch {
				case strings.HasPrefix(sql[i:], "/*"):
					depth++
					i += 2
				case strings.HasPrefix(sql[i:], "*/"):
					depth--
					i += 2
				default:
					i++
				}
			}
			if depth != 0 {
				return nil, fmt.Errorf("unterminated SQL block comment")
			}
		case sql[i] == '\'' || sql[i] == '"' || sql[i] == '`':
			token.quoted = true
			quote := sql[i]
			i++
			closed := false
			for i < len(sql) {
				if sql[i] == '\\' {
					i += 2
					continue
				}
				if sql[i] == quote {
					i++
					if i < len(sql) && sql[i] == quote {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated SQL quote")
			}
		case sql[i] == '$':
			return nil, fmt.Errorf("server generation does not yet support dollar-quoted SQL")
		case isSQLIdentifierByte(sql[i]):
			for i < len(sql) && isSQLIdentifierByte(sql[i]) {
				i++
			}
		default:
			i++
		}
		token.end, token.text = i, sql[start:i]
		tokens = append(tokens, token)
	}
	return tokens, nil
}

// PrepareServerSelect is independent of the offline parser and type registry.
// DESCRIBE plus readonly on the server is the authoritative statement check.
func PrepareServerSelect(sql string) (string, []ServerParameter, error) {
	raw, err := scanSQLBoundary(sql)
	if err != nil {
		return "", nil, err
	}
	var tokens []sqlBoundaryToken
	for _, token := range raw {
		if !token.comment {
			tokens = append(tokens, token)
		}
	}
	if len(tokens) == 0 || tokens[0].quoted || !strings.EqualFold(tokens[0].text, "SELECT") && !strings.EqualFold(tokens[0].text, "WITH") {
		return "", nil, fmt.Errorf("server generation requires one SELECT or WITH ... SELECT")
	}
	copySQL := []byte(sql)
	var stack []string
	var params []ServerParameter
	seen := make(map[string]string)
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if token.quoted {
			continue
		}
		switch strings.ToUpper(token.text) {
		case "SELECT", "WITH":
		case "FORMAT", "INTO", "INSERT", "UPDATE", "DELETE", "ALTER", "CREATE", "DROP", "RENAME", "EXCHANGE", "TRUNCATE", "ATTACH", "DETACH", "SYSTEM", "GRANT", "REVOKE":
			// Do not mistake format(...) or system.one for output clauses or
			// statements. The server still validates the complete SELECT grammar.
			identifierUse := i > 0 && tokens[i-1].text == "." ||
				i+1 < len(tokens) && (tokens[i+1].text == "." || tokens[i+1].text == "(")
			if len(stack) == 0 && !identifierUse {
				return "", nil, fmt.Errorf("server generation refuses top-level %s", token.text)
			}
		case "CHGEN":
			if i+1 < len(tokens) && tokens[i+1].text == "." {
				return "", nil, fmt.Errorf("server generation requires native SQL, not chgen macros")
			}
		case "?":
			return "", nil, fmt.Errorf("server generation requires typed {Name:Type} parameters, not ?")
		case "(", "[":
			stack = append(stack, token.text)
		case ")", "]":
			if len(stack) == 0 || stack[len(stack)-1] == "(" && token.text != ")" || stack[len(stack)-1] == "[" && token.text != "]" {
				return "", nil, fmt.Errorf("unbalanced SQL delimiters")
			}
			stack = stack[:len(stack)-1]
		case ";":
			if len(stack) != 0 || i != len(tokens)-1 {
				return "", nil, fmt.Errorf("server generation requires exactly one SELECT")
			}
			copySQL[token.start] = ' '
		case "{":
			end := i + 1
			for end < len(tokens) && tokens[end].text != "}" {
				end++
			}
			if end == len(tokens) {
				return "", nil, fmt.Errorf("unterminated native query parameter")
			}
			name, typeText, ok := strings.Cut(sql[token.end:tokens[end].start], ":")
			name, typeText = strings.TrimSpace(name), strings.TrimSpace(typeText)
			if !ok || !isGoIdentifier(name) || exportedIdentifier(name) == "" {
				return "", nil, fmt.Errorf("expected a named {Name:ClickHouseType} parameter")
			}
			typeOf, err := parseServerType(typeText)
			if err != nil {
				return "", nil, fmt.Errorf("server parameter %s: %w", name, err)
			}
			if !serverParameterType(typeOf) {
				return "", nil, fmt.Errorf("server parameter %s: unsupported ClickHouse parameter type %s", name, typeText)
			}
			if previous, exists := seen[name]; exists && previous != typeOf.String() {
				return "", nil, fmt.Errorf("server parameter %s has conflicting types", name)
			} else if !exists {
				seen[name] = typeOf.String()
				params = append(params, ServerParameter{Name: name, Type: typeOf})
			}
			i = end
		case "}":
			return "", nil, fmt.Errorf("unexpected closing query parameter delimiter")
		}
	}
	if len(stack) != 0 {
		return "", nil, fmt.Errorf("unbalanced SQL delimiters")
	}
	return string(copySQL), params, nil
}

func serverParameterType(t CHType) bool {
	// Share the established Go representation boundary instead of maintaining
	// a second roster of parameter types. ClickHouse validates native type use.
	_, err := goType(t)
	return err == nil
}

func ParseServerQuerySource(file, source string) ([]Query, error) {
	return ParseServerQuerySourceWithExternal(file, source, nil)
}

func ParseServerQuerySourceWithExternal(file, source string, externalSchema *Schema) ([]Query, error) {
	tokens, err := scanSQLBoundary(source)
	if err != nil {
		return nil, err
	}
	var queries []Query
	var declarations []ServerParameter
	var tableChoices []TableChoice
	bodyStart := 0
	finish := func(end int) error {
		if len(queries) == 0 {
			return nil
		}
		query := &queries[len(queries)-1]
		query.SQL = strings.TrimSpace(source[bodyStart:end])
		prepared, err := prepareServerComposition(*query, declarations, tableChoices, externalSchema)
		if err == nil {
			*query = prepared
		}
		return err
	}
	for _, token := range tokens {
		if !token.comment {
			if len(queries) == 0 {
				return nil, fmt.Errorf("SQL appears before -- name annotation")
			}
			continue
		}
		lineStart := strings.LastIndexByte(source[:token.start], '\n') + 1
		header := strings.TrimSpace(source[lineStart:token.start]) == ""
		if header && strings.HasPrefix(token.text, "-- name:") {
			if err := finish(token.start); err != nil {
				return nil, err
			}
			query, err := parseNameAnnotation(token.text)
			if err != nil || query.Command == CommandExec {
				return nil, fmt.Errorf("server generation supports -- name: Name :one or :many only")
			}
			query.File, query.Line = file, lineOfOffset(source, token.start)
			queries = append(queries, query)
			declarations = nil
			tableChoices = nil
			bodyStart = token.end
		} else if header && strings.HasPrefix(token.text, "-- param-chtype:") {
			if len(queries) == 0 {
				return nil, fmt.Errorf("parameter declaration requires a named query")
			}
			name, contract, err := parseTypeContractWith(resultCHTypeDirective+strings.TrimPrefix(token.text, "-- param-chtype:"), lineOfOffset(source, token.start), parseServerCHType)
			if err != nil {
				return nil, err
			}
			if !isGoIdentifier(name) || !ast.IsExported(name) || !serverParameterType(contract.typeOf) {
				return nil, fmt.Errorf("invalid server parameter declaration %s", name)
			}
			declarations = append(declarations, ServerParameter{Name: name, Type: contract.typeOf})
		} else if header && strings.HasPrefix(token.text, "-- param:") {
			if len(queries) == 0 {
				return nil, fmt.Errorf("parameter annotation requires a named query")
			}
			param, err := parseParamAnnotation(token.text)
			if err != nil {
				return nil, err
			}
			query := &queries[len(queries)-1]
			query.serverParamAnnotations = append(query.serverParamAnnotations, param)
		} else if header && strings.HasPrefix(token.text, resultCHTypeDirective) {
			if len(queries) == 0 {
				return nil, fmt.Errorf("result contract requires a named query")
			}
			name, contract, err := parseTypeContractWith(token.text, lineOfOffset(source, token.start), parseServerCHType)
			if err != nil {
				return nil, err
			}
			query := &queries[len(queries)-1]
			if query.resultContracts == nil {
				query.resultContracts = make(map[string]resultTypeContract)
			}
			if _, exists := query.resultContracts[name]; exists {
				return nil, fmt.Errorf("duplicate result contract %s", name)
			}
			query.resultContracts[name] = contract
		} else if header && strings.HasPrefix(token.text, "-- result:") {
			if len(queries) == 0 {
				return nil, fmt.Errorf("result annotation requires a named query")
			}
			result, err := parseResultAnnotation(token.text)
			if err != nil {
				return nil, err
			}
			query := &queries[len(queries)-1]
			query.serverResultAnnotations = append(query.serverResultAnnotations, result)
		} else if header && strings.HasPrefix(token.text, "-- chgen:table") {
			if len(queries) == 0 {
				return nil, fmt.Errorf("table choice requires a named query")
			}
			choice, err := parseTableChoice(token.text)
			if err != nil {
				return nil, err
			}
			tableChoices = append(tableChoices, choice)
		} else if header && isCompositionControlLine(token.text) {
			if len(queries) == 0 {
				return nil, fmt.Errorf("composition control requires a named query")
			}
			// Interpreted after the entire query has been collected.
			continue
		} else if strings.HasPrefix(token.text, "-- chgen:") || strings.HasPrefix(token.text, "-- param") || strings.HasPrefix(token.text, "-- result") {
			return nil, fmt.Errorf("server generation does not interpret offline query annotations: %s", token.text)
		}
	}
	if err := finish(len(source)); err != nil {
		return nil, err
	}
	if len(queries) == 0 {
		return nil, fmt.Errorf("no annotated SELECT queries")
	}
	return queries, nil
}

// WithServerResults installs a narrow, private code-generation authority. It
// cannot be forged through the public Query DTO or ordinary type annotations.
func WithServerResults(query Query, columns []ServerColumn, version string) (Query, error) {
	_, params, err := PrepareServerSelect(query.SQL)
	if err != nil || version == "" || len(columns) == 0 {
		return Query{}, fmt.Errorf("incomplete server query contract: %v", err)
	}
	for _, param := range params {
		goType, err := goType(param.Type)
		if err != nil {
			return Query{}, err
		}
		query.Params = append(query.Params, Param{GoName: serverResultGoName(param.Name), GoType: goType, CHType: param.Type})
		query.serverParams = append(query.serverParams, param.Name)
	}
	for _, annotation := range query.serverParamAnnotations {
		found := false
		for _, param := range query.Params {
			if param.GoName != annotation.GoName {
				continue
			}
			if annotation.GoType != "" && annotation.GoType != param.GoType {
				return Query{}, fmt.Errorf("parameter %s: Go type %s differs from server mapping %s", annotation.GoName, annotation.GoType, param.GoType)
			}
			found = true
		}
		if !found {
			return Query{}, fmt.Errorf("unused parameter annotation %s", annotation.GoName)
		}
	}
	for _, param := range params {
		if !serverNativeScalar(param.Type) {
			query.serverBindingSQL, query.ParamIndexes, err = serverPositionalSQL(query.SQL, params)
			if err != nil {
				return Query{}, err
			}
			break
		}
	}
	for index, column := range columns {
		if len(params) > 0 && !isGoIdentifier(column.Name) {
			// Expression names can contain analysis samples. Never print or
			// publish such a name; aliases also stabilize runtime metadata.
			return Query{}, fmt.Errorf("result column %d requires a simple SQL alias; expression names may contain parameter examples", index+1)
		}
		typeOf, err := parseServerType(column.Type)
		if err != nil {
			return Query{}, err
		}
		goType, err := goType(typeOf)
		if err != nil {
			return Query{}, err
		}
		query.Results = append(query.Results, Result{GoName: serverResultGoName(column.Name), SQLName: column.Name, GoType: goType, CHType: typeOf, Asserted: true})
	}
	for name, contract := range query.resultContracts {
		found := false
		for _, result := range query.Results {
			if result.SQLName != name {
				continue
			}
			if result.CHType.String() != contract.typeOf.String() {
				return Query{}, fmt.Errorf("result %s: declared type %s differs from server type %s", name, contract.typeOf.String(), result.CHType.String())
			}
			found = true
		}
		if !found {
			return Query{}, fmt.Errorf("result contract names unknown column %s", name)
		}
	}
	seenResults := make(map[string]bool)
	for _, annotation := range query.serverResultAnnotations {
		if seenResults[annotation.SQLName] {
			return Query{}, fmt.Errorf("duplicate result annotation %s", annotation.SQLName)
		}
		seenResults[annotation.SQLName] = true
		found := false
		for i := range query.Results {
			result := &query.Results[i]
			if result.SQLName != annotation.SQLName {
				continue
			}
			if annotation.GoType != "" && annotation.GoType != result.GoType {
				return Query{}, fmt.Errorf("result %s: Go type %s differs from server mapping %s", annotation.SQLName, annotation.GoType, result.GoType)
			}
			result.GoName = annotation.GoName
			found = true
		}
		if !found {
			return Query{}, fmt.Errorf("result annotation names unknown column %s", annotation.SQLName)
		}
	}
	query.serverVersion = version
	query.serverSQLHash = sha256.Sum256([]byte(query.SQL))
	if err := validateServerQuery(query); err != nil {
		return Query{}, err
	}
	return query, nil
}

func serverNativeScalar(t CHType) bool {
	switch t.Name {
	case "String", "Bool", "Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32", "UInt64", "Float32", "Float64":
		return true
	}
	return false
}

func serverAnalysisEvidence(query Query) string {
	if query.serverVersion == "" {
		return ""
	}
	return fmt.Sprintf("// server-analysis: ClickHouse %q; SQL SHA-256 %x.\n// Metadata is checked before Scan; this does not prove query value semantics.", query.serverVersion, query.serverSQLHash)
}

func serverBindingArg(t CHType, value string) string {
	switch t.Name {
	case "Date", "Date32":
		return "(" + value + ").Format(\"2006-01-02\")"
	case "DateTime":
		return "(" + value + ").Unix()"
	case "DateTime64":
		return "(" + value + ").UnixNano()"
	case "LowCardinality":
		return serverBindingArg(t.Params[0], value)
	case "SimpleAggregateFunction":
		return serverBindingArg(t.Params[1], value)
	case "Nullable":
		typeName, _ := goType(t)
		return fmt.Sprintf("func(value %s) any { if value == nil { return nil }; return %s }(%s)", typeName, serverBindingArg(t.Params[0], "*value"), value)
	case "Array":
		typeName, _ := goType(t)
		return fmt.Sprintf("func(value %s) []any { result := make([]any, len(value)); for i, item := range value { result[i] = %s }; return result }(%s)", typeName, serverBindingArg(t.Params[0], "item"), value)
	case "Map":
		typeName, _ := goType(t)
		return fmt.Sprintf("func(value %s) *chgenServerMap { result := &chgenServerMap{entries: make([][2]any, 0, len(value))}; for key, item := range value { result.Put(%s, %s) }; return result }(%s)", typeName, serverBindingArg(t.Params[0], "key"), serverBindingArg(t.Params[1], "item"), value)
	}
	return value
}

// Implement the driver's OrderedMap boundary: its plain-map binder does not
// escape keys. The ordered-map binder uses its normal safe value formatter.
const serverMapHelper = `
type chgenServerMap struct { entries [][2]any }
func (m *chgenServerMap) Put(key, value any) { m.entries = append(m.entries, [2]any{key, value}) }
func (m *chgenServerMap) Iterator() column.MapIterator { return &chgenServerMapIterator{entries: m.entries, position: -1} }
type chgenServerMapIterator struct { entries [][2]any; position int }
func (i *chgenServerMapIterator) Next() bool { i.position++; return i.position < len(i.entries) }
func (i *chgenServerMapIterator) Key() any { return i.entries[i.position][0] }
func (i *chgenServerMapIterator) Value() any { return i.entries[i.position][1] }
`

func serverTypeContains(t CHType, name string) bool {
	if t.Name == name {
		return true
	}
	for _, param := range t.Params {
		if serverTypeContains(param, name) {
			return true
		}
	}
	return false
}

func validateServerQuery(query Query) error {
	if query.Command != CommandOne && query.Command != CommandMany || query.serverSQLHash != sha256.Sum256([]byte(query.SQL)) {
		return fmt.Errorf("server contract does not match this SELECT")
	}
	if len(query.serverParams) != len(query.Params) || len(query.Results) == 0 {
		return fmt.Errorf("incomplete server contract")
	}
	seen := make(map[string]bool)
	for _, param := range query.Params {
		if !isGoIdentifier(param.GoName) || !token.IsExported(param.GoName) || seen[param.GoName] {
			return fmt.Errorf("server parameter Go name collision: %s", param.GoName)
		}
		seen[param.GoName] = true
	}
	return validateResultGoNames(query.Results)
}

func serverResultGoName(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	result := exportedIdentifier(strings.Join(words, "_"))
	if !isGoIdentifier(result) || !token.IsExported(result) {
		result = "Column" + result
	}
	return result
}

func serverParamContext(query Query) string {
	if query.serverBindingSQL != "" {
		// Explicit native parameters make the driver bypass positional binding.
		// Keep settings and external tables but clear a caller's older map.
		return "ctx = clickhouse.Context(ctx, clickhouse.WithParameters(nil))\n"
	}
	if len(query.serverParams) == 0 {
		return ""
	}
	var source strings.Builder
	source.WriteString("ctx = clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters{\n")
	for i, param := range query.Params {
		// Scalar Go primitives have exact round-trip text representations.
		// Explicit context parameters avoid driver regex heuristics and prevent
		// a caller's older parameter map from shadowing this method's arguments.
		if param.CHType.Name == "String" {
			fmt.Fprintf(&source, "%q: chgenNativeStringEscaper.Replace(arg.%s),\n", query.serverParams[i], param.GoName)
		} else {
			fmt.Fprintf(&source, "%q: fmt.Sprint(arg.%s),\n", query.serverParams[i], param.GoName)
		}
	}
	source.WriteString("}))\n")
	return source.String()
}

// Lower typed native placeholders only when a composite value needs the
// driver's established value binder. Explicit casts retain the declared types;
// tokens inside comments and quoted strings are never rewritten.
func serverPositionalSQL(sql string, params []ServerParameter) (string, []int, error) {
	tokens, err := scanSQLBoundary(sql)
	if err != nil {
		return "", nil, err
	}
	byName := make(map[string]int, len(params))
	for index, param := range params {
		byName[param.Name] = index
	}
	var out strings.Builder
	var indexes []int
	position := 0
	for i := 0; i < len(tokens); i++ {
		if tokens[i].comment || tokens[i].quoted || tokens[i].text != "{" {
			continue
		}
		end := i + 1
		for end < len(tokens) && tokens[end].text != "}" {
			end++
		}
		if end == len(tokens) {
			return "", nil, fmt.Errorf("unterminated native parameter")
		}
		name, _, _ := strings.Cut(sql[tokens[i].end:tokens[end].start], ":")
		index, ok := byName[strings.TrimSpace(name)]
		if !ok {
			return "", nil, fmt.Errorf("unknown native parameter %s", name)
		}
		out.WriteString(sql[position:tokens[i].start])
		transport := serverTransportType(params[index].Type)
		input := fmt.Sprintf("CAST(? AS %s)", transport.String())
		fmt.Fprintf(&out, "CAST(%s AS %s)", serverTransportSQL(params[index].Type, input), params[index].Type.String())
		indexes = append(indexes, index)
		position = tokens[end].end
		i = end
	}
	out.WriteString(sql[position:])
	return out.String(), indexes, nil
}
