package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/checks"
	"github.com/opencenter-cloud/opencli-testbench/internal/gitopsupdate"
	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
	"github.com/opencenter-cloud/opencli-testbench/internal/sandbox"
	"github.com/opencenter-cloud/opencli-testbench/internal/workflow"
)

// Stage 11 without a browser.
//
// The console could already do all of this and the package it calls is tested.
// What was missing was a door for CI: GitHub Actions has nowhere to click, so
// action.yml called `bench gitops preview|update` and got "unknown command". The
// integration was a chain with this one link absent.
//
// So nothing here decides anything. Eligibility, the two gates, the eleven
// steps, the exit codes and the redaction all live in internal/gitopsupdate and
// stay there. This file's whole job is to turn a run on disk into a RunSummary
// and a process exit status — because the alternative is a second promotion gate
// written in a hurry for CI, and that one would be the one that drifts.

// benchVersion is what this bench records about itself in the evidence it
// publishes. Provenance that names the CLI under test but not the harness that
// tested it is half a trail.
//
// A var rather than a const so a release build can stamp it with
// -ldflags "-X main.benchVersion=…". A const cannot be stamped, and the linker
// does not complain when asked to stamp one — it just quietly does nothing.
var benchVersion = "0.1.0"

// commandGitOps handles `bench gitops <subcommand> [run-id] [--approve]`.
func commandGitOps(ctx context.Context, root string, args []string) error {
	if len(args) == 0 {
		fmt.Print(gitopsUsage)
		return exitWith(gitopsupdate.ExitBadConfig, "no subcommand given")
	}

	subcommand, rest := args[0], args[1:]

	// Hand-parsed rather than through flag, because the documented form puts the
	// run id first: `bench gitops update <run-id> --approve`. flag stops at the
	// first non-flag argument, so --approve would be silently ignored there —
	// and a flag that silently fails to grant approval on the one command that
	// writes to a remote is the wrong kind of surprise.
	runID, approved := "", false
	for _, argument := range rest {
		switch {
		case argument == "--approve", argument == "--approve-gitops":
			approved = true
		case argument == "--json":
			// Accepted and ignored: every subcommand here already prints JSON.
		case strings.HasPrefix(argument, "-"):
			return exitWith(gitopsupdate.ExitBadConfig,
				fmt.Sprintf("unknown option %q", argument))
		case runID != "":
			return exitWith(gitopsupdate.ExitBadConfig,
				fmt.Sprintf("more than one run id given: %q and %q", runID, argument))
		default:
			runID = argument
		}
	}

	// The environment is the only source here. No flag carries a GitOps value in
	// the headless path, because the one that matters is a token and a token on a
	// command line lands in shell history and in the process list.
	config := gitopsupdate.Load(nil, nil)

	switch subcommand {
	case "config":
		return gitopsShowConfig(config)
	case "preflight":
		return gitopsPreflight(root, runID, config)
	case "preview":
		return gitopsExecute(ctx, root, runID, config, gitopsupdate.ModePreview, false)
	case "update":
		return gitopsExecute(ctx, root, runID, config, gitopsupdate.ModeApproved, approved)
	case "status":
		return gitopsStatus(root, runID)
	default:
		fmt.Print(gitopsUsage)
		return exitWith(gitopsupdate.ExitBadConfig,
			fmt.Sprintf("unknown gitops subcommand %q", subcommand))
	}
}

const gitopsUsage = `bench gitops <subcommand> [run-id]

  config              print the resolved configuration; no secret is printed
  preflight [run-id]  whether a run may be promoted; touches no remote
  preview [run-id]    prepare the whole change locally; pushes nothing
  update [run-id] --approve
                      commit, push and open a pull request
  status [run-id]     the last recorded result for a run

  Without a run id the most recent run is used, which is what CI wants after
  running exactly one.

  update needs both gates: OPENCLI_ALLOW_GITOPS_UPDATE=1 in the environment and
  --approve on the command. One is intent and the other is consent, and a write
  to somebody's delivery repository needs both.

  Configuration comes from the environment: OPENCLI_GIT_REPOSITORY,
  _BASE_BRANCH, _MANIFEST_PATH, _EVIDENCE_PATH, _CONTAINER_NAME, _CREATE_PR and
  _TOKEN, plus OPENCLI_IMAGE_REPOSITORY and OPENCLI_IMAGE_TAG. The prefix is
  OPENCLI_GIT_, not OPENCLI_GITOPS_ — only the gate above and
  OPENCLI_GITOPS_ALLOW_WARNINGS use that longer form. Getting it wrong is silent:
  the variable is set, nothing reads it, and the stage quietly falls back to its
  default repository. Run "bench gitops config" to see what actually resolved.

  Exit codes: 0 ok · 2 configuration · 3 not eligible · 4 approval missing
              5 git failed · 6 pull request failed
`

