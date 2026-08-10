package actionsetup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The workflow must call a binary that the build actually produces.
//
// It did not. The committed workflow was copied from a prototype where the
// command was called opencenter-test-bench; folding it in here renamed the
// command to opencenter-e2e and nobody updated the file. Every "Run the
// lifecycle" step in CI then invoked a path that did not exist, and the
// evidence upload failed after it because the run never got far enough to
// create a directory. Five CI runs, four of them across a matrix, all failing
// on a name.
//
// Nothing caught it because nothing compared the two. This does.

var binaryCall = regexp.MustCompile(`\./(?:\.bench/)?bin/([a-z0-9-]+)`)

// buildsIn reads the binary names `mise run build` produces.
func buildsIn(t *testing.T, root string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".mise.toml"))
	if err != nil {
		t.Skipf("no .mise.toml at %s: %v", root, err)
	}
	built := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		_, after, found := strings.Cut(line, "go build -o ")
		if !found {
			continue
		}
		path := strings.Fields(after)[0]
		built[strings.TrimPrefix(path, "bin/")] = true
	}
	if len(built) == 0 {
		t.Fatal(".mise.toml builds nothing, so no workflow can call anything")
	}
	return built
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// internal/actionsetup -> ../..
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// The rendered workflow calls the published action, and nothing else.
//
// It used to check out the bench and run a binary out of it, which is where the
// name drift this file exists for came from. The action does that now — staged
// from github.action_path at the ref the caller pinned — so the generated file
// should name no binary at all, and naming one would mean the checkout-and-
// build shape had crept back.
func TestTheRenderedWorkflowCallsTheAction(t *testing.T) {
	rendered := string(Workflow(Options{Kind: KindE2E, E2E: E2EOptions{
		Nightly: "kind", RealEnvironment: "e2e-real-provider",
	}}))

	if !strings.Contains(rendered, "uses: "+DefaultE2EAction) {
		t.Errorf("the workflow does not call %s", DefaultE2EAction)
	}
	if !strings.Contains(rendered, "mode: lifecycle") {
		t.Error("the workflow does not ask the action for the lifecycle")
	}
	if found := binaryCall.FindAllStringSubmatch(rendered, -1); len(found) > 0 {
		t.Errorf("the workflow runs %s directly; the action builds and runs it, "+
			"and a path here is the drift that broke every run once already",
			found[0][0])
	}
	if strings.Contains(rendered, "mise run build") {
		t.Error("the workflow builds the bench itself; the action does that")
	}
}

// A pinned ref reaches the file, so a caller can stop a bench change moving
// their verdicts.
func TestThePinnedRefIsUsed(t *testing.T) {
	rendered := string(Workflow(Options{Kind: KindE2E,
		E2E: E2EOptions{BenchRepo: "Sherlock2019/fullopenclitestbench@v1"}}))
	if !strings.Contains(rendered, "uses: Sherlock2019/fullopenclitestbench@v1") {
		t.Error("the pinned ref did not reach the generated workflow")
	}

	// And a repository with no ref still gets one, because `uses:` without a ref
	// is not valid.
	rendered = string(Workflow(Options{Kind: KindE2E,
		E2E: E2EOptions{BenchRepo: "someone/their-fork"}}))
	if !strings.Contains(rendered, "uses: someone/their-fork@main") {
		t.Error("a bench repository with no ref did not get a default one")
	}
}

// The action itself is what runs the binary, so the name check moves there.
//
// Only the bench's own binaries. action.yml also runs ./bin/opencenter, which
// is the CLI it just built in a different tree — checking that against the
// bench's .mise.toml would be comparing two unrelated builds.
func TestTheActionRunsABenchBinaryThatIsBuilt(t *testing.T) {
	root := repoRoot(t)
	built := buildsIn(t, root)

	raw, err := os.ReadFile(filepath.Join(root, "action.yml"))
	if err != nil {
		t.Skipf("no action.yml: %v", err)
	}

	// The lifecycle binary is the one this package's workflow depends on, so it
	// is the one that must exist. Named explicitly rather than swept up by a
	// regex, because the sweep is what produced a false failure on the CLI's.
	if !strings.Contains(string(raw), "bin/opencenter-e2e") {
		t.Fatal("action.yml never runs the lifecycle binary, so mode: lifecycle " +
			"does nothing")
	}
	if !built["opencenter-e2e"] {
		t.Errorf("action.yml runs bin/opencenter-e2e, which the build does not "+
			"produce (it builds: %s)", names(built))
	}
	// And it has to build it, or it runs a path that is not there — which is
	// exactly the failure this file was written for.
	if !strings.Contains(string(raw), "go build -o bin/opencenter-e2e") {
		t.Error("action.yml runs the lifecycle binary without building it")
	}
}

// And the copy committed in this repository, which is what CI actually runs.
// The renderer being right did not stop the committed file being wrong.
func TestTheCommittedWorkflowCallsABinaryThatIsBuilt(t *testing.T) {
	root := repoRoot(t)
	built := buildsIn(t, root)

	path := filepath.Join(root, ".github", "workflows", "opencenter-e2e.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no committed workflow: %v", err)
	}

	seen := map[string]bool{}
	for _, match := range binaryCall.FindAllStringSubmatch(string(raw), -1) {
		seen[match[1]] = true
	}
	if len(seen) == 0 {
		t.Fatal("the committed workflow calls no binary at all")
	}
	for name := range seen {
		if !built[name] {
			t.Errorf(".github/workflows/opencenter-e2e.yml calls ./bin/%s, which "+
				"`mise run build` does not produce (it builds: %s)", name, names(built))
		}
	}
}

// A workflow that never runs tests nothing. It declared workflow_dispatch,
// pull_request and schedule and no push, so four pushes produced four runs of
// the other workflow and none of this one.
func TestTheWorkflowRunsOnEveryCommit(t *testing.T) {
	rendered := string(Workflow(Options{Kind: KindE2E}))
	if !strings.Contains(rendered, "\n  push:\n") {
		t.Error("the workflow has no push trigger, so it tests nothing until " +
			"somebody opens a pull request")
	}
	// And the job has to accept a push, or the trigger starts a workflow that
	// skips every job in it.
	if !strings.Contains(rendered, "github.event_name == 'push'") {
		t.Error("no job accepts a push, so the push trigger would run nothing")
	}
}

func names(set map[string]bool) string {
	var out []string
	for name := range set {
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}
