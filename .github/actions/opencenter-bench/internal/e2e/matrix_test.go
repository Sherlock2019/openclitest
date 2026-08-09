package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A matrix is only worth having if it answers the question a red result
// raises: is this the product, or is it this provider?

func writeRun(t *testing.T, root, id, profile, commit string, states map[ID]State) {
	t.Helper()
	run := &Run{ID: id, Profile: profile, CLICommit: commit,
		Root: filepath.Join(root, id)}
	for _, phase := range Order {
		state := StateNotStarted
		if want, ok := states[phase.ID]; ok {
			state = want
		}
		run.Phases = append(run.Phases, PhaseResult{
			ID: phase.ID, Number: phase.Number, Title: phase.Title, State: state,
		})
	}
	if err := os.MkdirAll(filepath.Join(run.Root, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Root, "state", "run.json"),
		encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheMatrixTellsAProviderProblemFromADefect(t *testing.T) {
	root := t.TempDir()

	// deploy is red on vmware and green everywhere else — a provider problem.
	// generate is red on both — the product's.
	writeRun(t, root, "e2e-20260101-000001", "openstack-emulated", "abc123",
		map[ID]State{PhaseDeploy: StatePassed, PhaseGenerate: StateFailed})
	writeRun(t, root, "e2e-20260101-000002", "vmware-emulated", "abc123",
		map[ID]State{PhaseDeploy: StateFailed, PhaseGenerate: StateFailed})

	matrix := BuildMatrix(root)

	if len(matrix.Profiles) != 2 {
		t.Fatalf("matrix has %d column(s), want 2: %v", len(matrix.Profiles),
			matrix.Profiles)
	}
	if !matrix.ProviderOnly(PhaseDeploy) {
		t.Error("deploy is red on one provider and green on another, and the matrix " +
			"does not call it provider-specific")
	}
	if matrix.ProviderOnly(PhaseGenerate) {
		t.Error("generate is red everywhere, so it is a defect — not a provider " +
			"problem, which is the reading that sends somebody to the wrong team")
	}
}

// Columns from different builds are not a comparison, and a matrix that hides
// that invites exactly the wrong conclusion.
func TestTheMatrixSaysWhenTheColumnsAreDifferentBuilds(t *testing.T) {
	root := t.TempDir()
	writeRun(t, root, "e2e-20260101-000001", "openstack-emulated", "abc123", nil)
	writeRun(t, root, "e2e-20260101-000002", "vmware-emulated", "def456", nil)

	if BuildMatrix(root).SameBuild {
		t.Error("two different CLI commits are reported as one build")
	}

	same := t.TempDir()
	writeRun(t, same, "e2e-20260101-000001", "openstack-emulated", "abc123", nil)
	writeRun(t, same, "e2e-20260101-000002", "vmware-emulated", "abc123", nil)
	if !BuildMatrix(same).SameBuild {
		t.Error("one commit across both columns is reported as different builds")
	}
}

// The newest run per profile, not every run. Ten runs of kind and one of vmware
// is a history, not a matrix.
func TestTheMatrixTakesTheNewestRunPerProfile(t *testing.T) {
	root := t.TempDir()
	writeRun(t, root, "e2e-20260101-000001", "kind", "old",
		map[ID]State{PhaseDeploy: StateFailed})
	writeRun(t, root, "e2e-20260101-000009", "kind", "new",
		map[ID]State{PhaseDeploy: StatePassed})

	matrix := BuildMatrix(root)
	if len(matrix.Profiles) != 1 {
		t.Fatalf("one profile ran twice and produced %d column(s)", len(matrix.Profiles))
	}
	for _, row := range matrix.Rows {
		if row.Phase != PhaseDeploy {
			continue
		}
		if got := row.Cells["kind"].State; got != StatePassed {
			t.Errorf("the matrix shows %q for deploy; the newest run passed", got)
		}
	}
}

// Skipped and blocked phases are not covered. A profile that skips six by
// design covers 15 of 21, and calling that fully covered is how a hole gets
// certified.
func TestCoverageDoesNotCountSkippedPhases(t *testing.T) {
	root := t.TempDir()
	states := map[ID]State{}
	for index, phase := range Order {
		if index < 15 {
			states[phase.ID] = StatePassed
		} else {
			states[phase.ID] = StateSkipped
		}
	}
	writeRun(t, root, "e2e-20260101-000001", "configuration-only", "abc", states)

	coverage := BuildMatrix(root).Coverage["configuration-only"]
	if coverage.Ran != 15 || coverage.Skipped != len(Order)-15 {
		t.Fatalf("coverage ran=%d skipped=%d, want 15 and %d",
			coverage.Ran, coverage.Skipped, len(Order)-15)
	}
	if got := coverage.Percent(); got != 15*100/len(Order) {
		t.Errorf("coverage is %d%%, want %d%%", got, 15*100/len(Order))
	}
	if coverage.Percent() == 100 {
		t.Error("a profile that skipped six phases reports full coverage")
	}
}

// An empty or missing directory is a normal state — nothing has run yet — not
// a crash.
func TestAnEmptyRunDirectoryIsNotAFailure(t *testing.T) {
	if got := BuildMatrix(t.TempDir()); len(got.Profiles) != 0 {
		t.Errorf("an empty directory produced %d column(s)", len(got.Profiles))
	}
	if got := BuildMatrix("/no/such/path"); len(got.Profiles) != 0 {
		t.Errorf("a missing directory produced %d column(s)", len(got.Profiles))
	}
}

// Every finding says how to see it again, and the command has to be one that
// exists.
func TestReproduceNamesARealInvocation(t *testing.T) {
	got := ReproduceCommand("e2e-20260101-000001", "kind", PhaseDeploy)
	for _, want := range []string{
		"e2e phase", "--run-id e2e-20260101-000001", "--profile kind",
		"--only-phase deploy",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the reproduce command lacks %q: %s", want, got)
		}
	}
}
