package gitopsupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The whole sequence against a real git, with no network anywhere.
//
// A bare repository on disk is a real remote as far as git is concerned: clone,
// branch, commit and push all take the same code path they would against
// GitHub. What it cannot exercise is the pull request, which has no equivalent
// on disk — that is covered separately against an httptest server, so between
// the two every line runs without either test needing to reach the internet.

// gitRequired skips when git is not installed. This is a Go project and a
// machine without git is unusual but not broken.
func gitRequired(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// remote builds a bare repository with a base branch, a kustomization to
// promote into, and one commit.
func remote(t *testing.T) string {
	t.Helper()
	gitRequired(t)

	dir := t.TempDir()
	bare := filepath.Join(dir, "gitops-remote.git")
	seed := filepath.Join(dir, "seed")

	run := func(cwd string, args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = cwd
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run(dir, "init", "--bare", "--initial-branch=main", bare)
	run(dir, "init", "--initial-branch=main", seed)
	writeKustomization(t, seed, "clusters/my-cluster/kustomization.yaml", "sha-000000")
	if err := WriteInto(seed, "README.md", []byte("# gitops\n")); err != nil {
		t.Fatal(err)
	}
	run(seed, "add", ".")
	run(seed, "commit", "-m", "seed")
	run(seed, "remote", "add", "origin", bare)
	run(seed, "push", "origin", "main")

	return "file://" + bare
}

// request builds a Request pointed at a bare repository, with a run that
// deserves promotion.
func request(t *testing.T, repository string, mode Mode) Request {
	t.Helper()
	sandbox := t.TempDir()
	bench := t.TempDir()
	reports := filepath.Join(bench, "artifacts", "runs", "run-1", "reports")
	if err := os.MkdirAll(reports, 0o755); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(reports, "report.json")
	if err := os.WriteFile(report, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	config := Load(nil, nil)
	config.Repository = repository
	config.BaseBranch = "main"
	config.ManifestPath = "clusters/my-cluster/kustomization.yaml"
	config.EvidencePath = DefaultEvidencePath
	config.ImageRepository = DefaultImageRepository
	config.ContainerName = DefaultContainerName

	return Request{
		Config: config,
		Run: RunSummary{
			RunID: "run-1", Completed: true, Passed: 89, CleanupState: CleanupPassed,
			ReportPath: report, SourceCommit: "e9b465b1122334455",
			SourceRepository: "opencenter-cloud/openCenter-cli", CLIVersion: "0.0.1",
			Environment: "kind",
		},
		Approval:    Approval{GateSet: true, Approved: true},
		Mode:        mode,
		SandboxRoot: sandbox,
		BenchRoot:   bench,
		ReportsDir:  reports,
		EvidenceDir: filepath.Join(bench, "artifacts", "runs", "run-1", "evidence"),
	}
}

// heads lists the branches on a bare repository, which is how the tests assert
// that a preview really wrote nothing.
func heads(t *testing.T, remoteURL string) []string {
	t.Helper()
	command := exec.Command("git", "ls-remote", "--heads", strings.TrimPrefix(remoteURL, "file://"))
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ls-remote: %v\n%s", err, out)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if _, ref, ok := strings.Cut(line, "refs/heads/"); ok {
			names = append(names, ref)
		}
	}
	return names
}

func TestFullSequenceAgainstABareRepository(t *testing.T) {
	remoteURL := remote(t)
	req := request(t, remoteURL, ModeApproved)

	result := Run(context.Background(), req)
	if result.Status != StatusPassed {
		t.Fatalf("status = %s: %s\nreasons: %v", result.Status, result.Message, result.Reasons)
	}
	if !result.Eligible || !result.Changed {
		t.Fatalf("result = %+v", result)
	}
	if result.Branch != BranchName("run-1") {
		t.Fatalf("branch = %q", result.Branch)
	}
	if result.CommitSHA == "" {
		t.Fatal("no commit sha recorded")
	}
	if result.ImageReference != DefaultImageRepository+":sha-e9b465b" {
		t.Fatalf("image = %q", result.ImageReference)
	}

	// Every step up to and including verify ran. A bare repository has no API,
	// so the pull request is skipped and says why.
	for _, id := range []string{
		StepPreflight, StepQualityGate, StepEvidence, StepCheckout, StepBranch,
		StepManifest, StepValidate, StepCommit, StepPush, StepVerify,
	} {
		step, _ := result.Step(id)
		if step.Status != StepOK {
			t.Errorf("step %s = %s (%s)", id, step.Status, step.Detail)
		}
	}
	if step, _ := result.Step(StepPullRequest); step.Status != StepSkipped {
		t.Errorf("pull request step = %s, want skipped on a non-GitHub remote", step.Status)
	}

	// The branch is really on the remote, and main was not touched.
	names := heads(t, remoteURL)
	if !contains(names, BranchName("run-1")) {
		t.Fatalf("the update branch is not on the remote: %v", names)
	}

	// Exactly the intended files, and nothing else.
	want := map[string]bool{
		"clusters/my-cluster/kustomization.yaml":       true,
		"test-evidence/opencenter-cli/latest.json":     true,
		"test-evidence/opencenter-cli/runs/run-1.json": true,
	}
	if len(result.FilesChanged) != len(want) {
		t.Fatalf("files changed = %v, want %d", result.FilesChanged, len(want))
	}
	for _, file := range result.FilesChanged {
		if !want[file] {
			t.Errorf("unexpected file in the change: %s", file)
		}
	}
}

func TestPreviewMakesNoRemoteChange(t *testing.T) {
	remoteURL := remote(t)
	before := heads(t, remoteURL)

	req := request(t, remoteURL, ModePreview)
	result := Run(context.Background(), req)

	if result.Status != StatusPreview {
		t.Fatalf("status = %s: %s", result.Status, result.Message)
	}
	if !result.Changed || len(result.FilesChanged) == 0 {
		t.Fatal("preview produced no proposed change")
	}
	// It prepared the whole thing and wrote nothing remote. That is the
	// contract: preview is the update with the writing turned off, not a
	// simulation of it.
	for _, id := range []string{StepCommit, StepPush, StepPullRequest} {
		step, _ := result.Step(id)
		if step.Status != StepSkipped {
			t.Errorf("step %s = %s in preview mode, want skipped", id, step.Status)
		}
	}
	if after := heads(t, remoteURL); len(after) != len(before) {
		t.Fatalf("preview changed the remote: %v → %v", before, after)
	}
	if result.PatchPath == "" {
		t.Fatal("preview wrote no patch to review")
	}
	if _, err := os.Stat(result.PatchPath); err != nil {
		t.Fatalf("the patch was not written: %v", err)
	}
}

func TestNeitherGateAloneMutates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approval Approval
	}{
		{"approval without the environment gate", Approval{Approved: true}},
		{"environment gate without approval", Approval{GateSet: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remoteURL := remote(t)
			before := heads(t, remoteURL)

			req := request(t, remoteURL, ModeApproved)
			req.Approval = tc.approval
			result := Run(context.Background(), req)

			if result.Status != StatusBlocked {
				t.Fatalf("status = %s: %s", result.Status, result.Message)
			}
			if result.CommitSHA != "" {
				t.Fatal("a commit was made with only one gate open")
			}
			if after := heads(t, remoteURL); len(after) != len(before) {
				t.Fatalf("the remote changed with only one gate open: %v → %v", before, after)
			}
			if result.ExitCode() != ExitApprovalMissing {
				t.Fatalf("exit code = %d, want %d", result.ExitCode(), ExitApprovalMissing)
			}
		})
	}
}

