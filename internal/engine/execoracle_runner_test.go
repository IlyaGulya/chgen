//go:build execoracle

package engine

// Runner-module writer for the execution oracle. The runner is a standalone
// Go program compiled once per oracle run. It contains the chgen-generated
// package and drives every generated query with the real clickhouse-go
// driver: first every read, then every probe, then every re-insert through
// the generated :exec queries. The output is one JSON document on stdout.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const execOracleRunnerFixture = "internal/engine/testdata/execoraclerunner"

func writeRunnerModule(runnerDir string, cases []xcase, probes []probe) error {
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(moduleRootPath(execOracleRunnerFixture, name))
		if err != nil {
			return err
		}
		if name == "go.mod" {
			data = []byte(strings.Replace(string(data), "module github.com/IlyaGulya/chgen/internal/engine/testdata/execoraclerunner", "module execrunner", 1))
		}
		if err := os.WriteFile(filepath.Join(runnerDir, name), data, 0o644); err != nil {
			return err
		}
	}
	var registrations strings.Builder
	for index := range cases {
		c := &cases[index]
		if c.category != "" {
			continue
		}
		goName := goCaseName(c.name)
		insert := "nil"
		if !c.noInsert {
			insert = "q.Ins" + goName
		}
		fmt.Fprintf(&registrations,
			"\tregister(%q, %d, func(ctx context.Context) (any, error) { return q.Read%s(ctx, gen.Read%sParams{}) }, func(ctx context.Context, rowid uint32) (any, error) { return q.Read%sAt(ctx, gen.Read%sAtParams{Rowid: rowid}) }, %s)\n",
			"case_"+c.name, len(c.rows), goName, goName, goName, goName, insert)
	}
	for _, p := range probes {
		fmt.Fprintf(&registrations, "\tregister(%q, 0, %s, nil, nil)\n", p.name, p.call)
	}
	main := strings.Replace(runnerMainTemplate, "//REGISTRATIONS", registrations.String(), 1)
	return os.WriteFile(filepath.Join(runnerDir, "main.go"), []byte(main), 0o644)
}

