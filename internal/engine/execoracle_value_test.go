//go:build execoracle

package engine

// Value model for the execution oracle. This file defines the oracle type
// tree, the boundary-value SQL literals, the parser that reads a ClickHouse
// TabSeparated cell into a canonical value, the converter that reads the
// runner JSON dump into the same canonical form, and the comparator.
//
// The reference channel is the ClickHouse HTTP interface in TabSeparated
// text form. The channel under test is the clickhouse-go native protocol
// inside the generated code. The two channels share no client code, so an
// agreement between them is evidence and not self-confirmation.

import (
	"encoding/base64"
	"fmt"
	"math"
	"math/big"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// xt is one oracle ClickHouse type. kind is the lowercase constructor name.
type xt struct {
	kind  string // bool,int8..int64,uint8..uint64,float32,float64,decimal,string,fixedstring,uuid,ipv4,ipv6,date,date32,datetime,datetime64,nullable,lowcardinality,array,map
	prec  int    // decimal precision, datetime64 scale, fixedstring length
	scale int    // decimal scale
	key   *xt    // map key
	elem  *xt    // nullable/lowcardinality/array inner, map value
}

// The Enum value sets that the oracle uses. They are named constants so the
// type text of a case and the DDL that creates its table cannot drift apart.
// The Enum8 set is numbered from a negative value, which ClickHouse allows
// and which proves that the name, not the number, is the wire form.
const (
	enum8SQLType  = "Enum8('a' = -3, 'zz' = 0, 'neg' = 7)"
	enum16SQLType = "Enum16('x' = 1, 'y' = 2)"
)

func scalar(kind string) *xt  { return &xt{kind: kind} }
func decT(p, s int) *xt       { return &xt{kind: "decimal", prec: p, scale: s} }
func fsT(n int) *xt           { return &xt{kind: "fixedstring", prec: n} }
func dt64T(scale int) *xt     { return &xt{kind: "datetime64", prec: scale} }
func nullableT(inner *xt) *xt { return &xt{kind: "nullable", elem: inner} }
func lcT(inner *xt) *xt       { return &xt{kind: "lowcardinality", elem: inner} }
func arrayT(inner *xt) *xt    { return &xt{kind: "array", elem: inner} }
func mapT(key, value *xt) *xt { return &xt{kind: "map", key: key, elem: value} }

// sqlType renders the ClickHouse type text.
func (t *xt) sqlType() string {
	switch t.kind {
	case "decimal":
		return fmt.Sprintf("Decimal(%d, %d)", t.prec, t.scale)
	case "fixedstring":
		return fmt.Sprintf("FixedString(%d)", t.prec)
	case "datetime64":
		return fmt.Sprintf("DateTime64(%d)", t.prec)
	case "nullable":
		return "Nullable(" + t.elem.sqlType() + ")"
	case "lowcardinality":
		return "LowCardinality(" + t.elem.sqlType() + ")"
	case "array":
		return "Array(" + t.elem.sqlType() + ")"
	case "map":
		return fmt.Sprintf("Map(%s, %s)", t.key.sqlType(), t.elem.sqlType())
	case "bool":
		return "Bool"
	case "string":
		return "String"
	case "uuid":
		return "UUID"
	case "tuple":
		return "Tuple(Int32, String)"
	case "simpleaggregate_sum_int64":
		return "SimpleAggregateFunction(sum, Int64)"
	case "enum8":
		return enum8SQLType
	case "enum16":
		return enum16SQLType
	case "ipv4":
		return "IPv4"
	case "ipv6":
		return "IPv6"
	case "date":
		return "Date"
	case "date32":
		return "Date32"
	case "datetime":
		return "DateTime"
	default: // int*/uint*/float*
		if rest, ok := strings.CutPrefix(t.kind, "uint"); ok {
			return "UInt" + rest
		}
		return strings.ToUpper(t.kind[:1]) + t.kind[1:]
	}
}

// scalarSamples gives the boundary SQL literals of one scalar kind.
func scalarSamples(t *xt) []string {
	switch t.kind {
	case "bool":
		return []string{"true", "false"}
	case "int8":
		return []string{"-128", "-1", "0", "127"}
	case "int16":
		return []string{"-32768", "0", "32767"}
	case "int32":
		return []string{"-2147483648", "0", "2147483647"}
	case "int64":
		return []string{"-9223372036854775808", "-1", "0", "9223372036854775807"}
	case "simpleaggregate_sum_int64":
		return []string{"-9223372036854775808", "-1", "0", "9223372036854775807"}
	case "int128":
		return []string{"-170141183460469231731687303715884105728", "-1", "0", "170141183460469231731687303715884105727"}
	case "int256":
		return []string{"-57896044618658097711785492504343953926634992332820282019728792003956564819968", "-1", "0", "57896044618658097711785492504343953926634992332820282019728792003956564819967"}
	case "uint8":
		return []string{"0", "1", "255"}
	case "uint16":
		return []string{"0", "65535"}
	case "uint32":
		return []string{"0", "4294967295"}
	case "uint64":
		return []string{"0", "1", "18446744073709551615"}
	case "uint128":
		return []string{"0", "1", "340282366920938463463374607431768211455"}
	case "uint256":
		return []string{"0", "1", "115792089237316195423570985008687907853269984665640564039457584007913129639935"}
	case "float32":
		return []string{"0", "-0.0", "1.5", "3.4028235e38", "1.1754944e-38", "1e-45", "nan", "inf", "-inf"}
	case "float64":
		return []string{"0", "-0.0", "0.1", "1.5", "1.7976931348623157e308", "5e-324", "nan", "inf", "-inf"}
	case "decimal":
		switch {
		case t.prec >= 38:
			// Every sample keeps the exact column type, so an array
			// literal of these samples has one supertype.
			return []string{
				"toDecimal128('0', 10)",
				"toDecimal128('0.0000000001', 10)",
				"toDecimal128('1234567890123456789012345678.0123456789', 10)",
			}
		case t.prec >= 18:
			return []string{"0", "0.0001", "-1.2345", "99999999999999.9999"}
		default:
			return []string{"0", "0.1", "-0.1", "12345.67", "99999999.99", "-99999999.99"}
		}
	case "string":
		return []string{"''", "'abc'", "'a\\tb\\nc'", "'it''s \\\\ ok'", "'héllo'"}
	case "fixedstring":
		return []string{"''", "'abc'", "'abcdefgh'"}
	case "uuid":
		return []string{"'00000000-0000-0000-0000-000000000000'", "'ffffffff-ffff-ffff-ffff-ffffffffffff'", "'61f0c404-5cb3-11e7-907b-a6006ad3dba0'"}
	case "enum8":
		// The set is numbered from a negative value on purpose: the
		// number is not the wire form, and a name must survive it.
		return []string{"'a'", "'zz'", "'neg'"}
	case "enum16":
		return []string{"'x'", "'y'"}
	case "ipv4":
		return []string{"'0.0.0.0'", "'255.255.255.255'", "'192.168.1.1'"}
	case "ipv6":
		return []string{"'::'", "'::1'", "'2001:db8::ff00:42:8329'", "'::ffff:1.2.3.4'"}
	case "date":
		return []string{"'1970-01-01'", "'2024-02-29'", "'2149-06-06'"}
	case "date32":
		return []string{"'1900-01-01'", "'2024-02-29'", "'2299-12-31'"}
	case "datetime":
		return []string{"'1970-01-01 00:00:00'", "'2024-02-29 23:59:59'", "'2106-02-07 06:28:15'"}
	case "datetime64":
		if t.prec >= 9 {
			return []string{"'1970-01-01 00:00:00.000000001'", "'2100-12-31 23:59:59.123456789'"}
		}
		return []string{"'1900-01-01 00:00:00.000'", "'2024-02-29 12:34:56.789'", "'2299-12-31 23:59:59.999'"}
	}
	return nil
}

// rowLiterals gives one SQL literal per table row for a type.
func rowLiterals(t *xt) []string {
	switch t.kind {
	case "nullable":
		return append([]string{"NULL"}, rowLiterals(t.elem)...)
	case "lowcardinality":
		return rowLiterals(t.elem)
	case "array":
		inner := rowLiterals(t.elem)
		full := "[" + strings.Join(capN(inner, 4), ", ") + "]"
		return []string{"[]", full}
	case "map":
		keys := distinctN(rowLiterals(t.key), 2)
		values := capN(rowLiterals(t.elem), 2)
		if len(keys) == 0 || len(values) == 0 {
			return []string{"map()"}
		}
		var pairs []string
		for index, key := range keys {
			pairs = append(pairs, key, values[index%len(values)])
		}
		return []string{"map()", "map(" + strings.Join(pairs, ", ") + ")"}
	default:
		return scalarSamples(t)
	}
}

func capN(items []string, n int) []string {
	filtered := make([]string, 0, len(items))
	for _, item := range items {
		if item == "NULL" && len(filtered) > 0 {
			continue // one NULL is enough inside a composite
		}
		filtered = append(filtered, item)
	}
	if len(filtered) > n {
		return filtered[:n]
	}
	return filtered
}

func distinctN(items []string, n int) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		if item == "NULL" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
		if len(out) == n {
			break
		}
	}
	return out
}

