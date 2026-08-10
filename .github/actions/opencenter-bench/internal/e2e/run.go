package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The record of one run, written to disk after every phase.
//
// Persisted that often for one reason: resume. A run that fell over at deploy
// has real infrastructure attached to it, and the only way to pick it up again —
// or even to know what to destroy — is a record written before the process died
// rather than after it finished. Nothing here is kept only in memory.

// PhaseResult is what happened in one phase.
type PhaseResult struct {
	ID       ID        `json:"id"`
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	State    State     `json:"state"`
	Started  time.Time `json:"started,omitempty"`
	Ended    time.Time `json:"ended,omitempty"`
	Millis   int64     `json:"millis"`
	Message  string    `json:"message,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Commands []Command `json:"commands,omitempty"`
	Findings []Finding `json:"findings,omitempty"`
}

// Command is one invocation, recorded exactly as it ran.
//
// Argv rather than a rendered string: a report that shows a command with the
// quoting lost is a command nobody can paste back, and reproducing a failure is
// most of what evidence is for. Redaction happens on the way in.
type Command struct {
	Argv     []string `json:"argv"`
	Dir      string   `json:"dir,omitempty"`
	ExitCode int      `json:"exit_code"`
	Millis   int64    `json:"millis"`
	Stdout   string   `json:"stdout,omitempty"`
	Stderr   string   `json:"stderr,omitempty"`
	TimedOut bool     `json:"timed_out,omitempty"`
}

// String renders the invocation for a human, without pretending it is shell-safe.
func (c Command) String() string { return strings.Join(c.Argv, " ") }

// Category is why something failed. Taken from the brief, because a release gate
// that cannot tell a provider outage from a product defect will either block
// good builds or ship broken ones.
type Category string

const (
	CategoryProductDefect  Category = "Product defect"
	CategoryRegression     Category = "Regression"
	CategoryProviderIssue  Category = "Provider issue"
	CategoryEnvironment    Category = "Environment issue"
	CategoryMissingPrereq  Category = "Missing prerequisite"
	CategoryInvalidConfig  Category = "Invalid configuration"
	CategoryExpectedInject Category = "Expected injected failure"
	CategoryBenchDefect    Category = "Test Bench defect"
	CategoryCleanupDefect  Category = "Cleanup defect"
	CategoryUnknown        Category = "Unknown"
)

// Finding is one row of the report's failure table.
type Finding struct {
	Phase       ID       `json:"phase"`
	Command     string   `json:"command,omitempty"`
	Environment string   `json:"environment"`
	Expected    string   `json:"expected"`
	Actual      string   `json:"actual"`
	Cause       string   `json:"cause"`
	Category    Category `json:"category"`
	Regression  bool     `json:"regression"`
	Remediation string   `json:"remediation,omitempty"`
	Evidence    string   `json:"evidence,omitempty"`

	// Reproduce is the exact bench invocation that runs this failure again.
	//
	// Not the same thing as Command, which is what the CLI was asked to do.
	// After a run, the question is "how do I see this again", and the answer is
	// rarely the raw CLI call: it needs the run's workspace, its configuration
	// and its resource registry. This is the one-liner that gives all three, and
	// it is filled in by the engine rather than by each phase so no phase can
	// forget it.
	Reproduce string `json:"reproduce,omitempty"`
}

// ReproduceCommand is how to run one phase of one run again.
//
// `e2e phase` rather than `e2e run`: it resumes into the existing workspace, so
// the failure is reproduced against the state that produced it instead of
// against a fresh one that may not fail at all.
func ReproduceCommand(runID, profile string, phase ID) string {
	return "./bin/opencenter-e2e e2e phase --run-id " + runID +
		" --profile " + profile + " --only-phase " + string(phase)
}

// Resource is one thing the run brought into existence.
//
// Registered the moment it is created, not when the phase that created it
// finishes. A deploy killed halfway has already made things, and a registry
// written at the end of a phase that never ended is empty.
type Resource struct {
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
	ID        string    `json:"id,omitempty"`
	Created   time.Time `json:"created"`
	Removed   bool      `json:"removed"`
	RemovedAt time.Time `json:"removed_at,omitempty"`
	// Order is the cleanup order. Lower goes first: smoke-test workloads before
	// the cluster they run on, the cluster before the machines under it.
	Order       int    `json:"order"`
	Remediation string `json:"remediation,omitempty"`
}

// Cleanup ordering, from the brief's dependency list.
const (
	OrderSmokeResource   = 10
	OrderServiceResource = 20
	OrderGitOpsResource  = 30
	OrderKubernetes      = 40
	OrderInfrastructure  = 50
	OrderGitBranch       = 60
	OrderTempFile        = 70
)

// Run is everything about one execution.
type Run struct {
	ID      string    `json:"id"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitempty"`

	// Identity of the thing under test.
	Profile        string         `json:"profile"`
	Channel        Channel        `json:"channel"`
	Infrastructure Infrastructure `json:"infrastructure"`
	Provider       Provider       `json:"provider"`
	Cluster        string         `json:"cluster"`
	Organisation   string         `json:"organisation"`

	// Identity of the build under test, filled in by verify-binary.
	CLIVersion   string `json:"cli_version,omitempty"`
	CLICommit    string `json:"cli_commit,omitempty"`
	CLIChecksum  string `json:"cli_checksum,omitempty"`
	CLIBinary    string `json:"cli_binary,omitempty"`
	SourceCommit string `json:"source_commit,omitempty"`

	// CLIPlugin is the local plugin this run installed.
	//
	// Recorded because it is half the build under test and used not to be. A
	// report naming the CLI commit while the plugin beside it is three weeks
	// older describes a binary nobody ran — which is exactly what happened here,
	// and it cost every phase after ten.
	CLIPlugin string `json:"cli_plugin,omitempty"`

	// Identity of the machine.
	Host string `json:"host"`
	OS   string `json:"os"`
	Arch string `json:"arch"`

	// Choices.
	DestroyAfter bool          `json:"destroy_after"`
	KeepOnFail   bool          `json:"keep_on_failure"`
	Approved     bool          `json:"live_approved"`
	Timeout      time.Duration `json:"timeout"`

	// Simulated marks a run whose infrastructure phases were faked. It is
	// recorded in the run state, so a report read months later still says so
	// even if nobody remembers which flag was used.
	Simulated bool `json:"simulated"`

	Root      string        `json:"root"`
	Phases    []PhaseResult `json:"phases"`
	Resources []Resource    `json:"resources"`

	// Selection is remembered so a resume runs the same shape of run.
	Selection Selection `json:"selection"`
}

