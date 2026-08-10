// Package checks holds the tests themselves.
//
// A check is a small Go function that drives the real binary and records what
// it found. Checks are written against an environment rather than against a
// fixed setup, so the same question — does authentication fail clearly with a
// bad credential? — is asked of the simulator and of a real cloud with the
// same code. If it passes against the simulator and fails against FLEX, the
// difference is real cloud behaviour and not test scaffolding.
package checks

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
	"github.com/opencenter-cloud/opencli-testbench/internal/registry"
	"github.com/opencenter-cloud/opencli-testbench/internal/sandbox"
	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
)

// Status is the outcome of a check or of one assertion inside it.
type Status string

const (
	// StatusPass means every assertion held.
	StatusPass Status = "pass"
	// StatusFail means the CLI did something it should not have.
	StatusFail Status = "fail"
	// StatusSkip means the question could not be asked here — a missing tool,
	// a gate that is not set. It is never used to hide a failure.
	StatusSkip Status = "skip"
	// StatusError means the bench itself broke.
	StatusError Status = "error"
	// StatusRunning is only ever seen by the console, mid-run.
	StatusRunning Status = "running"
	// StatusPending is a check that has not started.
	StatusPending Status = "pending"
)

// Canary values injected on purpose and then hunted for in every byte the CLI
// produces. They are deliberately unmistakable: if one of these appears in a
// log, a report or a generated file, a real secret would have appeared too.
const (
	CanaryPassword = "CANARY_PASSWORD_MUST_NOT_LEAK_8f31c0"
	CanarySecret   = "CANARY_APPCRED_SECRET_MUST_NOT_LEAK_2b7d94"
	CanaryToken    = "CANARY_TOKEN_MUST_NOT_LEAK_5e08aa"
	CanaryAgeKey   = "AGE-SECRET-KEY-1CANARYMUSTNOTLEAK000000000000000000000000000000000000"
)

// Canaries is every canary, for the leak sweep.
func Canaries() []string {
	return []string{CanaryPassword, CanarySecret, CanaryToken, CanaryAgeKey}
}

// Env is the world one check runs against.
type Env struct {
	// ID is the environment: local, sim, flex or kind.
	ID string
	// Spec is the loaded config/, for checks that assert against the checklist.
	Spec *spec.Spec
	// Root is the bench checkout.
	Root string
	// Sandbox is the throwaway filesystem for this run.
	Sandbox *sandbox.Sandbox
	// CLI runs the binary under test.
	CLI *cli.Runner
	// Sim controls the OpenStack simulator. Nil outside the sim environment.
	Sim SimControl
	// Cloud is the OS_CLOUD profile in use, empty in the local environment.
	Cloud string
	// Mutate reports whether the run is allowed to create things that outlive
	// a command: a Kind cluster, a real server.
	Mutate bool
	// Registry records everything the run creates, so cleanup has something
	// better than hope to work from.
	Registry *registry.Registry
	// Primary is the fixture the continuous workflow hands from module to
	// module: Module 5 creates it, Module 20 runs the lifecycle against it,
	// Module 30 removes it. Nil until something asks for it.
	Primary *Fixture
	// Log records a line against the run as a whole.
	Log func(format string, args ...any)
}

// Fixture is a cluster configuration several modules share, so the workflow
// tests continuity rather than thirty unrelated setups.
type Fixture struct {
	Organization string
	Cluster      string
	Provider     string
}

// Reference is the org/cluster form the CLI accepts.
func (f *Fixture) Reference() string { return f.Organization + "/" + f.Cluster }

// SimControl is the slice of the simulator a check is allowed to touch: it can
// make the far end misbehave, and ask what the CLI actually requested.
type SimControl interface {
	// Fault makes the next matching request fail. path is a substring match
	// against the request path; count is how many times to fail before
	// behaving again.
	Fault(path string, status int, count int) error
	// Malformed makes matching responses return JSON that does not parse.
	Malformed(path string, count int) error
	// Hang makes matching requests stall for the given delay.
	Hang(path string, delay time.Duration, count int) error
	// Clear removes every injected fault.
	Clear() error
	// Requests returns the calls the CLI has made so far, most recent last.
	Requests() ([]SimRequest, error)
}

// SimRequest is one call the CLI made to the simulator.
type SimRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	At     string `json:"at"`
}

