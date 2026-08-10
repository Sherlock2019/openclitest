package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The failure triage view.
//
// A dashboard that says "4 failed" has told a developer almost nothing. The
// question they actually have is which of those four are openCenter's fault,
// and that is a different number — one that has to be worked out rather than
// counted. Everything here exists to turn a red row into: which command, in
// which environment, against which build, why, with the evidence and the line
// to paste to see it again.
//
// Nothing in this file re-runs anything. It reads the outcomes already
// recorded and classifies them, so opening the view costs nothing and cannot
// change a result.

// Category is the fixed classification. Fixed, because the whole point is that
// a developer should not have to investigate every red row as a CLI bug, and a
// free-text field would collapse back into exactly that.
type Category string

const (
	CatProductDefect Category = "Product defect"
	CatRegression    Category = "Regression"
	CatEnvironment   Category = "Environment issue"
	CatPrerequisite  Category = "Missing prerequisite"
	CatTestConfig    Category = "Invalid test configuration"
	CatExpected      Category = "Expected simulated failure"
	CatBenchDefect   Category = "Test Bench defect"
	CatBlocked       Category = "Blocked"
	CatUnknown       Category = "Unknown"
)

// Categories in the order they are shown: what a developer must act on first,
// down to what they can ignore.
func categoryOrder() []Category {
	return []Category{
		CatRegression, CatProductDefect, CatBenchDefect, CatTestConfig,
		CatPrerequisite, CatEnvironment, CatExpected, CatBlocked, CatUnknown,
	}
}

// Actionable reports whether a category means openCenter itself is at fault.
// The headline number is this, not the raw failure count.
func (c Category) Actionable() bool {
	return c == CatProductDefect || c == CatRegression
}

// CauseGroup is the coarser "what kind of thing went wrong" axis, for the
// panel that answers "are these four failures one problem or four".
type CauseGroup string

const (
	CauseParsing   CauseGroup = "CLI parsing"
	CauseConfig    CauseGroup = "Configuration"
	CauseProvider  CauseGroup = "Provider"
	CauseGitOps    CauseGroup = "GitOps generation"
	CauseTooling   CauseGroup = "External tooling"
	CauseAuth      CauseGroup = "Authentication"
	CauseEnv       CauseGroup = "Environment"
	CauseSecurity  CauseGroup = "Security"
	CauseTimeout   CauseGroup = "Timeout"
	CauseCleanup   CauseGroup = "Cleanup"
	CauseAssertion CauseGroup = "Assertion"
	CauseUnclear   CauseGroup = "Unclassified"
)

