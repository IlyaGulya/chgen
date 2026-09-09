package engine

import (
	"fmt"
	"strings"
)

func hasResultContracts(queries []Query) bool {
	for _, query := range queries {
		for _, result := range query.Results {
			if result.Asserted {
				return true
			}
		}
	}
	return false
}

func renderResultContractCheck(query Query, empty string) string {
	if !hasResultContracts([]Query{query}) {
		return ""
	}
	var source strings.Builder
	source.WriteString("\t// Client-asserted result types are checked before scanning, including empty results.\n")
	fmt.Fprintf(&source, "\tif err := chgenCheckResultContract(rows, []string{")
	for _, result := range query.Results {
		fmt.Fprintf(&source, "%q,", result.SQLName)
	}
	source.WriteString("}, []string{")
	for _, result := range query.Results {
		if result.Asserted {
			fmt.Fprintf(&source, "%q,", result.CHType.String())
		} else {
			source.WriteString(`"",`)
		}
	}
	fmt.Fprintf(&source, "}); err != nil {\n\t\treturn %s, fmt.Errorf(%q, err)\n\t}\n", empty, query.Name+" result contract: %w")
	return source.String()
}

// Token boundaries and quoted contents matter; whitespace between tokens does
// not. No wrapper, parameter, or type-family equivalences are guessed here.
const resultContractHelpers = `
func chgenTypeTokens(text string) string {
	var tokens strings.Builder
	for i := 0; i < len(text); {
		if strings.ContainsRune(" \t\r\n", rune(text[i])) {
			i++
			continue
		}
		start := i
		if text[i] == '\'' || text[i] == '"' || text[i] == 96 {
			quote := text[i]
			i++
			closed := false
			for i < len(text) {
				if text[i] == '\\' {
					i += 2
					continue
				}
				if text[i] == quote {
					i++
					if i < len(text) && text[i] == quote {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return ""
			}
		} else if strings.ContainsRune("(),=", rune(text[i])) {
			i++
		} else {
			for i < len(text) && !strings.ContainsRune(" \t\r\n(),='\"", rune(text[i])) && text[i] != 96 {
				i++
			}
		}
		fmt.Fprintf(&tokens, "%d:%s", i-start, text[start:i])
	}
	return tokens.String()
}

func chgenCheckResultContract(rows driver.Rows, names, expected []string) error {
	columns := rows.ColumnTypes()
	actualNames := rows.Columns()
	if len(columns) != len(expected) || len(actualNames) != len(names) {
		return fmt.Errorf("missing or mismatched result metadata: want %d columns, got %d types and %d names", len(expected), len(columns), len(actualNames))
	}
	for i, want := range expected {
		if actualNames[i] != names[i] {
			return fmt.Errorf("column %d: expected alias %q, got %q", i+1, names[i], actualNames[i])
		}
		if want == "" {
			continue
		}
		if columns[i] == nil {
			return fmt.Errorf("column %d (%s): missing type metadata", i+1, names[i])
		}
		got := columns[i].DatabaseTypeName()
		if got == "" || chgenTypeTokens(got) != chgenTypeTokens(want) {
			return fmt.Errorf("column %d (%s): expected %s, got %s", i+1, names[i], want, got)
		}
	}
	return nil
}
`
