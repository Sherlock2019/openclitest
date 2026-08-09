package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/opencenter-cloud/opencli-testbench/internal/checks"
	"github.com/opencenter-cloud/opencli-testbench/internal/gitopsupdate"
	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func loadPlan(t *testing.T) (*spec.Spec, *Plan) {
	t.Helper()
	root := repoRoot(t)
	loaded, err := spec.Load(root)
	if err != nil {
		t.Fatalf("loading config/: %v", err)
	}
	plan, err := LoadPlan(loaded, root)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	return loaded, plan
}

// The order is the whole point of the continuous workflow, so it is asserted
// against the checklist rather than against a copy of it.
func TestModuleOrderMatchesTheChecklistExactly(t *testing.T) {
	loaded, plan := loadPlan(t)

	if len(plan.Modules) != len(loaded.Categories) {
		t.Fatalf("plan has %d modules, the checklist has %d rows",
			len(plan.Modules), len(loaded.Categories))
	}
	for index, module := range plan.Modules {
		if module.ID != loaded.Categories[index].ID {
			t.Errorf("module %d is %q, the checklist says %q",
				index+1, module.ID, loaded.Categories[index].ID)
		}
		if module.Order != index+1 {
			t.Errorf("module %q has order %d, want %d", module.ID, module.Order, index+1)
		}
	}
}

func TestThereAreThirtyModules(t *testing.T) {
	_, plan := loadPlan(t)
	if len(plan.Modules) != 30 {
		t.Errorf("the workflow has %d modules, the specification says 30", len(plan.Modules))
	}
}

// A module in no phase would never be displayed and, worse, would look like a
// gap rather than an omission.
func TestEveryModuleIsInExactlyOnePhase(t *testing.T) {
	_, plan := loadPlan(t)

	seen := map[string]int{}
	for _, phase := range plan.Phases {
		for _, moduleID := range phase.Modules {
			seen[moduleID]++
		}
	}
	for _, module := range plan.Modules {
		switch seen[module.ID] {
		case 1:
		case 0:
			t.Errorf("module %q is in no phase", module.ID)
		default:
			t.Errorf("module %q is in %d phases", module.ID, seen[module.ID])
		}
	}
}

func TestEveryModuleResolvesToAtLeastOneCheck(t *testing.T) {
	_, plan := loadPlan(t)
	for _, module := range plan.Modules {
		if len(module.CheckIDs) == 0 {
			t.Errorf("module %d (%s) has no checks, so it can never answer its question",
				module.Order, module.ID)
		}
	}
}

func TestLiveModulesAreMarkedAndGated(t *testing.T) {
	_, plan := loadPlan(t)

	live := 0
	for _, module := range plan.Modules {
		if !module.Live {
			continue
		}
		live++
		if len(module.Requires) == 0 {
			t.Errorf("live module %q declares no prerequisites", module.ID)
		}
	}
	if live == 0 {
		t.Error("no module is marked live, so Module 29 would run unguarded")
	}
}

func TestPhasesAreInOrder(t *testing.T) {
	_, plan := loadPlan(t)
	for index := 1; index < len(plan.Phases); index++ {
		if plan.Phases[index].Order < plan.Phases[index-1].Order {
			t.Errorf("phase %q comes after %q but has a lower order",
				plan.Phases[index].ID, plan.Phases[index-1].ID)
		}
	}
}

func TestSafetyAgreementHasConfirmations(t *testing.T) {
	_, plan := loadPlan(t)
	if len(plan.Safety.Confirmations) < 3 {
		t.Errorf("the safety agreement has %d confirmations; the specification requires three",
			len(plan.Safety.Confirmations))
	}
	for _, confirmation := range plan.Safety.Confirmations {
		if confirmation.ID == "" || confirmation.Text == "" {
			t.Errorf("confirmation %+v is incomplete", confirmation)
		}
	}
}

// Everything below is the rule that matters most in a report: a module that
// did not run is never a pass.

func TestExecutedIsFalseForEverythingThatDidNotRun(t *testing.T) {
	ran := []ModuleState{StatePassed, StateWarning, StateFailed}
	didNot := []ModuleState{
		StateNotStarted, StateBlocked, StateSkipped, StateLocked,
		StateCancelled, StateReady, StateRunning,
	}
	for _, state := range ran {
		if !state.Executed() {
			t.Errorf("%q should count as executed", state)
		}
	}
	for _, state := range didNot {
		if state.Executed() {
			t.Errorf("%q must not count as executed", state)
		}
	}
}

