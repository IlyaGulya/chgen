package apiinventory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Collector reads inventory rows through the ClickHouse HTTP interface.
type Collector struct {
	URL    string
	Client *http.Client
}

// NewCollector creates a collector with a bounded request timeout.
func NewCollector(serverURL string) *Collector {
	return &Collector{URL: serverURL, Client: &http.Client{Timeout: 120 * time.Second}}
}

// Collect reads all inventory classes from one server build.
func (c *Collector) Collect(ctx context.Context) (Inventory, error) {
	var inventory Inventory
	inventory.FormatVersion = FormatVersion
	if err := c.query(ctx, sourceQuery, &inventory.Source); err != nil {
		return Inventory{}, fmt.Errorf("read source identity: %w", err)
	}

	var functions []Function
	if err := c.queryRows(ctx, functionsQuery, &functions); err != nil {
		return Inventory{}, fmt.Errorf("read functions: %w", err)
	}
	for _, function := range functions {
		set := &inventory.Functions.UserDefined
		if function.Origin == "System" {
			set = &inventory.Functions.BuiltIn
		}
		if function.AliasTo == "" {
			set.Canonical = append(set.Canonical, function)
		} else {
			set.Aliases = append(set.Aliases, function)
		}
	}

	var dataTypes []DataTypeFamily
	if err := c.queryRows(ctx, dataTypesQuery, &dataTypes); err != nil {
		return Inventory{}, fmt.Errorf("read data type families: %w", err)
	}
	for _, dataType := range dataTypes {
		if dataType.AliasTo == "" {
			inventory.DataTypeFamilies.Canonical = append(inventory.DataTypeFamilies.Canonical, dataType)
		} else {
			inventory.DataTypeFamilies.Aliases = append(inventory.DataTypeFamilies.Aliases, dataType)
		}
	}
	if err := c.queryRows(ctx, aggregateFunctionCombinatorsQuery, &inventory.AggregateFunctionCombinators); err != nil {
		return Inventory{}, fmt.Errorf("read aggregate function combinators: %w", err)
	}
	if err := c.queryRows(ctx, tableFunctionsQuery, &inventory.TableFunctions); err != nil {
		return Inventory{}, fmt.Errorf("read table functions: %w", err)
	}
	if err := c.queryRows(ctx, settingsQuery, &inventory.Settings); err != nil {
		return Inventory{}, fmt.Errorf("read settings: %w", err)
	}
	inventory.Normalize()
	if err := inventory.Validate(); err != nil {
		return Inventory{}, err
	}
	return inventory, nil
}

func (c *Collector) query(ctx context.Context, query string, target any) error {
	body, err := c.execute(ctx, query)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(target); err != nil {
		body.Close()
		return err
	}
	return body.Close()
}

func (c *Collector) queryRows(ctx context.Context, query string, target any) error {
	body, err := c.execute(ctx, query)
	if err != nil {
		return err
	}
	defer body.Close()
	decoder := json.NewDecoder(body)
	switch rows := target.(type) {
	case *[]Function:
		for decoder.More() {
			var row Function
			if err := decoder.Decode(&row); err != nil {
				return err
			}
			*rows = append(*rows, row)
		}
	case *[]DataTypeFamily:
		for decoder.More() {
			var row DataTypeFamily
			if err := decoder.Decode(&row); err != nil {
				return err
			}
			*rows = append(*rows, row)
		}
	case *[]AggregateFunctionCombinator:
		for decoder.More() {
			var row AggregateFunctionCombinator
			if err := decoder.Decode(&row); err != nil {
				return err
			}
			*rows = append(*rows, row)
		}
	case *[]TableFunction:
		for decoder.More() {
			var row TableFunction
			if err := decoder.Decode(&row); err != nil {
				return err
			}
			*rows = append(*rows, row)
		}
	case *[]Setting:
		for decoder.More() {
			var row Setting
			if err := decoder.Decode(&row); err != nil {
				return err
			}
			*rows = append(*rows, row)
		}
	default:
		return fmt.Errorf("unsupported row target %T", target)
	}
	return nil
}

func (c *Collector) execute(ctx context.Context, query string) (io.ReadCloser, error) {
	if strings.TrimSpace(c.URL) == "" {
		return nil, fmt.Errorf("server URL is empty")
	}
	serverURL, err := url.Parse(c.URL)
	if err != nil {
		return nil, err
	}
	parameters := serverURL.Query()
	parameters.Set("default_format", "JSONEachRow")
	serverURL.RawQuery = parameters.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL.String(), strings.NewReader(query))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "text/plain")
	response, err := c.Client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusOK {
		return response.Body, nil
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, readErr
	}
	return nil, fmt.Errorf("ClickHouse returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
}

const sourceQuery = `SELECT
    version() AS version,
    toUInt64(revision()) AS revision,
    buildId() AS build_id`

const functionsQuery = `SELECT
    name,
    toBool(is_aggregate) AS is_aggregate,
    toBool(case_insensitive) AS case_insensitive,
    alias_to,
    toString(origin) AS origin,
    introduced_in
FROM system.functions
ORDER BY origin, alias_to != '', name`

const dataTypesQuery = `SELECT
    name,
    toBool(case_insensitive) AS case_insensitive,
    alias_to
FROM system.data_type_families
ORDER BY alias_to != '', name`

const aggregateFunctionCombinatorsQuery = `SELECT
    name,
    toBool(is_internal) AS is_internal
FROM system.aggregate_function_combinators
ORDER BY name`

const tableFunctionsQuery = `SELECT
    name,
    toBool(allow_readonly) AS allow_readonly
FROM system.table_functions
ORDER BY name`

const settingsQuery = `SELECT
    name,
    type,
    default,
    alias_for,
    toBool(is_obsolete) AS is_obsolete,
    toString(tier) AS tier
FROM system.settings
ORDER BY name`
