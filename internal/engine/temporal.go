package engine

import (
	"fmt"
	"strconv"
	"strings"
)

// This file holds the temporal range policy. The project rule is: never
// silently wrong. A time.Time that a ClickHouse temporal column cannot hold
// must produce an explicit error, not a wrapped or saturated value.
//
// The driver gives no such error. Measured on ClickHouse 25.8.29.51 with
// clickhouse-go v2.47.0, four different silent corruptions occur, and EVERY
// call returned err = nil:
//
//	path                        written        stored
//	conn.Exec text interpolation  2299-12-31   2106-02-07 06:28:15.000
//	conn.Exec text interpolation  ...05.123    ...05.000 (fraction lost)
//	native PrepareBatch/Append    2299-12-31   1900-01-01 00:00:00.291
//	native PrepareBatch/Append    ...05.123    ...05.123 (correct)
//
// The native batch path is therefore NOT safe by itself. It repairs the lost
// sub-second fraction, but it still corrupts a far date, only differently. A
// range guard is thus necessary on BOTH the text path and the batch path.

// The temporal boundaries below were MEASURED against ClickHouse 25.8.29.51
// through the HTTP interface, which does not involve the Go driver. Each
// boundary is the last value that the server stores unchanged; one unit more
// is either saturated or refused. Do not replace these numbers from memory.
const (
	// Date is an unsigned 16-bit day count from the epoch. Measured:
	// toDate('2149-06-07') returns 2149-06-06, so the server saturates.
	chgenDateMinUnix int64 = 0              // 1970-01-01 00:00:00 UTC
	chgenDateMaxUnix int64 = 5662310400 - 1 // 2149-06-06 23:59:59 UTC

	// Date32 is a signed 32-bit day count. Measured: toDate32('1899-12-31')
	// returns 1900-01-01 and toDate32('2300-01-01') returns 2299-12-31.
	chgenDate32MinUnix int64 = -2208988800     // 1900-01-01 00:00:00 UTC
	chgenDate32MaxUnix int64 = 10413792000 - 1 // 2299-12-31 23:59:59 UTC

	// DateTime is an unsigned 32-bit second count. Measured:
	// toDateTime('2106-02-07 06:28:16') returns 2106-02-07 06:28:15.
	chgenDateTimeMinUnix int64 = 0          // 1970-01-01 00:00:00 UTC
	chgenDateTimeMaxUnix int64 = 4294967295 // 2106-02-07 06:28:15 UTC

	// DateTime64 with precision 0 through 8 has the Date32 calendar range in
	// the SERVER: an insert of '1899-12-31' stores 1900-01-01, and
	// toDateTime64('2300-01-01', 3) returns 2299-12-31. The column can
	// therefore hold 2299-12-31.
	//
	// The DRIVER cannot carry it. clickhouse-go converts every DateTime64
	// through int64 nanoseconds in both directions, whatever the declared
	// precision. Measured with v2.47.0: a batch write of 2299-12-31 into a
	// DateTime64(3) column stored 1900-01-01 00:00:00.291, and conn.Exec
	// stored 2106-02-07 06:28:15; both returned nil. The write limit is thus
	// the nanosecond ceiling, not the column calendar. A guard set to the
	// wider calendar range would pass exactly the values that corrupt.
	chgenDateTime64MinUnix int64 = -2208988800 // 1900-01-01 00:00:00 UTC
	chgenDateTime64MaxUnix int64 = 9223372036  // 2262-04-11 23:47:16 UTC

	// chgenDateTime64ColumnMaxUnix is the limit that the SERVER accepts for
	// precision 0 through 8. It is kept as a named value because it is what
	// the column can hold; it is deliberately NOT the guard limit, for the
	// reason above.
	chgenDateTime64ColumnMaxUnix int64 = 10413792000 - 1 // 2299-12-31 23:59:59 UTC

	// DateTime64(9) counts nanoseconds in a signed 64-bit integer, so its
	// range is much narrower than the other precisions. Measured:
	// toDateTime64('2262-04-11 23:47:17', 9) raises DECIMAL_OVERFLOW
	// (code 407), and 2262-04-11 23:47:16 is accepted. The lower end
	// saturates to 1900-01-01 exactly as the other precisions do, because
	// the column calendar, not the nanosecond count, is the binding limit
	// there.
	chgenDateTime64NanoMinUnix int64 = -2208988800 // 1900-01-01 00:00:00 UTC
	chgenDateTime64NanoMaxUnix int64 = 9223372036  // 2262-04-11 23:47:16 UTC
)

