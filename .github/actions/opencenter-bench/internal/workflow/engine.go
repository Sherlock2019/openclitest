package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/checks"
	"github.com/opencenter-cloud/opencli-testbench/internal/gitopsupdate"
	"github.com/opencenter-cloud/opencli-testbench/internal/preflight"
	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
	"github.com/opencenter-cloud/opencli-testbench/internal/registry"
	"github.com/opencenter-cloud/opencli-testbench/internal/runner"
	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
)

// Options describe one A-to-Z run.
type Options struct {
	// Root is the bench checkout.
	Root string
	// DataDir is where run roots live. Empty means <root>/artifacts.
	DataDir string
	// Binary is the opencenter executable under test.
	Binary string
	// Source is the CLI checkout the binary came from, when known. Recorded in
	// the report so a result can be traced back to a commit.
	Source string

	// Live turns Module 29 on. It needs both the safety agreement and the
	// mutation gate; neither alone is enough.
	Live bool
	// LiveProviders is which disposable environments Module 29 uses: kind,
	// flex, or both.
	LiveProviders []string
	// Agreed holds the confirmation ids the person ticked.
	Agreed []string

	// Credentials are handed to the CLI. Secret-looking values are registered
	// for redaction before the first command runs.
	Credentials map[string]string

	// StopOnBlockingFailure is the default and the safe choice.
	StopOnBlockingFailure bool
	// SkipSlow drops the checks measured in minutes, for a quick pass.
	SkipSlow bool
	// KeepWorkspace leaves the run root in place after cleanup.
	KeepWorkspace bool
	// ArchiveEvidence writes evidence files even when the workspace is removed.
	ArchiveEvidence bool
}

// Event is streamed to whoever is watching.
type Event struct {
	Type      string            `json:"type"`
	At        time.Time         `json:"at"`
	Message   string            `json:"message,omitempty"`
	RunID     string            `json:"run_id,omitempty"`
	Module    *ModuleResult     `json:"module,omitempty"`
	ModuleID  string            `json:"module_id,omitempty"`
	Order     int               `json:"order,omitempty"`
	Total     int               `json:"total,omitempty"`
	Check     *checks.Result    `json:"check,omitempty"`
	Step      *checks.Assertion `json:"step,omitempty"`
	Run       *Run              `json:"run,omitempty"`
	Preflight *preflight.Report `json:"preflight,omitempty"`
}

