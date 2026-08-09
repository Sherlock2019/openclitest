package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
	"github.com/opencenter-cloud/opencli-testbench/internal/preflight"
	"github.com/opencenter-cloud/opencli-testbench/internal/report"
	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
	"github.com/opencenter-cloud/opencli-testbench/internal/workflow"
)

// commandFull is the headless twin of the console's one button: preflight,
// thirty modules in order, cleanup, then four reports.
func commandFull(ctx context.Context, root string, loaded *spec.Spec, args []string) error {
	flags := flag.NewFlagSet("run full", flag.ContinueOnError)
	source := flags.String("source", "", "the openCenter CLI checkout the binary came from")
	binary := flags.String("bin", "", "the opencenter binary to test")
	providers := flags.String("provider", "kind", "disposable environments for Module 29: kind, flex, or both")
	approveLive := flags.Bool("approve-live", false, "allow Module 29 to create real infrastructure")
	approveCleanup := flags.Bool("approve-cleanup", false, "allow automatic cleanup afterwards")
	disposable := flags.Bool("confirm-disposable", false, "confirm this is a disposable environment")
	reportDir := flags.String("report-dir", "", "where to write the reports (default: inside the run workspace)")
	quick := flags.Bool("quick", false, "skip the checks measured in minutes")
	keep := flags.Bool("keep-workspace", false, "leave the run workspace in place")
	carryOn := flags.Bool("continue-on-blocking-failure", false, "do not stop when a blocking module fails")
	asJSON := flags.Bool("json", false, "write the report to stdout as JSON instead of text")
	if err := flags.Parse(args); err != nil {
		return err
	}

	path := *binary
	if path == "" {
		located, err := cli.Locate(root)
		if err != nil {
			return err
		}
		path = located
	}

	plan, err := workflow.LoadPlan(loaded, root)
	if err != nil {
		return err
	}

	// All three confirmations, or no live testing. Passing two of the three on
	// a command line is not consent to the third.
	var agreed []string
	if *approveLive && *approveCleanup && *disposable {
		for _, confirmation := range plan.Safety.Confirmations {
			agreed = append(agreed, confirmation.ID)
		}
	}
	live := *approveLive && *approveCleanup && *disposable

	if *approveLive && !live {
		fmt.Fprintln(os.Stderr,
			"note: --approve-live also needs --approve-cleanup and --confirm-disposable; Module 29 stays locked")
	}

	options := workflow.Options{
		Root:                  root,
		Binary:                path,
		Source:                *source,
		Live:                  live,
		LiveProviders:         split(*providers),
		Agreed:                agreed,
		Credentials:           credentialsFromAllMethods(loaded),
		StopOnBlockingFailure: !*carryOn,
		SkipSlow:              *quick,
		KeepWorkspace:         *keep,
	}

	engine := workflow.New(loaded, plan, options, func(event workflow.Event) {
		if *asJSON {
			return
		}
		printWorkflowEvent(event, len(plan.Modules))
	})

	run, err := engine.Execute(ctx)
	if run == nil {
		return err
	}

	destination := *reportDir
	if destination == "" {
		destination = filepath.Join(run.Root, "reports")
	}
	written, writeErr := report.WriteAll(run, engine.Redactor(), destination)
	if writeErr != nil {
		fmt.Fprintln(os.Stderr, "could not write the reports:", writeErr)
	}

	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if encodeErr := encoder.Encode(run); encodeErr != nil {
			return encodeErr
		}
	} else {
		printWorkflowSummary(run, written)
	}

	if err != nil {
		return err
	}
	if !run.OK() {
		return fmt.Errorf("%d modules failed, %d were not answered",
			run.Failed, run.Blocked+run.Skipped+run.NotRun)
	}
	return nil
}

func printWorkflowEvent(event workflow.Event, total int) {
	switch event.Type {
	case "run-start":
		fmt.Printf("\n  openCenter CLI full A-to-Z test\n  %s\n\n", event.Message)
	case "phase":
		fmt.Printf("\n  %s\n", event.Message)
	case "preflight":
		fmt.Printf("  preflight: %s\n", event.Message)
	case "module-start":
		fmt.Printf("\n  %2d/%d  %s\n", event.Order, total, event.Message)
	case "module-done":
		module := event.Module
		fmt.Printf("        %-9s %s\n", moduleSymbol(module.State), moduleDetail(module))
		for _, result := range module.Results {
			for _, assertion := range result.Assertions {
				if assertion.Status == "fail" {
					fmt.Printf("          - %s: %s\n", assertion.Name, assertion.Detail)
				}
			}
		}
	case "log":
		fmt.Printf("        · %s\n", event.Message)
	case "run-error":
		fmt.Printf("\n  stopped: %s\n", event.Message)
	}
}

func moduleSymbol(state workflow.ModuleState) string {
	return map[workflow.ModuleState]string{
		workflow.StatePassed:    "passed",
		workflow.StateFailed:    "FAILED",
		workflow.StateWarning:   "warning",
		workflow.StateBlocked:   "BLOCKED",
		workflow.StateSkipped:   "skipped",
		workflow.StateLocked:    "locked",
		workflow.StateCancelled: "cancelled",
	}[state]
}

