package actionsetup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/gitopsupdate"
)

// Making a repository's pipeline run, on purpose.
//
// The end-to-end check nothing else performs: push a commit to the configured
// repository and let GitHub run the bench against it. Installing the workflow
// proves the file is there; this proves the file works.
//
// An empty commit. The point is to trigger the pipeline, not to change
// anybody's code — a commit that adds a marker file leaves litter somebody has
// to clean up later, and `git commit --allow-empty` is the honest way to say
// "run CI against this tree".

// TriggerBranch is where the commit lands.
//
// A branch of its own, not the default branch. The installed workflow has no
// `branches:` filter on push, so a branch triggers it exactly as main would —
// and pushing a commit to somebody's main to see whether CI works is a cost
// their history carries forever for a question answered in two minutes.
const TriggerBranch = "automation/test-bench-trigger"

// TriggerMarkerPath is the one file a trigger commit touches.
//
// Outside .github, deliberately: the workflows ignore pushes that only change
// files under .github/workflows, and a marker living there would be ignored
// along with them.
const TriggerMarkerPath = ".opencenter-test-bench-trigger"

// E2ETriggerMarkerPath is the lifecycle's own marker.
//
// One file for both kinds meant pressing either button started both workflows:
// the branch was already per-kind, but the file was not, and a workflow decides
// whether to run from the paths a push touched — not from its branch. So each
// workflow now ignores the other's marker, and a trigger commit that touches
// only the other's is a push that changed nothing it cares about.
const E2ETriggerMarkerPath = ".opencenter-e2e-trigger"

// TriggerMarkerPathFor is the marker one kind writes.
func TriggerMarkerPathFor(kind Kind) string {
	if kind.resolved() == KindE2E {
		return E2ETriggerMarkerPath
	}
	return TriggerMarkerPath
}

// otherTriggerMarker is the marker this kind must ignore.
func otherTriggerMarker(kind Kind) string {
	if kind.resolved() == KindE2E {
		return TriggerMarkerPath
	}
	return E2ETriggerMarkerPath
}

// triggerMarker is the file's contents — an explanation, not a timestamp on its
// own. Somebody finding this in their repository should not have to work out
// what put it there.
func triggerMarker(stamp string) string {
	return "# openCenter test bench — trigger marker\n" +
		"#\n" +
		"# This file exists so that pressing \"Run GitHub Action test\" produces a\n" +
		"# commit that changes something. The workflows ignore pushes that only\n" +
		"# touch .github/workflows, so a commit changing nothing at all would be\n" +
		"# skipped and no run would start.\n" +
		"#\n" +
		"# It lives on " + TriggerBranch + " and is safe to delete with that branch.\n" +
		"# Nothing reads it.\n" +
		"\n" +
		"last-triggered: " + stamp + "\n"
}

// TriggerBranchFor is the branch for one kind.
//
// The kind was honoured for the workflow path and the install branch and not
// here, so pressing "run" with --kind=opencenter-e2e pushed onto the command
// bench's trigger branch. Two buttons writing to one branch means the second
// press moves the first one's commit, and a reader looking at that branch
// cannot tell which button made it.
func TriggerBranchFor(kind Kind) string {
	if kind.resolved() == KindE2E {
		return "automation/opencenter-e2e-trigger"
	}
	return TriggerBranch
}

// PreviousTrigger is what was already on the branch when this button was
// pressed — the commit a previous press left there.
type PreviousTrigger struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
	When    string `json:"when,omitempty"`
	Age     string `json:"age,omitempty"`
	// Ours distinguishes a commit this button made from anything else that
	// found its way onto the branch.
	Ours bool `json:"ours"`
}

// TriggerResult is what happened.
type TriggerResult struct {
	Status     Status `json:"status"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit,omitempty"`
	ActionsURL string `json:"actionsUrl,omitempty"`
	Message    string `json:"message"`
	// Warning is said before the result, not instead of it. See triggerWarning.
	Warning  string           `json:"warning,omitempty"`
	Previous *PreviousTrigger `json:"previous,omitempty"`
	Steps    []Step           `json:"steps,omitempty"`
}

// TriggerSteps are the phases, in order.
var TriggerSteps = []string{
	StepPreflight, StepCheckout, StepBranch, StepCommit, StepPush, StepVerify,
}

func (r *TriggerResult) set(id string, status StepStatus, detail string) {
	for index := range r.Steps {
		if r.Steps[index].ID == id {
			r.Steps[index].Status = status
			r.Steps[index].Detail = detail
			return
		}
	}
}

