package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func init() {
	register(
		Check{
			ID:           "results-init-creates-config",
			Name:         "cluster init writes the configuration it says it wrote",
			Category:     "results",
			Environments: everywhere,
			Fn:           checkInitCreatesConfig,
		},
		Check{
			ID:           "config-precedence",
			Name:         "Flag beats environment beats default",
			Category:     "configuration",
			Environments: everywhere,
			Fn:           checkConfigPrecedence,
		},
		Check{
			ID:           "files-permissions",
			Name:         "Configuration and key files are not world readable",
			Category:     "files",
			Environments: everywhere,
			Fn:           checkFilePermissions,
		},
		Check{
			ID:           "files-corrupt-input",
			Name:         "Corrupt, empty and unknown configuration is reported, not swallowed",
			Category:     "files",
			Environments: everywhere,
			Fn:           checkCorruptConfig,
		},
		Check{
			ID:           "idempotency-init-force",
			Name:         "Re-initialising needs --force, and --force works",
			Category:     "idempotency",
			Environments: everywhere,
			Fn:           checkInitIdempotency,
		},
		Check{
			ID:           "idempotency-generate",
			Name:         "Generating twice produces the same repository",
			Category:     "idempotency",
			Environments: everywhere,
			Fn:           checkGenerateIdempotency,
			Slow:         true,
		},
		Check{
			ID:           "dry-run-no-writes",
			Name:         "--dry-run changes nothing on disk",
			Category:     "dry-run",
			Environments: everywhere,
			Fn:           checkDryRun,
		},
		Check{
			ID:           "lifecycle-local",
			Name:         "init, use, validate, generate, describe, status, export",
			Category:     "lifecycle",
			Environments: everywhere,
			Fn:           checkLocalLifecycle,
			Slow:         true,
		},
		Check{
			ID:           "providers-accepted-types",
			Name:         "Each supported provider is accepted, and an unsupported one is refused",
			Category:     "providers",
			Environments: everywhere,
			Fn:           checkProviderTypes,
		},
		Check{
			ID:           "gitops-generated-assets",
			Name:         "Generated manifests are real, parseable YAML in the right place",
			Category:     "gitops",
			Environments: everywhere,
			Fn:           checkGeneratedAssets,
			Slow:         true,
		},
		Check{
			ID:           "large-inputs-many-clusters",
			Name:         "A configuration directory with many clusters still lists quickly",
			Category:     "large-inputs",
			Environments: everywhere,
			Fn:           checkLargeInputs,
			Slow:         true,
		},
		Check{
			ID:           "cross-platform-paths",
			Name:         "Spaces and Unicode in the configuration path survive",
			Category:     "cross-platform",
			Environments: everywhere,
			Fn:           checkAwkwardPaths,
		},
		Check{
			ID:           "upgrade-config-still-readable",
			Name:         "A configuration written earlier is still readable, and migration is idempotent",
			Category:     "upgrade",
			Environments: everywhere,
			Fn:           checkUpgradeCompatibility,
		},
		Check{
			ID:           "cleanup-no-stray-files",
			Name:         "Commands leave no temporary files or stale locks behind",
			Category:     "cleanup",
			Environments: everywhere,
			Fn:           checkCleanup,
		},
	)
}

// initCluster is the setup most checks start from: a cluster that exists, made
// without touching any key material.
func (t *T) initCluster(name, org string, extra ...string) {
	args := append([]string{"cluster", "init", name, "--org", org, "--no-keygen", "--no-sops-keygen"}, extra...)
	result := t.Run(args...)
	t.Require("cluster init "+name+" succeeded", result.OK(),
		fmt.Sprintf("exit %d: %s", result.ExitCode, firstLine(result.Output())))
}

// configPath is where the CLI puts a cluster's configuration.
func (t *T) configPath(org, name string) string {
	return filepath.Join(t.Env.Sandbox.ConfigDir, "clusters", "blueprints", org, name, name+"-config.yaml")
}

