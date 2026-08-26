package project

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("-- sql"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func TestExpandInputsFileUsedAsIs(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "queries.sql")
	entry := InputEntry{Entry: "queries.sql", Path: filepath.Join(dir, "queries.sql")}
	got, err := expandInputEntries("queries", []InputEntry{entry})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(got) != 1 || got[0] != entry.Path {
		t.Fatalf("got %v", got)
	}
}

func TestExpandInputsMissingEntry(t *testing.T) {
	entry := InputEntry{Entry: "missing.sql", Path: filepath.Join(t.TempDir(), "missing.sql")}
	_, err := expandInputEntries("schema", []InputEntry{entry})
	want := `schema entry "missing.sql": no such file or directory`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestExpandInputsDirectoryRules(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir,
		"m/000002_b.up.sql",
		"m/000001_a.up.sql",
		"m/000001_a.down.sql",
		"m/readme.md",
		"m/nested/000003_c.up.sql",
	)
	entry := InputEntry{Entry: "m", Path: filepath.Join(dir, "m")}
	got, err := expandInputEntries("schema", []InputEntry{entry})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	want := []string{
		filepath.Join(dir, "m/000001_a.up.sql"),
		filepath.Join(dir, "m/000002_b.up.sql"),
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandInputsEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "m/only.down.sql")
	entry := InputEntry{Entry: "m", Path: filepath.Join(dir, "m")}
	_, err := expandInputEntries("schema", []InputEntry{entry})
	want := `schema directory "m" contains no .sql files (after ignoring .down.sql)`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestExpandInputsGlob(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "m/b.sql", "m/a.sql", "m/a.down.sql", "m/c.txt")
	entry := InputEntry{Entry: "m/*.sql", Path: filepath.Join(dir, "m/*.sql")}
	got, err := expandInputEntries("queries", []InputEntry{entry})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(got) != 2 || !strings.HasSuffix(got[0], "a.sql") || !strings.HasSuffix(got[1], "b.sql") {
		t.Fatalf("got %v", got)
	}
}

func TestExpandInputsGlobNoMatches(t *testing.T) {
	dir := t.TempDir()
	entry := InputEntry{Entry: "m/*.sql", Path: filepath.Join(dir, "m/*.sql")}
	_, err := expandInputEntries("queries", []InputEntry{entry})
	want := `queries glob "m/*.sql" matches no files`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestExpandInputsGlobMatchesDirectoriesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "m/sub.sql/inner.sql", "m/a.sql")
	entry := InputEntry{Entry: "m/*.sql", Path: filepath.Join(dir, "m/*.sql")}
	got, err := expandInputEntries("queries", []InputEntry{entry})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(got) != 1 || !strings.HasSuffix(got[0], "a.sql") {
		t.Fatalf("got %v", got)
	}
}

func TestExpandInputsRejectsOverlappingEntries(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "m/a.sql", "m/b.sql")
	entries := []InputEntry{
		{Entry: "m/a.sql", Path: filepath.Join(dir, "m/a.sql")},
		{Entry: "m", Path: filepath.Join(dir, "m")},
	}
	_, err := expandInputEntries("queries", entries)
	if err == nil {
		t.Fatal("the overlapping entries did not fail")
	}
	want := `queries entries "m/a.sql" and "m" select the same file ` + filepath.Join(dir, "m/a.sql")
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err, want)
	}
}

func TestExpandInputsRejectsMalformedGlob(t *testing.T) {
	entry := InputEntry{Entry: "m/[", Path: filepath.Join(t.TempDir(), "m/[")}
	_, err := expandInputEntries("schema", []InputEntry{entry})
	want := `schema glob "m/[" has a malformed pattern`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestExpandInputsAllowsSymbolicLinksToRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test needs symbolic-link creation without an extra privilege")
	}
	dir := t.TempDir()
	writeFiles(t, dir, "target.sql")
	if err := os.Symlink("target.sql", filepath.Join(dir, "link.sql")); err != nil {
		t.Fatal(err)
	}
	entry := InputEntry{Entry: "link.sql", Path: filepath.Join(dir, "link.sql")}
	got, err := expandInputEntries("schema", []InputEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != entry.Path {
		t.Fatalf("got %v", got)
	}
}

