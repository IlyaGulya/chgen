package typespecimen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IlyaGulya/chgen/internal/apiinventory"
)

var clickHouseCodePattern = regexp.MustCompile(`Code: ([0-9]+)\.`)

// Collector measures type specimens through the ClickHouse HTTP interface.
type Collector struct {
	URL    string
	Client *http.Client
}

// NewCollector creates a collector with a bounded request timeout.
func NewCollector(serverURL string) *Collector {
	return &Collector{URL: serverURL, Client: &http.Client{Timeout: 120 * time.Second}}
}

// Collect creates, seeds, analyses, and executes one real column per family.
func (c *Collector) Collect(ctx context.Context, inventory apiinventory.Inventory) (Catalog, error) {
	if err := inventory.Validate(); err != nil {
		return Catalog{}, err
	}
	source, err := c.source(ctx)
	if err != nil {
		return Catalog{}, err
	}
	if source != inventory.Source {
		return Catalog{}, fmt.Errorf("server source %+v does not match inventory source %+v", source, inventory.Source)
	}
	digest, err := InventoryDigest(inventory)
	if err != nil {
		return Catalog{}, err
	}
	database := fmt.Sprintf("chgen_type_specimens_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := c.exec(ctx, "", "CREATE DATABASE "+database); err != nil {
		return Catalog{}, err
	}
	defer c.exec(context.Background(), "", "DROP DATABASE IF EXISTS "+database)

	catalog := Catalog{FormatVersion: FormatVersion, Source: source, InventorySHA256: digest}
	families := append([]apiinventory.DataTypeFamily(nil), inventory.DataTypeFamilies.Canonical...)
	sort.Slice(families, func(a, b int) bool { return families[a].Name < families[b].Name })
	for _, family := range families {
		entry := Entry{Family: family.Name, Column: ColumnName(family.Name), DeclaredType: CandidateType(family.Name)}
		if err := c.measure(ctx, database, &entry); err != nil {
			return Catalog{}, err
		}
		catalog.Entries = append(catalog.Entries, entry)
	}
	if err := catalog.ValidateAgainst(inventory); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func (c *Collector) measure(ctx context.Context, database string, entry *Entry) error {
	const table = "probe"
	if _, err := c.exec(ctx, database, "DROP TABLE IF EXISTS "+table); err != nil {
		return err
	}
	create := fmt.Sprintf("CREATE TABLE %s (seed UInt8, value %s) ENGINE=Memory", table, entry.DeclaredType)
	if _, err := c.exec(ctx, database, create); err != nil {
		return setRefusal(entry, "create", err)
	}
	seed := "INSERT INTO probe (seed) VALUES (1)"
	if _, err := c.exec(ctx, database, seed); err != nil {
		return setRefusal(entry, "seed", err)
	}
	analysis := "SELECT toTypeName(value) FROM probe"
	observed, err := c.exec(ctx, database, analysis)
	if err != nil {
		return setRefusal(entry, "analysis", err)
	}
	execution := "SELECT ignore(value) FROM probe"
	if _, err := c.exec(ctx, database, execution); err != nil {
		return setRefusal(entry, "execution", err)
	}
	entry.Status = Accepted
	entry.ObservedType = strings.TrimSpace(observed)
	entry.SeedWitness = seed
	entry.AnalysisWitness = analysis
	entry.ExecutionWitness = execution
	return nil
}

func setRefusal(entry *Entry, stage string, err error) error {
	match := clickHouseCodePattern.FindStringSubmatch(err.Error())
	if len(match) != 2 {
		return fmt.Errorf("%s refusal for %s has no ClickHouse error code", stage, entry.Family)
	}
	code, parseErr := strconv.Atoi(match[1])
	if parseErr != nil || code <= 0 {
		return fmt.Errorf("%s refusal for %s has an invalid ClickHouse error code", stage, entry.Family)
	}
	entry.Status = ExplicitlyRefused
	entry.RefusalStage = stage
	entry.RefusalCode = code
	entry.RefusalClass = clickHouseRefusalClass
	return nil
}

func (c *Collector) source(ctx context.Context) (apiinventory.Source, error) {
	body, err := c.exec(ctx, "", "SELECT version() AS version, toUInt64(revision()) AS revision, buildId() AS build_id FORMAT JSONEachRow")
	if err != nil {
		return apiinventory.Source{}, err
	}
	var source apiinventory.Source
	if err := json.Unmarshal([]byte(body), &source); err != nil {
		return apiinventory.Source{}, err
	}
	return source, nil
}

func (c *Collector) exec(ctx context.Context, database, query string) (string, error) {
	serverURL, err := url.Parse(c.URL)
	if err != nil {
		return "", err
	}
	parameters := serverURL.Query()
	parameters.Set("default_format", "TabSeparatedRaw")
	if database != "" {
		parameters.Set("database", database)
	}
	serverURL.RawQuery = parameters.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL.String(), strings.NewReader(query))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "text/plain")
	response, err := c.Client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	return strings.TrimRight(string(body), "\n"), nil
}