// checkInitCreatesConfig is Module 5, and it is where the workflow's primary
// fixture comes from: the configuration every later module works against. A
// failure here leaves nothing for Modules 9, 19, 20 and 22 to test, which is
// why the module is marked blocking.
func checkInitCreatesConfig(ctx context.Context, t *T) {
	fixture := t.primary()
	org, name := fixture.Organization, fixture.Cluster

	path := t.configPath(org, name)
	raw, err := os.ReadFile(path)
	t.Require("the configuration file exists where init said it would", err == nil,
		fmt.Sprintf("%s: %v", path, err))

	var config struct {
		SchemaVersion string `yaml:"schema_version"`
		OpenCenter    struct {
			Meta struct {
				Name         string `yaml:"name"`
				Organization string `yaml:"organization"`
			} `yaml:"meta"`
			Cluster struct {
				Provider string `yaml:"provider"`
			} `yaml:"cluster"`
			Infrastructure struct {
				Provider string `yaml:"provider"`
			} `yaml:"infrastructure"`
		} `yaml:"opencenter"`
	}
	err = yaml.Unmarshal(raw, &config)
	t.Require("the configuration parses as YAML", err == nil, fmt.Sprint(err))

	t.Assertf("schema version is recorded", config.SchemaVersion != "",
		"schema_version was %q", config.SchemaVersion)
	t.Assertf("cluster name is what was asked for", config.OpenCenter.Meta.Name == name,
		"got %q", config.OpenCenter.Meta.Name)
	t.Assertf("organization is what was asked for", config.OpenCenter.Meta.Organization == org,
		"got %q", config.OpenCenter.Meta.Organization)

	// The provider lives in one of two places depending on schema shape; look
	// for the value rather than guessing the key.
	t.Assert("provider is kind", strings.Contains(string(raw), "kind"),
		"no kind provider found in the generated configuration")

	// --no-keygen must mean no keys.
	var keys []string
	_ = filepath.Walk(t.Env.Sandbox.Root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, "keys.txt") || strings.HasSuffix(p, "id_ed25519") || strings.HasSuffix(p, "id_rsa") {
			keys = append(keys, p)
		}
		return nil
	})
	t.Assertf("--no-keygen generated no private keys", len(keys) == 0, "found %v", keys)

	// The cluster it just made must be visible to the command that lists them.
	list := t.Run("cluster", "list", "--output", "json")
	t.Require("cluster list succeeded", list.OK(), firstLine(list.Stderr))
	t.Assertf("the new cluster is listed", strings.Contains(list.Stdout, name),
		"cluster list returned %q", firstLine(list.Stdout))
}

func checkConfigPrecedence(ctx context.Context, t *T) {
	// Two configuration roots, each holding a different cluster. Whichever one
	// the CLI reports tells us which source won.
	flagDir := filepath.Join(t.Env.Sandbox.Root, "precedence-flag")
	envDir := filepath.Join(t.Env.Sandbox.Root, "precedence-env")
	for _, dir := range []string{flagDir, envDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	viaFlag := t.Run("--config-dir", flagDir, "cluster", "init", "from-flag",
		"--org", "prec", "--no-keygen", "--no-sops-keygen")
	t.Require("init into an explicit --config-dir works", viaFlag.OK(), firstLine(viaFlag.Output()))
	t.Assert("--config-dir is honoured", strings.Contains(viaFlag.Stdout, flagDir),
		firstLine(viaFlag.Stdout))

	viaEnv := t.RunWithEnv(map[string]string{"OPENCENTER_CONFIG_DIR": envDir},
		"cluster", "init", "from-env", "--org", "prec", "--no-keygen", "--no-sops-keygen")
	t.Require("init into OPENCENTER_CONFIG_DIR works", viaEnv.OK(), firstLine(viaEnv.Output()))
	t.Assert("the environment variable is honoured", strings.Contains(viaEnv.Stdout, envDir),
		firstLine(viaEnv.Stdout))

	// Both set at once: the flag must win.
	both := t.RunWithEnv(map[string]string{"OPENCENTER_CONFIG_DIR": envDir},
		"--config-dir", flagDir, "cluster", "list", "--output", "json")
	t.Require("listing with both set works", both.OK(), firstLine(both.Stderr))
	t.Assertf("the flag overrides the environment",
		strings.Contains(both.Stdout, "from-flag") && !strings.Contains(both.Stdout, "from-env"),
		"cluster list returned %q", firstLine(both.Stdout))

	// And the default root is untouched by either.
	def := t.Run("cluster", "list", "--output", "json")
	t.Assert("neither leaked into the default configuration root",
		!strings.Contains(def.Stdout, "from-flag") && !strings.Contains(def.Stdout, "from-env"),
		firstLine(def.Stdout))
}

func checkFilePermissions(ctx context.Context, t *T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions do not apply on this platform")
	}
	const org, name = "permorg", "perm-kind"
	t.initCluster(name, org, "--type", "kind")

	path := t.configPath(org, name)
	info, err := os.Stat(path)
	t.Require("the configuration file exists", err == nil, fmt.Sprint(err))
	mode := info.Mode().Perm()
	t.Assertf("configuration is not group or world readable", mode&0o077 == 0,
		"mode is %04o", mode)

	// Then everything that actually holds key material. Filenames are a poor
	// guide — a manifest called keycloak.yaml contains no key, and a script
	// called scan-secrets holds no secret — so the contents decide, with a
	// short list of names whose meaning is unambiguous.
	var loose []string
	_ = filepath.Walk(t.Env.Sandbox.Root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Size() > 1<<20 {
			return nil
		}
		if !holdsKeyMaterial(p) {
			return nil
		}
		if info.Mode().Perm()&0o077 != 0 {
			loose = append(loose, fmt.Sprintf("%s (%04o)", p, info.Mode().Perm()))
		}
		return nil
	})
	t.Assertf("no file holding key material is readable by others", len(loose) == 0, "%v", trim(loose))
}

