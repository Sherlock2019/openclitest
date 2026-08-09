package actionsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencenter-cloud/opencli-testbench/internal/e2e"
	"gopkg.in/yaml.v3"
)

// The two kinds must stay two known files. The whole safety argument for "let
// the bench configure my repository" is that the blast radius is one named path
// out of a closed set — not an arbitrary write under .github/workflows.
func TestEachKindHasItsOwnConstantPath(t *testing.T) {
	if KindTestBench.Path() != ".github/workflows/test-bench.yml" {
		t.Fatalf("the command bench path moved to %q; the guard must move with it",
			KindTestBench.Path())
	}
	if KindE2E.Path() != ".github/workflows/opencenter-e2e.yml" {
		t.Fatalf("the lifecycle path moved to %q; the guard must move with it",
			KindE2E.Path())
	}
	if KindTestBench.Path() == KindE2E.Path() {
		t.Fatal("both kinds write the same file, so installing one would destroy the other")
	}
	if KindTestBench.Branch() == KindE2E.Branch() {
		t.Fatal("both kinds use one branch, so installing both would put two unrelated " +
			"files on it")
	}
}

// An empty kind is every caller written before there were two, and all of them
// meant the command bench.
func TestAnEmptyKindIsTheCommandBench(t *testing.T) {
	var unset Kind
	if unset.Path() != KindTestBench.Path() {
		t.Fatalf("an unset kind resolves to %q", unset.Path())
	}
	if !unset.Valid() {
		t.Fatal("an unset kind is refused, which breaks every existing caller")
	}
	if Kind("something-else").Valid() {
		t.Fatal("an unknown kind is accepted; a typo would install the other workflow")
	}
}

func TestTheRenderedLifecycleWorkflowIsValidYAML(t *testing.T) {
	rendered := Workflow(Options{Kind: KindE2E, E2E: E2EOptions{
		CLIRepo: "opencenter-cloud/openCenter-cli", Nightly: "kind",
		RealEnvironment: "e2e-real-provider", TimeoutMinutes: 45,
	}})
	var parsed map[string]any
	if err := yaml.Unmarshal(rendered, &parsed); err != nil {
		t.Fatalf("the workflow is not valid YAML: %v\n\n%s", err, rendered)
	}
	if _, ok := parsed["jobs"]; !ok {
		t.Fatal("the workflow declares no jobs")
	}
}

// The one that matters most. A pull request — which a fork can open — must
// never be able to run a profile that creates infrastructure or spends money.
func TestAPullRequestOnlyRunsProfilesThatCreateNothing(t *testing.T) {
	rendered := string(Workflow(Options{Kind: KindE2E, E2E: E2EOptions{Nightly: "kind"}}))

	// Only the safe job's own matrix. The workflow_dispatch input lists every
	// profile by design — a person choosing one deliberately is the whole point
	// of dispatch — so cutting from the top would test the wrong block and pass
	// for the wrong reason.
	start := strings.Index(rendered, "\n  safe:\n")
	if start < 0 {
		t.Fatal("cannot find the pull-request job")
	}
	safe, _, found := strings.Cut(rendered[start:], "  # The full lifecycle")
	if !found {
		t.Fatal("cannot find where the pull-request job ends")
	}
	// The line is LiveApproval, not Deploys.
	//
	// It used to be Deploys, and that kept Kind off the matrix — so every
	// automatic run tested everything except standing a cluster up and taking it
	// down, which is what the bench is for. Kind deploys containers on the
	// runner's own Docker: GitHub provides it, throws it away when the job ends,
	// bills nothing and involves no credential.
	//
	// What must never appear here is a profile that reaches somebody's real
	// infrastructure or spends money, and that is exactly what LiveApproval
	// marks. A fork's pull request runs this matrix, so this test is the whole
	// safety argument for that.
	for _, profile := range e2e.Profiles {
		if !profile.LiveApproval {
			continue
		}
		if strings.Contains(safe, "- "+profile.Name+"\n") {
			t.Errorf("%s reaches real infrastructure and is on the pull-request "+
				"matrix, which a fork can trigger", profile.Name)
		}
	}

	// And Kind is on it, because leaving it off is the mistake this rule change
	// was making.
	if !strings.Contains(safe, "- kind\n") {
		t.Error("kind is not on the automatic matrix, so no automatic run ever " +
			"builds a cluster")
	}
	// And the safe ones are all actually there, or CI silently tests less than
	// it claims to.
	for _, name := range safeProfiles() {
		if !strings.Contains(safe, "- "+name+"\n") {
			t.Errorf("%s creates nothing but is missing from the pull-request matrix", name)
		}
	}
}

// Without an environment there is no human in the way, so the job is left out
// entirely rather than written and hoped over.
func TestRealProviderJobsNeedAnEnvironment(t *testing.T) {
	without := string(Workflow(Options{Kind: KindE2E}))
	if strings.Contains(without, "real-provider:") {
		t.Fatal("a real-provider job was written with no GitHub Environment behind it")
	}
	// The approval reaches the action as an input now rather than as a flag on a
	// command line, but it must still be absent when there is no job to carry it.
	if strings.Contains(without, "approve_live") {
		t.Fatal("live approval appears in a workflow with no approval gate")
	}

	with := string(Workflow(Options{Kind: KindE2E,
		E2E: E2EOptions{RealEnvironment: "e2e-real-provider"}}))
	if !strings.Contains(with, "environment: e2e-real-provider") {
		t.Fatal("the environment was not written into the real-provider job")
	}
	if !strings.Contains(with, `approve_live: "true"`) {
		t.Fatal("the real-provider job does not approve live infrastructure, so it " +
			"would be refused by the bench after a human had already approved it")
	}
	if !strings.Contains(with, "workflow_dispatch'") {
		t.Fatal("the real-provider job is reachable other than by workflow_dispatch")
	}
}

