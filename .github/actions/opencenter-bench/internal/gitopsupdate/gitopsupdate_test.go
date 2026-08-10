package gitopsupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- configuration ------------------------------------------------------------

func TestSlugNormalisesEverySpelling(t *testing.T) {
	const want = "Sherlock2019/opencenter-my-cluster-gitops"
	for _, input := range []string{
		want,
		want + ".git",
		"https://github.com/" + want,
		"https://github.com/" + want + ".git",
		"git@github.com:" + want + ".git",
		"ssh://git@github.com/" + want + ".git",
	} {
		if got := Slug(input); got != want {
			t.Errorf("Slug(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSlugIsEmptyForALocalRepository(t *testing.T) {
	// Not an error. A bare repository on disk has no API and no pull requests,
	// and the stage has to be able to say so rather than fail.
	for _, input := range []string{
		"file:///tmp/gitops-remote.git", "/srv/gitops.git", "./local.git", "",
	} {
		if got := Slug(input); got != "" {
			t.Errorf("Slug(%q) = %q, want empty", input, got)
		}
	}
}

func TestCloneURLNeverEmbedsACredential(t *testing.T) {
	config := Config{Repository: "owner/name"}
	if got := config.CloneURL(); got != "https://github.com/owner/name.git" {
		t.Fatalf("CloneURL() = %q", got)
	}
	// A local path is passed through untouched, which is how the integration
	// test pushes to a bare repository.
	config.Repository = "file:///tmp/x.git"
	if got := config.CloneURL(); got != "file:///tmp/x.git" {
		t.Fatalf("CloneURL() = %q", got)
	}
}

func TestStripCredentialsRemovesUserinfo(t *testing.T) {
	got := StripCredentials("https://user:ghp_abcdefghijklmnop@github.com/owner/name.git")
	if strings.Contains(got, "ghp_") || strings.Contains(got, "user:") {
		t.Fatalf("credential survived: %q", got)
	}
	if got != "https://github.com/owner/name.git" {
		t.Fatalf("StripCredentials() = %q", got)
	}
}

func TestResolveFollowsPrecedence(t *testing.T) {
	flag := func(string) string { return "from-flag" }
	env := func(string) string { return "from-env" }
	saved := func(string) string { return "from-saved" }

	for _, tc := range []struct {
		name             string
		flag, env, saved func(string) string
		want             string
		wantSource       Source
	}{
		{"flag wins", flag, env, saved, "from-flag", FromFlag},
		{"env beats saved", nil, env, saved, "from-env", FromEnv},
		{"saved beats default", nil, nil, saved, "from-saved", FromSaved},
		{"nothing set", nil, nil, nil, "", FromDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, source := Resolve("KEY", tc.flag, tc.env, tc.saved)
			if value != tc.want || source != tc.wantSource {
				t.Fatalf("Resolve() = %q/%s, want %q/%s", value, source, tc.want, tc.wantSource)
			}
		})
	}
}

func TestLoadUsesTheDefaultRepositoryAndCallersCanOverrideIt(t *testing.T) {
	t.Setenv(EnvRepository, "")
	if got := Load(nil, nil).Repository; got != DefaultRepository {
		t.Fatalf("default repository = %q, want %q", got, DefaultRepository)
	}
	// The default must be a default and never a constant: an Actions input or a
	// flag has to be able to point the stage somewhere else.
	t.Setenv(EnvRepository, "other/repo")
	if got := Load(nil, nil).Repository; got != "other/repo" {
		t.Fatalf("environment override ignored: %q", got)
	}
	flag := func(key string) string {
		if key == EnvRepository {
			return "flag/repo"
		}
		return ""
	}
	if got := Load(flag, nil).Repository; got != "flag/repo" {
		t.Fatalf("flag override ignored: %q", got)
	}
}

func TestValidateRejectsPathTraversalAndUnapprovedPaths(t *testing.T) {
	base := Config{
		Repository: "owner/name", BaseBranch: "main",
		ApprovedPaths: DefaultApprovedPaths,
	}
	for _, tc := range []struct{ name, path, wantIn string }{
		{"parent", "../../etc/passwd", "leaves the repository"},
		{"embedded parent", "clusters/../../etc/passwd", "leaves the repository"},
		{"absolute", "/etc/passwd", "absolute"},
		{"unapproved", "secrets/token.yaml", "outside the approved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := base
			config.ManifestPath = tc.path
			err := config.Validate()
			if err == nil {
				t.Fatalf("Validate() accepted %q", tc.path)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("Validate() = %v, want it to mention %q", err, tc.wantIn)
			}
		})
	}
}

func TestValidateAcceptsAnApprovedManifest(t *testing.T) {
	config := Config{
		Repository: "owner/name", BaseBranch: "main",
		ManifestPath:  DefaultManifestPath,
		EvidencePath:  DefaultEvidencePath,
		ApprovedPaths: DefaultApprovedPaths,
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestNotConfiguredIsNotAFailure(t *testing.T) {
	if (Config{}).Configured() {
		t.Fatal("an empty config reported itself as configured")
	}
}

// --- eligibility ---------------------------------------------------------------

// passing is a run that has earned promotion. Each test breaks exactly one
// thing, so a failure names the rule that changed.
func passing() RunSummary {
	return RunSummary{
		RunID: "20260805-140500-a1b2c3", Completed: true, Passed: 89,
		CleanupState: CleanupPassed, ReportPath: "", SourceCommit: "e9b465b1122334455",
	}
}

// withReport gives the run a report file that really exists, because the gate
// stats it rather than taking its word.
func withReport(t *testing.T, run RunSummary) RunSummary {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run.ReportPath = path
	return run
}

func TestEligibleWhenEverythingPassed(t *testing.T) {
	got := Eligible(withReport(t, passing()), Config{})
	if !got.Eligible {
		t.Fatalf("a clean run was refused: %v", got.Reasons)
	}
	if got.Warned {
		t.Fatal("a run with no warnings was reported as warned")
	}
}

func TestEligibilityRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*RunSummary)
		config Config
		wantIn string
	}{
		{"failed tests", func(r *RunSummary) { r.Failed = 2 }, Config{}, "2 test(s) failed"},
		{"blocked tests", func(r *RunSummary) { r.Blocked = 1 }, Config{}, "blocked"},
		{"cleanup failed", func(r *RunSummary) { r.CleanupState = CleanupFailed }, Config{},
			"cleanup did not complete"},
		{"cleanup skipped", func(r *RunSummary) { r.CleanupState = CleanupSkipped }, Config{},
			"cleanup was not verified"},
		{"cancelled", func(r *RunSummary) { r.Cancelled = true }, Config{}, "cancelled"},
		{"incomplete", func(r *RunSummary) { r.Completed = false }, Config{}, "did not complete"},
		{"secret leak", func(r *RunSummary) { r.SecretLeak = true }, Config{}, "secret-leak"},
		{"nothing ran", func(r *RunSummary) { r.Passed = 0 }, Config{}, "no checks were executed"},
		{"warnings, not allowed", func(r *RunSummary) { r.Warnings = 3 }, Config{},
			"3 warning(s)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := withReport(t, passing())
			tc.change(&run)
			got := Eligible(run, tc.config)
			if got.Eligible {
				t.Fatal("the run was allowed through")
			}
			if !containsSubstring(got.Reasons, tc.wantIn) {
				t.Fatalf("reasons %v do not mention %q", got.Reasons, tc.wantIn)
			}
		})
	}
}

