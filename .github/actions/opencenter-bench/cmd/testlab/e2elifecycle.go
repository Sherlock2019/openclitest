package main

// The cluster lifecycle, in this console.
//
// The command table above answers "does this command work" — one invocation,
// judged, next. It cannot answer "can this build stand a cluster up, prove it
// healthy, and take it down again without leaving anything behind", because
// that is not a longer list of commands: it is a sequence where each step
// depends on the last, where failure halfway leaves real infrastructure
// running, and where the interesting failures happen in the gaps between
// commands rather than inside them.
//
// So the lifecycle gets its own rail rather than being folded into the seven
// command stages. Seven of the names would have collided — configure, generate,
// deploy, operate, teardown, prerequisites, results — and they do not mean the
// same thing on both sides. "deploy" in the command table is one invocation to
// try; "deploy" in the lifecycle means the run now owes a destroy, whether it
// passed, failed or was cancelled. Merging them by name would have fused two
// vocabularies into one word that means both.
//
// Nothing here reimplements a phase. Every button shells into the same binary
// the GitHub Actions workflow calls: ./bin/opencenter-e2e.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/e2e"
)

// lifecycle is the E2E half of the console's state.
//
// Its own mutex rather than the console's: a lifecycle run takes minutes, and
// holding the lock the command table uses would freeze the rest of the page for
// the duration.
type lifecycle struct {
	mu sync.Mutex

	root   string // the bench root, for locating the binary and artifacts
	binary string // ./bin/opencenter-e2e

	running bool
	cmd     *exec.Cmd
	output  strings.Builder
	runID   string
}

// e2eReportDir is where lifecycle runs are written. Beside the command bench's
// own artifacts, not inside them: the two produce different evidence and a
// reader should not have to tell them apart by filename.
const e2eReportDir = "artifacts"

func newLifecycle(root string) *lifecycle {
	return &lifecycle{
		root:   root,
		binary: filepath.Join(root, "bin", "opencenter-e2e"),
	}
}

func (l *lifecycle) reportRoot() string { return filepath.Join(l.root, e2eReportDir) }

// registerLifecycle adds the endpoints. Kept in one function so the routing
// table in main.go stays readable and this file owns its own surface.
func (l *lifecycle) register(mux *http.ServeMux) {
	mux.HandleFunc("/api/e2e/catalogue", l.handleCatalogue)
	mux.HandleFunc("/api/e2e/state", l.handleState)
	mux.HandleFunc("/api/e2e/runs", l.handleRuns)
	mux.HandleFunc("/api/e2e/run/", l.handleRun)
	mux.HandleFunc("/api/e2e/start", l.handleStart)
	mux.HandleFunc("/api/e2e/stop", l.handleStop)
	mux.HandleFunc("/api/e2e/evidence/", l.handleEvidence)
	// The provider matrix and the coverage it implies. Read across runs rather
	// than out of one, because "is this broken everywhere or only on VMware" is
	// a question no single run can answer.
	mux.HandleFunc("/api/e2e/coverage", l.handleCoverage)
}

// --- the catalogue ---------------------------------------------------------

