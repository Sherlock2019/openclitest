package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Environment modes.
//
// What emulated mode actually is, stated plainly because the rest of this file
// depends on not overstating it: the real CLI, run against a machine with no
// provider credentials and no provider endpoint, under a synthetic identity.
//
// It does not simulate OpenStack. There is no fake Keystone here and no
// intercepted API. What it does is remove every real credential from the
// environment the command runs in, so that anything depending only on local
// code — parsing, schema, defaults, templates, generation — runs exactly as it
// really would, and anything needing a provider fails honestly rather than
// being faked into passing.
//
// That distinction is the whole value. A simulator that returns a cheerful
// "deployment succeeded" teaches a team to trust a result that means nothing.

// Emulation is config/emulation.yaml.
type Emulation struct {
	Modes     []Mode     `yaml:"modes"     json:"modes"`
	Providers []Named    `yaml:"providers" json:"providers"`
	Scenarios []Scenario `yaml:"scenarios" json:"scenarios"`
	Warning   Warning    `yaml:"warning"   json:"warning"`
}

type Mode struct {
	ID       string `yaml:"id"       json:"id"`
	Name     string `yaml:"name"     json:"name"`
	Detail   string `yaml:"detail"   json:"detail"`
	Badge    string `yaml:"badge"    json:"badge"`
	Emulated bool   `yaml:"emulated" json:"emulated"`
}

type Named struct {
	ID   string `yaml:"id"   json:"id"`
	Name string `yaml:"name" json:"name"`
}

type Scenario struct {
	ID     string            `yaml:"id"     json:"id"`
	Name   string            `yaml:"name"   json:"name"`
	Detail string            `yaml:"detail" json:"detail"`
	Env    map[string]string `yaml:"env"    json:"env"`
}

type Warning struct {
	Title           string       `yaml:"title"           json:"title"`
	Lead            string       `yaml:"lead"            json:"lead"`
	Facts           []Fact       `yaml:"facts"           json:"facts"`
	Acknowledgement string       `yaml:"acknowledgement" json:"acknowledgement"`
	Reliable        []string     `yaml:"reliable"        json:"reliable"`
	NotProven       []NotProven  `yaml:"not_proven"      json:"not_proven"`
	Confidence      []Confidence `yaml:"confidence"      json:"confidence"`
}

type Fact struct {
	Label string `yaml:"label" json:"label"`
	Value string `yaml:"value" json:"value"`
}

type NotProven struct {
	Label string `yaml:"label" json:"label"`
	Why   string `yaml:"why"   json:"why"`
}

type Confidence struct {
	Area  string `yaml:"area"  json:"area"`
	Level string `yaml:"level" json:"level"`
}

// EmulationState is what the console is currently set to.
type EmulationState struct {
	mu sync.RWMutex

	ModeID       string `json:"mode"`
	ProviderID   string `json:"provider"`
	ScenarioID   string `json:"scenario"`
	Acknowledged bool   `json:"acknowledged"`
}

// loadEmulation reads the modes. A missing file means the selector is not
// offered at all, rather than a half-built one that cannot be trusted.
func loadEmulation(root string) *Emulation {
	raw, err := os.ReadFile(filepath.Join(root, "config", "emulation.yaml"))
	if err != nil {
		return nil
	}
	var loaded Emulation
	if err := yaml.Unmarshal(raw, &loaded); err != nil {
		return nil
	}
	if len(loaded.Modes) == 0 {
		return nil
	}
	return &loaded
}

// Mode returns the selected mode, defaulting to the first declared one.
func (e *Emulation) Mode(id string) Mode {
	for _, mode := range e.Modes {
		if mode.ID == id {
			return mode
		}
	}
	return e.Modes[0]
}

// Scenario returns the selected scenario, or an empty one.
func (e *Emulation) Scenario(id string) Scenario {
	for _, scenario := range e.Scenarios {
		if scenario.ID == id {
			return scenario
		}
	}
	return Scenario{}
}

// Current reads the selection.
func (s *EmulationState) Current() (string, string, string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ModeID, s.ProviderID, s.ScenarioID, s.Acknowledged
}

// Set records a selection.
//
// Changing the mode clears the acknowledgement. Somebody who agreed to the
// limitations of an emulated run has not thereby agreed to anything about the
// next mode, and carrying the tick across would let a real run inherit consent
// that was given for a simulated one.
func (s *EmulationState) Set(mode, provider, scenario string, acknowledged bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mode != s.ModeID {
		s.Acknowledged = false
	}
	s.ModeID, s.ProviderID, s.ScenarioID = mode, provider, scenario
	if acknowledged {
		s.Acknowledged = true
	}
}

