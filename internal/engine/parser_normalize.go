package engine

import "strings"

// normalizeCasePrefixOperands makes a parser-only copy for the simple CASE
// form when its operand starts with +, -, or NOT. The upstream parser reads
// CASE as a column name in this form. ClickHouse and the resolver accept the
// form.
//
// The function replaces one horizontal space after CASE with '(' and one
// horizontal space before WHEN with ')'. It does not add or remove bytes or
// line breaks. Thus all byte offsets and line numbers stay equal to the source.
// Runtime SQL stays unchanged.
func normalizeCasePrefixOperands(sql string) (string, bool, bool) {
	normalized := []byte(sql)
	changed := false
	unsafe := false
	plus := false
	for index := 0; index < len(sql); {
		switch {
		case sql[index] == '\'' || sql[index] == '"' || sql[index] == '`':
			index = skipSQLQuoted(sql, index, sql[index])
		case strings.HasPrefix(sql[index:], "--"):
			index = skipSQLLineComment(sql, index)
		case strings.HasPrefix(sql[index:], "/*"):
			index = skipSQLBlockComment(sql, index)
		case sqlKeywordAt(sql, index, "CASE"):
			operandStart := skipSQLWhitespace(sql, index+len("CASE"))
			if operandStart == index+len("CASE") || operandStart >= len(sql) ||
				!casePrefixOperatorAt(sql, operandStart) {
				index += len("CASE")
				continue
			}
			whenStart := simpleCaseWhenStart(sql, operandStart)
			if whenStart < 0 {
				index += len("CASE")
				continue
			}
			open := horizontalPaddingBefore(sql, operandStart, index+len("CASE"))
			close := horizontalPaddingBefore(sql, whenStart, operandStart)
			if open < 0 || close < 0 {
				unsafe = true
				index += len("CASE")
				continue
			}
			if sql[operandStart] == '+' {
				plus = true
			}
			normalized[open] = '('
			normalized[close] = ')'
			changed = true
			index += len("CASE")
		default:
			index++
		}
	}
	if !changed {
		return sql, unsafe, plus
	}
	return string(normalized), unsafe, plus
}

func casePrefixOperatorAt(sql string, index int) bool {
	return sql[index] == '+' || sql[index] == '-' || sqlKeywordAt(sql, index, "NOT")
}

func horizontalPaddingBefore(sql string, end, start int) int {
	for index := end - 1; index >= start; index-- {
		if sql[index] == ' ' || sql[index] == '\t' {
			return index
		}
		if sql[index] != '\n' && sql[index] != '\r' {
			return -1
		}
	}
	return -1
}

// simpleCaseWhenStart finds the first WHEN of one simple CASE operand. It
// ignores nested parentheses, arrays, quoted text, comments, and nested CASE
// expressions. A negative result makes the normalizer refuse to change SQL.
func simpleCaseWhenStart(sql string, operandStart int) int {
	parenDepth := 0
	bracketDepth := 0
	caseDepth := 0
	for index := operandStart; index < len(sql); {
		switch {
		case sql[index] == '\'' || sql[index] == '"' || sql[index] == '`':
			index = skipSQLQuoted(sql, index, sql[index])
		case strings.HasPrefix(sql[index:], "--"):
			index = skipSQLLineComment(sql, index)
		case strings.HasPrefix(sql[index:], "/*"):
			index = skipSQLBlockComment(sql, index)
		case sql[index] == '(':
			parenDepth++
			index++
		case sql[index] == ')':
			if parenDepth == 0 {
				return -1
			}
			parenDepth--
			index++
		case sql[index] == '[':
			bracketDepth++
			index++
		case sql[index] == ']':
			if bracketDepth == 0 {
				return -1
			}
			bracketDepth--
			index++
		case parenDepth == 0 && bracketDepth == 0 && sqlKeywordAt(sql, index, "CASE"):
			caseDepth++
			index += len("CASE")
		case parenDepth == 0 && bracketDepth == 0 && sqlKeywordAt(sql, index, "END"):
			if caseDepth == 0 {
				return -1
			}
			caseDepth--
			index += len("END")
		case parenDepth == 0 && bracketDepth == 0 && caseDepth == 0 && sqlKeywordAt(sql, index, "WHEN"):
			return index
		default:
			index++
		}
	}
	return -1
}
