package actionsetup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencenter-cloud/opencli-testbench/internal/gitopsupdate"
	"gopkg.in/yaml.v3"
)

// --- rendering ------------------------------------------------------------------

// The file is the whole deliverable. If it does not parse, every other test
// here is testing the delivery of something broken.
func TestTheRenderedWorkflowIsValidYAML(t *testing.T) {
	for _, options := range []Options{
		{},
		{TargetRepository: "owner/name"},
		{TargetRepository: "owner/name", GitOpsRepository: "owner/gitops"},
		{GitOpsRepository: "git@github.com:owner/gitops.git"},
	} {
		var parsed map[string]any
		if err := yaml.Unmarshal(Workflow(options), &parsed); err != nil {
			t.Fatalf("Workflow(%+v) does not parse: %v\n%s", options, err, Workflow(options))
		}
		if parsed["jobs"] == nil {
			t.Errorf("Workflow(%+v) has no jobs", options)
		}
	}
}

// Blank means "test whoever calls me", which is what makes one file work in any
// repository. Naming a repository that was never asked for would send every
// fork's CI at the original.
func TestWithoutATargetTheWorkflowNamesNoRepository(t *testing.T) {
	rendered := string(Workflow(Options{}))
	if strings.Contains(rendered, "opencenter_cli_repository") {
		t.Errorf("a workflow with no target still pins a repository:\n%s", rendered)
	}
	// The with: block exists now, because mode is always written. What must
	// never happen is an empty one — that is a syntax error, and it is the
	// reason the block used to be conditional. Asserted on the content rather
	// than on the absence of the keyword.
	if !strings.Contains(rendered, "mode: commands") {
		t.Errorf("the workflow does not name its mode:\n%s", rendered)
	}
	assertWithBlockIsNotEmpty(t, rendered)
}

// assertWithBlockIsNotEmpty fails if `with:` is written with nothing under it.
func assertWithBlockIsNotEmpty(t *testing.T, rendered string) {
	t.Helper()
	const marker = "        with:\n"
	index := strings.Index(rendered, marker)
	if index < 0 {
		return
	}
	first := strings.SplitN(rendered[index+len(marker):], "\n", 2)[0]
	if !strings.HasPrefix(first, "          ") || strings.TrimSpace(first) == "" {
		t.Errorf("an empty with: block is a syntax error:\n%s", rendered)
	}
}

// Promotion is all-or-nothing. A file carrying half the settings looks
// configured and is not, and the failure lands in somebody else's repository.
func TestPromotionSettingsAppearTogetherOrNotAtAll(t *testing.T) {
	off := string(Workflow(Options{TargetRepository: "owner/name"}))
	for _, absent := range []string{"gitops_repository", "GITOPS_TOKEN", "packages: write", "publish:"} {
		if strings.Contains(off, absent) {
			t.Errorf("promotion is off but %q is in the file", absent)
		}
	}

	on := string(Workflow(Options{TargetRepository: "owner/name", GitOpsRepository: "owner/gitops"}))
	for _, present := range []string{"gitops_repository", "GITOPS_TOKEN", "packages: write", "publish:"} {
		if !strings.Contains(on, present) {
			t.Errorf("promotion is on but %q is missing", present)
		}
	}
}

// The console's Environment selection has to reach the file, or CI silently
// tests emulated openstack for ever and a vmware code path goes green without
// anything having looked at it.
func TestTheEnvironmentSelectionReachesTheWorkflow(t *testing.T) {
	rendered := string(Workflow(Options{EnvironmentMode: "kind", Provider: "vmware"}))
	for _, want := range []string{"environment_mode: kind", "provider: vmware"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the workflow does not carry %q:\n%s", want, rendered)
		}
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
		t.Fatalf("the workflow does not parse: %v", err)
	}
}

// real is the one mode a GitHub runner cannot do: no credentials for a private
// cloud, no route to one. Writing it would fail every run in a way that reads
// as a broken bench rather than an impossible request.
func TestRealModeIsNotWrittenIntoTheWorkflow(t *testing.T) {
	rendered := string(Workflow(Options{EnvironmentMode: "real", Provider: "openstack"}))
	if strings.Contains(rendered, "environment_mode: real") {
		t.Errorf("real was written into a workflow that cannot run it:\n%s", rendered)
	}
	// The provider still travels: which provider to emulate is a real choice
	// even when the mode falls back.
	if !strings.Contains(rendered, "provider: openstack") {
		t.Error("the provider was dropped along with the mode")
	}
}

