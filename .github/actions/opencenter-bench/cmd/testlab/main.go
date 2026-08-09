// Command testlab is the openCenter CLI test bench console.
//
//	./start.sh
//
// Every command the CLI has, per environment, per stage, per task, each one
// ready to run with a button beside it and the real output underneath.
//
// The command list is generated from the binary into config/commands.json, so
// it is what that build actually offers rather than a list kept by hand.
package main

import (
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
	"github.com/opencenter-cloud/opencli-testbench/internal/experimental"
	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
	"github.com/opencenter-cloud/opencli-testbench/internal/sandbox"
	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
)

//go:embed ui.html
var page []byte

// mutateGate has to be set in the environment the console was started from
// before a command that could create real infrastructure will run.
const mutateGate = "OPENCLI_ALLOW_MUTATE"

// Catalogue is config/commands.json: every command, per environment.
type Catalogue struct {
	Binary        string        `json:"binary"`
	Version       string        `json:"version"`
	Org           string        `json:"org"`
	StageOrder    []string      `json:"stage_order"`
	TotalCommands int           `json:"total_commands"`
	Environments  []Environment `json:"environments"`

	// StageNotes is the long "how it works" text, by stage id.
	//
	// Stage-level rather than a field on Command, because it describes the
	// stage and repeating it on every row would be the same paragraph four
	// times. Only stages that declare one appear here, so the map is usually
	// almost empty and a UI can treat its absence as "nothing to explain".
	StageNotes map[string]string `json:"stage_notes,omitempty"`
}

// Environment is one infrastructure type with its own ready-to-run lines.
type Environment struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Cluster  string    `json:"cluster"`
	Detail   string    `json:"detail"`
	Fixture  string    `json:"fixture"`
	Commands []Command `json:"commands"`
}

// Command is one ready-to-run invocation.
type Command struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Task     string   `json:"task"`
	Stage    string   `json:"stage"`
	Short    string   `json:"short"`
	Usage    string   `json:"usage"`
	Needs    []string `json:"needs"`
	Mutating bool     `json:"mutating"`
	IsGroup  bool     `json:"is_group"`
	Ready    string   `json:"ready"`

	// Filled in at load time from config/command-language.yaml, not from the
	// generated catalogue: the wording is edited far more often than the
	// command list changes.
	Plain    string `json:"plain,omitempty"`
	Metaphor string `json:"metaphor,omitempty"`

	// Experimental marks a command that came from
	// config/experimental-stages.yaml rather than from the binary's command
	// tree. The page says which, so nobody mistakes the bench's opinion for
	// the CLI's own structure.
	Experimental bool `json:"experimental,omitempty"`

	// Install is the command that provides a missing prerequisite. It is
	// rendered beside the check and has no Run button — see the note on
	// experimental.Command.Install for why.
	Install string `json:"install,omitempty"`

	// Shell marks a row whose Ready is a shell line rather than opencenter
	// arguments, so the page does not print "opencenter command -v git" and
	// the server does not try to run it as one.
	Shell bool `json:"shell,omitempty"`

	// Risk says what this row's install command would touch — safe, host or
	// root — and decides the wording on its button. Without it every button
	// read the same, which is the wrong amount of hesitation for "install
	// mise" and for "apt install as root" to share.
	Risk string `json:"risk,omitempty"`

	// Inputs are the form fields shown above this row's commands. Their values
	// are passed as environment variables, never substituted into the command.
	Inputs []experimental.Input `json:"inputs,omitempty"`
}

// Outcome is what happened when a command was run.
type Outcome struct {
	Command  string    `json:"command"`
	Env      string    `json:"env"`
	Args     string    `json:"args"`
	Stdout   string    `json:"stdout"`
	Stderr   string    `json:"stderr"`
	ExitCode int       `json:"exit_code"`
	Millis   int64     `json:"millis"`
	TimedOut bool      `json:"timed_out"`
	At       time.Time `json:"at"`
	// Diagnosis is nil when the command succeeded.
	Diagnosis *Diagnosis `json:"diagnosis,omitempty"`
}

type console struct {
	root         string
	binary       string
	sourceRepo   string
	sourceBranch string
	catalogue    *Catalogue
	spec         *spec.Spec
	language     *language
	longRunning  *LongRunning
	// Five phases over the eleven stages, so the rail can be read by somebody
	// who wants a verdict and expanded by somebody who wants a failing command.
	phases []Phase
	// Environment mode: real, kind, emulated or configuration-only.
	emulation      *Emulation
	emulationState EmulationState
	overview       *Overview
	redactor       *redact.Redactor

	mu       sync.Mutex
	boxes    map[string]*sandbox.Sandbox // one sandbox per environment
	outcomes map[string]Outcome          // env + command -> latest
	running  bool
	// What is running, and since when.
	//
	// Held so the refusal can name it. "a command is already running" is true
	// and useless: the operator pressed a button, nothing happened, and the page
	// gave them no way to find out which of a dozen buttons was the one still
	// going — or how long it had been. Several of these clone a repository over
	// the network and take seconds, so the collision is ordinary rather than
	// exceptional, and it looked like the button was broken.
	runningID    string
	runningSince time.Time
}

// remoteOnly reports whether a command does its work on GitHub rather than on
// this machine.
//
// The single lock exists because everything local shares one docker daemon, one
// sandbox and one cluster, so two of them at once corrupt each other. None of
// that is true of the GitHub buttons: they push a commit or read the API, touch
// no sandbox and no container.
//
// Holding them behind the same lock meant a `kind up` — which this bench now
// starts by itself when a command needs a cluster, and which takes minutes —
// refused to let anybody trigger a CI run:
//
//	kind up is already running (44s so far). Wait for it, or reload the page.
//
// Pressing "Run GitHub Action test" then did nothing at all, with a message
// about a local cluster, and no run appeared on GitHub. The two have nothing to
// do with each other.
func remoteOnly(id string) bool {
	switch id {
	case "actions-install", "actions-trigger", "actions-results",
		"actions-preview", "actions-workflow", "actions-config":
		return true
	}
	return strings.HasPrefix(id, "actions-")
}

