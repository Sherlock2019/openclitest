// Package spec loads the YAML files under config/ that describe what the bench
// tests, where it can test it, and what has to be present first.
//
// The console and the terminal runner read the same files, so what the browser
// shows is what actually executes. Nothing here runs anything; it only
// describes.
package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Category is one row of the test checklist: a question the bench answers.
type Category struct {
	ID           string   `yaml:"id" json:"id"`
	Order        int      `yaml:"order" json:"order"`
	Name         string   `yaml:"name" json:"name"`
	Question     string   `yaml:"question" json:"question"`
	LookingFor   string   `yaml:"looking_for" json:"looking_for"`
	Example      string   `yaml:"example" json:"example"`
	Environments []string `yaml:"environments" json:"environments"`
}

// Environment is one place the CLI can be put under test.
type Environment struct {
	ID               string   `yaml:"id" json:"id"`
	Order            int      `yaml:"order" json:"order"`
	Name             string   `yaml:"name" json:"name"`
	Short            string   `yaml:"short" json:"short"`
	Summary          string   `yaml:"summary" json:"summary"`
	Cost             string   `yaml:"cost" json:"cost"`
	CostWhenMutating string   `yaml:"cost_when_mutating" json:"cost_when_mutating"`
	Mutating         bool     `yaml:"mutating" json:"mutating"`
	Gate             string   `yaml:"gate" json:"gate"`
	Requires         []string `yaml:"requires" json:"requires"`
	DurationHint     string   `yaml:"duration_hint" json:"duration_hint"`
	Notes            string   `yaml:"notes" json:"notes"`
}

// Prerequisite is one thing that has to be present, with a read-only probe.
type Prerequisite struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Why         string   `yaml:"why" json:"why"`
	NeededFor   []string `yaml:"needed_for" json:"needed_for"`
	Check       string   `yaml:"check" json:"check"`
	Install     string   `yaml:"install" json:"install"`
	InstallSafe bool     `yaml:"install_safe" json:"install_safe"`

	// Group is filled in on load so a flat list still knows where it came from.
	Group     string `yaml:"-" json:"group"`
	GroupName string `yaml:"-" json:"group_name"`
}

// PrerequisiteGroup keeps related prerequisites together in the console.
type PrerequisiteGroup struct {
	ID      string         `yaml:"id" json:"id"`
	Name    string         `yaml:"name" json:"name"`
	Order   int            `yaml:"order" json:"order"`
	Summary string         `yaml:"summary" json:"summary"`
	Items   []Prerequisite `yaml:"items" json:"items"`
}

// CredentialField is one value a person can supply.
type CredentialField struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Env         string   `yaml:"env" json:"env"`
	Required    bool     `yaml:"required" json:"required"`
	Secret      bool     `yaml:"secret" json:"secret"`
	Default     string   `yaml:"default" json:"default"`
	Placeholder string   `yaml:"placeholder" json:"placeholder"`
	Help        string   `yaml:"help" json:"help"`
	Options     []string `yaml:"options" json:"options"`
}

// CredentialMethod is one way of authenticating.
type CredentialMethod struct {
	ID   string `yaml:"id" json:"id"`
	Name string `yaml:"name" json:"name"`
	// For names the spec's own environments (sim, flex, kind, local), which
	// the workflow engine uses.
	For []string `yaml:"for" json:"for"`
	// Envs names the console's environments (openstack, vmware, baremetal,
	// kind), which are a different set: the console has one tab per
	// infrastructure type, the spec has one per way of running. Without this
	// the panel showed all twenty fields whatever tab was selected, so
	// picking VMware offered OpenStack's credentials and none of vSphere's.
	Envs        []string          `yaml:"envs" json:"envs"`
	Order       int               `yaml:"order" json:"order"`
	Recommended bool              `yaml:"recommended" json:"recommended"`
	Summary     string            `yaml:"summary" json:"summary"`
	Fields      []CredentialField `yaml:"fields" json:"fields"`
}

// Goal is one property the whole bench exists to defend.
type Goal struct {
	ID      string `yaml:"id" json:"id"`
	Name    string `yaml:"name" json:"name"`
	Meaning string `yaml:"meaning" json:"meaning"`
	Detail  string `yaml:"detail" json:"detail"`
}

// Step is one part of the method, for the "How we test" panel.
type Step struct {
	Name   string `yaml:"name" json:"name"`
	Detail string `yaml:"detail" json:"detail"`
}

// About is the what, why and how the console leads with.
type About struct {
	What struct {
		Title   string `yaml:"title" json:"title"`
		Summary string `yaml:"summary" json:"summary"`
	} `yaml:"what" json:"what"`
	Why struct {
		Title   string `yaml:"title" json:"title"`
		Summary string `yaml:"summary" json:"summary"`
		Goals   []Goal `yaml:"goals" json:"goals"`
	} `yaml:"why" json:"why"`
	How struct {
		Title   string `yaml:"title" json:"title"`
		Summary string `yaml:"summary" json:"summary"`
		Steps   []Step `yaml:"steps" json:"steps"`
	} `yaml:"how" json:"how"`
}