// handleCatalogue is everything the lifecycle rail and its sections draw
// themselves from.
//
// One request, and the page keeps no copy of any of it. Phases, stages,
// profiles and their colours are read from internal/e2e — the same package the
// CLI and the report read — so the three cannot disagree about what the
// lifecycle is. A hardcoded list in the page is a list that goes stale the
// first time somebody adds a phase.
func (l *lifecycle) handleCatalogue(w http.ResponseWriter, r *http.Request) {
	type phase struct {
		ID       string   `json:"id"`
		Number   int      `json:"number"`
		Title    string   `json:"title"`
		Purpose  string   `json:"purpose"`
		Creates  bool     `json:"creates"`
		Required bool     `json:"required"`
		Needs    []string `json:"needs,omitempty"`
	}
	type stage struct {
		ID      string  `json:"id"`
		Number  int     `json:"number"`
		Name    string  `json:"name"`
		Purpose string  `json:"purpose"`
		Fill    string  `json:"fill,omitempty"`
		OnFill  string  `json:"on_fill,omitempty"`
		New     bool    `json:"new"`
		After   string  `json:"after,omitempty"`
		Phases  []phase `json:"phases"`
	}
	type profile struct {
		Name           string   `json:"name"`
		Infrastructure string   `json:"infrastructure"`
		Provider       string   `json:"provider"`
		ClusterType    string   `json:"cluster_type"`
		Deploys        bool     `json:"deploys"`
		Live           bool     `json:"live"`
		Emulated       bool     `json:"emulated"`
		Tools          []string `json:"tools"`
		Notes          string   `json:"notes"`
		Skips          []string `json:"skips,omitempty"`
	}

	stages := make([]stage, 0, len(e2e.Stages))
	for _, source := range e2e.Stages {
		entry := stage{
			ID: source.ID, Number: source.Number, Name: source.Name,
			Purpose: source.Purpose, Fill: source.Fill, OnFill: source.OnFill,
			New: source.New, After: source.After,
		}
		for _, id := range source.Phases {
			declared, ok := e2e.Lookup(id)
			if !ok {
				continue
			}
			needs := make([]string, 0, len(declared.Needs))
			for _, need := range declared.Needs {
				needs = append(needs, string(need))
			}
			entry.Phases = append(entry.Phases, phase{
				ID: string(declared.ID), Number: declared.Number, Title: declared.Title,
				Purpose: declared.Purpose, Creates: declared.Creates,
				Required: declared.Required, Needs: needs,
			})
		}
		stages = append(stages, entry)
	}

	profiles := make([]profile, 0, len(e2e.Profiles))
	for _, source := range e2e.Profiles {
		skips := make([]string, 0, 6)
		for _, id := range source.SkipsFrom() {
			skips = append(skips, string(id))
		}
		profiles = append(profiles, profile{
			Name: source.Name, Infrastructure: string(source.Infrastructure),
			Provider: string(source.Provider), ClusterType: source.ClusterType,
			Deploys: source.Deploys, Live: source.LiveApproval,
			Emulated: source.Emulated(), Tools: source.Tools, Notes: source.Notes,
			Skips: skips,
		})
	}

	creates, required := 0, 0
	for _, declared := range e2e.Order {
		if declared.Creates {
			creates++
		}
		if declared.Required {
			required++
		}
	}
	host, _ := os.Hostname()

	// Said out loud rather than discovered by a button that does nothing. The
	// binary is built by `mise run build`, and a console that silently fails to
	// start a run because it is missing is a console nobody can debug.
	_, binaryErr := os.Stat(l.binary)

	writeE2EJSON(w, map[string]any{
		"stages":   stages,
		"profiles": profiles,
		"counts": map[string]int{
			"phases": len(e2e.Order), "stages": len(e2e.Stages),
			"profiles": len(e2e.Profiles), "creates": creates,
			"required": required, "always": len(e2e.AlwaysRun()),
		},
		"host": host, "os": runtime.GOOS, "arch": runtime.GOARCH,
		"report_dir": l.reportRoot(),
		"binary":     l.binary,
		"binary_ok":  binaryErr == nil,
		"build_hint": "mise run build",
	})
}

// --- run state -------------------------------------------------------------

func (l *lifecycle) handleState(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	running, runID, output := l.running, l.runID, l.output.String()
	l.mu.Unlock()

	payload := map[string]any{"running": running, "output": output}

	// The rail is read from the run's own state file, which the engine writes
	// after every phase — so the page shows real progress rather than a guess
	// parsed out of the log.
	if runID == "" {
		runID = latestE2ERunID(l.reportRoot())
	}
	if runID != "" {
		if record, err := e2e.LoadRun(filepath.Join(l.reportRoot(), runID)); err == nil {
			type phase struct {
				ID      string `json:"id"`
				State   string `json:"state"`
				Message string `json:"message"`
				Millis  int64  `json:"millis"`
			}
			phases := make([]phase, 0, len(record.Phases))
			for _, result := range record.Phases {
				phases = append(phases, phase{
					string(result.ID), string(result.State), result.Message, result.Millis,
				})
			}
			removed := 0
			for _, resource := range record.Resources {
				if resource.Removed {
					removed++
				}
			}
			payload["phases"] = phases
			payload["run"] = record.ID
			payload["profile"] = record.Profile
			payload["simulated"] = record.Simulated
			payload["cli"] = map[string]string{
				"version": record.CLIVersion, "commit": record.CLICommit,
				"checksum": record.CLIChecksum, "binary": record.CLIBinary,
			}
			payload["resources"] = map[string]int{
				"created": len(record.Resources),
				"removed": removed,
				// The number that decides the verdict: a green run that leaks a
				// cluster has not passed.
				"remaining": len(record.Remaining()),
			}
			if !running {
				verdict, why := record.Gate()
				payload["verdict"] = string(verdict)
				payload["why"] = why
			}
		}
	}
	if payload["phases"] == nil {
		type phase struct {
			ID      string `json:"id"`
			State   string `json:"state"`
			Message string `json:"message"`
		}
		phases := make([]phase, 0, len(e2e.Order))
		for _, declared := range e2e.Order {
			phases = append(phases, phase{string(declared.ID), "not_started", declared.Purpose})
		}
		payload["phases"] = phases
	}
	writeE2EJSON(w, payload)
}