func TestExecOracleRunnerModuleContract(t *testing.T) {
	data, err := os.ReadFile(moduleRootPath(execOracleRunnerFixture, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	requirements := moduleRequirements(string(data))
	if got := "github.com/ClickHouse/clickhouse-go/v2 " + requirements["github.com/ClickHouse/clickhouse-go/v2"]; got != execOracleDriverVersion {
		t.Fatalf("the execution runner driver is %q, but the report identity is %q", got, execOracleDriverVersion)
	}
	for module, version := range map[string]string{
		"github.com/google/uuid":        "v1.6.0",
		"github.com/shopspring/decimal": "v1.4.0",
	} {
		if got := requirements[module]; got != version {
			t.Fatalf("the execution runner requires %s %s, want %s", module, got, version)
		}
	}
}

func TestExecOracleRunnerBuildsWithoutServer(t *testing.T) {
	runnerDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(runnerDir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "package gen\n\ntype Queries struct{}\n\nfunc New(any) *Queries { return &Queries{} }\n"
	if err := os.WriteFile(filepath.Join(runnerDir, "gen", "queries.sql.go"), []byte(stub), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeRunnerModule(runnerDir, nil, nil); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-mod=readonly", ".")
	command.Dir = runnerDir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build the execution runner: %v\n%s", err, output)
	}
}

func moduleRequirements(goMod string) map[string]string {
	requirements := make(map[string]string)
	for _, line := range strings.Split(goMod, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.Contains(fields[0], ".") && strings.HasPrefix(fields[1], "v") {
			requirements[fields[0]] = fields[1]
		}
	}
	return requirements
}

const runnerMainTemplate = `package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net"
	"os"
	"reflect"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"execrunner/gen"
)

type caseResult struct {
	ReadErr  string         ` + "`json:\"readErr\"`" + `
	CellErrs map[int]string ` + "`json:\"cellErrs,omitempty\"`" + `
	Rows     [][]any        ` + "`json:\"rows\"`" + `
	InsErr   string         ` + "`json:\"insErr\"`" + `
}

type registration struct {
	name string
	rowCount int
	read func(ctx context.Context) (any, error)
	readAt func(ctx context.Context, rowid uint32) (any, error)
	ins  any
	rows any
}

var registrations []registration

var q *gen.Queries

func register(name string, rowCount int, read func(ctx context.Context) (any, error), readAt func(ctx context.Context, rowid uint32) (any, error), ins any) {
	registrations = append(registrations, registration{name: name, rowCount: rowCount, read: read, readAt: readAt, ins: ins})
}

func main() {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{os.Getenv("CH_NATIVE")},
		// CH_DATABASE is the private database of the run. The oracle sets
		// it. The generated queries name the tables without a prefix,
		// thus the bound database decides which tables the runner uses.
		Auth: clickhouse.Auth{Database: os.Getenv("CH_DATABASE"), Username: "default", Password: os.Getenv("CH_PASSWORD")},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	q = gen.New(conn)
	ctx := context.Background()
//REGISTRATIONS

	results := map[string]*caseResult{}
	// Phase 1: every read, probes included, before any insert, so a probe
	// that scans a whole table never sees the re-inserted band.
	for index := range registrations {
		r := &registrations[index]
		result := &caseResult{}
		results[r.name] = result
		rows, err := r.read(ctx)
		if err != nil {
			result.ReadErr = err.Error()
			if r.readAt != nil {
				result.CellErrs = map[int]string{}
				for rowid := 0; rowid < r.rowCount; rowid++ {
					if _, cellErr := r.readAt(ctx, uint32(rowid)); cellErr != nil {
						result.CellErrs[rowid] = cellErr.Error()
					}
				}
			}
			continue
		}
		r.rows = rows
		value := reflect.ValueOf(rows)
		for i := 0; i < value.Len(); i++ {
			result.Rows = append(result.Rows, dumpRow(value.Index(i)))
		}
		if result.Rows == nil {
			result.Rows = [][]any{}
		}
	}
	// Phase 2: re-insert every scanned row at rowid+1000 through the
	// generated :exec query, with the exact Go values that arrived.
	for index := range registrations {
		r := &registrations[index]
		if r.ins == nil || r.rows == nil {
			continue
		}
		if err := reinsert(ctx, r.ins, r.rows); err != nil {
			results[r.name].InsErr = err.Error()
		}
	}
	blob, err := json.Marshal(map[string]any{"cases": results})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Stdout.Write(blob)
}

func reinsert(ctx context.Context, insFn any, rows any) error {
	fn := reflect.ValueOf(insFn)
	paramsType := fn.Type().In(1)
	value := reflect.ValueOf(rows)
	for i := 0; i < value.Len(); i++ {
		row := value.Index(i)
		params := reflect.New(paramsType).Elem()
		params.FieldByName("Rowid").SetUint(row.FieldByName("Rowid").Uint() + 1000)
		params.FieldByName("V").Set(row.FieldByName("V"))
		out := fn.Call([]reflect.Value{reflect.ValueOf(ctx), params})
		if err, _ := out[0].Interface().(error); err != nil {
			return fmt.Errorf("row %d: %w", i, err)
		}
	}
	return nil
}

func dumpRow(row reflect.Value) []any {
	fields := make([]any, 0, row.NumField())
	for i := 0; i < row.NumField(); i++ {
		fields = append(fields, dump(row.Field(i)))
	}
	return fields
}

var timeType = reflect.TypeOf(time.Time{})

var decimalType = reflect.TypeOf(decimal.Decimal{})

var ipType = reflect.TypeOf(net.IP{})

var uuidType = reflect.TypeOf(uuid.UUID{})

var bigIntType = reflect.TypeOf(big.Int{})

func dump(value reflect.Value) any {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return map[string]any{"t": "null"}
		}
		return dump(value.Elem())
	}
	if value.Type() == timeType {
		when := value.Interface().(time.Time)
		return map[string]any{"t": "t", "sec": fmt.Sprint(when.Unix()), "nsec": when.Nanosecond()}
	}
	if value.Type() == decimalType {
		return map[string]any{"t": "d", "v": value.Interface().(decimal.Decimal).String()}
	}
	// net.IP is a byte slice. Dump the raw bytes, not the generic slice
	// form, so the comparison can tell an IPv4-mapped IPv6 address from a
	// plain IPv4 address. The text form cannot: net.IP.String() prints
	// ::ffff:1.2.3.4 as "1.2.3.4", which is the loss that R6 recorded.
	if value.Type() == ipType {
		return map[string]any{"t": "ip", "v": base64.StdEncoding.EncodeToString(value.Bytes())}
	}
	// uuid.UUID is a [16]byte array. Dump the raw bytes, not the text
	// form, so the comparison cannot be fooled by the dash and case
	// conventions of the text: the HTTP channel prints lower-case with
	// dashes, and a Go text form that differs there would still be the
	// same value. The bytes are the value.
	if value.Type() == uuidType {
		identifier := value.Interface().(uuid.UUID)
		return map[string]any{"t": "uuid", "v": base64.StdEncoding.EncodeToString(identifier[:])}
	}
	if value.Type() == bigIntType {
		integer := value.Interface().(big.Int)
		return map[string]any{"t": "i", "v": integer.String()}
	}
	switch value.Kind() {
	case reflect.Bool:
		return map[string]any{"t": "b", "v": value.Bool()}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"t": "i", "v": fmt.Sprint(value.Int())}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"t": "i", "v": fmt.Sprint(value.Uint())}
	case reflect.Float32:
		return map[string]any{"t": "f", "bits": fmt.Sprint(uint64(math.Float32bits(float32(value.Float())))), "w": 32}
	case reflect.Float64:
		return map[string]any{"t": "f", "bits": fmt.Sprint(math.Float64bits(value.Float())), "w": 64}
	case reflect.String:
		return map[string]any{"t": "s", "v": base64.StdEncoding.EncodeToString([]byte(value.String()))}
	case reflect.Slice, reflect.Array:
		items := make([]any, 0, value.Len())
		for i := 0; i < value.Len(); i++ {
			items = append(items, dump(value.Index(i)))
		}
		return map[string]any{"t": "a", "v": items}
	case reflect.Map:
		keys := make([]any, 0, value.Len())
		values := make([]any, 0, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			keys = append(keys, dump(iterator.Key()))
			values = append(values, dump(iterator.Value()))
		}
		return map[string]any{"t": "m", "k": keys, "v": values}
	}
	return map[string]any{"t": "unsupported", "v": value.Type().String()}
}
`
