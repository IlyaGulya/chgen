package functionrules_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/IlyaGulya/chgen/internal/functionrules"
)

func FuzzCompactEvidence(f *testing.F) {
	for _, path := range []string{"../../testdata/clickhouse-function-rules.json", "../../testdata/clickhouse-string-function-rules.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(`{"format":2}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 65536 {
			return
		}
		checkEvidenceRoundTrip(t, data)
		// Re-sign mutated documents as well, so fuzzing can reach plan,
		// index and witness validation instead of stopping at the checksum.
		var document map[string]json.RawMessage
		if json.Unmarshal(data, &document) != nil || document == nil {
			return
		}
		resignEvidence(t, document)
		resigned, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		checkEvidenceRoundTrip(t, resigned)
	})
}

func checkEvidenceRoundTrip(t *testing.T, data []byte) {
	t.Helper()
	report, err := functionrules.Decode(data)
	if err != nil {
		return
	}
	compact, err := functionrules.EncodeCompact(report)
	if err != nil {
		t.Fatalf("accepted evidence cannot be encoded: %v", err)
	}
	actual, err := functionrules.Decode(compact)
	if err != nil {
		t.Fatalf("encoded evidence cannot be decoded: %v", err)
	}
	before, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("compact round trip lost an accepted witness")
	}
}
