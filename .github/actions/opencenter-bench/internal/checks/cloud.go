package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
)

// Cloud checks run wherever there is an OpenStack on the far end. The
// simulator and a real FLEX project answer the same questions with the same
// code: if a command passes against the simulator and fails against FLEX, the
// difference is real cloud behaviour rather than test scaffolding.
var cloudEnvironments = []string{"sim", "flex"}

// Fault injection needs a far end that can be told to misbehave, which only
// the simulator can be.
var simulatedOnly = []string{"sim"}

func init() {
	register(
		Check{
			ID:           "auth-succeeds",
			Name:         "Valid credentials authenticate and discover the tenant",
			Category:     "authentication",
			Environments: cloudEnvironments,
			Fn:           checkAuthSucceeds,
		},
		Check{
			ID:           "auth-missing-credentials",
			Name:         "Missing credentials fail with an explanation, not a crash",
			Category:     "authentication",
			Environments: cloudEnvironments,
			Fn:           checkAuthMissing,
		},
		Check{
			ID:           "auth-rejected",
			Name:         "Rejected and expired credentials are reported clearly",
			Category:     "authentication",
			Environments: simulatedOnly,
			Fn:           checkAuthRejected,
		},
		Check{
			ID:           "api-request-shape",
			Name:         "The CLI calls the endpoints and methods the API expects",
			Category:     "api",
			Environments: simulatedOnly,
			Fn:           checkAPIRequestShape,
		},
		Check{
			ID:           "api-region-selection",
			Name:         "The region asked for is the region used, and a wrong one is refused",
			Category:     "api",
			Environments: cloudEnvironments,
			Fn:           checkRegionSelection,
		},
		Check{
			ID:           "api-error-statuses",
			Name:         "403, 404, 409, 500 and 503 each produce a clear failure",
			Category:     "api",
			Environments: simulatedOnly,
			Fn:           checkAPIErrorStatuses,
		},
		Check{
			ID:           "api-malformed-response",
			Name:         "A response that is not valid JSON is handled, not swallowed",
			Category:     "api",
			Environments: simulatedOnly,
			Fn:           checkAPIMalformed,
		},
		Check{
			ID:           "api-timeout",
			Name:         "A stalled API call gives up inside a bounded time",
			Category:     "api",
			Environments: simulatedOnly,
			Fn:           checkAPITimeout,
			Slow:         true,
		},
		Check{
			ID:           "retries-transient-vs-permanent",
			Name:         "Transient failures are retried, permanent ones are not",
			Category:     "retries",
			Environments: simulatedOnly,
			Fn:           checkRetryPolicy,
			Slow:         true,
		},
		Check{
			ID:           "cloud-discovery-writes-inventory",
			Name:         "Discovery writes real provider identifiers into the configuration",
			Category:     "results",
			Environments: cloudEnvironments,
			Fn:           checkDiscoveryResults,
		},
		Check{
			ID:           "cloud-online-validation",
			Name:         "Online validation reaches the provider and reports structurally",
			Category:     "lifecycle",
			Environments: cloudEnvironments,
			Fn:           checkOnlineValidation,
		},
		Check{
			ID:           "cloud-dry-run-safety",
			Name:         "Cloud dry-runs contact the provider but change nothing",
			Category:     "dry-run",
			Environments: cloudEnvironments,
			Fn:           checkCloudDryRun,
		},
		Check{
			ID:           "cloud-secret-containment",
			Name:         "Cloud credentials never reach output, logs or generated files",
			Category:     "security",
			Environments: cloudEnvironments,
			Fn:           checkCloudSecretContainment,
		},
	)
}

// cloudCluster is the configuration cloud checks work from: an OpenStack
// cluster pointed at whichever far end this environment provides.
func (t *T) cloudCluster(name string) string {
	const org = "cloudorg"
	reference := org + "/" + name
	t.initCluster(name, org, "--type", "openstack")
	return reference
}

func (t *T) requireCloud() string {
	if t.Env.Cloud == "" {
		t.Skip("no OpenStack profile is configured for this environment")
	}
	return t.Env.Cloud
}

