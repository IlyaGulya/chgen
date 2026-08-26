package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// expandInputEntries turns the queries or schema entries of one package into
// an ordered file list. Each entry is one of:
//
//   - a file path, used as-is;
//   - a directory path, expanded to its immediate *.sql files without
//     *.down.sql, sorted byte-wise;
//   - a glob pattern, expanded with the same filter and sort rules.
//
// An entry that resolves to nothing is an error: a silent empty input would
// generate a wrong catalog. kind is "queries" or "schema" and names the
// config key in error messages.
func expandInputEntries(kind string, entries []InputEntry) ([]string, error) {
	var files []string
	for _, entry := range entries {
		if strings.ContainsAny(entry.Entry, "*?[") {
			expanded, err := expandGlobEntry(kind, entry)
			if err != nil {
				return nil, err
			}
			files = append(files, expanded...)
			continue
		}
		info, err := os.Stat(entry.Path)
		if err != nil {
			return nil, fmt.Errorf("%s entry %q: no such file or directory", kind, entry.Entry)
		}
		if !info.IsDir() {
			files = append(files, entry.Path)
			continue
		}
		expanded, err := expandDirectoryEntry(kind, entry)
		if err != nil {
			return nil, err
		}
		files = append(files, expanded...)
	}
	return files, nil
}

func expandDirectoryEntry(kind string, entry InputEntry) ([]string, error) {
	listing, err := os.ReadDir(entry.Path)
	if err != nil {
		return nil, fmt.Errorf("%s entry %q: no such file or directory", kind, entry.Entry)
	}
	var files []string
	for _, item := range listing {
		if item.IsDir() || !keepSQLFileName(item.Name()) {
			continue
		}
		files = append(files, filepath.Join(entry.Path, item.Name()))
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s directory %q contains no .sql files (after ignoring .down.sql)", kind, entry.Entry)
	}
	sort.Strings(files)
	return files, nil
}

func expandGlobEntry(kind string, entry InputEntry) ([]string, error) {
	matches, err := filepath.Glob(entry.Path)
	if err != nil {
		return nil, fmt.Errorf("%s glob %q: %v", kind, entry.Entry, err)
	}
	var files []string
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil || info.IsDir() {
			continue
		}
		if !keepSQLFileName(filepath.Base(match)) {
			continue
		}
		files = append(files, match)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s glob %q matches no files", kind, entry.Entry)
	}
	sort.Strings(files)
	return files, nil
}

func keepSQLFileName(name string) bool {
	return strings.HasSuffix(name, ".sql") && !strings.HasSuffix(name, ".down.sql")
}
