package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/IlyaGulya/chgen/internal/drivercompat"
)

func suiteKey(s suiteResult) string {
	if s.ID == "compatibility" && s.Configuration["go"] != "" {
		return s.ID + "/" + s.Configuration["go"] + "/" + s.Configuration["driver"]
	}
	return s.ID
}

func expectedCI() []string {
	var ids []string
	for _, suite := range catalog() {
		if suite.ID == "compatibility" {
			for _, cell := range drivercompat.Supported() {
				ids = append(ids, "compatibility/"+cell.MinimumGo+"/"+cell.DriverVersion)
			}
		} else {
			ids = append(ids, suite.ID)
		}
	}
	return ids
}

func mergeReports(input, output, expect string) int {
	out, err := prepareReport(output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	r := runner{out: out, report: summary{Version: 1, Profile: "merged"}}
	var paths []string
	err = filepath.WalkDir(input, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == "summary.json" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		r.mergeRefusal("input", "cannot read report bundles")
	}
	slices.Sort(paths)
	seen := make(map[string]bool)
	for i, path := range paths {
		data, err := os.ReadFile(path)
		var report summary
		if err != nil || json.Unmarshal(data, &report) != nil || report.Version != 1 || len(report.Suites) == 0 {
			r.mergeRefusal("bundle"+strconv.Itoa(i), "invalid or empty verification report")
			continue
		}
		for _, result := range report.Suites {
			if result.Configuration == nil {
				result.Configuration = report.Configuration
			}
			key := suiteKey(result)
			if seen[key] {
				r.mergeRefusal(key, "duplicate suite report")
				continue
			}
			seen[key] = true
			if !slices.ContainsFunc(catalog(), func(s suite) bool { return s.ID == result.ID }) || !validReportSuite(result) {
				r.mergeRefusal(key, "invalid suite verdict or missing checks")
				continue
			}
			r.report.Suites = append(r.report.Suites, result)
			r.current = &r.report.Suites[len(r.report.Suites)-1]
			prefix := filepath.Join("runs", strconv.Itoa(i))
			for j, check := range result.Checks {
				if check.Log != "" {
					copied, err := copyEvidence(filepath.Dir(path), out, prefix, check.Log, true)
					if err != nil {
						r.refuse("evidence", "missing or unsafe command log")
						continue
					}
					r.current.Checks[j].Log = copied
				}
			}
			r.current.Artifacts = nil
			for _, artifact := range result.Artifacts {
				copied, err := copyEvidence(filepath.Dir(path), out, prefix, artifact, false)
				if err != nil {
					r.refuse("evidence", "missing or unsafe required artifact")
					continue
				}
				r.current.Artifacts = append(r.current.Artifacts, copied)
			}
		}
	}
	var required []string
	if expect == "ci" {
		required = expectedCI()
	} else if expect != "" {
		required = strings.Split(expect, ",")
	}
	for _, key := range required {
		if !seen[key] {
			r.mergeRefusal(key, "required suite report is missing")
		}
	}
	if len(paths) == 0 {
		r.mergeRefusal("input", "no verification reports were found")
	}
	if err := r.save(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fmt.Printf("Merged verification %s; report: %s\n", r.report.Status, filepath.Join(out, "summary.md"))
	return reportExit(r.report)
}

func validReportSuite(s suiteResult) bool {
	if len(s.Checks) == 0 || (s.Status != "passed" && s.Status != "failed" && s.Status != "refused") {
		return false
	}
	status := "passed"
	for _, c := range s.Checks {
		switch c.Status {
		case "passed":
			if c.ExitCode != 0 {
				return false
			}
		case "observed":
			if c.ExitCode != 0 && c.ExitCode != 1 {
				return false
			}
		case "failed":
			status = combineStatus(status, "failed")
		case "refused":
			status = "refused"
		case "blocked":
			if status == "passed" {
				status = "refused"
			}
		default:
			return false
		}
	}
	// A suite can also refuse due to missing prerequisites or evidence.
	return status == s.Status || s.Status == "refused"
}

func (r *runner) mergeRefusal(id, message string) {
	r.report.Suites = append(r.report.Suites, suiteResult{ID: id, Status: "refused"})
	r.current = &r.report.Suites[len(r.report.Suites)-1]
	r.refuse("report", message)
}

func copyEvidence(source, out, prefix, name string, allowEmpty bool) (string, error) {
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("evidence path must be relative")
	}
	path := filepath.Join(source, name)
	info, err := os.Lstat(path)
	// A successful compiler can be silent. Required data artifacts, unlike
	// command logs, must still contain evidence.
	if err != nil || !info.Mode().IsRegular() || (!allowEmpty && info.Size() == 0) {
		return "", fmt.Errorf("evidence must be a regular file with the required content")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	target := filepath.Join(prefix, name)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(out, target)), 0o700); err != nil {
		return "", err
	}
	return target, os.WriteFile(filepath.Join(out, target), data, 0o600)
}
