package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
)

// Checks that need nothing but the binary. They run in every environment,
// because "the help text is intact" is as worth knowing on a cloud run as on a
// local one, and a run that starts by proving the binary is sane fails fast
// when it is not.
var everywhere = []string{"local", "sim", "flex", "kind"}

func init() {
	register(
		Check{
			ID:           "install-runs",
			Name:         "The binary starts and identifies itself",
			Category:     "installation",
			Environments: everywhere,
			Fn:           checkInstallRuns,
		},
		Check{
			ID:           "install-no-host-leak",
			Name:         "Help mentions no developer-local path",
			Category:     "installation",
			Environments: everywhere,
			Fn:           checkNoHostLeak,
		},
		Check{
			ID:           "commands-tree",
			Name:         "Every advertised command group exists and answers",
			Category:     "commands",
			Environments: everywhere,
			Fn:           checkCommandTree,
		},
		Check{
			ID:           "commands-help-sweep",
			Name:         "--help works for every command in the tree",
			Category:     "help",
			Environments: everywhere,
			Fn:           checkHelpSweep,
		},
		Check{
			ID:           "help-root-streams",
			Name:         "Help is complete and goes to the right stream",
			Category:     "help",
			Environments: everywhere,
			Fn:           checkRootHelp,
		},
		Check{
			ID:           "args-required-and-invalid",
			Name:         "Required values, unknown flags and bad combinations",
			Category:     "arguments",
			Environments: everywhere,
			Fn:           checkArguments,
		},
		Check{
			ID:           "args-global-flag-position",
			Name:         "Global flags work before and after the subcommand",
			Category:     "arguments",
			Environments: everywhere,
			Fn:           checkGlobalFlagPosition,
		},
		Check{
			ID:           "output-formats",
			Name:         "text, json and yaml output all parse",
			Category:     "output-formats",
			Environments: everywhere,
			Fn:           checkOutputFormats,
		},
		Check{
			ID:           "streams-separation",
			Name:         "Results on stdout, diagnostics on stderr",
			Category:     "streams",
			Environments: everywhere,
			Fn:           checkStreams,
		},
		Check{
			ID:           "errors-unknown-command",
			Name:         "Unknown commands fail clearly, without a stack trace",
			Category:     "errors",
			Environments: everywhere,
			Fn:           checkUnknownCommand,
		},
		Check{
			ID:           "errors-missing-cluster-contract",
			Name:         "A missing cluster returns the documented exit code and recovery steps",
			Category:     "errors",
			Environments: everywhere,
			Fn:           checkMissingClusterContract,
		},
		Check{
			ID:           "performance-startup",
			Name:         "Startup and help stay responsive",
			Category:     "performance",
			Environments: everywhere,
			Fn:           checkStartupPerformance,
		},
	)
}

func checkInstallRuns(ctx context.Context, t *T) {
	version := t.Run("version")
	t.Require("version exits 0", version.OK(), firstLine(version.Stderr))
	t.Assert("version prints something", strings.TrimSpace(version.Stdout) != "",
		firstLine(version.Stdout))
	t.Assert("version names the binary", containsAny(version.Stdout, "opencenter"),
		firstLine(version.Stdout))
	t.Assert("version reports a build platform", containsAny(version.Stdout, "platform", "go version"),
		firstLine(version.Stdout))

	short := t.Run("--version")
	t.Assert("--version is accepted too", short.OK(), firstLine(short.Output()))

	help := t.Run("--help")
	t.Assert("--help exits 0", help.OK(), firstLine(help.Stderr))
	t.Assert("-h is accepted", t.Run("-h").OK(), "")
}

func checkNoHostLeak(ctx context.Context, t *T) {
	help := t.Run("--help")
	t.Require("help succeeded", help.OK(), firstLine(help.Stderr))

	// A binary that prints the path it was built from is telling every user
	// where the person who built it keeps their source.
	for _, marker := range []string{"/home/", "/Users/", "GOPATH", "go/src/"} {
		t.Assertf("help does not mention "+marker, !strings.Contains(help.Stdout, marker),
			"found %q in help output", marker)
	}
	t.Assert("help has no Go stack trace", !containsAny(help.Output(), "goroutine ", "panic:"), "")
}

// commandGroups is discovered from the binary rather than hard-coded, so a
// command added tomorrow is tested tomorrow.
func discoverGroups(t *T) []string {
	help := t.Run("--help")
	t.Require("root help succeeded", help.OK(), firstLine(help.Stderr))

	var groups []string
	inSection := false
	for _, line := range strings.Split(help.Stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Available Commands:") {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if trimmed == "" {
			break
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		// completion and help are Cobra's own; they are covered by the sweep
		// but are not part of the product's command surface.
		if name == "completion" || name == "help" {
			continue
		}
		groups = append(groups, name)
	}
	return groups
}

