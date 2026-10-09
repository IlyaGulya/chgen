package engine

import (
	"fmt"
	"strings"
)

// Lower before selecting optional blocks so positional declarations retain
// their original correspondence even when an earlier block is omitted.
// Explicit types avoid inference through unknown functions; only placeholders
// change, while the server remains responsible for expression semantics.
func lowerServerArguments(sql string, declarations []ServerParameter) (string, error) {
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
	return out.String(), nil
}
