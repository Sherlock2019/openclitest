// Package actionsetup wires a repository's GitHub Actions to this Test Bench.
//
// It writes exactly one file — .github/workflows/test-bench.yml — into somebody
// else's repository, and it proposes that as a pull request rather than pushing
// it. The reasoning is the same one stage 11 rests on: this project does not
// change what somebody else runs without a person agreeing to it, and a
// one-file pull request costs a reviewer five seconds.
//
// The division of labour with internal/gitopsupdate is deliberate. That package
// promotes a tested build into a delivery repository; this one installs the CI
// that produces the tested build. Different question, same machinery — so the
// clone, the credential handling, the redaction, the branch, the commit, the
// push and the pull request are all reused from there rather than written
// again. What is new here is only the file being written and the rules about
// which paths may change.
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

// WorkflowPath is the only path this package may ever write.
//
// A constant rather than a setting. The blast radius of "install CI for me" has
// to be one file in one place; making it configurable would turn a convenience
// into a way to overwrite anything in a repository the operator does not own.
const WorkflowPath = ".github/workflows/test-bench.yml"

// DefaultAction is the published action a generated workflow calls.
//
// The same action as the lifecycle workflow, in the same repository, run with a
// different mode. That is the whole point and it was not true until now.
//
// It used to be Sherlock2019/opencenterclitest-Simple@main — a different action
// in a different repository — while the lifecycle workflow called this one. Two
// workflows, two actions, two repositories, and the result was a bench that read
// as two benches because it was two benches. Every fix made here landed on the
// lifecycle half only: the Node 24 bumps, the cache key four jobs were fighting
// over, the module cache two setup-go steps could not share. The command half
// went on running code from a repository nobody is developing any more.
//
// One action, one repository, two modes:
//
//	mode: commands   every CLI command, on every environment, judged
//	mode: lifecycle  the twenty-one phases
//
// Everything before the mode is shared — stage the bench, build it, resolve and
// check out the CLI, build that with its own toolchain — which is why this was
// written as one composite action in the first place.
const DefaultAction = "Sherlock2019/fullopenclitestbench@main"

// Gate is the environment variable that permits writing to a remote repository.
//
// Its own gate, not the promotion one. Rewriting somebody's CI configuration
// and promoting an image into a delivery repository are different permissions
// held by different people: an operator trusted to ship a tested build is not
// thereby trusted to change what every future commit runs. Reusing
// OPENCLI_ALLOW_GITOPS_UPDATE would quietly merge the two.
const Gate = "OPENCLI_ALLOW_ACTIONS_SETUP"

// ReadGate reports whether the environment permits a remote write.
func ReadGate() bool { return os.Getenv(Gate) == "1" }

// Approval is the two-gate decision for this package.
//
// Its own type rather than gitopsupdate.Approval, for one reason that only
// showed up in front of somebody: that one's refusal names
// OPENCLI_ALLOW_GITOPS_UPDATE, because it is the promotion gate. Reusing it
// here printed a refusal naming a variable that has nothing to do with what was
// refused, directly above a line reporting the real gate as unset. Two names for
// one condition is worse than either alone.
type Approval struct {
	// GateSet is the environment's permission, given by whoever started the
	// console. Approved is the operator's, given per action.
	GateSet  bool
	Approved bool
}

// Permits reports whether both gates are open, and says which is not.
//
// The message carries the fix, not just the fault: the gate is read at start-up
// from the process environment, so "set it" means restarting the console, and a
// refusal that does not say so leaves somebody exporting a variable in a shell
// the console cannot see.
func (a Approval) Permits() (bool, string) {
	switch {
	case !a.GateSet && !a.Approved:
		return false, "not approved, and " + Gate + " is not set — tick the box " +
			"beside the button, and restart the console with " + Gate + "=1"
	case !a.GateSet:
		return false, Gate + " is not set. It is read when the console starts, so " +
			"restart it with:  " + Gate + "=1 ./bin/testlab"
	case !a.Approved:
		return false, "not approved — tick the box beside the button"
	}
	return true, ""
}

// EnvToken is the credential this package uses. It falls back to the GitOps
// token so an operator who has already configured one is not asked twice — but
// note that the GitOps token usually will NOT work here: writing under
// .github/workflows needs the `workflow` scope on top of contents:write.
const (
	EnvToken      = "OPENCLI_ACTIONS_TOKEN"
	EnvRepository = "OPENCLI_ACTIONS_REPOSITORY"
	EnvAction     = "OPENCLI_ACTION_REF"
	// EnvSSHKey is a deploy key for the target repository.
	//
	// Worth preferring over a token, for a reason that is not obvious: the
	// `workflow` scope restriction is a property of TOKEN authentication.
	// GitHub applies it to OAuth apps, personal access tokens and GitHub Apps,
	// and it is what makes "contents:write is not enough" true. An SSH deploy
	// key carries no scopes at all, so a key with write access may push a
	// workflow file that a contents:write token may not.
	//
	// It buys the git half only. Opening a pull request is a REST call, and
	// there is no such thing over SSH — so a key alone pushes the branch and
	// stops there, which is a complete and useful outcome.
	EnvSSHKey = "OPENCLI_ACTIONS_SSH_KEY"
	// EnvReplace permits overwriting a workflow that already calls this bench.
	EnvReplace = "OPENCLI_ACTIONS_REPLACE"
	// EnvMode and EnvProvider carry the console's environment choices into the
	// workflow it writes.
	EnvMode     = "OPENCLI_ACTIONS_ENV_MODE"
	EnvProvider = "OPENCLI_ACTIONS_PROVIDER"
)

