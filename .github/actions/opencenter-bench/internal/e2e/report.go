package e2e

import (
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Five formats, one run.
//
// Each exists for a different reader: JUnit for the CI that gates the release,
// JSON for anything programmatic, CSV for a spreadsheet, Markdown for a ticket,
// HTML for a person. They are generated from the same Run, so they cannot
// disagree about what happened — which is the failure mode of writing a summary
// by hand alongside a machine-readable one.

// WriteReports writes every format and returns how many files it wrote.
func WriteReports(run *Run, verdict Verdict, why string) (int, error) {
	reports := filepath.Join(run.Root, "reports")
	if err := os.MkdirAll(reports, 0o755); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Join(run.Root, "junit"), 0o755); err != nil {
		return 0, err
	}

	written := 0
	for _, item := range []struct {
		path    string
		content []byte
	}{
		{filepath.Join(reports, "report.json"), mustJSON(reportPayload(run, verdict, why))},
		{filepath.Join(reports, "report.md"), []byte(markdownReport(run, verdict, why))},
		{filepath.Join(reports, "report.html"), []byte(htmlReport(run, verdict, why))},
		{filepath.Join(run.Root, "junit", "e2e.xml"), junitReport(run)},
	} {
		if err := os.WriteFile(item.path, item.content, 0o644); err != nil {
			return written, err
		}
		written++
	}

	if err := writeCSV(filepath.Join(reports, "summary.csv"), run); err != nil {
		return written, err
	}
	return written + 1, nil
}

// consoleTokens is the console bench's palette, so the two look like one product.
//
// VS Code Dark Modern, copied rather than imported because these pages are
// standalone files with no stylesheet to link — a report emailed into a ticket
// or opened from a CI artifact has nothing else to load. The values are the
// contract: if the console's :root changes, this follows.
const consoleTokens = `:root{
  --bg:#1F1F1F; --panel:#181818; --panel2:#252526;
  --line:#2B2B2B; --fg:#CCCCCC; --fg2:#9D9D9D; --fg3:#6E7681;
  --vs-lightblue:#9CDCFE; --vs-teal:#4EC9B0; --vs-red:#F44747;
  --mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,monospace;
}
*{box-sizing:border-box}
body{background:var(--bg);color:var(--fg);margin:0;padding:0;
  font:14px/1.55 system-ui,-apple-system,"Segoe UI",sans-serif}
.page{max-width:72rem;margin:0 auto;padding:14px}
a{color:var(--vs-lightblue)}`

func reportPayload(run *Run, verdict Verdict, why string) map[string]any {
	return map[string]any{
		"run": run, "verdict": string(verdict), "verdict_reason": why,
		"findings": run.Findings(), "remaining": run.Remaining(),
	}
}

func markdownReport(run *Run, verdict Verdict, why string) string {
	out := &strings.Builder{}
	fmt.Fprintf(out, "# openCenter E2E — %s\n\n**%s** — %s\n\n", run.ID, verdict, why)
	if banner := simulatedBanner(run); banner != "" {
		fmt.Fprintf(out, "> **SIMULATED RUN.** %s\n\n", banner)
	}

	fmt.Fprintf(out, "| | |\n|---|---|\n")
	for _, row := range [][2]string{
		{"Profile", run.Profile}, {"Provider", string(run.Provider)},
		{"Infrastructure", string(run.Infrastructure)}, {"Channel", string(run.Channel)},
		{"Cluster", run.Cluster}, {"CLI version", run.CLIVersion},
		{"CLI commit", short(run.CLICommit)}, {"Binary checksum", short(run.CLIChecksum)},
		{"Host", run.Host}, {"Started", run.Started.Format("2006-01-02 15:04:05 MST")},
	} {
		if row[1] != "" {
			fmt.Fprintf(out, "| %s | %s |\n", row[0], row[1])
		}
	}

	fmt.Fprintf(out, "\n## Phases\n\n| # | Phase | State | Took | Notes |\n|---|---|---|---|---|\n")
	for _, phase := range run.Phases {
		if phase.State == StateNotStarted {
			continue
		}
		fmt.Fprintf(out, "| %d | %s | %s | %s | %s |\n",
			phase.Number, phase.ID, phase.State, took(phase.Millis), phase.Message)
	}

	findings := run.Findings()
	fmt.Fprintf(out, "\n## Failures\n\n")
	if len(findings) == 0 {
		fmt.Fprintf(out, "None.\n")
	} else {
		fmt.Fprintf(out, "| Command | Phase | Environment | Expected | Actual | Cause | Category | Regression |\n")
		fmt.Fprintf(out, "|---|---|---|---|---|---|---|---|\n")
		for _, f := range findings {
			fmt.Fprintf(out, "| `%s` | %s | %s | %s | %s | %s | %s | %s |\n",
				f.Command, f.Phase, f.Environment, f.Expected, f.Actual, f.Cause,
				f.Category, yesNo(f.Regression))
		}
	}

	fmt.Fprintf(out, "\n## Cleanup\n\n")
	remaining := run.Remaining()
	fmt.Fprintf(out, "- created: %d\n- removed: %d\n- remaining: %d\n",
		len(run.Resources), len(run.Resources)-len(remaining), len(remaining))
	for _, resource := range remaining {
		fmt.Fprintf(out, "  - **%s %s** — %s\n", resource.Kind, resource.Name, resource.Remediation)
	}
	return out.String()
}

