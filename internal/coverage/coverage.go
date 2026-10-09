// Package coverage measures an explicit SQL corpus independently of the
// supported-function registry. Declared server expectations are not evidence.
package coverage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"

	"github.com/IlyaGulya/chgen/internal/engine"
)

type Case struct {
	ID             string `json:"id"`
	Family         string `json:"family"`
	Source         string `json:"source"`
	Scope          string `json:"scope,omitempty"`
	Schema         string `json:"schema,omitempty"`
	SQL            string `json:"sql"`
	ExpectedServer string `json:"expected_server"`
}

type Corpus struct {
	Version           int    `json:"version"`
	ClickHouseVersion string `json:"clickhouse_version"`
	Cases             []Case `json:"cases"`
}

type Observation struct {
	ID             string                  `json:"id"`
	Family         string                  `json:"family"`
	Source         string                  `json:"source"`
	ExpectedServer string                  `json:"expected_server"`
	Scope          string                  `json:"scope"`
	ServerColumns  []engine.CoverageColumn `json:"server_columns,omitempty"`
	engine.SQLCoverage
}

type Report struct {
	ComparedFrontend  string                    `json:"compared_frontend,omitempty"`
	Version           int                       `json:"version"`
	ClickHouseVersion string                    `json:"clickhouse_version"`
	ServerVersion     string                    `json:"server_version,omitempty"`
	Frontend          string                    `json:"frontend"`
	CorpusSHA256      string                    `json:"corpus_sha256"`
	Cases             []Observation             `json:"cases"`
	Counts            map[string]map[string]int `json:"stage_counts"`
	Warning           string                    `json:"warning"`
}

func Read(data []byte) (Corpus, error) {
	var corpus Corpus
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return corpus, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&corpus); err != nil {
		return corpus, fmt.Errorf("decode corpus: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return corpus, fmt.Errorf("corpus must contain exactly one JSON object")
	}
	if corpus.Version != 1 || corpus.ClickHouseVersion == "" || len(corpus.Cases) == 0 {
		return corpus, fmt.Errorf("corpus requires version 1, clickhouse_version, and nonempty cases")
	}
	seen := make(map[string]bool)
	for _, cell := range corpus.Cases {
		if cell.ID == "" || seen[cell.ID] || cell.Family == "" || cell.Source == "" || cell.SQL == "" {
			return corpus, fmt.Errorf("case %q requires a unique id, family, source, and SQL", cell.ID)
		}
		seen[cell.ID] = true
		if cell.Scope != "" && cell.Scope != "query" && cell.Scope != "parse" {
			return corpus, fmt.Errorf("case %q has unknown scope %q", cell.ID, cell.Scope)
		}
		switch cell.ExpectedServer {
		case "accept", "refuse", "unknown":
		default:
			return corpus, fmt.Errorf("case %q requires expected_server accept, refuse, or unknown", cell.ID)
		}
	}
	return corpus, nil
}

func Measure(data []byte) (Report, error) {
	corpus, err := Read(data)
	if err != nil {
		return Report{}, err
	}
	hash := sha256.Sum256(data)
	report := Report{
		Version: 1, ClickHouseVersion: corpus.ClickHouseVersion, CorpusSHA256: hex.EncodeToString(hash[:]),
		Frontend: frontendIdentity(),
		Counts:   make(map[string]map[string]int),
		Warning:  "Offline observations only. Server expectations are declarations, not measurements. Resolve combines binding and inference; generate does not compile or execute the Go code. Parser-only scripts do not enter the typed-query denominator.",
	}
	for _, cell := range corpus.Cases {
		observation := Observation{ID: cell.ID, Family: cell.Family, Source: cell.Source, ExpectedServer: cell.ExpectedServer,
			Scope:       cell.Scope,
			SQLCoverage: engine.InspectSQL(cell.Source, cell.Schema, cell.SQL, cell.Scope == "parse")}
		if observation.Scope == "" {
			observation.Scope = "query"
		}
		report.Cases = append(report.Cases, observation)
	}
	report.recount()
	return report, nil
}

func frontendIdentity() string {
	const module = "github.com/AfterShip/clickhouse-sql-parser"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dependency := range info.Deps {
			if dependency.Path == module && dependency.Replace == nil {
				return module + "@" + dependency.Version
			}
		}
	}
	return module + "@unidentified"
}

func (report *Report) recount() {
	report.Counts = make(map[string]map[string]int)
	for _, cell := range report.Cases {
		for stage, result := range cell.Stages {
			if report.Counts[stage] == nil {
				report.Counts[stage] = make(map[string]int)
			}
			report.Counts[stage][result.Status]++
		}
	}
}
