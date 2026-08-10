package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Working out what went wrong, where, and why.
//
// A non-zero exit code and a wall of output is not a diagnosis. What a person
// needs is three things: the one line that actually says what failed, the file
// or log where it happened, and a short ranked list of what usually causes
// that — with the command that would confirm each one.
//
// The rules below come from failures seen against this CLI rather than from
// imagination. Each carries what to check, because a possible cause nobody can
// confirm is just a guess with better formatting.

// Location is where the failure happened, as precisely as the output allows.
type Location struct {
	// File and Line come from a parse error that names them.
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	// Log is a log file the command wrote and mentioned.
	Log string `json:"log,omitempty"`
	// Config is the configuration file being acted on.
	Config string `json:"config,omitempty"`
	// Step is the named stage that failed, for multi-step commands.
	Step string `json:"step,omitempty"`
	// Field is the configuration key at fault, when one is named.
	Field string `json:"field,omitempty"`
}

// Empty reports whether nothing could be located.
func (l Location) Empty() bool {
	return l.File == "" && l.Log == "" && l.Config == "" && l.Step == "" && l.Field == ""
}

// Cause is one thing that could explain the failure, and how to confirm it.
type Cause struct {
	Why   string `json:"why"`
	Check string `json:"check,omitempty"`
}

// Diagnosis is the whole answer for one failed command.
type Diagnosis struct {
	// Cause is the single line that says what failed.
	Cause string `json:"cause"`
	// Category groups the failure: config, credentials, network, tool,
	// permission, port, product, timeout, usage.
	Category string   `json:"category"`
	Location Location `json:"location"`
	Possible []Cause  `json:"possible"`
	// Confidence says whether a rule matched or this is the generic fallback.
	Confidence string `json:"confidence"`
}

// rule is one recognisable failure.
type rule struct {
	name     string
	category string
	// match is tried against stdout and stderr together.
	match *regexp.Regexp
	// causes are ranked: most likely first.
	causes []Cause
}

