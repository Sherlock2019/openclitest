// Package redact removes secrets from anything on its way out of the bench.
//
// Everything the workflow displays, logs, stores or exports goes through here
// first: the console, the run log, the evidence files and all four report
// formats. It is deliberately one chokepoint rather than a habit spread across
// call sites, because a habit gets forgotten exactly once and that is enough.
//
// Redaction is not switchable off. A setting that can hide a leak is a setting
// that will eventually be used to hide a leak.
package redact

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Placeholder is what a removed value is replaced with.
const Placeholder = "[REDACTED]"

// Patterns that catch a secret nobody registered: an Authorization header, a
// private key block, a token in a URL. Registration by value is the primary
// mechanism; these are the safety net for what the bench never saw.
//
// Every gap between a key and its value is [ \t]* rather than \s*. That is not
// a style choice. \s matches a newline, so `secrets:` followed by an indented
// `backend: sops` on the next line looks exactly like `secrets: backend`, and
// redacting it deletes the line break — turning a recorded YAML document into
// something that no longer parses, and turning a passing check into a
// mysterious failure in the product.
var patterns = []struct {
	name string
	expr *regexp.Regexp
	// replacement keeps the shape of the line so a person can still tell what
	// kind of thing was removed.
	replacement string
}{
	{
		// The whole rest of the line, not the first word: `Authorization:
		// Bearer <token>` would otherwise lose "Bearer" and keep the token.
		"authorization header",
		regexp.MustCompile(`(?i)((?:authorization|x-auth-token|x-subject-token|proxy-authorization)[ \t]*[:=][ \t]*)([^\r\n]+)`),
		"${1}" + Placeholder,
	},
	{
		"private key block",
		regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
		Placeholder,
	},
	{
		"age private key",
		regexp.MustCompile(`AGE-SECRET-KEY-1[0-9A-Za-z]+`),
		Placeholder,
	},
	{
		"credential in a URL",
		regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+):([^\s@/]+)@`),
		"${1}:" + Placeholder + "@",
	},
	{
		"secret-shaped assignment",
		regexp.MustCompile(`(?i)((?:password|passwd|secret|token|api[_-]?key|client[_-]?secret|access[_-]?key)"?[ \t]*[:=][ \t]*"?)([^\s",;}]{6,})`),
		"${1}" + Placeholder,
	},
	{
		"kubeconfig client key",
		regexp.MustCompile(`(?i)(client-key-data[ \t]*:[ \t]*)(\S+)`),
		"${1}" + Placeholder,
	},
	{
		"github token",
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`),
		Placeholder,
	},
}

// Redactor holds the values registered for this run.
//
// It is safe for concurrent use: the run writes to it while the console reads
// through it.
type Redactor struct {
	mu     sync.RWMutex
	values []string
}

// New returns an empty redactor. The pattern rules apply immediately; values
// are added as the run learns them.
func New() *Redactor { return &Redactor{} }

// Add registers a literal value to remove wherever it appears.
//
// Very short values are ignored: replacing every occurrence of a three-letter
// password would mangle ordinary prose and make the output less trustworthy,
// not more. A secret that short is a finding in its own right.
func (r *Redactor) Add(values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, value := range values {
		value = strings.TrimSpace(value)
		if len(value) < 6 {
			continue
		}
		if r.knownLocked(value) {
			continue
		}
		r.values = append(r.values, value)
	}
	// Longest first, so a secret that contains another secret is removed
	// whole rather than leaving a fragment behind.
	sort.SliceStable(r.values, func(i, j int) bool {
		return len(r.values[i]) > len(r.values[j])
	})
}

func (r *Redactor) knownLocked(value string) bool {
	for _, existing := range r.values {
		if existing == value {
			return true
		}
	}
	return false
}

// AddFromEnv registers the values of any variable whose name looks like it
// holds a secret. It is how a credential the bench was handed, but never
// looked at directly, still gets removed from a stack trace.
func (r *Redactor) AddFromEnv(environment map[string]string) {
	for key, value := range environment {
		if IsSecretName(key) {
			r.Add(value)
		}
	}
}

// IsSecretName reports whether a variable name suggests its value is a secret.
//
// Two passes. Substring matching catches the obvious names; whole-segment
// matching catches the ones where the word stands alone, such as the KEY in
// SOPS_AGE_KEY, without also matching every name that happens to contain those
// three letters.
func IsSecretName(name string) bool {
	upper := strings.ToUpper(name)

	// An endpoint is not a credential, and hiding it makes a report much
	// harder to act on for no gain.
	if strings.HasSuffix(upper, "_URL") || strings.HasSuffix(upper, "_ENDPOINT") {
		return false
	}

	for _, marker := range []string{
		"PASSWORD", "PASSWD", "SECRET", "TOKEN", "APIKEY", "API_KEY",
		"PRIVATE_KEY", "CREDENTIAL", "AUTH", "PASSPHRASE",
	} {
		if strings.Contains(upper, marker) {
			return true
		}
	}

	for _, segment := range strings.FieldsFunc(upper, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	}) {
		switch segment {
		case "KEY", "KEYS", "PW", "PASS", "SECRETS":
			return true
		}
	}
	return false
}

// String removes every registered value and every pattern match.
func (r *Redactor) String(input string) string {
	if input == "" {
		return input
	}
	r.mu.RLock()
	values := make([]string, len(r.values))
	copy(values, r.values)
	r.mu.RUnlock()

	out := input
	for _, value := range values {
		out = strings.ReplaceAll(out, value, Placeholder)
	}
	for _, pattern := range patterns {
		out = pattern.expr.ReplaceAllString(out, pattern.replacement)
	}
	return out
}

// Strings redacts a slice in place-free fashion.
func (r *Redactor) Strings(input []string) []string {
	out := make([]string, len(input))
	for index, value := range input {
		out[index] = r.String(value)
	}
	return out
}

// EnvNames returns the variable names of an environment, with the values
// dropped entirely. Evidence records which variables a command ran with, never
// what was in them.
func EnvNames(environment []string) []string {
	names := make([]string, 0, len(environment))
	for _, entry := range environment {
		if key, _, ok := strings.Cut(entry, "="); ok {
			if IsSecretName(key) {
				names = append(names, key+"="+Placeholder)
			} else {
				names = append(names, key)
			}
		}
	}
	sort.Strings(names)
	return names
}

// Leaked reports which of the given values survive redaction — the test the
// bench runs against itself.
func (r *Redactor) Leaked(output string, values ...string) []string {
	redacted := r.String(output)
	var found []string
	for _, value := range values {
		if len(value) >= 6 && strings.Contains(redacted, value) {
			found = append(found, value)
		}
	}
	return found
}
