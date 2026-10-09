package engine

import (
	"fmt"
	"strings"
)

func serverLegacyDeclarations(sql string, declarations []ServerParameter, annotations []Param) ([]ServerParameter, error) {
	known := make(map[string]CHType)
	declared := make(map[string]bool)
	for _, declaration := range declarations {
		known[declaration.Name] = declaration.Type
		declared[declaration.Name] = true
	}
	tokens, err := scanSQLBoundary(sql)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].quoted || tokens[i].comment || tokens[i].text != "{" {
			continue
		}
		end := i + 1
		for end < len(tokens) && tokens[end].text != "}" {
			end++
		}
		if end == len(tokens) {
			break
		} // The boundary validator diagnoses this.
		name, text, found := strings.Cut(sql[tokens[i].end:tokens[end].start], ":")
		if found {
			typ, err := parseServerType(text)
			if err != nil {
				return nil, err
			}
			if _, exists := known[strings.TrimSpace(name)]; !exists {
				known[strings.TrimSpace(name)] = typ
			}
		}
		i = end
	}
	seen := make(map[string]bool)
	for _, annotation := range annotations {
		if seen[annotation.GoName] {
			return nil, fmt.Errorf("duplicate parameter annotation %s", annotation.GoName)
		}
		seen[annotation.GoName] = true
		if declared[annotation.GoName] {
			continue
		}
		typ, ok := known[annotation.GoName]
		if !ok {
			typ, ok = serverTypeForGoPrimitive(annotation.GoType)
		}
		if !ok {
			continue
		} // Ambiguous types require an explicit CH declaration.
		declarations = append(declarations, ServerParameter{Name: annotation.GoName, Type: typ})
		known[annotation.GoName] = typ
		declared[annotation.GoName] = true
	}
	return declarations, nil
}

func serverTypeForGoPrimitive(name string) (CHType, bool) {
	if inner, ok := strings.CutPrefix(name, "[]"); ok {
		typ, found := serverTypeForGoPrimitive(inner)
		return CHType{Name: "Array", Params: []CHType{typ}}, found
	}
	if inner, ok := strings.CutPrefix(name, "*"); ok {
		typ, found := serverTypeForGoPrimitive(inner)
		if !found {
			return CHType{}, false
		}
		return wrapNullable(typ), true
	}
	for _, family := range []string{"String", "Bool", "Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32", "UInt64", "Float32", "Float64"} {
		typ := CHType{Name: family}
		mapped, _ := goType(typ)
		if mapped == name {
			return typ, true
		}
	}
	return CHType{}, false
}
