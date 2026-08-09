package actionsetup

import "testing"

// The bench writes its counts at the top level of report.json, beside modules.
// Reading only a nested `summary` reported every run as 0 passed, 0 failed —
// which looks like a bench that tested nothing rather than a parser looking in
// the wrong place. Both shapes are accepted, as the action's own parser does.
func TestCountsAreReadFromEitherShape(t *testing.T) {
	topLevel := []byte(`{"passed":1,"failed":1,"warning":0,"blocked":0,"skipped":28,
		"modules":[]}`)
	summary, _, err := parseReport(topLevel)
	if err != nil {
		t.Fatalf("parseReport: %v", err)
	}
	if summary.Passed != 1 || summary.Failed != 1 || summary.Skipped != 28 {
		t.Errorf("top-level counts read as %+v", summary)
	}

	nested := []byte(`{"summary":{"passed":7,"failed":2,"skipped":3},"modules":[]}`)
	summary, _, err = parseReport(nested)
	if err != nil {
		t.Fatalf("parseReport: %v", err)
	}
	if summary.Passed != 7 || summary.Failed != 2 || summary.Skipped != 3 {
		t.Errorf("nested counts read as %+v", summary)
	}
}

// A failing check carries its passing assertions too. Listing those buries the
// two lines that are the actual finding.
func TestOnlyFailingAssertionsAreReported(t *testing.T) {
	raw := []byte(`{
	  "passed":1,"failed":1,
	  "modules":[{"id":"commands","results":[
	    {"id":"coverage-cluster-backup","name":"Every cluster backup command runs",
	     "status":"fail","assertions":[
	       {"name":"it answers","status":"pass","detail":"fine"},
	       {"name":"--output json parses","status":"fail","detail":"invalid character 'N'"},
	       {"name":"it does not panic","status":"pass","detail":"fine"},
	       {"name":"schedule answers","status":"fail","detail":"did not return within 25s"}
	     ]},
	    {"id":"coverage-cluster-list","status":"pass","assertions":[
	       {"name":"it answers","status":"fail","detail":"never reached — the check passed"}
	     ]}
	  ]}]
	}`)

	_, failures, err := parseReport(raw)
	if err != nil {
		t.Fatalf("parseReport: %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("got %d failing checks, want 1 — a passing check was reported", len(failures))
	}
	if failures[0].Check != "coverage-cluster-backup" || failures[0].Module != "commands" {
		t.Errorf("wrong check reported: %+v", failures[0])
	}
	if len(failures[0].Assertions) != 2 {
		t.Fatalf("got %d assertions, want the 2 that failed: %v",
			len(failures[0].Assertions), failures[0].Assertions)
	}
	for _, assertion := range failures[0].Assertions {
		if assertion == "" {
			t.Error("an empty assertion line")
		}
	}
}

// A run that passed has nothing to show, and must not be reported as an error.
func TestACleanReportHasNoFailures(t *testing.T) {
	_, failures, err := parseReport([]byte(
		`{"passed":30,"failed":0,"modules":[{"id":"commands","results":[
		   {"id":"a","status":"pass"},{"id":"b","status":"skip"}]}]}`))
	if err != nil {
		t.Fatalf("parseReport: %v", err)
	}
	if len(failures) != 0 {
		t.Errorf("a clean run reported %d failures: %+v", len(failures), failures)
	}
}

func TestOutcomeReadsRunningAndFinishedRunsDifferently(t *testing.T) {
	for _, testCase := range []struct {
		run  Run
		want string
	}{
		{Run{Status: "completed", Conclusion: "success"}, "success"},
		{Run{Status: "completed", Conclusion: "failure"}, "failure"},
		{Run{Status: "in_progress"}, "in_progress"},
		{Run{Status: "queued"}, "queued"},
		{Run{Status: "completed"}, "unknown"},
	} {
		if got := testCase.run.Outcome(); got != testCase.want {
			t.Errorf("Outcome(%+v) = %q, want %q", testCase.run, got, testCase.want)
		}
	}
}
