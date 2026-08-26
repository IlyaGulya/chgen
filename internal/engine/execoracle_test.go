//go:build execoracle

package engine

// End-to-end execution oracle. The type-name oracle (fuzzoracle) compares
// only the server's opinion of a type. This oracle runs the whole chain:
// build boundary-value tables, let chgen generate Go code for them, compile
// and execute that code with the real clickhouse-go driver against a live
// ClickHouse server, and compare every value that arrived in a generated Go
// field against an independent reference.
//
// Independence of the reference: every table value enters the server as a
// SQL text literal over the HTTP interface, and the reference read is the
// HTTP TabSeparated text of the same cells. The clickhouse-go native binary
// protocol appears only inside the code under test. The two channels share
// no client code, so a wrong write and a wrong read cannot cancel out.
//
// The failure criterion is a VALUE DIFFERENCE, never a Scan error: the
// driver scans a NULL into a non-pointer field without any error, so an
// err != nil check is silent exactly where the harm is largest.
//
// Run:
//
//	docker run -d --rm --name chgen-execoracle -p 19010:8123 -p 19011:9000 \
//	    -e CLICKHOUSE_PASSWORD=x clickhouse/clickhouse-server:25.8
//	CHGEN_EXEC_HTTP='http://localhost:19010/?password=x' \
//	CHGEN_EXEC_NATIVE=localhost:19011 CHGEN_EXEC_PASSWORD=x \
//	    go test -tags execoracle -run TestExecOracle -v ./internal/engine
//
// Optional keys: CHGEN_EXEC_SEED (default 1), CHGEN_EXEC_RANDN (default 40
// random nested types), CHGEN_EXEC_OUT (machine-readable JSON path),
// CHGEN_EXEC_DATABASE (name of the run database; the default is a unique
// name that holds the process id and a nanosecond stamp).
//
// Isolation. Each run makes its own database and works only in it. Two runs
// on one server thus do not share tables. A run that passes removes its
// database. A run that fails keeps it, so a reader can look at the fixture.
// A database that CHGEN_EXEC_DATABASE names from the outside is never
// removed, because the run does not own it. The name is also written to the
// JSON artifact as run_database.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IlyaGulya/chgen/internal/conformance"
)

// xcase is one oracle case: a table with one typed value column and
// boundary-value rows.
type xcase struct {
	name     string
	typ      *xt
	ddlType  string
	rows     []string // SQL literals, one per row
	category string   // "", chgen_unsupported, ch_rejected, generation_failed
	note     string
	noInsert bool
}

func (c xcase) declaredSQLType() string {
	if c.ddlType != "" {
		return c.ddlType
	}
	return c.typ.sqlType()
}

// probe is one hand-written query that exercises a known risk area. The
// reference is the HTTP text of refSQL; the Go result of runnerSQL must
// match it.
type probe struct {
	name   string
	chgen  string // full annotated chgen query block
	call   string // runner Go expression that calls the generated method
	refSQL string // reference query for the HTTP channel
	typ    *xt    // type of the compared column (the last selected column)
	col    int    // index of the compared column in the result row
	ncols  int
	note   string
}

func buildScalarKinds() []*xt {
	return []*xt{
		scalar("bool"),
		scalar("int8"), scalar("int16"), scalar("int32"), scalar("int64"),
		scalar("uint8"), scalar("uint16"), scalar("uint32"), scalar("uint64"),
		scalar("float32"), scalar("float64"),
		decT(10, 2), decT(18, 4), decT(38, 10),
		scalar("string"), fsT(8), scalar("uuid"), scalar("ipv4"), scalar("ipv6"),
		scalar("date"), scalar("date32"), scalar("datetime"), dt64T(3), dt64T(9),
	}
}

func buildCases(seed int64, randN int) []xcase {
	var cases []xcase
	add := func(name string, t *xt) {
		cases = append(cases, xcase{name: name, typ: t, rows: rowLiterals(t)})
	}
	for _, base := range buildScalarKinds() {
		tag := strings.NewReplacer("(", "_", ")", "", ",", "_", " ", "").Replace(strings.ToLower(base.sqlType()))
		add("bare_"+tag, base)
		add("null_"+tag, nullableT(base))
		add("arr_"+tag, arrayT(base))
		add("arrnull_"+tag, arrayT(nullableT(base)))
		add("mapval_"+tag, mapT(scalar("string"), base))
	}
	for _, base := range []*xt{scalar("int128"), scalar("uint128"), scalar("int256"), scalar("uint256")} {
		tag := base.kind
		add("bare_"+tag, base)
		add("null_"+tag, nullableT(base))
		add("arr_"+tag, arrayT(base))
		add("arrnull_"+tag, arrayT(nullableT(base)))
		add("mapval_"+tag, mapT(scalar("string"), base))
	}
	for _, alias := range []struct {
		name      string
		declared  string
		canonical string
	}{
		{"int", "INT", "int32"}, {"integer", "INTEGER", "int32"}, {"mediumint", "MEDIUMINT", "int32"},
		{"bigint", "BIGINT", "int64"}, {"signed", "SIGNED", "int64"}, {"unsigned", "UNSIGNED", "uint64"},
		{"smallint", "SMALLINT", "int16"}, {"tinyint", "TINYINT", "int8"}, {"int1", "INT1", "int8"},
		{"byte", "BYTE", "int8"}, {"bit", "BIT", "uint64"},
		{"float", "FLOAT", "float32"}, {"real", "REAL", "float32"}, {"single", "SINGLE", "float32"},
		{"double", "DOUBLE", "float64"}, {"year", "YEAR", "uint16"},
	} {
		typ := scalar(alias.canonical)
		cases = append(cases, xcase{name: "alias_" + alias.name, typ: typ, ddlType: alias.declared, rows: rowLiterals(typ)})
	}
	for _, alias := range []struct {
		name      string
		declared  string
		canonical *xt
	}{
		{"datetime_scale0", "DateTime(0)", scalar("datetime")},
		{"datetime_scale3", "DateTime(3)", dt64T(3)},
		{"datetime64_default", "DateTime64", dt64T(3)},
	} {
		cases = append(cases, xcase{name: "alias_" + alias.name, typ: alias.canonical, ddlType: alias.declared, rows: rowLiterals(alias.canonical)})
	}
	for _, alias := range []struct {
		name     string
		declared string
		prec     int
		scale    int
	}{
		{"decimal", "Decimal", 10, 0}, {"dec", "DEC", 10, 0}, {"fixed", "FIXED", 10, 0}, {"numeric", "NUMERIC", 10, 0},
		{"dec12", "DEC(12)", 12, 0}, {"dec12_2", "DEC(12, 2)", 12, 2},
	} {
		typ := decT(alias.prec, alias.scale)
		cases = append(cases, xcase{name: "alias_" + alias.name, typ: typ, ddlType: alias.declared, rows: rowLiterals(typ)})
	}
	for _, alias := range []struct {
		name      string
		declared  string
		canonical string
	}{
		{"float24", "FLOAT(24)", "float32"}, {"float25", "FLOAT(25)", "float32"}, {"float53", "FLOAT(53)", "float32"},
		{"real_args", "REAL(1, 2)", "float32"}, {"double_arg", "DOUBLE(24)", "float64"},
	} {
		typ := scalar(alias.canonical)
		cases = append(cases, xcase{name: "alias_" + alias.name, typ: typ, ddlType: alias.declared, rows: rowLiterals(typ)})
	}
	// LowCardinality set.
	add("lc_string", lcT(scalar("string")))
	add("lc_fixedstring8", lcT(fsT(8)))
	add("lc_null_string", lcT(nullableT(scalar("string"))))
	add("lc_date", lcT(scalar("date")))
	add("lc_uint64", lcT(scalar("uint64")))
	add("arr_lc_string", arrayT(lcT(scalar("string"))))
	// Curated deep combinations.
	add("arr_arr_int32", arrayT(arrayT(scalar("int32"))))
	add("arr_map_string_int64", arrayT(mapT(scalar("string"), scalar("int64"))))
	add("map_string_arr_null_string", mapT(scalar("string"), arrayT(nullableT(scalar("string")))))
	add("map_uint64_map_string_int64", mapT(scalar("uint64"), mapT(scalar("string"), scalar("int64"))))
	add("map_date_decimal10_2", mapT(scalar("date"), decT(10, 2)))
	add("arr_arr_null_string", arrayT(arrayT(nullableT(scalar("string")))))
	add("arr_arr_int128", arrayT(arrayT(scalar("int128"))))
	add("simple_aggregate_sum_int64", scalar("simpleaggregate_sum_int64"))
	// Enum in every wrapper shape. Enum8 and Enum16 used to have no Go
	// mapping at all, thus the oracle could only record the refusal
	// (finding R7). The refusal is now closed: measurement showed that an
	// Enum travels as its NAME and that a Go string carries it in every
	// shape, so these cases must round-trip like any other type.
	for _, base := range []*xt{scalar("enum8"), scalar("enum16")} {
		tag := base.kind
		add("bare_"+tag, base)
		add("null_"+tag, nullableT(base))
		add("arr_"+tag, arrayT(base))
		add("arrnull_"+tag, arrayT(nullableT(base)))
		add("mapval_"+tag, mapT(scalar("string"), base))
	}
	// An Enum as a Map KEY. The name is the key, and the negative
	// numbering of the set must not reach it.
	add("mapkey_enum8", mapT(scalar("enum8"), scalar("string")))
	// A UUID as a Map KEY. uuid.UUID is a [16]byte array, thus unlike
	// net.IP it is a valid Go map key and needs no refusal. This case
	// guards that difference.
	add("mapkey_uuid", mapT(scalar("uuid"), scalar("string")))
	// A UUID nested two containers deep, which the flat shapes above do
	// not reach.
	add("arr_arr_uuid", arrayT(arrayT(scalar("uuid"))))
	// Canary A: Decimal(10,2) with 0.1, a value that no binary float
	// holds exactly. Before the decimal.Decimal mapping this case was a
	// mandatory finding; now it is a mandatory NON-finding: the exact
	// rational comparison against the HTTP reference must agree.
	cases = append(cases, xcase{name: "canary_decimal", typ: decT(10, 2), rows: []string{"0.1", "12345.67"}})
	// Canary C: an IPv4-mapped IPv6 address inside an Array. It joins the
	// two defects that the net.IP mapping removed. A string target gave
	// raw binary for the container (R5) and dropped the address family of
	// the mapped value (R6), and neither loss raised an error. This is a
	// mandatory NON-finding: the round trip against the HTTP reference
	// must keep ::ffff:1.2.3.4 in both directions.
	cases = append(cases, xcase{
		name: "canary_ip_mapped",
		typ:  arrayT(scalar("ipv6")),
		rows: []string{"['::ffff:1.2.3.4','2001:db8::1','::']"},
	})
	// Canary D: a UUID inside containers, in the shapes that the string
	// mapping could not carry. Array(UUID), Array(Nullable(UUID)) and
	// Map(String, UUID) all refused the scan
	// ("converting uuid.UUID to string is unsupported"), and a UUID as a
	// Map KEY refused it too. Since the uuid.UUID mapping this is a
	// regression pin: a finding on it fails the oracle. The nil UUID and
	// an upper-case declaration are in the rows, because the text form of
	// a UUID has both a case and a dash convention and neither may change
	// the value.
	cases = append(cases, xcase{
		name: "canary_uuid_container",
		typ:  arrayT(scalar("uuid")),
		rows: []string{
			"['00000000-0000-0000-0000-000000000000','FEDCBA98-7654-3210-FEDC-BA9876543210']",
		},
	})
	// Canary E: an Enum whose value set is numbered from a negative
	// number, inside an Array. The wire form is the NAME, thus the number
	// must never reach the Go value. Enum had no Go mapping at all before
	// (finding R7), so this whole shape could not run. It is now a
	// mandatory NON-finding.
	cases = append(cases, xcase{
		name: "canary_enum_negative",
		typ:  arrayT(scalar("enum8")),
		rows: []string{"['a','zz','neg']"},
	})
	// Refusal witness: a type that chgen still has no Go mapping for. The
	// oracle must keep recording a refusal as a refusal instead of
	// generating code for a shape it cannot carry. Enum8 held this role
	// until the Enum mapping closed it; Tuple is the replacement, because
	// its element types are still lost (type-oracle finding N5) and it has
	// no goType branch.
	cases = append(cases, xcase{name: "unsupported_tuple", typ: &xt{kind: "tuple"}, rows: []string{"(1, 'a')"}})
	// Random nested combinations, deterministic by seed.
	r := rand.New(rand.NewSource(seed))
	for index := 0; index < randN; index++ {
		t := randomType(r, 3)
		add(fmt.Sprintf("rand_%02d", index), t)
	}
	return cases
}

