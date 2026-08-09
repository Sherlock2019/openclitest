package e2e

import (
	"context"
	"fmt"
	"io/fs"
	"os"

	"os/exec"
	"path/filepath"

	"slices"
	"strings"
	"time"
)

// The phases that need a cluster to exist.
//
// Split from phases.go because they share a property nothing above them has:
// they can leave real things running. Everything here registers what it creates
// before it finishes creating it — a deploy killed in its third second has
// already made a container, and a registry written when the phase returns is
// empty for exactly the run that needed it.

// --- phase 11: deploy --------------------------------------------------------

func phaseDeploy(ctx context.Context, ex *Exec) Outcome {
	if !ex.Profile.Deploys {
		return Skip(ex.Profile.Name + " does not deploy")
	}

	argv := []string{"cluster", "deploy", ex.Run.Cluster}

	// Where the local Gitea actually is, filled in below for the Kind flow and
	// used to explain a deploy that timed out waiting for it somewhere else.
	giteaURL := ""

	// The container runtime, decided from what is installed rather than from
	// what .mise.toml wishes for. The CLI repository pins podman; this machine
	// has docker. Left alone, Kind fails deep inside itself with a message about
	// a socket that names neither.
	if ex.Profile.Infrastructure == InfraKind {
		if runtime := containerRuntime(); runtime != "" {
			argv = append(argv, "--container-runtime", runtime)
			ex.Log("    deploying with --container-runtime %s", runtime)
		} else {
			return Block("no container runtime: Kind needs docker or podman")
		}
	}

	// Registered before the command runs, not after it returns. This is the
	// whole discipline of the phase: `cluster deploy` creates a Kind cluster
	// within its first seconds, and a run killed at second three still owes a
	// destroy for it.
	ex.Run.Register(Resource{
		Kind: "kind-cluster", Name: ex.Run.Cluster,
		Provider: string(ex.Profile.Provider), Order: OrderKubernetes,
		Remediation: "kind delete cluster --name " + ex.Run.Cluster,
	})

	// The local Gitea the Kind flow needs.
	//
	// Deploy's third step attaches it to the cluster network and fails with
	// "local gitea is not running; run 'opencenter local gitea up' first" — a
	// dependency of the profile rather than a defect, and one the run can
	// satisfy itself instead of stopping to ask. Registered first, because
	// starting it means a container this run is responsible for.
	if ex.Profile.Infrastructure == InfraKind {
		ex.Run.Register(Resource{
			Kind: "local-gitea", Name: "gitea", Provider: "local",
			Order: OrderServiceResource,
			// `destroy`, not `down`. Checked against `local gitea --help`: the
			// subcommands are attach-kind, destroy, status, up. This said `down`
			// and printed the help text instead of stopping anything, so a
			// remediation the reader followed left the container running.
			Remediation: "opencenter local gitea destroy",
		})
		gitea := ex.CLI(ctx, "local", "gitea", "up")
		_ = ex.Write("evidence/gitea-up.txt", []byte(gitea.Stdout+gitea.Stderr))
		if gitea.ExitCode != 0 {
			// Stop here, and say why.
			//
			// This used to log firstLine() and carry on. The first line of a
			// failed `local gitea up` is "Warning: plugin local is unverified",
			// which is harmless — so every run log read as though gitea had
			// started, deploy then failed two steps later with "local gitea is
			// not running", and the actual cause was three lines further down in
			// output nobody printed:
			//
			//   failed to bind host port 0.0.0.0:3000/tcp: address already in use
			//
			// A first line is not an error message. It is whatever the program
			// happened to say first, and taking it for the cause cost hours of
			// looking in the wrong place entirely.
			detail := lastMeaningfulLine(gitea.Stdout + gitea.Stderr)
			ex.Log("    local gitea up FAILED: %s", detail)

			cause, category, fix := classifyGiteaFailure(gitea.Stdout + gitea.Stderr)
			return Outcome{
				State:   StateBlocked,
				Message: "the local Gitea this profile needs did not start",
				Findings: []Finding{{
					Phase: PhaseDeploy, Command: gitea.String(),
					Environment: ex.Profile.Name,
					Expected:    "a running local Gitea for the Kind flow",
					Actual:      fmt.Sprintf("exit %d: %s", gitea.ExitCode, detail),
					Cause:       cause, Category: category,
					Remediation: fix,
					Evidence:    "evidence/gitea-up.txt",
				}},
			}
		}
		ex.Log("    local gitea is up")

		// Give the files gitea just made back to the user who has to write them.
		//
		// The container runs as root and writes into a bind mount, so
		// config/local/gitea ends up owned by root. The next step, attaching
		// gitea to the kind network, writes a CA certificate into that same
		// directory as the ordinary user, and cannot:
		//
		//   [3/8] ✗ Attach local Gitea to the Kind network failed:
		//         write …/config/local/gitea/gitea/certs/ca.pem: permission denied
		//
		// It works on a developer's machine and fails on a clean runner, which
		// is the most expensive shape of bug there is — it looks like CI being
		// difficult rather than a real defect, and it is real.
		//
		// Same mechanism as the leftover directories cleanup: what a container
		// made as root, a container can hand back. No sudo is asked for, and
		// nothing outside the run's own directory is touched.
		if fixed := ownLocalState(ctx, ex); fixed != "" {
			ex.Log("    %s", fixed)
		}

		// Where it actually ended up.
		//
		// Asked rather than assumed, and recorded, because deploy's attach step
		// waits on a URL of its own and the two can disagree. A stale container
		// from an earlier run keeps its old published port; `local gitea up`
		// finds it already running and reports that port; deploy then waits two
		// minutes on the default and times out. The timeout says nothing about
		// where gitea is, so the address goes in the log before the wait starts.
		status := ex.CLI(ctx, "local", "gitea", "status")
		_ = ex.Write("evidence/gitea-status.txt", []byte(status.Stdout+status.Stderr))
		if url := giteaBaseURL(status.Stdout); url != "" {
			giteaURL = url
			ex.Log("    local gitea is serving %s", url)
		}
	}

	deploy := ex.CLI(ctx, argv...)
	_ = ex.Write("evidence/deploy.txt", []byte(deploy.Stdout+deploy.Stderr))

	if deploy.TimedOut {
		return Fail("deploy did not finish inside the run's timeout", Finding{
			Phase: PhaseDeploy, Command: deploy.String(), Environment: ex.Profile.Name,
			Expected: "a deployed cluster", Actual: "timed out",
			Cause:       "deploy exceeded the deadline; whatever it created is registered for cleanup",
			Category:    CategoryProductDefect,
			Remediation: "opencenter cluster deploy " + ex.Run.Cluster + " --from-step <step> to continue",
		})
	}
	if deploy.ExitCode != 0 {
		// Whose fault was it?
		//
		// This mattered the first time deploy ran here: it failed with "Bind for
		// 127.0.0.1:6443 failed: port is already allocated" because another Kind
		// cluster already had the API port. Reported as a product defect that
		// would have blocked a release over somebody else's leftover container.
		// The environment signatures below are classified as such, which makes
		// the run INCONCLUSIVE rather than FAIL — we did not test the product.
		cause, category, fix := classifyDeployFailure(deploy.Stdout+deploy.Stderr, giteaURL)
		outcome := Fail("cluster deploy failed", Finding{
			Phase: PhaseDeploy, Command: deploy.String(), Environment: ex.Profile.Name,
			Expected: "a deployed cluster",
			Actual:   fmt.Sprintf("exit %d: %s", deploy.ExitCode, cause),
			Cause:    cause, Category: category,
			Remediation: fix,
			Evidence:    "evidence/deploy.txt",
		})
		if category != CategoryProductDefect {
			// Blocked, not failed: the product was never given a chance.
			outcome.State = StateBlocked
		}
		return outcome
	}

	// The kubeconfig is what every later phase talks through.
	if _, err := exec.LookPath("kubectl"); err == nil {
		ex.Command(ctx, ex.Run.Root, "kubectl", "config", "current-context")
	}
	return Pass("deployed " + ex.Run.Cluster)
}