// gitopsShowConfig prints what resolved, so a failing workflow can be diagnosed
// without guessing which variable reached the runner.
//
// Config.Redacted() is what makes this safe to print into a CI log, which on a
// public repository is a permanent public artifact.
func gitopsShowConfig(config gitopsupdate.Config) error {
	safe := config.Redacted()
	encoded, err := json.MarshalIndent(map[string]any{
		"configured":     config.Configured(),
		"repository":     gitopsupdate.StripCredentials(safe.Repository),
		"slug":           gitopsupdate.Slug(config.Repository),
		"base_branch":    safe.BaseBranch,
		"manifest_path":  safe.ManifestPath,
		"container_name": safe.ContainerName,
		"image_repo":     safe.ImageRepository,
		"image_tag":      safe.ImageTag,
		"create_pr":      safe.CreatePR,
		"allow_warnings": safe.AllowWarnings,
		"approved_paths": safe.ApprovedPaths,
		"gate":           gitopsupdate.Gate,
		"gate_set":       gitopsupdate.ReadGate(),
		// Read from the environment rather than from the config, because no
		// credential is a field on Config — by design, so a token cannot reach a
		// command line or a commit message by accident.
		//
		// Presence, never the value. A field that says whether a token exists is
		// useful; a field that says what it is is a leak with a tidy interface.
		"token_present":   strings.TrimSpace(os.Getenv(gitopsupdate.EnvToken)) != "",
		"ssh_key_present": strings.TrimSpace(os.Getenv(gitopsupdate.EnvSSHKey)) != "",
		"evidence_path":   safe.EvidencePath,
	}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))

	if !config.Configured() {
		return exitWith(gitopsupdate.ExitBadConfig,
			"no GitOps repository configured; set "+gitopsupdate.EnvRepository)
	}
	if err := config.Validate(); err != nil {
		return exitWith(gitopsupdate.ExitBadConfig, err.Error())
	}
	return nil
}

// gitopsPreflight answers "could this run be promoted" and changes nothing.
func gitopsPreflight(root, runID string, config gitopsupdate.Config) error {
	summary, err := gitopsSummary(root, runID)
	if err != nil {
		return exitWith(gitopsupdate.ExitBadConfig, err.Error())
	}

	view := map[string]any{
		"run":        gitopsSummaryView(summary),
		"repository": gitopsupdate.StripCredentials(config.Repository),
		"gate_set":   gitopsupdate.ReadGate(),
		"configured": config.Configured(),
	}

	// Not configured is not a failure. A bench nobody pointed at a delivery
	// repository has not failed; it has nothing to do, and CI should not go red
	// for it.
	if !config.Configured() {
		view["status"] = string(gitopsupdate.StatusNotConfigured)
		view["message"] = "no GitOps repository is configured"
		gitopsPrint(view)
		return nil
	}
	if err := config.Validate(); err != nil {
		view["status"] = string(gitopsupdate.StatusBlocked)
		view["reasons"] = []string{err.Error()}
		view["message"] = err.Error()
		gitopsPrint(view)
		return exitWith(gitopsupdate.ExitBadConfig, err.Error())
	}

	eligibility := gitopsupdate.Eligible(summary, config)
	view["eligible"] = eligibility.Eligible
	view["warned"] = eligibility.Warned
	view["reasons"] = eligibility.Reasons
	if eligibility.Eligible {
		view["status"] = string(gitopsupdate.StatusPassed)
		view["message"] = "this run is eligible for promotion"
		gitopsPrint(view)
		return nil
	}
	view["status"] = string(gitopsupdate.StatusBlocked)
	view["message"] = "this run may not be promoted: " + eligibility.Reasons[0]
	gitopsPrint(view)
	return exitWith(gitopsupdate.ExitNotEligible, eligibility.Reasons[0])
}

