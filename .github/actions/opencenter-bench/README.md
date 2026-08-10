# openCenter CLI Test Bench

**Can this build of `openCenter-cli` create, run and remove the Kubernetes
platform it claims to manage — and does every command do what it says?**

Those are two different questions, and a build can pass one while failing the
other. This bench asks both, on your machine or on GitHub's, and answers with
evidence rather than a colour.

---

## Table of contents

- [Who it is for](#who-it-is-for)
- [What it tests](#what-it-tests)
- [Where it runs](#where-it-runs)
- [How to run it](#how-to-run-it)
- [What you give it — inputs](#what-you-give-it--inputs)
- [What you get back — outputs](#what-you-get-back--outputs)
- [Reading the results](#reading-the-results)
- [Choosing a scope](#choosing-a-scope)
- [Safety](#safety)
- [Layout](#layout)
- [Adding a check](#adding-a-check)

---

## Who it is for

| You are | What you want from it | Where to start |
|---|---|---|
| **Working on the CLI** | Did my change break something, and what exactly | [Run it locally](#how-to-run-it), read *What is broken* |
| **Deciding whether to ship** | Is this build releasable | The verdict and the count of *product defects* |
| **Running CI** | Every commit judged, without anybody remembering to | [GitHub Actions](#where-it-runs) |
| **Handed a red build** | Which command, on which provider, and how to reproduce | The *What is broken* table — every row carries all three |

It is deliberately **not** a unit test suite. It runs the real binary, against
real or emulated providers, and judges what came out.

---

## What it tests

### 1. Every CLI command

Around **388 command invocations across 4 environments**, each checked against
what the documentation promises — not merely that it exited zero.

That distinction is the point. `cluster backup list --output json` exits 0 while
printing an English sentence; an exit-code check calls that a pass. This bench
parses the JSON and fails it.

Grouped into thirty modules: command tree and help, configuration, secrets,
generation, GitOps, exit codes, documented recovery text, drift, backups,
diagnostics, cleanup.

### 2. The cluster lifecycle, end to end

**21 phases** in order, on one of eight profiles:

```
 1 Prerequisites   plan · workspace · prerequisites
 2 Build           build · verify-binary
 3 Init            configure
 4 Validate        validate-config · doctor
 5 Generate        render-preview · generate · validate-artifacts
 6 Deploy          deploy · infrastructure
 7 Health          kubernetes-health · platform-health
 8 Operate         smoke · failure-tests
 9 Reset           diagnostics · destroy · verify-cleanup
10 Results         report
```

It builds the CLI from source, makes a cluster definition, validates it,
generates manifests and Terraform, deploys a real Kubernetes cluster, proves the
platform services are up, runs an app on it, **deletes a pod on purpose to see
whether Kubernetes puts it back**, tears the whole thing down, and then proves
nothing was left behind.

That last phase matters more than it sounds. `verify-cleanup` is how the bench
found that a container was creating root-owned files the run could not delete.

### Why both

Every command can pass on its own while the whole cycle fails — ordering, state
carried between steps, whether teardown removed what setup made. Only the
lifecycle sees that. And the cycle can pass while individual commands lie about
their results. Only the command run sees that.

---

## Where it runs

| | On this machine | On GitHub Actions |
|---|---|---|
| **Runs** | the binaries in `./bin` | one composite action, on a clean runner |
| **Docker** | yours — only the Kind profile needs it | GitHub's |
| **Triggered by** | a button, or the CLI | every commit, every pull request, or a button |
| **Good for** | working on one thing | judging a build nobody has hand-held |

Both run the **same** engine. CI is not a second implementation — it invokes the
same binaries with the same flags, which is why a local result and a CI result
are comparable.

Installing it into a repository writes **one file**:
`.github/workflows/test-bench.yml` or `.github/workflows/opencenter-e2e.yml`.

> **Token note.** GitHub refuses writes under `.github/workflows/**` unless the
> token carries the `workflow` scope, and reports it as a **404, not a 403** — a
> plain `contents:write` token looks like a missing repository. An SSH deploy key
> has no scopes and can push what the token cannot.

---

## How to run it

### The console

```bash
export OPENCLI_BIN=/path/to/openCenter-cli/bin/opencenter
./start.sh
```

Opens on `127.0.0.1:7700`. Without `OPENCLI_BIN` it looks in `./bin/opencenter`,
then a sibling checkout, then `PATH`.

The console is one page: choose where it runs, what to test, and against which
environment; press one button, or press Run on individual commands.

### The command line

```bash
# Every command, judged
bin/bench run full --source /path/to/openCenter-cli --provider kind

# The whole lifecycle
bin/opencenter-e2e e2e run --profile kind

# One phase again, against a run that already happened
bin/opencenter-e2e e2e phase --run-id <id> --only-phase kubernetes-health
```

That last line is printed on **every finding**, filled in — so reproducing a
failure is a paste, not a reconstruction.

---

## What you give it — inputs

| Input | Required | Where it comes from |
|---|---|---|
| **A CLI build** | yes | `OPENCLI_BIN`, or a checkout it builds itself |
| **A profile** | yes | `configuration-only` (default, creates nothing) … `kind` … `*-real` |
| **Provider credentials** | only for real providers | the Credentials panel, saved `0600` and gitignored |
| **A GitOps repository** | only to test promotion | optional; never merges, never deploys |
| **Scope** | no | *What to test* — all commands, all stages, or chosen stages |
| **Approval** | for anything live | three separate confirmations, none of which implies another |

**The safe default creates nothing at all.** `configuration-only` generates and
validates a cluster definition and touches no infrastructure. You have to ask
for more, in writing, three times.

---

## What you get back — outputs

Every run writes to `artifacts/runs/<run-id>/reports/`:

| File | For |
|---|---|
| `report.html` | reading — the whole run, expandable |
| `report.md` | pasting into a ticket or a pull request |
| `report.json` | machines; the console and CI both parse this |
| `results.csv` | a spreadsheet |
| `junit.xml` | any CI that understands JUnit |

Plus the run's own workspace: `home`, `config`, `state`, `work`, `bin`,
`evidence`, `logs`.

### The verdict

| Verdict | Exit | Means |
|---|---|---|
| **PASS** | 0 | everything asked, everything answered |
| **WARNING** | 0 | something non-blocking, named |
| **FAIL** | 2 | a phase or command did not do what it says |
| **INCONCLUSIVE** | 3 | blocked by the environment — *not* a verdict about the CLI |
| **SIMULATED** | 4 | a fake provider answered; cannot be read as a pass |

`INCONCLUSIVE` exists so a port conflict on your laptop is never reported as an
openCenter defect.

### On GitHub

Each failing command becomes a **`::error::` annotation**, so the run's front
page names the commands rather than saying "Process completed with exit code 1".
The console reads those back through the checks API and shows them beside the
local ones.

---

## Reading the results

The results panel is two things.

**Test scope** — did each half run, and what did each conclude:

```
TEST SCOPE                      ON THIS MACHINE                 ON GITHUB
Test all CLI commands           0/388 not run                   failure · 10 problems
Test E2E lifecycle stages only  WARNING · 19/21 phases passed    success
```

**What is broken** — one row per *distinct problem*, whichever bench found it:

```
WHERE       WHAT                              WHY               FOUND BY          SEEN  WHEN         GITHUB RUN  EVIDENCE
5 Generate  cluster generate <run>            --render-only     lifecycle · here  29×   08 Aug 09:15  —          08 Aug 09:15 …
            --render-only                     has side effects
—           cluster backup list … --output    not JSON          commands · here+CI 2×   2 min ago    run #61     —
            json parses
```

Thirty runs printed fifty-six failure lines and carried **fourteen** problems
between them. Reading the same defect twenty-nine times is not more information
than reading it once — so the row is the defect, and the runs hang off it.

**`FOUND BY` is the column that makes merging safe.** `commands · here+CI` means
reproducible on your machine. `commands · CI` means it needs a clean runner —
a different investigation.

### Whose fault is it

Findings are classified, and the classification is fixed rather than free text —
a release gate that cannot tell a provider outage from a product defect will
either block good builds or ship broken ones:

`Product defect` · `Regression` · `Provider issue` · `Environment issue` ·
`Missing prerequisite` · `Invalid configuration` · `Expected injected failure` ·
`Test Bench defect` · `Cleanup defect`

The first two are openCenter's. The rest are separated below a divider so they
cannot crowd out the ones that block a release.

---

## Choosing a scope

Under **Run options → What to test**:

1. **CLI commands** — all 388.
2. **E2E lifecycle stages** — all 21 phases.
3. **E2E lifecycle stages — chosen** — tick the stages you want.

All stages start ticked. Two rules are not preferences:

- **Deploy cannot be unticked.** Everything after it needs a running cluster, so
  leaving it out does not shorten the run — it empties it.
- **Reset and Results always run.** A run that skipped tearing down leaves a
  cluster on somebody's account; one that skipped reporting proves nothing.

The choice travels to CI. A scope that only applied on your laptop is not a
scope — the console and CI would disagree about what "the lifecycle" means, and
CI is the answer people quote. A shortened CI run posts a `::notice::` naming
what it left out.

---

## Safety

The bench handles credentials and can drive real infrastructure, so the rules
are not left implicit:

- **A workspace per run**, at `artifacts/runs/<run-id>/`, with its own `home`,
  `config`, `state`, `work`, `bin`, `evidence`, `logs` and `reports`.
- **The environment is rebuilt, not filtered.** Variables are constructed from an
  allowlist rather than by deleting known credential names. Deleting is a losing
  game: it needs updating every time a provider invents a variable, and the cost
  of missing one is a real credential in a test process.
- **Nothing touches your real configuration.** `OPENCENTER_CONFIG_DIR`,
  `OPENCENTER_STATE_DIR` and the XDG variables all point inside the workspace.
- **Redaction is one chokepoint and cannot be turned off.** Every value that
  looks like a credential is removed on the way to the console, the log, the
  evidence files and all five report formats. `internal/redact` has its own test
  suite, including that redaction never changes the shape of what it redacts.
- **Canaries, not hope.** Unmistakable fake credentials are injected before each
  run and hunted for afterwards in every byte of output and every file on disk.
  If one turns up, a real secret would have too.
- **Every external process has a deadline**, and children run in their own
  process group so a cancellation reaches OpenTofu and Ansible too.
- **Secrets never reach a command line**, where they would land in shell history.
  What the CLI receives is a `clouds.yaml` written inside the run's own workspace
  and deleted with it.

Credentials you type go to `config/credentials.local.yaml`, written `0600` and
gitignored. They are never sent back to the browser — the field just reads
"saved".

---

## Layout

```
cmd/
  bench/              the command bench and the GitHub Actions setup
  opencenter-e2e/     the 21-phase lifecycle engine's CLI
  testlab/            the console: one Go server, one HTML page
internal/
  e2e/                phases, verdicts, findings, stages
  workflow/           the thirty modules and their order
  actionsetup/        workflow generation, install, trigger, run reading
  gitopsupdate/       the eleven promotion steps
  redact/             the one chokepoint
  sandbox/            the per-run workspace
config/               checklists, credentials, stage definitions
action.yml            the composite action CI calls
```

The lifecycle's phases, assertions, evidence and cleanup live in the binary and
nowhere else. `action.yml` chooses **when** to run and **what to keep** —
deliberately not **how** to test. There is one implementation of every gate.

---

## Adding a check

Checks are data, not code, wherever they can be. A new command check is an entry
in a checklist under `config/`, with the assertions it must satisfy. A new
lifecycle phase is a function in `internal/e2e` plus its place in `stage.go`.

Two rules:

- **A finding names its command and how to reproduce it.** A count is not an
  answer.
- **A check that cannot fail is not a check.** Assert on output, not on the
  existence of code.

### Requirements

`internal/e2e/requirements_test.go` holds 60 requirements, each pinned to a file
and the strings that prove it is met. It fails when a capability is removed —
including when it is removed by a rename, which is how it should be read: the
requirement is the behaviour, not the function name.

---

## Getting started

Go 1.24 or newer, and a build of the CLI to test. Everything else is optional
and gates only the module that needs it — the console says which.

```bash
git clone git@github.com:Sherlock2019/fullopenclitestbench.git
cd fullopenclitestbench
export OPENCLI_BIN=/path/to/openCenter-cli/bin/opencenter
./start.sh
```

Then press **Run the full test now**, or connect a repository under *GitHub
Action set up* and let CI do it on every commit.
