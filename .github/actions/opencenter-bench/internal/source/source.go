// Package source lists what a git remote offers, and decides whether a URL is
// one the bench is willing to hand to git.
//
// Both consoles let somebody type a repository into the page and pick a branch
// off it. That is the only way to test an openCenter CLI that has not been
// released, and it is also the one place where text typed into a browser
// reaches a command. The checks live here rather than in either console, so
// there is one answer to "is this a repository we will clone" instead of two
// that drift.
package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Default is the repository the bench tests when nobody names another.
const Default = "https://github.com/opencenter-cloud/openCenter-cli.git"

// Refs is what a remote has: the branches that can be built, and the tags that
// name a release.
type Refs struct {
	Repository string   `json:"repository"`
	Branches   []string `json:"branches"`
	Tags       []string `json:"tags"`
}

// ValidateRepository accepts the URL forms git can be given over a network and
// nothing else.
//
// The URL is passed to git as one argument, never through a shell, so this is
// not quoting — it is refusing the shapes that are trouble anyway: a leading
// dash that git would read as an option, whitespace and control characters
// that are never in a real remote, and local paths, which would turn a text
// box in a browser into a way to name any directory on the machine.
func ValidateRepository(repo string) error {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return errors.New("no repository given")
	}
	if len(repo) > 512 {
		return errors.New("that repository URL is implausibly long")
	}
	if strings.HasPrefix(repo, "-") {
		return errors.New("a repository URL cannot start with a dash")
	}
	for _, r := range repo {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return errors.New("a repository URL cannot contain spaces or control characters")
		}
	}
	switch {
	case strings.HasPrefix(repo, "https://"), strings.HasPrefix(repo, "http://"),
		strings.HasPrefix(repo, "ssh://"), strings.HasPrefix(repo, "git://"):
		if strings.Count(repo, "/") < 3 {
			return errors.New("that URL names a host but no repository")
		}
		return nil
	default:
		// The scp-like form: user@host:path — one colon, something either side.
		user, rest, ok := strings.Cut(repo, "@")
		if !ok || user == "" {
			return errors.New("use https://…, ssh://…, git://… or user@host:path")
		}
		host, path, ok := strings.Cut(rest, ":")
		if !ok || host == "" || path == "" {
			return errors.New("use https://…, ssh://…, git://… or user@host:path")
		}
		return nil
	}
}

// ValidateBranch accepts the characters a branch or tag name may contain.
//
// The same set the install script checks for, on purpose: a name this accepts
// and the script rejects would be a button that fails after the clone starts.
func ValidateBranch(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("no branch given")
	}
	if len(name) > 255 {
		return errors.New("that branch name is implausibly long")
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("a branch name cannot start with a dash")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '/', r == '-':
		default:
			return fmt.Errorf("a branch name cannot contain %q", r)
		}
	}
	return nil
}

// List asks a remote what it has, without cloning it.
//
// `git ls-remote` reads; it writes nothing to disk and needs no workspace, so
// filling a dropdown costs one network round trip rather than a clone. The
// context carries the deadline: a private repository over SSH is the case that
// would otherwise sit there, and the prompts are turned off below so it fails
// instead of waiting for a password nobody can type.
func List(ctx context.Context, repo string) (Refs, error) {
	repo = strings.TrimSpace(repo)
	if err := ValidateRepository(repo); err != nil {
		return Refs{}, err
	}
	refs := Refs{Repository: repo}

	command := exec.CommandContext(ctx, "git", "ls-remote", "--heads", "--tags", repo)
	command.Env = append(command.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/echo",
		"GIT_SSH_COMMAND=ssh -oBatchMode=yes -oStrictHostKeyChecking=accept-new",
	)
	out, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return refs, fmt.Errorf("git could not read %s: %s", repo,
				firstLine(string(exit.Stderr)))
		}
		return refs, fmt.Errorf("git could not read %s: %w", repo, err)
	}

	seenTags := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		_, ref, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(ref, "refs/heads/"):
			refs.Branches = append(refs.Branches, strings.TrimPrefix(ref, "refs/heads/"))
		case strings.HasPrefix(ref, "refs/tags/"):
			// An annotated tag appears twice, the second as "name^{}". One
			// entry per tag is what a dropdown wants.
			name := strings.TrimSuffix(strings.TrimPrefix(ref, "refs/tags/"), "^{}")
			if name != "" && !seenTags[name] {
				seenTags[name] = true
				refs.Tags = append(refs.Tags, name)
			}
		}
	}
	sort.Strings(refs.Branches)
	// Newest first: a tag list is read from the top, and v1.9 sorting above
	// v1.10 is a detail nobody wants to squint at. Reverse lexical is not
	// version order, but it puts the recent releases where they are looked for.
	sort.Sort(sort.Reverse(sort.StringSlice(refs.Tags)))
	return refs, nil
}

