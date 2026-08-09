package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/report"
	"github.com/opencenter-cloud/opencli-testbench/internal/workflow"
)

// The console's front screen is one button, so the API behind it is one POST
// and one stream. Everything else here exists to answer "what would happen if
// I pressed it?" before a person does.

type fullRequest struct {
	// Live and Agreed together unlock Module 29. The mutation gate is checked
	// separately, in the engine, against the environment the bench itself was
	// started with.
	Live              bool              `json:"live"`
	LiveProviders     []string          `json:"live_providers"`
	Agreed            []string          `json:"agreed"`
	Quick             bool              `json:"quick"`
	KeepWorkspace     bool              `json:"keep_workspace"`
	ContinueOnFailure bool              `json:"continue_on_failure"`
	Credentials       map[string]string `json:"credentials"`
	Source            string            `json:"source"`
}

func (c *console) handleWorkflowPlan(w http.ResponseWriter, r *http.Request) {
	plan, err := workflow.LoadPlan(c.spec, c.root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	live := r.URL.Query().Get("live") == "1"

	writeJSON(w, http.StatusOK, map[string]any{
		"phases":            plan.Phases,
		"modules":           plan.Modules,
		"safety_agreement":  plan.Safety,
		"estimated_seconds": plan.EstimatedSeconds(live),
		"live_modules":      countLiveModules(plan),
		"binary":            c.binary,
		"binary_version":    c.binaryVersion(),
		"mutate_gate":       workflowGate,
		"mutate_allowed":    c.mutateAllowed(),
	})
}

func countLiveModules(plan *workflow.Plan) int {
	count := 0
	for _, module := range plan.Modules {
		if module.Live {
			count++
		}
	}
	return count
}

func (c *console) handleWorkflowStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var request fullRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	c.mu.Lock()
	if c.workflowRunning {
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
	c.mu.Unlock()

	plan, err := workflow.LoadPlan(c.spec, c.root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	credentials := credentialsFromAllMethods(c.spec)
	for key, value := range c.storedCredentials() {
		if strings.TrimSpace(value) != "" {
			credentials[key] = value
		}
	}
	for key, value := range request.Credentials {
		if value == "__saved__" || strings.TrimSpace(value) == "" {
			continue
		}
		credentials[key] = value
	}

	options := workflow.Options{
		Root:                  c.root,
		Binary:                c.binary,
		Source:                request.Source,
		Live:                  request.Live,
		LiveProviders:         request.LiveProviders,
		Agreed:                request.Agreed,
		Credentials:           credentials,
		StopOnBlockingFailure: !request.ContinueOnFailure,
		SkipSlow:              request.Quick,
		KeepWorkspace:         request.KeepWorkspace,
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.workflowRunning = true
	c.workflowCancel = cancel
	c.workflowEvents = nil
	c.mu.Unlock()

	engine := workflow.New(c.spec, plan, options, c.broadcastWorkflow)

	go func() {
		defer cancel()
		run, err := engine.Execute(ctx)
		if err != nil {
			c.broadcastWorkflow(workflow.Event{Type: "run-error", Message: err.Error(), At: time.Now()})
		}
		if run != nil {
			written, writeErr := report.WriteAll(run, engine.Redactor(),
				filepath.Join(run.Root, "reports"))
			if writeErr == nil {
				c.broadcastWorkflow(workflow.Event{Type: "reports", At: time.Now(),
					Message: strings.Join(written, "\n")})
			}
			c.mu.Lock()
			c.lastWorkflow = run
			c.mu.Unlock()
			c.broadcastWorkflow(workflow.Event{Type: "run-done", Run: run, At: time.Now()})
		}
		c.mu.Lock()
		c.workflowRunning = false
		c.mu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (c *console) handleWorkflowCancel(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	if c.workflowCancel != nil && c.workflowRunning {
		c.workflowCancel()
	}
	c.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

func (c *console) handleWorkflowRuns(w http.ResponseWriter, _ *http.Request) {
	runs, err := workflow.FindRuns(filepath.Join(c.root, "artifacts"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// handleWorkflowReport serves a finished run's HTML report straight into the
// browser, so the page a person shares is the same one the file holds.
func (c *console) handleWorkflowReport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/workflow/report/")
	if id == "" || strings.Contains(id, "..") || strings.Contains(id, "/") {
		http.Error(w, "bad run id", http.StatusBadRequest)
		return
	}
	run, err := workflow.LoadRun(filepath.Join(c.root, "artifacts", "runs", id, "run.json"))
	if err != nil {
		http.Error(w, "no such run", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(report.HTML(run)))
}

// handleExport serves one report format as a download. CSV is the one people
// actually ask for: it opens in a spreadsheet and diffs between runs.
//
//	/api/export/<run-id>/results.csv
func (c *console) handleExport(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/export/")
	id, name, ok := strings.Cut(rest, "/")
	if !ok || id == "" || strings.Contains(id, "..") {
		http.Error(w, "usage: /api/export/<run-id>/<file>", http.StatusBadRequest)
		return
	}

	run, err := workflow.LoadRun(filepath.Join(c.root, "artifacts", "runs", id, "run.json"))
	if err != nil {
		http.Error(w, "no such run", http.StatusNotFound)
		return
	}

	var body, contentType string
	switch name {
	case "results.csv":
		body, contentType = report.CSV(run), "text/csv; charset=utf-8"
	case "report.md":
		body, contentType = report.Markdown(run), "text/markdown; charset=utf-8"
	case "report.html":
		body, contentType = report.HTML(run), "text/html; charset=utf-8"
	default:
		http.Error(w, "supported: results.csv, report.md, report.html", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\"opencenter-"+id+"-"+name+"\"")
	_, _ = w.Write([]byte(c.redactor.String(body)))
}

// handleWorkflowEvents streams the run. It replays what has happened so far,
// so a browser opened halfway through catches up rather than showing a blank
// screen against a run that is plainly going.
func (c *console) handleWorkflowEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	stream := make(chan workflow.Event, 512)
	c.mu.Lock()
	c.workflowListeners[stream] = struct{}{}
	backlog := append([]workflow.Event(nil), c.workflowEvents...)
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.workflowListeners, stream)
		c.mu.Unlock()
	}()

	for _, event := range backlog {
		writeWorkflowEvent(w, event)
	}
	flusher.Flush()

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-stream:
			writeWorkflowEvent(w, event)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": keep-alive\n\n"))
			flusher.Flush()
		}
	}
}

func writeWorkflowEvent(w http.ResponseWriter, event workflow.Event) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
}

func (c *console) broadcastWorkflow(event workflow.Event) {
	c.mu.Lock()
	c.workflowEvents = append(c.workflowEvents, event)
	if len(c.workflowEvents) > 6000 {
		c.workflowEvents = c.workflowEvents[len(c.workflowEvents)-3000:]
	}
	listeners := make([]chan workflow.Event, 0, len(c.workflowListeners))
	for listener := range c.workflowListeners {
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
