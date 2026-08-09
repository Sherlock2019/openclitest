package gitopsupdate

import (
	"strings"
	"time"
)

// Status is the stage's verdict. Six values, and the distinctions between them
// are the point of the stage rather than a nicety.
//
// StatusFailed and StatusBlocked are not the same thing: blocked means the run
// was not good enough to promote, failed means the promotion machinery itself
// broke. Conflating them would let a git error read as a test failure and a
// test failure read as an outage.
//
// StatusNotConfigured is not a failure at all. A bench nobody has pointed at a
// delivery repository has not failed; it has nothing to do.
type Status string

const (
	StatusPassed        Status = "passed"
	StatusWarning       Status = "warning"
	StatusFailed        Status = "failed"
	StatusBlocked       Status = "blocked"
	StatusNotConfigured Status = "not_configured"
	StatusPreview       Status = "preview"
	// StatusPRCreated is the one outcome that means a remote changed.
	StatusPRCreated Status = "pull_request_created"
)

// Mode says how far the operation was allowed to go.
type Mode string

const (
	// ModePreflight only answers "could this run".
	ModePreflight Mode = "preflight"
	// ModePreview prepares the whole change and writes nothing remote.
	ModePreview Mode = "preview"
	// ModeApproved is the only mode that commits, pushes and opens a PR, and
	// it requires both gates.
	ModeApproved Mode = "approved"
)

// StepStatus is where one of the eleven steps got to.
type StepStatus string

const (
	StepPending StepStatus = "pending"
	StepOK      StepStatus = "ok"
	StepSkipped StepStatus = "skipped"
	StepBlocked StepStatus = "blocked"
	StepFailed  StepStatus = "failed"
)

// The eleven step ids, matching `step:` in config/gitops-update.yaml. The
// console renders one row per id and fills it in from the result, so a name
// that drifts here shows up as a row that never lights rather than as a crash.
const (
	StepPreflight   = "preflight"
	StepQualityGate = "quality-gate"
	StepEvidence    = "evidence"
	StepCheckout    = "checkout"
	StepBranch      = "branch"
	StepManifest    = "manifest"
	StepValidate    = "validate"
	StepCommit      = "commit"
	StepPush        = "push"
	StepPullRequest = "pull-request"
	StepVerify      = "verify"
)

// Steps is the order the stage runs them in, and the order the page draws them.
var Steps = []string{
	StepPreflight, StepQualityGate, StepEvidence, StepCheckout, StepBranch,
	StepManifest, StepValidate, StepCommit, StepPush, StepPullRequest, StepVerify,
}

// Step is one phase's outcome.
type Step struct {
	ID      string     `json:"id"`
	Status  StepStatus `json:"status"`
	Detail  string     `json:"detail,omitempty"`
	Millis  int64      `json:"millis"`
	Started time.Time  `json:"-"`
}

// Result is the whole stage, as the report, the API and the exit code all read
// it.
//
// Eligible and Status are separate fields because they answer separate
// questions. A run can be eligible and still fail to push; a run can be
// ineligible with nothing broken at all. Collapsing them into one field is how
// a functional test failure ends up hidden behind a successful git commit,
// which this stage exists partly to prevent.
type Result struct {
	Status            Status   `json:"status"`
	Mode              Mode     `json:"mode"`
	Eligible          bool     `json:"eligible"`
	Changed           bool     `json:"changed"`
	Repository        string   `json:"repository,omitempty"`
	BaseBranch        string   `json:"baseBranch,omitempty"`
	Branch            string   `json:"updateBranch,omitempty"`
	FilesChanged      []string `json:"filesChanged,omitempty"`
	CommitSHA         string   `json:"commitSha,omitempty"`
	PullRequest       string   `json:"pullRequestUrl,omitempty"`
	PullRequestNumber int      `json:"pullRequestNumber,omitempty"`
	ImageReference    string   `json:"imageReference,omitempty"`
	EvidencePath      string   `json:"evidencePath,omitempty"`
	PatchPath         string   `json:"patchPath,omitempty"`
	Message           string   `json:"message"`
	// Reasons are why an ineligible run was refused, one line each, in the
	// order they were found.
	Reasons []string `json:"reasons,omitempty"`
	Steps   []Step   `json:"steps,omitempty"`
}

// newResult starts a result with every step pending, so a run that dies at step
// four still reports eleven rows and it is obvious where it stopped. A result
// that only lists what happened cannot show what did not.
func newResult(mode Mode) *Result {
	out := &Result{Mode: mode, Status: StatusBlocked, Message: "not started"}
	for _, id := range Steps {
		out.Steps = append(out.Steps, Step{ID: id, Status: StepPending})
	}
	return out
}

// begin marks a step as started and returns a function that closes it.
func (r *Result) begin(id string) func(StepStatus, string) {
	started := time.Now()
	return func(status StepStatus, detail string) {
		for index := range r.Steps {
			if r.Steps[index].ID != id {
				continue
			}
			r.Steps[index].Status = status
			r.Steps[index].Detail = detail
			r.Steps[index].Millis = time.Since(started).Milliseconds()
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

// Step returns one step's outcome by id.
func (r *Result) Step(id string) (Step, bool) {
	for _, step := range r.Steps {
		if step.ID == id {
			return step, true
		}
	}
	return Step{}, false
}

// Exit codes. The table is part of the contract with CI: a caller that knows
// only the number still knows whether to retry, to fix the run, or to page
// somebody about a credential.
const (
	ExitOK              = 0
	ExitBadConfig       = 2
	ExitNotEligible     = 3
	ExitApprovalMissing = 4
	ExitGitFailed       = 5
	ExitPRFailed        = 6
)

// ExitCode maps a finished result onto the process exit code.
func (r *Result) ExitCode() int {
	switch r.Status {
	case StatusPassed, StatusPreview, StatusPRCreated, StatusNotConfigured:
		return ExitOK
	case StatusBlocked:
		if r.approvalMissing() {
			return ExitApprovalMissing
		}
		if r.badConfig() {
			return ExitBadConfig
		}
		return ExitNotEligible
	case StatusFailed:
		if step, ok := r.Step(StepPullRequest); ok && step.Status == StepFailed {
			return ExitPRFailed
		}
		return ExitGitFailed
	}
	return ExitGitFailed
}

func (r *Result) approvalMissing() bool {
	for _, reason := range r.Reasons {
		if strings.Contains(reason, Gate) || strings.Contains(reason, "not approved") {
			return true
		}
	}
	return false
}

func (r *Result) badConfig() bool {
	step, ok := r.Step(StepPreflight)
	return ok && step.Status == StepFailed
}

// Headline is the one line the console prints and the UI shows above the steps.
func (r *Result) Headline() string {
	switch r.Status {
	case StatusNotConfigured:
		return "GITOPS UPDATE — NOT CONFIGURED"
	case StatusPreview:
		return "GITOPS UPDATE — PREVIEW"
	case StatusBlocked:
		return "GITOPS UPDATE — BLOCKED"
	case StatusFailed:
		return "GITOPS UPDATE — FAILED"
	case StatusWarning:
		return "GITOPS UPDATE — WARNING"
	case StatusPRCreated:
		return "GITOPS UPDATE — PULL REQUEST CREATED"
	}
	return "GITOPS UPDATE"
}
