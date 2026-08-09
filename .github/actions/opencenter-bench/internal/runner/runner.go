// Package runner prepares a world and runs checks in it.
//
// It has two callers. The advanced runner — `bench run --env sim` and the
// per-environment panel in the console — uses Execute below to run one
// environment's checks in any combination. The continuous workflow uses NewLab
// directly and drives the modules itself. Both build their world the same way,
// so isolation cannot be right in one and wrong in the other.
package runner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/checks"
	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
)

// Options describe one run.
type Options struct {
	Root        string
	Binary      string
	Environment string
	// Only limits the run to these check ids. Empty means every applicable check.
	Only []string
	// Categories limits the run to these checklist rows.
	Categories []string
	Mutate     bool
	// SkipSlow drops the checks measured in minutes.
	SkipSlow bool
	// Credentials are extra environment variables handed to the CLI.
	Credentials map[string]string
	// KeepSandbox leaves the throwaway directory in place for inspection.
	KeepSandbox bool
}

// Event is streamed to the console while a run is in progress.
type Event struct {
	Type    string            `json:"type"`
	At      time.Time         `json:"at"`
	Message string            `json:"message,omitempty"`
	Check   *checks.Result    `json:"check,omitempty"`
	Step    *checks.Assertion `json:"step,omitempty"`
	CheckID string            `json:"check_id,omitempty"`
	Report  *Report           `json:"report,omitempty"`
	Total   int               `json:"total,omitempty"`
	Done    int               `json:"done,omitempty"`
}

// CategoryCoverage is one checklist row, with what this run found.
type CategoryCoverage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Checks is how many ran in this run; Available is how many exist for the
	// environment at all, so a filtered run reads as "not run" rather than as
	// a hole in the checklist.
	Checks    int    `json:"checks"`
	Available int    `json:"available"`
	Passed    int    `json:"passed"`
	Failed    int    `json:"failed"`
	Skipped   int    `json:"skipped"`
	Errored   int    `json:"errored"`
	Status    string `json:"status"`
	Coverable bool   `json:"coverable"`
}

// Report is everything a finished run knows.
type Report struct {
	Environment string             `json:"environment"`
	Binary      string             `json:"binary"`
	Version     string             `json:"version"`
	Mutating    bool               `json:"mutating"`
	Started     time.Time          `json:"started"`
	Finished    time.Time          `json:"finished"`
	Millis      int64              `json:"millis"`
	Results     []checks.Result    `json:"results"`
	Coverage    []CategoryCoverage `json:"coverage"`
	Passed      int                `json:"passed"`
	Failed      int                `json:"failed"`
	Skipped     int                `json:"skipped"`
	Errored     int                `json:"errored"`
	SandboxPath string             `json:"sandbox_path,omitempty"`
	Notes       []string           `json:"notes,omitempty"`
}

// OK reports whether the run found nothing wrong.
func (r *Report) OK() bool { return r.Failed == 0 && r.Errored == 0 }

// Plan lists what a run would do, without doing it.
func Plan(loaded *spec.Spec, options Options) ([]checks.Check, error) {
	environment, ok := loaded.Environment(options.Environment)
	if !ok {
		return nil, fmt.Errorf("unknown environment %q", options.Environment)
	}
	return filter(checks.For(environment.ID, options.Mutate), options), nil
}

func filter(selected []checks.Check, options Options) []checks.Check {
	var out []checks.Check
	for _, check := range selected {
		if options.SkipSlow && check.Slow {
			continue
		}
		if len(options.Only) > 0 && !contains(options.Only, check.ID) {
			continue
		}
		if len(options.Categories) > 0 && !contains(options.Categories, check.Category) {
			continue
		}
		out = append(out, check)
	}
	return out
}