func (r *TriggerResult) skipRest(detail string) {
	for index := range r.Steps {
		if r.Steps[index].Status == StepPending {
			r.Steps[index].Status = StepSkipped
			r.Steps[index].Detail = detail
		}
	}
}

func (r *TriggerResult) fail(id, reason string) *TriggerResult {
	r.set(id, StepFailed, reason)
	r.skipRest("an earlier step failed")
	r.Status = StatusFailed
	r.Message = reason
	return r
}

// Headline is the one line a console prints above the steps.
func (r *TriggerResult) Headline() string {
	switch r.Status {
	case StatusPushed, StatusPRCreated:
		return "PIPELINE TRIGGERED"
	case StatusBlocked:
		return "TRIGGER — BLOCKED"
	case StatusFailed:
		return "TRIGGER — FAILED"
	}
	return "TRIGGER"
}

// ExitCode maps the result onto a process exit status.
func (r *TriggerResult) ExitCode() int {
	switch r.Status {
	case StatusPushed, StatusPRCreated:
		return ExitOK
	case StatusBlocked:
		return ExitApprovalMissing
	}
	return ExitGitFailed
}

// Trigger pushes an empty commit so the repository's pipeline runs.
func Trigger(ctx context.Context, request Request) *TriggerResult {
	result := &TriggerResult{Status: StatusFailed, Message: "not started"}
	for _, id := range TriggerSteps {
		result.Steps = append(result.Steps, Step{ID: id, Status: StepPending})
	}

	config := request.Config
	if request.Redactor == nil {
		request.Redactor = noRedactor{}
	}
	result.Repository = gitopsupdate.StripCredentials(config.Repository)
	// The kind's branch, not the constant. Resolved once here so no later
	// step can use the other button's.
	branch := TriggerBranchFor(request.Options.Kind)
	result.Branch = branch

	// ---- preflight ----------------------------------------------------------
	if !config.Configured() {
		return result.fail(StepPreflight,
			"no repository to trigger — give the owner/name of the repository whose "+
				"pipeline should run")
	}
	// Both gates, as for any other remote write. A commit is a commit even when
	// it is empty: it lands in somebody's history and starts a job that costs
	// them minutes of CI time.
	if permitted, why := request.Approval.Permits(); !permitted {
		result.set(StepPreflight, StepOK, "")
		result.skipRest("not approved")
		result.Status = StatusBlocked
		result.Message = why
		return result
	}
	result.set(StepPreflight, StepOK,
		fmt.Sprintf("%s → %s", result.Repository, branch))

	// ---- checkout -----------------------------------------------------------
	checkout := filepath.Join(request.SandboxRoot, "trigger")
	repo, err := gitopsupdate.Open(ctx, config, request.SandboxRoot, checkout, request.Redactor)
	if err != nil {
		return result.fail(StepCheckout, err.Error())
	}
	result.set(StepCheckout, StepOK, "cloned "+config.BaseBranch)

	// ---- branch -------------------------------------------------------------
	//
	// Reused if it already exists. A trigger branch is scratch space; a new one
	// per run would leave a repository full of them.
	//
	// Reused means anchored to what the remote already has. The second press of
	// this button used to fail here: the branch was rebuilt from the base branch
	// every time, so its commit was not a descendant of the one pushed last
	// time, and git rejected the push as the non-fast-forward it was.
	if err := repo.CreateBranch(ctx, branch); err != nil {
		return result.fail(StepBranch, err.Error())
	}
	parent, reused := remoteTip(ctx, repo, branch)
	if reused {
		result.Previous = describeTip(ctx, repo, parent)
		result.Warning = triggerWarning(result.Previous)
		result.set(StepBranch, StepOK, branch+" — already on the remote, committing onto it")
	} else {
		result.set(StepBranch, StepOK, branch+" — new")
	}

	// ---- commit -------------------------------------------------------------
	stamp := time.Now().UTC().Format(time.RFC3339)
	message := "ci: trigger the openCenter Test Bench (" + stamp + ")\n\n" +
		"Touches one marker file and nothing else. It exists to make the\n" +
		"pipeline run against the current HEAD.\n"

	// A marker file, not an empty commit.
	//
	// An empty commit was the tidier idea and it stopped working the moment the
	// workflows learned to ignore changes that are only to workflow files. A
	// path filter asks "did this push change a file I care about", and a push
	// that changed no file at all matches nothing — so GitHub skips it, the
	// button goes quiet, and no run appears.
	//
	// One line in one file at a known path is the cost of keeping both: the
	// filter can ignore workflow edits, and this still trips it. The file says
	// what it is, so nobody has to guess why it is in their repository.
	marker := filepath.Join(checkout, TriggerMarkerPathFor(request.Options.Kind))
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return result.fail(StepCommit, err.Error())
	}
	if err := os.WriteFile(marker, []byte(triggerMarker(stamp)), 0o644); err != nil {
		return result.fail(StepCommit, err.Error())
	}

	var sha string
	var commitErr error
	if reused {
		sha, commitErr = commitOnto(ctx, repo, branch, parent, message,
			TriggerMarkerPathFor(request.Options.Kind))
	} else {
		sha, commitErr = repo.Commit(ctx, message)
	}
	if commitErr != nil {
		return result.fail(StepCommit, commitErr.Error())
	}
	result.Commit = gitopsupdate.ShortSHA(sha)
	result.set(StepCommit, StepOK, result.Commit)

	// ---- push ---------------------------------------------------------------
	if err := repo.Push(ctx, branch); err != nil {
		return result.fail(StepPush, explainTriggerPushFailure(err))
	}
	result.set(StepPush, StepOK, "pushed "+branch)

	// ---- verify -------------------------------------------------------------
	if !repo.RemoteHasBranch(ctx, branch) {
		return result.fail(StepVerify, "the branch is not on the remote after a successful push")
	}
	result.set(StepVerify, StepOK, "branch is on the remote")

	if slug := gitopsupdate.Slug(config.Repository); slug != "" {
		result.ActionsURL = "https://github.com/" + slug + "/actions"
	}
	result.Status = StatusPushed
	result.Message = fmt.Sprintf(
		"Pushed %s to %s on %s. GitHub should start the pipeline within a few "+
			"seconds — press \"Show run results\" to see what it found.",
		result.Commit, result.Repository, branch)
	return result
}

