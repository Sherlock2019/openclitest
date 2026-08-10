package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/actionsetup"
	"github.com/opencenter-cloud/opencli-testbench/internal/gitopsupdate"
	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
	"github.com/opencenter-cloud/opencli-testbench/internal/sandbox"
)

// Wiring somebody else's repository to this bench.
//
// `bench gitops` promotes a tested build; this promotes the CI that produces
// one. The shape is the same and so is most of the machinery — see
// internal/actionsetup, which reuses the clone, credential handling, redaction,
// branch, commit, push and pull request from internal/gitopsupdate rather than
// writing a second copy of any of it.
//
// Nothing here decides anything. This file turns environment variables into a
// Request and a Result into an exit status.

// commandActions handles `bench actions <subcommand> [--approve]`.
func commandActions(ctx context.Context, args []string) error {
	subcommand := "workflow"
	rest := []string(nil)
	if len(args) > 0 {
		subcommand, rest = args[0], args[1:]
	}

	approved := false
	for _, argument := range rest {
		switch {
		case argument == "--approve":
			approved = true
		case argument == "--json":
			// Accepted and ignored: every subcommand here already prints JSON.
		case strings.HasPrefix(argument, "--kind="):
			// Which of the two workflows. Set into the environment rather than
			// threaded through six call sites, because that is how every other
			// choice on this command already travels.
			kind, err := actionsetup.ParseKind(strings.TrimPrefix(argument, "--kind="))
			if err != nil {
				return exitWith(actionsetup.ExitBadConfig, err.Error())
			}
			_ = os.Setenv(actionsetup.EnvKind, string(kind))
		default:
			if strings.HasPrefix(argument, "-") {
				return exitWith(actionsetup.ExitBadConfig,
					fmt.Sprintf("unknown option %q", argument))
			}
		}
	}

	switch subcommand {
	case "workflow":
		return actionsWorkflow()
	case "config":
		return actionsConfig()
	case "preview":
		return actionsRun(ctx, actionsetup.ModePreview, false)
	case "install":
		return actionsRun(ctx, actionsetup.ModeApproved, approved)
	case "runs", "results":
		return actionsResults(ctx, rest)
	case "trigger":
		return actionsTrigger(ctx, approved)
	default:
		fmt.Print(actionsUsage)
		return exitWith(actionsetup.ExitBadConfig,
			fmt.Sprintf("unknown actions subcommand %q", subcommand))
	}
}

const actionsUsage = `bench actions <subcommand> [--kind=test-bench|opencenter-e2e]

  --kind selects which workflow. test-bench (the default) runs every CLI
  command; opencenter-e2e runs the twenty-one-phase cluster lifecycle. They
  are two named files and nothing else is writable:

    .github/workflows/test-bench.yml
    .github/workflows/opencenter-e2e.yml

  workflow   print the workflow file; writes nothing, needs no credential
  config     print what resolved; no secret is printed
  preview    clone the target repository and show what would change
  install --approve
             commit the workflow on a branch and open a pull request

  install needs both gates: OPENCLI_ALLOW_ACTIONS_SETUP=1 in the environment and
  --approve on the command. One is intent and the other is consent.

  Configuration:
    OPENCLI_ACTIONS_REPOSITORY  the repository to wire up (owner/name, URL or ssh)
    OPENCLI_ACTIONS_TOKEN       a token that may write .github/workflows.
                                Falls back to OPENCLI_GIT_TOKEN.
    OPENCLI_ACTION_REF          which published action to call
    OPENCLI_GIT_REPOSITORY      optional: turn on GitOps promotion
    OPENCLI_GIT_MANIFEST_PATH   optional: the file promotion updates

  A token with contents:write is NOT enough. Writing under .github/workflows
  needs the ` + "`workflow`" + ` scope on a classic token, or Workflows: write on a
  fine-grained one. GITHUB_TOKEN inside Actions can never do it.

  Exit codes: 0 ok · 2 configuration · 4 approval missing · 5 git · 6 pull request
`

