package engine

import (
	"fmt"
	"strings"
)

func goType(columnType CHType) (string, error) {
	name := columnType.normalizedName()
	switch name {
	case "lowcardinality":
		if len(columnType.Params) != 1 {
			return "", fmt.Errorf("LowCardinality expects one type argument")
		}
		return goType(columnType.Params[0])
	case "nullable":
		if len(columnType.Params) != 1 {
			return "", fmt.Errorf("Nullable expects one type argument")
		}
		inner, err := goType(columnType.Params[0])
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(inner, "*") {
			return inner, nil
		}
		return "*" + inner, nil
	case "aggregatefunction":
		// An AggregateFunction value has no Go representation.
		// Measured with clickhouse-go v2.47.0 against ClickHouse
		// 25.8.29.51: a SELECT of such a column fails in the block
		// decoder with `clickhouse: unsupported column type
		// "AggregateFunction(sum, Int32)"`, before any Go value is
		// built. A []byte target fails the same way, thus no Go type
		// is correct and a refusal is the only honest answer.
		//
		// The write path stays available: an
		// INSERT ... SELECT sumState(x) moves the state inside the
		// server and no Go value crosses the wire. To read a state,
		// merge it (sumMerge) or finalize it (finalizeAggregation),
		// which both give an ordinary type with a Go mapping.
		return "", fmt.Errorf("ClickHouse type %s has no Go mapping: clickhouse-go cannot decode an AggregateFunction column; read it with a -Merge combinator or finalizeAggregation", columnType.String())
	case "simpleaggregatefunction":
		// A SimpleAggregateFunction(f, T) value decodes exactly as T.
		// Measured with clickhouse-go v2.47.0: the driver reports the
		// scan type of T and scans into a T target without an error.
		if len(columnType.Params) != 2 {
			return "", fmt.Errorf("SimpleAggregateFunction expects a function name and a type argument")
		}
		return goType(columnType.Params[1])
	case "int128", "uint128", "int256", "uint256":
		return "", fmt.Errorf("ClickHouse type %s has a measured refusal: clickhouse-go v2.47.0 can change nil to zero, wrap an out-of-range value, or misformat a text-path Array without an error", columnType.String())
	case "tuple":
		return "", fmt.Errorf("ClickHouse type %s has no Go mapping; this measured refusal keeps named and unnamed Tuple shapes separate, and a Tuple with a wide integer can return nil without an error", columnType.String())
	case "variant":
		return "", fmt.Errorf("ClickHouse type %s has a measured refusal: the driver value keeps a runtime alternative that the current generated type model cannot declare", columnType.String())
	case "dynamic":
		return "", fmt.Errorf("ClickHouse type %s has a measured refusal: the driver value keeps a runtime type that the current generated type model cannot declare", columnType.String())
	case "json":
		return "", fmt.Errorf("ClickHouse type %s has a measured refusal: the driver JSON representation depends on connection settings that generated code does not control", columnType.String())
	case "point", "ring", "linestring", "polygon", "multipolygon", "multilinestring":
		return "", fmt.Errorf("ClickHouse type %s has a measured refusal: direct columns use orb values, but ClickHouse removes the Geo alias from expression results", columnType.String())
	case "string", "fixedstring":
		return "string", nil
	case "enum":
		return "", fmt.Errorf("ClickHouse type %s has a measured refusal: the Enum alias can resolve to Enum8 or Enum16 from its value set", columnType.String())
	case "enum8", "enum16":
		// An Enum column travels as its NAME, not as its number.
		// Measured on ClickHouse 25.8.29.51 with driver v2.47.0, a Go
		// string scans and binds in the bare, Nullable, Array,
		// Array(Nullable), Map value and Map key shapes, and it also
		// carries a set that is numbered from a negative value. The
		// driver refuses the numeric target ("converting Enum8 to
		// *int8 is unsupported"), thus the name is the only form.
		//
		// A name outside the value set cannot become a silent wrong
		// value: the driver rejects it when the row is appended
		// (`unknown element "nope"`). The value set therefore does not
		// need to reach the Go type, and it stays in the ClickHouse
		// type where a consumer can read it.
		return "string", nil
	case "uuid":
		// clickhouse-go v2.47.0 gives text for a bare UUID column scanned
		// into a string, but it refuses the same type inside every
		// container: Array(UUID) fails with "converting uuid.UUID to
		// string is unsupported", and Map(String, UUID) names the fix
		// itself ("try using map[string]uuid.UUID"). uuid.UUID is the
		// driver's own scan and bind target. Measured against ClickHouse
		// 25.8.29.51 with driver v2.47.0, it round-trips the bare,
		// Nullable, Array, Array(Nullable), nested Array, Map value and
		// Map key shapes. The driver already depends on
		// github.com/google/uuid, thus this adds no new dependency tree.
		return "uuid.UUID", nil
	case "ipv4", "ipv6":
		// clickhouse-go v2.47.0 gives text for a bare IPv4/IPv6 column
		// scanned into a string, but raw binary for the same type inside
		// an Array or a Map, with no error. A string target also drops
		// the address family of an IPv4-mapped IPv6 value: ::ffff:1.2.3.4
		// arrives as "1.2.3.4". net.IP is the driver's own scan and bind
		// target. Measured against ClickHouse 25.8 with driver v2.47.0,
		// it round-trips the bare, Nullable, Array, Array(Nullable),
		// nested Array and Map value shapes, and it keeps the full 16
		// bytes of an IPv4-mapped address.
		return "net.IP", nil
	case "bool":
		return "bool", nil
	case "uint8":
		return "uint8", nil
	case "uint16":
		return "uint16", nil
	case "uint32":
		return "uint32", nil
	case "uint64":
		return "uint64", nil
	case "int8":
		return "int8", nil
	case "int16":
		return "int16", nil
	case "int32":
		return "int32", nil
	case "int64":
		return "int64", nil
	case "float32":
		return "float32", nil
	case "float64":
		return "float64", nil
	case "decimal", "decimal32", "decimal64", "decimal128", "decimal256":
		// clickhouse-go v2.47.0 refuses to scan a Decimal column into
		// *float64 ("converting Decimal to *float64 is unsupported"),
		// and a float target silently loses the decimal precision.
		// decimal.Decimal is the driver's own supported scan and bind
		// target; the measured round trips against ClickHouse
		// 25.8.29.51 are exact in every wrapper shape.
		return "decimal.Decimal", nil
	case "date", "date32", "datetime", "datetime64":
		return "time.Time", nil
	case "array":
		if len(columnType.Params) != 1 {
			return "", fmt.Errorf("Array expects one type argument")
		}
		// Keep a Nullable element as a pointer: clickhouse-go scans
		// Array(Nullable(T)) only into []*T.
		inner, err := goType(columnType.Params[0])
		if err != nil {
			return "", err
		}
		return "[]" + inner, nil
	case "map":
		if len(columnType.Params) != 2 {
			return "", fmt.Errorf("Map expects two type arguments")
		}
		if containsWideInteger(columnType.Params[0]) || containsWideInteger(columnType.Params[1]) {
			return "", fmt.Errorf("ClickHouse type %s has no Go mapping: clickhouse-go v2.47.0 can panic while it decodes a Map that contains a wide integer", columnType.String())
		}
		key, err := goType(columnType.Params[0])
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(key, "*") {
			return "", fmt.Errorf("Map key cannot be Nullable: %s", columnType.String())
		}
		// net.IP is a byte slice, thus it cannot be a Go map key. The
		// driver also refuses the column: a Map with an IPv4 or IPv6 key
		// fails the block decode with "unsupported column type". Neither
		// side can carry this shape, so stop with an explicit error
		// instead of generating code that cannot compile.
		if key == "net.IP" {
			return "", fmt.Errorf(
				"Map key %s has no Go mapping: clickhouse-go cannot decode a Map with an IP key: %s",
				columnType.Params[0].String(), columnType.String(),
			)
		}
		// Keep a Nullable value as a pointer: clickhouse-go scans
		// Map(K, Nullable(V)) only into map[K]*V.
		value, err := goType(columnType.Params[1])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("map[%s]%s", key, value), nil
	default:
		return "", fmt.Errorf("ClickHouse type %s has no Go mapping", columnType.String())
	}
}

