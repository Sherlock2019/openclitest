package experimental

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The prerequisites stage: what must exist before stage 1 can do anything.
//
// It is built from config/quickstart.yaml — a short, ordered list of the
// things somebody setting up actually works through.
//
// It used to be built from config/prerequisites.yaml, which lists twenty-two
// items across five groups. That file is exhaustive because the preflight
// panel needs it to be: it gates environments and knows about quota headroom
// and age keys. As the first screen anyone sees it was the wrong shape —
// nobody needs to be told about SOPS recipients before they have a binary to
// run. The two are separate now, and prerequisites.yaml still feeds preflight
// unchanged.
//
// The older file is still read when quickstart.yaml is absent, so a checkout
// that has not been updated keeps working rather than silently losing stage 0.
//
// Unlike the Kafka stage this one is not experimental. It is not the bench's
// opinion about how to exercise a feature; it is the plain fact that
// `cluster init` cannot run without a binary to run it.

// quickstartFile is config/quickstart.yaml: a stage and a flat list of steps.
type quickstartFile struct {
	Stage stageBlock  `yaml:"stage"`
	Steps []stageStep `yaml:"steps"`
}

type stageBlock struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Before   string `yaml:"before"`
	Colour   string `yaml:"colour"`
	OnColour string `yaml:"on_colour"`
	Summary  string `yaml:"summary"`
}

// stageStep is one numbered thing to do.
type stageStep struct {
	ID       string   `yaml:"id"`
	Name     string   `yaml:"name"`
	Detail   string   `yaml:"detail"`
	Why      string   `yaml:"why"`
	Metaphor string   `yaml:"metaphor"`
	Check    string   `yaml:"check"`
	Setup    string   `yaml:"setup"`
	Risk     string   `yaml:"risk"`
	Needs    []string `yaml:"needs"`
	Inputs   []Input  `yaml:"inputs"`
}

// Risk levels for a setup command.
const (
	RiskSafe = "safe"
	RiskHost = "host"
	RiskRoot = "root"
)

// riskOf decides what a setup command would touch.
//
// Derived rather than required, so a new step is classified the moment it is
// added rather than needing a hand annotation; the file can still say `risk:`
// and that wins.
//
// The sudo test errs towards root deliberately. Mistaking a safe command for
// one needing a password shows a more cautious label, which is a small
// annoyance. The other mistake tells somebody a command is harmless when it
// will ask for their password.
func riskOf(setup string) string {
	if strings.TrimSpace(setup) == "" {
		return RiskSafe
	}
	for _, line := range strings.Split(setup, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "sudo ") || strings.Contains(trimmed, "apt install") ||
			strings.Contains(trimmed, "apt-get install") || strings.Contains(trimmed, "xcode-select") {
			return RiskRoot
		}
	}
	// What is left writes under the user's own home: mise into ~/.local, a key
	// into ~/.ssh, a line into ~/.bashrc. Runnable, but the button says so.
	return RiskHost
}

// LoadPrerequisites builds the stage.
//
// A missing file returns (nil, nil): the console's job is the command table and
// it still does it without stage 0. A file that exists and cannot be used
// returns an error, and the caller says so.
//
// That distinction is worth the extra return value. An earlier version
// swallowed a parse error and returned nil, and one unquoted colon in a check
// made the whole stage vanish with nothing to say why — the console started
// cleanly, reported its usual command count, and was simply missing its rows.
func LoadPrerequisites(root string) (*Stage, error) {
	if stage, err := loadQuickstart(root); stage != nil || err != nil {
		return stage, err
	}
	return loadLegacyPrerequisites(root)
}

