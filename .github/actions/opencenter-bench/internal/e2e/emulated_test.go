package e2e

import (
	"strings"
	"testing"
)

// Both of these came from running all four profiles in CI side by side.
// baremetal-emulated and configuration-only passed; openstack-emulated and
// vmware-emulated failed, and the difference was never visible from a single
// profile.

// A destroy that could not find its tool has not found a cleanup defect.
//
// Destroying an OpenStack cluster runs OpenTofu, and the emulated profiles do
// not require it — their Tools are git, go and mise. On a machine without
// terraform, destroy failed with "executable file not found in $PATH" and was
// reported as a Cleanup defect, which is a FAIL and blocks a release. Nothing
// leaked; the tool that removes it was never installed.
func TestAMissingToolIsNamed(t *testing.T) {
	output := `Destroying infrastructure via OpenTofu...
Error: infrastructure destruction failed: step "opentofu-init" failed: ` +
		`command failed: terraform init: exec: "terraform": executable file not found in $PATH`

	if got := missingToolIn(output); got != "terraform" {
		t.Fatalf("missingToolIn = %q, want terraform", got)
	}
}

// And nothing else is read as one. A classifier that sees a missing tool in
// every failure is a release gate that never blocks anything.
func TestOnlyAMissingExecutableCounts(t *testing.T) {
	for _, output := range []string{
		"",
		"Error: validation failed",
		"Error: the API returned 500",
		// Names a binary, but the failure is the binary's, not its absence.
		`exec: "terraform": exit status 1`,
		"terraform: something else went wrong",
	} {
		if got := missingToolIn(output); got != "" {
			t.Errorf("missingToolIn(%q) = %q, want empty", output, got)
		}
	}
}

// OpenStack asks for Swift credentials that the other providers do not, and the
// bench filled in three of the five values `cluster init` leaves empty. The
// configuration it produced then failed the CLI's own validation, and the bench
// called that a product defect — while it was the one that generated it.
func TestOpenStackGetsItsSwiftSecretsSeeded(t *testing.T) {
	// The settings list is built inside phaseConfigure, so this asserts the
	// property that matters: every key the OpenStack validator demanded is one
	// this package knows to set.
	// Every key `cluster validate` named, on a run that had already been given
	// the three common ones. Taken from the CLI's own output, not guessed.
	demanded := []string{
		"secrets.loki.swift_application_credential_secret",
		"secrets.tempo.swift_application_credential_secret",
		"opencenter.infrastructure.cloud.openstack.application_credential_id",
		"opencenter.infrastructure.cloud.openstack.application_credential_secret",
	}
	source := configureSettingsFor(ProviderOpenStack)
	for _, key := range demanded {
		if !containsKey(source, key) {
			t.Errorf("openstack does not seed %s, so cluster validate will reject "+
				"the configuration this bench generated", key)
		}
	}

	// The fields that do not exist must not be attempted. Setting them fails
	// with "field not found", which is noise in the evidence of every run.
	for _, absent := range []string{
		"secrets.loki.swift_application_credential_id",
		"secrets.tempo.swift_application_credential_id",
	} {
		if containsKey(source, absent) {
			t.Errorf("%s is set, and there is no such field on the struct", absent)
		}
	}

	// And the providers without Swift are not given secrets that mean nothing
	// to them.
	for _, provider := range []Provider{ProviderBareMetal, ProviderKind} {
		for _, key := range demanded {
			if containsKey(configureSettingsFor(provider), key) {
				t.Errorf("%s is given %s, which it has no Swift storage for",
					provider, key)
			}
		}
	}
}

// vSphere CSI is on by default and wants a vCenter, which is why
// vmware-emulated failed validation for entirely different keys than OpenStack
// did. One profile passing told us nothing about the others.
func TestVMwareGetsItsCSISecretsSeeded(t *testing.T) {
	source := configureSettingsFor(ProviderVMware)
	for _, key := range []string{
		"secrets.vsphere_csi.vcenter_host",
		"secrets.vsphere_csi.username",
		"secrets.vsphere_csi.password",
	} {
		if !containsKey(source, key) {
			t.Errorf("vmware does not seed %s, so cluster validate will reject the "+
				"configuration this bench generated", key)
		}
	}
	// The host has to look like a host. An unroutable .invalid name says
	// "fixture" to a reader of the generated files, where a random string would
	// look like a leaked address.
	for _, setting := range source {
		if setting[0] == "secrets.vsphere_csi.vcenter_host" &&
			!strings.HasSuffix(setting[1], ".invalid") {
			t.Errorf("the vCenter host is %q, which does not read as a fixture",
				setting[1])
		}
	}
	if containsKey(configureSettingsFor(ProviderOpenStack),
		"secrets.vsphere_csi.username") {
		t.Error("openstack is given vSphere CSI credentials")
	}
}