// busy reports what is running, for a refusal that names it.
func (c *console) busy() string {
	id, since := c.runningID, c.runningSince
	if id == "" {
		id = "a command"
	}
	if since.IsZero() {
		return id + " is already running"
	}
	return fmt.Sprintf("%s is already running (%s so far). Wait for it, or "+
		"reload the page once it finishes.", id, time.Since(since).Round(time.Second))
}

// hold marks a command running and returns the release.
//
// One helper rather than the same six lines in three places, so a later handler
// cannot take the flag and forget to name what it took it for.
func (c *console) hold(id string) func() {
	c.running = true
	c.runningID = id
	c.runningSince = time.Now()
	return func() {
		c.mu.Lock()
		c.running = false
		c.runningID = ""
		c.runningSince = time.Time{}
		c.mu.Unlock()
	}
}

func main() {
	address := flag.String("addr", "127.0.0.1:7700", "address to listen on")
	noOpen := flag.Bool("no-open", false, "do not open a browser")
	flag.Parse()

	root, err := spec.FindRoot(".")
	if err != nil {
		if executable, execErr := os.Executable(); execErr == nil {
			root, err = spec.FindRoot(filepath.Dir(executable))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot find the bench root:", err)
			os.Exit(1)
		}
	}

	catalogue, err := loadCatalogue(filepath.Join(root, "config", "commands.json"))
	if err == nil {
		if extra := addExperimental(catalogue, root); len(extra) > 0 {
			for _, stage := range extra {
				// Says where it landed and on which side. Reporting `after ""`
				// for a stage that asked to go before init told the reader
				// nothing and looked like a missing value.
				position := "at the end"
				if stage.Before != "" {
					position = "before " + stage.Before
				} else if stage.After != "" {
					position = "after " + stage.After
				}
				label := "stage"
				if stage.Experimental {
					label = "experimental stage"
				}
				fmt.Printf("  %s %q added %s (%d commands)\n",
					label, stage.Name, position, len(stage.Commands))
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read config/commands.json:", err)
		fmt.Fprintln(os.Stderr, "generate it first:")
		fmt.Fprintln(os.Stderr, "  python3 scripts/generate-commands-json.py > config/commands.json")
		os.Exit(1)
	}

	binary := os.Getenv("OPENCLI_BIN")
	if binary == "" {
		binary = catalogue.Binary
	}
	if located, err := cli.Locate(root); err == nil && os.Getenv("OPENCLI_BIN") == "" {
		binary = located
	}
	// commands.json describes the command tree, but the selected binary is the
	// authority for build identity. Without this refresh an OPENCLI_BIN override
	// can produce a report for commit A while actually executing commit B.
	catalogue.Binary = binary
	if version := probeBinaryVersion(binary); version != "" {
		catalogue.Version = version
	}

	loaded, _ := spec.Load(root)

	// Attach the plain-language and metaphor columns to every command, once,
	// rather than looking them up on each request.
	words := loadLanguage(root)
	for ei := range catalogue.Environments {
		for ci := range catalogue.Environments[ei].Commands {
			command := &catalogue.Environments[ei].Commands[ci]
			said := words.forCommand(command.ID)
			// Only when the language file has something. An experimental
			// command carries its own wording inline, and this loop used to
			// overwrite it with an empty string — the descriptions were
			// written, loaded, and then thrown away one step later.
			if said.Plain != "" {
				command.Plain = said.Plain
			}
			if said.Metaphor != "" {
				command.Metaphor = said.Metaphor
			}
		}
	}

	c := &console{
		root: root, binary: binary, catalogue: catalogue, spec: loaded,
		language:    words,
		overview:    loadOverview(root),
		longRunning: loadLongRunning(root),
		phases:      loadPhases(root),
		emulation:   loadEmulation(root),
		redactor:    redact.New(),
		boxes:       map[string]*sandbox.Sandbox{},
		outcomes:    map[string]Outcome{},
	}
	c.redactor.AddFromEnv(c.savedCredentials())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/", c.handlePage)
	mux.HandleFunc("/api/catalogue", c.handleCatalogue)
	mux.HandleFunc("/api/source-branches", c.handleSourceBranches)
	mux.HandleFunc("/api/source-clone", c.handleSourceClone)
	mux.HandleFunc("/assets/city", c.handleAsset)
	mux.HandleFunc("/api/city-image", c.handleAssetUpload)
	mux.HandleFunc("/api/run", c.handleRun)
	mux.HandleFunc("/api/fixture", c.handleFixture)
	mux.HandleFunc("/api/reset", c.handleReset)
	mux.HandleFunc("/api/credentials", c.handleCredentials)
	mux.HandleFunc("/api/results.csv", c.handleCSV)
	mux.HandleFunc("/api/results", c.handleResults)
	// What GitHub found, beside what this machine found. See actionsboard.go.
	mux.HandleFunc("/api/actions-board", c.handleActionsBoard)
	mux.HandleFunc("/api/baseline", c.handleBaseline)
	mux.HandleFunc("/api/emulation", c.handleEmulation)
	mux.HandleFunc("/api/kind", c.handleKind)
	// The cluster lifecycle: its own rail, its own state, its own binary. See
	// e2elifecycle.go for why it is beside the command table rather than folded
	// into it.
	newLifecycle(root).register(mux)

	listener, err := net.Listen("tcp", *address)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		c.cleanup()
	}()

	url := "http://" + listener.Addr().String()
	fmt.Printf("\n  openCenter CLI test bench\n  %s\n\n", url)
	fmt.Printf("  %d commands · %d environments · %s\n", catalogue.TotalCommands,
		len(catalogue.Environments), catalogue.Version)
	fmt.Printf("  testing %s\n", binary)
	if os.Getenv(mutateGate) == "1" {
		fmt.Printf("  %s=1 — commands may create real resources\n", mutateGate)
	}
	// A command added to the CLI tomorrow gets two blank columns. Say so here
	// rather than letting them be quietly empty on the page. Asked of the
	// commands as loaded, so one carrying its wording inline does not get
	// reported as missing it.
	var unwritten []string
	seenCommand := map[string]bool{}
	for _, environment := range catalogue.Environments {
		for _, command := range environment.Commands {
			if seenCommand[command.ID] || command.Plain != "" {
				continue
			}
			seenCommand[command.ID] = true
			unwritten = append(unwritten, command.ID)
		}
	}
	if len(unwritten) > 0 {
		fmt.Printf("  %d command(s) have no plain-language description yet: %s\n",
			len(unwritten), strings.Join(unwritten, ", "))
	}
	fmt.Println()

	if !*noOpen {
		go openBrowser(url)
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadCatalogue(path string) (*Catalogue, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	catalogue := &Catalogue{}
	if err := json.Unmarshal(raw, catalogue); err != nil {
		return nil, err
	}
	if len(catalogue.Environments) == 0 {
		return nil, fmt.Errorf("no environments in the catalogue")
	}
	return catalogue, nil
}

// addExperimental folds config/experimental-stages.yaml into the catalogue:
// extra bands in the table, in the position each one asks for.
//
// They are added rather than generated, so they carry Experimental and the
// page can say which parts came from the binary and which came from us.

// insertResultsStage puts a "results" band after teardown.
//
// It has no commands, which is the point: everything above it is work, and
// this is where the reader stops and looks at what the work produced. The
// rail shows it and clicking it goes to the triage panel; the command table
// skips it, because a band with nothing in it would be a heading over a gap.
//
// Before the teardown band, which the rail calls Reset and shows last.
//
// This has now been both ways round and the reason it settled here is that the
// band is two different things depending on which bench you mean, and only one
// of them is "teardown".
//
// To the lifecycle it is teardown: diagnostics, destroy, verify-cleanup, and
// they run before report, because the verdict depends on whether anything was
// left behind. That order is fixed in internal/e2e and this function does not
// touch it.
//
// To the command bench it is reset: cluster destroy, cluster unlock, settings
// reset — put the machine back the way you found it so the next run starts from
// the same place. That is something you do after reading the results, not
// before, and it is the last thing anybody does here.
//
// So the rail reads results-then-reset, and the band's own help says that a
// lifecycle run tears down before it reports. Stating it is better than a rail
// that quietly implies one order while the engine performs another.
//
// The evidence is not lost either way. Every run writes its report to disk
// before anything is destroyed, and the report is what the band opens.
func insertResultsStage(catalogue *Catalogue) {
	for _, stage := range catalogue.StageOrder {
		if stage == "results" {
			return
		}
	}
	out := make([]string, 0, len(catalogue.StageOrder)+1)
	placed := false
	for _, stage := range catalogue.StageOrder {
		if stage == "teardown" && !placed {
			out = append(out, "results")
			placed = true
		}
		out = append(out, stage)
	}
	if !placed {
		out = append(out, "results")
	}
	catalogue.StageOrder = out
}

func addExperimental(catalogue *Catalogue, root string) []experimental.Stage {
	stages := experimental.Load(root)

	// The prerequisites stage comes from config/prerequisites.yaml — the same
	// file the preflight panel reads — and asks to sit ahead of init rather
	// than after some stage. It is added here because the insertion machinery
	// is the same; it is not experimental, and carries its own flag saying so.
	prerequisites, err := experimental.LoadPrerequisites(root)
	if err != nil {
		// Loud, on stderr, and the console still starts. A broken
		// prerequisites file should not stop anyone testing the CLI, but it
		// must not vanish sixteen rows in silence either.
		fmt.Fprintln(os.Stderr, "  warning: prerequisites stage not loaded:", err)
	}
	if prerequisites != nil {
		stages = append([]experimental.Stage{*prerequisites}, stages...)
	}

	if len(stages) == 0 {
		return nil
	}
	// Results goes in first, before the added stages are placed.
	//
	// Ordering matters here and it caught me out: this used to run at the end of
	// the function, after InsertOrder. So when a stage declared `after: results`
	// there was no "results" in the order yet, the match failed, and the stage
	// fell to the end — which is how GitHub Actions Integration stayed at 11
	// while its config said 10.
	insertResultsStage(catalogue)

	catalogue.StageOrder = experimental.InsertOrder(catalogue.StageOrder, stages)

	for _, stage := range stages {
		if strings.TrimSpace(stage.HowItWorks) == "" {
			continue
		}
		if catalogue.StageNotes == nil {
			catalogue.StageNotes = map[string]string{}
		}
		catalogue.StageNotes[stage.ID] = stage.HowItWorks
	}

	for ei := range catalogue.Environments {
		environment := &catalogue.Environments[ei]
		for _, stage := range stages {
			for _, command := range stage.Commands {
				catalogue.Environments[ei].Commands = append(environment.Commands, Command{
					ID:    command.ID,
					Name:  command.ID,
					Task:  command.Task,
					Stage: stage.ID,
					Short: command.Short,
					Needs: command.Needs,
					// Nothing in an experimental stage may quietly mutate: it
					// says so in the file, and the gate still decides.
					Mutating: command.Mutating,
					Ready: experimental.Reference(
						command.Ready, catalogue.Org, environment.Cluster),
					Plain:    command.Plain,
					Metaphor: command.Metaphor,
					// The stage's own flag, not a constant. This used to be a
					// hardcoded true, which was right while Kafka was the only
					// added stage and would have labelled the prerequisites
					// rows experimental the moment a second one arrived.
					Experimental: stage.Experimental,
					// Shown beside the check, never wired to Run.
					Install: command.Install,
					Shell:   command.Shell,
					Risk:    command.Risk,
					Inputs:  command.Inputs,
				})
			}
		}
	}
	catalogue.TotalCommands = 0
	seen := map[string]bool{}
	for _, environment := range catalogue.Environments {
		for _, command := range environment.Commands {
			if !seen[command.ID] {
				seen[command.ID] = true
				catalogue.TotalCommands++
			}
		}
	}
	return stages
}

// --- handlers ---------------------------------------------------------------

func (c *console) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	_, _ = w.Write(page)
}

func (c *console) handleCatalogue(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	catalogue := c.catalogue
	binary := c.binary
	outcomes := make(map[string]Outcome, len(c.outcomes))
	for key, outcome := range c.outcomes {
		outcome.Stdout = c.redactor.String(outcome.Stdout)
		outcome.Stderr = c.redactor.String(outcome.Stderr)
		outcomes[key] = outcome
	}
	c.mu.Unlock()
	repository, branch := c.sourceOf()
	source := map[string]string{
		"version":    catalogue.Version,
		"repository": repository,
		"branch":     branch,
		"commit":     commitOf(catalogue.Version),
		"binary":     binary,
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"catalogue":      catalogue,
		"binary":         binary,
		"source":         source,
		"host":           hostDescription(),
		"mutate_gate":    mutateGate,
		"mutate_allowed": os.Getenv(mutateGate) == "1",
		"credentials":    credentialFields(c.spec),
		"saved":          c.maskedCredentials(),
		"outcomes":       outcomes,
		"what":           whatWeTest(),
		"stage_language": c.language.Stages,
		// Five phases over the eleven stages, counted from the same outcomes the
		// stage bands use so the two cannot disagree.
		"phases":   c.phasesWithCounts(c.catalogue.Environments[0].ID),
		"overview": c.overview,
	})
}

// handleFixture creates the cluster a section's commands act on.
func (c *console) handleFixture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Env string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	environment := c.environment(request.Env)
	if environment == nil {
		http.Error(w, "unknown environment", http.StatusNotFound)
		return
	}
	c.stream(w, r, request.Env, "fixture", environment.Fixture)

	// Each click starts a new CLI process. A session-scoped `cluster use` dies
	// with that process, so make the fixture persistently active inside its own
	// sandbox. Commands that only support active-cluster lookup can then use the
	// fixture the button just promised to create.
	c.mu.Lock()
	fixture, created := c.outcomes[request.Env+"|fixture"]
	c.mu.Unlock()
	if created && fixture.ExitCode == 0 {
		reference := c.catalogue.Org + "/" + environment.Cluster
		c.streamContinuation(w, r, request.Env, "fixture-active",
			"cluster use "+reference+" --persistent")
	}
}

