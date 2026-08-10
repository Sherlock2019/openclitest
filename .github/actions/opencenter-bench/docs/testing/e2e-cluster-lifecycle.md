# E2E cluster lifecycle — design

The test design for cluster deployment and lifecycle validation using
openCenter-cli, executable from GitHub Actions and from a command line.

Companion documents: [`e2e-current-state.md`](e2e-current-state.md) (what was
found in the repositories) and [`e2e-runbook.md`](e2e-runbook.md) (how to run it).

---

## 1. Purpose

One workflow that builds the CLI, stands a cluster up with it, proves the cluster
healthy, tears it down, proves the teardown, and leaves evidence a release
decision can rest on. The same workflow serves automated release validation, CI,
development and troubleshooting — because a separate CI path and a separate
manual path drift, and then disagree at the worst moment.

## 2. Architecture

```
                    ┌──────────────────────┐
                    │  E2E Workflow Engine │   internal/e2e
                    │  phases · states     │
                    │  assertions          │
                    │  registry · gate     │
                    └──────────┬───────────┘
                               │
          ┌────────────────────┼────────────────────┐
          │                    │                    │
   Local CLI adapter    GitHub Actions        Report adapter
   cmd/opencenter-      .github/workflows/    reports/*.html|md|json|csv
   test-bench           opencenter-e2e.yml    junit/e2e.xml
```

The workflow file passes `--execution-channel github-actions` and nothing else
differs. It chooses *when* to run and *what to keep*; it never expresses *how* to
test. A phase implemented twice is a phase that will eventually be implemented
differently.

**Reused from the console bench**, not rebuilt: the redactor (through
`pkg/redact`, aliased so there is one implementation), and the models the
assessment maps — sandbox, registry, report, cli, preflight, flexsim.

## 3. Profiles

| Profile | Infrastructure | Provider | Deploys | Approval |
|---|---|---|---|---|
| `configuration-only` | none | — | no | — |
| `openstack-emulated` | emulated | OpenStack | no | — |
| `vmware-emulated` | emulated | VMware | no | — |
| `baremetal-emulated` | emulated | bare metal | no | — |
| `kind` | local Kubernetes | Kind | yes | — |
| `openstack-real` | real | OpenStack | yes | `--approve-live` |
| `vmware-real` | real | VMware | yes | `--approve-live` |
| `baremetal-real` | real | bare metal | yes | `--approve-live` |

Three dimensions, deliberately independent: **execution channel**,
**infrastructure mode**, **provider**. Conflating channel with provider produces
a matrix where "openstack" and "github-actions" are alternatives, and then the
thing that actually matters — the same OpenStack test running in both places —
cannot be expressed.

## 4. Phases

21, ordered, each declaring what it needs and whether it can create anything.

| # | Phase | Creates | Required | Command it drives |
|---|---|---|---|---|
| 0 | plan | — | ✓ | — (approval gate) |
| 1 | workspace | — | ✓ | — |
| 2 | prerequisites | — | ✓ | tool discovery, API-port check |
| 3 | build | — | | `mise run build` |
| 4 | verify-binary | — | | `version`, `cluster --help` |
| 5 | configure | — | | `cluster init`, `cluster set` |
| 6 | validate-config | — | | `cluster validate [--output json]` |
| 7 | doctor | — | | `cluster doctor` |
| 8 | render-preview | — | | `cluster generate --render-only` |
| 9 | generate | — | | `cluster generate` |
| 10 | validate-artifacts | — | | tofu/kustomize/kubectl, secret scan |
| 11 | deploy | **✓** | | `cluster deploy [--container-runtime]` |
| 12 | infrastructure | — | | `kind get clusters`, `cluster status` |
| 13 | kubernetes-health | — | | `kubectl` |
| 14 | platform-health | — | | `flux`, `kubectl get csv` |
| 15 | smoke | **✓** | | `kubectl` |
| 16 | failure-tests | **✓** | | injected scenarios |
| 17 | diagnostics | — | ✓ | collector |
| 18 | destroy | — | ✓ | `cluster destroy --force --remove-files` |
| 19 | verify-cleanup | — | ✓ | `cluster list`, `kind get clusters` |
| 20 | report | — | ✓ | — |

**Required phases cannot be skipped.** `--skip-phase destroy` is refused, not
honoured: it is the flag people reach for when a run is slow, and the one that
leaves a cluster running on somebody's account.

**Always-run phases** — diagnostics, destroy, verify-cleanup, report — execute
after a failure, after a cancellation, and after a `--to-phase` window that stops
early. A run that fell over is when they matter most.