// Spec is everything config/ describes, loaded once.
type Spec struct {
	Root          string              `json:"root"`
	About         About               `json:"about"`
	Categories    []Category          `json:"categories"`
	Environments  []Environment       `json:"environments"`
	Prerequisites []PrerequisiteGroup `json:"prerequisites"`
	Credentials   []CredentialMethod  `json:"credentials"`
}

// Load reads every file under root/config.
func Load(root string) (*Spec, error) {
	s := &Spec{Root: root}
	dir := filepath.Join(root, "config")

	if err := readYAML(filepath.Join(dir, "about.yaml"), &s.About); err != nil {
		return nil, err
	}

	var checklist struct {
		Categories []Category `yaml:"categories"`
	}
	if err := readYAML(filepath.Join(dir, "checklist.yaml"), &checklist); err != nil {
		return nil, err
	}
	s.Categories = checklist.Categories
	sort.SliceStable(s.Categories, func(i, j int) bool {
		return s.Categories[i].Order < s.Categories[j].Order
	})

	var environments struct {
		Environments []Environment `yaml:"environments"`
	}
	if err := readYAML(filepath.Join(dir, "environments.yaml"), &environments); err != nil {
		return nil, err
	}
	s.Environments = environments.Environments
	sort.SliceStable(s.Environments, func(i, j int) bool {
		return s.Environments[i].Order < s.Environments[j].Order
	})

	var prerequisites struct {
		Groups []PrerequisiteGroup `yaml:"groups"`
	}
	if err := readYAML(filepath.Join(dir, "prerequisites.yaml"), &prerequisites); err != nil {
		return nil, err
	}
	s.Prerequisites = prerequisites.Groups
	sort.SliceStable(s.Prerequisites, func(i, j int) bool {
		return s.Prerequisites[i].Order < s.Prerequisites[j].Order
	})
	for gi := range s.Prerequisites {
		for ii := range s.Prerequisites[gi].Items {
			s.Prerequisites[gi].Items[ii].Group = s.Prerequisites[gi].ID
			s.Prerequisites[gi].Items[ii].GroupName = s.Prerequisites[gi].Name
		}
	}

	var credentials struct {
		Methods []CredentialMethod `yaml:"methods"`
	}
	if err := readYAML(filepath.Join(dir, "credentials.yaml"), &credentials); err != nil {
		return nil, err
	}
	s.Credentials = credentials.Methods
	sort.SliceStable(s.Credentials, func(i, j int) bool {
		return s.Credentials[i].Order < s.Credentials[j].Order
	})

	return s, s.validate()
}

// validate catches the mistakes that would otherwise show up as an empty panel
// in the console rather than as an error.
func (s *Spec) validate() error {
	categories := map[string]bool{}
	for _, c := range s.Categories {
		if categories[c.ID] {
			return fmt.Errorf("checklist.yaml: duplicate category %q", c.ID)
		}
		categories[c.ID] = true
	}
	environments := map[string]bool{}
	for _, e := range s.Environments {
		if environments[e.ID] {
			return fmt.Errorf("environments.yaml: duplicate environment %q", e.ID)
		}
		environments[e.ID] = true
	}
	for _, c := range s.Categories {
		for _, id := range c.Environments {
			if !environments[id] {
				return fmt.Errorf("checklist.yaml: category %q names unknown environment %q", c.ID, id)
			}
		}
	}
	prerequisites := s.PrerequisiteIndex()
	for _, e := range s.Environments {
		for _, id := range e.Requires {
			if _, ok := prerequisites[id]; !ok {
				return fmt.Errorf("environments.yaml: environment %q requires unknown prerequisite %q", e.ID, id)
			}
		}
	}
	return nil
}

// Category returns one row of the checklist.
func (s *Spec) Category(id string) (Category, bool) {
	for _, c := range s.Categories {
		if c.ID == id {
			return c, true
		}
	}
	return Category{}, false
}

// Environment returns one environment definition.
func (s *Spec) Environment(id string) (Environment, bool) {
	for _, e := range s.Environments {
		if e.ID == id {
			return e, true
		}
	}
	return Environment{}, false
}

// PrerequisiteIndex flattens the groups into a lookup by id.
func (s *Spec) PrerequisiteIndex() map[string]Prerequisite {
	index := map[string]Prerequisite{}
	for _, group := range s.Prerequisites {
		for _, item := range group.Items {
			index[item.ID] = item
		}
	}
	return index
}

// CredentialMethodsFor returns the authentication options that apply to an
// environment, most recommended first.
func (s *Spec) CredentialMethodsFor(environment string) []CredentialMethod {
	var out []CredentialMethod
	for _, method := range s.Credentials {
		for _, id := range method.For {
			if id == environment {
				out = append(out, method)
				break
			}
		}
	}
	return out
}

func readYAML(path string, into any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if err := yaml.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

// FindRoot walks up from start looking for the directory that holds config/
// and go.mod, so the bench works from any working directory.
func FindRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "config", "checklist.yaml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no config/checklist.yaml at or above %s", start)
		}
		dir = parent
	}
}