func TestWarningsPromoteOnlyWhenExplicitlyAllowed(t *testing.T) {
	run := withReport(t, passing())
	run.Warnings = 3

	if Eligible(run, Config{}).Eligible {
		t.Fatal("a warning-only run was promoted by default")
	}
	got := Eligible(run, Config{AllowWarnings: true})
	if !got.Eligible {
		t.Fatalf("allow_warnings did not permit the run: %v", got.Reasons)
	}
	if !got.Warned {
		// The promotion has to stay visible as one made under an allowance.
		t.Fatal("a warning-only promotion was not marked as warned")
	}
}

func TestMissingReportBlocks(t *testing.T) {
	run := passing()
	run.ReportPath = ""
	if got := Eligible(run, Config{}); got.Eligible {
		t.Fatal("a run with no report was promoted")
	}
	run.ReportPath = filepath.Join(t.TempDir(), "gone.json")
	if got := Eligible(run, Config{}); got.Eligible {
		t.Fatal("a run whose report does not exist was promoted")
	}
}

func TestEligibilityCollectsEveryReason(t *testing.T) {
	// Whack-a-mole is a bad way to fix a blocked promotion.
	run := passing()
	run.Failed = 1
	run.CleanupState = CleanupFailed
	got := Eligible(run, Config{})
	if len(got.Reasons) < 3 {
		t.Fatalf("only %d reason(s) reported: %v", len(got.Reasons), got.Reasons)
	}
}

