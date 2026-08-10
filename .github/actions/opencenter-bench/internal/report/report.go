// Package report turns a finished run into the four things people need from
// it: a page to read, a file to diff, a summary to paste, and XML for CI.
//
// One rule runs through all four. A module that was blocked, skipped, locked
// or never reached is never rendered as a pass. In JUnit that means a real
// <skipped/> element rather than a silent success, because a green pipeline
// built on tests that did not run is worse than a red one.
package report

import (
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/checks"
	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
	"github.com/opencenter-cloud/opencli-testbench/internal/registry"
	"github.com/opencenter-cloud/opencli-testbench/internal/workflow"
)

// WriteAll writes every format into directory and returns the paths written.
func WriteAll(run *workflow.Run, redactor *redact.Redactor, directory string) ([]string, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	if redactor == nil {
		redactor = redact.New()
	}

	files := map[string][]byte{
		"report.json": mustJSON(run),
		"report.md":   []byte(Markdown(run)),
		"report.html": []byte(HTML(run)),
		"junit.xml":   mustJUnit(run),
		"results.csv": []byte(CSV(run)),
	}

	var written []string
	for name, content := range files {
		path := filepath.Join(directory, name)
		// Everything goes through the redactor on the way out, including the
		// formats that were built from already-redacted data. Redacting twice
		// costs nothing; missing once costs a credential.
		safe := redactor.String(string(content))
		if err := os.WriteFile(path, []byte(safe), 0o600); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}

func mustJSON(run *workflow.Run) []byte {
	encoded, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// --- CSV --------------------------------------------------------------------

// CSV is the whole run flattened to one row per assertion — the shape a
// spreadsheet, a diff or a `grep` wants.
//
// Every module appears, including the ones that did not run: a row saying
// "blocked, sops missing" is the point. Filtering those out would give a
// spreadsheet that quietly agrees everything passed.
func CSV(run *workflow.Run) string {
	var buffer strings.Builder
	writer := csv.NewWriter(&buffer)

	_ = writer.Write([]string{
		"run_id", "module_order", "module_id", "module", "phase", "module_state",
		"module_message", "check_id", "check_name", "check_status", "check_millis",
		"assertion", "assertion_status", "detail", "commands", "last_exit_code",
	})

	for _, module := range run.Modules {
		base := []string{
			run.ID, itoa(module.Order), module.ID, module.Name, module.Phase,
			string(module.State), module.Message,
		}

		// A module that never ran still gets a row, so the file has thirty
		// modules in it whatever happened.
		if len(module.Results) == 0 {
			_ = writer.Write(append(append([]string{}, base...),
				"", "", string(module.State), "0", "", "", module.Message, "0", ""))
			continue
		}

		for _, result := range module.Results {
			lastExit := ""
			if count := len(result.Commands); count > 0 {
				lastExit = itoa(result.Commands[count-1].ExitCode)
			}
			checkFields := []string{
				result.ID, result.Name, string(result.Status),
				itoa64(result.Millis),
			}

			if len(result.Assertions) == 0 {
				_ = writer.Write(append(append(append([]string{}, base...), checkFields...),
					"", string(result.Status), result.Message,
					itoa(len(result.Commands)), lastExit))
				continue
			}
			for _, assertion := range result.Assertions {
				_ = writer.Write(append(append(append([]string{}, base...), checkFields...),
					assertion.Name, string(assertion.Status), oneLine(assertion.Detail),
					itoa(len(result.Commands)), lastExit))
			}
		}
	}

	writer.Flush()
	return buffer.String()
}

// oneLine keeps a cell readable. A spreadsheet handles an embedded newline,
// but a person reading the file in a terminal does not.
func oneLine(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 400 {
		value = value[:400] + "…"
	}
	return strings.TrimSpace(value)
}

func itoa(value int) string     { return fmt.Sprintf("%d", value) }
func itoa64(value int64) string { return fmt.Sprintf("%d", value) }

// --- Markdown ---------------------------------------------------------------

// Markdown is the summary to paste into a ticket.
func Markdown(run *workflow.Run) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# openCenter CLI A-to-Z test report\n\n")
	fmt.Fprintf(&b, "**%s**\n\n", verdict(run))

	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	row := func(key, value string) {
		if value != "" {
			fmt.Fprintf(&b, "| %s | %s |\n", key, value)
		}
	}
	row("Run", run.ID)
	row("Binary", run.Binary)
	row("Version", run.Version)
	row("Source", run.Source)
	row("Branch", run.SourceBranch)
	row("Commit", run.SourceCommit)
	row("Working tree", run.SourceDirty)
	row("Host", run.Host)
	row("Platform", run.Platform)
	row("Started", run.Started.Format(time.RFC3339))
	row("Duration", formatDuration(run.Millis))
	row("Live infrastructure", liveDescription(run))

	fmt.Fprintf(&b, "\n## Modules\n\n")
	fmt.Fprintf(&b, "| # | Module | Result | Checks | Duration |\n|---|---|---|---|---|\n")
	for _, module := range run.Modules {
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s |\n",
			module.Order, module.Name, stateLabel(module.State),
			checkRatio(&module), formatDuration(module.Millis))
	}

	failures := failingChecks(run)
	if len(failures) > 0 {
		fmt.Fprintf(&b, "\n## Failed checks\n\n")
		for _, item := range failures {
			fmt.Fprintf(&b, "### %s — %s\n\n", item.module, item.result.Name)
			for _, assertion := range item.result.Assertions {
				if assertion.Status == checks.StatusFail {
					fmt.Fprintf(&b, "- **%s** — %s\n", assertion.Name, assertion.Detail)
				}
			}
			if item.result.Message != "" {
				fmt.Fprintf(&b, "\n%s\n", item.result.Message)
			}
			fmt.Fprintln(&b)
		}
	}

	notRun := unansweredModules(run)
	if len(notRun) > 0 {
		fmt.Fprintf(&b, "\n## Questions this run did not answer\n\n")
		for _, module := range notRun {
			fmt.Fprintf(&b, "- **%s** (%s) — %s\n", module.Name, stateLabel(module.State), module.Message)
		}
	}

	if len(run.Resources) > 0 {
		fmt.Fprintf(&b, "\n## Resources created, and what became of them\n\n")
		fmt.Fprintf(&b, "| Type | Name | Provider | Cleanup |\n|---|---|---|---|\n")
		for _, resource := range run.Resources {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
				resource.Type, resource.Name, resource.Provider, cleanupLabel(resource))
		}
	}

	return b.String()
}