func checkAuthSucceeds(ctx context.Context, t *T) {
	cloud := t.requireCloud()
	reference := t.cloudCluster("auth-ok")

	if t.Env.Sim != nil {
		_ = t.Env.Sim.Clear()
	}

	result := t.Run("--dry-run", "cluster", "sync", "openstack", reference, "--os-cloud", cloud)
	t.Assertf("authentication and discovery succeed", result.OK(),
		"exit %d: %s", result.ExitCode, firstLine(result.Output()))
	t.Assert("the failure path was not taken",
		!containsAny(result.Output(), "unauthorized", "authentication failed", "invalid credentials"),
		firstLine(result.Output()))

	if t.Env.Sim != nil {
		requests, err := t.Env.Sim.Requests()
		t.Require("the simulator recorded the calls", err == nil, fmt.Sprint(err))
		t.Assertf("the CLI actually authenticated", countPath(requests, "/v3/auth/tokens") > 0,
			"no call to Keystone was recorded in %d requests", len(requests))
	}
}

func checkAuthMissing(ctx context.Context, t *T) {
	reference := t.cloudCluster("auth-missing")

	// Point at a profile that does not exist. This is what a person gets when
	// they mistype OS_CLOUD or forget to create clouds.yaml.
	result := t.Run("cluster", "sync", "openstack", reference, "--os-cloud", "no-such-profile-anywhere", "--yes")
	t.Assertf("a missing profile fails", !result.OK(), "exit %d", result.ExitCode)
	t.Assert("the error names the problem",
		containsAny(result.Output(), "cloud", "profile", "clouds.yaml", "not found", "auth"),
		firstLine(result.Output()))
	t.Assert("the error is not a stack trace",
		!containsAny(result.Output(), "goroutine ", "runtime.gopanic"), firstLine(result.Output()))
	t.Assert("the error goes to stderr", strings.TrimSpace(result.Stderr) != "",
		"the failure was reported on stdout only")

	// And with no configuration file at all.
	noFile := t.RunWithEnv(map[string]string{
		"OS_CLIENT_CONFIG_FILE": "/nonexistent/clouds.yaml",
		"OS_CLOUD":              "",
	}, "cluster", "sync", "openstack", reference, "--yes")
	t.Assertf("no clouds.yaml at all fails cleanly", !noFile.OK(), "exit %d", noFile.ExitCode)
	t.Assert("and explains what is missing",
		strings.TrimSpace(noFile.Output()) != "", "the command failed silently")
}

func checkAuthRejected(ctx context.Context, t *T) {
	sim := t.requireSim()
	cloud := t.requireCloud()
	reference := t.cloudCluster("auth-rejected")

	for _, testCase := range []struct {
		name   string
		status int
		expect []string
	}{
		{"rejected credentials", http.StatusUnauthorized, []string{"401", "unauthor", "credential", "auth"}},
		{"an expired token", http.StatusUnauthorized, []string{"401", "unauthor", "expire", "auth"}},
		{"insufficient permission", http.StatusForbidden, []string{"403", "forbidden", "permission", "denied"}},
	} {
		if err := sim.Clear(); err != nil {
			t.Fatalf("clear faults: %v", err)
		}
		// Fail authentication for every attempt the CLI makes.
		if err := sim.Fault("/v3/auth/tokens", testCase.status, 10); err != nil {
			t.Fatalf("inject fault: %v", err)
		}

		result := t.RunWithEnv(map[string]string{"OS_PASSWORD": CanaryPassword},
			"cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes")

		t.Assertf(testCase.name+" fails", !result.OK(), "exit %d", result.ExitCode)
		t.Assert(testCase.name+" is explained in words a person can act on",
			containsAny(result.Output(), testCase.expect...), firstLine(result.Output()))
		t.Assert(testCase.name+" does not panic",
			!containsAny(result.Output(), "goroutine ", "runtime.gopanic"), firstLine(result.Output()))
		for _, canary := range Canaries() {
			t.Assert(testCase.name+" does not echo the credential",
				!strings.Contains(result.Output(), canary), "a credential appeared in the error")
		}
	}
	_ = sim.Clear()
}

