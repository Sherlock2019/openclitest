package redact

import (
	"strings"
	"testing"
)

// The redactor is the last thing between a credential and a report that gets
// attached to a ticket, so it is tested harder than anything else here.

func TestRegisteredValueIsRemovedEverywhere(t *testing.T) {
	redactor := New()
	redactor.Add("hunter2-not-a-real-password")

	for _, input := range []string{
		"password is hunter2-not-a-real-password",
		"OS_PASSWORD=hunter2-not-a-real-password",
		`{"secret":"hunter2-not-a-real-password"}`,
		"prefixhunter2-not-a-real-passwordsuffix",
	} {
		out := redactor.String(input)
		if strings.Contains(out, "hunter2-not-a-real-password") {
			t.Errorf("the value survived redaction of %q: %q", input, out)
		}
		if !strings.Contains(out, Placeholder) {
			t.Errorf("nothing marked the removal in %q: %q", input, out)
		}
	}
}

// A short value cannot be removed without mangling ordinary prose, so it is
// deliberately left alone. This test records that decision rather than letting
// it be discovered by surprise.
func TestVeryShortValuesAreNotRegistered(t *testing.T) {
	redactor := New()
	redactor.Add("abc")
	out := redactor.String("the abc of it")
	if out != "the abc of it" {
		t.Errorf("a three-character value was redacted and mangled the text: %q", out)
	}
}

func TestLongerSecretIsRemovedBeforeAShorterOneInsideIt(t *testing.T) {
	redactor := New()
	redactor.Add("secret-value", "secret-value-with-suffix")

	out := redactor.String("token=secret-value-with-suffix")
	if strings.Contains(out, "with-suffix") {
		t.Errorf("the longer secret left a fragment behind: %q", out)
	}
}

func TestPatternsCatchWhatWasNeverRegistered(t *testing.T) {
	redactor := New()

	cases := []struct {
		name  string
		input string
		gone  string
	}{
		{"authorization header", "Authorization: Bearer abcdef1234567890", "abcdef1234567890"},
		{"subject token", "X-Subject-Token: gAAAAABm-longtokenvalue", "gAAAAABm-longtokenvalue"},
		{"age private key", "key: AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQ", "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQ"},
		{"url credential", "https://user:supersecretpw@example.com/repo.git", "supersecretpw"},
		{"password assignment", `"password": "correcthorsebattery"`, "correcthorsebattery"},
		{"github token", "token ghp_abcdefghijklmnopqrstuvwxyz012345", "ghp_abcdefghijklmnopqrstuvwxyz012345"},
		{"kubeconfig client key", "client-key-data: LS0tLS1CRUdJTlBSSVZBVEU", "LS0tLS1CRUdJTlBSSVZBVEU"},
	}

	for _, testCase := range cases {
		out := redactor.String(testCase.input)
		if strings.Contains(out, testCase.gone) {
			t.Errorf("%s survived: %q", testCase.name, out)
		}
	}
}

// Redaction must not change the shape of what it redacts. A pattern whose gap
// matched a newline once turned `secrets:` plus an indented `backend: sops`
// into `secrets:[REDACTED]`, which meant a recorded YAML document no longer
// parsed — and the bench reported that as a defect in the product.
func TestRedactionPreservesLineStructure(t *testing.T) {
	redactor := New()

	document := `opencenter:
    cluster:
        admin_email: admin@example.com
    secrets:
        backend: sops
        keycloak:
            admin_password: changeme-please
    services:
        loki:
            token: abcdefghijklmnop
`
	out := redactor.String(document)

	if strings.Count(out, "\n") != strings.Count(document, "\n") {
		t.Errorf("redaction changed the line count:\n%s", out)
	}
	for _, structural := range []string{"secrets:", "backend: sops", "services:", "keycloak:"} {
		if !strings.Contains(out, structural) {
			t.Errorf("redaction ate the structural line %q:\n%s", structural, out)
		}
	}
	if strings.Contains(out, "changeme-please") || strings.Contains(out, "abcdefghijklmnop") {
		t.Errorf("a secret survived:\n%s", out)
	}
	// The key stays, so a reader can still see what was removed.
	if !strings.Contains(out, "admin_password: "+Placeholder) {
		t.Errorf("the redacted assignment lost its key:\n%s", out)
	}
}