func (c *console) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Env     string `json:"env"`
		Command string `json:"command"`
		Args    string `json:"args"`
		// Part selects which of a prerequisite row's two commands to run:
		// "check" (the default) or "install". A name, not a command — the
		// text still comes from the catalogue.
		Part string `json:"part"`
		// Inputs are the form values beside a prerequisite step, keyed by the
		// environment variable each one is passed as. Filtered against the
		// step's own declared fields before any of it reaches a process.
		Inputs map[string]string `json:"inputs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	command, known := c.lookupCommand(request.Env, request.Command)
	preview := dryRunRequested(request.Args)

	// The environment mode can refuse a command before the gate is even
	// consulted: an emulated run has nothing for a deploy to deploy to.
	// An explicit dry-run is allowed because it does not act on that far end.
	if known && !preview {
		if blocked, why := c.emulationBlocked(command); blocked {
			http.Error(w, why, http.StatusForbidden)
			return
		}
	}

	if !preview && ((known && command.Mutating) || isMutating(request.Args)) &&
		os.Getenv(mutateGate) != "1" {
		http.Error(w, "this command can change real infrastructure. Restart with "+
			mutateGate+"=1 to allow it.", http.StatusForbidden)
		return
	}

	if request.Part == "install" {
		command, known := c.lookupCommand(request.Env, request.Command)
		if !known || command.Install == "" {
			http.Error(w, "no install command for "+request.Command, http.StatusNotFound)
			return
		}
		// Every prerequisite runs, including the ones needing root.
		//
		// The first version refused those, reasoning that the console has no
		// terminal for a password prompt. That was wrong twice over: sudo is
		// frequently passwordless on a lab machine, in which case it simply
		// works; and where it is not, sudo fails in milliseconds with a
		// precise message rather than hanging. Refusing to try turned a
		// working button into no button on the guess that it might not work.
		// The output explains the sudo case when it comes up.
		c.streamShell(w, r, request.Env, request.Command, command.Install,
			inputEnv(command, request.Inputs, c.redactor))
		return
	}

	c.stream(w, r, request.Env, request.Command, request.Args,
		inputEnvFor(c, request.Env, request.Command, request.Inputs)...)
}

// stream runs one command and writes its output as it arrives.
func (c *console) stream(w http.ResponseWriter, r *http.Request, env, id, args string, extra ...string) {
	c.streamResponse(w, r, true, env, id, args, extra...)
}

// streamContinuation appends another command to a response whose headers have
// already been sent. Fixture creation uses it to select the cluster it made.
func (c *console) streamContinuation(w http.ResponseWriter, r *http.Request, env, id, args string, extra ...string) {
	c.streamResponse(w, r, false, env, id, args, extra...)
}

func (c *console) streamResponse(w http.ResponseWriter, r *http.Request, writeHeader bool, env, id, args string, extra ...string) {
	c.mu.Lock()
	if c.running && !remoteOnly(id) {
		busy := c.busy()
		c.mu.Unlock()
		http.Error(w, busy, http.StatusConflict)
		return
	}
	release := c.hold(id)
	c.mu.Unlock()
	defer release()

	box, err := c.sandboxFor(env)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	flusher, _ := w.(http.Flusher)
	if writeHeader {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
	}

	write := func(line string) {
		_, _ = io.WriteString(w, c.redactor.String(line)+"\n")
		if flusher != nil {
			flusher.Flush()
		}
	}

	// A prerequisite check is a shell line, so it cannot go through the CLI
	// runner. Its text is taken from the catalogue by id and the caller's
	// `args` is discarded entirely: a shell string supplied in a request body
	// and executed by the server is remote code execution, and no amount of
	// escaping makes that a good idea. Every one of these is read-only by the
	// contract stated at the top of config/prerequisites.yaml.
	var result cli.Result
	// Filled in for a command that never returns, and written out after the
	// exit line so it reads as an explanation rather than an error.
	var longRunningNote []string
	if command, known := c.lookupCommand(env, id); known && command.Shell {
		write("$ " + command.Ready)
		write("")

		// The real environment, not the sandbox's.
		//
		// A prerequisite check asks "does this machine have git?". Running it
		// inside the sandbox answers "does the sandbox have git?", which is a
		// different question with a different answer: the sandbox exists
		// precisely to have a stripped PATH and its own HOME.
		//
		// This was measured rather than reasoned about. The two consoles
		// disagreed on the same eight checks on the same machine — simple
		// found mise at ~/.local/bin, ultra found nothing — because their
		// sandboxes inherited different amounts of the environment. Both
		// answers were wrong; only one of them looked it.
		//
		// The commands the CLI runs still go through the sandbox. Only these
		// shell checks do not, and they are read-only by the contract at the
		// top of config/quickstart.yaml.
		// OPENCLI_BENCH_ROOT, so a step can name something that ships with the
		// repository. These run in the user's HOME, so a relative path finds
		// nothing — and an unset variable is worse than a missing one, because
		// "${OPENCLI_BENCH_ROOT}/bin/bench" then expands to "/bin/bench" and
		// fails with a path nobody wrote. The prerequisites path already set
		// it; this one did not, so every card that called a bench binary broke
		// the same way.
		benchRoot := []string{"OPENCLI_BENCH_ROOT=" + c.root}

		// The saved credentials, which these commands could not see.
		//
		// They were handed to the sandbox and only to the sandbox, and these
		// shell steps deliberately do not run in one — so "Save credentials"
		// wrote config/credentials.local.yaml and then nothing ever read it
		// back. Every card that needs a token or a key was blank on the next
		// visit however many times it had been saved.
		//
		// Before extra, so a value typed into a form still wins over a stored
		// one: what is on screen is what the operator means right now.
		var stored []string
		for key, value := range c.savedCredentials() {
			if strings.TrimSpace(value) != "" {
				stored = append(stored, key+"="+value)
			}
		}

		environment := append(os.Environ(), benchRoot...)
		environment = append(environment, stored...)
		environment = append(environment, c.emulationEnv()...)
		environment = append(environment, extra...)
		result = runShell(r.Context(), command.Ready, homeDir(), environment, c.redactor)
	} else {
		fields := splitArgs(args)
		write("$ opencenter " + strings.Join(fields, " "))
		write("")

		// A foreground scheduler gets a short leash rather than the full
		// timeout. See config/long-running.yaml: these never return, so
		// waiting twenty-five seconds to discover that tells nobody anything.
		timeout := 25 * time.Second
		scheduler, isScheduler := c.longRunning.Lookup(args)
		if isScheduler {
			timeout = c.longRunning.Grace()
			write("(this one does not return — the bench will stop it after " +
				timeout.String() + ")")
			write("")
		}

		runner := &cli.Runner{
			Binary: c.binary, Dir: box.Work,
			// The emulated environment goes last so its blanked credentials
			// win over anything the sandbox inherited.
			Env:     append(box.Env(), c.emulationEnv()...),
			Timeout: timeout, Redactor: c.redactor,
		}
		result = runner.Run(r.Context(), fields...)

		if isScheduler {
			longRunningNote = scheduler.Verdict(result.Stdout+result.Stderr, timeout)
			// A scheduler stopped on purpose did not time out in the sense
			// this page means by it, and a red row here would be a lie.
			result.TimedOut = false
			if strings.Contains(result.Stdout+result.Stderr, scheduler.Expect) ||
				scheduler.Expect == "" {
				result.ExitCode = 0
			}
		}
	}

	for _, line := range splitLines(result.Stdout) {
		write(line)
	}
	if result.Stderr != "" {
		if result.Stdout != "" {
			write("")
		}
		write("--- stderr ---")
		for _, line := range splitLines(result.Stderr) {
			write(line)
		}
	}
	if result.Stdout == "" && result.Stderr == "" {
		write("(no output)")
	}
	write("")
	if len(longRunningNote) > 0 {
		write(fmt.Sprintf("[stopped after %dms — see below]", result.Duration.Milliseconds()))
	} else if result.TimedOut {
		write("[did not return within 25s]")
	} else {
		write(fmt.Sprintf("[exit %d · %dms]", result.ExitCode, result.Duration.Milliseconds()))
	}

	// A scheduler that started and was stopped on purpose is not a failure, so
	// it gets an explanation instead of a diagnosis.
	if len(longRunningNote) > 0 {
		for _, line := range longRunningNote {
			write(line)
		}
	} else if finding := diagnose(result.Stdout, result.Stderr, result.ExitCode, result.TimedOut); finding != nil {
		// When it failed, say what went wrong, where, and what usually causes
		// it. A wall of output and a number is not a diagnosis.
		for _, line := range diagnosisLines(finding) {
			write(line)
		}
	}

	c.mu.Lock()
	c.outcomes[env+"|"+id] = Outcome{
		Command: id, Env: env, Args: args,
		Stdout: result.Stdout, Stderr: result.Stderr,
		ExitCode: result.ExitCode, Millis: result.Duration.Milliseconds(),
		TimedOut: result.TimedOut, At: time.Now(),
		Diagnosis: diagnose(result.Stdout, result.Stderr, result.ExitCode, result.TimedOut),
	}
	c.mu.Unlock()
}

func (c *console) handleReset(w http.ResponseWriter, _ *http.Request) {
	c.cleanup()
	c.mu.Lock()
	c.outcomes = map[string]Outcome{}
	c.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

func (c *console) handleCSV(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	outcomes := make([]Outcome, 0, len(c.outcomes))
	for _, outcome := range c.outcomes {
		outcomes = append(outcomes, outcome)
	}
	c.mu.Unlock()
	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].At.Before(outcomes[j].At) })

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="opencenter-commands-`+
		time.Now().Format("20060102-150405")+`.csv"`)

	// The triage classification, so the export carries the same judgement the
	// page shows. A CSV that omits it would leave whoever opens it doing the
	// classification again by eye, which is the work this bench exists to do.
	report := c.triage()
	triaged := map[string]Failure{}
	for _, failure := range report.Failures {
		triaged[failure.Environment+"|"+failure.Command] = failure
	}
	environmentName := map[string]string{}
	for _, environment := range c.catalogue.Environments {
		environmentName[environment.ID] = environment.Name
	}

	writer := csv.NewWriter(w)
	defer writer.Flush()

	// Every field the triage view shows, so the export is the whole report
	// rather than a summary of it: build identity on each row, because a
	// result is only meaningful against the binary that produced it and a
	// spreadsheet gets separated from its context immediately.
	_ = writer.Write([]string{
		"at", "cli_version", "cli_repository", "cli_ref", "cli_commit", "platform",
		"environment", "mode", "stage", "command", "invocation",
		"exit_code", "result", "millis",
		"triage_category", "cause_group", "cause", "suggested_action",
		"regression", "expected", "actual", "reproduce",
		"diagnosis_cause", "diagnosis_category", "location", "possible_causes",
		"stdout", "stderr",
	})

	for _, outcome := range outcomes {
		verdict := "ok"
		if outcome.TimedOut {
			verdict = "did not return"
		} else if outcome.ExitCode != 0 {
			verdict = "failed"
		}

		cause, category, location, possible := "", "", "", ""
		if outcome.Diagnosis != nil {
			cause = c.redactor.String(outcome.Diagnosis.Cause)
			category = outcome.Diagnosis.Category
			location = describeLocation(outcome.Diagnosis.Location)
			var reasons []string
			for index, entry := range outcome.Diagnosis.Possible {
				reasons = append(reasons, strconv.Itoa(index+1)+". "+entry.Why)
			}
			possible = strings.Join(reasons, " | ")
		}

		// A passing row has no triage entry, and that is correct — it carries
		// empty judgement columns rather than a fabricated verdict.
		failure := triaged[environmentName[outcome.Env]+"|"+outcome.Command]
		regression := ""
		if verdict != "ok" {
			regression = "no"
			if failure.Regression {
				regression = "yes"
			}
		}
		reproduce := failure.Reproduce
		if reproduce == "" {
			reproduce = c.binary + " " + outcome.Args
		}

		_ = writer.Write([]string{
			outcome.At.Format(time.RFC3339),
			report.Build.Version, report.Build.Repo, report.Build.Branch,
			report.Build.Commit, report.Build.Platform,
			outcome.Env, failure.Mode, failure.Stage, outcome.Command,
			"opencenter " + outcome.Args,
			strconv.Itoa(outcome.ExitCode), verdict,
			strconv.FormatInt(outcome.Millis, 10),
			string(failure.Category), string(failure.CauseGroup),
			c.redactor.String(failure.Cause), failure.Action,
			regression, failure.Expected, failure.Actual,
			c.redactor.String(reproduce),
			cause, category, location, possible,
			oneLine(c.redactor.String(outcome.Stdout)),
			oneLine(c.redactor.String(outcome.Stderr)),
		})
	}

	// A trailing summary block, so a reader who opens the file in a spreadsheet
	// sees the totals without having to derive them from 97 rows.
	_ = writer.Write(nil)
	_ = writer.Write([]string{"SUMMARY"})
	for _, line := range [][2]string{
		{"cli version", report.Build.Version},
		{"repository", report.Build.Repo},
		{"ref", report.Build.Branch},
		{"commit", report.Build.Commit},
		{"binary", report.Build.Binary},
		{"platform", report.Build.Platform},
		{"executed", fmt.Sprintf("%d / %d", report.Summary.Executed, report.Summary.Total)},
		{"passed", strconv.Itoa(report.Summary.Passed)},
		{"failed", strconv.Itoa(report.Summary.Failed)},
		{"regressions", strconv.Itoa(report.Summary.Regressions)},
		{"product defects", strconv.Itoa(report.Summary.ProductDefects)},
		{"environment issues", strconv.Itoa(report.Summary.EnvironmentIssues)},
		{"expected failures", strconv.Itoa(report.Summary.ExpectedFailures)},
		{"test bench defects", strconv.Itoa(report.Summary.BenchDefects)},
		{"blocked", strconv.Itoa(report.Summary.Blocked)},
		{"environments affected", strings.Join(report.Summary.Environments, "; ")},
		{"duration ms", strconv.FormatInt(report.Summary.DurationMS, 10)},
		{"sandboxes still on disk", strconv.Itoa(report.Cleanup.SandboxesOpen)},
	} {
		_ = writer.Write([]string{line[0], line[1]})
	}
}

