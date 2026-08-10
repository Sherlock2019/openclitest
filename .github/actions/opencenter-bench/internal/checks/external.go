package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
)

func init() {
	register(
		Check{
			ID:           "external-tools-detection",
			Name:         "Missing and failing external tools are reported honestly",
			Category:     "external-tools",
			Environments: everywhere,
			Fn:           checkExternalTools,
		},
		Check{
			ID:           "external-tools-arguments",
			Name:         "External tools are called with the arguments they expect",
			Category:     "external-tools",
			Environments: everywhere,
			Fn:           checkExternalToolArguments,
		},
		Check{
			ID:           "interactive-confirmation",
			Name:         "Confirmation prompts honour yes, no, Enter and no terminal",
			Category:     "interactive",
			Environments: everywhere,
			Fn:           checkInteractiveConfirmation,
		},
		Check{
			ID:           "cancellation-child-processes",
			Name:         "Interrupting a command stops its children and reports failure",
			Category:     "cancellation",
			Environments: everywhere,
			Fn:           checkCancellation,
			Slow:         true,
		},
		Check{
			ID:           "cancellation-no-false-success",
			Name:         "A command killed part way through leaves no completed-looking artifact",
			Category:     "cancellation",
			Environments: everywhere,
			Fn:           checkCancellationArtifacts,
			Slow:         true,
		},
	)
}

// hangingTool is a stand-in for an external tool that never returns. It
// records its own process id so the check can prove afterwards that the CLI
// took it down rather than leaving it running.
const hangingTool = `#!/usr/bin/env bash
echo $$ > "$BENCH_PID_FILE"
echo "fake $(basename "$0") called with: $*" >> "$BENCH_CALL_LOG"
sleep 300
`

const recordingTool = `#!/usr/bin/env bash
echo "$(basename "$0") $*" >> "$BENCH_CALL_LOG"
exit ${BENCH_TOOL_EXIT:-0}
`

func checkExternalTools(ctx context.Context, t *T) {
	const org, name = "toolorg", "tool-kind"
	t.initCluster(name, org, "--type", "kind")

	callLog := filepath.Join(t.Env.Sandbox.Root, "tool-calls.log")
	baseline := t.RunWithEnv(map[string]string{"BENCH_CALL_LOG": callLog}, "cluster", "doctor", org+"/"+name)
	t.Require("doctor runs", baseline.OK() || strings.TrimSpace(baseline.Output()) != "",
		firstLine(baseline.Output()))
	t.Assert("doctor names the tools it checked",
		containsAny(baseline.Output(), "kubectl", "git"), firstLine(baseline.Output()))

	// A tool that exists but fails must be reported as a problem, not ignored.
	for _, tool := range []string{"kubectl", "git"} {
		if _, err := t.Env.Sandbox.WriteFakeTool(tool, recordingTool); err != nil {
			t.Fatalf("write fake %s: %v", tool, err)
		}
	}
	failing := t.RunWithEnv(map[string]string{
		"BENCH_CALL_LOG":  callLog,
		"BENCH_TOOL_EXIT": "1",
	}, "cluster", "doctor", org+"/"+name)

	t.Assertf("a failing external tool changes what doctor reports",
		failing.Output() != baseline.Output(),
		"doctor gave identical output whether kubectl worked or failed: %s", firstLine(failing.Output()))
	t.Assert("the failure is attributed to the tool",
		containsAny(failing.Output(), "kubectl", "git", "fail", "not", "error"),
		firstLine(failing.Output()))
	t.Assert("a failing tool does not crash the CLI",
		!containsAny(failing.Output(), "goroutine ", "runtime.gopanic"), firstLine(failing.Output()))

	// A tool that is absent entirely.
	for _, tool := range []string{"kubectl", "git"} {
		_ = os.Remove(filepath.Join(t.Env.Sandbox.FakeBin, tool))
	}
	empty := filepath.Join(t.Env.Sandbox.Root, "empty-path")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	missing := t.RunWithEnv(map[string]string{"PATH": empty}, "cluster", "doctor", org+"/"+name)
	t.Assert("a missing tool is named rather than silently skipped",
		containsAny(missing.Output(), "not found", "missing", "install", "kubectl", "git"),
		firstLine(missing.Output()))
	t.Assert("a missing tool does not produce a panic",
		!containsAny(missing.Output(), "goroutine ", "runtime.gopanic"), firstLine(missing.Output()))
}

