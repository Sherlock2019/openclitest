package gitopsupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The last thing that looks at the change before it is committed.
//
// Everything up to here decided what to write; this decides whether what was
// actually written is what was meant. The two are not the same claim — a
// manifest edit that silently rewrote an anchor, a stray file a tool dropped in
// the checkout, an editor backup — and the gap between them is exactly where an
// automated commit goes wrong.
//
// The check is on the diff rather than on the intent, because the diff is the
// thing that gets pushed.

// forbiddenNames are files that must never appear in a GitOps commit from this
// bench, matched on the base name anywhere in the tree.
var forbiddenNames = []string{
	".env", ".env.local", ".netrc", ".git-credentials",
	"credentials.local.yaml", "credentials.yaml",
	"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
	"kubeconfig", "kubeconfig.yaml", ".npmrc", ".pypirc",
}

// forbiddenSuffixes catch key material and archives by extension.
var forbiddenSuffixes = []string{
	".pem", ".key", ".p12", ".pfx", ".jks", ".keystore",
	".swp", ".orig", ".rej", ".bak", "~",
}

// DiffReport is what the validation step found.
type DiffReport struct {
	Files    []string
	Stat     string
	Patch    string
	Problems []string
}

// OK reports whether the diff may be committed.
func (d DiffReport) OK() bool { return len(d.Problems) == 0 && len(d.Files) > 0 }

// Inspect reads the working tree's change and judges it.
//
// Four git commands rather than one, because they answer four questions and a
// combined output would make it ambiguous which one failed:
//
//	status --porcelain   what is there at all, including untracked files
//	diff --check         whitespace damage and conflict markers
//	diff --name-only     which paths
//	diff --stat          how much, for the summary line
//
// --name-only alone would miss an untracked file entirely, which is how a
// tool's leftover temp file gets swept into `git add .` later.
func Inspect(ctx context.Context, repo *Repo, config Config, expected []string) (DiffReport, error) {
	var report DiffReport

	// -uall, not the default. Without it git collapses a new directory into one
	// entry — "?? test-evidence/" — and the individual files this stage wrote
	// are never named, so the check that each expected file really changed
	// fails on a change that is perfectly correct.
	status, err := repo.Git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return report, err
	}
	// Untracked files count. They are not in `git diff` and they would be in
	// the commit.
	seen := map[string]bool{}
	// Trimmed at the ends only, and by newline only.
	//
	// TrimSpace over the whole output eats the leading space of the *first*
	// line, and porcelain v1 puts the status in fixed columns — " M path"
	// became "M path", the path was read from column three, and every modified
	// file arrived with its first character missing. It failed as "lusters/..
	// is outside the approved paths", which reads like a configuration mistake
	// rather than a parsing one.
	for _, line := range strings.Split(strings.Trim(status, "\r\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Porcelain v1: two status characters, a space, then the path. A
		// rename is "old -> new"; the new name is what lands in the commit.
		path := strings.TrimSpace(line)
		if len(line) > 3 {
			path = strings.TrimSpace(line[3:])
		}
		if _, after, ok := strings.Cut(path, " -> "); ok {
			path = after
		}
		path = strings.Trim(path, `"`)
		if !seen[path] {
			seen[path] = true
			report.Files = append(report.Files, path)
		}
	}

	if out, checkErr := repo.Git(ctx, "diff", "--check"); checkErr != nil {
		// --check exits non-zero for whitespace errors and conflict markers.
		// Both are worth refusing: a conflict marker in a manifest deploys.
		report.Problems = append(report.Problems,
			"the change has whitespace errors or conflict markers: "+firstLine(out))
	}

	if stat, statErr := repo.Git(ctx, "diff", "--stat"); statErr == nil {
		report.Stat = strings.TrimSpace(stat)
	}
	// Untracked files are invisible to `git diff`, so they are staged first and
	// the patch is taken from the index. Nothing is committed by this.
	if _, addErr := repo.Git(ctx, "add", "--intent-to-add", "--", "."); addErr == nil {
		if patch, patchErr := repo.Git(ctx, "diff", "--", "."); patchErr == nil {
			report.Patch = patch
		}
	}

	report.Problems = append(report.Problems, judge(report.Files, config, expected, repo.Dir)...)
	return report, nil
}