// --- sandboxes --------------------------------------------------------------

// sandboxFor gives each environment its own throwaway world, so a cluster made
// for VMware cannot be found by a command run against Kind.
func (c *console) sandboxFor(env string) (*sandbox.Sandbox, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if box, ok := c.boxes[env]; ok {
		return box, nil
	}
	box, err := sandbox.New(env)
	if err != nil {
		return nil, err
	}
	for key, value := range c.savedCredentials() {
		if strings.TrimSpace(value) != "" {
			box.Set(key, value)
		}
	}
	// The sandbox puts a non-interactive vi shim first on PATH. `vi` is also
	// accepted by the CLI's editor allowlist; `true` no longer is.
	box.Set("EDITOR", "vi")
	box.Set("VISUAL", "vi")
	box.Set("PAGER", "cat")

	// A command that talks to a cluster needs a kubeconfig, and the sandbox
	// builds its environment from an allowlist, so nothing reaches it unless
	// it is put there. Only the test bench's own cluster is offered: the
	// ambient KUBECONFIG could point at anything, including production.
	if path := c.kindKubeconfig(); path != "" {
		box.Set("KUBECONFIG", path)
		// kind and kubectl live in ~/.local/bin here, which is not on PATH in
		// a non-login shell, so a deploy would report "kind: not found".
		// PathValue keeps the sandbox's fake bin at the front; setting PATH
		// outright would discard it.
		if home := os.Getenv("HOME"); home != "" {
			box.Set("PATH", box.PathValue()+string(os.PathListSeparator)+home+"/.local/bin")
		}
	}

	c.boxes[env] = box
	return box, nil
}

