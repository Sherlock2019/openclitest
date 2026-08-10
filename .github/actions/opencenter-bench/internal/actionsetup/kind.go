package actionsetup

import (
	"fmt"
	"strings"

	"github.com/opencenter-cloud/opencli-testbench/internal/e2e"
)

// Two workflows, one installer.
//
// This package was written when there was one thing to install: the command
// bench's own workflow, at a path that was a constant precisely so the blast
// radius of "configure my repository for me" stayed at one known file. The
// cluster lifecycle needs a second workflow, and the temptation is to make the
// path a setting.
//
// It is not a setting. It is a Kind: a closed set of two, each carrying its own
// constant path, filename and branch. A repository owner agreeing to this is
// agreeing to one of two named files, not to an arbitrary write under
// .github/workflows — and the validate step still refuses a commit that touches
// anything else.

// Kind names which workflow is being installed, listed or triggered.
type Kind string

const (
	// KindTestBench is the command bench: every CLI command, on every
	// environment, judged.
	KindTestBench Kind = "test-bench"

	// KindE2E is the cluster lifecycle: build, deploy, prove healthy, destroy,
	// prove nothing was left.
	KindE2E Kind = "opencenter-e2e"
)

// Kinds is the closed set, for a caller that wants to offer a choice.
func Kinds() []Kind { return []Kind{KindTestBench, KindE2E} }

// Environment variables carrying the choice and the lifecycle's settings, so
// the console can pass them the same way it passes everything else.
const (
	EnvKind = "OPENCLI_ACTIONS_KIND"

	EnvE2ECLIRepo     = "OPENCLI_E2E_CLI_REPO"
	EnvE2ENightly     = "OPENCLI_E2E_NIGHTLY"
	EnvE2ETimeout     = "OPENCLI_E2E_TIMEOUT_MINUTES"
	EnvE2EEnvironment = "OPENCLI_E2E_REAL_ENVIRONMENT"
	EnvE2EDestroy     = "OPENCLI_E2E_DESTROY_AFTER_TEST"
	EnvE2ESkipPhases  = "OPENCLI_E2E_SKIP_PHASES"
)

// ParseKind turns a string into a kind, refusing anything that is not one.
//
// Refusing rather than defaulting. A typo that silently installs the other
// workflow puts a file somebody did not ask for into somebody else's
// repository, and "it defaulted" is not a defence.
func ParseKind(value string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(KindTestBench), "testbench", "bench":
		return KindTestBench, nil
	case string(KindE2E), "e2e", "lifecycle":
		return KindE2E, nil
	}
	return "", fmt.Errorf("no workflow called %q. The workflows are: %s, %s",
		value, KindTestBench, KindE2E)
}

// resolved defaults an empty kind to the command bench.
//
// Empty means "a caller written before there were two", and those callers all
// meant the test bench. Defaulting keeps every existing call site correct
// without touching it.
func (k Kind) resolved() Kind {
	if k == KindE2E {
		return KindE2E
	}
	return KindTestBench
}

// File is the workflow's filename, as the GitHub API names it when listing runs.
func (k Kind) File() string {
	if k.resolved() == KindE2E {
		return "opencenter-e2e.yml"
	}
	return "test-bench.yml"
}

// Path is the only path this kind may ever write.
func (k Kind) Path() string { return ".github/workflows/" + k.File() }

// Branch is deterministic, so re-running reuses the branch and its pull request
// rather than opening a second one. One per kind, so installing both does not
// put two unrelated files on one branch.
func (k Kind) Branch() string {
	if k.resolved() == KindE2E {
		return "automation/opencenter-e2e-setup"
	}
	return BranchName
}

// String is the resolved name, so an empty kind prints as what it actually is
// rather than as nothing.
func (k Kind) String() string { return string(k.resolved()) }

// Label is what a person calls it.
func (k Kind) Label() string {
	if k.resolved() == KindE2E {
		return "cluster lifecycle E2E"
	}
	return "CLI test bench"
}

// Valid reports whether a string names a kind. Used at the API edge, so a typo
// is refused rather than silently installing the other workflow.
func (k Kind) Valid() bool {
	return k == "" || k == KindTestBench || k == KindE2E
}

