package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedLedger writes records into a throwaway root and returns that root.
func seedLedger(t *testing.T, lines ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if len(lines) > 0 {
		body := strings.Join(lines, "\n") + "\n"
		if err := os.WriteFile(ledgerPath(root), []byte(body), 0o644); err != nil {
			t.Fatalf("seeding the ledger: %v", err)
		}
	}
	return root
}

// The count is the point of the ledger, and it has to survive a process that
// exits — every CI job is a new process, so an in-memory counter would report 1
// forever.
func TestRecordingAccumulatesAcrossProcesses(t *testing.T) {
	root := seedLedger(t)
	t.Setenv("GITHUB_OUTPUT", "")

	for index := 1; index <= 3; index++ {
		t.Setenv("GITHUB_REPOSITORY", "owner/name")
		t.Setenv("GITHUB_SHA", "abc123def4567890")
		if err := traceRecord(root, nil); err != nil {
			t.Fatalf("traceRecord: %v", err)
		}
		records, err := readLedger(root)
		if err != nil {
			t.Fatalf("readLedger: %v", err)
		}
		if len(records) != index {
			t.Fatalf("after %d call(s) the ledger holds %d", index, len(records))
		}
		if records[index-1].Sequence != index {
			t.Errorf("record %d has sequence %d", index, records[index-1].Sequence)
		}
	}
}

// Every field the caller asked to see: which repo, which commit, and when.
func TestARecordCarriesTheCallerAndTheCommit(t *testing.T) {
	root := seedLedger(t)
	t.Setenv("GITHUB_OUTPUT", "")
	t.Setenv("GITHUB_REPOSITORY", "Sherlock2019/openCenter-cli-testDzoan")
	t.Setenv("GITHUB_SHA", "abc123def4567890abcdef1234567890abcdef12")
	t.Setenv("GITHUB_REF_NAME", "main")
	t.Setenv("GITHUB_EVENT_NAME", "push")
	t.Setenv("GITHUB_ACTOR", "Sherlock2019")
	t.Setenv("GITHUB_RUN_ID", "42")

	if err := traceRecord(root, nil); err != nil {
		t.Fatalf("traceRecord: %v", err)
	}
	records, err := readLedger(root)
	if err != nil || len(records) != 1 {
		t.Fatalf("readLedger: %v (%d records)", err, len(records))
	}
	record := records[0]

	if record.Repository != "Sherlock2019/openCenter-cli-testDzoan" {
		t.Errorf("repository is %q", record.Repository)
	}
	if record.Commit != "abc123def4567890abcdef1234567890abcdef12" {
		t.Errorf("commit is %q", record.Commit)
	}
	if record.Event != "push" || record.Actor != "Sherlock2019" || record.Ref != "main" {
		t.Errorf("event/actor/ref are %q/%q/%q", record.Event, record.Actor, record.Ref)
	}
	if record.Date == "" || record.Time == "" || record.Timestamp == "" {
		t.Errorf("the record has no date/time: %+v", record)
	}
	// The URL is derived rather than passed in, so it is worth asserting once.
	want := "https://github.com/Sherlock2019/openCenter-cli-testDzoan/actions/runs/42"
	if record.RunURL != want {
		t.Errorf("run url is %q, want %q", record.RunURL, want)
	}
}

// A call from a shell is still a call. Dropping it would undercount exactly
// when somebody is testing the integration by hand.
func TestACallOutsideActionsIsStillRecorded(t *testing.T) {
	root := seedLedger(t)
	t.Setenv("GITHUB_OUTPUT", "")
	for _, key := range []string{
		"GITHUB_REPOSITORY", "GITHUB_SHA", "GITHUB_REF_NAME",
		"GITHUB_EVENT_NAME", "GITHUB_ACTOR", "GITHUB_RUN_ID",
	} {
		t.Setenv(key, "")
	}

	if err := traceRecord(root, nil); err != nil {
		t.Fatalf("traceRecord: %v", err)
	}
	records, _ := readLedger(root)
	if len(records) != 1 {
		t.Fatalf("the ledger holds %d records", len(records))
	}
	if records[0].Repository != "(local)" {
		t.Errorf("repository is %q, want %q", records[0].Repository, "(local)")
	}
}

// One torn line must not cost the whole history — the file is appended by jobs
// that can be killed mid-write.
func TestACorruptLineDoesNotDiscardTheLedger(t *testing.T) {
	root := seedLedger(t,
		`{"sequence":1,"repository":"owner/one"}`,
		`{"sequence":2,"repository":"owner`, // truncated
		`{"sequence":3,"repository":"owner/three"}`,
		``,
	)
	records, err := readLedger(root)
	if err != nil {
		t.Fatalf("readLedger: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want the 2 that parse", len(records))
	}
	if records[0].Repository != "owner/one" || records[1].Repository != "owner/three" {
		t.Errorf("the surviving records are wrong: %+v", records)
	}
}

// A missing ledger is an empty one, not an error: the first call in a fresh
// checkout must not fail.
func TestNoLedgerYetReadsAsZero(t *testing.T) {
	records, err := readLedger(t.TempDir())
	if err != nil {
		t.Fatalf("readLedger on a fresh root: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("got %d records from nowhere", len(records))
	}
}

// The overrides exist so this is usable from a shell and testable off a runner.
func TestFlagsOverrideTheEnvironment(t *testing.T) {
	root := seedLedger(t)
	t.Setenv("GITHUB_OUTPUT", "")
	t.Setenv("GITHUB_REPOSITORY", "from/environment")

	err := traceRecord(root, []string{"--repository", "from/flag", "--commit", "ff00ff00ff00"})
	if err != nil {
		t.Fatalf("traceRecord: %v", err)
	}
	records, _ := readLedger(root)
	if records[0].Repository != "from/flag" {
		t.Errorf("repository is %q, want the flag to win", records[0].Repository)
	}
	if records[0].Commit != "ff00ff00ff00" {
		t.Errorf("commit is %q", records[0].Commit)
	}
}
