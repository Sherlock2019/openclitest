// Package e2e models a cluster's whole life as an ordered, resumable run.
//
// The existing bench answers "does this command work?" — it runs one invocation,
// judges it and moves on. This package answers a different question: "can this
// build stand up a cluster, prove it healthy, and take it down again without
// leaving anything behind?" That is not a longer list of commands. It is a
// sequence where each step depends on the last, where failure halfway leaves
// real infrastructure running, and where the interesting failures happen in the
// gaps between commands rather than inside them.
//
// So the unit here is the phase, not the command. A phase knows what must have
// happened before it, what it is allowed to skip, and — most importantly —
// whether stopping at it leaves anything that has to be destroyed.
package e2e

import (
	"fmt"
	"sort"
	"strings"
)

// State is where a phase stands.
//
// Nine of them, and the distinctions are the point. "failed" and "blocked" are
// not the same claim: the first says the product is wrong, the second says we
// never got to find out. A release gate that treats a missing prerequisite as a
// product failure blocks a good build; one that treats it as a pass ships an
// untested one.
type State string

const (
	StateNotStarted State = "not_started"
	StateRunning    State = "running"
	StatePassed     State = "passed"
	StateWarning    State = "warning"
	StateFailed     State = "failed"
	StateBlocked    State = "blocked"
	StateSkipped    State = "skipped"
	StateCancelled  State = "cancelled"
	StateCleaning   State = "cleaning"
)

// Terminal reports whether a phase in this state has finished with it.
func (s State) Terminal() bool {
	switch s {
	case StatePassed, StateWarning, StateFailed, StateBlocked, StateSkipped, StateCancelled:
		return true
	}
	return false
}

// Bad reports whether this state should stop the run.
//
// A warning does not. The emulated providers cannot answer some questions at all,
// and a run that halts on every "cannot check this here" would never reach the
// phases that matter.
func (s State) Bad() bool {
	return s == StateFailed || s == StateCancelled
}

// ID identifies a phase on the command line and in the run state on disk.
type ID string

const (
	PhasePlan              ID = "plan"
	PhaseWorkspace         ID = "workspace"
	PhasePrerequisites     ID = "prerequisites"
	PhaseBuild             ID = "build"
	PhaseVerifyBinary      ID = "verify-binary"
	PhaseConfigure         ID = "configure"
	PhaseValidateConfig    ID = "validate-config"
	PhaseDoctor            ID = "doctor"
	PhaseRenderPreview     ID = "render-preview"
	PhaseGenerate          ID = "generate"
	PhaseValidateArtifacts ID = "validate-artifacts"
	PhaseDeploy            ID = "deploy"
	PhaseInfrastructure    ID = "infrastructure"
	PhaseKubernetes        ID = "kubernetes-health"
	PhasePlatform          ID = "platform-health"
	PhaseSmoke             ID = "smoke"
	PhaseFailureTests      ID = "failure-tests"
	PhaseDiagnostics       ID = "diagnostics"
	PhaseDestroy           ID = "destroy"
	PhaseVerifyCleanup     ID = "verify-cleanup"
	PhaseReport            ID = "report"
)

// Phase is one step of the lifecycle.
type Phase struct {
	ID      ID
	Number  int
	Title   string
	Purpose string

	// Creates says this phase can bring real resources into existence. Once one
	// of these has started, the run owes a destroy — even if it failed, even if
	// it was cancelled, and especially if it crashed halfway.
	Creates bool

	// Required phases cannot be skipped from the command line. Safety and
	// cleanup are required for a reason that has nothing to do with tidiness:
	// --skip-phase destroy on a real provider is how a test bench starts costing
	// somebody money in the background.
	Required bool

	// Needs are the phases whose success this one assumes. Not merely ordering:
	// running kubernetes-health without deploy having passed reports a broken
	// cluster when what happened is there is no cluster.
	Needs []ID
}

