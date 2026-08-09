package report

import (
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/checks"
	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
	"github.com/opencenter-cloud/opencli-testbench/internal/registry"
	"github.com/opencenter-cloud/opencli-testbench/internal/workflow"
)

func sampleRun() *workflow.Run {
	return &workflow.Run{
		ID:       "20260802-140250",
		Started:  time.Date(2026, 8, 2, 14, 2, 50, 0, time.UTC),
		Millis:   93_000,
		Binary:   "/tmp/opencenter",
		Version:  "opencenter version: 0.0.1",
		Host:     "linux/amd64",
		Platform: "linux/amd64",
		Modules: []workflow.ModuleResult{
			{
				ID: "installation", Order: 1, Name: "Installation",
				State: workflow.StatePassed, Millis: 8_000,
				Results: []checks.Result{
					{ID: "install-runs", Name: "The binary starts", Status: checks.StatusPass},
				},
			},
			{
				ID: "errors", Order: 6, Name: "Errors",
				State: workflow.StateFailed, Millis: 1_200, Blocking: true,
				Results: []checks.Result{{
					ID: "errors-exit-code", Name: "Documented exit code",
					Status: checks.StatusFail,
					Assertions: []checks.Assertion{
						{Name: "exit code is 3", Status: checks.StatusFail, Detail: "got exit 1"},
					},
					Commands: []checks.Invocation{{
						Command:  "opencenter cluster validate missing",
						ExitCode: 1,
						Stderr:   "Error: not found",
					}},
				}},
			},
			{
				ID: "secrets", Order: 23, Name: "Secrets management",
				State: workflow.StateBlocked, Message: "missing: sops",
			},
			{
				ID: "real-environment", Order: 29, Name: "Real environment",
				State: workflow.StateLocked, Message: "live testing was not approved", Live: true,
			},
		},
		Resources: []registry.Resource{
			{ID: "kind-1", Type: "kind-cluster", Name: "bench-42",
				Provider: "kind", Status: registry.StatusDeleted},
		},
		Failed: 1, Passed: 1, Blocked: 1, Skipped: 1,
	}
}

// A pipeline believes the JUnit file. A module that never ran has to appear in
// it as a skipped case, never as a silent success and never as nothing at all.
func TestJUnitRepresentsUnrunModulesAsSkipped(t *testing.T) {
	var parsed junitView
	if err := xml.Unmarshal(mustJUnit(sampleRun()), &parsed); err != nil {
		t.Fatalf("the generated JUnit does not parse: %v", err)
	}

	if parsed.Tests != 4 {
		t.Errorf("JUnit reports %d tests, want 4 — an unrun module must not vanish", parsed.Tests)
	}
	if parsed.Failures != 1 {
		t.Errorf("JUnit reports %d failures, want 1", parsed.Failures)
	}
	if parsed.Skipped != 2 {
		t.Errorf("JUnit reports %d skipped, want 2 (one blocked, one locked)", parsed.Skipped)
	}

	for _, suite := range parsed.Suites {
		switch {
		case strings.Contains(suite.Name, "Real environment"),
			strings.Contains(suite.Name, "Secrets"):
			if len(suite.Cases) != 1 || suite.Cases[0].Skipped == nil {
				t.Errorf("%s is not marked skipped", suite.Name)
			}
			if suite.Cases[0].Skipped != nil && suite.Cases[0].Skipped.Message == "" {
				t.Errorf("%s is skipped with no reason given", suite.Name)
			}
		}
	}
}

func TestJUnitCarriesTheFailureDetail(t *testing.T) {
	var parsed junitView
	if err := xml.Unmarshal(mustJUnit(sampleRun()), &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	for _, suite := range parsed.Suites {
		for _, testCase := range suite.Cases {
			if testCase.Failure != nil {
				found = true
				if !strings.Contains(testCase.Failure.Message, "exit code is 3") {
					t.Errorf("the failure message does not say what failed: %q", testCase.Failure.Message)
				}
			}
		}
	}
	if !found {
		t.Error("the failed check produced no <failure> element")
	}
}

func TestMarkdownNamesTheGaps(t *testing.T) {
	markdown := Markdown(sampleRun())

	for _, wanted := range []string{
		"Questions this run did not answer",
		"Secrets management",
		"Real environment",
		"Blocked",
		"Locked",
	} {
		if !strings.Contains(markdown, wanted) {
			t.Errorf("the Markdown report does not mention %q", wanted)
		}
	}
	if strings.Contains(markdown, "Passed — all") {
		t.Error("a run with a failure and two gaps was described as fully passed")
	}
}

func TestHTMLIsSelfContained(t *testing.T) {
	page := HTML(sampleRun())

	for _, forbidden := range []string{"<script", "http://", "https://cdn", "<link rel=\"stylesheet\""} {
		if strings.Contains(page, forbidden) {
			t.Errorf("the HTML report is not self-contained: it contains %q", forbidden)
		}
	}
	if !strings.Contains(page, "Questions this run did not answer") {
		t.Error("the HTML report does not list the modules that did not run")
	}
}

func TestHTMLEscapesWhatCameFromTheCLI(t *testing.T) {
	run := sampleRun()
	run.Modules[1].Results[0].Commands[0].Stderr = `<script>alert("x")</script>`

	page := HTML(run)
	if strings.Contains(page, "<script>alert") {
		t.Error("CLI output was rendered as markup rather than escaped")
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Error("the escaped form is missing, so the output was dropped instead")
	}
}

// Everything leaving the bench goes through the redactor, including the
// formats that were built from already-clean data.
func TestWriteAllRedacts(t *testing.T) {
	run := sampleRun()
	run.Modules[1].Results[0].Commands[0].Stdout = "password=hunter2-not-a-real-password"

	redactor := redact.New()
	redactor.Add("hunter2-not-a-real-password")

	directory := t.TempDir()
	written, err := WriteAll(run, redactor, directory)
	if err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	if len(written) != 5 {
		t.Errorf("wrote %d files, want 5 (html, json, md, junit, csv)", len(written))
	}

	for _, path := range written {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(content), "hunter2-not-a-real-password") {
			t.Errorf("%s contains an unredacted secret", filepath.Base(path))
		}
	}
}

