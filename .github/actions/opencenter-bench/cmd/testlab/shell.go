package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
	"github.com/opencenter-cloud/opencli-testbench/internal/experimental"
	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
)

// detach puts a shell command in its own session, with no controlling terminal.
//
// This is what makes the "sudo has no terminal" contract true rather than
// merely assumed.
//
// Stdin = nil gives sudo EOF, and the comment in streamShell below has always
// claimed that makes it fail in milliseconds. That is only true when this
// process has no controlling terminal. Started with ./start.sh from an
// interactive shell, it inherits one — and sudo, finding stdin closed, opens
// /dev/tty instead and prompts *there*. The observed result:
//
//	the browser hangs silently for the full five-minute timeout
//	the password prompt appears in a terminal nobody clicking is looking at
//	the only way out is Ctrl-C on the server
//
// Setsid makes a new session, which has no controlling terminal to fall back
// to, so sudo fails at once with "no tty present" — which the explainer under
// streamShell already knows how to translate, in the browser, where the person
// is.
//
// It also makes the child a process group leader, so killGroup below reaches
// grandchildren. A downloader spawned by an installer used to survive the
// deadline and hold the output pipes open; WaitDelay bounded that, but leaked
// the process. Now the whole group goes.
func detach(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// killGroup signals the whole session, not just the direct child.
//
// The same negative-pid idiom internal/cli uses. CommandContext kills only the
// process it started, which for `bash -c "curl … | sh"` is the bash.
func killGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}

// runShell runs one prerequisite check.
//
// Only ever called with text the server loaded from config/prerequisites.yaml,
// never with anything from a request body. That is the whole safety argument:
// the set of shell lines this process will run is fixed at startup and visible
// in a file, so the endpoint cannot be turned into a shell by a caller.
//
// The checks are read-only by the contract at the top of that file, and the
// conformance of that claim is a matter of review rather than enforcement —
// which is exactly why the `install` commands beside them, the ones that need
// root, are shown and never run.
func runShell(
	ctx context.Context, line, dir string, env []string, redactor *redact.Redactor,
) cli.Result {
	started := time.Now()

	// A generous bound rather than none. A check that hangs — a DNS lookup
	// against an unreachable endpoint, say — must not hold the console's
	// single run slot open for the rest of the session.
	bounded, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	// -c, not -lc. A login shell sources the user's profile, which changes the
	// answer: a check would report a tool as present because .bashrc puts it
	// on PATH, in a shell the CLI under test never runs in.
	command := exec.CommandContext(bounded, "bash", "-c", line)
	command.Dir = dir
	command.Env = env

	// Nothing to read. An install script that asks a question — and several
	// do — gets EOF and fails immediately, instead of waiting for an answer
	// from a terminal that does not exist.
	command.Stdin = nil
	// And there really is no terminal, rather than one inherited from whoever
	// ran start.sh. See detach.
	detach(command)
	command.Cancel = func() error { return killGroup(command) }

	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	// Without this, the timeout does not reliably end the call.
	//
	// CommandContext kills the direct child on cancellation, but Run() then
	// waits for the output pipes to close, and any grandchild that inherited
	// them holds them open. `mise install kind` spawns a downloader; killing
	// bash leaves the downloader with the pipe, and Run() blocks past the
	// deadline indefinitely. This was measured, not theorised: the first
	// version of this hung a test past four hundred seconds on a
	// twenty-five second timeout.
	//
	// WaitDelay gives those pipes a bounded grace period and then gives up on
	// them, so the deadline is real.
	command.WaitDelay = 3 * time.Second

	err := command.Run()

	result := cli.Result{
		Stdout:   redactor.String(stdout.String()),
		Stderr:   redactor.String(stderr.String()),
		Duration: time.Since(started),
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
	} else if err != nil {
		// bash itself could not be started. Distinct from the check failing,
		// and worth saying so rather than reporting a bare non-zero exit.
		result.ExitCode = -1
		result.Stderr = redactor.String(stderr.String() + "\n" + err.Error())
	}
	if bounded.Err() == context.DeadlineExceeded {
		result.TimedOut = true
	}
	return result
}