var rules = []rule{
	{
		name: "unknown configuration field", category: "config",
		match: regexp.MustCompile(`field (\S+) not found in type (\S+)`),
		causes: []Cause{
			{Why: "A command wrote a field the loader does not know, so the file it produced cannot be read back.",
				Check: "opencenter cluster export <cluster>  — it will fail at the same line"},
			{Why: "The configuration was written by a newer or older build than the one reading it.",
				Check: "opencenter version, and compare with whatever produced the file"},
			{Why: "The field was added by hand and is not in the schema.",
				Check: "grep -n '<field>' on the configuration file named above"},
		},
	},
	{
		name: "yaml parse error", category: "config",
		match: regexp.MustCompile(`(?i)(yaml|parse).{0,40}(line (\d+)|mapping values|could not find expected)`),
		causes: []Cause{
			{Why: "The configuration is not valid YAML — usually indentation, or a value containing a colon.",
				Check: "sed -n '<line>p' on the file named above"},
			{Why: "A value was written unquoted that YAML reads as something else.",
				Check: "python3 -c \"import yaml,sys; yaml.safe_load(open(sys.argv[1]))\" <file>"},
		},
	},
	{
		// "Error: EOF" alone is the least helpful message in the set. It means
		// the command asked a question and stdin was already closed — which is
		// always the case here, because a command run from a web page has no
		// terminal to answer it.
		name: "waiting for an answer that cannot come", category: "usage",
		match: regexp.MustCompile(`(?im)^\s*(?:error):\s*(?:unexpected\s+)?EOF\s*$|inappropriate ioctl for device|not a terminal`),
		causes: []Cause{
			{Why: "The command is interactive. It asked a question, and there is no terminal here to answer it, so the read returned end-of-file at once.",
				Check: "run it in a terminal, or use the flags that supply the answers"},
			{Why: "Most interactive commands have a non-interactive form.",
				Check: "opencenter <command> --help — look for --yes, --non-interactive, or --set"},
			{Why: "A sibling command may do the same job without asking.",
				Check: "cluster configure asks; cluster init and cluster set do not"},
		},
	},
	{
		// The CLI handles this one well and prints its own Fix block. The
		// diagnosis should agree with it rather than invent a third opinion.
		name: "no active cluster", category: "usage",
		match: regexp.MustCompile(`(?i)no active cluster is set`),
		causes: []Cause{
			{Why: "The command works on whichever cluster is active, and none has been selected yet.",
				Check: "opencenter cluster use <org/cluster>"},
			{Why: "Or name the cluster on the command line instead of selecting one.",
				Check: "append <org/cluster> to the command above"},
			{Why: "There may be no clusters at all yet — the fixture creates one.",
				Check: "opencenter cluster list"},
		},
	},
	{
		name: "cluster not found", category: "usage",
		match: regexp.MustCompile(`cluster (\S+) not found|configuration not found`),
		causes: []Cause{
			{Why: "The cluster name or organization is wrong, or the fixture was never created.",
				Check: "opencenter cluster list"},
			{Why: "The command is looking in a different configuration directory.",
				Check: "echo $OPENCENTER_CONFIG_DIR"},
			{Why: "It exists under an organization, and only the bare name was given.",
				Check: "use org/cluster rather than cluster"},
		},
	},
	{
		name: "port already allocated", category: "port",
		match: regexp.MustCompile(`(?i)port is already allocated|address already in use|bind for [\d.]+:(\d+) failed`),
		causes: []Cause{
			{Why: "Another cluster or process already holds that port. openCenter defaults the Kind API server to 6443, so a second cluster collides with the first.",
				Check: "ss -ltn | grep :6443   and   docker ps --format '{{.Names}}\\t{{.Ports}}'"},
			{Why: "A previous run was interrupted and left its containers behind.",
				Check: "kind get clusters   and   docker ps -a"},
			{Why: "The port can be changed rather than the other cluster removed.",
				Check: "opencenter cluster set <cluster> opencenter.infrastructure.kind.api_server_port=6444"},
		},
	},
	{
		// These causes were guesses until they were tested. Plain kind, with
		// no openCenter involved, was given one node (worked, 34s), then the
		// three nodes openCenter asks for (failed at 251s), then one node on
		// openCenter's pinned image (worked, 34s), then three nodes on kind's
		// own image (failed at 70s). So it is the node count, not the image
		// and not WSL — a one-node cluster builds here perfectly well. What
		// the earlier version of this rule blamed, cgroup v1 and low memory,
		// was wrong on both counts: cgroup v2, 13.9 GB free.
		name: "kubelet did not start", category: "environment",
		match: regexp.MustCompile(`(?i)waiting for the kubelet to start|kubelet.{0,30}healthz|cannot obtain client without bootstrap`),
		causes: []Cause{
			{Why: "Too many kubelets are being started at once for the inotify budget. Each node needs instances, and the default of 128 is not enough for a multi-node cluster beside one already running.",
				Check: "cat /proc/sys/fs/inotify/max_user_instances   — kind wants 512; raise it with sysctl -w fs.inotify.max_user_instances=512"},
			{Why: "Another kind cluster is already running and holding those resources.",
				Check: "kind get clusters   and   docker ps --filter ancestor=kindest/node -q | wc -l"},
			{Why: "The cluster is larger than it needs to be. openCenter defaults to one control plane and two workers; one node is enough to exercise the lifecycle.",
				Check: "opencenter cluster set <cluster> opencenter.infrastructure.kind.worker_count=0"},
			{Why: "The node image and the requested Kubernetes version disagree.",
				Check: "the kubeadm output in the log file named above"},
		},
	},
	{
		name: "authentication rejected", category: "credentials",
		match: regexp.MustCompile(`(?i)\b401\b|unauthoriz|authentication failed|invalid credential`),
		causes: []Cause{
			{Why: "The credential is wrong, expired, or scoped to a different project.",
				Check: "openstack token issue -f value -c project_id"},
			{Why: "No credential reached the command — the sandbox passes only what the panel holds.",
				Check: "open Credentials above and confirm the values are saved"},
			{Why: "The right variables are set but for a different auth method.",
				Check: "an application credential needs ID and SECRET; a password needs USERNAME and PROJECT"},
		},
	},
	{
		name: "permission denied by the provider", category: "credentials",
		match: regexp.MustCompile(`(?i)\b403\b|forbidden|not authorized to perform`),
		causes: []Cause{
			{Why: "The credential authenticates but lacks the role for this action.",
				Check: "openstack role assignment list --user <user> --project <project>"},
			{Why: "The project is right but the credential is scoped to another one.",
				Check: "openstack token issue -f value -c project_id"},
		},
	},
	{
		name: "endpoint unreachable", category: "network",
		match: regexp.MustCompile(`(?i)connection refused|no such host|i/o timeout|dial tcp|network is unreachable|context deadline exceeded`),
		causes: []Cause{
			{Why: "The endpoint is wrong, or nothing is listening on it.",
				Check: "curl -sS -o /dev/null -w '%{http_code}' <the URL in the error>"},
			{Why: "A proxy or VPN is needed to reach it from here.",
				Check: "echo $HTTPS_PROXY $NO_PROXY"},
			{Why: "The service is up but slower than the timeout allows.",
				Check: "run the same command from a terminal and time it"},
		},
	},
	{
		name: "external tool missing", category: "tool",
		match: regexp.MustCompile(`(?i)executable file not found|command not found|(\bsops\b|\bkubectl\b|\bkind\b|\bhelm\b|\btofu\b|\bage\b).{0,30}(not found|not installed)`),
		causes: []Cause{
			{Why: "The tool is not on PATH for the sandbox the command ran in.",
				Check: "command -v sops kubectl kind helm tofu age"},
			{Why: "It is installed for your shell but not where the console can see it.",
				Check: "echo $PATH, and compare with the PATH the sandbox uses"},
		},
	},
	{
		name: "sops or key failure", category: "tool",
		match: regexp.MustCompile(`(?i)sops.{0,40}(fail|error)|failed to load age key|no matching creation rules`),
		causes: []Cause{
			{Why: "sops is not installed, so nothing can be encrypted or decrypted.",
				Check: "command -v sops age"},
			{Why: "There is no Age key, or it is not where sops looks for it.",
				Check: "opencenter secrets keys generate   then   ls ~/.config/sops/age/keys.txt"},
			{Why: "The .sops.yaml rules do not match the file being encrypted.",
				Check: "cat .sops.yaml in the directory the command ran from"},
		},
	},
	{
		name: "permission denied on a file", category: "permission",
		match: regexp.MustCompile(`(?i)permission denied|read-only file system|operation not permitted`),
		causes: []Cause{
			{Why: "The configuration or state directory is not writable by this user.",
				Check: "ls -ld $OPENCENTER_CONFIG_DIR"},
			{Why: "A file was created by another user or by a container as root.",
				Check: "ls -l on the path in the error"},
		},
	},
	{
		name: "validation reported work to do", category: "config",
		match: regexp.MustCompile(`(?i)validation failed|Action Items:`),
		causes: []Cause{
			{Why: "The configuration is readable but incomplete — a fresh one always is.",
				Check: "opencenter cluster validate <cluster> --output json | python3 -m json.tool"},
			{Why: "Placeholder secrets are still in place.",
				Check: "look for CHANGEME in the generated manifests"},
			{Why: "The GitOps repository has uncommitted changes.",
				Check: "git -C <gitops dir> status --short"},
		},
	},
	{
		name: "missing required argument", category: "usage",
		match: regexp.MustCompile(`(?i)(required|reason is required|accepts \d+ arg|missing).{0,40}(flag|argument|value)|unknown command|unknown flag`),
		causes: []Cause{
			{Why: "The invocation is missing something the command requires.",
				Check: "opencenter <command> --help"},
			{Why: "A placeholder in the ready-to-run line was never replaced.",
				Check: "look for BACKUP_ID or SECRET_NAME in the line above"},
		},
	},
	{
		name: "already exists", category: "usage",
		match: regexp.MustCompile(`(?i)already exists`),
		causes: []Cause{
			{Why: "The thing is already there — often the correct outcome, not a fault.",
				Check: "opencenter cluster list"},
			{Why: "It can be replaced deliberately.", Check: "add --force"},
		},
	},
	{
		name: "quota or limit", category: "provider",
		match: regexp.MustCompile(`(?i)quota exceeded|limit exceeded|insufficient|\b429\b|too many requests`),
		causes: []Cause{
			{Why: "The project has no room left for what was asked.",
				Check: "openstack quota show"},
			{Why: "The provider is rate limiting; the command may succeed if retried.",
				Check: "wait and run it again"},
		},
	},
	{
		name: "a named step failed", category: "product",
		match: regexp.MustCompile(`step "([^"]+)" failed`),
		causes: []Cause{
			{Why: "One stage of a multi-step operation failed; the rest never ran.",
				Check: "the log file named above holds that step's full output"},
			{Why: "The operation can usually be resumed rather than restarted.",
				Check: "look for a resume state file in the output"},
		},
	},
}