func htmlReport(run *Run, verdict Verdict, why string) string {
	tone := map[Verdict]string{
		VerdictPass: "#1a7f37", VerdictWarning: "#9a6700",
		VerdictFail: "#cf222e", VerdictInconclusive: "#57606a",
		VerdictSimulated: "#8250df",
	}[verdict]

	out := &strings.Builder{}
	fmt.Fprintf(out, `<!doctype html><meta charset="utf-8">
<title>openCenter E2E %s</title>
<style>
%s
/* The banner Environment and GitHub Actions wear in the console, so the two
   benches read as one product rather than as two that happen to share a name. */
.hd{margin:-14px -14px 18px;padding:16px 20px;
  background:linear-gradient(90deg,#0A84FF,#1B7CFA);color:#fff;
  box-shadow:0 3px 12px rgba(10,132,255,.22)}
.hd h1{margin:0;font-size:17px;font-weight:850;letter-spacing:.09em;
  text-transform:uppercase;color:#fff}
.hd .sub{font-size:11.5px;opacity:.9;margin-top:3px;font-family:var(--mono)}
.v{display:inline-block;padding:.32rem .85rem;border-radius:4px;
  color:#fff;font-weight:800;letter-spacing:.05em;background:%s}
h2{font-size:11px;letter-spacing:.11em;text-transform:uppercase;color:var(--fg2);
  margin:22px 0 8px;font-weight:700}
table{border-collapse:collapse;width:100%%;margin:.5rem 0 1rem;font-size:12.5px}
th,td{border:1px solid var(--line);padding:.42rem .6rem;text-align:left;vertical-align:top}
th{background:var(--panel2);color:var(--fg2);font-weight:650}
code{font-family:var(--mono);font-size:11.5px;color:var(--vs-lightblue)}
.passed{color:#50D267;font-weight:650}.failed{color:#FA3C32;font-weight:650}
.warning{color:#FFCB30;font-weight:650}
.blocked{color:#C586C0;font-weight:650}.skipped,.not_started{color:var(--fg3)}
.cancelled{color:#FA3C32;font-weight:650}
/* The lifecycle rail, the same shape as the console's stage rail: how far this
   run got, answerable without reading a table. */
.rail{display:flex;flex-wrap:wrap;gap:4px;margin:.6rem 0 1rem}
.rail .r{display:flex;flex-direction:column;align-items:center;gap:2px;
  min-width:5.4rem;padding:6px 5px 7px;border:1px solid var(--line);border-radius:4px;
  font-size:15px;background:var(--panel2);color:var(--fg3)}
.rail .r b{font-size:8.5px;font-weight:700;letter-spacing:.03em;color:var(--fg2);
  text-align:center;word-break:break-word;text-transform:uppercase}
.rail .passed{border-color:rgba(80,210,103,.5);color:#50D267;background:rgba(80,210,103,.09)}
.rail .passed b{color:#50D267}
.rail .failed{border-color:rgba(250,60,50,.5);color:#FA3C32;background:rgba(250,60,50,.09)}
.rail .failed b{color:#FA3C32}
.rail .warning{border-color:rgba(255,203,48,.5);color:#FFCB30;background:rgba(255,203,48,.09)}
.rail .warning b{color:#FFCB30}
.rail .blocked{border-color:rgba(197,134,192,.5);color:#C586C0;background:rgba(197,134,192,.09)}
.rail .blocked b{color:#C586C0}
.rail .not_started,.rail .skipped{opacity:.5}
</style>
<div class="page">
<div class="hd">
  <h1>Cluster Deployment &amp; Lifecycle E2E</h1>
  <div class="sub">%s</div>
</div>
<p><span class="v">%s</span> %s</p>
`, html.EscapeString(run.ID), consoleTokens, tone,
		html.EscapeString(run.ID), verdict, html.EscapeString(why))

	// The banner goes above everything, not in a footer. Somebody who reads only
	// the top of this page must not be able to mistake it for real evidence.
	if banner := simulatedBanner(run); banner != "" {
		fmt.Fprintf(out, `<p style="border:2px solid #8250df;background:#f3eefc;`+
			`color:#4c2889;padding:.7rem .9rem;border-radius:5px;font-weight:600">`+
			`SIMULATED RUN — %s</p>`, html.EscapeString(banner))
	}

	fmt.Fprintf(out, "<table><tr><th>Profile</th><td>%s</td><th>Provider</th><td>%s</td></tr>"+
		"<tr><th>Channel</th><td>%s</td><th>Cluster</th><td>%s</td></tr>"+
		"<tr><th>CLI</th><td>%s %s</td><th>Checksum</th><td><code>%s</code></td></tr>"+
		"<tr><th>Host</th><td>%s</td><th>Started</th><td>%s</td></tr></table>",
		html.EscapeString(run.Profile), html.EscapeString(string(run.Provider)),
		html.EscapeString(string(run.Channel)), html.EscapeString(run.Cluster),
		html.EscapeString(run.CLIVersion), short(run.CLICommit), short(run.CLIChecksum),
		html.EscapeString(run.Host), run.Started.Format("2006-01-02 15:04:05 MST"))

	// The lifecycle as a rail rather than only a table.
	//
	// A twenty-one row table answers "what happened to phase 14"; it does not
	// answer "how far did this get", which is the question somebody opening a
	// failed run actually has. The rail answers it without reading anything.
	fmt.Fprintf(out, "<h2>Lifecycle</h2><div class=\"rail\">")
	for _, phase := range run.Phases {
		mark := map[State]string{
			StatePassed: "●", StateWarning: "▲", StateFailed: "✕",
			StateBlocked: "⊘", StateSkipped: "–", StateCancelled: "■",
		}[phase.State]
		if mark == "" {
			mark = "·"
		}
		fmt.Fprintf(out, `<span class="r %s" title="%s — %s">%s<b>%s</b></span>`,
			phase.State, phase.ID, html.EscapeString(phase.Message), mark, phase.ID)
	}
	fmt.Fprintf(out, "</div>")

	fmt.Fprintf(out, "<h2>Phases</h2><table><tr><th>#</th><th>Phase</th><th>State</th>"+
		"<th>Took</th><th>Notes</th></tr>")
	for _, phase := range run.Phases {
		if phase.State == StateNotStarted {
			continue
		}
		fmt.Fprintf(out, `<tr><td>%d</td><td>%s</td><td class="%s">%s</td><td>%s</td><td>%s</td></tr>`,
			phase.Number, phase.ID, phase.State, phase.State, took(phase.Millis),
			html.EscapeString(phase.Message))
	}
	fmt.Fprintf(out, "</table>")

	// Environment, stated rather than inferred from the profile name. An
	// emulated result that reads like a real one is how a simulated pass gets
	// quoted as evidence that a provider works.
	emulated := ""
	if run.Infrastructure == InfraEmulated {
		emulated = " — results describe a simulated provider, not a real one"
	}
	fmt.Fprintf(out, "<h2>Environment</h2><table>"+
		"<tr><th>Provider</th><td>%s</td><th>Infrastructure</th><td>%s%s</td></tr>"+
		"<tr><th>Cluster</th><td>%s</td><th>Organisation</th><td>%s</td></tr>"+
		"<tr><th>Execution channel</th><td>%s</td><th>Host</th><td>%s (%s/%s)</td></tr>"+
		"</table>",
		html.EscapeString(string(run.Provider)),
		html.EscapeString(string(run.Infrastructure)), emulated,
		html.EscapeString(run.Cluster), html.EscapeString(run.Organisation),
		html.EscapeString(string(run.Channel)), html.EscapeString(run.Host),
		run.OS, run.Arch)

	findings := run.Findings()
	fmt.Fprintf(out, "<h2>Failures</h2>")
	if len(findings) == 0 {
		fmt.Fprintf(out, "<p>None.</p>")
	} else {
		fmt.Fprintf(out, "<table><tr><th>Command</th><th>Phase</th><th>Environment</th>"+
			"<th>Expected</th><th>Actual</th><th>Cause</th><th>Category</th><th>Regression</th></tr>")
		for _, f := range findings {
			fmt.Fprintf(out, "<tr><td><code>%s</code></td><td>%s</td><td>%s</td><td>%s</td>"+
				"<td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>",
				html.EscapeString(f.Command), f.Phase, html.EscapeString(f.Environment),
				html.EscapeString(f.Expected), html.EscapeString(f.Actual),
				html.EscapeString(f.Cause), f.Category, yesNo(f.Regression))
		}
		fmt.Fprintf(out, "</table>")
	}

	// Cleanup, last and always shown — including when nothing was created.
	//
	// "Created 0, removed 0, remaining 0" is worth printing: a reader who cannot
	// find the cleanup section does not conclude it was clean, they conclude
	// nobody checked.
	remaining := run.Remaining()
	fmt.Fprintf(out, "<h2>Cleanup</h2><table><tr><th>Created</th><td>%d</td>"+
		"<th>Removed</th><td>%d</td><th>Remaining</th><td class=\"%s\">%d</td></tr></table>",
		len(run.Resources), len(run.Resources)-len(remaining),
		map[bool]string{true: "failed", false: "passed"}[len(remaining) > 0],
		len(remaining))

	if len(remaining) > 0 {
		fmt.Fprintf(out, "<table><tr><th>Kind</th><th>Name</th><th>Provider</th>"+
			"<th>Remove it with</th></tr>")
		for _, resource := range remaining {
			fmt.Fprintf(out, "<tr><td>%s</td><td>%s</td><td>%s</td><td><code>%s</code></td></tr>",
				html.EscapeString(resource.Kind), html.EscapeString(resource.Name),
				html.EscapeString(resource.Provider),
				html.EscapeString(resource.Remediation))
		}
		fmt.Fprintf(out, "</table>")
	}
	fmt.Fprintf(out, "</div>")
	return out.String()
}