// canon is the canonical comparison value.
type canon struct {
	kind  string // null,int,rat,float,bool,bytes,time,list,map
	i     *big.Int
	r     *big.Rat
	bits  uint64
	width int
	b     bool
	by    []byte
	sec   int64
	nsec  int
	items []canon
	pairs [][2]canon
}

func (c canon) String() string {
	switch c.kind {
	case "null":
		return "NULL"
	case "int":
		return c.i.String()
	case "rat":
		return c.r.RatString()
	case "float":
		if c.width == 32 {
			return fmt.Sprintf("f32(%v/0x%08x)", math.Float32frombits(uint32(c.bits)), c.bits)
		}
		return fmt.Sprintf("f64(%v/0x%016x)", math.Float64frombits(c.bits), c.bits)
	case "bool":
		return strconv.FormatBool(c.b)
	case "bytes":
		return strconv.Quote(string(c.by))
	case "time":
		return time.Unix(c.sec, int64(c.nsec)).UTC().Format("2006-01-02 15:04:05.000000000")
	case "list":
		parts := make([]string, len(c.items))
		for index, item := range c.items {
			parts[index] = item.String()
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case "map":
		parts := make([]string, len(c.pairs))
		for index, pair := range c.pairs {
			parts[index] = pair[0].String() + ":" + pair[1].String()
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return "?"
}

func canonNull() canon                    { return canon{kind: "null"} }
func canonBool(b bool) canon              { return canon{kind: "bool", b: b} }
func canonBytes(b []byte) canon           { return canon{kind: "bytes", by: b} }
func canonInt(i *big.Int) canon           { return canon{kind: "int", i: i} }
func canonRat(r *big.Rat) canon           { return canon{kind: "rat", r: r} }
func canonTime(sec int64, nsec int) canon { return canon{kind: "time", sec: sec, nsec: nsec} }

func canonFloat64(f float64) canon {
	bits := math.Float64bits(f)
	if math.IsNaN(f) {
		bits = 0x7ff8000000000000
	}
	return canon{kind: "float", bits: bits, width: 64}
}

func canonFloat32(f float32) canon {
	bits := uint64(math.Float32bits(f))
	if f != f {
		bits = 0x7fc00000
	}
	return canon{kind: "float", bits: bits, width: 32}
}

// compareCanon returns "" when equal and a diff description otherwise.
func compareCanon(reference, got canon) string {
	if reference.kind != got.kind {
		return fmt.Sprintf("kind %s != %s (reference %s, got %s)", reference.kind, got.kind, reference, got)
	}
	switch reference.kind {
	case "null":
		return ""
	case "int":
		if reference.i.Cmp(got.i) != 0 {
			return fmt.Sprintf("int %s != %s", reference.i, got.i)
		}
	case "rat":
		if reference.r.Cmp(got.r) != 0 {
			return fmt.Sprintf("exact value %s != %s", reference.r.RatString(), got.r.RatString())
		}
	case "float":
		if reference.bits != got.bits || reference.width != got.width {
			return fmt.Sprintf("float %s != %s", reference, got)
		}
	case "bool":
		if reference.b != got.b {
			return fmt.Sprintf("bool %v != %v", reference.b, got.b)
		}
	case "bytes":
		if string(reference.by) != string(got.by) {
			return fmt.Sprintf("bytes %s != %s", reference, got)
		}
	case "time":
		if reference.sec != got.sec || reference.nsec != got.nsec {
			return fmt.Sprintf("time %s != %s", reference, got)
		}
	case "list":
		if len(reference.items) != len(got.items) {
			return fmt.Sprintf("list length %d != %d (reference %s, got %s)", len(reference.items), len(got.items), reference, got)
		}
		for index := range reference.items {
			if diff := compareCanon(reference.items[index], got.items[index]); diff != "" {
				return fmt.Sprintf("[%d]: %s", index, diff)
			}
		}
	case "map":
		if len(reference.pairs) != len(got.pairs) {
			return fmt.Sprintf("map size %d != %d (reference %s, got %s)", len(reference.pairs), len(got.pairs), reference, got)
		}
		for index := range reference.pairs {
			if diff := compareCanon(reference.pairs[index][0], got.pairs[index][0]); diff != "" {
				return fmt.Sprintf("key[%d]: %s", index, diff)
			}
			if diff := compareCanon(reference.pairs[index][1], got.pairs[index][1]); diff != "" {
				return fmt.Sprintf("value[%d]: %s", index, diff)
			}
		}
	}
	return ""
}

func sortPairs(pairs [][2]canon) {
	sort.Slice(pairs, func(a, b int) bool { return pairs[a][0].String() < pairs[b][0].String() })
}

// tsvUnescape decodes one top-level TabSeparated field.
func tsvUnescape(field string) []byte {
	out := make([]byte, 0, len(field))
	for index := 0; index < len(field); index++ {
		c := field[index]
		if c != '\\' || index+1 == len(field) {
			out = append(out, c)
			continue
		}
		index++
		switch field[index] {
		case 't':
			out = append(out, '\t')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case '0':
			out = append(out, 0)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'a':
			out = append(out, '\a')
		case 'v':
			out = append(out, '\v')
		case '\\':
			out = append(out, '\\')
		case '\'':
			out = append(out, '\'')
		default:
			out = append(out, '\\', field[index])
		}
	}
	return out
}

// parseCHText parses one ClickHouse text value of the given type into the
// canonical form. nested is false for a whole top-level TSV field and true
// for a value inside an array or map, where strings and dates are quoted.
func parseCHText(text string, t *xt, nested bool) (canon, error) {
	switch t.kind {
	case "nullable":
		if (!nested && text == "\\N") || (nested && text == "NULL") {
			return canonNull(), nil
		}
		return parseCHText(text, t.elem, nested)
	case "lowcardinality":
		return parseCHText(text, t.elem, nested)
	case "bool":
		switch text {
		case "true":
			return canonBool(true), nil
		case "false":
			return canonBool(false), nil
		}
		return canon{}, fmt.Errorf("bad bool %q", text)
	case "int8", "int16", "int32", "int64", "int128", "int256", "simpleaggregate_sum_int64", "uint8", "uint16", "uint32", "uint64", "uint128", "uint256":
		value, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return canon{}, fmt.Errorf("bad integer %q", text)
		}
		return canonInt(value), nil
	case "float32", "float64":
		f, err := parseCHFloat(text)
		if err != nil {
			return canon{}, err
		}
		if t.kind == "float32" {
			return canonFloat32(float32(f)), nil
		}
		return canonFloat64(f), nil
	case "decimal":
		value, ok := new(big.Rat).SetString(text)
		if !ok {
			return canon{}, fmt.Errorf("bad decimal %q", text)
		}
		return canonRat(value), nil
	case "string", "fixedstring", "enum8", "enum16":
		raw, err := textBytes(text, nested)
		if err != nil {
			return canon{}, err
		}
		return canonBytes(raw), nil
	case "uuid":
		raw, err := textBytes(text, nested)
		if err != nil {
			return canon{}, err
		}
		return canonBytes([]byte(strings.ToLower(string(raw)))), nil
	case "ipv4", "ipv6":
		raw, err := textBytes(text, nested)
		if err != nil {
			return canon{}, err
		}
		address, err := netip.ParseAddr(string(raw))
		if err != nil {
			return canon{}, fmt.Errorf("bad ip %q: %v", raw, err)
		}
		return canonBytes([]byte(address.String())), nil
	case "date", "date32", "datetime", "datetime64":
		raw, err := textBytes(text, nested)
		if err != nil {
			return canon{}, err
		}
		return parseCHTime(string(raw), t)
	case "array":
		items, err := splitComposite(text, '[', ']')
		if err != nil {
			return canon{}, err
		}
		result := canon{kind: "list"}
		for _, item := range items {
			parsed, err := parseCHText(item, t.elem, true)
			if err != nil {
				return canon{}, err
			}
			result.items = append(result.items, parsed)
		}
		return result, nil
	case "map":
		items, err := splitComposite(text, '{', '}')
		if err != nil {
			return canon{}, err
		}
		result := canon{kind: "map"}
		for _, item := range items {
			keyText, valueText, err := splitPair(item)
			if err != nil {
				return canon{}, err
			}
			key, err := parseCHText(keyText, t.key, true)
			if err != nil {
				return canon{}, err
			}
			value, err := parseCHText(valueText, t.elem, true)
			if err != nil {
				return canon{}, err
			}
			result.pairs = append(result.pairs, [2]canon{key, value})
		}
		sortPairs(result.pairs)
		return result, nil
	}
	return canon{}, fmt.Errorf("unhandled type %s", t.sqlType())
}

func parseCHFloat(text string) (float64, error) {
	switch strings.ToLower(text) {
	case "nan", "-nan":
		return math.NaN(), nil
	case "inf", "+inf":
		return math.Inf(1), nil
	case "-inf":
		return math.Inf(-1), nil
	}
	return strconv.ParseFloat(text, 64)
}

func parseCHTime(raw string, t *xt) (canon, error) {
	layout := "2006-01-02"
	if t.kind == "datetime" || t.kind == "datetime64" {
		layout = "2006-01-02 15:04:05"
	}
	base := raw
	nsec := 0
	if dot := strings.IndexByte(raw, '.'); dot >= 0 {
		base = raw[:dot]
		fraction := raw[dot+1:]
		for len(fraction) < 9 {
			fraction += "0"
		}
		parsed, err := strconv.Atoi(fraction[:9])
		if err != nil {
			return canon{}, fmt.Errorf("bad fraction in %q", raw)
		}
		nsec = parsed
	}
	when, err := time.ParseInLocation(layout, base, time.UTC)
	if err != nil {
		return canon{}, fmt.Errorf("bad time %q: %v", raw, err)
	}
	return canonTime(when.Unix(), nsec), nil
}

// textBytes gives the raw bytes of a textual value: a quoted literal with
// backslash escapes when nested, the TSV-unescaped field text otherwise.
func textBytes(text string, nested bool) ([]byte, error) {
	if !nested {
		return tsvUnescape(text), nil
	}
	if len(text) < 2 || text[0] != '\'' || text[len(text)-1] != '\'' {
		return nil, fmt.Errorf("expected quoted value, got %q", text)
	}
	return tsvUnescape(text[1 : len(text)-1]), nil
}

// splitComposite splits the top-level comma items of an array or map text.
func splitComposite(text string, open, close byte) ([]string, error) {
	if len(text) < 2 || text[0] != open || text[len(text)-1] != close {
		return nil, fmt.Errorf("expected %c...%c, got %q", open, close, text)
	}
	inner := text[1 : len(text)-1]
	if strings.TrimSpace(inner) == "" {
		return nil, nil
	}
	var items []string
	depth := 0
	inQuote := false
	start := 0
	for index := 0; index < len(inner); index++ {
		c := inner[index]
		if inQuote {
			if c == '\\' {
				index++
			} else if c == '\'' {
				inQuote = false
			}
			continue
		}
		switch c {
		case '\'':
			inQuote = true
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, strings.TrimSpace(inner[start:index]))
				start = index + 1
			}
		}
	}
	items = append(items, strings.TrimSpace(inner[start:]))
	return items, nil
}