// commitIdentity is who the trigger commit is by.
//
// Its own identity rather than the repository's config: the sandbox has no user
// configured, and a machine with no global git identity would otherwise fail
// with a message about user.email that has nothing to do with what was asked.
var commitIdentity = []string{
	"-c", "user.name=openCenter Test Bench",
	"-c", "user.email=test-bench@opencenter.local",
}

func gitArgs(rest ...string) []string {
	return append(append([]string{}, commitIdentity...), rest...)
}

// remoteTip is the commit the trigger branch points at on the remote, if it is
// there at all.
//
// It has to be asked for by name. The clone is shallow and single-branch, so
// the trigger branch is absent from it even when the remote has had one for
// months, and asking the clone alone would answer "new" every time.
func remoteTip(ctx context.Context, repo *gitopsupdate.Repo, branch string) (string, bool) {
	if !repo.RemoteHasBranch(ctx, branch) {
		return "", false
	}
	ref := "refs/remotes/origin/" + branch
	if _, err := repo.Git(ctx, "fetch", "--depth", "1", "origin",
		"refs/heads/"+branch+":"+ref); err != nil {
		return "", false
	}
	sha, err := repo.Git(ctx, "rev-parse", ref)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(sha), true
}

// triggerSubject is how a commit from this button is recognised later.
const triggerSubject = "ci: trigger the openCenter Test Bench"

// describeTip reads what is already sitting on the trigger branch.
func describeTip(ctx context.Context, repo *gitopsupdate.Repo, sha string) *PreviousTrigger {
	out, err := repo.Git(ctx, "show", "-s", "--format=%s%n%cI", sha)
	if err != nil {
		return nil
	}
	lines := strings.SplitN(strings.TrimSpace(out), "\n", 2)
	previous := &PreviousTrigger{
		Commit:  gitopsupdate.ShortSHA(sha),
		Subject: strings.TrimSpace(lines[0]),
	}
	previous.Ours = strings.HasPrefix(previous.Subject, triggerSubject)
	if len(lines) > 1 {
		previous.When = strings.TrimSpace(lines[1])
		if at, err := time.Parse(time.RFC3339, previous.When); err == nil {
			previous.Age = humanAge(time.Since(at))
		}
	}
	return previous
}

func humanAge(since time.Duration) string {
	switch {
	case since < time.Minute:
		return "less than a minute ago"
	case since < 2*time.Minute:
		return "a minute ago"
	case since < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(since.Minutes()))
	case since < 2*time.Hour:
		return "an hour ago"
	case since < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(since.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(since.Hours()/24))
}