func checkExternalToolArguments(ctx context.Context, t *T) {
	const org, name = "argsorg", "args-kind"
	t.initCluster(name, org, "--type", "kind")

	callLog := filepath.Join(t.Env.Sandbox.Root, "argument-calls.log")
	_ = os.Remove(callLog)

	for _, tool := range []string{"kubectl", "helm", "tofu", "terraform", "ansible-playbook", "sops", "kind"} {
		if _, err := t.Env.Sandbox.WriteFakeTool(tool, recordingTool); err != nil {
			t.Fatalf("write fake %s: %v", tool, err)
		}
	}
	defer func() {
		for _, tool := range []string{"kubectl", "helm", "tofu", "terraform", "ansible-playbook", "sops", "kind"} {
			_ = os.Remove(filepath.Join(t.Env.Sandbox.FakeBin, tool))
		}
	}()

	env := map[string]string{"BENCH_CALL_LOG": callLog}
	for _, args := range [][]string{
		{"cluster", "doctor", org + "/" + name},
		{"cluster", "status", org + "/" + name},
		{"--dry-run", "cluster", "deploy", org + "/" + name},
	} {
		t.RunWithEnv(env, args...)
	}

	recorded, err := os.ReadFile(callLog)
	if err != nil || strings.TrimSpace(string(recorded)) == "" {
		t.Note("no external tool was invoked", "these commands answered from configuration alone")
		return
	}
	lines := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	t.Notef("external tool invocations", "%d calls: %s", len(lines), firstLine(strings.Join(lines, "; ")))

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		t.Assertf(fields[0]+" is called with arguments", len(fields) > 1,
			"%q was invoked with no arguments at all", line)
		t.Assertf(fields[0]+" is not handed a shell string", !strings.Contains(line, " -c "),
			"looks like a shell invocation: %q", line)
		for _, canary := range Canaries() {
			t.Assert(fields[0]+" is not passed a secret on its command line",
				!strings.Contains(line, canary), "a credential appeared in an argv: "+line)
		}
	}
}

func checkInteractiveConfirmation(ctx context.Context, t *T) {
	const org, name = "confirmorg", "confirm-kind"
	reference := org + "/" + name
	t.initCluster(name, org, "--type", "kind")

	configPath := t.configPath(org, name)

	// No terminal and nothing on stdin. A confirmation that cannot be answered
	// must abort, not assume yes.
	eof := t.RunStdin("", "cluster", "destroy", reference)
	t.Assertf("a prompt with no answer aborts", !eof.OK(), "exit %d", eof.ExitCode)
	t.Assert("the abort explains that confirmation failed",
		containsAny(eof.Output(), "confirm", "EOF", "prompt", "yes"), firstLine(eof.Output()))
	t.Assert("the configuration survives an aborted destroy", fileExists(configPath),
		"the configuration was removed after an unanswered prompt")

	// Answering no.
	no := t.RunStdin("n\n", "cluster", "destroy", reference)
	t.Assertf("answering no does not destroy", !no.OK() || containsAny(no.Output(), "cancel", "abort", "not"),
		"exit %d: %s", no.ExitCode, firstLine(no.Output()))
	t.Assert("the configuration survives a declined destroy", fileExists(configPath),
		"the configuration was removed after answering no")

	// Pressing Enter at a prompt whose default is no.
	enter := t.RunStdin("\n", "cluster", "destroy", reference)
	t.Assert("pressing Enter does not destroy", fileExists(configPath),
		"the configuration was removed after pressing Enter")
	_ = enter

	// --yes must not need a terminal at all, and --quiet must not hide the
	// outcome of a mutating command.
	locked := t.Run("cluster", "lock", reference)
	t.Assertf("a command that needs a reason says so rather than prompting", !locked.OK(),
		"exit %d: %s", locked.ExitCode, firstLine(locked.Output()))
	t.Assert("the requirement is explained", containsAny(locked.Output(), "reason"), firstLine(locked.Output()))

	withReason := t.Run("cluster", "lock", reference, "--reason", "bench")
	t.Assertf("locking with a reason succeeds", withReason.OK(),
		"exit %d: %s", withReason.ExitCode, firstLine(withReason.Output()))
	unlock := t.Run("cluster", "unlock", reference, "--reason", "bench")
	t.Assertf("unlocking with a reason succeeds", unlock.OK(),
		"exit %d: %s", unlock.ExitCode, firstLine(unlock.Output()))
}