// Order is the lifecycle, in the order it happens.
//
// Numbered from zero to match the design document, and read from this one place
// so the command line, the dashboard and the report cannot disagree about what
// the phases are or what order they come in.
var Order = []Phase{
	{PhasePlan, 0, "Plan and safety",
		"Show what is about to happen and take approval before anything runs.",
		false, true, nil},

	{PhaseWorkspace, 1, "Clean workspace",
		"An isolated run directory. Never the engineer's own openCenter configuration.",
		false, true, []ID{PhasePlan}},

	{PhasePrerequisites, 2, "Prerequisites",
		"Which tools this profile needs, which are present, and at what version.",
		false, true, []ID{PhaseWorkspace}},

	{PhaseBuild, 3, "Build the CLI",
		"mise run build, in the CLI repository. The repository's own build, not a second one.",
		false, false, []ID{PhasePrerequisites}},

	{PhaseVerifyBinary, 4, "Verify the binary",
		"Version, command tree, checksum, and that the build matches the source commit.",
		false, false, []ID{PhaseBuild}},

	{PhaseConfigure, 5, "Generate configuration",
		"cluster init, from profile defaults and run-specific names. Never hardcoded credentials.",
		false, false, []ID{PhaseVerifyBinary}},

	{PhaseValidateConfig, 6, "Validate configuration",
		"cluster validate, with --output json where the command supports it.",
		false, false, []ID{PhaseConfigure}},

	{PhaseDoctor, 7, "Provider preflight",
		"cluster doctor. There is no `preflight` command; doctor is it.",
		false, false, []ID{PhaseValidateConfig}},

	{PhaseRenderPreview, 8, "Generation preview",
		"cluster generate --render-only. There is no --dry-run flag.",
		false, false, []ID{PhaseDoctor}},

	{PhaseGenerate, 9, "Generate artifacts",
		"cluster generate. Records every produced file with size, checksum and sensitivity.",
		false, false, []ID{PhaseRenderPreview}},

	{PhaseValidateArtifacts, 10, "Validate artifacts",
		"Only the validators whose tools are present. A missing tofu skips, it does not fail.",
		false, false, []ID{PhaseGenerate}},

	{PhaseDeploy, 11, "Deploy",
		"cluster deploy. Long-running, cancellable, and every resource registered as it appears.",
		true, false, []ID{PhaseValidateArtifacts}},

	{PhaseInfrastructure, 12, "Provider infrastructure",
		"What the provider says exists. In emulation, the simulated state model — labelled as such.",
		false, false, []ID{PhaseDeploy}},

	{PhaseKubernetes, 13, "Kubernetes health",
		"API, nodes, system pods, PVCs, DNS. Polled against deadlines, never slept through.",
		false, false, []ID{PhaseDeploy}},

	{PhasePlatform, 14, "Platform services",
		"Only the services the configuration enables. A disabled service is not a failure.",
		false, false, []ID{PhaseKubernetes}},

	{PhaseSmoke, 15, "Smoke tests",
		"A real application: deploy, resolve, call, verify, delete.",
		true, false, []ID{PhaseKubernetes}},

	{PhaseFailureTests, 16, "Failure and retry",
		"Injected failures, bounded and classified apart from product defects.",
		true, false, []ID{PhaseKubernetes}},

	{PhaseDiagnostics, 17, "Diagnostics",
		"The same collector locally and in CI. Runs whatever happened above.",
		false, true, []ID{PhaseWorkspace}},

	{PhaseDestroy, 18, "Destroy",
		"cluster destroy, in dependency order. Runs after success, after failure, after cancellation.",
		false, true, []ID{PhaseWorkspace}},

	{PhaseVerifyCleanup, 19, "Verify cleanup",
		"Proof of removal. A zero exit code from destroy is a claim, not evidence.",
		false, true, []ID{PhaseDestroy}},

	{PhaseReport, 20, "Evidence and report",
		"HTML, Markdown, JSON, JUnit and CSV, with a cause for every failure.",
		false, true, []ID{PhaseWorkspace}},
}

// Lookup finds a phase by id.
func Lookup(id ID) (Phase, bool) {
	for _, phase := range Order {
		if phase.ID == id {
			return phase, true
		}
	}
	return Phase{}, false
}