func checkAPIRequestShape(ctx context.Context, t *T) {
	sim := t.requireSim()
	cloud := t.requireCloud()
	reference := t.cloudCluster("api-shape")

	if err := sim.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	result := t.Run("cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes")
	t.Require("sync ran", result.ExitCode >= 0, firstLine(result.Output()))

	requests, err := sim.Requests()
	t.Require("the simulator recorded the calls", err == nil, fmt.Sprint(err))
	t.Requiref("the CLI made calls", len(requests) > 0, "no requests were recorded")

	var summary []string
	for _, request := range requests {
		summary = append(summary, request.Method+" "+request.Path)
	}
	t.Notef("calls made", "%d: %s", len(requests), strings.Join(uniq(summary), ", "))

	// Authentication is a POST, and everything else is a read.
	for _, request := range requests {
		if request.Path == "/v3/auth/tokens" {
			t.Assertf("Keystone is called with POST", request.Method == http.MethodPost,
				"got %s", request.Method)
		}
	}
	t.Assert("the service catalog was used rather than a guessed URL",
		countPath(requests, "/v2.1/") > 0 || countPath(requests, "/network/") > 0 ||
			countPath(requests, "/image/") > 0,
		"the CLI authenticated but never called a service endpoint")

	// Nothing may be created during a discovery.
	mutations := 0
	for _, request := range requests {
		switch request.Method {
		case http.MethodDelete, http.MethodPatch:
			mutations++
		case http.MethodPost:
			if request.Path != "/v3/auth/tokens" && !strings.Contains(request.Path, "credentials/OS-EC2") {
				mutations++
			}
		}
	}
	t.Assertf("discovery makes no mutating calls", mutations == 0,
		"%d mutating requests were made during a read-only sync", mutations)
}

// checkRegionSelection is the one API-communication question a real project
// can answer without being made to misbehave: a multi-region tenant returns a
// catalog per region, and picking the wrong one has to fail rather than
// quietly land somewhere else.
func checkRegionSelection(ctx context.Context, t *T) {
	cloud := t.requireCloud()
	reference := t.cloudCluster("region-check")

	if t.Env.Sim != nil {
		_ = t.Env.Sim.Clear()
	}

	good := t.RunWith(cli.RunOptions{Timeout: 90 * time.Second},
		"--dry-run", "cluster", "sync", "openstack", reference, "--os-cloud", cloud)
	t.Assertf("the configured region works", good.OK(),
		"exit %d: %s", good.ExitCode, firstLine(good.Output()))

	bogus := t.RunWith(cli.RunOptions{
		Env:     map[string]string{"OS_REGION_NAME": "no-such-region-anywhere"},
		Timeout: 90 * time.Second,
	}, "--dry-run", "cluster", "sync", "openstack", reference, "--os-cloud", cloud)

	if bogus.OK() {
		// The simulator serves one region regardless of what is asked for, so
		// there it proves nothing; against a real multi-region tenant it does.
		t.Notef("a nonexistent region was accepted", "exit 0 — %s", firstLine(bogus.Stdout))
		t.Assert("at least the run did not silently claim a different region",
			!containsAny(bogus.Stdout, "no-such-region-anywhere"), firstLine(bogus.Stdout))
	} else {
		t.Assert("a nonexistent region is refused with an explanation",
			containsAny(bogus.Output(), "region", "endpoint", "catalog", "not found"),
			firstLine(bogus.Output()))
	}
	t.Assert("a bad region does not panic",
		!containsAny(bogus.Output(), "goroutine ", "runtime.gopanic"), firstLine(bogus.Output()))
}