// E2EOptions are the choices that only the lifecycle workflow has.
//
// Kept in their own struct rather than added to Options as loose fields,
// because every one of them would be meaningless on the command bench's
// workflow and a reader should not have to work out which half applies.
type E2EOptions struct {
	// BenchRepo is the published action to call, owner/repo[@ref].
	//
	// Empty takes DefaultE2EAction. A ref can be pinned — `@v1` rather than
	// `@main` — so a change in the bench cannot move a verdict in somebody
	// else's repository without a commit of theirs.
	//
	// This used to be a repository to check out and build, which was wrong for
	// the same three reasons the command bench's action documents: the caller's
	// token is not scoped to it, a checkout of @main ignores whatever the caller
	// pinned, and it is a network round trip for files GitHub has already put on
	// the runner.
	BenchRepo string

	// CLIRepo is the openCenter CLI source, as owner/name.
	//
	// Empty means "the repository this workflow is committed to", which is the
	// case that matters: installed into the CLI repository, the run must build
	// the commit that triggered it. Naming a repository here instead pins CI to
	// a fixed ref, so every run would test the same old commit and report green
	// while the change under review went untested.
	CLIRepo string

	// Nightly is the profile the scheduled job runs. Empty leaves the schedule
	// out entirely rather than writing a cron that runs a default nobody chose.
	Nightly string

	// TimeoutMinutes bounds the deploying jobs. A lifecycle that hangs at deploy
	// otherwise holds a cluster open until GitHub's six-hour ceiling, which is
	// both a bill and a queue nobody can clear.
	TimeoutMinutes int

	// DestroyAfterTest is the default for the dispatch input.
	DestroyAfterTest bool

	// SkipPhases are the lifecycle phases this workflow leaves out, chosen in
	// the console under "What to test".
	//
	// Carried into the workflow rather than left as a local preference, because
	// a scope that only applies on somebody's laptop is not a scope: the two
	// would disagree about what "the run" means, and CI is the answer people
	// quote. The required phases refuse to be skipped whatever arrives here —
	// the engine enforces that, not this list.
	SkipPhases []string

	// RealEnvironment is the GitHub Environment that gates the real-provider
	// jobs. Empty omits those jobs completely: a real-provider job with no
	// environment behind it is an unreviewed path to somebody's OpenStack
	// project, and leaving it out is safer than writing it and hoping.
	RealEnvironment string

	// RunsOn is the runner label for the jobs that reach infrastructure.
	//
	// A GitHub-hosted runner cannot see a private OpenStack, a vCenter or a
	// bare-metal BMC — they are on somebody's private network by definition. So
	// the profiles that touch real infrastructure have to be able to run
	// somewhere that can, and that is a self-hosted runner. Hardcoding
	// ubuntu-latest made the real-provider job unusable for exactly the three
	// providers it exists for.
	//
	// The safe jobs stay on ubuntu-latest regardless: they create nothing, need
	// no network, and running them on a private runner would put fork pull
	// requests on somebody's infrastructure.
	RunsOn string
}

// DefaultE2EAction is the published action a generated lifecycle workflow
// calls, owner/repo@ref.
//
// The same shape as DefaultAction, which the command bench has used all along.
// A ref rather than a bare repository, so a caller can pin: `@v1` tests the
// bench version it asked for, where `@main` lets a change here move a verdict
// in somebody else's repository with no commit of theirs.
const DefaultE2EAction = "Sherlock2019/fullopenclitestbench@main"

// action is the published action to call.
func (e E2EOptions) action() string {
	if value := strings.TrimSpace(e.BenchRepo); value != "" {
		if !strings.Contains(value, "@") {
			value += "@main"
		}
		return value
	}
	return DefaultE2EAction
}

// cliRepo is the CLI source, or empty for "the repository this is committed to".
//
// Empty is the important case and the default. Installed into the CLI
// repository, the run has to build the commit that triggered it; naming a
// repository pins CI to a fixed ref, so every run would test the same old
// commit and report green while the change under review went untested.
func (e E2EOptions) cliRepo() string { return strings.TrimSpace(e.CLIRepo) }

// runsOn is the runner for the jobs that reach infrastructure.
//
// Rendered as a workflow_dispatch input with a default rather than baked in, so
// an operator can send one run to a private runner without editing the file —
// and so the default stays visible in the file rather than hidden in this code.
func (e E2EOptions) runsOn() string {
	if value := strings.TrimSpace(e.RunsOn); value != "" {
		return value
	}
	return "ubuntu-latest"
}

func (e E2EOptions) timeout() int {
	if e.TimeoutMinutes > 0 {
		return e.TimeoutMinutes
	}
	return 60
}

