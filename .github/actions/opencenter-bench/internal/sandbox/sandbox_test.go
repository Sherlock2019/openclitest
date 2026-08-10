package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestSandbox(t *testing.T) *Sandbox {
	t.Helper()
	box, err := New("unit")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = box.Cleanup() })
	return box
}

// The whole point of the sandbox: a credential exported in the developer's own
// shell must not reach a process it starts.
func TestEnvironmentDoesNotInheritCredentials(t *testing.T) {
	for _, leaky := range []string{
		"OS_PASSWORD", "OS_APPLICATION_CREDENTIAL_SECRET", "OS_CLOUD",
		"AWS_SECRET_ACCESS_KEY", "VSPHERE_PASSWORD", "GITHUB_TOKEN", "SOPS_AGE_KEY",
	} {
		t.Setenv(leaky, "this-must-not-be-inherited")
	}

	box := newTestSandbox(t)
	for _, entry := range box.Env() {
		key, value, _ := strings.Cut(entry, "=")
		if value == "this-must-not-be-inherited" {
			t.Errorf("%s leaked into the sandbox environment", key)
		}
	}
}

func TestEnvironmentRedirectsEveryPath(t *testing.T) {
	box := newTestSandbox(t)
	env := map[string]string{}
	for _, entry := range box.Env() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}

	for _, key := range []string{
		"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
		"OPENCENTER_CONFIG_DIR", "OPENCENTER_STATE_DIR",
	} {
		value, ok := env[key]
		if !ok {
			t.Errorf("%s is not set in the sandbox environment", key)
			continue
		}
		if !strings.HasPrefix(value, box.Root) {
			t.Errorf("%s points at %q, outside the sandbox root %q", key, value, box.Root)
		}
	}

	if !strings.HasPrefix(env["PATH"], box.FakeBin) {
		t.Errorf("PATH does not start with the sandbox bin directory: %q", env["PATH"])
	}
	if env["EDITOR"] != "vi" || env["VISUAL"] != "vi" {
		t.Errorf("headless editor is not the safe vi shim: EDITOR=%q VISUAL=%q",
			env["EDITOR"], env["VISUAL"])
	}
	if info, err := os.Stat(filepath.Join(box.FakeBin, "vi")); err != nil {
		t.Errorf("headless vi shim is missing: %v", err)
	} else if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("headless vi shim is not executable: %04o", info.Mode().Perm())
	}
}

func TestSetOverridesAndUnsetRestores(t *testing.T) {
	box := newTestSandbox(t)
	box.Set("OS_CLOUD", "flex-sim")

	lookup := func() string {
		for _, entry := range box.Env() {
			if key, value, _ := strings.Cut(entry, "="); key == "OS_CLOUD" {
				return value
			}
		}
		return ""
	}

	if got := lookup(); got != "flex-sim" {
		t.Errorf("Set did not take effect, got %q", got)
	}
	box.Unset("OS_CLOUD")
	if got := lookup(); got != "" {
		t.Errorf("Unset left %q behind", got)
	}
}

// Cleanup removes a directory tree, so it refuses to touch a root it did not
// name. A bug in the caller should fail loudly rather than delete a home
// directory.
func TestCleanupRefusesAnUnexpectedRoot(t *testing.T) {
	box := &Sandbox{Root: t.TempDir()}
	if err := box.Cleanup(); err == nil {
		t.Error("Cleanup accepted a root this package never created")
	}
}

func TestWriteFakeToolIsExecutableAndFirstOnPath(t *testing.T) {
	box := newTestSandbox(t)
	path, err := box.WriteFakeTool("kubectl", "echo fake\n")
	if err != nil {
		t.Fatalf("WriteFakeTool: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("fake tool is not executable: %04o", info.Mode().Perm())
	}
	if filepath.Dir(path) != box.FakeBin {
		t.Errorf("fake tool landed in %q, not in %q", filepath.Dir(path), box.FakeBin)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(string(content), "#!") {
		t.Error("a script without a shebang cannot be executed")
	}
}

func TestTreeListsWhatWasWritten(t *testing.T) {
	box := newTestSandbox(t)
	if _, err := box.WriteFile("config/opencenter/marker.yaml", []byte("x: 1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	files, err := box.Tree("")
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	found := false
	for _, file := range files {
		if strings.HasSuffix(file, "marker.yaml") {
			found = true
		}
	}
	if !found {
		t.Errorf("Tree did not list the file that was just written: %v", files)
	}
}
