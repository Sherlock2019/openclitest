package spec

import (
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot finds the checkout from this file's own location, so the tests do
// not depend on where `go test` was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func TestLoadRealConfig(t *testing.T) {
	loaded, err := Load(repoRoot(t))
	if err != nil {
		t.Fatalf("loading config/: %v", err)
	}

	if len(loaded.Categories) == 0 {
		t.Error("no checklist categories loaded")
	}
	if len(loaded.Environments) == 0 {
		t.Error("no environments loaded")
	}
	if len(loaded.Prerequisites) == 0 {
		t.Error("no prerequisites loaded")
	}
	if len(loaded.Credentials) == 0 {
		t.Error("no credential methods loaded")
	}
	if len(loaded.About.Why.Goals) == 0 {
		t.Error("no goals loaded; the Why panel would be empty")
	}
	if len(loaded.About.How.Steps) == 0 {
		t.Error("no method steps loaded; the How panel would be empty")
	}
}

// Every category has to carry the four columns the console renders. A missing
// one shows up as a blank cell rather than as an error, so it is checked here.
func TestEveryCategoryIsComplete(t *testing.T) {
	loaded, err := Load(repoRoot(t))
	if err != nil {
		t.Fatalf("loading config/: %v", err)
	}
	for _, category := range loaded.Categories {
		if category.Name == "" || category.Question == "" ||
			category.LookingFor == "" || category.Example == "" {
			t.Errorf("category %q is missing one of name, question, looking_for, example", category.ID)
		}
		if len(category.Environments) == 0 {
			t.Errorf("category %q names no environment it can be answered in", category.ID)
		}
	}
}

// A prerequisite with no probe would be reported as missing for ever, and one
// with no install text leaves a person stuck.
func TestEveryPrerequisiteHasAProbe(t *testing.T) {
	loaded, err := Load(repoRoot(t))
	if err != nil {
		t.Fatalf("loading config/: %v", err)
	}
	for id, item := range loaded.PrerequisiteIndex() {
		if item.Check == "" {
			t.Errorf("prerequisite %q has no check", id)
		}
		if item.Why == "" {
			t.Errorf("prerequisite %q does not say why it is needed", id)
		}
		if item.Install == "" {
			t.Errorf("prerequisite %q has no install instructions", id)
		}
	}
}

func TestCredentialFieldsNameAnEnvironmentVariable(t *testing.T) {
	loaded, err := Load(repoRoot(t))
	if err != nil {
		t.Fatalf("loading config/: %v", err)
	}
	for _, method := range loaded.Credentials {
		for _, field := range method.Fields {
			if field.Env == "" {
				t.Errorf("credential %s.%s has no environment variable, so it can never be used",
					method.ID, field.ID)
			}
		}
		for _, environment := range method.For {
			if _, ok := loaded.Environment(environment); !ok {
				t.Errorf("credential method %q applies to unknown environment %q", method.ID, environment)
			}
		}
	}
}

func TestFindRootWalksUp(t *testing.T) {
	root := repoRoot(t)
	found, err := FindRoot(filepath.Join(root, "internal", "spec"))
	if err != nil {
		t.Fatalf("FindRoot: %v", err)
	}
	if found != root {
		t.Errorf("FindRoot returned %q, want %q", found, root)
	}
}