// holdsKeyMaterial decides whether a file is one whose permissions matter. It
// reads rather than guesses: a private key is recognisable, and a filename is
// not.
func holdsKeyMaterial(path string) bool {
	switch filepath.Base(path) {
	case "keys.txt", "kubeconfig.yaml", "id_ed25519", "id_rsa", "audit.key":
		return true
	}
	if strings.HasSuffix(path, ".age") || strings.HasSuffix(path, ".agekey") || strings.HasSuffix(path, ".pem") {
		return true
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return containsAny(string(content),
		"AGE-SECRET-KEY", "BEGIN OPENSSH PRIVATE KEY", "BEGIN RSA PRIVATE KEY",
		"BEGIN EC PRIVATE KEY", "BEGIN PRIVATE KEY")
}

func checkCorruptConfig(ctx context.Context, t *T) {
	const org = "corruptorg"

	cases := []struct {
		name    string
		cluster string
		body    string
	}{
		{"corrupt YAML", "corrupt-yaml", "opencenter:\n  meta:\n   name: [unclosed\n"},
		{"an empty file", "empty-config", ""},
		{"an unknown top-level field", "unknown-field", "schema_version: \"2.0\"\nnot_a_real_field: true\n"},
		{"missing required fields", "missing-fields", "schema_version: \"2.0\"\nopencenter: {}\n"},
	}

	for _, testCase := range cases {
		dir := filepath.Join(t.Env.Sandbox.ConfigDir, "clusters", "blueprints", org, testCase.cluster)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		path := filepath.Join(dir, testCase.cluster+"-config.yaml")
		if err := os.WriteFile(path, []byte(testCase.body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		result := t.Run("cluster", "validate", org+"/"+testCase.cluster)
		t.Assertf(testCase.name+" is rejected", !result.OK(),
			"exit %d — a broken configuration must not validate", result.ExitCode)
		t.Assert(testCase.name+" produces an explanation",
			strings.TrimSpace(result.Stderr) != "" || strings.TrimSpace(result.Stdout) != "",
			"the command failed silently")
		t.Assert(testCase.name+" does not panic",
			!containsAny(result.Output(), "goroutine ", "runtime.gopanic", "nil pointer dereference"),
			firstLine(result.Output()))
	}
}

func checkInitIdempotency(ctx context.Context, t *T) {
	const org, name = "idemorg", "idem-kind"
	t.initCluster(name, org, "--type", "kind")

	again := t.Run("cluster", "init", name, "--org", org, "--type", "kind", "--no-keygen", "--no-sops-keygen")
	t.Assertf("a second init without --force is refused", !again.OK(), "exit %d", again.ExitCode)
	t.Assert("the refusal suggests --force", containsAny(again.Output(), "--force", "already exists"),
		firstLine(again.Output()))

	forced := t.Run("cluster", "init", name, "--org", org, "--type", "kind",
		"--no-keygen", "--no-sops-keygen", "--force")
	t.Assertf("--force overwrites", forced.OK(), "exit %d: %s", forced.ExitCode, firstLine(forced.Output()))

	list := t.Run("cluster", "list", "--output", "json")
	var clusters []string
	if err := json.Unmarshal([]byte(list.Stdout), &clusters); err == nil {
		count := 0
		for _, cluster := range clusters {
			if strings.HasSuffix(cluster, "/"+name) {
				count++
			}
		}
		t.Assertf("--force did not create a duplicate entry", count == 1, "found %d entries", count)
	} else {
		t.Assertf("cluster list returns parseable JSON", false, "%v", err)
	}
}

func checkGenerateIdempotency(ctx context.Context, t *T) {
	// Module 19 runs against the fixture Module 5 created, so what is measured
	// is the same repository the lifecycle module goes on to use.
	fixture := t.primary()
	org, name := fixture.Organization, fixture.Cluster

	first := t.Run("cluster", "generate", org+"/"+name)
	t.Require("first generate succeeds", first.OK(),
		fmt.Sprintf("exit %d: %s", first.ExitCode, firstLine(first.Output())))
	before := t.fingerprintGitops(org)

	second := t.Run("cluster", "generate", org+"/"+name)
	t.Require("second generate succeeds", second.OK(),
		fmt.Sprintf("exit %d: %s", second.ExitCode, firstLine(second.Output())))
	after := t.fingerprintGitops(org)

	t.Assertf("the generated repository is byte-identical", before == after,
		"fingerprint changed between runs: %s -> %s", before, after)
	t.Assert("the second run reports the same manifest count",
		manifestCount(first.Stdout) == manifestCount(second.Stdout),
		fmt.Sprintf("%s then %s", manifestCount(first.Stdout), manifestCount(second.Stdout)))
}

func manifestCount(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Manifests created") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// fingerprintGitops hashes the generated tree, skipping .git — Git records a
// timestamp in every object, so including it would report a false difference
// on every run.
func (t *T) fingerprintGitops(org string) string {
	root := filepath.Join(t.Env.Sandbox.ConfigDir, "clusters", "gitops", org)
	var entries []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.Contains(path, string(os.PathSeparator)+".git"+string(os.PathSeparator)) {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		entries = append(entries, fmt.Sprintf("%s:%x", relative, sha256Sum(content)))
		return nil
	})
	sort.Strings(entries)
	return fmt.Sprintf("%d files %x", len(entries), sha256Sum([]byte(strings.Join(entries, "\n"))))
}

func checkDryRun(ctx context.Context, t *T) {
	const org, name = "dryorg", "dry-kind"
	t.initCluster(name, org, "--type", "kind")

	before := t.snapshot()

	generate := t.Run("--dry-run", "cluster", "generate", org+"/"+name)
	t.Assertf("generate --dry-run succeeds", generate.OK(),
		"exit %d: %s", generate.ExitCode, firstLine(generate.Output()))
	t.Assert("generate --dry-run says it is a preview",
		containsAny(generate.Output(), "dry-run", "dry run", "preview"), firstLine(generate.Stdout))

	deploy := t.Run("--dry-run", "cluster", "deploy", org+"/"+name)
	t.Assert("deploy --dry-run does not start a deployment",
		!containsAny(generate.Output(), "Deployment complete"), firstLine(deploy.Stdout))

	after := t.snapshot()
	added, removed, changed := diffSnapshots(before, after)
	t.Assertf("dry-run created no files", len(added) == 0, "%v", trim(added))
	t.Assertf("dry-run removed no files", len(removed) == 0, "%v", trim(removed))
	t.Assertf("dry-run modified no files", len(changed) == 0, "%v", trim(changed))
}

func checkLocalLifecycle(ctx context.Context, t *T) {
	// Module 20 is the continuity test: the same configuration Module 5 made,
	// carried through the whole journey rather than rebuilt for the occasion.
	fixture := t.primary()
	name := fixture.Cluster
	reference := fixture.Reference()

	use := t.Run("cluster", "use", reference)
	t.Assertf("cluster use succeeds", use.OK(), "exit %d: %s", use.ExitCode, firstLine(use.Output()))

	active := t.Run("cluster", "active")
	t.Assertf("cluster active reports the cluster just selected", strings.Contains(active.Stdout, name),
		"got %q", firstLine(active.Stdout))

	// Validation of a fresh configuration legitimately reports work still to
	// do — an unset GitOps token, a service without a password. What matters
	// is that it produces a structured, honest report rather than a crash.
	validate := t.Run("cluster", "validate", reference, "--output", "json")
	var report map[string]any
	err := json.Unmarshal([]byte(validate.Stdout), &report)
	t.Assertf("validate emits parseable JSON", err == nil, "%v — %s", err, firstLine(validate.Stdout))
	if err == nil {
		_, hasValid := report["valid"]
		t.Assert("the report says whether the cluster is valid", hasValid,
			fmt.Sprintf("keys: %v", keysOf(report)))
		t.Assert("the report names the target", report["target"] != nil, "no target section")
	}
	t.Assertf("validate signals failure through the exit code too",
		(report["valid"] == true) == validate.OK(),
		"valid=%v but exit code was %d", report["valid"], validate.ExitCode)

	// The rest of the journey, each step depending on the last.
	for _, step := range [][]string{
		{"cluster", "generate", reference},
		{"cluster", "describe", reference},
		{"cluster", "status", reference},
		{"cluster", "export", reference},
		{"cluster", "env", reference},
		{"cluster", "doctor", reference},
	} {
		result := t.Run(step...)
		label := strings.Join(step[1:2], " ")
		t.Assertf("cluster "+label+" succeeds after the previous step", result.OK(),
			"exit %d: %s", result.ExitCode, firstLine(result.Output()))
	}

	export := t.Run("cluster", "export", reference)
	var exported map[string]any
	// The parse error is the finding; the first line of the document says
	// nothing about why it would not load.
	exportErr := yaml.Unmarshal([]byte(export.Stdout), &exported)
	t.Assertf("the exported configuration is valid YAML", exportErr == nil, "%v", exportErr)
	t.Assert("the export carries the cluster through the whole journey",
		strings.Contains(export.Stdout, name), firstLine(export.Stdout))
}

func checkProviderTypes(ctx context.Context, t *T) {
	for _, provider := range []string{"kind", "openstack", "vmware", "baremetal"} {
		result := t.Run("cluster", "init", "prov-"+provider, "--org", "provorg",
			"--type", provider, "--no-keygen", "--no-sops-keygen")
		t.Assertf(provider+" is accepted", result.OK(),
			"exit %d: %s", result.ExitCode, firstLine(result.Output()))
		if result.OK() {
			raw, err := os.ReadFile(t.configPath("provorg", "prov-"+provider))
			if err == nil {
				t.Assertf("the configuration records "+provider,
					strings.Contains(string(raw), provider),
					"the generated configuration does not mention %q", provider)
			}
		}
	}

	// An unsupported provider must be refused. Accepting it and silently
	// falling back to the default is worse than an error: the person believes
	// they configured something they did not.
	bogus := t.Run("cluster", "init", "prov-bogus", "--org", "provorg",
		"--type", "not-a-real-provider", "--no-keygen", "--no-sops-keygen")
	t.Assertf("an unsupported provider is refused", !bogus.OK(),
		"exit %d — the command reported success for --type not-a-real-provider: %s",
		bogus.ExitCode, firstLine(bogus.Stdout))
	if bogus.OK() {
		raw, err := os.ReadFile(t.configPath("provorg", "prov-bogus"))
		if err == nil {
			t.Assert("and it did not silently substitute another provider",
				strings.Contains(string(raw), "not-a-real-provider"),
				"the configuration was written with a different provider than the one requested")
		}
	}
}

func checkGeneratedAssets(ctx context.Context, t *T) {
	// Module 22 validates what Module 19 generated, from the same fixture.
	fixture := t.primary()
	org, name := fixture.Organization, fixture.Cluster

	generate := t.Run("cluster", "generate", org+"/"+name)
	t.Require("generate succeeds", generate.OK(),
		fmt.Sprintf("exit %d: %s", generate.ExitCode, firstLine(generate.Output())))

	root := filepath.Join(t.Env.Sandbox.ConfigDir, "clusters", "gitops", org)
	var yamlFiles, badYAML, outside []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.Contains(path, string(os.PathSeparator)+".git"+string(os.PathSeparator)) {
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}
		yamlFiles = append(yamlFiles, path)
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			badYAML = append(badYAML, path)
			return nil
		}
		// A manifest file can hold several documents.
		decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
		for {
			var document any
			decodeErr := decoder.Decode(&document)
			if decodeErr != nil {
				if decodeErr.Error() != "EOF" {
					badYAML = append(badYAML, filepath.Base(path)+": "+decodeErr.Error())
				}
				break
			}
		}
		return nil
	})

	t.Assertf("generate produced manifests", len(yamlFiles) > 10, "found %d YAML files", len(yamlFiles))
	t.Assertf("every generated manifest parses", len(badYAML) == 0, "%v", trim(badYAML))

	// Nothing may be written outside the GitOps root for this organization.
	_ = filepath.Walk(t.Env.Sandbox.Work, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		outside = append(outside, path)
		return nil
	})
	t.Assertf("nothing was generated into the working directory", len(outside) == 0, "%v", trim(outside))

	// A generated repository that is not a repository is not usable by Flux.
	_, err := os.Stat(filepath.Join(root, ".git"))
	t.Assert("the GitOps root is a Git repository", err == nil, fmt.Sprint(err))
}