// splitPair splits one "key:value" map item at the top-level colon.
func splitPair(item string) (string, string, error) {
	depth := 0
	inQuote := false
	for index := 0; index < len(item); index++ {
		c := item[index]
		if inQuote {
			if c == '\\' {
				index++
			} else if c == '\'' {
				inQuote = false
			}
			continue
		}
		switch c {
		case '\'':
			inQuote = true
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
		case ':':
			if depth == 0 {
				return strings.TrimSpace(item[:index]), strings.TrimSpace(item[index+1:]), nil
			}
		}
	}
	return "", "", fmt.Errorf("no top-level colon in map item %q", item)
}

// goDumpToCanon converts one runner JSON dump node into the canonical form,
// guided by the ClickHouse type the field came from.
func goDumpToCanon(node map[string]any, t *xt) (canon, error) {
	switch t.kind {
	case "nullable":
		if node["t"] == "null" {
			return canonNull(), nil
		}
		return goDumpToCanon(node, t.elem)
	case "lowcardinality":
		return goDumpToCanon(node, t.elem)
	}
	tag, _ := node["t"].(string)
	switch t.kind {
	case "bool":
		if tag != "b" {
			return canon{}, fmt.Errorf("expected bool dump, got %v", node)
		}
		return canonBool(node["v"].(bool)), nil
	case "int8", "int16", "int32", "int64", "int128", "int256", "simpleaggregate_sum_int64", "uint8", "uint16", "uint32", "uint64", "uint128", "uint256":
		if tag != "i" {
			return canon{}, fmt.Errorf("expected integer dump for %s, got %v", t.sqlType(), node)
		}
		value, ok := new(big.Int).SetString(node["v"].(string), 10)
		if !ok {
			return canon{}, fmt.Errorf("bad integer dump %v", node)
		}
		return canonInt(value), nil
	case "float32", "float64":
		bits, width, err := dumpFloatBits(node)
		if err != nil {
			return canon{}, err
		}
		if t.kind == "float32" {
			if width != 32 {
				return canon{}, fmt.Errorf("Float32 column scanned into a %d-bit Go float", width)
			}
			return canonFloat32(math.Float32frombits(uint32(bits))), nil
		}
		if width != 64 {
			return canon{}, fmt.Errorf("Float64 column scanned into a %d-bit Go float", width)
		}
		return canonFloat64(math.Float64frombits(bits)), nil
	case "decimal":
		// chgen maps Decimal to decimal.Decimal; the runner dumps the
		// exact decimal text under the "d" tag. A float dump is still
		// accepted and converted into its exact rational value, so a
		// regression back to a float mapping shows as a value
		// difference and not a formatting accident.
		if tag == "d" {
			value, ok := new(big.Rat).SetString(node["v"].(string))
			if !ok {
				return canon{}, fmt.Errorf("bad decimal dump %v", node)
			}
			return canonRat(value), nil
		}
		bits, width, err := dumpFloatBits(node)
		if err != nil {
			return canon{}, err
		}
		var f float64
		if width == 32 {
			f = float64(math.Float32frombits(uint32(bits)))
		} else {
			f = math.Float64frombits(bits)
		}
		exact := new(big.Rat).SetFloat64(f)
		if exact == nil {
			return canon{}, fmt.Errorf("decimal arrived as non-finite float %v", f)
		}
		return canonRat(exact), nil
	case "ipv4", "ipv6":
		// The Go side is net.IP, thus the dump carries the raw address
		// bytes. Canonicalize to the same text form that the HTTP
		// reference channel gives, and keep the address family: an
		// IPv4-mapped IPv6 value in an IPv6 column must stay
		// ::ffff:1.2.3.4 and must not collapse to 1.2.3.4.
		if tag != "ip" {
			return canon{}, fmt.Errorf("expected ip dump for %s, got %v", t.sqlType(), node)
		}
		raw, err := base64.StdEncoding.DecodeString(node["v"].(string))
		if err != nil {
			return canon{}, err
		}
		address, ok := netip.AddrFromSlice(raw)
		if !ok {
			return canon{}, fmt.Errorf("Go value is not an IP address: %x", raw)
		}
		if t.kind == "ipv4" {
			// An IPv4 column arrives as a 4-byte or an IPv4-mapped
			// 16-byte net.IP. Both mean the same IPv4 address.
			address = address.Unmap()
		}
		return canonBytes([]byte(address.String())), nil
	case "uuid":
		// The Go side is uuid.UUID, thus the dump carries the raw 16
		// bytes. Render them in the same text form that the HTTP
		// reference channel gives, which is lower-case with dashes.
		// Comparing the Go text form directly would be a trap: the
		// dash and case conventions can differ while the value is the
		// same, and they can agree while the bytes differ.
		if tag != "uuid" {
			return canon{}, fmt.Errorf("expected uuid dump for %s, got %v", t.sqlType(), node)
		}
		raw, err := base64.StdEncoding.DecodeString(node["v"].(string))
		if err != nil {
			return canon{}, err
		}
		identifier, err := uuid.FromBytes(raw)
		if err != nil {
			return canon{}, fmt.Errorf("Go value is not a UUID: %x", raw)
		}
		return canonBytes([]byte(identifier.String())), nil
	case "enum8", "enum16":
		// An Enum column travels as its name, thus the Go side is a
		// string and the HTTP channel prints the same name.
		if tag != "s" {
			return canon{}, fmt.Errorf("expected string dump for %s, got %v", t.sqlType(), node)
		}
		raw, err := base64.StdEncoding.DecodeString(node["v"].(string))
		if err != nil {
			return canon{}, err
		}
		return canonBytes(raw), nil
	case "string", "fixedstring":
		if tag != "s" {
			return canon{}, fmt.Errorf("expected string dump for %s, got %v", t.sqlType(), node)
		}
		raw, err := base64.StdEncoding.DecodeString(node["v"].(string))
		if err != nil {
			return canon{}, err
		}
		return canonBytes(raw), nil
	case "date", "date32", "datetime", "datetime64":
		if tag != "t" {
			return canon{}, fmt.Errorf("expected time dump for %s, got %v", t.sqlType(), node)
		}
		sec, err := strconv.ParseInt(node["sec"].(string), 10, 64)
		if err != nil {
			return canon{}, err
		}
		nsec := int(node["nsec"].(float64))
		return canonTime(sec, nsec), nil
	case "array":
		if tag != "a" {
			return canon{}, fmt.Errorf("expected array dump for %s, got %v", t.sqlType(), node)
		}
		result := canon{kind: "list"}
		for _, raw := range node["v"].([]any) {
			item, err := goDumpToCanon(raw.(map[string]any), t.elem)
			if err != nil {
				return canon{}, err
			}
			result.items = append(result.items, item)
		}
		return result, nil
	case "map":
		if tag != "m" {
			return canon{}, fmt.Errorf("expected map dump for %s, got %v", t.sqlType(), node)
		}
		keys := node["k"].([]any)
		values := node["v"].([]any)
		result := canon{kind: "map"}
		for index := range keys {
			key, err := goDumpToCanon(keys[index].(map[string]any), t.key)
			if err != nil {
				return canon{}, err
			}
			value, err := goDumpToCanon(values[index].(map[string]any), t.elem)
			if err != nil {
				return canon{}, err
			}
			result.pairs = append(result.pairs, [2]canon{key, value})
		}
		sortPairs(result.pairs)
		return result, nil
	}
	return canon{}, fmt.Errorf("unhandled type %s", t.sqlType())
}

