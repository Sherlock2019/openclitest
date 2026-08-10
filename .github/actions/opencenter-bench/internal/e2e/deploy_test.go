package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Both of these were found by running the bench against real local Kubernetes,
// not by reading the code. They are here so the next change cannot quietly undo
// either.

// A deploy that timed out waiting for gitea somewhere else is not a defect in
// the build. Left to the generic timeout branch it read "deployment timed out —
// Product defect", which would block a release over a container an earlier run
// left running.
func TestAGiteaOnTheWrongPortIsNotAProductDefect(t *testing.T) {
	output := `  [3/8] → Attach local Gitea to the Kind network (gitea-attach-kind)...
  [3/8] ✗ failed after 1m52s: attach gitea to kind network: timed out waiting for gitea at https://localhost:3001
Error: provisioning infrastructure: step "gitea-attach-kind" failed`

	cause, category, fix := classifyDeployFailure(output, "https://localhost:3301")

	if category != CategoryEnvironment {
		t.Errorf("category is %q, want %q — a stale container is not the product's fault",
			category, CategoryEnvironment)
	}
	for _, want := range []string{"3001", "3301"} {
		if !strings.Contains(cause, want) {
			t.Errorf("the cause does not name port %s: %s", want, cause)
		}
	}
	// The cause must not guess at why. An earlier version blamed a container
	// from an earlier run; the ports were both free before the run and gitea
	// still came up on the higher one, so that was invented. A cause somebody
	// acts on has to stop at what was observed.
	for _, invented := range []string{"earlier run", "stale", "holding the old port"} {
		if strings.Contains(strings.ToLower(cause), invented) {
			t.Errorf("the cause guesses at a reason it did not observe (%q): %s",
				invented, cause)
		}
	}
	// `destroy`, not `down`. The subcommands are attach-kind, destroy, status
	// and up; this said `down`, which prints the help text and stops nothing, so
	// a reader who followed the remediation was left with the container still
	// running and the same failure on the next run.
	if !strings.Contains(fix, "gitea destroy") {
		t.Errorf("the remediation does not name a real subcommand: %s", fix)
	}
	if strings.Contains(fix, "gitea down") {
		t.Errorf("the remediation names `gitea down`, which does not exist: %s", fix)
	}
}

// Every remediation this package prints has to name a command that exists.
// `local gitea down` did not, and nothing caught it because nothing ran it.
//
// Matched on "local gitea <word>" rather than on "gitea" anywhere, so the
// prose around the command is not mistaken for part of it.
func TestNoRemediationNamesAGiteaSubcommandThatDoesNotExist(t *testing.T) {
	real := map[string]bool{
		"attach-kind": true, "destroy": true, "status": true, "up": true,
	}
	for _, text := range []string{
		classifyDeployFailureFix("timed out waiting for gitea at https://localhost:3001", ""),
		classifyDeployFailureFix("timed out waiting for gitea at https://localhost:3001",
			"https://localhost:3301"),
	} {
		for _, sub := range giteaSubcommandsIn(text) {
			if !real[sub] {
				t.Errorf("remediation names `local gitea %s`, which is not a "+
					"subcommand: %s", sub, text)
			}
		}
	}
}

// giteaSubcommandsIn returns the word after each "local gitea " in a string.
func giteaSubcommandsIn(text string) []string {
	const marker = "local gitea "
	var out []string
	for rest := text; ; {
		index := strings.Index(rest, marker)
		if index < 0 {
			return out
		}
		rest = rest[index+len(marker):]
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return out
		}
		out = append(out, strings.Trim(fields[0], "`,.;:"))
	}
}

func classifyDeployFailureFix(output, giteaURL string) string {
	_, _, fix := classifyDeployFailure(output, giteaURL)
	return fix
}

// Without a status to compare against, it is still an environment problem —
// the product was never given a cluster to fail on.
func TestAGiteaTimeoutWithNoStatusIsStillEnvironmental(t *testing.T) {
	_, category, _ := classifyDeployFailure(
		"timed out waiting for gitea at https://localhost:3001", "")
	if category != CategoryEnvironment {
		t.Errorf("category is %q, want %q", category, CategoryEnvironment)
	}
}

// The signature must not swallow every timeout. A deploy that times out with no
// mention of gitea is still the product's problem to answer for.
func TestAnOrdinaryTimeoutIsStillAProductDefect(t *testing.T) {
	_, category, _ := classifyDeployFailure(
		"Error: bootstrap timed out waiting for flux reconciliation", "")
	if category != CategoryProductDefect {
		t.Errorf("category is %q, want %q", category, CategoryProductDefect)
	}
}

func TestGiteaBaseURLIsReadFromStatus(t *testing.T) {
	status := "Runtime: docker\nRunning: true\nBase URL: https://localhost:3301\n" +
		"Repository URL: https://localhost:3301/newuser/test-repo.git\n"
	if got := giteaBaseURL(status); got != "https://localhost:3301" {
		t.Fatalf("giteaBaseURL = %q", got)
	}
	if got := giteaBaseURL("Running: false\n"); got != "" {
		t.Fatalf("giteaBaseURL invented %q from a status with no address", got)
	}
}

func TestWaitedForGiteaReadsTheAddressDeployGaveUpOn(t *testing.T) {
	got := waitedForGitea("attach gitea to kind network: timed out waiting for " +
		"gitea at https://localhost:3001\nError: provisioning infrastructure")
	if got != "https://localhost:3001" {
		t.Fatalf("waitedForGitea = %q", got)
	}
	if got := waitedForGitea("something else entirely"); got != "" {
		t.Fatalf("waitedForGitea invented %q", got)
	}
}

// A Kind run leaves config/local/gitea/ssh owned by root and mode 0700, because
// the container made it. The registry knew nothing about it, so cleanup said
// "nothing left behind" while the engineer was left with a directory they
// cannot delete — inside their own checkout, where it then breaks
// `go build ./...` for every later command.
func TestUnreadablePathsFindsWhatTheRunCannotRemove(t *testing.T) {
	root := t.TempDir()
	open := filepath.Join(root, "reports")
	if err := os.MkdirAll(open, 0o755); err != nil {
		t.Fatal(err)
	}
	shut := filepath.Join(root, "config", "local", "gitea", "ssh")
	if err := os.MkdirAll(shut, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shut, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restored so the temp directory can be cleaned up after the test.
	defer func() { _ = os.Chmod(shut, 0o755) }()

	if os.Geteuid() == 0 {
		t.Skip("root can enter a 0000 directory, so there is nothing to detect")
	}

	found := unreadablePaths(root)
	if len(found) != 1 || found[0] != shut {
		t.Fatalf("unreadablePaths = %v, want [%s]", found, shut)
	}

	// And a workspace nobody has locked reports nothing, or every run would end
	// in a false cleanup failure.
	if got := unreadablePaths(open); len(got) != 0 {
		t.Fatalf("a readable tree reported %v", got)
	}
	if got := unreadablePaths(""); got != nil {
		t.Fatalf("an empty root reported %v", got)
	}
}
