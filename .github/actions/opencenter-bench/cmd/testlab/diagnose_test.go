package main

import (
	"strings"
	"testing"
)

// Every case below is output this CLI actually produced during testing. A
// diagnosis engine tested against invented errors diagnoses invented errors.

func TestSuccessIsNotDiagnosed(t *testing.T) {
	if diagnose("acme/demo\n", "", 0, false) != nil {
		t.Error("a successful command was given a diagnosis, which is noise")
	}
}

func TestUnknownConfigurationField(t *testing.T) {
	stderr := `Error: failed to load configuration: failed to parse YAML configuration: ` +
		`stage 1 (load): YAML type errors (4):
  - line 383: field etcd-backup not found in type v2.SecretsConfig`

	finding := diagnose("", stderr, 1, false)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	if finding.Category != "config" {
		t.Errorf("category is %q, want config", finding.Category)
	}
	if finding.Location.Line != 383 {
		t.Errorf("line is %d, want 383", finding.Location.Line)
	}
	if finding.Location.Field != "etcd-backup" {
		t.Errorf("field is %q, want etcd-backup", finding.Location.Field)
	}
	if !strings.Contains(finding.Cause, "field etcd-backup") {
		t.Errorf("the cause does not name the field: %q", finding.Cause)
	}
	if len(finding.Possible) == 0 {
		t.Error("no possible causes offered")
	}
	for _, cause := range finding.Possible {
		if cause.Check == "" {
			t.Errorf("possible cause with nothing to check it with: %q", cause.Why)
		}
	}
}

func TestPortAlreadyAllocated(t *testing.T) {
	stderr := `docker: Error response from daemon: failed to set up container networking: ` +
		`driver failed programming external connectivity on endpoint live-79641-control-plane: ` +
		`Bind for 127.0.0.1:6443 failed: port is already allocated`

	finding := diagnose("", stderr, 1, false)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	if finding.Category != "port" {
		t.Errorf("category is %q, want port", finding.Category)
	}
	joined := ""
	for _, cause := range finding.Possible {
		joined += cause.Why + " " + cause.Check + " "
	}
	if !strings.Contains(joined, "6443") {
		t.Error("the diagnosis does not mention the port that collided")
	}
	if !strings.Contains(joined, "api_server_port") {
		t.Error("the diagnosis does not offer the way out — changing the port")
	}
}

func TestFailedStepIsLocated(t *testing.T) {
	stderr := `Bootstrap started for live-80046
Log file: /tmp/live-kind/state/logs/bootstrap/livelab/live-80046/bootstrap.log
Error: provisioning infrastructure: step "kind-create" failed: command failed`

	finding := diagnose("", stderr, 1, false)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	if finding.Location.Step != "kind-create" {
		t.Errorf("step is %q, want kind-create", finding.Location.Step)
	}
	if !strings.Contains(finding.Location.Log, "bootstrap.log") {
		t.Errorf("the log file was not located: %q", finding.Location.Log)
	}
}

func TestKubeletFailureIsEnvironmental(t *testing.T) {
	stderr := `error: error execution phase wait-control-plane: failed while waiting for ` +
		`the kubelet to start: The HTTP call equal to 'curl -sSL http://127.0.0.1:10248/healthz' ` +
		`returned error: Get "http://127.0.0.1:10248/healthz": context deadline exceeded`

	finding := diagnose("", stderr, 1, false)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	if finding.Category != "environment" {
		t.Errorf("category is %q, want environment", finding.Category)
	}
	// The leading cause has to be the one that was actually proven by
	// building the same cluster with plain kind, not a guess about WSL.
	if !strings.Contains(finding.Possible[0].Check, "max_user_instances") {
		t.Errorf("the proven cause does not lead, got %q", finding.Possible[0].Check)
	}
}

// The three-node failure surfaced this second message rather than the kubelet
// one, and it has the same cause.
func TestBootstrapTimeoutSharesTheKubeletDiagnosis(t *testing.T) {
	stderr := "error: error execution phase wait-control-plane: cannot obtain client " +
		"without bootstrap: could not bootstrap the admin user in file admin.conf: " +
		"unable to create ClusterRoleBinding: client rate limiter Wait returned an " +
		"error: context deadline exceeded"

	finding := diagnose("", stderr, 1, false)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	if finding.Category != "environment" {
		t.Errorf("category is %q, want environment", finding.Category)
	}
	if !strings.Contains(finding.Possible[0].Check, "max_user_instances") {
		t.Errorf("the proven cause does not lead, got %q", finding.Possible[0].Check)
	}
}

func TestMissingClusterCarriesTheDocumentedExitCode(t *testing.T) {
	stderr := `Error: validation error: resolving cluster paths: cluster no-such-cluster ` +
		`not found in any organization`

	finding := diagnose("", stderr, 3, false)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	if !strings.Contains(finding.Possible[0].Why, "Exit code 3") {
		t.Errorf("exit 3 is documented and should lead the causes, got %q", finding.Possible[0].Why)
	}
	if finding.Possible[0].Check != "opencenter cluster list" {
		t.Errorf("the first check should be cluster list, got %q", finding.Possible[0].Check)
	}
}

