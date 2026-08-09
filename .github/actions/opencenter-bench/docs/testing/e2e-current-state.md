# E2E cluster lifecycle — current state assessment

*Subtask 1 deliverable. Written 2026-08-07 by inspecting the built binary, the
repositories and the installed toolchain — not the documentation.*

The rule this document exists to enforce: **the code is the source of truth.**
Everything below was verified by running something, and the corrections in §2 are
the reason the assessment comes before the implementation.

---

## 1. Repositories on this machine

| Path | Go module | Role |
|---|---|---|
| `openCenter-cli-testDzoan` | `github.com/opencenter-cloud/opencenter-cli` | **The CLI under test.** Owns `.mise.toml`. |
| `openclitestsimple` | `github.com/opencenter-cloud/opencli-testbench` | **The existing bench.** The foundation to reuse. |
| `opencli-ultra` | `github.com/opencenter-cloud/opencli-testbench` | Older variant of the bench. Same module path — cannot be imported alongside. |
| `opencli-SDK` | `github.com/opencenter-cloud/opencenter-sdk` | SDK. Not needed for E2E. |
| `opencli-test`, `opencli-benchmark` | — | Earlier experiments. |
| `fulltestbench` | *(new)* | **This project.** Was empty. |

Built binary under test: `/home/dzoan/.local/bin/opencenter` — version `main`,
commit `095df39829d39b906d305a852272914d849e4248`, built 2026-07-07.

---

## 2. Corrections to the specification

The brief names commands and flags that **do not exist**. Verified by parsing the
`Available Commands:` block of `opencenter cluster --help`, then confirming each
absence by running it.

| The brief says | Reality | What the phase must use |
|---|---|---|
| `cluster preflight` | `unknown command "preflight"` | **`cluster doctor`** |
| `cluster diagnose` | `unknown command "diagnose"` | `cluster status` / `describe` / `export`, plus our own collector |
| `cluster generate --dry-run` | no such flag | **`cluster generate --render-only`** |
| `cluster plan` | `unknown command "plan"` | no equivalent — Phase 0 planning is the bench's own |
| `cluster render` | `unknown command "render"` | `generate --render-only` |

> **Probing trap.** `opencenter cluster <anything> --help` exits **0** and prints the
> parent help, so a `--help` probe reports every command as present. An earlier pass
> of this assessment reported all four as existing for exactly that reason. Parse the
> command list; do not trust the exit code.

### Commands that do exist

```
active  backup  configure  deploy  describe  destroy  doctor  drift  edit  env
export  generate  import  init  list  lock  migrate-layout  normalize  pool
service  set  status  unlock  use  validate
```

### Flags that matter

| Command | Flags |
|---|---|
| `init` | `--org --type --config-file --force --strict --full-schema --server-pool --no-keygen --no-sops-keygen --regenerate-keys --kind-disable-default-cni` |
| `validate` | `--config-file --manifests --output-dir --validation --generate-debug-config -v` |
| `generate` | `--force --render-only --skip-validation` |
| `deploy` | `--from-step --step --restart --container-runtime --kubeconfig --break-lock --log --debug` |
| `destroy` | `--force --remove-files --skip-infrastructure` |
| `doctor` | none beyond `--help` |

`--output json` is available on `validate`, `status`, `list`, `describe`, `export`.

**`deploy --from-step/--step/--restart` is significant:** the CLI already has native
resume. Phase 11 should drive that rather than inventing a parallel resume mechanism.

---

## 3. mise: the real build path

mise **is** installed — `/home/dzoan/.local/bin/mise`, version `2026.8.2` — but is
**not on `PATH`**. Any phase invoking it must use the absolute path or extend `PATH`.

`.mise.toml` lives in the CLI repo and pins:

```toml
[tools]
golang = "1.26.4"
kubectl = "latest"
kind = "latest"
helm = "latest"
"go:golang.org/x/vuln/cmd/govulncheck" = "latest"
"aqua:gitleaks/gitleaks" = "latest"
```

### Discovered tasks

| Task | What it does |
|---|---|
| `build-cli` | `go build` with `-X main.version/gitCommit/gitBranch/gitTag/buildDate` → `bin/opencenter` |
| `build-local-plugin` | → `bin/opencenter-local` |
| **`build`** | `["mise run build-cli", "mise run build-local-plugin"]` |
| `build-linux`, `build-all` | cross-compilation |
| `local-install` | builds, copies to `~/.local/bin`, registers the plugin checksum |
| `release` | versioned release binaries |

**Phase 3 uses `mise run build`.** It exists; no competing build process is needed.

Because the build injects `gitCommit` via ldflags, **Phase 4's "built version matches
source commit" check is supported** — compare `opencenter version`'s `Git commit`
against `git rev-parse HEAD` in the source tree.

### An environment conflict to resolve

`.mise.toml` sets:

```toml
KIND_EXPERIMENTAL_PROVIDER = "podman"
CONTAINER_RUNTIME = "podman"
```

