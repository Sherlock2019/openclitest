package checks

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
)

// Every command the CLI has, invoked for real, tagged with where it belongs.
//
// The rest of this package tests behaviour. This file exists for a blunter
// reason: fifty of the eighty commands this binary offers were never executed
// by any check. A bench with a hundred assertions that never runs `cluster
// backup create` has not tested `cluster backup create`.
//
// Every entry carries four dimensions, because "run every command" is not
// enough on its own — the same command means different things in different
// places:
//
//	env    which far end it needs: local, sim, flex, kind
//	stage  where it sits in the journey: init → configure → validate →
//	       generate → deploy → operate → teardown
//	task   what a person is doing: cluster, secrets, settings, shell
//	needs  what has to be present first: a tool, a credential, the gate
//
// A command whose env or needs are not satisfied is skipped with that reason
// recorded, never silently passed.

// Stage is where a command sits in the cluster journey.
type Stage string

const (
	StageInit      Stage = "init"
	StageConfigure Stage = "configure"
	StageValidate  Stage = "validate"
	StageGenerate  Stage = "generate"
	StageDeploy    Stage = "deploy"
	StageOperate   Stage = "operate"
	StageTeardown  Stage = "teardown"
)

// invocation is one ready-to-run command with everything needed to judge it.
type invocation struct {
	// Args is exactly what is passed to the binary.
	Args []string
	// Intent says what a reader should understand this run to be probing.
	Intent string
	// Stage, Task and Envs place the command.
	Stage Stage
	Task  string
	Envs  []string
	// Needs are prerequisite ids that must be present, from prerequisites.yaml.
	Needs []string
	// ExpectFailure marks invocations that should fail: a missing required
	// flag, a cluster that does not exist. There, a clean failure is the pass.
	ExpectFailure bool
	// JSON marks commands whose output should parse with --output json.
	JSON bool
	// Mutating marks anything that could reach beyond the sandbox.
	Mutating bool
}

// allEnvs is the default: a command that needs no particular far end.
var allEnvs = []string{"local", "sim", "flex", "kind"}

// cloudEnvs is for commands that only mean something with a provider behind
// them.
var cloudEnvs = []string{"sim", "flex"}

func init() {
	register(
		Check{ID: "coverage-cluster-backup", Name: "Every cluster backup command runs",
			Category: "commands", Environments: everywhere, Fn: coverageFor(clusterBackupCommands)},
		Check{ID: "coverage-cluster-drift", Name: "Every cluster drift command runs",
			Category: "commands", Environments: everywhere, Fn: coverageFor(clusterDriftCommands)},
		Check{ID: "coverage-cluster-import", Name: "Every cluster import command runs",
			Category: "commands", Environments: everywhere, Fn: coverageFor(clusterImportCommands)},
		Check{ID: "coverage-cluster-pool", Name: "Every worker pool command runs",
			Category: "commands", Environments: everywhere, Fn: coverageFor(clusterPoolCommands)},
		Check{ID: "coverage-cluster-service", Name: "Every cluster service command runs",
			Category: "commands", Environments: everywhere, Fn: coverageFor(clusterServiceCommands)},
		Check{ID: "coverage-cluster-misc", Name: "cluster configure and edit run",
			Category: "commands", Environments: everywhere, Fn: coverageFor(clusterMiscCommands)},
		Check{ID: "coverage-secrets", Name: "Every secrets command runs",
			Category: "commands", Environments: everywhere, Fn: coverageFor(secretsCommands)},
		Check{ID: "coverage-secrets-keys", Name: "Every secrets keys command runs",
			Category: "secrets", Environments: everywhere, Fn: coverageFor(secretsKeysCommands)},
		Check{ID: "coverage-settings", Name: "Every settings command runs",
			Category: "configuration", Environments: everywhere, Fn: coverageFor(settingsCommands)},
		Check{ID: "coverage-shell-init", Name: "shell-init produces usable shell code",
			Category: "commands", Environments: everywhere, Fn: checkShellInit},
	)
}

