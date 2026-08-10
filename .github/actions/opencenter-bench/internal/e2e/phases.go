package e2e

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The phase bodies.
//
// Every openCenter invocation below was checked against `opencenter cluster
// --help` on the built binary before it was written. The brief names
// `cluster preflight`, `cluster diagnose` and `generate --dry-run`; none of the
// three exists, and phases built on them would have failed at run time with
// "unknown command" — which reads as a broken CLI rather than a wrong test.
// See docs/testing/e2e-current-state.md §2.

// DefaultBodies wires the lifecycle.
func DefaultBodies() map[ID]Body {
	return map[ID]Body{
		PhasePlan:              phasePlan,
		PhaseWorkspace:         phaseWorkspace,
		PhasePrerequisites:     phasePrerequisites,
		PhaseBuild:             phaseBuild,
		PhaseVerifyBinary:      phaseVerifyBinary,
		PhaseConfigure:         phaseConfigure,
		PhaseValidateConfig:    phaseValidateConfig,
		PhaseDoctor:            phaseDoctor,
		PhaseRenderPreview:     phaseRenderPreview,
		PhaseGenerate:          phaseGenerate,
		PhaseValidateArtifacts: phaseValidateArtifacts,
		PhaseDeploy:            phaseDeploy,
		PhaseInfrastructure:    phaseInfrastructure,
		PhaseKubernetes:        phaseKubernetes,
		PhasePlatform:          phasePlatform,
		PhaseSmoke:             phaseSmoke,
		PhaseFailureTests:      phaseFailureTests,
		PhaseDiagnostics:       phaseDiagnostics,
		PhaseDestroy:           phaseDestroy,
		PhaseVerifyCleanup:     phaseVerifyCleanup,
		PhaseReport:            phaseReport,
	}
}

// --- phase 0: plan and safety ----------------------------------------------

func phasePlan(ctx context.Context, ex *Exec) Outcome {
	// The approval gate. Refusing here costs nothing; refusing after deploy
	// costs whatever the provider charges for the things already running.
	if ex.Profile.LiveApproval && !ex.Run.Approved {
		return Block("this profile creates real infrastructure and was not approved. " +
			"Re-run with --approve-live, having confirmed: an approved disposable " +
			"environment, approval to create infrastructure, approval to destroy it " +
			"automatically, and that provider costs may be incurred")
	}
	if ex.Profile.Emulated() {
		ex.Log("    emulated provider: results describe a simulated %s, not a real one",
			ex.Profile.Provider)
	}
	plan := map[string]any{
		"profile": ex.Profile.Name, "provider": ex.Profile.Provider,
		"infrastructure": ex.Profile.Infrastructure, "channel": ex.Run.Channel,
		"cluster": ex.Run.Cluster, "organisation": ex.Run.Organisation,
		"deploys": ex.Profile.Deploys, "destroy_after": ex.Run.DestroyAfter,
		"keep_on_failure": ex.Run.KeepOnFail, "timeout": ex.Run.Timeout.String(),
		"notes": ex.Profile.Notes,
	}
	encoded, _ := json.MarshalIndent(plan, "", "  ")
	_ = ex.Write("evidence/plan.json", encoded)
	return Pass(fmt.Sprintf("%s on %s, cluster %s",
		ex.Profile.Name, ex.Profile.Provider, ex.Run.Cluster))
}

// --- phase 1: clean workspace ----------------------------------------------

func phaseWorkspace(ctx context.Context, ex *Exec) Outcome {
	for _, dir := range []string{
		"home", "config", "state", "source", "bin", "cluster", "gitops",
		"evidence", "diagnostics", "reports", "junit", "logs", "commands",
		"resources", "cleanup",
	} {
		if err := os.MkdirAll(filepath.Join(ex.Run.Root, dir), 0o755); err != nil {
			return Fail("could not create the run directory", Finding{
				Phase: PhaseWorkspace, Environment: ex.Profile.Name,
				Expected: "an isolated run directory", Actual: err.Error(),
				Cause: "the run root is not writable", Category: CategoryEnvironment,
			})
		}
	}
	environment := map[string]string{
		"run_id": ex.Run.ID, "host": ex.Run.Host, "os": ex.Run.OS, "arch": ex.Run.Arch,
		"channel": string(ex.Run.Channel), "started": ex.Run.Started.Format(time.RFC3339),
		"HOME":                  filepath.Join(ex.Run.Root, "home"),
		"OPENCENTER_CONFIG_DIR": filepath.Join(ex.Run.Root, "config"),
		"OPENCENTER_STATE_DIR":  filepath.Join(ex.Run.Root, "state"),
	}
	encoded, _ := json.MarshalIndent(environment, "", "  ")
	_ = ex.Write("diagnostics/environment.json", encoded)
	return Pass("isolated at " + ex.Run.Root)
}

// --- phase 2: prerequisites -------------------------------------------------

// Tool is one prerequisite and what was found.
type Tool struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	Version  string `json:"version,omitempty"`
	Required bool   `json:"required"`
	Present  bool   `json:"present"`
}

func phasePrerequisites(ctx context.Context, ex *Exec) Outcome {
	// Required is per profile, from the profile itself. Demanding the OpenStack
	// client for a Kind run is how a prerequisites phase teaches people to skip
	// reading it.
	required := map[string]bool{}
	for _, name := range ex.Profile.Tools {
		required[name] = true
	}
	candidates := []string{"git", "go", "mise", "kubectl", "kind", "docker", "podman",
		"helm", "flux", "jq", "yq", "kustomize", "tofu", "terraform", "sops", "age", "ssh"}

	var found []Tool
	var missing []string
	for _, name := range candidates {
		tool := Tool{Name: name, Required: required[name]}
		path, err := exec.LookPath(name)
		if err == nil {
			tool.Present, tool.Path = true, path
			tool.Version = firstLine(ex.Command(ctx, ex.Run.Root, name, "--version").Stdout)
		}
		if !tool.Present && tool.Required {
			missing = append(missing, name)
		}
		found = append(found, tool)
	}

	// mise is installed on this machine but not on PATH, so LookPath misses it.
	if ex.Engine.MisePath != "" {
		for index := range found {
			if found[index].Name == "mise" && !found[index].Present {
				found[index].Present = true
				found[index].Path = ex.Engine.MisePath
				found[index].Version = firstLine(
					ex.Command(ctx, ex.Run.Root, ex.Engine.MisePath, "--version").Stdout)
				missing = remove(missing, "mise")
			}
		}
	}

	encoded, _ := json.MarshalIndent(found, "", "  ")
	_ = ex.Write("evidence/prerequisites.json", encoded)

	// The podman/docker conflict. The CLI repository's .mise.toml pins
	// CONTAINER_RUNTIME=podman, and a machine with only docker fails deep inside
	// Kind with a message about a socket. Saying it here, with the fix, is worth
	// more than discovering it at phase 11.
	// A simulated run needs none of the cluster tooling, and blocking it on a
	// port nothing will bind or a kind that nothing will call would make
	// --simulate useless on exactly the machines it exists for.
	if ex.Run.Simulated {
		ex.Log("    --simulate: cluster tooling is not required for this run")
		return Warn(simulatedPrefix + "cluster prerequisites not required")
	}

	if ex.Profile.Infrastructure == InfraKind {
		if !present(found, "podman") && present(found, "docker") {
			ex.Log("    note: .mise.toml pins CONTAINER_RUNTIME=podman but only docker " +
				"is installed — deploy will be given --container-runtime docker")
		}

		// The API port, settled now rather than eight phases later.
		//
		// Kind publishes the API server on 127.0.0.1:6443, and a machine that
		// already has a cluster there failed at deploy with "port is already
		// allocated" — after building the CLI, generating a configuration and
		// rendering 120 files.
		//
		// Blocking was the first answer, and it was the wrong one: it made a
		// perfectly capable machine unable to run the profile because of somebody
		// else's cluster, and told the operator to delete theirs. The port is
		// configurable — opencenter.infrastructure.kind.api_server_port — so the
		// run takes a free one instead and leaves every existing cluster alone.
		if holder := whoHoldsAPIPort(ctx, ex); holder != "" {
			free := freeTCPPort()
			if free == 0 {
				return Outcome{State: StateBlocked,
					Message: "127.0.0.1:6443 is taken by " + holder + " and no free port could be found",
					Findings: []Finding{{
						Phase: PhasePrerequisites, Environment: ex.Profile.Name,
						Expected:    "a free Kubernetes API port",
						Actual:      "6443 held by " + holder + ", and nothing else available",
						Cause:       "this machine has no port to give the cluster",
						Category:    CategoryEnvironment,
						Remediation: "kind delete cluster --name " + holder,
					}}}
			}
			ex.Engine.APIServerPort = free
			ex.Log("    6443 is taken by %s — this cluster will use %d instead", holder, free)
		}
	}

	if len(missing) > 0 {
		return Outcome{State: StateBlocked,
			Message: "missing required tool(s): " + strings.Join(missing, ", "),
			Findings: []Finding{{
				Phase: PhasePrerequisites, Environment: ex.Profile.Name,
				Expected: "all of " + strings.Join(ex.Profile.Tools, ", "),
				Actual:   "missing " + strings.Join(missing, ", "),
				Cause:    "the machine does not have what this profile needs",
				Category: CategoryMissingPrereq,
				Remediation: "install " + strings.Join(missing, ", ") +
					", or choose a profile that does not need it (configuration-only needs none of them)",
			}}}
	}
	return Pass(fmt.Sprintf("%d of %d tools present", countPresent(found), len(found)))
}