var (
	fileLinePattern = regexp.MustCompile(`(?m)line (\d+): field (\S+)`)
	linePattern     = regexp.MustCompile(`(?i)line (\d+)`)
	logPattern      = regexp.MustCompile(`(?i)log file:\s*(\S+)`)
	configPattern   = regexp.MustCompile(`(/\S+?-config\.ya?ml)`)
	pathPattern     = regexp.MustCompile(`(/\S+\.(?:ya?ml|json|log|txt))`)
	// Directories have no extension, so the pattern above misses them. The
	// path in "blueprints directory does not exist: /tmp/…" is the single
	// most useful thing in that message and must not be dropped.
	missingPathPattern = regexp.MustCompile(
		`(?i)(?:does not exist|no such file or directory|cannot find)[:\s]+([^\s)]+)`)
	stepPattern  = regexp.MustCompile(`step "([^"]+)"`)
	fieldPattern = regexp.MustCompile(`field (\S+) not found`)
	errorLine    = regexp.MustCompile(`(?im)^\s*(?:Error|ERROR|error):\s*(.+)$`)
)

// diagnose reads a failed command's output and says what went wrong.
//
// It returns nil when the command succeeded: a diagnosis of success is noise.
func diagnose(stdout, stderr string, exitCode int, timedOut bool) *Diagnosis {
	if exitCode == 0 && !timedOut {
		return nil
	}
	combined := stdout + "\n" + stderr

	// The bench's own commands answer for themselves.
	//
	// Everything below this point assumes the failure came from the openCenter
	// CLI, and offers CLI advice: check the arguments, run cluster doctor, this
	// exit code is undocumented. For `bench gitops` and `bench actions` all
	// three are wrong — their exit codes ARE documented, and cluster doctor has
	// nothing to say about a pull request that was refused. Worse, the message
	// on screen has usually already said exactly what to do, and the panel
	// underneath then contradicts it.
	if bench := benchDiagnosis(combined, exitCode); bench != nil {
		return bench
	}

	if timedOut {
		return &Diagnosis{
			Cause:      "The command did not return within the time allowed.",
			Category:   "timeout",
			Confidence: "certain",
			Location:   locate(combined),
			Possible: []Cause{
				{Why: "It is waiting for input that will never arrive — a confirmation prompt with no terminal.",
					Check: "add --yes, or check whether the command prompts"},
				{Why: "It is a long-running or scheduling command that stays in the foreground by design.",
					Check: "run it in a terminal and see whether it ever returns"},
				{Why: "It is genuinely hung on something external.",
					Check: "the log file named above, if there is one"},
			},
		}
	}

	diagnosis := &Diagnosis{
		Cause:      causeLine(combined, exitCode),
		Category:   "unknown",
		Confidence: "guess",
		Location:   locate(combined),
	}

	// The first matching rule wins: they are ordered from most specific to
	// most general, and a specific match is a better answer than three vague
	// ones.
	for _, candidate := range rules {
		if candidate.match.MatchString(combined) {
			diagnosis.Category = candidate.category
			diagnosis.Possible = candidate.causes
			diagnosis.Confidence = "matched: " + candidate.name
			break
		}
	}

	if len(diagnosis.Possible) == 0 {
		diagnosis.Possible = genericCauses(exitCode)
	}

	// Exit 3 is documented, so say what it means whatever else matched.
	if exitCode == 3 {
		diagnosis.Category = "usage"
		diagnosis.Possible = append([]Cause{{
			Why:   "Exit code 3 is documented as the configuration not existing.",
			Check: "opencenter cluster list",
		}}, diagnosis.Possible...)
	}

	return diagnosis
}