// --- the invocations --------------------------------------------------------

func clusterBackupCommands(reference string) []invocation {
	task, envs := "cluster", allEnvs
	return []invocation{
		{Args: []string{"cluster", "backup"}, Intent: "the group prints its help",
			Stage: StageOperate, Task: task, Envs: envs},
		{Args: []string{"cluster", "backup", "list", reference}, JSON: true,
			Intent: "listing backups of a cluster that has none",
			Stage:  StageOperate, Task: task, Envs: envs},
		{Args: []string{"cluster", "backup", "create", reference},
			Intent: "creating a backup of the fixture",
			Stage:  StageOperate, Task: task, Envs: envs},
		{Args: []string{"cluster", "backup", "delete", reference, "no-such-backup"},
			Intent: "deleting a backup that does not exist", ExpectFailure: true,
			Stage: StageOperate, Task: task, Envs: envs},
		{Args: []string{"cluster", "backup", "restore", reference, "no-such-backup"},
			Intent: "restoring a backup that does not exist", ExpectFailure: true, Mutating: true,
			Stage: StageOperate, Task: task, Envs: envs},
		{Args: []string{"cluster", "backup", "schedule", reference, "--interval", "24h"},
			Intent: "scheduling periodic backups",
			Stage:  StageOperate, Task: task, Envs: envs},
	}
}

func clusterDriftCommands(reference string) []invocation {
	task := "cluster"
	return []invocation{
		{Args: []string{"cluster", "drift"}, Intent: "the group prints its help",
			Stage: StageOperate, Task: task, Envs: allEnvs},
		{Args: []string{"cluster", "drift", "detect", reference}, JSON: true,
			Intent: "detecting drift against a cluster that was never deployed",
			Stage:  StageOperate, Task: task, Envs: allEnvs},
		{Args: []string{"cluster", "drift", "reconcile", reference}, Mutating: true,
			Intent: "reconciling drift back to the configuration",
			Stage:  StageOperate, Task: task, Envs: cloudEnvs, Needs: []string{"kubectl"}},
		{Args: []string{"cluster", "drift", "schedule", reference, "--interval", "24h"},
			Intent: "scheduling drift detection",
			Stage:  StageOperate, Task: task, Envs: allEnvs},
	}
}

func clusterImportCommands(reference string) []invocation {
	task := "cluster"
	return []invocation{
		{Args: []string{"cluster", "import"}, Intent: "the group prints its help",
			Stage: StageInit, Task: task, Envs: allEnvs},
		{Args: []string{"cluster", "import", "scan"}, ExpectFailure: true,
			Intent: "scanning with no repository given",
			Stage:  StageInit, Task: task, Envs: allEnvs, Needs: []string{"git"}},
		{Args: []string{"cluster", "import", "report"}, ExpectFailure: true,
			Intent: "reporting with no artifact to report on",
			Stage:  StageInit, Task: task, Envs: allEnvs},
		{Args: []string{"cluster", "import", "apply"}, ExpectFailure: true, Mutating: true,
			Intent: "applying with no artifact to apply",
			Stage:  StageInit, Task: task, Envs: allEnvs},
	}
}

func clusterPoolCommands(reference string) []invocation {
	task, envs := "cluster", allEnvs
	return []invocation{
		{Args: []string{"cluster", "pool", "list", reference}, JSON: true,
			Intent: "listing the pools of the fixture",
			Stage:  StageConfigure, Task: task, Envs: envs},
		{Args: []string{"cluster", "pool", "add", reference, "--name", "extra", "--count", "2"},
			Intent: "adding a worker pool",
			Stage:  StageConfigure, Task: task, Envs: envs},
		{Args: []string{"cluster", "pool", "update", reference, "extra", "--count", "3"},
			Intent: "updating the pool just added",
			Stage:  StageConfigure, Task: task, Envs: envs},
		{Args: []string{"cluster", "pool", "scale", reference, "extra", "4"},
			Intent: "scaling that pool",
			Stage:  StageConfigure, Task: task, Envs: envs},
		{Args: []string{"cluster", "pool", "remove", reference, "extra"},
			Intent: "removing it again",
			Stage:  StageConfigure, Task: task, Envs: envs},
		{Args: []string{"cluster", "pool", "remove", reference, "never-existed"},
			Intent: "removing a pool that was never there", ExpectFailure: true,
			Stage: StageConfigure, Task: task, Envs: envs},
	}
}

