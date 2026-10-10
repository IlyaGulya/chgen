package functionrules_test

import (
	"go/format"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/IlyaGulya/chgen"
)

func TestFunctionRulesGeneratedRuntime(t *testing.T) {
	if os.Getenv("CHGEN_CONTRACT_NATIVE") == "" {
		t.Skip("CHGEN_CONTRACT_NATIVE is not set")
	}
	directory := t.TempDir()
	schema := filepath.Join(directory, "schema.sql")
	sql := filepath.Join(directory, "queries.sql")
	for path, content := range map[string]string{
		schema: "CREATE TABLE t (id UInt8, v Nullable(Float64), d Decimal(9, 2)) ENGINE=Memory",
		sql:    "-- name: ReadMath :many\n-- result: CubeRoot cube_root\n-- result: Cos cos_value\n-- result: Sign sign_value\n-- result: DecimalSin decimal_sin\nSELECT cbrt(v) AS cube_root, cos(v) AS cos_value, sign(v) AS sign_value, sin(d) AS decimal_sin FROM t ORDER BY id",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalogs, err := chgen.ParseSchemaCatalogs([]string{schema})
	if err != nil {
		t.Fatal(err)
	}
	queries, err := chgen.ParseQueryFiles([]string{sql}, catalogs)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := chgen.Generate("contracts", queries)
	if err != nil {
		t.Fatal(err)
	}
	consumerSource, err := format.Source([]byte(runtimeConsumer))
	if err != nil {
		t.Fatal(err)
	}
	drivers := []string{"v2.42.0"}
	if version.Compare(runtime.Version(), "go1.25") >= 0 {
		drivers = append(drivers, "v2.47.0")
	}
	for _, driver := range drivers {
		t.Run(driver, func(t *testing.T) {
			consumer := t.TempDir()
			for name, content := range map[string][]byte{
				"go.mod":          []byte("module functionrulesconsumer\n\ngo 1.24.0\n\nrequire github.com/ClickHouse/clickhouse-go/v2 " + driver + "\n"),
				"queries.go":      generated,
				"runtime_test.go": consumerSource,
			} {
				if err := os.WriteFile(filepath.Join(consumer, name), content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-count=1", "-v", ".")
			command.Dir = consumer
			if data, err := command.CombinedOutput(); err != nil {
				t.Fatalf("execute generated %s code: %v\n%s", driver, err, data)
			}
		})
	}
}

const runtimeConsumer = `package contracts
import (
 "crypto/rand"
 "encoding/hex"
 "os"
 "testing"
 "github.com/ClickHouse/clickhouse-go/v2"
)
func TestMeasuredMath(t *testing.T) {
 ctx := t.Context()
 address := os.Getenv("CHGEN_CONTRACT_NATIVE")
 admin, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}})
 if err != nil { t.Fatal(err) }
 defer admin.Close()
 var serverVersion string
 if err := admin.QueryRow(ctx, "SELECT version()").Scan(&serverVersion); err != nil { t.Fatal(err) }
 if serverVersion != "25.8.29.51" { t.Fatalf("wrong ClickHouse version: %s", serverVersion) }
 var nonce [12]byte
 if _, err := rand.Read(nonce[:]); err != nil { t.Fatal(err) }
 database := "chgen_fn_runtime_"+hex.EncodeToString(nonce[:])
 if err := admin.Exec(ctx, "CREATE DATABASE "+database); err != nil { t.Fatal(err) }
 defer func() { if err := admin.Exec(ctx, "DROP DATABASE "+database); err != nil { t.Error(err) } }()
 conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}, Auth: clickhouse.Auth{Database: database}})
 if err != nil { t.Fatal(err) }
 defer conn.Close()
 if err := conn.Exec(ctx, "CREATE TABLE t (id UInt8, v Nullable(Float64), d Decimal(9, 2)) ENGINE=Memory"); err != nil { t.Fatal(err) }
 if err := conn.Exec(ctx, "INSERT INTO t VALUES (1,0,0),(2,NULL,0)"); err != nil { t.Fatal(err) }
 rows, err := New(conn).ReadMath(ctx, ReadMathParams{})
 if err != nil { t.Fatal(err) }
 if len(rows)!=2 || rows[0].CubeRoot==nil || *rows[0].CubeRoot!=0 || rows[0].Cos==nil || *rows[0].Cos!=1 || rows[1].CubeRoot!=nil || rows[1].Cos!=nil { t.Fatalf("wrong generated values: %+v", rows) }
 if rows[0].Sign==nil || *rows[0].Sign!=0 || rows[1].Sign!=nil || rows[0].DecimalSin!=0 || rows[1].DecimalSin!=0 { t.Fatalf("wrong generated Int8/Decimal values: %+v", rows) }
}
`