// DefaultManifestPath is where a Kustomization usually lives.
const DefaultManifestPath = "clusters/my-cluster/kustomization.yaml"

// Options describe the workflow to render.
type Options struct {
	// Kind selects which of the two workflows this is. Empty means the command
	// bench, which is what every caller written before there were two meant.
	Kind Kind

	// E2E carries the choices only the lifecycle workflow has. Ignored for the
	// command bench.
	E2E E2EOptions

	// Action is owner/repo@ref of the published Test Bench action.
	Action string
	// TargetRepository is the repository being wired up, as owner/name. Empty
	// means the workflow tests whichever repository it is committed to, which
	// is the common case and the one that survives a fork.
	TargetRepository string
	// GitOpsRepository turns promotion on. Empty leaves it off, and the
	// rendered workflow then mentions nothing about it — a file carrying
	// half-filled promotion settings looks configured and is not.
	GitOpsRepository string
	ManifestPath     string

	// EnvironmentMode and Provider carry the console's choices into CI.
	//
	// Without them the generated workflow names neither, the action falls back
	// to its own defaults — emulated, openstack — and a run configured for
	// vmware here quietly tests something else there. Naming them makes the
	// file say what it runs instead of leaving it to a default two repositories
	// away.
	//
	// Only these two. The credentials that go with them stay on this machine by
	// design: a fork's pull request must never reach them, GitHub's runners
	// cannot see a private cloud anyway, and a credential in a repository is a
	// credential leaked. CI reaching a real provider is a per-repository
	// decision made with repository secrets, not one this console makes.
	EnvironmentMode string
	Provider        string

	// Replace permits overwriting a workflow that already calls this bench.
	//
	// Off by default, and the default is the whole point. A repository that is
	// already wired up usually has a workflow somebody has since customised —
	// a provider, an environment mode, GitOps settings, extra steps. Rendering
	// is not a merge: it produces the canonical file and nothing else, so
	// installing over a customised one silently deletes all of it. Measured on
	// a real repository, that was 106 lines removed and 4 added.
	//
	// So an existing workflow that already calls this action stops the install
	// and says what would be lost. Setting this says "yes, I mean it".
	Replace bool
}

func (o Options) action() string {
	if value := strings.TrimSpace(o.Action); value != "" {
		return value
	}
	return DefaultAction
}

func (o Options) manifest() string {
	if value := strings.TrimSpace(o.ManifestPath); value != "" {
		return value
	}
	return DefaultManifestPath
}

func (o Options) promoting() bool {
	return strings.TrimSpace(o.GitOpsRepository) != ""
}

// sshRemote reports whether the GitOps repository can only be pushed to with a
// key. An ssh remote cannot authenticate with a token, so the workflow has to
// carry a deploy key as well — discovered here rather than by the operator,
// three failed runs later.
func (o Options) sshRemote() bool {
	value := strings.TrimSpace(o.GitOpsRepository)
	return strings.HasPrefix(value, "git@") || strings.HasPrefix(value, "ssh://")
}