// --- HTML -------------------------------------------------------------------

// HTML is the page to read. It is one self-contained file: no stylesheet, no
// script, nothing fetched from anywhere, so it can be attached to a ticket or
// opened from a CI artifact without a network.
func HTML(run *workflow.Run) string {
	var b strings.Builder

	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	fmt.Fprintf(&b, `<title>openCenter CLI A-to-Z report %s</title>`, esc(run.ID))
	b.WriteString(`<style>
:root{--bg:#1e1e1e;--panel:#252526;--fg:#ccc;--dim:#858585;--line:#333;
--pass:#73c991;--fail:#f14c4c;--warn:#cca700;--link:#3794ff}
@media(prefers-color-scheme:light){:root{--bg:#fff;--panel:#f3f3f3;--fg:#333;
--dim:#666;--line:#e0e0e0;--pass:#16825d;--fail:#cd3131;--warn:#a67200;--link:#005fb8}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);
font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
.wrap{max-width:1100px;margin:0 auto;padding:32px 24px 80px}
h1{font-size:22px;font-weight:400;margin:0 0 4px}h2{font-size:15px;margin:34px 0 12px}
h3{font-size:13px;margin:20px 0 6px}
.verdict{display:inline-block;padding:4px 12px;border-radius:3px;font-weight:600;margin:8px 0 20px}
.verdict.pass{background:rgba(115,201,145,.15);color:var(--pass)}
.verdict.fail{background:rgba(241,76,76,.15);color:var(--fail)}
.verdict.warn{background:rgba(204,167,0,.15);color:var(--warn)}
table{width:100%;border-collapse:collapse;font-size:13px;margin-bottom:8px}
th,td{text-align:left;padding:6px 10px;border-bottom:1px solid var(--line);vertical-align:top}
th{font-size:11px;text-transform:uppercase;letter-spacing:.05em;color:var(--dim)}
td.num{width:40px;color:var(--dim);font-variant-numeric:tabular-nums}
.s{font-weight:600}.s.passed{color:var(--pass)}.s.failed{color:var(--fail)}
.s.warning,.s.blocked{color:var(--warn)}.s.skipped,.s.locked,.s.not_started{color:var(--dim)}
.meta{display:grid;grid-template-columns:170px 1fr;gap:2px 16px;font-size:13px;margin-bottom:8px}
.meta dt{color:var(--dim)}.meta dd{margin:0}
pre{background:var(--panel);padding:10px 12px;overflow-x:auto;white-space:pre-wrap;
word-break:break-word;font-size:12px;border-left:2px solid var(--line);margin:6px 0}
.f{background:var(--panel);padding:12px 14px;margin:10px 0;border-left:3px solid var(--fail)}
.a{font-size:12.5px;padding:2px 0}.a .n{color:var(--fail)}
.dim{color:var(--dim)}
footer{margin-top:48px;color:var(--dim);font-size:12px}
</style></head><body><div class="wrap">`)

	fmt.Fprintf(&b, `<h1>openCenter CLI A-to-Z test report</h1>`)
	fmt.Fprintf(&b, `<div class="verdict %s">%s</div>`, verdictClass(run), esc(verdict(run)))

	b.WriteString(`<dl class="meta">`)
	meta := func(key, value string) {
		if value != "" {
			fmt.Fprintf(&b, `<dt>%s</dt><dd>%s</dd>`, esc(key), esc(value))
		}
	}
	meta("Run", run.ID)
	meta("Binary", run.Binary)
	meta("Version", run.Version)
	meta("Source", run.Source)
	meta("Branch", run.SourceBranch)
	meta("Commit", run.SourceCommit)
	meta("Working tree", run.SourceDirty)
	meta("Host", run.Host)
	meta("Platform", run.Platform)
	meta("Started", run.Started.Format(time.RFC3339))
	meta("Duration", formatDuration(run.Millis))
	meta("Live infrastructure", liveDescription(run))
	b.WriteString(`</dl>`)

	b.WriteString(`<h2>Modules</h2><table><thead><tr><th>#</th><th>Module</th>` +
		`<th>Result</th><th>Checks</th><th>Duration</th><th>Note</th></tr></thead><tbody>`)
	for _, module := range run.Modules {
		fmt.Fprintf(&b,
			`<tr><td class="num">%d</td><td>%s</td><td class="s %s">%s</td>`+
				`<td>%s</td><td>%s</td><td class="dim">%s</td></tr>`,
			module.Order, esc(module.Name), esc(string(module.State)),
			esc(stateLabel(module.State)), esc(checkRatio(&module)),
			esc(formatDuration(module.Millis)), esc(module.Message))
	}
	b.WriteString(`</tbody></table>`)

	failures := failingChecks(run)
	if len(failures) > 0 {
		b.WriteString(`<h2>Failed checks</h2>`)
		for _, item := range failures {
			fmt.Fprintf(&b, `<div class="f"><h3>%s — %s</h3>`,
				esc(item.module), esc(item.result.Name))
			for _, assertion := range item.result.Assertions {
				if assertion.Status == checks.StatusFail {
					fmt.Fprintf(&b, `<div class="a"><span class="n">✕ %s</span> — %s</div>`,
						esc(assertion.Name), esc(assertion.Detail))
				}
			}
			for _, invocation := range item.result.Commands {
				fmt.Fprintf(&b, `<pre>$ %s
→ exit %d
%s%s</pre>`, esc(invocation.Command), invocation.ExitCode,
					esc(invocation.Stdout), esc(invocation.Stderr))
			}
			b.WriteString(`</div>`)
		}
	}

	if notRun := unansweredModules(run); len(notRun) > 0 {
		b.WriteString(`<h2>Questions this run did not answer</h2><table><tbody>`)
		for _, module := range notRun {
			fmt.Fprintf(&b, `<tr><td>%s</td><td class="s %s">%s</td><td class="dim">%s</td></tr>`,
				esc(module.Name), esc(string(module.State)),
				esc(stateLabel(module.State)), esc(module.Message))
		}
		b.WriteString(`</tbody></table>`)
	}

	if len(run.Resources) > 0 {
		b.WriteString(`<h2>Resources created</h2><table><thead><tr><th>Type</th>` +
			`<th>Name</th><th>Provider</th><th>Cleanup</th></tr></thead><tbody>`)
		for _, resource := range run.Resources {
			fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				esc(resource.Type), esc(resource.Name), esc(resource.Provider),
				esc(cleanupLabel(resource)))
		}
		b.WriteString(`</tbody></table>`)
	}

	if run.Preflight != nil {
		fmt.Fprintf(&b, `<h2>Preflight</h2><p class="dim">%d present, %d missing.</p>`,
			run.Preflight.Present, run.Preflight.Missing)
		b.WriteString(`<table><tbody>`)
		for _, item := range run.Preflight.Results {
			fmt.Fprintf(&b, `<tr><td>%s</td><td class="s %s">%s</td><td class="dim">%s</td></tr>`,
				esc(item.Name),
				map[bool]string{true: "passed", false: "blocked"}[item.Status == "present"],
				esc(string(item.Status)), esc(item.Detail))
		}
		b.WriteString(`</tbody></table>`)
	}

	b.WriteString(`<footer>Every value that looked like a credential was removed before this ` +
		`file was written. Modules that were blocked, skipped or never reached are ` +
		`reported as such and are never counted as passes.</footer>`)
	b.WriteString(`</div></body></html>`)
	return b.String()
}

