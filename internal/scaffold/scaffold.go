// Package scaffold creates the initial files for a chgen project.
package scaffold

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type projectFile struct {
	name    string
	content string
}

type fileOperations struct {
	stat   func(string) (os.FileInfo, error)
	lstat  func(string) (os.FileInfo, error)
	create func(string) (io.WriteCloser, error)
}

var osFileOperations = fileOperations{
	stat:  os.Stat,
	lstat: os.Lstat,
	create: func(path string) (io.WriteCloser, error) {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	},
}

var projectFiles = []projectFile{
	{
		name: "chgen.yaml",
		content: `version: 1
packages:
  - name: querygen
    output: internal/querygen/queries.sql.go
    schema: schema.sql
    queries: queries.sql
`,
	},
	{
		name: "schema.sql",
		content: `CREATE TABLE users
(
    id   UInt64,
    name String
)
ENGINE = MergeTree
ORDER BY id;
`,
	},
	{
		name: "queries.sql",
		content: `-- name: GetUser :one
SELECT id, name
FROM users
WHERE id = chgen.arg('ID');
`,
	},
}

// Init creates a minimal chgen project in dir. It does not replace a file.
func Init(dir string) error {
	return initWithOperations(dir, osFileOperations)
}

func initWithOperations(dir string, operations fileOperations) error {
	info, err := operations.stat(dir)
	if err != nil {
		return fmt.Errorf("inspect target directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("target %q is not a directory", dir)
	}

	for _, file := range projectFiles {
		path := filepath.Join(dir, file.name)
		_, err := operations.lstat(path)
		switch {
		case err == nil:
			return fmt.Errorf("refuse to initialize: %s already exists", path)
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("inspect %s: %w", path, err)
		}
	}

	created := make([]string, 0, len(projectFiles))
	for _, file := range projectFiles {
		path := filepath.Join(dir, file.name)
		handle, err := operations.create(path)
		if err != nil {
			return reportPartialCreation(fmt.Errorf("create %s: %w", path, err), created)
		}
		created = append(created, path)
		if _, err := io.WriteString(handle, file.content); err != nil {
			writeErr := fmt.Errorf("write %s: %w", path, err)
			if closeErr := handle.Close(); closeErr != nil {
				writeErr = errors.Join(writeErr, fmt.Errorf("close %s after the write failure: %w", path, closeErr))
			}
			return reportPartialCreation(writeErr, created)
		}
		if err := handle.Close(); err != nil {
			return reportPartialCreation(fmt.Errorf("close %s: %w", path, err), created)
		}
	}
	return nil
}

func reportPartialCreation(err error, created []string) error {
	if len(created) == 0 {
		return err
	}
	return fmt.Errorf("%w; files created before failure: %s", err, strings.Join(created, ", "))
}
