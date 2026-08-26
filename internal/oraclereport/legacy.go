package oraclereport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PrefixMapping names one explicit historical kind-prefix conversion.
type PrefixMapping struct {
	From string
	To   string
}

// LegacyConversionSpec is an allowlist entry for one exact JSON artifact.
// ArtifactSHA256 is the hash of the source bytes, not a value in the report.
type LegacyConversionSpec struct {
	ArtifactSHA256 string
	PopulationID   string
	FixtureHash    string
	FixtureShape   string
	PrefixMappings []PrefixMapping
}

// LegacyBaselineConversionSpec is an allowlist entry for one exact baseline.
type LegacyBaselineConversionSpec struct {
	ArtifactSHA256     string
	SourceCell         string
	FixtureHash        string
	FixtureShapeSHA256 string
	PrefixMappings     []PrefixMapping
}

// LegacyArchive is a checked historical report. It is not a current Report,
// so Compare and Gate cannot use it as current-profile evidence.
type LegacyArchive struct {
	ArtifactSHA256 string                       `json:"artifact_sha256"`
	PopulationID   string                       `json:"population_id"`
	FixtureHash    string                       `json:"fixture_hash"`
	FixtureShape   string                       `json:"fixture_signature"`
	CHVersion      string                       `json:"clickhouse_version"`
	ServerRun      int64                        `json:"server_run"`
	Seed           int64                        `json:"seed"`
	Generated      int                          `json:"generated_expressions"`
	Mismatch       []string                     `json:"mismatch_signatures"`
	Blind43        []string                     `json:"blind43_signatures"`
	Retired        map[string]*RetirementRecord `json:"retired,omitempty"`
}

type legacyPopulation struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
}

type legacyReport struct {
	Version          int              `json:"version"`
	Population       legacyPopulation `json:"population"`
	CHVersion        string           `json:"clickhouse_version"`
	Seed             int64            `json:"seed"`
	Generated        int              `json:"generated_expressions"`
	ServerRun        int64            `json:"server_run"`
	FixtureHash      string           `json:"fixture_hash"`
	FixtureSignature string           `json:"fixture_signature"`
	MismatchSig      map[string]int   `json:"mismatch_signatures"`
	Findings         []Finding        `json:"findings"`
}

// ConvertLegacyArtifact checks and converts one allowlisted historical
// report. The result stays archive-only and cannot acquire current identity.
func ConvertLegacyArtifact(data []byte, spec LegacyConversionSpec) (*LegacyArchive, error) {
	sum := sha256.Sum256(data)
	artifactSHA := hex.EncodeToString(sum[:])
	if !validSHA256(spec.ArtifactSHA256) || artifactSHA != spec.ArtifactSHA256 {
		return nil, fmt.Errorf("legacy artifact SHA-256 %s is not the allowlisted SHA-256 %s; keep the report archive-only and rerun the current profile", artifactSHA, spec.ArtifactSHA256)
	}
	var legacy legacyReport
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("decode allowlisted legacy report: %w", err)
	}
	if legacy.Version != 1 || legacy.Population.Version != 1 || legacy.Population.ID != spec.PopulationID {
		return nil, fmt.Errorf("legacy population does not match the allowlisted conversion spec")
	}
	if legacy.FixtureHash == "" || legacy.FixtureSignature == "" || legacy.ServerRun == 0 || legacy.CHVersion == "" {
		return nil, fmt.Errorf("legacy report has incomplete fixture, ClickHouse, or server-run identity; keep it archive-only and rerun the current profile")
	}
	if legacy.FixtureHash != spec.FixtureHash || legacy.FixtureSignature != spec.FixtureShape {
		return nil, fmt.Errorf("legacy fixture does not match the allowlisted conversion spec")
	}
	mismatch, err := mapSignatures(sortedKeys(legacy.MismatchSig), spec.PrefixMappings)
	if err != nil {
		return nil, err
	}
	var blind []string
	for _, finding := range legacy.Findings {
		if finding.Class == "CH_ERROR_43_CHGEN_TYPED" {
			blind = append(blind, fmt.Sprintf("%s: chgen=%s", finding.Kind, finding.ChgenType))
		}
	}
	blind, err = mapSignatures(blind, spec.PrefixMappings)
	if err != nil {
		return nil, err
	}
	if len(mismatch)+len(blind) == 0 {
		return nil, fmt.Errorf("legacy report has no transferable kind prefix evidence; keep it archive-only and rerun the current profile")
	}
	return &LegacyArchive{
		ArtifactSHA256: artifactSHA,
		PopulationID:   legacy.Population.ID,
		FixtureHash:    legacy.FixtureHash,
		FixtureShape:   legacy.FixtureSignature,
		CHVersion:      legacy.CHVersion,
		ServerRun:      legacy.ServerRun,
		Seed:           legacy.Seed,
		Generated:      legacy.Generated,
		Mismatch:       mismatch,
		Blind43:        blind,
	}, nil
}