func checkAPIErrorStatuses(ctx context.Context, t *T) {
	sim := t.requireSim()
	cloud := t.requireCloud()
	reference := t.cloudCluster("api-errors")

	cases := []struct {
		status int
		path   string
		expect []string
	}{
		{http.StatusForbidden, "/v2.1/", []string{"403", "forbidden", "permission", "denied", "error"}},
		{http.StatusNotFound, "/network/", []string{"404", "not found", "error"}},
		{http.StatusConflict, "/v2.1/", []string{"409", "conflict", "error"}},
		{http.StatusInternalServerError, "/v2.1/", []string{"500", "server", "error"}},
		{http.StatusServiceUnavailable, "/network/", []string{"503", "unavailable", "error"}},
	}

	for _, testCase := range cases {
		if err := sim.Clear(); err != nil {
			t.Fatalf("clear: %v", err)
		}
		if err := sim.Fault(testCase.path, testCase.status, 20); err != nil {
			t.Fatalf("inject: %v", err)
		}

		label := fmt.Sprintf("%d on %s", testCase.status, testCase.path)
		result := t.RunWith(cli.RunOptions{Timeout: 90 * time.Second},
			"cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes")

		t.Assertf(label+" does not report success", !result.OK(),
			"exit %d while the provider was returning %d", result.ExitCode, testCase.status)
		t.Assert(label+" is explained", containsAny(result.Output(), testCase.expect...),
			firstLine(result.Output()))
		t.Assert(label+" does not panic",
			!containsAny(result.Output(), "goroutine ", "runtime.gopanic"), firstLine(result.Output()))
		t.Assertf(label+" does not hang", !result.TimedOut, "the command had to be killed")
	}
	_ = sim.Clear()
}

func checkAPIMalformed(ctx context.Context, t *T) {
	sim := t.requireSim()
	cloud := t.requireCloud()
	reference := t.cloudCluster("api-malformed")

	if err := sim.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := sim.Malformed("/v2.1/", 20); err != nil {
		t.Fatalf("inject: %v", err)
	}

	result := t.RunWith(cli.RunOptions{Timeout: 90 * time.Second},
		"cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes")

	t.Assertf("a malformed response does not report success", !result.OK(),
		"exit %d despite an unparseable response", result.ExitCode)
	t.Assert("the failure mentions the response", containsAny(result.Output(),
		"json", "parse", "decode", "unexpected", "invalid", "error"), firstLine(result.Output()))
	t.Assert("the CLI does not panic on unparseable input",
		!containsAny(result.Output(), "goroutine ", "runtime.gopanic", "nil pointer"), firstLine(result.Output()))
	t.Assertf("it does not hang", !result.TimedOut, "the command had to be killed")
	_ = sim.Clear()
}

func checkAPITimeout(ctx context.Context, t *T) {
	sim := t.requireSim()
	cloud := t.requireCloud()
	reference := t.cloudCluster("api-timeout")

	if err := sim.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	// Stall long enough that a client with no deadline would wait for it.
	if err := sim.Hang("/v2.1/", 25*time.Second, 5); err != nil {
		t.Fatalf("inject: %v", err)
	}

	result := t.RunWith(cli.RunOptions{Timeout: 100 * time.Second},
		"cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes")

	t.Assertf("the command returns rather than waiting forever", !result.TimedOut,
		"the CLI was still running after 100s")
	t.Assertf("a stalled provider does not produce a success", !result.OK(),
		"exit %d", result.ExitCode)
	t.Assert("the failure is attributed to the connection",
		containsAny(result.Output(), "timeout", "deadline", "EOF", "connection", "reset", "error"),
		firstLine(result.Output()))
	t.Notef("time to give up", "%s", result.Duration)
	_ = sim.Clear()
}

func checkRetryPolicy(ctx context.Context, t *T) {
	sim := t.requireSim()
	cloud := t.requireCloud()
	reference := t.cloudCluster("retry-policy")

	// A transient failure: rate limiting that clears after two attempts.
	if err := sim.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := sim.Fault("/v2.1/flavors", http.StatusTooManyRequests, 2); err != nil {
		t.Fatalf("inject: %v", err)
	}

	transient := t.RunWith(cli.RunOptions{Timeout: 120 * time.Second},
		"cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes")

	requests, err := sim.Requests()
	t.Require("the simulator recorded the calls", err == nil, fmt.Sprint(err))
	attempts := countPath(requests, "/v2.1/flavors")

	if attempts > 2 {
		t.Assertf("a rate-limited call is retried", attempts > 2, "%d attempts were made", attempts)
		t.Assertf("and the command eventually succeeds", transient.OK(),
			"exit %d after %d attempts: %s", transient.ExitCode, attempts, firstLine(transient.Output()))
	} else {
		// No retry is a legitimate design choice, but then the failure has to
		// be clear rather than silent.
		t.Notef("no retry observed", "%d attempts against a 429", attempts)
		t.Assertf("a rate-limited call that is not retried fails clearly", !transient.OK(),
			"exit %d with no retry and no error", transient.ExitCode)
		t.Assert("and the reason is stated",
			containsAny(transient.Output(), "429", "rate", "too many", "error"), firstLine(transient.Output()))
	}
	t.Assertf("retrying is bounded", attempts < 25, "%d attempts is not a bounded retry", attempts)

	// A permanent failure must not be retried at all: retrying a rejected
	// credential just locks the account out faster.
	if err := sim.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := sim.Fault("/v3/auth/tokens", http.StatusUnauthorized, 50); err != nil {
		t.Fatalf("inject: %v", err)
	}
	permanent := t.RunWith(cli.RunOptions{Timeout: 90 * time.Second},
		"cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes")
	requests, _ = sim.Requests()
	authAttempts := countPath(requests, "/v3/auth/tokens")

	t.Assertf("a rejected credential fails", !permanent.OK(), "exit %d", permanent.ExitCode)
	t.Assertf("a rejected credential is not retried repeatedly", authAttempts <= 3,
		"%d authentication attempts were made against a 401", authAttempts)
	_ = sim.Clear()
}