// --- phase 3: build ---------------------------------------------------------

func phaseBuild(ctx context.Context, ex *Exec) Outcome {
	if ex.Engine.CLIRepo == "" {
		return Skip("no openCenter source tree was given (--cli-repo)")
	}
	if ex.Engine.MisePath == "" {
		return Block("mise was not found, and the repository's build is a mise task")
	}

	// mise refuses untrusted config files, and the message it gives is about
	// trust rather than about the build. Trusting the repository we were pointed
	// at is the operator's intent; doing it silently is not, so it is logged.
	trust := ex.Command(ctx, ex.Engine.CLIRepo, ex.Engine.MisePath, "trust")
	if trust.ExitCode != 0 {
		ex.Log("    mise trust: %s", firstLine(trust.Stderr))
	}

	// `build` is the repository's own task — verified present in .mise.toml. It
	// runs build-cli and build-local-plugin.
	build := ex.Command(ctx, ex.Engine.CLIRepo, ex.Engine.MisePath, "run", "build")
	if build.ExitCode != 0 {
		return Fail("mise run build failed", Finding{
			Phase: PhaseBuild, Command: build.String(), Environment: ex.Profile.Name,
			Expected: "the repository's build task produces bin/opencenter",
			Actual:   fmt.Sprintf("exit %d: %s", build.ExitCode, firstLine(build.Stderr)),
			Cause:    "the CLI source does not build",
			Category: CategoryProductDefect,
		})
	}

	binary := filepath.Join(ex.Engine.CLIRepo, "bin", "opencenter")
	if _, err := os.Stat(binary); err != nil {
		return Fail("the build reported success but produced no binary", Finding{
			Phase: PhaseBuild, Command: build.String(), Environment: ex.Profile.Name,
			Expected: binary, Actual: err.Error(),
			Cause:    "the build task did not write the binary where it says it does",
			Category: CategoryProductDefect,
		})
	}
	ex.Engine.CLIBinary = binary
	ex.Run.CLIBinary = binary

	// The plugin is a second binary, and it was never installed.
	//
	// `mise run build` produces bin/opencenter and bin/opencenter-local. The CLI
	// loads the plugin from ~/.local/bin, which the repository's own installer
	// populates — and this bench never ran that installer. So a run built a
	// fresh CLI, verified it, and then executed it against whatever plugin was
	// last installed by hand.
	//
	// Measured, not theorised: the installed plugin here was three weeks older
	// than the build under test and defaulted the local Gitea to different
	// ports. `local gitea up` reported one address and `cluster deploy` waited
	// on another, deploy timed out after two minutes, and the lifecycle never
	// reached a cluster. Every phase after ten was blocked by a stale file
	// nobody had thought to look at.
	//
	// A bench whose job is "record exactly what build was tested" cannot leave
	// half the build to whatever is lying around.
	if installed := installPlugin(ctx, ex); installed != "" {
		ex.Run.CLIPlugin = installed
		ex.Log("    installed the local plugin to %s", installed)
	}

	return Pass("built " + binary)
}

// --- phase 4: verify the binary ---------------------------------------------

func phaseVerifyBinary(ctx context.Context, ex *Exec) Outcome {
	if ex.Engine.CLIBinary == "" {
		return Block("no binary to verify: the build phase produced none")
	}
	version := ex.Command(ctx, ex.Run.Root, ex.Engine.CLIBinary, "version")
	if version.ExitCode != 0 {
		return Fail("the binary will not report its version", Finding{
			Phase: PhaseVerifyBinary, Command: version.String(), Environment: ex.Profile.Name,
			Expected: "version output", Actual: firstLine(version.Stderr),
			Cause: "the built binary does not run", Category: CategoryProductDefect,
		})
	}
	ex.Run.CLIVersion = fieldFrom(version.Stdout, "opencenter version:")
	ex.Run.CLICommit = fieldFrom(version.Stdout, "Git commit:")

	// The command tree has to be discoverable, because every later phase is
	// built on it.
	tree := ex.Command(ctx, ex.Run.Root, ex.Engine.CLIBinary, "cluster", "--help")
	if tree.ExitCode != 0 || !strings.Contains(tree.Stdout, "Available Commands:") {
		return Fail("the cluster command tree cannot be discovered", Finding{
			Phase: PhaseVerifyBinary, Command: tree.String(), Environment: ex.Profile.Name,
			Expected: "an Available Commands list", Actual: firstLine(tree.Stderr),
			Cause: "the binary's command tree is not readable", Category: CategoryProductDefect,
		})
	}

	if sum, err := checksum(ex.Engine.CLIBinary); err == nil {
		ex.Run.CLIChecksum = sum
	}

	// The build injects the commit through ldflags, so this check is supported —
	// and it catches the thing nobody notices: testing yesterday's binary.
	var findings []Finding
	state := StatePassed
	if ex.Engine.CLIRepo != "" {
		head := ex.Command(ctx, ex.Engine.CLIRepo, "git", "rev-parse", "HEAD")
		ex.Run.SourceCommit = strings.TrimSpace(head.Stdout)
		if ex.Run.SourceCommit != "" && ex.Run.CLICommit != "" &&
			!strings.HasPrefix(ex.Run.SourceCommit, ex.Run.CLICommit) &&
			!strings.HasPrefix(ex.Run.CLICommit, ex.Run.SourceCommit) {
			state = StateWarning
			findings = append(findings, Finding{
				Phase: PhaseVerifyBinary, Environment: ex.Profile.Name,
				Expected:    "binary built from " + short(ex.Run.SourceCommit),
				Actual:      "binary reports " + short(ex.Run.CLICommit),
				Cause:       "the binary under test was not built from the checked-out source",
				Category:    CategoryEnvironment,
				Remediation: "rebuild with `mise run build` in " + ex.Engine.CLIRepo,
			})
		}
	}
	_ = ex.Write("evidence/binary.json", mustJSON(map[string]string{
		"binary": ex.Engine.CLIBinary, "version": ex.Run.CLIVersion,
		"commit": ex.Run.CLICommit, "source_commit": ex.Run.SourceCommit,
		"checksum": ex.Run.CLIChecksum,
	}))

	message := ex.Run.CLIVersion + " " + short(ex.Run.CLICommit)
	if state == StateWarning {
		return Outcome{State: StateWarning,
			Message:  message + " — does not match the source commit",
			Findings: findings}
	}
	return Pass(message)
}