// Nothing selected must not produce an empty with:, which is a syntax error.
//
// The block is unconditional now — mode is always in it — so the property to
// hold is that it always has content, not that it is sometimes absent.
func TestNoSelectionStillLeavesAUsableWithBlock(t *testing.T) {
	rendered := string(Workflow(Options{}))
	assertWithBlockIsNotEmpty(t, rendered)
	if !strings.Contains(rendered, "mode: commands") {
		t.Errorf("a workflow with nothing selected does not name its mode:\n%s", rendered)
	}
}

// An ssh remote cannot authenticate with a token, so the key has to be wired in
// or the first publishing run fails at clone.
func TestAnSSHGitOpsRemoteAlsoWiresADeployKey(t *testing.T) {
	ssh := string(Workflow(Options{GitOpsRepository: "git@github.com:owner/gitops.git"}))
	if !strings.Contains(ssh, "GITOPS_SSH_KEY") {
		t.Error("an ssh GitOps remote did not wire GITOPS_SSH_KEY")
	}
	https := string(Workflow(Options{GitOpsRepository: "owner/gitops"}))
	if strings.Contains(https, "GITOPS_SSH_KEY") {
		t.Error("an https GitOps remote asked for a deploy key it cannot use")
	}
}

// A push or a fork's pull request must never publish, whatever secrets exist.
func TestPublishingIsGatedInsideTheGeneratedWorkflow(t *testing.T) {
	rendered := string(Workflow(Options{GitOpsRepository: "owner/gitops"}))
	if !strings.Contains(rendered, "github.event_name == 'workflow_dispatch'") {
		t.Error("publish is not gated on a manual run")
	}
	if !strings.Contains(rendered, "inputs.publish == true") {
		t.Error("publish is not gated on the operator ticking the box")
	}
}

// --- installing -----------------------------------------------------------------