// --- approval gates ------------------------------------------------------------

func TestBothGatesAreRequired(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approval Approval
		want     bool
		wantIn   string
	}{
		{"neither", Approval{}, false, Gate},
		{"gate only", Approval{GateSet: true}, false, "not approved"},
		{"approval only", Approval{Approved: true}, false, Gate},
		{"both", Approval{GateSet: true, Approved: true}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, why := tc.approval.Permits()
			if got != tc.want {
				t.Fatalf("Permits() = %v (%s), want %v", got, why, tc.want)
			}
			if tc.wantIn != "" && !strings.Contains(why, tc.wantIn) {
				t.Fatalf("Permits() said %q, want it to mention %q", why, tc.wantIn)
			}
		})
	}
}

// --- branch names and image tags ------------------------------------------------

func TestBranchNameIsDeterministicAndSafe(t *testing.T) {
	got := BranchName("20260805-140500-a1b2c3")
	if got != "automation/opencenter-testbench-20260805-140500-a1b2c3" {
		t.Fatalf("BranchName() = %q", got)
	}
	if BranchName("x") != BranchName("x") {
		t.Fatal("BranchName is not deterministic")
	}
}

func TestSanitiseSegmentRemovesEverythingGitRefuses(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"20260805-140500", "20260805-140500"},
		{"feature/thing", "feature/thing"},
		{"a b c", "a-b-c"},
		{"../../escape", "escape"},
		{"has..dots", "has-dots"},
		{"trailing.lock", "trailing-lock"},
		{"~^:?*[", "run"},
		{"", "run"},
		{"--leading--", "leading"},
	} {
		if got := SanitiseSegment(tc.in); got != tc.want {
			t.Errorf("SanitiseSegment(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := SanitiseSegment(strings.Repeat("a", 400))
	if len(long) > 100 {
		t.Fatalf("SanitiseSegment did not bound the length: %d", len(long))
	}
}

// gitFailure decides what a person paged by a failed promotion actually reads,
// so the cases here are real git output rather than invented strings. Every one
// of them used to report "Cloning into 'gitops'..." — git's progress line, which
// says nothing about why anything failed.
func TestGitFailureReportsTheCauseAndNotTheProgressLine(t *testing.T) {
	for _, tc := range []struct{ name, out, want string }{
		{
			name: "dns failure, which none of the recognised cases cover",
			out: "Cloning into 'gitops'...\n" +
				"fatal: unable to access 'https://github.com/o/n.git/': " +
				"Could not resolve host: github.com",
			want: "fatal: unable to access 'https://github.com/o/n.git/': " +
				"Could not resolve host: github.com",
		},
		{
			name: "the last fatal wins, because git prints the cause last",
			out:  "Cloning into 'gitops'...\nfatal: early EOF\nfatal: index-pack failed",
			want: "fatal: index-pack failed",
		},
		{
			name: "a push, whose first line is the remote and not an error",
			out:  "To github.com:owner/name.git\n ! [rejected] main -> main (fetch first)\nerror: failed to push some refs",
			want: "error: failed to push some refs",
		},
		{
			name: "a rejected key",
			out:  "Cloning into 'gitops'...\nssh: connect to host github.com port 22: Connection timed out",
			want: "ssh: connect to host github.com port 22: Connection timed out",
		},
		{
			name: "no labelled error at all: the last line with content",
			out:  "Cloning into 'gitops'...\nsomething unusual happened\n\n",
			want: "something unusual happened",
		},
		{
			name: "progress frames must not become the message",
			out:  "Cloning into 'gitops'...\nReceiving objects:  50%\rReceiving objects: 100%",
			want: "Receiving objects: 100%",
		},
		{name: "nothing", out: "", want: "no output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitFailure(tc.out); got != tc.want {
				t.Errorf("gitFailure() = %q\n            want %q", got, tc.want)
			}
			if got := gitFailure(tc.out); strings.HasPrefix(got, "Cloning into") {
				t.Error("reported git's progress line as the failure")
			}
		})
	}
}