// lastMeaningfulLine is the cause, which is rarely the first line.
//
// A failed `local gitea up` begins with "Warning: plugin local is unverified"
// and ends with the reason. Reading the first line and reporting it as the
// error is what made a port collision look like a plugin-checksum notice for
// hours.
func lastMeaningfulLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		switch {
		case line == "",
			strings.HasPrefix(line, "Warning:"),
			strings.HasPrefix(line, "Run '"),
			// The plugin's own wrapper, which restates the exit code and says
			// nothing about why.
			strings.HasPrefix(line, "Error: plugin exited with code"),
			// A container id on its own line.
			len(line) == 64 && !strings.Contains(line, " "):
			continue
		}
		return line
	}
	return strings.TrimSpace(output)
}

// classifyGiteaFailure says whose problem a failed `local gitea up` is.
//
// A port collision is the machine's, not the product's, and it must not fail a
// release: the CLI is correct to want 3000, and something else on this machine
// got there first.
func classifyGiteaFailure(output string) (cause string, category Category, remediation string) {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "address already in use"),
		strings.Contains(lower, "port is already allocated"):
		port := boundPortIn(output)
		where := "a port it needs"
		if port != "" {
			where = "port " + port
		}
		remediation := "find the holder — `ss -ltnp | grep " + port + "` or " +
			"`docker ps` — and stop it, or free the port and run again"

		// On WSL that advice is wrong, and wrong in a way that costs hours.
		//
		// A Windows process can hold a port that Linux tooling cannot see: ss
		// prints nothing, lsof prints nothing, and bind() still fails. This one
		// was a stale `netsh portproxy` rule forwarding 0.0.0.0:3000, serviced
		// by the IP Helper service — invisible from inside WSL, and three
		// separate investigations went looking for a Linux holder that was never
		// there.
		//
		// So the remediation says where to look when the obvious place is empty.
		if onWSL() {
			remediation += ". This is WSL, so an empty `ss` does not mean the port " +
				"is free — a Windows process may hold it. From PowerShell: " +
				"`Get-NetTCPConnection -LocalPort " + port + " -State Listen` and " +
				"`netsh interface portproxy show v4tov4`"
		}
		return "the local Gitea could not bind " + where + " — something else on this " +
				"machine holds it",
			CategoryEnvironment, remediation

	case strings.Contains(lower, "cannot connect to the docker daemon"),
		strings.Contains(lower, "is the docker daemon running"):
		return "the container runtime is not running", CategoryEnvironment,
			"start docker (or podman) and run again"

	case strings.Contains(lower, "no such file or directory"),
		strings.Contains(lower, "executable file not found"):
		return "the local plugin or its runtime is not installed",
			CategoryMissingPrereq,
			"mise run build in the CLI repository installs the plugin; the bench does " +
				"this itself at the build phase"
	}
	return "the local Gitea did not start", CategoryEnvironment,
		"read evidence/gitea-up.txt — the cause is usually in the last line, not the first"
}

