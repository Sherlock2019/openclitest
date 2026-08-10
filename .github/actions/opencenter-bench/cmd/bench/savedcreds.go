package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
)

// The answers already typed into the console, available to this binary too.
//
// config/credentials.local.yaml is written by the console and read by it, and
// until now nothing else looked at it. So the same repository and the same
// deploy key that make the Actions panel work made `bench actions` fail with
// "no repository to wire up" — the credentials were on the machine, in the
// file the product tells you to put them in, and this command could not see
// them. The operator's fix was to re-export by hand what they had already
// saved.
//
// Loaded into the process environment rather than threaded through, because
// the environment is where every one of these is already read from. Nothing
// here overrides a variable that is already set: an explicit export is a
// deliberate act for this run, and a saved value is what was true last time.

// loadSavedCredentials fills empty environment variables from the console's
// credentials file. It is best-effort: a missing or unreadable file simply
// leaves the environment as it was.
func loadSavedCredentials() {
	root, err := spec.FindRoot(".")
	if err != nil {
		if executable, execErr := os.Executable(); execErr == nil {
			root, err = spec.FindRoot(filepath.Dir(executable))
		}
	}
	if err != nil {
		return
	}

	raw, err := os.ReadFile(filepath.Join(root, "config", "credentials.local.yaml"))
	if err != nil {
		return
	}

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		// Comments and the document marker. A "#" inside a value is left alone —
		// only a line that starts with one is a comment, and a token can
		// legitimately contain almost anything.
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "---") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" || value == "" {
			continue
		}
		if _, already := os.LookupEnv(key); already {
			continue
		}
		_ = os.Setenv(key, value)
	}
}
