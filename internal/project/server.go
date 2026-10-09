package project

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/IlyaGulya/chgen/internal/describe"
	"github.com/IlyaGulya/chgen/internal/engine"
)

// RunServer uses the same input/output collision checks and commit phase as
// offline generation, but obtains result contracts from an existing database.
// Schema inputs are never executed, and their catalog is not used for inference.
func RunServer(ctx context.Context, configPath string, options describe.Options, examples map[string]map[string]string, examplesPath string) error {
	return runServer(ctx, configPath, options, examples, examplesPath, false, ServerSnapshotOptions{})
}

// CheckServer reanalyzes all queries and reports stale generated files without
// creating directories or writing any output.
func CheckServer(ctx context.Context, configPath string, options describe.Options, examples map[string]map[string]string, examplesPath string) error {
	return runServer(ctx, configPath, options, examples, examplesPath, true, ServerSnapshotOptions{})
}

// RunServerWithSnapshot captures or replays server result contracts. Replay
// performs no network requests and refuses inputs absent from the snapshot.
func RunServerWithSnapshot(ctx context.Context, configPath string, options describe.Options, examples map[string]map[string]string, examplesPath string, check bool, snapshot ServerSnapshotOptions) error {
	return runServer(ctx, configPath, options, examples, examplesPath, check, snapshot)
}

func runServer(ctx context.Context, configPath string, options describe.Options, examples map[string]map[string]string, examplesPath string, check bool, snapshot ServerSnapshotOptions) error {
	store, err := openServerSnapshot(snapshot, check)
	if err != nil {
		return err
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	seenExamples := make(map[string]bool)
	version := ""
	analysis := make(map[string]string, len(config.Packages))
	for _, pkg := range config.Packages {
		analysis[pkg.Name] = pkg.Analysis
	}
	plan, err := buildExecutionPlanWithResolver(config, func(packageName string, schemaPaths []string, paths []string) ([]engine.Query, error) {
		if analysis[packageName] == "offline" {
			catalogs, err := engine.ParseSchemaCatalogs(schemaPaths)
			if err != nil {
				return nil, err
			}
			return engine.ParseQueryFiles(paths, catalogs)
		}
		externalSchema, err := engine.ParseServerExternalSchemas(schemaPaths)
		if err != nil {
			return nil, err
		}
		var queries []engine.Query
		names := make(map[string]bool)
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			inputs, err := engine.ParseServerQuerySourceWithExternal(path, string(data), externalSchema)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			for _, input := range inputs {
				if names[input.Name] {
					return nil, fmt.Errorf("duplicate query %s", input.Name)
				}
				names[input.Name] = true
				exampleKey := packageName + "." + input.Name
				if _, exists := examples[exampleKey]; !exists {
					exampleKey = input.Name
					if _, supplied := examples[exampleKey]; supplied && seenExamples[exampleKey] {
						return nil, fmt.Errorf("parameter examples for %s are ambiguous; use package.Query keys", input.Name)
					}
				}
				seenExamples[exampleKey] = true
				query, err := analyzeServerQuery(ctx, options, input, examples[exampleKey], &version, store.describe)
				if err != nil {
					return nil, fmt.Errorf("query %s: %w", input.Name, err)
				}
				queries = append(queries, query)
			}
		}
		return queries, nil
	})
	if err != nil {
		return err
	}
	if err := store.finish(plan, config.Path, examplesPath); err != nil {
		return err
	}
	for name := range examples {
		if !seenExamples[name] {
			return fmt.Errorf("parameter examples name unknown query %s", name)
		}
	}
	if examplesPath != "" {
		path, err := canonicalExistingPath(examplesPath)
		if err != nil {
			return err
		}
		info, err := os.Stat(examplesPath)
		if err != nil {
			return err
		}
		for _, pkg := range plan.packages {
			if pkg.outputKey == portableCollisionKey(path) || pkg.outputInfo != nil && os.SameFile(pkg.outputInfo, info) {
				return fmt.Errorf("output %s is also the parameter examples input", pkg.output)
			}
		}
	}
	if check {
		for _, pkg := range plan.packages {
			current, err := os.ReadFile(pkg.output)
			if err != nil {
				return fmt.Errorf("generated output %s is missing or unreadable: %w", pkg.output, err)
			}
			if !bytes.Equal(current, pkg.generated) {
				return fmt.Errorf("generated output %s is out of date; run generate-server", pkg.output)
			}
		}
		return nil
	}
	return commitExecutionPlan(plan)
}
