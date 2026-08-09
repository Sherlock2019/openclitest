package e2e

import (
	"strings"
	"testing"
	"time"
)

// A phase named for rerun is not treated as already done.
//
// `e2e phase --run-id … --only-phase destroy` printed
//
//	destroy   blocked (from the earlier run)
//
// and did nothing, because destroy had a recorded state from the failed run.
// The two leaked resources stayed where they were, and `e2e cleanup` — which is
// the verb for removing them — behaved the same way, because it is the same
// code path with the phase list filled in.
//
// It is also the command printed as the Reproduce line on every finding. A
// reproduce line that replays a stored verdict rather than reproducing anything
// is worse than none at all: it agrees with the report, which reads as
// confirmation.
func TestANamedPhaseRunsAgainEvenWithAnEarlierResult(t *testing.T) {
	earlier := time.Now().Add(-time.Hour)
	run := &Run{Root: t.TempDir(), Phases: []PhaseResult{
		{ID: PhaseDestroy, State: StateBlocked, Started: earlier, Ended: earlier},
	}}

	engine := &Engine{Run: run}
	engine.SetStart(time.Now())

	// Without the rerun flag this is a resume, and an earlier result is exactly
	// what a resume should honour.
	if !engine.resumed(PhaseDestroy) {
		t.Error("a resume does not see the earlier result, so it would repeat " +
			"work it has already finished")
	}

	// With it, the phase runs.
	engine.Rerun = map[ID]bool{PhaseDestroy: true}
	if engine.resumed(PhaseDestroy) {
		t.Error("a phase named with --only-phase is skipped as already done, so " +
			"`e2e phase --only-phase destroy` does nothing and the resources stay")
	}

	// And naming one phase does not make every phase rerun. A rerun of build
	// would rebuild the CLI on the way to destroying a cluster.
	if !engine.resumed(PhaseDestroy) && engine.Rerun[PhaseBuild] {
		t.Error("naming destroy also marked build for rerun")
	}
}

// The Reproduce line has to be the command that actually reruns the phase.
//
// If these two ever disagree, the report tells the reader to run something that
// does not do what the report says it does.
func TestReproduceNamesThePhaseVerb(t *testing.T) {
	line := ReproduceCommand("e2e-20260808-035237", "openstack-emulated", PhaseDestroy)
	for _, want := range []string{"e2e phase", "--only-phase destroy", "--run-id e2e-20260808-035237"} {
		if !strings.Contains(line, want) {
			t.Errorf("the reproduce line %q does not contain %q", line, want)
		}
	}
}