// --- phase 5: configuration --------------------------------------------------

func phaseConfigure(ctx context.Context, ex *Exec) Outcome {
	// `cluster init` is non-interactive and takes --org and --type. Verified.
	// Credentials never appear here: the profile decides the type, the run
	// decides the names, and secrets reach the CLI through the environment.
	init := ex.CLI(ctx, "cluster", "init", ex.Run.Cluster,
		"--org", ex.Run.Organisation,
		"--type", ex.Profile.ClusterType)
	if init.ExitCode != 0 {
		return Fail("cluster init failed", Finding{
			Phase: PhaseConfigure, Command: init.String(), Environment: ex.Profile.Name,
			Expected: "a cluster configuration for " + ex.Run.Cluster,
			Actual:   fmt.Sprintf("exit %d: %s", init.ExitCode, firstLine(init.Stderr)),
			Cause:    "the CLI could not create a configuration",
			Category: CategoryProductDefect,
		})
	}
	// Registered now: init writes files and keys, and a run that dies at the next
	// phase still has to clean them up.
	ex.Run.Register(Resource{Kind: "cluster-config", Name: ex.Run.Cluster,
		Provider: string(ex.Profile.Provider), Order: OrderTempFile,
		Remediation: "opencenter cluster destroy " + ex.Run.Cluster + " --remove-files"})

	_ = ex.Write("evidence/cluster-init.txt", []byte(init.Stdout+init.Stderr))

	// What `cluster init` leaves out.
	//
	// A freshly initialised configuration does not pass `cluster validate`: it
	// wants a Keycloak admin password, a GitOps repository URL and a token for
	// it. That is a finding about the CLI and it is reported by phase 6 — but a
	// bench that stops there can never reach deploy, so the missing values are
	// filled in here the way an engineer would, with `cluster set`.
	//
	// Generated, never hardcoded, and handed to the redactor the moment they
	// exist so they cannot appear in a log, a report or an artifact. They are
	// throwaway values for a throwaway cluster.
	settings := configureSettingsFor(ex.Profile.Provider)
	var applied []string
	for _, setting := range settings {
		if setting[0] != "opencenter.gitops.repository.url" {
			ex.Redactor.Add(setting[1])
		}
		out := ex.CLI(ctx, "cluster", "set", ex.Run.Cluster, setting[0]+"="+setting[1])
		if out.ExitCode == 0 {
			applied = append(applied, setting[0])
		} else {
			ex.Log("    could not set %s: %s", setting[0], firstLine(out.Stderr))
		}
	}
	// The API port phase 2 picked, if it had to pick one.
	if ex.Engine.APIServerPort != 0 {
		port := strconv.Itoa(ex.Engine.APIServerPort)
		out := ex.CLI(ctx, "cluster", "set", ex.Run.Cluster,
			"opencenter.infrastructure.kind.api_server_port="+port)
		if out.ExitCode == 0 {
			applied = append(applied, "api_server_port="+port)
		} else {
			ex.Log("    could not set the API port: %s", firstLine(out.Stderr))
		}
	}

	if len(applied) > 0 {
		ex.Log("    filled in %d value(s) cluster init leaves empty", len(applied))
	}
	return Pass("configured " + ex.Run.Cluster)
}

// freeTCPPort asks the kernel for a port nothing is using.
//
// Bound and released immediately: the window between releasing it and Kind
// claiming it is a race in principle, and in practice the kernel does not hand
// the same ephemeral port out twice in that time. The alternative — scanning a
// range and hoping — races too, and guesses as well.
func freeTCPPort() int {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer func() { _ = listener.Close() }()
	if addr, ok := listener.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}

// gitopsFixtureURL is a deterministic placeholder. Nothing is pushed to it: the
// value exists because validation requires a repository URL, and a run-specific
// one would make two runs' configurations differ for no reason.
const gitopsFixtureURL = "https://github.com/opencenter-cloud/e2e-fixture.git"

// generatedSecret makes a throwaway credential for a throwaway cluster.
func generatedSecret() string {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		// Never a fixed fallback: a predictable "random" password that reaches a
		// real provider is worse than a failure here.
		return "e2e-" + hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return "E2e-" + hex.EncodeToString(raw)
}

func phaseValidateConfig(ctx context.Context, ex *Exec) Outcome {
	human := ex.CLI(ctx, "cluster", "validate", ex.Run.Cluster)
	// --output json exists on validate; the structured form is evidence, the
	// human form is what a person reads in the log.
	structured := ex.CLI(ctx, "cluster", "validate", ex.Run.Cluster, "--output", "json")
	if structured.ExitCode == 0 && strings.TrimSpace(structured.Stdout) != "" {
		_ = ex.Write("evidence/validate.json", []byte(structured.Stdout))
	}
	_ = ex.Write("evidence/validate.txt", []byte(human.Stdout+human.Stderr))

	if human.ExitCode != 0 {
		return Fail("cluster validate rejected the generated configuration", Finding{
			Phase: PhaseValidateConfig, Command: human.String(), Environment: ex.Profile.Name,
			Expected: "a valid configuration",
			Actual:   fmt.Sprintf("exit %d: %s", human.ExitCode, firstLine(human.Stderr)),
			Cause:    "the configuration the CLI generated does not pass the CLI's own validation",
			Category: CategoryProductDefect,
		})
	}
	return Pass("configuration is valid")
}

// --- phase 7: preflight, which is `doctor` -----------------------------------

func phaseDoctor(ctx context.Context, ex *Exec) Outcome {
	if ex.Profile.Emulated() {
		// The brief is explicit: emulation must not contact a real provider.
		// doctor reaches for credentials and endpoints, so it is not run here.
		return Skip("emulated provider: no real endpoint is contacted")
	}
	doctor := ex.CLI(ctx, "cluster", "doctor", ex.Run.Cluster)
	_ = ex.Write("evidence/doctor.txt", []byte(doctor.Stdout+doctor.Stderr))
	if doctor.ExitCode != 0 {
		// A doctor failure is usually the environment, not the product: absent
		// credentials, an unreachable endpoint, a quota. Classified as such so a
		// release gate does not read somebody's expired token as a defect.
		return Outcome{State: StateWarning,
			Message: "doctor reported problems: " + firstLine(doctor.Stdout+doctor.Stderr),
			Findings: []Finding{{
				Phase: PhaseDoctor, Command: doctor.String(), Environment: ex.Profile.Name,
				Expected: "local tools, credentials and provider readiness all good",
				Actual:   fmt.Sprintf("exit %d", doctor.ExitCode),
				Cause:    firstLine(doctor.Stdout + doctor.Stderr),
				Category: CategoryEnvironment,
			}}}
	}
	return Pass("provider readiness confirmed")
}