func TestImageTagIsImmutable(t *testing.T) {
	if got := ImageTag("e9b465b1122334455"); got != "sha-e9b465b" {
		t.Fatalf("ImageTag() = %q, want sha-e9b465b", got)
	}
	if got := ImageTag(""); got != "" {
		t.Fatalf("ImageTag(\"\") = %q, want empty", got)
	}
}

func TestPromotingLatestIsRefused(t *testing.T) {
	root := t.TempDir()
	writeKustomization(t, root, "clusters/my-cluster/kustomization.yaml", "sha-000000")
	_, err := UpdateImage(root, "clusters/my-cluster/kustomization.yaml",
		DefaultImageRepository, "latest", DefaultContainerName)
	if err == nil || !strings.Contains(err.Error(), "latest") {
		t.Fatalf("UpdateImage accepted the mutable tag: %v", err)
	}
}

// --- manifests ------------------------------------------------------------------

func writeKustomization(t *testing.T, root, relative, tag string) {
	t.Helper()
	body := "# a comment that must survive\n" +
		"apiVersion: kustomize.config.k8s.io/v1beta1\n" +
		"kind: Kustomization\n" +
		"resources:\n  - deployment.yaml\n" +
		"images:\n" +
		"  - name: " + DefaultImageRepository + "\n" +
		"    newTag: " + tag + "\n"
	if err := WriteInto(root, relative, []byte(body)); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateImageRewritesAKustomizationAndKeepsComments(t *testing.T) {
	root := t.TempDir()
	const path = "clusters/my-cluster/kustomization.yaml"
	writeKustomization(t, root, path, "sha-000000")

	change, err := UpdateImage(root, path, DefaultImageRepository, "sha-e9b465b", DefaultContainerName)
	if err != nil {
		t.Fatal(err)
	}
	if !change.Changed || change.Kind != "kustomization" || change.Previous != "sha-000000" {
		t.Fatalf("change = %+v", change)
	}

	raw, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "newTag: sha-e9b465b") {
		t.Fatalf("tag not updated:\n%s", body)
	}
	if !strings.Contains(body, "a comment that must survive") {
		// The file belongs to somebody else. Losing their comments to a robot
		// is a good way to have the robot switched off.
		t.Fatalf("comments were lost:\n%s", body)
	}
	if !strings.Contains(body, "resources:") {
		t.Fatalf("unrelated keys were lost:\n%s", body)
	}
}

func TestUpdateImageIsIdempotent(t *testing.T) {
	root := t.TempDir()
	const path = "clusters/my-cluster/kustomization.yaml"
	writeKustomization(t, root, path, "sha-e9b465b")

	change, err := UpdateImage(root, path, DefaultImageRepository, "sha-e9b465b", DefaultContainerName)
	if err != nil {
		t.Fatal(err)
	}
	if change.Changed {
		t.Fatal("re-promoting the same tag reported a change")
	}
}

func TestUpdateImageRewritesADeploymentContainer(t *testing.T) {
	root := t.TempDir()
	const path = "apps/opencenter-cli-test-bench/deployment.yaml"
	body := "apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n" +
		"      containers:\n" +
		"        - name: sidecar\n          image: busybox:1.0\n" +
		"        - name: test-bench\n          image: " + DefaultImageRepository + ":sha-000000\n"
	if err := WriteInto(root, path, []byte(body)); err != nil {
		t.Fatal(err)
	}

	change, err := UpdateImage(root, path, DefaultImageRepository, "sha-e9b465b", "test-bench")
	if err != nil {
		t.Fatal(err)
	}
	if !change.Changed || change.Kind != "deployment" {
		t.Fatalf("change = %+v", change)
	}

	raw, _ := os.ReadFile(filepath.Join(root, path))
	if !strings.Contains(string(raw), DefaultImageRepository+":sha-e9b465b") {
		t.Fatalf("image not updated:\n%s", raw)
	}
	// Exactly one image moved. A regex would have been just as happy to rewrite
	// the sidecar, and the manifest would still have parsed.
	if !strings.Contains(string(raw), "busybox:1.0") {
		t.Fatalf("the sidecar was modified:\n%s", raw)
	}
}

