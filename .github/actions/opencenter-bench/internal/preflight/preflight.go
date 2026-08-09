// Package preflight answers "is everything here?" without changing anything.
//
// Every probe comes from config/prerequisites.yaml and is run as an ordinary
// shell command with a short deadline. Probes run against the real
// environment, not the sandbox: the question is whether this machine has kind,
// kubectl and a working cloud profile, and a sandbox would hide the answer.
package preflight

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
)

// Status is the outcome of one probe.
type Status string

const (
	// StatusPresent means the probe answered successfully.
	StatusPresent Status = "present"
	// StatusMissing means it did not.
	StatusMissing Status = "missing"
	// StatusTimeout means the probe never returned, which for a credential
	// check usually means an endpoint is unreachable.
	StatusTimeout Status = "timeout"
)

// Result is one prerequisite, checked.
type Result struct {
	spec.Prerequisite
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Millis int64  `json:"millis"`
}

// Report is a whole sweep.
type Report struct {
	Scope   string   `json:"scope"`
	Results []Result `json:"results"`
	Present int      `json:"present"`
	Missing int      `json:"missing"`
}

// Satisfied reports whether every prerequisite an environment names is present.
// It returns the missing ones so the caller can say which.
func (r *Report) Satisfied(required []string) (bool, []Result) {
	var missing []Result
	for _, result := range r.Results {
		for _, id := range required {
			if result.ID == id && result.Status != StatusPresent {
				missing = append(missing, result)
			}
		}
	}
	return len(missing) == 0, missing
}

// Run probes every prerequisite, or only those an environment needs when scope
// names one.
func Run(ctx context.Context, loaded *spec.Spec, scope string) *Report {
	report := &Report{Scope: scope}

	for _, group := range loaded.Prerequisites {
		for _, item := range group.Items {
			if scope != "" && !needs(item, scope) {
				continue
			}
			report.Results = append(report.Results, probe(ctx, item))
		}
	}
	for _, result := range report.Results {
		if result.Status == StatusPresent {
			report.Present++
		} else {
			report.Missing++
		}
	}
	return report
}

// One probes a single prerequisite by id.
func One(ctx context.Context, loaded *spec.Spec, id string) (Result, bool) {
	item, ok := loaded.PrerequisiteIndex()[id]
	if !ok {
		return Result{}, false
	}
	return probe(ctx, item), true
}

func needs(item spec.Prerequisite, scope string) bool {
	for _, id := range item.NeededFor {
		if id == scope {
			return true
		}
	}
	return false
}

func probe(ctx context.Context, item spec.Prerequisite) Result {
	// Ten seconds is long enough for a token request against a healthy
	// endpoint and short enough that a dead one does not stall the console.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	started := time.Now()
	command := exec.CommandContext(ctx, "bash", "-c", item.Check)
	output, err := command.CombinedOutput()
	elapsed := time.Since(started)

	result := Result{Prerequisite: item, Millis: elapsed.Milliseconds()}
	result.Detail = firstMeaningfulLine(string(output))

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		result.Status = StatusTimeout
		result.Detail = "the check did not return within 10s"
	case err != nil || result.Detail == "":
		result.Status = StatusMissing
		if result.Detail == "" {
			result.Detail = "not found"
		}
	default:
		result.Status = StatusPresent
	}
	return result
}

// firstMeaningfulLine is what the console shows beside a present item: the
// version, the path, the project id. A probe that prints a banner first would
// otherwise report the banner.
func firstMeaningfulLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if len(trimmed) > 90 {
			trimmed = trimmed[:90] + "..."
		}
		return trimmed
	}
	return ""
}