func checkLargeInputs(ctx context.Context, t *T) {
	const org = "bulkorg"
	const count = 60

	for i := 0; i < count; i++ {
		name := fmt.Sprintf("bulk-%03d", i)
		result := t.Env.CLI.Run(ctx, "cluster", "init", name, "--org", org,
			"--type", "kind", "--no-keygen", "--no-sops-keygen")
		if !result.OK() {
			t.record(result)
			t.Require(fmt.Sprintf("creating cluster %d of %d", i, count), false, firstLine(result.Output()))
		}
	}
	t.Notef("clusters created", "%d", count)

	list := t.Run("cluster", "list", "--output", "json")
	t.Require("listing many clusters succeeds", list.OK(), firstLine(list.Stderr))

	var clusters []string
	err := json.Unmarshal([]byte(list.Stdout), &clusters)
	t.Assertf("the listing is still valid JSON at scale", err == nil, "%v", err)
	t.Assertf("every cluster is listed", len(clusters) >= count,
		"expected at least %d, got %d", count, len(clusters))
	t.Assertf("listing %d clusters stays under 5s", list.Duration.Seconds() < 5,
		"took %s", list.Duration)

	// A pool with a lot of nodes is the other kind of large input.
	pool := t.Run("cluster", "pool", "scale", "bulk-000", "--org", org, "workers", "250")
	t.Notef("scaling a pool to 250 nodes", "exit %d: %s", pool.ExitCode, firstLine(pool.Output()))
	t.Assert("scaling a large pool does not panic",
		!containsAny(pool.Output(), "goroutine ", "runtime.gopanic"), firstLine(pool.Output()))
}

