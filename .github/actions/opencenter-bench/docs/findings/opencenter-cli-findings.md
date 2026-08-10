# What the test bench found in openCenter CLI

**Build tested:** `openCenter-cli-testDzoan`, commit `e7b0ad3`
**Found by:** the command bench, running all 30 groups on a clean GitHub runner
**Date:** 8 August 2026
**Result:** 20 groups passed, 9 failed, 1 skipped — 16 individual problems

---

## Why there are suddenly 16 and not 2

Until today the command bench stopped as soon as the "Commands" group failed,
because that group was marked *if this fails, nothing else is worth running*.
Two problems in `cluster backup` were enough to stop it, so every run reported
**1 passed, 1 failed, 28 skipped** — two problems visible and twenty-eight
groups never executed.

That behaviour is right when a person is sitting there watching. It is wrong for
an automated run, where the price of two known problems was losing all coverage
of everything else. The automated run now continues after a blocking failure.
Nothing is softened — a failure still fails the run — it just finds out what
else is wrong before it stops.

The first run with that change found fourteen more.

**None of these are problems with the test bench.** Every one is behaviour of
openCenter CLI itself. The bench has not been changed to make any of them pass,
and openCenter's code has not been touched.

---

## 1. Answering "no" to a destroy still destroys

**Severity: high — this can lose work.**

The command asks for confirmation before destroying a cluster. Answering *no*
does not stop it.

| | |
|---|---|
| Check | `answering no does not destroy` |
| What happened | exit code 0, output `Destroying Kind cluster...` |
| What should happen | the command stops, destroys nothing, and says the confirmation failed |

A second check in the same area — `the abort explains that confirmation failed`
— fails for the same reason: the abort path is not taken at all.

This is the one to fix first. Everything else on this list costs time or trust;
this one costs a cluster.

---

## 2. Things that report success when they should fail

A command that fails silently is worse than one that fails loudly, because
whatever runs next assumes it worked.

### 2.1 An invalid provider type is accepted

| | |
|---|---|
| Check | `an unsupported provider is refused` |
| Command | `opencenter cluster init … --type not-a-real-provider` |
| What happened | exit code 0, `Created cluster configuration in organization 'provorg' at …` |
| What should happen | non-zero exit, and a message naming the provider types that exist |

A configuration now exists on disk for a provider that does not.

### 2.2 An unknown subcommand succeeds

| | |
|---|---|
| Checks | `secrets definitely-not-a-command exits non-zero`, `… explains itself on stderr` |
| What happened | exit code 0, and the help text printed as though the command had run |
| What should happen | non-zero exit, and "unknown command" on stderr |

A typo in a script does not stop the script.

### 2.3 Encryption that encrypted nothing reports success

| | |
|---|---|
| Check | `an encryption that processed no files does not report success` |
| What happened | exit code 0, output `🔒 Starting secrets encryption...` and nothing encrypted |
| What should happen | say that no files matched, and exit non-zero |

Someone can believe their secrets are encrypted when no file was touched.

### 2.4 A prompt with no answer exits 0

| | |
|---|---|
| Check | `a prompt with no answer aborts` |
| What happened | exit code 0 |
| What should happen | give up and exit non-zero |

In an automated pipeline there is nobody to answer a prompt, and this reports
success.

---

## 3. Things that do not match the documentation

### 3.1 `--output json` returns something that is not JSON

Two commands accept the flag and return a different format.

| Command | What came back |
|---|---|
| `opencenter cluster backup list az-test-org/az-test --output json` | the sentence `No backups found for cluster az-test-org/az-test` |
| `opencenter settings view --output json` | YAML, starting `logging:` |

Anything reading the output automatically fails at the first character. When
there is nothing to report, the correct JSON answer is `[]` or `{}`.

### 3.2 The documented exit code is not the one returned

| | |
|---|---|
| Check | `exit code is 3, as documented` |
| What happened | exit code 1 |
| Where it is documented | `docs/reference/exit-codes.md` — 3 means a missing configuration |

A pipeline that distinguishes "missing configuration" from "general error"
cannot.

### 3.3 Two errors promise recovery advice and do not give it

| | |
|---|---|
| Checks | `recovery suggests cluster init`, `recovery suggests cluster list` |
| What happened | `Error: validation error: resolving cluster paths: cluster no-such-cluster-anywhere not found in any organization` |
| What should happen | the documented next step — try `cluster list`, or `cluster init` |

---

## 4. A check that is not checking

| | |
|---|---|
| Check | `a failing external tool changes what doctor reports` |
| What happened | `doctor` printed the same output whether `kubectl` worked or was broken — `git: OK` either way |
| What should happen | a broken tool is reported as broken |

`doctor` exists to tell you whether this machine can do the job. If it answers
the same either way, it cannot be relied on — and it is the command people run
first when something is wrong.

---

## 5. Smaller ones

### 5.1 A preview writes files

| | |
|---|---|
| Check | `nothing was generated into the working directory` |
| Command | `opencenter cluster generate <name> --render-only` |
| What happened | 120 files written, including `schema/opencenter-v2.schema.json` |
| What should happen | a preview shows what would be produced and writes nothing |

This is also the single warning on the full-cycle test, on every run.

### 5.2 A command times out instead of answering

| | |
|---|---|
| Check | `opencenter cluster backup schedule … --interval 24h answers` |
| What happened | no answer within 25 seconds |
| What should happen | answer, or explain why it cannot |

### 5.3 An error message repeats itself

`Error: failed to load configuration: failed to parse YAML configuration: failed
to parse YAML configuration: stage 1 (load): YAML type errors (4)`

The same clause appears twice. The four type errors are counted but not listed.

---

## Suggested order

1. **Section 1** — declining a destroy still destroys. This one can lose work.
2. **Section 2** — four commands that report success when they failed. These
   make every script built on the tool unreliable.
3. **Section 3.1** — `--output json` returning something else. Small fix,
   immediate benefit to anything automated.
4. **Section 4** — `doctor` not actually checking. It is the first command
   people run when something is wrong.
5. The rest, in any order.

---

## How to see this yourself

The full-cycle test and the command test both run on every commit to
`openCenter-cli-testDzoan`:

- `.github/workflows/test-bench.yml` — the command test, this list
- `.github/workflows/opencenter-e2e.yml` — the full cycle, currently passing

Each run's summary page lists every failing command with its probable cause. The
console at `./start.sh` shows the same list, with the exact command and output
for each.
