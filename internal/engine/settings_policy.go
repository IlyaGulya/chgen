package engine

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

const uncheckedSettingDirective = "-- chgen:unchecked-setting"

func parseUncheckedSettingAnnotation(line string, previous []string) (string, error) {
	value, ok := strings.CutPrefix(line, uncheckedSettingDirective+" ")
	if !ok {
		value, ok = strings.CutPrefix(line, uncheckedSettingDirective+"\t")
	}
	name := strings.TrimSpace(value)
	if !ok || !isSettingName(name) {
		return "", fmt.Errorf("expected %s name (one exact lowercase setting name)", uncheckedSettingDirective)
	}
	if slices.Contains(previous, name) {
		return "", fmt.Errorf("duplicate %s %s", uncheckedSettingDirective, name)
	}
	return name, nil
}

func isSettingName(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range name {
		if char >= 'a' && char <= 'z' || char == '_' || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

// omitUncheckedSettingsForResolution modifies only the freshly parsed resolver
// AST, never Query.SQL or the shared roster. The client explicitly accepts that
// type inference ignores these settings. The executable SQL retains every
// setting and value. Built-in rules always win, including after an upgrade that
// adds a previously unknown name to the roster.
func omitUncheckedSettingsForResolution(statement clickhouse.Expr, names []string) error {
	if len(names) == 0 {
		return nil
	}
	used := make(map[string]bool, len(names))
	for _, name := range names {
		used[name] = false
	}
	var failure error
	clickhouse.Walk(statement, func(node clickhouse.Expr) bool {
		if failure != nil {
			return false
		}
		settings, ok := node.(*clickhouse.SettingsClause)
		if !ok {
			return true
		}
		settings.Items = slices.DeleteFunc(settings.Items, func(item *clickhouse.SettingExpr) bool {
			if item == nil || item.Name == nil {
				return false // The normal validator owns malformed items.
			}
			name := item.Name.Name
			if _, allowed := used[name]; !allowed {
				return false
			}
			used[name] = true
			if _, builtIn := selectSettingRoster[name]; builtIn {
				return false
			}
			if err := validateUncheckedSettingLiteral(item); err != nil {
				failure = err
				return false
			}
			return true
		})
		return true
	})
	if failure != nil {
		return failure
	}
	for _, name := range names {
		if !used[name] {
			return fmt.Errorf("%s %s does not match a SETTINGS item in this query", uncheckedSettingDirective, name)
		}
	}
	return nil
}

func validateUncheckedSettingLiteral(item *clickhouse.SettingExpr) error {
	switch literal := item.Expr.(type) {
	case *clickhouse.BoolLiteral, *clickhouse.StringLiteral:
		return nil
	case *clickhouse.NumberLiteral:
		if _, err := strconv.ParseUint(literal.Literal, 10, 64); err == nil {
			return nil
		}
	}
	return fmt.Errorf("unchecked SETTINGS %s must be an unsigned integer, Boolean, or string literal", item.Name.Name)
}