// judge applies every rule to the list of changed paths.
func judge(files []string, config Config, expected []string, root string) []string {
	var problems []string

	if len(files) == 0 {
		return []string{"nothing changed — there is no update to propose"}
	}

	wanted := map[string]bool{}
	for _, path := range expected {
		wanted[filepath.ToSlash(path)] = true
	}
	changed := map[string]bool{}

	for _, path := range files {
		clean := filepath.ToSlash(path)
		changed[clean] = true
		base := strings.ToLower(filepath.Base(clean))

		if !config.Approved(clean) {
			problems = append(problems,
				fmt.Sprintf("%s is outside the approved GitOps paths", clean))
			continue
		}
		for _, name := range forbiddenNames {
			if base == name {
				problems = append(problems,
					fmt.Sprintf("%s is a credential file and must never be committed", clean))
			}
		}
		for _, suffix := range forbiddenSuffixes {
			if strings.HasSuffix(base, suffix) {
				problems = append(problems,
					fmt.Sprintf("%s looks like key material or an editor artefact", clean))
			}
		}
		if strings.Contains(clean, "/.git/") || strings.HasPrefix(clean, ".git/") {
			problems = append(problems, fmt.Sprintf("%s is inside the git directory", clean))
		}
		if binary, why := looksBinary(filepath.Join(root, filepath.FromSlash(clean))); binary {
			problems = append(problems,
				fmt.Sprintf("%s is a binary file and this stage only writes text (%s)", clean, why))
		}
	}

	// Every file this stage meant to write must actually be in the diff. A
	// manifest update that silently no-opped would otherwise produce a pull
	// request that promotes nothing while looking exactly like one that does.
	for path := range wanted {
		if !changed[path] {
			problems = append(problems,
				fmt.Sprintf("%s was expected to change and did not", path))
		}
	}
	return problems
}

// looksBinary reads the head of a file and decides. Cheap and deliberately
// crude: a NUL byte in the first 8 KB is the same test git itself uses.
func looksBinary(path string) (bool, string) {
	file, err := os.Open(path)
	if err != nil {
		// A deleted file cannot be inspected and is not a binary finding.
		return false, ""
	}
	defer file.Close()

	head := make([]byte, 8000)
	read, _ := file.Read(head)
	for _, b := range head[:read] {
		if b == 0 {
			return true, "contains a NUL byte"
		}
	}
	if info, statErr := file.Stat(); statErr == nil && info.Size() > 1<<20 {
		return true, "larger than 1 MB"
	}
	return false, ""
}

// ScanSecrets checks the patch itself for credential material.
//
// A separate pass from the file-name rules, because a token pasted into a
// manifest is in an approved path with an approved name. Two independent
// sources: the canaries the caller planted, and the redactor's own patterns —
// a placeholder surviving into the patch means the redactor matched something,
// which is as good as a hit.
func ScanSecrets(patch string, redactor Redactor, canaries []string) []string {
	var problems []string
	if strings.TrimSpace(patch) == "" {
		return nil
	}
	for _, canary := range canaries {
		if len(canary) >= 6 && strings.Contains(patch, canary) {
			problems = append(problems,
				"the proposed change contains a secret canary — something leaked a credential into it")
			break
		}
	}
	if redactor != nil && strings.Contains(redactor.String(patch), "[REDACTED]") {
		problems = append(problems,
			"the proposed change contains something the redactor recognises as a credential")
	}
	for _, marker := range []string{
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----",
		"AGE-SECRET-KEY-1",
	} {
		if strings.Contains(patch, marker) {
			problems = append(problems, "the proposed change contains a private key block")
			break
		}
	}
	return problems
}

// SavePatch writes the redacted patch beside the run's other evidence.
//
// Through the redactor on the way out, without exception. This file is the one
// artefact of the stage a person is most likely to download and paste
// somewhere, which makes it the one most worth being sure about.
func SavePatch(evidenceDir, patch string, redactor Redactor) (string, error) {
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		return "", err
	}
	body := patch
	if redactor != nil {
		body = redactor.String(body)
	}
	path := filepath.Join(evidenceDir, "gitops-update.patch")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
