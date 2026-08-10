package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Simulation: every feature runnable before the infrastructure exists.
//
// The phases that need a live cluster cannot run on a machine without one, and
// until they can, nobody can see the reports populated, exercise the dashboard,
// review the evidence layout or demonstrate the workflow. So they can be run
// against a built-in fake instead.
//
// The danger is obvious and the design answers it directly: **a simulated run
// can never PASS.** Not "should not" — cannot. The gate refuses it before it
// looks at anything else, every phase message is prefixed, every finding is
// categorised as a bench artefact rather than a product result, and the report
// carries a banner. A test bench able to report a green release from invented
// data is worse than one with honest gaps, because the gap is visible and the
// lie is not.
//
// It has two outcomes, not one, and the difference is worth stating because it
// has already been documented wrongly once. SIMULATED means the simulation held
// together; FAIL means a phase that still runs for real failed inside it. Only
// six phases are faked — everything the machine can genuinely do still runs,
// including configure, validate-config and doctor. So an emulated profile with
// no reachable provider reaches FAIL, and that is correct: a real failure in a
// real phase is a real failure, and hiding it behind the simulation banner
// would be the exact dishonesty this file exists to prevent.
//
// Turned on with --simulate. There is no way to reach it accidentally, and no
// profile enables it.

// simulatedPrefix marks every message a simulated phase produces.
const simulatedPrefix = "SIMULATED — "

// SimulatedBodies replaces the phases that need real infrastructure.
//
// Only those. Everything the machine can genuinely do — building the CLI,
// generating a configuration, validating it, rendering artifacts, writing
// reports — still runs for real, because a simulation of work that could
// actually be done teaches nothing.
func SimulatedBodies(base map[ID]Body) map[ID]Body {
	simulated := map[ID]Body{
		PhaseDeploy:         simulateDeploy,
		PhaseInfrastructure: simulateInfrastructure,
		PhaseKubernetes:     simulateKubernetes,
		PhasePlatform:       simulatePlatform,
		PhaseSmoke:          simulateSmoke,
		PhaseFailureTests:   simulateFailureTests,
	}
	out := map[ID]Body{}
	for id, body := range base {
		out[id] = body
	}
	for id, body := range simulated {
		out[id] = body
	}
	return out
}

func simulateDeploy(ctx context.Context, ex *Exec) Outcome {
	// Registered exactly as the real phase does, so cleanup, the registry and
	// the report are exercised rather than bypassed. The resource is named as a
	// simulation so nobody hunts for a container that was never made.
	ex.Run.Register(Resource{
		Kind: "simulated-cluster", Name: ex.Run.Cluster,
		Provider: string(ex.Profile.Provider), Order: OrderKubernetes,
		Remediation: "nothing to remove — this run created no infrastructure",
	})
	steps := []string{
		"[1/8] Create Kind cluster (kind-create)",
		"[2/8] Wait for control plane (control-plane-ready)",
		"[3/8] Install CNI (cni-install)",
		"[4/8] Join workers (worker-join)",
		"[5/8] Bootstrap Flux (flux-bootstrap)",
		"[6/8] Apply infrastructure kustomization (infra-apply)",
		"[7/8] Apply platform services (platform-apply)",
		"[8/8] Wait for reconciliation (reconcile-wait)",
	}
	transcript := &strings.Builder{}
	for _, step := range steps {
		fmt.Fprintf(transcript, "  %s ✓ (simulated)\n", step)
	}
	_ = ex.Write("evidence/deploy.txt",
		[]byte("SIMULATED DEPLOY — no infrastructure was created.\n\n"+transcript.String()))

	return Outcome{State: StateWarning,
		Message: simulatedPrefix + "8 deployment steps, no infrastructure created",
		Detail:  transcript.String()}
}

func simulateInfrastructure(ctx context.Context, ex *Exec) Outcome {
	_ = ex.Write("diagnostics/provider/simulated.txt", []byte(
		"SIMULATED PROVIDER STATE — nothing here was read from a real API.\n\n"+
			"cluster        "+ex.Run.Cluster+"\n"+
			"provider       "+string(ex.Provider())+"\n"+
			"control plane  1 node   Ready (simulated)\n"+
			"workers        2 nodes  Ready (simulated)\n"))
	return Outcome{State: StateWarning,
		Message: simulatedPrefix + "3 nodes reported by a fake provider"}
}

