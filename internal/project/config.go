package project

import (
	"bytes"
	"errors"
	"fmt"
	"go/token"
	"io"
	"os"
	"path/filepath"

	yaml "go.yaml.in/yaml/v3"
)

// Config is the parsed chgen.yaml file. All paths are resolved against the
// directory that holds the configuration file.
type Config struct {
	Path     string
	Version  int
	Packages []PackageConfig
}

// PackageConfig is one generation unit from the configuration file.
type PackageConfig struct {
	Name    string
	Output  string
	Queries []InputEntry
	Schema  []InputEntry
}

// InputEntry is one queries or schema input as written in the configuration
// file. Entry keeps the original text for error messages; Path is the entry
// resolved against the configuration file directory.
type InputEntry struct {
	Entry string
	Path  string
}

type configWire struct {
	Version  strictInteger `yaml:"version"`
	Packages []packageWire `yaml:"packages"`
}

// Keep the concise diagnostic while retaining the underlying filesystem
// error for library callers using errors.Is or errors.As.
type missingConfigError struct {
	directory string
	cause     error
}

func (e *missingConfigError) Error() string {
	return fmt.Sprintf("chgen.yaml not found in %s; create one or pass -f <path>", e.directory)
}

func (e *missingConfigError) Unwrap() error { return e.cause }

type packageWire struct {
	Name    strictString `yaml:"name"`
	Output  strictString `yaml:"output"`
	Queries pathList     `yaml:"queries"`
	Schema  pathList     `yaml:"schema"`
}

type strictInteger struct {
	value int
	set   bool
}

func (value *strictInteger) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return fmt.Errorf("must be an integer")
	}
	var decoded int
	if err := node.Decode(&decoded); err != nil {
		return fmt.Errorf("must be an integer")
	}
	value.value = decoded
	value.set = true
	return nil
}

type strictString struct {
	value string
	set   bool
}

func (value *strictString) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return fmt.Errorf("must be a string")
	}
	value.value = node.Value
	value.set = true
	return nil
}

type pathList struct {
	values []string
	set    bool
}

func (list *pathList) UnmarshalYAML(node *yaml.Node) error {
	list.set = true
	switch node.Kind {
	case yaml.ScalarNode:
		value, err := decodePathNode(node)
		if err != nil {
			return err
		}
		list.values = []string{value}
		return nil
	case yaml.SequenceNode:
		list.values = make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			value, err := decodePathNode(item)
			if err != nil {
				return fmt.Errorf("entries must be strings")
			}
			list.values = append(list.values, value)
		}
		return nil
	default:
		return fmt.Errorf("must be one string or a list of strings")
	}
}

func decodePathNode(node *yaml.Node) (string, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("must be a string")
	}
	return node.Value, nil
}

// LoadConfig reads and validates a chgen.yaml file. Relative paths in the
// file resolve against the directory that holds the file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			dir := filepath.Dir(path)
			if abs, absErr := filepath.Abs(dir); absErr == nil {
				dir = abs
			}
			return nil, &missingConfigError{directory: dir, cause: err}
		}
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	wire, err := decodeConfig(path, data)
	if err != nil {
		return nil, err
	}
	return buildConfig(path, wire)
}

func decodeConfig(path string, data []byte) (configWire, error) {
	if err := validateYAMLSyntax(path, data); err != nil {
		return configWire{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var wire configWire
	if err := decoder.Decode(&wire); err != nil {
		return configWire{}, fmt.Errorf("%s: invalid config: %w", path, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return configWire{}, fmt.Errorf("%s: invalid trailing YAML document: %w", path, err)
		}
		return configWire{}, fmt.Errorf("%s: the file must contain exactly one YAML document", path)
	}
	return wire, nil
}

func validateYAMLSyntax(path string, data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: the file is empty; expected version and packages", path)
		}
		return fmt.Errorf("%s: invalid config: %w", path, err)
	}
	if len(document.Content) == 0 {
		return fmt.Errorf("%s: the file is empty; expected version and packages", path)
	}
	if document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: the top level must be a mapping with version and packages", path)
	}
	root := document.Content[0]
	if err := rejectYAMLReferences(path, root); err != nil {
		return err
	}
	if err := validateMappingKeys(path, "top level", root, map[string]bool{
		"version":  true,
		"packages": true,
	}); err != nil {
		return err
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		key := root.Content[index]
		value := root.Content[index+1]
		if key.Value == "version" {
			if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
				return fmt.Errorf("%s: line %d: version must be an integer", path, value.Line)
			}
			continue
		}
		if key.Value != "packages" {
			continue
		}
		packages := value
		if packages.Kind != yaml.SequenceNode {
			return fmt.Errorf("%s: line %d: packages must be a list", path, packages.Line)
		}
		for ordinal, node := range packages.Content {
			if node.Kind != yaml.MappingNode {
				return fmt.Errorf("%s: line %d: package %d must be a mapping", path, node.Line, ordinal+1)
			}
			if err := validateMappingKeys(path, fmt.Sprintf("package %d", ordinal+1), node, map[string]bool{
				"name": true, "output": true, "queries": true, "schema": true,
			}); err != nil {
				return err
			}
			if err := validatePackageValues(path, ordinal+1, node); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePackageValues(path string, ordinal int, node *yaml.Node) error {
	for index := 0; index+1 < len(node.Content); index += 2 {
		field := node.Content[index].Value
		value := node.Content[index+1]
		switch field {
		case "name", "output":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return fmt.Errorf("%s: line %d: package %d: %s must be a string", path, value.Line, ordinal, field)
			}
		case "queries", "schema":
			if err := validatePathListValue(path, ordinal, field, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePathListValue(path string, ordinal int, field string, node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return fmt.Errorf("%s: line %d: package %d: %s must be a string or a list of strings", path, node.Line, ordinal, field)
		}
	case yaml.SequenceNode:
		for index, item := range node.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return fmt.Errorf("%s: line %d: package %d: %s entry %d must be a string", path, item.Line, ordinal, field, index+1)
			}
		}
	default:
		return fmt.Errorf("%s: line %d: package %d: %s must be a string or a list of strings", path, node.Line, ordinal, field)
	}
	return nil
}