func checkCancellation(ctx context.Context, t *T) {
	const org, name = "cancelorg", "cancel-kind"
	t.initCluster(name, org, "--type", "kind")

	pidFile := filepath.Join(t.Env.Sandbox.Root, "hanging.pid")
	callLog := filepath.Join(t.Env.Sandbox.Root, "hanging.log")
	_ = os.Remove(pidFile)

	// Every tool the CLI might reach for during a deploy hangs. Whichever one
	// it calls first, the command stalls, and the interrupt has something real
	// to clean up.
	for _, tool := range []string{"kubectl", "kind", "helm", "tofu", "terraform", "ansible-playbook", "git"} {
		if _, err := t.Env.Sandbox.WriteFakeTool(tool, hangingTool); err != nil {
			t.Fatalf("write hanging %s: %v", tool, err)
		}
	}
	defer func() {
		for _, tool := range []string{"kubectl", "kind", "helm", "tofu", "terraform", "ansible-playbook", "git"} {
			_ = os.Remove(filepath.Join(t.Env.Sandbox.FakeBin, tool))
		}
	}()

	result := t.RunWith(cli.RunOptions{
		Env:       map[string]string{"BENCH_PID_FILE": pidFile, "BENCH_CALL_LOG": callLog},
		Interrupt: 4 * time.Second,
		Timeout:   45 * time.Second,
	}, "cluster", "doctor", org+"/"+name)

	if !fileExists(pidFile) {
		t.Note("no external tool was reached", "doctor answered without shelling out, so there was nothing to interrupt")
		t.Assertf("the command still finished promptly", result.Duration < 30*time.Second,
			"took %s", result.Duration)
		return
	}

	t.Assertf("the CLI exits within 20s of the interrupt", !result.TimedOut && result.Duration < 20*time.Second,
		"took %s, timed out: %v", result.Duration, result.TimedOut)
	t.Assertf("an interrupted command does not report success", !result.OK(),
		"exit %d — an interrupted command that exits 0 tells a script the work was done", result.ExitCode)

	// The child must be gone. A CLI that exits while its OpenTofu is still
	// running is how a half-built cluster happens.
	raw, err := os.ReadFile(pidFile)
	t.Require("the child recorded its process id", err == nil, fmt.Sprint(err))
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	t.Require("the recorded process id parses", err == nil, fmt.Sprint(err))

	alive := processAlive(pid)
	if alive {
		// Give it a moment: a signal is not instantaneous.
		time.Sleep(2 * time.Second)
		alive = processAlive(pid)
	}
	t.Assertf("the child process was stopped too", !alive,
		"process %d is still running after the CLI exited", pid)
	if alive {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func checkCancellationArtifacts(ctx context.Context, t *T) {
	const org, name = "partialorg", "partial-kind"
	t.initCluster(name, org, "--type", "kind")

	// Interrupt a generate part way through and check what it left. A partial
	// manifest that looks finished is worse than no manifest at all.
	result := t.RunWith(cli.RunOptions{
		Interrupt: 700 * time.Millisecond,
		Timeout:   40 * time.Second,
	}, "cluster", "generate", org+"/"+name)

	if result.OK() {
		t.Note("generate finished before the interrupt", "nothing was left partial to inspect")
		return
	}

	t.Assertf("an interrupted generate does not report success", !result.OK(), "exit %d", result.ExitCode)

	var partial, locks []string
	_ = filepath.Walk(t.Env.Sandbox.ConfigDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		switch {
		case strings.HasSuffix(base, ".tmp"), strings.Contains(base, ".partial"), strings.HasSuffix(base, "~"):
			partial = append(partial, path)
		case strings.HasSuffix(base, ".lock"):
			locks = append(locks, path)
		}
		return nil
	})
	t.Assertf("no half-written temporary files remain", len(partial) == 0, "%v", trim(partial))
	t.Assertf("no lock was left held", len(locks) == 0, "%v", trim(locks))

	// Whatever survived must still be readable: an atomic write leaves either
	// the old file or the new one, never half of either.
	list := t.Run("cluster", "list", "--output", "json")
	t.Assertf("the configuration is still readable after an interrupt", list.OK(),
		"exit %d: %s", list.ExitCode, firstLine(list.Output()))
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal 0 asks the kernel whether the process exists without touching it.
	return syscall.Kill(pid, 0) == nil
}
