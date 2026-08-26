// Package typeboundary captures a durable snapshot of the ClickHouse type
// boundary and compares two such snapshots.
//
// The type oracle (typeoracle_fuzz_test.go) already measures this
// boundary, but by RANDOM SAMPLING over a grammar. Its own counts move with
// the uptime of the server on one and the same commit, because the random
// draw itself is a
// second source of change: the same seed still draws the same expressions,
// but the SERVER answers a shifting subset of them as it warms, which moves
// which class an expression falls into.
//
// A probe removes that second source. Every query in Catalog is FIXED text,
// chosen by a person and committed to this file. The same fixed query, sent
// twice to one warm server, gives the same verdict both times (measured on
// ClickHouse 25.8.29.51, see probe_test.go). A probe artifact is therefore
// comparable across two runs of ONE server without an uptime tolerance, and
// a difference between two probes taken from servers of different versions
// names a real change in the type boundary, not sampling noise.
//
// A probe is not a replacement for the type oracle. It is a small, hand-
// picked set of known interesting cells — the families the oracle has
// already shown to move across a server boundary (trim/FixedString,
// greatest/least over SimpleAggregateFunction, cityHash64 over arrays,
// Decimal/Enum comparison) plus a spread of ordinary type rules — kept as a
// fast, exact tripwire. The full sweep stays the tool that FINDS a new
// family; the probe is the tool that NOTICES one has moved.
package typeboundary

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

// Cell is one committed probe query.
//
// Name identifies the cell across artifacts and across time; it MUST be
// stable, because Compare keys on it. Query is one full SELECT, run over the
// columns of Schema (see schema.go), and it must ask for both the analysed
// type and an execution witness so that a type the server would refuse to
// compute at run time is not recorded as if it were safe. See
// exprToQuery for the exact shape.
type Cell struct {
	Name  string
	Query string
}

// Verdict is what one cell answered on one server.
type Verdict struct {
	// TypeName is the toTypeName() answer, empty when the query errored.
	TypeName string `json:"type_name,omitempty"`
	// ErrorCode is the ClickHouse numeric error code, 0 when the query
	// succeeded. A code is compared, never the free-text message: the
	// message wording is not a contract and changes between versions
	// without the underlying rule changing.
	ErrorCode int `json:"error_code,omitempty"`
}

func (v Verdict) String() string {
	if v.ErrorCode != 0 {
		return fmt.Sprintf("CH_ERROR(%d)", v.ErrorCode)
	}
	if v.TypeName == "" {
		return "EMPTY"
	}
	return v.TypeName
}

// Equal reports whether two verdicts name the same boundary answer.
func (v Verdict) Equal(o Verdict) bool {
	return v.TypeName == o.TypeName && v.ErrorCode == o.ErrorCode
}

// Client runs probe queries against one ClickHouse HTTP endpoint. It talks
// the same POST-with-database protocol as the type oracle's own client
// (typeoracle_fuzz_test.go, chOracle), kept as a small copy here
// because that file is a _test.go file behind a build tag and cannot be
// imported.
type Client struct {
	URL      string
	Database string
	HTTP     *http.Client
}

// NewClient builds a Client with a sane default timeout.
func NewClient(baseURL, database string) *Client {
	return &Client{
		URL:      baseURL,
		Database: database,
		HTTP:     &http.Client{Timeout: 60 * time.Second},
	}
}

// exec sends one query, bound to the client's database, and returns its raw
// TSV body, or an error that carries the ClickHouse numeric code when the
// server itself refused the query.
func (c *Client) exec(ctx context.Context, query string) (string, error) {
	return c.execIn(ctx, c.Database, query)
}

// adminExec sends a query with no database bound.
func (c *Client) adminExec(ctx context.Context, query string) (string, error) {
	return c.execIn(ctx, "", query)
}

func (c *Client) execIn(ctx context.Context, database, query string) (string, error) {
	separator := "/?"
	if strings.Contains(c.URL, "?") {
		separator = "&"
	}
	params := url.Values{"default_format": []string{"TabSeparatedRaw"}}
	if database != "" {
		params.Set("database", database)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+separator+params.Encode(), strings.NewReader(query))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", &chError{code: parseCHCode(string(body)), message: strings.TrimSpace(string(body))}
	}
	return strings.TrimRight(string(body), "\n"), nil
}

// chError carries the ClickHouse numeric error code out of a failed query.
type chError struct {
	code    int
	message string
}

func (e *chError) Error() string { return e.message }

// parseCHCode extracts the numeric code from a ClickHouse error body, which
// starts "Code: <n>. DB::Exception: ...". A body that does not match the
// shape gives code 0, which Verdict never confuses with a real code, because
// a real ClickHouse code is always positive; this project has never observed
// code 0 on the wire.
func parseCHCode(body string) int {
	const prefix = "Code: "
	if !strings.HasPrefix(body, prefix) {
		return 0
	}
	rest := body[len(prefix):]
	end := strings.IndexByte(rest, '.')
	if end < 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		return 0
	}
	return n
}

// ServerInfo names the server run that a probe measured, in the same shape
// the type oracle already uses (internal/oraclereport.Report.ServerRun /
// ServerUptimeS): the boot moment in Unix seconds, stable while the server
// lives and different after every restart, plus the uptime for a human
// reader. ClickHouse 25.8 has no getServerUUID (code 46), so the boot moment
// computed from now() and uptime() is the identity this project uses instead.
type ServerInfo struct {
	Version   string `json:"version"`
	ServerRun int64  `json:"server_run"`
	UptimeS   int64  `json:"server_uptime_s"`
}

// ReadServerInfo reads the version and the boot-moment identity of the
// server the client points at.
func ReadServerInfo(ctx context.Context, c *Client) (ServerInfo, error) {
	version, err := c.adminExec(ctx, "SELECT version()")
	if err != nil {
		return ServerInfo{}, fmt.Errorf("read version: %w", err)
	}
	raw, err := c.adminExec(ctx,
		"SELECT toUnixTimestamp(now() - toIntervalSecond(toUInt32(uptime()))), toUInt32(uptime())")
	if err != nil {
		return ServerInfo{}, fmt.Errorf("read server run: %w", err)
	}
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) != 2 {
		return ServerInfo{}, fmt.Errorf("unexpected server_run answer %q", raw)
	}
	boot, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("parse boot moment %q: %w", fields[0], err)
	}
	uptime, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("parse uptime %q: %w", fields[1], err)
	}
	return ServerInfo{Version: strings.TrimSpace(version), ServerRun: boot, UptimeS: uptime}, nil
}
