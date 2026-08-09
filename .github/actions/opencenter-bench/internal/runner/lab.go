package runner

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/checks"
	"github.com/opencenter-cloud/opencli-testbench/internal/cli"
	"github.com/opencenter-cloud/opencli-testbench/internal/flexsim"
	"github.com/opencenter-cloud/opencli-testbench/internal/redact"
	"github.com/opencenter-cloud/opencli-testbench/internal/registry"
	"github.com/opencenter-cloud/opencli-testbench/internal/sandbox"
	"github.com/opencenter-cloud/opencli-testbench/internal/spec"
)

// MutateGate is the variable that has to be set before an environment is
// allowed to create anything that outlives a command. A checkbox in a browser
// is not enough: the person running the bench has to have said so in the
// environment they started it from.
const MutateGate = "OPENCLI_ALLOW_MUTATE"

// Lab is a prepared world: a sandbox, a runner pointed at the binary, and
// whatever far end the environment needs, already standing.
//
// Both the one-environment runner and the continuous workflow build their
// worlds through here, so a change to how isolation works cannot apply to one
// and not the other.
type Lab struct {
	Env     *checks.Env
	Sandbox *sandbox.Sandbox
	Runner  *cli.Runner
	// Describe is a one-line summary of the far end, for the log.
	Describe string

	stop func()
}

// LabOptions describe the world to build.
type LabOptions struct {
	// Root is the bench checkout, for config/flex-sim.yaml.
	Root string
	// Binary is the opencenter executable under test.
	Binary string
	// Environment is local, sim, flex or kind.
	Environment string
	// SandboxRoot places the sandbox somewhere specific. Empty means a fresh
	// directory under the system temporary directory.
	SandboxRoot string
	// Mutate allows checks that create things outliving a command.
	Mutate bool
	// Credentials are handed to the CLI, and every secret-looking one is
	// registered for redaction before a single command runs.
	Credentials map[string]string
	// Redactor is shared with the caller so evidence and reports use the same
	// one. Nil means a private one is created.
	Redactor *redact.Redactor
	// Registry records what the run creates. Nil means a private one.
	Registry *registry.Registry
	// Log receives progress lines.
	Log func(format string, args ...any)
}