// MapRetirementAudit applies an explicit prefix mapping and refuses a
// collision. It preserves each historical reason and measured version.
func MapRetirementAudit(retired map[string]*RetirementRecord, mappings []PrefixMapping) (map[string]*RetirementRecord, error) {
	out := make(map[string]*RetirementRecord, len(retired))
	for source, record := range retired {
		mapped, err := mapSignature(source, mappings)
		if err != nil {
			return nil, err
		}
		if _, exists := out[mapped]; exists {
			return nil, fmt.Errorf("retirement prefix mapping collides at %q", mapped)
		}
		copyRecord := *record
		out[mapped] = &copyRecord
	}
	return out, nil
}

// ConvertLegacyBaselineRetirements extracts retirement audit data from one
// exact allowlisted grammar baseline. It transfers no accepted signature.
func ConvertLegacyBaselineRetirements(data []byte, spec LegacyBaselineConversionSpec) (map[string]*RetirementRecord, error) {
	sum := sha256.Sum256(data)
	artifactSHA := hex.EncodeToString(sum[:])
	if artifactSHA != spec.ArtifactSHA256 || !validSHA256(spec.ArtifactSHA256) {
		return nil, fmt.Errorf("legacy baseline SHA-256 %s is not allowlisted", artifactSHA)
	}
	var legacy struct {
		Version    int `json:"version"`
		UnionCells map[string]struct {
			FixtureHash      string                       `json:"fixture_hash"`
			FixtureSignature string                       `json:"fixture_signature"`
			Retired          map[string]*RetirementRecord `json:"retired"`
		} `json:"union_cells"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("decode allowlisted legacy baseline: %w", err)
	}
	if legacy.Version != 2 {
		return nil, fmt.Errorf("allowlisted legacy baseline has version %d, want 2", legacy.Version)
	}
	cell, ok := legacy.UnionCells[spec.SourceCell]
	if !ok {
		return nil, fmt.Errorf("allowlisted legacy baseline has no source cell %q", spec.SourceCell)
	}
	shapeSum := sha256.Sum256([]byte(cell.FixtureSignature))
	if cell.FixtureHash != spec.FixtureHash || hex.EncodeToString(shapeSum[:]) != spec.FixtureShapeSHA256 {
		return nil, fmt.Errorf("legacy baseline fixture does not match the allowlisted conversion spec")
	}
	return MapRetirementAudit(cell.Retired, spec.PrefixMappings)
}

func mapSignatures(signatures []string, mappings []PrefixMapping) ([]string, error) {
	seen := map[string]string{}
	for _, source := range signatures {
		mapped, err := mapSignature(source, mappings)
		if err != nil {
			return nil, err
		}
		if prior, exists := seen[mapped]; exists && prior != source {
			return nil, fmt.Errorf("kind-prefix mapping collides: %q and %q become %q", prior, source, mapped)
		}
		seen[mapped] = source
	}
	out := make([]string, 0, len(seen))
	for mapped := range seen {
		out = append(out, mapped)
	}
	sort.Strings(out)
	return out, nil
}

func mapSignature(signature string, mappings []PrefixMapping) (string, error) {
	for _, mapping := range mappings {
		if mapping.From != "" && strings.HasPrefix(signature, mapping.From) {
			return mapping.To + strings.TrimPrefix(signature, mapping.From), nil
		}
	}
	return "", fmt.Errorf("signature %q does not match an allowlisted kind prefix", signature)
}