// splitList turns "a, b ,c" into three entries, dropping the empties.
//
// Its own function because an empty variable must produce no entries rather
// than one empty one: a lone "" reaching the workflow would render a skip_phases
// line saying nothing was skipped, which reads as a shortened run.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// actionsOptions builds the render options from the environment.
func actionsOptions() actionsetup.Options {
	// An unreadable kind is not defaulted away — see ParseKind. The error is
	// surfaced by every caller that uses the options, so a typo cannot quietly
	// install the other workflow.
	kind, err := actionsetup.ParseKind(os.Getenv(actionsetup.EnvKind))
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %s\n", err)
		os.Exit(actionsetup.ExitBadConfig)
	}

	timeout, _ := strconv.Atoi(strings.TrimSpace(os.Getenv(actionsetup.EnvE2ETimeout)))

	return actionsetup.Options{
		Kind: kind,
		E2E: actionsetup.E2EOptions{
			CLIRepo:          strings.TrimSpace(os.Getenv(actionsetup.EnvE2ECLIRepo)),
			Nightly:          strings.TrimSpace(os.Getenv(actionsetup.EnvE2ENightly)),
			TimeoutMinutes:   timeout,
			RealEnvironment:  strings.TrimSpace(os.Getenv(actionsetup.EnvE2EEnvironment)),
			DestroyAfterTest: yes(os.Getenv(actionsetup.EnvE2EDestroy)),
			SkipPhases:       splitList(os.Getenv(actionsetup.EnvE2ESkipPhases)),
		},
		Action: strings.TrimSpace(os.Getenv(actionsetup.EnvAction)),
		// The workflow names the repository only when the operator gave one AND
		// it is not simply the repository the file lands in. Left empty, the
		// action defaults to whoever calls it, which survives a fork.
		TargetRepository: "",
		GitOpsRepository: strings.TrimSpace(os.Getenv(gitopsupdate.EnvRepository)),
		ManifestPath:     strings.TrimSpace(os.Getenv(gitopsupdate.EnvManifestPath)),
		Replace:          yes(os.Getenv(actionsetup.EnvReplace)),
		// The console's own selection, so the file it writes runs what is on
		// screen rather than whatever the action defaults to.
		EnvironmentMode: strings.TrimSpace(os.Getenv(actionsetup.EnvMode)),
		Provider:        strings.TrimSpace(os.Getenv(actionsetup.EnvProvider)),
	}
}

// yes reads a checkbox-shaped value.
func yes(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// actionsConfigFor builds the target-repository configuration.
//
// The token is copied into the variable the reused machinery reads. Both names
// are accepted because an operator who already set a GitOps token should not be
// asked for a second one — while the help makes clear that the GitOps token
// usually lacks the scope this needs.
func actionsConfigFor() gitopsupdate.Config {
	if token := strings.TrimSpace(os.Getenv(actionsetup.EnvToken)); token != "" {
		_ = os.Setenv(gitopsupdate.EnvToken, token)
	}
	// The same for the key. repository.go reads OPENCLI_GIT_SSH_KEY when it
	// builds the git environment, writes it to a file inside the sandbox and
	// points GIT_SSH_COMMAND at it — the key never reaches a command line.
	if key := strings.TrimSpace(os.Getenv(actionsetup.EnvSSHKey)); key != "" {
		_ = os.Setenv(gitopsupdate.EnvSSHKey, key)
	}
	config := gitopsupdate.Load(nil, nil)
	config.Repository = strings.TrimSpace(os.Getenv(actionsetup.EnvRepository))
	// CreatePR is left as Load resolved it — it defaults to true, and forcing
	// it here overrode an operator who had switched it off, which is how a
	// deliberate "push the branch, I will open the request myself" turned into
	// a failed step against a remote that has no API at all.
	return config
}

func actionsWorkflow() error {
	fmt.Print(string(actionsetup.Workflow(actionsOptions())))
	return nil
}

func actionsConfig() error {
	config := actionsConfigFor()
	options := actionsOptions()
	action := options.Action
	if action == "" {
		action = actionsetup.DefaultAction
	}
	encoded, err := json.MarshalIndent(map[string]any{
		"target_repository": gitopsupdate.StripCredentials(config.Repository),
		"target_slug":       gitopsupdate.Slug(config.Repository),
		"base_branch":       config.BaseBranch,
		"action":            action,
		// The kind's, not the constant. Printing the command bench's path while
		// --kind=opencenter-e2e was asked for is how somebody checks the
		// configuration, sees the wrong file named, and concludes the flag did
		// nothing.
		"workflow":          options.Kind.String(),
		"workflow_path":     options.Kind.Path(),
		"branch":            options.Kind.Branch(),
		"promoting":         options.GitOpsRepository != "",
		"gitops_repository": gitopsupdate.StripCredentials(options.GitOpsRepository),
		"gate":              actionsetup.Gate,
		"gate_set":          actionsetup.ReadGate(),
		// Presence, never the value.
		"token_present": strings.TrimSpace(os.Getenv(gitopsupdate.EnvToken)) != "",
	}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))

	if strings.TrimSpace(config.Repository) == "" {
		return exitWith(actionsetup.ExitBadConfig,
			"no repository to wire up; set "+actionsetup.EnvRepository)
	}
	return nil
}