// safeProfiles are the profiles a pull request may run: the ones that create
// nothing.
//
// Read from the catalogue rather than listed here, so a profile added to
// internal/e2e cannot be missing from CI and — the part that matters — a
// profile that starts deploying cannot stay on the pull-request matrix because
// somebody forgot to move it.
func safeProfiles() []string {
	var out []string
	for _, profile := range e2e.Profiles {
		if !profile.Deploys && !profile.LiveApproval {
			out = append(out, profile.Name)
		}
	}
	// Kind, on top of the ones that create nothing.
	//
	// It deploys, so it does not qualify above — but what it deploys is
	// containers on the runner's own Docker, which GitHub provides and throws
	// away when the job ends. Nothing is billed, nothing outlives the job, and
	// no credential is involved. That is a different kind of "deploys" from the
	// real-provider profiles, which reach somebody's OpenStack and cost money.
	//
	// It is the only profile that proves a cluster can actually be stood up and
	// taken down, so leaving it out meant every automatic run tested everything
	// except the thing the bench is named after. It costs about three extra
	// minutes.
	//
	// LiveApproval is still the line nothing crosses: the three *-real profiles
	// remain dispatch-only, behind an environment with a human on it.
	for _, profile := range e2e.Profiles {
		if profile.Infrastructure == e2e.InfraKind && !profile.LiveApproval {
			out = append(out, profile.Name)
		}
	}
	return out
}

func allProfiles() []string {
	out := make([]string, 0, len(e2e.Profiles))
	for _, profile := range e2e.Profiles {
		out = append(out, profile.Name)
	}
	return out
}

// realSecrets are the provider credentials a real profile needs.
//
// Per provider rather than a union of all of them: a workflow that maps every
// secret into every job teaches people the list is decoration, and then the one
// that matters is lost among the seven that do not.
func realSecrets(provider e2e.Provider) []string {
	switch provider {
	case e2e.ProviderOpenStack:
		return []string{"OS_AUTH_URL", "OS_APPLICATION_CREDENTIAL_ID",
			"OS_APPLICATION_CREDENTIAL_SECRET"}
	case e2e.ProviderVMware:
		return []string{"VSPHERE_SERVER", "VSPHERE_USER", "VSPHERE_PASSWORD"}
	case e2e.ProviderBareMetal:
		return []string{"BMC_ENDPOINT", "BMC_USERNAME", "BMC_PASSWORD"}
	}
	return nil
}