// Run is the persisted state of one A-to-Z execution.
type Run struct {
	ID       string    `json:"id"`
	Root     string    `json:"root"`
	State    RunState  `json:"state"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
	Millis   int64     `json:"millis"`

	Binary        string   `json:"binary"`
	Version       string   `json:"version"`
	Source        string   `json:"source,omitempty"`
	SourceBranch  string   `json:"source_branch,omitempty"`
	SourceCommit  string   `json:"source_commit,omitempty"`
	SourceDirty   string   `json:"source_dirty,omitempty"`
	Host          string   `json:"host"`
	Platform      string   `json:"platform"`
	Live          bool     `json:"live"`
	LiveProviders []string `json:"live_providers,omitempty"`

	Modules   []ModuleResult      `json:"modules"`
	Preflight *preflight.Report   `json:"preflight,omitempty"`
	Resources []registry.Resource `json:"resources,omitempty"`
	Message   string              `json:"message,omitempty"`

	// Counts, filled at the end so a report does not have to recompute them.
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Warning int `json:"warning"`
	Blocked int `json:"blocked"`
	Skipped int `json:"skipped"`
	NotRun  int `json:"not_run"`

	// GitOps is the promotion stage's outcome, when it ran. Nil means the stage
	// did not run at all — which the reports keep distinct from a stage that ran
	// and proposed nothing, because those are different things to a reader.
	//
	// Not counted in Passed/Failed and deliberately outside OK(): a functional
	// verdict about the CLI must never be moved by whether a delivery pull
	// request opened. That is the rule the stage exists to protect, and the
	// cheapest place to protect it is by not mixing the numbers.
	GitOps *gitopsupdate.Result `json:"gitopsUpdate,omitempty"`
}

// OK reports whether every executed module passed and nothing was left
// unexplained. A run with blocked or not-run modules is not OK: it did not
// answer the questions it set out to.
func (r *Run) OK() bool {
	return r.Failed == 0 && r.Blocked == 0 && r.NotRun == 0
}

// Engine executes a plan.
type Engine struct {
	spec    *spec.Spec
	plan    *Plan
	options Options
	emit    func(Event)

	run       *Run
	redactor  *redact.Redactor
	resources *registry.Registry
	lab       *runner.Lab
}

// New prepares an engine. It does not touch the filesystem yet.
func New(loaded *spec.Spec, plan *Plan, options Options, emit func(Event)) *Engine {
	if emit == nil {
		emit = func(Event) {}
	}
	if options.DataDir == "" {
		options.DataDir = filepath.Join(options.Root, "artifacts")
	}
	return &Engine{
		spec:      loaded,
		plan:      plan,
		options:   options,
		emit:      emit,
		redactor:  redact.New(),
		resources: registry.New(),
	}
}

func (e *Engine) send(event Event) {
	event.At = time.Now()
	if e.run != nil {
		event.RunID = e.run.ID
	}
	event.Message = e.redactor.String(event.Message)
	e.emit(event)
}

func (e *Engine) logf(format string, args ...any) {
	e.send(Event{Type: "log", Message: fmt.Sprintf(format, args...)})
}

// Execute runs the whole workflow: preflight, thirty modules in order,
// cleanup, then the reports.
func (e *Engine) Execute(ctx context.Context) (*Run, error) {
	if err := e.prepare(); err != nil {
		return nil, err
	}
	defer e.finish()

	e.send(Event{Type: "run-start", Run: e.run, Total: len(e.plan.Modules),
		Message: "workspace " + e.run.Root})

	// Phase 0. Not a module, and not optional.
	e.run.State = RunPreparing
	if err := e.runPreflight(ctx); err != nil {
		e.run.State = RunFailed
		e.run.Message = err.Error()
		e.send(Event{Type: "run-error", Message: err.Error()})
		return e.run, err
	}

	if err := e.openPrimaryLab(ctx); err != nil {
		e.run.State = RunFailed
		e.run.Message = err.Error()
		e.send(Event{Type: "run-error", Message: err.Error()})
		return e.run, err
	}
	defer e.lab.Close()

	e.run.State = RunRunning
	e.save()

	stopped := false
	for index := range e.run.Modules {
		if ctx.Err() != nil {
			e.markRemaining(index, StateCancelled, "the run was cancelled")
			e.run.State = RunCancelling
			break
		}
		if stopped {
			e.run.Modules[index].State = StateSkipped
			e.run.Modules[index].Message = "an earlier blocking module failed"
			e.send(Event{Type: "module-done", Module: &e.run.Modules[index],
				Order: e.run.Modules[index].Order, Total: len(e.run.Modules)})
			continue
		}

		module := e.plan.Modules[index]
		result := e.runModule(ctx, module, &e.run.Modules[index])
		e.save()

		if result.State == StateFailed && module.Blocking && e.options.StopOnBlockingFailure {
			stopped = true
			e.run.State = RunFailed
			e.logf("stopping: %s is a blocking module and it failed", module.Name)
		}
	}

	e.summarise()
	e.save()
	return e.run, nil
}

// prepare creates the run root and its subdirectories.
func (e *Engine) prepare() error {
	id := time.Now().Format("20060102-150405")
	root := filepath.Join(e.options.DataDir, "runs", id)

	for _, directory := range []string{
		"home", "config", "state", "work", "bin", "fixtures", "logs",
		"evidence", "reports", "credentials-redacted", "generated", "cleanup",
	} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			return fmt.Errorf("create run workspace: %w", err)
		}
	}

	e.redactor.AddFromEnv(e.options.Credentials)

	e.run = &Run{
		ID:            id,
		Root:          root,
		State:         RunNotStarted,
		Started:       time.Now(),
		Binary:        e.options.Binary,
		Source:        e.options.Source,
		Host:          hostDescription(),
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		Live:          e.options.Live,
		LiveProviders: e.options.LiveProviders,
	}
	e.describeSource()

	// Record which credential variables were supplied, never their values.
	names := make([]string, 0, len(e.options.Credentials))
	for key := range e.options.Credentials {
		if redact.IsSecretName(key) {
			names = append(names, key+"="+redact.Placeholder)
		} else {
			names = append(names, key+"="+e.options.Credentials[key])
		}
	}
	_ = os.WriteFile(filepath.Join(root, "credentials-redacted", "supplied.txt"),
		[]byte(strings.Join(names, "\n")+"\n"), 0o600)

	for _, module := range e.plan.Modules {
		e.run.Modules = append(e.run.Modules, ModuleResult{
			ID: module.ID, Order: module.Order, Name: module.Name,
			Phase: module.Phase, State: StateNotStarted,
			Blocking: module.Blocking, Live: module.Live,
		})
	}
	return nil
}

// describeSource records where the binary came from, so a result can be traced
// back to a commit. It reads the repository and changes nothing in it.
func (e *Engine) describeSource() {
	if e.options.Source == "" {
		return
	}
	git := func(args ...string) string {
		command := exec.Command("git", append([]string{"-C", e.options.Source}, args...)...)
		output, err := command.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(output))
	}
	e.run.SourceBranch = git("rev-parse", "--abbrev-ref", "HEAD")
	e.run.SourceCommit = git("rev-parse", "--short", "HEAD")
	if dirty := git("status", "--porcelain"); dirty != "" {
		e.run.SourceDirty = fmt.Sprintf("%d modified files", len(strings.Split(dirty, "\n")))
	} else if e.run.SourceCommit != "" {
		e.run.SourceDirty = "clean"
	}
}

// runPreflight is Phase 0. Every blocking prerequisite has to be present
// before Module 1 is allowed to start.
func (e *Engine) runPreflight(ctx context.Context) error {
	e.send(Event{Type: "phase", Message: "Phase 0 — preflight and prerequisites"})

	report := preflight.Run(ctx, e.spec, "")
	e.run.Preflight = report
	e.send(Event{Type: "preflight", Preflight: report,
		Message: fmt.Sprintf("%d present, %d missing", report.Present, report.Missing)})

	// Blocking means: needed by a module that is going to run. A missing kind
	// only stops the workflow when Module 29 is switched on.
	required := map[string]bool{}
	for _, module := range e.plan.Modules {
		if module.Live && !e.options.Live {
			continue
		}
		for _, id := range module.Requires {
			required[id] = true
		}
	}
	// The binary itself is required no matter what.
	required["opencenter"] = true

	var missing []string
	for _, result := range report.Results {
		if required[result.ID] && result.Status != preflight.StatusPresent {
			missing = append(missing, result.Name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("preflight blocked: %s. Install what is missing, or turn off the modules that need it",
			strings.Join(missing, ", "))
	}

	if e.options.Live {
		if err := e.checkLiveApproval(); err != nil {
			return err
		}
	}
	e.logf("preflight passed: %d prerequisites present", report.Present)
	return nil
}

// checkLiveApproval enforces both halves of the gate. Ticking boxes in a
// browser is not enough on its own, and neither is the environment variable:
// one is consent, the other is intent.
func (e *Engine) checkLiveApproval() error {
	agreed := map[string]bool{}
	for _, id := range e.options.Agreed {
		agreed[id] = true
	}
	var unticked []string
	for _, confirmation := range e.plan.Safety.Confirmations {
		if !agreed[confirmation.ID] {
			unticked = append(unticked, confirmation.Text)
		}
	}
	if len(unticked) > 0 {
		return fmt.Errorf("the live infrastructure agreement is incomplete: %s",
			strings.Join(unticked, " / "))
	}
	if os.Getenv(runner.MutateGate) != "1" {
		return fmt.Errorf("live testing also needs %s=1 in the environment the bench was started from",
			runner.MutateGate)
	}
	return nil
}

// openPrimaryLab builds the world Modules 1 to 28 run in.
//
// The simulated OpenStack is the far end throughout, which is what makes
// authentication, API communication and retries answerable in a run that costs
// nothing. Module 29 gets its own world.
func (e *Engine) openPrimaryLab(ctx context.Context) error {
	lab, err := runner.NewLab(e.spec, runner.LabOptions{
		Root:        e.options.Root,
		Binary:      e.options.Binary,
		Environment: "sim",
		SandboxRoot: e.run.Root,
		Credentials: e.options.Credentials,
		Redactor:    e.redactor,
		Registry:    e.resources,
		Log:         e.logf,
	})
	if err != nil {
		return err
	}
	e.lab = lab
	e.run.Version = lab.Version(ctx)
	e.logf("%s", lab.Describe)
	return nil
}

// runModule executes one module: prerequisites, then its checks, in order.
func (e *Engine) runModule(ctx context.Context, module Module, result *ModuleResult) *ModuleResult {
	result.State = StateCheckingPrerequisites
	e.send(Event{Type: "module-start", ModuleID: module.ID, Order: module.Order,
		Total: len(e.plan.Modules), Message: module.Name})

	// Locked beats blocked: a live module without approval was never going to
	// run, and saying "SOPS is missing" about it would be misleading.
	if module.Live && !e.options.Live {
		result.State = StateLocked
		result.Message = "live infrastructure testing was not approved for this run"
		e.finishModule(result)
		return result
	}

	if missing := e.missingPrerequisites(ctx, module); len(missing) > 0 {
		result.State = StateBlocked
		result.Missing = missing
		result.Message = "missing: " + strings.Join(missing, ", ")
		e.finishModule(result)
		return result
	}

	selected := e.selectChecks(module)
	if len(selected) == 0 {
		result.State = StateSkipped
		// Say which it is. "Nothing answers this question" and "the quick pass
		// dropped the checks that do" look identical in a report and mean
		// completely different things.
		switch {
		case e.options.SkipSlow && e.hasSlowChecks(module):
			result.Message = "every check for this module is a slow one, and this was a quick pass"
		default:
			result.Message = "no check in this build answers this question here"
		}
		e.finishModule(result)
		return result
	}

	result.State = StateRunning
	result.Started = time.Now()

	environment := e.environmentFor(module)
	if environment == nil {
		result.State = StateBlocked
		result.Message = "no environment could be prepared for this module"
		e.finishModule(result)
		return result
	}

	for _, check := range selected {
		if ctx.Err() != nil {
			break
		}
		e.lab.Refresh()
		checkResult := checks.Execute(ctx, check, environment, func(step checks.Assertion) {
			e.send(Event{Type: "assertion", ModuleID: module.ID, Step: &step})
		})
		result.Results = append(result.Results, checkResult)
		e.writeEvidence(module, checkResult)
		e.send(Event{Type: "check-done", ModuleID: module.ID, Check: &checkResult})
	}

	result.Finished = time.Now()
	result.Millis = result.Finished.Sub(result.Started).Milliseconds()
	result.summarise()

	// Module 30 is not finished when its checks are: it also has to prove the
	// registry is empty.
	if module.ID == "cleanup" {
		e.performCleanup(ctx, result)
	}

	e.finishModule(result)
	return result
}

func (e *Engine) finishModule(result *ModuleResult) {
	if result.Finished.IsZero() {
		result.Finished = time.Now()
	}
	e.send(Event{Type: "module-done", Module: result, Order: result.Order,
		Total: len(e.plan.Modules)})
}

// missingPrerequisites rechecks a module's own prerequisites immediately
// before it runs, rather than trusting the Phase 0 sweep: a tool can be
// installed while the workflow is waiting, and that should unblock it.
func (e *Engine) missingPrerequisites(ctx context.Context, module Module) []string {
	var missing []string
	for _, id := range module.Requires {
		result, ok := preflight.One(ctx, e.spec, id)
		if !ok {
			continue
		}
		if result.Status != preflight.StatusPresent {
			missing = append(missing, result.Name)
		}
	}
	return missing
}

// selectChecks resolves a module's checks and puts them in a stable order.
func (e *Engine) selectChecks(module Module) []checks.Check {
	wanted := map[string]bool{}
	for _, id := range module.CheckIDs {
		wanted[id] = true
	}
	environment := "sim"
	if module.Live {
		environment = e.liveEnvironment()
	}

	var out []checks.Check
	for _, check := range checks.All() {
		if !wanted[check.ID] || !check.AppliesTo(environment) {
			continue
		}
		if check.Mutating && !e.options.Live {
			continue
		}
		if e.options.SkipSlow && check.Slow && !module.Live {
			continue
		}
		out = append(out, check)
	}
	return out
}

// hasSlowChecks reports whether a module would have had something to run had
// the quick pass not dropped it.
func (e *Engine) hasSlowChecks(module Module) bool {
	wanted := map[string]bool{}
	for _, id := range module.CheckIDs {
		wanted[id] = true
	}
	for _, check := range checks.All() {
		if wanted[check.ID] && check.Slow {
			return true
		}
	}
	return false
}

func (e *Engine) liveEnvironment() string {
	for _, provider := range e.options.LiveProviders {
		if provider == "kind" || provider == "flex" {
			return provider
		}
	}
	return "kind"
}

// environmentFor returns the world a module runs in. Everything up to 28 uses
// the primary lab; Module 29 gets one built for the provider it targets.
func (e *Engine) environmentFor(module Module) *checks.Env {
	if !module.Live {
		return e.lab.Env
	}

	liveRoot := filepath.Join(e.run.Root, "live-"+e.liveEnvironment())
	lab, err := runner.NewLab(e.spec, runner.LabOptions{
		Root:        e.options.Root,
		Binary:      e.options.Binary,
		Environment: e.liveEnvironment(),
		SandboxRoot: liveRoot,
		Mutate:      true,
		Credentials: e.options.Credentials,
		Redactor:    e.redactor,
		Registry:    e.resources,
		Log:         e.logf,
	})
	if err != nil {
		e.logf("could not prepare the live environment: %v", err)
		return nil
	}
	e.logf("%s", lab.Describe)
	// The live lab is deliberately not closed here: Module 30 needs it to
	// verify that what was created is gone.
	return lab.Env
}

// performCleanup is the second half of Module 30. Deleting is not the test;
// confirming the deletion is.
func (e *Engine) performCleanup(ctx context.Context, result *ModuleResult) {
	resources := e.resources.TeardownOrder()
	if len(resources) == 0 {
		e.logf("cleanup: nothing was registered")
		e.run.Resources = nil
		return
	}
	e.logf("cleanup: %d registered resources, in dependency order", len(resources))

	for _, resource := range resources {
		if len(resource.Cleanup) > 0 {
			outcome := e.lab.Runner.Run(ctx, resource.Cleanup...)
			if !outcome.OK() {
				e.logf("cleanup: %s %s did not delete cleanly: %s",
					resource.Type, resource.Name, firstLine(outcome.Output()))
			}
		}

		status, detail := e.verifyGone(ctx, resource)
		e.resources.Update(resource.ID, status, detail)

		assertion := checks.Assertion{
			Name:   fmt.Sprintf("%s %s is gone", resource.Type, resource.Name),
			Status: checks.StatusPass,
			Detail: detail,
		}
		if status == registry.StatusFailed {
			assertion.Status = checks.StatusFail
		}
		e.attachCleanupAssertion(result, assertion)
	}

	e.run.Resources = e.resources.All()
	_ = e.resources.Save(filepath.Join(e.run.Root, "cleanup", "resources.json"))

	if outstanding := e.resources.Outstanding(); len(outstanding) > 0 {
		result.State = StateFailed
		result.Message = fmt.Sprintf("%d resources were not confirmed gone", len(outstanding))
	}
}

// verifyGone asks the environment rather than trusting an exit code.
func (e *Engine) verifyGone(ctx context.Context, resource registry.Resource) (registry.CleanupStatus, string) {
	// A file or directory is answered by looking.
	if resource.Location != "" && (resource.Type == "file" || resource.Type == "directory" || resource.Type == "config") {
		if _, err := os.Stat(resource.Location); os.IsNotExist(err) {
			return registry.StatusDeleted, "no longer on disk"
		}
		if resource.Type == "config" {
			// The configuration lives inside the run workspace, which is
			// removed wholesale at the end; that is a deletion, just a later
			// one.
			return registry.StatusDeleted, "inside the run workspace, removed with it"
		}
		return registry.StatusFailed, "still present at " + resource.Location
	}

	if len(resource.Verify) == 0 {
		return registry.StatusNotFound, "nothing to verify against"
	}

	verifyCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(verifyCtx, resource.Verify[0], resource.Verify[1:]...)
	command.Env = e.lab.Runner.Env
	output, err := command.CombinedOutput()
	if err != nil {
		return registry.StatusNotFound, "could not ask: " + firstLine(string(output))
	}
	if strings.Contains(string(output), resource.Name) {
		return registry.StatusFailed, resource.Name + " is still listed by the provider"
	}
	return registry.StatusDeleted, "the provider no longer lists it"
}

func (e *Engine) attachCleanupAssertion(result *ModuleResult, assertion checks.Assertion) {
	// Cleanup verification is attached to a synthetic result so it appears in
	// the module alongside the checks, rather than only in the log.
	const id = "cleanup-registry-verification"
	for index := range result.Results {
		if result.Results[index].ID == id {
			result.Results[index].Assertions = append(result.Results[index].Assertions, assertion)
			if assertion.Status == checks.StatusFail {
				result.Results[index].Status = checks.StatusFail
			}
			result.summarise()
			return
		}
	}
	status := checks.StatusPass
	if assertion.Status == checks.StatusFail {
		status = checks.StatusFail
	}
	result.Results = append(result.Results, checks.Result{
		ID: id, Name: "Every registered resource is confirmed gone",
		Category: "cleanup", Environment: "workflow", Status: status,
		Assertions: []checks.Assertion{assertion}, Commands: []checks.Invocation{},
	})
	result.summarise()
}

func (e *Engine) markRemaining(from int, state ModuleState, message string) {
	for index := from; index < len(e.run.Modules); index++ {
		if e.run.Modules[index].State == StateNotStarted {
			e.run.Modules[index].State = state
			e.run.Modules[index].Message = message
		}
	}
}

// writeEvidence stores what one check did, redacted, under the run root.
func (e *Engine) writeEvidence(module Module, result checks.Result) {
	path := filepath.Join(e.run.Root, "evidence",
		fmt.Sprintf("%02d-%s", module.Order, module.ID))
	if err := os.MkdirAll(path, 0o700); err != nil {
		return
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return
	}
	// Redact once more on the way to disk. The runner already redacts command
	// output, but an assertion detail is composed by a check and has not been
	// through it.
	safe := e.redactor.String(string(encoded))
	_ = os.WriteFile(filepath.Join(path, result.ID+".json"), []byte(safe), 0o600)
}

func (e *Engine) summarise() {
	e.run.Finished = time.Now()
	e.run.Millis = e.run.Finished.Sub(e.run.Started).Milliseconds()
	e.run.Passed, e.run.Failed, e.run.Warning = 0, 0, 0
	e.run.Blocked, e.run.Skipped, e.run.NotRun = 0, 0, 0

	for _, module := range e.run.Modules {
		switch module.State {
		case StatePassed:
			e.run.Passed++
		case StateFailed:
			e.run.Failed++
		case StateWarning:
			e.run.Warning++
		case StateBlocked:
			e.run.Blocked++
		case StateSkipped, StateLocked:
			e.run.Skipped++
		default:
			e.run.NotRun++
		}
	}

	switch {
	case e.run.State == RunCancelling:
		e.run.State = RunFailed
		e.run.Message = "cancelled before every module ran"
	case e.run.Failed > 0:
		e.run.State = RunFailed
	case e.run.Warning > 0 || e.run.Blocked > 0 || e.run.Skipped > 0 || e.run.NotRun > 0:
		e.run.State = RunCompletedWithWarnings
	default:
		e.run.State = RunCompleted
	}
	e.run.Resources = e.resources.All()
}

// finish removes the workspace unless it was asked to be kept. Evidence and
// reports are moved out first when archiving is on.
func (e *Engine) finish() {
	if e.run == nil {
		return
	}
	e.save()
	if e.options.KeepWorkspace {
		e.logf("workspace kept at %s", e.run.Root)
		return
	}
	// Reports and evidence are the point of the run; only the sandbox goes.
	for _, directory := range []string{"home", "config", "state", "work", "generated", "fixtures"} {
		_ = os.RemoveAll(filepath.Join(e.run.Root, directory))
	}
	e.logf("sandbox removed; reports and evidence kept at %s", e.run.Root)
}

// save persists the run so an interrupted workflow can be resumed or cleaned
// up later. It holds no credential: the state is the result of the run, not
// its inputs.
func (e *Engine) save() {
	if e.run == nil {
		return
	}
	encoded, err := json.MarshalIndent(e.run, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(e.run.Root, "run.json"),
		[]byte(e.redactor.String(string(encoded))), 0o600)
}

// Redactor exposes the run's redactor so the report writer uses the same one.
func (e *Engine) Redactor() *redact.Redactor { return e.redactor }

// RecordGitOps attaches a promotion outcome to a run already saved on disk.
//
// The headless path needs this and save() cannot serve it: save() is a method
// on the Engine over the Engine's own in-memory run, and `bench gitops` runs in
// a process that shares neither. Without a writer the GitOps field is set by
// nobody, so `bench gitops status` can only ever answer "nothing recorded" and
// the evidence the action uploads never says a pull request was opened.
//
// Read-modify-write over the whole document rather than a patch: run.json is a
// single file, the run that produced it exited long ago, and the promotion is
// the only writer left, so there is nothing to race.
//
// Redacted on the way out like every other artefact. The result is already
// credential-stripped by the package that built it, which makes this the second
// guard rather than the only one — and the second guard is the one that still
// holds when somebody adds a field to Result later.
func RecordGitOps(path string, result *gitopsupdate.Result, redactor *redact.Redactor) error {
	run, err := LoadRun(path)
	if err != nil {
		return err
	}
	run.GitOps = result

	encoded, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	text := string(encoded)
	if redactor != nil {
		text = redactor.String(text)
	}
	return os.WriteFile(path, []byte(text), 0o600)
}

// LoadRun reads a saved run.
func LoadRun(path string) (*Run, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	run := &Run{}
	if err := json.Unmarshal(raw, run); err != nil {
		return nil, err
	}
	return run, nil
}

// FindRuns lists saved runs, newest first.
func FindRuns(dataDir string) ([]*Run, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, "runs"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var runs []*Run
	for index := len(entries) - 1; index >= 0; index-- {
		if !entries[index].IsDir() {
			continue
		}
		run, err := LoadRun(filepath.Join(dataDir, "runs", entries[index].Name(), "run.json"))
		if err != nil {
			continue
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func hostDescription() string {
	description := runtime.GOOS + "/" + runtime.GOARCH
	if raw, err := os.ReadFile("/proc/version"); err == nil &&
		strings.Contains(strings.ToLower(string(raw)), "microsoft") {
		description = "WSL on Windows · " + description
	}
	if name, err := os.Hostname(); err == nil {
		description += " · " + name
	}
	return description
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if index := strings.IndexByte(s, '\n'); index >= 0 {
		s = s[:index]
	}
	if len(s) > 140 {
		s = s[:140] + "..."
	}
	return s
}