## 5. States

| State | Meaning |
|---|---|
| `not_started` `running` `cleaning` | in flight |
| `passed` | the assertion held |
| `warning` | held, with something worth reading |
| `failed` | **the product is wrong** |
| `blocked` | **we never found out** |
| `skipped` | not applicable to this profile |
| `cancelled` | the operator stopped it |

`failed` and `blocked` are the distinction the whole gate rests on. Treating a
missing prerequisite as a product failure blocks a good build; treating it as a
pass ships an untested one.

## 6. Assertions

**Kubernetes (13)** — API reachable; every node `Ready`; no pod in
`CrashLoopBackOff` or `ImagePullBackOff`; every PVC `Bound`; CoreDNS has ready
replicas. Polled against deadlines, never slept through: a fixed sleep is too
short on a loaded runner and wasted minutes on an idle one.

**Platform (14)** — services read from `cluster export --output json`, so only
what this cluster enables is checked. Flux kustomizations `Ready`, OLM CSVs
`Succeeded`, a ready-replica check per named service. A disabled service reports
*not enabled*; it never fails.

**Smoke (15)** — namespace → deployment → service → **call it by DNS name from
inside the cluster** → delete. The in-cluster call is the assertion that covers
DNS and service networking, which a `Ready` deployment does not prove. Cleanup is
deferred, on a fresh context, because a smoke test that returns early on failure
is exactly when its namespace gets left behind.

**Failure injection (16)** — invalid configuration rejected cleanly; a command
past its deadline terminated and recorded as timed out; a missing prerequisite
blocking rather than failing; a deleted pod recreated. Every finding is
recategorised as `Expected injected failure` so it cannot gate a release.

## 7. Failure classification

`Product defect` · `Regression` · `Provider issue` · `Environment issue` ·
`Missing prerequisite` · `Invalid configuration` · `Expected injected failure` ·
`Test Bench defect` · `Cleanup defect` · `Unknown`

Deploy failures are classified from the output. Signatures that are somebody
else's problem — port already allocated, daemon not running, disk full, socket
permissions, registry rate limit — become `Environment issue` and *block* rather
than fail. This is not theoretical: the first deploy here failed with
`Bind for 127.0.0.1:6443 failed: port is already allocated` and was reported as a
product defect, which would have blocked a release over a leftover container.

Anything unrecognised stays a product defect. A classifier that shrugs
"environment" at every failure is a gate that never blocks anything.

## 8. Release gate

| Verdict | Exit | When |
|---|---|---|
| `PASS` | 0 | every phase that ran passed, **and nothing was left behind** |
| `WARNING` | 0 | passed with warnings |
| `FAIL` | 2 | a phase failed, **or** a resource survived cleanup |
| `INCONCLUSIVE` | 3 | blocked, cancelled, or the environment was unavailable |

A green run that leaks a cluster is a `FAIL`. `INCONCLUSIVE` is not a soft
failure — it means the build was not tested, and must never be recorded as a pass.

## 9. Evidence and cleanup

Every created resource is registered **before** the command that creates it
returns, with a cleanup order (smoke workloads → services → GitOps → Kubernetes →
infrastructure → branches → files) and a remediation string. A deploy killed in
its third second has already made a container; a registry written when the phase
ends is empty for exactly the run that needed it.

Cleanup is **verified, not assumed**: `cluster destroy` exiting 0 is a claim, and
phase 19 goes and looks. It has already caught destroy leaving the cluster listed.

Run state is written to `state/run.json` after every phase, atomically, so a
process killed mid-save cannot lose the record of what it created.

## 10. Secrets

Seeded into the redactor before phase 1, and everything written — command output,
evidence, diagnostics, reports — passes through it. Generated test credentials
are added the moment they exist. Secrets reach CI through `env:`, never through
a command line, where they would be visible in the process table.

## 11. Runners

| Profile | Runner |
|---|---|
| configuration-only, emulated | GitHub-hosted |
| kind | GitHub-hosted, if the resource limits allow |
| `*-real` | approved self-hosted, behind the `e2e-real-provider` environment |

Real-provider jobs are `workflow_dispatch` only and unreachable from a pull
request, which makes fork access to credentials impossible rather than unlikely.

## 12. Limitations

- Phases 12–16 are implemented and have **not** executed against a live cluster.
- No real provider has been contacted.
- The `Regression` column cannot yet be true: no baseline of previous runs is
  stored to diff against.
- Emulated profiles stop before deploy, so `flexsim` is not yet exercised.
