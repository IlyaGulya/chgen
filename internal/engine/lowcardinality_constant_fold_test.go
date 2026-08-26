package engine

import "testing"

// ClickHouse keeps the LowCardinality wrapper of a result when exactly
// one operand is LowCardinality and every other operand is a CONSTANT.
// "Constant" means an expression that the server folds before it types
// the call. It does NOT mean "a literal in the SQL text".
//
// The difference shows in `if`. The server folds a constant condition
// and removes the dead branch BEFORE it types the branches, thus an
// `if` with a constant condition and a constant taken branch is itself
// a constant, although the dead branch names a column.
//
// Measured on ClickHouse 25.8.29.51 with the real fixture columns of
// oracleSchemaDDL (SELECT toTypeName(<expr>) FROM t; each expression
// was also run with SELECT <expr> FROM t and executed):
//
//	expression                                    server type
//	-129 % length(lcn)                            LowCardinality(Nullable(Int64))
//	if(false, i16, -129) % length(lcn)            LowCardinality(Nullable(Int64))
//	if(false, i16, -129) + length(lc)             LowCardinality(Int64)
//	if(false, i16, -129) - length(lc)             LowCardinality(Int64)
//	if(false, i16, -129) * length(lc)             LowCardinality(Int64)
//	if(false, i16, -129) / length(lc)             LowCardinality(Float64)
//	length(lc) + if(true, 1, i16)                 LowCardinality(Int64)
//	length(lc) + toInt64(if(false, i16, 1))       LowCardinality(Int64)
//	length(lc) + -if(false, i16, 1)               LowCardinality(Int64)
//	length(lc) + (if(false, i16, 1) + 2)          LowCardinality(Int64)
//	concat(lc, if(false, s, 'x'))                 LowCardinality(String)
//	concat(lc, if(true, 'x', s))                  LowCardinality(String)
//	lower(if(false, s, 'AB')) = lc                LowCardinality(UInt8)
//	lc = if(1 > 0, 'a', 'b')                      LowCardinality(UInt8)
//
// The negative cases keep the wrapper OFF. multiIf and CASE type every
// branch, thus a column in a dead branch still reaches the result. A
// non-constant `if` condition does not fold at all:
//
//	length(lc) + multiIf(false, i16, 1)           Int64
//	length(lc) + (CASE WHEN false THEN i16 ELSE 1 END)  Int64
//	length(lc) + if(b, 1, 2)                      UInt64
//	concat(lc, if(b, 'a', 'b'))                   String
//	length(lc) + if(false, i16, u8)               Int64
//	length(lc) + length(s)                        UInt64
//	lc = lower(s)                                 UInt8
//	lc || s                                       String
//
// Before the fix, chgen read "constant" as "a literal in the SQL text",
// thus every `if` row above with a column in a branch lost the wrapper.
// The constant-folding path of the `if` function itself already kept
// the wrapper, which is why the two paths disagreed.
//
// The predicate rows expect Bool where the server says UInt8. That is
// the known benign M4 base difference and is not part of this rule; the
// WRAPPER is what these cases pin.
func TestLowCardinalityConstantIfFoldKeepsWrapper(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// Arithmetic, all five operators, with the constant `if` on
		// either side.
		{"-129 % length(lcn)", "LowCardinality(Nullable(Int64))"},
		{"if(false, i16, -129) % length(lcn)", "LowCardinality(Nullable(Int64))"},
		{"if(false, i16, -129) + length(lc)", "LowCardinality(Int64)"},
		{"if(false, i16, -129) - length(lc)", "LowCardinality(Int64)"},
		{"if(false, i16, -129) * length(lc)", "LowCardinality(Int64)"},
		{"if(false, i16, -129) / length(lc)", "LowCardinality(Float64)"},
		{"length(lc) + if(true, 1, i16)", "LowCardinality(Int64)"},
		// The folded `if` stays a constant inside a further
		// constant expression.
		{"length(lc) + toInt64(if(false, i16, 1))", "LowCardinality(Int64)"},
		{"length(lc) + -if(false, i16, 1)", "LowCardinality(Int64)"},
		{"length(lc) + (if(false, i16, 1) + 2)", "LowCardinality(Int64)"},
		// The transparent function path.
		{"concat(lc, if(false, s, 'x'))", "LowCardinality(String)"},
		{"concat(lc, if(true, 'x', s))", "LowCardinality(String)"},
		// The comparison path. The base is UInt8: a comparison is a
		// predicate, and the predicate family always answers UInt8.
		{"lower(if(false, s, 'AB')) = lc", "LowCardinality(UInt8)"},
		{"lc = if(1 > 0, 'a', 'b')", "LowCardinality(UInt8)"},
	}
	for _, testCase := range cases {
		got := inferCHTypeString(t, schema, testCase.expr)
		if got != testCase.want {
			t.Errorf("inferCHTypeString(%q) = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestLowCardinalityConstantFoldNegatives pins the shapes that must NOT
// gain the wrapper. Without them the fix could widen into a silently
// wrong type, which is worse than the refusal it would replace.
func TestLowCardinalityConstantFoldNegatives(t *testing.T) {
	schema := wrapperTestSchema(t)
	cases := []struct{ expr, want string }{
		// multiIf and CASE type every branch; no fold.
		{"length(lc) + multiIf(false, i16, 1)", "Int64"},
		{"length(lc) + (CASE WHEN false THEN i16 ELSE 1 END)", "Int64"},
		// A non-constant condition does not fold.
		{"length(lc) + if(b, 1, 2)", "UInt64"},
		{"concat(lc, if(b, 'a', 'b'))", "String"},
		// A constant condition whose TAKEN branch is a column does
		// not make the `if` constant.
		{"length(lc) + if(false, i16, u8)", "Int64"},
		// Two column operands never keep the wrapper.
		{"length(lc) + length(s)", "UInt64"},
		{"lc = lower(s)", "UInt8"},
		{"lc || s", "String"},
		{"lc = s", "UInt8"},
		// Two LowCardinality operands lose the wrapper as well.
		{"length(lc) + length(lc)", "UInt64"},
		{"lc || lc", "String"},
	}
	for _, testCase := range cases {
		got := inferCHTypeString(t, schema, testCase.expr)
		if got != testCase.want {
			t.Errorf("inferCHTypeString(%q) = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}
