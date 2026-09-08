package engine

import (
	"reflect"
	"strings"
	"testing"
)

func TestCatalogRenamePreservesDefinition(t *testing.T) {
	catalogs := catalogsFromDDL(t, "CREATE TABLE old (id UInt64, version UInt64, label String) ENGINE = ReplacingMergeTree(version) ORDER BY (id, label);")
	want := catalogs.Physical.Tables["old"]
	want.Name = "renamed"
	if err := applySchemaSource(catalogs, "rename.sql", "RENAME TABLE old TO renamed ON CLUSTER cluster;"); err != nil {
		t.Fatal(err)
	}
	if _, exists := catalogs.Physical.Tables["old"]; exists {
		t.Fatal("old table remains in catalog")
	}
	if got := catalogs.Physical.Tables["renamed"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("renamed definition = %#v, want %#v", got, want)
	}
	queries, err := parseQueriesWithCatalogs(t, "-- name: Read :many\nSELECT id, label FROM renamed FINAL", catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Generate("renamegen", queries); err != nil {
		t.Fatal(err)
	}
	if _, err := parseQueriesWithCatalogs(t, "-- name: Read :many\nSELECT id FROM old", catalogs); err == nil {
		t.Fatal("query against old name was accepted")
	}
	if err := applySchemaSource(catalogs, "later.sql", "ALTER TABLE renamed ADD COLUMN extra String; DROP TABLE renamed;"); err != nil {
		t.Fatal(err)
	}
	if len(catalogs.Physical.Tables) != 0 {
		t.Fatal("DROP after rename did not remove table")
	}
}

func TestCatalogRenameOrderedPairs(t *testing.T) {
	for _, test := range []struct{ sql, first, second string }{
		{"RENAME TABLE a TO archived, staged TO a;", "archived", "a"},
		{"RENAME TABLE a TO tmp, staged TO a, tmp TO staged;", "staged", "a"},
		{"RENAME TABLE a TO b, staged TO c;", "b", "c"},
		{"RENAME TABLE a TO tmp, tmp TO renamed;", "renamed", "staged"},
	} {
		t.Run(test.sql, func(t *testing.T) {
			catalogs := catalogsFromDDL(t, "CREATE TABLE a (id UInt64); CREATE TABLE staged (id String);")
			if err := applySchemaSource(catalogs, "rename.sql", test.sql); err != nil {
				t.Fatal(err)
			}
			if len(catalogs.Physical.Tables) != 2 || catalogs.Physical.Tables[test.first].Columns["id"].Type.String() != "UInt64" || catalogs.Physical.Tables[test.second].Columns["id"].Type.String() != "String" {
				t.Fatalf("wrong pair result: %#v", catalogs.Physical.Tables)
			}
		})
	}
}

func TestCatalogRenameErrorsLeaveCatalogUnchanged(t *testing.T) {
	for _, test := range []struct{ sql, want string }{
		{"RENAME TABLE missing TO b;", `source "missing" is an unknown table`},
		{"RENAME TABLE a TO staged;", `target "staged" already exists`},
		{"RENAME TABLE a TO a;", `target "a" already exists`},
		{"RENAME TABLE a TO staged, staged TO a;", `target "staged" already exists`},
		{"RENAME TABLE a TO b,\nmissing TO c;", `rename.sql:2: RENAME TABLE source "missing"`},
		{"RENAME TABLE a TO b, staged TO b;", `target "b" already exists`},
		{"RENAME TABLE a TO b, a TO c;", `source "a" is an unknown table`},
		{"RENAME TABLE external_keys TO b;", "targets an external schema"},
		{"RENAME TABLE a TO external_keys;", "targets an external schema"},
		{"RENAME TABLE db.a TO b;", "requires unqualified names"},
		{"RENAME TABLE a TO db.b;", "requires unqualified names"},
		{"RENAME TABLE a TO schema_migrations;", "excluded table"},
		{"RENAME TABLE schema_migrations TO a;", "excluded table"},
		{"RENAME DATABASE a TO b;", "RENAME DATABASE is not supported"},
		{"RENAME DICTIONARY a TO b;", "RENAME DICTIONARY is not supported"},
		{"RENAME TABLE IF EXISTS missing TO b;", "parse schema SQL"},
		{"EXCHANGE TABLES a AND staged;", "parse schema SQL"},
	} {
		t.Run(test.sql, func(t *testing.T) {
			const ddl = "CREATE TABLE a (id UInt64); CREATE TABLE staged (id String);\n-- chgen:external\nCREATE TABLE external_keys (id String);"
			catalogs := catalogsFromDDL(t, ddl)
			before := catalogs.Physical.Tables
			err := applySchemaSource(catalogs, "rename.sql", test.sql)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "rename.sql:") {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if len(before) != 2 || before["a"].Name != "a" || before["staged"].Name != "staged" || !reflect.DeepEqual(catalogs.Physical.Tables, before) {
				t.Fatal("failed rename changed catalog")
			}
		})
	}
}
