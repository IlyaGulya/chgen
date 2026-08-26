package conformance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTPServer implements Server through ClickHouse HTTP TabSeparatedRaw.
type HTTPServer struct {
	URL      string
	Database string
	Client   *http.Client
}

// NewHTTPServer creates a server with a bounded request timeout.
func NewHTTPServer(baseURL, database string) *HTTPServer {
	return &HTTPServer{URL: baseURL, Database: database, Client: &http.Client{Timeout: 120 * time.Second}}
}

// Analyze asks only for the analysed type tree.
func (s *HTTPServer) Analyze(ctx context.Context, input Input) (string, error) {
	if input.Query != "" {
		results, err := s.AnalyzeResults(ctx, input)
		if err != nil {
			return "", err
		}
		return results[0].Type, nil
	}
	query := "SELECT toTypeName(" + input.Expression + ")"
	if input.Table != "" {
		query += " FROM " + input.Table
	}
	out, err := s.exec(ctx, s.Database, query)
	return strings.TrimSpace(out), err
}

// AnalyzeResults asks for all result names and types in projection order.
func (s *HTTPServer) AnalyzeResults(ctx context.Context, input Input) ([]RawResultType, error) {
	out, err := s.exec(ctx, s.Database, "DESCRIBE TABLE ("+input.Query+")")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	results := make([]RawResultType, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			return nil, fmt.Errorf("statement analysis has %d fields", len(fields))
		}
		results = append(results, RawResultType{Name: strings.TrimSpace(fields[0]), Type: strings.TrimSpace(fields[1])})
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("statement analysis returned no results")
	}
	return results, nil
}

// Execute independently runs the expression over a real row.
func (s *HTTPServer) Execute(ctx context.Context, input Input) error {
	if input.Query != "" {
		_, err := s.exec(ctx, s.Database, input.Query)
		return err
	}
	query := "SELECT ignore(" + input.Expression + ")"
	if input.Table != "" {
		query += " FROM " + input.Table
	}
	_, err := s.exec(ctx, s.Database, query)
	return err
}

// Info reads the version and process boot identity.
func (s *HTTPServer) Info(ctx context.Context) (ServerInfo, error) {
	version, err := s.exec(ctx, "", "SELECT version()")
	if err != nil {
		return ServerInfo{}, err
	}
	raw, err := s.exec(ctx, "", "SELECT toUnixTimestamp(now() - toIntervalSecond(toUInt32(uptime()))), toUInt32(uptime())")
	if err != nil {
		return ServerInfo{}, err
	}
	fields := strings.Fields(raw)
	if len(fields) != 2 {
		return ServerInfo{}, fmt.Errorf("server identity has %d fields", len(fields))
	}
	serverRun, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return ServerInfo{}, err
	}
	uptime, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return ServerInfo{}, err
	}
	return ServerInfo{Version: strings.TrimSpace(version), ServerRun: serverRun, UptimeS: uptime}, nil
}

func (s *HTTPServer) exec(ctx context.Context, database, query string) (string, error) {
	params := url.Values{"default_format": []string{"TabSeparatedRaw"}}
	if database != "" {
		params.Set("database", database)
	}
	separator := "/?"
	if strings.Contains(s.URL, "?") {
		separator = "&"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+separator+params.Encode(), strings.NewReader(query))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "text/plain")
	response, err := s.Client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", &HTTPError{Status: response.StatusCode, ClickHouseCode: parseCode(string(body)), Message: strings.TrimSpace(string(body))}
	}
	return strings.TrimRight(string(body), "\n"), nil
}

// HTTPError carries the stable numeric ClickHouse code.
type HTTPError struct {
	Status         int
	ClickHouseCode int
	Message        string
}

func (e *HTTPError) Error() string { return e.Message }

// Code returns the ClickHouse error code.
func (e *HTTPError) Code() int { return e.ClickHouseCode }

func parseCode(body string) int {
	if !strings.HasPrefix(body, "Code: ") {
		return 0
	}
	rest := strings.TrimPrefix(body, "Code: ")
	end := strings.IndexByte(rest, '.')
	if end < 0 {
		return 0
	}
	value, _ := strconv.Atoi(strings.TrimSpace(rest[:end]))
	return value
}
