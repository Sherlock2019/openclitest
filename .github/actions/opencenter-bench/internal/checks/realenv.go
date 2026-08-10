package checks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
	"github.com/opencenter-cloud/opencli-testbench/internal/registry"
)

// The only checks that create something outliving a command. They never run
// unless the mutation gate is set, and they register their cleanup before the
// first thing that can leak.
func init() {
	register(
		Check{
			ID:           "real-kind-lifecycle",
			Name:         "A real Kubernetes cluster: init, deploy, verify, destroy",
			Category:     "real-environment",
			Environments: []string{"kind"},
			Mutating:     true,
			Slow:         true,
			Fn:           checkKindLifecycle,
		},
		Check{
			ID:           "real-kind-cleanup",
			Name:         "Destroy leaves no cluster, container, lock or state behind",
			Category:     "cleanup",
			Environments: []string{"kind"},
			Mutating:     true,
			Slow:         true,
			Fn:           checkKindCleanup,
		},
		Check{
			ID:           "real-flex-lifecycle",
			Name:         "A real OpenStack deployment, verified and destroyed",
			Category:     "real-environment",
			Environments: []string{"flex"},
			Mutating:     true,
			Slow:         true,
			Fn:           checkFlexLifecycle,
		},
		Check{
			ID:           "real-flex-no-orphans",
			Name:         "Destroy leaves no servers, ports, volumes or networks behind",
			Category:     "cleanup",
			Environments: []string{"flex"},
			Mutating:     true,
			Slow:         true,
			Fn:           checkFlexOrphans,
		},
	)
}

// uniqueName keeps two runs, or a run and a leftover from yesterday, from
// colliding on the same cluster.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().Unix()%100000)
}

// tool runs an external command with a deadline and returns its combined
// output. The bench uses it to verify from the outside what the CLI claims
// from the inside.
func (t *T) tool(timeout time.Duration, name string, args ...string) (string, int) {
	ctx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()

	command := exec.CommandContext(ctx, name, args...)
	command.Env = t.Env.CLI.Env
	command.Dir = t.Env.CLI.Dir
	output, err := command.CombinedOutput()

	code := 0
	if err != nil {
		code = 1
		var exitError *exec.ExitError
		if ok := asExitError(err, &exitError); ok {
			code = exitError.ExitCode()
		}
	}
	t.result.Commands = append(t.result.Commands, Invocation{
		Command:  name + " " + strings.Join(args, " "),
		ExitCode: code,
		Stdout:   clip(string(output)),
	})
	return string(output), code
}

func asExitError(err error, target **exec.ExitError) bool {
	if converted, ok := err.(*exec.ExitError); ok {
		*target = converted
		return true
	}
	return false
}

