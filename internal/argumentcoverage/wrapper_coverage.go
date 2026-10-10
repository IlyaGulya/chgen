package argumentcoverage

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/IlyaGulya/chgen/internal/functionrules"
)

// WrapperReport has its own denominator: grid and profile cases overlap.
type WrapperReport struct {
	Version     string                    `json:"clickhouse_version"`
	Digest      string                    `json:"evidence_sha256"`
	Scope       string                    `json:"scope"`
	Total       int                       `json:"total"`
	Counts      map[string]int            `json:"counts"`
	EntryCounts map[string]map[string]int `json:"entry_counts"`
	Cells       []WrapperCell             `json:"cells"`
}

type WrapperCell struct {
	Coordinate string `json:"coordinate"`
	Status     string `json:"status"`
	ServerType string `json:"server_type"`
	ServerCode string `json:"server_code,omitempty"`
	ChgenType  string `json:"chgen_type"`
	Expression string `json:"expression"`
}

// WrapperCoverage preserves existing oracle verdicts, including mismatches.
// It does not replace the roster parity or live comparison gates.
func WrapperCoverage(data []byte) (WrapperReport, error) {
	result := WrapperReport{Digest: fmt.Sprintf("%x", sha256.Sum256(data)), Scope: "committed grid coordinates and SQL; analysis only, not runtime evidence", Counts: map[string]int{}, EntryCounts: map[string]map[string]int{}, Cells: []WrapperCell{}}
	wantCounts := map[string]int{}
	wantTotal := -1
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			parts := strings.Split(strings.TrimPrefix(line, "# "), "\t")
			switch parts[0] {
			case "clickhouse_version":
				if len(parts) != 2 || result.Version != "" {
					return result, fmt.Errorf("invalid grid version header")
				}
				result.Version = parts[1]
			case "cells":
				if len(parts) != 2 || wantTotal != -1 {
					return result, fmt.Errorf("invalid grid size header")
				}
				value, err := strconv.Atoi(parts[1])
				if err != nil {
					return result, err
				}
				wantTotal = value
			case "verdict":
				if len(parts) != 3 {
					return result, fmt.Errorf("invalid grid verdict header")
				}
				if _, exists := wantCounts[parts[1]]; exists {
					return result, fmt.Errorf("duplicate grid verdict header")
				}
				value, err := strconv.Atoi(parts[2])
				if err != nil || value <= 0 {
					return result, fmt.Errorf("invalid grid verdict count")
				}
				wantCounts[parts[1]] = value
			}
			continue
		}
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 6 || parts[0] == "" || parts[5] == "" || seen[parts[0]] {
			return result, fmt.Errorf("invalid or duplicate grid cell %q", line)
		}
		seen[parts[0]] = true
		cell := WrapperCell{parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]}
		if cell.ServerCode != "" {
			code, err := strconv.Atoi(cell.ServerCode)
			if err != nil || code <= 0 || cell.ServerType != "<refused>" {
				return result, fmt.Errorf("invalid server refusal in %s", cell.Coordinate)
			}
		} else if cell.ServerType == "" || cell.ServerType == "<refused>" {
			return result, fmt.Errorf("missing server type in %s", cell.Coordinate)
		}
		if cell.ChgenType == "" {
			return result, fmt.Errorf("missing chgen outcome in %s", cell.Coordinate)
		}
		switch cell.Status {
		case "AGREE", "BOTH_REFUSE", "CHGEN_REFUSES_SERVER_ACCEPTS", "MISMATCH", "CHGEN_TYPES_SERVER_REFUSES", "CHGEN_TYPES_SERVER_REFUSES_PAIR", "EXCLUDED":
		default:
			return result, fmt.Errorf("unknown grid verdict %s", cell.Status)
		}
		serverRefused, chgenRefused := cell.ServerCode != "", cell.ChgenType == "<refused>"
		contradiction := false
		switch cell.Status {
		case "AGREE", "MISMATCH":
			contradiction = serverRefused || chgenRefused
		case "BOTH_REFUSE":
			contradiction = !serverRefused || !chgenRefused
		case "CHGEN_REFUSES_SERVER_ACCEPTS":
			contradiction = serverRefused || !chgenRefused
		case "CHGEN_TYPES_SERVER_REFUSES", "CHGEN_TYPES_SERVER_REFUSES_PAIR":
			contradiction = !serverRefused || chgenRefused
		}
		if contradiction {
			return result, fmt.Errorf("contradictory verdict in %s", cell.Coordinate)
		}
		result.Cells = append(result.Cells, cell)
		result.Counts[cell.Status]++
		address := strings.Split(cell.Coordinate, "/")
		if len(address) < 3 || address[0] == "" || address[1] == "" {
			return result, fmt.Errorf("invalid grid coordinate %s", cell.Coordinate)
		}
		entry := address[0] + "/" + address[1]
		if result.EntryCounts[entry] == nil {
			result.EntryCounts[entry] = map[string]int{}
		}
		result.EntryCounts[entry][cell.Status]++
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	result.Total = len(result.Cells)
	if result.Version != functionrules.Version || result.Total == 0 || result.Total != wantTotal || !reflect.DeepEqual(result.Counts, wantCounts) {
		return result, fmt.Errorf("incomplete grid provenance or counts")
	}
	return result, nil
}
