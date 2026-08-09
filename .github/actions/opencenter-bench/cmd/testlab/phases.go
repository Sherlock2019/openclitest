package main

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Five phases over the eleven stages.
//
// Two audiences, one page: a manager wants to know whether the pipeline is green
// and where it stopped, an engineer wants the failing command and its stderr.
// Eleven rows is noise for the first and five is a hidden answer for the second,
// so the rail groups and expands rather than choosing.
//
// Nothing is removed. Every stage still runs, still counts and still reports —
// this only decides how the rail is drawn.

// Phase is one of the five, with the stages it contains.
type Phase struct {
	ID     string   `yaml:"id"     json:"id"`
	Name   string   `yaml:"name"   json:"name"`
	Detail string   `yaml:"detail" json:"detail"`
	Stages []string `yaml:"stages" json:"stages"`
	// Owner is who does the work: "bench" for a phase this runs, "flux" for one
	// it only draws. A phase nobody here performs must not report a status as
	// though it had.
	Owner string `yaml:"owner" json:"owner"`

	// Filled in per request from the outcomes, so the rail can show 12/19
	// without the page counting it.
	Total    int `yaml:"-" json:"total"`
	Executed int `yaml:"-" json:"executed"`
	Passed   int `yaml:"-" json:"passed"`
	Failed   int `yaml:"-" json:"failed"`
}

type phaseFile struct {
	Phases []Phase `yaml:"phases"`
}

// loadPhases reads config/phases.yaml.
//
// A missing file returns nil, and the page falls back to the flat eleven-stage
// rail. That is a worse view, not a broken one — which is the right failure for
// something that only changes a grouping.
func loadPhases(root string) []Phase {
	raw, err := os.ReadFile(filepath.Join(root, "config", "phases.yaml"))
	if err != nil {
		return nil
	}
	var parsed phaseFile
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return parsed.Phases
}

// phasesWithCounts fills in the per-phase totals for one environment.
//
// Counted from the same outcomes the stage bands use, so the two cannot
// disagree: a phase reading 7/7 above a stage reading 6/7 would make the page
// its own contradiction.
func (c *console) phasesWithCounts(environmentID string) []Phase {
	if len(c.phases) == 0 {
		return nil
	}

	// Which stage belongs to which phase, and which stages exist at all.
	inPhase := map[string]int{}
	for index, phase := range c.phases {
		for _, stage := range phase.Stages {
			inPhase[stage] = index
		}
	}

	out := make([]Phase, len(c.phases))
	copy(out, c.phases)

	environment := c.environment(environmentID)
	if environment == nil {
		return out
	}

	c.mu.Lock()
	outcomes := make(map[string]Outcome, len(c.outcomes))
	for key, outcome := range c.outcomes {
		outcomes[key] = outcome
	}
	c.mu.Unlock()

	for _, command := range environment.Commands {
		index, known := inPhase[command.Stage]
		if !known {
			// A stage no phase claims. Counted nowhere rather than silently
			// folded into the last phase, and reported at startup instead.
			continue
		}
		out[index].Total++
		outcome, ran := outcomes[environmentID+"|"+command.ID]
		if !ran {
			continue
		}
		out[index].Executed++
		if outcome.ExitCode == 0 && !outcome.TimedOut {
			out[index].Passed++
		} else {
			out[index].Failed++
		}
	}
	return out
}

// unclaimedStages lists stages no phase contains.
//
// Reported at startup rather than discovered later. A stage missing from
// phases.yaml would vanish from a grouped rail while still being in the table,
// and the page would quietly stop offering a way to reach it.
func unclaimedStages(phases []Phase, order []string) []string {
	if len(phases) == 0 {
		return nil
	}
	claimed := map[string]bool{}
	for _, phase := range phases {
		for _, stage := range phase.Stages {
			claimed[stage] = true
		}
	}
	var missing []string
	for _, stage := range order {
		if !claimed[stage] {
			missing = append(missing, stage)
		}
	}
	return missing
}