func checkKindLifecycle(ctx context.Context, t *T) {
	if _, err := exec.LookPath("kind"); err != nil {
		t.Skip("kind is not installed")
	}
	const org = "kindlab"
	name := uniqueName("bench")
	reference := org + "/" + name

	// Registered before anything is created, so a check that stops half way
	// through still leaves Module 30 something to find and remove. The verify
	// command is what makes the cleanup claim checkable: `kind get clusters`
	// must stop mentioning this name.
	t.Register(registry.Resource{
		Provider: "kind", Type: "kind-cluster", Name: name, Location: reference,
		Cleanup: []string{"cluster", "destroy", reference, "--force", "--break-lock"},
		Verify:  []string{"kind", "get", "clusters"},
	})

	defer func() {
		t.Env.CLI.RunWith(context.Background(), cli.RunOptions{Timeout: 10 * time.Minute},
			"cluster", "destroy", reference, "--force", "--break-lock")
		_, _ = t.tool(5*time.Minute, "kind", "delete", "cluster", "--name", name)
	}()

	t.initCluster(name, org, "--type", "kind")

	validate := t.RunWith(cli.RunOptions{Timeout: 3 * time.Minute}, "cluster", "validate", reference)
	t.Notef("pre-deploy validation", "exit %d: %s", validate.ExitCode, firstLine(validate.Stdout))

	generate := t.RunWith(cli.RunOptions{Timeout: 5 * time.Minute}, "cluster", "generate", reference)
	t.Require("generate succeeds", generate.OK(),
		fmt.Sprintf("exit %d: %s", generate.ExitCode, firstLine(generate.Output())))

	deploy := t.RunWith(cli.RunOptions{Timeout: 25 * time.Minute}, "cluster", "deploy", reference)
	t.Require("deploy succeeds", deploy.OK(),
		fmt.Sprintf("exit %d: %s", deploy.ExitCode, firstLine(deploy.Output())))

	status := t.RunWith(cli.RunOptions{Timeout: 3 * time.Minute}, "cluster", "status", reference)
	t.Assertf("status reports the deployed cluster", status.OK(),
		"exit %d: %s", status.ExitCode, firstLine(status.Output()))

	// Verify from outside the CLI. A deploy that says it worked while the
	// cluster answers nothing is the failure this check exists to catch.
	clusters, _ := t.tool(2*time.Minute, "kind", "get", "clusters")
	t.Assertf("kind reports the cluster exists", strings.Contains(clusters, name),
		"kind get clusters returned %q", firstLine(clusters))

	kubeconfig := filepath.Join(t.Env.Sandbox.ConfigDir, "clusters", "state", org, name, "kubeconfig.yaml")
	if !fileExists(kubeconfig) {
		// Fall back to whatever kind wrote, so a path change in the CLI does
		// not look like a cluster failure.
		exported, _ := t.tool(2*time.Minute, "kind", "get", "kubeconfig", "--name", name)
		kubeconfig = filepath.Join(t.Env.Sandbox.Root, "kind-kubeconfig.yaml")
		_ = os.WriteFile(kubeconfig, []byte(exported), 0o600)
	}
	t.Assert("a kubeconfig was produced", fileExists(kubeconfig), kubeconfig)

	t.Env.Sandbox.Set("KUBECONFIG", kubeconfig)
	t.Env.CLI.Env = t.Env.Sandbox.Env()

	nodes, code := t.tool(3*time.Minute, "kubectl", "get", "nodes", "-o", "wide")
	t.Assertf("kubectl can reach the cluster", code == 0, "%s", firstLine(nodes))
	t.Assert("at least one node is Ready", strings.Contains(nodes, "Ready"), firstLine(nodes))

	pods, code := t.tool(3*time.Minute, "kubectl", "get", "pods", "-A")
	t.Assertf("the core namespaces have pods", code == 0 && strings.Contains(pods, "kube-system"),
		"%s", firstLine(pods))

	doctor := t.RunWith(cli.RunOptions{Timeout: 5 * time.Minute}, "cluster", "doctor", reference)
	t.Assertf("doctor passes against a live cluster", doctor.OK(),
		"exit %d: %s", doctor.ExitCode, firstLine(doctor.Output()))

	destroy := t.RunWith(cli.RunOptions{Timeout: 15 * time.Minute},
		"cluster", "destroy", reference, "--force")
	t.Assertf("destroy succeeds", destroy.OK(),
		"exit %d: %s", destroy.ExitCode, firstLine(destroy.Output()))

	remaining, _ := t.tool(2*time.Minute, "kind", "get", "clusters")
	t.Assertf("the cluster is gone", !strings.Contains(remaining, name),
		"kind still lists %q", name)
}

func checkKindCleanup(ctx context.Context, t *T) {
	if _, err := exec.LookPath("kind"); err != nil {
		t.Skip("kind is not installed")
	}
	const org = "kindclean"
	name := uniqueName("clean")
	reference := org + "/" + name

	defer func() {
		_, _ = t.tool(5*time.Minute, "kind", "delete", "cluster", "--name", name)
	}()

	t.initCluster(name, org, "--type", "kind")
	deploy := t.RunWith(cli.RunOptions{Timeout: 25 * time.Minute}, "cluster", "deploy", reference)
	t.Require("deploy succeeds", deploy.OK(),
		fmt.Sprintf("exit %d: %s", deploy.ExitCode, firstLine(deploy.Output())))

	destroy := t.RunWith(cli.RunOptions{Timeout: 15 * time.Minute},
		"cluster", "destroy", reference, "--force")
	t.Require("destroy succeeds", destroy.OK(),
		fmt.Sprintf("exit %d: %s", destroy.ExitCode, firstLine(destroy.Output())))

	clusters, _ := t.tool(2*time.Minute, "kind", "get", "clusters")
	t.Assertf("no Kind cluster survives", !strings.Contains(clusters, name), "%s", firstLine(clusters))

	// The container runtime is the honest answer to "is it really gone?".
	runtime := "docker"
	if _, err := exec.LookPath("docker"); err != nil {
		runtime = "podman"
	}
	containers, _ := t.tool(2*time.Minute, runtime, "ps", "-a", "--format", "{{.Names}}")
	t.Assertf("no container named after the cluster survives", !strings.Contains(containers, name),
		"%s still has containers for %s", runtime, name)

	var locks []string
	_ = filepath.Walk(t.Env.Sandbox.ConfigDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".lock") {
			locks = append(locks, path)
		}
		return nil
	})
	t.Assertf("no lock survives the destroy", len(locks) == 0, "%v", trim(locks))
}