// actionsRun performs the preview or the install.
func actionsRun(ctx context.Context, mode actionsetup.Mode, approved bool) error {
	config := actionsConfigFor()

	approval := actionsetup.Approval{
		GateSet:  actionsetup.ReadGate(),
		Approved: approved,
	}

	// Checked here as well as inside the package, so refusing costs no clone
	// and the message names the gate that was shut.
	if mode == actionsetup.ModeApproved {
		if permitted, why := approval.Permits(); !permitted {
			fmt.Fprintf(os.Stderr, "\n  refused: %s\n", why)
			fmt.Fprintf(os.Stderr, "    %-34s %v\n", actionsetup.Gate+"=1", approval.GateSet)
			fmt.Fprintf(os.Stderr, "    %-34s %v\n", "--approve", approval.Approved)
			fmt.Fprintf(os.Stderr,
				"\n  Nothing was written. `bench actions preview` shows the change instead.\n\n")
			return exitWith(actionsetup.ExitApprovalMissing, why)
		}
	}

	box, err := sandbox.New("actions")
	if err != nil {
		return err
	}
	defer func() { _ = box.Cleanup() }()

	deadline := 5 * time.Minute
	if mode == actionsetup.ModeApproved {
		deadline = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	redactor := redact.New()
	result := actionsetup.Install(ctx, actionsetup.Request{
		Config:      config,
		Options:     actionsOptions(),
		Approval:    approval,
		Mode:        mode,
		SandboxRoot: box.Root,
		Redactor:    redactor,
	})

	actionsReport(result)
	if code := result.ExitCode(); code != actionsetup.ExitOK {
		return exitWith(code, result.Message)
	}
	return nil
}

// actionsTrigger pushes an empty commit so the repository's pipeline runs.
//
// The end-to-end proof: install puts the workflow there, this makes it fire.
func actionsTrigger(ctx context.Context, approved bool) error {
	config := actionsConfigFor()
	approval := actionsetup.Approval{
		GateSet:  actionsetup.ReadGate(),
		Approved: approved,
	}

	if permitted, why := approval.Permits(); !permitted {
		fmt.Fprintf(os.Stderr, "\n  refused: %s\n", why)
		fmt.Fprintf(os.Stderr, "    %-34s %v\n", actionsetup.Gate+"=1", approval.GateSet)
		fmt.Fprintf(os.Stderr, "    %-34s %v\n", "approval", approval.Approved)
		fmt.Fprintf(os.Stderr,
			"\n  Nothing was pushed. A commit is a commit even when it is empty.\n\n")
		return exitWith(actionsetup.ExitApprovalMissing, why)
	}

	box, err := sandbox.New("trigger")
	if err != nil {
		return err
	}
	defer func() { _ = box.Cleanup() }()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	result := actionsetup.Trigger(ctx, actionsetup.Request{
		Config:      config,
		Options:     actionsOptions(),
		Approval:    approval,
		Mode:        actionsetup.ModeApproved,
		SandboxRoot: box.Root,
		Redactor:    redact.New(),
	})

	fmt.Printf("\n  %s\n\n", result.Headline())
	for _, step := range result.Steps {
		symbol := map[actionsetup.StepStatus]string{
			actionsetup.StepOK:      "ok     ",
			actionsetup.StepFailed:  "FAILED ",
			actionsetup.StepSkipped: "skipped",
			actionsetup.StepPending: "-      ",
		}[step.Status]
		fmt.Printf("    %s %-12s %s\n", symbol, step.ID, truncate(step.Detail, 62))
	}
	// Before the result, not after it. What this warns about has already
	// happened by the time the result is read, and the point of saying it is
	// that the next press is an informed one.
	if result.Warning != "" {
		fmt.Printf("\n  heads up\n%s", wrapAt(result.Warning, 68, "    "))
	}
	fmt.Printf("\n  %s\n", result.Message)
	if result.ActionsURL != "" {
		fmt.Printf("\n  %s\n", result.ActionsURL)
	}
	fmt.Println()

	encoded, err := json.MarshalIndent(result, "", "  ")
	if err == nil {
		fmt.Println(string(encoded))
	}
	if code := result.ExitCode(); code != actionsetup.ExitOK {
		return exitWith(code, result.Message)
	}
	return nil
}

// actionsResults shows what GitHub found, not what this machine found.
//
// The console can already explain a local run. This answers the question it
// could not: the run CI did, against the commit that was actually pushed, and
// which command failed in it.
func actionsResults(ctx context.Context, args []string) error {
	config := actionsConfigFor()
	if strings.TrimSpace(config.Repository) == "" {
		return exitWith(actionsetup.ExitBadConfig,
			"no repository set; fill in the repository above or set "+
				actionsetup.EnvRepository)
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	redactor := redact.New()

	// Scoped to whichever workflow was asked for. Listing both together would
	// report one's green run as the other's, which is a dashboard lying about
	// which thing passed.
	runs, err := actionsetup.ListRunsOf(ctx, config, redactor, 8, actionsOptions().Kind)
	if err != nil {
		return exitWith(actionsetup.ExitBadConfig, err.Error())
	}
	if len(runs) == 0 {
		fmt.Printf("\n  No runs of %s in %s yet.\n\n",
			actionsetup.WorkflowFile, gitopsupdate.StripCredentials(config.Repository))
		return nil
	}

	fmt.Printf("\n  %s · %s\n\n",
		gitopsupdate.StripCredentials(config.Repository), actionsetup.WorkflowFile)
	fmt.Printf("  %-4s %-10s %-9s %-8s %-16s %s\n",
		"run", "result", "commit", "took", "when", "title")
	for _, run := range runs {
		mark := map[string]string{
			"success": "ok", "failure": "FAILED", "cancelled": "cancelled",
			"in_progress": "running", "queued": "queued",
		}[run.Outcome()]
		if mark == "" {
			mark = run.Outcome()
		}
		fmt.Printf("  #%-3d %-10s %-9s %-8s %-16s %s\n",
			run.Number, mark, run.Commit,
			fmt.Sprintf("%dm%02ds", run.Seconds/60, run.Seconds%60),
			run.Started.Local().Format("Jan 2 15:04"), truncate(run.Title, 40))
	}

	// The newest run that actually finished badly is the one somebody is here
	// about. Picked automatically so the common case needs no run id typed.
	target := int64(0)
	for _, run := range runs {
		if run.Outcome() == "failure" {
			target = run.ID
			break
		}
	}
	for _, argument := range args {
		if parsed, convErr := strconv.ParseInt(argument, 10, 64); convErr == nil {
			target = parsed
		}
	}
	if target == 0 {
		fmt.Printf("\n  Nothing has failed. Pass a run id to inspect one anyway.\n\n")
		return nil
	}

	summary, failures, err := actionsetup.Failures(ctx, config, redactor, target)
	if err != nil {
		fmt.Printf("\n  Could not read the evidence for run %d: %s\n\n", target, err)
		return nil
	}

	fmt.Printf("\n  ── what failed in run %d ─────────────────────────────────\n\n", target)
	fmt.Printf("  %d passed · %d failed · %d warnings · %d blocked · %d skipped\n\n",
		summary.Passed, summary.Failed, summary.Warnings,
		summary.Blocked, summary.Skipped)

	if len(failures) == 0 {
		fmt.Printf("  The report lists no failing command. The job may have failed\n")
		fmt.Printf("  before the bench ran — check the run log.\n\n")
		return nil
	}
	for _, failure := range failures {
		fmt.Printf("  %s  (module %s)\n", failure.Check, failure.Module)
		if failure.Name != "" {
			fmt.Printf("    %s\n", failure.Name)
		}
		for _, assertion := range failure.Assertions {
			fmt.Printf("    ✗ %s\n", assertion)
		}
		fmt.Println()
	}
	return nil
}

// actionsReport prints the steps for a person, then the result as JSON for
// whatever is parsing it.
func actionsReport(result *actionsetup.Result) {
	fmt.Printf("\n  %s\n\n", result.Headline())
	for _, step := range result.Steps {
		symbol := map[actionsetup.StepStatus]string{
			actionsetup.StepOK:      "ok     ",
			actionsetup.StepFailed:  "FAILED ",
			actionsetup.StepSkipped: "skipped",
			actionsetup.StepPending: "-      ",
		}[step.Status]
		fmt.Printf("    %s %-14s %s\n", symbol, step.ID, truncate(step.Detail, 62))
	}
	if len(result.Reasons) > 0 {
		fmt.Println("\n  why not:")
		for _, reason := range result.Reasons {
			fmt.Printf("    - %s\n", reason)
		}
	}
	if result.PullRequest != "" {
		fmt.Printf("\n  pull request: %s\n", result.PullRequest)
	}
	fmt.Printf("\n  %s\n\n", result.Message)

	encoded, err := json.MarshalIndent(result, "", "  ")
	if err == nil {
		fmt.Println(string(encoded))
	}
	actionsOutputs(result)
}

// actionsOutputs writes the step outputs a workflow can read.
func actionsOutputs(result *actionsetup.Result) {
	path := strings.TrimSpace(os.Getenv("GITHUB_OUTPUT"))
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	for key, value := range map[string]string{
		"actions_status":   string(result.Status),
		"actions_changed":  fmt.Sprint(result.Changed),
		"pull_request_url": result.PullRequest,
		"setup_branch":     result.Branch,
	} {
		fmt.Fprintf(file, "%s<<__BENCH_EOF__\n%s\n__BENCH_EOF__\n", key, value)
	}
}
