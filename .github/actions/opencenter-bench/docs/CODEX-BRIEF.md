# Codex brief — grow this project into the openCenter E2E cluster lifecycle bench

This is an implementation instruction, not a discussion document. Work **inside this
repository** (`~/opencenter/fullopen`). Follow it in order.

## Status — steps 1 to 3 are done

| Step | State |
|---|---|
| 1. `internal/e2e` folded in, imports rewritten, build and tests green | **done** |
| 2. `.mise.toml` added and reconciled with `scripts/` | **done** |
| 3. E2E CLI command, lifecycle proven headless | **done** |
| 4. Generalise `internal/actionsetup` to two workflow kinds | not started |
| 5. Wire the lifecycle into the existing console | not started |
| 6. Workflow selector in the Actions panel | not started |
| 7. Documentation | partial — the three `docs/testing/*.md` are copied in but unverified |

Verified by running, not by reading:

- `go build ./...` and `go test ./...` are green, including every pre-existing package.
  Nothing regressed.
- `mise run build` produces `bin/testlab`, `bin/bench`, `bin/opencenter-e2e`.
- `mise run e2e-safe` runs all twenty-one phases: builds the real CLI from
  `~/opencenter/openCenter-cli-testDzoan`, generates 120 files, validates, destroys,
  proves cleanup, and writes `report.html`, `report.md`, `report.json`, `summary.csv`
  and `junit/e2e.xml`. Verdict WARNING, in 8.7s.

**Known finding, pre-existing, not introduced here.** `--simulate` on an emulated profile
does not reach SIMULATED — it reaches FAIL. `validate-config` runs for real (correctly:
simulation replaces only deploy, infrastructure, kubernetes-health, platform-health, smoke
and failure-tests), it rejects the generated configuration, everything downstream is
blocked, the real `destroy` then fails against a provider that was never contacted, and
`verify-cleanup` reports two survivors. The gate is behaving correctly — a simulated run
with real failures is a FAIL, and `Gate()` checks `Simulated` first so it can never be a
PASS either way. What is wrong is the documented claim that `--simulate` "reports SIMULATED
and exits 4": that holds only when no real phase fails. Decide which to change — the
simulation's coverage, or the sentence — and change it deliberately. Do not paper over it
by simulating `destroy`, which was tried here and is worse: it stops real resources
registered by real phases from being cleaned at all.

## 0. The three trees

| Path | Module | Role |
|---|---|---|
| `~/opencenter/fullopen` | `github.com/opencenter-cloud/opencli-testbench` | **This one. The thing you are building.** A byte-identical working copy of the finished openCenter CLI test bench, on `main`, clean, remote `Sherlock2019/opencenterclitest-Simple`. |
| `~/opencenter/openclitestsimple` | same module | The origin of that copy. **Read-only. Do not edit it.** |
| `~/opencenter/fulltestbench` | `github.com/opencenter-cloud/opencenter-e2e-testbench` | A separate prototype holding the 21-phase E2E lifecycle engine. **Harvest from it.** Do not develop it further. |

The point of the copy is that `fullopen` already *is* a working test bench. You are not
porting a UI, not building a GitHub Actions integration, and not starting a project. You
are adding one capability — cluster deployment and lifecycle E2E — to a product that
already has the console, the CI wiring, the redaction and the reporting.

**One consequence matters more than any other:** `fullopen` is a single Go module, so the
E2E engine can import `internal/redact`, `internal/sandbox`, `internal/report`,
`internal/gitopsupdate`, `internal/actionsetup` and the rest **directly**. The prototype
could not — Go forbids importing another module's `internal/`, which is why it carries a
duplicate `pkg/redact` shim and its own cut-down Actions code. Folding it in here deletes
that whole class of problem. Do not recreate it.

## 1. The requirement being satisfied

> As an openCenter platform engineer, I want a documented end-to-end test design that can
> be executed from GitHub Actions and manually from a command line, so the same cluster
> deployment and validation workflow serves automated release validation, CI testing,
> development and troubleshooting.

In a clean environment, the workflow must:

