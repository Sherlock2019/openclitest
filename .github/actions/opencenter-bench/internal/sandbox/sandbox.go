// Package sandbox gives each run a throwaway world to work in.
//
// The CLI under test writes to a config directory, a state directory and a
// cache, reads credentials out of the environment, and shells out to tools it
// finds on PATH. A bench that let it use the real ones would be testing the
// developer's laptop as much as the binary — and would happily delete their
// clusters. Everything is redirected instead, and the environment is rebuilt
// from nothing rather than filtered, so a credential that gains a new variable
// name next month does not silently leak into a run.
package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Sandbox is one isolated filesystem root.
type Sandbox struct {
	// Root holds everything below. Removing it removes the whole world.
	Root string
	// Home stands in for the developer's home directory.
	Home string
	// ConfigDir is where the CLI keeps cluster configuration.
	ConfigDir string
	// StateDir is where it keeps state, locks and kubeconfigs.
	StateDir string
	// CacheDir is XDG_CACHE_HOME.
	CacheDir string
	// Work is the working directory commands run from.
	Work string
	// FakeBin goes at the front of PATH, so a check can put a controlled
	// stand-in for kubectl or tofu in the way of the real thing.
	FakeBin string
	// Outside is a directory beside the sandbox root that nothing should ever
	// write to. Path-traversal checks aim at it and then assert it is empty.
	Outside string

	extra map[string]string
}

// New creates a sandbox under the system temporary directory.
func New(prefix string) (*Sandbox, error) {
	root, err := os.MkdirTemp("", "opencli-bench-"+prefix+"-")
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	return NewIn(root)
}