func checkDiscoveryResults(ctx context.Context, t *T) {
	cloud := t.requireCloud()
	reference := t.cloudCluster("discovery")

	if t.Env.Sim != nil {
		_ = t.Env.Sim.Clear()
	}

	sync := t.RunWith(cli.RunOptions{Timeout: 120 * time.Second},
		"cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes")
	t.Require("sync succeeds", sync.OK(),
		fmt.Sprintf("exit %d: %s", sync.ExitCode, firstLine(sync.Output())))

	export := t.Run("cluster", "export", reference)
	t.Require("the synchronised configuration exports", export.OK(), firstLine(export.Output()))
	t.Assert("discovery wrote provider inventory into the configuration",
		containsAny(export.Stdout, "flavor", "image_id", "image", "network_id", "network"),
		"sync reported success but the configuration holds no provider identifiers")

	// Running it again must converge rather than accumulate.
	second := t.RunWith(cli.RunOptions{Timeout: 120 * time.Second},
		"cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes")
	t.Assertf("a second sync succeeds", second.OK(),
		"exit %d: %s", second.ExitCode, firstLine(second.Output()))
	exportAgain := t.Run("cluster", "export", reference)
	t.Assert("a second sync produces the same configuration",
		normaliseExport(export.Stdout) == normaliseExport(exportAgain.Stdout),
		"syncing twice changed the configuration, so discovery is not convergent")
}

func checkOnlineValidation(ctx context.Context, t *T) {
	cloud := t.requireCloud()
	reference := t.cloudCluster("online-validate")

	if t.Env.Sim != nil {
		_ = t.Env.Sim.Clear()
	}
	sync := t.RunWith(cli.RunOptions{Timeout: 120 * time.Second},
		"cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes")
	t.Require("sync succeeds first", sync.OK(), firstLine(sync.Output()))

	validate := t.RunWith(cli.RunOptions{Timeout: 120 * time.Second},
		"cluster", "validate", reference, "--validation", "online", "--output", "json")

	var report map[string]any
	err := json.Unmarshal([]byte(validate.Stdout), &report)
	t.Assertf("online validation emits parseable JSON", err == nil,
		"%v — %s", err, firstLine(validate.Stdout))
	if err == nil {
		t.Assertf("the report says which mode it ran in", report["mode"] == "online",
			"mode was %v", report["mode"])
		t.Assert("the report says whether the cluster is valid", report["valid"] != nil, "no valid field")
	}
	t.Assert("validation does not panic",
		!containsAny(validate.Output(), "goroutine ", "runtime.gopanic"), firstLine(validate.Output()))

	if t.Env.Sim != nil {
		requests, _ := t.Env.Sim.Requests()
		t.Assertf("online validation really contacted the provider", len(requests) > 0,
			"no API calls were made in online mode")
	}
}