func clusterServiceCommands(reference string) []invocation {
	task, envs := "cluster", allEnvs
	return []invocation{
		{Args: []string{"cluster", "service"}, Intent: "the group prints its help",
			Stage: StageConfigure, Task: task, Envs: envs},
		{Args: []string{"cluster", "service", "status", reference}, JSON: true,
			Intent: "the state of every service",
			Stage:  StageConfigure, Task: task, Envs: envs},
		{Args: []string{"cluster", "service", "options", "loki"},
			Intent: "the options a service accepts",
			Stage:  StageConfigure, Task: task, Envs: envs},
		{Args: []string{"cluster", "service", "enable", reference, "loki"},
			Intent: "enabling a service",
			Stage:  StageConfigure, Task: task, Envs: envs},
		{Args: []string{"cluster", "service", "disable", reference, "loki"},
			Intent: "disabling it again",
			Stage:  StageConfigure, Task: task, Envs: envs},
		{Args: []string{"cluster", "service", "enable", reference, "not-a-real-service"},
			Intent: "enabling a service that does not exist", ExpectFailure: true,
			Stage: StageConfigure, Task: task, Envs: envs},
	}
}

func clusterMiscCommands(reference string) []invocation {
	task, envs := "cluster", allEnvs
	return []invocation{
		// Guided configuration with no terminal must decline, not hang.
		{Args: []string{"cluster", "configure", reference},
			Intent: "guided configuration with no terminal attached",
			Stage:  StageConfigure, Task: task, Envs: envs},
		// edit opens $EDITOR, which the sandbox sets to `true`, so it must return.
		{Args: []string{"cluster", "edit", reference},
			Intent: "opening the configuration in an editor",
			Stage:  StageConfigure, Task: task, Envs: envs},
	}
}

func secretsCommands(reference string) []invocation {
	task, envs := "secrets", allEnvs
	return []invocation{
		{Args: []string{"secrets", "list"}, JSON: true,
			Intent: "listing the secrets of the active cluster",
			Stage:  StageOperate, Task: task, Envs: envs},
		{Args: []string{"secrets", "status"},
			Intent: "the encryption status of the project",
			Stage:  StageOperate, Task: task, Envs: envs},
		{Args: []string{"secrets", "validate"},
			Intent: "validating secrets against the configuration",
			Stage:  StageValidate, Task: task, Envs: envs},
		{Args: []string{"secrets", "sync"},
			Intent: "syncing configuration secrets into manifests",
			Stage:  StageGenerate, Task: task, Envs: envs, Needs: []string{"sops"}},
		{Args: []string{"secrets", "describe", "no-such-secret"}, ExpectFailure: true,
			Intent: "describing a secret that does not exist",
			Stage:  StageOperate, Task: task, Envs: envs},
		{Args: []string{"secrets", "get", "no-such-secret"}, ExpectFailure: true,
			Intent: "downloading a secret that does not exist",
			Stage:  StageOperate, Task: task, Envs: envs},
		{Args: []string{"secrets", "delete", "no-such-secret"}, ExpectFailure: true,
			Intent: "deleting a secret that does not exist",
			Stage:  StageOperate, Task: task, Envs: envs},
		{Args: []string{"secrets", "set", "bench-secret", "bench-value"},
			Intent: "creating a secret",
			Stage:  StageOperate, Task: task, Envs: envs},
		{Args: []string{"secrets", "login"}, ExpectFailure: true,
			Intent: "obtaining a Keystone token with no credentials",
			Stage:  StageOperate, Task: task, Envs: cloudEnvs},
	}
}

