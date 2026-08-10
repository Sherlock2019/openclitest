package gitopsupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Pull request creation, over the REST API from Go rather than by shelling out
// to `gh`.
//
// Two reasons, and neither is taste. `gh` is one more prerequisite on every
// machine and every runner that wants this stage. More importantly its output
// goes to a terminal this process does not control, and this stage's whole
// contract is that everything leaving it passes through the redactor first — a
// subprocess printing an API error containing a token would defeat that before
// anything could intercept it. An HTTP client keeps the bytes in this process.

// PullRequest is what the API returned, reduced to what is worth keeping.
//
// Only the number and the URL are stored. The full response carries the actor,
// the head repository and a good deal else that would end up in a report for no
// benefit.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	// Existing is set when the pull request was already open for this branch.
	// A re-run of the same run id is a normal thing to do, and finding the PR
	// that is already there is a better answer than failing on a duplicate.
	Existing bool `json:"-"`
}

// Client talks to GitHub.
type Client struct {
	// Base is the API root. A field so a test can point it at httptest.
	Base string
	// HTTP is the transport. Defaults to a client with its own timeout — never
	// http.DefaultClient, which has none and would let a hung API call outlive
	// the run.
	HTTP     *http.Client
	Redactor Redactor
}

// NewClient builds a client for a configuration. The token is read at call time
// from the environment and never stored on the struct, so a Client that ends up
// in a log or a panic message carries nothing.
func NewClient(config Config, redactor Redactor) *Client {
	base := strings.TrimSuffix(strings.TrimSpace(config.APIBase), "/")
	if base == "" {
		base = DefaultAPIBase
	}
	if redactor == nil {
		redactor = noRedactor{}
	}
	return &Client{
		Base:     base,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		Redactor: redactor,
	}
}

// Create opens the pull request, or finds the one that is already open for the
// same branch.
func (c *Client) Create(
	ctx context.Context, slug, head, base, title, body string,
) (PullRequest, error) {
	if slug == "" {
		return PullRequest{}, fmt.Errorf(
			"no GitHub repository to open a pull request in — " +
				"the branch was pushed, but the remote is not a GitHub owner/name")
	}
	token := strings.TrimSpace(os.Getenv(EnvToken))
	if token == "" {
		return PullRequest{}, fmt.Errorf(
			"no GitHub token: set %s to open a pull request, or set %s=false "+
				"to push the branch and open it by hand", EnvToken, EnvCreatePR)
	}
	c.Redactor.Add(token)

	payload, err := json.Marshal(map[string]any{
		"title": title, "head": head, "base": base, "body": body,
		// Never a draft. A draft pull request does not notify reviewers, and a
		// promotion nobody is told about is a promotion that sits for a week.
		"draft": false,
	})
	if err != nil {
		return PullRequest{}, err
	}

	response, raw, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/repos/%s/pulls", c.Base, slug), token, payload)
	if err != nil {
		return PullRequest{}, err
	}

	switch {
	case response == http.StatusCreated:
		var pr PullRequest
		if err := json.Unmarshal(raw, &pr); err != nil {
			return PullRequest{}, fmt.Errorf("the pull request was created but the response "+
				"did not parse: %w", err)
		}
		return pr, nil

	case response == http.StatusUnprocessableEntity &&
		bytes.Contains(bytes.ToLower(raw), []byte("already exists")):
		// Not an error. Look up the one that exists and report it.
		if existing, findErr := c.find(ctx, slug, head, base, token); findErr == nil {
			existing.Existing = true
			return existing, nil
		}
		return PullRequest{}, fmt.Errorf(
			"a pull request for %s already exists and could not be looked up", head)

	case response == http.StatusUnauthorized, response == http.StatusForbidden:
		return PullRequest{}, fmt.Errorf(
			"the GitOps token cannot open a pull request in %s — "+
				"it needs write access to contents and pull requests", slug)

	case response == http.StatusNotFound:
		return PullRequest{}, fmt.Errorf(
			"GitOps repository not found, or the token cannot see it: %s", slug)
	}
	return PullRequest{}, fmt.Errorf("GitHub refused the pull request (HTTP %d): %s",
		response, c.Redactor.String(firstLine(string(raw))))
}

