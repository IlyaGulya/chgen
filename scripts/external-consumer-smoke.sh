#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
smoke_dir="$(mktemp -d)"
trap 'rm -rf "${smoke_dir}"' EXIT

if grep -Eq '^[[:space:]]*replace[[:space:](]' "${repo_root}/go.mod"; then
	echo "go.mod has a replace directive, so a versioned install is not safe." >&2
	exit 1
fi

sed "s|REPO_ROOT|${repo_root}|" >"${smoke_dir}/go.mod" <<'EOF'
module example.com/chgen-consumer

go 1.26.0

require github.com/IlyaGulya/chgen v0.0.0

replace github.com/IlyaGulya/chgen => REPO_ROOT
EOF

mkdir -p "${smoke_dir}/consumer"
sed 's/^+//' >"${smoke_dir}/consumer/consumer.go" <<'EOF'
+package consumer
+
+import "github.com/IlyaGulya/chgen"
+
+var (
+	_ func(string) error                                          = chgen.Run
+	_ func(string) (*chgen.Config, error)                         = chgen.LoadConfig
+	_ func([]string) (*chgen.SchemaCatalogs, error)               = chgen.ParseSchemaCatalogs
+	_ func([]string, *chgen.SchemaCatalogs) ([]chgen.Query, error) = chgen.ParseQueryFiles
+	_ func(string, []chgen.Query) ([]byte, error)                  = chgen.Generate
+	_ func(string, string, string) (chgen.CHType, error)           = chgen.InferExpressionType
+	_ func(string, string) (chgen.CHType, error)                   = chgen.InferQueryResultType
+)
+
+func exercisePublicDTOs() {
+	typeValue := chgen.CHType{Name: "Tuple", Params: []chgen.CHType{{Name: "UInt64"}}, LiteralParams: []string{}, ParamNames: []string{"id"}}
+	typeValue.Name = "UInt64"
+	typeValue.Params = nil
+	typeValue.LiteralParams = nil
+	typeValue.ParamNames = nil
+	_ = typeValue.String()
+
+	column := chgen.Column{Name: "id", Type: typeValue, Insertable: true}
+	column.Name = "value"
+	column.Type = chgen.CHType{Name: "String"}
+	column.Insertable = false
+	engine := chgen.TableEngine{Name: "Memory", Params: []string{}, OrderBy: []string{}}
+	engine.Name = "MergeTree"
+	engine.Params = []string{"version"}
+	engine.OrderBy = []string{"id"}
+	table := chgen.Table{Name: "events", File: "schema.sql", Line: 1, Columns: map[string]chgen.Column{"id": column}, ColumnOrder: []string{"id"}, Engine: &engine}
+	table.Name = "values"
+	table.File = "values.sql"
+	table.Line = 2
+	table.Columns = map[string]chgen.Column{"value": column}
+	table.ColumnOrder = []string{"value"}
+	table.Engine = nil
+	schema := chgen.Schema{Tables: map[string]chgen.Table{"values": table}}
+	schema.Tables = map[string]chgen.Table{"events": table}
+	catalogs := chgen.SchemaCatalogs{Physical: &schema, External: &chgen.Schema{Tables: map[string]chgen.Table{}}}
+	catalogs.Physical = &schema
+	catalogs.External = nil
+
+	externalColumn := chgen.ExternalColumn{GoName: "ID", SQLName: "id", GoType: "uint64", ClickHouseType: "UInt64"}
+	externalColumn.GoName = "Value"
+	externalColumn.SQLName = "value"
+	externalColumn.GoType = "string"
+	externalColumn.ClickHouseType = "String"
+	external := chgen.ExternalParam{GoName: "Rows", SchemaName: "row", WireName: "rows", RowType: "Row", Columns: []chgen.ExternalColumn{externalColumn}}
+	external.GoName = "Values"
+	external.SchemaName = "value"
+	external.WireName = "values"
+	external.RowType = "Value"
+	external.Columns = nil
+	param := chgen.Param{GoName: "ID", GoType: "uint64", CHType: typeValue}
+	param.GoName = "Value"
+	param.GoType = "string"
+	param.CHType = chgen.CHType{Name: "String"}
+	result := chgen.Result{GoName: "ID", SQLName: "id", GoType: "uint64", CHType: typeValue}
+	result.GoName = "Value"
+	result.SQLName = "value"
+	result.GoType = "string"
+	result.CHType = chgen.CHType{Name: "String"}
+	query := chgen.Query{Name: "List", File: "queries.sql", Line: 1, Command: chgen.CommandMany, Params: []chgen.Param{param}, ExternalParams: []chgen.ExternalParam{external}, ParamIndexes: []int{0}, Results: []chgen.Result{result}, ResultCapacity: "1", SQL: "SELECT ? AS value", NamedParamNames: []string{"Value"}}
+	query.Name = "One"
+	query.File = "one.sql"
+	query.Line = 2
+	query.Command = chgen.CommandOne
+	query.Params = nil
+	query.ExternalParams = nil
+	query.ParamIndexes = nil
+	query.Results = nil
+	query.ResultCapacity = "2"
+	query.SQL = "SELECT 1 AS value"
+	query.NamedParamNames = nil
+	entry := chgen.InputEntry{Entry: "queries.sql", Path: "/queries.sql"}
+	entry.Entry = "schema.sql"
+	entry.Path = "/schema.sql"
+	packageConfig := chgen.PackageConfig{Name: "query", Output: "query.go", Queries: []chgen.InputEntry{entry}, Schema: []chgen.InputEntry{entry}}
+	packageConfig.Name = "queries"
+	packageConfig.Output = "queries.go"
+	packageConfig.Queries = nil
+	packageConfig.Schema = nil
+	config := chgen.Config{Path: "chgen.yaml", Version: 1, Packages: []chgen.PackageConfig{packageConfig}}
+	config.Path = "other.yaml"
+	config.Version = 2
+	config.Packages = nil
+	_ = []any{catalogs, query, config, chgen.CommandExec}
+}
EOF

(
	cd "${smoke_dir}"
	go mod tidy
	go test ./...
	GOBIN="${smoke_dir}/bin" go install github.com/IlyaGulya/chgen/cmd/chgen
	"${smoke_dir}/bin/chgen" -version
)