func moduleDetail(module *workflow.ModuleResult) string {
	if module.Checks() == 0 {
		return module.Message
	}
	detail := fmt.Sprintf("%d/%d checks", module.Passed, module.Checks())
	if module.Millis > 0 {
		detail += fmt.Sprintf("  %.1fs", float64(module.Millis)/1000)
	}
	if module.Message != "" {
		detail += "  " + module.Message
	}
	return detail
}

func printWorkflowSummary(run *workflow.Run, written []string) {
	fmt.Printf("\n  %s\n", strings.ToUpper(string(run.State)))
	fmt.Printf("  %d passed · %d failed · %d warning · %d blocked · %d skipped · %d not run\n",
		run.Passed, run.Failed, run.Warning, run.Blocked, run.Skipped, run.NotRun)
	fmt.Printf("  %.1fs · %s\n", float64(run.Millis)/1000, run.Version)

	if len(run.Resources) > 0 {
		outstanding := 0
		for _, resource := range run.Resources {
			if resource.Status != "deleted" && resource.Status != "not_found" {
				outstanding++
			}
		}
		fmt.Printf("  %d resources created, %d not confirmed gone\n", len(run.Resources), outstanding)
	}

	if len(written) > 0 {
		fmt.Printf("\n  reports\n")
		for _, path := range written {
			fmt.Printf("    %s\n", path)
		}
	}
	fmt.Println()
}

// credentialsFromAllMethods collects whatever is already exported for every
// credential field the bench knows about, so a headless run needs no flags to
// use an existing cloud profile — and no secret ever appears in a command
// line, where it would land in shell history.
func credentialsFromAllMethods(loaded *spec.Spec) map[string]string {
	out := map[string]string{}
	for _, method := range loaded.Credentials {
		for _, field := range method.Fields {
			if field.Env == "" {
				continue
			}
			if value := os.Getenv(field.Env); value != "" {
				out[field.Env] = value
			}
		}
	}
	return out
}

// --- the supporting commands ------------------------------------------------

func commandToolsCheck(ctx context.Context, loaded *spec.Spec, args []string) error {
	return commandPreflight(ctx, loaded, args)
}

func commandCredentialsCheck(ctx context.Context, loaded *spec.Spec, args []string) error {
	report := preflight.Run(ctx, loaded, "flex")
	fmt.Println()
	for _, result := range report.Results {
		symbol := "ok  "
		detail := result.Detail
		if result.Status != preflight.StatusPresent {
			symbol = "miss"
			detail = result.Why
		}
		fmt.Printf("  %s %-24s %s\n", symbol, result.Name, truncate(detail, 70))
	}
	fmt.Printf("\n  %d present, %d missing\n", report.Present, report.Missing)
	fmt.Println("  No credential value is printed here, only whether it works.")
	fmt.Println()
	return nil
}

func commandReport(root string, args []string) error {
	if len(args) == 0 {
		return listRuns(root)
	}
	runPath := filepath.Join(root, "artifacts", "runs", args[0], "run.json")
	run, err := workflow.LoadRun(runPath)
	if err != nil {
		return fmt.Errorf("no such run %q: %w", args[0], err)
	}
	fmt.Print(report.Markdown(run))
	return nil
}

func listRuns(root string) error {
	runs, err := workflow.FindRuns(filepath.Join(root, "artifacts"))
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Print("\n  No runs yet. Start one with: bench run full\n\n")
		return nil
	}
	fmt.Println("\n  runs")
	for _, run := range runs {
		fmt.Printf("    %-18s %-26s %d passed, %d failed, %d not answered\n",
			run.ID, run.State, run.Passed, run.Failed, run.Blocked+run.Skipped+run.NotRun)
	}
	fmt.Println()
	return nil
}

// commandCleanup removes what a run left behind, using its own registry. It is
// the escape hatch for a run that was interrupted before Module 30.
func commandCleanup(ctx context.Context, root string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bench cleanup <run-id>")
	}
	runRoot := filepath.Join(root, "artifacts", "runs", args[0])
	run, err := workflow.LoadRun(filepath.Join(runRoot, "run.json"))
	if err != nil {
		return fmt.Errorf("no such run %q: %w", args[0], err)
	}

	outstanding := 0
	for _, resource := range run.Resources {
		if resource.Status == "deleted" || resource.Status == "not_found" {
			continue
		}
		outstanding++
		fmt.Printf("  %s %s (%s) — %s\n", resource.Type, resource.Name, resource.Provider, resource.Status)
		if len(resource.Cleanup) > 0 {
			fmt.Printf("    opencenter %s\n", strings.Join(resource.Cleanup, " "))
		}
	}
	if outstanding == 0 {
		fmt.Println("  Nothing outstanding: every registered resource was confirmed gone.")
	} else {
		fmt.Printf("\n  %d resources may still exist. The commands above remove them.\n", outstanding)
		fmt.Println("  They are printed rather than run: deleting infrastructure is not something")
		fmt.Println("  to do on the strength of a stale file.")
	}

	if err := os.RemoveAll(filepath.Join(runRoot, "home")); err == nil {
		fmt.Println("  Removed the run's sandbox home.")
	}
	return nil
}