1. Build openCenter-cli using **mise**.
2. Generate and validate a cluster configuration.
3. Deploy an openCenter Kubernetes cluster.
4. Validate Kubernetes and openCenter platform health.
5. Run functional smoke tests.
6. Test failure, retry and diagnostic behaviour.
7. Remove the test environment.
8. Capture evidence suitable for release validation and troubleshooting.

Two execution modes, producing **identical results and evidence**:

**GitHub Actions** — started automatically or by `workflow_dispatch`; on an approved
GitHub-hosted or self-hosted runner; using GitHub Actions secrets or an approved external
secret manager; publishing test results, logs and sanitised diagnostic artifacts.

**Manual command line** — started by an engineer from a workstation, bastion or approved
admin environment; using documented mise tasks and CLI commands; producing the same
validation results and evidence as CI; supporting targeted troubleshooting and rerunning
individual test phases.

Existing documentation is a reference only. **The current code, CLI commands,
configuration schemas, mise tasks and actual platform behaviour are the source of truth.**
Where the docs and the binary disagree, read the binary and fix the docs.

## 2. What `fullopen` already has — do not rebuild any of it

- **The console.** `cmd/testlab/ui.html` (~4,100 lines): the VS Code Dark/Light token
  pairs, the sticky rail + main shell, the listing header, the `.tsum` verdict board, the
  `.tri-*` triage tables, the theme toggle. Served by `cmd/testlab/main.go`.
- **The GitHub Actions stage.** It is *not* missing here. `internal/actionsetup`
  (`Workflow`, `Install`, `Trigger`, `ListRuns`, `Failures`, the `OPENCLI_ALLOW_ACTIONS_SETUP`
  gate, the two-gate `Approval`), `internal/gitopsupdate` (the eleven promotion steps,
  `Config`, `Repo`, the GitHub client), `cmd/testlab/actionsboard.go` (`/api/actions-board`),
  `cmd/bench/actions.go` / `gitops.go` / `workflow_api.go`, and the panel's copy in
  `config/github-actions-gitops.yaml`.
- **Redaction.** `internal/redact` — one implementation, secrets matched on the way in.
- **Reporting.** `internal/report`, plus per-run artifacts under `artifacts/`.
- **Credentials, environments, prerequisites, emulation, kind.** `config/credentials.yaml`,
  `environments.yaml`, `prerequisites.yaml`, `emulation.yaml`, `cmd/testlab/kind.go`.
- **Packaging.** `action.yml`, `Dockerfile`, `start.sh`, `.github/workflows/opencenter-test-bench.yml`.
- **The stage system.** Seven lifecycle stages read out of the CLI's own command tree, plus
  extra stages declared in `config/experimental-stages.yaml` (`kafka` is the worked
  example: `id`, `name`, `experimental`, `after`, `colour`, `on_colour`, `summary`,
  `commands[]`).

Two real gaps:

**(a) No cluster lifecycle.** This bench answers "does this command work" — one
invocation, judged, next. It cannot answer "can this build stand a cluster up, prove it
healthy, and take it down again without leaving anything behind". That is not a longer
list of commands: it is a sequence where each step depends on the last, where failure
halfway leaves real infrastructure running, and where the interesting failures happen in
the gaps between commands.

**(b) No mise.** The brief requires building openCenter-cli with mise and documented mise
tasks, and there is no `.mise.toml` in this repository at all — `scripts/` and `start.sh`
do the building.

## 3. What to harvest from `~/opencenter/fulltestbench`

It already works: it builds, its tests pass, and its console runs. Take it, do not rewrite
it.