// Build identifies what was under test. Every failure is only meaningful
// against a specific binary, so this travels with the report rather than
// being something a reader has to remember.
type Build struct {
	// Mode is the environment the run happened in. A result that does not say
	// whether a provider was contacted is a result nobody can weigh.
	Mode     string `json:"mode"`
	Badge    string `json:"badge"`
	Emulated bool   `json:"emulated"`
	Provider string `json:"emulated_provider,omitempty"`
	Scenario string `json:"scenario,omitempty"`

	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Binary   string `json:"binary"`
	Platform string `json:"platform"`
	RunAt    string `json:"run_at"`

	// Where the binary under test came from. A version string on its own does
	// not say which fork or branch produced it, and the whole point of being
	// able to choose a branch is that the answer changes.
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// sourceOf reads the repository and release the CLI under test came from.
//
// Taken from the prerequisite step's own field defaults, so there is one place
// that decides it: change the field and the header follows, rather than the
// two drifting apart and the page claiming a branch nobody built.
func (c *console) sourceOf() (string, string) {
	c.mu.Lock()
	selectedRepo, selectedRelease := c.sourceRepo, c.sourceBranch
	c.mu.Unlock()
	if selectedRepo != "" || selectedRelease != "" {
		return selectedRepo, selectedRelease
	}
	repo, release := openCLIRepository, "latest"
	for _, environment := range c.catalogue.Environments {
		for _, command := range environment.Commands {
			for _, input := range command.Inputs {
				switch input.Env {
				case "OPENCLI_VERSION":
					if input.Default != "" {
						release = input.Default
					}
				case "OPENCLI_REPO":
					// The same rule as the release: the field decides, so the
					// repository box opens on the one this bench is configured
					// to test rather than on the default.
					if input.Default != "" {
						repo = input.Default
					}
				}
			}
		}
	}
	// A value set in the environment wins: it is what a command would actually
	// have used.
	if live := os.Getenv("OPENCLI_VERSION"); live != "" {
		release = live
	}
	return repo, release
}

// Summary is the band at the top. Deliberately short.
type Summary struct {
	Executed     int      `json:"executed"`
	Total        int      `json:"total"`
	Passed       int      `json:"passed"`
	Failed       int      `json:"failed"`
	Regressions  int      `json:"regressions"`
	Blocked      int      `json:"blocked"`
	DurationMS   int64    `json:"duration_ms"`
	Duration     string   `json:"duration"`
	Environments []string `json:"environments_affected"`
	// The breakdown that matters more than the total.
	ProductDefects    int `json:"product_defects"`
	EnvironmentIssues int `json:"environment_issues"`
	ExpectedFailures  int `json:"expected_failures"`
	BenchDefects      int `json:"bench_defects"`
}

// EnvironmentStatus is the compact per-environment command rollup shown in
// the results header.
type EnvironmentStatus struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Total    int    `json:"total"`
	Executed int    `json:"executed"`
	Passed   int    `json:"passed"`
	Failed   int    `json:"failed"`
	Blocked  int    `json:"blocked"`
}