// Workflow renders the file.
//
// The single source of truth for what a generated workflow contains. The card
// prints this and Install commits this; when they were two copies — one shell,
// one Go — the only question was which would drift first.
func Workflow(o Options) []byte {
	// Two workflows, one entry point. The card prints whichever this returns and
	// Install commits whichever this returns, so they cannot diverge — which is
	// the same reason there was one renderer when there was one workflow.
	if o.Kind.resolved() == KindE2E {
		return e2eWorkflow(o)
	}

	var b strings.Builder

	b.WriteString("# .github/workflows/test-bench.yml\n")
	b.WriteString("#\n")
	b.WriteString("# Installed by the openCenter CLI Test Bench.\n")
	b.WriteString("# Every commit is tested. Nothing is deployed.\n")
	b.WriteString("name: Test Bench\n\n")

	b.WriteString("on:\n")
	// Every commit on every branch, except one that only edits a workflow file:
	// that changes no openCenter code, so testing it proves nothing and costs a
	// full run. The trigger button touches .opencenter-test-bench-trigger,
	// which is outside this filter, so pressing it still starts a run.
	// And the lifecycle's trigger marker, so pressing its button does not also
	// start this one. The branch was per-kind and the marker was not, so both
	// workflows ran whichever button was pressed.
	ignore := "['.github/workflows/**', '" + otherTriggerMarker(KindTestBench) + "']"
	b.WriteString("  push:                 # every commit on every branch\n")
	b.WriteString("    paths-ignore: " + ignore + "\n")
	b.WriteString("  pull_request:\n")
	b.WriteString("    paths-ignore: " + ignore + "\n")
	b.WriteString("    branches: [main]\n")
	b.WriteString("  workflow_dispatch:\n")
	if o.promoting() {
		b.WriteString("    inputs:\n")
		b.WriteString("      publish:\n")
		b.WriteString("        description: Open a GitOps pull request when the run passes\n")
		b.WriteString("        required: true\n")
		b.WriteString("        default: false\n")
		b.WriteString("        type: boolean\n")
	}

	b.WriteString("\npermissions:\n")
	b.WriteString("  contents: read\n")
	if o.promoting() {
		b.WriteString("  packages: write       # pushing the image to ghcr.io\n")
	}

	b.WriteString("\nconcurrency:\n")
	b.WriteString("  group: test-bench-${{ github.ref }}\n")
	b.WriteString("  cancel-in-progress: true\n\n")

	b.WriteString("jobs:\n")
	b.WriteString("  test:\n")
	b.WriteString("    runs-on: ubuntu-latest\n")
	b.WriteString("    timeout-minutes: 120\n")
	b.WriteString("    steps:\n")
	b.WriteString("      - uses: " + o.action() + "\n")

	// A `with:` block only when there is something to put in it: an empty one
	// is a syntax error, and one full of defaults reads as settings somebody
	// now has to maintain.
	target := strings.TrimSpace(o.TargetRepository)
	mode := strings.TrimSpace(o.EnvironmentMode)
	provider := strings.TrimSpace(o.Provider)
	// `real` is dropped rather than written. A GitHub runner has no credentials
	// for a private cloud and cannot reach one, so a workflow asking for it
	// would fail every run in a way that looks like a broken bench rather than
	// an impossible request. The console's other three modes all work in CI:
	// emulated needs nothing, and kind runs in the runner's own Docker.
	if strings.EqualFold(mode, "real") {
		mode = ""
	}

	// Always a `with:` block now, because mode is always written.
	//
	// Said out loud rather than left to the action's default, so the two
	// workflows read as what they are: one bench, two modes. A reader comparing
	// them should see `mode: commands` here and `mode: lifecycle` there, not one
	// file that names a mode and one that says nothing and relies on a default
	// being what they assumed.
	b.WriteString("        with:\n")
	b.WriteString("          mode: commands\n")
	{
		if target != "" {
			b.WriteString("          opencenter_cli_repository: " + target + "\n")
		}
		// Named rather than left to the action's defaults. Without these every
		// run silently tests emulated openstack, so a vmware code path could go
		// green for months without CI having looked at it once.
		if mode != "" {
			b.WriteString("          environment_mode: " + mode + "\n")
		}
		if provider != "" {
			b.WriteString("          provider: " + provider + "\n")
		}
		if o.promoting() {
			b.WriteString("          gitops_repository: " + strings.TrimSpace(o.GitOpsRepository) + "\n")
			b.WriteString("          gitops_kustomization_path: " + o.manifest() + "\n")
			// Both gates, expressed in the workflow itself, so a push or a
			// fork's pull request can never publish whatever secrets exist.
			b.WriteString("          publish: >-\n")
			b.WriteString("            ${{ github.event_name == 'workflow_dispatch'\n")
			b.WriteString("                && inputs.publish == true }}\n")
			b.WriteString("          gitops_token: ${{ secrets.GITOPS_TOKEN }}\n")
			if o.sshRemote() {
				b.WriteString("          gitops_ssh_key: ${{ secrets.GITOPS_SSH_KEY }}\n")
			}
		}
	}
	return []byte(b.String())
}

// --- installing ----------------------------------------------------------------

// Mode says how far the operation may go.
type Mode string

const (
	// ModePreview renders and compares, and writes nothing remote.
	ModePreview Mode = "preview"
	// ModeApproved is the only mode that pushes and opens a pull request.
	ModeApproved Mode = "approved"
)

// Status is the outcome.
type Status string

const (
	// StatusUnchanged means the repository already has this exact workflow.
	// Distinct from a success that changed something, because "already wired
	// up" and "just wired up" are different answers to the operator.
	StatusUnchanged Status = "unchanged"
	StatusPreview   Status = "preview"
	StatusPRCreated Status = "pull_request_created"
	// StatusPushed is a branch on the remote with no pull request behind it:
	// either the remote is not a GitHub owner/name (a mirror, a local path) or
	// pull request creation was switched off. The write happened, so this is a
	// success — reporting it as a failure would say the file did not land when
	// it did, and send somebody looking for a bug that is not there.
	StatusPushed  Status = "pushed"
	StatusBlocked Status = "blocked"
	StatusFailed  Status = "failed"
)

