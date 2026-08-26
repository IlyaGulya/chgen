package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// expandedInput keeps the source entry that selected one input file. The
// execution planner uses this information to report overlapping entries.
type expandedInput struct {
	Path         string
	Canonical    string
	CollisionKey string
	Entry        string
	Info         fs.FileInfo
}

type inputOperations struct {
	stat    func(string) (fs.FileInfo, error)
	readDir func(string) ([]fs.DirEntry, error)
}

var osInputOperations = inputOperations{
	stat:    os.Stat,
	readDir: os.ReadDir,
}

// expandInputEntries turns the queries or schema entries of one package into
// an ordered file list. It refuses an overlap because parsing one file twice
// can change the schema or can declare one query twice.
func expandInputEntries(kind string, entries []InputEntry) ([]string, error) {
	expanded, err := expandInputEntriesWithOperations(kind, entries, osInputOperations)
	if err != nil {
		return nil, err
	}
	files := make([]string, len(expanded))
	for index := range expanded {
		files[index] = expanded[index].Path
	}
	return files, nil
}

func expandInputEntriesWithOperations(kind string, entries []InputEntry, operations inputOperations) ([]expandedInput, error) {
	var files []expandedInput
	seen := make(map[string]expandedInput)
	for _, entry := range entries {
		var selected []string
		var err error
		if strings.ContainsAny(entry.Entry, "*?[") {
			selected, err = expandGlobEntryWithOperations(kind, entry, operations)
		} else {
			selected, err = expandPathEntryWithOperations(kind, entry, operations)
		}
		if err != nil {
			return nil, err
		}
		for _, path := range selected {
			canonical, err := canonicalExistingPath(path)
			if err != nil {
				return nil, fmt.Errorf("%s entry %q: cannot resolve the selected path: %w", kind, entry.Entry, err)
			}
			info, err := operations.stat(path)
			if err != nil {
				return nil, inputPathError(kind, "selected file", entry.Entry, "inspect", err)
			}
			item := expandedInput{
				Path: path, Canonical: canonical, CollisionKey: portableCollisionKey(canonical),
				Entry: entry.Entry, Info: info,
			}
			if previous, exists := seen[canonical]; exists {
				return nil, duplicateInputError(kind, previous, item)
			}
			for _, previous := range files {
				if previous.Info != nil && item.Info != nil && os.SameFile(previous.Info, item.Info) {
					return nil, duplicateInputError(kind, previous, item)
				}
			}
			seen[canonical] = item
			files = append(files, item)
		}
	}
	return files, nil
}

func duplicateInputError(kind string, previous, current expandedInput) error {
	return fmt.Errorf(
		"%s entries %q and %q select the same file %s",
		kind, previous.Entry, current.Entry, displayInputPath(current.Path),
	)
}

func expandPathEntryWithOperations(kind string, entry InputEntry, operations inputOperations) ([]string, error) {
	info, err := operations.stat(entry.Path)
	if err != nil {
		return nil, inputPathError(kind, "entry", entry.Entry, "inspect", err)
	}
	if info.IsDir() {
		return expandDirectoryEntryWithOperations(kind, entry, operations)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s entry %q: the input is not a regular file", kind, entry.Entry)
	}
	return []string{entry.Path}, nil
}

func expandDirectoryEntryWithOperations(kind string, entry InputEntry, operations inputOperations) ([]string, error) {
	listing, err := operations.readDir(entry.Path)
	if err != nil {
		return nil, inputPathError(kind, "directory", entry.Entry, "read", err)
	}
	var files []string
	for _, item := range listing {
		if !keepSQLFileName(item.Name()) {
			continue
		}
		path := filepath.Join(entry.Path, item.Name())
		info, err := operations.stat(path)
		if err != nil {
			return nil, inputPathError(kind, "directory file", filepath.Join(entry.Entry, item.Name()), "inspect", err)
		}
		if info.IsDir() {
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s directory %q selects %q, which is not a regular file", kind, entry.Entry, item.Name())
		}
		files = append(files, path)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s directory %q contains no .sql files (after ignoring .down.sql)", kind, entry.Entry)
	}
	sort.Strings(files)
	return files, nil
}