// chgenReadableMaxUnix is the largest instant that a scanned DateTime64 can
// report without ambiguity. clickhouse-go converts every DateTime64 through
// int64 nanoseconds, so a stored value past 2262-04-11 23:47:16 wraps.
// Measured: a stored 2299-12-31 arrives in Go as 1715-06-12 with err = nil.
const chgenReadableMaxUnix int64 = 9223372036

// chgenReadableMinUnix is the detector for that wrap. No DateTime64 column can
// hold an instant before 1900-01-01, so a scanned value before that limit
// cannot be a stored value: it is the int64 nanosecond count of a stored
// instant beyond 2262-04-11 read back as a signed integer. Measured: a stored
// 2299-12-31 arrives as 1715-06-12, and a stored 2262-04-11 23:47:16 arrives
// unchanged. The detector is therefore exact for the DateTime64 calendar.
const chgenReadableMinUnix int64 = chgenDateTime64MinUnix

// temporalKind names one temporal guard family. The zero value means the type
// is not temporal and needs no guard.
type temporalKind string

const (
	temporalNone       temporalKind = ""
	temporalDate       temporalKind = "Date"
	temporalDate32     temporalKind = "Date32"
	temporalDateTime   temporalKind = "DateTime"
	temporalDateTime64 temporalKind = "DateTime64"
	// temporalDateTime64Nano is DateTime64 with precision 9. It gets its own
	// kind because its upper bound is the int64 nanosecond limit, which is
	// nearly 40 years earlier than the limit of the lower precisions.
	temporalDateTime64Nano temporalKind = "DateTime64Nano"
)

// guardFunc is the name of the generated runtime helper for this kind.
func (k temporalKind) guardFunc() string {
	switch k {
	case temporalDate:
		return "chgenGuardDate"
	case temporalDate32:
		return "chgenGuardDate32"
	case temporalDateTime:
		return "chgenGuardDateTime"
	case temporalDateTime64:
		return "chgenGuardDateTime64"
	case temporalDateTime64Nano:
		return "chgenGuardDateTime64Nano"
	}
	return ""
}

// temporalKindOf reports the guard family of a bare temporal type. It does
// not look through Array, Map or Nullable; walkTemporal does that.
func temporalKindOf(columnType CHType) temporalKind {
	switch columnType.normalizedName() {
	case "date":
		return temporalDate
	case "date32":
		return temporalDate32
	case "datetime":
		return temporalDateTime
	case "datetime64":
		if dateTime64Precision(columnType) >= 9 {
			return temporalDateTime64Nano
		}
		return temporalDateTime64
	}
	return temporalNone
}

// dateTime64Precision returns the declared precision of a DateTime64 type.
// The default is 3 when the declaration omits it, which matches ClickHouse.
// A timezone-qualified declaration such as DateTime64(9, 'UTC') keeps the
// precision in its first literal argument, so the timezone does not change
// the guard family.
func dateTime64Precision(columnType CHType) int {
	if len(columnType.LiteralParams) == 0 {
		return 3
	}
	precision, err := strconv.Atoi(strings.TrimSpace(columnType.LiteralParams[0]))
	if err != nil {
		return 3
	}
	return precision
}

// temporalShape describes how to reach the time.Time values inside one Go
// value. Kind is the guard family; the wrapper chain says which containers
// the generated code must walk.
type temporalShape struct {
	Kind temporalKind
	// Nullable marks a pointer that must be nil-checked before the guard.
	Nullable bool
	// Array marks a slice whose elements carry the shape in Elem.
	Array bool
	// MapKey and MapValue mark a map whose key or value carries a temporal.
	MapKey   *temporalShape
	MapValue *temporalShape
	Elem     *temporalShape
}

