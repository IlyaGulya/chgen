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
		schema: "CREATE TABLE t (id UInt8, v Nullable(Float64), d Decimal(9, 2), s Nullable(String), f32 Float32, a SimpleAggregateFunction(anyLast, Float64)) ENGINE=Memory",
		sql:    runtimeQueries,
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

const runtimeQueries = `-- name: ReadMath :many
-- result: CubeRoot cube_root
-- result: Cos cos_value
-- result: Sign sign_value
-- result: DecimalSin decimal_sin
SELECT cbrt(v) AS cube_root, cos(v) AS cos_value, sign(v) AS sign_value, sin(d) AS decimal_sin
FROM t WHERE id < 3 ORDER BY id;

-- name: ReadPortableMath :many
-- result: Exp exp_value
-- result: Log log_value
-- result: Tanh tanh_value
-- result: NullableExp nullable_exp
SELECT exp(CAST(f32 AS Float64)) AS exp_value,
 log(CAST(a AS Float64)) AS log_value,
 tanh(CAST(f32 AS Float64)) AS tanh_value, exp(v) AS nullable_exp
FROM t WHERE id < 3 ORDER BY id;

-- name: ReadStrings :many
-- result: Encoded encoded
-- result: Decoded decoded
-- result: URL url
-- result: URLDecoded url_decoded
-- result: Lower lower_value
-- result: Upper upper_value
-- result: Reverse reverse_value
-- result: Soundex soundex_value
-- result: Quoted quoted
-- result: NFC nfc
-- result: NFD nfd
-- result: NFKC nfkc
-- result: NFKD nfkd
SELECT base64Encode(s) AS encoded, tryBase64Decode(s) AS decoded,
 encodeURLComponent(s) AS url, decodeURLComponent(s) AS url_decoded,
 lowerUTF8(s) AS lower_value, upperUTF8(s) AS upper_value,
 reverseUTF8(s) AS reverse_value, soundex(s) AS soundex_value,
 regexpQuoteMeta(s) AS quoted, normalizeUTF8NFC(s) AS nfc,
 normalizeUTF8NFD(s) AS nfd, normalizeUTF8NFKC(s) AS nfkc,
 normalizeUTF8NFKD(s) AS nfkd
FROM t ORDER BY id;

-- name: ReadAggregates :one
-- result: Sum sum_value
-- result: ArraySum array_sum
-- result: NullableSum nullable_sum
-- result: Average average_value
-- result: Unique unique_value
SELECT sumIf(v, id > 0) AS sum_value,
 sumArrayIf([v], id > 0) AS array_sum,
 sumIfOrNull(v, id > 0) AS nullable_sum,
 avgIf(v, id > 0) AS average_value,
 uniqExactIf(v, id > 0) AS unique_value
FROM t;
`

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
 if err := conn.Exec(ctx, "CREATE TABLE t (id UInt8, v Nullable(Float64), d Decimal(9, 2), s Nullable(String), f32 Float32, a SimpleAggregateFunction(anyLast, Float64)) ENGINE=Memory"); err != nil { t.Fatal(err) }
 if err := conn.Exec(ctx, "INSERT INTO t VALUES (1,0,0,'AbC',1,1),(2,NULL,0,NULL,1,1),(3,1,0,'Привет café',1,1)"); err != nil { t.Fatal(err) }
 rows, err := New(conn).ReadMath(ctx, ReadMathParams{})
 if err != nil { t.Fatal(err) }
 if len(rows)!=2 || rows[0].CubeRoot==nil || *rows[0].CubeRoot!=0 || rows[0].Cos==nil || *rows[0].Cos!=1 || rows[1].CubeRoot!=nil || rows[1].Cos!=nil { t.Fatalf("wrong generated values: %+v", rows) }
 if rows[0].Sign==nil || *rows[0].Sign!=0 || rows[1].Sign!=nil || rows[0].DecimalSin!=0 || rows[1].DecimalSin!=0 { t.Fatalf("wrong generated Int8/Decimal values: %+v", rows) }
 portable, err := New(conn).ReadPortableMath(ctx, ReadPortableMathParams{})
 if err != nil { t.Fatal(err) }
 if len(portable)!=2 { t.Fatalf("wrong portable rows: %+v",portable) }
 // FastOps and the fallback need not agree numerically. The generator's
 // contract is correct types and lossless scanning of THIS server's values.
 // A direct driver call is independent of the generated method and checks
 // the actual result metadata as well as every value and field position.
 witness, err := conn.Query(ctx, "SELECT exp(CAST(f32 AS Float64)), log(CAST(a AS Float64)), tanh(CAST(f32 AS Float64)), exp(v) FROM t WHERE id < 3 ORDER BY id")
 if err != nil { t.Fatal(err) }
 defer witness.Close()
 types := witness.ColumnTypes()
 if len(types)!=4 { t.Fatalf("wrong witness columns: %v",types) }
 for i, want := range []string{"Float64","Float64","Float64","Nullable(Float64)"} {
  if types[i].DatabaseTypeName()!=want { t.Fatalf("column %d: got %s, want %s",i,types[i].DatabaseTypeName(),want) }
 }
 index := 0
 for witness.Next() {
  var expValue, logValue, tanhValue float64
  var nullableExp *float64
  if err := witness.Scan(&expValue,&logValue,&tanhValue,&nullableExp); err != nil { t.Fatal(err) }
  if index>=len(portable) { t.Fatal("generated method lost a row") }
  row := portable[index]
  if row.Exp!=expValue || row.Log!=logValue || row.Tanh!=tanhValue || (row.NullableExp==nil)!=(nullableExp==nil) { t.Fatalf("generated values differ from driver witness: %+v",row) }
  if nullableExp!=nil && *row.NullableExp!=*nullableExp { t.Fatal("generated nullable value differs from driver witness") }
  index++
 }
 if err := witness.Err(); err != nil { t.Fatal(err) }
 if index!=len(portable) { t.Fatal("generated method added a row") }
 if portable[0].NullableExp==nil || *portable[0].NullableExp!=1 || portable[1].NullableExp!=nil { t.Fatalf("nullable portable values: %+v",portable) }
 strings, err := New(conn).ReadStrings(ctx, ReadStringsParams{})
 if err != nil { t.Fatal(err) }
 if len(strings)!=3 { t.Fatalf("wrong string rows: %+v", strings) }
 first, null := strings[0], strings[1]
 for _, check := range []struct{actual, null *string; want string}{
  {first.Encoded,null.Encoded,"QWJD"},{first.Decoded,null.Decoded,""},
  {first.URL,null.URL,"AbC"},{first.URLDecoded,null.URLDecoded,"AbC"},
  {first.Lower,null.Lower,"abc"},{first.Upper,null.Upper,"ABC"},
  {first.Reverse,null.Reverse,"CbA"},{first.Soundex,null.Soundex,"A120"},
  {first.Quoted,null.Quoted,"AbC"},{first.NFC,null.NFC,"AbC"},
  {first.NFD,null.NFD,"AbC"},{first.NFKC,null.NFKC,"AbC"},{first.NFKD,null.NFKD,"AbC"},
 } {
  if check.actual==nil || *check.actual!=check.want || check.null!=nil { t.Fatalf("string value: got %v, null %v; want %q",check.actual,check.null,check.want) }
 }
 if strings[2].Lower==nil || *strings[2].Lower!="привет café" || strings[2].Reverse==nil || *strings[2].Reverse!="éfac тевирП" { t.Fatalf("wrong Unicode: %+v",strings[2]) }
 aggregates, err := New(conn).ReadAggregates(ctx, ReadAggregatesParams{})
 if err != nil { t.Fatal(err) }
 if aggregates.Sum==nil || *aggregates.Sum!=1 || aggregates.ArraySum==nil || *aggregates.ArraySum!=1 || aggregates.NullableSum==nil || *aggregates.NullableSum!=1 || aggregates.Average==nil || *aggregates.Average!=0.5 || aggregates.Unique!=2 { t.Fatalf("wrong generated aggregate values: %+v",aggregates) }
}
`