func TestExpandInputsCanonicalizesSymbolicLinksForOverlap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test needs symbolic-link creation without an extra privilege")
	}
	dir := t.TempDir()
	writeFiles(t, dir, "target.sql")
	if err := os.Symlink("target.sql", filepath.Join(dir, "link.sql")); err != nil {
		t.Fatal(err)
	}
	entries := []InputEntry{
		{Entry: "target.sql", Path: filepath.Join(dir, "target.sql")},
		{Entry: "link.sql", Path: filepath.Join(dir, "link.sql")},
	}
	_, err := expandInputEntries("queries", entries)
	if err == nil || !strings.Contains(err.Error(), `queries entries "target.sql" and "link.sql" select the same file`) {
		t.Fatalf("got %v", err)
	}
}

func TestExpandInputsUsesFileIdentityForHardLinkOverlap(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "first.sql")
	if err := os.Link(filepath.Join(dir, "first.sql"), filepath.Join(dir, "second.sql")); err != nil {
		t.Skipf("hard links are not available: %v", err)
	}
	entries := []InputEntry{
		{Entry: "first.sql", Path: filepath.Join(dir, "first.sql")},
		{Entry: "second.sql", Path: filepath.Join(dir, "second.sql")},
	}
	_, err := expandInputEntries("queries", entries)
	if err == nil || !strings.Contains(err.Error(), `queries entries "first.sql" and "second.sql" select the same file`) {
		t.Fatalf("got %v", err)
	}
}

func TestExpandInputsDetectsCaseAliasWhereFilesystemProvidesIt(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "Case.sql")
	upper := filepath.Join(dir, "Case.sql")
	lower := filepath.Join(dir, "case.sql")
	upperInfo, err := os.Stat(upper)
	if err != nil {
		t.Fatal(err)
	}
	lowerInfo, err := os.Stat(lower)
	if err != nil || !os.SameFile(upperInfo, lowerInfo) {
		t.Skip("the file system keeps case variants as different paths")
	}
	entries := []InputEntry{{Entry: "Case.sql", Path: upper}, {Entry: "case.sql", Path: lower}}
	if _, err := expandInputEntries("queries", entries); err == nil {
		t.Fatal("the case alias did not fail")
	}
}

func TestExpandInputsDetectsUnicodeAliasWhereFilesystemProvidesIt(t *testing.T) {
	dir := t.TempDir()
	nfcName := "caf\u00e9.sql"
	nfdName := "cafe\u0301.sql"
	writeFiles(t, dir, nfcName)
	nfcPath := filepath.Join(dir, nfcName)
	nfdPath := filepath.Join(dir, nfdName)
	nfcInfo, err := os.Stat(nfcPath)
	if err != nil {
		t.Fatal(err)
	}
	nfdInfo, err := os.Stat(nfdPath)
	if err != nil || !os.SameFile(nfcInfo, nfdInfo) {
		t.Skip("the file system keeps Unicode normalization variants as different paths")
	}
	entries := []InputEntry{{Entry: nfcName, Path: nfcPath}, {Entry: nfdName, Path: nfdPath}}
	if _, err := expandInputEntries("queries", entries); err == nil {
		t.Fatal("the Unicode alias did not fail")
	}
}

func TestPortableCollisionKeyFoldsCaseAndUnicode(t *testing.T) {
	first := filepath.Join("root", "Caf\u00e9", "Output.go")
	second := filepath.Join("root", "cafe\u0301", "output.go")
	if portableCollisionKey(first) != portableCollisionKey(second) {
		t.Fatalf("collision keys differ: %q and %q", portableCollisionKey(first), portableCollisionKey(second))
	}
}