// --- phases 8 and 9: preview, then generate ----------------------------------

func phaseRenderPreview(ctx context.Context, ex *Exec) Outcome {
	before := snapshot(ex.Run.Root)
	// --render-only, not --dry-run: the latter does not exist.
	preview := ex.CLI(ctx, "cluster", "generate", ex.Run.Cluster, "--render-only")
	_ = ex.Write("evidence/render-preview.txt", []byte(preview.Stdout+preview.Stderr))
	if preview.ExitCode != 0 {
		return Fail("generate --render-only failed", Finding{
			Phase: PhaseRenderPreview, Command: preview.String(), Environment: ex.Profile.Name,
			Expected: "a preview of what generation would write",
			Actual:   fmt.Sprintf("exit %d: %s", preview.ExitCode, firstLine(preview.Stderr)),
			Cause:    "the preview path is broken", Category: CategoryProductDefect,
		})
	}
	// The point of a preview is that it previews. If the tree changed, it did
	// something, and that is worth a finding rather than a shrug.
	if after := snapshot(ex.Run.Root); len(after) > len(before)+2 {
		return Outcome{State: StateWarning,
			Message: fmt.Sprintf("the preview added %d files", len(after)-len(before)),
			Findings: []Finding{{
				Phase: PhaseRenderPreview, Command: preview.String(),
				Environment: ex.Profile.Name,
				Expected:    "a preview writes nothing persistent",
				Actual:      fmt.Sprintf("%d new files", len(after)-len(before)),
				Cause:       "--render-only has side effects", Category: CategoryProductDefect,
			}}}
	}
	return Pass("preview produced no persistent changes")
}

// Artifact is one generated file.
type Artifact struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Checksum  string `json:"checksum"`
	Sensitive bool   `json:"sensitive"`
}

func phaseGenerate(ctx context.Context, ex *Exec) Outcome {
	generate := ex.CLI(ctx, "cluster", "generate", ex.Run.Cluster)
	_ = ex.Write("evidence/generate.txt", []byte(generate.Stdout+generate.Stderr))
	if generate.ExitCode != 0 {
		return Fail("cluster generate failed", Finding{
			Phase: PhaseGenerate, Command: generate.String(), Environment: ex.Profile.Name,
			Expected: "GitOps repository and rendered manifests",
			Actual:   fmt.Sprintf("exit %d: %s", generate.ExitCode, firstLine(generate.Stderr)),
			Cause:    "generation failed", Category: CategoryProductDefect,
		})
	}
	ex.Run.Register(Resource{Kind: "generated-artifacts", Name: ex.Run.Cluster,
		Order: OrderGitOpsResource, Remediation: "remove " + ex.Run.Root + "/gitops"})

	var artifacts []Artifact
	for _, path := range snapshot(ex.Run.Root) {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		sum, _ := checksum(path)
		relative, _ := filepath.Rel(ex.Run.Root, path)
		artifacts = append(artifacts, Artifact{
			Path: relative, Size: info.Size(), Checksum: sum,
			Sensitive: looksSensitive(relative),
		})
	}
	_ = ex.Write("evidence/artifacts.json", mustJSON(artifacts))
	return Pass(fmt.Sprintf("%d files generated", len(artifacts)))
}

func phaseValidateArtifacts(ctx context.Context, ex *Exec) Outcome {
	// Only the validators whose tools exist. A missing tofu means this machine
	// cannot answer the question — not that the artifacts are wrong.
	type validator struct {
		tool string
		argv []string
		// style marks a check about how the output is written rather than
		// whether it is correct. `tofu fmt -check` answers "is this canonically
		// formatted", not "is this valid" — a difference the phase used to
		// ignore, so a whitespace complaint failed the run and blocked a
		// release. It is still worth saying; it is not worth stopping for.
		style bool
	}
	var ran, skipped []string
	var findings, styleFindings []Finding

	for _, v := range []validator{
		{tool: "tofu", argv: []string{"tofu", "fmt", "-check", "-recursive"}, style: true},
		{tool: "terraform", argv: []string{"terraform", "fmt", "-check", "-recursive"}, style: true},
		{tool: "kustomize", argv: []string{"kustomize", "version"}},
		{tool: "kubectl", argv: []string{"kubectl", "version", "--client=true"}},
	} {
		if _, err := exec.LookPath(v.tool); err != nil {
			skipped = append(skipped, v.tool)
			continue
		}
		ran = append(ran, v.tool)
		out := ex.Command(ctx, ex.Run.Root, v.argv...)
		if out.ExitCode == 0 {
			continue
		}
		// stdout, not stderr. `fmt -check` names the offending files on stdout
		// and says nothing on stderr, so the finding read
		//
		//     actual: exit 3:
		//
		// with the list of files it had just been handed thrown away. A finding
		// that names no file cannot be acted on, and the reader is left to guess
		// whether the CLI emitted broken HCL or a stray blank line.
		detail := lastMeaningfulLine(out.Stdout)
		if detail == "" {
			detail = lastMeaningfulLine(out.Stderr)
		}
		finding := Finding{
			Phase: PhaseValidateArtifacts, Command: out.String(),
			Environment: ex.Profile.Name,
			Expected:    v.tool + " accepts the artifacts",
			Actual:      fmt.Sprintf("exit %d: %s", out.ExitCode, detail),
			Cause:       v.tool + " rejected the generated output",
			Category:    CategoryProductDefect,
		}
		if v.style {
			finding.Expected = v.tool + " reports the generated files as canonically formatted"
			finding.Cause = "generated " + v.tool + " files are not canonically formatted"
			_ = ex.Write("evidence/"+v.tool+"-fmt.txt", []byte(out.Stdout+out.Stderr))
			finding.Evidence = "evidence/" + v.tool + "-fmt.txt"
			styleFindings = append(styleFindings, finding)
			continue
		}
		findings = append(findings, finding)
	}

	// A secret in a generated file is a failure on its own, whatever the
	// validators said.
	if leaked := scanForSecrets(ex.Run.Root); len(leaked) > 0 {
		findings = append(findings, Finding{
			Phase: PhaseValidateArtifacts, Environment: ex.Profile.Name,
			Expected: "no plaintext secrets in generated files",
			Actual:   strings.Join(leaked, ", "),
			Cause:    "generation wrote something that looks like a credential",
			Category: CategoryProductDefect,
		})
		return Fail("possible plaintext secret in generated output", findings...)
	}

	message := fmt.Sprintf("ran %d validator(s)", len(ran))
	if len(skipped) > 0 {
		message += ", skipped " + strings.Join(skipped, ", ") + " (not installed)"
	}
	if len(findings) > 0 {
		return Fail("a validator rejected the generated artifacts",
			append(findings, styleFindings...)...)
	}
	if len(ran) == 0 {
		return Warn("no validators are installed on this machine: " +
			strings.Join(skipped, ", "))
	}
	// Reported, not fatal. Three of the four profiles failed their whole run on
	// this, having passed every other phase, because the generated OpenTofu was
	// not `tofu fmt` clean. It is a real observation about the product and the
	// bench keeps making it — but a release is not blocked by whitespace.
	if len(styleFindings) > 0 {
		return Outcome{
			State: StateWarning,
			Message: fmt.Sprintf("%s; %d formatting finding(s)",
				message, len(styleFindings)),
			Findings: styleFindings,
		}
	}
	return Pass(message)
}

