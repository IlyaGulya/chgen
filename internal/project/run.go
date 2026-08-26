package project

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/IlyaGulya/chgen/internal/engine"
)

// Run generates every package declared in the configuration file at
// configPath. One run generates all packages; there is no partial mode.
func Run(configPath string) error {
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	for _, pkg := range config.Packages {
		if err := runPackage(pkg); err != nil {
			return err
		}
	}
	return nil
}

func runPackage(pkg PackageConfig) error {
	schemaFiles, err := expandInputEntries("schema", pkg.Schema)
	if err != nil {
		return err
	}
	queryFiles, err := expandInputEntries("queries", pkg.Queries)
	if err != nil {
		return err
	}
	catalogs, err := engine.ParseSchemaCatalogs(schemaFiles)
	if err != nil {
		return err
	}
	queries, err := engine.ParseQueryFiles(queryFiles, catalogs)
	if err != nil {
		return err
	}
	generated, err := engine.Generate(pkg.Name, queries)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pkg.Output), 0o755); err != nil {
		return fmt.Errorf("create output directory for %s: %w", pkg.Output, err)
	}
	if err := os.WriteFile(pkg.Output, generated, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", pkg.Output, err)
	}
	return nil
}
