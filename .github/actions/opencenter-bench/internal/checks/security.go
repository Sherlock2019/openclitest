package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() {
	register(
		Check{
			ID:           "security-no-secret-leak",
			Name:         "Injected secrets never reach output, logs or files",
			Category:     "security",
			Environments: everywhere,
			Fn:           checkSecretLeak,
		},
		Check{
			ID:           "security-path-traversal",
			Name:         "Traversal in names and organizations writes nothing outside the config root",
			Category:     "security",
			Environments: everywhere,
			Fn:           checkPathTraversal,
		},
		Check{
			ID:           "security-shell-injection",
			Name:         "Shell metacharacters in arguments execute nothing",
			Category:     "security",
			Environments: everywhere,
			Fn:           checkShellInjection,
		},
		Check{
			ID:           "security-error-safety",
			Name:         "Errors expose no stack traces or internal paths",
			Category:     "security",
			Environments: everywhere,
			Fn:           checkErrorSafety,
		},
		Check{
			ID:           "plugins-verification",
			Name:         "Plugins are discovered, verified, and rejected once modified",
			Category:     "plugins",
			Environments: everywhere,
			Fn:           checkPluginVerification,
		},
		Check{
			ID:           "plugins-no-shadowing",
			Name:         "A plugin cannot take over a built-in command",
			Category:     "plugins",
			Environments: everywhere,
			Fn:           checkPluginShadowing,
		},
		Check{
			ID:           "secrets-round-trip",
			Name:         "Encrypt then decrypt returns the original, and failure is reported",
			Category:     "secrets",
			Environments: everywhere,
			Fn:           checkSecretsRoundTrip,
		},
		Check{
			ID:           "secrets-key-handling",
			Name:         "Generated key material is private and never printed",
			Category:     "secrets",
			Environments: everywhere,
			Fn:           checkSecretsKeys,
		},
	)
}

func checkSecretLeak(ctx context.Context, t *T) {
	const org, name = "leakorg", "leak-kind"
	t.initCluster(name, org, "--type", "kind")

	// Every shape a credential arrives in: cloud password, application
	// credential secret, bearer token, and an Age private key.
	injected := map[string]string{
		"OS_PASSWORD":                      CanaryPassword,
		"OS_APPLICATION_CREDENTIAL_SECRET": CanarySecret,
		"OS_TOKEN":                         CanaryToken,
		"SOPS_AGE_KEY":                     CanaryAgeKey,
		"OPENCLI_GIT_TOKEN":                CanaryToken,
	}

	// Normal, failing and debug paths all get the same treatment: the debug
	// path is where a leak usually happens, and the failing path is where the
	// error formatter is most tempted to print everything it knows.
	invocations := [][]string{
		{"cluster", "list", "--output", "json"},
		{"cluster", "validate", org + "/" + name},
		{"--log-level", "debug", "cluster", "validate", org + "/" + name},
		{"--log-level", "debug", "cluster", "describe", org + "/" + name},
		{"--log-level", "debug", "cluster", "export", org + "/" + name},
		{"--log-level", "debug", "cluster", "doctor", org + "/" + name},
		{"--log-level", "debug", "cluster", "validate", "no-such-cluster"},
	}

	leaked := map[string][]string{}
	for _, args := range invocations {
		result := t.RunWithEnv(injected, args...)
		for _, canary := range Canaries() {
			if strings.Contains(result.Stdout, canary) {
				leaked[canary] = append(leaked[canary], strings.Join(args, " ")+" (stdout)")
			}
			if strings.Contains(result.Stderr, canary) {
				leaked[canary] = append(leaked[canary], strings.Join(args, " ")+" (stderr)")
			}
		}
	}
	t.Assertf("no canary reached stdout or stderr", len(leaked) == 0, "%v", leaked)

	// Then the same sweep over everything on disk. A credential that is absent
	// from the terminal but written into a state file is still a leak.
	var files []string
	_ = filepath.Walk(t.Env.Sandbox.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Size() > 4<<20 {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, canary := range Canaries() {
			if strings.Contains(string(content), canary) {
				relative, _ := filepath.Rel(t.Env.Sandbox.Root, path)
				files = append(files, relative)
				break
			}
		}
		return nil
	})
	t.Assertf("no canary was written to disk", len(files) == 0, "%v", trim(files))
}