// --- phase 13: Kubernetes health ---------------------------------------------

func phaseKubernetes(ctx context.Context, ex *Exec) Outcome {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return Block("kubectl is not installed, so cluster health cannot be read")
	}
	// Polled against a deadline rather than slept through: a fixed sleep is
	// either too short for a slow runner or wasted time on a fast one.
	deadline := time.Now().Add(3 * time.Minute)
	var last Command
	for time.Now().Before(deadline) {
		last = ex.Command(ctx, ex.Run.Root, "kubectl", "cluster-info")
		if last.ExitCode == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return Outcome{State: StateCancelled, Message: "cancelled while waiting for the API"}
		case <-time.After(5 * time.Second):
		}
	}
	if last.ExitCode != 0 {
		return Fail("the Kubernetes API never became reachable", Finding{
			Phase: PhaseKubernetes, Command: last.String(), Environment: ex.Profile.Name,
			Expected: "cluster-info answers within 3 minutes",
			Actual:   firstLine(last.Stderr), Cause: "no reachable Kubernetes API",
			Category: CategoryEnvironment,
		})
	}
	for _, argv := range [][]string{
		{"kubectl", "get", "nodes", "-o", "wide"},
		{"kubectl", "get", "pods", "-A"},
		{"kubectl", "get", "deployments", "-A"},
		{"kubectl", "get", "pvc", "-A"},
		{"kubectl", "get", "events", "-A"},
	} {
		out := ex.Command(ctx, ex.Run.Root, argv...)
		_ = ex.Write("diagnostics/kubernetes/"+argv[2]+".txt", []byte(out.Stdout+out.Stderr))
	}

	var findings []Finding

	// Nodes Ready. "The API answered" is not health: an API server answers
	// perfectly well while every node it knows about is NotReady.
	nodesReady := waitFor(ctx, 4*time.Minute, func() bool {
		got := ex.Command(ctx, ex.Run.Root, "kubectl", "get", "nodes",
			"-o", "jsonpath={.items[*].status.conditions[?(@.type==\"Ready\")].status}")
		statuses := strings.Fields(got.Stdout)
		if len(statuses) == 0 {
			return false
		}
		for _, status := range statuses {
			if status != "True" {
				return false
			}
		}
		return true
	})
	if !nodesReady {
		state := ex.Command(ctx, ex.Run.Root, "kubectl", "get", "nodes")
		findings = append(findings, Finding{
			Phase: PhaseKubernetes, Command: state.String(), Environment: ex.Profile.Name,
			Expected: "every node Ready within 4 minutes",
			Actual:   firstLine(state.Stdout), Cause: "nodes did not become Ready",
			Category: CategoryProductDefect, Evidence: "diagnostics/kubernetes/nodes.txt",
		})
	}

	// Pods that cannot start. CrashLoopBackOff and ImagePullBackOff are the two
	// that mean something is actually wrong rather than merely slow.
	stuck := ex.Command(ctx, ex.Run.Root, "kubectl", "get", "pods", "-A",
		"--field-selector=status.phase!=Running,status.phase!=Succeeded", "--no-headers")
	for _, line := range strings.Split(strings.TrimSpace(stuck.Stdout), "\n") {
		if strings.Contains(line, "CrashLoopBackOff") || strings.Contains(line, "ImagePullBackOff") {
			findings = append(findings, Finding{
				Phase: PhaseKubernetes, Command: stuck.String(), Environment: ex.Profile.Name,
				Expected: "no pod stuck in a back-off state",
				Actual:   strings.TrimSpace(line), Cause: "a pod cannot start",
				Category: CategoryProductDefect, Evidence: "diagnostics/kubernetes/pods.txt",
			})
		}
	}

	// PVCs that never bind hang everything that mounts them, and the pod-level
	// symptom is a pending pod with no obvious reason.
	pvcs := ex.Command(ctx, ex.Run.Root, "kubectl", "get", "pvc", "-A", "--no-headers")
	for _, line := range strings.Split(strings.TrimSpace(pvcs.Stdout), "\n") {
		if strings.Contains(line, "Pending") {
			findings = append(findings, Finding{
				Phase: PhaseKubernetes, Command: pvcs.String(), Environment: ex.Profile.Name,
				Expected: "every PersistentVolumeClaim Bound",
				Actual:   strings.TrimSpace(line), Cause: "a claim never bound",
				Category: CategoryProductDefect,
			})
		}
	}

	// DNS, checked directly. Everything above can be green while name
	// resolution is broken, and then every service-to-service call fails with a
	// message about a host nobody recognises.
	dns := ex.Command(ctx, ex.Run.Root, "kubectl", "-n", "kube-system",
		"get", "deployment", "coredns", "-o", "jsonpath={.status.readyReplicas}")
	if strings.TrimSpace(dns.Stdout) == "" || strings.TrimSpace(dns.Stdout) == "0" {
		findings = append(findings, Finding{
			Phase: PhaseKubernetes, Command: dns.String(), Environment: ex.Profile.Name,
			Expected: "CoreDNS has ready replicas", Actual: "none",
			Cause: "cluster DNS is not running", Category: CategoryProductDefect,
		})
	}

	if len(findings) > 0 {
		return Fail(fmt.Sprintf("%d Kubernetes health problem(s)", len(findings)), findings...)
	}
	return Pass("API reachable, nodes Ready, no stuck pods, claims bound, DNS up")
}

// --- phase 17: diagnostics ---------------------------------------------------

func phaseDiagnostics(ctx context.Context, ex *Exec) Outcome {
	// The same collector locally and in CI — one implementation, so evidence
	// from a laptop and evidence from a runner are comparable.
	var commands []Command
	for _, phase := range ex.Run.Phases {
		commands = append(commands, phase.Commands...)
	}
	_ = ex.Write("diagnostics/commands.json", mustJSON(commands))
	_ = ex.Write("diagnostics/resources.json", mustJSON(ex.Run.Resources))

	summary := &strings.Builder{}
	fmt.Fprintf(summary, "# Diagnostics for %s\n\n", ex.Run.ID)
	fmt.Fprintf(summary, "- profile: %s\n- provider: %s\n- channel: %s\n",
		ex.Run.Profile, ex.Run.Provider, ex.Run.Channel)
	fmt.Fprintf(summary, "- CLI: %s %s\n\n## Phases\n\n",
		ex.Run.CLIVersion, short(ex.Run.CLICommit))
	for _, phase := range ex.Run.Phases {
		if phase.State == StateNotStarted {
			continue
		}
		fmt.Fprintf(summary, "- **%s** — %s — %s\n", phase.ID, phase.State, phase.Message)
	}
	if remaining := ex.Run.Remaining(); len(remaining) > 0 {
		fmt.Fprintf(summary, "\n## Not removed\n\n")
		for _, resource := range remaining {
			fmt.Fprintf(summary, "- %s %s — %s\n", resource.Kind, resource.Name,
				resource.Remediation)
		}
	}
	_ = ex.Write("diagnostics/summary.md", []byte(summary.String()))
	return Pass(fmt.Sprintf("%d commands and %d resources recorded",
		len(commands), len(ex.Run.Resources)))
}