// JUnit, so a CI system can gate on this without parsing our own formats.
type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name  string      `xml:"name,attr"`
	Tests int         `xml:"tests,attr"`
	Cases []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name    string        `xml:"name,attr"`
	Time    string        `xml:"time,attr"`
	Failure *junitMessage `xml:"failure,omitempty"`
	Skipped *junitMessage `xml:"skipped,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

func junitReport(run *Run) []byte {
	suite := junitSuite{Name: "opencenter-e2e-" + run.Profile}
	suites := junitSuites{Name: "openCenter E2E"}

	for _, phase := range run.Phases {
		if phase.State == StateNotStarted {
			continue
		}
		testcase := junitCase{
			Name: fmt.Sprintf("%02d %s", phase.Number, phase.ID),
			Time: strconv.FormatFloat(float64(phase.Millis)/1000, 'f', 3, 64),
		}
		switch phase.State {
		case StateFailed, StateCancelled:
			testcase.Failure = &junitMessage{Message: phase.Message, Body: phase.Detail}
			suites.Failures++
		case StateSkipped, StateBlocked:
			testcase.Skipped = &junitMessage{Message: phase.Message}
			suites.Skipped++
		}
		suite.Cases = append(suite.Cases, testcase)
		suite.Tests++
		suites.Tests++
	}
	suites.Suites = []junitSuite{suite}

	encoded, err := xml.MarshalIndent(suites, "", "  ")
	if err != nil {
		return []byte(`<testsuites/>`)
	}
	return append([]byte(xml.Header), encoded...)
}

func writeCSV(path string, run *Run) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	if err := writer.Write([]string{"phase", "number", "state", "millis", "message",
		"profile", "provider", "channel", "cli_version", "cli_commit"}); err != nil {
		return err
	}
	for _, phase := range run.Phases {
		if phase.State == StateNotStarted {
			continue
		}
		if err := writer.Write([]string{
			string(phase.ID), strconv.Itoa(phase.Number), string(phase.State),
			strconv.FormatInt(phase.Millis, 10), phase.Message,
			run.Profile, string(run.Provider), string(run.Channel),
			run.CLIVersion, short(run.CLICommit),
		}); err != nil {
			return err
		}
	}
	return nil
}

func took(millis int64) string {
	if millis < 1000 {
		return fmt.Sprintf("%dms", millis)
	}
	seconds := millis / 1000
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
