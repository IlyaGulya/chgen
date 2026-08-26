package engine

import "testing"

// This file pins the COMMON TYPE rule of greatest and least, and the
// grid of container shapes that goes with it.
//
// Two defects made this file necessary.
//
// the regression: greatest and least read only the FIRST argument to find
// the result type. The server folds the common type over EVERY
// argument. Because chgen read the first argument, it was correct by
// accident whenever the widest argument came first, and silently wrong
// when it came last. A test that measures one argument order only
// cannot see this: half the cells pass.
//
// the regression: greatest and least keep a NESTED LowCardinality wrapper at
// every arity. The server removes the wrapper at every depth below the
// top. Only a TOP-LEVEL scalar wrapper at arity one survives.
//
// Every expected value below was measured on ClickHouse 25.8.29.51
// through the HTTP interface, against real columns of a real table in
// the probe database, never over literals, because the server folds a
// constant argument and then gives a different answer.
const greatestLeastCommonTypeSchema = `
CREATE TABLE t (
    i32  Int32,
    i64  Int64,
    u64  UInt64,
    f64  Float64,
    dec  Decimal(18, 4),
    d    Date,
    dt   DateTime,
    s    String,
    fs   FixedString(8),
    lc   LowCardinality(String),
    lcn  LowCardinality(Nullable(String)),
    ni32 Nullable(Int32),
    alc  Array(LowCardinality(String)),
    as_  Array(String),
    ai32 Array(Int32),
    tlc  Tuple(LowCardinality(String)),
    mlc  Map(String, LowCardinality(String))
);
`