// --- phases 18 and 19: destroy, then prove it --------------------------------

func phaseDestroy(ctx context.Context, ex *Exec) Outcome {
	if ex.Run.KeepOnFail && ex.anyFailed() {
		return Skip("--keep-on-failure was given and the run failed: " +
			"the environment is being kept for troubleshooting")
	}
	// A simulated run registered resources so the registry, the cleanup ordering
	// and the report would all be exercised — but nothing exists to destroy.
	// Marking them removed here keeps phase 19 honest: it goes and looks for
	// each one, and finds nothing, which is the truth.
	// A simulated run registered resources so the registry, the cleanup ordering
	// and the report would all be exercised, and nothing exists to destroy for
	// those. But the phases before deploy ran for real — `cluster init` wrote a
	// configuration, keys and a GitOps tree — so the real destroy still has to
	// happen. Releasing the fakes and returning early left that behind, and
	// phase 19 caught it, which is the phase working exactly as intended.
	simulatedReleased := 0
	if ex.Run.Simulated {
		for _, resource := range ex.Run.Resources {
			if strings.HasPrefix(resource.Kind, "simulated-") {
				ex.Run.MarkRemoved(resource.Kind, resource.Name)
				simulatedReleased++
			}
		}
		_ = ex.Run.Save()
	}

	if !ex.Run.Created() {
		return Skip("nothing was created")
	}
	if ex.Engine.CLIBinary == "" {
		return Warn("no CLI binary to destroy with; resources are recorded for manual cleanup")
	}

	// Dependency order: the registry's own ordering, lowest first. Smoke-test
	// workloads before the cluster they run on.
	sorted := append([]Resource(nil), ex.Run.Resources...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Order < sorted[j].Order })

	destroy := ex.CLI(ctx, "cluster", "destroy", ex.Run.Cluster, "--force", "--remove-files")
	_ = ex.Write("evidence/destroy.txt", []byte(destroy.Stdout+destroy.Stderr))
	if destroy.ExitCode != 0 {
		// Whose fault, before deciding how loudly to say it.
		//
		// Destroying an OpenStack cluster runs OpenTofu, and the emulated
		// profiles do not require it — their Tools are git, go and mise. So on a
		// machine without terraform this failed with "executable file not found
		// in $PATH" and was reported as a Cleanup defect, which is a FAIL and
		// blocks a release. Nothing was leaked and nothing is broken: the tool
		// that removes it was never installed.
		//
		// A missing prerequisite is its own category for exactly this reason.
		cause, category := CategoryCleanupDefect, "destroy did not complete"
		remediation := "opencenter cluster destroy " + ex.Run.Cluster +
			" --force --remove-files"
		if tool := missingToolIn(destroy.Stdout + destroy.Stderr); tool != "" {
			cause, category = CategoryMissingPrereq,
				"destroy needs "+tool+", which is not installed"
			remediation = "install " + tool + ", then: " + remediation
		}
		outcome := Fail("cluster destroy failed", Finding{
			Phase: PhaseDestroy, Command: destroy.String(), Environment: ex.Profile.Name,
			Expected: "the cluster and its files removed",
			Actual:   fmt.Sprintf("exit %d: %s", destroy.ExitCode, firstLine(destroy.Stderr)),
			Cause:    category, Category: cause,
			Remediation: remediation,
		})
		if cause == CategoryMissingPrereq {
			// Blocked rather than failed: the cleanup was never attempted, so
			// this says nothing about whether cleanup works.
			outcome.State = StateBlocked
		}
		return outcome
	}
	for _, resource := range sorted {
		ex.Run.MarkRemoved(resource.Kind, resource.Name)
	}
	_ = ex.Run.Save()

	if simulatedReleased > 0 {
		return Outcome{State: StateWarning, Message: fmt.Sprintf(
			"%sdestroyed the real configuration; %d simulated resource(s) needed nothing",
			simulatedPrefix, simulatedReleased)}
	}
	return Pass(fmt.Sprintf("destroyed, %d resource(s) marked removed", len(sorted)))
}

func phaseVerifyCleanup(ctx context.Context, ex *Exec) Outcome {
	// Exit zero from destroy is a claim. This phase is the evidence.
	var orphans []string
	list := ex.CLI(ctx, "cluster", "list")
	if list.ExitCode == 0 && strings.Contains(list.Stdout, ex.Run.Cluster) {
		orphans = append(orphans, "cluster "+ex.Run.Cluster+" is still configured")
	}
	if _, err := exec.LookPath("kind"); err == nil {
		clusters := ex.Command(ctx, ex.Run.Root, "kind", "get", "clusters")
		if strings.Contains(clusters.Stdout, ex.Run.Cluster) {
			orphans = append(orphans, "kind cluster "+ex.Run.Cluster+" still exists")
		}
	}
	for _, resource := range ex.Run.Remaining() {
		orphans = append(orphans, resource.Kind+" "+resource.Name)
	}

	// Directories this run made and cannot remove.
	//
	// Found by this bench, about this bench. A Kind run leaves
	// config/local/gitea/ssh owned by root and mode 0700, because the container
	// created it — and the registry knew nothing about it, so cleanup reported
	// "nothing left behind" while the engineer was left with a directory they
	// cannot delete without sudo, inside their own checkout, which then breaks
	// `go build ./...` for every later command in that tree.
	//
	// A resource is anything the run brought into existence that outlives it.
	// A file nobody can remove qualifies.
	//
	// Reported for four runs with the remediation "sudo rm -rf …", which is a
	// bench asking the engineer to clean up after it. What made it as root can
	// unmake it as root: the removal goes back through the container runtime,
	// so no privilege is asked for and nothing outside the run root is touched.
	unremovable := reclaimRootOwned(ctx, ex, unreadablePaths(ex.Run.Root))
	for _, path := range unremovable {
		orphans = append(orphans, "unremovable path "+path)
	}

	_ = ex.Write("cleanup/verification.json", mustJSON(map[string]any{
		"orphans": orphans, "resources": ex.Run.Resources,
		"unremovable": unremovable,
	}))
	if len(unremovable) > 0 {
		return Fail(fmt.Sprintf("%d path(s) this run cannot remove", len(unremovable)),
			Finding{
				Phase: PhaseVerifyCleanup, Environment: ex.Profile.Name,
				Expected: "a run directory the operator owns",
				Actual:   strings.Join(unremovable, "; "),
				Cause: "a container created these as root, so the run cannot delete " +
					"them and neither can the engineer without sudo",
				Category: CategoryCleanupDefect,
				Remediation: "sudo rm -rf " + strings.Join(unremovable, " ") +
					"  — and note that until they are gone, `go build ./...` in this " +
					"checkout fails on them",
			})
	}
	if len(orphans) > 0 {
		return Fail(fmt.Sprintf("%d resource(s) survived cleanup", len(orphans)), Finding{
			Phase: PhaseVerifyCleanup, Environment: ex.Profile.Name,
			Expected: "nothing left behind", Actual: strings.Join(orphans, "; "),
			Cause:    "cleanup did not remove everything it created",
			Category: CategoryCleanupDefect,
			Remediation: "opencenter cluster destroy " + ex.Run.Cluster +
				" --force --remove-files, then check `kind get clusters`",
		})
	}
	return Pass("nothing left behind")
}