// --- JUnit ------------------------------------------------------------------

type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Time     string       `xml:"time,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	Skipped   *junitSkipped `xml:"skipped,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

type junitSkipped struct {
	Message string `xml:"message,attr"`
}

func mustJUnit(run *workflow.Run) []byte {
	suites := junitSuites{Name: "opencenter-cli-a-to-z", Time: seconds(run.Millis)}

	for _, module := range run.Modules {
		suite := junitSuite{
			Name: fmt.Sprintf("%02d %s", module.Order, module.Name),
			Time: seconds(module.Millis),
		}

		// A module that never executed becomes one skipped case rather than
		// vanishing. A suite with no cases reads as "nothing to test here",
		// which is the opposite of what happened.
		if !module.State.Executed() {
			suite.Cases = append(suite.Cases, junitCase{
				Name:      module.Name,
				ClassName: "module." + module.ID,
				Time:      "0",
				Skipped:   &junitSkipped{Message: stateLabel(module.State) + ": " + module.Message},
			})
			suite.Tests = 1
			suite.Skipped = 1
			suites.Suites = append(suites.Suites, suite)
			suites.Tests++
			suites.Skipped++
			continue
		}

		for _, result := range module.Results {
			testCase := junitCase{
				Name:      result.Name,
				ClassName: "module." + module.ID + "." + result.ID,
				Time:      seconds(result.Millis),
			}
			switch result.Status {
			case checks.StatusFail, checks.StatusError:
				var details []string
				for _, assertion := range result.Assertions {
					if assertion.Status == checks.StatusFail {
						details = append(details, assertion.Name+": "+assertion.Detail)
					}
				}
				if result.Message != "" {
					details = append(details, result.Message)
				}
				testCase.Failure = &junitFailure{
					Message: firstOf(details, string(result.Status)),
					Type:    string(result.Status),
					Body:    strings.Join(details, "\n"),
				}
				suite.Failures++
				suites.Failures++
			case checks.StatusSkip:
				testCase.Skipped = &junitSkipped{Message: result.Message}
				suite.Skipped++
				suites.Skipped++
			}
			suite.Cases = append(suite.Cases, testCase)
			suite.Tests++
			suites.Tests++
		}
		suites.Suites = append(suites.Suites, suite)
	}

	encoded, err := xml.MarshalIndent(suites, "", "  ")
	if err != nil {
		return []byte(`<testsuites/>`)
	}
	return append([]byte(xml.Header), encoded...)
}

