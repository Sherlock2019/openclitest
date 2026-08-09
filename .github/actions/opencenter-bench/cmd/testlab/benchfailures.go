package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The failures the deep bench found, on the same board as the button runs.
//
// The console judges a command it ran by its exit code, which is the only
// thing a button press can honestly know. The bench asks harder questions —
// whether `--output json` produced JSON, whether a command answered at all —
// and those are the failures worth reading. Until now they were written to
// artifacts/runs and never shown, so a console that had found real defects
// reported "0 failed".
//
// This is also what the CI run reports, because CI runs this same bench. One
// list of failures, from whichever half of the bench found them.

// benchFailure is one failing assertion, flattened out of the run report.
type benchFailure struct {
	Command string `json:"command"`
	Detail  string `json:"detail"`
	Module  string `json:"module"`
	Check   string `json:"check"`
	RunID   string `json:"run_id"`
	// Environment is the bench's own word for where it ran — "sim" for the
	// sandbox, a provider name for a live one.
	Environment string `json:"environment"`
}

// benchReport is the part of reports/report.json this needs. Declared narrowly
// on purpose: a struct mirroring the whole report would break every time the
// bench gained a field.
type benchReport struct {
	ID      string `json:"id"`
	Modules []struct {
		Name    string `json:"name"`
		Results []struct {
			Name        string `json:"name"`
			Category    string `json:"category"`
			Environment string `json:"environment"`
			Status      string `json:"status"`
			Assertions  []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Detail string `json:"detail"`
			} `json:"assertions"`
		} `json:"results"`
	} `json:"modules"`
}

// latestBenchFailures reads the newest run under root and returns what failed.
//
// Errors are swallowed and reported as no failures. A console that refuses to
// draw its results panel because an old artifact directory holds a truncated
// report is worse than one that shows the button runs alone.
func latestBenchFailures(root string) []benchFailure {
	matches, err := filepath.Glob(filepath.Join(root, "artifacts", "runs", "*", "reports", "report.json"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	// Run directories are timestamps, so the newest sorts last by name — no
	// stat call, and no dependence on mtimes that a copy would have rewritten.
	sort.Strings(matches)

	raw, err := os.ReadFile(matches[len(matches)-1])
	if err != nil {
		return nil
	}
	var report benchReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil
	}

	var failures []benchFailure
	for _, module := range report.Modules {
		for _, result := range module.Results {
			if !strings.EqualFold(result.Status, "fail") {
				continue
			}
			for _, assertion := range result.Assertions {
				if !strings.EqualFold(assertion.Status, "fail") {
					continue
				}
				failures = append(failures, benchFailure{
					// The assertion is named with the command it ran, which is
					// what somebody reading a failure needs first.
					Command:     assertion.Name,
					Detail:      assertion.Detail,
					Module:      module.Name,
					Check:       result.Name,
					RunID:       report.ID,
					Environment: result.Environment,
				})
			}
		}
	}
	return failures
}