func secretsKeysCommands(reference string) []invocation {
	task, envs := "secrets", allEnvs
	return []invocation{
		{Args: []string{"secrets", "keys", "check"},
			Intent: "key expiry status",
			Stage:  StageOperate, Task: task, Envs: envs},
		{Args: []string{"secrets", "keys", "validate"},
			Intent: "validating the Age and SOPS setup",
			Stage:  StageValidate, Task: task, Envs: envs},
		{Args: []string{"secrets", "keys", "backup"},
			Intent: "backing up the key material",
			Stage:  StageOperate, Task: task, Envs: envs},
		{Args: []string{"secrets", "keys", "rotate"}, Mutating: true,
			Intent: "rotating the key and re-encrypting",
			Stage:  StageOperate, Task: task, Envs: envs, Needs: []string{"sops", "age"}},
		{Args: []string{"secrets", "keys", "revoke"}, ExpectFailure: true,
			Intent: "revoking with nothing named to revoke",
			Stage:  StageOperate, Task: task, Envs: envs},
	}
}

func settingsCommands(reference string) []invocation {
	task, envs := "settings", allEnvs
	return []invocation{
		{Args: []string{"settings"}, Intent: "the group prints its help",
			Stage: StageInit, Task: task, Envs: envs},
		{Args: []string{"settings", "path"}, Intent: "where the settings file lives",
			Stage: StageInit, Task: task, Envs: envs},
		{Args: []string{"settings", "view"}, JSON: true, Intent: "the whole settings file",
			Stage: StageInit, Task: task, Envs: envs},
		{Args: []string{"settings", "get", "logging.level"},
			Intent: "one value by dot notation",
			Stage:  StageInit, Task: task, Envs: envs},
		{Args: []string{"settings", "get", "no.such.setting"}, ExpectFailure: true,
			Intent: "a value that does not exist",
			Stage:  StageInit, Task: task, Envs: envs},
		{Args: []string{"settings", "set", "logging.level", "debug"},
			Intent: "changing a value",
			Stage:  StageInit, Task: task, Envs: envs},
		{Args: []string{"settings", "explain"},
			Intent: "how settings affect behaviour",
			Stage:  StageInit, Task: task, Envs: envs},
		{Args: []string{"settings", "explain", "cluster-defaults"},
			Intent: "explaining one section",
			Stage:  StageInit, Task: task, Envs: envs},
		{Args: []string{"settings", "ide"},
			Intent: "generating the schema and editor setup",
			Stage:  StageInit, Task: task, Envs: envs},
		{Args: []string{"settings", "edit"},
			Intent: "opening the settings in an editor",
			Stage:  StageInit, Task: task, Envs: envs},
		// reset goes last: it undoes the set above, and running it earlier
		// would make the other results depend on ordering.
		{Args: []string{"settings", "reset", "--yes"},
			Intent: "resetting settings to defaults",
			Stage:  StageInit, Task: task, Envs: envs},
	}
}

// --- the engine -------------------------------------------------------------