// streamShell runs an install command and streams its output.
//
// Separate from stream() because an install is the one thing on this page that
// deliberately changes the machine, and it runs in the user's own HOME rather
// than in the throwaway sandbox — installing mise into a directory that is
// deleted at the end of the run would be an elaborate way to achieve nothing.
//
// The text is always the catalogue's, never the caller's.
func (c *console) streamShell(
	w http.ResponseWriter, r *http.Request, env, id, line string, extra []string,
) {
	c.mu.Lock()
	if c.running {
		busy := c.busy()
		c.mu.Unlock()
		http.Error(w, busy, http.StatusConflict)
		return
	}
	release := c.hold(id)
	c.mu.Unlock()
	defer release()

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	write := func(text string) {
		_, _ = io.WriteString(w, c.redactor.String(text)+"\n")
		if flusher != nil {
			flusher.Flush()
		}
	}

	write("$ " + line)
	write("")
	write("--- this runs on your machine, in your own HOME, not in the sandbox ---")
	write("")

	// Streamed as it arrives, not buffered until the end.
	//
	// This is not a nicety. Buffering meant the response went completely
	// silent for however long the command took — twenty-three seconds for the
	// openstack client — and a silent HTTP response gets dropped by the
	// Windows-to-WSL localhost relay long before it finishes. The browser
	// reported "TypeError: Failed to fetch" while the install was in fact
	// succeeding, which is the worst combination available: it worked, and the
	// page said it had not.
	//
	// curl never saw it, because curl waits. Only the browser did.
	// The bench root, so a setup command can name a script that ships with
	// the repository. The command runs in the user's HOME, which is where an
	// install belongs, so a relative path would not find it.
	runEnv := append(os.Environ(), "OPENCLI_BENCH_ROOT="+c.root)
	result := runShellStreaming(
		r.Context(), line, homeDir(), append(runEnv, extra...), c.redactor, write)
	if result.ExitCode == 0 && id == "prereq-opencenter" {
		if active := c.activateOpenCLI(extra); active != "" {
			write("")
			write("Now testing " + active)
		}
	}

	if result.Stdout == "" && result.Stderr == "" {
		write("(no output)")
	}
	write("")
	if result.TimedOut {
		write("[did not return within 25s — an install that needs longer belongs in your own terminal]")
	} else {
		write(fmt.Sprintf("[exit %d · %dms]", result.ExitCode, result.Duration.Milliseconds()))
	}
	// sudo with no terminal fails in milliseconds with a message that reads
	// like a bug in the bench unless somebody explains it. This is the one
	// failure that is expected rather than exceptional.
	combined := result.Stdout + result.Stderr
	if strings.Contains(combined, "no tty present") ||
		strings.Contains(combined, "a terminal is required") ||
		strings.Contains(combined, "askpass") {
		write("")
		write("--- what happened ---")
		write("sudo needs a password and the console has no terminal to type it into.")
		write("Copy the command above into your own terminal and run it there, then")
		write("press Run on the check to confirm. Everything that does not need root")
		write("runs from here.")
	}

	if result.ExitCode == 0 {
		write("")
		write("Now press Run on the check above to confirm it took effect.")
		// A shell that has already started will not see a new PATH entry, and
		// somebody watching a successful install followed by a failing check
		// deserves to know why before they conclude the install lied.
		write("A new tool on PATH may need a new shell before the check finds it.")
	}

	c.mu.Lock()
	c.outcomes[env+"|"+id+"|install"] = Outcome{
		Command: id + " (install)", Env: env, Args: line,
		Stdout: result.Stdout, Stderr: result.Stderr,
		ExitCode: result.ExitCode, Millis: result.Duration.Milliseconds(),
		TimedOut: result.TimedOut, At: time.Now(),
	}
	c.mu.Unlock()
}

func (c *console) activateOpenCLI(extra []string) string {
	binary := filepath.Join(homeDir(), ".local", "bin", "opencenter")
	out, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(out))
	if index := strings.IndexByte(version, '\n'); index >= 0 {
		version = version[:index]
	}
	// What was actually asked for, so the header names the fork and the branch
	// that were installed rather than the repository this bench defaults to.
	repo, release := openCLIRepository, "latest"
	branch := ""
	for _, item := range extra {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		switch key {
		case "OPENCLI_VERSION":
			release = value
		case "OPENCLI_BRANCH":
			branch = value
		case "OPENCLI_REPO":
			if value != "" {
				repo = value
			}
		}
	}
	if branch != "" {
		release = branch
	}
	if installed, readErr := os.ReadFile(binary + ".release"); readErr == nil {
		if exact := strings.TrimSpace(string(installed)); exact != "" {
			release = exact
		}
	}
	c.mu.Lock()
	c.binary = binary
	c.catalogue.Binary = binary
	c.catalogue.Version = version
	c.sourceRepo = repo
	c.sourceBranch = release
	c.mu.Unlock()
	return version + " from release " + release
}

// homeDir is where an install runs. Falls back to the working directory rather
// than failing: a missing HOME is odd but not a reason to refuse.
func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "."
}