// causeLine picks the single line that says what failed.
func causeLine(output string, exitCode int) string {
	// An explicit Error: line is what the CLI itself considers the cause.
	if match := errorLine.FindStringSubmatch(output); match != nil {
		cause := strings.TrimSpace(match[1])

		// An error ending in a colon is a heading, not the fault: the
		// specific one is on the line beneath it. "YAML type errors (4):"
		// tells nobody which field is wrong.
		if strings.HasSuffix(cause, ":") {
			if detail := firstDetailAfter(output, match[0]); detail != "" {
				return trimTo(cause+" "+detail, 300)
			}
		}
		return trimTo(cause, 300)
	}
	// Otherwise the last non-empty line that looks like a complaint.
	lines := strings.Split(output, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "fail") || strings.Contains(lower, "error") ||
			strings.Contains(lower, "cannot") || strings.Contains(lower, "unable") ||
			strings.Contains(lower, "not found") || strings.Contains(lower, "denied") {
			return trimTo(line, 300)
		}
	}
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return trimTo(line, 300)
		}
	}
	return fmt.Sprintf("The command exited %d without saying why.", exitCode)
}

// firstDetailAfter returns the first bulleted or indented detail line that
// follows the given heading. CLIs report "4 errors:" then list them; the list
// is the useful part.
func firstDetailAfter(output, heading string) string {
	position := strings.Index(output, heading)
	if position < 0 {
		return ""
	}
	rest := output[position+len(heading):]
	for _, line := range strings.Split(rest, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		trimmed = strings.TrimPrefix(trimmed, "- ")
		trimmed = strings.TrimPrefix(trimmed, "* ")
		return strings.TrimSpace(trimmed)
	}
	return ""
}