// Step ids, in the order they run.
const (
	StepPreflight   = "preflight"
	StepRender      = "render"
	StepCheckout    = "checkout"
	StepCompare     = "compare"
	StepBranch      = "branch"
	StepWrite       = "write"
	StepValidate    = "validate"
	StepCommit      = "commit"
	StepPush        = "push"
	StepPullRequest = "pull-request"
	StepVerify      = "verify"
)

// Steps is the order, and the order a UI draws them in.
var Steps = []string{
	StepPreflight, StepRender, StepCheckout, StepCompare, StepBranch,
	StepWrite, StepValidate, StepCommit, StepPush, StepPullRequest, StepVerify,
}

// StepStatus is where one step got to.
type StepStatus string

const (
	StepPending StepStatus = "pending"
	StepOK      StepStatus = "ok"
	StepSkipped StepStatus = "skipped"
	StepFailed  StepStatus = "failed"
)

// Step is one phase's outcome.
type Step struct {
	ID     string     `json:"id"`
	Status StepStatus `json:"status"`
	Detail string     `json:"detail,omitempty"`
}

// Result is the whole operation.
type Result struct {
	Status      Status   `json:"status"`
	Mode        Mode     `json:"mode"`
	Repository  string   `json:"repository,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	Changed     bool     `json:"changed"`
	Existing    bool     `json:"existingWorkflow"`
	CommitSHA   string   `json:"commitSha,omitempty"`
	PullRequest string   `json:"pullRequestUrl,omitempty"`
	Diff        string   `json:"diff,omitempty"`
	Message     string   `json:"message"`
	Reasons     []string `json:"reasons,omitempty"`
	Steps       []Step   `json:"steps,omitempty"`
}

// Request is everything Install needs.
type Request struct {
	// Config carries the TARGET repository — the one being wired up — plus its
	// base branch and API root. Reused wholesale so the clone, the credential
	// helper and the redaction behave exactly as they do for a promotion.
	Config   gitopsupdate.Config
	Options  Options
	Approval Approval
	Mode     Mode

	SandboxRoot string
	Redactor    gitopsupdate.Redactor
}

func newResult(mode Mode) *Result {
	out := &Result{Mode: mode, Status: StatusBlocked, Message: "not started"}
	for _, id := range Steps {
		out.Steps = append(out.Steps, Step{ID: id, Status: StepPending})
	}
	return out
}

func (r *Result) set(id string, status StepStatus, detail string) {
	for index := range r.Steps {
		if r.Steps[index].ID == id {
			r.Steps[index].Status = status
			r.Steps[index].Detail = detail
			return
		}
	}
}

// skipRest marks every still-pending step, so nothing is left looking as though
// it might yet run.
func (r *Result) skipRest(status StepStatus, detail string) {
	for index := range r.Steps {
		if r.Steps[index].Status == StepPending {
			r.Steps[index].Status = status
			r.Steps[index].Detail = detail
		}
	}
}

func (r *Result) fail(id, reason string) *Result {
	r.set(id, StepFailed, reason)
	r.skipRest(StepSkipped, "an earlier step failed")
	r.Status = StatusFailed
	r.Message = reason
	r.Reasons = append(r.Reasons, reason)
	return r
}

// Exit codes, shared with the promotion table so a caller that knows one knows
// both.
const (
	ExitOK              = 0
	ExitBadConfig       = 2
	ExitApprovalMissing = 4
	ExitGitFailed       = 5
	ExitPRFailed        = 6
)

// ExitCode maps a finished result onto a process exit status.
func (r *Result) ExitCode() int {
	switch r.Status {
	case StatusUnchanged, StatusPreview, StatusPRCreated, StatusPushed:
		return ExitOK
	case StatusBlocked:
		return ExitApprovalMissing
	case StatusFailed:
		for _, step := range r.Steps {
			if step.ID == StepPullRequest && step.Status == StepFailed {
				return ExitPRFailed
			}
		}
		return ExitGitFailed
	}
	return ExitGitFailed
}

// Headline is the one line a console prints above the steps.
func (r *Result) Headline() string {
	switch r.Status {
	case StatusUnchanged:
		return "ACTIONS SETUP — ALREADY WIRED UP"
	case StatusPreview:
		return "ACTIONS SETUP — PREVIEW"
	case StatusPRCreated:
		return "ACTIONS SETUP — PULL REQUEST OPENED"
	case StatusPushed:
		return "ACTIONS SETUP — BRANCH PUSHED"
	case StatusBlocked:
		return "ACTIONS SETUP — BLOCKED"
	case StatusFailed:
		return "ACTIONS SETUP — FAILED"
	}
	return "ACTIONS SETUP"
}

// BranchName is deterministic, so re-running reuses the branch and its pull
// request rather than opening a second one.
const BranchName = "automation/opencenter-test-bench-setup"

// Install writes the workflow into the target repository and proposes it.
func Install(ctx context.Context, request Request) *Result {
	result := newResult(request.Mode)
	config := request.Config
	if request.Redactor == nil {
		request.Redactor = noRedactor{}
	}
	result.Repository = gitopsupdate.StripCredentials(config.Repository)

	// ---- preflight ----------------------------------------------------------
	if !config.Configured() {
		return result.fail(StepPreflight,
			"no repository to wire up — give the owner/name of the repository whose "+
				"Actions should run the bench")
	}
	if gitopsupdate.Slug(config.Repository) == "" &&
		!strings.Contains(config.Repository, "://") &&
		!strings.HasPrefix(config.Repository, "/") {
		return result.fail(StepPreflight,
			fmt.Sprintf("%q is not owner/name, an https:// URL or an ssh remote",
				result.Repository))
	}
	if request.Mode == ModeApproved {
		if permitted, why := request.Approval.Permits(); !permitted {
			result.set(StepPreflight, StepOK, "")
			result.skipRest(StepSkipped, "not approved")
			result.Status = StatusBlocked
			result.Message = why
			result.Reasons = append(result.Reasons, why)
			return result
		}
	}
	result.set(StepPreflight, StepOK,
		fmt.Sprintf("%s → %s", result.Repository, config.BaseBranch))

	// Which of the two workflows this install is for. Read once, here, so no
	// later step can use one kind's path with another kind's content — the
	// failure that would put the lifecycle workflow at the command bench's path
	// and leave a repository running neither.
	kind := request.Options.Kind.resolved()
	workflowPath := kind.Path()
	branchName := kind.Branch()

	// ---- render -------------------------------------------------------------
	rendered := Workflow(request.Options)
	result.set(StepRender, StepOK,
		fmt.Sprintf("%s, %d bytes", workflowPath, len(rendered)))

	// ---- checkout -----------------------------------------------------------
	checkout := filepath.Join(request.SandboxRoot, "target")
	repo, err := gitopsupdate.Open(ctx, config, request.SandboxRoot, checkout, request.Redactor)
	if err != nil {
		return result.fail(StepCheckout, err.Error())
	}
	result.set(StepCheckout, StepOK, "cloned "+config.BaseBranch)

	// ---- compare ------------------------------------------------------------
	//
	// Read before writing. The realistic case is not an empty repository: it is
	// one that already has a workflow, possibly pointing at a different bench,
	// and blindly creating would either fail or discard whatever else it wired
	// up. Identical content is a success with nothing to do, not a no-op to be
	// silently skipped past.
	existing, readErr := os.ReadFile(filepath.Join(checkout, workflowPath))
	result.Existing = readErr == nil
	if readErr == nil && string(existing) == string(rendered) {
		result.set(StepCompare, StepOK, "already identical")
		result.skipRest(StepSkipped, "nothing to change")
		result.Status = StatusUnchanged
		result.Changed = false
		result.Message = fmt.Sprintf(
			"%s already runs the %s — %s is identical, nothing to do.",
			result.Repository, kind.Label(), workflowPath)
		return result
	}
	switch {
	case readErr == nil && callsThisBench(string(existing), request.Options.action()) &&
		!request.Options.Replace:
		// Already wired up, and customised. Rendering is not a merge — it emits
		// the canonical file and nothing else — so continuing would delete
		// whatever was added since. Stopping is the useful answer, and the
		// count makes the cost concrete rather than abstract.
		removed, added := lineDelta(string(existing), string(rendered))
		result.set(StepCompare, StepFailed, fmt.Sprintf(
			"already calls %s, and replacing it would remove %d line(s) and add %d",
			request.Options.action(), removed, added))
		result.skipRest(StepSkipped, "not replacing a customised workflow")
		result.Status = StatusBlocked
		result.Changed = false
		result.Message = fmt.Sprintf(
			"%s already runs this bench. Its workflow differs from the standard one — "+
				"probably because somebody configured it. Installing would replace it, "+
				"removing %d line(s). Nothing was changed. Set replace to yes if that "+
				"is what you want, or preview to see the diff first.",
			result.Repository, removed)
		result.Reasons = append(result.Reasons,
			"an existing workflow already calls this bench and would be overwritten")
		return result
	case readErr == nil:
		detail := "a workflow exists and differs; it will be updated"
		if request.Options.Replace && callsThisBench(string(existing), request.Options.action()) {
			removed, added := lineDelta(string(existing), string(rendered))
			detail = fmt.Sprintf("replacing the existing workflow: -%d +%d lines",
				removed, added)
		}
		result.set(StepCompare, StepOK, detail)
	default:
		result.set(StepCompare, StepOK, "no workflow yet; it will be created")
	}

	// ---- branch -------------------------------------------------------------
	if err := repo.CreateBranch(ctx, branchName); err != nil {
		return result.fail(StepBranch, err.Error())
	}
	result.Branch = branchName
	result.set(StepBranch, StepOK, branchName)

	// ---- write --------------------------------------------------------------
	//
	// Through WriteInto, which refuses a path that leaves the checkout. The
	// path is a constant here so that cannot happen, but the guard costs
	// nothing and survives somebody making it configurable later.
	if err := gitopsupdate.WriteInto(checkout, workflowPath, rendered); err != nil {
		return result.fail(StepWrite, err.Error())
	}
	result.Changed = true
	result.set(StepWrite, StepOK, workflowPath)

	// ---- validate -----------------------------------------------------------
	//
	// One path, and only that path. This is the guard that makes "let the bench
	// configure my repository" a safe thing to agree to: whatever else goes
	// wrong, nothing but the workflow file can be in the commit.
	changed, err := changedPaths(ctx, repo)
	if err != nil {
		return result.fail(StepValidate, err.Error())
	}
	for _, path := range changed {
		if path != workflowPath {
			return result.fail(StepValidate, fmt.Sprintf(
				"%s would also change; this may only ever write %s", path, workflowPath))
		}
	}
	if len(changed) == 0 {
		return result.fail(StepValidate, "nothing changed, which should not happen after a write")
	}
	result.set(StepValidate, StepOK, "only "+workflowPath+" changed")

	// --cached, because changedPaths staged everything a moment ago to ask what
	// would be committed. A plain `git diff` compares the working tree against
	// the index and is therefore empty here — which looked like "no change" and
	// would have shipped a preview that showed the operator nothing.
	if diff, diffErr := repo.Git(ctx, "diff", "--cached", "--", workflowPath); diffErr == nil {
		result.Diff = diff
	}

	// A preview stops here, with everything prepared and nothing sent.
	if request.Mode != ModeApproved {
		result.skipRest(StepSkipped, "preview — no remote changes made")
		result.Status = StatusPreview
		verb := "create"
		if result.Existing {
			verb = "update"
		}
		result.Message = fmt.Sprintf(
			"PREVIEW — nothing was sent. Would %s %s in %s on branch %s.",
			verb, workflowPath, result.Repository, branchName)
		return result
	}

	// ---- commit -------------------------------------------------------------
	message := commitMessage(request.Options, result.Existing)
	sha, err := repo.Commit(ctx, message)
	if err != nil {
		return result.fail(StepCommit, err.Error())
	}
	result.CommitSHA = sha
	result.set(StepCommit, StepOK, gitopsupdate.ShortSHA(sha))

	// ---- push ---------------------------------------------------------------
	// Updating, not refusing. The branch name is this package's own constant, in
	// its own namespace, and its only content is the one file this package may
	// write — so a second install is an update of a proposal, not a collision.
	// Refusing meant every re-run ended in a dead end the operator had to clear
	// by deleting a branch by hand.
	if err := repo.PushUpdating(ctx, branchName); err != nil {
		return result.fail(StepPush, explainPushFailure(err))
	}
	result.set(StepPush, StepOK, "pushed "+branchName)

	// ---- pull request -------------------------------------------------------
	// An empty slug is not an error. Slug() returns "" for anything that is not
	// a GitHub owner/name — a local path, a file:// URL, a mirror on another
	// host — and there is no API there to open a request against. The branch is
	// already pushed at this point, so refusing here would report a failure for
	// work that succeeded.
	slug := gitopsupdate.Slug(config.Repository)
	switch {
	case !config.CreatePR:
		result.set(StepPullRequest, StepSkipped, "pull request creation is switched off")
	case slug == "":
		result.set(StepPullRequest, StepSkipped,
			"the remote is not a GitHub owner/name, so there is no API to open one")
	default:
		client := gitopsupdate.NewClient(config, request.Redactor)
		pr, prErr := client.Create(ctx, slug, branchName, config.BaseBranch,
			pullRequestTitleFor(kind, result.Existing),
			pullRequestBody(request.Options, result.Existing))
		if prErr != nil {
			// A missing token is not a failed install.
			//
			// The branch is already on the remote at this point — the file is
			// committed, pushed and verifiable — and opening the request is a
			// REST call that a deploy key cannot make. There is no such thing as
			// a pull request over SSH, and the panel's own advice is that a key
			// is the easier credential, so this is the *recommended* setup: it
			// ended with FAILED and exit 6 over work that had entirely
			// succeeded, which is how somebody re-runs an install that already
			// landed.
			//
			// Only the no-token case. A token that exists and was refused is a
			// real failure and still reports as one.
			if noGitHubToken(prErr) {
				result.set(StepPullRequest, StepSkipped,
					"no token — a deploy key cannot open a pull request")
			} else {
				return result.fail(StepPullRequest, explainPushFailure(prErr))
			}
		} else {
			result.PullRequest = pr.URL
			detail := "opened"
			if pr.Existing {
				detail = "already open, reused"
			}
			result.set(StepPullRequest, StepOK, detail)
		}
	}

	// ---- verify -------------------------------------------------------------
	//
	// Asked of the remote, not of the local clone. A step that claims to have
	// pushed should be able to prove it from the other end.
	if !repo.RemoteHasBranch(ctx, branchName) {
		return result.fail(StepVerify, "the branch is not on the remote after a successful push")
	}
	result.set(StepVerify, StepOK, "branch "+branchName+" is on the remote")

	verb := "updated"
	if !result.Existing {
		verb = "added"
	}
	if result.PullRequest == "" {
		// The branch is on the remote and that is the whole of what happened.
		// Saying so, and saying what is left to do, beats a success message
		// that implies a request somebody will go looking for.
		result.Status = StatusPushed
		result.Message = fmt.Sprintf(
			"%s %s in %s on branch %s. No pull request was opened — "+
				"open one from that branch, or merge it, and every commit is tested.",
			verb, workflowPath, result.Repository, branchName)
		return result
	}
	result.Status = StatusPRCreated
	result.Message = fmt.Sprintf(
		"%s %s in %s. Review and merge the pull request and every commit is tested. "+
			"Nothing was merged and nothing was deployed.",
		verb, workflowPath, result.Repository)
	return result
}

// callsThisBench reports whether a workflow already uses this action.
//
// Matched on owner/repo without the ref, so a repository pinned to @v1 is still
// recognised as wired up when the default names @main. The question is "is this
// already ours", not "is it the same version".
func callsThisBench(workflow, action string) bool {
	name := action
	if at := strings.IndexByte(name, '@'); at > 0 {
		name = name[:at]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	return strings.Contains(workflow, name)
}

// lineDelta counts what replacing one file with another would cost, so the
// refusal can say how much rather than merely that it would be a lot.
func lineDelta(before, after string) (removed, added int) {
	keep := map[string]int{}
	for _, line := range strings.Split(after, "\n") {
		keep[strings.TrimSpace(line)]++
	}
	for _, line := range strings.Split(before, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if keep[trimmed] > 0 {
			keep[trimmed]--
			continue
		}
		removed++
	}
	have := map[string]int{}
	for _, line := range strings.Split(before, "\n") {
		have[strings.TrimSpace(line)]++
	}
	for _, line := range strings.Split(after, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if have[trimmed] > 0 {
			have[trimmed]--
			continue
		}
		added++
	}
	return removed, added
}

// changedPaths lists what the working tree would commit.
func changedPaths(ctx context.Context, repo *gitopsupdate.Repo) ([]string, error) {
	if _, err := repo.Git(ctx, "add", "--", "."); err != nil {
		return nil, err
	}
	out, err := repo.Git(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

// explainPushFailure turns GitHub's refusal into something actionable.
//
// This one message is the difference between a usable feature and a support
// ticket. A token with contents:write is not enough to write under
// .github/workflows — GitHub requires the `workflow` scope on a classic token,
// or Workflows:write on a fine-grained one, and refuses with wording that reads
// like a bug in the tool rather than a missing checkbox on the token.
func explainPushFailure(err error) string {
	text := err.Error()
	lower := strings.ToLower(text)
	if strings.Contains(lower, "workflow") &&
		(strings.Contains(lower, "scope") || strings.Contains(lower, "permission") ||
			strings.Contains(lower, "refusing")) {
		return "the token may not write GitHub Actions workflows. " +
			"contents:write is not enough for .github/workflows: a classic token " +
			"also needs the `workflow` scope, and a fine-grained token needs " +
			"Workflows: write. GITHUB_TOKEN inside Actions can never do this. " +
			"Original message: " + text
	}
	return text
}

func commitMessage(o Options, existing bool) string {
	verb := "Add"
	if existing {
		verb = "Update"
	}
	kind := o.Kind.resolved()
	if kind == KindE2E {
		return fmt.Sprintf(""+
			"ci: %s the openCenter cluster lifecycle E2E workflow\n"+
			"\n"+
			"Runs %s on every commit: build the CLI with mise, generate and\n"+
			"validate a cluster configuration, deploy, prove Kubernetes and the\n"+
			"platform services healthy, smoke test, destroy, and prove nothing\n"+
			"was left behind.\n"+
			"\n"+
			"Nothing is deployed to any environment by this workflow.\n"+
			"\n"+
			"Installed at %s.\n",
			strings.ToLower(verb), kind.Path(),
			time.Now().UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf(""+
		"ci: %s the openCenter CLI Test Bench workflow\n"+
		"\n"+
		"Runs %s on every commit, through %s.\n"+
		"The bench tests and reports; it does not deploy.\n"+
		"\n"+
		"Installed at %s.\n",
		strings.ToLower(verb), kind.Path(), o.action(),
		time.Now().UTC().Format(time.RFC3339))
}

// noGitHubToken reports whether a pull request failed only for want of a token.
//
// Matched on the message because that is what the client returns, and the
// distinction matters: no token is an SSH-key install that finished, while a
// token that was refused is a real failure with a real cause.
func noGitHubToken(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "no github token") ||
		strings.Contains(text, "no token")
}

func pullRequestTitle(existing bool) string {
	if existing {
		return "ci: update the openCenter CLI Test Bench workflow"
	}
	return "ci: test every commit with the openCenter CLI Test Bench"
}

// pullRequestTitleFor names the workflow, so two installs into one repository
// do not open two pull requests with the same title.
func pullRequestTitleFor(kind Kind, existing bool) string {
	if kind.resolved() != KindE2E {
		return pullRequestTitle(existing)
	}
	if existing {
		return "ci: update the openCenter cluster lifecycle E2E workflow"
	}
	return "ci: run the openCenter cluster lifecycle E2E on every commit"
}

func pullRequestBody(o Options, existing bool) string {
	var b strings.Builder
	verb := "adds"
	if existing {
		verb = "updates"
	}
	if o.Kind.resolved() == KindE2E {
		return e2ePullRequestBody(o, verb)
	}
	b.WriteString("## openCenter CLI Test Bench\n\n")
	b.WriteString("This pull request " + verb + " `" + KindTestBench.Path() + "`.\n\n")
	b.WriteString("| Field | Value |\n|---|---|\n")
	b.WriteString("| Action | `" + o.action() + "` |\n")
	if strings.TrimSpace(o.TargetRepository) != "" {
		b.WriteString("| Tests | `" + o.TargetRepository + "` |\n")
	} else {
		b.WriteString("| Tests | this repository |\n")
	}
	if o.promoting() {
		b.WriteString("| GitOps | `" + o.GitOpsRepository + "` |\n")
		b.WriteString("| Manifest | `" + o.manifest() + "` |\n")
	} else {
		b.WriteString("| GitOps | not configured — testing only |\n")
	}

	b.WriteString("\n### What happens after merge\n\n")
	b.WriteString("- Every push and pull request runs the bench and reports a verdict.\n")
	if o.promoting() {
		b.WriteString("- A manual run with `publish` ticked may open a pull request against\n")
		b.WriteString("  the GitOps repository. It needs `GITOPS_TOKEN`")
		if o.sshRemote() {
			b.WriteString(" and `GITOPS_SSH_KEY`")
		}
		b.WriteString(" as repository secrets.\n")
	}
	b.WriteString("\nThe Test Bench never merges and never deploys.\n")
	return b.String()
}

// e2ePullRequestBody describes the lifecycle workflow to whoever has to approve
// it.
//
// The reviewer is being asked to let a stranger's tool run twenty-one phases in
// their repository, so the body says what those phases are, which of them can
// create anything, and — the question they will actually have — what a pull
// request from a fork is allowed to do.
func e2ePullRequestBody(o Options, verb string) string {
	var b strings.Builder
	e := o.E2E

	b.WriteString("## openCenter cluster lifecycle E2E\n\n")
	b.WriteString("This pull request " + verb + " `" + KindE2E.Path() + "`. One file.\n\n")
	b.WriteString("It runs the full lifecycle on every commit: build openCenter-cli with " +
		"mise, generate and validate a cluster configuration, deploy a Kubernetes " +
		"cluster, prove Kubernetes and the platform services healthy, run smoke tests, " +
		"exercise failure and retry, destroy everything, and prove nothing was left " +
		"behind.\n\n")

	b.WriteString("| Field | Value |\n|---|---|\n")
	b.WriteString("| CLI under test | `" + e.cliRepo() + "` |\n")
	if nightly := strings.TrimSpace(e.Nightly); nightly != "" {
		b.WriteString("| Nightly profile | `" + nightly + "` |\n")
	} else {
		b.WriteString("| Nightly | not scheduled |\n")
	}
	if environment := strings.TrimSpace(e.RealEnvironment); environment != "" {
		b.WriteString("| Real providers | behind the `" + environment + "` environment |\n")
	} else {
		b.WriteString("| Real providers | no job — real infrastructure is not reachable |\n")
	}
	b.WriteString(fmt.Sprintf("| Timeout | %d minutes |\n", e.timeout()))

	b.WriteString("\n### What a pull request is allowed to do\n\n")
	b.WriteString("Only the profiles that create nothing:\n\n")
	for _, name := range safeProfiles() {
		b.WriteString("- `" + name + "`\n")
	}
	b.WriteString("\nNo credentials, nothing to spend, safe from a fork. The profiles " +
		"that build real infrastructure are `workflow_dispatch` only")
	if strings.TrimSpace(e.RealEnvironment) != "" {
		b.WriteString(" and sit behind a GitHub Environment, so a human approves before " +
			"anything is created")
	}
	b.WriteString(".\n")

	b.WriteString("\n### What it does not do\n\n")
	b.WriteString("- It never merges anything.\n")
	b.WriteString("- It never deploys to any environment of yours.\n")
	b.WriteString("- Cleanup and evidence upload run with `if: always()`, so a run that " +
		"dies mid-deploy still destroys what it made and still tells you what happened.\n")
	b.WriteString("- Secrets reach the process through `env:`, never a command line.\n")
	return b.String()
}

// noRedactor is the fallback when a caller supplies none.
type noRedactor struct{}

func (noRedactor) Add(...string)           {}
func (noRedactor) String(in string) string { return in }
