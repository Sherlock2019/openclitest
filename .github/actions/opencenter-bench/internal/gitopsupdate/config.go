// Package gitopsupdate proposes a version-controlled deployment change from a
// finished Test Bench run.
//
// It is the eleventh and last lifecycle stage. Everything before it answers
// "does this build hold together"; this one answers "should this build become
// the desired state", and its answer is a pull request rather than a
// deployment. Nothing in this package merges, reconciles or applies. The
// division of labour is deliberate and total:
//
//	Test Bench       validates, and prepares evidence
//	GitHub Actions   executes the bench and supplies the credential
//	this package     proposes a desired-state change on a branch
//	pull request     provides review and approval
//	Flux             deploys, only after a human merged
//
// Two gates stand in front of every remote write, and they are separate on
// purpose. OPENCLI_ALLOW_GITOPS_UPDATE=1 is the environment's permission;
// Approve is the operator's. Neither implies the other, and the infrastructure
// mutation gate (OPENCLI_ALLOW_MUTATE) grants nothing here — a run allowed to
// build a cluster is not thereby allowed to write to the delivery repository.
package gitopsupdate

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Gate is the environment variable that permits a remote write.
//
// Not OPENCLI_ALLOW_MUTATE. Reusing that would mean every run that is allowed
// to create a cluster is also allowed to push to the repository that decides
// what production runs, which are different permissions held by different
// people.
const Gate = "OPENCLI_ALLOW_GITOPS_UPDATE"

// Defaults. The repository is a default and never a constant: every caller —
// the CLI flag, the Actions input, the saved configuration, the UI field — can
// override it, and nothing in this package may hardcode it.
const (
	DefaultRepository      = "Sherlock2019/opencenter-my-cluster-gitops"
	DefaultBaseBranch      = "main"
	DefaultManifestPath    = "clusters/my-cluster/kustomization.yaml"
	DefaultContainerName   = "test-bench"
	DefaultImageRepository = "ghcr.io/sherlock2019/opencenter-cli-test-bench"
	DefaultEvidencePath    = "test-evidence/opencenter-cli/latest.json"
	DefaultAPIBase         = "https://api.github.com"
)

// DefaultApprovedPaths are the only places in the GitOps repository this stage
// may write. Configurable, but a change here is a change to the blast radius
// and reads like one.
var DefaultApprovedPaths = []string{
	"apps/opencenter-cli-test-bench/",
	"clusters/",
	"test-evidence/opencenter-cli/",
}

// Config is everything the stage needs to know about where it is writing.
//
// No credential is a field. The token and the SSH key never enter this struct,
// never reach a command line, and never reach a commit message; they are read
// from the environment at the moment a subprocess is built, and registered with
// the redactor before that. See repository.go.
type Config struct {
	// Repository is whatever the operator typed: an owner/name pair, an HTTPS
	// URL, or an SSH remote. Normalise turns it into both forms this package
	// needs and neither caller has to care which was given.
	Repository      string
	BaseBranch      string
	ManifestPath    string
	EvidencePath    string
	ContainerName   string
	ImageRepository string
	ImageTag        string
	CreatePR        bool
	AllowWarnings   bool
	ApprovedPaths   []string

	// APIBase is the GitHub REST root. A field so the tests can point it at an
	// httptest server rather than at github.com.
	APIBase string
}

// Source records where a resolved value came from, so the UI and the report can
// explain a surprising setting rather than merely showing it.
type Source string

const (
	FromFlag    Source = "flag"
	FromEnv     Source = "environment"
	FromSaved   Source = "saved"
	FromDefault Source = "default"
)

// Resolve applies the precedence the stage promises: a CLI flag beats the
// environment, which beats saved console configuration, which beats the
// default.
//
// Each layer is a lookup function rather than a map, so a caller can pass
// os.Getenv directly and a test can pass a closure over a fixture.
func Resolve(key string, flag, env, saved func(string) string) (string, Source) {
	for _, layer := range []struct {
		read   func(string) string
		source Source
	}{
		{flag, FromFlag}, {env, FromEnv}, {saved, FromSaved},
	} {
		if layer.read == nil {
			continue
		}
		if value := strings.TrimSpace(layer.read(key)); value != "" {
			return value, layer.source
		}
	}
	return "", FromDefault
}