// handleRun returns one run in full: every phase, every command, every finding.
//
// Deliberately a separate request from the state poll. State is fetched every
// second while a run is in flight, and shipping every command's stdout with it
// would be a megabyte a second for no reason.
func (l *lifecycle) handleRun(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/e2e/run/")
	if !safeE2ERunID(id) {
		http.NotFound(w, r)
		return
	}
	root := filepath.Join(l.reportRoot(), id)
	record, err := e2e.LoadRun(root)
	if err != nil {
		writeE2EJSON(w, map[string]any{"error": err.Error()})
		return
	}
	verdict, why := record.Gate()
	writeE2EJSON(w, map[string]any{
		"run": record, "verdict": string(verdict), "why": why,
		"findings": record.Findings(), "remaining": record.Remaining(),
		"artifacts": e2eArtifacts(root),
	})
}

// e2eArtifacts lists the evidence a run produced, and only the formats actually
// on disk. A link to a report that a failed run never got to write is a 404 with
// the reader's confidence attached to it.
func e2eArtifacts(root string) []map[string]string {
	candidates := []struct{ label, path string }{
		{"HTML report", "reports/report.html"},
		{"Markdown", "reports/report.md"},
		{"JSON", "reports/report.json"},
		{"CSV summary", "reports/summary.csv"},
		{"JUnit", "junit/e2e.xml"},
	}
	var out []map[string]string
	for _, candidate := range candidates {
		path := filepath.Join(root, filepath.FromSlash(candidate.path))
		if info, err := os.Stat(path); err == nil {
			out = append(out, map[string]string{
				"label": candidate.label, "path": candidate.path,
				"size": fmt.Sprintf("%d", info.Size()),
			})
		}
	}
	return out
}

