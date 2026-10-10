package project

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/IlyaGulya/chgen/internal/describe"
	"github.com/IlyaGulya/chgen/internal/diagnostic"
)

// ServerSnapshotOptions selects capture or replay, never both. These files
// contain type metadata, not schema DDL or parameter values.
type ServerSnapshotOptions struct{ Input, Output string }

type serverSnapshot struct {
	Format  int                        `json:"format"`
	Inputs  string                     `json:"inputs_sha256"`
	Reports map[string]describe.Report `json:"reports"`
	options ServerSnapshotOptions
	used    map[string]bool
}

func openServerSnapshot(options ServerSnapshotOptions, check bool) (*serverSnapshot, error) {
	if options.Input != "" && options.Output != "" || check && options.Output != "" {
		return nil, fmt.Errorf("snapshot capture and replay are mutually exclusive; check cannot capture")
	}
	store := &serverSnapshot{Format: 1, Reports: make(map[string]describe.Report), options: options, used: make(map[string]bool)}
	if options.Input == "" {
		return store, nil
	}
	file, err := os.Open(options.Input)
	if err != nil {
		return nil, diagnostic.With(fmt.Errorf("read snapshot: %w", err), diagnostic.Detail{
			Code: "server-contracts-unreadable", Status: diagnostic.Unknown, Stage: "analysis",
			Hint: "Check the -contracts path. To create contracts, run chgen -server URL -contracts contracts.json against a prepared test database. No files were changed.",
		})
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		return nil, fmt.Errorf("snapshot is unreadable or exceeds 16 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(store); err != nil {
		return nil, fmt.Errorf("invalid snapshot: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF || store.Format != 1 || store.Inputs == "" || len(store.Reports) == 0 {
		return nil, fmt.Errorf("invalid or unsupported snapshot")
	}
	return store, nil
}

func snapshotHash(value any) string {
	data, _ := json.Marshal(value)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (s *serverSnapshot) describe(ctx context.Context, options describe.Options, sql string) (describe.Report, error) {
	key := snapshotHash(struct {
		SQL, Database string
		Parameters    map[string]string
		External      []describe.ExternalTable
	}{sql, options.Database, options.Parameters, options.ExternalTables})
	if s.options.Input != "" {
		report, exists := s.Reports[key]
		hash := sha256.Sum256([]byte(sql))
		if !exists || report.Provenance != "server-analysis" || report.QuerySHA256 != hex.EncodeToString(hash[:]) || report.ServerVersion == "" || len(report.Columns) == 0 {
			return describe.Report{}, staleServerContracts(fmt.Errorf("snapshot has no matching valid contract; recapture after SQL, database, parameter examples or external structure changes"))
		}
		s.used[key] = true
		return report, nil
	}
	report, err := describe.NativeQuery(ctx, options, sql)
	if err == nil {
		s.Reports[key] = report
	}
	return report, err
}

func (s *serverSnapshot) finish(plan *executionPlan, configPath, examplesPath string) error {
	if s.options.Input == "" && s.options.Output == "" {
		return nil
	}
	inputs := []string{configPath}
	for _, pkg := range plan.packages {
		for _, input := range pkg.schema {
			inputs = append(inputs, input.Path)
		}
		for _, input := range pkg.queries {
			inputs = append(inputs, input.Path)
		}
	}
	var content [][]byte
	for _, path := range inputs {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content = append(content, data)
	}
	digest := snapshotHash(content)
	if s.options.Input != "" && (s.Inputs != digest || len(s.used) != len(s.Reports)) {
		return staleServerContracts(fmt.Errorf("snapshot inputs are stale; recapture server analysis"))
	}
	if examplesPath != "" {
		inputs = append(inputs, examplesPath)
	}
	path := s.options.Input
	if s.options.Output != "" {
		path = s.options.Output
	}
	canonical, err := canonicalPotentialPath(path)
	if err != nil {
		return err
	}
	info, statErr := os.Lstat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if info != nil && !info.Mode().IsRegular() {
		return fmt.Errorf("snapshot must be a regular file, not a link or directory")
	}
	key := portableCollisionKey(canonical)
	for _, input := range inputs {
		inputPath, err := canonicalExistingPath(input)
		if err != nil {
			return err
		}
		inputInfo, err := os.Stat(input)
		if err != nil {
			return err
		}
		if portableCollisionKey(inputPath) == key || info != nil && os.SameFile(info, inputInfo) {
			return fmt.Errorf("snapshot collides with input %s", input)
		}
	}
	for _, pkg := range plan.packages {
		if pkg.outputKey == key || info != nil && pkg.outputInfo != nil && os.SameFile(info, pkg.outputInfo) {
			return fmt.Errorf("snapshot collides with generated output %s", pkg.output)
		}
	}
	if s.options.Output == "" {
		return nil
	}
	if len(s.Reports) == 0 {
		return fmt.Errorf("cannot capture snapshot without server-analyzed queries")
	}
	s.Inputs = digest
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(bytes.TrimSpace(data), '\n')
	directory, err := canonicalPotentialPath(filepath.Dir(path))
	if err != nil {
		return err
	}
	plan.packages = append(plan.packages, plannedPackage{name: "server snapshot", output: path, canonicalOutput: canonical, outputKey: key,
		outputDirectory: directory, directoryKey: portableCollisionKey(directory), mode: 0o600, outputInfo: info, generated: data})
	return nil
}

func staleServerContracts(err error) error {
	return diagnostic.With(err, diagnostic.Detail{
		Code: "server-contracts-stale", Status: diagnostic.Unknown, Stage: "analysis",
		Hint: "Saved contracts no longer match the inputs. Refresh with chgen -server URL -contracts contracts.json using the same -database and -params options, then commit the contracts alongside SQL. No files were changed; replay never connects automatically.",
	})
}