// runShellStreaming runs a command and hands each line to emit as it appears.
//
// Same contract as runShell — the caller supplies text loaded from
// config/prerequisites.yaml, never from a request — with the output delivered
// live so the connection never goes quiet.
func runShellStreaming(
	ctx context.Context, line, dir string, env []string,
	redactor *redact.Redactor, emit func(string),
) cli.Result {
	started := time.Now()

	// Installs are slower than checks: fetching a Python toolchain took
	// twenty-three seconds on this machine, and the check timeout of
	// twenty-five would have cut it off with nothing to show for the wait.
	bounded, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	command := exec.CommandContext(bounded, "bash", "-c", line)
	command.Dir = dir
	command.Env = env
	command.Stdin = nil
	// See detach. This is the path a person presses "Run on the host" for, so
	// it is the one that was hanging on a sudo prompt nobody could see.
	detach(command)
	command.Cancel = func() error { return killGroup(command) }
	// See runShell: without this a grandchild holding the pipes outlives the
	// deadline and Run() never returns.
	command.WaitDelay = 3 * time.Second

	var stdout, stderr strings.Builder
	// No "stderr: " prefix. An installer writes its progress to stderr, so
	// prefixing every line stamped it across twenty lines of a download bar
	// and buried the two lines that mattered. A terminal does not do this and
	// neither should this; the streams interleave in the order they happened,
	// which is what somebody watching an install expects to see.
	command.Stdout = &streamWriter{emit: emit, keep: &stdout, redactor: redactor}
	command.Stderr = &streamWriter{emit: emit, keep: &stderr, redactor: redactor}

	err := command.Run()

	result := cli.Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(started),
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
	} else if err != nil {
		result.ExitCode = -1
		emit("could not start: " + err.Error())
	}
	if bounded.Err() == context.DeadlineExceeded {
		result.TimedOut = true
	}
	return result
}

// streamWriter turns writes into whole redacted lines.
//
// A partial line is held back rather than emitted, so a progress bar arriving
// in fragments does not become one output line per fragment — and so redaction
// never sees half a secret and fails to recognise it.
type streamWriter struct {
	emit     func(string)
	keep     *strings.Builder
	redactor *redact.Redactor
	prefix   string
	pending  string
}

func (w *streamWriter) Write(data []byte) (int, error) {
	w.keep.Write(data)
	w.pending += string(data)

	for {
		index := strings.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		text := w.pending[:index]
		w.pending = w.pending[index+1:]

		// A progress bar redraws itself with carriage returns, so one "line"
		// can hold thirty frames of it. A terminal shows only the last, and
		// printing all thirty turned a two-second download into forty lines of
		// hashes. Keep the final frame, which is the finished bar.
		if at := strings.LastIndexByte(text, '\r'); at >= 0 {
			text = text[at+1:]
		}
		text = strings.TrimRight(text, " \t")
		if text == "" {
			continue
		}
		w.emit(w.prefix + w.redactor.String(text))
	}
	return len(data), nil
}

// inputEnv turns the values a caller submitted into environment entries.
//
// This is the security boundary for the whole forms feature, and it works by
// allowlist: a value is accepted only for a variable the step itself declares.
// Without that, a request could set PATH, LD_PRELOAD or BASH_ENV and take over
// the shell the command runs in — the values would still never be spliced into
// the command text, and it would still be a complete compromise.
//
// Values are never interpreted. They are handed over as environment variables,
// which the commands read with ${OPENCLI_ORG:-myorg}. A cluster name of
// "; rm -rf ~" is a cluster name with an odd spelling, not a second command.
func inputEnv(command Command, submitted map[string]string, redactor *redact.Redactor) []string {
	if len(command.Inputs) == 0 || len(submitted) == 0 {
		return nil
	}

	allowed := make(map[string]experimental.Input, len(command.Inputs))
	for _, input := range command.Inputs {
		allowed[input.Env] = input
	}

	var out []string
	for name, value := range submitted {
		input, ok := allowed[name]
		if !ok {
			// Silently dropped rather than refused: a page from a slightly
			// older build may still send a field that has since been removed,
			// and failing the whole run over it helps nobody.
			continue
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		// NUL always. It terminates a C string, so an environment entry
		// containing one is truncated by execve itself — the value that arrives
		// is not the value that was sent, which is the one case that cannot be
		// made safe by being careful.
		if strings.ContainsRune(value, 0) {
			continue
		}
		// Newlines only in a secret, because a PEM private key is newlines by
		// construction and refusing them made the SSH deploy key field unusable:
		// the value was dropped in silence and the step ran as though nothing
		// had been typed.
		//
		// Safe here specifically because Go's os/exec passes each "KEY=VALUE"
		// straight to execve, where a newline is an ordinary byte. It would not
		// be safe if this env were ever serialised into a shell script or a
		// .env file, and that is the assumption to re-check if it ever is.
		if !input.Secret() && strings.ContainsAny(value, "\n\r") {
			continue
		}
		if input.Secret() {
			// Registered before the command runs, so it cannot appear in the
			// streamed output even if the command echoes it back.
			redactor.Add(value)
		}
		out = append(out, name+"="+value)
	}
	return out
}

// inputEnvFor is inputEnv for a caller that has not looked the command up yet.
func inputEnvFor(c *console, env, id string, submitted map[string]string) []string {
	command, known := c.lookupCommand(env, id)
	if !known {
		return nil
	}
	return inputEnv(command, submitted, c.redactor)
}

// lookupCommand finds a command in the loaded catalogue.
//
// The point of entry for the rule that a shell command's text comes from the
// server, not the caller.
func (c *console) lookupCommand(env, id string) (Command, bool) {
	for _, environment := range c.catalogue.Environments {
		if environment.ID != env {
			continue
		}
		for _, command := range environment.Commands {
			if command.ID == id {
				return command, true
			}
		}
	}
	return Command{}, false
}