// NewRunID is a sortable identifier that reads as a date.
func NewRunID(now time.Time) string {
	return "e2e-" + now.UTC().Format("20060102-150405")
}

// NewRun starts a record.
func NewRun(profile Profile, channel Channel, root string, now time.Time) *Run {
	host, _ := os.Hostname()
	id := NewRunID(now)
	run := &Run{
		ID:             id,
		Started:        now.UTC(),
		Profile:        profile.Name,
		Channel:        channel,
		Infrastructure: profile.Infrastructure,
		Provider:       profile.Provider,
		Cluster:        strings.ToLower(strings.ReplaceAll(id, "e2e-", "e2e")),
		Organisation:   "e2e-testbench",
		Host:           host,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		Root:           root,
	}
	for _, phase := range Order {
		run.Phases = append(run.Phases, PhaseResult{
			ID: phase.ID, Number: phase.Number, Title: phase.Title, State: StateNotStarted,
		})
	}
	return run
}

// Result finds a phase's record.
func (r *Run) Result(id ID) *PhaseResult {
	for index := range r.Phases {
		if r.Phases[index].ID == id {
			return &r.Phases[index]
		}
	}
	return nil
}

// States is the current state of every phase.
func (r *Run) States() map[ID]State {
	out := map[ID]State{}
	for _, phase := range r.Phases {
		out[phase.ID] = phase.State
	}
	return out
}

// Register records a created resource and saves immediately.
//
// Immediately, because the whole value of the registry is that it survives the
// process that wrote it.
func (r *Run) Register(resource Resource) {
	resource.Created = time.Now().UTC()
	r.Resources = append(r.Resources, resource)
	_ = r.Save()
}

// MarkRemoved records that a resource is gone.
func (r *Run) MarkRemoved(kind, name string) {
	for index := range r.Resources {
		if r.Resources[index].Kind == kind && r.Resources[index].Name == name {
			r.Resources[index].Removed = true
			r.Resources[index].RemovedAt = time.Now().UTC()
		}
	}
}

// Remaining lists what was created and not removed.
func (r *Run) Remaining() []Resource {
	var out []Resource
	for _, resource := range r.Resources {
		if !resource.Removed {
			out = append(out, resource)
		}
	}
	return out
}

// Findings collects every failure across the phases.
func (r *Run) Findings() []Finding {
	var out []Finding
	for _, phase := range r.Phases {
		out = append(out, phase.Findings...)
	}
	return out
}