// Env is the environment variable each setting is read from. Exported because
// the console, the CLI and the workflow all have to name the same ones.
const (
	EnvRepository      = "OPENCLI_GIT_REPOSITORY"
	EnvBaseBranch      = "OPENCLI_GIT_BASE_BRANCH"
	EnvManifestPath    = "OPENCLI_GIT_MANIFEST_PATH"
	EnvEvidencePath    = "OPENCLI_GIT_EVIDENCE_PATH"
	EnvContainerName   = "OPENCLI_GIT_CONTAINER_NAME"
	EnvImageRepository = "OPENCLI_IMAGE_REPOSITORY"
	EnvImageTag        = "OPENCLI_IMAGE_TAG"
	EnvCreatePR        = "OPENCLI_GIT_CREATE_PR"
	EnvAllowWarnings   = "OPENCLI_GITOPS_ALLOW_WARNINGS"
	EnvToken           = "OPENCLI_GIT_TOKEN"
	EnvSSHKey          = "OPENCLI_GIT_SSH_KEY"
	EnvAPIBase         = "OPENCLI_GITHUB_API"
)

// Load builds a Config from the three overridable layers plus the defaults.
//
// flag and saved may be nil, which is the headless and the unconfigured case.
func Load(flag, saved func(string) string) Config {
	env := os.Getenv
	pick := func(key, fallback string) string {
		if value, _ := Resolve(key, flag, env, saved); value != "" {
			return value
		}
		return fallback
	}
	yes := func(key string, fallback bool) bool {
		value, _ := Resolve(key, flag, env, saved)
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		default:
			return fallback
		}
	}
	return Config{
		Repository:      pick(EnvRepository, DefaultRepository),
		BaseBranch:      pick(EnvBaseBranch, DefaultBaseBranch),
		ManifestPath:    pick(EnvManifestPath, DefaultManifestPath),
		EvidencePath:    pick(EnvEvidencePath, DefaultEvidencePath),
		ContainerName:   pick(EnvContainerName, DefaultContainerName),
		ImageRepository: pick(EnvImageRepository, DefaultImageRepository),
		ImageTag:        pick(EnvImageTag, ""),
		CreatePR:        yes(EnvCreatePR, true),
		AllowWarnings:   yes(EnvAllowWarnings, false),
		ApprovedPaths:   append([]string(nil), DefaultApprovedPaths...),
		APIBase:         pick(EnvAPIBase, DefaultAPIBase),
	}
}

// Configured reports whether a GitOps repository was named at all.
//
// A stage with nowhere to write is NOT CONFIGURED, which is a different answer
// from FAILED and has to stay different: a bench nobody has pointed at a
// delivery repository has not failed anything.
func (c Config) Configured() bool { return strings.TrimSpace(c.Repository) != "" }

// --- repository naming --------------------------------------------------------

// slug is the owner/name pair, which is what the REST API wants.
var slugPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Slug normalises any of the three accepted spellings to owner/name.
//
//	Sherlock2019/opencenter-my-cluster-gitops
//	https://github.com/Sherlock2019/opencenter-my-cluster-gitops.git
//	git@github.com:Sherlock2019/opencenter-my-cluster-gitops.git
//
// A local path or a file:// URL has no slug and returns "", which is not an
// error: the integration tests and the offline default push to a bare
// repository on disk, where there is no API to call and no pull request to
// open. Callers treat an empty slug as "no pull request possible".
func Slug(repository string) string {
	value := strings.TrimSpace(repository)
	if value == "" {
		return ""
	}
	value = strings.TrimSuffix(value, "/")
	value = strings.TrimSuffix(value, ".git")

	switch {
	case strings.HasPrefix(value, "file://"), strings.HasPrefix(value, "/"),
		strings.HasPrefix(value, "./"), strings.HasPrefix(value, "../"):
		return ""
	case strings.HasPrefix(value, "git@"):
		// git@github.com:owner/name
		if _, rest, ok := strings.Cut(value, ":"); ok {
			value = rest
		}
	case strings.Contains(value, "://"):
		// https://github.com/owner/name — take the last two path segments, so a
		// GitHub Enterprise host with a longer path still resolves.
		_, rest, _ := strings.Cut(value, "://")
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		if len(parts) < 3 {
			return ""
		}
		value = strings.Join(parts[len(parts)-2:], "/")
	}

	if !slugPattern.MatchString(value) {
		return ""
	}
	return value
}

