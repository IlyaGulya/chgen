package engine

import (
	"encoding/json"
	"os"
	"testing"
)

type typeTransportEvidence struct {
	FormatVersion int `json:"format_version"`
	Cases         []struct {
		ID             string `json:"id"`
		ClickHouseType string `json:"clickhouse_type"`
		Path           string `json:"path"`
		Outcome        string `json:"outcome"`
	} `json:"cases"`
}

func TestPinnedTypeTransportEvidenceHasRequiredHazards(t *testing.T) {
	data, err := os.ReadFile(moduleRootPath("testdata", "clickhouse-type-transport-evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence typeTransportEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.FormatVersion != 1 {
		t.Fatalf("format version = %d, want 1", evidence.FormatVersion)
	}
	want := map[string]bool{
		"wide-non-null-nil":      false,
		"wide-signed-overflow":   false,
		"wide-unsigned-negative": false,
		"wide-array-text":        false,
		"wide-map-read":          false,
		"tuple-wide-read":        false,
		"aggregate-state-read":   false,
	}
	for _, item := range evidence.Cases {
		if _, exists := want[item.ID]; !exists {
			continue
		}
		if item.ClickHouseType == "" || item.Path == "" || item.Outcome == "" {
			t.Errorf("evidence case %s is incomplete", item.ID)
		}
		want[item.ID] = true
	}
	for id, found := range want {
		if !found {
			t.Errorf("evidence has no case %s", id)
		}
	}
}
