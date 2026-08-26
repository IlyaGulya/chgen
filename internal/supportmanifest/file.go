package supportmanifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Load reads a support manifest file.
func Load(path string) (Manifest, error) {
	var manifest Manifest
	if err := loadJSON(path, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ValidateWitnesses checks that every named Go test exists.
func ValidateWitnesses(root string, manifest Manifest) error {
	parsed := make(map[string]map[string]struct{})
	for _, entry := range manifest.Entries {
		parts := strings.Split(entry.TestWitness, ":")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("support manifest item %s has invalid test witness %q", entryKey(entry.Kind, entry.Name), entry.TestWitness)
		}
		cleanPath := filepath.Clean(parts[0])
		if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
			return fmt.Errorf("support manifest item %s has unsafe test witness path %q", entryKey(entry.Kind, entry.Name), parts[0])
		}
		functions, found := parsed[cleanPath]
		if !found {
			files := token.NewFileSet()
			file, err := parser.ParseFile(files, filepath.Join(root, cleanPath), nil, 0)
			if err != nil {
				return fmt.Errorf("read test witness %s: %w", cleanPath, err)
			}
			functions = make(map[string]struct{})
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if ok {
					functions[function.Name.Name] = struct{}{}
				}
			}
			parsed[cleanPath] = functions
		}
		if _, found := functions[parts[1]]; !found {
			return fmt.Errorf("support manifest item %s names missing test witness %s", entryKey(entry.Kind, entry.Name), entry.TestWitness)
		}
	}
	return nil
}

// LoadConfig reads the strict support overrides.
func LoadConfig(path string) (Config, error) {
	var config Config
	if err := loadJSON(path, &config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func loadJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode %s: trailing JSON value", path)
		}
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
