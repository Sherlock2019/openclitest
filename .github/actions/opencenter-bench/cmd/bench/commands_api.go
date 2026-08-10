package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/workflow"
)

// The Commands view: every useful invocation with a Run button beside it and
// the real terminal output underneath.
//
// Each entry runs the bench's own executable with the arguments shown, so what
// appears on screen is exactly what the printed command would produce in a
// shell. There is no second code path to drift: copy the line, paste it into a
// terminal, get the same thing.

type runnableCommand struct {
	ID          string   `json:"id"`
	Group       string   `json:"group"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Args        []string `json:"args"`
	// Shell is the copy-pasteable form.
	Shell string `json:"shell"`
	// Mutating marks a command that can create infrastructure, so the console
	// can put it behind the gate rather than beside the read-only ones.
	Mutating bool `json:"mutating"`
	Minutes  int  `json:"minutes"`
}

func (c *console) commandCatalogue() []runnableCommand {
	var out []runnableCommand
	add := func(command runnableCommand) {
		command.Shell = "bench " + strings.Join(command.Args, " ")
		out = append(out, command)
	}

	add(runnableCommand{
		ID: "full", Group: "The whole thing", Label: "Full A-to-Z test",
		Description: "Preflight, all 30 modules in order, cleanup, then four reports.",
		Args:        []string{"run", "full"}, Minutes: 3,
	})
	add(runnableCommand{
		ID: "full-quick", Group: "The whole thing", Label: "Full A-to-Z test (quick)",
		Description: "The same, skipping the checks measured in minutes.",
		Args:        []string{"run", "full", "--quick"}, Minutes: 1,
	})

	add(runnableCommand{
		ID: "preflight", Group: "Before a run", Label: "Check prerequisites",
		Description: "Every probe is read-only and changes nothing.",
		Args:        []string{"preflight"},
	})
	add(runnableCommand{
		ID: "credentials", Group: "Before a run", Label: "Check credentials",
		Description: "Whether the configured cloud credentials work. No value is printed.",
		Args:        []string{"credentials", "check"},
	})
	add(runnableCommand{
		ID: "list", Group: "Before a run", Label: "Show the plan",
		Description: "Environments, checks and checklist coverage.",
		Args:        []string{"list"},
	})

	// One entry per module, so a single question can be chased on its own.
	plan, err := workflow.LoadPlan(c.spec, c.root)
	if err == nil {
		for _, module := range plan.Modules {
			add(runnableCommand{
				ID:          "module-" + module.ID,
				Group:       "One module at a time",
				Label:       fmt.Sprintf("%d. %s", module.Order, module.Name),
				Description: module.Question,
				Args:        []string{"run", "--env", moduleEnvironment(module), "--category", module.ID},
				Mutating:    module.Live,
			})
		}
	}

	for _, environment := range c.spec.Environments {
		add(runnableCommand{
			ID:          "env-" + environment.ID,
			Group:       "One environment at a time",
			Label:       environment.Name,
			Description: environment.Summary,
			Args:        []string{"run", "--env", environment.ID},
			Mutating:    environment.Mutating,
		})
	}

	add(runnableCommand{
		ID: "runs", Group: "Afterwards", Label: "List previous runs",
		Args: []string{"runs"},
	})
	return out
}

// moduleEnvironment picks the far end a module's checks need. Everything that
// is not a cloud question runs in the simulated environment too, because the
// simulator costs nothing and covers strictly more.
func moduleEnvironment(module workflow.Module) string {
	if module.Live {
		return "kind"
	}
	return "sim"
}

func (c *console) handleCommandCatalogue(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, c.commandCatalogue())
}

// handleCommandRun executes one catalogue entry and streams its output.
//
// The response is the terminal: plain text, flushed line by line, so the
// browser shows it arriving rather than after it has finished.
func (c *console) handleCommandRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var request struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var chosen *runnableCommand
	for _, candidate := range c.commandCatalogue() {
		if candidate.ID == request.ID {
			copied := candidate
			chosen = &copied
			break
		}
	}
	if chosen == nil {
		// Only catalogue entries run. The browser cannot name arbitrary
		// arguments, so this endpoint is not a shell.
		http.Error(w, "unknown command", http.StatusNotFound)
		return
	}

	if chosen.Mutating && !c.mutateAllowed() {
		http.Error(w, "this command can create real resources; restart the console with "+
			workflowGate+"=1", http.StatusForbidden)
		return
	}

	c.mu.Lock()
	if c.commandRunning {
		c.mu.Unlock()
		http.Error(w, "a command is already running", http.StatusConflict)
		return
	}
	c.commandRunning = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.commandRunning = false
		c.mu.Unlock()
	}()

	executable, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	writeLine := func(line string) {
		// Everything on its way to a browser goes through redaction, the same
		// as everything on its way to a file.
		_, _ = io.WriteString(w, c.redactor.String(line)+"\n")
		if flusher != nil {
			flusher.Flush()
		}
	}

	writeLine("$ " + chosen.Shell)
	writeLine("")

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Minute)
	defer cancel()

	command := exec.CommandContext(ctx, executable, chosen.Args...)
	command.Dir = c.root
	command.Env = append(os.Environ(), "NO_COLOR=1", "TERM=dumb")

	stdout, err := command.StdoutPipe()
	if err != nil {
		writeLine("could not start: " + err.Error())
		return
	}
	command.Stderr = command.Stdout

	if err := command.Start(); err != nil {
		writeLine("could not start: " + err.Error())
		return
	}

	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			writeLine(scanner.Text())
		}
	}()
	wait.Wait()

	exit := 0
	if err := command.Wait(); err != nil {
		var exitError *exec.ExitError
		if ok := asExit(err, &exitError); ok {
			exit = exitError.ExitCode()
		} else {
			exit = -1
			writeLine(err.Error())
		}
	}
	writeLine("")
	writeLine(fmt.Sprintf("[exit %d]", exit))
}

func asExit(err error, target **exec.ExitError) bool {
	converted, ok := err.(*exec.ExitError)
	if ok {
		*target = converted
	}
	return ok
}