func randomType(r *rand.Rand, depth int) *xt {
	scalars := buildScalarKinds()
	if depth <= 0 || r.Intn(3) == 0 {
		return scalars[r.Intn(len(scalars))]
	}
	switch r.Intn(4) {
	case 0:
		inner := randomType(r, 0)
		return nullableT(inner)
	case 1:
		pool := []*xt{scalar("string"), fsT(8), scalar("date"), scalar("uint64"), nullableT(scalar("string"))}
		return lcT(pool[r.Intn(len(pool))])
	case 2:
		return arrayT(randomType(r, depth-1))
	default:
		keys := []*xt{scalar("string"), scalar("uint64"), scalar("int32"), scalar("date"), scalar("uuid"), fsT(8)}
		return mapT(keys[r.Intn(len(keys))], randomType(r, depth-1))
	}
}

// httpCH is the HTTP text channel to the reference server. The database
// field binds every query to the database of the run. It is empty only for
// the admin statements that make and remove that database.
type httpCH struct {
	base     string
	database string
}

// admin returns a channel to the same server with no database bound. Use it
// for CREATE DATABASE and DROP DATABASE.
func (h *httpCH) admin() *httpCH { return &httpCH{base: h.base} }

// withDatabase returns a channel that binds every query to the given
// database.
func (h *httpCH) withDatabase(database string) *httpCH {
	return &httpCH{base: h.base, database: database}
}

