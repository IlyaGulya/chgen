package engine

import (
	"fmt"
	"strings"
)

// Explicit CH types remove the need to infer an argument through an unknown
// function. Only placeholders change; the server owns expression semantics.
func normalizeServerArguments(sql string, declarations []ServerParameter) (string, error) {
	tokens, err := scanSQLBoundary(sql)
	if err != nil {
		return "", err
	}
	byName := make(map[string]CHType, len(declarations))
	for _, param := range declarations {
		if _, exists := byName[param.Name]; exists {
			return "", fmt.Errorf("duplicate parameter declaration %s", param.Name)
		}
		byName[param.Name] = param.Type
	}
	var out strings.Builder
	position, positional := 0, 0
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if token.comment || token.quoted {
			continue
		}
		var name string
		end := token.end
		if token.text == "?" {
			if positional >= len(declarations) {
				return "", fmt.Errorf("positional parameter requires -- param-chtype: Name ClickHouseType")
			}
			name = declarations[positional].Name
			positional++
		} else {
			var matched bool
			name, end, matched, err = parseNamedArgAt(sql, token.start)
			if err != nil {
				return "", err
			}
			if !matched {
				continue
			}
		}
		typ, ok := byName[name]
		if !ok {
			return "", fmt.Errorf("argument %s requires -- param-chtype: %s ClickHouseType", name, name)
		}
		out.WriteString(sql[position:token.start])
		fmt.Fprintf(&out, "{%s:%s}", name, typ.String())
		position = end
		for i+1 < len(tokens) && tokens[i+1].start < end {
			i++
		}
	}
	out.WriteString(sql[position:])
	normalized := out.String()
	_, parameters, err := PrepareServerSelect(normalized)
	if err != nil {
		return "", err
	}
	for _, declaration := range declarations {
		found := false
		for _, param := range parameters {
			if param.Name != declaration.Name {
				continue
			}
			if param.Type.String() != declaration.Type.String() {
				return "", fmt.Errorf("conflicting type for parameter %s", param.Name)
			}
			found = true
		}
		if !found {
			return "", fmt.Errorf("unused parameter declaration %s", declaration.Name)
		}
	}
	return normalized, nil
}