// TestGreatestLeastFoldsCommonTypeOverEveryArgument is the regression
// regression. Each pair appears in BOTH argument orders, because the
// defect was invisible in one order.
//
// Measured on ClickHouse 25.8.29.51:
//
//	SELECT toTypeName(greatest(i32, u64)) FROM t -> Int128
//	SELECT toTypeName(greatest(u64, i32)) FROM t -> Int128
//	SELECT toTypeName(greatest(i32, dec)) FROM t -> Decimal(18, 4)
//	SELECT toTypeName(greatest(d, dt)) FROM t    -> DateTime
//	SELECT toTypeName(greatest(fs, s)) FROM t    -> String
//
// The answer does not depend on the argument order in any cell.
func TestGreatestLeastFoldsCommonTypeOverEveryArgument(t *testing.T) {
	schema, err := schemaFromDDLErr(t, greatestLeastCommonTypeSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// The widening pairs, widest argument LAST. chgen answered the
		// type of the first argument in every one of these before the
		// fix.
		{"greatest(i32, f64)", "Float64"},
		{"greatest(i32, dec)", "Decimal(18, 4)"},
		{"greatest(i32, i64)", "Int64"},
		{"greatest(d, dt)", "DateTime"},
		{"greatest(fs, s)", "String"},
		// The same pairs, widest argument FIRST. These already passed
		// before the fix, by accident. They must not regress.
		{"greatest(f64, i32)", "Float64"},
		{"greatest(dec, i32)", "Decimal(18, 4)"},
		{"greatest(i64, i32)", "Int64"},
		{"greatest(dt, d)", "DateTime"},
		{"greatest(s, fs)", "String"},
		// least gives the same answer in every cell.
		{"least(i32, f64)", "Float64"},
		{"least(i32, dec)", "Decimal(18, 4)"},
		{"least(d, dt)", "DateTime"},
		{"least(fs, s)", "String"},
		{"least(f64, i32)", "Float64"},
		{"least(dec, i32)", "Decimal(18, 4)"},
		{"least(dt, d)", "DateTime"},
		{"least(s, fs)", "String"},
		// A Nullable argument in any position makes the result
		// Nullable, and the common type is still folded.
		{"greatest(ni32, i32)", "Nullable(Int32)"},
		{"greatest(i32, ni32)", "Nullable(Int32)"},
		{"greatest(ni32, ni32)", "Nullable(Int32)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestGreatestLeastRefusesWhenNoCommonTypeExists locks the refusal
// boundary that the common-type fold creates.
//
// A mix of a signed integer, an unsigned 64-bit integer and a float has
// NO supertype on the server, in either argument order. chgen must
// refuse it rather than answer a type.
//
// Measured on ClickHouse 25.8.29.51:
//
//	SELECT toTypeName(greatest(i32, u64, f64)) FROM t
//	  -> Code: 386 (NO_COMMON_TYPE) "There is no supertype for types
//	     Float64, UInt64, Int32 ..."
//	SELECT toTypeName(greatest(f64, u64, i32)) FROM t
//	  -> Code: 386 (NO_COMMON_TYPE)
//
// Before the fix chgen answered Int32 for the first and Float64 for the
// second, which is a silently wrong type on both sides of a refusal.
func TestGreatestLeastRefusesWhenNoCommonTypeExists(t *testing.T) {
	schema, err := schemaFromDDLErr(t, greatestLeastCommonTypeSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	exprs := []string{
		"greatest(i32, u64, f64)",
		"greatest(f64, u64, i32)",
		"least(i32, u64, f64)",
		"least(f64, u64, i32)",
	}
	for _, expr := range exprs {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+expr+" AS a FROM t")
		if err == nil {
			t.Errorf("%s: CH type = %q, want a refusal", expr, got)
		}
	}
}

// TestGreatestLeastAcceptsSignedUnsignedMix confirms that the call site
// under test, inferSelectItemCHType through greatestLeastFunctionType in
// infer_function.go, is now wired to greatestLeastCommonCHTypes
// (supertype.go) instead of calling commonCHTypes directly.
//
// greatest and least are NOT members of the branch family on this axis.
// The branch family refuses a signed integer mixed with UInt64, and
// greatest and least accept it at arity two (measured on ClickHouse
// 25.8.29.51 with real columns):
//
//	if(c, i32, u64)     Code: 386 (NO_COMMON_TYPE)
//	greatest(i32, u64)  Int128          least(i32, u64)   Int128
//	greatest(i8, u64)   Int128          least(i8, u64)    Int128
//	greatest(i64, u64)  UInt64          least(i64, u64)   Int64
//
// The first common-type change added the fix itself as
// greatestLeastCommonCHTypes in supertype.go, together with its own unit
// test (TestGreatestLeastCommonCHTypesSignedUInt64Pair in this file).
// A later change wired infer_function.go's greatestLeastFunctionType
// to call it, so the four expressions below now answer Int128 instead of
// refusing, and this test was renamed and its expectation flipped to
// match.
//
// The three measured reasons a common-type rule cannot express this
// mix still hold and still justify keeping greatestLeastCommonCHTypes
// SEPARATE from commonCHTypes rather than widening the shared rule:
//
//   - The accepted answers are irregular. Int32 with UInt64 widens to
//     Int128, but Int64 with UInt64 does not widen at all.
//   - The answer depends on the FUNCTION, not only on the argument
//     types: greatest(i64, u64) is UInt64 and least(i64, u64) is Int64.
//     A common-type rule cannot express that, because it never sees
//     which function asked.
//   - The acceptance does not survive arity three. greatest(i32, u64,
//     i8) is Code 386 again, although the pair greatest(i32, u64) is
//     accepted.
func TestGreatestLeastAcceptsSignedUnsignedMix(t *testing.T) {
	schema, err := schemaFromDDLErr(t, greatestLeastCommonTypeSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	exprs := []string{
		"greatest(i32, u64)",
		"greatest(u64, i32)",
		"least(i32, u64)",
		"least(u64, i32)",
	}
	for _, expr := range exprs {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want Int128", expr, err)
			continue
		}
		if got != "Int128" {
			t.Errorf("%s: CH type = %q, want Int128", expr, got)
		}
	}
}

// TestGreatestLeastDropsNestedLowCardinality is the regression
// regression. A LowCardinality wrapper BELOW the top level goes away at
// EVERY arity, for every container shape.
//
// Measured on ClickHouse 25.8.29.51:
//
//	SELECT toTypeName(greatest(alc)) FROM t       -> Array(String)
//	SELECT toTypeName(greatest(alc, alc)) FROM t  -> Array(String)
//	SELECT toTypeName(greatest(alc, as_)) FROM t  -> Array(String)
//	SELECT toTypeName(greatest(as_, alc)) FROM t  -> Array(String)
//	SELECT toTypeName(greatest(tlc)) FROM t       -> Tuple(String)
//	SELECT toTypeName(greatest(mlc)) FROM t       -> Map(String, String)
//
// Note the contrast with the scalar cell in
// TestGreatestLeastKeepsLowCardinalityAtArityOne: greatest(lc) at arity
// one KEEPS its wrapper. The rule is therefore NOT one rule. A reader
// who measures the scalar cell alone, or the Array cell alone, gets a
// wrong rule either way. That partial reading is what let this defect
// live, so both halves are pinned here together.
func TestGreatestLeastDropsNestedLowCardinality(t *testing.T) {
	schema, err := schemaFromDDLErr(t, greatestLeastCommonTypeSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// Array base, arity one. chgen answered
		// Array(LowCardinality(String)) here before the fix.
		{"greatest(alc)", "Array(String)"},
		{"least(alc)", "Array(String)"},
		// Array base, arity two and three, and both argument orders.
		{"greatest(alc, alc)", "Array(String)"},
		{"greatest(alc, as_)", "Array(String)"},
		{"greatest(as_, alc)", "Array(String)"},
		{"greatest(alc, alc, alc)", "Array(String)"},
		{"least(alc, alc)", "Array(String)"},
		{"least(alc, as_)", "Array(String)"},
		// Tuple and Map bases drop the nested wrapper at every arity
		// too, thus the rule is about DEPTH, not about Array.
		{"greatest(tlc)", "Tuple(String)"},
		{"greatest(tlc, tlc)", "Tuple(String)"},
		{"greatest(mlc)", "Map(String, String)"},
		{"greatest(mlc, mlc)", "Map(String, String)"},
		{"least(tlc)", "Tuple(String)"},
		{"least(mlc)", "Map(String, String)"},
		// A container with no wrapper is unchanged, which shows the
		// strip removes a wrapper and does not rewrite the shape.
		{"greatest(ai32)", "Array(Int32)"},
		{"greatest(ai32, ai32)", "Array(Int32)"},
		{"greatest(as_, as_)", "Array(String)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestGreatestLeastTopLevelLowCardinalityGrid pins the boundary between
// the cell that KEEPS a top-level LowCardinality wrapper and the cells
// that drop it. The wrapper survives in exactly ONE cell: a scalar base
// at arity one.
//
// Measured on ClickHouse 25.8.29.51:
//
//	SELECT toTypeName(greatest(lc)) FROM t        -> LowCardinality(String)
//	SELECT toTypeName(greatest(lc, lc)) FROM t    -> String
//	SELECT toTypeName(greatest(lc, s)) FROM t     -> String
//	SELECT toTypeName(greatest(s, lc)) FROM t     -> String
//	SELECT toTypeName(greatest(lcn)) FROM t       -> LowCardinality(Nullable(String))
//	SELECT toTypeName(greatest(lcn, lc)) FROM t   -> Nullable(String)
func TestGreatestLeastTopLevelLowCardinalityGrid(t *testing.T) {
	schema, err := schemaFromDDLErr(t, greatestLeastCommonTypeSchema)
	if err != nil {
		t.Fatalf("schemaFromDDLErr() error = %v", err)
	}
	cases := []struct{ expr, want string }{
		// The one surviving cell, and its Nullable form.
		{"greatest(lc)", "LowCardinality(String)"},
		{"greatest(lcn)", "LowCardinality(Nullable(String))"},
		{"least(lc)", "LowCardinality(String)"},
		// Arity two drops it, in both argument orders, even when EVERY
		// argument carries the wrapper.
		{"greatest(lc, lc)", "String"},
		{"greatest(lc, s)", "String"},
		{"greatest(s, lc)", "String"},
		{"greatest(lc, lc, lc)", "String"},
		{"greatest(lcn, lcn)", "Nullable(String)"},
		{"greatest(lcn, s)", "Nullable(String)"},
		{"greatest(lcn, lc)", "Nullable(String)"},
	}
	for _, testCase := range cases {
		got, err := inferSelectItemCHType(t, schema, "SELECT "+testCase.expr+" AS a FROM t")
		if err != nil {
			t.Errorf("%s: error = %v, want %q", testCase.expr, err, testCase.want)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: CH type = %q, want %q", testCase.expr, got, testCase.want)
		}
	}
}

// TestGreatestLeastCommonCHTypesSignedUInt64Pair pins
// greatestLeastCommonCHTypes, the fix that the related regressions added.
// This is a UNIT test of the helper itself, not an end-to-end test
// through inferSelectItemCHType. infer_function.go now calls this
// helper instead of commonCHTypes, so the fix is
// also observable at the SQL level. See
// TestGreatestLeastAcceptsSignedUnsignedMix for the end-to-end
// behaviour.
//
// Every want value below is a direct transcription of a measurement on
// ClickHouse 25.8.29.51 with real table columns (see the doc comment of
// greatestLeastCommonCHTypes for the full grid), never over literals.
func TestGreatestLeastCommonCHTypesSignedUInt64Pair(t *testing.T) {
	i8 := CHType{Name: "Int8"}
	i16 := CHType{Name: "Int16"}
	i32 := CHType{Name: "Int32"}
	i64 := CHType{Name: "Int64"}
	u8 := CHType{Name: "UInt8"}
	u64 := CHType{Name: "UInt64"}
	int128 := CHType{Name: "Int128"}

	cases := []struct {
		name  string
		types []CHType
		want  CHType
	}{
		{"greatest", []CHType{i32, u64}, int128},
		{"greatest", []CHType{u64, i32}, int128},
		{"least", []CHType{i32, u64}, int128},
		{"least", []CHType{u64, i32}, int128},
		{"greatest", []CHType{i8, u64}, int128},
		{"least", []CHType{i8, u64}, int128},
		{"greatest", []CHType{i16, u64}, int128},
		{"least", []CHType{i16, u64}, int128},
		// The one pair that does not widen at all, and the one row
		// where greatest and least disagree with EACH OTHER on the
		// identical operand pair.
		{"greatest", []CHType{i64, u64}, CHType{Name: "UInt64"}},
		{"least", []CHType{i64, u64}, CHType{Name: "Int64"}},
		{"greatest", []CHType{u64, i64}, CHType{Name: "UInt64"}},
		{"least", []CHType{u64, i64}, CHType{Name: "Int64"}},
	}
	for _, testCase := range cases {
		got, err := greatestLeastCommonCHTypes(testCase.name, testCase.types)
		if err != nil {
			t.Errorf("%s(%v): error = %v, want %s", testCase.name, testCase.types, err, testCase.want.String())
			continue
		}
		if got.String() != testCase.want.String() {
			t.Errorf("%s(%v): CH type = %s, want %s", testCase.name, testCase.types, got.String(), testCase.want.String())
		}
	}

	// Arity three refuses again, exactly like the branch family, for
	// every third operand tried, including a duplicate of an operand
	// that the pair alone accepts. Measured: greatest(i64, u64, u64),
	// greatest(i64, u64, i64) and greatest(i32, u64, i32) are all
	// Code: 386 (NO_COMMON_TYPE).
	refusals := [][]CHType{
		{i64, u64, u64},
		{i64, u64, i64},
		{i32, u64, i32},
		{i32, u64, i8},
	}
	for _, types := range refusals {
		if _, err := greatestLeastCommonCHTypes("greatest", types); err == nil {
			t.Errorf("greatest(%v): want a refusal, arity three does not survive", types)
		}
	}

	// A pair that does not match the (signed, UInt64) shape at all
	// falls through to the ordinary commonCHTypes answer unchanged.
	got, err := greatestLeastCommonCHTypes("greatest", []CHType{i32, u8})
	if err != nil {
		t.Fatalf("greatest(Int32, UInt8): unexpected error = %v", err)
	}
	if got.String() != "Int32" {
		t.Errorf("greatest(Int32, UInt8): CH type = %s, want Int32", got.String())
	}
}
