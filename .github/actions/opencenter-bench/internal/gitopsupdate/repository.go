package gitopsupdate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Every git command this stage runs, and the rules it runs them under.
//
// Three rules, all of them load-bearing:
//
//  1. No credential ever appears in a command line. Not in the remote URL, not
//     in a flag, not in an environment variable git echoes back. The token goes
//     into a credential helper file inside the sandbox, mode 0600; the SSH key
//     goes into GIT_SSH_COMMAND pointing at a key file in the same place. A
//     process list is world-readable on most machines this will run on.
//
//  2. Every command has a deadline and dies with its children. A git fetch
//     against an unreachable host otherwise holds the console's run slot open
//     for the rest of the session.
//
//  3. Output is redacted before it is stored, and `git remote -v` is never run
//     raw — an HTTPS remote can carry userinfo, and printing it is how a token
//     ends up in a log that gets pasted into an issue.

// Redactor is the subset of internal/redact this package needs.
//
// An interface rather than the concrete type so the package has no dependency
// on the console, and so a test can assert that a value really was registered
// before a subprocess ran.
type Redactor interface {
	Add(values ...string)
	String(input string) string
}

// noRedactor is the fallback when a caller supplies none. It registers nothing
// and passes text through — which is correct only because every caller inside
// this repository does supply one, and a test that does not has no secrets.
type noRedactor struct{}

func (noRedactor) Add(...string)           {}
func (noRedactor) String(in string) string { return in }

// commandTimeout bounds one git invocation. Generous enough for a clone of a
// real cluster repository over a slow link, short enough that a hung network
// call is noticed within a coffee break.
const commandTimeout = 3 * time.Minute

// Repo is a GitOps checkout inside the run sandbox.
type Repo struct {
	// Dir is the working tree. Always inside the sandbox the caller supplied;
	// Open refuses anything else.
	Dir      string
	config   Config
	redactor Redactor
	// env is the environment every git command runs with, built once so the
	// credential helper and the SSH command cannot be forgotten on one call.
	env []string
	// log records what ran, redacted, for the evidence trail.
	log []string
}

// Open clones the configured repository into dir.
//
// dir must be inside sandboxRoot. That check is the reason this takes two paths
// rather than one: a manifest path or a repository URL that walked out of the
// sandbox would let the stage write to the developer's own checkout, and the
// cheapest place to refuse it is before the clone.
func Open(
	ctx context.Context, config Config, sandboxRoot, dir string, redactor Redactor,
) (*Repo, error) {
	if redactor == nil {
		redactor = noRedactor{}
	}
	if err := insideSandbox(sandboxRoot, dir); err != nil {
		return nil, err
	}

	repo := &Repo{Dir: dir, config: config, redactor: redactor}
	env, err := repo.buildEnv(sandboxRoot)
	if err != nil {
		return nil, err
	}
	repo.env = env

	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, fmt.Errorf("create checkout parent: %w", err)
	}

	// A shallow single-branch clone. The stage needs one branch and one commit
	// on top of it; fetching years of history to change one line is rude to
	// whoever is paying for the bandwidth.
	out, err := repo.run(ctx, filepath.Dir(dir), "clone",
		"--no-tags", "--depth", "1", "--single-branch",
		"--branch", config.BaseBranch, "--", config.CloneURL(), filepath.Base(dir))
	if err != nil {
		// The base branch not existing is the single most common
		// misconfiguration, and git's own wording for it is not obvious.
		if strings.Contains(out, "Remote branch") && strings.Contains(out, "not found") {
			return nil, fmt.Errorf("GitOps base branch %q does not exist in %s",
				config.BaseBranch, StripCredentials(config.Repository))
		}
		if strings.Contains(out, "not found") || strings.Contains(out, "does not exist") ||
			strings.Contains(out, "Repository not found") {
			return nil, fmt.Errorf("GitOps repository not found: %s",
				StripCredentials(config.Repository))
		}
		if strings.Contains(out, "Authentication failed") ||
			strings.Contains(out, "Permission denied") {
			return nil, fmt.Errorf("GitOps credential was rejected by %s",
				StripCredentials(config.Repository))
		}
		return nil, fmt.Errorf("clone %s: %s", StripCredentials(config.Repository), gitFailure(out))
	}

	// A checkout that already has changes in it is a checkout somebody else is
	// using, and committing on top of it would sweep their work into a pull
	// request about a test run.
	if status, _ := repo.Git(ctx, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		return nil, fmt.Errorf("the GitOps checkout is not clean before any change was made")
	}
	return repo, nil
}