// Checkout is where a clone landed.
type Checkout struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref,omitempty"`
	Path       string `json:"path"`
	Commit     string `json:"commit,omitempty"`
	Reused     bool   `json:"reused"`
}

// CloneRoot is where checkouts are kept: beside the bench's other caches, not
// in the repository and not in the run sandbox. A sandbox is deleted after
// every run, and somebody who asked for a copy of the source wants it to still
// be there afterwards.
func CloneRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "opencli-bench", "src")
	}
	return filepath.Join(home, ".cache", "opencli-bench", "src")
}

// Clone fetches a repository at a ref and returns where it is.
//
// Shallow and single-branch: this exists so somebody can read the code that was
// tested, not so they can do archaeology in it, and a full history of a large
// repository is a slow answer to a question nobody asked.
//
// An existing checkout is updated rather than re-cloned. Re-cloning would throw
// away anything a person had done in there, and doing that silently to a
// directory somebody may be working in is not a thing a button should do.
func Clone(ctx context.Context, repo, ref string) (Checkout, error) {
	repo = strings.TrimSpace(repo)
	if err := ValidateRepository(repo); err != nil {
		return Checkout{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref != "" {
		if err := ValidateBranch(ref); err != nil {
			return Checkout{}, err
		}
	}

	// The directory is named from the repository, never from anything typed
	// straight through: a name that walked out of CloneRoot would put a clone
	// somewhere nobody asked for.
	name := strings.TrimSuffix(path.Base(strings.TrimSuffix(repo, "/")), ".git")
	name = safeSegment(name)
	if name == "" {
		return Checkout{}, errors.New("could not work out a directory name for that repository")
	}
	destination := filepath.Join(CloneRoot(), name)

	if err := os.MkdirAll(CloneRoot(), 0o755); err != nil {
		return Checkout{}, err
	}

	environment := func(command *exec.Cmd) *exec.Cmd {
		command.Env = append(command.Environ(),
			"GIT_TERMINAL_PROMPT=0",
			"GIT_ASKPASS=/bin/echo",
			"GIT_SSH_COMMAND=ssh -oBatchMode=yes -oStrictHostKeyChecking=accept-new",
		)
		return command
	}

	result := Checkout{Repository: repo, Ref: ref, Path: destination}
	if _, err := os.Stat(filepath.Join(destination, ".git")); err == nil {
		result.Reused = true
		fetch := []string{"-C", destination, "fetch", "--depth", "1", "origin"}
		if ref != "" {
			fetch = append(fetch, ref)
		}
		if out, err := environment(exec.CommandContext(ctx, "git", fetch...)).CombinedOutput(); err != nil {
			return result, fmt.Errorf("git fetch: %s", firstLine(string(out)))
		}
		target := "FETCH_HEAD"
		if ref == "" {
			target = "origin/HEAD"
		}
		if out, err := environment(exec.CommandContext(ctx, "git",
			"-C", destination, "checkout", "--force", target)).CombinedOutput(); err != nil {
			return result, fmt.Errorf("git checkout: %s", firstLine(string(out)))
		}
	} else {
		args := []string{"clone", "--depth", "1", "--single-branch"}
		if ref != "" {
			args = append(args, "--branch", ref)
		}
		args = append(args, repo, destination)
		if out, err := environment(exec.CommandContext(ctx, "git", args...)).CombinedOutput(); err != nil {
			return result, fmt.Errorf("git clone: %s", firstLine(string(out)))
		}
	}

	if out, err := exec.CommandContext(ctx, "git",
		"-C", destination, "rev-parse", "--short", "HEAD").Output(); err == nil {
		result.Commit = strings.TrimSpace(string(out))
	}
	return result, nil
}

// safeSegment keeps a directory name to characters that cannot escape a path.
func safeSegment(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), ".-")
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return strings.TrimSpace(text[:index])
	}
	return text
}