// locate pulls out every position the output offers.
func locate(output string) Location {
	location := Location{}

	if match := fileLinePattern.FindStringSubmatch(output); match != nil {
		location.Line, _ = strconv.Atoi(match[1])
		location.Field = match[2]
	} else if match := linePattern.FindStringSubmatch(output); match != nil {
		location.Line, _ = strconv.Atoi(match[1])
	}
	if match := fieldPattern.FindStringSubmatch(output); match != nil && location.Field == "" {
		location.Field = match[1]
	}
	if match := logPattern.FindStringSubmatch(output); match != nil {
		location.Log = match[1]
	}
	if match := configPattern.FindStringSubmatch(output); match != nil {
		location.Config = match[1]
		location.File = match[1]
	} else if match := pathPattern.FindStringSubmatch(output); match != nil {
		location.File = match[1]
	} else if match := missingPathPattern.FindStringSubmatch(output); match != nil {
		location.File = match[1]
	}
	if match := stepPattern.FindStringSubmatch(output); match != nil {
		location.Step = match[1]
	}
	return location
}

// benchDiagnosis explains a failure from the bench's own subcommands.
//
// Recognised by the headline they print rather than by the command id, because
// the same lines appear whether the step ran from the card, from a script or
// from a terminal — and the headline is what a reader is looking at.
//
// Returns nil for anything else, so the CLI diagnosis below is untouched.
func benchDiagnosis(combined string, exitCode int) *Diagnosis {
	// The headlines the binary prints, plus the refusals the card prints before
	// ever reaching it. A step that stops at its own approval check produces no
	// headline at all, so matching only on those sent the commonest failure of
	// the lot — "you did not tick the box" — back to the generic CLI advice.
	markers := []string{
		"ACTIONS SETUP —", "GITOPS UPDATE —", "PIPELINE TRIGGERED", "TRIGGER —",
		// Both spellings. The card shouts REFUSED before it spends a clone; the
		// binary says "refused:" lower case when it gets there first. Matching
		// one and not the other sent half the refusals back to generic CLI
		// advice, which is how the same wrong panel kept reappearing.
		"REFUSED —", "Not approved —", "refused:",
		"OPENCLI_ALLOW_ACTIONS_SETUP", "OPENCLI_ALLOW_GITOPS_UPDATE",
	}
	found := false
	for _, marker := range markers {
		if strings.Contains(combined, marker) {
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	// The documented table, shared by both subcommands. A reader who knows only
	// the number still knows whether to fix a setting, tick a box, or page
	// somebody about a credential.
	meaning := map[int]struct{ cause, why, check string }{
		2: {"The configuration is incomplete or malformed.",
			"A repository, path or branch is missing or not in a form this accepts.",
			"press Check settings — it prints what resolved, with no secret"},
		3: {"The run may not be promoted.",
			"The quality gate refused: something failed, was cancelled, or left resources behind.",
			"open Results and fix what failed first"},
		4: {"Refused: it was not approved, or a guard stopped it.",
			"Both gates are required, and an existing customised workflow is never replaced silently.",
			"tick the approval box, and set replace to yes only after reading the diff"},
		5: {"Git could not do what was asked.",
			"Usually the credential cannot write, or the branch is protected.",
			"check the key or token has write access to that repository"},
		6: {"The pull request could not be opened.",
			"The branch was pushed; only the API call failed — often a token without pull-requests:write.",
			"open the request by hand from the branch, or fix the token's permissions"},
	}

	// A refusal that never left the card says exactly what is missing, and the
	// panel repeating a generic version of it underneath only adds doubt.
	// A gate that is shut. Which of the two, and what to do about it, differ
	// enough to be worth separating: one is a checkbox on screen, the other is
	// an environment variable read when the console started — and telling
	// somebody to "set" the second leaves them exporting it in a shell the
	// console cannot see.
	if strings.Contains(combined, "is not set") &&
		(strings.Contains(combined, "OPENCLI_ALLOW_ACTIONS_SETUP") ||
			strings.Contains(combined, "OPENCLI_ALLOW_GITOPS_UPDATE")) {
		gate := "OPENCLI_ALLOW_ACTIONS_SETUP"
		if strings.Contains(combined, "OPENCLI_ALLOW_GITOPS_UPDATE") {
			gate = "OPENCLI_ALLOW_GITOPS_UPDATE"
		}
		return &Diagnosis{
			Cause:      "The environment gate is not set, so this console may not write to a remote repository.",
			Category:   "test-bench",
			Confidence: "certain",
			Location:   locate(combined),
			Possible: []Cause{
				{Why: "The gate is read once, when the console starts. Exporting it in another shell has no effect on this process.",
					Check: gate + "=1 ./bin/testlab --addr 127.0.0.1:7700"},
				{Why: "It is separate from the box on screen on purpose: the environment's permission is given by whoever started the console, the approval by whoever is looking at the page.",
					Check: "both are required; neither alone is enough"},
			},
		}
	}
	if strings.Contains(combined, "REFUSED —") || strings.Contains(combined, "Not approved —") ||
		strings.Contains(combined, "not approved") {
		return &Diagnosis{
			Cause:      "Refused before anything ran: the approval box is not ticked.",
			Category:   "test-bench",
			Confidence: "certain",
			Location:   locate(combined),
			Possible: []Cause{{
				Why:   "This step writes to somebody's repository, so it needs the box beside it ticked as well as the gate in the environment.",
				Check: "set the approval beside this button to yes, then press it again",
			}},
		}
	}

	entry, known := meaning[exitCode]
	if !known {
		return &Diagnosis{
			Cause:      "The bench step did not complete.",
			Category:   "test-bench",
			Confidence: "certain",
			Location:   locate(combined),
			Possible: []Cause{{
				Why:   "The step list above says which phase stopped and why.",
				Check: "read the FAILED line and the \"why not\" beneath it",
			}},
		}
	}
	return &Diagnosis{
		Cause:      entry.cause,
		Category:   "test-bench",
		Confidence: "certain",
		Location:   locate(combined),
		Possible: []Cause{
			{Why: entry.why, Check: entry.check},
			{Why: "The step list above names the phase that stopped; everything after it is skipped, not failed.",
				Check: "read the FAILED line"},
		},
	}
}

// genericCauses is the fallback when nothing specific matched. It is
// deliberately short: three plausible things beat ten.
func genericCauses(exitCode int) []Cause {
	return []Cause{
		{Why: "The arguments may not be what this command expects.",
			Check: "opencenter <command> --help"},
		{Why: "Something the command depends on may be missing — a cluster, a key, a tool.",
			Check: "opencenter cluster doctor <cluster>"},
		{Why: fmt.Sprintf("Exit %d is not one of the documented codes, so the message above is the best evidence.", exitCode),
			Check: "run the same line in a terminal with --log-level debug"},
	}
}

func trimTo(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