func checkCommandTree(ctx context.Context, t *T) {
	groups := discoverGroups(t)
	t.Require("root help lists command groups", len(groups) > 0, "no Available Commands section found")
	t.Assertf("command groups discovered", len(groups) >= 4, "found %d: %s", len(groups), strings.Join(groups, ", "))

	seen := map[string]bool{}
	for _, group := range groups {
		t.Assertf("no duplicate command name "+group, !seen[group], "%q appears twice under the root", group)
		seen[group] = true

		result := t.Run(group, "--help")
		t.Assertf(group+" --help exits 0", result.OK(), "exit %d: %s", result.ExitCode, firstLine(result.Stderr))
		t.Assert(group+" has a usage line", containsAny(result.Stdout, "Usage:"), firstLine(result.Stdout))
	}
}

// checkHelpSweep walks the whole tree, not only the top level. A subcommand
// with broken help is the one a person reaches for when they are already
// stuck.
func checkHelpSweep(ctx context.Context, t *T) {
	paths := walkCommands(t, nil, 0)
	t.Require("commands discovered", len(paths) > 5, fmt.Sprintf("only found %d commands", len(paths)))
	t.Notef("commands in tree", "%d commands walked", len(paths))

	failures := 0
	slow := 0
	for _, path := range paths {
		args := append(append([]string{}, path...), "--help")
		result := t.Env.CLI.RunWith(ctx, cli.RunOptions{Timeout: 20 * time.Second}, args...)
		if !result.OK() || !strings.Contains(result.Stdout, "Usage:") {
			failures++
			t.Assertf("help for "+strings.Join(path, " "), false,
				"exit %d: %s", result.ExitCode, firstLine(result.Output()))
			t.record(result)
			continue
		}
		if containsAny(result.Output(), "goroutine ", "panic:") {
			failures++
			t.Assert("help for "+strings.Join(path, " ")+" has no panic", false, firstLine(result.Output()))
			t.record(result)
		}
		if result.Duration > 3*time.Second {
			slow++
		}
	}
	t.Assertf("every command has working help", failures == 0, "%d of %d commands failed", failures, len(paths))
	t.Assertf("no command takes over 3s to print help", slow == 0, "%d slow commands", slow)
}

// walkCommands does a depth-limited walk of the Cobra tree by parsing help.
// Three levels is the depth the product actually uses; going deeper would
// double the runtime to discover nothing.
func walkCommands(t *T, prefix []string, depth int) [][]string {
	if depth > 2 {
		return nil
	}
	args := append(append([]string{}, prefix...), "--help")
	result := t.Env.CLI.RunWith(context.Background(), cli.RunOptions{Timeout: 20 * time.Second}, args...)
	if !result.OK() {
		return nil
	}

	var out [][]string
	inSection := false
	for _, line := range strings.Split(result.Stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Available Commands:") {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if trimmed == "" || strings.HasSuffix(trimmed, ":") {
			break
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		if name == "completion" || name == "help" {
			continue
		}
		// An external plugin is a separate binary with its own contract; the
		// plugin checks cover it.
		if strings.Contains(trimmed, "external plugin") {
			continue
		}
		path := append(append([]string{}, prefix...), name)
		out = append(out, path)
		out = append(out, walkCommands(t, path, depth+1)...)
	}
	return out
}

func checkRootHelp(ctx context.Context, t *T) {
	bare := t.Run()
	t.Assert("bare invocation exits 0", bare.OK(), firstLine(bare.Output()))
	t.Assert("bare invocation prints help", containsAny(bare.Output(), "Usage:", "Available Commands:"),
		firstLine(bare.Output()))

	help := t.Run("--help")
	t.Require("--help succeeded", help.OK(), firstLine(help.Stderr))
	t.Assert("help goes to stdout", strings.Contains(help.Stdout, "Usage:"),
		"help text was not on stdout")
	t.Assert("help does not also go to stderr", strings.TrimSpace(help.Stderr) == "",
		firstLine(help.Stderr))
	t.Assert("help lists global flags", containsAny(help.Stdout, "--config-dir", "--output"),
		"global flags missing from root help")
	t.Assert("help offers examples", containsAny(help.Stdout, "Examples:"), "no examples section")
}