// gitopsExecute runs the eleven steps in either mode.
func gitopsExecute(
	ctx context.Context, root, runID string,
	config gitopsupdate.Config, mode gitopsupdate.Mode, approved bool,
) error {
	summary, err := gitopsSummary(root, runID)
	if err != nil {
		return exitWith(gitopsupdate.ExitBadConfig, err.Error())
	}

	approval := gitopsupdate.Approval{
		GateSet:  gitopsupdate.ReadGate(),
		Approved: approved,
	}

	// Checked here as well as inside the package, so refusing costs no clone and
	// the message names the gate that was shut rather than leaving somebody to
	// read eleven pending steps to find out.
	if mode == gitopsupdate.ModeApproved {
		if permitted, why := approval.Permits(); !permitted {
			fmt.Fprintf(os.Stderr, "\n  refused: %s\n", why)
			fmt.Fprintf(os.Stderr, "    %-32s %v\n", gitopsupdate.Gate+"=1", approval.GateSet)
			fmt.Fprintf(os.Stderr, "    %-32s %v\n", "--approve", approval.Approved)
			fmt.Fprintf(os.Stderr,
				"\n  Nothing was pushed. `bench gitops preview` shows the change instead.\n\n")
			return exitWith(gitopsupdate.ExitApprovalMissing, why)
		}
	}

	// Its own throwaway checkout, removed however this ends. A failed promotion
	// must not leave somebody else's repository in a temporary directory.
	box, err := sandbox.New("gitops")
	if err != nil {
		return err
	}
	defer func() { _ = box.Cleanup() }()

	// A deadline rather than none: an unreachable host would otherwise hold a CI
	// job for the whole job timeout and report nothing useful.
	deadline := 10 * time.Minute
	if mode == gitopsupdate.ModeApproved {
		deadline = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	// Sanitised, because that is the form the directory on disk is named with.
	// The unsanitised id would point at a path that does not exist for any run id
	// git or the filesystem would not take verbatim.
	artifacts := gitopsupdate.SanitiseSegment(summary.RunID)
	if artifacts == "" {
		artifacts = "no-run"
	}
	runRoot := filepath.Join(root, "artifacts", "runs", artifacts)

	// One redactor for the whole operation, so the tokens it learns while cloning
	// and calling the API are still known when the result is written to disk.
	redactor := redact.New()

	result := gitopsupdate.Run(ctx, gitopsupdate.Request{
		Config:      config,
		Run:         summary,
		Approval:    approval,
		Mode:        mode,
		SandboxRoot: box.Root,
		BenchRoot:   root,
		ReportsDir:  filepath.Join(runRoot, "reports"),
		EvidenceDir: filepath.Join(runRoot, "evidence"),
		Redactor:    redactor,
		Canaries:    checks.Canaries(),
	})

	gitopsReport(result)
	gitopsOutputs(result, summary)

	// Recorded against the run, so the outcome outlives this process. The step
	// outputs reach only the workflow that ran it, and the log is prose; run.json
	// travels with the uploaded evidence and is what `bench gitops status` reads.
	//
	// A failure to record is reported and does not change the verdict. The
	// promotion either happened or it did not, and losing the note about it is
	// not grounds for restating what it was.
	if summary.RunID != "" {
		runFile := filepath.Join(runRoot, "run.json")
		if err := workflow.RecordGitOps(runFile, result, redactor); err != nil {
			fmt.Fprintf(os.Stderr, "could not record the result against the run: %v\n", err)
		}
	}

	if code := result.ExitCode(); code != gitopsupdate.ExitOK {
		return exitWith(code, result.Message)
	}
	return nil
}

// gitopsStatus prints the result recorded against a run.
func gitopsStatus(root, runID string) error {
	summary, err := gitopsSummary(root, runID)
	if err != nil {
		return exitWith(gitopsupdate.ExitBadConfig, err.Error())
	}

	// Read from the run's own report rather than from memory: a headless
	// invocation shares no process with the one that did the work, so the file on
	// disk is the only thing that can answer.
	path := filepath.Join(root, "artifacts", "runs",
		gitopsupdate.SanitiseSegment(summary.RunID), "run.json")
	run, err := workflow.LoadRun(path)
	if err != nil {
		return exitWith(gitopsupdate.ExitBadConfig,
			fmt.Sprintf("no such run %q", summary.RunID))
	}
	if run.GitOps == nil {
		fmt.Printf("no GitOps result recorded for run %s\n", summary.RunID)
		return nil
	}
	gitopsPrint(run.GitOps)
	return nil
}

// --- turning a run on disk into what the gate reads ---------------------------

// gitopsSummary loads a run and maps it onto the eligibility gate's input.
//
// The mapping is the interesting part, and one line of it deserves saying out
// loud: blocked and not-run count as unanswered, skipped does not. A module
// blocked for a missing prerequisite, or never reached because the run stopped,
// is a required question with no answer — promoting over it would publish
// evidence that claims more than was tested. A skipped module was skipped on
// purpose, by --quick or by the live gate being shut, and treating a deliberate
// decision as a fault would make every ordinary CI run ineligible and teach
// people to pass --allow-warnings to make the noise stop.
func gitopsSummary(root, runID string) (gitopsupdate.RunSummary, error) {
	if runID == "" {
		latest, err := gitopsLatestRun(root)
		if err != nil {
			return gitopsupdate.RunSummary{}, err
		}
		runID = latest
	}
	runID = gitopsupdate.SanitiseSegment(runID)

	path := filepath.Join(root, "artifacts", "runs", runID, "run.json")
	run, err := workflow.LoadRun(path)
	if err != nil {
		return gitopsupdate.RunSummary{}, fmt.Errorf(
			"no such run %q: %s is not readable", runID, path)
	}

	summary := gitopsupdate.RunSummary{
		RunID:            run.ID,
		Completed:        !run.Finished.IsZero() && run.State != workflow.RunState(workflow.StateRunning),
		Cancelled:        run.State == workflow.RunState(workflow.StateCancelled),
		Passed:           run.Passed,
		Warnings:         run.Warning,
		Failed:           run.Failed,
		Blocked:          run.Blocked + run.NotRun,
		CleanupState:     gitopsupdate.CleanupUnknown,
		SourceRepository: run.Source,
		SourceCommit:     run.SourceCommit,
		CLIVersion:       run.Version,
		BenchVersion:     benchVersion,
		Environment:      gitopsEnvironment(run),
	}

	// A run built from a branch other than main is worth carrying in the version
	// string rather than promoting as though it came from main.
	if branch := strings.TrimSpace(run.SourceBranch); branch != "" && branch != "main" {
		summary.CLIVersion += " (" + branch + ")"
	}
	// An uncommitted working tree is not the commit it claims to be.
	if strings.TrimSpace(run.SourceDirty) != "" {
		summary.CLIVersion += " (dirty)"
	}

	// The report has to exist on disk, because the evidence points at it. The
	// gate stats whatever path it is given, so it is made absolute here.
	for _, candidate := range []string{
		filepath.Join(run.Root, "reports", "report.json"),
		filepath.Join(root, "artifacts", "runs", runID, "reports", "report.json"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			summary.ReportPath = candidate
			break
		}
	}

	// Cleanup is read from the module that ran it, never assumed. Its own
	// verification step is the only thing that can say the run left nothing
	// behind, and "no cleanup module" stays unknown rather than becoming passed.
	for _, module := range run.Modules {
		if module.ID != "cleanup" {
			continue
		}
		switch module.State {
		case workflow.StatePassed:
			summary.CleanupState = gitopsupdate.CleanupPassed
		case workflow.StateFailed, workflow.StateWarning, workflow.StateBlocked:
			summary.CleanupState = gitopsupdate.CleanupFailed
		case workflow.StateSkipped, workflow.StateLocked, workflow.StateNotStarted:
			summary.CleanupState = gitopsupdate.CleanupSkipped
		}
		break
	}

	// A recorded leak blocks promotion outright. The bench plants canaries so
	// this is detectable, and a run that leaked one is the worst possible moment
	// to publish evidence from it.
	for _, module := range run.Modules {
		for _, result := range module.Results {
			if result.Status == checks.StatusPass {
				continue
			}
			if result.Category == "security" &&
				strings.Contains(strings.ToLower(result.Message), "leak") {
				summary.SecretLeak = true
			}
		}
	}
	return summary, nil
}

// gitopsLatestRun finds the most recent run, so CI need not thread an id
// through.
func gitopsLatestRun(root string) (string, error) {
	runs, err := workflow.FindRuns(filepath.Join(root, "artifacts"))
	if err != nil || len(runs) == 0 {
		return "", fmt.Errorf("no runs found; `bench run full` first")
	}
	newest := runs[0]
	for _, run := range runs {
		if run.Started.After(newest.Started) {
			newest = run
		}
	}
	return newest.ID, nil
}

// gitopsEnvironment names what the run executed against, for the evidence.
func gitopsEnvironment(run *workflow.Run) string {
	if !run.Live {
		return "local"
	}
	if len(run.LiveProviders) > 0 {
		return strings.Join(run.LiveProviders, "+")
	}
	return "live"
}

func gitopsSummaryView(summary gitopsupdate.RunSummary) map[string]any {
	return map[string]any{
		"id": summary.RunID, "completed": summary.Completed,
		"cancelled": summary.Cancelled, "passed": summary.Passed,
		"warnings": summary.Warnings, "failed": summary.Failed,
		"blocked": summary.Blocked, "cleanup": summary.CleanupState,
		"report": summary.ReportPath, "cli_version": summary.CLIVersion,
		"source_commit": summary.SourceCommit, "environment": summary.Environment,
	}
}

// --- output -------------------------------------------------------------------

func gitopsPrint(value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not encode the result:", err)
		return
	}
	fmt.Println(string(encoded))
}

