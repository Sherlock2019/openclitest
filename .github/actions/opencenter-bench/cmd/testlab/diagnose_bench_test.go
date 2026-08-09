package main

import "testing"

// The bench's own subcommands answer for themselves.
//
// Without this the panel under a refused install said three wrong things at
// once: that the arguments might be wrong, that `opencenter cluster doctor`
// might help, and that exit 4 was undocumented. All three contradicted the
// message printed directly above them, which had already said what to do.
func TestABenchRefusalIsExplainedByTheBenchNotTheCLI(t *testing.T) {
	output := `
  ACTIONS SETUP — BLOCKED

    ok      preflight      git@github.com:owner/name.git → main
    FAILED  compare        already calls Owner/bench@main

  why not:
    - an existing workflow already calls this bench and would be overwritten
`
	diagnosis := diagnose(output, "", 4, false)
	if diagnosis == nil {
		t.Fatal("a failed bench step produced no diagnosis")
	}
	if diagnosis.Category != "test-bench" {
		t.Errorf("category is %q, want test-bench", diagnosis.Category)
	}
	for _, cause := range diagnosis.Possible {
		if cause.Check == "opencenter cluster doctor <cluster>" {
			t.Error("still offering cluster doctor for a bench refusal")
		}
		if contains(cause.Why, "not one of the documented codes") {
			t.Error("exit 4 is documented; the panel says it is not")
		}
	}
	// It has to say what exit 4 actually means.
	if !contains(diagnosis.Cause, "approv") && !contains(diagnosis.Cause, "Refused") {
		t.Errorf("the cause does not explain the refusal: %q", diagnosis.Cause)
	}
}

// Each documented code gets its own explanation rather than one catch-all.
func TestEachDocumentedExitCodeIsExplained(t *testing.T) {
	output := "\n  GITOPS UPDATE — FAILED\n"
	for _, code := range []int{2, 3, 4, 5, 6} {
		diagnosis := diagnose(output, "", code, false)
		if diagnosis == nil {
			t.Fatalf("exit %d produced no diagnosis", code)
		}
		if diagnosis.Cause == "" || len(diagnosis.Possible) == 0 {
			t.Errorf("exit %d has no explanation", code)
		}
		for _, cause := range diagnosis.Possible {
			if contains(cause.Why, "not one of the documented codes") {
				t.Errorf("exit %d reported as undocumented", code)
			}
		}
	}
}

// A CLI failure must still get CLI advice — this must not swallow everything.
func TestANormalCLIFailureIsUntouched(t *testing.T) {
	diagnosis := diagnose("Error: cluster configuration not found\n", "", 3, false)
	if diagnosis == nil {
		t.Fatal("no diagnosis for a CLI failure")
	}
	if diagnosis.Category == "test-bench" {
		t.Error("a CLI failure was diagnosed as a bench failure")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}()
}