// temporalShapeOf builds the walk plan for a ClickHouse type, or reports
// false when the type holds no temporal value at all. LowCardinality and
// AggregateFunction are transparent wrappers here: they do not change the Go
// representation, only the storage.
func temporalShapeOf(columnType CHType) (temporalShape, bool) {
	switch columnType.normalizedName() {
	case "lowcardinality":
		if len(columnType.Params) == 1 {
			return temporalShapeOf(columnType.Params[0])
		}
		return temporalShape{}, false
	case "aggregatefunction":
		if len(columnType.Params) >= 2 {
			return temporalShapeOf(columnType.Params[1])
		}
		return temporalShape{}, false
	case "nullable":
		if len(columnType.Params) != 1 {
			return temporalShape{}, false
		}
		inner, ok := temporalShapeOf(columnType.Params[0])
		if !ok {
			return temporalShape{}, false
		}
		inner.Nullable = true
		return inner, true
	case "array":
		if len(columnType.Params) != 1 {
			return temporalShape{}, false
		}
		elem, ok := temporalShapeOf(columnType.Params[0])
		if !ok {
			return temporalShape{}, false
		}
		return temporalShape{Array: true, Elem: &elem}, true
	case "map":
		if len(columnType.Params) != 2 {
			return temporalShape{}, false
		}
		shape := temporalShape{}
		found := false
		if key, ok := temporalShapeOf(columnType.Params[0]); ok {
			shape.MapKey = &key
			found = true
		}
		if value, ok := temporalShapeOf(columnType.Params[1]); ok {
			shape.MapValue = &value
			found = true
		}
		if !found {
			return temporalShape{}, false
		}
		return shape, true
	}
	kind := temporalKindOf(columnType)
	if kind == temporalNone {
		return temporalShape{}, false
	}
	return temporalShape{Kind: kind}, true
}

// isMap reports that this shape is a map level rather than a scalar or array.
func (s temporalShape) isMap() bool {
	return s.MapKey != nil || s.MapValue != nil
}

// chgenGuardStatements renders the guard statements for one value. expr is
// the Go expression that holds the value, label is the name that the error
// message reports, and depth keeps the generated loop variables unique.
// errPrefix is the return statement fragment used on failure.
func chgenGuardStatements(shape temporalShape, expr, label string, depth int, indent string, errReturn func(string) string) []string {
	var lines []string
	emit := func(text string) { lines = append(lines, indent+text) }

	if shape.Nullable {
		emit(fmt.Sprintf("if %s != nil {", expr))
		inner := shape
		inner.Nullable = false
		nested := chgenGuardStatements(inner, "(*"+expr+")", label, depth, indent+"\t", errReturn)
		lines = append(lines, nested...)
		emit("}")
		return lines
	}

	switch {
	case shape.Array:
		loopVar := fmt.Sprintf("chgenV%d", depth)
		emit(fmt.Sprintf("for _, %s := range %s {", loopVar, expr))
		nested := chgenGuardStatements(*shape.Elem, loopVar, label, depth+1, indent+"\t", errReturn)
		lines = append(lines, nested...)
		emit("}")
	case shape.isMap():
		keyVar := fmt.Sprintf("chgenK%d", depth)
		valueVar := fmt.Sprintf("chgenV%d", depth)
		if shape.MapValue != nil {
			emit(fmt.Sprintf("for %s, %s := range %s {", keyVar, valueVar, expr))
			if shape.MapKey != nil {
				nested := chgenGuardStatements(*shape.MapKey, keyVar, label, depth+1, indent+"\t", errReturn)
				lines = append(lines, nested...)
			} else {
				emit(fmt.Sprintf("\t_ = %s", keyVar))
			}
			nested := chgenGuardStatements(*shape.MapValue, valueVar, label, depth+1, indent+"\t", errReturn)
			lines = append(lines, nested...)
			emit("}")
		} else {
			emit(fmt.Sprintf("for %s := range %s {", keyVar, expr))
			nested := chgenGuardStatements(*shape.MapKey, keyVar, label, depth+1, indent+"\t", errReturn)
			lines = append(lines, nested...)
			emit("}")
		}
	default:
		call := fmt.Sprintf("%s(%s, %s)", shape.Kind.guardFunc(), expr, strconv.Quote(label))
		emit(fmt.Sprintf("if err := %s; err != nil {", call))
		emit("\t" + errReturn("err"))
		emit("}")
	}
	return lines
}

