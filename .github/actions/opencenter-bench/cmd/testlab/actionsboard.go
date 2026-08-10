package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/actionsetup"
	"github.com/opencenter-cloud/opencli-testbench/internal/gitopsupdate"
)

// What GitHub found, on the same board as what this machine found.
//
// The two answers belong together. A developer asking "can I ship this" gets a
// different answer from a laptop that has run eleven commands than from a CI
// run that has run all of them on a clean machine, and until now the second
// answer lived behind a button in another panel. The board states both and
// says which is which.

// actionsBoard is the shape the page reads.
type actionsBoard struct {
	// Configured is false when no repository has been connected yet. The page
	// draws nothing at all in that case rather than an error: not having
	// connected a repository is a normal state, not a fault.
	Configured bool              `json:"configured"`
	Repository string            `json:"repository,omitempty"`
	Runs       []actionsBoardRun `json:"runs,omitempty"`
	// E2ERuns are the lifecycle workflow's runs. A separate list, not merged:
	// the two benches answer different questions, and a summary that showed
	// one CI verdict could not say which of them it belonged to.
	E2ERuns []actionsBoardRun `json:"e2e_runs,omitempty"`
	// Error is reported rather than swallowed. A board that silently shows no
	// runs when the token expired is worse than one that says so.
	Error string `json:"error,omitempty"`
}

// actionsBoardRun is one CI run as the page draws it.
//
// Failures carries the commands that broke, read back from the annotations the
// action wrote. Without them a red row can only offer a link, which means the
// answer to "what failed" lives on another site — so the row says nothing that
// the colour had not already said.
type actionsBoardRun struct {
	Number   int                      `json:"number"`
	Title    string                   `json:"title"`
	Outcome  string                   `json:"outcome"`
	Commit   string                   `json:"commit"`
	Branch   string                   `json:"branch"`
	Age      string                   `json:"age"`
	Duration string                   `json:"duration"`
	URL      string                   `json:"url"`
	Failures []actionsetup.Annotation `json:"failures,omitempty"`
}

// tokenOnce copies a saved token into the environment that actionsetup reads.
//
// actionsetup.api takes its token from the process environment, which is right
// for a CLI invoked with one exported. The console holds it in the credentials
// file instead, so it has to be put where that code looks. Done once, and only
// when nothing has already set it, so an explicitly exported token still wins.
var tokenOnce sync.Once

func (c *console) actionsCredentials() (repository string, ok bool) {
	saved := c.savedCredentials()

	repository = strings.TrimSpace(saved["OPENCLI_ACTIONS_REPOSITORY"])
	if repository == "" {
		repository = strings.TrimSpace(os.Getenv("OPENCLI_ACTIONS_REPOSITORY"))
	}
	if repository == "" {
		return "", false
	}

	tokenOnce.Do(func() {
		if strings.TrimSpace(os.Getenv(gitopsupdate.EnvToken)) != "" {
			return
		}
		if token := strings.TrimSpace(saved["OPENCLI_ACTIONS_TOKEN"]); token != "" {
			_ = os.Setenv(gitopsupdate.EnvToken, token)
			return
		}
		// Failing that, whatever `gh` is already logged in with.
		//
		// An SSH key authenticates git and cannot authenticate the REST API —
		// there is no SSH transport for it — so a repository reached perfectly
		// well over SSH still reads as "not found" when the API is asked
		// anonymously and the hourly budget of sixty is spent. Anybody with gh
		// installed has already answered this question once; asking them to
		// mint a second credential for the same account is asking twice.
		//
		// Read, never stored: it stays in this process's environment and never
		// reaches the credentials file, so nothing here can leak a token the
		// operator did not choose to save.
		if token := ghToken(); token != "" {
			_ = os.Setenv(gitopsupdate.EnvToken, token)
		}
	})
	return repository, true
}

// ghToken asks the GitHub CLI for the token it is already using.
//
// Short deadline and no error surfaced: gh may be absent, logged out, or slow,
// and none of those is a fault of this console — the board simply falls back to
// anonymous requests, which work on a public repository until the hourly limit
// is spent.
func ghToken() string {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	token := strings.TrimSpace(string(out))
	// A token, not a message. gh prints advice to stdout in some states, and
	// sending a sentence to GitHub as a bearer token turns a missing login into
	// an authentication error, which reads as a broken repository.
	if strings.ContainsAny(token, " \t\n") || len(token) < 20 {
		return ""
	}
	return token
}

