package engine

import (
	"fmt"
	"strings"

	clickhouse "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// normalizeSchemaExchanges adapts only complete EXCHANGE statement shapes to
// the upstream RENAME grammar. Replacements retain byte offsets and newlines.
// The recorded positions select exchange semantics; they are never replayed as
// renames. Quoted tokens and comments are not interpreted as keywords.
func normalizeSchemaExchanges(sql string) (string, map[int]bool, error) {
	type token struct{ start, end int }
	var tokens []token
	out := []byte(sql)
	exchanges := make(map[int]bool)
	flush := func() error {
		defer func() { tokens = nil }()
		if len(tokens) == 0 || !strings.EqualFold(sql[tokens[0].start:tokens[0].end], "EXCHANGE") {
			return nil
		}
		word := func(i int, want string) bool {
			return i < len(tokens) && strings.EqualFold(sql[tokens[i].start:tokens[i].end], want)
		}
		line := lineOfOffset(sql, tokens[0].start)
		if word(1, "DICTIONARIES") {
			return fmt.Errorf("%d: EXCHANGE DICTIONARIES is not supported; supported EXCHANGE operation: EXCHANGE TABLES", line)
		}
		// Qualified names are deliberately outside the catalog boundary. Each
		// name is one token; the upstream parser validates identifier spelling.
		if !word(1, "TABLES") || !word(3, "AND") || !(len(tokens) == 5 || len(tokens) == 8 && word(5, "ON") && word(6, "CLUSTER")) {
			return fmt.Errorf("%d: expected EXCHANGE TABLES name AND name [ON CLUSTER cluster] with unqualified names", line)
		}
		for _, replacement := range []struct {
			index int
			text  string
		}{{0, "RENAME"}, {1, "TABLE"}, {3, "TO"}} {
			t := tokens[replacement.index]
			copy(out[t.start:t.end], replacement.text+strings.Repeat(" ", t.end-t.start-len(replacement.text)))
		}
		exchanges[tokens[0].start] = true
		return nil
	}
	for i := 0; i < len(sql); {
		switch {
		case strings.ContainsRune(" \t\r\n\f", rune(sql[i])):
			i++
		case strings.HasPrefix(sql[i:], "--"):
			i = skipSQLLineComment(sql, i)
		case strings.HasPrefix(sql[i:], "/*"):
			i = skipSQLBlockComment(sql, i)
		case sql[i] == ';':
			if err := flush(); err != nil {
				return "", nil, err
			}
			i++
		case sql[i] == '\'' || sql[i] == '"' || sql[i] == '`':
			end := skipSQLQuoted(sql, i, sql[i])
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
		return "", nil, err
	}
	return string(out), exchanges, nil
}

func applyCatalogExchange(catalogs *SchemaCatalogs, path string, line int, statement *clickhouse.RenameStmt) error {
	if len(statement.TargetPairList) != 1 {
		return fmt.Errorf("%s:%d: EXCHANGE TABLES requires exactly two names", path, line)
	}
	pair := statement.TargetPairList[0]
	if pair.Old.Database != nil || pair.New.Database != nil {
		return fmt.Errorf("%s:%d: EXCHANGE TABLES requires unqualified names", path, line)
	}
	a, b := pair.Old.Table.Name, pair.New.Table.Name
	if a == b {
		return fmt.Errorf("%s:%d: EXCHANGE TABLES requires distinct names, got %q twice", path, line, a)
	}
	for _, name := range []string{a, b} {
		if name == migrationsTableName {
			return fmt.Errorf("%s:%d: EXCHANGE TABLES cannot target excluded table %q", path, line, name)
		}
		if _, exists := catalogs.External.Tables[name]; exists {
			return fmt.Errorf("%s:%d: EXCHANGE TABLES %q targets an external schema; edit its CREATE TABLE instead", path, line, name)
		}
		if _, exists := catalogs.Physical.Tables[name]; !exists {
			return fmt.Errorf("%s:%d: EXCHANGE TABLES %q is an unknown table", path, line, name)
		}
	}
	left, right := catalogs.Physical.Tables[a], catalogs.Physical.Tables[b]
	left.Name, right.Name = b, a
	catalogs.Physical.Tables[a], catalogs.Physical.Tables[b] = right, left
	return nil
}