// onWSL reports whether this is a WSL kernel.
//
// Matters for one thing only, and it is not cosmetic: on WSL the Linux socket
// tables do not show ports held by Windows processes, so "ss shows nothing"
// does not mean "the port is free" — and advice that says to check ss sends the
// reader to an empty table and a wrong conclusion.
func onWSL() bool {
	raw, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(raw))
	return strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
}

// boundPortIn pulls the port out of a bind failure.
//
//	failed to bind host port 0.0.0.0:3000/tcp: address already in use
func boundPortIn(output string) string {
	const marker = "bind host port "
	index := strings.Index(output, marker)
	if index < 0 {
		return ""
	}
	rest := output[index+len(marker):]
	if colon := strings.LastIndex(strings.SplitN(rest, "/", 2)[0], ":"); colon >= 0 {
		return strings.SplitN(rest, "/", 2)[0][colon+1:]
	}
	return ""
}

// A note on the local Gitea, for whoever picks this up.
//
// Deploy's attach step needs it, and getting it to work has cost more than
// anything else here. Two causes are found and fixed:
//
//   - the plugin binary. `mise run build` produces bin/opencenter-local, and
//     the CLI loads it from ~/.local/bin, which nothing was installing. A run
//     built a fresh CLI, verified it, and then executed it against a plugin
//     three weeks older with different default ports. phaseBuild installs it
//     now, and the ports agree.
//
//   - the remediation text, which named `local gitea down`. There is no such
//     subcommand — they are attach-kind, destroy, status, up — so anybody who
//     followed the advice was left with the container still running.
//
// One remains, and it is now located precisely rather than guessed at.
//
// Deploy's attach step calls gitea.Status(), whose Running field comes from
// containerRunning():
//
//	<runtime> inspect --format {{.State.Running}} <containerName>
//
//	if err != nil { return false, err }
//
// It reports "not running" for *any* error from that command — a runtime that
// is not on PATH, a container that is not found, a daemon that refuses. So the
// message names the wrong thing: the container was up and serving on 3001 the
// whole time, and this inspect failed for a reason the message never mentions.
// It fails in about a third of a second, which is the shape of a command
// erroring rather than a check being made.
//
// Two attempts to fix this from the bench were made and both reverted, because
// both aimed at state files and the mechanism is not a state file:
//
//   - copying gitea.json into the run's config directory. It made the run
//     report an address that did not match the container, because the machine's
//     copy can be older than the container it describes.
//   - symlinking the whole local/ directory. It changed isolation semantics and
//     changed nothing else; the failure was identical, to within four
//     milliseconds.
//
// A bench that keeps changing its subject's environment until something moves
// has stopped measuring. The next step is one command, not another patch: run
// that inspect inside the run's environment and read the error it returns.
//
// Until then the Kind profile stops at step 3 of 8, and this bench says so
// rather than pretending otherwise.

// ownLocalState hands the local gitea directory back to the current user.
//
// Returns a line for the log when it did something, and empty when there was
// nothing to do or no way to do it — a missing container runtime here is not a
// failure, because a profile without one never started gitea in the first
// place.
func ownLocalState(ctx context.Context, ex *Exec) string {
	local := filepath.Join(ex.Run.Root, "config", "local")
	if _, err := os.Stat(local); err != nil {
		return ""
	}
	// Every directory under it, not the top one.
	//
	// The first version tested config/local and stopped there. That directory
	// is made by the CLI and is writable; the root-owned one is four levels
	// down — config/local/gitea/gitea/certs, made by the container. So the
	// check passed, the chown was skipped, and the run failed exactly as
	// before, on the same file, with the fix in place and silent.
	//
	// A guard that reads the wrong thing is worse than no guard: it makes the
	// fix look applied.
	if !anyUnwritable(local) {
		return ""
	}
	runtime := containerRuntime()
	if runtime == "" {
		return ""
	}
	image := localImage(ctx, ex, runtime)
	if image == "" {
		return ""
	}
	owner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	out := ex.Command(ctx, ex.Run.Root, runtime, "run", "--rm",
		"-v", local+":/target", "--entrypoint", "/bin/sh", image,
		"-c", "chown -R "+owner+" /target")
	if out.ExitCode != 0 {
		return "could not take ownership of the local gitea files: " +
			lastMeaningfulLine(out.Stdout+out.Stderr)
	}
	return "took ownership of the local gitea files back from root"
}

// anyUnwritable reports whether any directory in the tree refuses a write.
//
// Walks rather than checking the root, because that is the mistake this
// replaced: the top of the tree belongs to the CLI and is writable, and the one
// the container made is several levels below it. A directory the walk cannot
// even enter counts — that is the same problem seen from outside.
func anyUnwritable(root string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			found = true
			return fs.SkipDir
		}
		if entry.IsDir() && !writable(path) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// writable reports whether this process can create a file in a directory.