// installPlugin puts the plugin just built where the CLI will load it from.
//
// Copied rather than symlinked: the CLI checks the file, and a link into a
// build tree breaks the moment somebody rebuilds or moves it. Returns the path
// installed, or empty when there is no plugin to install — which is a normal
// state for a CLI layout that has none, not a failure.
func installPlugin(ctx context.Context, ex *Exec) string {
	source := filepath.Join(ex.Engine.CLIRepo, "bin", "opencenter-local")
	if _, err := os.Stat(source); err != nil {
		return ""
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	destination := filepath.Join(home, ".local", "bin", "opencenter-local")

	// Already the same file, which is the ordinary case on a second run.
	if same(source, destination) {
		return destination
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		ex.Log("    could not install the plugin: %s", err)
		return ""
	}
	body, err := os.ReadFile(source)
	if err != nil {
		ex.Log("    could not read the built plugin: %s", err)
		return ""
	}
	// Written beside and renamed: a plugin half-written is a plugin that fails
	// to execute, and the message says nothing about why.
	temporary := destination + ".tmp"
	if err := os.WriteFile(temporary, body, 0o755); err != nil {
		ex.Log("    could not install the plugin: %s", err)
		return ""
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		ex.Log("    could not install the plugin: %s", err)
		return ""
	}
	return destination
}

// same reports whether two paths hold identical bytes.
func same(one, other string) bool {
	first, err := os.ReadFile(one)
	if err != nil {
		return false
	}
	second, err := os.ReadFile(other)
	if err != nil {
		return false
	}
	return string(first) == string(second)
}

// configureSettingsFor is what `cluster init` leaves empty, per provider.
//
// A freshly initialised configuration does not pass `cluster validate`: it
// wants a Keycloak admin password, a GitOps repository URL and a token for it.
// That is a finding about the CLI and phase 6 reports it — but a bench that
// stops there can never reach deploy, so the values are filled in here the way
// an engineer would.
//
// OpenStack wants four more, and only OpenStack: Loki and Tempo store to Swift
// there and each needs an application credential id and secret. Without them
// `cluster validate` rejected the configuration the bench had just generated,
// and the bench reported that as a product defect and failed the run — while it
// was the one that generated it incomplete. baremetal and vmware have no Swift,
// so they validated cleanly and the gap stayed invisible until CI ran all four
// profiles side by side.
//
// A function rather than a literal inside the phase, so the list can be
// asserted without running a cluster.
func configureSettingsFor(provider Provider) [][2]string {
	settings := [][2]string{
		{"secrets.keycloak.admin_password", generatedSecret()},
		{"opencenter.gitops.auth.token.token", generatedSecret()},

		// The five the generated overlay leaves as the literal CHANGEME.
		//
		// Deploy commits the GitOps repository, and that commit runs a security
		// check which refuses any file still containing a stub secret. It named
		// all five at once — headlamp's OIDC client secret, Loki's S3 pair and
		// Tempo's S3 pair — and step 6 of 8 failed with "GitOps security
		// validation failed with 5 finding(s)".
		//
		// The CLI is right to refuse. A platform whose object storage credential
		// is the word CHANGEME is not deployed, it is waiting to be. The bench
		// generated that overlay, so the bench fills them, exactly as it already
		// does for OpenStack's Swift credentials below.
		//
		// Every provider, not only Kind: the stubs are rendered from the service
		// defaults and have nothing to do with which infrastructure is underneath.
		// Kind is simply the only profile that gets far enough to be told.
		{"secrets.headlamp.oidc_client_secret", generatedSecret()},
		{"secrets.loki.s3_access_key_id", generatedSecret()},
		{"secrets.loki.s3_secret_access_key", generatedSecret()},
		{"secrets.tempo.access_key", generatedSecret()},
		{"secrets.tempo.secret_key", generatedSecret()},
	}
	// Kind bootstraps Flux from the Gitea the bench just started, so it must not
	// be told about a repository somewhere else.
	//
	// The CLI decides between `flux bootstrap github` and the local path by
	// asking Config.ConfiguredGitURL(), which returns "" when the URL is still
	// the schema default — and only then falls back to the running Gitea's own
	// address, which it computes at bootstrap time on the kind network.
	//
	// Setting a real-looking github.com URL here turned that fallback off. The
	// run then reached step 4 of 8 and asked api.github.com for
	// opencenter-cloud/e2e-fixture with a secret this bench had generated
	// seconds earlier, and got 401 Bad credentials — correctly, because the
	// credential was never a GitHub credential and the repository is a
	// placeholder that has never existed.
	//
	// So Kind keeps the default and the CLI does what it was built to do. The
	// other providers have no local Gitea and do need a URL to validate against.
	if provider != ProviderKind {
		settings = append(settings,
			[2]string{"opencenter.gitops.repository.url", gitopsFixtureURL})
	}
	switch provider {
	case ProviderOpenStack:
		settings = append(settings,
			// Loki and Tempo store to Swift. Only the secret exists as a field —
			// there is no matching *_id on LokiSecrets or TempoSecrets, and
			// setting one fails with "field not found". Asked for and removed.
			[2]string{"secrets.loki.swift_application_credential_secret", generatedSecret()},
			[2]string{"secrets.tempo.swift_application_credential_secret", generatedSecret()},
			// And readiness validation wants the cluster's own application
			// credential, which lives under infrastructure rather than secrets.
			[2]string{"opencenter.infrastructure.cloud.openstack.application_credential_id",
				generatedSecret()},
			[2]string{"opencenter.infrastructure.cloud.openstack.application_credential_secret",
				generatedSecret()},
		)
	case ProviderVMware:
		// vSphere CSI is enabled by default and wants a vCenter to talk to.
		settings = append(settings,
			// A host rather than a random string: this one is shaped like the
			// thing it stands for, so a reader of the generated configuration
			// can see it is a fixture and not a leaked address.
			[2]string{"secrets.vsphere_csi.vcenter_host", "vcenter.emulated.invalid"},
			[2]string{"secrets.vsphere_csi.username", "e2e-testbench"},
			[2]string{"secrets.vsphere_csi.password", generatedSecret()},
		)
	}
	return settings
}

// missingToolIn names the executable a command could not find, if that is why
// it failed.
//
// Matched on Go's own exec error, which is what the CLI surfaces verbatim:
//
//	exec: "terraform": executable file not found in $PATH
//
// Narrow deliberately. Anything else stays whatever the caller decided, because
// a classifier that reads "missing tool" into every failure is a release gate
// that never blocks anything.
func missingToolIn(output string) string {
	const marker = `exec: "`
	index := strings.Index(output, marker)
	if index < 0 {
		return ""
	}
	rest := output[index+len(marker):]
	name, _, found := strings.Cut(rest, `"`)
	if !found || !strings.Contains(rest, "executable file not found") {
		return ""
	}
	return name
}

// unreadablePaths lists directories under root this process cannot enter.
//
// Cannot enter, rather than "owned by somebody else": the question is whether
// the operator can clear their own workspace, and the answer is an attempted
// read rather than a guess from the mode bits. Deliberately shallow in what it
// reports — the top of each unreadable subtree, not every file under it, which
// it could not enumerate anyway.
func unreadablePaths(root string) []string {
	if root == "" {
		return nil
	}
	var found []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// The walk failed to descend. That is the condition being looked for,
			// so it is recorded rather than propagated — returning err here would
			// abandon the walk at the first one and miss the rest.
			if path != root {
				found = append(found, path)
			}
			return fs.SkipDir
		}
		return nil
	})
	sort.Strings(found)
	return found
}

