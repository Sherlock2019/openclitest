package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The sixty features the brief asks for, checked against what is actually in
// the tree rather than against anybody's memory of it.
//
// Every check names a concrete artefact — a symbol, a flag, a config key, a
// file — so a failure says which requirement regressed and where to look. A
// requirement that cannot be checked mechanically says so and is listed as
// unverified rather than quietly counted as present, because a checklist that
// marks its own gaps as passing is worse than no checklist.

// requirement is one row of the brief.
type requirement struct {
	number int
	name   string
	// where names the file that should carry it, relative to the repo root.
	where string
	// needs are strings that must all be present in that file.
	needs []string
}

func repoRootFor(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func read(t *testing.T, root, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return ""
	}
	return string(raw)
}

// The sixty, in the brief's order.
var requirements = []requirement{
	{1, "Dual execution mode", "cmd/opencenter-e2e/main.go",
		[]string{"execution-channel", "ChannelGitHub"}},
	{2, "Clean test environment", "internal/e2e/phases.go",
		[]string{"phaseWorkspace"}},
	{3, "Build openCenter with mise", "internal/e2e/phases.go",
		[]string{"phaseBuild", "MisePath", `"run", "build"`}},
	{4, "Binary verification", "internal/e2e/phases.go",
		[]string{"phaseVerifyBinary", "CLIChecksum"}},
	{5, "Cluster configuration generator", "internal/e2e/phases.go",
		[]string{"phaseConfigure", `"cluster", "init"`}},
	{6, "Configuration validation", "internal/e2e/phases.go",
		[]string{"phaseValidateConfig", `"cluster", "validate"`}},
	{7, "Provider preflight", "internal/e2e/phases.go",
		[]string{"phaseDoctor", `"cluster", "doctor"`}},
	{8, "Dry-run validation", "internal/e2e/phases.go",
		[]string{"phaseRenderPreview", "--render-only"}},
	{9, "Infrastructure generation testing", "internal/e2e/phases.go",
		[]string{"tofu", "terraform"}},
	{10, "GitOps generation testing", "internal/e2e/phases.go",
		[]string{"kustomize"}},
	{11, "Cluster deployment", "internal/e2e/cluster_phases.go",
		[]string{"phaseDeploy", `"cluster", "deploy"`}},
	{12, "Provider environment validation", "internal/e2e/cluster_phases.go",
		[]string{"phaseInfrastructure"}},
	{13, "Kubernetes health validation", "internal/e2e/phases.go",
		[]string{"phaseKubernetes", "nodes", "pods"}},
	{14, "openCenter platform health", "internal/e2e/cluster_phases.go",
		[]string{"phasePlatform"}},
	{15, "Functional smoke tests", "internal/e2e/cluster_phases.go",
		[]string{"phaseSmoke"}},
	{16, "Kafka smoke test", "internal/e2e/cluster_phases.go",
		[]string{"kafka"}},
	{17, "Failure injection testing", "internal/e2e/cluster_phases.go",
		[]string{"phaseFailureTests"}},
	{18, "Retry testing", "internal/e2e/cluster_phases.go",
		[]string{"retry"}},
	{19, "Recovery testing", "internal/e2e/cluster_phases.go",
		[]string{"recover"}},
	{20, "Timeout testing", "internal/e2e/run.go",
		[]string{"Timeout"}},
	{21, "Diagnostic collection", "internal/e2e/phases.go",
		[]string{"phaseDiagnostics"}},
	{22, "Failure cause classification", "internal/e2e/run.go",
		[]string{"CategoryProductDefect", "CategoryProviderIssue", "CategoryEnvironment"}},
	{23, "Failed command view", "internal/e2e/run.go",
		[]string{"Command", "json:\"command"}},
	{24, "Expected vs actual result", "internal/e2e/run.go",
		[]string{"Expected", "Actual"}},
	{25, "stdout / stderr capture", "internal/e2e/run.go",
		[]string{"Stdout", "Stderr"}},
	{26, "Reproduction command", "internal/e2e/run.go",
		[]string{"Reproduce"}},
	{27, "Regression detection", "internal/e2e/run.go",
		[]string{"Regression"}},
	{28, "Provider matrix", "internal/e2e/matrix.go",
		[]string{"BuildMatrix", "ProviderOnly"}},
	{29, "Environment emulation", "internal/e2e/profile.go",
		[]string{"InfraEmulated", "Emulated()"}},
	{30, "Kind local runtime", "internal/e2e/profile.go",
		[]string{"InfraKind"}},
	{31, "Real vs emulated labels", "internal/e2e/report.go",
		[]string{"emulated"}},
	{32, "Simulation limitation warning", "internal/e2e/simulate.go",
		[]string{"simulatedBanner"}},
	{33, "Environment profiles", "internal/e2e/profile.go",
		[]string{"configuration-only", "kind", "openstack-real"}},
	{34, "Run individual phases", "cmd/opencenter-e2e/main.go",
		[]string{"only-phase"}},
	{35, "Resume failed run", "cmd/opencenter-e2e/main.go",
		[]string{`case "resume"`}},
	{36, "Run from / to phase", "cmd/opencenter-e2e/main.go",
		[]string{"from-phase", "to-phase"}},
	{37, "Resource registry", "internal/e2e/run.go",
		[]string{"Register(", "Resources"}},
	{38, "Automatic cleanup", "internal/e2e/phases.go",
		[]string{"phaseDestroy"}},
	{39, "Cleanup verification", "internal/e2e/phases.go",
		[]string{"phaseVerifyCleanup"}},
	{40, "Keep environment for troubleshooting", "cmd/opencenter-e2e/main.go",
		[]string{"keep-on-failure"}},
	{41, "Evidence capture", "internal/e2e/phases.go",
		[]string{"evidence/"}},
	{42, "Secret redaction", "internal/e2e/engine.go",
		[]string{"Redactor"}},
	{43, "HTML report", "internal/e2e/report.go", []string{"report.html"}},
	{44, "JSON report", "internal/e2e/report.go", []string{"report.json"}},
	{45, "JUnit XML", "internal/e2e/report.go", []string{"junit"}},
	{46, "Markdown report", "internal/e2e/report.go", []string{"report.md"}},
	{47, "CSV summary", "internal/e2e/report.go", []string{"summary.csv"}},
	{48, "GitHub Actions workflow", "internal/actionsetup/kind.go",
		[]string{"workflow_dispatch", "pull_request"}},
	{49, "GitHub artifact upload", "internal/actionsetup/kind.go",
		[]string{"upload-artifact"}},
	{50, "GitHub secrets support", "internal/actionsetup/kind.go",
		[]string{"secrets."}},
	{51, "Self-hosted runner support", "internal/actionsetup/kind.go",
		[]string{"runs-on: ${{"}},
	{52, "Local CLI runner", ".mise.toml", []string{"opencenter-e2e e2e run"}},
	{53, "mise task integration", ".mise.toml",
		[]string{"[tasks.e2e]", "[tasks.build]"}},
	{54, "Test profiles", ".mise.toml",
		[]string{"e2e-safe", "e2e-kind", "e2e-emulated"}},
	{55, "Lifecycle dashboard", "internal/e2e/stage.go", []string{"var Stages"}},
	// The summary is the Test scope table and the findings table beside it, not
	// a per-run verdict panel. What the requirement asks for is that a developer
	// is told how the build did, by both benches, in one place.
	{56, "Developer summary dashboard", "cmd/testlab/ui.html",
		[]string{"renderTriageBand", "tsum-bench", "Test scope", "renderDefects"}},
	// renderDefects, not labTriage. The triage table used to be the lifecycle
	// panel's own, beside four other lists of the same findings; it is now one
	// table fed by both benches, and the requirement is that failures are
	// triaged by category and named — not that a particular function draws it.
	{57, "Failure triage dashboard", "cmd/testlab/ui.html",
		[]string{"renderDefects", "LAB_NOT_OURS", "tsum-dfx-cat"}},
	{58, "Provider coverage dashboard", "cmd/testlab/e2elifecycle.go",
		[]string{"coverage"}},
	{59, "Release gate result", "internal/e2e/run.go",
		[]string{"VerdictPass", "VerdictWarning", "VerdictFail", "VerdictInconclusive"}},
	{60, "Documented runbook", "docs/testing/e2e-runbook.md", []string{"#"}},
}

// TestEveryRequirementIsPresent is the audit. It names each gap rather than
// failing on the first one, because the useful output is the whole list.
func TestEveryRequirementIsPresent(t *testing.T) {
	root := repoRootFor(t)
	var missing []string

	for _, want := range requirements {
		body := read(t, root, want.where)
		if body == "" {
			missing = append(missing, report(want, "the file does not exist"))
			continue
		}
		var absent []string
		for _, needle := range want.needs {
			if !strings.Contains(body, needle) {
				absent = append(absent, needle)
			}
		}
		if len(absent) > 0 {
			missing = append(missing, report(want, "no "+strings.Join(absent, ", ")))
		}
	}

	if len(missing) > 0 {
		t.Errorf("%d of %d requirements are not met:\n  %s",
			len(missing), len(requirements), strings.Join(missing, "\n  "))
	}
}

func report(want requirement, why string) string {
	return strings.Join([]string{
		pad(want.number), want.name, "—", why, "(" + want.where + ")",
	}, " ")
}

func pad(n int) string {
	if n < 10 {
		return " " + itoa(n) + "."
	}
	return itoa(n) + "."
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}
