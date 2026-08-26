package engine

import "testing"

// commonCHType must keep a Nullable ELEMENT when it takes the supertype
// of two containers. The bare compatibleNamedArgTypes check used to
// treat Array(Int32) and Array(Nullable(Int32)) as "the same type" and
// answer the bare left operand, which silently drops the element
// wrapper.
//
// Measured on ClickHouse 25.8.29.51 with real columns, over both
// toTypeName (the analysis) and the executed value (the server really
// carries a NULL, so the loss is not cosmetic):
//
//	a  Array(Int32), an Array(Nullable(Int32))
//	al Array(Int64), anl Array(Nullable(Int64))
//	m  Map(String, Int32), mn Map(String, Nullable(Int32))
//	tp Tuple(Int32, String), tpn Tuple(Nullable(Int32), String)
//	aa Array(Array(Int32)), aan Array(Array(Nullable(Int32)))
//
//	concat(a, an)            Array(Nullable(Int32))   [1,2,1,NULL]
//	concat(a, anl)           Array(Nullable(Int64))   [1,2,1,NULL]
//	concat(al, an)           Array(Nullable(Int64))   [1,1,NULL]
//	if(1, m, mn)             Map(String, Nullable(Int32))
//	if(1, tp, tpn)           Tuple(Nullable(Int32), String)
//	concat(aa, aan)          Array(Array(Nullable(Int32)))   [[1],[1,NULL]]
//
// A Tuple SIZE mismatch still has no supertype on the server (measured:
// if(b, Tuple(i32, s), Tuple(1, 2, 3)) is Code 386, NO_COMMON_TYPE,
// "Tuples have different sizes"), and chgen must refuse that pair too.
func supertypeContainerNullableElementSchema(t *testing.T) *Schema {
	t.Helper()
	schema, err := schemaFromDDLErr(t, `CREATE TABLE t (
    a    Array(Int32),  an  Array(Nullable(Int32)),
    al   Array(Int64),  anl Array(Nullable(Int64)),
    m    Map(String, Int32), mn Map(String, Nullable(Int32)),
    tp   Tuple(Int32, String), tpn Tuple(Nullable(Int32), String),
    tp3  Tuple(Int32, Int32, Int32),
    aa   Array(Array(Int32)), aan Array(Array(Nullable(Int32)))
) ENGINE = MergeTree ORDER BY tuple();`)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	return schema
}

func TestCommonCHTypeKeepsNullableContainerElement(t *testing.T) {
	schema := supertypeContainerNullableElementSchema(t)
	cases := []struct{ expr, want string }{
		// Array element Nullable, same base type.
		{"concat(a, an)", "Array(Nullable(Int32))"},
		// Array element Nullable, base type ALSO widens.
		{"concat(a, anl)", "Array(Nullable(Int64))"},
		{"concat(al, an)", "Array(Nullable(Int64))"},
		// Map VALUE Nullable.
		{"if(1, m, mn)", "Map(String, Nullable(Int32))"},
		// Tuple ELEMENT Nullable.
		{"if(1, tp, tpn)", "Tuple(Nullable(Int32), String)"},
		// Nested Array depth.
		{"concat(aa, aan)", "Array(Array(Nullable(Int32)))"},
		// Two identical types still take the fast path.
		{"concat(a, a)", "Array(Int32)"},
	}
	for _, testCase := range cases {
		if got := inferCHTypeString(t, schema, testCase.expr); got != testCase.want {
			t.Errorf("type of %q = %s, want %s", testCase.expr, got, testCase.want)
		}
	}
}

// A Tuple size mismatch has no supertype on the server and chgen must
// refuse it, not answer a guessed shape.
func TestCommonCHTypeRefusesTupleSizeMismatch(t *testing.T) {
	schema := supertypeContainerNullableElementSchema(t)
	if _, err := inferCHTypeStringErr(t, schema, "if(1, tp, tp3)"); err == nil {
		t.Fatalf("if(1, tp, tp3) = no error, want a refusal (Tuple size mismatch has no supertype)")
	}
}
