// Package experimental adds stages to the command table that the CLI's own
// command tree does not produce.
//
// The seven lifecycle stages are a fact: every command declares one, and the
// generator reads them out of the binary. A stage here is the bench's opinion
// about how a feature should be exercised — Kafka is a scenario across
// existing commands, not a new part of a cluster's life — so it is loaded
// separately, marked experimental, and drawn differently.
//
// Keeping the two apart matters. A reader should be able to tell which parts
// of the page come from the binary and which come from us.
package experimental

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Command is one ready-to-run invocation in an experimental stage.
type Command struct {
	ID       string   `yaml:"id"       json:"id"`
	Task     string   `yaml:"task"     json:"task"`
	Short    string   `yaml:"short"    json:"short"`
	Ready    string   `yaml:"ready"    json:"ready"`
	Plain    string   `yaml:"plain"    json:"plain"`
	Metaphor string   `yaml:"metaphor" json:"metaphor"`
	Needs    []string `yaml:"needs"    json:"needs"`
	Mutating bool     `yaml:"mutating" json:"mutating"`
	// Install is the command that fixes a missing requirement. It is shown
	// and never run.
	//
	// Every other command on this page runs the CLI in a throwaway sandbox.
	// These are the exception: `sudo apt install`, `curl | sh`. Wiring a Run
	// button to them would make the console a way to install packages as root
	// on the machine it happens to be serving from, which is a much larger
	// thing than a test bench. The check beside it is runnable precisely
	// because it only ever reads.
	Install string `yaml:"install" json:"install,omitempty"`
	// Risk says what running the install command would touch, and decides the
	// wording on its button:
	//
	//   safe  — reads only. "Run".
	//   host  — writes under $HOME: a tool into ~/.local, a line into
	//           ~/.bashrc, a key into ~/.ssh. "Run on the host".
	//   root  — needs sudo. No Run button at all; the console has no terminal
	//           to type a password into, so a button here would hang and then
	//           fail, which is a worse answer than not offering one.
	//
	// Derived from the command text when the file does not say.
	Risk string `yaml:"risk" json:"risk,omitempty"`
	// Shell marks a command that is a shell line rather than arguments to the
	// opencenter binary. Prerequisite checks are the only ones: asking
	// "is kubectl installed?" cannot be phrased as an opencenter subcommand.
	//
	// The server never takes a shell command's text from the request. It looks
	// the stored text up by id, because a shell string supplied by a caller
	// and executed by the server is remote code execution, not a test bench.
	Shell bool `yaml:"shell" json:"shell,omitempty"`
	// Inputs are fields shown above the command, whose values are handed to it
	// as environment variables.
	//
	// Environment variables rather than substitution into the command text,
	// and that is the whole design. A cluster name typed into a form and
	// spliced into a shell line is command injection with extra steps; the
	// same value in the environment is inert, and the commands already read
	// ${OPENCLI_ORG:-myorg} and friends. Nothing a user types ever becomes
	// part of a command.
	Inputs []Input `yaml:"inputs" json:"inputs,omitempty"`
}

// Input is one form field beside a command.
type Input struct {
	// Env is the variable the value is passed as, and doubles as the field's
	// identity. The server accepts a value only for a variable the step itself
	// declares — an allowlist, so a request cannot set PATH or LD_PRELOAD.
	Env         string   `yaml:"env"         json:"env"`
	Label       string   `yaml:"label"       json:"label"`
	Kind        string   `yaml:"kind"        json:"kind,omitempty"`
	Default     string   `yaml:"default"     json:"default,omitempty"`
	Placeholder string   `yaml:"placeholder" json:"placeholder,omitempty"`
	Options     []string `yaml:"options"     json:"options,omitempty"`
	Help        string   `yaml:"help"        json:"help,omitempty"`
}

// Field kinds. A secret is masked, never echoed back to the page, and
// registered for redaction before the command runs.
const (
	InputText   = "text"
	InputSelect = "select"
	InputSecret = "secret"
)

// Secret reports whether a field holds credential material.
func (i Input) Secret() bool { return i.Kind == InputSecret }