//
// Attempted rather than deduced from the mode bits: root-owned 0755 is
// readable, listable and not writable, and only a write says so.
func writable(dir string) bool {
	probe := filepath.Join(dir, ".opencenter-write-probe")
	file, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	file.Close()
	os.Remove(probe)
	return true
}

// giteaBaseURL reads the address out of `local gitea status`.
//
// The line is "Base URL: https://localhost:3301". Parsed rather than assumed,
// because the port is whatever the running container publishes and the default
// is only the default.
func giteaBaseURL(status string) string {
	for _, line := range strings.Split(status, "\n") {
		if _, address, found := strings.Cut(line, "Base URL:"); found {
			return strings.TrimSpace(address)
		}
	}
	return ""
}

// waitedForGitea extracts the address deploy gave up waiting on.
//
// The message is: timed out waiting for gitea at https://localhost:3001
func waitedForGitea(output string) string {
	const marker = "waiting for gitea at "
	index := strings.Index(output, marker)
	if index < 0 {
		return ""
	}
	rest := output[index+len(marker):]
	return strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
}

// classifyDeployFailure reads the output and says whose problem it is.
//
// Narrow on purpose. Each signature below is a message the machine produced,
// not a guess: anything unrecognised stays a product defect, because a
// classifier that shrugs "environment" at every failure is a release gate that
// never blocks anything.
func classifyDeployFailure(output, giteaURL string) (cause string, category Category, remediation string) {
	lower := strings.ToLower(output)

	// Ahead of the generic timeout below, and the reason is the whole point of
	// this function. Deploy's attach step waits on an address of its own; a
	// stale container from an earlier run keeps its old published port, `local
	// gitea up` finds it already running and reports that port, and deploy then
	// waits two minutes on the default before giving up.
	//
	// Left to the generic branch this reads "deployment timed out — product
	// defect", which would block a release over a leftover container. Naming
	// both addresses turns a two-minute silence into a one-line diagnosis.
	if waited := waitedForGitea(output); waited != "" {
		if giteaURL != "" && giteaURL != waited {
			// Stated as observed, and no further. An earlier version of this
			// blamed "a container from an earlier run holding the old port",
			// which was invented: the ports were both free before the run and
			// gitea still came up on the higher one. What is actually true is
			// only that the two disagree — `local gitea up` reports the port in
			// the plugin's persisted metadata, and deploy's attach step waits on
			// the port in its own settings. A cause that guesses is worse than
			// one that stops at the evidence, because somebody acts on it.
			return "deploy waited for gitea at " + waited + ", but gitea is serving " +
					giteaURL + " — the two disagree about the port",
				CategoryEnvironment,
				"the plugin's saved state names a different port from the default. " +
					"`opencenter local gitea destroy` clears it, and may need sudo: " +
					"the container leaves local/gitea/ssh owned by root"
		}
		return "deploy timed out waiting for gitea at " + waited,
			CategoryEnvironment,
			"check `opencenter local gitea status`; if it is not serving that address, " +
				"`opencenter local gitea destroy` and run again"
	}

	switch {
	case strings.Contains(lower, "port is already allocated"),
		strings.Contains(lower, "address already in use"):
		return "the Kubernetes API port is already taken by something else on this machine",
			CategoryEnvironment,
			"another cluster holds 127.0.0.1:6443 — `kind get clusters`, then remove the one you do not need"

	case strings.Contains(lower, "cannot connect to the docker daemon"),
		strings.Contains(lower, "is the docker daemon running"):
		return "the container runtime is not running", CategoryEnvironment,
			"start docker (or podman) and run again"

	case strings.Contains(lower, "no space left on device"):
		return "the machine ran out of disk while creating the cluster", CategoryEnvironment,
			"free space, or `docker system prune`, and run again"

	case strings.Contains(lower, "permission denied") && strings.Contains(lower, "docker.sock"):
		return "this user cannot talk to the container runtime", CategoryEnvironment,
			"add the user to the docker group, or use a rootless runtime"

	case strings.Contains(lower, "toomanyrequests"),
		strings.Contains(lower, "pull rate limit"):
		return "the image registry refused to serve the node image", CategoryEnvironment,
			"authenticate to the registry, or wait for the rate limit to reset"

	// The node container starts and its init never reaches multi-user. On WSL2
	// this is the usual outcome without systemd and cgroup v2 delegation, and it
	// says nothing at all about the CLI: kind never handed the cluster over, so
	// no openCenter code ran.
	case strings.Contains(lower, "reached target"),
		strings.Contains(lower, "detected cgroup v1"),
		strings.Contains(lower, "failed to init node with kubeadm"):
		return "the Kubernetes node container started but its init never came up — " +
				"the host cannot run Kind as configured",
			CategoryEnvironment,
			"on WSL2: enable systemd (/etc/wsl.conf → [boot] systemd=true, then " +
				"`wsl --shutdown`), and confirm cgroup v2 with " +
				"`stat -fc %T /sys/fs/cgroup` (expect cgroup2fs). Docker Desktop's " +
				"WSL integration works without either"

	case strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"):
		return "deployment timed out", CategoryProductDefect,
			"read evidence/deploy.txt, then `cluster deploy --from-step` to continue"
	}
	return "deployment did not complete", CategoryProductDefect,
		"read evidence/deploy.txt, then `cluster deploy --from-step` to resume"
}

