package gitopsupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EvidenceSchema is the version of the record written into the GitOps
// repository. A consumer there reads a file this bench wrote, so the shape is
// an interface and gets a number.
const EvidenceSchema = "1.0"

// Evidence is the compact, immutable record of what was tested.
//
// Deliberately small. It goes into somebody else's repository and stays in its
// history for ever, so it holds identifiers and counts and nothing else: no
// command output, no sandbox paths, no environment, no credential. A reviewer
// looking at the pull request should be able to read the whole thing.
type Evidence struct {
	SchemaVersion  string    `json:"schemaVersion"`
	RunID          string    `json:"runId"`
	SourceRepo     string    `json:"sourceRepository"`
	SourceCommit   string    `json:"sourceCommit"`
	CLIVersion     string    `json:"cliVersion"`
	BenchVersion   string    `json:"benchVersion"`
	Environment    string    `json:"environment"`
	Status         string    `json:"status"`
	Passed         int       `json:"passed"`
	Warnings       int       `json:"warnings"`
	Failed         int       `json:"failed"`
	Blocked        int       `json:"blocked"`
	CleanupStatus  string    `json:"cleanupStatus"`
	ReportPath     string    `json:"reportPath"`
	WorkflowRunURL string    `json:"workflowRunUrl,omitempty"`
	CompletedAt    time.Time `json:"completedAt"`
}

// NewEvidence builds the record from a finished run.
//
// The report path is stored relative to the bench root. An absolute one would
// publish the layout of whatever machine happened to run the tests into a
// repository other people read, which is both a small leak and useless to them.
func NewEvidence(run RunSummary, benchRoot string, warned bool) Evidence {
	status := "passed"
	if warned {
		status = "passed_with_warnings"
	}
	return Evidence{
		SchemaVersion:  EvidenceSchema,
		RunID:          run.RunID,
		SourceRepo:     run.SourceRepository,
		SourceCommit:   run.SourceCommit,
		CLIVersion:     run.CLIVersion,
		BenchVersion:   run.BenchVersion,
		Environment:    run.Environment,
		Status:         status,
		Passed:         run.Passed,
		Warnings:       run.Warnings,
		Failed:         run.Failed,
		Blocked:        run.Blocked,
		CleanupStatus:  cleanupWord(run.CleanupState),
		ReportPath:     relativeTo(benchRoot, run.ReportPath),
		WorkflowRunURL: strings.TrimSpace(os.Getenv("GITHUB_RUN_URL")),
		CompletedAt:    time.Now().UTC().Truncate(time.Second),
	}
}

func relativeTo(root, path string) string {
	if root == "" || path == "" {
		return path
	}
	if relative, err := filepath.Rel(root, path); err == nil &&
		!strings.HasPrefix(relative, "..") {
		return filepath.ToSlash(relative)
	}
	return filepath.ToSlash(path)
}

// Bytes is the record as it is written, with a trailing newline so the file is
// a well-behaved text file in somebody else's repository.
func (e Evidence) Bytes() ([]byte, error) {
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode evidence: %w", err)
	}
	return append(raw, '\n'), nil
}

// Write puts the record in the run's own reports directory.
//
// This copy is written whatever the mode: a preview that produced no commit
// still produced evidence, and a reader should be able to see exactly what
// would have been published.
func (e Evidence) Write(reportsDir string) (string, error) {
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", reportsDir, err)
	}
	raw, err := e.Bytes()
	if err != nil {
		return "", err
	}
	path := filepath.Join(reportsDir, "gitops-evidence.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// HistoryPath is where a per-run copy is kept alongside the latest one, so the
// GitOps repository accumulates a history rather than only ever showing the
// most recent promotion.
//
// Derived from the configured latest path rather than hardcoded, so moving one
// moves both.
func HistoryPath(latest, runID string) string {
	if latest == "" || runID == "" {
		return ""
	}
	directory := filepath.ToSlash(filepath.Dir(latest))
	if directory == "." {
		directory = ""
	} else {
		directory += "/"
	}
	return directory + "runs/" + SanitiseSegment(runID) + ".json"
}
