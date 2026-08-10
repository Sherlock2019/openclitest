package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A real Kubernetes cluster to test against, created on demand.
//
// Everything in the Kind environment up to "generate" works against files.
// Past that — deploy, the day-two commands, teardown — needs a cluster that
// exists. Asking a person to build one by hand before they can press Run on
// half the table is asking them not to run it.
//
// The shape is what was measured rather than what openCenter defaults to
// (docs/kind-node-count.md): one node, because three fail on this machine at
// any image version and one builds in about 32 seconds; and never port 6443,
// because openCenter hardcodes it and collides with any cluster already
// holding it.
//
// The work is done by scripts/kind-cluster.sh, which is the same thing a
// person would run in a terminal. This is a button on top of it, not a
// second implementation that can drift from it.

const kindClusterName = "opencli-testbench"

// kindScript returns the path to the helper beside this binary's source.
func (c *console) kindScript() string {
	return filepath.Join(c.root, "scripts", "kind-cluster.sh")
}

type kindState struct {
	Available  bool   `json:"available"`  // kind and docker are both usable
	Running    bool   `json:"running"`    // our cluster exists
	Cluster    string `json:"cluster"`    // its name
	Kubeconfig string `json:"kubeconfig"` // where its kubeconfig is
	Nodes      int    `json:"nodes"`      // how many nodes it has
	Detail     string `json:"detail"`     // what to show when it is not running
	Others     int    `json:"others"`     // other clusters, which are not ours
}

// kindStatus asks the helper, so the page and the terminal agree.
func (c *console) kindStatus(ctx context.Context) kindState {
	state := kindState{Cluster: kindClusterName}

	output, code := c.runKind(ctx, "status", 20*time.Second)
	state.Available = !strings.Contains(output, "kind is not installed") &&
		!strings.Contains(output, "docker is not installed")
	state.Running = code == 0 && strings.Contains(output, "running:")

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "kubeconfig:"):
			state.Kubeconfig = strings.TrimSpace(strings.TrimPrefix(line, "kubeconfig:"))
		case strings.HasPrefix(line, "nodes:"):
			count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "nodes:")))
			if err == nil {
				state.Nodes = count
			}
		}
	}
	if !state.Running {
		state.Detail = strings.TrimSpace(output)
	}
	return state
}

// runKind executes one subcommand of the helper and returns its combined
// output. The process gets its own group so a cancelled request kills kind
// and everything kind started, not just the shell wrapping them.
func (c *console) runKind(ctx context.Context, action string, limit time.Duration) (string, int) {
	script := c.kindScript()
	if _, err := os.Stat(script); err != nil {
		return "the helper scripts/kind-cluster.sh is missing", 1
	}

	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	command := exec.Command("bash", script, action)
	command.Dir = c.root
	command.Env = kindEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	output, err := command.CombinedOutput()
	code := 0
	if err != nil {
		code = 1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
	}
	return string(output), code
}

// kindEnvironment is an allowlist, like the sandbox's: kind needs a real PATH
// and a real HOME to reach docker and write its kubeconfig, but nothing else
// from the shell this server was started in has any business being here.
func kindEnvironment() []string {
	keep := []string{"HOME", "PATH", "USER", "LANG", "DOCKER_HOST", "XDG_CACHE_HOME"}
	var out []string
	for _, name := range keep {
		if value := os.Getenv(name); value != "" {
			out = append(out, name+"="+value)
		}
	}
	// kind lives in ~/.local/bin here and that is not on PATH in a
	// non-login shell, which is how the server is usually started.
	home := os.Getenv("HOME")
	for index, entry := range out {
		if strings.HasPrefix(entry, "PATH=") && home != "" {
			out[index] = "PATH=" + home + "/.local/bin:" + strings.TrimPrefix(entry, "PATH=")
		}
	}
	return out
}

// handleKind serves the cluster's state, and creates or removes it.
//
//	GET  /api/kind          the state
//	POST /api/kind {up}     create it, streaming progress
//	POST /api/kind {down}   remove it, streaming progress
func (c *console) handleKind(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, c.kindStatus(r.Context()))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}

	var request struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if request.Action != "up" && request.Action != "down" {
		http.Error(w, `action must be "up" or "down"`, http.StatusBadRequest)
		return
	}

	// One at a time, and not while a command is running: both would be
	// competing for the same docker daemon and the same cluster.
	c.mu.Lock()
	if c.running {
		busy := c.busy()
		c.mu.Unlock()
		http.Error(w, busy, http.StatusConflict)
		return
	}
	release := c.hold("kind " + request.Action)
	c.mu.Unlock()
	defer release()

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	write := func(line string) {
		_, _ = io.WriteString(w, c.redactor.String(line)+"\n")
		if flusher != nil {
			flusher.Flush()
		}
	}

	write("$ scripts/kind-cluster.sh " + request.Action)
	write("")

	// Creating a node image on a cold cache can take minutes; removing one
	// is quick. Neither should be able to hang the server for ever.
	limit := 8 * time.Minute
	if request.Action == "down" {
		limit = 2 * time.Minute
	}

	start := time.Now()
	output, code := c.runKind(r.Context(), request.Action, limit)
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		write(line)
	}

	write("")
	write(fmt.Sprintf("[exit %d · %dms]", code, time.Since(start).Milliseconds()))

	// A failure here has the same shape as any other, so it gets the same
	// diagnosis rather than a bare exit code.
	if code != 0 {
		if finding := diagnose("", output, code, false); finding != nil {
			write("")
			for _, line := range diagnosisLines(finding) {
				write(line)
			}
		}
	}
}
