package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
)

// The one engine. Local and GitHub Actions are adapters over this, not
// reimplementations of it — the brief's central requirement, and the reason the
// phase bodies below take an Exec rather than shelling out themselves.

// Outcome is what a phase body reports.
type Outcome struct {
	State    State
	Message  string
	Detail   string
	Findings []Finding
}

// Pass and friends: the vocabulary phase bodies use.
func Pass(message string) Outcome  { return Outcome{State: StatePassed, Message: message} }
func Warn(message string) Outcome  { return Outcome{State: StateWarning, Message: message} }
func Skip(message string) Outcome  { return Outcome{State: StateSkipped, Message: message} }
func Block(message string) Outcome { return Outcome{State: StateBlocked, Message: message} }

// Fail carries a finding, because a failure without a cause is a number nobody
// can act on — and the report's whole point is the cause column.
func Fail(message string, findings ...Finding) Outcome {
	return Outcome{State: StateFailed, Message: message, Findings: findings}
}

// Body is a phase implementation.
type Body func(ctx context.Context, ex *Exec) Outcome

// Engine runs a lifecycle.
type Engine struct {
	Run      *Run
	Profile  Profile
	Redactor *redact.Redactor
	Out      io.Writer

	// Bodies maps phases to implementations. Phases with no body are reported as
	// skipped with a reason rather than silently passing — a lifecycle that
	// claims twenty-one green phases while implementing nine is worse than one
	// that admits what it does.
	Bodies map[ID]Body

	// CLIRepo is where the openCenter source lives; CLIBinary is the built
	// binary once phase 3 has produced one.
	CLIRepo   string
	CLIBinary string
	MisePath  string

	// APIServerPort is set by phase 2 when 6443 is already taken, and applied by
	// phase 5. Zero means the configuration's own default is left alone.
	APIServerPort int

	// Rerun names phases that must run even though a previous execution already
	// recorded a result for them.
	//
	// Set by `e2e phase`, which exists to run a named phase again. Empty for
	// `run` and for `resume`, where an earlier result is exactly the thing that
	// should be honoured — a resume that repeated the phases it had already
	// finished would rebuild the CLI and redeploy the cluster.
	Rerun map[ID]bool

	// startedAt separates this execution's phase results from those a previous
	// one left behind, which is how a resume knows what it may skip.
	startedAt time.Time
}

// Exec is what a phase body is handed: somewhere to run commands, somewhere to
// write files, and the run to register resources on.
type Exec struct {
	Engine   *Engine
	Run      *Run
	Profile  Profile
	Phase    Phase
	Redactor *redact.Redactor

	commands *[]Command
}

// Dir returns a directory inside the run root, creating it.
func (e *Exec) Dir(parts ...string) string {
	path := filepath.Join(append([]string{e.Run.Root}, parts...)...)
	_ = os.MkdirAll(path, 0o755)
	return path
}

// Write puts evidence in the run directory, redacted.
func (e *Exec) Write(relative string, content []byte) error {
	path := filepath.Join(e.Run.Root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(e.Redactor.String(string(content))), 0o600)
}