func TestExpandInputsAllowsSymbolicLinkInParentPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test needs symbolic-link creation without an extra privilege")
	}
	dir := t.TempDir()
	writeFiles(t, dir, "real/a.sql")
	if err := os.Symlink("real", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	entry := InputEntry{Entry: "alias/a.sql", Path: filepath.Join(dir, "alias/a.sql")}
	got, err := expandInputEntries("queries", []InputEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != entry.Path {
		t.Fatalf("got %v", got)
	}
}

func TestExpandInputsRejectsNonRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test uses the standard null device path")
	}
	entry := InputEntry{Entry: "null.sql", Path: "/dev/null"}
	_, err := expandInputEntries("schema", []InputEntry{entry})
	want := `schema entry "null.sql": the input is not a regular file`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestExpandInputsKeepsFilesystemErrorClasses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "permission", err: fs.ErrPermission, want: `queries entry "q.sql": permission denied`},
		{name: "other", err: errors.New("device failed"), want: `queries entry "q.sql": cannot inspect the path: device failed`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := inputOperations{
				stat: func(string) (fs.FileInfo, error) { return nil, test.err },
				readDir: func(string) ([]fs.DirEntry, error) {
					t.Fatal("readDir must not run")
					return nil, nil
				},
			}
			entry := InputEntry{Entry: "q.sql", Path: "/unused/q.sql"}
			_, err := expandInputEntriesWithOperations("queries", []InputEntry{entry}, operations)
			if err == nil || err.Error() != test.want {
				t.Fatalf("got %v, want %q", err, test.want)
			}
		})
	}
}

func TestExpandInputsKeepsDirectoryReadErrorClasses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "not found", err: fs.ErrNotExist, want: `schema directory "m": no such file or directory`},
		{name: "permission", err: fs.ErrPermission, want: `schema directory "m": permission denied`},
		{name: "other", err: errors.New("device failed"), want: `schema directory "m": cannot read the path: device failed`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := inputOperations{
				stat: os.Stat,
				readDir: func(string) ([]fs.DirEntry, error) {
					return nil, test.err
				},
			}
			entry := InputEntry{Entry: "m", Path: "/unused/m"}
			_, err := expandDirectoryEntryWithOperations("schema", entry, operations)
			if err == nil || err.Error() != test.want {
				t.Fatalf("got %v, want %q", err, test.want)
			}
		})
	}
}

func TestExpandInputsPropagatesGlobIOErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "m"), 0o755); err != nil {
		t.Fatal(err)
	}
	operations := inputOperations{
		stat: os.Stat,
		readDir: func(string) ([]fs.DirEntry, error) {
			return nil, fs.ErrPermission
		},
	}
	entry := InputEntry{Entry: "m/*.sql", Path: filepath.Join(dir, "m/*.sql")}
	_, err := expandInputEntriesWithOperations("queries", []InputEntry{entry}, operations)
	want := `queries glob "m/*.sql": permission denied`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestExpandInputsKeepsFilepathGlobBraceSemantics(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "schema/item{old}.sql", "schema/itemold.sql")
	pattern := "schema/*{old}.sql"
	entry := InputEntry{Entry: pattern, Path: filepath.Join(dir, pattern)}
	got, err := expandInputEntries("schema", []InputEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "schema/item{old}.sql")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %v, want [%s]", got, want)
	}
}

func TestExpandInputsKeepsFilepathGlobAdjacentStarSemantics(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "query/fooxbar.sql")
	pattern := "query/foo**bar.sql"
	entry := InputEntry{Entry: pattern, Path: filepath.Join(dir, pattern)}
	got, err := expandInputEntries("queries", []InputEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "query/fooxbar.sql")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %v, want [%s]", got, want)
	}
}

func TestFailClosedGlobKeepsFilepathGlobMatches(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir,
		"m/a.sql", "m/b.sql", "m/x{old}.sql", "m/fooxbar.sql",
		"m/nested/c.sql", "other/nested/d.sql",
	)
	patterns := []string{
		"m/*.sql", "m/?.sql", "m/[ab].sql", "m/*{old}.sql",
		"m/foo**bar.sql", "*/nested/*.sql",
	}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			absolute := filepath.Join(dir, pattern)
			want, err := filepath.Glob(absolute)
			if err != nil {
				t.Fatal(err)
			}
			got, err := filepathGlobWithOperations(absolute, osInputOperations, 0)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}
