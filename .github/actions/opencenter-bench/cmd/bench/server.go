package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/opencenter-cloud/opencli-testbench/internal/checks"
	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
	"github.com/opencenter-cloud/opencli-testbench/internal/preflight"
	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
	"github.com/opencenter-cloud/opencli-testbench/internal/runner"
	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
	"github.com/opencenter-cloud/opencli-testbench/internal/workflow"
)

//go:embed ui.html
var consoleHTML []byte

// console holds the state one browser session works against. There is one
// bench per process and one run at a time: this is a tool for a person
// watching a run, not a service.
type console struct {
	root   string
	spec   *spec.Spec
	binary string

	mu        sync.Mutex
	active    *liveRun
	lastRun   *runner.Report
	listeners map[chan runner.Event]struct{}

	// The continuous workflow has its own stream and its own state: it is a
	// different thing from the advanced runner, and sharing one channel would
	// mean each interrupting the other's display.
	workflowRunning   bool
	workflowCancel    context.CancelFunc
	workflowEvents    []workflow.Event
	workflowListeners map[chan workflow.Event]struct{}
	lastWorkflow      *workflow.Run

	// One catalogue command at a time, streamed straight to the browser.
	commandRunning bool
	redactor       *redact.Redactor
}

// workflowGate is the variable name shown in the console. It is the runner's
// gate, named here so the page and the engine cannot drift apart.
const workflowGate = runner.MutateGate

func (c *console) mutateAllowed() bool { return os.Getenv(runner.MutateGate) == "1" }

type liveRun struct {
	environment string
	started     time.Time
	cancel      context.CancelFunc
	events      []runner.Event
	done        bool
}

func commandUI(ctx context.Context, root string, loaded *spec.Spec, args []string) error {
	address := "127.0.0.1:7676"
	open := true
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-addr", "--addr":
			if index+1 >= len(args) {
				return fmt.Errorf("-addr needs an address")
			}
			address = args[index+1]
			index++
		case "-no-open", "--no-open":
			open = false
		default:
			return fmt.Errorf("unknown option %q", args[index])
		}
	}

	binary, err := cli.Locate(root)
	if err != nil {
		// The console is still useful without a binary: it can show the
		// prerequisites and tell you which one is missing.
		fmt.Fprintln(os.Stderr, "note:", err)
	}

	c := &console{
		root:              root,
		spec:              loaded,
		binary:            binary,
		listeners:         map[chan runner.Event]struct{}{},
		workflowListeners: map[chan workflow.Event]struct{}{},
		redactor:          redact.New(),
	}
	// Anything already exported that looks like a credential is registered
	// before the first byte can reach a browser.
	c.redactor.AddFromEnv(c.storedCredentials())
	c.redactor.AddFromEnv(credentialsFromAllMethods(loaded))

	mux := http.NewServeMux()
	mux.HandleFunc("/", c.handleIndex)
	mux.HandleFunc("/api/state", c.handleState)

	// The front screen: one button, one stream.
	mux.HandleFunc("/api/workflow/plan", c.handleWorkflowPlan)
	mux.HandleFunc("/api/workflow/start", c.handleWorkflowStart)
	mux.HandleFunc("/api/workflow/cancel", c.handleWorkflowCancel)
	mux.HandleFunc("/api/workflow/events", c.handleWorkflowEvents)
	mux.HandleFunc("/api/workflow/runs", c.handleWorkflowRuns)
	mux.HandleFunc("/api/workflow/report/", c.handleWorkflowReport)

	// Every command with a Run button, and the terminal output it produces.
	mux.HandleFunc("/api/commands", c.handleCommandCatalogue)
	mux.HandleFunc("/api/commands/run", c.handleCommandRun)
	mux.HandleFunc("/api/export/", c.handleExport)

	mux.HandleFunc("/api/preflight", c.handlePreflight)
	mux.HandleFunc("/api/plan", c.handlePlan)
	mux.HandleFunc("/api/run", c.handleRun)
	mux.HandleFunc("/api/cancel", c.handleCancel)
	mux.HandleFunc("/api/events", c.handleEvents)
	mux.HandleFunc("/api/report", c.handleReport)
	mux.HandleFunc("/api/credentials", c.handleCredentials)

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	url := "http://" + listener.Addr().String()
	fmt.Printf("\n  openCenter CLI test bench\n  %s\n\n", url)
	if binary != "" {
		fmt.Printf("  testing %s\n\n", binary)
	}
	if open {
		go openBrowser(url)
	}

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (c *console) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The console is loopback-only and holds no cookies, but it does render
	// values that came from a person's own configuration, so the page is kept
	// from loading anything remote.
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	_, _ = w.Write(consoleHTML)
}

