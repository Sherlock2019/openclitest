package e2e

import (
	"strings"
	"testing"
	"time"
)

// The safety properties, tested because they are the ones whose failure is
// expensive and silent: a skipped destroy leaves infrastructure running, and a
// mistyped phase name that quietly runs nothing reports a green build that was
// never tested.

func TestDestroyCannotBeSkipped(t *testing.T) {
	// The flag somebody reaches for when a run is slow, and the one that leaves
	// a cluster running on somebody's account overnight.
	_, err := Selection{Skip: []ID{PhaseDestroy}}.Resolve()
	if err == nil {
		t.Fatal("destroy was skippable")
	}
	if !strings.Contains(err.Error(), "cannot be skipped") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

func TestEveryRequiredPhaseRefusesToBeSkipped(t *testing.T) {
	for _, phase := range Order {
		if !phase.Required {
			continue
		}
		if _, err := (Selection{Skip: []ID{phase.ID}}).Resolve(); err == nil {
			t.Errorf("%s is marked required but can be skipped", phase.ID)
		}
	}
}

func TestAnUnknownPhaseIsRefusedRatherThanIgnored(t *testing.T) {
	// Silently running nothing would report a build as fine when nothing tested
	// it — the worst outcome available to a test bench.
	_, err := Selection{Only: []ID{"kubernets-health"}}.Resolve()
	if err == nil {
		t.Fatal("a mistyped phase name was accepted")
	}
	if !strings.Contains(err.Error(), "kubernetes-health") {
		t.Errorf("the error does not list the real phase names: %v", err)
	}
}

func TestCleanupStillRunsWhenTheWindowEndsEarly(t *testing.T) {
	// --to-phase deploy must still destroy. A window that stops at the phase
	// which creates infrastructure and then goes home is the leak this guards.
	selected, err := Selection{To: PhaseDeploy}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []ID{PhaseDestroy, PhaseVerifyCleanup, PhaseDiagnostics, PhaseReport} {
		if !contains(selected, wanted) {
			t.Errorf("%s did not run for a window ending at deploy", wanted)
		}
	}
}

func TestOnlyPhaseStillRunsCleanup(t *testing.T) {
	selected, err := Selection{Only: []ID{PhaseKubernetes}}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !contains(selected, PhaseDestroy) {
		t.Error("--only-phase dropped destroy")
	}
}

func TestAReversedWindowIsRefused(t *testing.T) {
	if _, err := (Selection{From: PhaseDestroy, To: PhaseBuild}).Resolve(); err == nil {
		t.Fatal("a window running backwards was accepted")
	}
}

func TestPhasesComeOutInLifecycleOrder(t *testing.T) {
	selected, err := Selection{}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	position := map[ID]int{}
	for index, phase := range Order {
		position[phase.ID] = index
	}
	for index := 1; index < len(selected); index++ {
		if position[selected[index]] <= position[selected[index-1]] {
			t.Fatalf("%s came after %s", selected[index], selected[index-1])
		}
	}
}

func TestDependenciesBlockRatherThanFail(t *testing.T) {
	// kubernetes-health against a cluster nobody deployed reports an unreachable
	// API, which reads as a broken cluster rather than as an absent one.
	phase, _ := Lookup(PhaseKubernetes)
	missing := Unsatisfied(phase, map[ID]State{PhaseDeploy: StateFailed})
	if len(missing) != 1 || missing[0] != PhaseDeploy {
		t.Fatalf("kubernetes-health did not report deploy as unsatisfied: %v", missing)
	}
	// A skipped dependency is satisfied: configuration-only skips deploy on
	// purpose, and that must not block everything downstream.
	if left := Unsatisfied(phase, map[ID]State{PhaseDeploy: StateSkipped}); len(left) != 0 {
		t.Errorf("a deliberately skipped dependency blocked the phase: %v", left)
	}
}

// --- profiles ---------------------------------------------------------------

func TestNonDeployingProfilesSkipTheClusterPhases(t *testing.T) {
	profile, err := FindProfile("configuration-only")
	if err != nil {
		t.Fatal(err)
	}
	skipped := profile.SkipsFrom()
	for _, wanted := range []ID{PhaseDeploy, PhaseKubernetes, PhaseSmoke} {
		if !contains(skipped, wanted) {
			t.Errorf("configuration-only would have run %s", wanted)
		}
	}
}

func TestRealProfilesRequireApproval(t *testing.T) {
	for _, name := range []string{"openstack-real", "vmware-real", "baremetal-real"} {
		profile, err := FindProfile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !profile.LiveApproval {
			t.Errorf("%s creates real infrastructure without requiring approval", name)
		}
	}
}

func TestEmulatedProfilesNeverDeploy(t *testing.T) {
	// The brief forbids emulation from contacting a real provider. A profile
	// that both emulates and deploys would be doing exactly that.
	for _, profile := range Profiles {
		if profile.Emulated() && profile.Deploys {
			t.Errorf("%s is emulated but deploys", profile.Name)
		}
	}
}

func TestAnUnknownProfileListsTheRealOnes(t *testing.T) {
	_, err := FindProfile("kubernetes")
	if err == nil {
		t.Fatal("an unknown profile was accepted")
	}
	if !strings.Contains(err.Error(), "configuration-only") {
		t.Errorf("the error does not list the profiles: %v", err)
	}
}

// --- the release gate --------------------------------------------------------

func TestALeakedResourceFailsAnOtherwiseGreenRun(t *testing.T) {
	// The rule that matters most: a run where every test passed but a cluster is
	// still running has not passed.
	run := freshRun()
	for index := range run.Phases {
		run.Phases[index].State = StatePassed
	}
	run.Resources = []Resource{{Kind: "kind-cluster", Name: "e2e-test"}}

	verdict, why := run.Gate()
	if verdict != VerdictFail {
		t.Fatalf("a leaked cluster gave %s (%s)", verdict, why)
	}
}

func TestABlockedRunIsInconclusiveNotAPass(t *testing.T) {
	// Reporting a missing prerequisite as a pass ships an untested build.
	run := freshRun()
	for index := range run.Phases {
		run.Phases[index].State = StatePassed
	}
	run.Result(PhasePrerequisites).State = StateBlocked

	verdict, _ := run.Gate()
	if verdict != VerdictInconclusive {
		t.Fatalf("a blocked run gave %s, wanted INCONCLUSIVE", verdict)
	}
}

func TestACleanRunPasses(t *testing.T) {
	run := freshRun()
	for index := range run.Phases {
		run.Phases[index].State = StatePassed
	}
	if verdict, why := run.Gate(); verdict != VerdictPass {
		t.Fatalf("a clean run gave %s (%s)", verdict, why)
	}
}

func TestCancellationIsInconclusive(t *testing.T) {
	run := freshRun()
	run.Result(PhaseDeploy).State = StateCancelled
	if verdict, _ := run.Gate(); verdict != VerdictInconclusive {
		t.Fatalf("a cancelled run gave %s, wanted INCONCLUSIVE", verdict)
	}
}

// --- the resource registry ---------------------------------------------------

func TestAStartedCreatingPhaseMeansSomethingMayExist(t *testing.T) {
	// A deploy that died in its first second has still made things, and only the
	// provider knows what. Destroy must run anyway.
	run := freshRun()
	run.Result(PhaseDeploy).State = StateFailed
	if !run.Created() {
		t.Fatal("a failed deploy reported that nothing was created")
	}
}

func TestNothingCreatedWhenTheCreatingPhasesNeverRan(t *testing.T) {
	run := freshRun()
	run.Result(PhaseDeploy).State = StateSkipped
	if run.Created() {
		t.Error("a skipped deploy reported that something was created")
	}
}

func TestRemovedResourcesLeaveTheRemainingList(t *testing.T) {
	run := freshRun()
	run.Resources = []Resource{
		{Kind: "cluster", Name: "a"},
		{Kind: "cluster", Name: "b"},
	}
	run.MarkRemoved("cluster", "a")
	if remaining := run.Remaining(); len(remaining) != 1 || remaining[0].Name != "b" {
		t.Fatalf("remaining was %v", remaining)
	}
}

func freshRun() *Run {
	profile, _ := FindProfile("kind")
	return NewRun(profile, ChannelLocal, "", time.Now())
}

func contains(list []ID, wanted ID) bool {
	for _, item := range list {
		if item == wanted {
			return true
		}
	}
	return false
}