// A run that died mid-deploy is the one that left a cluster behind, so cleanup
// and evidence must not be conditional on success.
// Cleanup moved into the action, so this asserts it there. The property is
// unchanged and is the one that matters: a run that died mid-deploy is the one
// that left a cluster behind, so cleaning up must not depend on success.
func TestCleanupAlwaysRuns(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "action.yml"))
	if err != nil {
		t.Skipf("no action.yml: %v", err)
	}
	action := string(raw)

	cleanup := strings.Index(action, "Clean up whatever the lifecycle left")
	if cleanup < 0 {
		t.Fatal("the action never cleans up after a lifecycle run")
	}
	window := action[max(0, cleanup-200) : cleanup+200]
	if !strings.Contains(window, "if: always()") {
		t.Error("cleanup is conditional, so a failed run can leave a cluster running")
	}
}

// And the evidence upload, which lives in the action rather than the workflow.
//
// It was in the workflow, pointing at artifacts/ — the path the bench uses when
// run by hand, not the one it uses under this action. It found nothing on all
// four profiles. The action uploads from where it actually put the run.
func TestEvidenceAlwaysUploads(t *testing.T) {
	action, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}
	yml := string(action)
	upload := strings.Index(yml, "uses: actions/upload-artifact@v7")
	if upload < 0 {
		t.Fatal("nothing is uploaded, so a run leaves no evidence")
	}
	// Unconditional: a failed run's evidence is the evidence that matters.
	if window := yml[max(0, upload-200) : upload+200]; !strings.Contains(window, "if: always()") {
		t.Error("the evidence upload is conditional, so a failed run uploads nothing")
	}
	// And it must reach the lifecycle's own run directory. It listed runs/,
	// invocations.jsonl and the log — none of which the lifecycle writes — so
	// the entire upload came to 702 bytes.
	if !strings.Contains(yml, ".opencenter-test-bench/artifacts/e2e-*/") {
		t.Error("the upload does not include the lifecycle's run directory")
	}
}

// No schedule unless one was chosen. A cron that runs a default nobody picked
// is a nightly bill nobody agreed to.
func TestNoNightlyMeansNoCron(t *testing.T) {
	if strings.Contains(string(Workflow(Options{Kind: KindE2E})), "cron:") {
		t.Fatal("a schedule was written with no nightly profile chosen")
	}
	if !strings.Contains(string(Workflow(Options{Kind: KindE2E,
		E2E: E2EOptions{Nightly: "kind"}})), "cron:") {
		t.Fatal("a nightly profile was chosen and no schedule was written")
	}
}

// Secrets go in env:, never argv. An argument is visible in the process table
// and echoed by the step itself.
func TestSecretsNeverReachACommandLine(t *testing.T) {
	rendered := string(Workflow(Options{Kind: KindE2E,
		E2E: E2EOptions{RealEnvironment: "e2e-real-provider"}}))
	for _, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "secrets.") {
			continue
		}
		if strings.HasPrefix(trimmed, "-") || strings.Contains(trimmed, "--") {
			t.Errorf("a secret appears on a command line: %s", trimmed)
		}
	}
}

// Found on a real run, not by reasoning: `cluster init` writes a working SSH
// private key and an age private key into the cluster's secrets tree, which
// lives under the run directory. Uploading that directory whole publishes both
// as a downloadable artifact, on every commit, to anybody who can read the
// repository.
func TestPrivateKeysAreNeverUploaded(t *testing.T) {
	// The workflow must not upload at all. It did, from artifacts/, which is
	// where the bench writes when run by hand and not where it writes under the
	// action — so the steps found nothing and failed the job with
	// if-no-files-found: error, on all four profiles, after the lifecycle had
	// already finished and reported.
	rendered := string(Workflow(Options{Kind: KindE2E, E2E: E2EOptions{
		Nightly: "kind", RealEnvironment: "e2e-real-provider",
	}}))
	if strings.Contains(rendered, "actions/upload-artifact@v7") {
		t.Errorf("the workflow uploads evidence itself, which means guessing "+
			"where the action keeps its run directory:\n%s", rendered)
	}

	// The action is the one uploader, so the exclusions have to be there.
	action, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}
	yml := string(action)
	if !strings.Contains(yml, "actions/upload-artifact@v7") {
		t.Fatal("the action uploads nothing, so there is no evidence at all")
	}
	for _, pattern := range secretPaths {
		// The action works from .opencenter-test-bench/, so the same patterns
		// carry that prefix there.
		want := "!.opencenter-test-bench/" + strings.TrimPrefix(pattern, "!")
		if !strings.Contains(yml, want) {
			t.Errorf("action.yml does not exclude %q, so `cluster init`'s real "+
				"SSH and age private keys ship as a downloadable artifact", want)
		}
	}

	// And the uploaded name must distinguish the profiles. All four matrix jobs
	// uploaded opencenter-test-bench-<run id>, one name for four runs.
	if !strings.Contains(yml, "name: opencenter-test-bench-${{ inputs.profile }}-") {
		t.Error("every profile uploads under the same artifact name")
	}
}

// The two workflows must not be confusable by the run listing, or a dashboard
// reports one's green run as the other's.
func TestEachKindListsItsOwnRuns(t *testing.T) {
	if KindTestBench.File() == KindE2E.File() {
		t.Fatal("both kinds list runs of the same workflow file")
	}
}