// IDs is the lifecycle as a list of identifiers, in order.
func IDs() []ID {
	out := make([]ID, 0, len(Order))
	for _, phase := range Order {
		out = append(out, phase.ID)
	}
	return out
}

// AlwaysRun are the phases that happen whatever went before.
//
// Diagnostics, destroy, verify-cleanup and report. A run that fell over at
// deploy needs these more than one that passed, and "the run failed so we
// stopped" is how a half-built cluster is left running overnight.
func AlwaysRun() []ID {
	return []ID{PhaseDiagnostics, PhaseDestroy, PhaseVerifyCleanup, PhaseReport}
}

func isAlways(id ID) bool {
	for _, candidate := range AlwaysRun() {
		if candidate == id {
			return true
		}
	}
	return false
}

// Selection is what the operator asked to run: --from-phase, --to-phase,
// --only-phase and --skip-phase, resolved together.
type Selection struct {
	From ID
	To   ID
	Only []ID
	Skip []ID
}

// Resolve turns a selection into the phases to run, in lifecycle order.
//
// It refuses rather than silently narrowing. Someone who mistypes a phase name
// and gets a run that quietly does nothing has been told their build is fine by
// a test that never ran.
func (s Selection) Resolve() ([]ID, error) {
	index := map[ID]int{}
	for position, phase := range Order {
		index[phase.ID] = position
	}

	for _, group := range [][]ID{s.Only, s.Skip} {
		for _, id := range group {
			if _, ok := index[id]; !ok {
				return nil, unknownPhase(id)
			}
		}
	}
	for _, id := range []ID{s.From, s.To} {
		if id == "" {
			continue
		}
		if _, ok := index[id]; !ok {
			return nil, unknownPhase(id)
		}
	}

	first, last := 0, len(Order)-1
	if s.From != "" {
		first = index[s.From]
	}
	if s.To != "" {
		last = index[s.To]
	}
	if first > last {
		return nil, fmt.Errorf("--from-phase %s comes after --to-phase %s in the lifecycle",
			s.From, s.To)
	}

	only := map[ID]bool{}
	for _, id := range s.Only {
		only[id] = true
	}
	skip := map[ID]bool{}
	for _, id := range s.Skip {
		// Required phases are not skippable. Saying so out loud beats honouring
		// it: --skip-phase destroy is exactly the flag somebody reaches for when
		// a run is slow, and it is the one that leaves a cluster running.
		if phase, ok := Lookup(id); ok && phase.Required {
			return nil, fmt.Errorf("%s cannot be skipped: %s", id, phase.Purpose)
		}
		skip[id] = true
	}

	var out []ID
	for position, phase := range Order {
		switch {
		case skip[phase.ID]:
			continue
		case len(only) > 0 && !only[phase.ID] && !isAlways(phase.ID):
			continue
		case len(only) > 0:
			out = append(out, phase.ID)
		case position < first || position > last:
			// Outside the window, unless it is one of the phases that always
			// runs — a window ending at deploy still owes a destroy.
			if isAlways(phase.ID) {
				out = append(out, phase.ID)
			}
		default:
			out = append(out, phase.ID)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("that selection runs no phases at all")
	}
	return out, nil
}

func unknownPhase(id ID) error {
	names := make([]string, 0, len(Order))
	for _, phase := range Order {
		names = append(names, string(phase.ID))
	}
	sort.Strings(names)
	return fmt.Errorf("no phase called %q. The phases are: %s",
		id, strings.Join(names, ", "))
}

// Unsatisfied reports which of a phase's dependencies did not pass.
//
// Used to mark a phase blocked rather than run it and watch it fail for a reason
// that is not its own: kubernetes-health against a cluster that was never
// deployed reports an unreachable API, which reads as a broken cluster.
func Unsatisfied(phase Phase, states map[ID]State) []ID {
	var missing []ID
	for _, need := range phase.Needs {
		switch states[need] {
		case StatePassed, StateWarning, StateSkipped:
			continue
		default:
			missing = append(missing, need)
		}
	}
	return missing
}