// emulationEnv is the environment an emulated command runs with.
//
// Every real credential is blanked rather than merely left unset, because the
// sandbox inherits some of the host environment and "not set by us" is not the
// same as "not present". A run that quietly picked up OS_CLOUD from the user's
// shell would contact a real cloud while the page said it had not.
func (c *console) emulationEnv() []string {
	if c.emulation == nil {
		return nil
	}
	modeID, providerID, scenarioID, _ := c.emulationState.Current()
	mode := c.emulation.Mode(modeID)
	if !mode.Emulated {
		return nil
	}

	out := []string{
		"OPENCENTER_ENV_MODE=" + mode.ID,
		"OPENCLI_ENV_MODE=" + mode.ID,
		"OPENCLI_EMULATED_PROVIDER=" + providerID,
		"OPENCLI_EMULATION_SCENARIO=" + scenarioID,
		// A sentinel rather than a plausible-looking secret. If this ever
		// appears in a log or a report, something is printing what it should
		// not, and a value that says so is easier to find than one that looks
		// like a real token.
		"OPENCLI_EMULATED_SECRET=EMULATED_SECRET_DO_NOT_LOG",
	}

	// Blank every real credential. Named explicitly rather than by pattern:
	// a pattern that misses one is a pattern that lets a real cloud be
	// contacted from a run labelled emulated.
	for _, name := range []string{
		"OS_CLOUD", "OS_AUTH_URL", "OS_USERNAME", "OS_PASSWORD", "OS_TOKEN",
		"OS_PROJECT_ID", "OS_PROJECT_NAME", "OS_APPLICATION_CREDENTIAL_ID",
		"OS_APPLICATION_CREDENTIAL_SECRET", "OS_CLIENT_CONFIG_FILE",
		"VSPHERE_SERVER", "VSPHERE_USER", "VSPHERE_PASSWORD",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"OPENCLI_GIT_TOKEN", "SOPS_AGE_KEY_FILE",
	} {
		out = append(out, name+"=")
	}

	// The scenario's own values go last, so a scenario can set something the
	// blanking above cleared.
	for name, value := range c.emulation.Scenario(scenarioID).Env {
		out = append(out, name+"="+value)
	}
	return out
}

// emulationBlocked reports whether a command may not run in the current mode,
// and why.
//
// Refusing is the honest answer. A deploy in emulated mode has nothing to
// deploy to, and letting it run so it can fail confusingly is worse than
// saying up front that this mode does not cover it.
func (c *console) emulationBlocked(command Command) (bool, string) {
	if c.emulation == nil {
		return false, ""
	}
	modeID, _, _, _ := c.emulationState.Current()
	mode := c.emulation.Mode(modeID)
	if !mode.Emulated {
		return false, ""
	}
	if command.Mutating {
		return true, "Not available in " + mode.Name +
			": this command would change real infrastructure, and there is none here."
	}
	if mode.ID == "config-only" {
		for _, verb := range []string{"deploy", "destroy", "status", "drift", "backup"} {
			if strings.Contains(command.ID, verb) {
				return true, "Not available in configuration-only mode: " +
					"this command needs a running cluster."
			}
		}
	}
	return false, ""
}

// handleEmulation reads and sets the mode.
func (c *console) handleEmulation(w http.ResponseWriter, r *http.Request) {
	if c.emulation == nil {
		http.Error(w, "emulation is not configured", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodPost {
		var request struct {
			Mode         string `json:"mode"`
			Provider     string `json:"provider"`
			Scenario     string `json:"scenario"`
			Acknowledged bool   `json:"acknowledged"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c.emulationState.Set(request.Mode, request.Provider, request.Scenario, request.Acknowledged)
		// The sandboxes carry the old environment, so they are torn down
		// rather than reused. A command running with the previous mode's
		// credentials under the new mode's label is the one thing this must
		// never do.
		c.cleanup()
	}

	modeID, providerID, scenarioID, acknowledged := c.emulationState.Current()
	writeJSON(w, http.StatusOK, map[string]any{
		"config": c.emulation,
		"state": map[string]any{
			"mode": modeID, "provider": providerID,
			"scenario": scenarioID, "acknowledged": acknowledged,
			"emulated": c.emulation.Mode(modeID).Emulated,
			"badge":    c.emulation.Mode(modeID).Badge,
		},
	})
}