// Execute prepares the environment, runs the checks and returns the report.
// emit may be nil.
func Execute(ctx context.Context, loaded *spec.Spec, options Options, emit func(Event)) (*Report, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	send := func(event Event) {
		event.At = time.Now()
		emit(event)
	}

	environment, ok := loaded.Environment(options.Environment)
	if !ok {
		return nil, fmt.Errorf("unknown environment %q", options.Environment)
	}
	if !environment.Mutating {
		options.Mutate = false
	}

	selected := filter(checks.For(environment.ID, options.Mutate), options)
	if len(selected) == 0 {
		return nil, errors.New("no checks match this selection")
	}

	lab, err := NewLab(loaded, LabOptions{
		Root:        options.Root,
		Binary:      options.Binary,
		Environment: options.Environment,
		Mutate:      options.Mutate,
		Credentials: options.Credentials,
		Log: func(format string, args ...any) {
			send(Event{Type: "log", Message: fmt.Sprintf(format, args...)})
		},
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		lab.Close()
		if options.KeepSandbox {
			return
		}
		if err := lab.Remove(); err != nil {
			send(Event{Type: "log", Message: "sandbox cleanup: " + err.Error()})
		}
	}()

	report := &Report{
		Environment: environment.ID,
		Binary:      options.Binary,
		Mutating:    options.Mutate,
		Started:     time.Now(),
		Version:     lab.Version(ctx),
	}
	if options.KeepSandbox {
		report.SandboxPath = lab.Sandbox.Root
	}
	if lab.Describe != "" {
		send(Event{Type: "log", Message: lab.Describe})
	}

	send(Event{Type: "run-start", Total: len(selected),
		Message: fmt.Sprintf("%s · %d checks", environment.Name, len(selected))})

	for index, check := range selected {
		send(Event{Type: "check-start", CheckID: check.ID, Done: index, Total: len(selected),
			Message: check.Name})

		lab.Refresh()
		result := checks.Execute(ctx, check, lab.Env, func(step checks.Assertion) {
			send(Event{Type: "assertion", CheckID: check.ID, Step: &step})
		})
		report.Results = append(report.Results, result)

		send(Event{Type: "check-done", CheckID: check.ID, Check: &result,
			Done: index + 1, Total: len(selected)})

		if ctx.Err() != nil {
			send(Event{Type: "log", Message: "run cancelled"})
			break
		}
	}

	report.Finished = time.Now()
	report.Millis = report.Finished.Sub(report.Started).Milliseconds()
	summarise(report, loaded, environment.ID)

	send(Event{Type: "run-done", Report: report,
		Message: fmt.Sprintf("%d passed, %d failed, %d skipped", report.Passed, report.Failed, report.Skipped)})

	return report, nil
}

func summarise(report *Report, loaded *spec.Spec, environment string) {
	// What exists for this environment, as opposed to what this particular
	// run selected. Without the distinction, a category skipped by --quick
	// looks the same as a category nothing has ever been written for.
	available := checks.Categories(environment)

	byCategory := map[string]*CategoryCoverage{}
	for _, category := range loaded.Categories {
		coverable := false
		for _, id := range category.Environments {
			if id == environment {
				coverable = true
				break
			}
		}
		byCategory[category.ID] = &CategoryCoverage{
			ID: category.ID, Name: category.Name, Coverable: coverable,
			Available: available[category.ID], Status: "uncovered",
		}
	}

	for _, result := range report.Results {
		switch result.Status {
		case checks.StatusPass:
			report.Passed++
		case checks.StatusFail:
			report.Failed++
		case checks.StatusSkip:
			report.Skipped++
		default:
			report.Errored++
		}

		coverage, ok := byCategory[result.Category]
		if !ok {
			coverage = &CategoryCoverage{ID: result.Category, Name: result.Category, Coverable: true}
			byCategory[result.Category] = coverage
		}
		coverage.Checks++
		switch result.Status {
		case checks.StatusPass:
			coverage.Passed++
		case checks.StatusFail:
			coverage.Failed++
		case checks.StatusSkip:
			coverage.Skipped++
		default:
			coverage.Errored++
		}
	}

	for _, coverage := range byCategory {
		switch {
		case coverage.Failed > 0:
			coverage.Status = "fail"
		case coverage.Errored > 0:
			coverage.Status = "error"
		case coverage.Passed > 0:
			coverage.Status = "pass"
		case coverage.Checks > 0:
			coverage.Status = "skip"
		case !coverage.Coverable:
			coverage.Status = "n/a"
		case coverage.Available > 0:
			coverage.Status = "not run"
		default:
			coverage.Status = "uncovered"
		}
		report.Coverage = append(report.Coverage, *coverage)
	}

	order := map[string]int{}
	for index, category := range loaded.Categories {
		order[category.ID] = index
	}
	sort.SliceStable(report.Coverage, func(i, j int) bool {
		return order[report.Coverage[i].ID] < order[report.Coverage[j].ID]
	})
}

func contains(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}