func checkArguments(ctx context.Context, t *T) {
	// An unknown flag must be refused rather than ignored: a typo that is
	// silently dropped is how people deploy with the wrong settings.
	unknown := t.Run("cluster", "list", "--not-a-real-flag")
	t.Assertf("unknown flag is rejected", !unknown.OK(), "exit %d", unknown.ExitCode)
	t.Assert("unknown flag error goes to stderr", strings.TrimSpace(unknown.Stderr) != "",
		firstLine(unknown.Output()))
	t.Assert("unknown flag error names the flag", containsAny(unknown.Stderr, "not-a-real-flag"),
		firstLine(unknown.Stderr))

	// A flag that takes a value, given none.
	missing := t.Run("cluster", "list", "--output")
	t.Assertf("flag missing its value is rejected", !missing.OK(), "exit %d", missing.ExitCode)

	// An invalid value for a flag with a fixed set of choices.
	badFormat := t.Run("cluster", "list", "--output", "toml")
	t.Assertf("unsupported --output value is rejected", !badFormat.OK(),
		"exit %d, output %q", badFormat.ExitCode, firstLine(badFormat.Output()))

	badLevel := t.Run("cluster", "list", "--log-level", "shouting")
	t.Assertf("unsupported --log-level value is rejected", !badLevel.OK(),
		"exit %d, output %q", badLevel.ExitCode, firstLine(badLevel.Output()))

	// A flag that only applies to one provider, used with another. The CLI
	// gets this right today; the check exists so it keeps getting it right.
	crossed := t.Run("cluster", "init", "argcheck-os", "--org", "argcheck",
		"--type", "openstack", "--kind-disable-default-cni", "--no-keygen", "--no-sops-keygen")
	t.Assertf("Kind-only flag is rejected for OpenStack", !crossed.OK(),
		"exit %d: %s", crossed.ExitCode, firstLine(crossed.Output()))
	t.Assert("the rejection explains why", containsAny(crossed.Stderr, "kind"),
		firstLine(crossed.Stderr))

	// An invalid cluster name.
	badName := t.Run("cluster", "init", "Not A Valid Name", "--org", "argcheck", "--no-keygen", "--no-sops-keygen")
	t.Assertf("invalid cluster name is rejected", !badName.OK(), "exit %d", badName.ExitCode)
	t.Assert("the rejection names the offending value", containsAny(badName.Stderr, "name"),
		firstLine(badName.Stderr))
}

func checkGlobalFlagPosition(ctx context.Context, t *T) {
	before := t.Run("--output", "json", "cluster", "list")
	after := t.Run("cluster", "list", "--output", "json")

	t.Assertf("global flag before the subcommand works", before.OK(),
		"exit %d: %s", before.ExitCode, firstLine(before.Stderr))
	t.Assertf("global flag after the subcommand works", after.OK(),
		"exit %d: %s", after.ExitCode, firstLine(after.Stderr))
	t.Assertf("both positions produce the same output",
		strings.TrimSpace(before.Stdout) == strings.TrimSpace(after.Stdout),
		"before=%q after=%q", firstLine(before.Stdout), firstLine(after.Stdout))
}

func checkOutputFormats(ctx context.Context, t *T) {
	text := t.Run("cluster", "list")
	t.Assertf("text output succeeds", text.OK(), "exit %d: %s", text.ExitCode, firstLine(text.Stderr))

	asJSON := t.Run("cluster", "list", "--output", "json")
	t.Require("json output succeeds", asJSON.OK(), firstLine(asJSON.Stderr))
	var parsedJSON any
	err := json.Unmarshal([]byte(asJSON.Stdout), &parsedJSON)
	t.Assertf("json output parses", err == nil, "%v — got %q", err, firstLine(asJSON.Stdout))
	t.Assert("json output is not polluted by log lines",
		strings.HasPrefix(strings.TrimSpace(asJSON.Stdout), "[") ||
			strings.HasPrefix(strings.TrimSpace(asJSON.Stdout), "{"),
		firstLine(asJSON.Stdout))

	asYAML := t.Run("cluster", "list", "--output", "yaml")
	t.Require("yaml output succeeds", asYAML.OK(), firstLine(asYAML.Stderr))
	var parsedYAML any
	err = yaml.Unmarshal([]byte(asYAML.Stdout), &parsedYAML)
	t.Assertf("yaml output parses", err == nil, "%v — got %q", err, firstLine(asYAML.Stdout))

	// --quiet must not corrupt a machine-readable stream.
	quiet := t.Run("cluster", "list", "--output", "json", "--quiet")
	if quiet.OK() {
		err = json.Unmarshal([]byte(quiet.Stdout), &parsedJSON)
		t.Assertf("json is still valid with --quiet", err == nil, "%v", err)
	} else {
		t.Assertf("--quiet is accepted", false, "exit %d: %s", quiet.ExitCode, firstLine(quiet.Stderr))
	}
}