// NewLab prepares an environment. Call Close when finished; it stops the
// simulator and, unless keep is set, removes the sandbox.
func NewLab(loaded *spec.Spec, options LabOptions) (*Lab, error) {
	environment, ok := loaded.Environment(options.Environment)
	if !ok {
		return nil, fmt.Errorf("unknown environment %q", options.Environment)
	}
	if environment.Mutating && options.Mutate && os.Getenv(MutateGate) != "1" {
		return nil, fmt.Errorf("%s can create real resources; set %s=1 to allow it",
			environment.Name, MutateGate)
	}
	if !environment.Mutating {
		options.Mutate = false
	}
	if options.Log == nil {
		options.Log = func(string, ...any) {}
	}

	var box *sandbox.Sandbox
	var err error
	if options.SandboxRoot != "" {
		if mkErr := os.MkdirAll(options.SandboxRoot, 0o700); mkErr != nil {
			return nil, mkErr
		}
		box, err = sandbox.NewIn(options.SandboxRoot)
	} else {
		box, err = sandbox.New(environment.ID)
	}
	if err != nil {
		return nil, err
	}

	redactor := options.Redactor
	if redactor == nil {
		redactor = redact.New()
	}
	resources := options.Registry
	if resources == nil {
		resources = registry.New()
	}

	runner := &cli.Runner{
		Binary:   options.Binary,
		Dir:      box.Work,
		Timeout:  cli.DefaultTimeout,
		Redactor: redactor,
	}

	// Register every secret the caller supplied before anything runs, so even
	// a crash on the first command cannot print one.
	redactor.AddFromEnv(options.Credentials)
	for key, value := range options.Credentials {
		if strings.TrimSpace(value) != "" {
			box.Set(key, value)
		}
	}

	lab := &Lab{Sandbox: box, Runner: runner}
	lab.Env = &checks.Env{
		ID:       environment.ID,
		Spec:     loaded,
		Root:     options.Root,
		Sandbox:  box,
		CLI:      runner,
		Mutate:   options.Mutate,
		Registry: resources,
		Log:      options.Log,
	}

	switch environment.ID {
	case "sim":
		simulator, stop, simErr := startSimulator(options.Root, box)
		if simErr != nil {
			_ = box.Cleanup()
			return nil, simErr
		}
		lab.stop = stop
		lab.Env.Sim = simulator
		lab.Env.Cloud = "flex-sim"
		lab.Describe = "simulated OpenStack on " + simulator.describe

	case "flex":
		cloud := options.Credentials["OS_CLOUD"]
		if cloud == "" {
			cloud = os.Getenv("OS_CLOUD")
		}
		if cloud == "" {
			_ = box.Cleanup()
			return nil, fmt.Errorf("the FLEX environment needs a cloud profile: set OS_CLOUD, or fill in the credentials panel")
		}
		lab.Env.Cloud = cloud
		box.Set("OS_CLOUD", cloud)

		written, writeErr := writeCloudsFromCredentials(box, options.Credentials)
		if writeErr != nil {
			_ = box.Cleanup()
			return nil, writeErr
		}
		switch {
		case written != "":
			box.Set("OS_CLIENT_CONFIG_FILE", written)
			lab.Describe = "real OpenStack, profile " + cloud + " (written from the credentials panel)"
		default:
			path := options.Credentials["OS_CLIENT_CONFIG_FILE"]
			if path == "" {
				path = os.Getenv("OS_CLIENT_CONFIG_FILE")
			}
			if path == "" {
				home, _ := os.UserHomeDir()
				path = filepath.Join(home, ".config", "openstack", "clouds.yaml")
			}
			box.Set("OS_CLIENT_CONFIG_FILE", expandHome(path))
			lab.Describe = "real OpenStack, profile " + cloud + " from " + expandHome(path)
		}

	case "kind":
		if provider := options.Credentials["KIND_EXPERIMENTAL_PROVIDER"]; provider != "" {
			box.Set("KIND_EXPERIMENTAL_PROVIDER", provider)
		}
		lab.Describe = "local Kubernetes through kind"

	default:
		lab.Describe = "the binary alone, in a sandbox"
	}

	runner.Env = box.Env()
	return lab, nil
}

// Refresh rebuilds the command environment. A check that added a variable —
// a kubeconfig, say — needs the next command to see it.
func (l *Lab) Refresh() {
	l.Runner.Env = l.Sandbox.Env()
}

// Version is the first line of `opencenter version`, or empty.
func (l *Lab) Version(ctx context.Context) string {
	result := l.Runner.Run(ctx, "version")
	if !result.OK() {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(result.Stdout, "\n", 2)[0])
}

// Close stops the far end. It does not remove the sandbox; the caller decides
// whether the evidence is still wanted.
func (l *Lab) Close() {
	if l.stop != nil {
		l.stop()
		l.stop = nil
	}
}

// Remove deletes the sandbox.
func (l *Lab) Remove() error {
	if l.Sandbox == nil {
		return nil
	}
	return l.Sandbox.Cleanup()
}

// --- simulator --------------------------------------------------------------

type simulator struct {
	server   *flexsim.Server
	describe string
}

func (s *simulator) Fault(path string, status, count int) error {
	s.server.InjectStatus(path, status, count)
	return nil
}

func (s *simulator) Malformed(path string, count int) error {
	s.server.InjectMalformed(path, count)
	return nil
}

func (s *simulator) Hang(path string, delay time.Duration, count int) error {
	s.server.InjectDelay(path, delay, count)
	return nil
}

func (s *simulator) Clear() error {
	s.server.ClearFaults()
	return nil
}

func (s *simulator) Requests() ([]checks.SimRequest, error) {
	history := s.server.History()
	out := make([]checks.SimRequest, 0, len(history))
	for _, request := range history {
		out = append(out, checks.SimRequest{
			Method: request.Method, Path: request.Path,
			Status: request.Status, At: request.At.Format(time.RFC3339Nano),
		})
	}
	return out, nil
}