func TestUpdateImageRefusesAMissingContainer(t *testing.T) {
	root := t.TempDir()
	const path = "apps/opencenter-cli-test-bench/deployment.yaml"
	body := "apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n" +
		"      containers:\n        - name: other\n          image: busybox:1.0\n"
	if err := WriteInto(root, path, []byte(body)); err != nil {
		t.Fatal(err)
	}
	_, err := UpdateImage(root, path, DefaultImageRepository, "sha-e9b465b", "test-bench")
	if err == nil || !strings.Contains(err.Error(), "no container named") {
		t.Fatalf("UpdateImage() = %v", err)
	}
}

func TestUpdateImageRefusesTwoEntriesForTheSameImage(t *testing.T) {
	root := t.TempDir()
	const path = "clusters/my-cluster/kustomization.yaml"
	body := "images:\n" +
		"  - name: " + DefaultImageRepository + "\n    newTag: a\n" +
		"  - name: " + DefaultImageRepository + "\n    newTag: b\n"
	if err := WriteInto(root, path, []byte(body)); err != nil {
		t.Fatal(err)
	}
	_, err := UpdateImage(root, path, DefaultImageRepository, "sha-e9b465b", DefaultContainerName)
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("UpdateImage() = %v", err)
	}
}

func TestUpdateImageRefusesAMissingManifest(t *testing.T) {
	_, err := UpdateImage(t.TempDir(), "clusters/my-cluster/kustomization.yaml",
		DefaultImageRepository, "sha-e9b465b", DefaultContainerName)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("UpdateImage() = %v", err)
	}
}

func TestWriteIntoRefusesToLeaveTheCheckout(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"../escape.json", "/etc/passwd", "a/../../escape.json"} {
		if err := WriteInto(root, path, []byte("x")); err == nil {
			t.Errorf("WriteInto accepted %q", path)
		}
	}
}

// --- evidence -------------------------------------------------------------------

func TestEvidenceSerialisesToTheDocumentedShape(t *testing.T) {
	run := passing()
	run.SourceRepository = "opencenter-cloud/openCenter-cli"
	run.CLIVersion = "0.0.1"
	run.BenchVersion = "0.1.0"
	run.Environment = "openstack-emulated"
	run.ReportPath = "/bench/artifacts/runs/x/reports/report.json"

	evidence := NewEvidence(run, "/bench", false)
	raw, err := evidence.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"schemaVersion", "runId", "sourceRepository", "sourceCommit", "cliVersion",
		"benchVersion", "environment", "status", "passed", "warnings", "failed",
		"blocked", "cleanupStatus", "reportPath", "completedAt",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("evidence is missing %q", key)
		}
	}
	if decoded["schemaVersion"] != EvidenceSchema {
		t.Errorf("schemaVersion = %v", decoded["schemaVersion"])
	}
	// The path is relative. An absolute one publishes the layout of whoever's
	// machine ran the tests into somebody else's repository.
	if got := decoded["reportPath"]; got != "artifacts/runs/x/reports/report.json" {
		t.Errorf("reportPath = %v, want a path relative to the bench root", got)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Error("evidence has no trailing newline")
	}
}

func TestEvidenceRecordsAWarnedPromotion(t *testing.T) {
	if got := NewEvidence(passing(), "", true).Status; got != "passed_with_warnings" {
		t.Fatalf("status = %q", got)
	}
}

func TestHistoryPathSitsBesideTheLatestOne(t *testing.T) {
	got := HistoryPath(DefaultEvidencePath, "20260805-140500-a1b2c3")
	want := "test-evidence/opencenter-cli/runs/20260805-140500-a1b2c3.json"
	if got != want {
		t.Fatalf("HistoryPath() = %q, want %q", got, want)
	}
}

// --- commit message and pull request body ---------------------------------------