// chgenScanCheckStatements renders the read-side statements for one scanned
// value. The driver converts DateTime64 through int64 nanoseconds, so a
// stored value past 2262-04-11 arrives wrapped with no error. The check
// cannot repair the value, because the wrap is not reversible without knowing
// the stored instant, so it reports the unreadable cell as an explicit error.
func chgenScanCheckStatements(shape temporalShape, expr, label string, depth int, indent string, errReturn func(string) string) []string {
	if !shapeNeedsScanCheck(shape) {
		return nil
	}
	var lines []string
	emit := func(text string) { lines = append(lines, indent+text) }

	if shape.Nullable {
		emit(fmt.Sprintf("if %s != nil {", expr))
		inner := shape
		inner.Nullable = false
		lines = append(lines, chgenScanCheckStatements(inner, "(*"+expr+")", label, depth, indent+"\t", errReturn)...)
		emit("}")
		return lines
	}

	switch {
	case shape.Array:
		loopVar := fmt.Sprintf("chgenV%d", depth)
		emit(fmt.Sprintf("for _, %s := range %s {", loopVar, expr))
		lines = append(lines, chgenScanCheckStatements(*shape.Elem, loopVar, label, depth+1, indent+"\t", errReturn)...)
		emit("}")
	case shape.isMap():
		keyVar := fmt.Sprintf("chgenK%d", depth)
		valueVar := fmt.Sprintf("chgenV%d", depth)
		if shape.MapValue != nil && shapeNeedsScanCheck(*shape.MapValue) {
			emit(fmt.Sprintf("for %s, %s := range %s {", keyVar, valueVar, expr))
			if shape.MapKey != nil && shapeNeedsScanCheck(*shape.MapKey) {
				lines = append(lines, chgenScanCheckStatements(*shape.MapKey, keyVar, label, depth+1, indent+"\t", errReturn)...)
			} else {
				emit(fmt.Sprintf("\t_ = %s", keyVar))
			}
			lines = append(lines, chgenScanCheckStatements(*shape.MapValue, valueVar, label, depth+1, indent+"\t", errReturn)...)
			emit("}")
		} else if shape.MapKey != nil {
			emit(fmt.Sprintf("for %s := range %s {", keyVar, expr))
			lines = append(lines, chgenScanCheckStatements(*shape.MapKey, keyVar, label, depth+1, indent+"\t", errReturn)...)
			emit("}")
		}
	default:
		call := fmt.Sprintf("chgenCheckScannedTime(%s, %s)", expr, strconv.Quote(label))
		emit(fmt.Sprintf("if err := %s; err != nil {", call))
		emit("\t" + errReturn("err"))
		emit("}")
	}
	return lines
}

// shapeNeedsScanCheck reports whether any leaf of this shape can wrap on the
// read path. Only DateTime64 goes through int64 nanoseconds in the driver.
// Date, Date32 and DateTime cannot exceed the nanosecond limit, so they need
// no read-side check.
func shapeNeedsScanCheck(shape temporalShape) bool {
	switch {
	case shape.Array:
		return shape.Elem != nil && shapeNeedsScanCheck(*shape.Elem)
	case shape.isMap():
		if shape.MapKey != nil && shapeNeedsScanCheck(*shape.MapKey) {
			return true
		}
		return shape.MapValue != nil && shapeNeedsScanCheck(*shape.MapValue)
	}
	return shape.Kind == temporalDateTime64 || shape.Kind == temporalDateTime64Nano
}

// ---------------------------------------------------------------------------
// INTERVAL arithmetic result types.
//
// All the rules below were MEASURED on ClickHouse 25.8.29.51 through the HTTP
// interface, against REAL TABLE COLUMNS of each type. Literals are not usable
// as evidence here, because the server folds constants and then reports the
// type of the folded value, not the type of the operator.
//
// Measured matrix for "<temporal> + INTERVAL 1 <unit>" and the same with "-".
// The operator does not change the result type; only the left operand and the
// unit do.
//
//	unit         Date            Date32           DateTime      DateTime64(P)
//	NANOSECOND   (rejected)      (rejected)       DateTime64(9) DateTime64(max(P,9))
//	MICROSECOND  (rejected)      (rejected)       DateTime64(6) DateTime64(max(P,6))
//	MILLISECOND  (rejected)      (rejected)       DateTime64(3) DateTime64(max(P,3))
//	SECOND       DateTime        DateTime64(3)    DateTime      DateTime64(P)
//	MINUTE       DateTime        DateTime64(3)    DateTime      DateTime64(P)
//	HOUR         DateTime        DateTime64(3)    DateTime      DateTime64(P)
//	DAY          Date            Date32           DateTime      DateTime64(P)
//	WEEK         Date            Date32           DateTime      DateTime64(P)
//	MONTH        Date            Date32           DateTime      DateTime64(P)
//	QUARTER      Date            Date32           DateTime      DateTime64(P)
//	YEAR         Date            Date32           DateTime      DateTime64(P)
//
// In words: a unit of one day or more keeps the left operand type. A unit
// below one day promotes a date-only operand to a time-carrying one, and
// raises the DateTime64 precision to at least the precision of the unit.
//
// Timezone: a DateTime or DateTime64 operand keeps its declared timezone
// (measured: dtz + INTERVAL 1 DAY is DateTime('UTC'), dtz64 + INTERVAL 1
// MILLISECOND is DateTime64(6, 'UTC')). A promoted Date or Date32 operand
// gets a result with NO timezone name, even when the session timezone is set
// (measured with session_timezone=Asia/Tokyo: d + INTERVAL 1 HOUR is a bare
// DateTime). chgen therefore never invents a timezone argument.
//
// Wrappers: both Nullable and LowCardinality pass through unchanged
// (measured: nd + INTERVAL 1 SECOND is Nullable(DateTime), lcnd + INTERVAL 1
// DAY is LowCardinality(Nullable(Date))). The promotion happens inside the
// wrappers.

