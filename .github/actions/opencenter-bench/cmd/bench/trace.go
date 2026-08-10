package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Who called the bench, from where, on what commit, and how many times.
//
// The action already reports what a run found. What it could not answer was
// "how often is this thing actually being used, and by whom" — a question about
// the integration rather than about any one run, so no single run's report can
// hold it. The answer has to accumulate somewhere outside a run, which is what
// the ledger is.
//
// Append-only JSONL rather than a counter file. A count on its own can only say
// "47"; it cannot say which repository stopped calling last month, or that every
// invocation this week came from one branch. Keeping the records and deriving
// the count from them costs one line per run and answers both.
//
// Deliberately not part of a Run. A trace is written before the bench has run
// anything — the point is to record the call, including calls whose run then
// fails — so it cannot live in run.json, which does not exist yet.

// ledgerName is the file inside the artifacts directory. One file, appended by
// every invocation.
const ledgerName = "invocations.jsonl"

// Invocation is one recorded call of the bench through GitHub Actions.
//
// Timestamp is the machine-readable field; Date and Time are split out beside
// it because this is read by people in a CI log as often as by a parser, and
// asking somebody to eyeball an RFC3339 string to answer "when did this run" is
// a small unkindness that costs nothing to avoid.
type Invocation struct {
	Sequence  int    `json:"sequence"`
	Timestamp string `json:"timestamp"`
	Date      string `json:"date"`
	Time      string `json:"time"`

	// Repository is the repository whose Actions workflow called the bench,
	// which is not this repository — that is the whole point of the field.
	Repository string `json:"repository"`
	Commit     string `json:"commit,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Event      string `json:"event,omitempty"`
	Actor      string `json:"actor,omitempty"`

	WorkflowRunID     string `json:"workflowRunId,omitempty"`
	WorkflowRunNumber string `json:"workflowRunNumber,omitempty"`
	RunURL            string `json:"runUrl,omitempty"`

	BenchVersion string `json:"benchVersion,omitempty"`
}

// commandTrace handles `bench trace <subcommand>`.
func commandTrace(root string, args []string) error {
	subcommand := "count"
	rest := []string(nil)
	if len(args) > 0 {
		subcommand, rest = args[0], args[1:]
	}

	switch subcommand {
	case "record":
		return traceRecord(root, rest)
	case "count":
		return traceCount(root)
	case "list":
		return traceList(root, rest)
	default:
		fmt.Print(traceUsage)
		return exitWith(2, fmt.Sprintf("unknown trace subcommand %q", subcommand))
	}
}

const traceUsage = `bench trace <subcommand>

  record    record this invocation and print the running count
  count     print the running count and a per-repository breakdown
  list [-n] print the most recent invocations (default 20)

  The ledger is artifacts/invocations.jsonl, one JSON object per line. Fields
  are read from the GITHUB_* variables GitHub Actions sets, so no workflow has
  to pass them; --repository and --commit override for a local test.

  The runner is thrown away after every job, so persist artifacts/ between runs
  (actions/cache, or the uploaded artifact) or the count restarts at 1.
`

// traceRecord appends one invocation and reports where that leaves the count.
func traceRecord(root string, args []string) error {
	invocation := Invocation{
		Repository:        env("GITHUB_REPOSITORY", ""),
		Commit:            env("GITHUB_SHA", ""),
		Ref:               env("GITHUB_REF_NAME", env("GITHUB_REF", "")),
		Event:             env("GITHUB_EVENT_NAME", ""),
		Actor:             env("GITHUB_ACTOR", ""),
		WorkflowRunID:     env("GITHUB_RUN_ID", ""),
		WorkflowRunNumber: env("GITHUB_RUN_NUMBER", ""),
		BenchVersion:      benchVersion,
	}

	// Overrides, so this is testable off a runner and usable from a local shell.
	for index := 0; index+1 < len(args); index += 2 {
		value := args[index+1]
		switch args[index] {
		case "--repository":
			invocation.Repository = value
		case "--commit":
			invocation.Commit = value
		case "--ref":
			invocation.Ref = value
		case "--event":
			invocation.Event = value
		case "--actor":
			invocation.Actor = value
		}
	}

	// Recorded rather than refused. A call from outside Actions is still a call,
	// and a ledger that silently drops the ones it cannot fully describe would
	// undercount exactly when somebody is testing by hand.
	if strings.TrimSpace(invocation.Repository) == "" {
		invocation.Repository = "(local)"
	}

	now := time.Now().UTC()
	invocation.Timestamp = now.Format(time.RFC3339)
	invocation.Date = now.Format("2006-01-02")
	invocation.Time = now.Format("15:04:05") + " UTC"

	if invocation.RunURL == "" && invocation.WorkflowRunID != "" {
		server := env("GITHUB_SERVER_URL", "https://github.com")
		invocation.RunURL = fmt.Sprintf("%s/%s/actions/runs/%s",
			server, invocation.Repository, invocation.WorkflowRunID)
	}

	existing, err := readLedger(root)
	if err != nil {
		return err
	}
	invocation.Sequence = len(existing) + 1

	if err := appendLedger(root, invocation); err != nil {
		return err
	}

	fmt.Printf("  call #%d to the Test Bench\n\n", invocation.Sequence)
	fmt.Printf("    %-12s %s\n", "repository", invocation.Repository)
	fmt.Printf("    %-12s %s\n", "commit", shortCommit(invocation.Commit))
	fmt.Printf("    %-12s %s\n", "ref", orDash(invocation.Ref))
	fmt.Printf("    %-12s %s\n", "event", orDash(invocation.Event))
	fmt.Printf("    %-12s %s\n", "actor", orDash(invocation.Actor))
	fmt.Printf("    %-12s %s at %s\n\n", "when", invocation.Date, invocation.Time)

	writeTraceOutputs(invocation, append(existing, invocation))
	return nil
}

// traceCount prints the total and who it came from.
func traceCount(root string) error {
	records, err := readLedger(root)
	if err != nil {
		return err
	}
	byRepository := map[string]int{}
	for _, record := range records {
		byRepository[record.Repository]++
	}

	fmt.Printf("\n  %d call(s) to the Test Bench\n\n", len(records))
	for repository, count := range byRepository {
		fmt.Printf("    %5d  %s\n", count, repository)
	}
	if len(records) > 0 {
		last := records[len(records)-1]
		fmt.Printf("\n  most recent: %s at %s from %s (%s)\n\n",
			last.Date, last.Time, last.Repository, shortCommit(last.Commit))
	}
	return nil
}

// traceList prints recent invocations newest last, which is the order a log
// reader expects.
func traceList(root string, args []string) error {
	limit := 20
	for index := 0; index+1 < len(args); index += 2 {
		if args[index] == "-n" || args[index] == "--limit" {
			if parsed, err := strconv.Atoi(args[index+1]); err == nil && parsed > 0 {
				limit = parsed
			}
		}
	}

	records, err := readLedger(root)
	if err != nil {
		return err
	}
	if len(records) > limit {
		records = records[len(records)-limit:]
	}

	// truncate appends an ellipsis, so it is asked for three fewer than the
	// column is wide; otherwise a long repository name pushes every column after
	// it out of line and the table stops being one.
	fmt.Printf("\n  %-4s %-10s %-8s %-34s %-12s %s\n",
		"#", "date", "time", "repository", "commit", "event")
	for _, record := range records {
		fmt.Printf("  %-4d %-10s %-8s %-34s %-12s %s\n",
			record.Sequence, record.Date, strings.TrimSuffix(record.Time, " UTC"),
			truncate(record.Repository, 31), shortCommit(record.Commit),
			orDash(record.Event))
	}
	fmt.Println()
	return nil
}

// --- the ledger ---------------------------------------------------------------

func ledgerPath(root string) string {
	return filepath.Join(root, "artifacts", ledgerName)
}

// readLedger returns every record, skipping any line that will not parse.
//
// A corrupt line is skipped rather than fatal. The ledger is append-only from
// concurrent jobs; a torn write costs one record, and refusing to read the file
// at all would turn that into losing the whole history.
func readLedger(root string) ([]Invocation, error) {
	file, err := os.Open(ledgerPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var records []Invocation
	scanner := bufio.NewScanner(file)
	// Records are small, but a long RunURL plus a long repository name can pass
	// the default 64K on a pathological line; give it room rather than silently
	// stopping at one.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Invocation
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}

// appendLedger adds one record. O_APPEND so two jobs writing at once interleave
// whole lines rather than overwriting each other's offsets.
func appendLedger(root string, invocation Invocation) error {
	path := ledgerPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.Marshal(invocation)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = file.Write(append(encoded, '\n'))
	return err
}

// writeTraceOutputs publishes the count to the workflow, so a summary or a
// badge can show it without re-reading the ledger.
func writeTraceOutputs(invocation Invocation, all []Invocation) {
	path := strings.TrimSpace(os.Getenv("GITHUB_OUTPUT"))
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()

	fromThisRepository := 0
	for _, record := range all {
		if record.Repository == invocation.Repository {
			fromThisRepository++
		}
	}

	for key, value := range map[string]string{
		"invocation_sequence":   strconv.Itoa(invocation.Sequence),
		"invocation_total":      strconv.Itoa(len(all)),
		"invocation_from_repo":  strconv.Itoa(fromThisRepository),
		"invocation_repository": invocation.Repository,
		"invocation_commit":     shortCommit(invocation.Commit),
		"invocation_date":       invocation.Date,
		"invocation_time":       invocation.Time,
	} {
		fmt.Fprintf(file, "%s<<__BENCH_EOF__\n%s\n__BENCH_EOF__\n", key, value)
	}
}

// --- small helpers --------------------------------------------------------------

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func shortCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if len(commit) > 12 {
		return commit[:12]
	}
	if commit == "" {
		return "-"
	}
	return commit
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