// insideSandbox refuses a path that is not under root.
func insideSandbox(root, path string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("no sandbox root: refusing to check out anywhere")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(absRoot, absPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to check out at %q: it is outside the run sandbox", path)
	}
	return nil
}

// buildEnv assembles the environment for every git command, including the
// credential material — which is why it writes files rather than setting
// variables git will hand to a subprocess or print back.
func (r *Repo) buildEnv(sandboxRoot string) ([]string, error) {
	env := []string{
		"HOME=" + sandboxRoot,
		"PATH=" + os.Getenv("PATH"),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_AUTHOR_NAME=openCenter Test Bench",
		"GIT_AUTHOR_EMAIL=test-bench@opencenter.invalid",
		"GIT_COMMITTER_NAME=openCenter Test Bench",
		"GIT_COMMITTER_EMAIL=test-bench@opencenter.invalid",
		"LC_ALL=C",
		"NO_COLOR=1",
	}

	// The token, into a credential store file. Registered with the redactor
	// first, so even a git error quoting it back cannot print it.
	if token := strings.TrimSpace(os.Getenv(EnvToken)); token != "" {
		r.redactor.Add(token)
		path := filepath.Join(sandboxRoot, ".git-credentials")
		host := "github.com"
		if slug := Slug(r.config.Repository); slug == "" {
			// A non-GitHub HTTPS remote: take its host so the helper matches.
			if _, rest, ok := strings.Cut(r.config.CloneURL(), "://"); ok {
				if h, _, _ := strings.Cut(rest, "/"); h != "" {
					host = h
				}
			}
		}
		line := fmt.Sprintf("https://x-access-token:%s@%s\n", token, host)
		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			return nil, fmt.Errorf("write credential store: %w", err)
		}
		env = append(env, "GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=store --file="+path,
			"GIT_CONFIG_KEY_1=credential.useHttpPath", "GIT_CONFIG_VALUE_1=false")
	}

	// The SSH key. Accepts either a path to a key or the key itself, because
	// GitHub Actions secrets hold the material and a developer's machine holds
	// a path, and making them use the same field is worth the sniff.
	if key := strings.TrimSpace(os.Getenv(EnvSSHKey)); key != "" {
		path := key
		if strings.Contains(key, "PRIVATE KEY") {
			r.redactor.Add(key)
			path = filepath.Join(sandboxRoot, "gitops-key")
			body := key
			if !strings.HasSuffix(body, "\n") {
				body += "\n"
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				return nil, fmt.Errorf("write ssh key: %w", err)
			}
		}
		env = append(env, "GIT_SSH_COMMAND=ssh -i "+path+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o BatchMode=yes")
	}
	return env, nil
}

// Git runs one git command in the checkout and returns its combined, redacted
// output.
func (r *Repo) Git(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, r.Dir, args...)
}

func (r *Repo) run(ctx context.Context, dir string, args ...string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	command := exec.CommandContext(bounded, "git", args...)
	command.Dir = dir
	command.Env = r.env
	command.Stdin = nil
	// Without this a grandchild holding the pipes outlives the deadline and
	// Wait never returns — the same failure runShell hit, for the same reason.
	command.WaitDelay = 3 * time.Second

	var combined bytes.Buffer
	command.Stdout = &combined
	command.Stderr = &combined

	err := command.Run()
	out := r.redactor.String(combined.String())
	r.log = append(r.log, r.redactor.String("git "+strings.Join(args, " ")))

	if bounded.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("git %s timed out after %s", args[0], commandTimeout)
	}
	if err != nil {
		return out, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}

// Log is what ran, redacted, for the evidence trail. Arguments only — never
// output, which may be large and is stored separately where it matters.
func (r *Repo) Log() []string { return append([]string(nil), r.log...) }

// RemoteURL is the remote with any credential stripped.
//
// This exists so that nothing anywhere has to run `git remote -v` and hope. An
// HTTPS remote can carry userinfo; printing it raw is how a token reaches a log.
func (r *Repo) RemoteURL(ctx context.Context) string {
	out, err := r.Git(ctx, "config", "--get", "remote.origin.url")
	if err != nil {
		return StripCredentials(r.config.Repository)
	}
	return StripCredentials(strings.TrimSpace(out))
}