func (c *console) handleActionsBoard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	board := actionsBoard{}
	repository, ok := c.actionsCredentials()
	if !ok {
		_ = json.NewEncoder(w).Encode(board)
		return
	}
	board.Configured = true
	board.Repository = gitopsupdate.StripCredentials(repository)

	// Short. This runs on every page render, and a board that blocks for a
	// minute because GitHub is slow is a board nobody keeps open.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	config := gitopsupdate.Config{Repository: repository}
	runs, err := actionsetup.ListRuns(ctx, config, c.redactor, 5)
	if err != nil {
		// Reported, not fatal.
		//
		// A repository can carry one workflow and not the other — installing
		// the lifecycle alone is a normal thing to do, and this bench now sends
		// the kind so that is exactly what happens. Returning here on the
		// command bench's 404 blanked the lifecycle half as well, so a console
		// with a perfectly good set of lifecycle runs showed nothing at all and
		// blamed the token.
		board.Error = c.redactor.String(err.Error())
	}

	asked := 0
	for _, run := range runs {
		entry := actionsBoardRun{
			Number:   run.Number,
			Title:    run.Title,
			Outcome:  run.Outcome(),
			Commit:   shortCommit(run.Commit),
			Branch:   run.Branch,
			Duration: durationWords(run.Seconds),
			URL:      run.URL,
		}
		if !run.Started.IsZero() {
			entry.Age = ageWords(time.Since(run.Started))
		}
		// Only a failed run has annotations, and each one costs two round
		// trips. Asking about the green ones would double the wait to learn
		// nothing.
		// The newest few only. Each uncached run costs two or more requests,
		// and anonymously GitHub allows sixty an hour — a board that spends
		// them on the tenth-oldest row cannot draw the newest one.
		//
		// Green runs are asked too: a lifecycle run ending in WARNING exits 0,
		// so GitHub calls it a success while it carries every finding it made.
		if entry.Outcome != "" && entry.Outcome != "cancelled" && asked < 3 {
			asked++
			failures, err := actionsetup.RunFailures(ctx,
				gitopsupdate.Config{Repository: repository}, c.redactor, run.ID, 12)
			if err == nil {
				entry.Failures = failures
			}
		}
		board.Runs = append(board.Runs, entry)
	}

	// The lifecycle's own workflow. Failing to read it is not an error on the
	// board: a repository that runs the command bench and not the lifecycle is
	// a normal thing to be, and reporting it as a fault would make every such
	// board red.
	e2eRuns, e2eErr := actionsetup.ListRunsOf(ctx, config, c.redactor, 3,
		actionsetup.KindE2E)
	if e2eErr == nil {
		asked = 0
		for _, run := range e2eRuns {
			entry := actionsBoardRun{
				Number:   run.Number,
				Title:    run.Title,
				Outcome:  run.Outcome(),
				Commit:   shortCommit(run.Commit),
				Branch:   run.Branch,
				Duration: durationWords(run.Seconds),
				URL:      run.URL,
			}
			if !run.Started.IsZero() {
				entry.Age = ageWords(time.Since(run.Started))
			}
			if entry.Outcome != "" && entry.Outcome != "cancelled" && asked < 2 {
				asked++
				if failures, err := actionsetup.RunFailures(ctx, config,
					c.redactor, run.ID, 12); err == nil {
					entry.Failures = failures
				}
			}
			board.E2ERuns = append(board.E2ERuns, entry)
		}
	}
	_ = json.NewEncoder(w).Encode(board)
}

func shortCommit(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func durationWords(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	if seconds < 60 {
		return strconv.Itoa(seconds) + "s"
	}
	return strconv.Itoa(seconds/60) + "m " + strconv.Itoa(seconds%60) + "s"
}

func ageWords(since time.Duration) string {
	switch {
	case since < time.Minute:
		return "just now"
	case since < time.Hour:
		return strconv.Itoa(int(since.Minutes())) + " min ago"
	case since < 24*time.Hour:
		return strconv.Itoa(int(since.Hours())) + "h ago"
	}
	return strconv.Itoa(int(since.Hours()/24)) + "d ago"
}