// A directory has no extension, so the file patterns miss it — but the path in
// this message is the most useful thing in it.
func TestMissingDirectoryIsLocated(t *testing.T) {
	stderr := `Error: validation error: resolving cluster paths: cluster no-such-cluster ` +
		`not found in any organization (blueprints directory does not exist: ` +
		`/tmp/opencli-bench-sandbox-2616457397/config/opencenter/clusters/blueprints)`

	finding := diagnose("", stderr, 1, false)
	if finding.Location.File != "/tmp/opencli-bench-sandbox-2616457397/config/opencenter/clusters/blueprints" {
		t.Errorf("the missing directory was not located, got %q", finding.Location.File)
	}
	if finding.Location.Empty() {
		t.Error("the location reports itself as empty when it has a path")
	}
}

func TestTimeoutIsItsOwnCategory(t *testing.T) {
	finding := diagnose("", "", -1, true)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	if finding.Category != "timeout" {
		t.Errorf("category is %q, want timeout", finding.Category)
	}
	if finding.Confidence != "certain" {
		t.Errorf("a timeout is not a guess, confidence was %q", finding.Confidence)
	}
	joined := ""
	for _, cause := range finding.Possible {
		joined += cause.Why + " "
	}
	if !strings.Contains(joined, "prompt") {
		t.Error("waiting on a prompt is the commonest cause and is not offered")
	}
}

func TestSopsFailure(t *testing.T) {
	stderr := "Error: failed to load SOPS age keys: failed to load age key: failed to read public key"
	finding := diagnose("", stderr, 1, false)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	if finding.Category != "tool" {
		t.Errorf("category is %q, want tool", finding.Category)
	}
}

func TestAuthenticationFailure(t *testing.T) {
	for _, output := range []string{
		"Error: authentication failed: 401 Unauthorized",
		"Error: Unauthorized",
	} {
		finding := diagnose("", output, 1, false)
		if finding == nil || finding.Category != "credentials" {
			t.Errorf("%q was not diagnosed as a credentials problem", output)
		}
	}
}

func TestValidationIsNotTreatedAsACrash(t *testing.T) {
	stdout := `Action Items:
  1. Set gitops.auth.token.token or gitops.auth.token.token_file.`
	finding := diagnose(stdout, "Error: validation failed", 1, false)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	if finding.Category != "config" {
		t.Errorf("category is %q, want config", finding.Category)
	}
	if !strings.Contains(finding.Possible[0].Why, "incomplete") {
		t.Errorf("a fresh configuration being incomplete should lead, got %q", finding.Possible[0].Why)
	}
}

// Anything unrecognised still has to produce something a person can act on.
func TestUnrecognisedFailureStillGetsCauses(t *testing.T) {
	finding := diagnose("", "something nobody has ever seen before", 42, false)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	if len(finding.Possible) == 0 {
		t.Error("an unrecognised failure was left with no suggestions at all")
	}
	if finding.Confidence != "guess" {
		t.Errorf("an unmatched failure should say it is guessing, got %q", finding.Confidence)
	}
	if finding.Cause == "" {
		t.Error("no cause line extracted")
	}
}

// "Error: EOF" is what an interactive command produces when run from a page
// with no terminal, and on its own it explains nothing.
func TestInteractiveCommandWithNoTerminal(t *testing.T) {
	finding := diagnose("", "Error: EOF", 1, false)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	if finding.Category != "usage" {
		t.Errorf("category is %q, want usage", finding.Category)
	}
	if !strings.Contains(finding.Possible[0].Why, "interactive") {
		t.Errorf("the leading cause does not say it is interactive: %q", finding.Possible[0].Why)
	}
}

// A stray "EOF" inside a longer message is not this case.
func TestEOFInsideAnotherMessageIsNotTreatedAsInteractive(t *testing.T) {
	finding := diagnose("", "Error: reading manifest: unexpected EOF while parsing YAML", 1, false)
	if finding == nil {
		t.Fatal("no diagnosis")
	}
	for _, cause := range finding.Possible {
		if strings.Contains(cause.Why, "asked a question") {
			t.Error("a YAML parse failure was diagnosed as an interactive prompt")
		}
	}
}

// The cause has to be the line that matters, not the first or last line.
func TestCauseIsTheErrorLine(t *testing.T) {
	stdout := "Starting secrets encryption...\nSearch path: /tmp/x\nBackup created: /tmp/x.bak"
	stderr := "Error: 2 of 2 files could not be encrypted; they are still in plain text"

	finding := diagnose(stdout, stderr, 1, false)
	if !strings.Contains(finding.Cause, "could not be encrypted") {
		t.Errorf("the cause is %q, not the line that explains the failure", finding.Cause)
	}
}