func simulateKubernetes(ctx context.Context, ex *Exec) Outcome {
	nodes := "NAME                        STATUS   ROLES           VERSION\n" +
		ex.Run.Cluster + "-control-plane   Ready    control-plane   v1.35.0\n" +
		ex.Run.Cluster + "-worker          Ready    <none>          v1.35.0\n" +
		ex.Run.Cluster + "-worker2         Ready    <none>          v1.35.0\n"
	pods := "NAMESPACE     NAME                        READY   STATUS    RESTARTS\n" +
		"kube-system   coredns-abc                 1/1     Running   0\n" +
		"kube-system   etcd-control-plane          1/1     Running   0\n" +
		"flux-system   source-controller-xyz       1/1     Running   0\n"

	_ = ex.Write("diagnostics/kubernetes/nodes.txt", []byte("SIMULATED\n\n"+nodes))
	_ = ex.Write("diagnostics/kubernetes/pods.txt", []byte("SIMULATED\n\n"+pods))
	_ = ex.Write("diagnostics/kubernetes/events.txt", []byte("SIMULATED — no events collected\n"))

	return Outcome{State: StateWarning,
		Message: simulatedPrefix + "API reachable, 3 nodes Ready, no stuck pods, DNS up",
		Detail:  nodes}
}

func simulatePlatform(ctx context.Context, ex *Exec) Outcome {
	// Read from the configuration the earlier phases really generated, so the
	// service list is this cluster's own rather than a hardcoded one. The
	// health verdict is invented; the list is not.
	enabled := enabledServices(ctx, ex)
	if len(enabled) == 0 {
		enabled = []string{"fluxcd", "cert-manager", "kyverno", "kube-prometheus-stack"}
	}
	report := &strings.Builder{}
	fmt.Fprintf(report, "SIMULATED SERVICE HEALTH — nothing was queried.\n\n")
	for _, service := range enabled {
		fmt.Fprintf(report, "  %-28s Ready (simulated)\n", service)
	}
	_ = ex.Write("diagnostics/services/simulated.txt", []byte(report.String()))

	return Outcome{State: StateWarning,
		Message: fmt.Sprintf("%s%d enabled service(s) reported healthy by a fake",
			simulatedPrefix, len(enabled))}
}

func simulateSmoke(ctx context.Context, ex *Exec) Outcome {
	ex.Run.Register(Resource{
		Kind: "simulated-namespace", Name: "e2e-smoke", Namespace: "e2e-smoke",
		Order:       OrderSmokeResource,
		Remediation: "nothing to remove — this run created no workload",
	})
	transcript := "SIMULATED SMOKE TEST — no workload was created.\n\n" +
		"  create namespace e2e-smoke            ✓ (simulated)\n" +
		"  create deployment smoke               ✓ (simulated)\n" +
		"  wait for 1 ready replica              ✓ (simulated)\n" +
		"  expose service on 8080                ✓ (simulated)\n" +
		"  GET http://smoke:8080/echo?msg=…      ✓ (simulated)\n" +
		"  delete namespace                      ✓ (simulated)\n"
	_ = ex.Write("diagnostics/smoke/simulated.txt", []byte(transcript))
	ex.Run.MarkRemoved("simulated-namespace", "e2e-smoke")

	return Outcome{State: StateWarning,
		Message: simulatedPrefix + "deployed, resolved by name, called and answered",
		Detail:  transcript}
}

func simulateFailureTests(ctx context.Context, ex *Exec) Outcome {
	// The two scenarios that need nothing are run for real even here, because a
	// simulated result is worth less than a real one whenever a real one is
	// available. Only the cluster-dependent scenario is faked.
	real := phaseFailureTests(ctx, ex)
	return Outcome{
		State:   StateWarning,
		Message: simulatedPrefix + real.Message + "; cluster scenarios not exercised",
		Detail:  real.Detail, Findings: real.Findings,
	}
}

// Provider is the run's provider, for the simulated transcripts.
func (e *Exec) Provider() Provider { return e.Profile.Provider }

// simulatedVerdict is the gate's answer for a simulated run.
//
// Deliberately its own verdict rather than a PASS with a footnote. A footnote is
// something a dashboard truncates, a script ignores and a screenshot crops out.
func (r *Run) simulatedVerdict() (Verdict, string) {
	failed := 0
	for _, phase := range r.Phases {
		if phase.State == StateFailed {
			failed++
		}
	}
	if failed > 0 {
		return VerdictFail, fmt.Sprintf(
			"%d phase(s) failed for real, in a simulated run", failed)
	}
	return VerdictSimulated,
		"the phases needing infrastructure were simulated — this run proves nothing " +
			"about a real cluster and cannot gate a release"
}

func simulatedBanner(run *Run) string {
	if !run.Simulated {
		return ""
	}
	return "This run used --simulate. Deploy, infrastructure, Kubernetes, platform, " +
		"smoke and failure-tests were produced by a built-in fake at " +
		run.Started.Format(time.RFC3339) + ". Nothing was deployed and nothing was " +
		"measured. Every other phase ran for real, so a failure reported outside " +
		"those six is a real one. Either way this cannot be used as release evidence."
}