func TestAnIneligibleRunNeverReachesGit(t *testing.T) {
	remoteURL := remote(t)
	before := heads(t, remoteURL)

	req := request(t, remoteURL, ModeApproved)
	req.Run.CleanupState = CleanupFailed

	result := Run(context.Background(), req)
	if result.Status != StatusBlocked {
		t.Fatalf("status = %s", result.Status)
	}
	if !containsSubstring(result.Reasons, "cleanup did not complete") {
		t.Fatalf("reasons = %v", result.Reasons)
	}
	// Blocked before the checkout, not after it. Nothing was cloned and nothing
	// was written.
	if step, _ := result.Step(StepCheckout); step.Status != StepBlocked {
		t.Fatalf("checkout step = %s, want blocked", step.Status)
	}
	if after := heads(t, remoteURL); len(after) != len(before) {
		t.Fatal("an ineligible run changed the remote")
	}
}

func TestAMissingBaseBranchIsReportedClearly(t *testing.T) {
	req := request(t, remote(t), ModePreview)
	req.Config.BaseBranch = "does-not-exist"

	result := Run(context.Background(), req)
	if result.Status != StatusFailed {
		t.Fatalf("status = %s: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "does not exist") {
		t.Fatalf("message = %q", result.Message)
	}
}

func TestNoRepositoryIsNotConfiguredRatherThanFailed(t *testing.T) {
	req := request(t, "", ModePreview)
	result := Run(context.Background(), req)

	if result.Status != StatusNotConfigured {
		t.Fatalf("status = %s, want %s", result.Status, StatusNotConfigured)
	}
	// A bench nobody pointed at a delivery repository has not failed anything.
	if result.ExitCode() != ExitOK {
		t.Fatalf("exit code = %d, want %d", result.ExitCode(), ExitOK)
	}
}

func TestACheckoutOutsideTheSandboxIsRefused(t *testing.T) {
	gitRequired(t)
	config := Load(nil, nil)
	config.Repository = remote(t)

	_, err := Open(context.Background(), config, t.TempDir(),
		filepath.Join(t.TempDir(), "elsewhere"), nil)
	if err == nil || !strings.Contains(err.Error(), "outside the run sandbox") {
		t.Fatalf("Open() = %v", err)
	}
}

func TestEvidenceIsWrittenEvenInPreview(t *testing.T) {
	req := request(t, remote(t), ModePreview)
	result := Run(context.Background(), req)

	raw, err := os.ReadFile(result.EvidencePath)
	if err != nil {
		t.Fatalf("no evidence written in preview: %v", err)
	}
	var evidence Evidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.RunID != "run-1" || evidence.SourceCommit != "e9b465b1122334455" {
		t.Fatalf("evidence = %+v", evidence)
	}
}

// --- the pull request, against a mock GitHub -------------------------------------

func TestCreatePullRequest(t *testing.T) {
	t.Setenv(EnvToken, "ghp_0123456789abcdefghij")

	var gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			raw := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(raw)
			gotBody = string(raw)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/o/n/pull/42"}`))
		}))
	defer server.Close()

	client := NewClient(Config{APIBase: server.URL}, nil)
	pr, err := client.Create(context.Background(), "o/n", "automation/x", "main", "title", "body")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 42 || pr.URL != "https://github.com/o/n/pull/42" {
		t.Fatalf("pr = %+v", pr)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Fatalf("authorization header = %q", gotAuth)
	}
	// Never a draft: a draft notifies nobody, and a promotion nobody is told
	// about sits for a week.
	if !strings.Contains(gotBody, `"draft":false`) {
		t.Fatalf("request body = %s", gotBody)
	}
}

