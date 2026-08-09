package main

import "testing"

func TestMutationDetectionAllowsOnlyExplicitPreviews(t *testing.T) {
	tests := []struct {
		args string
		want bool
	}{
		{"cluster deploy testbench/tb-kind", true},
		{"cluster destroy testbench/tb-kind", true},
		{"secrets keys rotate", true},
		{"cluster deploy testbench/tb-kind --dry-run", false},
		{"--dry-run cluster destroy testbench/tb-kind", false},
		{"cluster deploy testbench/tb-kind --dry-run=true", false},
		{"cluster deploy testbench/tb-kind --dry-run=false", true},
		{"cluster deploy testbench/tb-kind --dry-run --dry-run=false", true},
		{"cluster deploy testbench/tb-kind --dry-run=false --dry-run", false},
		{"cluster status testbench/tb-kind", false},
	}
	for _, test := range tests {
		t.Run(test.args, func(t *testing.T) {
			if got := isMutating(test.args); got != test.want {
				t.Fatalf("isMutating(%q) = %v, want %v", test.args, got, test.want)
			}
		})
	}
}

func TestPrerequisiteFailureDoesNotRecommendSandboxFixture(t *testing.T) {
	category, _, _, action := classify(Outcome{Stderr: "no cluster configuration yet"},
		Command{Shell: true}, nil)
	if category != CatPrerequisite {
		t.Fatalf("category = %q, want %q", category, CatPrerequisite)
	}
	if action != "Use the setup command beside the check." {
		t.Fatalf("action = %q", action)
	}
}
