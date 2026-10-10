package main

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

type suite struct {
	ID          string                 `json:"id"`
	Description string                 `json:"description"`
	HTTP        bool                   `json:"requires_http,omitempty"`
	Native      bool                   `json:"requires_native,omitempty"`
	Run         func(*runner, options) `json:"-"`
}

func catalog() []suite {
	return []suite{
		{"offline", "Existing offline verification, syntax and API coverage", false, false, (*runner).offline},
		{"minimum", "Build and test with the minimum supported Go", false, false, (*runner).minimum},
		{"compatibility", "Compile generated code for supported Go/driver pairs", false, false, (*runner).compatibility},
		{"fuzz", "Mutate SQL through the public library pipeline", false, false, (*runner).fuzz},
		{"types", "Pinned API, generated runtime and type-oracle union gate", true, true, (*runner).types},
		{"execution", "Existing execution oracle and baseline gate", true, true, (*runner).execution},
		{"boundary", "Deterministic boundary probe and baseline gate", true, false, (*runner).boundary},
		{"versions", "Executed and sampled cross-version comparison", true, false, (*runner).versions},
	}
}

type options struct {
	http, native, candidate, fuzzTime, seedText, goVersion, driver string
	seeds                                                          []int
	samples, execSamples, explorationSeed                          int
	deep                                                           bool
}

func parseOptions() *options {
	var o options
	flag.StringVar(&o.http, "http", os.Getenv("CHGEN_ORACLE_URL"), "disposable pinned ClickHouse HTTP endpoint")
	flag.StringVar(&o.native, "native", os.Getenv("CHGEN_CONTRACT_NATIVE"), "pinned ClickHouse native host:port")
	flag.StringVar(&o.candidate, "candidate-http", "", "disposable ClickHouse 24.8.14.39 HTTP endpoint")
	flag.StringVar(&o.fuzzTime, "fuzz-time", "30s", "native fuzz budget (deep defaults to 10m)")
	flag.StringVar(&o.seedText, "seeds", "1,7,42,99", "type-oracle seeds, including every fixed regression seed")
	flag.IntVar(&o.samples, "samples", 2000, "type-oracle sample count (deep defaults to 10000)")
	flag.IntVar(&o.execSamples, "exec-samples", 300, "execution-oracle random cases (deep defaults to 2000)")
	flag.IntVar(&o.explorationSeed, "exploration-seed", 0, "extra deep seed; default is today's UTC YYYYMMDD, recorded in report")
	flag.StringVar(&o.goVersion, "go", "", "single supported compatibility Go version; requires -driver")
	flag.StringVar(&o.driver, "driver", "", "single supported compatibility driver version; requires -go")
	return &o
}

func (o *options) finish(profile string) error {
	o.deep = profile == "deep"
	if o.explorationSeed == 0 {
		o.explorationSeed, _ = strconv.Atoi(time.Now().UTC().Format("20060102"))
	}
	if o.deep {
		provided := make(map[string]bool)
		flag.Visit(func(f *flag.Flag) { provided[f.Name] = true })
		if !provided["fuzz-time"] {
			o.fuzzTime = "10m"
		}
		if !provided["samples"] {
			o.samples = 10000
		}
		if !provided["exec-samples"] {
			o.execSamples = 2000
		}
	}
	if o.samples < 1 || o.execSamples < 1 || o.explorationSeed < 1 {
		return fmt.Errorf("sample counts and exploration seed must be positive")
	}
	if (o.goVersion == "") != (o.driver == "") {
		return fmt.Errorf("-go and -driver must be supplied together")
	}
	for _, text := range strings.Split(o.seedText, ",") {
		seed, err := strconv.Atoi(text)
		if err != nil || seed < 1 || slices.Contains(o.seeds, seed) {
			return fmt.Errorf("seeds must be unique positive integers")
		}
		o.seeds = append(o.seeds, seed)
	}
	for _, fixed := range []int{1, 7, 42, 99} {
		if !slices.Contains(o.seeds, fixed) {
			return fmt.Errorf("seed list must retain fixed regression seed %d", fixed)
		}
	}
	if o.deep && !slices.Contains(o.seeds, o.explorationSeed) {
		o.seeds = append(o.seeds, o.explorationSeed)
	}
	return nil
}

func selection(profile, selected string) ([]string, error) {
	if profile != "quick" && profile != "full" && profile != "deep" {
		return nil, fmt.Errorf("unknown profile %q", profile)
	}
	var ids []string
	if selected == "" {
		if profile == "quick" {
			ids = []string{"offline", "fuzz"}
		} else {
			for _, suite := range catalog() {
				ids = append(ids, suite.ID)
			}
		}
	} else {
		ids = strings.Split(selected, ",")
	}
	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] || !slices.ContainsFunc(catalog(), func(s suite) bool { return s.ID == id }) {
			return nil, fmt.Errorf("unknown or duplicate suite %q", id)
		}
		seen[id] = true
	}
	return ids, nil
}

func validEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

func (o options) publicConfiguration() map[string]string {
	config := map[string]string{
		"clickhouse": pinnedClickHouse, "candidate_clickhouse": "24.8.14.39", "runner_go": runtime.Version(),
		"seeds": fmt.Sprint(o.seeds), "samples": strconv.Itoa(o.samples), "exec_samples": strconv.Itoa(o.execSamples),
		"fuzz_time": o.fuzzTime, "exploration_seed": strconv.Itoa(o.explorationSeed),
		"go": o.goVersion, "driver": o.driver,
	}
	for name, endpoint := range map[string]string{"http": o.http, "candidate_http": o.candidate} {
		if validEndpoint(endpoint) {
			config[name] = endpoint
		}
	}
	if _, _, err := net.SplitHostPort(o.native); err == nil {
		config["native"] = o.native
	}
	return config
}

func (r *runner) runSuite(id string, o options) {
	for _, suite := range catalog() {
		if suite.ID != id {
			continue
		}
		if suite.HTTP {
			if !validEndpoint(o.http) {
				r.refuse("server", "live verification requires -http with an HTTP endpoint without credentials or query parameters")
				return
			}
			if suite.Native {
				if _, _, err := net.SplitHostPort(o.native); err != nil {
					r.refuse("server", "live runtime verification requires -native host:port")
					return
				}
			}
			if !r.server(o.http, pinnedClickHouse) {
				return
			}
			if id == "versions" {
				if !validEndpoint(o.candidate) {
					r.refuse("candidate", "version comparison requires -candidate-http for ClickHouse 24.8.14.39")
					return
				}
				if !r.server(o.candidate, "24.8.14.39") {
					return
				}
			}
		}
		suite.Run(r, o)
		return
	}
}