But on this machine **podman is absent and docker is present**. The Kind profile will
fail with the repo defaults. Phase 2 must detect this and either select docker
explicitly (`deploy --container-runtime`) or report it as a blocking prerequisite with
that remediation — not fail obscurely inside Kind.

---

## 4. Installed tooling, measured

| Present | Absent |
|---|---|
| docker, kubectl, helm, flux, git, go, jq, mise | **kind**, podman, yq, kustomize, tofu, terraform, sops, age |

Consequences for the first implementation target:

- `configuration-only` — **runnable now.**
- `*-emulated` — **runnable now** (see `flexsim` below).
- `kind` — **not runnable until `kind` is installed.** `mise` pins it, so
  `mise install` inside the CLI repo should provide it; Phase 2 must verify rather
  than assume.
- Artifact validation (Phase 10) must **skip, not fail**, the validators whose tools
  are missing — `tofu`, `kustomize`, `sops` are all absent here.

---

## 5. What the existing bench already provides

`openclitestsimple/internal/` — 15 packages, all reusable. This is why the brief says
not to build a disconnected framework.

| Package | Lines | What it gives the E2E engine |
|---|---:|---|
| `checks` | 4178 | Assertion library |
| `gitopsupdate` | 3709 | Git/GitOps operations, repo config, redaction-aware clone/commit/push |
| `actionsetup` | 2216 | GitHub Actions install, trigger, run reading |
| `workflow` | 1442 | **The execution engine** |
| `report` | 910 | HTML/Markdown/JSON/JUnit/CSV output |
| `flexsim` | 902 | **A stand-in OpenStack API** — the emulated provider already exists |
| `runner` | 687 | Prepares a world and runs checks in it |
| `experimental` | 488 | Adds stages to the command table |
| `redact` | 461 | **Secret redaction** |
| `spec` | 427 | Loads the YAML under `config/` |
| `sandbox` | 392 | **Throwaway isolated workspace per run** |
| `source` | 285 | Git remote listing, branch/tag selection |
| `cli` | 269 | **Runs the openCenter binary, recording exact invocations** |
| `registry` | 204 | **The cleanup registry** — records everything a run creates |
| `preflight` | 146 | "Is everything here?" — the prerequisites phase |

Plus `cmd/testlab/`: failure classification and the category model (`results.go`),
emulation modes (`emulation.go`), and the dashboard (`ui.html`).

**Mapping to the brief's reuse list:**

| Brief asks to reuse | Exists as |
|---|---|
| CLI binary discovery | `internal/spec` + `internal/source` |
| Command execution engine | `internal/cli` + `internal/workflow` |
| Isolated workspace | `internal/sandbox` |
| Environment selector, modes | `cmd/testlab/emulation.go` + `config/*.yaml` |
| stdout/stderr capture | `internal/cli` |
| Result models, failure classification | `cmd/testlab/results.go` |
| Secret redaction | `internal/redact` |
| Dashboard | `cmd/testlab/ui.html` |
| Reports | `internal/report` |
| **Cleanup registry** | `internal/registry` *(named `registry`, not `cleanup`)* |

Nothing on the reuse list is missing.

---

## 6. Missing capabilities — what this project must actually add

Everything above is machinery for *running commands and judging them*. None of it
models a **long-running, resumable, ordered lifecycle**. New work:

1. **Phase model** — 21 ordered phases, 9 states, dependencies, skip/only/from/to.
2. **Run context persisted after every phase** — resume needs it on disk.
3. **Profiles** — `configuration-only`, three `*-emulated`, `kind`, three `*-real`.
4. **Safety gate** — Phase 0 approvals for real providers.
5. **Kubernetes and platform-service assertions** — polling with deadlines.
6. **Smoke tests** — sample app, optional Kafka.
7. **Injected-failure scenarios**, classified separately from product failures.
8. **Cleanup verification** — proving removal, not trusting exit 0.
9. **`e2e` command surface** — plan/run/resume/phase/diagnose/cleanup.
10. **Cluster E2E dashboard page** and the Actions workflow that calls the same engine.

---

## 7. Decisions taken from this assessment

1. **New module in `fulltestbench`**, importing the existing bench packages. It cannot
   live in `openclitestsimple` without entangling the working console, and it cannot
   import `opencli-ultra` (same module path).
2. **`cluster doctor` is the preflight** (Phase 7).
3. **`generate --render-only` is the dry run** (Phase 8).
4. **Phase 11 drives `deploy --from-step`** rather than reimplementing resume.
5. **Phase 4 verifies the commit**, since the build injects it.
6. **Missing validators skip, not fail** (Phase 10).
7. **Phase 2 resolves the podman/docker conflict explicitly.**
8. **First target: `configuration-only` and the three emulated profiles** — the only
   ones this machine can run today. `kind` is implemented but will report *blocked*
   with remediation until `kind` is installed. Real providers stay gated.

---

## 8. What has not been verified

Stated plainly, because the brief forbids claiming a provider passed without running
against it:

- No real OpenStack, VMware or bare-metal environment was contacted.
- `kind` has never been run here — the binary is absent.
- `mise run build` has not yet been executed by this project.
- Kubernetes, Flux and service assertions are untested against a live cluster.
