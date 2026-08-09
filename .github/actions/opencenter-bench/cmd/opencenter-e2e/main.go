// Command opencenter-e2e runs the cluster lifecycle end to end.
//
// One binary, driven identically from a laptop and from GitHub Actions. The
// workflow says --execution-channel github-actions and nothing else changes:
// same phases, same assertions, same evidence. That is the point of the brief's
// "do not reimplement the test logic in YAML" — the workflow file calls this.
//
// Headless on purpose. There is one console in this repository — testlab — and
// the lifecycle is a part of it rather than a second web UI on a second port.
// Two consoles is two places to look and two things to keep in step.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/redact"

	"github.com/opencenter-cloud/opencli-testbench/internal/e2e"
)

const usage = `openCenter E2E cluster lifecycle test bench

  opencenter-e2e e2e plan     --profile kind
  opencenter-e2e e2e run      --profile kind
  opencenter-e2e e2e resume   --run-id e2e-20260807-001
  opencenter-e2e e2e phase    --run-id <id> --only-phase kubernetes-health
  opencenter-e2e e2e diagnose --run-id <id>
  opencenter-e2e e2e cleanup  --run-id <id>
  opencenter-e2e e2e profiles
  opencenter-e2e e2e phases

  The console is ./bin/testlab — the lifecycle appears there, not here.

Flags:
  --profile             which combination to run (see: e2e profiles)
  --execution-channel   local (default) or github-actions
  --cli-repo            the openCenter CLI source tree to build
  --report-dir          where runs are written (default ./artifacts)
  --run-id              an existing run, for resume/phase/diagnose/cleanup
  --from-phase --to-phase --only-phase --skip-phase
  --approve-live        required by the real-provider profiles
  --destroy-after-test  destroy when finished (default true)
  --keep-on-failure     keep the environment if the run fails
  --simulate            run the infrastructure phases against a built-in fake,
                        so every feature can be exercised before a cluster
                        exists. Such a run can never PASS. It reports SIMULATED
                        (exit 4) when nothing failed for real, and FAIL (exit 2)
                        when a phase that still runs for real failed inside it —
                        validate-config and doctor do, so an emulated profile
                        with no reachable provider will reach FAIL, not
                        SIMULATED. Either way it cannot gate a release.
  --timeout             whole-run deadline (default 60m)
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "opencenter-e2e: "+err.Error())
		os.Exit(1)
	}
}

type options struct {
	profile     string
	channel     string
	cliRepo     string
	reportDir   string
	runID       string
	from, to    string
	only, skip  stringList
	approveLive bool
	destroy     bool
	keepOnFail  bool
	simulate    bool
	timeout     time.Duration
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func run() error {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Print(usage)
		return nil
	}
	if os.Args[1] != "e2e" {
		return fmt.Errorf("unknown command %q. Try: opencenter-e2e e2e run --profile kind", os.Args[1])
	}
	if len(os.Args) < 3 {
		fmt.Print(usage)
		return nil
	}
	action := os.Args[2]

	opts := options{}
	flags := flag.NewFlagSet("e2e "+action, flag.ContinueOnError)
	flags.StringVar(&opts.profile, "profile", "configuration-only", "")
	flags.StringVar(&opts.channel, "execution-channel", "local", "")
	flags.StringVar(&opts.cliRepo, "cli-repo", defaultCLIRepo(), "")
	flags.StringVar(&opts.reportDir, "report-dir", "artifacts", "")
	flags.StringVar(&opts.runID, "run-id", "", "")
	flags.StringVar(&opts.from, "from-phase", "", "")
	flags.StringVar(&opts.to, "to-phase", "", "")
	flags.Var(&opts.only, "only-phase", "")
	flags.Var(&opts.skip, "skip-phase", "")
	flags.BoolVar(&opts.approveLive, "approve-live", false, "")
	flags.BoolVar(&opts.destroy, "destroy-after-test", true, "")
	flags.BoolVar(&opts.keepOnFail, "keep-on-failure", false, "")
	flags.BoolVar(&opts.simulate, "simulate", false, "")
	flags.DurationVar(&opts.timeout, "timeout", time.Hour, "")
	flags.SetOutput(os.Stderr)
	if err := flags.Parse(os.Args[3:]); err != nil {
		return err
	}

	switch action {
	case "serve":
		// Named rather than silently unknown: the prototype this came from
		// served its own page here, and somebody who learned that command
		// deserves to be told where it went rather than told it never existed.
		return fmt.Errorf("there is no separate E2E console. The lifecycle is a " +
			"section of the bench's own console: run ./bin/testlab")
	case "profiles":
		return listProfiles()
	case "phases":
		return listPhases()
	case "plan":
		return plan(opts)
	// The third argument is whether the phases named by --only-phase run again
	// even though an earlier execution already recorded a result for them.
	//
	// run     no  — nothing has run yet
	// resume  no  — "carry on from where you stopped" is the whole meaning
	// phase   YES — naming a phase is asking for it, again
	// diagnose YES — collecting diagnostics twice is free and the point
	// cleanup YES — this is the verb for "that run left something; remove it",
	//               and it is the one that was silently doing nothing
	case "run":
		return execute(opts, false, false)
	case "resume":
		return execute(opts, true, false)
	case "phase":
		if len(opts.only) == 0 {
			return fmt.Errorf("e2e phase needs --only-phase")
		}
		return execute(opts, true, true)
	case "diagnose":
		opts.only = stringList{string(e2e.PhaseDiagnostics)}
		return execute(opts, true, true)
	case "cleanup":
		opts.only = stringList{string(e2e.PhaseDestroy), string(e2e.PhaseVerifyCleanup)}
		return execute(opts, true, true)
	}
	return fmt.Errorf("unknown action %q. Try: plan, run, resume, phase, diagnose, cleanup, profiles, phases", action)
}

func listProfiles() error {
	fmt.Printf("  %-22s %-18s %-11s %-8s %s\n", "PROFILE", "INFRASTRUCTURE", "PROVIDER", "DEPLOYS", "NEEDS")
	for _, profile := range e2e.Profiles {
		deploys := "no"
		if profile.Deploys {
			deploys = "yes"
		}
		if profile.LiveApproval {
			deploys = "yes*"
		}
		fmt.Printf("  %-22s %-18s %-11s %-8s %s\n", profile.Name, profile.Infrastructure,
			profile.Provider, deploys, strings.Join(profile.Tools, " "))
	}
	fmt.Println("\n  * needs --approve-live: creates real infrastructure.")
	return nil
}

func listPhases() error {
	fmt.Printf("  %-3s %-20s %-8s %-8s %s\n", "#", "PHASE", "CREATES", "REQUIRED", "PURPOSE")
	for _, phase := range e2e.Order {
		fmt.Printf("  %-3d %-20s %-8s %-8s %s\n", phase.Number, phase.ID,
			yesNo(phase.Creates), yesNo(phase.Required), phase.Purpose)
	}
	return nil
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "—"
}

func plan(opts options) error {
	profile, err := e2e.FindProfile(opts.profile)
	if err != nil {
		return err
	}
	selected, err := selection(opts, profile).Resolve()
	if err != nil {
		return err
	}

	fmt.Printf("\n  PLAN — %s\n\n", profile.Name)
	fmt.Printf("    execution channel   %s\n", opts.channel)
	fmt.Printf("    infrastructure      %s\n", profile.Infrastructure)
	fmt.Printf("    provider            %s\n", profile.Provider)
	fmt.Printf("    cluster type        %s\n", profile.ClusterType)
	fmt.Printf("    deploys             %v\n", profile.Deploys)
	fmt.Printf("    destroy after test  %v\n", opts.destroy)
	fmt.Printf("    timeout             %s\n", opts.timeout)
	fmt.Printf("    needs               %s\n", strings.Join(profile.Tools, ", "))
	fmt.Printf("\n    %s\n", profile.Notes)

	if profile.LiveApproval {
		fmt.Printf("\n  THIS PROFILE CREATES REAL INFRASTRUCTURE\n\n")
		for _, line := range []string{
			"this is an approved disposable test environment",
			"you approve infrastructure creation",
			"you approve automatic destruction",
			"you understand that provider costs may be incurred",
		} {
			mark := " "
			if opts.approveLive {
				mark = "x"
			}
			fmt.Printf("    [%s] %s\n", mark, line)
		}
		if !opts.approveLive {
			fmt.Printf("\n    Pass --approve-live to confirm all four.\n")
		}
	}

	fmt.Printf("\n  PHASES (%d of %d)\n\n", len(selected), len(e2e.Order))
	wanted := map[e2e.ID]bool{}
	for _, id := range selected {
		wanted[id] = true
	}
	for _, phase := range e2e.Order {
		mark := "  -  "
		if wanted[phase.ID] {
			mark = "  run"
		}
		fmt.Printf("  %s %2d %-20s %s\n", mark, phase.Number, phase.ID, phase.Purpose)
	}
	fmt.Println()
	return nil
}

func selection(opts options, profile e2e.Profile) e2e.Selection {
	skip := make([]e2e.ID, 0, len(opts.skip))
	for _, id := range opts.skip {
		skip = append(skip, e2e.ID(id))
	}
	// A profile that does not deploy skips the phases that need a cluster. Left
	// in, they would fail for the honest reason that nothing was deployed, which
	// is not a finding about the product.
	// A simulated run exercises the cluster phases rather than skipping them —
	// that is the point of it. Without this, --simulate on a non-deploying
	// profile would produce the same run it always did.
	if !opts.simulate {
		skip = append(skip, profile.SkipsFrom()...)
	}

	only := make([]e2e.ID, 0, len(opts.only))
	for _, id := range opts.only {
		only = append(only, e2e.ID(id))
	}
	return e2e.Selection{From: e2e.ID(opts.from), To: e2e.ID(opts.to), Only: only, Skip: skip}
}

func execute(opts options, resuming, rerunsNamedPhases bool) error {
	profile, err := e2e.FindProfile(opts.profile)
	if err != nil {
		return err
	}
	channel := e2e.Channel(opts.channel)
	if channel != e2e.ChannelLocal && channel != e2e.ChannelGitHub {
		return fmt.Errorf("--execution-channel must be local or github-actions, not %q", opts.channel)
	}

	root, err := filepath.Abs(opts.reportDir)
	if err != nil {
		return err
	}

	var record *e2e.Run
	if resuming {
		if opts.runID == "" {
			return fmt.Errorf("that action needs --run-id")
		}
		record, err = e2e.LoadRun(filepath.Join(root, opts.runID))
		if err != nil {
			return err
		}
		// Resuming into a different profile would run one profile's phases
		// against another's cluster.
		if record.Profile != profile.Name && opts.profile != "configuration-only" {
			return fmt.Errorf("run %s used profile %q; refusing to resume it as %q",
				record.ID, record.Profile, profile.Name)
		}
		profile, _ = e2e.FindProfile(record.Profile)
		fmt.Printf("\n  RESUMING %s (%s)\n", record.ID, record.Profile)
	} else {
		now := time.Now()
		record = e2e.NewRun(profile, channel, "", now)
		record.Root = filepath.Join(root, record.ID)
		if err := os.MkdirAll(record.Root, 0o755); err != nil {
			return err
		}
		record.DestroyAfter = opts.destroy
		record.KeepOnFail = opts.keepOnFail
		record.Approved = opts.approveLive
		record.Timeout = opts.timeout
		fmt.Printf("\n  RUN %s — %s on %s\n", record.ID, profile.Name, profile.Provider)
	}
	record.Approved = record.Approved || opts.approveLive

	selected, err := selection(opts, profile).Resolve()
	if err != nil {
		return err
	}
	record.Selection = selection(opts, profile)

	redactor := redact.New()
	// Everything the process was given that looks like a secret goes in before
	// the first command runs, so nothing is written unredacted even once.
	redactor.AddFromEnv(environmentMap())

	bodies := e2e.DefaultBodies()
	if opts.simulate {
		record.Simulated = true
		bodies = e2e.SimulatedBodies(bodies)
		// Named exactly, and the ones that are NOT simulated named too. The
		// difference decides what the verdict can be: configure, validate-config
		// and doctor still run for real, so a profile whose provider is not
		// reachable reaches FAIL rather than SIMULATED — which reads as a broken
		// bench unless it was said in advance.
		fmt.Println("\n  --simulate: deploy, infrastructure, Kubernetes, platform, smoke")
		fmt.Println("  and failure-tests come from a built-in fake. Everything else —")
		fmt.Println("  build, configure, validate-config, doctor, generate, destroy —")
		fmt.Println("  still runs for real, so a real failure in one of those is a real")
		fmt.Println("  FAIL. This run cannot reach PASS by any route.")
	}

	// A phase named with --only-phase runs again, whatever an earlier execution
	// recorded for it. See Engine.Rerun: `e2e phase --only-phase destroy` used
	// to print destroy's stored "blocked" and do nothing, which is also what the
	// Reproduce line on every finding does.
	//
	// Only for the phase-shaped verbs. `resume` also carries --only-phase
	// occasionally, but its meaning there is "carry on, and limit yourself to
	// these" — repeating a finished build under it would be wrong.
	rerun := map[e2e.ID]bool{}
	if rerunsNamedPhases {
		for _, id := range opts.only {
			rerun[e2e.ID(id)] = true
		}
	}

	engine := &e2e.Engine{
		Run: record, Profile: profile, Redactor: redactor, Out: os.Stdout,
		Bodies: bodies, CLIRepo: opts.cliRepo, MisePath: findMise(),
		Rerun: rerun,
	}
	engine.SetStart(time.Now())
	if record.CLIBinary != "" {
		engine.CLIBinary = record.CLIBinary
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	// Ctrl-C cancels the run rather than killing it, so destroy and diagnostics
	// still happen. A test bench that leaks a cluster when interrupted is worse
	// than one that takes another minute to stop.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		fmt.Println("\n  interrupted — cleaning up. Press Ctrl-C again to abandon the run.")
		cancel()
	}()

	fmt.Println()
	if err := engine.Execute(ctx, selected); err != nil {
		return err
	}

	verdict, why := record.Gate()
	fmt.Printf("\n  %s — %s\n", verdict, why)
	fmt.Printf("  evidence: %s\n\n", record.Root)

	switch verdict {
	case e2e.VerdictFail:
		os.Exit(2)
	case e2e.VerdictInconclusive:
		os.Exit(3)
	case e2e.VerdictSimulated:
		// Its own exit code, so no CI job can treat a simulated run as a pass by
		// checking only for zero.
		os.Exit(4)
	}
	return nil
}

func environmentMap() map[string]string {
	out := map[string]string{}
	for _, entry := range os.Environ() {
		if key, value, found := strings.Cut(entry, "="); found {
			out[key] = value
		}
	}
	return out
}

// findMise locates mise.
//
// PATH first, and that is the fix rather than a tidy-up. This used to check
// three hardcoded paths only — the comment even said "installed on this machine
// but not on PATH" — which is true of this laptop and false of a GitHub runner,
// where jdx/mise-action installs it somewhere else entirely and puts it on PATH.
// So every CI run found no mise, blocked at the build phase, came out
// INCONCLUSIVE and exited 3. Four profiles, every commit, all failing on a
// lookup that never asked the obvious question.
//
// The hardcoded candidates stay behind it, because they are what makes this
// work in a shell that has not sourced a profile.
func findMise() string {
	if path, err := exec.LookPath("mise"); err == nil {
		return path
	}
	home, _ := os.UserHomeDir()
	for _, candidate := range []string{
		filepath.Join(home, ".local", "bin", "mise"),
		filepath.Join(home, ".local", "share", "mise", "bin", "mise"),
		"/usr/local/bin/mise", "/usr/bin/mise",
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func defaultCLIRepo() string {
	home, _ := os.UserHomeDir()
	candidate := filepath.Join(home, "opencenter", "openCenter-cli-testDzoan")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}