// Created reports whether any phase that can make resources has started.
//
// The question destroy asks: not "did the run succeed" but "is there anything
// out there". A deploy that failed in its first second still counts, because
// "failed" and "created nothing" are different claims and only the provider
// knows which happened.
func (r *Run) Created() bool {
	for _, phase := range Order {
		if !phase.Creates {
			continue
		}
		if state := r.Result(phase.ID); state != nil && state.State != StateNotStarted &&
			state.State != StateSkipped {
			return true
		}
	}
	return len(r.Resources) > 0
}

// StatePath is where the record lives.
func (r *Run) StatePath() string { return filepath.Join(r.Root, "state", "run.json") }

// Save writes the record.
func (r *Run) Save() error {
	if r.Root == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.StatePath()), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	// Written beside and renamed: a run killed during its own save should not
	// leave a truncated record, because that record is the only way to find the
	// infrastructure it left behind.
	temporary := r.StatePath() + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, r.StatePath())
}

// LoadRun reads a previous run's record.
func LoadRun(root string) (*Run, error) {
	path := filepath.Join(root, "state", "run.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no run state at %s: %w", path, err)
	}
	run := &Run{}
	if err := json.Unmarshal(raw, run); err != nil {
		return nil, fmt.Errorf("run state at %s is not readable: %w", path, err)
	}
	run.Root = root
	return run, nil
}

// Verdict is the release-gate outcome.
type Verdict string

const (
	VerdictPass         Verdict = "PASS"
	VerdictWarning      Verdict = "WARNING"
	VerdictFail         Verdict = "FAIL"
	VerdictInconclusive Verdict = "INCONCLUSIVE"
	// VerdictSimulated is its own outcome, never a PASS with a footnote. A
	// footnote is what a dashboard truncates, a script ignores and a screenshot
	// crops out; a verdict is what everything has to carry.
	VerdictSimulated Verdict = "SIMULATED"
)

// Gate applies the release rules.
//
// Inconclusive is a real outcome and not a synonym for failure. A run that could
// not reach the provider has not tested the product; reporting that as a pass
// ships an untested build, and reporting it as a failure blocks a good one on
// somebody else's outage.
func (r *Run) Gate() (Verdict, string) {
	// Before anything else. A simulated run cannot reach PASS by any route —
	// not by every phase being green, not by there being no findings. The check
	// is first so no later branch can return a pass ahead of it.
	if r.Simulated {
		return r.simulatedVerdict()
	}

	var blocked, failed, warned int
	for _, phase := range r.Phases {
		switch phase.State {
		case StateFailed:
			failed++
		case StateBlocked:
			blocked++
		case StateWarning:
			warned++
		case StateCancelled:
			return VerdictInconclusive, "the run was cancelled"
		}
	}

	// Anything left behind is a failure regardless of what the tests said: a
	// green run that leaks a cluster has not passed.
	//
	// Unless cleanup never got to run. A destroy blocked for want of a tool —
	// OpenTofu, on an emulated profile that does not require it — leaves the
	// configuration on disk, and calling that a leak says this build fails to
	// clean up after itself. It does not: the thing that removes it was never
	// installed, so we did not find out either way. That is the difference
	// between FAIL and INCONCLUSIVE, and it is the difference between blocking a
	// release and asking somebody to install a package.
	if remaining := r.Remaining(); len(remaining) > 0 {
		if destroy := r.Result(PhaseDestroy); destroy != nil &&
			destroy.State == StateBlocked {
			return VerdictInconclusive, fmt.Sprintf(
				"%d resource(s) remain because destroy could not run: %s",
				len(remaining), destroy.Message)
		}
		return VerdictFail, fmt.Sprintf(
			"%d resource(s) were created and not removed", len(remaining))
	}
	for _, finding := range r.Findings() {
		switch finding.Category {
		case CategoryEnvironment, CategoryProviderIssue, CategoryMissingPrereq:
			// Not the product's fault. Only inconclusive if it actually stopped
			// the run; a warning about an absent optional tool is not.
			if blocked > 0 {
				return VerdictInconclusive, "blocked by " + string(finding.Category) +
					": " + finding.Cause
			}
		}
	}
	switch {
	case failed > 0:
		return VerdictFail, fmt.Sprintf("%d phase(s) failed", failed)
	case blocked > 0:
		return VerdictInconclusive, fmt.Sprintf("%d phase(s) never ran", blocked)
	case warned > 0:
		return VerdictWarning, fmt.Sprintf("%d phase(s) reported warnings", warned)
	}
	return VerdictPass, "every phase that ran passed, and nothing was left behind"
}