// find looks up an open pull request for a branch.
func (c *Client) find(ctx context.Context, slug, head, base, token string) (PullRequest, error) {
	owner, _, _ := strings.Cut(slug, "/")
	url := fmt.Sprintf("%s/repos/%s/pulls?state=open&base=%s&head=%s:%s",
		c.Base, slug, base, owner, head)

	status, raw, err := c.do(ctx, http.MethodGet, url, token, nil)
	if err != nil {
		return PullRequest{}, err
	}
	if status != http.StatusOK {
		return PullRequest{}, fmt.Errorf("looking for the existing pull request: HTTP %d", status)
	}
	var list []PullRequest
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
		return PullRequest{}, fmt.Errorf("no open pull request found for %s", head)
	}
	return list[0], nil
}

// do performs one request. Every call goes through here so the Authorization
// header is set in exactly one place and can never be logged.
func (c *Client) do(
	ctx context.Context, method, url, token string, body []byte,
) (int, []byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequestWithContext(bounded, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "opencenter-cli-test-bench")

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		// The error can quote the URL, which for a misconfigured remote can
		// carry userinfo. Redacted rather than wrapped raw.
		return 0, nil, fmt.Errorf("%s", c.Redactor.String(err.Error()))
	}
	defer response.Body.Close()

	// Bounded. A body this large from the pull request API means something is
	// very wrong, and reading it into memory would make it worse.
	raw := make([]byte, 0, 4096)
	buffer := make([]byte, 4096)
	for len(raw) < 1<<20 {
		read, readErr := response.Body.Read(buffer)
		raw = append(raw, buffer[:read]...)
		if readErr != nil {
			break
		}
	}
	return response.StatusCode, raw, nil
}

// PullRequestTitle is the subject a reviewer sees in their list.
func PullRequestTitle(shortSHA string) string {
	return "Promote openCenter CLI Test Bench " + shortSHA
}

// PullRequestBody is the review material: what was tested, what changed, and an
// explicit statement that nothing was deployed.
//
// The last section is not decoration. A reviewer arriving at an automated pull
// request needs to know within one screen whether a robot has already changed
// production, and the answer is no.
func PullRequestBody(evidence Evidence, change ManifestChange, files []string) string {
	var body strings.Builder
	body.WriteString("## openCenter CLI Test Bench promotion\n\n")
	body.WriteString("| Field | Value |\n|---|---|\n")
	for _, row := range [][2]string{
		{"Test Bench run", evidence.RunID},
		{"Source commit", evidence.SourceCommit},
		{"CLI version", evidence.CLIVersion},
		{"Environment", evidence.Environment},
		{"Passed", fmt.Sprint(evidence.Passed)},
		{"Warnings", fmt.Sprint(evidence.Warnings)},
		{"Failed", fmt.Sprint(evidence.Failed)},
		{"Cleanup", evidence.CleanupStatus},
	} {
		if strings.TrimSpace(row[1]) == "" {
			continue
		}
		fmt.Fprintf(&body, "| %s | `%s` |\n", row[0], row[1])
	}
	if evidence.WorkflowRunURL != "" {
		fmt.Fprintf(&body, "| Workflow run | %s |\n", evidence.WorkflowRunURL)
	}

	body.WriteString("\n### Change\n\n")
	if change.Changed {
		fmt.Fprintf(&body, "- Updated the Test Bench deployment image to `%s`.\n", change.Image)
		if change.Previous != "" {
			fmt.Fprintf(&body, "  Previously `%s`.\n", change.Previous)
		}
	} else {
		body.WriteString("- The deployment image was already at the tested tag.\n")
	}
	body.WriteString("- Published the successful test evidence.\n")
	body.WriteString("- No deployment was performed by the Test Bench.\n")

	if len(files) > 0 {
		body.WriteString("\n<details><summary>Files changed</summary>\n\n")
		for _, file := range files {
			fmt.Fprintf(&body, "- `%s`\n", file)
		}
		body.WriteString("\n</details>\n")
	}

	body.WriteString("\n### Delivery\n\n")
	body.WriteString("After review and merge, Flux may reconcile the new desired state. ")
	body.WriteString("The Test Bench does not merge this pull request and does not deploy.\n")
	return body.String()
}