func checkAwkwardPaths(ctx context.Context, t *T) {
	for label, directory := range map[string]string{
		"a path with spaces":   "config with spaces",
		"a Unicode path":       "config-ünïcode-日本語",
		"a deeply nested path": filepath.Join("a", "b", "c", "d", "e", "f", "config"),
	} {
		root := filepath.Join(t.Env.Sandbox.Root, directory)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", root, err)
		}
		result := t.Run("--config-dir", root, "cluster", "init", "awkward",
			"--org", "awkorg", "--no-keygen", "--no-sops-keygen")
		t.Assertf(label+" is accepted", result.OK(),
			"exit %d: %s", result.ExitCode, firstLine(result.Output()))

		if result.OK() {
			list := t.Run("--config-dir", root, "cluster", "list", "--output", "json")
			t.Assertf(label+" can be read back", strings.Contains(list.Stdout, "awkward"),
				"cluster list returned %q", firstLine(list.Stdout))
		}
	}
}

func checkUpgradeCompatibility(ctx context.Context, t *T) {
	const org, name = "upgradeorg", "upgrade-kind"
	t.initCluster(name, org, "--type", "kind")

	path := t.configPath(org, name)
	original, err := os.ReadFile(path)
	t.Require("the configuration was written", err == nil, fmt.Sprint(err))

	// Everything a person's existing configuration has to survive: reading it,
	// exporting it, normalising it, and normalising it again.
	export := t.Run("cluster", "export", org+"/"+name)
	t.Assertf("an existing configuration still exports", export.OK(),
		"exit %d: %s", export.ExitCode, firstLine(export.Output()))

	normalise := t.Run("cluster", "normalize", org+"/"+name)
	t.Assertf("normalize accepts it", normalise.OK(),
		"exit %d: %s", normalise.ExitCode, firstLine(normalise.Output()))

	afterFirst, err := os.ReadFile(path)
	t.Require("the configuration survived normalize", err == nil, fmt.Sprint(err))

	again := t.Run("cluster", "normalize", org+"/"+name)
	t.Assertf("normalize runs a second time", again.OK(),
		"exit %d: %s", again.ExitCode, firstLine(again.Output()))
	afterSecond, err := os.ReadFile(path)
	t.Require("the configuration survived the second normalize", err == nil, fmt.Sprint(err))

	t.Assert("normalize is idempotent", string(afterFirst) == string(afterSecond),
		"the second run changed the file again, so a migration is not convergent")
	t.Assertf("the user's own values were preserved",
		strings.Contains(string(afterSecond), name), "the cluster name is gone from %s", path)
	t.Assertf("nothing was truncated", len(afterSecond) >= len(original)/2,
		"file shrank from %d to %d bytes", len(original), len(afterSecond))

	// A migration command that reports success on an already-migrated layout,
	// twice, is the property that makes an upgrade safe to retry.
	migrate := t.Run("cluster", "migrate-layout", "--org", org, "--dry-run")
	t.Assert("migrate-layout --dry-run does not fail on an already-current layout",
		migrate.OK() || containsAny(migrate.Output(), "no legacy", "nothing to migrate", "not found"),
		firstLine(migrate.Output()))
}