func TestCommitMessageCarriesTraceabilityAndNothingElse(t *testing.T) {
	run := passing()
	run.CLIVersion = "0.0.1"
	run.Environment = "kind"
	evidence := NewEvidence(run, "/bench/root", false)
	evidence.ReportPath = "artifacts/runs/x/reports/report.json"

	message := CommitMessage(evidence, "e9b465b")
	if !strings.HasPrefix(message, "chore(gitops): promote openCenter test bench e9b465b") {
		t.Fatalf("subject = %q", strings.SplitN(message, "\n", 2)[0])
	}
	for _, want := range []string{"Test Bench run:", "Source commit:", "Cleanup: passed"} {
		if !strings.Contains(message, want) {
			t.Errorf("commit message is missing %q", want)
		}
	}
	// What must never be in it.
	for _, forbidden := range []string{"/bench/root", "token", "ghp_", "OPENCLI_GIT_TOKEN"} {
		if strings.Contains(message, forbidden) {
			t.Errorf("commit message leaked %q:\n%s", forbidden, message)
		}
	}
}

func TestPullRequestBodySaysNothingWasDeployed(t *testing.T) {
	body := PullRequestBody(NewEvidence(passing(), "", false),
		ManifestChange{Changed: true, Image: "ghcr.io/x:sha-abc", Previous: "ghcr.io/x:sha-000"},
		[]string{"clusters/my-cluster/kustomization.yaml"})

	// A reviewer arriving at an automated pull request needs to know within one
	// screen whether a robot has already changed production.
	if !strings.Contains(body, "No deployment was performed by the Test Bench") {
		t.Fatalf("the body does not say that nothing was deployed:\n%s", body)
	}
	if !strings.Contains(body, "sha-abc") {
		t.Fatalf("the body does not name the promoted image:\n%s", body)
	}
}

// --- diff judgement -------------------------------------------------------------

func TestJudgeRejectsAnEmptyDiff(t *testing.T) {
	problems := judge(nil, Config{ApprovedPaths: DefaultApprovedPaths}, nil, t.TempDir())
	if !containsSubstring(problems, "nothing changed") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestJudgeRejectsUnapprovedAndSecretFiles(t *testing.T) {
	config := Config{ApprovedPaths: DefaultApprovedPaths}
	for _, tc := range []struct{ name, path, wantIn string }{
		{"unrelated", "README.md", "outside the approved"},
		{"dotenv", "clusters/.env", "credential file"},
		{"ssh key", "clusters/id_ed25519", "credential file"},
		{"pem", "clusters/tls.pem", "key material"},
		{"saved credentials", "clusters/credentials.local.yaml", "credential file"},
		{"editor backup", "clusters/kustomization.yaml.bak", "key material or an editor artefact"},
		{"git internals", ".git/config", "outside the approved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := judge([]string{tc.path}, config, nil, t.TempDir())
			if !containsSubstring(problems, tc.wantIn) {
				t.Fatalf("judge(%q) = %v, want it to mention %q", tc.path, problems, tc.wantIn)
			}
		})
	}
}