// --- branches -----------------------------------------------------------------

// branchUnsafe matches everything git will not accept in a ref name, plus a few
// it accepts and should not have to.
var branchUnsafe = regexp.MustCompile(`[^A-Za-z0-9._/-]+`)

// BranchName is the deterministic name for one run's update branch.
//
// Deterministic so a re-run of the same run id lands on the same branch rather
// than littering the repository with near-identical ones, and so the pull
// request can be found again from the run alone.
func BranchName(runID string) string {
	return "automation/opencenter-testbench-" + SanitiseSegment(runID)
}

// SanitiseSegment turns arbitrary text into something git will accept as one
// path component of a ref.
//
// git's rules are a list of prohibitions rather than an allowlist, so this
// inverts them: anything outside a small safe set becomes a dash, and the
// specific sequences git rejects outright are cleaned up afterwards.
func SanitiseSegment(value string) string {
	clean := branchUnsafe.ReplaceAllString(strings.TrimSpace(value), "-")
	clean = strings.ReplaceAll(clean, "..", "-")
	clean = strings.Trim(clean, "-./")
	for strings.Contains(clean, "--") {
		clean = strings.ReplaceAll(clean, "--", "-")
	}
	if clean == "" {
		return "run"
	}
	if strings.HasSuffix(clean, ".lock") {
		clean = strings.TrimSuffix(clean, ".lock") + "-lock"
	}
	if len(clean) > 100 {
		clean = strings.Trim(clean[:100], "-./")
	}
	return clean
}

// CreateBranch starts the update branch from the fetched base.
//
// Detached from origin/<base> rather than from whatever HEAD happens to be, so
// the branch is a change to the base branch and cannot accidentally carry
// something a previous step left behind.
func (r *Repo) CreateBranch(ctx context.Context, name string) error {
	if _, err := r.Git(ctx, "checkout", "--detach", "origin/"+r.config.BaseBranch); err != nil {
		// A shallow single-branch clone may not have the remote-tracking ref
		// under that name; the local branch is the same commit.
		if _, fallback := r.Git(ctx, "checkout", "--detach", r.config.BaseBranch); fallback != nil {
			return fmt.Errorf("base branch %q is not checked out: %w", r.config.BaseBranch, err)
		}
	}
	if out, err := r.Git(ctx, "checkout", "-B", name); err != nil {
		return fmt.Errorf("create branch %s: %s", name, gitFailure(out))
	}
	return nil
}

// Commit records the staged change. It returns the new commit's SHA.
//
// The message body carries run identity and nothing else — no token, no
// credential, no local path, no command output. See CommitMessage.
func (r *Repo) Commit(ctx context.Context, message string) (string, error) {
	if _, err := r.Git(ctx, "add", "--", "."); err != nil {
		return "", err
	}
	// --no-verify is not used. A GitOps repository's own hooks are exactly the
	// review this stage wants to pass, not one to skip.
	if out, err := r.Git(ctx, "commit", "-m", message); err != nil {
		return "", fmt.Errorf("commit: %s", gitFailure(out))
	}
	sha, err := r.Git(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(sha), err
}

// Push sends the branch to origin.
//
// No --force, ever. A deterministic branch name means a re-run of the same run
// id would collide, and the right answer to that is to fail and say so rather
// than to overwrite whatever was there.
func (r *Repo) Push(ctx context.Context, branch string) error {
	return r.push(ctx, branch, false)
}

// PushUpdating replaces a branch this tool owns.
//
// Re-running an install is ordinary — the workflow changed, or the first
// attempt stopped at the pull request for want of a token — and it failed every
// time with "branch already exists", leaving the operator with a stale branch
// they had to delete by hand before anything could proceed. That is not a
// safety property, it is a dead end.
//
// Only ever a branch in this tool's own namespace, whose name it chose, whose
// only content is the one file it may write. Somebody else's branch is still
// refused, because the caller decides which of these to use and no caller
// passes a name it did not generate.
func (r *Repo) PushUpdating(ctx context.Context, branch string) error {
	return r.push(ctx, branch, true)
}

func (r *Repo) push(ctx context.Context, branch string, update bool) error {
	args := []string{"push", "--set-upstream", "origin", branch}
	if update {
		// --force-with-lease would need the remote's tip, which we do not have
		// after a shallow clone of another branch. The lease this replaces is
		// the namespace: nothing but this tool writes here.
		args = []string{"push", "--force", "--set-upstream", "origin", branch}
	}
	out, err := r.Git(ctx, args...)
	if err != nil {
		if strings.Contains(out, "denied") || strings.Contains(out, "403") ||
			strings.Contains(out, "read-only") {
			return fmt.Errorf("the GitOps token has no write access to %s",
				StripCredentials(r.config.Repository))
		}
		if strings.Contains(out, "rejected") || strings.Contains(out, "already exists") {
			return fmt.Errorf("branch %s already exists on the remote — "+
				"a pull request for this run may already be open", branch)
		}
		return fmt.Errorf("push %s: %s", branch, gitFailure(out))
	}
	return nil
}

// RemoteHasBranch is the verify step's question, asked of the remote rather
// than of the local ref, because a push that appeared to work and a branch that
// exists are two different claims.
func (r *Repo) RemoteHasBranch(ctx context.Context, branch string) bool {
	out, err := r.Git(ctx, "ls-remote", "--heads", "origin", branch)
	return err == nil && strings.Contains(out, "refs/heads/"+branch)
}

func firstLine(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "no output"
	}
	if index := strings.IndexByte(trimmed, '\n'); index >= 0 {
		return trimmed[:index]
	}
	return trimmed
}