func validateMappingKeys(path, scope string, node *yaml.Node, allowed map[string]bool) error {
	seen := make(map[string]int, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return fmt.Errorf("%s: line %d: %s keys must be strings", path, key.Line, scope)
		}
		if firstLine, exists := seen[key.Value]; exists {
			return fmt.Errorf("%s: line %d: %s has duplicate key %q; first declared at line %d", path, key.Line, scope, key.Value, firstLine)
		}
		seen[key.Value] = key.Line
		if !allowed[key.Value] {
			return fmt.Errorf("%s: line %d: %s: %w", path, key.Line, scope, unknownKeyError(key.Value))
		}
	}
	return nil
}

func rejectYAMLReferences(path string, node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		return fmt.Errorf("%s: line %d: YAML aliases are not supported", path, node.Line)
	}
	if node.Kind == yaml.MappingNode {
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Tag == "!!merge" {
				return fmt.Errorf("%s: line %d: YAML merge keys are not supported", path, key.Line)
			}
		}
	}
	for _, child := range node.Content {
		if err := rejectYAMLReferences(path, child); err != nil {
			return err
		}
	}
	return nil
}

func buildConfig(path string, wire configWire) (*Config, error) {
	if !wire.Version.set {
		return nil, fmt.Errorf(`%s: missing required key "version"`, path)
	}
	if wire.Version.value != 1 {
		return nil, fmt.Errorf("%s: unsupported config version %d; this chgen supports version 1", path, wire.Version.value)
	}
	if wire.Packages == nil {
		return nil, fmt.Errorf(`%s: missing required key "packages"`, path)
	}
	if len(wire.Packages) == 0 {
		return nil, fmt.Errorf("%s: packages must be a list with at least one entry", path)
	}

	baseDir := filepath.Dir(path)
	config := &Config{Path: path, Version: wire.Version.value}
	for index, packageWire := range wire.Packages {
		pkg, err := buildPackage(path, baseDir, index+1, packageWire)
		if err != nil {
			return nil, err
		}
		config.Packages = append(config.Packages, pkg)
	}

	outputs := make(map[string]string, len(config.Packages))
	for _, pkg := range config.Packages {
		if previous, exists := outputs[pkg.Output]; exists {
			display, err := filepath.Rel(baseDir, pkg.Output)
			if err != nil {
				display = pkg.Output
			}
			return nil, fmt.Errorf("%s: packages %q and %q declare the same output %s", path, previous, pkg.Name, display)
		}
		outputs[pkg.Output] = pkg.Name
	}
	return config, nil
}

func buildPackage(path, baseDir string, ordinal int, wire packageWire) (PackageConfig, error) {
	for _, field := range []struct {
		name string
		set  bool
	}{
		{name: "name", set: wire.Name.set},
		{name: "output", set: wire.Output.set},
		{name: "queries", set: wire.Queries.set},
		{name: "schema", set: wire.Schema.set},
	} {
		if !field.set {
			return PackageConfig{}, fmt.Errorf("%s: package %d: missing required key %q", path, ordinal, field.name)
		}
	}
	if wire.Name.value == "" {
		return PackageConfig{}, fmt.Errorf("%s: package %d: name must be a non-empty Go package identifier", path, ordinal)
	}
	if !validPackageName(wire.Name.value) {
		return PackageConfig{}, fmt.Errorf("%s: package %d: invalid package name %q", path, ordinal, wire.Name.value)
	}
	if wire.Output.value == "" {
		return PackageConfig{}, fmt.Errorf("%s: package %d: output must be a non-empty path", path, ordinal)
	}
	queries, err := buildInputs(path, baseDir, ordinal, "queries", wire.Queries.values)
	if err != nil {
		return PackageConfig{}, err
	}
	schema, err := buildInputs(path, baseDir, ordinal, "schema", wire.Schema.values)
	if err != nil {
		return PackageConfig{}, err
	}
	return PackageConfig{
		Name:    wire.Name.value,
		Output:  resolveConfigPath(baseDir, wire.Output.value),
		Queries: queries,
		Schema:  schema,
	}, nil
}

func validPackageName(name string) bool {
	return name != "_" && token.IsIdentifier(name)
}

func resolveConfigPath(baseDir, value string) string {
	path := filepath.FromSlash(value)
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(baseDir, path)
}

func buildInputs(path, baseDir string, ordinal int, key string, values []string) ([]InputEntry, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%s: package %d: %s must list at least one path", path, ordinal, key)
	}
	entries := make([]InputEntry, 0, len(values))
	for _, value := range values {
		if value == "" {
			return nil, fmt.Errorf("%s: package %d: %s entries must be non-empty paths", path, ordinal, key)
		}
		entries = append(entries, InputEntry{Entry: value, Path: resolveConfigPath(baseDir, value)})
	}
	return entries, nil
}

func unknownKeyError(key string) error {
	return fmt.Errorf("unknown key %q; supported keys: version, packages, name, output, queries, schema", key)
}