// CloneURL is what git is handed.
//
// A slug becomes an HTTPS GitHub URL with no credential in it — authentication
// goes through a credential helper or an SSH key, never through userinfo in the
// remote. A URL or a path is passed through untouched, which is how the local
// bare repository in the tests works.
func (c Config) CloneURL() string {
	value := strings.TrimSpace(c.Repository)
	if slugPattern.MatchString(strings.TrimSuffix(value, ".git")) &&
		!strings.Contains(value, "://") {
		return "https://github.com/" + strings.TrimSuffix(value, ".git") + ".git"
	}
	return value
}

// Validate catches what can be known before a single git command runs.
//
// Reachability, branch existence and token scope cannot be checked from here —
// they need the network — so they are checked in the preflight step against the
// real remote and reported with their own messages.
func (c Config) Validate() error {
	if !c.Configured() {
		return fmt.Errorf("no GitOps repository configured — set %s", EnvRepository)
	}
	if Slug(c.Repository) == "" && !strings.Contains(c.Repository, "://") &&
		!strings.HasPrefix(c.Repository, "/") {
		return fmt.Errorf("GitOps repository %q is not owner/name, an https:// URL, "+
			"an ssh remote or a local path", c.Repository)
	}
	if strings.TrimSpace(c.BaseBranch) == "" {
		return fmt.Errorf("no GitOps base branch configured — set %s", EnvBaseBranch)
	}
	if strings.TrimSpace(c.ManifestPath) == "" && strings.TrimSpace(c.EvidencePath) == "" {
		return fmt.Errorf("nothing to update: neither a manifest path nor an evidence path is set")
	}
	for _, path := range []string{c.ManifestPath, c.EvidencePath} {
		if path == "" {
			continue
		}
		if err := checkRelative(path); err != nil {
			return err
		}
		if !c.approved(path) {
			return fmt.Errorf("%q is outside the approved GitOps paths (%s)",
				path, strings.Join(c.ApprovedPaths, ", "))
		}
	}
	return nil
}

// checkRelative refuses a path that could leave the checkout.
//
// Both spellings, because they are different bugs. An absolute path escapes by
// ignoring the root; a .. segment escapes by walking out of it. filepath.Join
// would silently clean the second one into something plausible-looking, which
// is exactly the failure mode worth refusing loudly instead.
func checkRelative(path string) error {
	clean := strings.ReplaceAll(path, `\`, "/")
	if strings.HasPrefix(clean, "/") {
		return fmt.Errorf("GitOps path %q is absolute; it must be relative to the repository root", path)
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") ||
		strings.HasSuffix(clean, "/..") {
		return fmt.Errorf("GitOps path %q leaves the repository checkout", path)
	}
	return nil
}

// approved reports whether a path sits under one of the approved prefixes.
func (c Config) approved(path string) bool {
	clean := strings.TrimPrefix(strings.ReplaceAll(path, `\`, "/"), "./")
	for _, prefix := range c.ApprovedPaths {
		prefix = strings.TrimPrefix(strings.ReplaceAll(prefix, `\`, "/"), "./")
		if prefix == "" {
			continue
		}
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(clean, prefix) {
				return true
			}
			continue
		}
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

// Approved is the exported form, used to validate a diff after the fact as well
// as a configured path before it.
func (c Config) Approved(path string) bool { return c.approved(path) }

// Redacted is the Config as it may appear in a report, in the UI, or in a log.
//
// The repository is the only field that can carry a credential — an operator
// who pastes https://user:token@github.com/... into the field has put a token
// where a URL goes — so it is stripped here rather than trusted to the
// redactor's patterns alone. Defence in depth: the redactor still runs.
func (c Config) Redacted() Config {
	out := c
	out.Repository = StripCredentials(c.Repository)
	return out
}

// StripCredentials removes userinfo from a URL, keeping the shape so a reader
// can still tell what the remote is.
func StripCredentials(remote string) string {
	scheme, rest, ok := strings.Cut(remote, "://")
	if !ok {
		return remote
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		// Only the authority section: a path may legitimately contain @.
		host := rest
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			host = rest[:slash]
		}
		if inner := strings.LastIndex(host, "@"); inner >= 0 {
			rest = rest[inner+1:]
		}
	}
	return scheme + "://" + rest
}