// reclaimRootOwned deletes root-owned paths using the runtime that made them,
// and returns whatever is still there afterwards.
//
// A container writing into a bind mount writes as root, and the run then owns
// a directory it cannot enter. Asking the engineer for sudo is not cleanup, and
// a bench that leaves undeletable files in a checkout breaks `go build ./...`
// for everything that follows in that tree.
//
// The image is one already on the machine. Cleanup must not depend on a
// network: a teardown that fails because a registry is unreachable has turned a
// tidy run into a leaked one. If there is no runtime and no local image, the
// paths come back unchanged and are reported exactly as before.
func reclaimRootOwned(ctx context.Context, ex *Exec, paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	runtime := containerRuntime()
	if runtime == "" {
		return paths
	}
	image := localImage(ctx, ex, runtime)
	if image == "" {
		return paths
	}
	for _, path := range paths {
		// Only inside the run root. A path outside it is not this run's to
		// delete, and mounting an arbitrary parent as root would be a much
		// larger thing than cleaning up after a test.
		if !strings.HasPrefix(path, ex.Run.Root+string(filepath.Separator)) {
			continue
		}
		parent, base := filepath.Split(strings.TrimRight(path, string(filepath.Separator)))
		ex.Command(ctx, ex.Run.Root, runtime, "run", "--rm",
			"-v", filepath.Clean(parent)+":/target",
			"--entrypoint", "/bin/sh", image,
			"-c", "rm -rf /target/"+base)
	}
	return unreadablePaths(ex.Run.Root)
}

// localImage names an image already pulled, preferring the ones this run used.
func localImage(ctx context.Context, ex *Exec, runtime string) string {
	listed := ex.Command(ctx, ex.Run.Root, runtime, "images", "--format", "{{.Repository}}:{{.Tag}}")
	var images []string
	for _, line := range strings.Split(listed.Stdout, "\n") {
		if name := strings.TrimSpace(line); name != "" && !strings.Contains(name, "<none>") {
			images = append(images, name)
		}
	}
	// Gitea is the container that created the directory in the first place, and
	// kind's node image is a whole distribution — both certainly have a shell.
	for _, preferred := range []string{"gitea", "kindest/node", "alpine", "busybox"} {
		for _, name := range images {
			if strings.Contains(name, preferred) {
				return name
			}
		}
	}
	if len(images) > 0 {
		return images[0]
	}
	return ""
}

// --- phase 20: the report ----------------------------------------------------

func phaseReport(ctx context.Context, ex *Exec) Outcome {
	verdict, why := ex.Run.Gate()
	written, err := WriteReports(ex.Run, verdict, why)
	if err != nil {
		return Fail("could not write the report", Finding{
			Phase: PhaseReport, Environment: ex.Profile.Name,
			Expected: "reports in every format", Actual: err.Error(),
			Cause: "the report could not be written", Category: CategoryBenchDefect,
		})
	}
	return Pass(fmt.Sprintf("%s — %s (%d files)", verdict, why, written))
}

// --- helpers -----------------------------------------------------------------

func (e *Exec) anyFailed() bool {
	for _, phase := range e.Run.Phases {
		if phase.State == StateFailed {
			return true
		}
	}
	return false
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return strings.TrimSpace(text[:index])
	}
	if len(text) > 200 {
		return text[:200]
	}
	return text
}

func fieldFrom(text, label string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label) {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), label))
		}
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func checksum(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// snapshot lists the files the run produced that are worth calling artifacts.
//
// home/ is excluded, and the reason is measured rather than theoretical: the
// isolated HOME means `mise run build` writes Go's build cache inside the run
// directory, and the first real run put **31,476** files there against 25
// everywhere else. Walking them made the artifact manifest meaningless, the
// preview's before/after comparison noise, and the secret scan read a hundred
// megabytes of object files looking for private keys.
//
// state/ goes too: the run's own bookkeeping is not something the CLI generated.
func snapshot(root string) []string {
	skip := map[string]bool{"home": true, "state": true}
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if relative, relErr := filepath.Rel(root, path); relErr == nil {
				if top := strings.SplitN(relative, string(filepath.Separator), 2)[0]; skip[top] {
					return filepath.SkipDir
				}
			}
			return nil
		}
		out = append(out, path)
		return nil
	})
	sort.Strings(out)
	return out
}

func looksSensitive(path string) bool {
	lower := strings.ToLower(path)
	for _, marker := range []string{"secret", "key", "credential", "token", "password", ".age", ".sops"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// scanForSecrets looks for material that should never be written in the clear.
//
// Deliberately narrow: private-key headers and nothing else. A scanner that
// flags every string containing "password" produces a page of false positives,
// and a check nobody believes is a check nobody reads.
func scanForSecrets(root string) []string {
	markers := []string{
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----",
		"AGE-SECRET-KEY-",
	}
	// The cluster's own key store is not a leak.
	//
	// openCenter generates SSH and age private keys into
	// config/clusters/secrets/ on purpose, 0600, and that is where they belong.
	// Reading a private-key header there and calling it a plaintext secret in
	// generated output is a false positive — and one that failed a whole run
	// before deploy ever got a chance to start. The question this scan asks is
	// whether key material escaped into the *rendered* artifacts.
	var hits []string
	for _, path := range snapshot(root) {
		if strings.Contains(filepath.ToSlash(path), "/config/clusters/secrets/") {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() > 4<<20 {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, marker := range markers {
			if strings.Contains(string(raw), marker) {
				relative, _ := filepath.Rel(root, path)
				hits = append(hits, relative)
				break
			}
		}
	}
	return hits
}

// whoHoldsAPIPort names the cluster already using 127.0.0.1:6443, if any.
//
// Asked of the runtime rather than by opening a socket, because the useful
// answer is not "busy" but "which one" — that is the difference between a
// message somebody can act on and one they have to investigate.
func whoHoldsAPIPort(ctx context.Context, ex *Exec) string {
	runtime := containerRuntime()
	if runtime == "" {
		return ""
	}
	listing := ex.Command(ctx, ex.Run.Root, runtime, "ps",
		"--filter", "publish=6443", "--format", "{{.Label \"io.x-k8s.kind.cluster\"}}|{{.Names}}")
	for _, line := range strings.Split(strings.TrimSpace(listing.Stdout), "\n") {
		cluster, name, _ := strings.Cut(strings.TrimSpace(line), "|")
		if cluster != "" {
			return cluster
		}
		if name != "" {
			return name
		}
	}
	return ""
}

func present(tools []Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name && tool.Present {
			return true
		}
	}
	return false
}

func countPresent(tools []Tool) int {
	count := 0
	for _, tool := range tools {
		if tool.Present {
			count++
		}
	}
	return count
}

func remove(list []string, unwanted string) []string {
	var out []string
	for _, item := range list {
		if item != unwanted {
			out = append(out, item)
		}
	}
	return out
}

func mustJSON(value any) []byte {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return encoded
}