func checkPathTraversal(ctx context.Context, t *T) {
	sentinelDir := t.Env.Sandbox.Outside
	before := t.snapshot()

	attempts := [][]string{
		{"cluster", "init", "../escape", "--org", "travorg"},
		{"cluster", "init", "../../../escape", "--org", "travorg"},
		{"cluster", "init", "ok", "--org", "../../escape-org"},
		{"cluster", "init", sentinelDir + "/absolute", "--org", "travorg"},
		{"cluster", "init", "a/b/c", "--org", "travorg"},
		{"cluster", "init", strings.Repeat("x", 400), "--org", "travorg"},
		{"cluster", "init", "esc⁄ape", "--org", "travorg"}, // Unicode fraction slash
	}

	for _, attempt := range attempts {
		args := append(append([]string{}, attempt...), "--no-keygen", "--no-sops-keygen")
		result := t.Run(args...)
		label := strings.Join(attempt[2:4], " ")
		if result.OK() {
			// Accepting a traversal value is only safe if it was neutralised.
			// Note it either way: silently rewriting what a person typed is
			// worth knowing about even when it is not exploitable.
			t.Notef("accepted "+label, "the value was rewritten rather than refused: %s", firstLine(result.Stdout))
		} else {
			t.Assert("rejected "+label, true, firstLine(result.Stderr))
		}
	}

	// The finding that matters: did anything land outside the configuration
	// root, anywhere in the sandbox?
	after := t.snapshot()
	added, _, _ := diffSnapshots(before, after)
	var escaped []string
	for _, path := range added {
		switch {
		case strings.HasPrefix(path, t.Env.Sandbox.ConfigDir),
			strings.HasPrefix(path, t.Env.Sandbox.StateDir),
			strings.HasPrefix(path, t.Env.Sandbox.CacheDir),
			strings.HasPrefix(path, filepath.Join(t.Env.Sandbox.Root, "state")),
			strings.HasPrefix(path, filepath.Join(t.Env.Sandbox.Root, "tmp")),
			strings.HasPrefix(path, t.Env.Sandbox.Home):
			continue
		default:
			escaped = append(escaped, path)
		}
	}
	t.Assertf("nothing was written outside the configuration and state roots",
		len(escaped) == 0, "%v", trim(escaped))

	entries, _ := os.ReadDir(sentinelDir)
	t.Assertf("the directory beside the sandbox is still empty", len(entries) == 0,
		"%d entries appeared in %s", len(entries), sentinelDir)
}

func checkShellInjection(ctx context.Context, t *T) {
	// A file that only exists if a payload ran. Nothing legitimate creates it.
	sentinel := filepath.Join(t.Env.Sandbox.Outside, "pwned")

	payloads := []string{
		"inj;touch " + sentinel,
		"inj&&touch " + sentinel,
		"inj|touch " + sentinel,
		"inj$(touch " + sentinel + ")",
		"inj`touch " + sentinel + "`",
		"inj\ntouch " + sentinel,
		"inj' ; touch " + sentinel + " ; '",
		"inj\" ; touch " + sentinel + " ; \"",
	}

	for i, payload := range payloads {
		result := t.Run("cluster", "init", payload, "--org", "injorg", "--no-keygen", "--no-sops-keygen")
		t.Assertf(fmt.Sprintf("payload %d is not executed as a shell command", i+1),
			!fileExists(sentinel), "the sentinel %s exists after %q", sentinel, payload)
		t.Assert(fmt.Sprintf("payload %d does not crash the CLI", i+1),
			!containsAny(result.Output(), "goroutine ", "runtime.gopanic"), firstLine(result.Output()))

		// The same values as an organization, which takes a different path
		// through name validation.
		orgResult := t.Run("cluster", "init", "ok", "--org", payload, "--no-keygen", "--no-sops-keygen")
		t.Assertf(fmt.Sprintf("payload %d in --org is not executed", i+1),
			!fileExists(sentinel), "the sentinel exists after --org %q", payload)
		_ = orgResult
	}

	// And as a value that reaches an external tool rather than a filename.
	t.Run("cluster", "set", "injorg/nothing", "opencenter.meta.env=$(touch "+sentinel+")")
	t.Assert("an injected value in cluster set is not executed", !fileExists(sentinel),
		"the sentinel exists after cluster set")
}