// startSimulator listens on a free loopback port, writes a clouds.yaml into the
// sandbox pointing at it, and returns a stop function.
func startSimulator(root string, box *sandbox.Sandbox) (*simulator, func(), error) {
	inventory, err := flexsim.LoadInventory(filepath.Join(root, "config", "flex-sim.yaml"))
	if err != nil {
		return nil, nil, fmt.Errorf("simulator inventory: %w", err)
	}

	// Port 0 lets the kernel pick, so two runs never collide.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("simulator listen: %w", err)
	}
	address := listener.Addr().String()
	server := flexsim.New(inventory, "http://"+address, false)

	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 10 * time.Second}
	var once sync.Once
	go func() { _ = httpServer.Serve(listener) }()

	cloudsPath := filepath.Join(box.Root, "clouds.yaml")
	if err := os.WriteFile(cloudsPath, []byte(server.CloudsYAML("flex-sim")), 0o600); err != nil {
		_ = httpServer.Close()
		return nil, nil, fmt.Errorf("write clouds.yaml: %w", err)
	}
	box.Set("OS_CLOUD", "flex-sim")
	box.Set("OS_CLIENT_CONFIG_FILE", cloudsPath)

	stop := func() {
		once.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = httpServer.Shutdown(shutdownCtx)
		})
	}
	return &simulator{server: server, describe: address + " · " + server.Describe()}, stop, nil
}

// --- credentials ------------------------------------------------------------

// writeCloudsFromCredentials builds a clouds.yaml inside the sandbox when the
// console supplied the pieces directly rather than naming an existing profile.
// It returns "" when there was nothing to write.
func writeCloudsFromCredentials(box *sandbox.Sandbox, credentials map[string]string) (string, error) {
	get := func(key string) string { return strings.TrimSpace(credentials[key]) }

	authURL := get("OS_AUTH_URL")
	if authURL == "" {
		return "", nil
	}
	region := get("OS_REGION_NAME")
	iface := get("OS_INTERFACE")
	if iface == "" {
		iface = "public"
	}
	profile := get("OS_CLOUD")
	if profile == "" {
		profile = "opencenter-bench"
	}

	var auth []string
	switch {
	case get("OS_APPLICATION_CREDENTIAL_ID") != "":
		auth = append(auth,
			"      application_credential_id: "+get("OS_APPLICATION_CREDENTIAL_ID"),
			"      application_credential_secret: "+get("OS_APPLICATION_CREDENTIAL_SECRET"))
	case get("OS_TOKEN") != "":
		auth = append(auth,
			"      token: "+get("OS_TOKEN"),
			"      project_id: "+get("OS_PROJECT_ID"))
	case get("OS_USERNAME") != "":
		auth = append(auth,
			"      username: "+get("OS_USERNAME"),
			"      password: "+get("OS_PASSWORD"))
		for _, pair := range [][2]string{
			{"OS_PROJECT_NAME", "project_name"},
			{"OS_PROJECT_ID", "project_id"},
			{"OS_USER_DOMAIN_NAME", "user_domain_name"},
			{"OS_PROJECT_DOMAIN_NAME", "project_domain_name"},
		} {
			if value := get(pair[0]); value != "" {
				auth = append(auth, "      "+pair[1]+": "+value)
			}
		}
	default:
		return "", nil
	}

	var builder strings.Builder
	builder.WriteString("clouds:\n  " + profile + ":\n")
	switch {
	case get("OS_APPLICATION_CREDENTIAL_ID") != "":
		builder.WriteString("    auth_type: v3applicationcredential\n")
	case get("OS_TOKEN") != "":
		builder.WriteString("    auth_type: v3token\n")
	}
	builder.WriteString("    auth:\n      auth_url: " + authURL + "\n")
	builder.WriteString(strings.Join(auth, "\n") + "\n")
	if region != "" {
		builder.WriteString("    region_name: " + region + "\n")
	}
	builder.WriteString("    interface: " + iface + "\n    identity_api_version: 3\n")

	path := filepath.Join(box.Root, "clouds.yaml")
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		return "", err
	}
	box.Set("OS_CLOUD", profile)
	return path, nil
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}