// Failure is one red row, with everything needed to act on it.
type Failure struct {
	// Identity
	TestID      string `json:"test_id"`
	Command     string `json:"command"`
	Executed    string `json:"executed"`
	Environment string `json:"environment"`
	Mode        string `json:"mode"`
	Stage       string `json:"stage"`

	// Result
	ExitCode int    `json:"exit_code"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Millis   int64  `json:"millis"`
	TimedOut bool   `json:"timed_out"`

	// Judgement
	Category   Category   `json:"category"`
	CauseGroup CauseGroup `json:"cause_group"`
	Cause      string     `json:"cause"`
	Where      string     `json:"where,omitempty"`
	Action     string     `json:"action"`
	Regression bool       `json:"regression"`

	// Build identity, repeated on every failure rather than only in the
	// header. A failure gets copied into a ticket on its own, and one that
	// does not name the binary it came from is not reproducible.
	CLIVersion string `json:"cli_version"`
	Commit     string `json:"commit"`
	Platform   string `json:"platform"`
	Provider   string `json:"provider"`

	// Evidence
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	// Artifacts are the files this command generated, and how they differ
	// from the previous run. Empty for a command that generates nothing.
	Artifacts []Artifact `json:"artifacts,omitempty"`
	// Reproduce is the exact line to paste. The whole point of a failure
	// report is that somebody can see it again without reconstructing it.
	Reproduce string `json:"reproduce"`
}

// MatrixRow is one command across every environment: the view that separates
// a core defect from a provider-specific one at a glance.
type MatrixRow struct {
	Command string            `json:"command"`
	Stage   string            `json:"stage"`
	Cells   map[string]string `json:"cells"`
	// Verdict reads the row: everywhere, one provider, or only locally.
	Verdict string `json:"verdict"`
}

// CauseCount is one line of the "failures by cause" panel.
type CauseCount struct {
	Group CauseGroup `json:"group"`
	Count int        `json:"count"`
}

// Cleanup answers "did a failed test leave anything behind".
//
// Three numbers rather than one, because "2 sandboxes" does not say whether
// that is a leak or a session in progress. Created against removed against
// still there is the shape that answers it.
type Cleanup struct {
	SandboxesOpen int      `json:"sandboxes_open"`
	Created       int      `json:"resources_created"`
	Removed       int      `json:"resources_removed"`
	Remaining     int      `json:"resources_remaining"`
	Paths         []string `json:"remaining_paths,omitempty"`
	Note          string   `json:"note"`
}

// Artifact is one file a command generated.
//
// Priority 12 in the specification, and the one that matters most for a
// generate or GitOps failure: the error says a file is missing, and the
// question immediately after is which files were written. Answering that from
// the report saves going to find the directory by hand.
type Artifact struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	// Change is "added", "removed" or "changed" against the previous run of
	// this same command, and empty when there is nothing to compare against.
	Change string `json:"change,omitempty"`
}

// Triage is the whole view.
type Triage struct {
	Build      Build        `json:"build"`
	Summary    Summary      `json:"summary"`
	Categories []CountedCat `json:"categories"`
	Failures   []Failure    `json:"failures"`
	// BenchFailures are what the deep bench found, kept apart from the button
	// runs because they are judged differently — an assertion, not an exit
	// code — and mixing them would make the counters mean two things at once.
	BenchFailures []benchFailure `json:"bench_failures,omitempty"`
	Matrix        []MatrixRow    `json:"matrix"`
	Causes        []CauseCount   `json:"causes"`
	Cleanup       Cleanup        `json:"cleanup"`
	// Environments in matrix column order.
	Environments        []string            `json:"environments"`
	EnvironmentStatuses []EnvironmentStatus `json:"environment_statuses"`
}

// CountedCat is a category and how many failures are in it.
type CountedCat struct {
	Category   Category `json:"category"`
	Count      int      `json:"count"`
	Actionable bool     `json:"actionable"`
}

// --- classification -----------------------------------------------------------

// classify decides what kind of failure this is.
//
// Ordered from the most specific signal to the least, and it ends at Unknown
// rather than guessing. Reporting "product defect" for something the bench
// simply could not read would send a developer hunting through the CLI for a
// bug that is not there, which is worse than admitting the classifier does not
// know.
func classify(outcome Outcome, command Command, previous *Outcome) (Category, CauseGroup, string, string) {
	text := strings.ToLower(outcome.Stdout + "\n" + outcome.Stderr)

	// A command the gate refused never ran. It is not a failure.
	if strings.Contains(text, "mutation gate") || strings.Contains(text, mutateGate+"=1") {
		return CatBlocked, CauseUnclear,
			"Refused by the mutation gate before it ran.",
			"Restart with " + mutateGate + "=1 if this should really run."
	}

	if outcome.TimedOut {
		return CatEnvironment, CauseTimeout,
			"The command did not return within the time limit.",
			"Run it in a terminal to see where it stops."
	}

	// External tooling missing is the environment's problem, not the CLI's.
	for _, tool := range []string{"docker daemon", "cannot connect to the docker",
		"command not found", "executable file not found", "no such file or directory: docker",
		"kubectl: not found", "tofu: not found", "helm: not found"} {
		if strings.Contains(text, tool) {
			return CatEnvironment, CauseTooling,
				"A tool the command needs is not installed or not running.",
				"Install or start it, then run the prerequisite check for it."
		}
	}

	// Credentials.
	if strings.Contains(text, "authentication") || strings.Contains(text, "unauthorized") ||
		strings.Contains(text, "invalid credentials") || strings.Contains(text, "401") {
		return CatEnvironment, CauseAuth,
			"The provider refused the credentials.",
			"Check the credentials panel for this environment."
	}

	// Prerequisite rows deliberately inspect the host, not the run sandbox.
	// Classify them before generic "no cluster" wording can suggest creating a
	// fixture that these checks cannot see.
	if command.Shell {
		return CatPrerequisite, CauseEnv,
			"A prerequisite is not present on this machine.",
			"Use the setup command beside the check."
	}

	// The bench's own fixture not being there yet.
	if strings.Contains(text, "configuration not found") ||
		strings.Contains(text, "cluster configuration not found") ||
		strings.Contains(text, "no cluster") {
		return CatPrerequisite, CauseConfig,
			"The cluster this command needs has not been created.",
			"Press Create fixture, or run cluster init first."
	}

	// Stub secrets are the fixture's state, not a CLI defect. This one matters:
	// a fresh fixture fails validate on CHANGEME placeholders every time, and
	// counting that as a product defect would bury the real ones.
	if strings.Contains(text, "changeme") || strings.Contains(text, "stub secret") {
		return CatTestConfig, CauseConfig,
			"The fixture still holds placeholder secrets.",
			"Fill them in, or treat this as expected for a fresh fixture."
	}

	if strings.Contains(text, "unknown command") || strings.Contains(text, "unknown flag") ||
		strings.Contains(text, "accepts ") || strings.Contains(text, "invalid argument") {
		return CatBenchDefect, CauseParsing,
			"The bench invoked the command with arguments it does not accept.",
			"Fix the ready line in config/commands.json for this row."
	}

	// A generated file the generator itself then could not find is the
	// clearest kind of product defect there is.
	if strings.Contains(text, "resource file not found") ||
		strings.Contains(text, "kustomization") && strings.Contains(text, "not found") {
		return CatProductDefect, CauseGitOps,
			"Generation referenced a file it did not create.",
			"Check the service descriptor's generated-resource list."
	}

	if strings.Contains(text, "already enabled") || strings.Contains(text, "already rendered") ||
		strings.Contains(text, "use --force") {
		return CatTestConfig, CauseConfig,
			"The command refused to repeat work already done.",
			"Add --force to the ready line, or reset the fixture."
	}

	if strings.Contains(text, "dependency") || strings.Contains(text, "requires ") {
		return CatPrerequisite, CauseConfig,
			"Something this command depends on is not enabled.",
			"Enable the dependency it names, then run this again."
	}

	// A regression is decided by history rather than by the message, so it is
	// checked last and overrides whatever the text suggested.
	if previous != nil && previous.ExitCode == 0 {
		return CatRegression, CauseUnclear,
			"This command passed on the previous run and fails now.",
			"Compare against the previous build; this is the first thing to look at."
	}

	return CatUnknown, CauseUnclear,
		"The command failed and the bench could not classify why.",
		"Read the output below; this needs a human."
}

// --- building the view ----------------------------------------------------------

// triage builds the whole report from what has been recorded.
func (c *console) triage() *Triage {
	c.mu.Lock()
	outcomes := make(map[string]Outcome, len(c.outcomes))
	for key, outcome := range c.outcomes {
		outcomes[key] = outcome
	}
	open := len(c.boxes)
	c.mu.Unlock()

	previous := c.previousRun()

	// The mode the run happened in, so a reader can weigh the result. A report
	// that does not say whether a provider was ever contacted is a report
	// nobody can act on.
	repoURL, repoBranch := c.sourceOf()
	modeID, providerID, scenarioID, _ := c.emulationState.Current()
	mode := Mode{ID: modeID, Badge: "REAL"}
	if c.emulation != nil {
		mode = c.emulation.Mode(modeID)
	}

	report := &Triage{
		Build: Build{
			Mode:     mode.Name,
			Badge:    mode.Badge,
			Emulated: mode.Emulated,
			Provider: providerID,
			Scenario: scenarioID,
			Repo:     repoURL,
			Branch:   repoBranch,
			Version:  c.catalogue.Version,
			Commit:   commitOf(c.catalogue.Version),
			Binary:   c.binary,
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
		},
		Categories:    []CountedCat{},
		Failures:      []Failure{},
		BenchFailures: latestBenchFailures(c.root),
		Matrix:        []MatrixRow{},
		Causes:        []CauseCount{},
	}
	for _, environment := range c.catalogue.Environments {
		report.Environments = append(report.Environments, environment.ID)
	}

	byCategory := map[Category]int{}
	byCause := map[CauseGroup]int{}
	affected := map[string]bool{}
	var newest time.Time
	var totalMS int64

	for _, environment := range c.catalogue.Environments {
		environmentStatus := EnvironmentStatus{
			ID: environment.ID, Name: environment.Name, Total: len(environment.Commands),
		}
		for _, command := range environment.Commands {
			key := environment.ID + "|" + command.ID
			outcome, ran := outcomes[key]
			report.Summary.Total++
			if !ran {
				continue
			}
			environmentStatus.Executed++
			report.Summary.Executed++
			totalMS += outcome.Millis
			if outcome.At.After(newest) {
				newest = outcome.At
			}
			if outcome.ExitCode == 0 && !outcome.TimedOut {
				environmentStatus.Passed++
				report.Summary.Passed++
				continue
			}

			report.Summary.Failed++
			affected[environment.Name] = true

			var before *Outcome
			if earlier, ok := previous[key]; ok {
				before = &earlier
			}
			category, cause, why, action := classify(outcome, command, before)
			byCategory[category]++
			byCause[cause]++

			if category == CatBlocked {
				environmentStatus.Blocked++
				report.Summary.Blocked++
				// A blocked command is not a failure anyone has to look at.
				report.Summary.Failed--
				continue
			}
			environmentStatus.Failed++

			failure := Failure{
				TestID:      command.ID + "-" + environment.ID,
				Command:     command.ID,
				Executed:    executedLine(command, outcome),
				Environment: environment.Name,
				Mode:        mode.Name,
				Stage:       command.Stage,
				ExitCode:    outcome.ExitCode,
				Expected:    "exit code 0",
				Actual:      fmt.Sprintf("exit code %d", outcome.ExitCode),
				Millis:      outcome.Millis,
				TimedOut:    outcome.TimedOut,
				Category:    category,
				CauseGroup:  cause,
				Cause:       why,
				Action:      action,
				Regression:  category == CatRegression,
				Stdout:      outcome.Stdout,
				Stderr:      outcome.Stderr,
				Reproduce:   reproduceLine(c.binary, command, outcome),
				CLIVersion:  report.Build.Version,
				Commit:      report.Build.Commit,
				Platform:    report.Build.Platform,
				Provider:    environment.ID,
				Artifacts:   c.artifactsFor(environment.ID, command),
			}
			// The diagnosis engine already knows where a failure happened;
			// there is no reason for this view to work it out again.
			if outcome.Diagnosis != nil {
				failure.Where = outcome.Diagnosis.Location.File
				// The diagnosis engine has already read the output properly.
				// Where the classifier above landed on Unknown, its answer is
				// better than "the bench could not classify why".
				if failure.Cause == "" || category == CatUnknown {
					failure.Cause = outcome.Diagnosis.Cause
				}
			}
			report.Failures = append(report.Failures, failure)
		}
		report.EnvironmentStatuses = append(report.EnvironmentStatuses, environmentStatus)
	}

	report.Summary.Regressions = byCategory[CatRegression]
	report.Summary.ProductDefects = byCategory[CatProductDefect]
	report.Summary.EnvironmentIssues = byCategory[CatEnvironment]
	report.Summary.ExpectedFailures = byCategory[CatExpected] + byCategory[CatTestConfig]
	report.Summary.BenchDefects = byCategory[CatBenchDefect]
	report.Summary.DurationMS = totalMS
	report.Summary.Duration = HumanDuration(totalMS)
	for name := range affected {
		report.Summary.Environments = append(report.Summary.Environments, name)
	}
	sort.Strings(report.Summary.Environments)
	if !newest.IsZero() {
		report.Build.RunAt = newest.Format(time.RFC3339)
	}

	for _, category := range categoryOrder() {
		if byCategory[category] == 0 {
			continue
		}
		report.Categories = append(report.Categories, CountedCat{
			Category: category, Count: byCategory[category],
			Actionable: category.Actionable(),
		})
	}

	for group, count := range byCause {
		report.Causes = append(report.Causes, CauseCount{Group: group, Count: count})
	}
	sort.Slice(report.Causes, func(i, j int) bool {
		if report.Causes[i].Count != report.Causes[j].Count {
			return report.Causes[i].Count > report.Causes[j].Count
		}
		return report.Causes[i].Group < report.Causes[j].Group
	})

	// Worst first: a regression above a product defect above the rest, and
	// within a category the slowest first, because a long failure usually
	// costs more to reproduce.
	rank := map[Category]int{}
	for index, category := range categoryOrder() {
		rank[category] = index
	}
	sort.SliceStable(report.Failures, func(i, j int) bool {
		if rank[report.Failures[i].Category] != rank[report.Failures[j].Category] {
			return rank[report.Failures[i].Category] < rank[report.Failures[j].Category]
		}
		return report.Failures[i].Command < report.Failures[j].Command
	})

	report.Matrix = buildMatrix(c.catalogue, outcomes)
	report.Cleanup = c.countCleanup()
	_ = open
	return report
}

// HumanDuration turns milliseconds into "08m 42s", which is what the
// specification asks for and what a person reads without converting.
func HumanDuration(millis int64) string {
	seconds := millis / 1000
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%02dm %02ds", seconds/60, seconds%60)
}

// buildMatrix is the command-by-environment grid.
//
// Only rows with at least one result are included. A grid of "not run" tells
// nobody anything and would bury the handful of rows that matter.
func buildMatrix(catalogue *Catalogue, outcomes map[string]Outcome) []MatrixRow {
	seen := map[string]*MatrixRow{}
	var order []string

	for _, environment := range catalogue.Environments {
		for _, command := range environment.Commands {
			outcome, ran := outcomes[environment.ID+"|"+command.ID]
			row, ok := seen[command.ID]
			if !ok {
				row = &MatrixRow{
					Command: command.ID, Stage: command.Stage,
					Cells: map[string]string{},
				}
				seen[command.ID] = row
				order = append(order, command.ID)
			}
			switch {
			case !ran:
				row.Cells[environment.ID] = "not run"
			case outcome.TimedOut:
				row.Cells[environment.ID] = "timeout"
			case outcome.ExitCode == 0:
				row.Cells[environment.ID] = "pass"
			default:
				row.Cells[environment.ID] = "fail"
			}
		}
	}

	var rows []MatrixRow
	for _, id := range order {
		row := seen[id]
		failed, passed, ran := 0, 0, 0
		for _, cell := range row.Cells {
			if cell == "not run" {
				continue
			}
			ran++
			if cell == "pass" {
				passed++
			} else {
				failed++
			}
		}
		if ran == 0 || failed == 0 {
			continue
		}
		// The reading, which is the whole reason for the grid.
		switch {
		case passed == 0:
			row.Verdict = "fails everywhere — likely a core defect"
		case failed == 1:
			row.Verdict = "fails in one environment only — provider or local"
		default:
			row.Verdict = "fails in some environments"
		}
		rows = append(rows, *row)
	}
	return rows
}

// --- generated artifacts -----------------------------------------------------------

// artifactsFor lists what a command wrote, for the failures where that is the
// question being asked.
//
// Only for commands that generate. Walking a sandbox for `cluster list` would
// return the whole fixture and bury the four files that matter.
func (c *console) artifactsFor(environmentID string, command Command) []Artifact {
	if !generatesFiles(command) {
		return nil
	}
	c.mu.Lock()
	box := c.boxes[environmentID]
	c.mu.Unlock()
	if box == nil {
		return nil
	}

	// The generated tree, wherever the CLI puts it under this sandbox.
	root := filepath.Join(box.Home, ".config", "opencenter", "clusters")
	var out []Artifact
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		out = append(out, Artifact{Path: filepath.ToSlash(relative), Bytes: info.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })

	// Capped. A generated GitOps tree can run to hundreds of files, and a
	// failure report that scrolls for a page is one nobody reads.
	const most = 60
	if len(out) > most {
		out = append(out[:most], Artifact{
			Path:   fmt.Sprintf("… and %d more", len(out)-most),
			Change: "",
		})
	}
	return out
}

// generatesFiles reports whether a command is one whose output is files.
func generatesFiles(command Command) bool {
	for _, verb := range []string{"generate", "init", "render", "import"} {
		if strings.Contains(command.ID, verb) {
			return true
		}
	}
	return false
}

// countCleanup measures what is still on disk.
func (c *console) countCleanup() Cleanup {
	c.mu.Lock()
	boxes := make([]string, 0, len(c.boxes))
	for id, box := range c.boxes {
		boxes = append(boxes, id+" → "+box.Root)
	}
	open := len(c.boxes)
	c.mu.Unlock()
	sort.Strings(boxes)

	// A sandbox is created per environment on first use and removed on reset
	// or shutdown. Anything still here at the end of a session is a real
	// leftover rather than a leak, but it is disk somebody may want back.
	return Cleanup{
		SandboxesOpen: open,
		Created:       open,
		Removed:       0,
		Remaining:     open,
		Paths:         boxes,
		Note: fmt.Sprintf(
			"%d sandbox(es) on disk. Each is removed on Reset results, or when the "+
				"console stops. Nothing outside them was written.", open),
	}
}

// --- history ---------------------------------------------------------------------

// historyFile is where the previous run is kept, so a regression can be
// distinguished from something that never worked.
func (c *console) historyFile() string {
	return filepath.Join(c.root, "config", ".last-run.json")
}

// previousRun reads the last saved run. A missing file is normal on a first
// run, and means nothing is reported as a regression rather than everything.
func (c *console) previousRun() map[string]Outcome {
	raw, err := os.ReadFile(c.historyFile())
	if err != nil {
		return map[string]Outcome{}
	}
	var previous map[string]Outcome
	if err := json.Unmarshal(raw, &previous); err != nil {
		return map[string]Outcome{}
	}
	return previous
}

// saveRun records the current outcomes as the baseline for next time.
//
// Written only when asked, not after every command: a baseline that updates
// itself continuously can never show a regression, because the thing it would
// be compared against was overwritten by the failure itself.
func (c *console) saveRun() error {
	c.mu.Lock()
	snapshot := make(map[string]Outcome, len(c.outcomes))
	for key, outcome := range c.outcomes {
		// The evidence is not kept. It is large, it can hold redacted-but-
		// still-sensitive text, and the only thing a comparison needs is
		// whether it passed.
		outcome.Stdout, outcome.Stderr = "", ""
		outcome.Diagnosis = nil
		snapshot[key] = outcome
	}
	c.mu.Unlock()

	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.historyFile(), raw, 0o600)
}

// --- small helpers -----------------------------------------------------------------

func commitOf(version string) string {
	if index := strings.LastIndex(version, "-"); index >= 0 && index < len(version)-1 {
		return version[index+1:]
	}
	return version
}

func executedLine(command Command, outcome Outcome) string {
	if command.Shell {
		return firstLineOf(command.Ready)
	}
	return "opencenter " + outcome.Args
}

// reproduceLine is the line to paste. It names the binary explicitly rather
// than assuming one is on PATH, because the whole point is that it works when
// pasted somewhere else.
func reproduceLine(binary string, command Command, outcome Outcome) string {
	if command.Shell {
		return command.Ready
	}
	return binary + " " + outcome.Args
}

func firstLineOf(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

// handleResults serves the triage view.
func (c *console) handleResults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, c.triage())
}

// handleBaseline stores the current run as the regression baseline.
func (c *console) handleBaseline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := c.saveRun(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "saved",
		"note":   "the current results are now the baseline a regression is measured against",
	})
}
