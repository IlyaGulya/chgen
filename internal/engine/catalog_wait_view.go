package engine

import (
	"fmt"
	"strings"
)

// normalizeSchemaWaitViews validates the complete SYSTEM WAIT VIEW [db.]name
// grammar, then blanks that statement in a parser-only copy. Waiting changes
// neither columns nor modeled engine metadata, and views are not cataloged.
// Semicolons, byte offsets and line breaks remain intact. All other
// statements still reach the upstream parser; query SQL never uses this adapter.
func normalizeSchemaWaitViews(sql string) (string, error) {
	type token struct{ start, end int }
	var tokens []token
	var comments []token
	out := []byte(sql)
	flush := func() error {
		defer func() { tokens, comments = nil, nil }()
		word := func(i int, want string) bool {
			return i < len(tokens) && strings.EqualFold(sql[tokens[i].start:tokens[i].end], want)
		}
		if !word(0, "SYSTEM") || !word(1, "WAIT") {
			return nil
		}
		if !word(2, "VIEW") || !(len(tokens) == 4 || len(tokens) == 6 && word(4, ".")) {
			return fmt.Errorf("%d: expected SYSTEM WAIT VIEW [database.]view_name", lineOfOffset(sql, tokens[0].start))
		}
		for i := 3; i < len(tokens); i += 2 {
			t := tokens[i]
			if !schemaWaitViewIdentifier(sql[t.start:t.end]) {
				return fmt.Errorf("%d: SYSTEM WAIT VIEW expects an identifier, got %q", lineOfOffset(sql, t.start), sql[t.start:t.end])
			}
		}
		for _, t := range append(tokens, comments...) {
			for i := t.start; i < t.end; i++ {
				if out[i] != '\n' && out[i] != '\r' {
					out[i] = ' '
				}
			}
		}
		return nil
	}
	for i := 0; i < len(sql); {
		switch {
		case strings.ContainsRune(" \t\r\n\f\v", rune(sql[i])):
			i++
		case strings.HasPrefix(sql[i:], "--"):
			i = skipSQLLineComment(sql, i)
		case strings.HasPrefix(sql[i:], "/*"):
			end, closed := schemaBlockCommentEnd(sql, i)
			if !closed {
				return "", fmt.Errorf("%d: unterminated block comment", lineOfOffset(sql, i))
			}
			comments = append(comments, token{i, end})
			i = end
		case sql[i] == ';':
			if err := flush(); err != nil {
				return "", err
			}
			i++
		case sql[i] == '\'' || sql[i] == '"' || sql[i] == '`':
			end, closed := schemaQuotedEnd(sql, i)
			if !closed {
				return "", fmt.Errorf("%d: unterminated quoted token", lineOfOffset(sql, i))
			}
			tokens = append(tokens, token{i, end})
			i = end
		default:
			start := i
			if isSQLIdentifierByte(sql[i]) {
				for i < len(sql) && isSQLIdentifierByte(sql[i]) {
					i++
				}
			} else {
				i++
			}
			tokens = append(tokens, token{start, i})
		}
	}
	if err := flush(); err != nil {
		return "", err
	}
	return string(out), nil
}

// Quoted tokens have already been checked for a closing delimiter. Bare names
// follow ClickHouse's [a-zA-Z_][a-zA-Z0-9_]* identifier grammar; strings are not
// identifiers. Unlike table operations, WAIT needs no catalog name resolution.
func schemaWaitViewIdentifier(raw string) bool {
	if raw[0] == '`' || raw[0] == '"' {
		return len(raw) > 2
	}
	if raw[0] >= '0' && raw[0] <= '9' {
		return false
	}
	for i := range len(raw) {
		if !isSQLIdentifierByte(raw[i]) {
			return false
		}
	}
	return true
}

func schemaQuotedEnd(sql string, start int) (int, bool) {
	quote := sql[start]
	for i := start + 1; i < len(sql); i++ {
		if sql[i] == '\\' {
			i++
			continue
		}
		if sql[i] != quote {
			continue
		}
		if i+1 < len(sql) && sql[i+1] == quote {
			i++
			continue
		}
		return i + 1, true
	}
	return len(sql), false
}

func schemaBlockCommentEnd(sql string, start int) (int, bool) {
	depth := 1
	for i := start + 2; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(sql[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i, true
			}
		default:
			i++
		}
	}
	return len(sql), false
}
