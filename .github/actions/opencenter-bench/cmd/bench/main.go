// Command bench is the openCenter CLI test bench.
//
//	bench              open the console in a browser
//	bench run --env sim
//	bench preflight kind
//	bench list
//	bench sim          run the OpenStack simulator on its own
//
// The console and the terminal read the same specification files and run the
// same checks, so what the browser shows is what actually executed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/opencenter-cloud/opencli-testbench/internal/checks"
	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
	"github.com/opencenter-cloud/opencli-testbench/internal/preflight"
	"github.com/opencenter-cloud/opencli-testbench/internal/runner"
	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
)

func main() {
	// Before anything reads the environment. The console already saved the
	// repository and the deploy key; without this, `bench actions` could not see
	// them and asked the operator to export by hand what they had just typed in.
	loadSavedCredentials()

	if err := run(); err != nil {
		var coded codedError
		if errors.As(err, &coded) {
			// A distinct status, so a workflow can branch on the reason rather
			// than on the wording of a log line.
			fmt.Fprintln(os.Stderr, "bench:", coded.message)
			os.Exit(coded.code)
		}
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

// codedError carries a specific exit status out through the ordinary error
// return, so no command has to call os.Exit from halfway down a call stack and
// skip the deferred cleanup on its way out.
type codedError struct {
	code    int
	message string
}

func (e codedError) Error() string { return e.message }

func exitWith(code int, message string) error {
	return codedError{code: code, message: message}
}

func run() error {
	command := "ui"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}

	root, err := spec.FindRoot(".")
	if err != nil {
		// Fall back to the directory the executable came from, so a built
		// binary works from anywhere.
		if executable, execErr := os.Executable(); execErr == nil {
			root, err = spec.FindRoot(filepath.Dir(executable))
		}
		if err != nil {
			return err
		}
	}

	loaded, err := spec.Load(root)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch command {
	case "ui", "console", "serve":
		return commandUI(ctx, root, loaded, args)
	case "run":
		// `bench run full` is the continuous workflow; `bench run --env sim`
		// is the advanced per-environment runner the workflow is built from.
		if len(args) > 0 && args[0] == "full" {
			return commandFull(ctx, root, loaded, args[1:])
		}
		return commandRun(ctx, root, loaded, args)
	case "full":
		return commandFull(ctx, root, loaded, args)
	case "preflight":
		return commandPreflight(ctx, loaded, args)
	case "tools":
		return commandToolsCheck(ctx, loaded, dropWord(args, "check"))
	case "credentials":
		return commandCredentialsCheck(ctx, loaded, dropWord(args, "check"))
	case "gitops":
		return commandGitOps(ctx, root, args)
	case "trace":
		return commandTrace(root, args)
	case "actions":
		return commandActions(ctx, args)
	case "report":
		return commandReport(root, args)
	case "runs":
		return listRuns(root)
	case "cleanup":
		return commandCleanup(ctx, root, args)
	case "list":
		return commandList(loaded, args)
	case "sim":
		return commandSim(ctx, root, args)
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

// dropWord lets `bench tools check` and `bench tools` mean the same thing,
// so the documented two-word form works without a subcommand parser.
func dropWord(args []string, word string) []string {
	if len(args) > 0 && args[0] == word {
		return args[1:]
	}
	return args
}

func usage() {
	fmt.Print(`openCenter CLI test bench

  bench                     open the console (default)
  bench run full            the whole thing: preflight, 30 modules, cleanup, reports

  bench preflight [scope]   check prerequisites, changing nothing
  bench tools check         the same, phrased as a tool inventory
  bench credentials check   whether the configured credentials work
  bench runs                list previous runs
  bench report <run-id>     print a previous run's report
  bench cleanup <run-id>    show what a run left behind and how to remove it

  bench gitops preflight    whether the last run may be promoted
  bench gitops preview      prepare the GitOps change; push nothing
  bench gitops update --approve
                            commit, push and open a pull request

  bench trace record        record this CI invocation in the ledger
  bench trace count         how many times, and from which repositories
  bench trace list          the most recent invocations

  bench run --env <id>      advanced: one environment, any selection of checks
  bench list                advanced: environments, checks, checklist coverage
  bench sim                 advanced: run the OpenStack simulator on its own

A full run against a disposable Kind cluster:

  bench run full --source /path/to/openCenter-cli --provider kind \
    --approve-live --approve-cleanup --confirm-disposable

The binary under test is found through OPENCLI_BIN, ./bin/opencenter, or PATH.
Module 29 also needs OPENCLI_ALLOW_MUTATE=1 in the environment: the flags are
consent, the variable is intent, and creating infrastructure needs both.

Credentials are read from the environment, never from a command line, so they
do not end up in shell history.
`)
}

// --- run --------------------------------------------------------------------

func commandRun(ctx context.Context, root string, loaded *spec.Spec, args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	environment := flags.String("env", "local", "environment id: local, sim, flex or kind")
	only := flags.String("only", "", "comma-separated check ids")
	categories := flags.String("category", "", "comma-separated checklist category ids")
	mutate := flags.Bool("mutate", false, "allow checks that create real resources")
	skipSlow := flags.Bool("quick", false, "skip the checks measured in minutes")
	asJSON := flags.Bool("json", false, "write the report as JSON instead of text")
	keep := flags.Bool("keep-sandbox", false, "leave the throwaway directory in place")
	binary := flags.String("bin", "", "the opencenter binary to test")
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

	options := runner.Options{
		Root:        root,
		Binary:      path,
		Environment: *environment,
		Only:        split(*only),
		Categories:  split(*categories),
		Mutate:      *mutate,
		SkipSlow:    *skipSlow,
		KeepSandbox: *keep,
		Credentials: credentialsFromEnvironment(loaded, *environment),
	}

	emit := func(event runner.Event) {
		if *asJSON {
			return
		}
		switch event.Type {
		case "run-start":
			fmt.Printf("\n%s\n\n", event.Message)
		case "check-done":
			printCheck(*event.Check, event.Done, event.Total)
		case "log":
			fmt.Printf("  · %s\n", event.Message)
		}
	}

	report, err := runner.Execute(ctx, loaded, options, emit)
	if err != nil {
		return err
	}

	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		printSummary(report)
	}

	if !report.OK() {
		return fmt.Errorf("%d checks failed", report.Failed+report.Errored)
	}
	return nil
}

func printCheck(result checks.Result, done, total int) {
	symbol := map[checks.Status]string{
		checks.StatusPass:  "pass",
		checks.StatusFail:  "FAIL",
		checks.StatusSkip:  "skip",
		checks.StatusError: "ERROR",
	}[result.Status]

	fmt.Printf("  %-5s %2d/%-2d  %-14s %s (%dms)\n",
		symbol, done, total, result.Category, result.Name, result.Millis)

	if result.Status == checks.StatusPass {
		return
	}
	if result.Message != "" {
		fmt.Printf("           %s\n", result.Message)
	}
	for _, assertion := range result.Assertions {
		if assertion.Status == checks.StatusFail {
			fmt.Printf("           - %s: %s\n", assertion.Name, assertion.Detail)
		}
	}
}

func printSummary(report *runner.Report) {
	fmt.Printf("\n  %s · %s\n", report.Environment, report.Version)
	fmt.Printf("  %d passed, %d failed, %d skipped, %d errored in %.1fs\n\n",
		report.Passed, report.Failed, report.Skipped, report.Errored,
		float64(report.Millis)/1000)

	fmt.Println("  checklist coverage")
	for _, coverage := range report.Coverage {
		state := coverage.Status
		if !coverage.Coverable {
			state = "n/a here"
		}
		fmt.Printf("    %-24s %-10s %d checks\n", coverage.Name, state, coverage.Checks)
	}
	if report.SandboxPath != "" {
		fmt.Printf("\n  sandbox kept at %s\n", report.SandboxPath)
	}
	fmt.Println()
}

// --- preflight --------------------------------------------------------------

func commandPreflight(ctx context.Context, loaded *spec.Spec, args []string) error {
	flags := flag.NewFlagSet("preflight", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "write the report as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}

	scope := ""
	if flags.NArg() > 0 {
		scope = flags.Arg(0)
	}

	report := preflight.Run(ctx, loaded, scope)

	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}

	group := ""
	fmt.Println()
	for _, result := range report.Results {
		if result.GroupName != group {
			group = result.GroupName
			fmt.Printf("\n  %s\n", group)
		}
		symbol := "ok  "
		if result.Status != preflight.StatusPresent {
			symbol = "miss"
		}
		detail := result.Detail
		if result.Status != preflight.StatusPresent {
			detail = result.Why
		}
		fmt.Printf("    %s %-22s %s\n", symbol, result.Name, truncate(detail, 70))
	}
	fmt.Printf("\n  %d present, %d missing\n\n", report.Present, report.Missing)
	if report.Missing > 0 {
		fmt.Println("  Open the console for the install command of each missing item.")
		fmt.Println()
	}
	return nil
}

// --- list -------------------------------------------------------------------

func commandList(loaded *spec.Spec, args []string) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "write the plan as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(map[string]any{
			"environments": loaded.Environments,
			"categories":   loaded.Categories,
			"checks":       describeChecks(),
		})
	}

	fmt.Println("\n  environments")
	for _, environment := range loaded.Environments {
		gate := ""
		if environment.Gate != "" {
			gate = " (needs " + environment.Gate + "=1)"
		}
		fmt.Printf("    %-6s %-28s %-8s %s%s\n",
			environment.ID, environment.Name, environment.Cost, environment.DurationHint, gate)
	}

	fmt.Println("\n  checklist coverage by environment")
	fmt.Printf("    %-26s %5s %5s %5s %5s\n", "category", "local", "sim", "flex", "kind")
	for _, category := range loaded.Categories {
		counts := ""
		for _, environment := range []string{"local", "sim", "flex", "kind"} {
			count := checks.Categories(environment)[category.ID]
			cell := "-"
			if count > 0 {
				cell = fmt.Sprint(count)
			}
			counts += fmt.Sprintf(" %5s", cell)
		}
		fmt.Printf("    %-26s%s\n", category.Name, counts)
	}

	fmt.Printf("\n  %d checks in total\n\n", len(checks.All()))
	return nil
}