// remote builds a bare repository standing in for the target, optionally with a
// workflow already in it — which is the realistic case, not the empty one.
func remote(t *testing.T, existing string) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "target.git")
	seed := filepath.Join(dir, "seed")

	run := func(where string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = where
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	run(dir, "init", "--bare", "--initial-branch=main", bare)
	run(seed, "init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# cli\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if existing != "" {
		if err := os.MkdirAll(filepath.Join(seed, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(seed, WorkflowPath), []byte(existing), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(seed, "add", "-A")
	run(seed, "commit", "-m", "seed")
	run(seed, "remote", "add", "origin", bare)
	run(seed, "push", "-q", "origin", "main")
	return "file://" + bare
}

func request(t *testing.T, url string, mode Mode, approved bool) Request {
	t.Helper()
	config := gitopsupdate.Load(nil, nil)
	config.Repository = url
	config.BaseBranch = "main"
	// A bare repository on disk has no API, so no pull request is possible and
	// none is attempted.
	config.CreatePR = false
	return Request{
		Config:      config,
		Options:     Options{Action: "Owner/bench@v1"},
		Approval:    Approval{GateSet: approved, Approved: approved},
		Mode:        mode,
		SandboxRoot: t.TempDir(),
	}
}

// A remote with no GitHub API behind it — a local path, a mirror — still gets
// the file. The push is the work; the pull request is a convenience on top, and
// reporting a failure when the branch landed sends somebody hunting a bug that
// is not there.
func TestABranchWithoutAnAPIIsASuccessNotAFailure(t *testing.T) {
	url := remote(t, "")
	result := Install(context.Background(), request(t, url, ModeApproved, true))

	if result.Status != StatusPushed {
		t.Fatalf("status is %q, want %q: %s", result.Status, StatusPushed, result.Message)
	}
	if result.ExitCode() != ExitOK {
		t.Errorf("exit code is %d, want 0 — the file landed", result.ExitCode())
	}
	for _, step := range result.Steps {
		if step.ID == StepPullRequest && step.Status != StepSkipped {
			t.Errorf("pull-request is %q, want skipped: %s", step.Status, step.Detail)
		}
		if step.ID == StepPush && step.Status != StepOK {
			t.Errorf("push did not succeed: %s", step.Detail)
		}
	}
	if !strings.Contains(result.Message, "No pull request") {
		t.Errorf("the message does not say a request is still to be opened: %q", result.Message)
	}
}

func TestInstallAddsTheWorkflowAndPushesABranch(t *testing.T) {
	url := remote(t, "")
	result := Install(context.Background(), request(t, url, ModeApproved, true))

	if result.Status != StatusPushed {
		t.Fatalf("status is %q: %s", result.Status, result.Message)
	}
	if !result.Changed || result.Existing {
		t.Errorf("changed=%v existing=%v, want a fresh install", result.Changed, result.Existing)
	}
	if result.Branch != BranchName {
		t.Errorf("branch is %q", result.Branch)
	}
}

// The realistic case: a workflow is already there, pointing somewhere else. A
// blind create would fail; this has to update in place.
func TestAnExistingWorkflowIsUpdatedRatherThanRefused(t *testing.T) {
	url := remote(t, "name: Test Bench\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n")
	result := Install(context.Background(), request(t, url, ModeApproved, true))

	if result.Status != StatusPushed {
		t.Fatalf("status is %q: %s", result.Status, result.Message)
	}
	if !result.Existing {
		t.Error("an existing workflow was not detected")
	}
	if !result.Changed {
		t.Error("the differing workflow was not changed")
	}
}

// The destructive case, and the one that only showed up against a real
// repository: a workflow that already calls this bench but has been customised
// since. Rendering is not a merge — it emits the canonical file and nothing
// else — so installing over it deletes every setting somebody added. Measured
// on a real repository that was 97 lines removed for 4 added.
func TestACustomisedWorkflowIsNotSilentlyReplaced(t *testing.T) {
	customised := "name: Test Bench\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n" +
		"    steps:\n      - uses: Owner/bench@v1\n        with:\n" +
		"          gitops_repository: owner/gitops\n" +
		"          provider: openstack\n"
	url := remote(t, customised)

	req := request(t, url, ModeApproved, true)
	result := Install(context.Background(), req)

	if result.Status != StatusBlocked {
		t.Fatalf("status is %q, want blocked: %s", result.Status, result.Message)
	}
	if result.Changed {
		t.Error("a blocked install reported a change")
	}
	// The cost has to be in the message. "This would overwrite something" is
	// ignorable; "this removes 97 lines" is not.
	if !strings.Contains(result.Message, "line(s)") {
		t.Errorf("the refusal does not say how much would be lost: %q", result.Message)
	}
	bare := strings.TrimPrefix(url, "file://")
	out, _ := exec.Command("git", "--git-dir="+bare, "for-each-ref",
		"--format=%(refname:short)", "refs/heads").CombinedOutput()
	if strings.Contains(string(out), BranchName) {
		t.Errorf("a blocked install still pushed: %s", out)
	}

	// Saying so explicitly goes through. A fresh request, because Install clones
	// into SandboxRoot and the first run already put a checkout there — reusing
	// it fails at the clone and would pass this assertion for the wrong reason.
	second := request(t, url, ModeApproved, true)
	second.Options.Replace = true
	result = Install(context.Background(), second)
	if result.Status == StatusBlocked {
		t.Fatalf("replace:true was still blocked: %s", result.Message)
	}
	if !result.Changed {
		t.Fatalf("replace:true changed nothing; status %q: %s", result.Status, result.Message)
	}
}

// Matched on owner/repo, not on the ref: a repository pinned to @v1 is still
// wired up when the default names @main, and asking "is this already ours"
// must not be confused with "is it the same version".
func TestAPinnedVersionStillCountsAsWiredUp(t *testing.T) {
	for _, testCase := range []struct {
		workflow, action string
		want             bool
	}{
		{"uses: Owner/bench@v1.2", "Owner/bench@main", true},
		{"uses: Owner/bench@main", "Owner/bench@main", true},
		{"uses: Someone/else@main", "Owner/bench@main", false},
		{"no uses at all", "Owner/bench@main", false},
	} {
		if got := callsThisBench(testCase.workflow, testCase.action); got != testCase.want {
			t.Errorf("callsThisBench(%q, %q) = %v", testCase.workflow, testCase.action, got)
		}
	}
}

// Running twice must not open a second pull request or make an empty commit.
func TestAnIdenticalWorkflowIsLeftAlone(t *testing.T) {
	rendered := string(Workflow(Options{Action: "Owner/bench@v1"}))
	url := remote(t, rendered)
	result := Install(context.Background(), request(t, url, ModeApproved, true))

	if result.Status != StatusUnchanged {
		t.Fatalf("status is %q, want %q: %s", result.Status, StatusUnchanged, result.Message)
	}
	if result.Changed {
		t.Error("an identical file was reported as changed")
	}
	if result.Branch != "" {
		t.Error("a branch was created for a no-op")
	}
}

func TestPreviewWritesNothingRemote(t *testing.T) {
	url := remote(t, "")
	result := Install(context.Background(), request(t, url, ModePreview, false))

	if result.Status != StatusPreview {
		t.Fatalf("status is %q: %s", result.Status, result.Message)
	}
	for _, step := range result.Steps {
		if step.ID == StepPush && step.Status == StepOK {
			t.Error("preview pushed")
		}
	}
	bare := strings.TrimPrefix(url, "file://")
	out, _ := exec.Command("git", "--git-dir="+bare, "for-each-ref",
		"--format=%(refname:short)", "refs/heads").CombinedOutput()
	if strings.Contains(string(out), BranchName) {
		t.Errorf("preview left a branch on the remote: %s", out)
	}
}

// Both gates, and neither alone. Same rule the promotion stage enforces.
func TestNeitherGateAloneWrites(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		gateSet, approved bool
	}{
		{"neither", false, false},
		{"gate only", true, false},
		{"approval only", false, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			url := remote(t, "")
			req := request(t, url, ModeApproved, false)
			req.Approval = Approval{
				GateSet: testCase.gateSet, Approved: testCase.approved,
			}
			result := Install(context.Background(), req)
			if result.Status != StatusBlocked {
				t.Fatalf("status is %q, want blocked", result.Status)
			}
			bare := strings.TrimPrefix(url, "file://")
			out, _ := exec.Command("git", "--git-dir="+bare, "for-each-ref",
				"--format=%(refname:short)", "refs/heads").CombinedOutput()
			if strings.Contains(string(out), BranchName) {
				t.Errorf("a refused run still pushed: %s", out)
			}
		})
	}
}

// The guard that makes "let the bench configure my repository" safe to agree
// to: whatever else goes wrong, only the workflow file can be in the commit.
func TestOnlyTheWorkflowPathMayEverChange(t *testing.T) {
	if WorkflowPath != ".github/workflows/test-bench.yml" {
		t.Fatalf("the single writable path moved to %q; the guard must move with it", WorkflowPath)
	}
	url := remote(t, "")
	result := Install(context.Background(), request(t, url, ModePreview, false))
	for _, step := range result.Steps {
		if step.ID == StepValidate && step.Status != StepOK {
			t.Fatalf("validate did not pass: %s", step.Detail)
		}
	}
	if !strings.Contains(result.Diff, "test-bench.yml") {
		t.Errorf("the diff does not mention the workflow:\n%s", result.Diff)
	}
}

func TestNoRepositoryIsAClearRefusal(t *testing.T) {
	req := request(t, "", ModePreview, false)
	req.Config.Repository = ""
	result := Install(context.Background(), req)
	if result.Status != StatusFailed {
		t.Fatalf("status is %q", result.Status)
	}
	if !strings.Contains(result.Message, "repository") {
		t.Errorf("the message does not name the missing setting: %q", result.Message)
	}
}

// The failure everyone will hit first. A token with contents:write cannot write
// under .github/workflows, and GitHub says so in wording that reads like a bug
// in the tool. If this message is not actionable the feature is not usable.
func TestTheMissingWorkflowScopeIsExplained(t *testing.T) {
	explained := explainPushFailure(
		errRefused("refusing to allow a Personal Access Token to create or update " +
			"workflow `.github/workflows/test-bench.yml` without `workflow` scope"))

	for _, want := range []string{"workflow", "scope", "contents:write is not enough"} {
		if !strings.Contains(explained, want) {
			t.Errorf("the explanation omits %q:\n%s", want, explained)
		}
	}
	if !strings.Contains(explained, "GITHUB_TOKEN") {
		t.Error("the explanation does not mention that GITHUB_TOKEN can never do it")
	}

	// An unrelated failure must pass through unchanged rather than being
	// explained as a scope problem it is not.
	other := explainPushFailure(errRefused("Could not resolve host: github.com"))
	if strings.Contains(other, "contents:write") {
		t.Errorf("a DNS failure was explained as a token scope problem:\n%s", other)
	}
}

type errRefused string

func (e errRefused) Error() string { return string(e) }
