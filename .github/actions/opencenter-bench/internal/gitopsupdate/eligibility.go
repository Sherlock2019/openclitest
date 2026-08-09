package gitopsupdate

import (
	"fmt"
	"os"
	"strings"
)

// The promotion gate: whether a finished run has earned the right to change
// the desired state.
//
// The rule the whole stage is built around is that a Test Bench functional
// failure must never be hidden by a successful Git commit. So eligibility is
// evaluated before any write, from the run's own numbers, and a failure here
// produces BLOCKED with a reason rather than a quiet skip.

// RunSummary is what the gate reads. It is deliberately a plain struct rather
// than the console's Run type: the CLI, the API and the tests all produce one,
// and coupling the gate to the console's in-memory shape would mean the
// headless path could not use it.
type RunSummary struct {
	RunID        string
	Completed    bool
	Cancelled    bool
	Passed       int
	Warnings     int
	Failed       int
	Blocked      int
	CleanupState string // passed, failed, skipped, unknown
	ReportPath   string
	SecretLeak   bool

	SourceRepository string
	SourceCommit     string
	CLIVersion       string
	BenchVersion     string
	Environment      string
}

// Cleanup states. A cleanup that was never attempted is not a cleanup that
// passed, and the difference decides whether a run may be promoted.
const (
	CleanupPassed  = "passed"
	CleanupFailed  = "failed"
	CleanupSkipped = "skipped"
	CleanupUnknown = "unknown"
)

// Eligibility is the gate's answer.
type Eligibility struct {
	Eligible bool
	// Warned is true when the run passed only because warnings were explicitly
	// allowed. The result carries WARNING rather than PASSED, so a promotion
	// made under that allowance is visible in the report afterwards.
	Warned  bool
	Reasons []string
}

// Eligible evaluates a finished run against the configuration.
//
// Every refusal is collected rather than returned at the first one. Somebody
// looking at a blocked stage wants the list, not a game of whack-a-mole where
// fixing the cleanup reveals that the report was missing all along.
func Eligible(run RunSummary, config Config) Eligibility {
	var reasons []string
	refuse := func(format string, args ...any) {
		reasons = append(reasons, fmt.Sprintf(format, args...))
	}

	switch {
	case run.Cancelled:
		refuse("the run was cancelled")
	case !run.Completed:
		refuse("the run did not complete")
	}

	if strings.TrimSpace(run.ReportPath) == "" {
		refuse("no report was generated")
	} else if _, err := os.Stat(run.ReportPath); err != nil {
		refuse("the report is missing: %s", run.ReportPath)
	}

	if run.Failed > 0 {
		refuse("%d test(s) failed", run.Failed)
	}
	if run.Blocked > 0 {
		refuse("%d required test(s) were blocked", run.Blocked)
	}
	if run.SecretLeak {
		refuse("a secret-leak finding was recorded")
	}

	switch run.CleanupState {
	case CleanupPassed:
	case CleanupFailed:
		refuse("cleanup did not complete successfully")
	default:
		// Unknown and skipped are refused together and on purpose. GitOps
		// evidence claims to describe the final state of the run; a cleanup
		// nobody ran means nobody knows what that state is.
		refuse("cleanup was not verified (%s)", cleanupWord(run.CleanupState))
	}

	// A run that executed nothing has not passed. It is the emptiest possible
	// green, and promoting on it would mean a bench pointed at a broken binary
	// promotes it enthusiastically.
	if run.Passed == 0 && run.Failed == 0 && run.Warnings == 0 {
		refuse("no checks were executed")
	}

	warned := false
	if run.Warnings > 0 {
		if config.AllowWarnings {
			warned = true
		} else {
			refuse("%d warning(s) — set %s=true to promote a warning-only run",
				run.Warnings, EnvAllowWarnings)
		}
	}

	return Eligibility{Eligible: len(reasons) == 0, Warned: warned, Reasons: reasons}
}

func cleanupWord(state string) string {
	if strings.TrimSpace(state) == "" {
		return CleanupUnknown
	}
	return state
}

// Approval is the pair of permissions a remote write needs.
//
// Two independent gates. The environment variable says this machine is allowed
// to publish at all — in CI it is set by the workflow only after the tests
// passed and only on an event that may publish. Approved says a person, or an
// explicit --approve-gitops, asked for this particular one. Neither implies
// the other, and that is the whole design: a CI runner with the variable set
// still will not publish a run nobody asked it to.
type Approval struct {
	GateSet  bool
	Approved bool
}

// ReadGate reads the environment gate.
func ReadGate() bool { return os.Getenv(Gate) == "1" }

// Permits reports whether both gates are open, and why not when they are not.
func (a Approval) Permits() (bool, string) {
	switch {
	case !a.GateSet && !a.Approved:
		return false, "not approved, and " + Gate + " is not set"
	case !a.GateSet:
		return false, Gate + " is not set — this environment may not publish"
	case !a.Approved:
		return false, "not approved — nobody asked for this update"
	}
	return true, ""
}
