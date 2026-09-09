package engine

import (
	"reflect"
	"strings"
	"testing"
)

func TestCatalogExchangeDefinitions(t *testing.T) {
	c := catalogsFromDDL(t, `
CREATE TABLE a (id UInt64)
ENGINE = MergeTree ORDER BY id;

CREATE TABLE b (id String, version UInt64)
ENGINE = ReplacingMergeTree(version) ORDER BY (id, version);
`)
	a, b := c.Physical.Tables["a"], c.Physical.Tables["b"]
	a.Name, b.Name = "b", "a"
	if err := applySchemaSource(c, "swap.sql", "-- before\nexchange /* hint */ TABLES `a` AND \"b\" ON CLUSTER 'cluster';"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Physical.Tables["a"], b) || !reflect.DeepEqual(c.Physical.Tables["b"], a) {
		t.Fatal("full definitions not exchanged")
	}
	q, err := parseQueriesWithCatalogs(t, "-- name: Read :many\nSELECT id, version FROM a FINAL", c)
	if err != nil {
		t.Fatal(err)
	}
	if q[0].Results[0].GoType != "string" {
		t.Fatal("query resolved old definition")
	}
	if _, err = Generate("swapgen", q); err != nil {
		t.Fatal(err)
	}
	if err = applySchemaSource(c, "after.sql", "ALTER TABLE a DROP COLUMN version; RENAME TABLE b TO old; DROP TABLE old;"); err != nil {
		t.Fatal(err)
	}
	if len(c.Physical.Tables) != 1 || len(c.Physical.Tables["a"].Columns) != 1 {
		t.Fatal("later DDL resolved wrong definition")
	}
}

func TestCatalogExchangeRefusals(t *testing.T) {
	for _, test := range []struct {
		sql  string
		want string
	}{
		{"EXCHANGE TABLES absent AND a", "unknown table"},
		{"EXCHANGE TABLES a AND absent", "unknown table"},
		{"EXCHANGE TABLES a AND a", "distinct names"},
		{"EXCHANGE TABLES ext AND a", "external schema"},
		{"EXCHANGE TABLES a AND ext", "external schema"},
		{"EXCHANGE TABLES a AND schema_migrations", "excluded table"},
		{"EXCHANGE DICTIONARIES a AND b", "EXCHANGE DICTIONARIES is not supported"},
		{"EXCHANGE TABLE a AND b", "expected EXCHANGE TABLES"},
		{"EXCHANGE TABLES a TO b", "expected EXCHANGE TABLES"},
		{"EXCHANGE TABLES db.a AND b", "unqualified names"},
		{"EXCHANGE TABLES a AND b, c AND d", "expected EXCHANGE TABLES"},
		{"EXCHANGE TABLES IF EXISTS a AND b", "expected EXCHANGE TABLES"},
		{"EXCHANGE TABLES a AND b trailing", "expected EXCHANGE TABLES"},
		{"EXCHANGE TABLES a AND b ON CLUSTER", "expected EXCHANGE TABLES"},
	} {
		t.Run(test.sql, func(t *testing.T) {
			c := catalogsFromDDL(t, `
CREATE TABLE a (id UInt64);
CREATE TABLE b (id String);
-- chgen:external
CREATE TABLE ext (id String);
`)
			a, b := c.Physical.Tables["a"], c.Physical.Tables["b"]
			err := applySchemaSource(c, "swap.sql", "\n"+test.sql)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "swap.sql:2:") {
				t.Fatalf("error = %v, want %s", err, test.want)
			}
			if !reflect.DeepEqual(a, c.Physical.Tables["a"]) || !reflect.DeepEqual(b, c.Physical.Tables["b"]) {
				t.Fatal("failed exchange mutated definitions")
			}
		})
	}
}

func TestSchemaExchangeNormalizationBoundary(t *testing.T) {
	const sql = `-- EXCHANGE TABLES x AND y;
CREATE TABLE a (id String DEFAULT 'EXCHANGE TABLES x AND y;');
EXCHANGE
TABLES a
AND b;
RENAME TABLE b TO c;`
	got, positions, err := normalizeSchemaExchanges(sql)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(sql) || len(positions) != 1 {
		t.Fatal("offsets changed")
	}
	for i := range sql {
		if (sql[i] == '\n') != (got[i] == '\n') {
			t.Fatal("lines changed")
		}
	}
	if !strings.Contains(got, "DEFAULT 'EXCHANGE TABLES x AND y;'") || !strings.HasPrefix(got, "-- EXCHANGE TABLES x AND y;") {
		t.Fatal("quoted/comment contents changed")
	}
	for _, input := range []string{"SELECT exchange FROM a", "CREATE TABLE exchange (id String);", "INSERT INTO a VALUES ('EXCHANGE TABLES a AND b');"} {
		out, p, err := normalizeSchemaExchanges(input)
		if err != nil || out != input || len(p) != 0 {
			t.Fatalf("unrelated SQL changed: %q", input)
		}
	}
}
