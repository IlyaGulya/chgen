package typeboundary

// Catalog is the fixed set of probe cells. Every entry is a hand-picked,
// named expression over the columns of SchemaDDL. The set favours breadth
// over depth on purpose: a probe is a tripwire, not a replacement for the
// type oracle's own sweep, so it is cheap to keep small and cheap to grow one
// cell at a time as a real cross-version difference is found.
//
// Ordering is not significant; Name is the only stable key. Do not remove or
// rename a cell casually: doing so drops history from every artifact taken
// before the change (see docs on retiring a union signature in
// internal/oraclereport/baseline.go for the same discipline applied to the
// oracle's own baseline).
//
// The named families below the ordinary rules are not decorative: they are
// the exact three families that the regression's measurement found to move
// between ClickHouse 24.8.14.39 and 25.8.29.51 (see
// docs/ci-and-the-oracle-baseline.md, "The version-matrix job"). A probe that
// did not carry them would not be provable against the one known real
// version difference this project has on record.
var Catalog = []Cell{
	// ---- ordinary numeric and string promotion ----
	{"int_widen_add", "i32 + i64"},
	{"int_unsigned_add", "u32 + u64"},
	{"int_mixed_sign_add", "i32 + u32"},
	{"float_div", "f32 / f64"},
	{"int_str_add_refused", "i32 + str"},
	{"decimal_add", "dec + dec"},
	{"decimal_int_add", "dec + i32"},
	{"decimal_float_add_refused", "dec + f64"},
	{"string_concat", "str || fs"},
	{"fixedstring_bare", "fs"},

	// ---- nullable / low-cardinality propagation ----
	{"nullable_prop_add", "nul_i32 + i32"},
	{"nullable_prop_eq", "nul_i32 = i32"},
	{"lowcard_concat", "lc_str || str"},
	{"lowcard_bare", "lc_str"},

	// ---- array shapes ----
	{"array_bare", "arr_i32"},
	{"array_concat_same", "arrayConcat(arr_i32, arr_i32)"},
	{"array_concat_mismatch", "arrayConcat(arr_i32, arr_str)"},
	{"array_eq_mismatch", "arr_i32 = arr_str"},

	// ---- temporal ----
	{"date_bare", "dte"},
	{"datetime64_bare", "dt64"},
	{"datetime_sub", "dt64 - dt"},
	{"tostartofinterval_datetime", "toStartOfInterval(dt, INTERVAL 1 HOUR)"},

	// ---- domain types (UUID, IPv4, Enum) ----
	{"uuid_bare", "uid"},
	{"uuid_eq_string_refused", "uid = str"},
	{"ip4_bare", "ip4"},
	{"ip4_add_int", "ip4 + u32"},
	{"enum_bare", "en"},
	{"enum_add_int_refused", "en + i32"},

	// ---- the three families known to move at the 24.8/25.8 boundary ----
	{"v24_8_trim_fixedstring", "trim(fs)"},
	{"v24_8_trimleft_fixedstring", "trimLeft(fs)"},
	{"v24_8_trimright_fixedstring", "trimRight(fs)"},
	// These three cells predate the executed boundary contract. Keep their
	// names and expressions so old probe artifacts do not lose identity.
	{"v24_8_greatest_decimal", "greatest(dec, dec)"},
	{"v24_8_least_decimal", "least(dec, dec)"},
	{"v24_8_cityhash64_array", "cityHash64(arr_i32)"},
	{"v24_8_greatest_safn_i32", "greatest(safn_i32)"},
	{"v24_8_greatest_safn_u64", "greatest(safn_u64)"},
	{"v24_8_least_safn_i32", "least(safn_i32)"},
	{"v24_8_least_safn_u64", "least(safn_u64)"},
	{"v24_8_cityhash64_array_nullable", "cityHash64(arr_n_i32)"},
	{"v24_8_cityhash64_saf_array_nullable", "cityHash64(saf_narr)"},
	{"v24_8_cityhash64_saf_array_array_nullable", "cityHash64(safarr_narr)"},

	// ---- comparisons across incompatible domains (mismatch/refusal edge) ----
	{"decimal_ge_enum", "dec >= en"},
	{"decimal_ge_decimal", "dec >= dec"},
}