// Environment is the isolated environment every command runs in.
//
// HOME included, and pointed at the run directory. The brief is emphatic that
// the engineer's own openCenter configuration is never touched, and the only way
// to guarantee that against a CLI that writes to ~/.config is to move HOME.
func (e *Exec) Environment() []string {
	home := filepath.Join(e.Run.Root, "home")
	env := append(os.Environ(),
		"HOME="+home,
		"USERPROFILE="+home,
		"OPENCENTER_CONFIG_DIR="+filepath.Join(e.Run.Root, "config"),
		"OPENCENTER_STATE_DIR="+filepath.Join(e.Run.Root, "state"),
	)

	// The container runtime, said once so every code path agrees.
	//
	// The CLI repository's .mise.toml pins CONTAINER_RUNTIME=podman. This
	// machine has docker, and the bench already passes --container-runtime
	// docker to `cluster deploy` — but that flag reaches the deploy command,
	// not the gitea service built inside its bootstrap step, which takes
	// DefaultSettings(runtime) from the environment.
	//
	// So the container was created with docker and deploy asked podman whether
	// it was running. `Status().Running` is a container inspect, not a state
	// file, so it answered no in a third of a second and reported "local gitea
	// is not running" while gitea was up and serving. Three days of the Kind
	// profile stopping at step 3 of 8.
	//
	// Set from what is actually installed rather than from what a config file
	// wishes for, and only when the environment has not already chosen.
	if os.Getenv("CONTAINER_RUNTIME") == "" {
		if runtime := containerRuntime(); runtime != "" {
			env = append(env, "CONTAINER_RUNTIME="+runtime)
		}
	}
	// Go's caches stay outside the run directory.
	//
	// The isolation this phase owes is openCenter's: the engineer's own cluster
	// configuration must never be read or written. Go's build and module caches
	// are neither, and letting them follow HOME into the run directory put
	// 31,476 files there on the first real run — which broke `go build ./...`
	// in this very repository, made the artifact manifest meaningless, and made
	// every build download the world again.
	if cache := goCacheRoot(); cache != "" {
		env = append(env,
			"GOCACHE="+filepath.Join(cache, "go-build"),
			"GOMODCACHE="+filepath.Join(cache, "go-mod"),
		)
	}

	// A git identity, because HOME moved and took ~/.gitconfig with it.
	//
	// Deploy's gitea-rebase step commits the GitOps checkout, and git refuses to
	// commit without an author. The engineer has an identity configured; the run
	// cannot see it, because isolating openCenter's configuration means isolating
	// the whole of HOME. Step 6 of 8 failed with "Author identity unknown" and
	// advice to run `git config --global`, which is exactly what a bench must not
	// do — the machine's git configuration is not the bench's to change.
	//
	// Environment variables instead: they last as long as the command does, are
	// visible in the evidence, and name the run rather than impersonating anyone.
	// Only set when the caller has not, so CI can supply its own.
	if os.Getenv("GIT_AUTHOR_NAME") == "" {
		identity := "openCenter E2E Test Bench"
		address := "e2e-testbench@opencenter.invalid"
		env = append(env,
			"GIT_AUTHOR_NAME="+identity,
			"GIT_AUTHOR_EMAIL="+address,
			"GIT_COMMITTER_NAME="+identity,
			"GIT_COMMITTER_EMAIL="+address,
		)
	}

	// mise keeps its tools where the operator installed them, not under the
	// moved HOME.
	//
	// Two problems in one. A moved HOME made mise re-download Go, kubectl, kind,
	// helm, govulncheck and gitleaks into every run — 15,078 files, minutes of
	// downloading before a phase did any work. And pointing it at a private
	// directory instead was worse: `mise install kind` puts kind in the real
	// store, so the run would not have found the tool the operator had just
	// installed for it.
	paths := []string{}
	if realHome := realHomeDir(); realHome != "" {
		env = append(env,
			"MISE_DATA_DIR="+filepath.Join(realHome, ".local", "share", "mise"),
			"MISE_CACHE_DIR="+filepath.Join(realHome, ".cache", "mise"),
			"MISE_STATE_DIR="+filepath.Join(realHome, ".local", "state", "mise"),
		)
		// The real install directories, not mise's shims.
		//
		// A shim re-enters mise, which parses whatever .mise.toml it finds in the
		// working directory and refuses if that file is untrusted. This
		// repository has one — so the CLI's own `kind get clusters` failed with
		// a message about trust, and `cluster destroy` reported that it could not
		// destroy the cluster. A test bench whose config file breaks the tool
		// under test has stopped testing anything.
		installs := filepath.Join(realHome, ".local", "share", "mise", "installs")
		if entries, err := os.ReadDir(installs); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				candidate := filepath.Join(installs, entry.Name(), "latest")
				if info, err := os.Stat(candidate); err == nil && info.IsDir() {
					paths = append(paths, candidate)
					// Some tools install into a bin/ subdirectory instead.
					if bin := filepath.Join(candidate, "bin"); dirExists(bin) {
						paths = append(paths, bin)
					}
				}
			}
		}
	}
	if e.Engine.MisePath != "" {
		paths = append(paths, filepath.Dir(e.Engine.MisePath))
	}
	if len(paths) > 0 {
		env = append(env, "PATH="+strings.Join(paths, ":")+":"+os.Getenv("PATH"))
	}
	return env
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// realHomeDir is the operator's home, read before HOME is moved for isolation.
func realHomeDir() string {
	if cached := os.Getenv("OPENCLI_E2E_REAL_HOME"); cached != "" {
		return cached
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// goCacheRoot is a stable place for Go's caches, shared across runs.
func goCacheRoot() string {
	if explicit := os.Getenv("OPENCLI_E2E_GO_CACHE"); explicit != "" {
		return explicit
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "opencli-e2e")
}

// Command runs one invocation and records it.
//
// Every command a phase runs goes through here, so that the evidence is complete
// by construction rather than by each phase remembering to append to a list.
func (e *Exec) Command(ctx context.Context, dir string, argv ...string) Command {
	started := time.Now()
	record := Command{Argv: argv, Dir: dir}

	if len(argv) == 0 {
		record.ExitCode = -1
		record.Stderr = "no command given"
		*e.commands = append(*e.commands, record)
		return record
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = e.Environment()
	stdout := &strings.Builder{}
	stderr := &strings.Builder{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	record.Millis = time.Since(started).Milliseconds()
	record.Stdout = e.Redactor.String(stdout.String())
	record.Stderr = e.Redactor.String(stderr.String())
	record.Argv = e.Redactor.Strings(argv)

	if ctx.Err() == context.DeadlineExceeded {
		record.TimedOut = true
	}
	if err != nil {
		record.ExitCode = 1
		var exitError *exec.ExitError
		if ok := asExitError(err, &exitError); ok {
			record.ExitCode = exitError.ExitCode()
		}
	}
	*e.commands = append(*e.commands, record)
	return record
}

func asExitError(err error, target **exec.ExitError) bool {
	if exitError, ok := err.(*exec.ExitError); ok {
		*target = exitError
		return true
	}
	return false
}

// CLI runs the openCenter binary under test.
func (e *Exec) CLI(ctx context.Context, args ...string) Command {
	if e.Engine.CLIBinary == "" {
		return Command{Argv: append([]string{"opencenter"}, args...), ExitCode: -1,
			Stderr: "no openCenter binary has been built or discovered yet"}
	}
	return e.Command(ctx, e.Run.Root, append([]string{e.Engine.CLIBinary}, args...)...)
}

// Log writes a line to the operator's terminal and the run log.
func (e *Exec) Log(format string, args ...any) { e.Engine.logf(format, args...) }

func (g *Engine) logf(format string, args ...any) {
	if g.Out == nil {
		return
	}
	fmt.Fprintf(g.Out, format+"\n", args...)
}

// Execute runs the selected phases in order.
//
// The shape of the loop is the design: dependencies decide whether a phase runs
// at all, a bad state stops the ordinary phases, and the always-run phases go
// ahead regardless — because a run that fell over is exactly when diagnostics and
// destroy matter most.
func (g *Engine) Execute(ctx context.Context, selected []ID) error {
	wanted := map[ID]bool{}
	for _, id := range selected {
		wanted[id] = true
	}

	stopped := false
	for _, phase := range Order {
		if !wanted[phase.ID] {
			continue
		}
		result := g.Run.Result(phase.ID)

		// Already done, on a resume.
		if result.State.Terminal() && result.State != StateNotStarted && g.resumed(phase.ID) {
			g.logf("  %-20s %s (from the earlier run)", phase.ID, result.State)
			continue
		}

		if stopped && !isAlways(phase.ID) {
			result.State = StateSkipped
			result.Message = "an earlier phase stopped the run"
			_ = g.Run.Save()
			continue
		}

		if missing := Unsatisfied(phase, g.Run.States()); len(missing) > 0 && !isAlways(phase.ID) {
			result.State = StateBlocked
			result.Message = "needs " + joinIDs(missing)
			g.logf("  %-20s BLOCKED  %s", phase.ID, result.Message)
			_ = g.Run.Save()
			continue
		}

		body, ok := g.Bodies[phase.ID]
		if !ok {
			result.State = StateSkipped
			result.Message = "not implemented yet"
			g.logf("  %-20s skipped  not implemented yet", phase.ID)
			_ = g.Run.Save()
			continue
		}

		result.State = StateRunning
		result.Started = time.Now().UTC()
		_ = g.Run.Save()
		g.logf("  %-20s running…", phase.ID)

		exec := &Exec{Engine: g, Run: g.Run, Profile: g.Profile, Phase: phase,
			Redactor: g.Redactor, commands: &result.Commands}
		outcome := body(ctx, exec)

		result.Ended = time.Now().UTC()
		result.Millis = result.Ended.Sub(result.Started).Milliseconds()
		result.State = outcome.State
		result.Message = outcome.Message
		result.Detail = outcome.Detail
		// Every finding learns how to reproduce itself, here rather than in each
		// phase body. Twenty-one phases each remembering to fill this in is
		// twenty-one chances to forget, and the one that forgets is the one
		// somebody needed.
		for index := range outcome.Findings {
			if outcome.Findings[index].Reproduce == "" {
				id := outcome.Findings[index].Phase
				if id == "" {
					id = phase.ID
				}
				outcome.Findings[index].Reproduce =
					ReproduceCommand(g.Run.ID, g.Run.Profile, id)
			}
		}
		result.Findings = append(result.Findings, outcome.Findings...)
		_ = g.Run.Save()

		g.logf("  %-20s %-8s %s", phase.ID, strings.ToUpper(string(outcome.State)), outcome.Message)

		if outcome.State.Bad() {
			stopped = true
			// Keeping a failed environment is a deliberate choice and has to be
			// asked for; the default is that a failed run cleans up after itself.
			if g.Run.KeepOnFail {
				g.logf("  keeping the environment: --keep-on-failure was given")
			}
		}
	}

	g.Run.Ended = time.Now().UTC()
	return g.Run.Save()
}

// resumed reports whether this phase's state came from a previous run rather
// than from this one.
//
// A phase the caller named is never treated as resumed. `e2e phase --only-phase
// destroy` did nothing at all: destroy had a recorded state from the failed run,
// so the engine printed
//
//	destroy   blocked (from the earlier run)
//
// and moved on, leaving the two resources exactly where they were. That is the
// right behaviour for `resume`, which means "carry on from where you stopped",
// and the opposite of what `phase` means — somebody naming a phase is naming it
// because they want it run again, usually having just installed the tool that
// blocked it the first time.
//
// It is also the command printed as the Reproduce line on every finding this
// bench produces. A reproduce line that replays a stored verdict instead of
// reproducing anything is worse than none: it agrees with the report, which
// reads as confirmation.
func (g *Engine) resumed(id ID) bool {
	if g.Rerun[id] {
		return false
	}
	result := g.Run.Result(id)
	return result != nil && !result.Started.IsZero() && result.Started.Before(g.startedAt)
}

func (g *Engine) SetStart(at time.Time) { g.startedAt = at }

func joinIDs(ids []ID) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return strings.Join(out, ", ")
}
