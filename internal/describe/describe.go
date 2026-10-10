// Package describe provides explicit, bounded server-assisted type discovery.
// It never applies schema inputs or promotes analysis into execution evidence.
package describe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/IlyaGulya/chgen/internal/diagnostic"
	"github.com/IlyaGulya/chgen/internal/engine"
)

type Options struct {
	Server, Database, User, Password string
	Parameters                       map[string]string
	ExternalTables                   []ExternalTable
}

// ExternalTable describes an empty request-scoped analysis input. No table is
// created in the server database and no production rows are sent for analysis.
type ExternalTable struct{ Name, Structure string }

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

// ServerRefusal distinguishes a server verdict from a transport failure.
type ServerRefusal struct {
	HTTPStatus int
	Code       string
}

func (e *ServerRefusal) Error() string {
	return fmt.Sprintf("server analysis refused: HTTP %d, ClickHouse code %q (server prose omitted; inspect server logs)", e.HTTPStatus, e.Code)
}

// Diagnostic uses protocol status and exception codes, never server prose: messages may
// contain query text, parameter examples, or server paths.
func (e *ServerRefusal) Diagnostic() diagnostic.Detail {
	d := diagnostic.Detail{Code: "server-analysis-refused", Status: diagnostic.Unknown, Stage: "analysis",
		Hint: "ClickHouse refused analysis. Inspect its server logs for details; server prose is omitted to avoid exposing SQL and values. No generated files were changed."}
	if e.HTTPStatus == http.StatusUnauthorized || e.HTTPStatus == http.StatusForbidden || e.Code == "516" || e.Code == "497" {
		d.Code = "server-access-denied"
		d.Hint = "Check CHGEN_DESCRIBE_USER and CHGEN_DESCRIBE_PASSWORD and the account's read permissions on your test database. Do not put credentials in -server URL."
		return d
	}
	switch e.Code {
	case "60", "81":
		d.Code = "server-schema-missing"
		d.Hint = "Check -database and prepare the test database with the required tables and migrations. chgen does not execute schema inputs on the server."
	case "62":
		d.Code, d.Status = "server-sql-refused", diagnostic.Invalid
		d.Hint = "Check SQL syntax against the selected ClickHouse version. The server itself rejected parsing; a result-type annotation cannot repair it."
	case "46":
		d.Code = "server-function-unavailable"
		d.Hint = "Check the function spelling and whether it exists in your test ClickHouse version. Server analysis cannot add functions to ClickHouse."
	case "159":
		d.Code = "server-analysis-timeout"
		d.Hint = "Analysis exceeded the server time limit. Check test-server load and expensive constant expressions or table functions; do not switch to production merely to obtain a contract."
	}
	return d
}

func Query(ctx context.Context, options Options, sql string) (Report, error) {
	statement, err := engine.PrepareDescribeSQL(sql)
	if err != nil {
		return Report{}, err
	}
	return queryPrepared(ctx, options, sql, statement)
}

// NativeQuery delegates SQL grammar to ClickHouse. The independent lexical
// guard admits a single read query, with an exact set of native parameter
// examples. It does not turn analysis into execution evidence.
func NativeQuery(ctx context.Context, options Options, sql string) (Report, error) {
	body, parameters, err := engine.PrepareServerSelect(sql)
	if err != nil {
		return Report{}, err
	}
	if len(options.Parameters) != len(parameters) {
		return Report{}, fmt.Errorf("supply exactly one example for each native parameter")
	}
	options.Parameters = maps.Clone(options.Parameters)
	for _, param := range parameters {
		if _, ok := options.Parameters[param.Name]; !ok {
			return Report{}, fmt.Errorf("missing example for native parameter %s", param.Name)
		}
		if param.Type.Name == "String" {
			options.Parameters[param.Name] = engine.EncodeServerString(options.Parameters[param.Name])
		}
	}
	return queryPrepared(ctx, options, sql, "DESCRIBE TABLE (\n"+body+"\n) FORMAT JSON")
}

func queryPrepared(ctx context.Context, options Options, sql, statement string) (Report, error) {
	endpoint, err := url.Parse(options.Server)
	if err != nil || endpoint.Host == "" || endpoint.Scheme != "http" && endpoint.Scheme != "https" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return Report{}, fmt.Errorf("server must be an explicit HTTP(S) endpoint without credentials, query parameters, or fragment; use CHGEN_DESCRIBE_USER and CHGEN_DESCRIBE_PASSWORD for authentication")
	}
	params := url.Values{"readonly": {"1"}, "max_execution_time": {"10"}, "wait_end_of_query": {"1"}}
	for name, value := range options.Parameters {
		params.Set("param_"+name, value)
	}
	if options.Database != "" {
		params.Set("database", options.Database)
	}
	endpoint.RawQuery = params.Encode()
	client := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	request := func(sql string, target any) error {
		requestURL := *endpoint
		var body io.Reader = strings.NewReader(sql)
		contentType := "text/plain; charset=utf-8"
		if sql == statement && len(options.ExternalTables) > 0 {
			var buffer bytes.Buffer
			writer := multipart.NewWriter(&buffer)
			if err := writer.WriteField("query", sql); err != nil {
				return err
			}
			queryParams := requestURL.Query()
			for _, table := range options.ExternalTables {
				queryParams.Set(table.Name+"_structure", table.Structure)
				queryParams.Set(table.Name+"_format", "TabSeparated")
				if _, err := writer.CreateFormFile(table.Name, table.Name+".tsv"); err != nil {
					return err
				}
			}
			if err := writer.Close(); err != nil {
				return err
			}
			requestURL.RawQuery = queryParams.Encode()
			body, contentType = &buffer, writer.FormDataContentType()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), body)
		if err != nil {
			return fmt.Errorf("create analysis request: %w", err)
		}
		req.Header.Set("Content-Type", contentType)
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
			return diagnostic.With(fmt.Errorf("server analysis request: %w", err), diagnostic.Detail{
				Code: "server-connection-failed", Status: diagnostic.Unknown, Stage: "analysis",
				Hint: "Check the test ClickHouse HTTP(S) endpoint, network access, TLS, and server availability. The native driver port is not an HTTP endpoint. Cancellation or a client deadline can also stop analysis; no fallback connection is made.",
			})
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
			return &ServerRefusal{HTTPStatus: response.StatusCode, Code: response.Header.Get("X-ClickHouse-Exception-Code")}
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
