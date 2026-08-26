package chgen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/IlyaGulya/chgen/internal/engine"
	"github.com/IlyaGulya/chgen/internal/project"
)

func TestFacadeVersionMatchesEngine(t *testing.T) {
	if MeasuredCHVersion != engine.MeasuredCHVersion {
		t.Fatalf("public version is %s, internal version is %s", MeasuredCHVersion, engine.MeasuredCHVersion)
	}
}

func TestConfigConversionKeepsPublicOwnership(t *testing.T) {
	source := &project.Config{
		Path:    "chgen.yaml",
		Version: 1,
		Packages: []project.PackageConfig{{
			Name:    "querygen",
			Output:  "queries.sql.go",
			Queries: []project.InputEntry{{Entry: "queries.sql", Path: "resolved-queries.sql"}},
			Schema:  nil,
		}},
	}
	converted := fromProjectConfig(source)
	if converted.Packages[0].Schema != nil {
		t.Fatal("the conversion changed a nil schema slice")
	}
	converted.Packages[0].Queries[0].Entry = "changed.sql"
	converted.Packages[0].Queries = append(converted.Packages[0].Queries, InputEntry{})
	converted.Packages = append(converted.Packages, PackageConfig{})
	if source.Packages[0].Queries[0].Entry != "queries.sql" || len(source.Packages[0].Queries) != 1 || len(source.Packages) != 1 {
		t.Fatal("a caller mutation changed the internal project configuration")
	}
}

func TestFacadeDTOsHavePublicOwnership(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(CHType{}), reflect.TypeOf(Column{}), reflect.TypeOf(TableEngine{}),
		reflect.TypeOf(Table{}), reflect.TypeOf(Schema{}), reflect.TypeOf(SchemaCatalogs{}),
		reflect.TypeOf(ExternalColumn{}), reflect.TypeOf(ExternalParam{}), reflect.TypeOf(Param{}),
		reflect.TypeOf(Result{}), reflect.TypeOf(Query{}), reflect.TypeOf(Config{}),
		reflect.TypeOf(PackageConfig{}), reflect.TypeOf(InputEntry{}),
	}
	for _, value := range types {
		if value.PkgPath() != "github.com/IlyaGulya/chgen" {
			t.Errorf("%s belongs to %s", value.Name(), value.PkgPath())
		}
	}

	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "facade.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.TYPE {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec := specification.(*ast.TypeSpec)
			ast.Inspect(typeSpec.Type, func(node ast.Node) bool {
				if _, selector := node.(*ast.SelectorExpr); selector {
					t.Errorf("facade type %s contains a selector type", typeSpec.Name.Name)
				}
				return true
			})
		}
	}
}
