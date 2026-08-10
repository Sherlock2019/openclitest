package actionsetup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/gitopsupdate"
)

// Reading back what the bench found when GitHub ran it.
//
// The console can already show a local run in detail. What it could not show is
// the run that mattered — the one CI did, on somebody else's machine, against
// the commit that was actually pushed. That answer lives in two places on
// GitHub: the run list says whether it passed, and the uploaded artifact says
// which command failed. Neither is much use without the other, so this fetches
// both and joins them.
//
// Read-only throughout. Nothing here writes, pushes or approves anything.

// WorkflowFile is the workflow whose runs are listed. The same constant the
// installer writes, so the list can never drift onto a different workflow than
// the one this stage installed.
const WorkflowFile = "test-bench.yml"

// Run is one GitHub Actions run.
type Run struct {
	ID         int64     `json:"id"`
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	Commit     string    `json:"commit"`
	Branch     string    `json:"branch"`
	Actor      string    `json:"actor"`
	Started    time.Time `json:"started"`
	Seconds    int       `json:"seconds"`
	URL        string    `json:"url"`
}

// Outcome is how a run ended, in one word a person can scan.
func (r Run) Outcome() string {
	if r.Status != "completed" {
		return r.Status
	}
	if r.Conclusion == "" {
		return "unknown"
	}
	return r.Conclusion
}

// Failure is one command the bench found broken.
type Failure struct {
	Module string `json:"module"`
	Check  string `json:"check"`
	Name   string `json:"name"`
	// Assertions are the individual claims that did not hold. This is the part
	// somebody actually needs: "coverage-cluster-backup failed" is a label,
	// "--output json returned a sentence" is a bug report.
	Assertions []string `json:"assertions,omitempty"`
}

// Summary is the counts the bench recorded.
type Summary struct {
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`
	Warnings int `json:"warnings"`
	Blocked  int `json:"blocked"`
	Skipped  int `json:"skipped"`
}

// api performs an authenticated GET and returns the body.
//
// The token is read at call time and registered with the redactor before the
// request is built, never stored — the same rule the pull request client
// follows, for the same reason.
func api(ctx context.Context, config gitopsupdate.Config, redactor gitopsupdate.Redactor,
	url string, accept string) ([]byte, error) {
	token := strings.TrimSpace(os.Getenv(gitopsupdate.EnvToken))
	if token != "" && redactor != nil {
		redactor.Add(token)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return nil, err
	}

	switch response.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		// Without a token GitHub allows sixty requests an hour, and answers a
		// spent budget with the same 403 it uses for a token that cannot see
		// the repository. Reporting only the second sends somebody to check
		// permissions that were never the problem, so say which it is.
		if response.Header.Get("X-RateLimit-Remaining") == "0" {
			when := "shortly"
			if reset := response.Header.Get("X-RateLimit-Reset"); reset != "" {
				if seconds, err := strconv.ParseInt(reset, 10, 64); err == nil {
					when = "at " + time.Unix(seconds, 0).Format("15:04")
				}
			}
			if token == "" {
				return nil, fmt.Errorf("GitHub's hourly limit for requests without a "+
					"token is used up; it resets %s. Saving an Actions token in the "+
					"credentials panel raises the limit from 60 an hour to 5000", when)
			}
			return nil, fmt.Errorf("GitHub's hourly request limit is used up; it "+
				"resets %s", when)
		}
		return nil, fmt.Errorf("GitHub refused the request — the token needs read " +
			"access to Actions on this repository")
	case http.StatusNotFound:
		return nil, fmt.Errorf("not found: either the repository, the workflow " +
			WorkflowFile + ", or the token cannot see them")
	}
	return nil, fmt.Errorf("GitHub returned HTTP %d", response.StatusCode)
}

// ListRuns returns the most recent runs of the command bench's workflow.
//
// Kept as it was, so every existing caller keeps working. A caller that wants
// the lifecycle's runs asks ListRunsOf.
func ListRuns(ctx context.Context, config gitopsupdate.Config,
	redactor gitopsupdate.Redactor, limit int) ([]Run, error) {
	return ListRunsOf(ctx, config, redactor, limit, KindTestBench)
}

// ListRunsOf returns the most recent runs of one workflow, newest first.
//
// Scoped to a single workflow file rather than to the repository, so a
// repository running both workflows does not report one's green run as the
// other's — which would be a dashboard lying about which thing passed.
func ListRunsOf(ctx context.Context, config gitopsupdate.Config,
	redactor gitopsupdate.Redactor, limit int, kind Kind) ([]Run, error) {
	slug := gitopsupdate.Slug(config.Repository)
	if slug == "" {
		return nil, fmt.Errorf("%q is not a GitHub owner/name, so it has no Actions runs",
			gitopsupdate.StripCredentials(config.Repository))
	}
	if limit <= 0 {
		limit = 10
	}

	base := strings.TrimSuffix(strings.TrimSpace(config.APIBase), "/")
	if base == "" {
		base = gitopsupdate.DefaultAPIBase
	}
	url := fmt.Sprintf("%s/repos/%s/actions/workflows/%s/runs?per_page=%d",
		base, slug, kind.File(), limit)

	body, err := api(ctx, config, redactor, url, "")
	if err != nil {
		return nil, err
	}

	var payload struct {
		Runs []struct {
			ID         int64     `json:"id"`
			Number     int       `json:"run_number"`
			Name       string    `json:"display_title"`
			Status     string    `json:"status"`
			Conclusion string    `json:"conclusion"`
			SHA        string    `json:"head_sha"`
			Branch     string    `json:"head_branch"`
			URL        string    `json:"html_url"`
			Created    time.Time `json:"created_at"`
			Updated    time.Time `json:"updated_at"`
			Actor      struct {
				Login string `json:"login"`
			} `json:"actor"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("the run list did not parse: %w", err)
	}

	runs := make([]Run, 0, len(payload.Runs))
	for _, raw := range payload.Runs {
		seconds := 0
		if !raw.Updated.IsZero() && !raw.Created.IsZero() {
			seconds = int(raw.Updated.Sub(raw.Created).Seconds())
		}
		runs = append(runs, Run{
			ID: raw.ID, Number: raw.Number, Title: raw.Name,
			Status: raw.Status, Conclusion: raw.Conclusion,
			Commit: gitopsupdate.ShortSHA(raw.SHA), Branch: raw.Branch,
			Actor: raw.Actor.Login, Started: raw.Created,
			Seconds: seconds, URL: raw.URL,
		})
	}
	return runs, nil
}