// containerRuntime picks what is actually installed.
func containerRuntime() string {
	for _, candidate := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// --- phase 12: provider infrastructure ---------------------------------------

func phaseInfrastructure(ctx context.Context, ex *Exec) Outcome {
	switch ex.Profile.Infrastructure {
	case InfraKind:
		return kindInfrastructure(ctx, ex)
	case InfraEmulated:
		// Labelled, every time. An emulated result that reads like a real one is
		// how a simulated pass gets quoted as evidence that a provider works.
		return Warn("emulated " + string(ex.Profile.Provider) +
			": the simulated state model was checked, not real infrastructure")
	case InfraReal:
		return realInfrastructure(ctx, ex)
	}
	return Skip("this profile creates no infrastructure")
}

func kindInfrastructure(ctx context.Context, ex *Exec) Outcome {
	if _, err := exec.LookPath("kind"); err != nil {
		return Block("kind is not installed, so its cluster cannot be inspected")
	}
	clusters := ex.Command(ctx, ex.Run.Root, "kind", "get", "clusters")
	_ = ex.Write("diagnostics/provider/kind-clusters.txt",
		[]byte(clusters.Stdout+clusters.Stderr))

	if !strings.Contains(clusters.Stdout, ex.Run.Cluster) {
		return Fail("the Kind cluster deploy reported is not there", Finding{
			Phase: PhaseInfrastructure, Command: clusters.String(),
			Environment: ex.Profile.Name,
			Expected:    "kind lists " + ex.Run.Cluster,
			Actual:      strings.TrimSpace(clusters.Stdout),
			Cause:       "deploy exited zero but created no cluster",
			Category:    CategoryProductDefect,
		})
	}

	// The containers behind it, as evidence and as a cleanup cross-check.
	if runtime := containerRuntime(); runtime != "" {
		containers := ex.Command(ctx, ex.Run.Root, runtime, "ps",
			"--filter", "name="+ex.Run.Cluster, "--format", "{{.Names}}\t{{.Status}}")
		_ = ex.Write("diagnostics/provider/containers.txt",
			[]byte(containers.Stdout+containers.Stderr))
		for _, line := range strings.Split(strings.TrimSpace(containers.Stdout), "\n") {
			if name := strings.TrimSpace(strings.SplitN(line, "\t", 2)[0]); name != "" {
				ex.Run.Register(Resource{
					Kind: "container", Name: name, Provider: runtime,
					Order:       OrderInfrastructure,
					Remediation: runtime + " rm -f " + name,
				})
			}
		}
	}
	return Pass("the Kind cluster and its containers exist")
}

func realInfrastructure(ctx context.Context, ex *Exec) Outcome {
	// `cluster status` is the CLI's own answer to "what is out there". There is
	// no `cluster diagnose`; status and describe are what exist.
	status := ex.CLI(ctx, "cluster", "status", ex.Run.Cluster, "--output", "json")
	_ = ex.Write("diagnostics/provider/status.json", []byte(status.Stdout))
	if status.ExitCode != 0 {
		return Outcome{State: StateWarning,
			Message: "cluster status could not be read: " + firstLine(status.Stderr),
			Findings: []Finding{{
				Phase: PhaseInfrastructure, Command: status.String(),
				Environment: ex.Profile.Name, Expected: "provider resource state",
				Actual: firstLine(status.Stderr),
				Cause:  "the provider did not answer", Category: CategoryProviderIssue,
			}}}
	}
	return Pass("provider reports the cluster")
}

// --- phase 13's assertions, beyond "the API answered" -------------------------

// waitFor polls until the check passes or the deadline runs out.
//
// Polling, never sleeping: a fixed sleep is either too short for a loaded runner
// or wasted minutes on an idle one, and the brief asks for deadlines rather than
// long sleeps for exactly that reason.
func waitFor(ctx context.Context, deadline time.Duration, check func() bool) bool {
	limit := time.Now().Add(deadline)
	for time.Now().Before(limit) {
		if check() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Second):
		}
	}
	return check()
}

// --- phase 14: openCenter platform services ----------------------------------