func checkErrorSafety(ctx context.Context, t *T) {
	failures := [][]string{
		{"cluster", "validate", "no-such-cluster"},
		{"cluster", "export", "no-such-cluster"},
		{"cluster", "use", "no-such-org/no-such-cluster"},
		{"cluster", "generate", "no-such-cluster"},
		{"definitely-not-a-command"},
	}

	for _, args := range failures {
		result := t.Run(args...)
		label := strings.Join(args, " ")
		t.Assert(label+" prints no stack trace",
			!containsAny(result.Output(), "goroutine ", "runtime.gopanic", "/usr/local/go/src"),
			firstLine(result.Output()))
		t.Assert(label+" does not expose the build machine",
			!containsAny(result.Output(), "/root/go/", "/home/runner/"), firstLine(result.Output()))
		t.Assertf(label+" fails with a non-zero exit code", !result.OK(), "exit %d", result.ExitCode)
	}
}

func checkPluginVerification(ctx context.Context, t *T) {
	const pluginName = "benchplug"
	script := "#!/usr/bin/env bash\necho benchplug-ran\n"
	path, err := t.Env.Sandbox.WriteFakeTool("opencenter-"+pluginName, script)
	if err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	list := t.Run("plugins", "list")
	t.Require("plugins list succeeds", list.OK(), firstLine(list.Output()))
	t.Assert("the plugin is discovered", strings.Contains(list.Stdout, pluginName),
		firstLine(list.Stdout))
	t.Assert("an unregistered plugin is reported as unverified",
		strings.Contains(list.Stdout, "unverified"), firstLine(list.Stdout))

	run := t.Run(pluginName)
	t.Assert("running an unverified plugin warns about it",
		containsAny(run.Output(), "unverified", "checksum"), firstLine(run.Output()))

	// Register the correct checksum and confirm it is accepted.
	checksums := filepath.Join(t.Env.Sandbox.ConfigDir, "plugins", "checksums.txt")
	if err := os.MkdirAll(filepath.Dir(checksums), 0o700); err != nil {
		t.Fatalf("mkdir plugins dir: %v", err)
	}
	if err := writeChecksum(checksums, path); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	verified := t.Run("plugins", "list")
	t.Assert("a plugin with a matching checksum is verified",
		strings.Contains(verified.Stdout, "verified") && !strings.Contains(verified.Stdout, pluginName+"\tunverified"),
		firstLine(verified.Stdout))

	// Now modify the binary behind the recorded checksum. This is the case the
	// verification exists for.
	if _, err := t.Env.Sandbox.WriteFakeTool("opencenter-"+pluginName,
		"#!/usr/bin/env bash\necho tampered\n"); err != nil {
		t.Fatalf("tamper with plugin: %v", err)
	}

	tampered := t.Run("plugins", "list")
	t.Assert("a modified plugin is reported as a checksum mismatch",
		containsAny(tampered.Stdout, "mismatch", "checksum_mismatch", "invalid"),
		firstLine(tampered.Stdout))

	execution := t.Run(pluginName)
	t.Assertf("a modified plugin is not executed silently",
		!execution.OK() || containsAny(execution.Output(), "mismatch", "checksum", "refus"),
		"exit %d, output %q", execution.ExitCode, firstLine(execution.Output()))
}