// The same property for the header pattern: an Authorization line must not
// swallow the line after it.
func TestHeaderRedactionStopsAtTheLineEnd(t *testing.T) {
	out := New().String("Authorization: Bearer abcdef1234567890\nContent-Type: application/json\n")
	if !strings.Contains(out, "Content-Type: application/json") {
		t.Errorf("the following line was swallowed:\n%s", out)
	}
	if strings.Contains(out, "abcdef1234567890") {
		t.Errorf("the token survived:\n%s", out)
	}
}

func TestPrivateKeyBlockIsRemovedWhole(t *testing.T) {
	redactor := New()
	input := `before
-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAA
AAAAAAABAAABlwAAAAdzc2gtcn
-----END OPENSSH PRIVATE KEY-----
after`
	out := redactor.String(input)
	if strings.Contains(out, "b3BlbnNzaC1rZXktdjE") {
		t.Errorf("key material survived: %q", out)
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Errorf("redaction ate the surrounding text: %q", out)
	}
}

func TestIsSecretName(t *testing.T) {
	secret := []string{
		"OS_PASSWORD", "OS_APPLICATION_CREDENTIAL_SECRET", "GITHUB_TOKEN",
		"SOPS_AGE_KEY", "api_key", "VSPHERE_PASSWORD", "OS_TOKEN",
	}
	notSecret := []string{
		"OS_AUTH_URL", "OS_REGION_NAME", "HOME", "PATH", "OS_CLOUD", "KUBECONFIG",
	}

	for _, name := range secret {
		if !IsSecretName(name) {
			t.Errorf("%s should be treated as a secret", name)
		}
	}
	for _, name := range notSecret {
		if IsSecretName(name) {
			t.Errorf("%s is not a secret and hiding it makes reports harder to read", name)
		}
	}
}

// Evidence records which variables a command ran with, never what was in them.
func TestEnvNamesKeepsNamesAndDropsValues(t *testing.T) {
	names := EnvNames([]string{
		"HOME=/tmp/run/home",
		"OS_PASSWORD=hunter2-not-a-real-password",
		"OS_AUTH_URL=https://keystone.example.com/v3",
	})
	joined := strings.Join(names, " ")

	if strings.Contains(joined, "hunter2-not-a-real-password") {
		t.Errorf("a secret value reached the evidence: %v", names)
	}
	if strings.Contains(joined, "/tmp/run/home") {
		t.Errorf("EnvNames should record names, not values: %v", names)
	}
	if !strings.Contains(joined, "OS_PASSWORD") || !strings.Contains(joined, "OS_AUTH_URL") {
		t.Errorf("the variable names were lost: %v", names)
	}
}

func TestAddFromEnvRegistersOnlySecrets(t *testing.T) {
	redactor := New()
	redactor.AddFromEnv(map[string]string{
		"OS_PASSWORD":    "hunter2-not-a-real-password",
		"OS_AUTH_URL":    "https://keystone.example.com/v3",
		"OS_REGION_NAME": "IAD3",
	})

	out := redactor.String("url=https://keystone.example.com/v3 password=hunter2-not-a-real-password region=IAD3")
	if strings.Contains(out, "hunter2-not-a-real-password") {
		t.Errorf("the password survived: %q", out)
	}
	if !strings.Contains(out, "keystone.example.com") {
		t.Errorf("the endpoint was redacted; a report without endpoints is hard to act on: %q", out)
	}
	if !strings.Contains(out, "IAD3") {
		t.Errorf("the region was redacted: %q", out)
	}
}

// Leaked is what the bench uses to test itself: give it the values it should
// have removed and it says which ones are still there.
func TestLeakedReportsSurvivors(t *testing.T) {
	redactor := New()
	redactor.Add("removed-value-here")

	leaked := redactor.Leaked("removed-value-here and kept-value-here", "removed-value-here", "kept-value-here")
	if len(leaked) != 1 || leaked[0] != "kept-value-here" {
		t.Errorf("Leaked returned %v, want only the unregistered value", leaked)
	}
}

func TestRedactionIsIdempotent(t *testing.T) {
	redactor := New()
	redactor.Add("hunter2-not-a-real-password")

	once := redactor.String("password: hunter2-not-a-real-password")
	twice := redactor.String(once)
	if once != twice {
		t.Errorf("redacting twice changed the text: %q then %q", once, twice)
	}
}

func TestEmptyInputIsUntouched(t *testing.T) {
	if out := New().String(""); out != "" {
		t.Errorf("empty input became %q", out)
	}
}
