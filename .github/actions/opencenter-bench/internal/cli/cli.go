// Package cli runs the openCenter binary and records exactly what happened.
//
// Everything the bench asserts on comes from here: the two output streams kept
// apart, the real process exit code rather than an approximation of it, and
// how long it took. Commands always run under a deadline, because a CLI that
// hangs is a defect the bench has to be able to report rather than a reason
// for the bench itself to hang.
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
)

// DefaultTimeout is generous for a local command and short enough that a hung
// one is reported inside a single run rather than at the end of the day.
const DefaultTimeout = 60 * time.Second

// Result is one invocation of the binary.
type Result struct {
	Args     []string      `json:"args"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration_ms"`
	TimedOut bool          `json:"timed_out"`
	Err      string        `json:"error,omitempty"`
}

// Command renders the invocation the way a person would have typed it.
func (r Result) Command() string {
	return "opencenter " + strings.Join(r.Args, " ")
}

// Output is both streams together, for checks that only care that a string
// appeared somewhere.
func (r Result) Output() string {
	return r.Stdout + r.Stderr
}

// OK reports whether the command succeeded.
func (r Result) OK() bool { return r.ExitCode == 0 && !r.TimedOut }

// Runner invokes one binary inside one sandbox.
type Runner struct {
	// Binary is the absolute path to the opencenter executable under test.
	Binary string
	// Dir is the working directory commands run from.
	Dir string
	// Env is the complete environment, usually from Sandbox.Env.
	Env []string
	// Timeout bounds every command. Zero means DefaultTimeout.
	Timeout time.Duration
	// Redactor removes secrets from everything recorded here, so a credential
	// a test supplied on purpose cannot reach an artifact. Nil disables
	// nothing important — the run always supplies one — but a zero-value
	// Runner is still usable in a unit test.
	Redactor *redact.Redactor
}

// Run invokes the binary with the given arguments.
func (r *Runner) Run(ctx context.Context, args ...string) Result {
	return r.RunWith(ctx, RunOptions{}, args...)
}

// RunOptions adjusts a single invocation.
type RunOptions struct {
	// Env are extra variables layered on top of the runner's environment.
	Env map[string]string
	// Stdin is fed to the process. An empty string closes stdin immediately,
	// which is what a prompt sees when it is run from a script.
	Stdin string
	// Timeout overrides the runner default for this one command.
	Timeout time.Duration
	// Dir overrides the working directory for this one command.
	Dir string
	// Interrupt sends SIGINT after this delay, to test cancellation. Zero
	// means the command is left alone.
	Interrupt time.Duration
}

// RunWith invokes the binary with per-command overrides.
func (r *Runner) RunWith(ctx context.Context, options RunOptions, args ...string) Result {
	timeout := options.Timeout
	if timeout == 0 {
		timeout = r.Timeout
	}
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(ctx, r.Binary, args...)
	command.Dir = r.Dir
	if options.Dir != "" {
		command.Dir = options.Dir
	}
	command.Env = mergeEnv(r.Env, options.Env)
	command.Stdin = strings.NewReader(options.Stdin)

	// Put the child in its own process group so a cancellation reaches
	// everything it spawned — tofu, ansible, kubectl — rather than only the
	// CLI, which would leave orphans behind and make the leak checks lie.
	setProcessGroup(command)

	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	start := time.Now()
	result := Result{Args: args}

	if err := command.Start(); err != nil {
		result.Err = err.Error()
		result.ExitCode = -1
		result.Duration = time.Since(start)
		return result
	}

	if options.Interrupt > 0 {
		timer := time.AfterFunc(options.Interrupt, func() {
			interrupt(command)
		})
		defer timer.Stop()
	}

	// Wait can block past the deadline even after the child is killed: a
	// grandchild that inherited the pipes — an editor, a pager — keeps them
	// open, and Wait will not return until they close. Kill the whole group
	// and give it a moment; if it still will not return, report the timeout
	// rather than wedging the run for ever.
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()

	var err error
	select {
	case err = <-waited:
	case <-ctx.Done():
		killGroup(command)
		select {
		case err = <-waited:
		case <-time.After(5 * time.Second):
			result.TimedOut = true
			result.ExitCode = -1
			result.Err = fmt.Sprintf("timed out after %s; the process would not exit", timeout)
			result.Duration = time.Since(start)
			result.Stdout = r.redact(stdout.String())
			result.Stderr = r.redact(stderr.String())
			return result
		}
	}
	result.Duration = time.Since(start)
	result.Stdout = r.redact(stdout.String())
	result.Stderr = r.redact(stderr.String())

	switch {
	case err == nil:
		result.ExitCode = 0
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.TimedOut = true
		result.ExitCode = -1
		result.Err = fmt.Sprintf("timed out after %s", timeout)
		killGroup(command)
	default:
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			result.ExitCode = exitError.ExitCode()
		} else {
			result.ExitCode = -1
			result.Err = err.Error()
		}
	}
	return result
}

func (r *Runner) redact(s string) string {
	if r.Redactor == nil {
		return s
	}
	return r.Redactor.String(s)
}

func mergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(extra))
	for _, entry := range base {
		if key, value, ok := strings.Cut(entry, "="); ok {
			merged[key] = value
		}
	}
	for key, value := range extra {
		merged[key] = value
	}
	out := make([]string, 0, len(merged))
	for key, value := range merged {
		out = append(out, key+"="+value)
	}
	return out
}

func setProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func interrupt(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	// Negative pid signals the whole process group.
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGINT)
}

func killGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}

// Locate finds the binary under test: OPENCLI_BIN, the per-user installation,
// ./bin/opencenter beside the bench, then whatever is on PATH.
func Locate(root string) (string, error) {
	if path := os.Getenv("OPENCLI_BIN"); path != "" {
		return verify(path)
	}
	candidates := []string{
		root + "/bin/opencenter",
		root + "/../opencli-benchmark/openCenter-cli/bin/opencenter",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append([]string{home + "/.local/bin/opencenter"}, candidates...)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return verify(candidate)
		}
	}
	if path, err := exec.LookPath("opencenter"); err == nil {
		return verify(path)
	}
	return "", errors.New("no opencenter binary found: install it in ~/.local/bin, set OPENCLI_BIN, put one in ./bin/opencenter, or add one to PATH")
}

func verify(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("opencenter binary %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("opencenter binary %s is a directory", path)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("opencenter binary %s is not executable", path)
	}
	return path, nil
}