// runFailure is one finding, as a row in the runs table draws it.
type runFailure struct {
	// Phase is where it broke. Carried separately from the command because
	// "where" and "what" are different questions, and a finding with no
	// command of its own — a cleanup check, say — still has a location.
	Phase    string `json:"phase"`
	Command  string `json:"command"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Cause    string `json:"cause,omitempty"`
	Category string `json:"category,omitempty"`
}

func (l *lifecycle) handleRuns(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		ID        string `json:"id"`
		Profile   string `json:"profile"`
		Verdict   string `json:"verdict"`
		Why       string `json:"why"`
		Started   string `json:"started,omitempty"`
		Simulated bool   `json:"simulated"`
		Remaining int    `json:"remaining"`
		HasReport bool   `json:"has_report"`
		// Failures is what broke, by name. "1 phase(s) failed" is a count, and
		// a count sends the reader to another page to learn the one thing the
		// row exists to tell them. The findings are already on the record —
		// the list simply never carried them.
		Failures []runFailure `json:"failures,omitempty"`
	}
	root := l.reportRoot()
	var out []entry
	for _, id := range e2eRunIDs(root) {
		item := entry{ID: id, Verdict: "incomplete"}
		if record, err := e2e.LoadRun(filepath.Join(root, id)); err == nil {
			verdict, why := record.Gate()
			item.Profile, item.Verdict, item.Why = record.Profile, string(verdict), why
			item.Simulated = record.Simulated
			item.Remaining = len(record.Remaining())
			if !record.Started.IsZero() {
				item.Started = record.Started.Format(time.RFC3339)
			}
			for _, finding := range record.Findings() {
				name := finding.Command
				if name == "" {
					name = string(finding.Phase)
				}
				item.Failures = append(item.Failures, runFailure{
					Phase:    string(finding.Phase),
					Command:  name,
					Expected: finding.Expected,
					Actual:   finding.Actual,
					Cause:    finding.Cause,
					Category: string(finding.Category),
				})
			}
		}
		if _, err := os.Stat(filepath.Join(root, id, "reports", "report.html")); err == nil {
			item.HasReport = true
		}
		out = append(out, item)
	}
	writeE2EJSON(w, map[string]any{"runs": out})
}

// --- starting and stopping -------------------------------------------------

func (l *lifecycle) handleStart(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Action   string   `json:"action"`
		Profile  string   `json:"profile"`
		Simulate bool     `json:"simulate"`
		From     string   `json:"from"`
		To       string   `json:"to"`
		Only     []string `json:"only"`
		Skip     []string `json:"skip"`
		RunID    string   `json:"run_id"`
		CLIRepo  string   `json:"cli_repo"`
	}
	_ = json.NewDecoder(r.Body).Decode(&request)

	l.mu.Lock()
	if l.running {
		l.mu.Unlock()
		writeE2EJSON(w, map[string]any{"error": "a lifecycle run is already in flight"})
		return
	}
	if _, err := os.Stat(l.binary); err != nil {
		l.mu.Unlock()
		writeE2EJSON(w, map[string]any{"error": "the lifecycle binary is not built yet. " +
			"Run:  mise run build"})
		return
	}

	profile, err := e2e.FindProfile(request.Profile)
	if err != nil {
		l.mu.Unlock()
		writeE2EJSON(w, map[string]any{"error": err.Error()})
		return
	}
	// The approval gate is not something a checkbox on a page can satisfy. A
	// profile that creates real infrastructure has to be started deliberately,
	// from a command line, by somebody who typed the flag.
	if profile.LiveApproval {
		l.mu.Unlock()
		writeE2EJSON(w, map[string]any{"error": profile.Name +
			" creates real infrastructure and cannot be started from this page. " +
			"Run it deliberately:  ./bin/opencenter-e2e e2e run --profile " +
			profile.Name + " --approve-live"})
		return
	}

	switch request.Action {
	case "run", "plan", "resume", "phase", "diagnose", "cleanup":
	default:
		l.mu.Unlock()
		writeE2EJSON(w, map[string]any{"error": "unknown action " + request.Action})
		return
	}

	args := []string{"e2e", request.Action, "--profile", profile.Name,
		"--report-dir", l.reportRoot()}

	switch request.Action {
	case "resume", "cleanup", "diagnose", "phase":
		id := strings.TrimSpace(request.RunID)
		if id == "" {
			id = latestE2ERunID(l.reportRoot())
		}
		if id == "" || !safeE2ERunID(id) {
			l.mu.Unlock()
			writeE2EJSON(w, map[string]any{
				"error": "there is no previous run to " + request.Action})
			return
		}
		args = append(args, "--run-id", id)
	}

	// The phase window. The command line has had these from the start and no
	// console exposed them, which made "rerun just kubernetes-health against the
	// cluster that is already up" something you had to leave the page to do —
	// and that is most of what troubleshooting is.
	for _, pair := range []struct{ flag, value string }{
		{"--from-phase", request.From}, {"--to-phase", request.To},
	} {
		value := strings.TrimSpace(pair.value)
		if value == "" {
			continue
		}
		if _, ok := e2e.Lookup(e2e.ID(value)); !ok {
			l.mu.Unlock()
			writeE2EJSON(w, map[string]any{"error": "no phase called " + value})
			return
		}
		args = append(args, pair.flag, value)
	}
	for _, group := range []struct {
		flag   string
		values []string
	}{{"--only-phase", request.Only}, {"--skip-phase", request.Skip}} {
		for _, value := range group.values {
			if value = strings.TrimSpace(value); value == "" {
				continue
			}
			if _, ok := e2e.Lookup(e2e.ID(value)); !ok {
				l.mu.Unlock()
				writeE2EJSON(w, map[string]any{"error": "no phase called " + value})
				return
			}
			args = append(args, group.flag, value)
		}
	}

	if request.Simulate && request.Action == "run" {
		args = append(args, "--simulate")
	}
	if repo := strings.TrimSpace(request.CLIRepo); repo != "" {
		args = append(args, "--cli-repo", repo)
	}

	cmd := exec.Command(l.binary, args...)
	cmd.Dir = l.root
	cmd.Env = os.Environ()
	l.output.Reset()
	l.output.WriteString("$ ./bin/opencenter-e2e " + strings.Join(args, " ") + "\n\n")
	cmd.Stdout = &e2eWriter{lifecycle: l}
	cmd.Stderr = &e2eWriter{lifecycle: l}

	if err := cmd.Start(); err != nil {
		l.mu.Unlock()
		writeE2EJSON(w, map[string]any{"error": err.Error()})
		return
	}
	l.cmd, l.running, l.runID = cmd, true, ""
	l.mu.Unlock()

	go func() {
		_ = cmd.Wait()
		l.mu.Lock()
		l.running, l.cmd = false, nil
		l.runID = latestE2ERunID(l.reportRoot())
		l.mu.Unlock()
	}()

	writeE2EJSON(w, map[string]any{"started": true})
}

func (l *lifecycle) handleStop(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cmd != nil && l.cmd.Process != nil {
		// Interrupt, not kill. The run traps it and still runs diagnostics,
		// destroy, verify-cleanup and report. Killing here is how a stopped run
		// leaves a cluster behind, which is worse than taking another minute.
		_ = l.cmd.Process.Signal(os.Interrupt)
	}
	writeE2EJSON(w, map[string]any{"stopping": true})
}

// handleEvidence serves one of a run's report formats.
//
// The path after the run id is checked against the list a run can actually
// produce, rather than cleaned and hoped over. "I removed the ../" is not the
// same claim as "only these five files are reachable", and joining a URL onto a
// filesystem root is how a console serves /etc/shadow.
func (l *lifecycle) handleEvidence(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/e2e/evidence/")
	id, relative, found := strings.Cut(rest, "/")
	if !found || !safeE2ERunID(id) {
		http.NotFound(w, r)
		return
	}
	allowed := map[string]string{
		"reports/report.html": "text/html; charset=utf-8",
		"reports/report.md":   "text/plain; charset=utf-8",
		"reports/report.json": "application/json",
		"reports/summary.csv": "text/csv",
		"junit/e2e.xml":       "application/xml",
	}
	kind, ok := allowed[relative]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", kind)
	http.ServeFile(w, r, filepath.Join(l.reportRoot(), id, filepath.FromSlash(relative)))
}

// handleCoverage is the provider matrix: the same phase across every provider
// that has run, plus how much of the lifecycle each one actually exercised.
//
// It also says which phases are red on some providers and green on others,
// because that is the difference between "the product is broken" and "this
// provider is", and it is the first thing anybody wants to know from a matrix.
func (l *lifecycle) handleCoverage(w http.ResponseWriter, r *http.Request) {
	matrix := e2e.BuildMatrix(l.reportRoot())

	providerOnly := []string{}
	for _, row := range matrix.Rows {
		if matrix.ProviderOnly(row.Phase) {
			providerOnly = append(providerOnly, string(row.Phase))
		}
	}

	// Untested areas, which is what a coverage dashboard is for. A profile
	// nobody has run is not on the matrix at all, and saying so is more useful
	// than an empty column.
	var never []string
	for _, profile := range e2e.Profiles {
		found := false
		for _, name := range matrix.Profiles {
			if name == profile.Name {
				found = true
				break
			}
		}
		if !found {
			never = append(never, profile.Name)
		}
	}

	percent := map[string]int{}
	for name, coverage := range matrix.Coverage {
		percent[name] = coverage.Percent()
	}

	writeE2EJSON(w, map[string]any{
		"matrix": matrix, "percent": percent,
		"provider_only": providerOnly,
		"never_run":     never,
		"phases":        len(e2e.Order),
	})
}

// --- plumbing --------------------------------------------------------------

// e2eWriter appends command output under the lifecycle's own lock.
type e2eWriter struct{ lifecycle *lifecycle }

func (e *e2eWriter) Write(p []byte) (int, error) {
	e.lifecycle.mu.Lock()
	defer e.lifecycle.mu.Unlock()
	e.lifecycle.output.Write(p)
	return len(p), nil
}

// safeE2ERunID is the one check standing between a URL and the filesystem: no
// separators, no traversal, and the prefix the engine gives every run it makes.
func safeE2ERunID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`) && !strings.Contains(id, "..") &&
		strings.HasPrefix(id, "e2e-")
}

func e2eRunIDs(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "e2e-") {
			out = append(out, entry.Name())
		}
	}
	// The ids are timestamps, so reverse lexical order is newest first.
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func latestE2ERunID(root string) string {
	if ids := e2eRunIDs(root); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

func writeE2EJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
