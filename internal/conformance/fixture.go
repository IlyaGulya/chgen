package conformance

import (
	"fmt"
	"strings"
)

// FixtureColumn is one column parsed from the fixture DDL.
type FixtureColumn struct {
	Table string `json:"table,omitempty"`
	Name  string `json:"name"`
	Type  Type   `json:"type"`
}

// FixtureColumnsFromDDL derives the fixture column roster from CREATE TABLE.
// It does not accept a second hand-written column list.
func FixtureColumnsFromDDL(ddl string) ([]FixtureColumn, error) {
	var columns []FixtureColumn
	for offset := 0; offset < len(ddl); {
		upper := strings.ToUpper(ddl[offset:])
		relativeCreate := strings.Index(upper, "CREATE TABLE")
		if relativeCreate < 0 {
			break
		}
		create := offset + relativeCreate
		open := strings.IndexByte(ddl[create:], '(')
		if open < 0 {
			return nil, fmt.Errorf("CREATE TABLE has no column list")
		}
		open += create
		close := matchingParen(ddl, open)
		if close < 0 {
			return nil, fmt.Errorf("CREATE TABLE column list is unbalanced")
		}
		table := tableName(ddl[create+len("CREATE TABLE") : open])
		block := stripSQLComments(ddl[open+1 : close])
		for _, declaration := range splitTopLevel(block, ',') {
			line := strings.TrimSpace(declaration)
			if line == "" {
				continue
			}
			name, typeText, ok := splitColumn(line)
			if !ok {
				continue
			}
			parsed, err := ParseType(typeText)
			if err != nil {
				return nil, fmt.Errorf("column %s: %w", name, err)
			}
			columns = append(columns, FixtureColumn{Table: table, Name: name, Type: parsed})
		}
		offset = close + 1
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("DDL has no parsed CREATE TABLE columns")
	}
	return columns, nil
}

func tableName(text string) string {
	fields := strings.Fields(text)
	for len(fields) > 0 && (strings.EqualFold(fields[0], "IF") || strings.EqualFold(fields[0], "NOT") || strings.EqualFold(fields[0], "EXISTS")) {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], "`")
}

func matchingParen(text string, open int) int {
	depth := 0
	quote := byte(0)
	for index := open; index < len(text); index++ {
		char := text[index]
		if quote != 0 {
			if char == quote && (index == 0 || text[index-1] != '\\') {
				quote = 0
			}
			continue
		}
		switch char {
		case '\'', '"', '`':
			quote = char
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func stripSQLComments(text string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if comment := strings.Index(line, "--"); comment >= 0 {
			line = line[:comment]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		kept = append(kept, trimmed)
	}
	return strings.Join(kept, "\n")
}

func splitColumn(declaration string) (string, string, bool) {
	index := 0
	for index < len(declaration) && declaration[index] != ' ' && declaration[index] != '\t' && declaration[index] != '\n' {
		index++
	}
	if index == 0 || index == len(declaration) {
		return "", "", false
	}
	name := strings.Trim(declaration[:index], "`")
	typeText := strings.TrimSpace(declaration[index:])
	upper := strings.ToUpper(typeText)
	for _, suffix := range []string{" DEFAULT ", " MATERIALIZED ", " ALIAS ", " CODEC(", " TTL "} {
		if cut := strings.Index(upper, suffix); cut >= 0 {
			typeText = strings.TrimSpace(typeText[:cut])
			upper = upper[:cut]
		}
	}
	return name, typeText, name != "" && typeText != ""
}
