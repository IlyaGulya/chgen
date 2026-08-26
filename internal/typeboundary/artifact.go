package typeboundary

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	chgen "github.com/IlyaGulya/chgen"
	"github.com/IlyaGulya/chgen/internal/conformance"
)

// ArtifactVersion names the shape of the JSON file below. A reader that
// meets a number this package does not know refuses instead of silently
// reading a partial or misaligned structure.
const ArtifactVersion = 1

// Artifact is the durable file that `chgen probe` writes. It records the
// server this run measured (Server), the exact catalog names this run
// covers (CatalogNames — see Verify), and the verdict of every cell
// (Cells).
//
// CatalogNames is not decoration: Compare refuses two artifacts whose
// catalog differs, the same way oraclereport.Compare refuses two reports of
// a different fixture. Without that guard a cell added on one side and
// silently absent on the other would compare as "no difference" instead of
// "the question was not fully asked".
type Artifact struct {
	Version      int                `json:"version"`
	Server       ServerInfo         `json:"server"`
	CatalogNames []string           `json:"catalog_names"`
	Cells        map[string]Verdict `json:"cells"`
	FixtureHash  string             `json:"fixture_hash,omitempty"`
	SeedHash     string             `json:"seed_hash,omitempty"`
	MatrixHash   string             `json:"matrix_hash,omitempty"`
	Conformance  []conformance.Cell `json:"conformance_cells,omitempty"`
}

// Run executes every cell of Catalog against the server the client points
// at and returns the resulting artifact. It creates and drops its own
// fixture table so that a probe run leaves no trace.
func Run(ctx context.Context, c *Client) (*Artifact, error) {
	if _, err := c.exec(ctx, DropDDL); err != nil {
		return nil, fmt.Errorf("drop stale fixture: %w", err)
	}
	if _, err := c.exec(ctx, SchemaDDL); err != nil {
		return nil, fmt.Errorf("create fixture: %w", err)
	}
	if _, err := c.exec(ctx, SeedRowDML); err != nil {
		return nil, fmt.Errorf("seed fixture: %w", err)
	}
	defer func() { _, _ = c.exec(ctx, DropDDL) }()

	names := make([]string, 0, len(Catalog))
	inputs := make([]conformance.Input, 0, len(Catalog))
	seen := make(map[string]bool, len(Catalog))
	for _, cell := range Catalog {
		if seen[cell.Name] {
			return nil, fmt.Errorf("catalog has a duplicate cell name %q; every name must be unique because it is the comparison key", cell.Name)
		}
		seen[cell.Name] = true
		names = append(names, cell.Name)
		inputs = append(inputs, conformance.Input{ID: cell.Name, Expression: cell.Query, Table: "chgen_probe_t"})
	}
	sort.Strings(names)
	httpServer := conformance.NewHTTPServer(c.URL, c.Database)
	httpServer.Client = c.HTTP
	runner := conformance.Runner{
		Server: httpServer,
		Infer: func(input conformance.Input) (string, error) {
			inferred, inferErr := chgen.InferExpressionType(SchemaDDL, input.Table, input.Expression)
			if inferErr != nil {
				return "", inferErr
			}
			return inferred.String(), nil
		},
	}
	report, err := runner.Run(ctx, SchemaDDL, SeedRowDML, inputs)
	if err != nil {
		return nil, err
	}
	cells := make(map[string]Verdict, len(report.Cells))
	for _, cell := range report.Cells {
		switch {
		case cell.Execution.ErrorCode != 0:
			cells[cell.ID] = Verdict{ErrorCode: cell.Execution.ErrorCode}
		case cell.Execution.Error != "":
			return nil, fmt.Errorf("cell %q execution: %s", cell.ID, cell.Execution.Error)
		case cell.Analysis.ErrorCode != 0:
			cells[cell.ID] = Verdict{ErrorCode: cell.Analysis.ErrorCode}
		case cell.Analysis.Error != "":
			return nil, fmt.Errorf("cell %q analysis: %s", cell.ID, cell.Analysis.Error)
		default:
			cells[cell.ID] = Verdict{TypeName: cell.Analysis.Raw}
		}
	}

	return &Artifact{
		Version:      ArtifactVersion,
		Server:       ServerInfo{Version: report.Metadata.Server.Version, ServerRun: report.Metadata.Server.ServerRun, UptimeS: report.Metadata.Server.UptimeS},
		CatalogNames: names,
		Cells:        cells,
		FixtureHash:  report.Metadata.FixtureHash,
		SeedHash:     report.Metadata.SeedHash,
		MatrixHash:   report.Metadata.MatrixHash,
		Conformance:  report.Cells,
	}, nil
}

