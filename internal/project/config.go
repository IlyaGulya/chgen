package project

import (
	"fmt"
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
			return nil, fmt.Errorf("chgen.yaml not found in %s; create one or pass -f <path>", dir)
		}
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return nil, fmt.Errorf("%s: the file is empty; expected version and packages", path)
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: the top level must be a mapping with version and packages", path)
	}

	config := &Config{Path: path}
	baseDir := filepath.Dir(path)
	var packagesNode *yaml.Node
	versionSeen := false
	for index := 0; index+1 < len(doc.Content); index += 2 {
		key := doc.Content[index]
		value := doc.Content[index+1]
		switch key.Value {
		case "version":
			if err := value.Decode(&config.Version); err != nil {
				return nil, fmt.Errorf("%s: version must be an integer", path)
			}
			versionSeen = true
		case "packages":
			packagesNode = value
		default:
			return nil, unknownKeyError(path, key.Value)
		}
	}
	if !versionSeen {
		return nil, fmt.Errorf(`%s: missing required key "version"`, path)
	}
	if config.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported config version %d; this chgen supports version 1", path, config.Version)
	}
	if packagesNode == nil {
		return nil, fmt.Errorf(`%s: missing required key "packages"`, path)
	}
	if packagesNode.Kind != yaml.SequenceNode || len(packagesNode.Content) == 0 {
		return nil, fmt.Errorf("%s: packages must be a list with at least one entry", path)
	}

	for index, node := range packagesNode.Content {
		pkg, err := parsePackageNode(path, baseDir, index+1, node)
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

func parsePackageNode(path, baseDir string, ordinal int, node *yaml.Node) (PackageConfig, error) {
	if node.Kind != yaml.MappingNode {
		return PackageConfig{}, fmt.Errorf("%s: package %d must be a mapping", path, ordinal)
	}
	pkg := PackageConfig{}
	seen := make(map[string]bool)
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index]
		value := node.Content[index+1]
		seen[key.Value] = true
		switch key.Value {
		case "name":
			pkg.Name = value.Value
		case "output":
			pkg.Output = resolveConfigPath(baseDir, value.Value)
		case "queries", "schema":
			entries, err := parseInputsNode(path, baseDir, ordinal, key.Value, value)
			if err != nil {
				return PackageConfig{}, err
			}
			if key.Value == "queries" {
				pkg.Queries = entries
			} else {
				pkg.Schema = entries
			}
		default:
			return PackageConfig{}, unknownKeyError(path, key.Value)
		}
	}
	for _, required := range []string{"name", "output", "queries", "schema"} {
		if !seen[required] {
			return PackageConfig{}, fmt.Errorf("%s: package %d: missing required key %q", path, ordinal, required)
		}
	}
	return pkg, nil
}

func resolveConfigPath(baseDir, value string) string {
	path := filepath.FromSlash(value)
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(baseDir, path)
}

func parseInputsNode(path, baseDir string, ordinal int, key string, node *yaml.Node) ([]InputEntry, error) {
	makeEntry := func(text string) (InputEntry, error) {
		if text == "" {
			return InputEntry{}, fmt.Errorf("%s: package %d: %s entries must be non-empty paths", path, ordinal, key)
		}
		entryPath := resolveConfigPath(baseDir, text)
		return InputEntry{Entry: text, Path: entryPath}, nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		entry, err := makeEntry(node.Value)
		if err != nil {
			return nil, err
		}
		return []InputEntry{entry}, nil
	case yaml.SequenceNode:
		if len(node.Content) == 0 {
			return nil, fmt.Errorf("%s: package %d: %s must list at least one path", path, ordinal, key)
		}
		entries := make([]InputEntry, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("%s: package %d: %s entries must be paths", path, ordinal, key)
			}
			entry, err := makeEntry(item.Value)
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
		}
		return entries, nil
	default:
		return nil, fmt.Errorf("%s: package %d: %s must be one path or a list of paths", path, ordinal, key)
	}
}

func unknownKeyError(path, key string) error {
	return fmt.Errorf("%s: unknown key %q; supported keys: version, packages, name, output, queries, schema", path, key)
}
