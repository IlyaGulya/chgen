package engine

import "testing"

func TestIntegerAndFloatAliasesCanonicalizeAtTheSchemaBoundary(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		clickHouseType string
		goType         string
	}{
		"INT":        {"Int32", "int32"},
		"INTEGER":    {"Int32", "int32"},
		"MEDIUMINT":  {"Int32", "int32"},
		"BIGINT":     {"Int64", "int64"},
		"SIGNED":     {"Int64", "int64"},
		"UNSIGNED":   {"UInt64", "uint64"},
		"SMALLINT":   {"Int16", "int16"},
		"TINYINT":    {"Int8", "int8"},
		"INT1":       {"Int8", "int8"},
		"BYTE":       {"Int8", "int8"},
		"BIT":        {"UInt64", "uint64"},
		"FLOAT":      {"Float32", "float32"},
		"REAL":       {"Float32", "float32"},
		"SINGLE":     {"Float32", "float32"},
		"DOUBLE":     {"Float64", "float64"},
		"YEAR":       {"UInt16", "uint16"},
		"DEC":        {"Decimal(10, 0)", "decimal.Decimal"},
		"FIXED":      {"Decimal(10, 0)", "decimal.Decimal"},
		"NUMERIC":    {"Decimal(10, 0)", "decimal.Decimal"},
		"Decimal":    {"Decimal(10, 0)", "decimal.Decimal"},
		"FLOAT(24)":  {"Float32", "float32"},
		"FLOAT(25)":  {"Float32", "float32"},
		"FLOAT(53)":  {"Float32", "float32"},
		"FLOAT('x')": {"Float32", "float32"},
		"REAL(1, 2)": {"Float32", "float32"},
		"DOUBLE(24)": {"Float64", "float64"},
		"DEC(12)":    {"Decimal(12, 0)", "decimal.Decimal"},
		"INT(11)":    {"Int32", "int32"},
	}
	for alias, want := range cases {
		columnType, err := parseCHTypeName(alias)
		if err != nil {
			t.Fatalf("parse %s: %v", alias, err)
		}
		if got := columnType.String(); got != want.clickHouseType {
			t.Errorf("parse %s = %s, want %s", alias, got, want.clickHouseType)
		}
		gotGoType, err := goType(columnType)
		if err != nil {
			t.Fatalf("goType(%s): %v", alias, err)
		}
		if gotGoType != want.goType {
			t.Errorf("goType(%s) = %s, want %s", alias, gotGoType, want.goType)
		}
	}
}

func TestParameterizedAliasBoundariesStayExact(t *testing.T) {
	decimalType, err := parseCHTypeName("DECIMAL(12, 2)")
	if err != nil {
		t.Fatal(err)
	}
	if got := decimalType.String(); got != "Decimal(12, 2)" {
		t.Fatalf("DECIMAL(12, 2) = %s", got)
	}
	for _, alias := range []string{"FLOAT(1, 2, 3)", "BOOL(1)", "BOOLEAN(1)", "INT(1, 2)", "DOUBLE(1, 2, 3)", "DEC(0)", "DECIMAL(0)", "DEC(77)", "DEC(4, 5)"} {
		if _, err := parseCHTypeName(alias); err == nil {
			t.Errorf("parse %s succeeded", alias)
		}
	}
}

func TestNativeTypeFamilySpellingStaysCaseSensitive(t *testing.T) {
	for _, declared := range []string{"FIXEDSTRING(4)", "ENUM8('a' = 1)", "INT32", "FLOAT32"} {
		if _, err := parseCHTypeName(declared); err == nil {
			t.Errorf("parse %s succeeded", declared)
		}
	}
	for _, declared := range []string{"DECIMAL(9, 2)", "DATETIME64(3)"} {
		if _, err := parseCHTypeName(declared); err != nil {
			t.Errorf("parse %s: %v", declared, err)
		}
	}
}

func TestIntAliasStaysDistinctFromInt64(t *testing.T) {
	intType, err := parseCHTypeName("INT")
	if err != nil {
		t.Fatal(err)
	}
	int64Type, err := parseCHTypeName("Int64")
	if err != nil {
		t.Fatal(err)
	}
	signedType, err := parseCHTypeName("SIGNED")
	if err != nil {
		t.Fatal(err)
	}
	if intType.String() != "Int32" || int64Type.String() != "Int64" || signedType.String() != "Int64" {
		t.Fatalf("INT=%s, Int64=%s, SIGNED=%s", intType, int64Type, signedType)
	}
}

