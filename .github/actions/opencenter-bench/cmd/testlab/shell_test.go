package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
)

// The prerequisite runners, and the one property that matters about them: a
// command they start cannot reach a terminal.
//
// These tests exist because the opposite shipped. `command.Stdin = nil` was
// taken to mean "sudo has no terminal", and it does not: sudo falls back to
// /dev/tty, which the console inherits from whoever ran ./start.sh. Pressing a
// root install button hung the browser for five minutes while a password
// prompt sat in a terminal nobody was looking at.
//
// Asserting on "sudo prompts" directly would need sudo, a password and a
// tty — none of which a test has. So the tests assert the property that makes
// the prompt impossible instead.

func TestAShellCommandCannotOpenATerminal(t *testing.T) {
	// The whole fix in one assertion. Without Setsid this inherits the
	// terminal `go test` is attached to and prints HAS_TTY.
	result := runShell(context.Background(),
		`if : < /dev/tty 2>/dev/null; then echo HAS_TTY; else echo NO_TTY; fi`,
		t.TempDir(), nil, redact.New())

	if got := strings.TrimSpace(result.Stdout); got != "NO_TTY" {
		t.Fatalf("a shell command reached a terminal (%q) — "+
			"sudo would prompt on it instead of failing", got)
	}
}

func TestAStreamingShellCommandCannotOpenATerminal(t *testing.T) {
	// The other runner, and the one the root install buttons actually use.
	var lines []string
	result := runShellStreaming(context.Background(),
		`if : < /dev/tty 2>/dev/null; then echo HAS_TTY; else echo NO_TTY; fi`,
		t.TempDir(), nil, redact.New(),
		func(line string) { lines = append(lines, line) })

	if got := strings.TrimSpace(result.Stdout); got != "NO_TTY" {
		t.Fatalf("a streamed shell command reached a terminal (%q)", got)
	}
	if len(lines) == 0 || !strings.Contains(strings.Join(lines, "\n"), "NO_TTY") {
		t.Fatalf("the output was not streamed: %v", lines)
	}
}

func TestSudoFailsAtOnceRatherThanPrompting(t *testing.T) {
	if _, err := exec.LookPath("sudo"); err != nil {
		t.Skip("sudo is not installed")
	}
	// A command that certainly needs a password would hang under the old
	// behaviour. `sudo -v` refreshes the credential and is read-only.
	//
	// Skipped when sudo is already passwordless here, because then there is no
	// prompt to avoid and the test proves nothing either way.
	if exec.Command("sudo", "-n", "true").Run() == nil {
		t.Skip("sudo is passwordless on this machine; there is no prompt to avoid")
	}

	started := time.Now()
	result := runShell(context.Background(), "sudo -v", t.TempDir(), nil, redact.New())
	elapsed := time.Since(started)

	if result.TimedOut {
		t.Fatal("sudo hung until the deadline instead of failing immediately")
	}
	// Generously bounded. The point is "immediately" versus "five minutes",
	// not a benchmark.
	if elapsed > 10*time.Second {
		t.Fatalf("sudo took %s to fail — it is waiting for a password", elapsed)
	}
	if result.ExitCode == 0 {
		t.Fatal("sudo succeeded without a password on a machine that needs one")
	}

	// And it says why, in the words streamShell's explainer looks for, so the
	// browser shows the "run it in your own terminal" advice rather than a bare
	// non-zero exit.
	combined := result.Stdout + result.Stderr
	if !strings.Contains(combined, "no tty present") &&
		!strings.Contains(combined, "a terminal is required") &&
		!strings.Contains(combined, "askpass") {
		t.Fatalf("sudo failed without a message the console can explain: %q", combined)
	}
}

func TestADeadlineReachesGrandchildren(t *testing.T) {
	// The reason killGroup signals the process group rather than the child.
	//
	// bash starts a sleep and exits; the sleep inherits the output pipes. Under
	// the old behaviour CommandContext killed the bash, the sleep kept the pipe
	// open, and only WaitDelay eventually gave up — leaving the sleep running.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	started := time.Now()
	result := runShell(ctx, "sleep 120 & wait", t.TempDir(), nil, redact.New())
	elapsed := time.Since(started)

	if elapsed > 20*time.Second {
		t.Fatalf("the deadline took %s to take effect", elapsed)
	}
	if result.Duration > 20*time.Second {
		t.Fatalf("the command ran for %s past a 500ms deadline", result.Duration)
	}
}