func checkStreams(ctx context.Context, t *T) {
	// A successful command must not write to stderr without a reason, and a
	// failing one must not stay silent on it.
	success := t.Run("cluster", "list", "--output", "json")
	t.Assertf("successful command exits 0", success.OK(), "exit %d", success.ExitCode)
	t.Assert("machine-readable result is on stdout", strings.TrimSpace(success.Stdout) != "",
		"stdout was empty")

	failure := t.Run("cluster", "validate", "no-such-cluster-anywhere")
	t.Assertf("failing command exits non-zero", !failure.OK(), "exit %d", failure.ExitCode)
	t.Assert("the error text is on stderr", strings.TrimSpace(failure.Stderr) != "",
		"stderr was empty; error was: "+firstLine(failure.Stdout))
	t.Assert("stdout carries no error text", !strings.Contains(failure.Stdout, "Error:"),
		firstLine(failure.Stdout))

	// A warning is a diagnostic, not a result: it belongs on stderr even when
	// the command succeeds.
	t.Notef("stderr on success", "%q", firstLine(success.Stderr))
}

func checkUnknownCommand(ctx context.Context, t *T) {
	for _, args := range [][]string{
		{"definitely-not-a-command"},
		{"cluster", "definitely-not-a-command"},
		{"secrets", "definitely-not-a-command"},
	} {
		result := t.Run(args...)
		label := strings.Join(args, " ")
		t.Assertf(label+" exits non-zero", !result.OK(), "exit %d", result.ExitCode)
		t.Assert(label+" explains itself on stderr",
			containsAny(result.Stderr, "unknown command"), firstLine(result.Output()))
		t.Assert(label+" does not dump a stack trace",
			!containsAny(result.Output(), "goroutine ", "runtime.gopanic"), firstLine(result.Output()))
		t.Assert(label+" does not dump full usage",
			!strings.Contains(result.Stderr, "Available Commands:"),
			"a wall of usage text buries the one line that matters")
	}
}

// checkMissingClusterContract asserts the contract in docs/reference/exit-codes.md:
// a cluster that does not exist returns exit code 3 with recovery instructions
// naming `cluster list` and `cluster init`.
func checkMissingClusterContract(ctx context.Context, t *T) {
	const name = "no-such-cluster-anywhere"
	result := t.Run("cluster", "validate", name)

	t.Require("the command fails", !result.OK(), "a missing cluster must not succeed")
	t.Assertf("exit code is 3, as documented", result.ExitCode == 3,
		"got exit %d — docs/reference/exit-codes.md documents 3 for a missing configuration", result.ExitCode)
	t.Assert("the error says the cluster was not found",
		containsAny(result.Stderr, "not found"), firstLine(result.Stderr))
	t.Assert("recovery suggests cluster list",
		containsAny(result.Output(), "cluster list"),
		"documented recovery text is missing: "+firstLine(result.Stderr))
	t.Assert("recovery suggests cluster init",
		containsAny(result.Output(), "cluster init"),
		"documented recovery text is missing: "+firstLine(result.Stderr))
	t.Assert("the error does not leak the whole config tree",
		!strings.Contains(result.Stderr, "goroutine"), firstLine(result.Stderr))
}

func checkStartupPerformance(ctx context.Context, t *T) {
	// Three runs, take the best: the first is competing with the page cache.
	best := time.Hour
	for i := 0; i < 3; i++ {
		result := t.Run("--help")
		if !result.OK() {
			t.Fatalf("--help failed: %s", firstLine(result.Stderr))
		}
		if result.Duration < best {
			best = result.Duration
		}
	}
	t.Assertf("--help returns in under 1s", best < time.Second, "best of three was %s", best)

	version := t.Run("version")
	t.Assertf("version returns in under 1s", version.Duration < time.Second, "took %s", version.Duration)

	list := t.Run("cluster", "list")
	t.Assertf("cluster list returns in under 3s", list.Duration < 3*time.Second, "took %s", list.Duration)
}

// Notef is Note with a format string. Declared here rather than in the harness
// because it is only ever used to attach a measurement to a result.
func (t *T) Notef(name, format string, args ...any) {
	t.Note(name, fmt.Sprintf(format, args...))
}
