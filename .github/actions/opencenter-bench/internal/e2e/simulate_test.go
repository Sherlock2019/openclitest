package e2e

import (
	"strings"
	"testing"
)

// The simulated verdict has been documented wrongly once — as "reports
// SIMULATED and exits 4", which is only half of it. These pin both halves so
// the sentence and the code cannot drift apart again.

func simulatedRun(states ...State) *Run {
	run := &Run{Simulated: true}
	for index, state := range states {
		run.Phases = append(run.Phases, PhaseResult{
			ID: Order[index].ID, Number: index, State: state,
		})
	}
	return run
}

func TestASimulatedRunThatHeldTogetherIsSimulated(t *testing.T) {
	run := simulatedRun(StatePassed, StateWarning, StateSkipped, StatePassed)
	verdict, why := run.Gate()
	if verdict != VerdictSimulated {
		t.Fatalf("verdict is %q, want SIMULATED — %s", verdict, why)
	}
}

// The half that was missing from the documentation. Only six phases are faked;
// configure, validate-config and doctor still run for real, so an emulated
// profile with no reachable provider fails at one of them — and that failure is
// real, not simulated.
func TestARealFailureInsideASimulatedRunIsAFail(t *testing.T) {
	run := simulatedRun(StatePassed, StateFailed, StatePassed)
	verdict, why := run.Gate()
	if verdict != VerdictFail {
		t.Fatalf("verdict is %q, want FAIL — %s", verdict, why)
	}
	if why == "" {
		t.Fatal("a FAIL with no reason is a red light nobody can act on")
	}
}

// The one that matters. Whatever else changes, this must not: no arrangement of
// phase states may produce a PASS from a simulated run. A bench that can report
// a green release from invented data is worse than one with honest gaps,
// because the gap is visible and the lie is not.
func TestNoSimulatedRunCanEverPass(t *testing.T) {
	every := []State{
		StateNotStarted, StateRunning, StatePassed, StateWarning, StateFailed,
		StateBlocked, StateSkipped, StateCancelled, StateCleaning,
	}
	for _, first := range every {
		for _, second := range every {
			run := simulatedRun(first, second)
			// Nothing left behind, no findings: the most favourable case there is.
			if verdict, why := run.Gate(); verdict == VerdictPass {
				t.Fatalf("a simulated run reached PASS with phases %s/%s — %s",
					first, second, why)
			}
		}
	}
}

// The banner is read by people who were not there. It has to name what was
// faked, and it named five of the six.
func TestTheBannerNamesEveryFakedPhase(t *testing.T) {
	run := &Run{Simulated: true}
	banner := simulatedBanner(run)
	for _, phase := range []string{"failure-tests", "smoke", "platform"} {
		if !strings.Contains(banner, phase) {
			t.Errorf("the simulation banner does not mention %s", phase)
		}
	}
	if simulatedBanner(&Run{}) != "" {
		t.Error("an ordinary run carries a simulation banner")
	}
}

// SimulatedBodies must replace exactly the phases that need infrastructure, and
// nothing else. Adding destroy to this set was tried and is worse than the
// problem it appears to solve: it stops real resources registered by real
// phases from being cleaned at all.
func TestOnlyInfrastructurePhasesAreFaked(t *testing.T) {
	base := map[ID]Body{}
	for _, phase := range Order {
		base[phase.ID] = nil
	}
	faked := SimulatedBodies(base)

	shouldBeFaked := map[ID]bool{
		PhaseDeploy: true, PhaseInfrastructure: true, PhaseKubernetes: true,
		PhasePlatform: true, PhaseSmoke: true, PhaseFailureTests: true,
	}
	for _, phase := range Order {
		replaced := faked[phase.ID] != nil
		if replaced != shouldBeFaked[phase.ID] {
			if replaced {
				t.Errorf("%s is faked and should not be", phase.ID)
			} else {
				t.Errorf("%s should be faked and is not", phase.ID)
			}
		}
	}
	if faked[PhaseDestroy] != nil {
		t.Error("destroy is faked — real resources would never be cleaned")
	}
}