func expandGlobEntryWithOperations(kind string, entry InputEntry, operations inputOperations) ([]string, error) {
	matches, err := filepathGlobWithOperations(entry.Path, operations, 0)
	if err != nil {
		if errors.Is(err, filepath.ErrBadPattern) {
			return nil, fmt.Errorf("%s glob %q has a malformed pattern", kind, entry.Entry)
		}
		return nil, inputPathError(kind, "glob", entry.Entry, "expand", err)
	}
	var files []string
	for _, match := range matches {
		info, err := operations.stat(match)
		if err != nil {
			return nil, inputPathError(kind, "glob match", entry.Entry, "inspect", err)
		}
		if info.IsDir() || !keepSQLFileName(filepath.Base(match)) {
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s glob %q selects %q, which is not a regular file", kind, entry.Entry, displayInputPath(match))
		}
		files = append(files, match)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s glob %q matches no files", kind, entry.Entry)
	}
	sort.Strings(files)
	return files, nil
}

func filepathGlobWithOperations(pattern string, operations inputOperations, depth int) ([]string, error) {
	const pathSeparatorLimit = 10000
	if depth == pathSeparatorLimit {
		return nil, filepath.ErrBadPattern
	}
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}
	if !globHasMeta(pattern) {
		_, err := operations.stat(pattern)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []string{pattern}, nil
	}

	directory, file := filepath.Split(pattern)
	volumeLength := 0
	if runtime.GOOS == "windows" {
		volumeLength, directory = cleanWindowsGlobPath(directory)
	} else {
		directory = cleanGlobPath(directory)
	}
	if !globHasMeta(directory[volumeLength:]) {
		return globDirectoryWithOperations(directory, file, operations)
	}
	if directory == pattern {
		return nil, filepath.ErrBadPattern
	}
	directories, err := filepathGlobWithOperations(directory, operations, depth+1)
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, candidate := range directories {
		selected, err := globDirectoryWithOperations(candidate, file, operations)
		if err != nil {
			return nil, err
		}
		matches = append(matches, selected...)
	}
	return matches, nil
}

func globDirectoryWithOperations(directory, pattern string, operations inputOperations) ([]string, error) {
	info, err := operations.stat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	entries, err := operations.readDir(directory)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	var matches []string
	for _, name := range names {
		matched, err := filepath.Match(pattern, name)
		if err != nil {
			return nil, err
		}
		if matched {
			matches = append(matches, filepath.Join(directory, name))
		}
	}
	return matches, nil
}

func cleanGlobPath(path string) string {
	switch path {
	case "":
		return "."
	case string(filepath.Separator):
		return path
	default:
		return path[:len(path)-1]
	}
}

func cleanWindowsGlobPath(path string) (int, string) {
	volumeLength := len(filepath.VolumeName(path))
	switch {
	case path == "":
		return 0, "."
	case volumeLength+1 == len(path) && os.IsPathSeparator(path[len(path)-1]):
		return volumeLength + 1, path
	case volumeLength == len(path) && len(path) == 2:
		return volumeLength, path + "."
	default:
		if volumeLength >= len(path) {
			volumeLength = len(path) - 1
		}
		return volumeLength, path[:len(path)-1]
	}
}

func globHasMeta(path string) bool {
	characters := `*?[`
	if runtime.GOOS != "windows" {
		characters = `*?[\`
	}
	return strings.ContainsAny(path, characters)
}

func inputPathError(kind, subject, entry, operation string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s %s %q: no such file or directory", kind, subject, entry)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%s %s %q: permission denied", kind, subject, entry)
	default:
		return fmt.Errorf("%s %s %q: cannot %s the path: %w", kind, subject, entry, operation, err)
	}
}

func canonicalExistingPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func portableCollisionKey(path string) string {
	folded := cases.Fold().String(norm.NFC.String(filepath.Clean(path)))
	return norm.NFC.String(folded)
}

func displayInputPath(path string) string {
	return filepath.Clean(path)
}

func keepSQLFileName(name string) bool {
	return strings.HasSuffix(name, ".sql") && !strings.HasSuffix(name, ".down.sql")
}
