# Findings

What the bench found, and what was done about it.

```bash
bash scripts/rebuild-and-run.sh          # rebuild the CLI, run all 30 modules
bash scripts/summarise-run.sh <run-id>   # the non-passing modules
```

| Run | Result |
|---|---|
| `20260803-181247` — before the fixes | 24 passed, **5 failed**, 1 locked |
| `20260803-211100` — after the fixes | **29 passed, 0 failed**, 1 locked |

Module 29 is locked in both: live infrastructure was not approved, so **nothing
here says whether a real deployment works**.

The CLI's own test suite passes throughout: 46 packages, 0 failures,
`go build` and `go vet` clean.

---

## Fixed

Six defects, all found by running the bench and all fixed in the CLI. The
changes are left uncommitted in the `openCenter-cli` checkout for review —
that repository already had unrelated work in progress, and entangling the two
would make both harder to read.

### 1. `cluster sync openstack` wrote a configuration the CLI could not read

*Module 5 · Successful results* — `internal/cluster/sync/openstack/service.go`

Sync exited 0 and wrote `secrets.etcd-backup.*` and `secrets.velero.*`. Neither
exists on `v2.SecretsConfig` or in the schema, and the loader rejects unknown
fields, so every command after the sync failed:

```
field etcd-backup not found in type v2.SecretsConfig
```

A command must not write a file its own loader refuses. Services with a
dedicated struct field keep their top-level path; the rest now go under
`secrets.service_secrets.<service>`, which is the map that exists for this and
which the schema already allows.

Fixing that uncovered a second one it had been masking: sync also wrote
`secrets.tempo.s3_access_key_id`, but `TempoSecrets` calls those fields
`access_key` and `secret_key`. Both are now the names the loader and the
schema agree on.

**A configuration already written by the broken version will still not load.**
The fix stops new ones being produced; it does not repair existing files. Those
need the four bad keys removed by hand, or a migration in `cluster normalize`.

### 2. A missing cluster ignored its documented exit code

*Module 6 · Errors* — `internal/core/paths/errors.go`, `resolver.go`, `main.go`

`docs/reference/exit-codes.md` documents exit 3 plus recovery instructions.
`main.go` implemented exactly that — but only for `v2.ConfigNotFoundError`,
and the path resolver returned a plain `fmt.Errorf`. Any command that resolves
paths without going through the configuration manager — `validate`, `setup`,
`bootstrap` — got exit 1 and no recovery text.

The resolver now returns a typed `paths.ClusterNotFoundError`, so every caller
that wraps with `%w` keeps the contract, and `main.go` honours both types.

### 3. An unknown subcommand exited 0

*Module 6 · Errors* — seven files in `cmd/`

```
$ opencenter secrets definitely-not-a-command
Manage secrets across different backends…
→ exit 0
```

`cmd/cluster.go` had `Args: cobra.NoArgs`; seven other groups did not, so
Cobra parsed the typo as an argument, ran the group's help and exited 0. A
pipeline running `opencenter secrets encryptt` was told it succeeded.

Fixed in `secrets`, `secrets keys`, `cluster service`, `cluster backup`,
`cluster drift`, `cluster import` and `settings explain`. The bare group still
prints its help and exits 0. Root was left alone: Cobra restricts it already.

### 4. An unsupported provider was accepted and silently replaced

*Module 21 · Provider support* — `cmd/provider_availability.go`

`checkProviderAvailability` rejected a hard-coded list of *planned* providers
and let everything else through, so `--type not-a-real-provider` created an
OpenStack cluster and reported success. It is now an allowlist: unknown values
are refused, planned ones still get their more useful "coming later" message.

### 5. `cluster doctor` reported a broken tool as OK

*Module 14 · External tools* — `cmd/cluster_doctor.go`

The check was `exec.LookPath`. A `kubectl` that exists but cannot run — broken
symlink, missing library, wrong architecture — was reported `OK`, which is
precisely the situation doctor exists to catch. It now runs the tool with a
bounded probe and reports `NOT WORKING (<reason>)`.

### 6. `secrets encrypt` reported success after encrypting nothing

*Module 23 · Secrets management* — `cmd/secrets_sops_helpers.go`

```
🎉 Encryption completed: 0/2 files processed successfully
❌ Failed to encrypt secret.yaml: SOPS encryption failed
→ exit 0
```

Per-file failures were printed and then `return nil`. `secrets encrypt && git
commit` committed the plaintext. Both encrypt and decrypt now return an error
when any file failed.

---

## Still true, and worth saying so

The security modules pass, and they are not gentle:

- **No credential leak.** Four canaries injected through `OS_PASSWORD`,
  `OS_APPLICATION_CREDENTIAL_SECRET`, `OS_TOKEN` and `SOPS_AGE_KEY`, across
  seven invocations including `--log-level debug` and the failure paths.
- **No path traversal.** `../escape`, `../../../escape`, an absolute path, a
  400-character name, a Unicode fraction slash. Nothing written outside the
  configuration and state roots.
- **No shell injection.** Sixteen payloads as cluster names, organizations and
  field values. The sentinel file was never created.
- **Cancellation is clean.** The CLI exits non-zero inside the deadline, the
  child is gone, no lock or partial file survives.
- **Generation is idempotent.** Two `cluster generate` runs, byte-identical.
- **Every API failure mode is handled.** 401, 403, 404, 409, 429, 500, 503,
  unparseable JSON and a stalled connection: all nine produce a non-zero exit
  and an explanation, none panics, none hangs.

---

## Findings in the bench itself

Recorded because a bench that only reports other people's bugs is not being
looked at hard enough. All five were found by running it, and all are fixed.

1. **The redactor corrupted YAML.** A `\s*` matched across newlines, so
   `secrets:` followed by an indented `backend: sops` became
   `secrets:[REDACTED]`. Recorded output stopped parsing and the bench blamed
   `cluster export`. Every gap is now `[ \t]*`, with a test asserting redaction
   never changes the line count.
2. **A skip could bury a failure.** A check that failed an assertion and then
   skipped was reported as skipped. It is now a failure.
3. **A permission check matched filenames.** `keycloak.yaml` matched "key".
   It reads the file and looks for real key material now.
4. **A cleanup assertion was order-dependent.** It asserted a shared directory
   was empty. It now snapshots before its own commands.
5. **Marking Module 5 blocking hid 25 modules** behind one cloud finding. It is
   not blocking: later modules recreate the fixture on demand, so a failure
   there genuinely does not invalidate them.

A sixth is worth recording as a process note rather than a bug: while fixing
defect 6, a hand-written check of mine passed while the bench still failed —
my check had reproduced a different code path. The bench was right.

---

## Not answered

- **Module 29, the real environment.** Locked: no approval, no mutation gate.
  Nothing here says whether a cluster deploys, whether nodes come up, or
  whether a destroy leaves orphans. Run with `--approve-live
  --approve-cleanup --confirm-disposable` and `OPENCLI_ALLOW_MUTATE=1`.
- **A real OpenStack project.** Every cloud result is from the simulator.
- **The SOPS round trip.** `sops` is not installed here, so encrypt-then-decrypt
  was never attempted; only the failure path was.
- **macOS.** Everything above is Linux.
- **Existing broken configurations.** Defect 1 stops new ones being written; it
  does not repair files the broken version already produced.