// kindKubeconfig returns the test bench cluster's kubeconfig if it exists,
// and "" otherwise. Checked per sandbox rather than cached, so creating the
// cluster is picked up without restarting the server.
func (c *console) kindKubeconfig() string {
	path := filepath.Join(os.Getenv("HOME"), ".cache", "opencli-testbench", "kubeconfig")
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return path
	}
	return ""
}

func (c *console) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for env, box := range c.boxes {
		_ = box.Cleanup()
		delete(c.boxes, env)
	}
}

func (c *console) environment(id string) *Environment {
	for index := range c.catalogue.Environments {
		if c.catalogue.Environments[index].ID == id {
			return &c.catalogue.Environments[index]
		}
	}
	return nil
}

// --- credentials ------------------------------------------------------------

const savedMask = "__saved__"

func (c *console) credentialsFile() string {
	return filepath.Join(c.root, "config", "credentials.local.yaml")
}

func (c *console) savedCredentials() map[string]string {
	raw, err := os.ReadFile(c.credentialsFile())
	if err != nil {
		return map[string]string{}
	}
	saved := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		saved[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return saved
}

func (c *console) maskedCredentials() map[string]string {
	out := map[string]string{}
	for key, value := range c.savedCredentials() {
		if redact.IsSecretName(key) {
			if value != "" {
				out[key] = savedMask
			}
			continue
		}
		out[key] = value
	}
	return out
}

func (c *console) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, c.maskedCredentials())
		return
	}
	var incoming map[string]string
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	saved := c.savedCredentials()
	for key, value := range incoming {
		switch {
		case value == savedMask:
		case strings.TrimSpace(value) == "":
			delete(saved, key)
		default:
			saved[key] = value
		}
	}
	var builder strings.Builder
	keys := make([]string, 0, len(saved))
	for key := range saved {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		builder.WriteString(key + ": " + saved[key] + "\n")
	}
	if err := os.WriteFile(c.credentialsFile(), []byte(builder.String()), 0o600); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.redactor.AddFromEnv(saved)
	// New credentials mean the sandboxes need rebuilding with them.
	c.cleanup()
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "file": c.credentialsFile()})
}