func (h *httpCH) exec(t *testing.T, query string) (string, error) {
	endpoint := h.base
	if !strings.Contains(endpoint, "?") {
		endpoint += "?"
	} else {
		endpoint += "&"
	}
	if h.database != "" {
		endpoint += "database=" + url.QueryEscape(h.database) + "&"
	}
	endpoint += "allow_suspicious_low_cardinality_types=1&session_timezone=UTC&query=" + url.QueryEscape(query)
	response, err := http.Post(endpoint, "text/plain", nil)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

// tsvTypeHeader reads the second line of a TSVWithNamesAndTypes result,
// which holds the server's own type name for each selected column. This is
// the only source of truth for a column's real type: toTypeName is analysis
// over a literal and can differ from the type of a real stored column, so
// this helper must always run over a live table, never over a literal
// expression.
//
// The type-name field is TSV-escaped text like every other TSV field, thus
// an Enum's quoted member names arrive as \'a\' and must be TSV-unescaped
// before the string compares equal to the type text chgen holds.
func tsvTypeHeader(body string) ([]string, error) {
	lines := strings.SplitN(body, "\n", 3)
	if len(lines) < 2 {
		return nil, fmt.Errorf("TSVWithNamesAndTypes body has no type header: %q", body)
	}
	fields := strings.Split(lines[1], "\t")
	for index, field := range fields {
		fields[index] = string(tsvUnescape(field))
	}
	return fields, nil
}

// tsvRows splits an HTTP TabSeparated result into raw fields per row.
func tsvRows(body string) [][]string {
	// An empty line is a valid row (an empty String value); only the
	// final newline terminator is dropped.
	body = strings.TrimSuffix(body, "\n")
	if body == "" {
		return nil
	}
	var rows [][]string
	for _, line := range strings.Split(body, "\n") {
		rows = append(rows, strings.Split(line, "\t"))
	}
	return rows
}

// finding is one oracle result entry.
type finding struct {
	Case      string `json:"case"`
	CHType    string `json:"ch_type"`
	Direction string `json:"direction"` // scan, insert, probe, type, generation, ch_ddl
	RowID     int    `json:"rowid"`
	Class     string `json:"class"`
	Reference string `json:"reference"`
	Got       string `json:"got"`
	Detail    string `json:"detail"`
	Inherited bool   `json:"inherited,omitempty"`
}

type oracleArtifact struct {
	Date           string             `json:"date"`
	CHVersion      string             `json:"clickhouse_version"`
	DriverVersion  string             `json:"driver_version"`
	RunDatabase    string             `json:"run_database"`
	Seed           int64              `json:"seed"`
	RandomTypes    int                `json:"random_types"`
	Cases          int                `json:"cases"`
	ActiveCases    int                `json:"active_cases"`
	ScanCells      int                `json:"scan_cells_compared"`
	InsertCells    int                `json:"insert_cells_compared"`
	TypesChecked   int                `json:"types_checked"`
	Categories     map[string]int     `json:"case_categories"`
	FindingClasses map[string]int     `json:"finding_classes"`
	Findings       []finding          `json:"findings"`
	Conformance    conformance.Report `json:"conformance"`
}

const execOracleDriverVersion = "github.com/ClickHouse/clickhouse-go/v2 v2.47.0"

func TestExecOracle(t *testing.T) {
	httpBase := os.Getenv("CHGEN_EXEC_HTTP")
	nativeAddr := os.Getenv("CHGEN_EXEC_NATIVE")
	if httpBase == "" || nativeAddr == "" {
		t.Skip("set CHGEN_EXEC_HTTP and CHGEN_EXEC_NATIVE to run the execution oracle")
	}
	seed := int64(1)
	if raw := os.Getenv("CHGEN_EXEC_SEED"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.Fatalf("bad CHGEN_EXEC_SEED: %v", err)
		}
		seed = parsed
	}
	randN := 40
	if raw := os.Getenv("CHGEN_EXEC_RANDN"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("bad CHGEN_EXEC_RANDN: %v", err)
		}
		randN = parsed
	}
	admin := (&httpCH{base: httpBase}).admin()
	version, err := admin.exec(t, "SELECT version()")
	if err != nil {
		t.Fatalf("ClickHouse is not reachable: %v", err)
	}
	version = strings.TrimSpace(version)

	// Isolation. The run makes its own database and works only in it. All
	// oracle tables are named without a database prefix, thus the bound
	// database of the connection puts every one of them in the private
	// database. The name holds the process id and a nanosecond stamp, thus
	// two runs on one server, and also two runs on one machine, get
	// different names. Before this scheme the run used the default
	// database with fixed table names and dropped each table first, so a
	// second run deleted the fixture of the first one in silence.
	// CHGEN_EXEC_DATABASE names the database for a manual inspection.
	runDatabase := os.Getenv("CHGEN_EXEC_DATABASE")
	// A name from the outside makes the database the property of the
	// caller. The run then does not remove it at the end.
	ownDatabase := runDatabase == ""
	if ownDatabase {
		runDatabase = fmt.Sprintf("chgen_exec_%d_%d", os.Getpid(), time.Now().UnixNano())
	}
	create := "CREATE DATABASE " + runDatabase
	if !ownDatabase {
		// The caller named the database and can give the same name twice.
		create = "CREATE DATABASE IF NOT EXISTS " + runDatabase
	}
	// For a generated name a database that is already present is not ours
	// to own. Refuse, because a silent reuse is exactly the shared fixture
	// that this harness must not have.
	if _, err := admin.exec(t, create); err != nil {
		t.Fatalf("create run database %q: %v", runDatabase, err)
	}
	ch := admin.withDatabase(runDatabase)
	t.Logf("run database=%s", runDatabase)
	t.Cleanup(func() {
		// Clean up only a database that this run owns. A database given
		// from the outside stays, because the run does not own it.
		if !ownDatabase {
			t.Logf("database %s came from CHGEN_EXEC_DATABASE and is kept", runDatabase)
			return
		}
		// Keep the fixture of a failed run, so a reader can look at the
		// exact tables that produced the failure. A run that passed has
		// nothing left to look at, thus it removes its database.
		if t.Failed() {
			t.Logf("test failed: database %s is kept for inspection; remove it with DROP DATABASE %s", runDatabase, runDatabase)
			return
		}
		if _, err := admin.exec(t, "DROP DATABASE IF EXISTS "+runDatabase); err != nil {
			t.Logf("drop run database %q: %v", runDatabase, err)
		}
	})

	cases := buildCases(seed, randN)
	var findings []finding
	categories := map[string]int{}

	// Phase 1: filter by chgen type support, then create and fill tables.
	for index := range cases {
		c := &cases[index]
		// There is no hard-coded skip for Enum any more. The generic
		// probe below asks goType itself, thus a type that chgen
		// refuses is still recorded as a refusal, and a type that
		// chgen supports is executed instead of being skipped. The
		// old skip would have hidden the new Enum cases.
		parsed, err := parseCHTypeName(c.declaredSQLType())
		if err == nil {
			_, err = goType(parsed)
		}
		if err != nil {
			c.category = "chgen_unsupported"
			c.note = err.Error()
			continue
		}
		table := "c_" + c.name
		if _, err := ch.exec(t, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
		ddl := fmt.Sprintf("CREATE TABLE %s (rowid UInt32, v %s) ENGINE = Memory", table, c.declaredSQLType())
		if _, err := ch.exec(t, ddl); err != nil {
			c.category = "ch_rejected"
			c.note = firstLineOf(err.Error())
			continue
		}
		var tuples []string
		for rowID, literal := range c.rows {
			tuples = append(tuples, fmt.Sprintf("(%d, %s)", rowID, literal))
		}
		if _, err := ch.exec(t, fmt.Sprintf("INSERT INTO %s VALUES %s", table, strings.Join(tuples, ", "))); err != nil {
			c.category = "ch_rejected"
			c.note = firstLineOf(err.Error())
			continue
		}
	}

	// Canary B table: a Nullable column read through a -- result: override
	// into a non-pointer field. The driver scans the NULL into the zero
	// value WITHOUT an error; the oracle must flag the silent zero.
	if _, err := ch.exec(t, "DROP TABLE IF EXISTS canary_null"); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.exec(t, "CREATE TABLE canary_null (rowid UInt32, v Nullable(Int32)) ENGINE = Memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.exec(t, "INSERT INTO canary_null VALUES (0, NULL), (1, 42)"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE array_join_probe_rows (id UInt8, values Array(Int32), peers Array(UInt16), nullable_values Array(Nullable(Int16)), low_values Array(LowCardinality(String)), tuple_values Array(Tuple(x UInt8, y Nullable(String))), nested_values Nested(k UInt16, v String), empty_values Array(UInt32), mapped Map(String, UInt16)) ENGINE = Memory",
		"INSERT INTO array_join_probe_rows (id, values, peers, nullable_values, low_values, tuple_values, nested_values.k, nested_values.v, empty_values, mapped) VALUES (1, [10, 20], [1, 2], [NULL, 3], ['a', 'b'], [(1, 'x'), (2, NULL)], [3, 4], ['n3', 'n4'], [], map('x', toUInt16(7))), (2, [30, 40], [3, 4], [5, NULL], ['c', 'd'], [(5, 'y'), (6, 'z')], [7, 8], ['n7', 'n8'], [9], map('y', toUInt16(8)))",
		"CREATE TABLE final_probe_rows (id UInt64, value Int32, version UInt64) ENGINE = ReplacingMergeTree(version) ORDER BY id",
		"CREATE TABLE final_probe_other (id UInt64, value Int16, version UInt64) ENGINE = ReplacingMergeTree(version) ORDER BY id",
		"CREATE TABLE final_probe_summing (id UInt64, value Int32) ENGINE = SummingMergeTree ORDER BY id",
		"CREATE TABLE final_probe_collapsing (id UInt64, value Int32, sign Int8) ENGINE = CollapsingMergeTree(sign) ORDER BY id",
		"INSERT INTO final_probe_rows VALUES (1, 10, 1), (1, 20, 2), (2, 30, 1)",
		"INSERT INTO final_probe_other VALUES (1, 4, 1), (1, 5, 2), (3, 6, 1)",
		"INSERT INTO final_probe_summing VALUES (1, 2), (1, 3), (2, 4)",
		"INSERT INTO final_probe_collapsing VALUES (1, 10, 1), (1, 10, -1), (2, 20, 1)",
	} {
		if _, err := ch.exec(t, statement); err != nil {
			t.Fatal(err)
		}
	}

	probes := buildProbes()

	// Phase 2: build the chgen inputs and generate one Go package.
	scratch := t.TempDir()
	schemaSQL := &strings.Builder{}
	querySQL := &strings.Builder{}
	fmt.Fprintf(schemaSQL, "CREATE TABLE canary_null (rowid UInt32, v Nullable(Int32)) ENGINE = Memory;\n")
	fmt.Fprintln(schemaSQL, "CREATE TABLE array_join_probe_rows (id UInt8, values Array(Int32), peers Array(UInt16), nullable_values Array(Nullable(Int16)), low_values Array(LowCardinality(String)), tuple_values Array(Tuple(x UInt8, y Nullable(String))), nested_values Nested(k UInt16, v String), empty_values Array(UInt32), mapped Map(String, UInt16)) ENGINE = Memory;")
	fmt.Fprintln(schemaSQL, "CREATE TABLE final_probe_rows (id UInt64, value Int32, version UInt64) ENGINE = ReplacingMergeTree(version) ORDER BY id;")
	fmt.Fprintln(schemaSQL, "CREATE TABLE final_probe_other (id UInt64, value Int16, version UInt64) ENGINE = ReplacingMergeTree(version) ORDER BY id;")
	fmt.Fprintln(schemaSQL, "CREATE TABLE final_probe_summing (id UInt64, value Int32) ENGINE = SummingMergeTree ORDER BY id;")
	fmt.Fprintln(schemaSQL, "CREATE TABLE final_probe_collapsing (id UInt64, value Int32, sign Int8) ENGINE = CollapsingMergeTree(sign) ORDER BY id;")
	for index := range cases {
		c := &cases[index]
		if c.category != "" {
			continue
		}
		fmt.Fprintf(schemaSQL, "CREATE TABLE c_%s (rowid UInt32, v %s) ENGINE = Memory;\n", c.name, c.declaredSQLType())
		fmt.Fprintf(querySQL, "-- name: Read%s :many\nSELECT rowid, v FROM c_%s ORDER BY rowid;\n\n", goCaseName(c.name), c.name)
		fmt.Fprintf(querySQL, "-- name: Read%sAt :one\nSELECT rowid, v FROM c_%s WHERE rowid = chgen.arg('Rowid');\n\n", goCaseName(c.name), c.name)
		if !c.noInsert {
			fmt.Fprintf(querySQL, "-- name: Ins%s :exec\nINSERT INTO c_%s (rowid, v) VALUES (chgen.arg('Rowid'), chgen.arg('V'));\n\n", goCaseName(c.name), c.name)
		}
	}
	for _, p := range probes {
		querySQL.WriteString(p.chgen + "\n\n")
	}
	schemaPath := filepath.Join(scratch, "schema.sql")
	queryPath := filepath.Join(scratch, "queries.sql")
	if err := os.WriteFile(schemaPath, []byte(schemaSQL.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queryPath, []byte(querySQL.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	catalogs, err := ParseSchemaCatalogs([]string{schemaPath})
	if err != nil {
		t.Fatalf("schema catalog: %v", err)
	}
	queries, err := ParseQueryFiles([]string{queryPath}, catalogs)
	if err != nil {
		// Bisect: drop the failing cases one by one is expensive; report
		// the whole failure instead, because per-case pre-validation below
		// should have caught type-level refusals already.
		t.Fatalf("query parsing failed: %v", err)
	}
	generated, err := Generate("gen", queries)
	if err != nil {
		t.Fatalf("generation failed: %v", err)
	}

	// Phase 3: write and compile the runner module.
	runnerDir := filepath.Join(scratch, "runner")
	if err := os.MkdirAll(filepath.Join(runnerDir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runnerDir, "gen", "queries.sql.go"), generated, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeRunnerModule(runnerDir, cases, probes); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", "runner-bin", ".")
	build.Dir = runnerDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("runner does not compile (uncompilable generated code):\n%s", output)
	}

	// Phase 4: execute the runner against the live server.
	run := exec.Command("./runner-bin")
	run.Dir = runnerDir
	run.Env = append(os.Environ(),
		"CH_NATIVE="+nativeAddr,
		"CH_PASSWORD="+os.Getenv("CHGEN_EXEC_PASSWORD"),
		// The runner must read and write in the database of this run.
		// The generated queries name the tables without a prefix.
		"CH_DATABASE="+runDatabase,
	)
	output, err := run.Output()
	if err != nil {
		detail := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			detail = string(exitErr.Stderr)
		}
		t.Fatalf("runner failed: %v\n%s", err, detail)
	}
	var runnerResult struct {
		Cases map[string]struct {
			ReadErr  string             `json:"readErr"`
			CellErrs map[int]string     `json:"cellErrs"`
			Rows     [][]map[string]any `json:"rows"`
			InsErr   string             `json:"insErr"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(output, &runnerResult); err != nil {
		t.Fatalf("runner output is not JSON: %v\n%s", err, output)
	}

	caseByID := make(map[string]*xcase)
	var conformanceInputs []conformance.Input
	for index := range cases {
		c := &cases[index]
		if c.category != "" {
			continue
		}
		caseByID[c.name] = c
		conformanceInputs = append(conformanceInputs, conformance.Input{
			ID: c.name, Expression: "v", Table: "c_" + c.name,
		})
	}
	sharedServer := conformance.NewHTTPServer(httpBase, runDatabase)
	conformanceReport, err := (conformance.Runner{
		Server: sharedServer,
		Infer: func(input conformance.Input) (string, error) {
			c := caseByID[input.ID]
			if c == nil {
				return "", fmt.Errorf("execution oracle has no case %q", input.ID)
			}
			return c.typ.sqlType(), nil
		},
		Native: func(_ context.Context, input conformance.Input) conformance.NativeResult {
			result, ok := runnerResult.Cases["case_"+input.ID]
			if !ok {
				return conformance.NativeResult{Supported: true, Error: "generated runner has no result"}
			}
			return conformance.NativeResult{Supported: true, Value: result.Rows, Error: result.ReadErr}
		},
	}).Run(context.Background(), schemaSQL.String(), execOracleSeedSource(seed, cases), conformanceInputs)
	if err != nil {
		t.Fatalf("shared conformance runner: %v", err)
	}

	// Phase 4b: type identity. This dimension is independent of the value
	// comparison below. The value canon deliberately widens: every integer
	// width reads as one big.Int, and String/FixedString/Enum all read as
	// one byte string, so that a widening transport never reports a value
	// difference. That widening would hide a wrong TYPE forever, because a
	// value canon cannot distinguish Int8 from Int64 or String from Enum8.
	// This loop closes that gap by asking the server for its own type name
	// of the real stored column, over FORMAT TSVWithNamesAndTypes, and
	// comparing that name against the type chgen was told to generate for.
	// toTypeName over a literal is analysis, not execution, and is never
	// used here; the header comes from a SELECT over the real table.
	typesChecked := 0
	for index := range cases {
		c := &cases[index]
		if c.category != "" {
			continue
		}
		typeBody, err := ch.exec(t, fmt.Sprintf("SELECT v FROM c_%s LIMIT 1 FORMAT TSVWithNamesAndTypes", c.name))
		if err != nil {
			t.Fatalf("type header read %s: %v", c.name, err)
		}
		header, err := tsvTypeHeader(typeBody)
		if err != nil {
			t.Fatalf("case %s: %v", c.name, err)
		}
		typesChecked++
		serverType := header[0]
		if serverType != c.typ.sqlType() {
			findings = append(findings, finding{
				Case: c.name, CHType: c.typ.sqlType(), Direction: "type", RowID: -1,
				Class: "type_mismatch", Reference: c.typ.sqlType(), Got: serverType,
				Detail: "server's own column type differs from the type chgen generated for",
			})
		}
	}

	// Phase 5: compare. Scan direction first.
	scanCells := 0
	scanFlagged := map[string]map[int]bool{}
	for index := range cases {
		c := &cases[index]
		if c.category != "" {
			continue
		}
		result, ok := runnerResult.Cases["case_"+c.name]
		if !ok {
			t.Fatalf("runner has no result for case %s", c.name)
		}
		if result.ReadErr != "" {
			if len(result.CellErrs) == 0 {
				findings = append(findings, finding{
					Case: c.name, CHType: c.typ.sqlType(), Direction: "scan",
					RowID: -1, Class: "runtime_error", Detail: firstLineOf(result.ReadErr),
				})
				continue
			}
			rowIDs := make([]int, 0, len(result.CellErrs))
			for rowID := range result.CellErrs {
				rowIDs = append(rowIDs, rowID)
			}
			sort.Ints(rowIDs)
			for _, rowID := range rowIDs {
				refBody, refErr := ch.exec(t, fmt.Sprintf("SELECT v FROM c_%s WHERE rowid = %d FORMAT TSV", c.name, rowID))
				if refErr != nil {
					t.Fatalf("reference read %s row %d: %v", c.name, rowID, refErr)
				}
				findings = append(findings, finding{
					Case: c.name, CHType: c.typ.sqlType(), Direction: "scan",
					RowID: rowID, Class: "runtime_error", Reference: strings.TrimSuffix(refBody, "\n"),
					Got:    nativeValueFromDateTime64Error(result.CellErrs[rowID]),
					Detail: firstLineOf(result.CellErrs[rowID]),
				})
			}
			continue
		}
		refBody, err := ch.exec(t, fmt.Sprintf("SELECT v FROM c_%s WHERE rowid < 1000 ORDER BY rowid FORMAT TSV", c.name))
		if err != nil {
			t.Fatalf("reference read %s: %v", c.name, err)
		}
		refRows := tsvRows(refBody)
		if len(refRows) != len(result.Rows) {
			findings = append(findings, finding{
				Case: c.name, CHType: c.typ.sqlType(), Direction: "scan", RowID: -1,
				Class: "value_distortion", Reference: fmt.Sprintf("%d rows", len(refRows)),
				Got: fmt.Sprintf("%d rows", len(result.Rows)), Detail: "row count differs",
			})
			continue
		}
		for rowID := range refRows {
			scanCells++
			reference, err := parseCHText(refRows[rowID][0], c.typ, false)
			if err != nil {
				t.Fatalf("case %s row %d: cannot parse reference %q: %v", c.name, rowID, refRows[rowID][0], err)
			}
			got, err := goDumpToCanon(result.Rows[rowID][1], c.typ)
			if err != nil {
				findings = append(findings, addF(c, "scan", rowID, "value_distortion", reference.String(), fmt.Sprintf("%v", result.Rows[rowID][1]), err.Error()))
				markFlag(scanFlagged, c.name, rowID)
				continue
			}
			if diff := compareCanon(reference, got); diff != "" {
				findings = append(findings, addF(c, "scan", rowID, "value_distortion", reference.String(), got.String(), diff))
				markFlag(scanFlagged, c.name, rowID)
			}
		}
	}

	// Insert direction: the runner re-inserted every scanned row at
	// rowid+1000 through the generated :exec query. Compare the two bands
	// over the HTTP text channel.
	insertCells := 0
	for index := range cases {
		c := &cases[index]
		if c.category != "" || c.noInsert {
			continue
		}
		result := runnerResult.Cases["case_"+c.name]
		if result.ReadErr != "" {
			continue
		}
		if result.InsErr != "" {
			findings = append(findings, finding{
				Case: c.name, CHType: c.typ.sqlType(), Direction: "insert",
				RowID: -1, Class: "runtime_error", Detail: firstLineOf(result.InsErr),
			})
			continue
		}
		band0, err := ch.exec(t, fmt.Sprintf("SELECT v FROM c_%s WHERE rowid < 1000 ORDER BY rowid FORMAT TSV", c.name))
		if err != nil {
			t.Fatal(err)
		}
		band1, err := ch.exec(t, fmt.Sprintf("SELECT v FROM c_%s WHERE rowid >= 1000 ORDER BY rowid FORMAT TSV", c.name))
		if err != nil {
			t.Fatal(err)
		}
		rows0, rows1 := tsvRows(band0), tsvRows(band1)
		if len(rows0) != len(rows1) {
			findings = append(findings, finding{
				Case: c.name, CHType: c.typ.sqlType(), Direction: "insert", RowID: -1,
				Class: "insert_distortion", Reference: fmt.Sprintf("%d rows", len(rows0)),
				Got: fmt.Sprintf("%d rows", len(rows1)), Detail: "reinserted row count differs",
			})
			continue
		}
		for rowID := range rows0 {
			insertCells++
			reference, err := parseCHText(rows0[rowID][0], c.typ, false)
			if err != nil {
				t.Fatalf("case %s: %v", c.name, err)
			}
			got, err := parseCHText(rows1[rowID][0], c.typ, false)
			if err != nil {
				t.Fatalf("case %s reinserted row %d: %v", c.name, rowID, err)
			}
			if diff := compareCanon(reference, got); diff != "" {
				entry := addF(c, "insert", rowID, "insert_distortion", reference.String(), got.String(), diff)
				entry.Inherited = scanFlagged[c.name][rowID]
				findings = append(findings, entry)
			}
		}
	}

	// Probes.
	for _, p := range probes {
		result, ok := runnerResult.Cases[p.name]
		if !ok {
			t.Fatalf("runner has no result for probe %s", p.name)
		}
		if result.ReadErr != "" {
			findings = append(findings, finding{
				Case: p.name, CHType: p.typ.sqlType(), Direction: "probe",
				RowID: -1, Class: "runtime_error", Detail: firstLineOf(result.ReadErr) + " | " + p.note,
			})
			continue
		}
		refBody, err := ch.exec(t, p.refSQL+" FORMAT TSV")
		if err != nil {
			t.Fatalf("probe reference %s: %v", p.name, err)
		}
		refRows := tsvRows(refBody)
		if len(refRows) != len(result.Rows) {
			findings = append(findings, finding{
				Case: p.name, CHType: p.typ.sqlType(), Direction: "probe", RowID: -1,
				Class: "value_distortion", Reference: fmt.Sprintf("%d rows", len(refRows)),
				Got: fmt.Sprintf("%d rows", len(result.Rows)), Detail: "row count differs | " + p.note,
			})
			continue
		}
		for rowID := range refRows {
			reference, err := parseCHText(refRows[rowID][p.col], p.typ, false)
			if err != nil {
				t.Fatalf("probe %s: parse reference %q: %v", p.name, refRows[rowID][p.col], err)
			}
			got, err := goDumpToCanon(result.Rows[rowID][p.col], p.typ)
			if err != nil {
				findings = append(findings, finding{
					Case: p.name, CHType: p.typ.sqlType(), Direction: "probe", RowID: rowID,
					Class: "value_distortion", Reference: reference.String(),
					Got: fmt.Sprintf("%v", result.Rows[rowID][p.col]), Detail: err.Error() + " | " + p.note,
				})
				continue
			}
			if diff := compareCanon(reference, got); diff != "" {
				findings = append(findings, finding{
					Case: p.name, CHType: p.typ.sqlType(), Direction: "probe", RowID: rowID,
					Class: "value_distortion", Reference: reference.String(), Got: got.String(),
					Detail: diff + " | " + p.note,
				})
			}
		}
	}

	// Record the non-active cases.
	active := 0
	for _, c := range cases {
		if c.category == "" {
			active++
			categories["active"]++
			continue
		}
		categories[c.category]++
		findings = append(findings, finding{
			Case: c.name, CHType: c.typ.sqlType(), Direction: "generation", RowID: -1,
			Class: c.category, Detail: c.note,
		})
	}

	// Canary self-check. Canary A is a regression pin since the
	// decimal.Decimal mapping: a finding on it in any direction means
	// the Decimal path is broken again. Canary B stays a mandatory
	// finding; an oracle that misses it is blind and must not report.
	if hasFinding(findings, "canary_decimal", "scan") || hasFinding(findings, "canary_decimal", "insert") {
		t.Fatalf("REGRESSION: the Decimal(10,2) canary produced a finding; the decimal.Decimal mapping is broken")
	}
	if hasFinding(findings, "canary_ip_mapped", "scan") || hasFinding(findings, "canary_ip_mapped", "insert") {
		t.Fatalf("REGRESSION: the IPv4-mapped IPv6 array canary produced a finding; the net.IP mapping is broken")
	}
	// These two check EVERY direction, not only scan and insert. A lost
	// mapping does not show up as a scan finding: it makes the case a
	// "generation" refusal instead, and a check that looks only at scan
	// and insert would pass while the mapping is gone. Both canaries were
	// verified to fail with their fix reverted.
	if hasAnyFinding(findings, "canary_uuid_container") {
		t.Fatalf("REGRESSION: the UUID container canary produced a finding; the uuid.UUID mapping is broken")
	}
	if hasAnyFinding(findings, "canary_enum_negative") {
		t.Fatalf("REGRESSION: the negative-numbered Enum canary produced a finding; the Enum mapping is broken")
	}
	// The oracle must still be able to record a refusal. If this witness
	// stops being a refusal, chgen grew a Tuple mapping and the witness
	// needs to be replaced by another unsupported type, not deleted.
	if !hasFinding(findings, "unsupported_tuple", "generation") {
		t.Fatalf("BLIND ORACLE: the unsupported-type witness did not record a refusal")
	}
	for _, caseName := range []string{
		"bare_int128", "bare_uint128", "bare_int256", "bare_uint256",
		"null_int128", "null_uint128", "null_int256", "null_uint256",
		"arr_int128", "arr_uint128", "arr_int256", "arr_uint256",
		"arrnull_int128", "arrnull_uint128", "arrnull_int256", "arrnull_uint256",
		"mapval_int128", "mapval_uint128", "mapval_int256", "mapval_uint256",
		"arr_arr_int128",
	} {
		if !hasFinding(findings, caseName, "generation") {
			t.Fatalf("BLIND ORACLE: %s did not record a wide integer refusal", caseName)
		}
	}
	if !hasFinding(findings, "canary_null_zero", "probe") {
		t.Fatalf("BLIND ORACLE: the NULL-into-non-pointer canary was not flagged")
	}
	// Self-defence for the type-identity check, mirroring the
	// round_trip_checked guard in the type-oracle: a check that examines
	// zero columns is indistinguishable from a check that never ran. Every
	// active case creates exactly one real table, thus a non-empty active
	// population must always produce a positive count here; zero on a
	// non-empty run means the type-header read did not fire, not that
	// there was nothing to check.
	if active > 0 && typesChecked == 0 {
		t.Fatalf("types_checked is 0 although the run had %d active cases; "+
			"the type-identity check did not read a single column type header", active)
	}

	classes := map[string]int{}
	for _, entry := range findings {
		classes[entry.Class]++
	}
	sort.Slice(findings, func(a, b int) bool {
		if findings[a].Case != findings[b].Case {
			return findings[a].Case < findings[b].Case
		}
		return findings[a].RowID < findings[b].RowID
	})
	artifact := oracleArtifact{
		Date:           time.Now().UTC().Format(time.RFC3339),
		CHVersion:      version,
		DriverVersion:  execOracleDriverVersion,
		RunDatabase:    runDatabase,
		Seed:           seed,
		RandomTypes:    randN,
		Cases:          len(cases),
		ActiveCases:    active,
		ScanCells:      scanCells,
		InsertCells:    insertCells,
		TypesChecked:   typesChecked,
		Categories:     categories,
		FindingClasses: classes,
		Findings:       findings,
		Conformance:    conformanceReport,
	}
	if out := os.Getenv("CHGEN_EXEC_OUT"); out != "" {
		blob, err := json.MarshalIndent(artifact, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, append(blob, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("exec oracle: %d cases (%d active), %d scan cells, %d insert cells, %d types checked, findings by class: %v",
		len(cases), active, scanCells, insertCells, typesChecked, classes)
	for _, entry := range findings {
		if entry.Class == "value_distortion" || entry.Class == "insert_distortion" || strings.HasPrefix(entry.Class, "runtime_error") {
			t.Logf("%s %s %s row %d: reference=%s got=%s :: %s", entry.Class, entry.Case, entry.CHType, entry.RowID, entry.Reference, entry.Got, entry.Detail)
		}
	}
}

func execOracleSeedSource(seed int64, cases []xcase) string {
	var source strings.Builder
	fmt.Fprintf(&source, "seed=%d\n", seed)
	for _, c := range cases {
		fmt.Fprintf(&source, "%s\x00%s\n", c.name, strings.Join(c.rows, "\x00"))
	}
	return source.String()
}

func addF(c *xcase, direction string, rowID int, class, reference, got, detail string) finding {
	return finding{Case: c.name, CHType: c.typ.sqlType(), Direction: direction, RowID: rowID, Class: class, Reference: reference, Got: got, Detail: detail}
}

func markFlag(flags map[string]map[int]bool, name string, rowID int) {
	if flags[name] == nil {
		flags[name] = map[int]bool{}
	}
	flags[name][rowID] = true
}

func hasFinding(findings []finding, name, direction string) bool {
	for _, entry := range findings {
		if entry.Case == name && entry.Direction == direction {
			return true
		}
	}
	return false
}

// hasAnyFinding reports a finding on the case in ANY direction, including a
// "generation" refusal. A canary that guards a type mapping must use this:
// when the mapping is removed, the case is refused at generation time and
// never reaches the scan or the insert direction at all.
func hasAnyFinding(findings []finding, name string) bool {
	for _, entry := range findings {
		if entry.Case == name {
			return true
		}
	}
	return false
}

func firstLineOf(s string) string {
	if index := strings.IndexByte(s, '\n'); index >= 0 {
		return s[:index]
	}
	return s
}

func nativeValueFromDateTime64Error(detail string) string {
	const prefix = "the scanned DateTime64 "
	start := strings.Index(detail, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(detail[start:], " is before ")
	if end < 0 {
		return ""
	}
	return detail[start : start+end]
}

func goCaseName(name string) string {
	parts := strings.Split(name, "_")
	var out strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		out.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return out.String()
}

func buildProbes() []probe {
	probes := []probe{
		{
			name: "probe_explicit_time_param_nullable_source",
			chgen: "-- name: ListSelectedRows :many\n-- param: Cutoff time.Time\n" +
				"SELECT rowid, available_at AS value FROM (SELECT rowid, argMax(v, rowid) AS available_at FROM c_null_datetime64_3 GROUP BY rowid) AS current_state " +
				"WHERE available_at IS NULL OR available_at <= chgen.arg('Cutoff') ORDER BY rowid LIMIT chgen.arg('Limit');",
			call: "func(ctx context.Context) (any, error) { return q.ListSelectedRows(ctx, gen.ListSelectedRowsParams{" +
				"Cutoff: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC), Limit: 1000}) }",
			refSQL: "SELECT rowid, available_at AS value FROM (SELECT rowid, argMax(v, rowid) AS available_at FROM c_null_datetime64_3 GROUP BY rowid) AS current_state " +
				"WHERE (available_at IS NULL OR available_at <= toDateTime64('2024-01-03 00:00:00', 3, 'UTC')) AND rowid < 1000 ORDER BY rowid LIMIT 1000",
			typ: nullableT(dt64T(3)), col: 1, ncols: 2,
			note: "generated time.Time binding through a nested nullable DateTime64 predicate",
		},
		{
			name:   "probe_bare_null_cast",
			chgen:  "-- name: BareNullCast :many\nSELECT rowid, CAST(NULL, 'Nullable(Float64)') AS value FROM c_bare_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.BareNullCast(ctx, gen.BareNullCastParams{}) }",
			refSQL: "SELECT rowid, CAST(NULL, 'Nullable(Float64)') AS value FROM c_bare_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    nullableT(scalar("float64")), col: 1, ncols: 2,
			note: "generated pointer value lane for an unquoted NULL cast",
		},
		{
			name:   "probe_bare_null_predicate",
			chgen:  "-- name: BareNullPredicate :many\nSELECT rowid, NULL IS NULL AS value FROM c_bare_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.BareNullPredicate(ctx, gen.BareNullPredicateParams{}) }",
			refSQL: "SELECT rowid, NULL IS NULL AS value FROM c_bare_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("uint8"), col: 1, ncols: 2,
			note: "generated UInt8 value lane for an unquoted NULL predicate",
		},
		{
			name:   "probe_is_null_predicate",
			chgen:  "-- name: IsNullPredicate :many\nSELECT rowid, v IS NULL AS value FROM c_null_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.IsNullPredicate(ctx, gen.IsNullPredicateParams{}) }",
			refSQL: "SELECT rowid, v IS NULL AS value FROM c_null_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("uint8"), col: 1, ncols: 2,
			note: "generated UInt8 value lane for IS NULL over a real Nullable column",
		},
		{
			name:   "probe_to_yyyymmdd_cast",
			chgen:  "-- name: ToYYYYMMDDCast :many\nSELECT rowid, CAST(toYYYYMMDD(v), 'UInt32') AS value FROM c_bare_datetime64_3 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.ToYYYYMMDDCast(ctx, gen.ToYYYYMMDDCastParams{}) }",
			refSQL: "SELECT rowid, CAST(toYYYYMMDD(v), 'UInt32') AS value FROM c_bare_datetime64_3 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("uint32"), col: 1, ncols: 2,
			note: "generated UInt32 value lane for toYYYYMMDD inside CAST",
		},
		{
			name:   "canary_null_zero",
			chgen:  "-- name: CanaryNullZero :many\n-- result: V v int32\nSELECT rowid, v FROM canary_null ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.CanaryNullZero(ctx, gen.CanaryNullZeroParams{}) }",
			refSQL: "SELECT rowid, v FROM canary_null WHERE rowid < 1000 ORDER BY rowid",
			typ:    nullableT(scalar("int32")), col: 1, ncols: 2,
			note: "canary B: a NULL scanned into a non-pointer int32 becomes 0 without a Scan error",
		},
		{
			name:   "probe_find_window",
			chgen:  "-- name: FindWindow :many\nSELECT rowid FROM c_bare_int64 WHERE v >= chgen.arg('Lo') AND v <= chgen.arg('Hi') ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.FindWindow(ctx, gen.FindWindowParams{Lo: -1, Hi: 9223372036854775807}) }",
			refSQL: "SELECT rowid FROM c_bare_int64 WHERE v >= -1 AND v <= 9223372036854775807 AND rowid < 1000 ORDER BY rowid",
			typ:    scalar("uint32"), col: 0, ncols: 1,
			note: "two-parameter binding order",
		},
		{
			name:   "probe_find_repeat",
			chgen:  "-- name: FindRepeat :many\nSELECT rowid FROM c_bare_string WHERE (chgen.arg('Pattern') = '' OR v = chgen.arg('Pattern')) ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.FindRepeat(ctx, gen.FindRepeatParams{Pattern: \"abc\"}) }",
			refSQL: "SELECT rowid FROM c_bare_string WHERE ('abc' = '' OR v = 'abc') AND rowid < 1000 ORDER BY rowid",
			typ:    scalar("uint32"), col: 0, ncols: 1,
			note: "one named argument bound at two positions",
		},
		{
			name:   "probe_median_decimal",
			chgen:  "-- name: MedianDecimal :one\nSELECT median(v) AS m FROM c_bare_decimal_18_4;",
			call:   "func(ctx context.Context) (any, error) { r, err := q.MedianDecimal(ctx, gen.MedianDecimalParams{}); return []gen.MedianDecimalRow{r}, err }",
			refSQL: "SELECT median(v) AS m FROM c_bare_decimal_18_4 WHERE rowid < 1000",
			typ:    decT(18, 4), col: 0, ncols: 1,
			note: "known type-oracle mismatch M2: former type-oracle mismatch M2: chgen now types median(Decimal) as the input Decimal type; this probe pins the runtime fix",
		},
		{
			name:   "probe_fixedstring_concat",
			chgen:  "-- name: FsConcat :many\nSELECT (v || 'xyz') AS c FROM c_bare_fixedstring_8 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.FsConcat(ctx, gen.FsConcatParams{}) }",
			refSQL: "SELECT (v || 'xyz') AS c FROM c_bare_fixedstring_8 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("string"), col: 0, ncols: 1,
			note: "known type-oracle mismatch M5: chgen types FixedString || String as FixedString(8); both map to Go string",
		},
		{
			name:   "probe_array_sum_decimal256",
			chgen:  "-- name: ArraySumDecimal256 :many\nSELECT rowid, arraySum(x -> toDecimal256(x, 7), v) AS total FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.ArraySumDecimal256(ctx, gen.ArraySumDecimal256Params{}) }",
			refSQL: "SELECT rowid, arraySum(x -> toDecimal256(x, 7), v) AS total FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    decT(76, 7), col: 1, ncols: 2,
			note: "arraySum keeps a Decimal256 lambda result and generated code scans it as decimal.Decimal",
		},
		{
			name:   "probe_hof_sum_int64",
			chgen:  "-- name: HOFSumInt64 :many\nSELECT rowid, arraySum(x -> x, v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFSumInt64(ctx, gen.HOFSumInt64Params{}) }",
			refSQL: "SELECT rowid, arraySum(x -> x, v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("int64"), col: 1, ncols: 2,
			note: "higher-order promoted scalar sum result",
		},
		{
			name:   "probe_hof_min",
			chgen:  "-- name: HOFMin :many\nSELECT rowid, arrayMin(x -> x, v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFMin(ctx, gen.HOFMinParams{}) }",
			refSQL: "SELECT rowid, arrayMin(x -> x, v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("int32"), col: 1, ncols: 2,
			note: "higher-order lambda body result",
		},
		{
			name:   "probe_hof_predicate",
			chgen:  "-- name: HOFPredicate :many\nSELECT rowid, arrayExists(x -> x > 0, v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFPredicate(ctx, gen.HOFPredicateParams{}) }",
			refSQL: "SELECT rowid, arrayExists(x -> x > 0, v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("uint8"), col: 1, ncols: 2,
			note: "higher-order predicate result",
		},
		{
			name:   "probe_hof_first",
			chgen:  "-- name: HOFFirst :many\nSELECT rowid, arrayFirst(x -> x > 0, v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFFirst(ctx, gen.HOFFirstParams{}) }",
			refSQL: "SELECT rowid, arrayFirst(x -> x > 0, v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("int32"), col: 1, ncols: 2,
			note: "higher-order element and default result",
		},
		{
			name:   "probe_hof_avg",
			chgen:  "-- name: HOFAvg :many\nSELECT rowid, arrayAvg(v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFAvg(ctx, gen.HOFAvgParams{}) }",
			refSQL: "SELECT rowid, arrayAvg(v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("float64"), col: 1, ncols: 2,
			note: "higher-order Float64 reduction result",
		},
		{
			name:   "probe_hof_cumsum",
			chgen:  "-- name: HOFCumSum :many\nSELECT rowid, arrayCumSum(v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFCumSum(ctx, gen.HOFCumSumParams{}) }",
			refSQL: "SELECT rowid, arrayCumSum(v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    arrayT(scalar("int64")), col: 1, ncols: 2,
			note: "higher-order promoted cumulative Array result",
		},
		{
			name:   "probe_hof_first_or_null",
			chgen:  "-- name: HOFFirstOrNull :many\nSELECT rowid, arrayFirstOrNull(x -> x > 0, v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFFirstOrNull(ctx, gen.HOFFirstOrNullParams{}) }",
			refSQL: "SELECT rowid, arrayFirstOrNull(x -> x > 0, v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    nullableT(scalar("int32")), col: 1, ncols: 2,
			note: "higher-order nullable element result",
		},
		{
			name:   "probe_hof_first_index",
			chgen:  "-- name: HOFFirstIndex :many\nSELECT rowid, arrayFirstIndex(x -> x > 0, v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFFirstIndex(ctx, gen.HOFFirstIndexParams{}) }",
			refSQL: "SELECT rowid, arrayFirstIndex(x -> x > 0, v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("uint32"), col: 1, ncols: 2,
			note: "higher-order index result",
		},
		{
			name:   "probe_hof_fill",
			chgen:  "-- name: HOFFill :many\nSELECT rowid, arrayFill(x -> x > 0, v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFFill(ctx, gen.HOFFillParams{}) }",
			refSQL: "SELECT rowid, arrayFill(x -> x > 0, v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    arrayT(scalar("int32")), col: 1, ncols: 2,
			note: "higher-order preserved Array result",
		},
		{
			name:   "probe_hof_split",
			chgen:  "-- name: HOFSplit :many\nSELECT rowid, arraySplit(x -> x > 0, v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFSplit(ctx, gen.HOFSplitParams{}) }",
			refSQL: "SELECT rowid, arraySplit(x -> x > 0, v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    arrayT(arrayT(scalar("int32"))), col: 1, ncols: 2,
			note: "higher-order nested Array result",
		},
		{
			name:   "probe_hof_fold_bool",
			chgen:  "-- name: HOFFoldBool :many\nSELECT rowid, arrayFold((acc, x) -> toUInt8(x), v, false) AS value FROM c_arr_bool ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFFoldBool(ctx, gen.HOFFoldBoolParams{}) }",
			refSQL: "SELECT rowid, arrayFold((acc, x) -> toUInt8(x), v, false) AS value FROM c_arr_bool WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("bool"), col: 1, ncols: 2,
			note: "higher-order Fold keeps the Bool accumulator across the UInt8 alias boundary",
		},
		{
			name:   "probe_hof_multi_array",
			chgen:  "-- name: HOFMultiArray :many\nSELECT rowid, arrayMap((x, y) -> x + y, v, v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFMultiArray(ctx, gen.HOFMultiArrayParams{}) }",
			refSQL: "SELECT rowid, arrayMap((x, y) -> x + y, v, v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    arrayT(scalar("int64")), col: 1, ncols: 2,
			note: "higher-order linked arrays use one equal-length source",
		},
		{
			name:   "probe_hof_partial_sort",
			chgen:  "-- name: HOFPartialSort :many\nSELECT rowid, arrayPartialReverseSort(toInt32(rowid % 3), v) AS value FROM c_arr_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.HOFPartialSort(ctx, gen.HOFPartialSortParams{}) }",
			refSQL: "SELECT rowid, arrayPartialReverseSort(toInt32(rowid % 3), v) AS value FROM c_arr_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    arrayT(scalar("int32")), col: 1, ncols: 2,
			note: "higher-order partial sort reads a runtime integer limit before the Array",
		},
		{
			name:   "probe_window_row_number",
			chgen:  "-- name: WindowRowNumber :many\nSELECT rowid, row_number() OVER (ORDER BY rowid) AS value FROM c_bare_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.WindowRowNumber(ctx, gen.WindowRowNumberParams{}) }",
			refSQL: "SELECT rowid, row_number() OVER (ORDER BY rowid) AS value FROM c_bare_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("uint64"), col: 1, ncols: 2,
			note: "generated window value lane for fixed UInt64 results",
		},
		{
			name:   "probe_window_lag_in_frame",
			chgen:  "-- name: WindowLagInFrame :many\nSELECT rowid, lagInFrame(v, 1) OVER (ORDER BY rowid ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS value FROM c_bare_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.WindowLagInFrame(ctx, gen.WindowLagInFrameParams{}) }",
			refSQL: "SELECT rowid, lagInFrame(v, 1) OVER (ORDER BY rowid ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS value FROM c_bare_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("int32"), col: 1, ncols: 2,
			note: "generated window value lane for first-argument propagation",
		},
		{
			name:   "probe_window_sum_rows",
			chgen:  "-- name: WindowSumRows :many\nSELECT rowid, sum(v) OVER (ORDER BY rowid ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS value FROM c_bare_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.WindowSumRows(ctx, gen.WindowSumRowsParams{}) }",
			refSQL: "SELECT rowid, sum(v) OVER (ORDER BY rowid ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS value FROM c_bare_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("int64"), col: 1, ncols: 2,
			note: "generated window value lane for aggregate-as-window results",
		},
		{
			name:   "probe_window_lag_frame_difference",
			chgen:  "-- name: WindowLagFrameDifference :many\nSELECT rowid, lag(v, 1) OVER (ORDER BY rowid) AS standard_value, lagInFrame(v, 1) OVER (ORDER BY rowid ROWS BETWEEN CURRENT ROW AND CURRENT ROW) AS frame_value FROM c_bare_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.WindowLagFrameDifference(ctx, gen.WindowLagFrameDifferenceParams{}) }",
			refSQL: "SELECT rowid, lag(v, 1) OVER (ORDER BY rowid) AS standard_value, lagInFrame(v, 1) OVER (ORDER BY rowid ROWS BETWEEN CURRENT ROW AND CURRENT ROW) AS frame_value FROM c_bare_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("int32"), col: 2, ncols: 3,
			note: "generated value lane distinguishes lag from lagInFrame on a current-row frame",
		},
		{
			name:   "probe_window_lead_frame_difference",
			chgen:  "-- name: WindowLeadFrameDifference :many\nSELECT rowid, lead(v, 1) OVER (ORDER BY rowid) AS standard_value, leadInFrame(v, 1) OVER (ORDER BY rowid ROWS BETWEEN CURRENT ROW AND CURRENT ROW) AS frame_value FROM c_bare_int32 ORDER BY rowid;",
			call:   "func(ctx context.Context) (any, error) { return q.WindowLeadFrameDifference(ctx, gen.WindowLeadFrameDifferenceParams{}) }",
			refSQL: "SELECT rowid, lead(v, 1) OVER (ORDER BY rowid) AS standard_value, leadInFrame(v, 1) OVER (ORDER BY rowid ROWS BETWEEN CURRENT ROW AND CURRENT ROW) AS frame_value FROM c_bare_int32 WHERE rowid < 1000 ORDER BY rowid",
			typ:    scalar("int32"), col: 2, ncols: 3,
			note: "generated value lane distinguishes lead from leadInFrame on a current-row frame",
		},
	}
	probes = append(probes, buildAggregateCombinatorValueProbes()...)
	probes = append(probes, buildNestedScopeValueProbes()...)
	probes = append(probes, buildSetOperationValueProbes()...)
	probes = append(probes, buildFinalValueProbes()...)
	return append(probes, buildArrayJoinValueProbes()...)
}

func TestWindowMatrixExecProbeLinks(t *testing.T) {
	known := make(map[string]bool)
	for _, value := range buildProbes() {
		known[value.name] = true
	}
	for cellID, probeID := range currentWindowMatrix().ExecProbeLinks {
		if !known[probeID] {
			t.Errorf("window matrix cell %s links missing exec probe %s", cellID, probeID)
		}
	}
}

func TestHigherOrderMatrixExecProbeLinks(t *testing.T) {
	known := make(map[string]bool)
	for _, value := range buildProbes() {
		known[value.name] = true
	}
	for cellID, probeID := range buildHigherOrderMatrix().ExecProbeLinks {
		if !known[probeID] {
			t.Errorf("higher-order matrix cell %s links missing exec probe %s", cellID, probeID)
		}
	}
}

type aggregateCombinatorValueProbeSpec struct {
	primitives []aggregatePrimitive
	probe      probe
}

func aggregateCombinatorValueProbeSpecs() []aggregateCombinatorValueProbeSpec {
	rowCall := func(query string) string {
		return "func(ctx context.Context) (any, error) { r, err := q." + query + "(ctx, gen." + query + "Params{}); return []gen." + query + "Row{r}, err }"
	}
	return []aggregateCombinatorValueProbeSpec{
		{
			primitives: []aggregatePrimitive{aggregatePrimitiveIf},
			probe: probe{
				name: "probe_aggregate_if", chgen: "-- name: AggregateIf :one\nSELECT sumIf(v, v > 0) AS value FROM c_bare_int32;",
				call: rowCall("AggregateIf"), refSQL: "SELECT sumIf(v, v > 0) AS value FROM c_bare_int32 WHERE rowid < 1000",
				typ: scalar("int64"), col: 0, ncols: 1, note: "generated value lane for the If primitive",
			},
		},
		{
			primitives: []aggregatePrimitive{aggregatePrimitiveArray},
			probe: probe{
				name: "probe_aggregate_array", chgen: "-- name: AggregateArray :one\nSELECT sumArray(v) AS value FROM c_arr_int32;",
				call: rowCall("AggregateArray"), refSQL: "SELECT sumArray(v) AS value FROM c_arr_int32 WHERE rowid < 1000",
				typ: scalar("int64"), col: 0, ncols: 1, note: "generated value lane for the Array primitive",
			},
		},
		{
			primitives: []aggregatePrimitive{aggregatePrimitiveOrNull},
			probe: probe{
				name: "probe_aggregate_or_null", chgen: "-- name: AggregateOrNull :one\nSELECT sumOrNull(v) AS value FROM c_bare_int32;",
				call: rowCall("AggregateOrNull"), refSQL: "SELECT sumOrNull(v) AS value FROM c_bare_int32 WHERE rowid < 1000",
				typ: nullableT(scalar("int64")), col: 0, ncols: 1, note: "generated value lane for the OrNull primitive",
			},
		},
		{
			primitives: []aggregatePrimitive{aggregatePrimitiveOrDefault},
			probe: probe{
				name: "probe_aggregate_or_default", chgen: "-- name: AggregateOrDefault :one\nSELECT sumOrDefault(v) AS value FROM c_bare_int32;",
				call: rowCall("AggregateOrDefault"), refSQL: "SELECT sumOrDefault(v) AS value FROM c_bare_int32 WHERE rowid < 1000",
				typ: scalar("int64"), col: 0, ncols: 1, note: "generated value lane for the OrDefault primitive",
			},
		},
		{
			primitives: []aggregatePrimitive{aggregatePrimitiveResample},
			probe: probe{
				name: "probe_aggregate_resample", chgen: "-- name: AggregateResample :one\nSELECT sumResample(0, 4, 1)(v, toUInt32(rowid % 4)) AS value FROM c_bare_int32;",
				call: rowCall("AggregateResample"), refSQL: "SELECT sumResample(0, 4, 1)(v, toUInt32(rowid % 4)) AS value FROM c_bare_int32 WHERE rowid < 1000",
				typ: arrayT(scalar("int64")), col: 0, ncols: 1, note: "generated value lane for the Resample primitive",
			},
		},
		{
			primitives: []aggregatePrimitive{aggregatePrimitiveSimpleState},
			probe: probe{
				name: "probe_aggregate_simple_state", chgen: "-- name: AggregateSimpleState :one\nSELECT sumSimpleState(v) AS value FROM c_bare_int32;",
				call: rowCall("AggregateSimpleState"), refSQL: "SELECT sumSimpleState(v) AS value FROM c_bare_int32 WHERE rowid < 1000",
				typ: scalar("int64"), col: 0, ncols: 1, note: "generated value lane for the SimpleState primitive",
			},
		},
		{
			primitives: []aggregatePrimitive{aggregatePrimitiveState, aggregatePrimitiveMerge},
			probe: probe{
				name: "probe_aggregate_state_merge", chgen: "-- name: AggregateStateMerge :one\nSELECT sumMerge(s) AS value FROM (SELECT sumState(v) AS s FROM c_bare_int32);",
				call: rowCall("AggregateStateMerge"), refSQL: "SELECT sumMerge(s) AS value FROM (SELECT sumState(v) AS s FROM c_bare_int32 WHERE rowid < 1000)",
				typ: scalar("int64"), col: 0, ncols: 1, note: "generated value lane for the State and Merge primitives",
			},
		},
	}
}

func buildAggregateCombinatorValueProbes() []probe {
	specs := aggregateCombinatorValueProbeSpecs()
	result := make([]probe, 0, len(specs))
	for _, spec := range specs {
		result = append(result, spec.probe)
	}
	return result
}

func TestAggregateCombinatorValueProbesCoverSupportedPrimitives(t *testing.T) {
	supported := map[aggregatePrimitive]bool{}
	for _, chain := range measuredAggregatePrimitiveChains {
		for _, primitive := range chain {
			supported[primitive] = true
		}
	}
	covered := map[aggregatePrimitive]bool{}
	for _, spec := range aggregateCombinatorValueProbeSpecs() {
		if spec.probe.name == "" || spec.probe.chgen == "" || spec.probe.refSQL == "" {
			t.Fatal("an aggregate combinator value probe is incomplete")
		}
		for _, primitive := range spec.primitives {
			covered[primitive] = true
		}
	}
	for primitive := range supported {
		if !covered[primitive] {
			t.Errorf("supported primitive %s has no generated value probe", aggregatePrimitiveSpelling(primitive))
		}
	}
}