func TestJudgeRequiresEveryExpectedFileToHaveChanged(t *testing.T) {
	// A manifest update that silently no-opped would otherwise produce a pull
	// request that promotes nothing while looking exactly like one that does.
	problems := judge(
		[]string{"test-evidence/opencenter-cli/latest.json"},
		Config{ApprovedPaths: DefaultApprovedPaths},
		[]string{"clusters/my-cluster/kustomization.yaml"},
		t.TempDir())
	if !containsSubstring(problems, "was expected to change and did not") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestJudgeAcceptsTheIntendedChange(t *testing.T) {
	files := []string{
		"clusters/my-cluster/kustomization.yaml",
		"test-evidence/opencenter-cli/latest.json",
	}
	problems := judge(files, Config{ApprovedPaths: DefaultApprovedPaths}, files, t.TempDir())
	if len(problems) != 0 {
		t.Fatalf("a correct change was rejected: %v", problems)
	}
}

func TestJudgeRejectsABinaryFile(t *testing.T) {
	root := t.TempDir()
	if err := WriteInto(root, "clusters/blob.yaml", []byte{'a', 0, 'b'}); err != nil {
		t.Fatal(err)
	}
	problems := judge([]string{"clusters/blob.yaml"},
		Config{ApprovedPaths: DefaultApprovedPaths}, nil, root)
	if !containsSubstring(problems, "binary file") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestScanSecretsFindsCanariesAndKeyBlocks(t *testing.T) {
	canary := "canary-8f3a91be22"
	if got := ScanSecrets("+ token: "+canary+"\n", nil, []string{canary}); len(got) == 0 {
		t.Fatal("a planted canary passed the scan")
	}
	patch := "+key: -----BEGIN OPENSSH PRIVATE KEY-----\n"
	if got := ScanSecrets(patch, nil, nil); len(got) == 0 {
		t.Fatal("a private key block passed the scan")
	}
	if got := ScanSecrets("+  newTag: sha-e9b465b\n", nil, []string{canary}); len(got) != 0 {
		t.Fatalf("a clean patch was rejected: %v", got)
	}
}

// --- results and exit codes ------------------------------------------------------

func TestResultStartsWithEveryStepPending(t *testing.T) {
	result := newResult(ModePreview)
	if len(result.Steps) != len(Steps) {
		t.Fatalf("%d steps, want %d", len(result.Steps), len(Steps))
	}
	// A result that only lists what happened cannot show what did not.
	for _, step := range result.Steps {
		if step.Status != StepPending {
			t.Fatalf("step %s started as %s", step.ID, step.Status)
		}
	}
}

func TestExitCodesMatchTheDocumentedTable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *Result
		want   int
	}{
		{"preview", &Result{Status: StatusPreview}, ExitOK},
		{"pr created", &Result{Status: StatusPRCreated}, ExitOK},
		{"not configured", &Result{Status: StatusNotConfigured}, ExitOK},
		{"not eligible", &Result{Status: StatusBlocked, Reasons: []string{"2 test(s) failed"}},
			ExitNotEligible},
		{"approval missing", &Result{Status: StatusBlocked,
			Reasons: []string{"not approved — nobody asked for this update"}}, ExitApprovalMissing},
		{"gate missing", &Result{Status: StatusBlocked,
			Reasons: []string{Gate + " is not set"}}, ExitApprovalMissing},
		{"git failed", &Result{Status: StatusFailed,
			Steps: []Step{{ID: StepPush, Status: StepFailed}}}, ExitGitFailed},
		{"pr failed", &Result{Status: StatusFailed,
			Steps: []Step{{ID: StepPullRequest, Status: StepFailed}}}, ExitPRFailed},
		{"bad config", &Result{Status: StatusBlocked,
			Steps: []Step{{ID: StepPreflight, Status: StepFailed}}}, ExitBadConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.result.ExitCode(); got != tc.want {
				t.Fatalf("ExitCode() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSkipRestLeavesNothingLookingPending(t *testing.T) {
	result := newResult(ModeApproved)
	result.begin(StepPreflight)(StepOK, "")
	result.skipRest(StepBlocked, "stopped")
	for _, step := range result.Steps {
		if step.Status == StepPending {
			t.Fatalf("step %s is still pending", step.ID)
		}
	}
}

func TestHeadlinesAreDistinct(t *testing.T) {
	// PASS, WARN, FAIL, BLOCKED, NOT CONFIGURED and PREVIEW have to stay
	// tellable apart: they mean different things to whoever is reading.
	seen := map[string]bool{}
	for _, status := range []Status{
		StatusPassed, StatusWarning, StatusFailed, StatusBlocked,
		StatusNotConfigured, StatusPreview, StatusPRCreated,
	} {
		headline := (&Result{Status: status}).Headline()
		if seen[headline] && status != StatusPassed {
			t.Fatalf("%s shares a headline with an earlier status: %q", status, headline)
		}
		seen[headline] = true
	}
}

func containsSubstring(haystack []string, needle string) bool {
	for _, item := range haystack {
		if strings.Contains(item, needle) {
			return true
		}
	}
	return false
}

func TestEvidenceWriteLandsInTheReportsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reports")
	evidence := NewEvidence(passing(), "", false)
	evidence.CompletedAt = time.Date(2026, 8, 5, 7, 5, 0, 0, time.UTC)

	path, err := evidence.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "gitops-evidence.json" {
		t.Fatalf("wrote %q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"completedAt": "2026-08-05T07:05:00Z"`) {
		t.Fatalf("timestamp not as written:\n%s", raw)
	}
}