func TestAllChecksSkippedIsAWarningNotAPass(t *testing.T) {
	result := &ModuleResult{
		Results: []checks.Result{
			{Status: checks.StatusSkip},
			{Status: checks.StatusSkip},
		},
	}
	result.summarise()
	if result.State != StateWarning {
		t.Errorf("a module whose every check skipped reported %q, want %q", result.State, StateWarning)
	}
}

func TestAnyFailureFailsTheModule(t *testing.T) {
	result := &ModuleResult{
		Results: []checks.Result{
			{Status: checks.StatusPass},
			{Status: checks.StatusFail},
			{Status: checks.StatusPass},
		},
	}
	result.summarise()
	if result.State != StateFailed {
		t.Errorf("got %q, want %q", result.State, StateFailed)
	}
	if result.Passed != 2 || result.Failed != 1 {
		t.Errorf("counts are wrong: %d passed, %d failed", result.Passed, result.Failed)
	}
}

func TestPartialSkipIsAWarning(t *testing.T) {
	result := &ModuleResult{
		Results: []checks.Result{
			{Status: checks.StatusPass},
			{Status: checks.StatusSkip},
		},
	}
	result.summarise()
	if result.State != StateWarning {
		t.Errorf("got %q, want %q", result.State, StateWarning)
	}
	if !strings.Contains(result.Message, "skipped") {
		t.Errorf("the warning does not say what was skipped: %q", result.Message)
	}
}

// The JUnit representation of an unrun module is asserted in the report
// package, which owns the writer.

// --- recording the promotion outcome -------------------------------------------

// The headless promotion runs in its own process, so the only thing connecting
// it to the run it promoted is this file. If it does not round-trip, `bench
// gitops status` answers "nothing recorded" for a pull request that exists.
func TestRecordGitOpsRoundTripsThroughRunJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(path, []byte(`{"id":"run-1","passed":3}`), 0o600); err != nil {
		t.Fatalf("seeding run.json: %v", err)
	}

	result := &gitopsupdate.Result{
		Status:      gitopsupdate.StatusPRCreated,
		Mode:        gitopsupdate.ModeApproved,
		Eligible:    true,
		Changed:     true,
		PullRequest: "https://github.com/owner/name/pull/7",
		Message:     "pull request opened",
	}
	if err := RecordGitOps(path, result, nil); err != nil {
		t.Fatalf("RecordGitOps: %v", err)
	}

	back, err := LoadRun(path)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if back.GitOps == nil {
		t.Fatal("the GitOps result was not recorded against the run")
	}
	if back.GitOps.PullRequest != result.PullRequest {
		t.Errorf("pull request is %q, want %q", back.GitOps.PullRequest, result.PullRequest)
	}
	if back.GitOps.Status != gitopsupdate.StatusPRCreated {
		t.Errorf("status is %q, want %q", back.GitOps.Status, gitopsupdate.StatusPRCreated)
	}
	// The rest of the run has to survive. A promotion that overwrites the result
	// it was promoting would destroy the evidence it exists to publish.
	if back.ID != "run-1" || back.Passed != 3 {
		t.Errorf("recording the result damaged the run: id=%q passed=%d", back.ID, back.Passed)
	}
}

// Nil means the stage did not run, and the reports keep that distinct from a
// stage that ran and proposed nothing. An omitted key is what carries that.
func TestARunWithNoPromotionOmitsTheField(t *testing.T) {
	encoded, err := json.Marshal(&Run{ID: "run-2"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "gitopsUpdate") {
		t.Errorf("a run that never promoted still carries a gitopsUpdate key: %s", encoded)
	}
}

// The token registered while cloning must not survive into the run on disk.
func TestRecordGitOpsRedactsWhatItWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(path, []byte(`{"id":"run-3"}`), 0o600); err != nil {
		t.Fatalf("seeding run.json: %v", err)
	}

	secret := "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
	redactor := redact.New()
	redactor.Add(secret)

	err := RecordGitOps(path, &gitopsupdate.Result{
		Status:  gitopsupdate.StatusFailed,
		Message: "push rejected for " + secret,
	}, redactor)
	if err != nil {
		t.Fatalf("RecordGitOps: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Error("the recorded run contains the token verbatim")
	}
}