func loadQuickstart(root string) (*Stage, error) {
	path := filepath.Join(root, "config", "quickstart.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var parsed quickstartFile
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("config/quickstart.yaml did not parse: %w", err)
	}
	if parsed.Stage.ID == "" {
		return nil, fmt.Errorf("config/quickstart.yaml has no stage block")
	}
	if len(parsed.Steps) == 0 {
		return nil, fmt.Errorf("config/quickstart.yaml declares a stage but no steps")
	}
	// Git and the pinned toolchain are the only dependencies of the source
	// build, so keep those three first even if the YAML is edited later.
	var ordered []stageStep
	for _, id := range []string{"git", "mise", "opencenter"} {
		for _, step := range parsed.Steps {
			if step.ID == id {
				ordered = append(ordered, step)
			}
		}
	}
	for _, step := range parsed.Steps {
		if step.ID != "git" && step.ID != "mise" && step.ID != "opencenter" {
			ordered = append(ordered, step)
		}
	}
	parsed.Steps = ordered

	stage := newStage(parsed.Stage)
	for index, step := range parsed.Steps {
		if strings.TrimSpace(step.Check) == "" {
			return nil, fmt.Errorf("quickstart step %q has no check", step.ID)
		}
		risk := step.Risk
		if risk == "" {
			risk = riskOf(step.Setup)
		}
		stage.Commands = append(stage.Commands, Command{
			ID: "prereq-" + step.ID,
			// Numbered, because these are a sequence rather than a set: mise
			// before the build that uses it, a key before the repository that
			// needs it.
			Task:     fmt.Sprintf("%d — %s", index+1, step.Name),
			Short:    step.Detail,
			Ready:    strings.TrimRight(step.Check, "\n"),
			Plain:    step.Why,
			Metaphor: step.Metaphor,
			Needs:    step.Needs,
			Mutating: false,
			Install:  strings.TrimRight(step.Setup, "\n"),
			Risk:     risk,
			Shell:    true,
			Inputs:   step.Inputs,
		})
	}
	return stage, nil
}

func newStage(block stageBlock) *Stage {
	return &Stage{
		ID:       block.ID,
		Name:     block.Name,
		Before:   block.Before,
		Colour:   block.Colour,
		OnColour: block.OnColour,
		Summary:  block.Summary,
		// Not experimental. The seven lifecycle stages are read out of the
		// binary and this one is not, but it is not a matter of opinion
		// either: without these the binary does not run at all.
		Experimental: false,
	}
}

// --- the older, exhaustive file ------------------------------------------------

type prerequisiteFile struct {
	Stage  stageBlock          `yaml:"stage"`
	Groups []prerequisiteGroup `yaml:"groups"`
}

type prerequisiteGroup struct {
	ID    string             `yaml:"id"`
	Name  string             `yaml:"name"`
	Order int                `yaml:"order"`
	Items []prerequisiteItem `yaml:"items"`
}

type prerequisiteItem struct {
	ID        string   `yaml:"id"`
	Name      string   `yaml:"name"`
	Why       string   `yaml:"why"`
	Metaphor  string   `yaml:"metaphor"`
	NeededFor []string `yaml:"needed_for"`
	Check     string   `yaml:"check"`
	Install   string   `yaml:"install"`
	Risk      string   `yaml:"risk"`
}

// loadLegacyPrerequisites is the fallback for a checkout with no
// quickstart.yaml.
func loadLegacyPrerequisites(root string) (*Stage, error) {
	path := filepath.Join(root, "config", "prerequisites.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var parsed prerequisiteFile
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("config/prerequisites.yaml did not parse: %w", err)
	}
	// Not an error: the file is still perfectly good input for preflight, it
	// just is not describing a stage.
	if parsed.Stage.ID == "" || len(parsed.Groups) == 0 {
		return nil, nil
	}

	groups := make([]prerequisiteGroup, len(parsed.Groups))
	copy(groups, parsed.Groups)
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Order < groups[j].Order })

	stage := newStage(parsed.Stage)
	for _, group := range groups {
		for _, item := range group.Items {
			if item.Check == "" {
				continue
			}
			risk := item.Risk
			if risk == "" {
				risk = riskOf(item.Install)
			}
			stage.Commands = append(stage.Commands, Command{
				ID:       "prereq-" + item.ID,
				Task:     group.Name,
				Short:    item.Name,
				Ready:    item.Check,
				Plain:    item.Why,
				Metaphor: item.Metaphor,
				Needs:    item.NeededFor,
				Mutating: false,
				Install:  item.Install,
				Risk:     risk,
				Shell:    true,
			})
		}
	}
	if len(stage.Commands) == 0 {
		return nil, fmt.Errorf(
			"config/prerequisites.yaml has %d group(s) but no item with a check", len(groups))
	}
	return stage, nil
}