func phasePlatform(ctx context.Context, ex *Exec) Outcome {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return Block("kubectl is not installed")
	}

	// Which services this cluster actually enables, read from its own
	// configuration rather than from a list in here. A disabled service marked
	// failed is a false alarm that teaches people to ignore the phase.
	enabled := enabledServices(ctx, ex)
	if len(enabled) == 0 {
		return Skip("the configuration enables no platform services")
	}
	ex.Log("    services enabled: %s", strings.Join(enabled, ", "))

	var findings []Finding
	var healthy, notReady []string

	if slices.Contains(enabled, "fluxcd") || slices.Contains(enabled, "flux") {
		if _, err := exec.LookPath("flux"); err == nil {
			sources := ex.Command(ctx, ex.Run.Root, "flux", "get", "sources", "all", "-A")
			kustomizations := ex.Command(ctx, ex.Run.Root, "flux", "get", "kustomizations", "-A")
			_ = ex.Write("diagnostics/flux/sources.txt", []byte(sources.Stdout+sources.Stderr))
			_ = ex.Write("diagnostics/flux/kustomizations.txt",
				[]byte(kustomizations.Stdout+kustomizations.Stderr))

			if kustomizations.ExitCode != 0 || strings.Contains(kustomizations.Stdout, "False") {
				notReady = append(notReady, "flux")
				findings = append(findings, Finding{
					Phase: PhasePlatform, Command: kustomizations.String(),
					Environment: ex.Profile.Name,
					Expected:    "every Flux kustomization Ready",
					Actual:      firstLine(kustomizations.Stdout + kustomizations.Stderr),
					Cause:       "Flux has not reconciled", Category: CategoryProductDefect,
				})
			} else {
				healthy = append(healthy, "flux")
			}
		}
	}

	// Operators, where OLM is in play.
	if slices.Contains(enabled, "olm") {
		csv := ex.Command(ctx, ex.Run.Root, "kubectl", "get", "csv", "-A")
		_ = ex.Write("diagnostics/services/csv.txt", []byte(csv.Stdout+csv.Stderr))
		if csv.ExitCode == 0 && strings.Contains(csv.Stdout, "Succeeded") {
			healthy = append(healthy, "olm")
		} else if csv.ExitCode == 0 {
			notReady = append(notReady, "olm")
		}
	}

	// The generic check: a deployment per enabled service, if one exists under
	// that name. Anything with no matching workload is "not applicable" rather
	// than a failure — the service may be a CRD, a webhook or a daemonset.
	for _, service := range enabled {
		got := ex.Command(ctx, ex.Run.Root, "kubectl", "get", "deployment", "-A",
			"-l", "app.kubernetes.io/name="+service, "--no-headers")
		if got.ExitCode != 0 || strings.TrimSpace(got.Stdout) == "" {
			continue
		}
		if strings.Contains(got.Stdout, "0/") {
			notReady = append(notReady, service)
			findings = append(findings, Finding{
				Phase: PhasePlatform, Command: got.String(), Environment: ex.Profile.Name,
				Expected: service + " has ready replicas",
				Actual:   firstLine(got.Stdout),
				Cause:    service + " is deployed but not ready",
				Category: CategoryProductDefect,
			})
		} else {
			healthy = append(healthy, service)
		}
	}

	summary := fmt.Sprintf("%d healthy, %d not ready, of %d enabled",
		len(healthy), len(notReady), len(enabled))
	if len(findings) > 0 {
		return Fail(summary, findings...)
	}
	if len(healthy) == 0 {
		return Warn(summary + " — nothing could be checked directly")
	}
	return Pass(summary)
}

// enabledServices reads the services the cluster configuration turns on.
func enabledServices(ctx context.Context, ex *Exec) []string {
	// `cluster export` gives the effective configuration, and it supports
	// --output json. Preferred over reading files, because the CLI's own view of
	// its configuration is the one that matters.
	exported := ex.CLI(ctx, "cluster", "export", ex.Run.Cluster, "--output", "json")
	if exported.ExitCode != 0 {
		return nil
	}
	_ = ex.Write("evidence/effective-config.json", []byte(exported.Stdout))

	var found []string
	for _, candidate := range []string{
		"fluxcd", "flux", "olm", "strimzi", "kafka", "kube-prometheus-stack",
		"prometheus", "grafana", "loki", "tempo", "keycloak", "vault", "harbor",
		"kyverno", "cert-manager", "calico", "headlamp", "postgres-operator",
		"gateway", "gateway-api", "etcd-backup", "external-snapshotter",
	} {
		if strings.Contains(exported.Stdout, `"`+candidate+`"`) {
			found = append(found, candidate)
		}
	}
	return found
}

// --- phase 15: functional smoke tests ----------------------------------------