func containsWideInteger(columnType CHType) bool {
	switch columnType.normalizedName() {
	case "int128", "uint128", "int256", "uint256":
		return true
	}
	for _, param := range columnType.Params {
		if containsWideInteger(param) {
			return true
		}
	}
	return false
}

func exportedIdentifier(name string) string {
	parts := strings.FieldsFunc(name, func(character rune) bool {
		return character == '_' || character == '-' || character == ' ' || character == '.'
	})
	var result strings.Builder
	for _, part := range parts {
		if strings.EqualFold(part, "id") {
			result.WriteString("ID")
			continue
		}
		if len(part) == 0 {
			continue
		}
		first := part[0]
		if first >= 'a' && first <= 'z' {
			first -= 'a' - 'A'
		}
		result.WriteByte(first)
		result.WriteString(part[1:])
	}
	if result.Len() == 0 {
		return "Result"
	}
	return result.String()
}

// tableCandidates lists the names that a FROM reference could have meant:
// physical tables, CTE names, and aliases in scope.
func tableCandidates(schema *Schema, scope *queryScope) []string {
	var candidates []string
	for name := range schema.Tables {
		candidates = append(candidates, name)
	}
	for current := scope; current != nil; current = current.parent {
		for _, scoped := range current.tables {
			if scoped.alias != "" {
				candidates = append(candidates, scoped.alias)
			}
			if scoped.table.Name != "" {
				candidates = append(candidates, scoped.table.Name)
			}
		}
	}
	return candidates
}

// columnCandidates lists the column names visible from this scope, honoring
// an explicit qualifier the same way lookupColumn does.
func (scope queryScope) columnCandidates(qualifier string) []string {
	var candidates []string
	for current := &scope; current != nil; current = current.parent {
		for _, scoped := range current.tables {
			if qualifier != "" && !strings.EqualFold(qualifier, scoped.alias) && !strings.EqualFold(qualifier, scoped.table.Name) {
				continue
			}
			for name := range scoped.table.Columns {
				candidates = append(candidates, name)
			}
		}
	}
	return candidates
}
