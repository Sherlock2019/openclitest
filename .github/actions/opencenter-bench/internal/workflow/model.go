// Package workflow runs the thirty modules end to end, in order, once.
//
// The bench started as a set of independent checks you could run in any
// combination. That is still there, under the advanced runner. This package is
// the other thing a person wants: press one button, watch it go from checking
// the machine through to deleting everything it made, and get a report.
//
// Two rules shape the whole package. Modules run in the order the checklist
// declares and never in another — a module is not moved because it would be
// quicker somewhere else. And nothing that was not executed is ever reported
// as passed: blocked, skipped and not-run are distinct states all the way
// through to the JUnit file.
package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/opencenter-cloud/opencli-testbench/internal/checks"
	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
)

// ModuleState is where one module has got to.
type ModuleState string

const (
	StateNotStarted            ModuleState = "not_started"
	StateCheckingPrerequisites ModuleState = "checking_prerequisites"
	StateBlocked               ModuleState = "blocked"
	StateReady                 ModuleState = "ready"
	StateRunning               ModuleState = "running"
	StatePassed                ModuleState = "passed"
	StateWarning               ModuleState = "warning"
	StateFailed                ModuleState = "failed"
	StateSkipped               ModuleState = "skipped"
	StateCleaning              ModuleState = "cleaning"
	StateCancelled             ModuleState = "cancelled"
	StateLocked                ModuleState = "locked"
)

// Executed reports whether a state means the module actually ran. Anything
// else must never be counted as a pass, in any report.
func (s ModuleState) Executed() bool {
	return s == StatePassed || s == StateWarning || s == StateFailed
}

// RunState is where the workflow as a whole has got to.
type RunState string

const (
	RunNotStarted            RunState = "not_started"
	RunPreparing             RunState = "preparing"
	RunWaitingForUser        RunState = "waiting_for_user"
	RunRunning               RunState = "running"
	RunPaused                RunState = "paused"
	RunFailed                RunState = "failed"
	RunCancelling            RunState = "cancelling"
	RunCleaning              RunState = "cleaning"
	RunCompleted             RunState = "completed"
	RunCompletedWithWarnings RunState = "completed_with_warnings"
)

// Phase groups modules for the eye. It never changes their order.
type Phase struct {
	ID      string   `yaml:"id" json:"id"`
	Order   int      `yaml:"order" json:"order"`
	Name    string   `yaml:"name" json:"name"`
	Summary string   `yaml:"summary" json:"summary"`
	Modules []string `yaml:"modules" json:"modules"`
}

// moduleConfig is the workflow half of a module's definition; the question it
// answers comes from the checklist.
type moduleConfig struct {
	Blocking bool     `yaml:"blocking"`
	Live     bool     `yaml:"live"`
	Requires []string `yaml:"requires"`
	Estimate int      `yaml:"estimate"`
	Note     string   `yaml:"note"`
}

// Confirmation is one box a person has to tick before live modules unlock.
type Confirmation struct {
	ID   string `yaml:"id" json:"id"`
	Text string `yaml:"text" json:"text"`
}

// SafetyAgreement is what stands between a click and a real deployment.
type SafetyAgreement struct {
	Title         string         `yaml:"title" json:"title"`
	Preamble      string         `yaml:"preamble" json:"preamble"`
	Confirmations []Confirmation `yaml:"confirmations" json:"confirmations"`
}

// Module is one of the thirty, fully resolved.
type Module struct {
	ID         string `json:"id"`
	Order      int    `json:"order"`
	Name       string `json:"name"`
	Question   string `json:"question"`
	LookingFor string `json:"looking_for"`
	Example    string `json:"example"`
	Phase      string `json:"phase"`
	PhaseName  string `json:"phase_name"`

	Blocking bool     `json:"blocking"`
	Live     bool     `json:"live"`
	Requires []string `json:"requires"`
	Estimate int      `json:"estimate_seconds"`
	Note     string   `json:"note,omitempty"`

	// CheckIDs is what this module will execute, resolved from the check
	// registry by category. Kept as ids so the plan is serialisable.
	CheckIDs []string `json:"check_ids"`
}

// Plan is the whole workflow, ready to run.
type Plan struct {
	Phases  []Phase         `json:"phases"`
	Modules []Module        `json:"modules"`
	Safety  SafetyAgreement `json:"safety_agreement"`
}

// EstimatedSeconds is the sum of the module estimates that will actually run.
func (p *Plan) EstimatedSeconds(includeLive bool) int {
	total := 0
	for _, module := range p.Modules {
		if module.Live && !includeLive {
			continue
		}
		total += module.Estimate
	}
	return total
}