func checkCleanup(ctx context.Context, t *T) {
	const org, name = "cleanorg", "clean-kind"

	// What the working directory held before this check ran. The workspace is
	// shared with every other check in the run, so asserting that it is empty
	// would make this check's result depend on what happened to run first.
	// What it can honestly assert is that its own commands added nothing.
	before := map[string]bool{}
	if entries, err := os.ReadDir(t.Env.Sandbox.Work); err == nil {
		for _, entry := range entries {
			before[entry.Name()] = true
		}
	}

	t.initCluster(name, org, "--type", "kind")
	_ = t.Run("cluster", "generate", org+"/"+name)
	_ = t.Run("cluster", "validate", org+"/"+name)
	_ = t.Run("cluster", "export", org+"/"+name)

	var temporary, locks []string
	_ = filepath.Walk(t.Env.Sandbox.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		switch {
		case strings.HasSuffix(base, ".tmp"), strings.HasPrefix(base, ".tmp"),
			strings.Contains(base, ".partial"), strings.HasSuffix(base, ".swp"):
			temporary = append(temporary, path)
		case strings.HasSuffix(base, ".lock"), base == "lock", base == ".lock":
			locks = append(locks, path)
		}
		return nil
	})

	t.Assertf("no temporary files were left behind", len(temporary) == 0, "%v", trim(temporary))
	t.Assertf("no stale locks were left behind", len(locks) == 0, "%v", trim(locks))

	// The working directory is not a scratch space: a cluster command run from
	// somewhere should write into the configuration root, not into wherever
	// the person happened to be standing.
	var added []string
	entries, err := os.ReadDir(t.Env.Sandbox.Work)
	for _, entry := range entries {
		if !before[entry.Name()] {
			added = append(added, entry.Name())
		}
	}
	t.Assertf("these commands wrote nothing into the working directory",
		err == nil && len(added) == 0, "%v", added)
}

// --- snapshot helpers -------------------------------------------------------

type snapshot map[string]string

// snapshot fingerprints every file under the sandbox, so a check can prove a
// command wrote nothing rather than trusting it to say so.
func (t *T) snapshot() snapshot {
	out := snapshot{}
	_ = filepath.Walk(t.Env.Sandbox.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		out[path] = fmt.Sprintf("%x", sha256Sum(content))
		return nil
	})
	return out
}

func diffSnapshots(before, after snapshot) (added, removed, changed []string) {
	for path, sum := range after {
		previous, existed := before[path]
		switch {
		case !existed:
			added = append(added, path)
		case previous != sum:
			changed = append(changed, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			removed = append(removed, path)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return added, removed, changed
}

// trim keeps an assertion detail readable when a diff is large.
func trim(items []string) []string {
	if len(items) <= 5 {
		return items
	}
	return append(items[:5], fmt.Sprintf("... and %d more", len(items)-5))
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
