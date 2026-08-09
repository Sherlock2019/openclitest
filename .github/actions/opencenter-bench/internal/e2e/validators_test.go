package e2e

import (
	"os"
	"strings"
	"testing"
)

// phaseSource returns one function's body from phases.go.
//
// Sliced from "func <name>" to the next top-level func, so an assertion about
// this phase cannot be satisfied by text somewhere else in the file.
func phaseSource(t *testing.T, name string) string {
	t.Helper()
	file, err := os.ReadFile("phases.go")
	if err != nil {
		t.Fatalf("read phases.go: %v", err)
	}
	source := string(file)
	start := strings.Index(source, "func "+name)
	if start < 0 {
		t.Fatalf("phases.go has no %s", name)
	}
	rest := source[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end >= 0 {
		rest = rest[:end+1]
	}
	return rest
}

// A formatting complaint must not fail a run, and must name the files.
//
// Three of four profiles on GitHub passed every phase and then failed the whole
// run at validate-artifacts, because `tofu fmt -check -recursive` exited 3. The
// finding said
//
//	actual: exit 3:
//
// with nothing after the colon: `fmt -check` writes the offending file names to
// stdout and the phase was reading stderr. So the report blocked a release over
// whitespace and did not say which file.
//
// Asserted on the phase's own source rather than by running tofu, which is not
// installed on every machine this test runs on — the point is the two decisions,
// and both are visible in the code.
func TestFormattingIsAWarningThatNamesItsFiles(t *testing.T) {
	source := phaseSource(t, "phaseValidateArtifacts")

	if !strings.Contains(source, "style: true") {
		t.Error("no validator is marked as a style check, so `tofu fmt` " +
			"failing blocks a release over whitespace")
	}
	if !strings.Contains(source, "StateWarning") {
		t.Error("validate-artifacts cannot warn, so a formatting finding is " +
			"either fatal or silent")
	}
	if !strings.Contains(source, "lastMeaningfulLine(out.Stdout)") {
		t.Error("the finding does not read stdout, where `fmt -check` names " +
			"the files it rejected")
	}
	// And a real rejection — kustomize or kubectl refusing the artifacts — must
	// still fail. A phase that only ever warns is not a gate.
	if !strings.Contains(source, `Fail("a validator rejected the generated artifacts"`) {
		t.Error("nothing fails validate-artifacts any more")
	}
}