// --- shared -----------------------------------------------------------------

type failure struct {
	module string
	result checks.Result
}

func failingChecks(run *workflow.Run) []failure {
	var out []failure
	for _, module := range run.Modules {
		for _, result := range module.Results {
			if result.Status == checks.StatusFail || result.Status == checks.StatusError {
				out = append(out, failure{module: fmt.Sprintf("%d. %s", module.Order, module.Name), result: result})
			}
		}
	}
	return out
}

// unansweredModules is the section a reader should look at before believing a
// green run: the questions nobody asked.
func unansweredModules(run *workflow.Run) []workflow.ModuleResult {
	var out []workflow.ModuleResult
	for _, module := range run.Modules {
		if !module.State.Executed() {
			out = append(out, module)
		}
	}
	return out
}

func verdict(run *workflow.Run) string {
	switch {
	case run.Failed > 0:
		return fmt.Sprintf("Failed — %d of %d modules failed", run.Failed, len(run.Modules))
	case run.Blocked > 0 || run.Skipped > 0 || run.NotRun > 0:
		return fmt.Sprintf("Passed with gaps — %d passed, %d not answered",
			run.Passed, run.Blocked+run.Skipped+run.NotRun)
	case run.Warning > 0:
		return fmt.Sprintf("Passed with warnings — %d passed, %d with warnings", run.Passed, run.Warning)
	default:
		return fmt.Sprintf("Passed — all %d modules", len(run.Modules))
	}
}