// realSecretNames is every provider secret the real profiles could need,
// deduplicated and in a stable order.
func realSecretNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, profile := range e2e.Profiles {
		if !profile.LiveApproval {
			continue
		}
		for _, name := range realSecrets(profile.Provider) {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// safeDefault is the profile the dispatch form starts on: the cheapest one that
// creates nothing, so the least dangerous option is already selected.
func safeDefault() string {
	if names := safeProfiles(); len(names) > 0 {
		return names[0]
	}
	return "configuration-only"
}

// e2eWorkflow renders the cluster lifecycle workflow.
//
// What the file says is deliberately thin. It chooses *when* to run and *what*
// to keep, and never *how* to test: every job is a wrapper around one
// invocation of one binary. A second implementation of the phases in YAML is
// one that disagrees with the local one at the worst possible moment, which is
// the whole reason this bench is a binary rather than a workflow.
func e2eWorkflow(o Options) []byte {
	e := o.E2E
	var b strings.Builder

	b.WriteString("# " + KindE2E.Path() + "\n")
	b.WriteString("#\n")
	b.WriteString("# Installed by the openCenter cluster lifecycle test bench.\n")
	b.WriteString("#\n")
	b.WriteString("# Every job below is a thin wrapper around one action, which builds this\n")
	b.WriteString("# repository at the commit that triggered the run and puts it through the\n")
	b.WriteString("# twenty-one-phase cluster lifecycle.\n")
	b.WriteString("#\n")
	b.WriteString("# The phases, assertions, evidence and cleanup live in that binary and\n")
	b.WriteString("# nowhere else. This file chooses when to run and what to keep —\n")
	b.WriteString("# deliberately not how to test.\n")
	b.WriteString("name: openCenter E2E\n\n")

	// --- triggers ---------------------------------------------------------
	b.WriteString("on:\n")
	b.WriteString("  workflow_dispatch:\n")
	b.WriteString("    inputs:\n")
	b.WriteString("      profile:\n")
	b.WriteString("        description: Which lifecycle to run\n")
	b.WriteString("        type: choice\n")
	b.WriteString("        default: " + safeDefault() + "\n")
	b.WriteString("        options:\n")
	for _, name := range allProfiles() {
		b.WriteString("          - " + name + "\n")
	}
	b.WriteString("      runs_on:\n")
	b.WriteString("        description: >-\n")
	b.WriteString("          Runner for the jobs that reach infrastructure. A GitHub-hosted\n")
	b.WriteString("          runner cannot see a private OpenStack, a vCenter or a BMC, so\n")
	b.WriteString("          those need a self-hosted label here.\n")
	b.WriteString("        type: string\n")
	b.WriteString("        default: " + e.runsOn() + "\n")
	b.WriteString("      destroy_after_test:\n")
	b.WriteString("        description: Destroy the environment when finished\n")
	b.WriteString("        type: boolean\n")
	b.WriteString(fmt.Sprintf("        default: %v\n", e.DestroyAfterTest))
	// A push that only edits a workflow file tests nothing.
	//
	// Editing the workflow changes no openCenter code, so running the suite
	// against it proves nothing and costs a cluster build. Installing the
	// workflow started a full run for exactly that reason.
	//
	// The trigger button still works: it touches
	// .opencenter-test-bench-trigger, which is outside this filter — that file
	// exists only because this filter does.
	// And the other bench's trigger marker.
	//
	// Both workflows ran on every trigger press, whichever button was pushed:
	// the branch was per-kind but the marker file was not, and a workflow
	// decides from the paths a push touched, not from its branch. Ignoring the
	// other's marker makes a commit that touches only it a push this workflow
	// has no reason to care about. A real commit still runs both, which is the
	// point of installing both.
	ignore := "['.github/workflows/**', '" + otherTriggerMarker(KindE2E) + "']"
	b.WriteString("  pull_request:\n")
	b.WriteString("    paths-ignore: " + ignore + "\n")
	// Every commit, not only every pull request.
	//
	// Without this the workflow installs, sits there, and never runs: four
	// pushes to a repository carrying it produced four runs of the *other*
	// workflow and none of this one. "Every commit is tested from then on" is
	// what the card promises, and a push trigger is what makes that true.
	b.WriteString("  push:\n")
	b.WriteString("    paths-ignore: " + ignore + "\n")
	if strings.TrimSpace(e.Nightly) != "" {
		b.WriteString("  schedule:\n")
		b.WriteString("    # Nightly, on the full lifecycle rather than the cheap subset a\n")
		b.WriteString("    # pull request gets.\n")
		b.WriteString("    - cron: \"0 3 * * *\"\n")
	}

	b.WriteString("\npermissions:\n")
	b.WriteString("  contents: read\n")

	b.WriteString("\nconcurrency:\n")
	b.WriteString("  group: e2e-${{ github.ref }}\n")
	b.WriteString("  cancel-in-progress: true\n\n")

	b.WriteString("jobs:\n")

	// --- pull requests ----------------------------------------------------
	b.WriteString("  # Pull requests get the profiles that create nothing: fast,\n")
	b.WriteString("  # credential-free, and safe on a fork. The expensive and privileged\n")
	b.WriteString("  # ones are deliberately absent — a pull request must never be able to\n")
	b.WriteString("  # spend money or reach real infrastructure.\n")
	b.WriteString("  safe:\n")
	// Push as well as pull_request. With only the latter, adding a push trigger
	// above would still have run nothing: every job's condition excluded it, so
	// the workflow would start and immediately skip all three.
	b.WriteString("    if: github.event_name == 'pull_request' || " +
		"github.event_name == 'push'\n")
	b.WriteString("    runs-on: ubuntu-latest\n")
	b.WriteString("    strategy:\n")
	b.WriteString("      fail-fast: false\n")
	b.WriteString("      matrix:\n")
	b.WriteString("        profile:\n")
	for _, name := range safeProfiles() {
		b.WriteString("          - " + name + "\n")
	}
	b.WriteString("    steps:\n")
	e2eSteps(&b, e, "${{ matrix.profile }}")
	e2eEvidence(&b, "e2e-${{ matrix.profile }}")
	b.WriteString("\n")

	// --- nightly ----------------------------------------------------------
	if nightly := strings.TrimSpace(e.Nightly); nightly != "" {
		b.WriteString("  # The full lifecycle: nightly and on demand, not per pull request.\n")
		b.WriteString("  # It builds and destroys a real cluster and takes minutes.\n")
		b.WriteString("  " + e2eJobName(nightly) + ":\n")
		b.WriteString("    if: github.event_name == 'schedule' || (github.event_name == " +
			"'workflow_dispatch' && inputs.profile == '" + nightly + "')\n")
		b.WriteString("    runs-on: ${{ inputs.runs_on || '" + e.runsOn() + "' }}\n")
		b.WriteString(fmt.Sprintf("    timeout-minutes: %d\n", e.timeout()))
		b.WriteString("    steps:\n")
		e2eSteps(&b, e, nightly)
		b.WriteString("          destroy_after_test: \"${{ inputs.destroy_after_test || true }}\"\n")
		e2eEvidence(&b, "e2e-"+nightly)
		b.WriteString("\n")
	}

	// --- real providers ---------------------------------------------------
	if environment := strings.TrimSpace(e.RealEnvironment); environment != "" {
		b.WriteString("  # Real infrastructure. Manual only, and behind a GitHub Environment so\n")
		b.WriteString("  # a human approves before anything is created. Never reachable from a\n")
		b.WriteString("  # pull request, which is what makes fork credentials impossible rather\n")
		b.WriteString("  # than merely unlikely.\n")
		b.WriteString("  real-provider:\n")
		b.WriteString("    if: github.event_name == 'workflow_dispatch' && " +
			"endsWith(inputs.profile, '-real')\n")
		b.WriteString("    runs-on: ${{ inputs.runs_on || '" + e.runsOn() + "' }}\n")
		b.WriteString(fmt.Sprintf("    timeout-minutes: %d\n", e.timeout()*2))
		b.WriteString("    environment: " + environment + "\n")
		b.WriteString("    steps:\n")
		b.WriteString("      - uses: actions/checkout@v7\n\n")
		b.WriteString("      # Secrets reach the process through the environment and never\n")
		b.WriteString("      # through a command line: an argument is visible in the process\n")
		b.WriteString("      # table and in the step's own echoed command, and the bench\n")
		b.WriteString("      # redacts every value it was given before writing any of it down.\n")
		b.WriteString("      - uses: " + e.action() + "\n")
		b.WriteString("        env:\n")
		for _, name := range realSecretNames() {
			b.WriteString("          " + name + ": ${{ secrets." + name + " }}\n")
		}
		b.WriteString("          OPENCENTER_SSH_KEY: ${{ secrets.OPENCENTER_SSH_KEY }}\n")
		b.WriteString("          SOPS_AGE_KEY: ${{ secrets.SOPS_AGE_KEY }}\n")
		b.WriteString("        with:\n")
		b.WriteString("          mode: lifecycle\n")
		b.WriteString("          profile: \"${{ inputs.profile }}\"\n")
		b.WriteString("          approve_live: \"true\"\n")
		b.WriteString("          destroy_after_test: \"true\"\n")
		e.writeSkip(&b)
		if repo := e.cliRepo(); repo != "" {
			b.WriteString("          opencenter_cli_repository: " + repo + "\n")
		}
		e2eEvidence(&b, "e2e-${{ inputs.profile }}")
	}

	return []byte(b.String())
}

// e2eCheckout is the steps every job starts with.
//
// Which tree is which is the whole point, and it was inverted. The workflow is
// installed into somebody else's repository, so:
//
//	the checkout          is the thing under test — the commit that triggered
//	                      this run, which is what must be built and exercised
//	.bench                is this test bench, fetched because it is not there
//
// The first version built the bench in the target repository and pointed
// --cli-repo at a fixed ref of another one. In the bench's own repository that
// happens to work; installed into the CLI repository it cannot — there is no
// bench there to build — and it would have tested a pinned commit rather than
// the one under review.
// e2eSteps is the whole job body: check out, then call the published action.
//
// This used to check out the bench repository and run `mise run build` in it.
// The action does all of that already and does it better, which the command
// bench has been doing since before this existed:
//
//   - it stages the bench from github.action_path, so GitHub has already put
//     the files on the runner and there is no clone, no credential and no
//     network round trip;
//   - it stages the ref the caller pinned, so `uses: …@v1` tests that version
//     rather than whatever main happens to be. A checkout of @main meant a
//     change in the bench could move a CLI verdict with no commit in the CLI;
//   - it checks out and builds the CLI with the CLI's own toolchain, which is
//     a different Go version from the bench's and has to be.
//
// So the generated file is three lines instead of a dozen, and copying an
// existing pattern rather than inventing a second one.
func e2eSteps(b *strings.Builder, e E2EOptions, profile string) {
	b.WriteString("      # The repository under test, at the commit that triggered this run.\n")
	b.WriteString("      - uses: actions/checkout@v7\n\n")
	b.WriteString("      - uses: " + e.action() + "\n")
	b.WriteString("        with:\n")
	b.WriteString("          mode: lifecycle\n")
	b.WriteString("          profile: " + profile + "\n")
	e.writeSkip(b)
	// Blank means "whoever called me" — the zero-configuration case, and the
	// one that makes this usable from any repository. Naming a repository pins
	// CI to a fixed ref, so every run would test the same old commit.
	if repo := e.cliRepo(); repo != "" {
		b.WriteString("          opencenter_cli_repository: " + repo + "\n")
	}
}

// writeSkip names the phases this workflow leaves out, and says so in the file.
//
// A comment, because a workflow that quietly runs fifteen phases of twenty-one
// is the kind of thing somebody discovers a month later while wondering why CI
// never caught something. The line is omitted entirely when nothing is skipped,
// so the common file has nothing extra in it.
func (e E2EOptions) writeSkip(b *strings.Builder) {
	var phases []string
	for _, phase := range e.SkipPhases {
		if trimmed := strings.TrimSpace(phase); trimmed != "" {
			phases = append(phases, trimmed)
		}
	}
	if len(phases) == 0 {
		return
	}
	b.WriteString("          # Chosen in the console under \"What to test\". This run is\n")
	b.WriteString("          # deliberately shorter than the full lifecycle and proves less.\n")
	b.WriteString("          skip_phases: \"" + strings.Join(phases, ",") + "\"\n")
}

// secretPaths are the parts of a run's workspace that must never be uploaded.
//
// Found by grepping a real run's artifacts, not by reasoning about it:
// `cluster init` generates a working SSH private key and an age private key
// into the cluster's own secrets tree, and that tree lives under the run
// directory. Uploading the run directory wholesale therefore publishes two
// private keys as a downloadable artifact.
//
// The brief asks for *sanitized* diagnostic artifacts, and this is what that
// word is for. The redactor cannot help here: it removes secrets from text the
// bench writes, and these are files the CLI wrote, whole and correct.
//
// Excluded by pattern rather than deleted after the fact, because a run that
// dies before its cleanup step would still have uploaded them.
var secretPaths = []string{
	"!artifacts/*/config/clusters/secrets/**",
	"!artifacts/**/*.key",
	"!artifacts/**/id_*",
}

// e2eEvidence says where the evidence went. It does not upload it.
//
// It used to add two upload-artifact steps pointing at artifacts/ — which is
// where the bench writes when it is run by hand, and not where it writes under
// this action. The action puts its run directory in .opencenter-test-bench/
// and uploads it from there itself. So both steps found nothing, and
// if-no-files-found: error turned that into a red job on all four profiles
// after the lifecycle had already finished and reported.
//
// One uploader. A workflow that re-uploads what the action already uploaded is
// guessing at the action's internal layout, and it guessed wrong.
func e2eEvidence(b *strings.Builder, name string) {
	b.WriteString("\n      # No upload step here on purpose. The action uploads its own\n")
	b.WriteString("      # evidence — run directory, JUnit and log — as\n")
	b.WriteString("      # opencenter-test-bench-<profile>-<run id>, with the cluster's\n")
	b.WriteString("      # secrets tree, *.key and id_* excluded: `cluster init` generates a\n")
	b.WriteString("      # real SSH private key and a real age private key in there, and\n")
	b.WriteString("      # uploading the directory whole would publish both as a\n")
	b.WriteString("      # downloadable artifact.\n")
	b.WriteString("      #\n")
	b.WriteString("      # Adding a second uploader here means guessing where the action\n")
	b.WriteString("      # keeps its run directory. That guess was artifacts/, the path the\n")
	b.WriteString("      # bench uses when run by hand, and it was wrong.\n")
}

// e2eJobName turns a profile name into a YAML key.
func e2eJobName(profile string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		}
		return '-'
	}, profile)
	if name = strings.Trim(name, "-"); name == "" {
		return "lifecycle"
	}
	return name
}