// Stage is an added band in the command table.
type Stage struct {
	ID           string `yaml:"id"           json:"id"`
	Name         string `yaml:"name"         json:"name"`
	Experimental bool   `yaml:"experimental" json:"experimental"`
	After        string `yaml:"after"        json:"after"`
	// Before places a stage ahead of the one it names. Prerequisites use it:
	// checking whether git and kubectl exist belongs in front of `init`, not
	// after teardown, because none of the seven stages can run without them.
	Before   string `yaml:"before" json:"before"`
	Colour   string `yaml:"colour"       json:"colour"`
	OnColour string `yaml:"on_colour"    json:"on_colour"`
	Summary  string `yaml:"summary"      json:"summary"`
	// HowItWorks is the long explanation: what the stage does, what it writes,
	// and what it needs. Separate from Summary because they answer different
	// questions — Summary is the one line on the band, this is what somebody
	// reads once before trusting the stage with a credential.
	//
	// Preformatted. It is displayed verbatim, so its own line breaks and
	// indentation are the layout; wrapping it would destroy the tables in it.
	HowItWorks string    `yaml:"how_it_works" json:"how_it_works,omitempty"`
	Commands   []Command `yaml:"commands"     json:"commands"`
}

type file struct {
	Stages []Stage `yaml:"stages"`
}

// Load reads config/experimental-stages.yaml. A missing file is not an error:
// the extra stages are additions, and the console's job is the command table
// with or without them.
func Load(root string) []Stage {
	var stages []Stage

	// Several files, one mechanism. A stage file is a stage file, and adding a
	// second way to declare one is how the two drift — so config/stage files
	// are simply read in turn and their stages appended.
	//
	// experimental-stages.yaml holds a `stages:` list. The single-stage files
	// hold one `stage:` block, because a file about one stage reads better that
	// way; both shapes are accepted.
	for _, name := range []string{
		"experimental-stages.yaml",
		"github-actions-gitops.yaml",
		// There was a second card here for the lifecycle workflow, and it was
		// removed. It declared the same environment variables as the one above —
		// deliberately, so nothing had to be typed twice — and the panel renders
		// each field once, so the second card drew an empty repository box, an
		// empty credential line and a "nothing to save" button. A panel that
		// shows blanks for values that are set reads as broken.
		//
		// The capability is not gone: `bench actions … --kind=opencenter-e2e`
		// installs it, and e2eaction.sh is the four buttons as a script.
		// No promotion stage. Shipping a build to a delivery repository is not
		// something this console asks about any more: it had nothing to do with
		// the rest of the rail, and a stage nobody opens is a stage that gets
		// answered wrongly the once somebody does.
		//
		// The engine behind it stays. internal/gitopsupdate is what the GitHub
		// Actions setup clones, commits, pushes and opens pull requests with,
		// and `bench gitops` is what action.yml calls when a CI run is allowed
		// to promote. Removing the stage removes the console page, not the
		// capability.
	} {
		raw, err := os.ReadFile(filepath.Join(root, "config", name))
		if err != nil {
			continue
		}
		var many file
		if err := yaml.Unmarshal(raw, &many); err == nil && len(many.Stages) > 0 {
			stages = append(stages, many.Stages...)
			continue
		}
		var one singleStage
		if err := yaml.Unmarshal(raw, &one); err == nil && one.Stage.ID != "" {
			stages = append(stages, one.Stage)
		}
	}
	return stages
}

// singleStage is a file describing one stage, as
// config/github-actions-gitops.yaml does.
type singleStage struct {
	Stage Stage `yaml:"stage"`
}

// InsertOrder returns the stage order with the experimental stages placed
// after the stage each one names. A stage whose `after` names nothing real
// goes at the end rather than disappearing.
func InsertOrder(order []string, stages []Stage) []string {
	out := make([]string, 0, len(order)+len(stages))
	placed := map[string]bool{}

	for _, stage := range order {
		// Anything asking to sit ahead of this stage goes in first, so a
		// stage declaring `before: init` really does land at position one
		// rather than second.
		for _, extra := range stages {
			if extra.Before == stage && !placed[extra.ID] {
				out = append(out, extra.ID)
				placed[extra.ID] = true
			}
		}
		out = append(out, stage)
		for _, extra := range stages {
			if extra.After == stage && !placed[extra.ID] {
				out = append(out, extra.ID)
				placed[extra.ID] = true
			}
		}
	}
	for _, extra := range stages {
		if !placed[extra.ID] {
			out = append(out, extra.ID)
		}
	}
	return out
}

// Reference substitutes {{ref}} with an environment's own org/cluster, so one
// definition serves every environment rather than four near-copies.
func Reference(ready, org, cluster string) string {
	reference := cluster
	if org != "" && cluster != "" && !strings.Contains(cluster, "/") {
		reference = org + "/" + cluster
	}
	return strings.ReplaceAll(ready, "{{ref}}", reference)
}