func checkPluginShadowing(ctx context.Context, t *T) {
	if _, err := t.Env.Sandbox.WriteFakeTool("opencenter-cluster",
		"#!/usr/bin/env bash\necho PLUGIN-TOOK-OVER-CLUSTER\n"); err != nil {
		t.Fatalf("write shadowing plugin: %v", err)
	}
	defer os.Remove(filepath.Join(t.Env.Sandbox.FakeBin, "opencenter-cluster"))

	result := t.Run("cluster", "list")
	t.Assert("the built-in command still runs",
		!strings.Contains(result.Stdout, "PLUGIN-TOOK-OVER-CLUSTER"),
		"a plugin replaced a built-in command: "+firstLine(result.Stdout))
	t.Assertf("the built-in command still succeeds", result.OK(), "exit %d", result.ExitCode)

	// The same plugin reachable from two directories on PATH is the other
	// collision case: it must be listed once, not once per copy.
	second := filepath.Join(t.Env.Sandbox.Root, "fakebin2")
	if err := os.MkdirAll(second, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := t.Env.Sandbox.WriteFakeTool("opencenter-dupe", "#!/usr/bin/env bash\necho one\n"); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(second, "opencenter-dupe"),
		[]byte("#!/usr/bin/env bash\necho two\n"), 0o700); err != nil {
		t.Fatalf("write second copy: %v", err)
	}

	list := t.RunWithEnv(map[string]string{
		"PATH": second + string(os.PathListSeparator) + t.Env.Sandbox.PathValue(),
	}, "plugins", "list")
	t.Require("plugins list succeeds with two copies on PATH", list.OK(), firstLine(list.Output()))

	occurrences := countPluginEntries(list.Stdout, "dupe")
	t.Assertf("each plugin name is listed once", occurrences <= 1,
		"dupe appears %d times, so the same command has two implementations", occurrences)
}

func checkSecretsRoundTrip(ctx context.Context, t *T) {
	const plaintext = "CANARY_ROUNDTRIP_PLAINTEXT_4c81de"

	workdir := filepath.Join(t.Env.Sandbox.Root, "secrets-roundtrip")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Generating from inside the working directory also writes the .sops.yaml
	// that encrypt matches files against, so the directory is a realistic one.
	generate := t.RunWith(runIn(workdir), "secrets", "keys", "generate")
	t.Require("an Age key can be generated", generate.OK(), firstLine(generate.Output()))
	t.Assert("the private key is not printed", !strings.Contains(generate.Stdout, "AGE-SECRET-KEY"),
		"the generated private key appeared on stdout")

	target := filepath.Join(workdir, "secret.yaml")
	original := "apiVersion: v1\nkind: Secret\nstringData:\n  password: " + plaintext + "\n"
	if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// First, the failure path, made deterministic: a sops that always fails.
	// Whether sops is installed on this machine or not, encrypting nothing
	// successfully must not be reported as success.
	restore, err := t.Env.Sandbox.WriteFakeTool("sops", "#!/usr/bin/env bash\necho 'sops: refusing' >&2\nexit 1\n")
	if err != nil {
		t.Fatalf("write fake sops: %v", err)
	}
	broken := t.RunWith(runIn(workdir), "secrets", "encrypt", "--path", workdir)
	stillPlain, readErr := os.ReadFile(target)
	t.Require("the file survives a failed encryption", readErr == nil, fmt.Sprint(readErr))
	t.Assert("a failed encryption really did not encrypt",
		strings.Contains(string(stillPlain), plaintext), "the file changed despite sops failing")
	t.Assertf("an encryption that processed no files does not report success", !broken.OK(),
		"exit %d — output said %q", broken.ExitCode, firstLine(broken.Stdout))
	t.Assert("the failure is explained",
		containsAny(broken.Output(), "fail", "error", "sops"), firstLine(broken.Output()))
	_ = os.Remove(restore)

	// Then the real round trip, which needs a real sops.
	if _, err := exec.LookPath("sops"); err != nil {
		t.Note("the encrypt/decrypt round trip was not attempted",
			"sops is not installed, so only the failure path above was answered")
		return
	}

	encrypt := t.RunWith(runIn(workdir), "secrets", "encrypt", "--path", workdir)
	t.Assertf("encrypt succeeds", encrypt.OK(), "exit %d: %s", encrypt.ExitCode, firstLine(encrypt.Output()))

	encrypted, err := os.ReadFile(target)
	t.Require("the file exists after encrypt", err == nil, fmt.Sprint(err))
	t.Assert("the plaintext is gone from the encrypted file",
		!strings.Contains(string(encrypted), plaintext), "the secret survived encryption in clear text")
	t.Assert("the encrypted file is recognisably SOPS output",
		containsAny(string(encrypted), "sops", "ENC["), firstLine(string(encrypted)))

	decrypt := t.RunWith(runIn(workdir), "secrets", "decrypt", "--path", workdir)
	t.Assertf("decrypt succeeds", decrypt.OK(), "exit %d: %s", decrypt.ExitCode, firstLine(decrypt.Output()))

	restored, err := os.ReadFile(target)
	t.Require("the file exists after decrypt", err == nil, fmt.Sprint(err))
	t.Assert("decrypt returns the original secret", strings.Contains(string(restored), plaintext),
		"the round trip did not restore the plaintext")
}

