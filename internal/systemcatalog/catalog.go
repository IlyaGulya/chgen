// Package systemcatalog stores and measures the pinned system-table schemas.
package systemcatalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Column struct {
	Table    string `json:"table"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Position uint64 `json:"position"`
}

type Snapshot struct {
	Version string   `json:"version"`
	Columns []Column `json:"columns"`
}

//go:embed catalog.json
var pinned []byte

func Pinned() (Snapshot, error) {
	var snapshot Snapshot
	err := json.Unmarshal(pinned, &snapshot)
	return snapshot, err
}

// Collect is read-only. Run SYSTEM FLUSH LOGS on the disposable collection
// server beforehand so configured log-table schemas are instantiated.
func Collect(ctx context.Context, serverURL, expectedVersion string) (Snapshot, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	query := func(sql string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(sql))
		if err != nil {
			return nil, err
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("ClickHouse returned HTTP %d: %s", response.StatusCode, data)
		}
		return data, nil
	}
	version, err := query("SELECT version() FORMAT TabSeparated")
	if err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(string(version)) != expectedVersion {
		return Snapshot{}, fmt.Errorf("system catalog server version %s, want %s", strings.TrimSpace(string(version)), expectedVersion)
	}
	data, err := query("SELECT table, name, type, position FROM system.columns WHERE database = 'system' ORDER BY table, position FORMAT JSON")
	if err != nil {
		return Snapshot{}, err
	}
	var result struct {
		Data []Column `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return Snapshot{}, err
	}
	if len(result.Data) == 0 {
		return Snapshot{}, fmt.Errorf("system catalog contains no columns")
	}
	return Snapshot{Version: expectedVersion, Columns: result.Data}, nil
}

func Marshal(snapshot Snapshot) ([]byte, error) {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