func TestWriteAllProducesEveryFormat(t *testing.T) {
	directory := t.TempDir()
	if _, err := WriteAll(sampleRun(), nil, directory); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	for _, name := range []string{"report.html", "report.json", "report.md", "junit.xml", "results.csv"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded workflow.Run
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Errorf("the JSON report does not parse: %v", err)
	}
	if len(decoded.Modules) != 4 {
		t.Errorf("the JSON report holds %d modules, want 4", len(decoded.Modules))
	}
}

// The CSV is what people open in a spreadsheet, so a module that did not run
// has to be a row in it. Dropping those rows would give a file that quietly
// agrees everything passed.
func TestCSVKeepsEveryModuleIncludingTheOnesThatDidNotRun(t *testing.T) {
	rows, err := csv.NewReader(strings.NewReader(CSV(sampleRun()))).ReadAll()
	if err != nil {
		t.Fatalf("the generated CSV does not parse: %v", err)
	}
	if len(rows) < 2 {
		t.Fatal("the CSV has no data rows")
	}

	header := rows[0]
	if header[0] != "run_id" || !contains(header, "assertion_status") || !contains(header, "module_state") {
		t.Errorf("the header is missing columns: %v", header)
	}

	seen := map[string]string{}
	for _, row := range rows[1:] {
		if len(row) != len(header) {
			t.Errorf("row has %d fields, header has %d: %v", len(row), len(header), row)
		}
		seen[row[2]] = row[5] // module_id -> module_state
	}

	for _, expected := range []struct{ id, state string }{
		{"installation", "passed"},
		{"errors", "failed"},
		{"secrets", "blocked"},
		{"real-environment", "locked"},
	} {
		if seen[expected.id] != expected.state {
			t.Errorf("module %s is %q in the CSV, want %q", expected.id, seen[expected.id], expected.state)
		}
	}
}

func TestCSVCarriesTheFailingAssertion(t *testing.T) {
	body := CSV(sampleRun())
	if !strings.Contains(body, "exit code is 3") {
		t.Error("the failing assertion is not in the CSV")
	}
	if !strings.Contains(body, "got exit 1") {
		t.Error("the assertion detail is not in the CSV")
	}
}

func TestCSVFlattensNewlinesInDetails(t *testing.T) {
	run := sampleRun()
	run.Modules[1].Results[0].Assertions[0].Detail = "first line\nsecond line"

	rows, err := csv.NewReader(strings.NewReader(CSV(run))).ReadAll()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, row := range rows {
		for _, field := range row {
			if strings.Contains(field, "\n") {
				t.Errorf("a field still holds a newline: %q", field)
			}
		}
	}
}

func contains(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}

func TestVerdictDistinguishesGapsFromSuccess(t *testing.T) {
	clean := &workflow.Run{Passed: 30, Modules: make([]workflow.ModuleResult, 30)}
	if got := verdict(clean); !strings.HasPrefix(got, "Passed — all") {
		t.Errorf("a clean run reads as %q", got)
	}

	gappy := &workflow.Run{Passed: 28, Blocked: 2, Modules: make([]workflow.ModuleResult, 30)}
	if got := verdict(gappy); !strings.Contains(got, "not answered") {
		t.Errorf("a run with blocked modules reads as %q, which hides the gap", got)
	}

	broken := &workflow.Run{Passed: 29, Failed: 1, Modules: make([]workflow.ModuleResult, 30)}
	if got := verdict(broken); !strings.HasPrefix(got, "Failed") {
		t.Errorf("a run with a failure reads as %q", got)
	}
}

type junitView struct {
	XMLName  xml.Name `xml:"testsuites"`
	Tests    int      `xml:"tests,attr"`
	Failures int      `xml:"failures,attr"`
	Skipped  int      `xml:"skipped,attr"`
	Suites   []struct {
		Name  string `xml:"name,attr"`
		Cases []struct {
			Name    string `xml:"name,attr"`
			Skipped *struct {
				Message string `xml:"message,attr"`
			} `xml:"skipped"`
			Failure *struct {
				Message string `xml:"message,attr"`
			} `xml:"failure"`
		} `xml:"testcase"`
	} `xml:"testsuite"`
}