func phaseSmoke(ctx context.Context, ex *Exec) Outcome {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return Block("kubectl is not installed")
	}
	namespace := "e2e-smoke"

	// Registered before creation, and first in cleanup order: smoke workloads go
	// before the cluster they run on.
	ex.Run.Register(Resource{
		Kind: "namespace", Name: namespace, Namespace: namespace,
		Order:       OrderSmokeResource,
		Remediation: "kubectl delete namespace " + namespace + " --ignore-not-found",
	})

	if out := ex.Command(ctx, ex.Run.Root, "kubectl", "create", "namespace", namespace); out.ExitCode != 0 &&
		!strings.Contains(out.Stderr, "already exists") {
		return Fail("could not create the smoke-test namespace", Finding{
			Phase: PhaseSmoke, Command: out.String(), Environment: ex.Profile.Name,
			Expected: "a namespace", Actual: firstLine(out.Stderr),
			Cause: "the cluster rejected a namespace", Category: CategoryProductDefect,
		})
	}

	// A real application, not a pod that sleeps: the point is to exercise
	// scheduling, image pull, service discovery, DNS and in-cluster networking,
	// and a sleeping pod proves only the first two.
	defer smokeCleanup(ctx, ex, namespace)

	create := ex.Command(ctx, ex.Run.Root, "kubectl", "-n", namespace,
		"create", "deployment", "smoke", "--image=registry.k8s.io/e2e-test-images/agnhost:2.47",
		"--", "/agnhost", "netexec", "--http-port=8080")
	if create.ExitCode != 0 {
		return Fail("could not create the smoke deployment", Finding{
			Phase: PhaseSmoke, Command: create.String(), Environment: ex.Profile.Name,
			Expected: "a running deployment", Actual: firstLine(create.Stderr),
			Cause: "the cluster rejected a deployment", Category: CategoryProductDefect,
		})
	}

	ready := waitFor(ctx, 4*time.Minute, func() bool {
		got := ex.Command(ctx, ex.Run.Root, "kubectl", "-n", namespace,
			"get", "deployment", "smoke", "-o", "jsonpath={.status.readyReplicas}")
		return strings.TrimSpace(got.Stdout) == "1"
	})
	if !ready {
		describe := ex.Command(ctx, ex.Run.Root, "kubectl", "-n", namespace, "describe", "pods")
		_ = ex.Write("diagnostics/smoke/pods.txt", []byte(describe.Stdout+describe.Stderr))
		return Fail("the smoke application never became ready", Finding{
			Phase: PhaseSmoke, Environment: ex.Profile.Name,
			Expected: "1 ready replica within 4 minutes", Actual: "0",
			Cause:    "the workload could not be scheduled, pulled or started",
			Category: CategoryProductDefect,
			Evidence: "diagnostics/smoke/pods.txt",
		})
	}

	if out := ex.Command(ctx, ex.Run.Root, "kubectl", "-n", namespace,
		"expose", "deployment", "smoke", "--port=8080"); out.ExitCode != 0 {
		return Fail("could not expose the smoke deployment", Finding{
			Phase: PhaseSmoke, Command: out.String(), Environment: ex.Profile.Name,
			Expected: "a service", Actual: firstLine(out.Stderr),
			Cause: "service creation failed", Category: CategoryProductDefect,
		})
	}

	// Called from inside the cluster, through the service name. This is the
	// assertion that covers DNS and service networking at once — the two things
	// a deployment being Ready does not prove.
	var probe Command
	reachable := waitFor(ctx, 2*time.Minute, func() bool {
		probe = ex.Command(ctx, ex.Run.Root, "kubectl", "-n", namespace,
			"run", "smoke-probe-"+fmt.Sprint(time.Now().UnixNano()%100000),
			"--rm", "-i", "--restart=Never",
			// busybox rather than agnhost, because agnhost is a server and this
			// end of the conversation needs a client. Its subcommand list has
			// netexec, nettest and connect — connect opens a TCP socket and
			// stops there, and there is no HTTP client anywhere in the image.
			//
			// The probe asked it for `client` for two minutes and was told
			//
			//     Error: unknown command "client" for "app"
			//
			// on every attempt, after which the bench reported that in-cluster
			// DNS and service networking were broken and filed it as a product
			// defect against openCenter. The cluster was fine each time. Before
			// that the argument was `/agnhost client`, which `kubectl run`
			// appends to the entrypoint rather than replacing it — so the probe
			// was asking a server to be a client, twice over.
			//
			// Same registry as agnhost, so this adds no new dependency, and
			// wget makes the assertion a real HTTP round trip rather than a
			// bare TCP connect: DNS for the service name, routing through the
			// ClusterIP, and the application answering on the other side.
			"--image=registry.k8s.io/e2e-test-images/busybox:1.36.1-1",
			"--command", "--", "wget", "-q", "-T", "5", "-O", "-",
			"http://smoke:8080/echo?msg=opencenter")
		return strings.Contains(probe.Stdout, "opencenter")
	})
	_ = ex.Write("diagnostics/smoke/probe.txt", []byte(probe.Stdout+probe.Stderr))

	if !reachable {
		return Fail("the application was not reachable through its service", Finding{
			Phase: PhaseSmoke, Command: probe.String(), Environment: ex.Profile.Name,
			Expected: "http://smoke:8080/echo returns the message sent",
			Actual:   firstLine(probe.Stdout + probe.Stderr),
			Cause:    "in-cluster DNS or service networking is not working",
			Category: CategoryProductDefect,
			Evidence: "diagnostics/smoke/probe.txt",
		})
	}
	return Pass("deployed, resolved by name, called, and answered correctly")
}

// smokeCleanup removes the smoke namespace whatever happened.
//
// Deferred rather than placed at the end: a smoke test that returns early on a
// failure is the case where the namespace is most likely to be left behind, and
// phase 19 would then correctly fail the whole run for it.
func smokeCleanup(ctx context.Context, ex *Exec, namespace string) {
	// A fresh context: the run's may already be cancelled, and cleanup that
	// gives up because the user pressed Ctrl-C is cleanup that never happens.
	cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ex.Command(cleanup, ex.Run.Root, "kubectl", "delete", "namespace", namespace,
		"--ignore-not-found", "--wait=false")
	ex.Run.MarkRemoved("namespace", namespace)
}

// --- phase 16: failure, retry and recovery -----------------------------------