| Bring over | To | State |
|---|---|---|
| `internal/e2e/` — 9 sources + 2 test files | `internal/e2e/` | **landed.** The 21 phases, 8 profiles, the resumable engine, the release gate, the five report formats, and the 9-stage rail grouping with its tests. |
| `cmd/opencenter-test-bench/main.go` | `cmd/opencenter-e2e/main.go` | **landed**, headless. `e2e plan\|run\|resume\|phase\|diagnose\|cleanup\|profiles\|phases`. |
| `.mise.toml` | repository root | **landed**, extended: builds all three binaries, adds `e2e-phase` and `e2e-simulate`, calls `scripts/check.sh` rather than duplicating it. |
| `.github/workflows/opencenter-e2e.yml` | `.github/workflows/` | **landed.** Three jobs: `safe` on pull requests, the nightly full lifecycle, `real-provider` behind a GitHub Environment. Not yet exercised. |
| `docs/testing/*.md` | `docs/testing/` | **landed**, unverified against the binary — see §8. |
| `cmd/opencenter-test-bench/serve.go` + `ui.html` | nowhere, as files | Deliberately **not** copied. There is one console — `cmd/testlab` — and the lifecycle becomes a section of it, not a second web UI on a second port. Use them as the source of the section's markup and endpoint shapes (§4). |
| `internal/actions/` — `actions.go`, `workflow.go`, `github.go`, `install.go` | **Merge into `internal/actionsetup`, never as a second package** (§5) | Not started. Written for the prototype only because it could not import `actionsetup`. Here it must not exist as a rival. |

### Import rewrites — already done

`fulltestbench` imported `github.com/opencenter-cloud/opencli-testbench/pkg/redact` and its
own `opencenter-e2e-testbench/internal/…`. Both were rewritten to this module's
`internal/…` paths; there are no stale imports left. `pkg/redact` is an alias shim that
exists only for the cross-module case — leave it alone, but do not import it from any new
code here.

`e2e serve` is kept as a named refusal rather than removed, because somebody who learned
that command from the prototype deserves to be told where it went rather than told it
never existed.

While you are there, use what is already here instead of what the prototype had to
approximate: `internal/sandbox` for the isolated run directory, `internal/report` where the
formats overlap, `internal/preflight` alongside the prerequisites phase.

## 4. Wiring the lifecycle into the console

Do not ship a second web UI on a second port. There is one console; the lifecycle is a
part of it.

1. **A stage in the rail.** Add the lifecycle as a stage the rail renders, with its own
   colour, between `deploy` and `operate` — or as its own band if the 21 phases do not sit
   comfortably inside the existing seven. `internal/e2e/stage.go` already groups all 21
   phases into 9 numbered, coloured stages, and `stage_test.go` asserts every phase has
   exactly one home and that they run in lifecycle order. Keep that test.
2. **A section in the main column.** Profile picker with `Profile.Notes`; the run controls
   (plan / run / resume / phase / diagnose / cleanup / stop, plus `--simulate`); the phase
   window (`--from-phase`, `--to-phase`, `--only-phase`, `--skip-phase`); the 21 phases as
   expandable stage sections showing each `PhaseResult` with every `Command`'s argv, exit
   code, timing and output.
3. **The verdict on the existing board.** `Run.Gate()` returns PASS / WARNING / FAIL /
   INCONCLUSIVE / SIMULATED. Render it in the `.tsum` component that is already there, with
   the reason, the phase counts, and resources created / removed / **remaining**. Remaining
   is a FAIL regardless of what the tests said — a green run that leaks a cluster has not
   passed — and the board must say so in those words.
4. **Findings in the existing triage table.** `Run.Findings()` already carries the brief's
   taxonomy: product defect, regression, provider issue, environment issue, missing
   prerequisite, invalid configuration, expected injected failure, bench defect, cleanup
   defect. Colour the categories that are the product's fault differently from the ones
   that are not.
5. **New endpoints on the existing server**, beside `/api/catalogue`, `/api/run`,
   `/api/actions-board`: `/api/e2e/catalogue` (stages, phases, profiles — read from
   `internal/e2e`, never a copy in the page), `/api/e2e/state`, `/api/e2e/runs`,
   `/api/e2e/run/{id}`, `/api/e2e/start`, `/api/e2e/stop`, and evidence serving for the
   five report formats.

**SIMULATED is a verdict, never a footnote.** A footnote is what a dashboard truncates, a
script ignores and a screenshot crops out. Render it as loudly as FAIL, and keep
`--simulate` unable to reach PASS by any route (it exits 4).

**Real-provider profiles stay unstartable from the page.** A button press is not an
approval. The refusal must print the command to type:
`./bin/… e2e run --profile <name> --approve-live`.

## 5. Extending the GitHub Actions stage — do not fork it

