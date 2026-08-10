package checks

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
)

func loadSpec(t *testing.T) *spec.Spec {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	loaded, err := spec.Load(root)
	if err != nil {
		t.Fatalf("loading config/: %v", err)
	}
	return loaded
}

// The checklist is the contract. A check that names a row nobody wrote down
// would run and be reported under a heading the console cannot render.
func TestEveryCheckNamesARealCategory(t *testing.T) {
	loaded := loadSpec(t)
	for _, check := range All() {
		if _, ok := loaded.Category(check.Category); !ok {
			t.Errorf("check %q is filed under unknown category %q", check.ID, check.Category)
		}
	}
}

func TestEveryCheckNamesRealEnvironments(t *testing.T) {
	loaded := loadSpec(t)
	for _, check := range All() {
		if len(check.Environments) == 0 {
			t.Errorf("check %q runs nowhere", check.ID)
		}
		for _, environment := range check.Environments {
			if _, ok := loaded.Environment(environment); !ok {
				t.Errorf("check %q names unknown environment %q", check.ID, environment)
			}
		}
	}
}

// A category is only claimed to be answerable where a check actually answers
// it. The console dims a row it cannot cover, and this keeps that honest in
// the other direction: no row promises coverage that does not exist.
func TestChecklistPromisesMatchTheChecks(t *testing.T) {
	loaded := loadSpec(t)
	for _, category := range loaded.Categories {
		for _, environment := range category.Environments {
			if Categories(environment)[category.ID] == 0 {
				t.Errorf("checklist.yaml says %q is answerable in %q, but no check does so",
					category.ID, environment)
			}
		}
	}
}

func TestEveryCheckHasAnIDAndAName(t *testing.T) {
	seen := map[string]bool{}
	for _, check := range All() {
		if check.ID == "" || check.Name == "" || check.Fn == nil {
			t.Errorf("check %+v is incomplete", check)
		}
		if seen[check.ID] {
			t.Errorf("duplicate check id %q", check.ID)
		}
		seen[check.ID] = true
		if strings.ToLower(check.ID) != check.ID || strings.Contains(check.ID, " ") {
			t.Errorf("check id %q should be a lowercase slug; it appears in URLs and reports", check.ID)
		}
	}
}

// Anything that can create a resource outliving a command has to be marked,
// or the gate cannot hold it back.
func TestRealEnvironmentChecksAreMarkedMutating(t *testing.T) {
	for _, check := range All() {
		if check.Category != "real-environment" {
			continue
		}
		if !check.Mutating {
			t.Errorf("check %q is a real-environment check but is not marked mutating", check.ID)
		}
	}
}

func TestForFiltersByEnvironmentAndGate(t *testing.T) {
	withoutGate := For("kind", false)
	withGate := For("kind", true)

	if len(withGate) <= len(withoutGate) {
		t.Errorf("the mutation gate unlocked nothing: %d without, %d with",
			len(withoutGate), len(withGate))
	}
	for _, check := range withoutGate {
		if check.Mutating {
			t.Errorf("check %q runs without the mutation gate", check.ID)
		}
	}
}

// Execute has to survive a check that panics: one broken check must not take
// down a run that has already found something.
func TestExecuteTurnsAPanicIntoAResult(t *testing.T) {
	check := Check{
		ID: "unit-panic", Name: "panics", Category: "installation",
		Environments: []string{"local"},
		Fn: func(ctx context.Context, t *T) {
			panic("deliberate")
		},
	}
	result := Execute(context.Background(), check, &Env{ID: "local"}, nil)
	if result.Status != StatusError {
		t.Errorf("a panicking check reported %q, want %q", result.Status, StatusError)
	}
	if !strings.Contains(result.Message, "deliberate") {
		t.Errorf("the panic message was lost: %q", result.Message)
	}
}

func TestExecuteReportsSkipAndFail(t *testing.T) {
	skipping := Check{
		ID: "unit-skip", Name: "skips", Category: "installation",
		Environments: []string{"local"},
		Fn:           func(ctx context.Context, t *T) { t.Skip("no reason to run") },
	}
	if result := Execute(context.Background(), skipping, &Env{ID: "local"}, nil); result.Status != StatusSkip {
		t.Errorf("got %q, want %q", result.Status, StatusSkip)
	}

	failing := Check{
		ID: "unit-fail", Name: "fails", Category: "installation",
		Environments: []string{"local"},
		Fn: func(ctx context.Context, t *T) {
			t.Assert("one holds", true, "")
			t.Assert("the other does not", false, "because")
		},
	}
	result := Execute(context.Background(), failing, &Env{ID: "local"}, nil)
	if result.Status != StatusFail {
		t.Errorf("got %q, want %q", result.Status, StatusFail)
	}
	if len(result.Assertions) != 2 {
		t.Errorf("recorded %d assertions, want 2", len(result.Assertions))
	}
}

// Skipping is for a prerequisite that is genuinely absent. It must never be a
// way to make a finding that was already recorded disappear.
func TestASkipCannotBuryAFailure(t *testing.T) {
	check := Check{
		ID: "unit-skip-after-fail", Name: "fails then skips", Category: "installation",
		Environments: []string{"local"},
		Fn: func(ctx context.Context, t *T) {
			t.Assert("something is wrong", false, "a real finding")
			t.Skip("the tool is not installed")
		},
	}
	result := Execute(context.Background(), check, &Env{ID: "local"}, nil)
	if result.Status != StatusFail {
		t.Errorf("a skip after a failed assertion reported %q, want %q", result.Status, StatusFail)
	}
	if !strings.Contains(result.Message, "already failed") {
		t.Errorf("the result does not explain what happened: %q", result.Message)
	}
}

// A check that asserts nothing has not answered its question, and reporting it
// as a pass would be worse than reporting nothing at all.
func TestExecuteTreatsNoAssertionsAsAnError(t *testing.T) {
	empty := Check{
		ID: "unit-empty", Name: "asserts nothing", Category: "installation",
		Environments: []string{"local"},
		Fn:           func(ctx context.Context, t *T) {},
	}
	if result := Execute(context.Background(), empty, &Env{ID: "local"}, nil); result.Status != StatusError {
		t.Errorf("got %q, want %q", result.Status, StatusError)
	}
}

func TestCanariesAreDistinctAndUnmistakable(t *testing.T) {
	seen := map[string]bool{}
	for _, canary := range Canaries() {
		if len(canary) < 20 {
			t.Errorf("canary %q is too short to be unmistakable in a log", canary)
		}
		if seen[canary] {
			t.Errorf("duplicate canary %q", canary)
		}
		seen[canary] = true
	}
}