// Failures downloads a run's evidence and reports which commands failed.
//
// The counts alone cannot answer "what broke", and the run log is thousands of
// lines of which four matter. The uploaded artifact carries report.json, which
// already has the answer in a structured form — so this reads that rather than
// scraping anything.
func Failures(ctx context.Context, config gitopsupdate.Config,
	redactor gitopsupdate.Redactor, runID int64) (Summary, []Failure, error) {
	slug := gitopsupdate.Slug(config.Repository)
	if slug == "" {
		return Summary{}, nil, fmt.Errorf("not a GitHub repository")
	}
	base := strings.TrimSuffix(strings.TrimSpace(config.APIBase), "/")
	if base == "" {
		base = gitopsupdate.DefaultAPIBase
	}

	listing, err := api(ctx, config, redactor,
		fmt.Sprintf("%s/repos/%s/actions/runs/%d/artifacts", base, slug, runID), "")
	if err != nil {
		return Summary{}, nil, err
	}
	var artifacts struct {
		Artifacts []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Expired     bool   `json:"expired"`
			SizeInBytes int64  `json:"size_in_bytes"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(listing, &artifacts); err != nil {
		return Summary{}, nil, fmt.Errorf("the artifact list did not parse: %w", err)
	}
	if len(artifacts.Artifacts) == 0 {
		return Summary{}, nil, fmt.Errorf(
			"run %d uploaded no evidence — it probably failed before the bench ran", runID)
	}

	chosen := artifacts.Artifacts[0]
	if chosen.Expired {
		return Summary{}, nil, fmt.Errorf(
			"the evidence for run %d has expired and can no longer be downloaded", runID)
	}

	zipped, err := api(ctx, config, redactor,
		fmt.Sprintf("%s/repos/%s/actions/artifacts/%d/zip", base, slug, chosen.ID),
		"application/vnd.github+json")
	if err != nil {
		return Summary{}, nil, err
	}

	archive, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		return Summary{}, nil, fmt.Errorf("the evidence archive did not open: %w", err)
	}

	for _, entry := range archive.File {
		if !strings.HasSuffix(entry.Name, "reports/report.json") {
			continue
		}
		handle, openErr := entry.Open()
		if openErr != nil {
			return Summary{}, nil, openErr
		}
		raw, readErr := io.ReadAll(io.LimitReader(handle, 32<<20))
		_ = handle.Close()
		if readErr != nil {
			return Summary{}, nil, readErr
		}
		return parseReport(raw)
	}
	return Summary{}, nil, fmt.Errorf(
		"the evidence for run %d contains no report.json", runID)
}

// parseReport pulls the counts and the failures out of a bench report.
func parseReport(raw []byte) (Summary, []Failure, error) {
	// The counts live at the top level of the document, beside `modules` — not
	// under a `summary` object. Both shapes are read because the action's own
	// parser does the same (`d.get("summary", d)`), and a reader that assumes
	// one silently reports every run as 0 passed, 0 failed, which looks like a
	// bench that tested nothing rather than a struct that looked in one place.
	type counts struct {
		Passed   int `json:"passed"`
		Failed   int `json:"failed"`
		Warnings int `json:"warning"`
		Blocked  int `json:"blocked"`
		Skipped  int `json:"skipped"`
	}
	var report struct {
		counts
		Summary *counts `json:"summary"`
		Modules []struct {
			ID      string `json:"id"`
			Results []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Assertions []struct {
					Name   string `json:"name"`
					Status string `json:"status"`
					Detail string `json:"detail"`
				} `json:"assertions"`
			} `json:"results"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return Summary{}, nil, fmt.Errorf("the report did not parse: %w", err)
	}

	found := report.counts
	if report.Summary != nil {
		found = *report.Summary
	}
	summary := Summary{
		Passed: found.Passed, Failed: found.Failed,
		Warnings: found.Warnings, Blocked: found.Blocked,
		Skipped: found.Skipped,
	}

	var failures []Failure
	for _, module := range report.Modules {
		for _, result := range module.Results {
			if !failed(result.Status) {
				continue
			}
			failure := Failure{
				Module: module.ID, Check: result.ID, Name: result.Name,
			}
			// Only the assertions that did not hold. A failing check can carry
			// thirty passing ones, and listing those buries the two that are
			// the actual finding.
			for _, assertion := range result.Assertions {
				if failed(assertion.Status) {
					text := assertion.Name
					if assertion.Detail != "" {
						text += " — " + assertion.Detail
					}
					failure.Assertions = append(failure.Assertions, text)
				}
			}
			failures = append(failures, failure)
		}
	}
	return summary, failures, nil
}

func failed(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "fail", "failed", "error":
		return true
	}
	return false
}