// triggerWarning is the guardrail: what pressing this again costs.
//
// Not a refusal, and not a confirmation prompt. Pressing twice is a reasonable
// thing to want — the first run may have been cancelled, or run against code
// that has since changed. What is not reasonable is not being told the price,
// which is specific and easy to miss: the workflow this bench installs sets
//
//	concurrency: { group: test-bench-<ref>, cancel-in-progress: true }
//
// Both commits land on the same ref, so a second push cancels the run still
// going for the first. Somebody watching that run sees it die and reasonably
// concludes CI is broken, when what happened is that they pressed the button
// again while waiting.
func triggerWarning(previous *PreviousTrigger) string {
	if previous == nil {
		return ""
	}
	if !previous.Ours {
		return "the tip of " + TriggerBranch + " is not a commit from this button: " +
			previous.Commit + " " + previous.Subject + ". This branch is scratch " +
			"space owned by the Test Bench. The new commit goes on top; nothing " +
			"already there is changed or lost."
	}
	when := previous.Age
	if when == "" {
		when = "earlier"
	}
	return "this branch already carries a trigger from " + when + " (" + previous.Commit +
		"). The installed workflow cancels an in-progress run when a new commit " +
		"arrives on the same branch, so if that run has not finished, this push " +
		"ends it. The results that come back will be for the new commit only."
}

// commitEmpty records an empty commit on the checked-out branch. The first
// trigger, when the remote has no branch to build on.
func commitEmpty(ctx context.Context, repo *gitopsupdate.Repo, message string) (string, error) {
	if out, err := repo.Git(ctx, gitArgs("commit", "--allow-empty", "-m", message)...); err != nil {
		return "", fmt.Errorf("%s", firstLine(out))
	}
	sha, err := repo.Git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// commitOnto records a commit carrying the checked-out tree, with parent as its
// only parent, and moves branch to it.
//
// `git commit` cannot express this. It commits on top of HEAD, and the two
// properties wanted here belong to two different commits: the tree of the base
// branch, so the pipeline tests current main rather than main as it stood at
// the previous trigger, and a parent the remote already has, so the push is an
// ordinary fast-forward instead of a rejection. commit-tree takes both.
//
// The tree carries the marker file, so the commit changes something the
// workflows' path filter will not ignore.
func commitOnto(ctx context.Context, repo *gitopsupdate.Repo, branch, parent, message, marker string) (string, error) {
	// The index, not HEAD's tree.
	//
	// This read `HEAD^{tree}` — the tree as last committed — so the marker file
	// written moments earlier, which is only on disk, was never in it. The
	// commit went out carrying nothing of its own, the workflows' path filter
	// saw a change to workflow files and nothing else, and skipped both runs.
	// Pressing the button did nothing and said it had worked.
	//
	// I fixed the other commit path and not this one, and this is the path a
	// repository takes every time after the first — the branch already exists.
	// So the button worked once, on a fresh branch, and never again.
	if out, err := repo.Git(ctx, "add", "--", marker); err != nil {
		return "", fmt.Errorf("stage the trigger marker: %s", firstLine(out))
	}
	tree, err := repo.Git(ctx, "write-tree")
	if err != nil {
		return "", fmt.Errorf("read the tree to commit: %w", err)
	}
	out, err := repo.Git(ctx, gitArgs(
		"commit-tree", strings.TrimSpace(tree), "-p", parent, "-m", message)...)
	if err != nil {
		return "", fmt.Errorf("build the commit: %s", firstLine(out))
	}
	sha := strings.TrimSpace(out)
	// --hard, on a throwaway clone in the sandbox: HEAD is attached to the
	// branch, so this moves the branch and syncs the working tree at once.
	if out, err := repo.Git(ctx, "reset", "--hard", sha); err != nil {
		return "", fmt.Errorf("move %s to the new commit: %s", branch, firstLine(out))
	}
	return sha, nil
}

// explainTriggerPushFailure keeps the token advice and drops the pull request.
//
// The shared push helper is written for the install flow, where a rejected push
// really can mean an open pull request. This button opens none, and being sent
// to look for one wastes the reader's time on a thing that does not exist.
func explainTriggerPushFailure(err error) string {
	text := explainPushFailure(err)
	if strings.Contains(text, "may already be open") {
		return "the remote's copy of " + TriggerBranch + " moved while this ran — " +
			"something else pushed to it. Nothing was lost: press the button again " +
			"and the next attempt starts from where the branch is now."
	}
	return text
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return strings.TrimSpace(text[:index])
	}
	return text
}