// gitopsReport prints the eleven steps for a person reading a CI log, then the
// result as JSON for whatever is parsing it. Both, because a workflow log is
// read by people when it goes wrong and by machines when it goes right.
func gitopsReport(result *gitopsupdate.Result) {
	fmt.Printf("\n  %s\n\n", result.Headline())
	for _, step := range result.Steps {
		symbol := map[gitopsupdate.StepStatus]string{
			gitopsupdate.StepOK:      "ok     ",
			gitopsupdate.StepFailed:  "FAILED ",
			gitopsupdate.StepBlocked: "BLOCKED",
			gitopsupdate.StepSkipped: "skipped",
			gitopsupdate.StepPending: "-      ",
		}[step.Status]
		fmt.Printf("    %s %-14s %s\n", symbol, step.ID, truncate(step.Detail, 60))
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
	fmt.Println()
	gitopsPrint(result)
}

// gitopsOutputs writes the step outputs action.yml reads.
//
// Written here rather than parsed out of the log by the action, because a
// workflow that greps its own output for a pull request URL breaks the first
// time a message is reworded.
func gitopsOutputs(result *gitopsupdate.Result, summary gitopsupdate.RunSummary) {
	path := strings.TrimSpace(os.Getenv("GITHUB_OUTPUT"))
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not write GITHUB_OUTPUT:", err)
		return
	}
	defer func() { _ = file.Close() }()

	for key, value := range map[string]string{
		"gitops_status":    string(result.Status),
		"gitops_eligible":  fmt.Sprint(result.Eligible),
		"gitops_changed":   fmt.Sprint(result.Changed),
		"pull_request_url": result.PullRequest,
		"update_branch":    result.Branch,
		"commit_sha":       result.CommitSHA,
		"image_reference":  result.ImageReference,
		"gitops_run_id":    summary.RunID,
		"gitops_evidence":  result.EvidencePath,
		"gitops_patch":     result.PatchPath,
	} {
		// Newlines are impossible in these values, but the delimiter form is
		// used anyway: a value that ever grows a newline would otherwise inject
		// arbitrary outputs into the workflow.
		fmt.Fprintf(file, "%s<<__BENCH_EOF__\n%s\n__BENCH_EOF__\n", key, value)
	}
}
