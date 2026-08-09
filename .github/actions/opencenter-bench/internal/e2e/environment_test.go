package e2e

import (
	"strings"
	"testing"
)

// The run's environment must carry a git identity.
//
// Every command runs with HOME pointed at the run directory, which is how the
// engineer's own openCenter configuration is kept out of the run. It also hides
// ~/.gitconfig — and deploy's gitea-rebase step commits the GitOps checkout.
// Without an author, git exits 128 with "Author identity unknown" and tells the
// reader to run `git config --global`, which would change a machine the bench
// does not own.
func TestEnvironmentCarriesAGitIdentity(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "")
	ex := &Exec{Engine: &Engine{}, Run: &Run{Root: t.TempDir()}}
	env := ex.Environment()

	for _, key := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	} {
		value := valueOf(env, key)
		if value == "" {
			t.Errorf("%s is not set, so deploy's gitea-rebase step cannot commit", key)
			continue
		}
		// A committer is not an author. git needs both, and setting only the
		// author leaves the commit half-identified.
		if strings.Contains(key, "EMAIL") && !strings.Contains(value, "@") {
			t.Errorf("%s is %q, which is not an address", key, value)
		}
	}

	// HOME must still be the run's, or the isolation this bench promises is not
	// happening and the identity above is solving a problem that would not exist.
	if home := valueOf(env, "HOME"); !strings.Contains(home, ex.Run.Root) {
		t.Errorf("HOME is %q, outside the run directory", home)
	}
}

// The caller's identity wins, so CI can commit as itself.
func TestEnvironmentKeepsAnIdentityTheCallerSupplied(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "ci-runner")
	ex := &Exec{Engine: &Engine{}, Run: &Run{Root: t.TempDir()}}
	if name := valueOf(ex.Environment(), "GIT_AUTHOR_NAME"); name != "ci-runner" {
		t.Errorf("GIT_AUTHOR_NAME is %q, want the caller's %q", name, "ci-runner")
	}
}

// valueOf reads the last assignment of a key, which is the one that wins:
// Environment appends to os.Environ and a later entry overrides an earlier one.
func valueOf(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if name, rest, found := strings.Cut(entry, "="); found && name == key {
			value = rest
		}
	}
	return value
}