// coverageFor turns a list of invocations into a check.
func coverageFor(build func(reference string) []invocation) func(context.Context, *T) {
	return func(ctx context.Context, t *T) {
		fixture := t.primary()
		reference := fixture.Reference()

		// Every command is handed a credential, so the leak assertion below is
		// real rather than decorative.
		environment := map[string]string{
			"OS_PASSWORD":                      CanaryPassword,
			"OS_APPLICATION_CREDENTIAL_SECRET": CanarySecret,
			"OS_TOKEN":                         CanaryToken,
		}

		for _, entry := range build(reference) {
			label := "opencenter " + strings.Join(entry.Args, " ")
			where := string(entry.Stage) + " · " + entry.Task

			// Not for this far end.
			if len(entry.Envs) > 0 && !contains(entry.Envs, t.Env.ID) {
				t.Note(label, "not applicable in "+t.Env.ID+" ("+where+")")
				continue
			}
			// Would reach beyond the sandbox.
			if entry.Mutating && !t.Env.Mutate {
				t.Note(label, "skipped: can reach beyond the sandbox; needs the mutation gate")
				continue
			}
			// Something it depends on is absent.
			if missing := missingTools(entry.Needs); len(missing) > 0 {
				t.Note(label, "skipped: needs "+strings.Join(missing, ", ")+" ("+where+")")
				continue
			}

			result := t.RunWith(cli.RunOptions{Env: environment, Timeout: 25 * time.Second}, entry.Args...)

			t.Assertf(label+" answers", !result.TimedOut,
				"did not return within 25s — %s", entry.Intent)
			if result.TimedOut {
				continue
			}

			t.Assert(label+" does not panic",
				!containsAny(result.Output(), "goroutine ", "runtime.gopanic", "nil pointer dereference"),
				firstLine(result.Output()))

			t.Assert(label+" produces output", strings.TrimSpace(result.Output()) != "",
				"no output at all, which tells a person nothing")

			if !result.OK() {
				t.Assertf(label+" explains its failure", strings.TrimSpace(result.Stderr) != "",
					"exit %d with an empty stderr", result.ExitCode)
			}

			if entry.ExpectFailure {
				t.Assertf(label+" fails as it should", !result.OK(),
					"exit 0 — %s should not have succeeded", entry.Intent)
			}

			for _, secret := range Canaries() {
				t.Assert(label+" leaks no credential", !strings.Contains(result.Output(), secret),
					"a value supplied as a credential appeared in the output")
			}

			if entry.JSON {
				jsonArgs := append(append([]string{}, entry.Args...), "--output", "json")
				jsonResult := t.RunWith(cli.RunOptions{Env: environment, Timeout: 25 * time.Second}, jsonArgs...)
				trimmed := strings.TrimSpace(jsonResult.Stdout)
				if trimmed == "" {
					t.Note(label+" --output json", "produced nothing on stdout")
				} else {
					var parsed any
					err := json.Unmarshal([]byte(trimmed), &parsed)
					t.Assertf(label+" --output json parses", err == nil,
						"%v — got %s", err, firstLine(trimmed))
				}
			}
		}
	}
}

// missingTools reports which of the named tools are not on PATH. The ids match
// prerequisites.yaml, so a skip reason names the same thing the console does.
func missingTools(needs []string) []string {
	var missing []string
	for _, need := range needs {
		binary := need
		switch need {
		case "container-runtime":
			binary = "docker"
		}
		if _, err := exec.LookPath(binary); err != nil {
			missing = append(missing, need)
		}
	}
	return missing
}

func contains(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}

// checkShellInit is separate because what it emits has to be usable by a
// shell, which is a sharper question than "did it exit 0".
func checkShellInit(ctx context.Context, t *T) {
	result := t.Run("shell-init")
	t.Requiref("shell-init runs", result.OK(),
		"exit %d: %s", result.ExitCode, firstLine(result.Output()))

	script := result.Stdout
	t.Assert("it emits something", strings.TrimSpace(script) != "", "nothing to eval")
	t.Assert("it looks like shell code",
		containsAny(script, "export", "function", "()", "alias", "OPENCENTER"),
		firstLine(script))
	t.Assert("the script goes to stdout, not stderr", len(script) > len(result.Stderr),
		"a script on stderr cannot be evaluated")
	t.Assert("it has no unbalanced double quote", strings.Count(script, `"`)%2 == 0,
		"an odd number of double quotes would break eval \"$(opencenter shell-init)\"")
	t.Assert("it leaks no credential",
		!containsAny(script, CanaryPassword, CanarySecret, CanaryToken), "")
}