// credentialFields flattens the credential spec into the fields the page shows.
func credentialFields(loaded *spec.Spec) []map[string]any {
	var out []map[string]any
	if loaded == nil {
		return out
	}
	// A variable can appear in several methods — OS_AUTH_URL is in three of
	// them — so it is emitted once, carrying the union of the environments
	// those methods apply to. Emitting it per method would render the same
	// input several times, each overwriting the last.
	index := map[string]int{}
	for _, method := range loaded.Credentials {
		for _, field := range method.Fields {
			if field.Env == "" {
				continue
			}
			if at, ok := index[field.Env]; ok {
				out[at]["envs"] = union(out[at]["envs"].([]string), method.Envs)
				continue
			}
			index[field.Env] = len(out)
			out = append(out, map[string]any{
				"env": field.Env, "label": field.Name, "group": method.Name,
				"placeholder": field.Placeholder, "help": field.Help, "secret": field.Secret,
				// The spec has carried defaults all along and they were never
				// sent, so every field arrived blank however obvious its
				// value was. A default is never a secret: a prefilled
				// password would be a password in the page source.
				"default": defaultUnlessSecret(field),
				"options": field.Options,
				// Which console tabs this belongs under. Empty means every
				// tab, which is right for git and sops.
				"envs": append([]string{}, method.Envs...),
			})
		}
	}
	return out
}