// NewIn builds the directory layout inside an existing root. The caller owns
// the root and is responsible for removing it.
func NewIn(root string) (*Sandbox, error) {
	s := &Sandbox{
		Root:      root,
		Home:      filepath.Join(root, "home"),
		ConfigDir: filepath.Join(root, "config", "opencenter"),
		StateDir:  filepath.Join(root, "state", "opencenter"),
		CacheDir:  filepath.Join(root, "cache"),
		Work:      filepath.Join(root, "work"),
		FakeBin:   filepath.Join(root, "fakebin"),
		Outside:   filepath.Join(root, "outside-the-sandbox"),
		extra:     map[string]string{},
	}
	for _, dir := range []string{s.Home, s.ConfigDir, s.StateDir, s.CacheDir, s.Work, s.FakeBin, s.Outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	// The CLI accepts only a small editor allowlist. Put a non-interactive vi
	// stand-in first on PATH so edit commands exercise their file handling but
	// cannot inherit a terminal or wait forever.
	if _, err := s.WriteFakeTool("vi", "exit 0\n"); err != nil {
		return nil, fmt.Errorf("create headless editor: %w", err)
	}
	return s, nil
}

// Set adds or overrides one variable for every command run in this sandbox.
func (s *Sandbox) Set(key, value string) {
	s.extra[key] = value
}

// Unset removes a variable previously added with Set.
func (s *Sandbox) Unset(key string) {
	delete(s.extra, key)
}

// Get returns a variable previously added with Set.
func (s *Sandbox) Get(key string) string {
	return s.extra[key]
}

// Env builds the environment a command runs with.
//
// It is constructed from an explicit allowlist rather than by deleting known
// credential variables from the real environment. Deleting is a losing game:
// it has to be updated every time a provider invents a variable, and the cost
// of missing one is a real credential reaching a test process.
func (s *Sandbox) Env() []string {
	env := map[string]string{
		"HOME":            s.Home,
		"USERPROFILE":     s.Home, // harmless on Unix, correct if this ever runs elsewhere
		"XDG_CONFIG_HOME": filepath.Join(s.Root, "config"),
		"XDG_STATE_HOME":  filepath.Join(s.Root, "state"),
		"XDG_CACHE_HOME":  s.CacheDir,
		"XDG_DATA_HOME":   filepath.Join(s.Root, "data"),
		"TMPDIR":          filepath.Join(s.Root, "tmp"),

		"OPENCENTER_CONFIG_DIR": s.ConfigDir,
		"OPENCENTER_STATE_DIR":  s.StateDir,
		"OPENCENTER_TEST_MODE":  "1",

		// Deterministic, machine-readable output. A bench that has to strip
		// escape codes before it can assert on a string is a bench that will
		// eventually assert on the escape codes by accident.
		"TERM":     "dumb",
		"NO_COLOR": "1",
		"LC_ALL":   "C",
		"LANG":     "C",

		// `vi` resolves to the controlled, non-interactive shim in FakeBin.
		// It is also on the CLI's safe-editor allowlist.
		"EDITOR":     "vi",
		"VISUAL":     "vi",
		"GIT_EDITOR": "true",
		"PAGER":      "cat",
		"LESS":       "-F -X",

		// Some tools phone home or check for updates on first run. Neither is
		// wanted here, and both make timings meaningless.
		"CHECKPOINT_DISABLE":  "1",
		"DO_NOT_TRACK":        "1",
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "openCLI bench",
		"GIT_AUTHOR_EMAIL":    "bench@localhost",
		"GIT_COMMITTER_NAME":  "openCLI bench",
		"GIT_COMMITTER_EMAIL": "bench@localhost",
		"SSH_AUTH_SOCK":       "",
		"KUBECONFIG":          filepath.Join(s.Root, "kubeconfig.yaml"),
		"PATH":                s.FakeBin + string(os.PathListSeparator) + inheritedPath(),
	}
	for key, value := range s.extra {
		env[key] = value
	}

	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

// PathValue is the PATH commands run with, so a check that wants to add one
// more directory can prepend rather than replace.
func (s *Sandbox) PathValue() string {
	if custom, ok := s.extra["PATH"]; ok {
		return custom
	}
	return s.FakeBin + string(os.PathListSeparator) + inheritedPath()
}

// inheritedPath keeps the real PATH so kubectl, kind and git are still
// reachable. Tools are not credentials; hiding them would mean testing a CLI
// that can never succeed.
func inheritedPath() string {
	path := os.Getenv("PATH")
	if path == "" {
		if runtime.GOOS == "windows" {
			return `C:\Windows\System32`
		}
		return "/usr/local/bin:/usr/bin:/bin"
	}
	return path
}

// Cleanup removes everything the sandbox created.
func (s *Sandbox) Cleanup() error {
	if s.Root == "" || !strings.Contains(filepath.Base(s.Root), "opencli-bench") {
		// Refuse to recurse into a directory this package did not name. A
		// misconfigured root is a bug worth failing on, not one worth deleting
		// a home directory over.
		return fmt.Errorf("refusing to remove unexpected sandbox root %q", s.Root)
	}
	return os.RemoveAll(s.Root)
}

// WriteFile puts a file inside the sandbox, creating parent directories.
func (s *Sandbox) WriteFile(relative string, content []byte, mode os.FileMode) (string, error) {
	path := filepath.Join(s.Root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return "", err
	}
	return path, nil
}

// WriteFakeTool puts an executable script at the front of PATH under the given
// name, so a check can watch how the CLI calls an external tool, or make one
// fail on purpose.
func (s *Sandbox) WriteFakeTool(name, script string) (string, error) {
	path := filepath.Join(s.FakeBin, name)
	body := script
	if !strings.HasPrefix(body, "#!") {
		body = "#!/usr/bin/env bash\n" + body
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		return "", err
	}
	return path, nil
}

// Tree lists every regular file below the sandbox root, relative to it, so a
// check can prove that a command wrote nothing, or wrote only what it should.
func (s *Sandbox) Tree(under string) ([]string, error) {
	base := s.Root
	if under != "" {
		base = under
	}
	var files []string
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // a directory that vanished mid-walk is not a finding
		}
		if info.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(s.Root, path)
		if relErr != nil {
			relative = path
		}
		files = append(files, relative)
		return nil
	})
	return files, err
}
