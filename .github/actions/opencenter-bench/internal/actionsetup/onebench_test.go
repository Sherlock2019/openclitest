package actionsetup

import (
	"strings"
	"testing"
)

// One bench. Both workflows call the same action in the same repository, and
// differ only by mode.
//
// This is the thing that made it read as two benches, because it was two: the
// command workflow called Sherlock2019/opencenterclitest-Simple@main while the
// lifecycle workflow called this repository's action. Every fix landed on one
// half — the Node 24 bumps, the cache key a matrix of four was fighting over,
// the module cache two setup-go steps could not share — and the other half went
// on running a repository nobody is developing.
func TestBothWorkflowsCallOneAction(t *testing.T) {
	commands := string(Workflow(Options{Kind: KindTestBench}))
	lifecycle := string(Workflow(Options{Kind: KindE2E}))

	const action = "Sherlock2019/fullopenclitestbench@"
	for name, rendered := range map[string]string{
		"command bench": commands,
		"lifecycle":     lifecycle,
	} {
		if !strings.Contains(rendered, action) {
			t.Errorf("the %s workflow does not call %s:\n%s", name, action, rendered)
		}
	}

	// And neither still reaches for the other repository.
	for name, rendered := range map[string]string{
		"command bench": commands,
		"lifecycle":     lifecycle,
	} {
		if strings.Contains(rendered, "opencenterclitest-Simple") {
			t.Errorf("the %s workflow still calls the simple project's action", name)
		}
	}
}

// Each workflow names its mode, rather than relying on the action's default.
//
// A reader comparing the two files should be able to see that they are one
// bench run two ways. One file naming a mode and one saying nothing reads as
// two unrelated things, which is how this started.
func TestEachWorkflowNamesItsMode(t *testing.T) {
	commands := string(Workflow(Options{Kind: KindTestBench}))
	if !strings.Contains(commands, "mode: commands") {
		t.Errorf("the command workflow does not name its mode:\n%s", commands)
	}
	if strings.Contains(commands, "mode: lifecycle") {
		t.Error("the command workflow asks for the lifecycle")
	}

	lifecycle := string(Workflow(Options{Kind: KindE2E}))
	if !strings.Contains(lifecycle, "mode: lifecycle") {
		t.Errorf("the lifecycle workflow does not name its mode:\n%s", lifecycle)
	}
}

// A with: block that is always written must never be empty.
//
// An empty `with:` is a YAML syntax error and would break every generated
// workflow — which is why it used to be conditional. It is unconditional now
// only because mode is always in it.
func TestTheWithBlockIsNeverEmpty(t *testing.T) {
	rendered := string(Workflow(Options{Kind: KindTestBench}))
	index := strings.Index(rendered, "        with:\n")
	if index < 0 {
		t.Fatal("no with: block at all")
	}
	rest := rendered[index+len("        with:\n"):]
	first := strings.SplitN(rest, "\n", 2)[0]
	if strings.TrimSpace(first) == "" || !strings.HasPrefix(first, "          ") {
		t.Errorf("the with: block is empty, which is a syntax error:\n%s", rendered)
	}
}
