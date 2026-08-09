package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Commands that never return, and how the bench handles them.
//
// A foreground scheduler is not a command that failed to finish. The bench used
// to wait out the whole timeout on each and report a timeout, which reads as a
// fault: `cluster backup schedule` says in its own help that it "keeps running
// until interrupted", and neither it nor `cluster drift schedule` offers a
// one-shot flag.
//
// So they get a short leash. Start it, wait long enough to see whether it got
// past its own setup, stop it, and report that. Starting cleanly is the result.

// LongRunning is config/long-running.yaml.
type LongRunning struct {
	GraceSeconds int `yaml:"grace_seconds"`
	Commands     []struct {
		ID     string `yaml:"id"`
		Why    string `yaml:"why"`
		Expect string `yaml:"expect"`
	} `yaml:"commands"`

	byID map[string]longRunningEntry
}

type longRunningEntry struct {
	Why    string
	Expect string
}

// loadLongRunning reads the list. A missing file leaves every command treated
// normally, which is the behaviour that existed before this file.
func loadLongRunning(root string) *LongRunning {
	raw, err := os.ReadFile(filepath.Join(root, "config", "long-running.yaml"))
	if err != nil {
		return &LongRunning{byID: map[string]longRunningEntry{}}
	}
	var loaded LongRunning
	if err := yaml.Unmarshal(raw, &loaded); err != nil {
		return &LongRunning{byID: map[string]longRunningEntry{}}
	}
	loaded.byID = map[string]longRunningEntry{}
	for _, entry := range loaded.Commands {
		loaded.byID[entry.ID] = longRunningEntry{Why: entry.Why, Expect: entry.Expect}
	}
	if loaded.GraceSeconds <= 0 {
		loaded.GraceSeconds = 6
	}
	return &loaded
}

// Grace is how long a scheduler is given to prove it started.
func (l *LongRunning) Grace() time.Duration {
	if l == nil {
		return 0
	}
	return time.Duration(l.GraceSeconds) * time.Second
}

// Lookup reports whether a command is one of these.
//
// Matched on the arguments rather than the command id, because the id is the
// bench's own label and the arguments are what actually runs. A row whose
// ready line is "cluster backup schedule org/name --interval=24h" matches the
// entry "cluster backup schedule".
func (l *LongRunning) Lookup(args string) (longRunningEntry, bool) {
	if l == nil {
		return longRunningEntry{}, false
	}
	trimmed := strings.TrimSpace(args)
	for id, entry := range l.byID {
		if trimmed == id || strings.HasPrefix(trimmed, id+" ") {
			return entry, true
		}
	}
	return longRunningEntry{}, false
}

// Verdict turns a stopped scheduler's output into a result a reader can use.
//
// Expect exists because a command that crashed inside the grace period and one
// that started correctly both end up killed by the same deadline. Without
// something to look for, the two are indistinguishable and the bench would
// report a crash as a success.
func (e longRunningEntry) Verdict(output string, grace time.Duration) []string {
	started := e.Expect == "" || strings.Contains(output, e.Expect)
	if started {
		return []string{
			"",
			"--- this command does not return ---",
			e.Why,
			"",
			"It ran for " + grace.String() + " without failing, and was then stopped by the",
			"bench. That it started is the result; there is no exit code to wait for.",
		}
	}
	return []string{
		"",
		"--- this command does not return ---",
		e.Why,
		"",
		"It was stopped after " + grace.String() + ", but it never printed what a healthy",
		"start prints (" + e.Expect + "). Something went wrong before it settled.",
	}
}
