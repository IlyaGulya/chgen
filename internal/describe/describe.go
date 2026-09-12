// Package describe provides explicit, bounded server-assisted type discovery.
// It never applies schema inputs or promotes analysis into execution evidence.
package describe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/IlyaGulya/chgen/internal/engine"
)

type Options struct {
	Server, Database, User, Password string
}

type Column struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Annotation string `json:"annotation,omitempty"`
	Note       string `json:"note,omitempty"`
}

type Report struct {
	Provenance    string   `json:"provenance"`
	ServerVersion string   `json:"server_version"`
	QuerySHA256   string   `json:"query_sha256"`
	Columns       []Column `json:"columns"`
	Warning       string   `json:"warning"`
}

const maxResponseBytes = 4 << 20

func Query(ctx context.Context, options Options, sql string) (Report, error) {
	statement, err := engine.PrepareDescribeSQL(sql)
	if err != nil {
		return Report{}, err
	}
	endpoint, err := url.Parse(options.Server)
	if err != nil || endpoint.Host == "" || endpoint.Scheme != "http" && endpoint.Scheme != "https" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return Report{}, fmt.Errorf("server must be an explicit HTTP(S) endpoint without credentials, query parameters, or fragment; use CHGEN_DESCRIBE_USER and CHGEN_DESCRIBE_PASSWORD for authentication")
	}
	params := url.Values{"readonly": {"1"}, "max_execution_time": {"10"}, "wait_end_of_query": {"1"}}
	if options.Database != "" {
		params.Set("database", options.Database)
	}
	endpoint.RawQuery = params.Encode()
	client := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	request := func(sql string, target any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(sql))
		if err != nil {
			return fmt.Errorf("create analysis request: %w", err)
		}
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		if options.User != "" || options.Password != "" {
			req.SetBasicAuth(options.User, options.Password)
		}
		response, err := client.Do(req)
		if err != nil {
			// url.Error includes the URL. Keep credentials and endpoint details
			// out of diagnostic text; retain the underlying cancellation cause.
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				err = urlErr.Err
			}
			return fmt.Errorf("server analysis request: %w", err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		if err != nil {
			return fmt.Errorf("read analysis response: %w", err)
		}
		if len(data) > maxResponseBytes {
			return fmt.Errorf("analysis response exceeds %d bytes", maxResponseBytes)
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("server analysis refused: HTTP %d, ClickHouse code %q (server prose omitted; inspect server logs)", response.StatusCode, response.Header.Get("X-ClickHouse-Exception-Code"))
		}
		if err := json.Unmarshal(data, target); err != nil {
			return fmt.Errorf("invalid JSON analysis response: %w", err)
		}
		return nil
	}
	var identity struct {
		Data []struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := request("SELECT version() AS version FORMAT JSON", &identity); err != nil {
		return Report{}, err
	}
	if len(identity.Data) != 1 || identity.Data[0].Version == "" {
		return Report{}, fmt.Errorf("analysis response has no server version")
	}
	var description struct {
		Data []Column `json:"data"`
	}
	if err := request(statement, &description); err != nil {
		return Report{}, err
	}
	if len(description.Data) == 0 {
		return Report{}, fmt.Errorf("analysis response has no result columns")
	}
	names := make(map[string]int)
	for _, column := range description.Data {
		names[column.Name]++
	}
	for i := range description.Data {
		column := &description.Data[i]
		// Annotation and Note are locally derived, never server authority.
		column.Annotation, column.Note = "", ""
		if column.Name == "" || column.Type == "" {
			return Report{}, fmt.Errorf("analysis returned an incomplete column")
		}
		annotation, err := engine.ResultTypeAnnotation(column.Name, column.Type)
		if err != nil || names[column.Name] != 1 {
			column.Note = "No annotation proposed: use a unique simple alias and a ClickHouse type with a supported Go mapping."
			continue
		}
		column.Annotation = annotation
	}
	hash := sha256.Sum256([]byte(sql))
	return Report{
		Provenance: "server-analysis", ServerVersion: identity.Data[0].Version,
		QuerySHA256: hex.EncodeToString(hash[:]), Columns: description.Data,
		Warning: "Analysis types only, not execution, value correctness, or proof of a whole function family. Proposed annotations still obey normal result-contract placement and argument checks. Use real fixture columns, not just constants, and reverify on server upgrades.",
	}, nil
}