// runOne runs one catalog cell and turns either its answer or its refusal
// into a Verdict. A transport failure (a connection error, a malformed
// answer) is returned as an error and is NEVER folded into Verdict: only a
// ClickHouse-numbered refusal is a boundary answer, everything else is a
// harness fault that must stop the run instead of being recorded as if it
// were a type.
func runOne(ctx context.Context, c *Client, cell Cell) (Verdict, error) {
	raw, err := c.exec(ctx, exprToQuery(cell.Query))
	if err != nil {
		var chErr *chError
		if asCHError(err, &chErr) {
			if chErr.code == 0 {
				return Verdict{}, fmt.Errorf("server refused with an unparsed code: %s", chErr.message)
			}
			return Verdict{ErrorCode: chErr.code}, nil
		}
		return Verdict{}, err
	}
	fields := splitTSVFields(raw)
	if len(fields) != 2 {
		return Verdict{}, fmt.Errorf("unexpected answer shape %q for query %q", raw, cell.Query)
	}
	if fields[1] != chIgnoreConstant {
		return Verdict{}, fmt.Errorf("execution witness did not answer the expected constant (got %q) for query %q; "+
			"the analysed type cannot be trusted without a matching execution witness", fields[1], cell.Query)
	}
	return Verdict{TypeName: fields[0]}, nil
}

// chIgnoreConstant is the value ignore() answers, the same constant the type
// oracle's own witness relies on (typeoracle_fuzz_test.go).
const chIgnoreConstant = "0"

func asCHError(err error, target **chError) bool {
	e, ok := err.(*chError)
	if !ok {
		return false
	}
	*target = e
	return true
}

func splitTSVFields(s string) []string {
	var fields []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\t' {
			fields = append(fields, s[start:i])
			start = i + 1
		}
	}
	fields = append(fields, s[start:])
	return fields
}

// Save writes the artifact as indented JSON.
func (a *Artifact) Save(path string) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("encode artifact: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// Load reads a probe artifact from disk.
func Load(path string) (*Artifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a Artifact
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("decode artifact %s: %w", path, err)
	}
	if a.Version != ArtifactVersion {
		return nil, fmt.Errorf("artifact %s has version %d, this program reads version %d", path, a.Version, ArtifactVersion)
	}
	if len(a.Cells) == 0 {
		return nil, fmt.Errorf("artifact %s holds no cells; a probe that never ran must not be usable as if it had", path)
	}
	return &a, nil
}

// ValidateConformance checks that a current deterministic artifact contains
// the complete shared runner record. A legacy baseline can omit this record,
// but a new CI measurement cannot.
func (a *Artifact) ValidateConformance() error {
	if a.FixtureHash == "" || a.SeedHash == "" || a.MatrixHash == "" {
		return fmt.Errorf("conformance metadata has an empty stable hash")
	}
	if len(a.Conformance) != len(a.CatalogNames) {
		return fmt.Errorf("conformance has %d cells for a catalog of %d cells", len(a.Conformance), len(a.CatalogNames))
	}
	inputs := make([]conformance.Input, len(a.Conformance))
	seen := make(map[string]bool, len(a.Conformance))
	for index, cell := range a.Conformance {
		if seen[cell.ID] {
			return fmt.Errorf("conformance has duplicate cell %q", cell.ID)
		}
		seen[cell.ID] = true
		if _, ok := a.Cells[cell.ID]; !ok {
			return fmt.Errorf("conformance cell %q is not in the verdict map", cell.ID)
		}
		if cell.Chgen.Canonical == nil && cell.Chgen.Error == "" {
			return fmt.Errorf("conformance cell %q has no chgen result", cell.ID)
		}
		if cell.Analysis.Canonical == nil && cell.Analysis.Error == "" {
			return fmt.Errorf("conformance cell %q has no server analysis result", cell.ID)
		}
		if !cell.Execution.Ran && cell.Execution.Error == "" {
			return fmt.Errorf("conformance cell %q has no execution witness", cell.ID)
		}
		inputs[index] = conformance.Input{ID: cell.ID, Expression: cell.Expression, Table: cell.Table}
	}
	if got := conformance.MatrixHash(inputs); got != a.MatrixHash {
		return fmt.Errorf("conformance matrix hash is %s, want %s", a.MatrixHash, got)
	}
	return nil
}