The panel currently installs one workflow: `actionsetup.WorkflowPath` is the constant
`.github/workflows/test-bench.yml`, and `WorkflowFile` scopes the run list to it. The
lifecycle needs its own workflow, and both must be installable from the same panel.

Generalise, in `internal/actionsetup`:

- Turn `WorkflowPath` / `WorkflowFile` from package constants into a property of a **kind**
  — `test-bench` and `opencenter-e2e`. Keep them constants per kind. The blast radius of
  "install CI for me" must stay one known file in one known place; making the path a free
  string turns a convenience into an arbitrary write.
- `Workflow(Options)` gains the E2E rendering path. `internal/actions/workflow.go` in the
  prototype is that renderer already: it reads the profile matrix out of `e2e.Profiles` so
  a profile added to the catalogue cannot be missing from CI, and — the part that matters —
  a profile that starts deploying cannot stay on the pull-request matrix because somebody
  forgot to move it. Keep that property.
- `Install`, `Trigger` and `ListRuns` take the kind and otherwise stay exactly as they are.
- The panel gets a selector for which workflow it is acting on. `config/github-actions-gitops.yaml`
  carries the copy; extend `how_it_works` rather than writing a second explanation.

Enforce in the generated E2E workflow:

- Pull requests get only `Deploys == false` profiles. A fork must never be able to reach
  real infrastructure or spend money.
- `LiveApproval` profiles are `workflow_dispatch` only, behind a GitHub Environment,
  carrying `--approve-live` explicitly.
- Cleanup and evidence upload steps are `if: always()` — the evidence from a failed run is
  the evidence that matters, and a run that died mid-deploy is the one that left a cluster
  behind.
- Secrets reach the process through `env:`, never a command line: an argument is visible in
  the process table and in the step's own echoed command.
- Per-provider secrets, not a union of all of them. A workflow mapping every secret into
  every job teaches people the list is decoration.

**Both gates stay.** `OPENCLI_ALLOW_ACTIONS_SETUP` in the environment *and* the operator's
per-action checkbox, for anything that writes to a repository. Generate and Preview are
read-only and need neither.

## 6. mise

Required by the brief and absent here. Add `.mise.toml` at the root:

- `[tools] golang = "1.26.4"` — the version the openCenter CLI repository pins. Building
  the CLI under test with a different Go than its own `.mise.toml` specifies tests a binary
  nobody ships.
- Tasks are **thin wrappers**. Every one shells into the binary. Nothing in `.mise.toml`
  reimplements a phase, so `mise run e2e-kind` and the GitHub Actions job execute the same
  code.
- Reconcile with `scripts/`: where a script and a task would do the same thing, the task
  calls the script. Two build paths that drift is the failure this whole design avoids.

## 7. Non-negotiables

1. **One binary, two channels.** The workflow file chooses *when* to run and *what* to
   keep. It never says *how* to test. No phase logic in YAML, ever. The reference already
   deleted 746 lines of shell that duplicated the promotion steps, after the two copies
   drifted on which environment variables they read.
2. **No network at page load.** The console is one embedded HTML file. No CDN, no external
   font, no remote image.
3. **Redaction on the way in.** `redact.New()` plus `AddFromEnv` before the first command
   runs, so nothing is written unredacted even once. One redactor — do not add a second.
4. **Every claim needs evidence.** A zero exit from `destroy` is a claim; `verify-cleanup`
   is the evidence. Keep the distinction visible in the UI.
5. **Required phases stay unskippable.** `--skip-phase destroy` is exactly the flag someone
   reaches for when a run is slow, and it is the one that leaves a cluster running.
6. **Interrupt, never kill.** Ctrl-C and the Stop button cancel the run so destroy,
   diagnostics and report still happen. A bench that leaks a cluster when interrupted is
   worse than one that takes another minute to stop.
7. **Comments explain why, not what.** Match the density and tone of the files already
   here — they record the decision and the failure that motivated it. Do not strip them,
   and do not add narration of what the next line does.

## 8. Documentation

The brief asks for a *documented* design, so these are deliverables:

- `docs/testing/e2e-cluster-lifecycle.md` — the design: phases, profiles, gates, evidence.
- `docs/testing/e2e-runbook.md` — the manual path: every mise task, every CLI invocation,
  how to rerun one phase, how to clean up a run that died.