// defaultUnlessSecret returns a field's default value, and never a secret's.
// Defaults are rendered into the page, so a default password would be a
// password sitting in the HTML.
func defaultUnlessSecret(field spec.CredentialField) string {
	if field.Secret {
		return ""
	}
	return field.Default
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	for _, value := range a {
		seen[value] = true
	}
	out := append([]string{}, a...)
	for _, value := range b {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// whatWeTest is the strip across the top of the page: what, why, where and
// how, in that order. Deliberately terse — it is the first thing read and the
// least important thing on the screen, so it earns four short columns and no
// more.
func whatWeTest() []map[string]any {
	return []map[string]any{
		{
			"title": "What we test",
			"lead":  "Every command this build has, on every infrastructure type.",
			"points": []string{
				"Does it exist, run and answer?",
				"Does it do what it says?",
				"Does it fail clearly, with a useful exit code?",
				"Does it keep credentials out of its output?",
			},
		},
		{
			"title": "Why we test",
			"lead":  "A CLI that provisions infrastructure is trusted by a person and by a pipeline.",
			"points": []string{
				"Correct — the file on disk proves it",
				"Safe — nothing damaged, nothing leaked",
				"Clear — success, failure, next action",
				"Automatable — exit codes and JSON hold",
			},
		},
		{
			"title": "Where we test",
			"lead":  "A separate sandbox per infrastructure type, thrown away on reset.",
			"points": []string{
				"OpenStack — offline config, cloud needs credentials",
				"VMware — offline config, rest needs vCenter",
				"Bare metal — inventory, no cloud API",
				"Kind — the whole lifecycle, locally, free",
			},
		},
		{
			"title": "How we test",
			"lead":  "The real binary, as a child process, output shown unchanged.",
			"points": []string{
				"Pick an environment, create its fixture",
				"Press Run on any command",
				"Real stdout, stderr and exit code below it",
				"Nothing mutating runs without the gate",
			},
		},
	}
}

// --- helpers ----------------------------------------------------------------

func isMutating(args string) bool {
	if dryRunRequested(args) {
		return false
	}
	for _, field := range splitArgs(args) {
		if strings.HasPrefix(field, "-") {
			continue
		}
		switch field {
		case "deploy", "destroy", "bootstrap", "reconcile", "restore", "apply", "push", "rotate":
			return true
		}
	}
	return false
}

func dryRunRequested(args string) bool {
	preview := false
	for _, field := range splitArgs(args) {
		switch field {
		case "--dry-run", "--dry-run=true":
			preview = true
		case "--dry-run=false":
			preview = false
		}
	}
	return preview
}

func probeBinaryVersion(binary string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(out))
	if index := strings.IndexByte(version, '\n'); index >= 0 {
		version = version[:index]
	}
	return version
}

func splitArgs(input string) []string {
	input = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), "opencenter"))
	var args []string
	var current strings.Builder
	var quote rune
	for _, char := range input {
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
		case char == '\'' || char == '"':
			quote = char
		case char == ' ' || char == '\t' || char == '\n':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(char)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

func splitLines(value string) []string {
	value = strings.TrimRight(value, "\n")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

// describeLocation flattens a location into one CSV cell.
func describeLocation(location Location) string {
	var parts []string
	if location.Config != "" {
		parts = append(parts, location.Config)
	} else if location.File != "" {
		parts = append(parts, location.File)
	}
	if location.Line > 0 {
		parts = append(parts, "line "+strconv.Itoa(location.Line))
	}
	if location.Field != "" {
		parts = append(parts, "field "+location.Field)
	}
	if location.Step != "" {
		parts = append(parts, "step "+location.Step)
	}
	if location.Log != "" {
		parts = append(parts, "log "+location.Log)
	}
	return strings.Join(parts, " · ")
}

func oneLine(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 2000 {
		value = value[:2000] + "…"
	}
	return strings.TrimSpace(value)
}

// diagnosisLines renders a diagnosis for a terminal. It is shared so that
// every failure looks the same wherever it came from — a command, or building
// the Kind cluster the commands need.
func diagnosisLines(finding *Diagnosis) []string {
	lines := []string{"", "CAUSE", "  " + finding.Cause}

	if !finding.Location.Empty() {
		lines = append(lines, "", "LOCATION")
		if finding.Location.Config != "" {
			lines = append(lines, "  configuration  "+finding.Location.Config)
		} else if finding.Location.File != "" {
			lines = append(lines, "  file           "+finding.Location.File)
		}
		if finding.Location.Line > 0 {
			lines = append(lines, "  line           "+strconv.Itoa(finding.Location.Line))
		}
		if finding.Location.Field != "" {
			lines = append(lines, "  field          "+finding.Location.Field)
		}
		if finding.Location.Step != "" {
			lines = append(lines, "  failed step    "+finding.Location.Step)
		}
		if finding.Location.Log != "" {
			lines = append(lines, "  log            "+finding.Location.Log)
		}
	}

	lines = append(lines, "", "POSSIBLE CAUSES  ("+finding.Category+")")
	for index, cause := range finding.Possible {
		lines = append(lines, "  "+strconv.Itoa(index+1)+". "+cause.Why)
		if cause.Check != "" {
			lines = append(lines, "     check:  "+cause.Check)
		}
	}
	return lines
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(body)
}

func hostDescription() string {
	description := runtime.GOOS + "/" + runtime.GOARCH
	if raw, err := os.ReadFile("/proc/version"); err == nil &&
		strings.Contains(strings.ToLower(string(raw)), "microsoft") {
		description = "WSL · " + description
	}
	if name, err := os.Hostname(); err == nil {
		description += " · " + name
	}
	return description
}

func openBrowser(url string) {
	for attempt := 0; attempt < 80; attempt++ {
		if response, err := http.Get(url + "/api/catalogue"); err == nil {
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