// intervalUnitPrecision maps an INTERVAL unit to the DateTime64 precision it
// requires. A unit of one day or more needs no sub-second precision and gets
// -1, which marks "does not promote".
//
// The names are the ones the ClickHouse parser accepts. The plural spellings
// (INTERVAL 2 DAYS) reach chgen through the same Unit identifier, so they are
// normalized before the lookup.
func intervalUnitPrecision(unit string) (precision int, subDay bool, known bool) {
	switch strings.ToLower(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(unit)), "s")) {
	case "nanosecond":
		return 9, true, true
	case "microsecond":
		return 6, true, true
	case "millisecond":
		return 3, true, true
	case "second", "minute", "hour":
		return 0, true, true
	case "day", "week", "month", "quarter", "year":
		return 0, false, true
	}
	// An unknown unit stays an explicit refusal. Guessing "keeps the left
	// type" would be silently wrong for any future sub-day unit.
	return 0, false, false
}

// intervalArithmeticResultType returns the type of "<operand> +/- INTERVAL n
// <unit>". It reports false when the combination has no measured rule, so the
// caller keeps its refusal instead of guessing.
//
// The operand may carry Nullable and LowCardinality wrappers; they are put
// back on the result unchanged.
func intervalArithmeticResultType(operand CHType, unit string) (CHType, bool) {
	unitPrecision, subDay, known := intervalUnitPrecision(unit)
	if !known {
		return CHType{}, false
	}
	base, nullable, lowCardinality := splitCHWrappers(operand)

	var result CHType
	switch base.normalizedName() {
	case "date":
		switch {
		case unitPrecision > 0:
			// Measured: Date rejects every sub-second unit with
			// ILLEGAL_TYPE_OF_ARGUMENT, so there is no result type.
			return CHType{}, false
		case subDay:
			result = CHType{Name: "DateTime"}
		default:
			result = CHType{Name: "Date"}
		}
	case "date32":
		switch {
		case unitPrecision > 0:
			// Measured: Date32 rejects every sub-second unit too.
			return CHType{}, false
		case subDay:
			// Measured: d32 + INTERVAL 1 SECOND is DateTime64(3),
			// NOT DateTime. Date32 spans years that DateTime
			// cannot hold, so the server promotes further.
			result = CHType{Name: "DateTime64", LiteralParams: []string{"3"}}
		default:
			result = CHType{Name: "Date32"}
		}
	case "datetime":
		if unitPrecision > 0 {
			result = CHType{
				Name:          "DateTime64",
				LiteralParams: append([]string{strconv.Itoa(unitPrecision)}, dateTimeTimezoneParams(base)...),
			}
			break
		}
		result = base
	case "datetime64":
		precision := dateTime64Precision(base)
		if unitPrecision > precision {
			precision = unitPrecision
		}
		result = CHType{
			Name:          "DateTime64",
			LiteralParams: append([]string{strconv.Itoa(precision)}, dateTimeTimezoneParams(base)...),
		}
	default:
		// A non-temporal left operand has no INTERVAL rule here.
		return CHType{}, false
	}
	return applyCHWrappers(result, nullable, lowCardinality), true
}

// dateTimeTimezoneParams returns the timezone literal of a DateTime or
// DateTime64 declaration, or nothing when the type carries no timezone.
// DateTime keeps the timezone in its only literal argument, DateTime64 in the
// second one after the precision.
func dateTimeTimezoneParams(base CHType) []string {
	switch base.normalizedName() {
	case "datetime":
		if len(base.LiteralParams) >= 1 {
			return []string{base.LiteralParams[0]}
		}
	case "datetime64":
		if len(base.LiteralParams) >= 2 {
			return []string{base.LiteralParams[1]}
		}
	}
	return nil
}