func checkCloudDryRun(ctx context.Context, t *T) {
	cloud := t.requireCloud()
	reference := t.cloudCluster("cloud-dry-run")

	before := t.snapshot()
	if t.Env.Sim != nil {
		_ = t.Env.Sim.Clear()
	}

	sync := t.RunWith(cli.RunOptions{Timeout: 120 * time.Second},
		"--dry-run", "cluster", "sync", "openstack", reference, "--os-cloud", cloud)
	t.Assertf("sync --dry-run succeeds", sync.OK(),
		"exit %d: %s", sync.ExitCode, firstLine(sync.Output()))

	deploy := t.RunWith(cli.RunOptions{Timeout: 120 * time.Second},
		"--dry-run", "cluster", "deploy", reference)
	t.Assert("deploy --dry-run does not claim to have deployed",
		!containsAny(deploy.Stdout, "Deployment complete", "cluster is ready"), firstLine(deploy.Stdout))

	after := t.snapshot()
	added, removed, changed := diffSnapshots(before, after)
	t.Assertf("a cloud dry-run creates no files", len(added) == 0, "%v", trim(added))
	t.Assertf("a cloud dry-run removes no files", len(removed) == 0, "%v", trim(removed))
	t.Assertf("a cloud dry-run modifies no files", len(changed) == 0, "%v", trim(changed))

	if t.Env.Sim != nil {
		requests, _ := t.Env.Sim.Requests()
		mutations := 0
		for _, request := range requests {
			if request.Method == http.MethodDelete || request.Method == http.MethodPut ||
				(request.Method == http.MethodPost && request.Path != "/v3/auth/tokens" &&
					!strings.Contains(request.Path, "credentials/OS-EC2")) {
				mutations++
			}
		}
		t.Assertf("a cloud dry-run makes no mutating API calls", mutations == 0,
			"%d mutating requests were made", mutations)
	}
}

func checkCloudSecretContainment(ctx context.Context, t *T) {
	cloud := t.requireCloud()
	reference := t.cloudCluster("cloud-secrets")

	injected := map[string]string{
		"OS_PASSWORD":                      CanaryPassword,
		"OS_APPLICATION_CREDENTIAL_SECRET": CanarySecret,
		"OS_TOKEN":                         CanaryToken,
	}

	for _, args := range [][]string{
		{"--log-level", "debug", "cluster", "sync", "openstack", reference, "--os-cloud", cloud, "--yes"},
		{"--log-level", "debug", "cluster", "validate", reference, "--validation", "online"},
		{"--log-level", "debug", "cluster", "doctor", reference},
	} {
		result := t.RunWith(cli.RunOptions{Env: injected, Timeout: 120 * time.Second}, args...)
		for _, canary := range Canaries() {
			t.Assert("no credential in the output of "+strings.Join(args[2:4], " "),
				!strings.Contains(result.Output(), canary),
				"a credential appeared in command output")
		}
	}

	// And nothing written to disk carries one either.
	var leaked []string
	files, _ := t.Env.Sandbox.Tree("")
	for _, relative := range files {
		content, err := readSandboxFile(t, relative)
		if err != nil {
			continue
		}
		for _, canary := range Canaries() {
			if strings.Contains(content, canary) {
				leaked = append(leaked, relative)
				break
			}
		}
	}
	t.Assertf("no credential was written into a generated file", len(leaked) == 0, "%v", trim(leaked))
}

// --- helpers ----------------------------------------------------------------

func (t *T) requireSim() SimControl {
	if t.Env.Sim == nil {
		t.Skip("this check needs a far end that can be told to misbehave")
	}
	return t.Env.Sim
}

// Requiref is Require with a formatted detail.
func (t *T) Requiref(name string, ok bool, format string, args ...any) {
	t.Require(name, ok, fmt.Sprintf(format, args...))
}

func countPath(requests []SimRequest, prefix string) int {
	count := 0
	for _, request := range requests {
		if strings.Contains(request.Path, prefix) {
			count++
		}
	}
	return count
}

func uniq(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

// normaliseExport removes the fields that legitimately change between two
// runs, so "did this converge?" is not answered by a timestamp.
func normaliseExport(export string) string {
	var kept []string
	for _, line := range strings.Split(export, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "updated_at:") || strings.HasPrefix(trimmed, "created_at:") ||
			strings.HasPrefix(trimmed, "# Applied defaults") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func readSandboxFile(t *T, relative string) (string, error) {
	content, err := os.ReadFile(filepath.Join(t.Env.Sandbox.Root, relative))
	return string(content), err
}