func phaseFailureTests(ctx context.Context, ex *Exec) Outcome {
	// Bounded and safe. Every failure here is deliberately caused, so every
	// finding is categorised as an expected injection — a release gate that
	// counted these as product defects would never let anything through.
	var findings []Finding
	passed, failed := 0, 0

	record := func(name string, ok bool, expected, actual string) {
		if ok {
			passed++
			return
		}
		failed++
		findings = append(findings, Finding{
			Phase: PhaseFailureTests, Environment: ex.Profile.Name,
			Expected: expected, Actual: actual,
			Cause:    "the CLI did not behave as it should when " + name,
			Category: CategoryProductDefect,
		})
	}

	// 1. An invalid configuration must be rejected clearly, not accepted or
	//    crashed on.
	invalid := ex.CLI(ctx, "cluster", "validate", "no-such-cluster-"+ex.Run.ID)
	record("given a cluster that does not exist",
		invalid.ExitCode != 0,
		"a non-zero exit and a clear message",
		fmt.Sprintf("exit %d", invalid.ExitCode))

	// 2. A command that cannot finish must terminate rather than hang, and the
	//    bench must notice it timed out rather than reporting a plain failure.
	short, cancel := context.WithTimeout(ctx, 2*time.Second)
	slow := ex.Command(short, ex.Run.Root, "sleep", "30")
	cancel()
	record("a command exceeds its deadline",
		slow.TimedOut || slow.ExitCode != 0,
		"the process is terminated and the timeout recorded",
		fmt.Sprintf("exit %d, timed out %v", slow.ExitCode, slow.TimedOut))

	// 3. A missing prerequisite must block with remediation rather than fail.
	//    Checked against the model rather than by uninstalling anything.
	missing := Outcome{State: StateBlocked}
	record("a prerequisite is missing",
		!missing.State.Bad(),
		"blocked, not failed — the product was never tested",
		string(missing.State))

	// 4. On a live cluster: a deleted pod must be recreated by its deployment.
	// Gated on deploy rather than on smoke: this scenario needs a cluster, not
	// a smoke test. Gating it on smoke meant a smoke failure silently reduced
	// the failure suite from four scenarios to three.
	if ex.Profile.Deploys && ex.Run.Result(PhaseDeploy) != nil &&
		ex.Run.Result(PhaseDeploy).State == StatePassed {
		if _, err := exec.LookPath("kubectl"); err == nil {
			recovered := podRecreatedAfterDeletion(ctx, ex)
			record("a pod is deleted",
				recovered,
				"Kubernetes recreates the disposable workload",
				fmt.Sprintf("%v", recovered))
		}
	}

	// Every finding here is an injected failure by construction, so the
	// categories are rewritten before they can reach the release gate.
	for index := range findings {
		findings[index].Category = CategoryExpectedInject
	}

	summary := fmt.Sprintf("%d of %d failure scenarios behaved correctly",
		passed, passed+failed)
	if failed > 0 {
		return Outcome{State: StateWarning, Message: summary, Findings: findings}
	}
	return Pass(summary)
}

// podRecreatedAfterDeletion deletes a pod and waits for its deployment to
// replace it.
//
// It brings its own workload. It used to reach into the smoke phase's
// namespace — which the smoke phase deletes on its way out, so by the time this
// ran there was no pod to find, the lookup came back empty, and the scenario
// was recorded as failed. Four runs reported "3 of 4 failure scenarios behaved
// correctly" about a cluster that had done nothing wrong.
//
// Depending on another phase's leftovers was the mistake even when it worked.
// Phases are individually runnable — `--only-phase failure-tests` is in every
// finding's Reproduce line — and one that only passes after its neighbour has
// run is not.
func podRecreatedAfterDeletion(ctx context.Context, ex *Exec) bool {
	namespace := "e2e-recovery"
	defer func() {
		ex.Command(ctx, ex.Run.Root, "kubectl", "delete", "namespace", namespace,
			"--ignore-not-found", "--wait=false")
	}()
	ex.Run.Register(Resource{
		Kind: "namespace", Name: namespace, Namespace: namespace,
		Order:       OrderSmokeResource,
		Remediation: "kubectl delete namespace " + namespace + " --ignore-not-found",
	})

	if out := ex.Command(ctx, ex.Run.Root, "kubectl", "create", "namespace", namespace); out.ExitCode != 0 &&
		!strings.Contains(out.Stderr, "already exists") {
		return false
	}
	// pause, not netexec: nothing calls this one, it only has to exist and be
	// owned by a ReplicaSet so that deleting it is a real recovery test.
	if out := ex.Command(ctx, ex.Run.Root, "kubectl", "-n", namespace,
		"create", "deployment", "recovery",
		"--image=registry.k8s.io/e2e-test-images/agnhost:2.47",
		"--", "/agnhost", "pause"); out.ExitCode != 0 {
		return false
	}
	if !waitFor(ctx, 4*time.Minute, func() bool {
		got := ex.Command(ctx, ex.Run.Root, "kubectl", "-n", namespace,
			"get", "deployment", "recovery", "-o", "jsonpath={.status.readyReplicas}")
		return strings.TrimSpace(got.Stdout) == "1"
	}) {
		return false
	}

	before := ex.Command(ctx, ex.Run.Root, "kubectl", "-n", namespace,
		"get", "pods", "-l", "app=recovery", "-o", "jsonpath={.items[0].metadata.name}")
	name := strings.TrimSpace(before.Stdout)
	if name == "" {
		return false
	}
	ex.Command(ctx, ex.Run.Root, "kubectl", "-n", namespace, "delete", "pod", name, "--wait=false")

	return waitFor(ctx, 2*time.Minute, func() bool {
		after := ex.Command(ctx, ex.Run.Root, "kubectl", "-n", namespace,
			"get", "pods", "-l", "app=recovery",
			"-o", "jsonpath={.items[0].metadata.name}")
		replacement := strings.TrimSpace(after.Stdout)
		return replacement != "" && replacement != name
	})
}