// Module returns one module by id.
func (p *Plan) Module(id string) (Module, bool) {
	for _, module := range p.Modules {
		if module.ID == id {
			return module, true
		}
	}
	return Module{}, false
}

// LoadPlan builds the workflow from config/checklist.yaml and
// config/workflow.yaml, and resolves each module's checks from the registry.
//
// The order comes from the checklist and nowhere else. workflow.yaml may say
// which phase a module is displayed under, but a phase that tried to reorder
// modules would be ignored — and a module missing from every phase is an
// error rather than something quietly dropped from the run.
func LoadPlan(loaded *spec.Spec, root string) (*Plan, error) {
	raw, err := os.ReadFile(filepath.Join(root, "config", "workflow.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read workflow.yaml: %w", err)
	}
	var file struct {
		Phases  []Phase                 `yaml:"phases"`
		Modules map[string]moduleConfig `yaml:"modules"`
		Safety  SafetyAgreement         `yaml:"safety_agreement"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse workflow.yaml: %w", err)
	}

	sort.SliceStable(file.Phases, func(i, j int) bool { return file.Phases[i].Order < file.Phases[j].Order })

	phaseOf := map[string]Phase{}
	for _, phase := range file.Phases {
		for _, moduleID := range phase.Modules {
			if existing, clash := phaseOf[moduleID]; clash {
				return nil, fmt.Errorf("workflow.yaml: module %q is in both %q and %q",
					moduleID, existing.ID, phase.ID)
			}
			phaseOf[moduleID] = phase
		}
	}

	byCategory := map[string][]string{}
	for _, check := range checks.All() {
		byCategory[check.Category] = append(byCategory[check.Category], check.ID)
	}

	plan := &Plan{Phases: file.Phases, Safety: file.Safety}
	for index, category := range loaded.Categories {
		phase, ok := phaseOf[category.ID]
		if !ok {
			return nil, fmt.Errorf("workflow.yaml: module %q is in no phase, so it would never run", category.ID)
		}
		settings := file.Modules[category.ID]

		plan.Modules = append(plan.Modules, Module{
			ID:         category.ID,
			Order:      index + 1,
			Name:       category.Name,
			Question:   category.Question,
			LookingFor: category.LookingFor,
			Example:    category.Example,
			Phase:      phase.ID,
			PhaseName:  phase.Name,
			Blocking:   settings.Blocking,
			Live:       settings.Live,
			Requires:   settings.Requires,
			Estimate:   settings.Estimate,
			Note:       settings.Note,
			CheckIDs:   byCategory[category.ID],
		})
	}

	if len(plan.Modules) == 0 {
		return nil, fmt.Errorf("no modules: checklist.yaml is empty")
	}
	return plan, nil
}

// ModuleResult is what one module produced.
type ModuleResult struct {
	ID       string          `json:"id"`
	Order    int             `json:"order"`
	Name     string          `json:"name"`
	Phase    string          `json:"phase"`
	State    ModuleState     `json:"state"`
	Message  string          `json:"message,omitempty"`
	Results  []checks.Result `json:"results"`
	Started  time.Time       `json:"started,omitempty"`
	Finished time.Time       `json:"finished,omitempty"`
	Millis   int64           `json:"millis"`
	Passed   int             `json:"passed"`
	Failed   int             `json:"failed"`
	Skipped  int             `json:"skipped"`
	Errored  int             `json:"errored"`
	Blocking bool            `json:"blocking"`
	Live     bool            `json:"live"`
	// Missing lists the prerequisite ids that were absent, when blocked.
	Missing []string `json:"missing_prerequisites,omitempty"`
}

// Checks is the total number of checks the module executed.
func (m *ModuleResult) Checks() int { return m.Passed + m.Failed + m.Skipped + m.Errored }

func (m *ModuleResult) summarise() {
	m.Passed, m.Failed, m.Skipped, m.Errored = 0, 0, 0, 0
	for _, result := range m.Results {
		switch result.Status {
		case checks.StatusPass:
			m.Passed++
		case checks.StatusFail:
			m.Failed++
		case checks.StatusSkip:
			m.Skipped++
		default:
			m.Errored++
		}
	}
	switch {
	case m.Failed > 0 || m.Errored > 0:
		m.State = StateFailed
	case m.Passed == 0 && m.Skipped > 0:
		// Everything skipped is not a pass. It is a module that did not get
		// to answer its question, and the report has to say so.
		m.State = StateWarning
		m.Message = "every check skipped; the question was not answered"
	case m.Passed == 0:
		m.State = StateWarning
		m.Message = "no checks ran"
	case m.Skipped > 0:
		m.State = StateWarning
		m.Message = fmt.Sprintf("%d of %d checks skipped", m.Skipped, m.Checks())
	default:
		m.State = StatePassed
	}
}