- `docs/testing/e2e-current-state.md` — implemented vs. asked-for, honestly, including gaps.
- `README.md` and `COMMANDS.md` — the two execution modes side by side, with exact commands.

Verify every command by running it. Where a documented command does not exist — the
prototype already caught two, there is no `preflight` command (`doctor` is it) and no
`--dry-run` flag (`generate --render-only` is it) — fix the document, not the code.

## 9. Definition of done

- [ ] `go build ./...` and `go test ./...` pass.
- [ ] `mise run build` produces the binaries; `mise run test` is green.
- [ ] `mise run e2e-safe` passes and writes all five report formats.
- [ ] `PROFILE=openstack-emulated mise run e2e` with `--simulate` reports **SIMULATED** and
      exits 4.
- [ ] The existing console still works exactly as before — the CLI command table, the
      credentials panel, the environments, the GitHub Actions panel. Nothing regressed.
- [ ] The lifecycle appears in the same console: rail stage, section, verdict on the
      existing board, findings in the existing triage table.
- [ ] Both themes, no console errors, no horizontal page scroll at 1280 px and at 900 px.
- [ ] Every one of the 21 phases is in exactly one rail stage, asserted by a test.
- [ ] The Actions panel can install **either** workflow: generate, preview a diff, install
      as a pull request, trigger a run, read runs back — the three read-only actions
      working with no gate, the two writing ones refusing without both gates.
- [ ] A `LiveApproval` profile cannot be started from the page, and the refusal prints the
      command to type.
- [ ] A run interrupted mid-deploy still runs diagnostics, destroy, verify-cleanup and
      report.
- [ ] No secret appears in any artifact, log, report or API response. Grep the artifacts
      directory for the test credentials and prove it.
- [ ] The four documents match the binary's actual behaviour.

## 10. Order of work

1. ~~Copy `internal/e2e/` in and rewrite its imports.~~ **Done.**
2. ~~Add `.mise.toml` and reconcile it with `scripts/`.~~ **Done.**
3. ~~Add the E2E CLI command and prove the lifecycle runs headless.~~ **Done** —
   `mise run e2e-safe` completes all twenty-one phases and writes all five formats.
4. **Start here.** Generalise `internal/actionsetup` to two workflow kinds, with tests.
   `.github/workflows/opencenter-e2e.yml` is already in the tree; make the panel able to
   render and install it.
5. Wire the lifecycle into the existing console — rail stage, section, verdict on the
   `.tsum` board, findings in the `.tri-*` table.
6. Extend the Actions panel with the workflow selector.
7. Verify the documentation against the binary, and settle the `--simulate` question in
   the Status section at the top.

One commit per step, in that order, so a bisect lands on something meaningful. Steps 1 to 3
are currently **uncommitted** in the working tree.

---

### Appendix — what is still in `~/opencenter/fulltestbench`

The engine is already here, so that tree is now only a source for steps 4 to 6. It builds
and its tests pass; nothing in it is committed.

- `internal/actions/` — config with 0600 storage and blank-means-unchanged saving (a blank
  token box means "unchanged", not "delete the token"; treating it as the latter is how
  saving a repository name silently unauthenticates the panel), the E2E workflow renderer
  driven by `e2e.Profiles`, a GitHub REST client (list runs, dispatch, open-or-find pull
  request, default branch), and git plumbing that keeps the token in `GIT_CONFIG_*` and the
  key in a 0600 file rather than in argv — an argument is visible in the process table and
  in the step's own echoed command. **Merge into `internal/actionsetup`.**
- `cmd/opencenter-test-bench/serve.go` — the endpoint shapes for step 5: `/api/catalogue`
  (stages, phases, profiles, all read from `internal/e2e` so the page holds no copy),
  `/api/run/{id}`, the phase-window flags plumbed through to the CLI, and evidence serving
  restricted to an allow-list of five paths rather than a cleaned URL join. "I removed the
  `../`" is not the same claim as "only these five files are reachable".
- `cmd/opencenter-test-bench/ui.html` — a console page written against this project's own
  design tokens. The source of the E2E section's markup for step 5. It is not a second
  console and must not ship as one.