func TestDuplicatePullRequestIsFoundRatherThanFailed(t *testing.T) {
	t.Setenv(EnvToken, "ghp_0123456789abcdefghij")

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(
					`{"message":"Validation Failed","errors":[{"message":"A pull request already exists"}]}`))
				return
			}
			_, _ = w.Write([]byte(`[{"number":7,"html_url":"https://github.com/o/n/pull/7"}]`))
		}))
	defer server.Close()

	client := NewClient(Config{APIBase: server.URL}, nil)
	pr, err := client.Create(context.Background(), "o/n", "automation/x", "main", "t", "b")
	if err != nil {
		t.Fatalf("a re-run of the same run failed instead of finding its pull request: %v", err)
	}
	if pr.Number != 7 || !pr.Existing {
		t.Fatalf("pr = %+v", pr)
	}
}

func TestAReadOnlyTokenIsReportedAsSuch(t *testing.T) {
	t.Setenv(EnvToken, "ghp_0123456789abcdefghij")

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		}))
	defer server.Close()

	client := NewClient(Config{APIBase: server.URL}, nil)
	_, err := client.Create(context.Background(), "o/n", "automation/x", "main", "t", "b")
	if err == nil || !strings.Contains(err.Error(), "write access") {
		t.Fatalf("Create() = %v, want a message about token permissions", err)
	}
}

func TestNoTokenIsAClearMessageRatherThanAnHTTPError(t *testing.T) {
	t.Setenv(EnvToken, "")
	client := NewClient(Config{APIBase: "http://127.0.0.1:1"}, nil)
	_, err := client.Create(context.Background(), "o/n", "automation/x", "main", "t", "b")
	if err == nil || !strings.Contains(err.Error(), EnvToken) {
		t.Fatalf("Create() = %v", err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