func TestParameterizedDateTimeCanonicalization(t *testing.T) {
	cases := map[string]string{
		"DateTime":              "DateTime",
		"DateTime('UTC')":       "DateTime('UTC')",
		"DateTime(0)":           "DateTime",
		"DateTime(0, 'UTC')":    "DateTime('UTC')",
		"DateTime(3)":           "DateTime64(3)",
		"DateTime(3, 'UTC')":    "DateTime64(3, 'UTC')",
		"DateTime64":            "DateTime64(3)",
		"DateTime64()":          "DateTime64(3)",
		"DateTime64(0)":         "DateTime64(0)",
		"DateTime64(3, 'UTC')":  "DateTime64(3, 'UTC')",
		"DateTime('UTC', 1, 2)": "DateTime('UTC')",
		"DateTime64(3, 7)":      "DateTime64(3)",
	}
	for declared, want := range cases {
		got, err := parseCHTypeName(declared)
		if err != nil {
			t.Fatalf("parse %s: %v", declared, err)
		}
		if got.String() != want {
			t.Errorf("parse %s = %s, want %s", declared, got, want)
		}
	}
	for _, declared := range []string{"DateTime(10)", "DateTime64(-1)", "DateTime64('UTC')", "DateTime('Nope')", "DateTime64(3, 'Nope')", "Array(DateTime('Nope'))"} {
		if _, err := parseCHTypeName(declared); err == nil {
			t.Errorf("parse %s succeeded", declared)
		}
	}
}

func TestWidthDependentEnumAliasIsRefused(t *testing.T) {
	for _, declared := range []string{"Enum('a' = 1)", "Enum('a' = 128)", "Array(Enum('a', 'b'))"} {
		if _, err := parseCHTypeName(declared); err == nil {
			t.Errorf("parse %s succeeded", declared)
		}
	}
	if _, err := schemaFromDDLErr(t, `CREATE TABLE events (e Enum('a' = 128)) ENGINE = Memory`); err == nil {
		t.Fatal("schema with width-dependent Enum succeeded before sumWithOverflow inference")
	}
}

func TestSupportedConstructorArgumentsStayExact(t *testing.T) {
	for _, declared := range []string{
		"Date(1)", "Date32(1)", "Bool(1)", "UUID(1)", "IPv4(1)", "IPv6(1)",
		"FixedString()", "FixedString(0)", "FixedString(16777216)", "FixedString(1, 2)",
		"Enum8()", "Enum8('a' = 128)", "Enum8('a' = 1, 'a' = 2)",
		"Enum8('a' = 1, 'b' = 1)", "Enum16('a' = 32768)",
	} {
		if _, err := parseCHTypeName(declared); err == nil {
			t.Errorf("parse %s succeeded", declared)
		}
	}
	for _, declared := range []string{"FixedString(1)", "FixedString(16777215)", "Enum8('a' = -128)", "Enum16('a' = 32767)"} {
		if _, err := parseCHTypeName(declared); err != nil {
			t.Errorf("parse %s: %v", declared, err)
		}
	}
}

func TestSimpleAggregateFunctionConstructorBoundary(t *testing.T) {
	for _, declared := range []string{
		"SimpleAggregateFunction(sum, Int64)",
		"SimpleAggregateFunction(SUM, Int64)",
		"SimpleAggregateFunction(MIN, String)",
		"SimpleAggregateFunction(MAX, Int64)",
		"SimpleAggregateFunction(sum, Nullable(Int64))",
		"SimpleAggregateFunction(min, String)",
		"SimpleAggregateFunction(anyLast, Array(Int32))",
		"SimpleAggregateFunction(groupArrayArray, Array(Int32))",
	} {
		if _, err := parseCHTypeName(declared); err != nil {
			t.Errorf("parse %s: %v", declared, err)
		}
	}
	for _, declared := range []string{
		"SimpleAggregateFunction(sum, String)",
		"SimpleAggregateFunction(sum, Int8)",
		"SimpleAggregateFunction(sum, Int16)",
		"SimpleAggregateFunction(sum, Int32)",
		"SimpleAggregateFunction(sum, UInt8)",
		"SimpleAggregateFunction(sum, Float32)",
		"SimpleAggregateFunction(sum, Decimal(10, 2))",
		"SimpleAggregateFunction(bogus, Int64)",
		"SimpleAggregateFunction(groupArrayArray, Int32)",
		"SimpleAggregateFunction(groupArrayArray, Array(Nullable(Int32)))",
		"SimpleAggregateFunction(ANY, Int64)",
		"SimpleAggregateFunction(Any, Int64)",
		"SimpleAggregateFunction(ANYLAST, Int64)",
		"SimpleAggregateFunction(AnyLast, Int64)",
		"SimpleAggregateFunction(GROUPARRAYARRAY, Array(Int32))",
		"SimpleAggregateFunction(GroupArrayArray, Array(Int32))",
	} {
		if _, err := parseCHTypeName(declared); err == nil {
			t.Errorf("parse %s succeeded", declared)
		}
	}
}