func dumpFloatBits(node map[string]any) (uint64, int, error) {
	if node["t"] != "f" {
		return 0, 0, fmt.Errorf("expected float dump, got %v", node)
	}
	bits, err := strconv.ParseUint(node["bits"].(string), 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return bits, int(node["w"].(float64)), nil
}

// Offline pins for the reference-text parser. These run without Docker:
// go test -tags execoracle -run TestExecOracleTextParser ./internal/engine
func TestExecOracleTextParser(t *testing.T) {
	check := func(text string, typ *xt, want string) {
		t.Helper()
		parsed, err := parseCHText(text, typ, false)
		if err != nil {
			t.Fatalf("parse %q as %s: %v", text, typ.sqlType(), err)
		}
		if parsed.String() != want {
			t.Fatalf("parse %q as %s: got %s, want %s", text, typ.sqlType(), parsed, want)
		}
	}
	check(`abc\0\0\0\0\0`, fsT(8), `"abc\x00\x00\x00\x00\x00"`)
	check(`a\tb\nc`, scalar("string"), `"a\tb\nc"`)
	check(``, scalar("string"), `""`)
	check(`\N`, nullableT(scalar("string")), `NULL`)
	check(`['x\ty','it\'s','a\\b']`, arrayT(scalar("string")), `["x\ty", "it's", "a\\b"]`)
	check(`[NULL,-128,127]`, arrayT(nullableT(scalar("int8"))), `[NULL, -128, 127]`)
	check(`{'k':NULL,'q':'v'}`, mapT(scalar("string"), nullableT(scalar("string"))), `{"k":NULL, "q":"v"}`)
	check(`0.1`, decT(10, 2), `1/10`)
	check(`-0`, scalar("float64"), `f64(-0/0x8000000000000000)`)
	check(`nan`, scalar("float32"), `f32(NaN/0x7fc00000)`)
	check(`2100-12-31 23:59:59.123456789`, dt64T(9), `2100-12-31 23:59:59.123456789`)
	check(`['2024-01-02','1970-01-01']`, arrayT(scalar("date")), `[2024-01-02 00:00:00.000000000, 1970-01-01 00:00:00.000000000]`)
	check(`[[],[NULL,'a']]`, arrayT(arrayT(nullableT(scalar("string")))), `[[], [NULL, "a"]]`)
	check(`::ffff:1.2.3.4`, scalar("ipv6"), `"::ffff:1.2.3.4"`)
}