// Assertion is one thing a check looked at.
type Assertion struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Invocation is a recorded command, kept for the console's detail pane.
type Invocation struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Millis   int64  `json:"millis"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	TimedOut bool   `json:"timed_out,omitempty"`
}

// Result is everything one check found.
type Result struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Category    string       `json:"category"`
	Environment string       `json:"environment"`
	Status      Status       `json:"status"`
	Message     string       `json:"message,omitempty"`
	Assertions  []Assertion  `json:"assertions"`
	Commands    []Invocation `json:"commands"`
	Millis      int64        `json:"millis"`
}

// Check is one question the bench asks.
type Check struct {
	// ID is stable and used in URLs, filters and reports.
	ID string
	// Name is what a person reads.
	Name string
	// Category is a checklist row id from config/checklist.yaml.
	Category string
	// Environments lists where this check can run.
	Environments []string
	// Mutating marks a check that creates something outliving the command,
	// so it only runs when the mutation gate is set.
	Mutating bool
	// Slow marks a check measured in minutes rather than seconds.
	Slow bool
	// Fn does the work.
	Fn func(ctx context.Context, t *T)
}

// AppliesTo reports whether a check runs in an environment.
func (c Check) AppliesTo(environment string) bool {
	for _, id := range c.Environments {
		if id == environment || id == "*" {
			return true
		}
	}
	return false
}

// T is the handle a check is given: it asserts, runs commands and records.
type T struct {
	Env    *Env
	result *Result
	onStep func(Assertion)
	ctx    context.Context
}

type abort struct {
	status  Status
	message string
}

// Assert records one finding. It returns whether it held, so a check can stop
// early without failing twice over the same cause.
func (t *T) Assert(name string, ok bool, detail string) bool {
	status := StatusPass
	if !ok {
		status = StatusFail
	}
	assertion := Assertion{Name: name, Status: status, Detail: detail}
	t.result.Assertions = append(t.result.Assertions, assertion)
	if t.onStep != nil {
		t.onStep(assertion)
	}
	return ok
}

// Assertf is Assert with a formatted detail.
func (t *T) Assertf(name string, ok bool, format string, args ...any) bool {
	return t.Assert(name, ok, fmt.Sprintf(format, args...))
}

// Note records something worth seeing that is not a pass or a fail.
func (t *T) Note(name, detail string) {
	assertion := Assertion{Name: name, Status: StatusSkip, Detail: detail}
	t.result.Assertions = append(t.result.Assertions, assertion)
	if t.onStep != nil {
		t.onStep(assertion)
	}
}

// Require is Assert that stops the check when it does not hold. Use it for a
// precondition: there is no point asserting on the contents of a file the
// command never created.
func (t *T) Require(name string, ok bool, detail string) {
	if !t.Assert(name, ok, detail) {
		panic(abort{status: StatusFail, message: name + ": " + detail})
	}
}

// Skip abandons the check without failing it. Reserved for a genuinely absent
// prerequisite — never for a failure that is inconvenient.
func (t *T) Skip(format string, args ...any) {
	panic(abort{status: StatusSkip, message: fmt.Sprintf(format, args...)})
}

// Fatalf abandons the check as a bench error rather than a CLI defect.
func (t *T) Fatalf(format string, args ...any) {
	panic(abort{status: StatusError, message: fmt.Sprintf(format, args...)})
}

// Run invokes the binary and records the invocation.
func (t *T) Run(args ...string) cli.Result {
	return t.RunWith(cli.RunOptions{}, args...)
}

// RunWith invokes the binary with per-command overrides.
func (t *T) RunWith(options cli.RunOptions, args ...string) cli.Result {
	result := t.Env.CLI.RunWith(t.ctx, options, args...)
	t.record(result)
	return result
}

// RunWithEnv invokes the binary with extra environment variables layered on
// top of the sandbox environment.
func (t *T) RunWithEnv(env map[string]string, args ...string) cli.Result {
	return t.RunWith(cli.RunOptions{Env: env}, args...)
}

// RunStdin invokes the binary with something waiting on standard input, which
// is how a confirmation prompt is answered without a terminal.
func (t *T) RunStdin(input string, args ...string) cli.Result {
	return t.RunWith(cli.RunOptions{Stdin: input}, args...)
}

// runIn is shorthand for running one command from a different directory.
func runIn(dir string) cli.RunOptions { return cli.RunOptions{Dir: dir} }

// Register records something this check created, so cleanup can remove it and
// then prove it is gone.
func (t *T) Register(resource registry.Resource) string {
	if t.Env.Registry == nil {
		return ""
	}
	resource.ModuleID = t.result.Category
	return t.Env.Registry.Add(resource)
}

// primary returns the fixture the workflow hands from module to module,
// creating it on first use. Outside the workflow — in a single-check run —
// the first check to ask for it makes it, which keeps every check runnable on
// its own.
func (t *T) primary() *Fixture {
	if t.Env.Primary != nil {
		return t.Env.Primary
	}
	fixture := &Fixture{Organization: "az-test-org", Cluster: "az-test", Provider: "kind"}
	t.initCluster(fixture.Cluster, fixture.Organization, "--type", fixture.Provider)
	t.Register(registry.Resource{
		Provider: "local", Type: "config", Name: fixture.Reference(),
		Location: t.configPath(fixture.Organization, fixture.Cluster),
		Cleanup:  []string{"cluster", "destroy", fixture.Reference(), "--force", "--remove-files"},
	})
	t.Env.Primary = fixture
	return fixture
}

func (t *T) record(result cli.Result) {
	t.result.Commands = append(t.result.Commands, Invocation{
		Command:  result.Command(),
		ExitCode: result.ExitCode,
		Millis:   result.Duration.Milliseconds(),
		Stdout:   clip(result.Stdout),
		Stderr:   clip(result.Stderr),
		TimedOut: result.TimedOut,
	})
}

// clip keeps a recorded stream readable in the console. A command that prints
// a megabyte is interesting for its size, not its contents.
func clip(s string) string {
	const limit = 4000
	if len(s) <= limit {
		return s
	}
	return s[:limit] + fmt.Sprintf("\n... [%d more bytes]", len(s)-limit)
}

// Logf records a line against the run.
func (t *T) Logf(format string, args ...any) {
	if t.Env.Log != nil {
		t.Env.Log(format, args...)
	}
}

// Execute runs one check and turns a panic, an abort or a clean return into a
// Result. A check that crashes is a bench error, reported as such, and never
// takes the run down with it.
func Execute(ctx context.Context, check Check, env *Env, onStep func(Assertion)) (result Result) {
	started := time.Now()
	result = Result{
		ID:          check.ID,
		Name:        check.Name,
		Category:    check.Category,
		Environment: env.ID,
		Status:      StatusPass,
		Assertions:  []Assertion{},
		Commands:    []Invocation{},
	}

	t := &T{Env: env, result: &result, onStep: onStep, ctx: ctx}

	defer func() {
		result.Millis = time.Since(started).Milliseconds()
		if recovered := recover(); recovered != nil {
			if stop, ok := recovered.(abort); ok {
				result.Status = stop.status
				result.Message = stop.message
				// A skip must never bury a finding. If something already
				// failed before the check decided it could not continue, the
				// failure is the result — otherwise "the tool is missing"
				// becomes a way to make a real defect disappear.
				if stop.status == StatusSkip && hasFailure(result.Assertions) {
					result.Status = StatusFail
					result.Message = "stopped early (" + stop.message +
						") but an assertion had already failed"
				}
			} else {
				result.Status = StatusError
				result.Message = fmt.Sprintf("check panicked: %v", recovered)
			}
			return
		}
		if result.Status == StatusPass {
			result.Status = summarise(result.Assertions)
		}
	}()

	check.Fn(ctx, t)
	return result
}

func hasFailure(assertions []Assertion) bool {
	for _, assertion := range assertions {
		if assertion.Status == StatusFail {
			return true
		}
	}
	return false
}

func summarise(assertions []Assertion) Status {
	if len(assertions) == 0 {
		return StatusError
	}
	for _, assertion := range assertions {
		if assertion.Status == StatusFail {
			return StatusFail
		}
	}
	return StatusPass
}

// --- registry ---------------------------------------------------------------

var checkRegistry []Check

// register adds a check at package init. Duplicate ids are a programming
// error and panic immediately rather than silently shadowing.
func register(checks ...Check) {
	for _, check := range checks {
		for _, existing := range checkRegistry {
			if existing.ID == check.ID {
				panic("duplicate check id: " + check.ID)
			}
		}
		checkRegistry = append(checkRegistry, check)
	}
}

// All returns every registered check, in id order so a run is reproducible.
func All() []Check {
	out := make([]Check, len(checkRegistry))
	copy(out, checkRegistry)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// For returns the checks that apply to an environment.
func For(environment string, mutate bool) []Check {
	var out []Check
	for _, check := range All() {
		if !check.AppliesTo(environment) {
			continue
		}
		if check.Mutating && !mutate {
			continue
		}
		out = append(out, check)
	}
	return out
}

// Categories returns the checklist rows covered in an environment, so the
// console can report a row with no checks as a gap rather than as a pass.
func Categories(environment string) map[string]int {
	counts := map[string]int{}
	for _, check := range All() {
		if check.AppliesTo(environment) {
			counts[check.Category]++
		}
	}
	return counts
}

// --- small shared helpers ---------------------------------------------------

// containsAny reports whether haystack holds any of the needles, case
// insensitively.
func containsAny(haystack string, needles ...string) bool {
	lower := strings.ToLower(haystack)
	for _, needle := range needles {
		if strings.Contains(lower, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

// sha256Sum fingerprints file contents for the "did this change?" checks.
func sha256Sum(content []byte) []byte {
	sum := sha256.Sum256(content)
	return sum[:]
}

// firstLine is used in assertion details, where a whole stream would drown the
// finding it is supposed to explain.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(no output)"
	}
	if index := strings.IndexByte(s, '\n'); index >= 0 {
		s = s[:index]
	}
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}