// gitFailure picks the line of git's output that says why it failed.
//
// Not the first line, which is what this used to report and is almost never the
// reason: git opens a clone with "Cloning into 'gitops'..." and a push with
// "To github.com:owner/name", both progress, and the actual cause arrives
// several lines later. A CI failure that says "clone: Cloning into 'gitops'..."
// tells whoever is paged exactly nothing, and the recognised cases above cannot
// cover DNS failure, a proxy, a TLS error or a timeout.
//
// So: the last line git prefixed with fatal: or error:, since git prints the
// proximate cause last; failing that, the last line that carries any content.
func gitFailure(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")

	best := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lowered := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lowered, "fatal:"),
			strings.HasPrefix(lowered, "error:"),
			strings.HasPrefix(lowered, "remote: error"),
			strings.HasPrefix(lowered, "ssh:"),
			strings.HasPrefix(lowered, "git:"):
			best = trimmed
		}
	}
	if best != "" {
		return best
	}

	// No labelled error: fall back to the last line with content, skipping the
	// progress frames git writes with a carriage return.
	for index := len(lines) - 1; index >= 0; index-- {
		trimmed := strings.TrimSpace(lines[index])
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "\r") {
			parts := strings.Split(trimmed, "\r")
			trimmed = strings.TrimSpace(parts[len(parts)-1])
		}
		if trimmed != "" {
			return trimmed
		}
	}
	return "no output"
}

// CommitMessage is the message the update commit carries.
//
// A subject a reviewer can scan and a body that traces the change back to a
// run. What is absent matters as much: no token, no credential, no local
// filesystem path, no cloud project id, no command output. Anything that would
// let this commit leak the machine it was made on is left out by construction
// rather than removed afterwards.
func CommitMessage(evidence Evidence, shortSHA string) string {
	var body strings.Builder
	fmt.Fprintf(&body, "chore(gitops): promote openCenter test bench %s\n\n", shortSHA)
	for _, line := range [][2]string{
		{"Test Bench run", evidence.RunID},
		{"Source commit", evidence.SourceCommit},
		{"CLI version", evidence.CLIVersion},
		{"Environment", evidence.Environment},
		{"Result", evidence.Status},
		{"Passed", fmt.Sprint(evidence.Passed)},
		{"Warnings", fmt.Sprint(evidence.Warnings)},
		{"Failed", fmt.Sprint(evidence.Failed)},
		{"Cleanup", evidence.CleanupStatus},
	} {
		if strings.TrimSpace(line[1]) == "" {
			continue
		}
		fmt.Fprintf(&body, "%s: %s\n", line[0], line[1])
	}
	return body.String()
}

// ShortSHA is the seven-character form used in the subject and the image tag.
func ShortSHA(sha string) string {
	clean := strings.TrimSpace(sha)
	if len(clean) > 7 {
		return clean[:7]
	}
	return clean
}
