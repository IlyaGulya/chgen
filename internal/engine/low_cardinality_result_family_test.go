package engine

import "testing"

// A LowCardinality argument does not always give a LowCardinality result.
// The server decides on the RESULT type family, not on the function: a
// family that cannot go inside the wrapper drops it, and every other
// family keeps it.
//
// Before the rule was shared, only the sized-constructor path asked the
// question. Every other path put the wrapper back without asking, thus
// toDateTime64(lcdt, 3) gave LowCardinality(DateTime64(3)) where the
// server gives DateTime64(3). That is a silently wrong type, which is
// the worst defect class, and the Nullable form was worse: the inferred
// LowCardinality(Nullable(DateTime64(3))) is a type that the server
// refuses to build at all with Code: 43.
//
// The rule now lives in one place, wrapLowCardinality, thus it holds for
// every path that gives a result the wrapper. The test pins BOTH sides,
// because a fix that dropped the wrapper everywhere would also pass a
// test that only pinned the dropping side.
//
// Every expected type was measured on ClickHouse 25.8.29.51 over real
// LowCardinality columns, with allow_suspicious_low_cardinality_types=1.
// The setting matters: with it off a numeric or Date column gives
// Code: 455, which is a policy guard against a wasteful column and not a
// statement that the type cannot exist.
func TestLowCardinalityFollowsTheResultFamily(t *testing.T) {
	// The wrapper DROPS, because the result family refuses it.
	//
	//	SELECT toTypeName(toDateTime64(lcdt, 3)) FROM t
	//	  -> DateTime64(3)
	//	SELECT toTypeName(toDateTime64(lcnd, 3)) FROM t
	//	  -> Nullable(DateTime64(3))
	dropped := []struct{ expr, want string }{
		{"toDateTime64(lcdt, 3)", "DateTime64(3)"},
		{"toDateTime64(lcdtz, 3)", "DateTime64(3, 'UTC')"},
		{"toDateTime64(lcd, 3)", "DateTime64(3)"},
		{"toDateTime64(lc, 3)", "DateTime64(3)"},
		// One Nullable layer is looked through, because the server
		// decides on the inner type. LowCardinality(Nullable(Date))
		// exists, while LowCardinality(Nullable(DateTime64(3))) is
		// Code: 43.
		{"toDateTime64(lcnd, 3)", "Nullable(DateTime64(3))"},
		{"toDateTime64(lcndt, 3)", "Nullable(DateTime64(3))"},
	}
	runTemporalTypeCases(t, dropped)

	// The wrapper STAYS, because the result family accepts it. Without
	// these cases a fix that dropped the wrapper for every function
	// would pass.
	//
	//	SELECT toTypeName(toDateTime(lcdt)) FROM t
	//	  -> LowCardinality(DateTime)
	//	SELECT toTypeName(toDate(lcdt)) FROM t
	//	  -> LowCardinality(Date)
	kept := []struct{ expr, want string }{
		{"toDateTime(lcdt)", "LowCardinality(DateTime)"},
		{"toDate(lcdt)", "LowCardinality(Date)"},
		{"toStartOfDay(lcdt)", "LowCardinality(DateTime)"},
		{"toTimeZone(lcdt, 'UTC')", "LowCardinality(DateTime('UTC'))"},
		{"toStartOfInterval(lcdt, INTERVAL 1 DAY)", "LowCardinality(DateTime)"},
		{"toDate(lcnd)", "LowCardinality(Nullable(Date))"},
	}
	runTemporalTypeCases(t, kept)
}