type checkDescription struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Category     string   `json:"category"`
	Environments []string `json:"environments"`
	Mutating     bool     `json:"mutating"`
	Slow         bool     `json:"slow"`
}

func describeChecks() []checkDescription {
	all := checks.All()
	out := make([]checkDescription, 0, len(all))
	for _, check := range all {
		out = append(out, checkDescription{
			ID: check.ID, Name: check.Name, Category: check.Category,
			Environments: check.Environments, Mutating: check.Mutating, Slow: check.Slow,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// --- simulator --------------------------------------------------------------

func commandSim(ctx context.Context, root string, args []string) error {
	flags := flag.NewFlagSet("sim", flag.ContinueOnError)
	address := flags.String("addr", "127.0.0.1:5000", "address to listen on (loopback only)")
	inventoryPath := flags.String("inventory", filepath.Join(root, "config", "flex-sim.yaml"), "tenant to serve")
	cloudsPath := flags.String("write-clouds", "", "also write a clouds.yaml pointing at the simulator")
	quiet := flags.Bool("quiet", false, "do not log every call")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return serveSimulator(ctx, *address, *inventoryPath, *cloudsPath, !*quiet)
}

// --- helpers ----------------------------------------------------------------

func split(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func truncate(value string, width int) string {
	value = strings.TrimSpace(value)
	if len(value) <= width {
		return value
	}
	return value[:width] + "..."
}

// wrapAt breaks text into indented lines no wider than width.
//
// For prose the console prints beside its step lists. truncate is the wrong
// tool there: a warning worth printing is worth printing in full, and cutting
// it at 68 characters loses the half that says what to do. Each line ends with
// a newline, so the result is appended rather than formatted around.
func wrapAt(text string, width int, indent string) string {
	var out strings.Builder
	line := indent
	for _, word := range strings.Fields(text) {
		if len(line) > len(indent) && len(line)+1+len(word) > width {
			out.WriteString(line + "\n")
			line = indent
		}
		if len(line) > len(indent) {
			line += " "
		}
		line += word
	}
	if len(line) > len(indent) {
		out.WriteString(line + "\n")
	}
	return out.String()
}

// credentialsFromEnvironment collects whatever the person already has exported,
// so the terminal runner needs no flags to use an existing cloud profile.
func credentialsFromEnvironment(loaded *spec.Spec, environment string) map[string]string {
	out := map[string]string{}
	for _, method := range loaded.CredentialMethodsFor(environment) {
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
