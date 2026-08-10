package main

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Two more columns: what a command does in ordinary words, and the same thing
// as a job on a building site.
//
// The --help line is not an explanation. "Initialize a new cluster
// configuration with default values" already assumes you know what a cluster
// configuration is, which is the thing you were trying to find out. The plain
// column says it without the vocabulary.
//
// The metaphor column is one picture held end to end — buy the plot, draw the
// plans, the inspector reads them, print the working drawings, build, live
// there, demolish. A metaphor that only works for three commands is a
// decoration; one that holds for all eighty tells you where you are in the
// job without reading anything else.
//
// This lives in config/command-language.yaml rather than in the generator, so
// the wording can be improved without regenerating the catalogue, and so a
// command with nothing written for it is visibly blank rather than silently
// given its stage's text.

type phrasing struct {
	Plain    string `yaml:"plain" json:"plain"`
	Metaphor string `yaml:"metaphor" json:"metaphor"`
}

type language struct {
	Stages   map[string]phrasing `yaml:"stages" json:"stages"`
	Commands map[string]phrasing `yaml:"commands" json:"commands"`
}

// loadLanguage reads the file. A missing or broken file is not fatal: the
// console's job is running commands, and it should still do that with two
// empty columns rather than refuse to start.
func loadLanguage(root string) *language {
	out := &language{Stages: map[string]phrasing{}, Commands: map[string]phrasing{}}

	raw, err := os.ReadFile(filepath.Join(root, "config", "command-language.yaml"))
	if err != nil {
		return out
	}
	var parsed language
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return out
	}
	if parsed.Stages != nil {
		out.Stages = parsed.Stages
	}
	if parsed.Commands != nil {
		out.Commands = parsed.Commands
	}
	return out
}

// forCommand returns the wording for one command. It deliberately does not
// fall back to the stage's wording: "Drawing the plans" repeated down twenty
// rows tells you nothing about any of them, and looks like it does.
func (l *language) forCommand(id string) phrasing {
	if l == nil {
		return phrasing{}
	}
	return l.Commands[id]
}

func (l *language) forStage(stage string) phrasing {
	if l == nil {
		return phrasing{}
	}
	return l.Stages[stage]
}

// missing lists commands in the catalogue with nothing written for them, so a
// command added to the CLI tomorrow shows up as work to do rather than as two
// quietly empty cells.
func (l *language) missing(catalogue *Catalogue) []string {
	var out []string
	seen := map[string]bool{}
	for _, environment := range catalogue.Environments {
		for _, command := range environment.Commands {
			if seen[command.ID] {
				continue
			}
			seen[command.ID] = true
			if l.Commands[command.ID].Plain == "" {
				out = append(out, command.ID)
			}
		}
	}
	return out
}
