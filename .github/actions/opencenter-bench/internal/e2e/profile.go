package e2e

import (
	"fmt"
	"sort"
	"strings"
)

// Three dimensions, kept apart on purpose.
//
// The brief is explicit that GitHub Actions is not a provider, and the reason is
// worth stating: conflating them produces a matrix where "openstack" and
// "github-actions" are alternatives, and then nobody can express the thing that
// actually matters — the same OpenStack test running in both places. Where the
// run is driven from, what kind of infrastructure it touches, and whose API it
// talks to are three independent questions.

// Channel is where the run is driven from.
type Channel string

const (
	ChannelLocal  Channel = "local"
	ChannelGitHub Channel = "github-actions"
)

// Infrastructure is what kind of thing the run creates.
type Infrastructure string

const (
	InfraConfigOnly Infrastructure = "configuration-only"
	InfraEmulated   Infrastructure = "emulated"
	InfraKind       Infrastructure = "kind"
	InfraReal       Infrastructure = "real"
)

// Provider is whose API is being spoken to.
type Provider string

const (
	ProviderNone      Provider = "none"
	ProviderOpenStack Provider = "openstack"
	ProviderVMware    Provider = "vmware"
	ProviderBareMetal Provider = "baremetal"
	ProviderKind      Provider = "kind"
)

// Profile is a named combination, plus what it is allowed to do.
type Profile struct {
	Name           string
	Infrastructure Infrastructure
	Provider       Provider

	// ClusterType is the value passed to `cluster init --type`. It is not always
	// the provider name: an emulated OpenStack cluster is still an OpenStack
	// cluster as far as the configuration is concerned, and the emulation lives
	// in where the endpoint points rather than in what the cluster claims to be.
	ClusterType string

	// Deploys is false for the profiles that stop before anything is created.
	Deploys bool

	// LiveApproval marks the profiles that can spend somebody's money. These
	// require the Phase 0 checkboxes and refuse to run without them.
	LiveApproval bool

	// Tools are the prerequisites this profile needs. Deliberately per-profile:
	// requiring the OpenStack client for a Kind run is how a prerequisites phase
	// teaches people to ignore it.
	Tools []string

	// Notes is what the operator should know before starting.
	Notes string
}

// Profiles are the supported combinations.
var Profiles = []Profile{
	{
		Name:           "configuration-only",
		Infrastructure: InfraConfigOnly,
		Provider:       ProviderNone,
		ClusterType:    "kind",
		Deploys:        false,
		Tools:          []string{"git", "go", "mise"},
		Notes: "Generates and validates, and stops before anything is created. " +
			"The only profile that needs no container runtime and no credentials.",
	},
	{
		Name:           "openstack-emulated",
		Infrastructure: InfraEmulated,
		Provider:       ProviderOpenStack,
		ClusterType:    "openstack",
		Deploys:        false,
		Tools:          []string{"git", "go", "mise"},
		Notes: "Talks to the bench's own stand-in OpenStack API. No real endpoint is " +
			"contacted, and every infrastructure result is labelled emulated.",
	},
	{
		Name:           "vmware-emulated",
		Infrastructure: InfraEmulated,
		Provider:       ProviderVMware,
		ClusterType:    "vmware",
		Deploys:        false,
		Tools:          []string{"git", "go", "mise"},
		Notes:          "Simulated vCenter responses. No real datacenter is contacted.",
	},
	{
		Name:           "baremetal-emulated",
		Infrastructure: InfraEmulated,
		Provider:       ProviderBareMetal,
		ClusterType:    "baremetal",
		Deploys:        false,
		Tools:          []string{"git", "go", "mise"},
		Notes:          "Simulated hosts and BMC. No real machine is contacted.",
	},
	{
		Name:           "kind",
		Infrastructure: InfraKind,
		Provider:       ProviderKind,
		ClusterType:    "kind",
		Deploys:        true,
		Tools:          []string{"git", "go", "mise", "kubectl", "kind", "docker"},
		Notes: "Real Kubernetes on a local container runtime. Creates and destroys a " +
			"cluster on this machine; nothing leaves it.",
	},
	{
		Name:           "openstack-real",
		Infrastructure: InfraReal,
		Provider:       ProviderOpenStack,
		ClusterType:    "openstack",
		Deploys:        true,
		LiveApproval:   true,
		Tools:          []string{"git", "go", "mise", "kubectl"},
		Notes:          "Creates real infrastructure on a real OpenStack project. Costs money.",
	},
	{
		Name:           "vmware-real",
		Infrastructure: InfraReal,
		Provider:       ProviderVMware,
		ClusterType:    "vmware",
		Deploys:        true,
		LiveApproval:   true,
		Tools:          []string{"git", "go", "mise", "kubectl"},
		Notes:          "Creates real virtual machines on a real vCenter.",
	},
	{
		Name:           "baremetal-real",
		Infrastructure: InfraReal,
		Provider:       ProviderBareMetal,
		ClusterType:    "baremetal",
		Deploys:        true,
		LiveApproval:   true,
		Tools:          []string{"git", "go", "mise", "kubectl"},
		Notes:          "Provisions real machines over BMC. Physically reconfigures hardware.",
	},
}

// FindProfile looks a profile up by name.
func FindProfile(name string) (Profile, error) {
	for _, profile := range Profiles {
		if profile.Name == name {
			return profile, nil
		}
	}
	names := make([]string, 0, len(Profiles))
	for _, profile := range Profiles {
		names = append(names, profile.Name)
	}
	sort.Strings(names)
	return Profile{}, fmt.Errorf("no profile called %q. The profiles are: %s",
		name, strings.Join(names, ", "))
}

// Emulated reports whether the provider is a stand-in.
func (p Profile) Emulated() bool { return p.Infrastructure == InfraEmulated }

// SkipsFrom lists the phases a profile has no business running.
//
// Returned as skips rather than left to fail, because a configuration-only run
// that reports "Kubernetes unreachable" has not found a defect — it has found
// that nobody deployed anything, which was the plan.
func (p Profile) SkipsFrom() []ID {
	if p.Deploys {
		return nil
	}
	return []ID{
		PhaseDeploy, PhaseInfrastructure, PhaseKubernetes,
		PhasePlatform, PhaseSmoke, PhaseFailureTests,
	}
}