func verdictClass(run *workflow.Run) string {
	switch {
	case run.Failed > 0:
		return "fail"
	case run.Blocked > 0 || run.Skipped > 0 || run.NotRun > 0 || run.Warning > 0:
		return "warn"
	default:
		return "pass"
	}
}

func stateLabel(state workflow.ModuleState) string {
	return map[workflow.ModuleState]string{
		workflow.StatePassed:     "Passed",
		workflow.StateFailed:     "Failed",
		workflow.StateWarning:    "Warning",
		workflow.StateBlocked:    "Blocked",
		workflow.StateSkipped:    "Skipped",
		workflow.StateLocked:     "Locked",
		workflow.StateCancelled:  "Cancelled",
		workflow.StateNotStarted: "Not run",
	}[state]
}

func checkRatio(module *workflow.ModuleResult) string {
	total := module.Checks()
	if total == 0 {
		return "—"
	}
	return fmt.Sprintf("%d/%d", module.Passed, total)
}

func cleanupLabel(resource registry.Resource) string {
	switch resource.Status {
	case registry.StatusDeleted:
		return "confirmed gone"
	case registry.StatusNotFound:
		return "already absent"
	case registry.StatusFailed:
		return "STILL PRESENT — " + resource.Detail
	default:
		return "not verified"
	}
}

func liveDescription(run *workflow.Run) string {
	if !run.Live {
		return "none — no infrastructure was created"
	}
	return strings.Join(run.LiveProviders, ", ")
}

func formatDuration(millis int64) string {
	if millis == 0 {
		return "—"
	}
	if millis < 1000 {
		return fmt.Sprintf("%dms", millis)
	}
	total := millis / 1000
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	return fmt.Sprintf("%dm %ds", total/60, total%60)
}

func seconds(millis int64) string { return fmt.Sprintf("%.3f", float64(millis)/1000) }

func firstOf(items []string, fallback string) string {
	if len(items) == 0 {
		return fallback
	}
	return items[0]
}

func esc(value string) string { return html.EscapeString(value) }