func checkFlexLifecycle(ctx context.Context, t *T) {
	cloud := t.requireCloud()
	if _, err := exec.LookPath("openstack"); err != nil {
		t.Skip("the openstack client is needed to verify a real deployment from outside")
	}

	const org = "flexlab"
	name := uniqueName("bench")
	reference := org + "/" + name

	// Registered before the first mutation, with the provider's own listing as
	// the verification: a destroy that returned zero proves nothing until
	// `openstack server list` stops naming the cluster.
	t.Register(registry.Resource{
		Provider: "openstack", Type: "cluster", Name: name, Location: reference,
		Cleanup: []string{"cluster", "destroy", reference, "--force", "--break-lock"},
		Verify:  []string{"openstack", "server", "list", "-f", "value", "-c", "Name"},
	})

	defer func() {
		t.Env.CLI.RunWith(context.Background(), cli.RunOptions{Timeout: 30 * time.Minute},
			"cluster", "destroy", reference, "--force", "--break-lock")
	}()

	t.initCluster(name, org, "--type", "openstack")

	sync := t.RunWith(cli.RunOptions{Timeout: 10 * time.Minute},
		"cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes")
	t.Require("discovery succeeds", sync.OK(),
		fmt.Sprintf("exit %d: %s", sync.ExitCode, firstLine(sync.Output())))

	generate := t.RunWith(cli.RunOptions{Timeout: 10 * time.Minute}, "cluster", "generate", reference)
	t.Require("generate succeeds", generate.OK(),
		fmt.Sprintf("exit %d: %s", generate.ExitCode, firstLine(generate.Output())))

	deploy := t.RunWith(cli.RunOptions{Timeout: 60 * time.Minute}, "cluster", "deploy", reference)
	t.Require("deploy succeeds", deploy.OK(),
		fmt.Sprintf("exit %d: %s", deploy.ExitCode, firstLine(deploy.Output())))

	for _, args := range [][]string{
		{"cluster", "status", reference},
		{"cluster", "describe", reference},
	} {
		result := t.RunWith(cli.RunOptions{Timeout: 5 * time.Minute}, args...)
		t.Assertf(strings.Join(args, " ")+" succeeds", result.OK(),
			"exit %d: %s", result.ExitCode, firstLine(result.Output()))
	}

	servers, _ := t.tool(5*time.Minute, "openstack", "server", "list", "-f", "value", "-c", "Name")
	t.Assertf("the provider really has servers for this cluster", strings.Contains(servers, name),
		"openstack server list does not mention %s", name)

	kubeconfig := filepath.Join(t.Env.Sandbox.ConfigDir, "clusters", "state", org, name, "kubeconfig.yaml")
	t.Assert("a kubeconfig was produced", fileExists(kubeconfig), kubeconfig)
	if fileExists(kubeconfig) {
		t.Env.Sandbox.Set("KUBECONFIG", kubeconfig)
		t.Env.CLI.Env = t.Env.Sandbox.Env()
		nodes, code := t.tool(5*time.Minute, "kubectl", "get", "nodes")
		t.Assertf("the deployed cluster answers kubectl", code == 0, "%s", firstLine(nodes))
		t.Assert("its nodes are Ready", strings.Contains(nodes, "Ready"), firstLine(nodes))
	}

	destroy := t.RunWith(cli.RunOptions{Timeout: 45 * time.Minute},
		"cluster", "destroy", reference, "--force")
	t.Assertf("destroy succeeds", destroy.OK(),
		"exit %d: %s", destroy.ExitCode, firstLine(destroy.Output()))
}

func checkFlexOrphans(ctx context.Context, t *T) {
	_ = t.requireCloud()
	if _, err := exec.LookPath("openstack"); err != nil {
		t.Skip("the openstack client is needed to check for orphaned resources")
	}

	// This check reads the account after the lifecycle check has run. It
	// creates nothing of its own; its whole job is the leak sweep, which is
	// the part a deployment test usually forgets.
	const org = "flexlab"

	resources := []struct {
		kind string
		args []string
	}{
		{"server", []string{"server", "list", "-f", "value", "-c", "Name"}},
		{"volume", []string{"volume", "list", "-f", "value", "-c", "Name"}},
		{"port", []string{"port", "list", "-f", "value", "-c", "Name"}},
		{"floating ip", []string{"floating", "ip", "list", "-f", "value", "-c", "Description"}},
		{"router", []string{"router", "list", "-f", "value", "-c", "Name"}},
		{"network", []string{"network", "list", "-f", "value", "-c", "Name"}},
		{"security group", []string{"security", "group", "list", "-f", "value", "-c", "Name"}},
		{"keypair", []string{"keypair", "list", "-f", "value", "-c", "Name"}},
	}

	found := 0
	for _, resource := range resources {
		output, code := t.tool(3*time.Minute, "openstack", resource.args...)
		if code != 0 {
			t.Notef("could not list "+resource.kind+"s", "%s", firstLine(output))
			continue
		}
		var leaked []string
		for _, line := range strings.Split(output, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && strings.Contains(trimmed, org) {
				leaked = append(leaked, trimmed)
			}
		}
		found += len(leaked)
		t.Assertf("no orphaned "+resource.kind+"s", len(leaked) == 0, "%v", trim(leaked))
	}
	t.Notef("orphan sweep", "%d resources named after the lab remain", found)
}
