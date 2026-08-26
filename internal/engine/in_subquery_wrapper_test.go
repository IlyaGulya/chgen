package engine

import "testing"

const inSubqueryWrapperSchema = `
CREATE TABLE g (
    c_bare_b Bool,
    c_bare_i32 Int32,
    c_bare_s String,
    c_lc_b LowCardinality(Bool),
    c_lc_i32 LowCardinality(Int32),
    c_lc_s LowCardinality(String),
    c_lcn_b LowCardinality(Nullable(Bool)),
    c_lcn_i32 LowCardinality(Nullable(Int32)),
    c_lcn_s LowCardinality(Nullable(String)),
    c_saf_b SimpleAggregateFunction(anyLast, Bool),
    c_saf_i32 SimpleAggregateFunction(anyLast, Int32),
    c_saf_s SimpleAggregateFunction(anyLast, String),
    c_safn_b SimpleAggregateFunction(anyLast, Nullable(Bool)),
    c_safn_i32 SimpleAggregateFunction(anyLast, Nullable(Int32)),
    c_safn_s SimpleAggregateFunction(anyLast, Nullable(String))
);
`

// TestInSubqueryReadsNullabilityThroughValueWrappers pins the measured
// result of IN and NOT IN with a set subquery. The Nullable wrapper can
// be at the top level, inside LowCardinality, or inside
// SimpleAggregateFunction. The server gives Nullable(UInt8) in all three
// cases. The server also executes every expression. The wrapper grid
// records both witnesses for these cells.
func TestInSubqueryReadsNullabilityThroughValueWrappers(t *testing.T) {
	schema, err := schemaFromDDLErr(t, inSubqueryWrapperSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"IN", "NOT IN"} {
		for _, base := range []string{"b", "i32", "s"} {
			for _, wrapper := range []string{"lcn", "safn"} {
				expression := "c_" + wrapper + "_" + base + " " + operation +
					" (SELECT c_" + wrapper + "_" + base + " FROM g)"
				query := Query{
					Name:    "in_subquery_nullable",
					Command: CommandMany,
					SQL:     "SELECT " + expression + " AS result FROM g",
				}
				if err := resolveQuery(&query, schema); err != nil {
					t.Errorf("%s: %v", expression, err)
					continue
				}
				if got := query.Results[0].CHType.String(); got != "Nullable(UInt8)" {
					t.Errorf("%s: result = %s, want Nullable(UInt8)", expression, got)
				}
			}
		}
	}
}

// TestInSubqueryDoesNotAddNullability pins the non-Nullable neighbours.
// A fix for a wrapped Nullable must not make a bare, LowCardinality, or
// SimpleAggregateFunction value Nullable.
func TestInSubqueryDoesNotAddNullability(t *testing.T) {
	schema, err := schemaFromDDLErr(t, inSubqueryWrapperSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"IN", "NOT IN"} {
		for _, base := range []string{"b", "i32", "s"} {
			for _, wrapper := range []string{"bare", "lc", "saf"} {
				expression := "c_" + wrapper + "_" + base + " " + operation +
					" (SELECT c_" + wrapper + "_" + base + " FROM g)"
				query := Query{
					Name:    "in_subquery_non_nullable",
					Command: CommandMany,
					SQL:     "SELECT " + expression + " AS result FROM g",
				}
				if err := resolveQuery(&query, schema); err != nil {
					t.Errorf("%s: %v", expression, err)
					continue
				}
				if got := query.Results[0].CHType.String(); got != "UInt8" {
					t.Errorf("%s: result = %s, want UInt8", expression, got)
				}
			}
		}
	}
}
