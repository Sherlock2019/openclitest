# E2E runbook

For the engineer with a failed run and a question.

## Run it

```bash
mise run e2e-safe                     # creates nothing, ~1 minute
mise run e2e-emulated PROVIDER=vmware # simulated provider, creates nothing
mise run e2e-kind                     # real local Kubernetes
```

Always look before you leap on a real provider:

```bash
./bin/opencenter-test-bench e2e plan --profile openstack-real
```

It prints the four approvals and refuses to start until `--approve-live` is
given.

## Read the result

```
FAIL — 2 phase(s) failed
evidence: artifacts/e2e-20260807-101500
```

| Verdict | Means |
|---|---|
| `PASS` | every phase that ran passed, and nothing was left behind |
| `WARNING` | passed, with something worth reading |
| `FAIL` | the product is wrong, **or** something was left behind |
| `INCONCLUSIVE` | we never found out — blocked, cancelled, or the environment was unavailable |

`INCONCLUSIVE` is not a soft failure. It means the build was not tested, and it
must not be treated as a pass.

Exit codes: `0` pass or warning, `2` fail, `3` inconclusive.

## Where to look

```
artifacts/<run-id>/
├── reports/report.html      ← start here
├── reports/report.md        ← paste into a ticket
├── junit/e2e.xml            ← for CI
├── diagnostics/summary.md   ← phase-by-phase
├── diagnostics/commands.json← every command, exit code, stdout, stderr
├── evidence/                ← what each phase produced
└── cleanup/verification.json← what survived, and how to remove it
```

## Common situations

**"missing required tool(s)"** — the phase is `blocked`, not `failed`: nothing is
wrong with the product. The finding carries the remediation. `configuration-only`
needs only git, go and mise.

**"the binary under test was not built from the checked-out source"** — a warning,
and usually right. Something else built `bin/opencenter` earlier. Re-run; phase 3
rebuilds.

**mise says a config file is not trusted** — phase 3 runs `mise trust` in the CLI
repository before building and logs it. If it still fails, run it by hand once.

**Kind fails on the container runtime** — the CLI repository's `.mise.toml` pins
`CONTAINER_RUNTIME=podman`. If this machine has docker instead, phase 2 says so.
Install podman, or pass `--container-runtime docker` through the deploy phase.

**A run left something behind** — that is a `FAIL`, deliberately, even if every
test passed:

```bash
./bin/opencenter-test-bench e2e cleanup --run-id <run-id>
cat artifacts/<run-id>/cleanup/verification.json
```

Each remaining resource carries the exact command to remove it.

## Keeping a broken environment

```bash
./bin/opencenter-test-bench e2e run --profile kind --keep-on-failure
```

Destroy is skipped **only** if the run failed. Then investigate against the live
cluster:

```bash
./bin/opencenter-test-bench e2e phase --run-id <id> --only-phase kubernetes-health
./bin/opencenter-test-bench e2e diagnose --run-id <id>
```

and remember to clean up afterwards:

```bash
./bin/opencenter-test-bench e2e cleanup --run-id <id>
```

## Resuming

```bash
./bin/opencenter-test-bench e2e resume --run-id <id>
```

Phases that already passed are not re-run. Resuming into a different profile is
refused — it would run one profile's phases against another's cluster.

## Running part of it

```bash
--from-phase generate      # start here
--to-phase deploy          # stop here (cleanup still runs)
--only-phase kubernetes-health
--skip-phase doctor
```

Required phases refuse to be skipped. `--skip-phase destroy` is the flag people
reach for when a run is slow; it is also how a cluster gets left running, so it
is refused rather than honoured.

## In GitHub Actions

Actions → **openCenter E2E** → *Run workflow* → pick a profile.

- Pull requests get the four profiles that create nothing.
- `kind` runs nightly and on demand.
- The `-real` profiles are manual only and gated behind the
  `e2e-real-provider` environment, so a human approves before anything is
  created. They are unreachable from a pull request.

Evidence is uploaded with `if: always()` — the artifacts from a failed run are
the ones worth having.