func checkSecretsKeys(ctx context.Context, t *T) {
	generate := t.Run("secrets", "keys", "generate")
	t.Require("key generation succeeds", generate.OK(), firstLine(generate.Output()))

	// Find whatever it wrote and check nobody else can read it.
	var keyFiles []string
	_ = filepath.Walk(t.Env.Sandbox.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(path), "keys.txt") || strings.HasSuffix(path, ".agekey") {
			keyFiles = append(keyFiles, path)
		}
		return nil
	})
	t.Assertf("a key file was created", len(keyFiles) > 0, "no key file found under the sandbox")

	for _, path := range keyFiles {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		t.Assertf(filepath.Base(path)+" is private", info.Mode().Perm()&0o077 == 0,
			"%s has mode %04o", path, info.Mode().Perm())

		content, err := os.ReadFile(path)
		if err == nil {
			t.Assert(filepath.Base(path)+" holds a real key",
				strings.Contains(string(content), "AGE-SECRET-KEY"), "no Age key material in the file")
			// The private half must not have been echoed anywhere.
			for _, line := range strings.Split(string(content), "\n") {
				if strings.HasPrefix(line, "AGE-SECRET-KEY") {
					t.Assert("the private key was not printed to the terminal",
						!strings.Contains(generate.Output(), line), "the private key appeared in command output")
				}
			}
		}
	}

	// Running it again must not silently destroy the existing key.
	before, _ := os.ReadFile(firstOr(keyFiles, ""))
	again := t.Run("secrets", "keys", "generate")
	after, _ := os.ReadFile(firstOr(keyFiles, ""))
	if len(before) > 0 {
		t.Assert("a second generate does not overwrite an existing key without saying so",
			string(before) == string(after) || containsAny(again.Output(), "exists", "rotate", "overwrit", "backup"),
			"the key changed with no warning: "+firstLine(again.Output()))
	}
}

// --- helpers ----------------------------------------------------------------

// countPluginEntries counts rows of `plugins list` whose name column is the
// one asked about. Counting substring matches would also count the path the
// plugin was found at, and report every plugin twice.
func countPluginEntries(output, name string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == name {
			count++
		}
	}
	return count
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func firstOr(items []string, fallback string) string {
	if len(items) == 0 {
		return fallback
	}
	return items[0]
}

func writeChecksum(checksumFile, binary string) error {
	content, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	line := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(binary))
	return os.WriteFile(checksumFile, []byte(line), 0o600)
}
