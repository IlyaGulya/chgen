package engine

import (
	"strings"
	"testing"
)

// The parse-level raw placeholder check keeps anonymous placeholders out of
// schema-aware sources. These tests call resolveQuery directly to pin the
// resolver root cause: anonymous placeholders must never share one generated
// parameter only because they touch the same column.

func TestResolveQueryKeepsAnonymousPlaceholdersSeparate(t *testing.T) {
	schema := rawPlaceholderTestSchema(t)
	query := Query{
		Name:    "Range",
		Command: CommandMany,
		SQL:     "SELECT id FROM events WHERE ts >= ? AND ts < ?",
	}
	if err := resolveQuery(&query, schema); err != nil {
		t.Fatalf("resolveQuery() error = %v", err)
	}
	if got, want := len(query.Params), 2; got != want {
		t.Fatalf("params = %#v, want %d separate parameters", query.Params, want)
	}
	if got, want := query.ParamIndexes, []int{0, 1}; !equalInts(got, want) {
		t.Fatalf("parameter indexes = %#v, want %#v", got, want)
	}
	if query.Params[0].GoName == query.Params[1].GoName {
		t.Fatalf("both parameters generate Go field %s", query.Params[0].GoName)
	}
}

func TestResolveQueryDoesNotPropagateHintsAcrossConjunction(t *testing.T) {
	schema := rawPlaceholderTestSchema(t)
	query := Query{
		Name:    "AndProp",
		Command: CommandMany,
		SQL:     "SELECT id FROM events WHERE user_id = ? AND val > abs(?)",
	}
	err := resolveQuery(&query, schema)
	if err == nil {
		if len(query.Params) != 2 || query.Params[0].GoType == query.Params[1].GoType {
			t.Fatalf("params = %#v, want two differently typed parameters or an error", query.Params)
		}
		return
	}
	if !strings.Contains(err.Error(), "no inferable") {
		t.Fatalf("error = %v, want a missing-type error", err)
	}
}

func TestParseWithSchemaDoesNotPoisonNamedArgAcrossConjunction(t *testing.T) {
	schema := rawPlaceholderTestSchema(t)
	_, err := parseQueriesWithSchema(t, `-- name: AndProp :many
SELECT id
FROM events
WHERE user_id = chgen.arg('UserID') AND val > abs(chgen.arg('Threshold'))`, schema)
	if err == nil {
		t.Fatal("parseQueriesWithSchema() succeeded; Threshold silently took the user_id type")
	}
	if !strings.Contains(err.Error(), "Threshold") {
		t.Fatalf("error = %v, want it to name the untypeable parameter Threshold", err)
	}
}

func TestParseWithSchemaNamedArgConjunctionTypeFromAnnotation(t *testing.T) {
	schema := rawPlaceholderTestSchema(t)
	queries, err := parseQueriesWithSchema(t, `-- name: AndProp :many
-- param: Threshold float64
SELECT id
FROM events
WHERE user_id = chgen.arg('UserID') AND val > abs(chgen.arg('Threshold'))`, schema)
	if err != nil {
		t.Fatalf("parseQueriesWithSchema() error = %v", err)
	}
	if got, want := queries[0].Params, []Param{
		{GoName: "UserID", GoType: "uint64"},
		{GoName: "Threshold", GoType: "float64"},
	}; !equalParams(got, want) {
		t.Fatalf("params = %#v, want %#v", got, want)
	}
}