func (c *console) handleState(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	last := c.lastRun
	running := c.active != nil && !c.active.done
	workflowRunning := c.workflowRunning
	lastWorkflow := c.lastWorkflow
	c.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"about":             c.spec.About,
		"categories":        c.spec.Categories,
		"environments":      c.spec.Environments,
		"credentials":       c.spec.Credentials,
		"prerequisites":     c.spec.Prerequisites,
		"checks":            describeChecks(),
		"coverage":          coverageMatrix(c.spec),
		"binary":            c.binary,
		"binary_version":    c.binaryVersion(),
		"mutate_gate":       runner.MutateGate,
		"mutate_allowed":    os.Getenv(runner.MutateGate) == "1",
		"host":              describeHost(),
		"running":           running,
		"last_report":       last,
		"saved_credentials": c.loadCredentials(),
		"workflow_running":  workflowRunning,
		"last_workflow":     lastWorkflow,
	})
}

func (c *console) binaryVersion() string {
	if c.binary == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, c.binary, "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
}

func (c *console) handlePreflight(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, preflight.Run(ctx, c.spec, scope))
}

func (c *console) handlePlan(w http.ResponseWriter, r *http.Request) {
	environment := r.URL.Query().Get("env")
	if environment == "" {
		environment = "local"
	}
	mutate := r.URL.Query().Get("mutate") == "1"
	planned, err := runner.Plan(c.spec, runner.Options{Environment: environment, Mutate: mutate})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	out := make([]checkDescription, 0, len(planned))
	for _, check := range planned {
		out = append(out, checkDescription{
			ID: check.ID, Name: check.Name, Category: check.Category,
			Environments: check.Environments, Mutating: check.Mutating, Slow: check.Slow,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type runRequest struct {
	Environment string            `json:"environment"`
	Only        []string          `json:"only"`
	Categories  []string          `json:"categories"`
	Mutate      bool              `json:"mutate"`
	SkipSlow    bool              `json:"skip_slow"`
	KeepSandbox bool              `json:"keep_sandbox"`
	Credentials map[string]string `json:"credentials"`
}

func (c *console) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var request runRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	c.mu.Lock()
	if c.active != nil && !c.active.done {
		c.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a run is already in progress"})
		return
	}
	if c.binary == "" {
		c.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "no opencenter binary found: set OPENCLI_BIN or put one in ./bin/opencenter"})
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	live := &liveRun{environment: request.Environment, started: time.Now(), cancel: cancel}
	c.active = live
	c.mu.Unlock()

	// Merge what the browser sent with what is already exported, so a person
	// who has OS_CLOUD set does not have to retype it.
	credentials := credentialsFromEnvironment(c.spec, request.Environment)
	stored := c.storedCredentials()
	for key, value := range stored {
		if strings.TrimSpace(value) != "" {
			credentials[key] = value
		}
	}
	for key, value := range request.Credentials {
		// The browser echoes a mask for a secret it was never given; that
		// means "use the saved one", not "use the literal mask".
		if value == "__saved__" {
			continue
		}
		if strings.TrimSpace(value) != "" {
			credentials[key] = value
		}
	}

	options := runner.Options{
		Root:        c.root,
		Binary:      c.binary,
		Environment: request.Environment,
		Only:        request.Only,
		Categories:  request.Categories,
		Mutate:      request.Mutate,
		SkipSlow:    request.SkipSlow,
		KeepSandbox: request.KeepSandbox,
		Credentials: credentials,
	}

	go func() {
		defer cancel()
		report, err := runner.Execute(ctx, c.spec, options, c.broadcast)
		if err != nil {
			c.broadcast(runner.Event{Type: "run-error", Message: err.Error(), At: time.Now()})
		}
		c.mu.Lock()
		live.done = true
		if report != nil {
			c.lastRun = report
		}
		c.mu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (c *console) handleCancel(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	if c.active != nil && !c.active.done {
		c.active.cancel()
	}
	c.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

func (c *console) handleReport(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	report := c.lastRun
	c.mu.Unlock()
	if report == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no run has finished yet"})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handleEvents streams a run as it happens. Server-sent events rather than a
// socket: the traffic only ever goes one way, and a reconnect is free.
func (c *console) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	stream := make(chan runner.Event, 256)
	c.mu.Lock()
	c.listeners[stream] = struct{}{}
	backlog := append([]runner.Event(nil), currentEvents(c.active)...)
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.listeners, stream)
		c.mu.Unlock()
	}()

	for _, event := range backlog {
		writeEvent(w, event)
	}
	flusher.Flush()

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-stream:
			writeEvent(w, event)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

func currentEvents(live *liveRun) []runner.Event {
	if live == nil {
		return nil
	}
	return live.events
}

func writeEvent(w http.ResponseWriter, event runner.Event) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", payload)
}

func (c *console) broadcast(event runner.Event) {
	c.mu.Lock()
	if c.active != nil {
		// Keep enough history that a browser opened mid-run catches up, but
		// not so much that a long run grows without bound.
		c.active.events = append(c.active.events, event)
		if len(c.active.events) > 4000 {
			c.active.events = c.active.events[len(c.active.events)-2000:]
		}
	}
	listeners := make([]chan runner.Event, 0, len(c.listeners))
	for listener := range c.listeners {
		listeners = append(listeners, listener)
	}
	c.mu.Unlock()

	for _, listener := range listeners {
		select {
		case listener <- event:
		default: // a browser that cannot keep up drops frames rather than stalling the run
		}
	}
}

// --- credentials ------------------------------------------------------------

// credentialsFile is written 0600 and gitignored. It exists so a person does
// not retype an auth URL every time; secrets are stored only because the
// alternative is a person pasting them into a shell history instead.
func (c *console) credentialsFile() string {
	return filepath.Join(c.root, "config", "credentials.local.yaml")
}

// storedCredentials reads the file as it is, secrets included. Only the run
// path uses it; the browser is served loadCredentials instead.
func (c *console) storedCredentials() map[string]string {
	raw, err := os.ReadFile(c.credentialsFile())
	if err != nil {
		return map[string]string{}
	}
	stored := map[string]string{}
	if err := yaml.Unmarshal(raw, &stored); err != nil {
		return map[string]string{}
	}
	return stored
}

func (c *console) loadCredentials() map[string]string {
	raw, err := os.ReadFile(c.credentialsFile())
	if err != nil {
		return map[string]string{}
	}
	stored := map[string]string{}
	if err := yaml.Unmarshal(raw, &stored); err != nil {
		return map[string]string{}
	}
	// Never hand a secret back to the browser. The console shows "saved"
	// instead, and the value is used from disk when a run starts.
	out := map[string]string{}
	for key, value := range stored {
		if isSecretKey(key) {
			if value != "" {
				out[key] = "__saved__"
			}
			continue
		}
		out[key] = value
	}
	return out
}

func (c *console) handleCredentials(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, c.loadCredentials())
	case http.MethodPost:
		var incoming map[string]string
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		existing := map[string]string{}
		if raw, err := os.ReadFile(c.credentialsFile()); err == nil {
			_ = yaml.Unmarshal(raw, &existing)
		}
		for key, value := range incoming {
			// A masked secret coming back from the browser means "leave it
			// alone", not "set it to the mask".
			if value == "__saved__" {
				continue
			}
			if strings.TrimSpace(value) == "" {
				delete(existing, key)
				continue
			}
			existing[key] = value
		}
		encoded, err := yaml.Marshal(existing)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if err := os.WriteFile(c.credentialsFile(), encoded, 0o600); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func isSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range []string{"PASSWORD", "SECRET", "TOKEN"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// --- shared -----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(body)
}

// coverageMatrix says how many checks each checklist row has in each
// environment, so the console can show a gap as a gap.
func coverageMatrix(loaded *spec.Spec) map[string]map[string]int {
	out := map[string]map[string]int{}
	for _, environment := range loaded.Environments {
		counts := checks.Categories(environment.ID)
		for _, category := range loaded.Categories {
			if out[category.ID] == nil {
				out[category.ID] = map[string]int{}
			}
			out[category.ID][environment.ID] = counts[category.ID]
		}
	}
	return out
}

func describeHost() string {
	description := runtime.GOOS + "/" + runtime.GOARCH
	if raw, err := os.ReadFile("/proc/version"); err == nil &&
		strings.Contains(strings.ToLower(string(raw)), "microsoft") {
		description = "WSL on Windows · " + description
	}
	if name, err := os.Hostname(); err == nil {
		description += " · " + name
	}
	return description
}

// openBrowser tries the openers that work on this host, in the order that
// works. Under WSL, xdg-open exists but does nothing — there is no desktop
// session — so Windows is handed the URL instead.
func openBrowser(url string) {
	// Wait until the server actually answers, or the browser opens on nothing.
	for attempt := 0; attempt < 100; attempt++ {
		if response, err := http.Get(url + "/api/state"); err == nil {
			_ = response.Body.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	var openers [][]string
	switch {
	case isWSL():
		openers = [][]string{
			{"wslview", url},
			{"powershell.exe", "-NoProfile", "-Command", "Start-Process '" + url + "'"},
			{"explorer.exe", url},
		}
	case runtime.GOOS == "darwin":
		openers = [][]string{{"open", url}}
	default:
		openers = [][]string{{"xdg-open", url}, {"sensible-browser", url}}
	}

	for _, opener := range openers {
		if _, err := exec.LookPath(opener[0]); err != nil {
			continue
		}
		if err := exec.Command(opener[0], opener[1:]...).Start(); err == nil {
			return
		}
	}
	fmt.Printf("  Could not open a browser. Go to %s\n", url)
}

func isWSL() bool {
	raw, err := os.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(raw)), "microsoft")
}
