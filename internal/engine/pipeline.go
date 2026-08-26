package engine

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// ParseQueryFiles parses ordered annotated query files against the physical
// and external catalogs of one package. Every query records the file and the
// line of its -- name annotation; a duplicate query name across the files of
// one package is an error.
func ParseQueryFiles(paths []string, catalogs *SchemaCatalogs) ([]Query, error) {
	if catalogs == nil || catalogs.Physical == nil {
		return nil, fmt.Errorf("schema catalogs are required for schema-aware parsing")
	}
	var queries []Query
	type location struct {
		file string
		line int
	}
	seen := make(map[string]location)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read query file %q: %w", path, err)
		}
		if err := rejectRemovedIncludeDirective(path, string(data)); err != nil {
			return nil, err
		}
		parsed, err := parseQueriesInFile(path, string(data), catalogs.Physical, catalogs.External)
		if err != nil {
			return nil, err
		}
		for _, query := range parsed {
			if previous, exists := seen[query.Name]; exists {
				return nil, fmt.Errorf("%s:%d: duplicate query name %s; first declared at %s:%d", query.File, query.Line, query.Name, previous.file, previous.line)
			}
			seen[query.Name] = location{file: query.File, line: query.Line}
			queries = append(queries, query)
		}
	}
	return queries, nil
}

// externalReferenceError is a chgen.external resolution failure. Its message
// already carries the complete diagnostic, so the per-query wrapper adds only
// the file and line.
type externalReferenceError struct {
	message string
}

func (e *externalReferenceError) Error() string {
	return e.message
}

// suggestName returns the single candidate within edit distance 2 of name,
// or "" when no candidate qualifies or two candidates tie.
func suggestName(name string, candidates []string) string {
	sort.Strings(candidates)
	best := ""
	bestDistance := 3
	tie := false
	for _, candidate := range candidates {
		if candidate == name {
			continue
		}
		distance := editDistance(name, candidate)
		if distance < bestDistance {
			best = candidate
			bestDistance = distance
			tie = false
		} else if distance == bestDistance && candidate != best {
			tie = true
		}
	}
	if tie || bestDistance > 2 {
		return ""
	}
	return best
}

func editDistance(left, right string) int {
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for column := range previous {
		previous[column] = column
	}
	for row := 1; row <= len(left); row++ {
		current[0] = row
		for column := 1; column <= len(right); column++ {
			cost := 1
			if left[row-1] == right[column-1] {
				cost = 0
			}
			current[column] = minInt(previous[column]+1, current[column-1]+1, previous[column-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(right)]
}

func minInt(values ...int) int {
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

// removedIncludeDirective is the header directive that included an external
// row-schema file before the -- chgen:external marker replaced it.
const removedIncludeDirective = "-- chgen:external-schema"

// rejectRemovedIncludeDirective fails a query file that still carries the
// removed include directive. Without this check the directive is an ordinary
// comment, and the file fails later with "unknown external schema", which does
// not tell the reader what to do with the stale line.
//
// The scan is lexical and examines comment lines only, so the same text inside
// a SQL string literal stays data.
func rejectRemovedIncludeDirective(path, input string) error {
	for index, line := range strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != removedIncludeDirective && !strings.HasPrefix(trimmed, removedIncludeDirective+" ") &&
			!strings.HasPrefix(trimmed, removedIncludeDirective+"\t") {
			continue
		}
		included := strings.TrimSpace(strings.TrimPrefix(trimmed, removedIncludeDirective))
		target := "the external schema file"
		if included != "" {
			target = fmt.Sprintf("%q", included)
		}
		return fmt.Errorf(
			"%s:%d: the %s directive was removed; delete this line, add %s to schema in chgen.yaml, and mark each row schema with -- chgen:external before its CREATE TABLE",
			path, index+1, removedIncludeDirective, target,
		)
	}
	return nil
}