// Leftovers after a destroy that never ran are not a leak.
//
// An emulated profile does not require OpenTofu, and destroying an OpenStack
// cluster runs it. On a machine without it, destroy is blocked, the cluster
// configuration stays on disk, and the gate called that FAIL — "this build
// leaks" — when the truth is that the thing which removes it was never
// installed. That is a release blocked over a missing package.
func TestLeftoversAfterABlockedDestroyAreInconclusive(t *testing.T) {
	run := &Run{}
	for _, phase := range Order {
		run.Phases = append(run.Phases, PhaseResult{ID: phase.ID, State: StatePassed})
	}
	run.Result(PhaseDestroy).State = StateBlocked
	run.Result(PhaseDestroy).Message = "destroy needs terraform, which is not installed"
	run.Resources = []Resource{{Kind: "cluster-config", Name: "e2e-x"}}

	verdict, why := run.Gate()
	if verdict != VerdictInconclusive {
		t.Fatalf("verdict is %q, want INCONCLUSIVE — %s", verdict, why)
	}
	if !strings.Contains(why, "destroy could not run") {
		t.Errorf("the reason does not say cleanup never ran: %s", why)
	}
}

// And a destroy that ran and left something behind is still a failure. This is
// the case the rule exists for, and softening it would let a real leak through.
func TestLeftoversAfterADestroyThatRanAreStillAFailure(t *testing.T) {
	run := &Run{}
	for _, phase := range Order {
		run.Phases = append(run.Phases, PhaseResult{ID: phase.ID, State: StatePassed})
	}
	run.Resources = []Resource{{Kind: "kind-cluster", Name: "e2e-x"}}

	if verdict, why := run.Gate(); verdict != VerdictFail {
		t.Fatalf("verdict is %q, want FAIL — a run that leaks a cluster has not "+
			"passed (%s)", verdict, why)
	}
}

func containsKey(settings [][2]string, key string) bool {
	for _, setting := range settings {
		if setting[0] == key {
			return true
		}
	}
	return false
}

// No profile may leave a stub secret in what it generates.
//
// Deploy's GitOps commit runs a security check that rejects any file still
// holding the literal CHANGEME, and it rejected five at once. They are seeded
// for every provider because they come from the service defaults, not from the
// infrastructure — Kind is only the profile that reaches the check.
func TestTheStubSecretsAreSeededForEveryProvider(t *testing.T) {
	stubs := []string{
		"secrets.headlamp.oidc_client_secret",
		"secrets.loki.s3_access_key_id",
		"secrets.loki.s3_secret_access_key",
		"secrets.tempo.access_key",
		"secrets.tempo.secret_key",
	}
	for _, provider := range []Provider{
		ProviderKind, ProviderOpenStack, ProviderVMware, ProviderBareMetal,
	} {
		settings := configureSettingsFor(provider)
		for _, key := range stubs {
			if !containsKey(settings, key) {
				t.Errorf("%s does not seed %s, so the generated overlay keeps "+
					"CHANGEME there and the GitOps commit is refused", provider, key)
			}
		}
	}
}

// Kind must not be handed a GitOps repository URL.
//
// The CLI reads Config.ConfiguredGitURL() to choose between bootstrapping Flux
// from GitHub and bootstrapping it from the local Gitea, and that method
// reports "" — meaning "use the local one" — only while the URL is still the
// schema default. Any value the bench sets here, however plausible, sends a
// Kind run to api.github.com with a credential it generated itself.
//
// That is how deploy failed at step 4 of 8 with 401 Bad credentials for a
// repository that does not exist, after steps 1 to 3 had passed.
func TestKindIsNotGivenAGitOpsRepositoryURL(t *testing.T) {
	if containsKey(configureSettingsFor(ProviderKind), "opencenter.gitops.repository.url") {
		t.Error("kind is given a GitOps repository URL, which turns off the CLI's " +
			"fallback to the local Gitea and makes flux bootstrap against github.com")
	}
	// The providers that have no local Gitea still need one to validate against.
	for _, provider := range []Provider{ProviderOpenStack, ProviderVMware, ProviderBareMetal} {
		if !containsKey(configureSettingsFor(provider), "opencenter.gitops.repository.url") {
			t.Errorf("%s has no local Gitea to fall back to, so cluster validate "+
				"will reject the configuration without a repository URL", provider)
		}
	}
}

// The generated values must never be the same twice, or a "throwaway secret"
// is a shared one.
func TestSeededSecretsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, setting := range configureSettingsFor(ProviderOpenStack) {
		if strings.Contains(setting[0], "url") {
			continue
		}
		if seen[setting[1]] {
			t.Errorf("%s reuses a value another setting already has", setting[0])
		}
		seen[setting[1]] = true
	}
}
