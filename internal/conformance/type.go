// Package conformance provides the shared cell model and runner for every
// ClickHouse conformance lane.
package conformance

import (
	"fmt"
	"strings"
)

// Type is a canonical ClickHouse type tree.
type Type struct {
	Name string
	Args []TypeArg
}

// TypeArg is one type parameter. A named Tuple element uses Label. A literal
// parameter uses Literal. All other parameters use Type.
type TypeArg struct {
	Label   string
	Type    *Type
	Literal string
}

// ParseType parses one ClickHouse type name into a canonical tree.
func ParseType(raw string) (Type, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Type{}, fmt.Errorf("type name is empty")
	}
	name, body, hasArgs, err := splitType(raw)
	if err != nil {
		return Type{}, err
	}
	result := Type{Name: name}
	if !hasArgs {
		return canonicalType(result), nil
	}
	for _, rawArg := range splitTopLevel(body, ',') {
		arg, argErr := parseTypeArg(strings.TrimSpace(rawArg))
		if argErr != nil {
			return Type{}, fmt.Errorf("type %s argument %q: %w", name, rawArg, argErr)
		}
		result.Args = append(result.Args, arg)
	}
	return canonicalType(result), nil
}

func splitType(raw string) (string, string, bool, error) {
	open := strings.IndexByte(raw, '(')
	if open < 0 {
		if strings.ContainsAny(raw, ",)") {
			return "", "", false, fmt.Errorf("invalid type %q", raw)
		}
		return strings.TrimSpace(raw), "", false, nil
	}
	if !strings.HasSuffix(raw, ")") {
		return "", "", false, fmt.Errorf("unbalanced type %q", raw)
	}
	name := strings.TrimSpace(raw[:open])
	body := raw[open+1 : len(raw)-1]
	if name == "" {
		return "", "", false, fmt.Errorf("type name is empty")
	}
	return name, body, true, nil
}

func parseTypeArg(raw string) (TypeArg, error) {
	if raw == "" {
		return TypeArg{}, fmt.Errorf("argument is empty")
	}
	if isLiteralArg(raw) {
		return TypeArg{Literal: canonicalLiteral(raw)}, nil
	}
	if label, value, ok := splitNamedTypeArg(raw); ok {
		parsed, err := ParseType(value)
		if err == nil {
			return TypeArg{Label: label, Type: &parsed}, nil
		}
	}
	parsed, err := ParseType(raw)
	if err != nil {
		return TypeArg{}, err
	}
	return TypeArg{Type: &parsed}, nil
}

func isLiteralArg(raw string) bool {
	first := raw[0]
	return first == '\'' || first == '"' || first == '-' || first >= '0' && first <= '9' || strings.Contains(raw, " = ")
}

func canonicalLiteral(raw string) string {
	return strings.Join(strings.Fields(raw), " ")
}

func splitNamedTypeArg(raw string) (string, string, bool) {
	quote := byte(0)
	depth := 0
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		if quote != 0 {
			if char == quote && (index == 0 || raw[index-1] != '\\') {
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
		case ' ', '\t', '\n':
			if depth == 0 {
				label := strings.Trim(strings.TrimSpace(raw[:index]), "`")
				value := strings.TrimSpace(raw[index:])
				return label, value, label != "" && value != ""
			}
		}
	}
	return "", "", false
}

func splitTopLevel(raw string, separator byte) []string {
	var result []string
	start := 0
	depth := 0
	quote := byte(0)
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		if quote != 0 {
			if char == quote && (index == 0 || raw[index-1] != '\\') {
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
		default:
			if char == separator && depth == 0 {
				result = append(result, raw[start:index])
				start = index + 1
			}
		}
	}
	return append(result, raw[start:])
}

func canonicalType(value Type) Type {
	for index := range value.Args {
		if value.Args[index].Type != nil {
			canonical := canonicalType(*value.Args[index].Type)
			value.Args[index].Type = &canonical
		}
	}
	precision := map[string]string{"Decimal32": "9", "Decimal64": "18", "Decimal128": "38", "Decimal256": "76"}
	if digits, ok := precision[value.Name]; ok && len(value.Args) == 1 {
		return Type{Name: "Decimal", Args: []TypeArg{{Literal: digits}, value.Args[0]}}
	}
	return value
}

// Equal reports structural equality between two canonical type trees.
func (t Type) Equal(other Type) bool {
	if t.Name != other.Name || len(t.Args) != len(other.Args) {
		return false
	}
	for index := range t.Args {
		left, right := t.Args[index], other.Args[index]
		if left.Label != right.Label || left.Literal != right.Literal || (left.Type == nil) != (right.Type == nil) {
			return false
		}
		if left.Type != nil && !left.Type.Equal(*right.Type) {
			return false
		}
	}
	return true
}
