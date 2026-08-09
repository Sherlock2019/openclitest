// Package flexsim serves a stand-in for the OpenStack APIs a Rackspace FLEX
// project exposes, so the cloud half of the CLI can be exercised without an
// account.
//
// This is not a stub inside the CLI. It is a real HTTP server speaking the
// Keystone, Nova, Glance, Neutron, Cinder and Swift wire formats, and the CLI
// is pointed at it with an ordinary clouds.yaml. Every request it receives is
// a request the CLI genuinely made — the same code path, the same HTTP client,
// the same parsing. Only the far end is fabricated.
//
// On top of serving a plausible tenant it can be told to misbehave: return a
// 401 on the next authentication, rate-limit the third call to Nova, hand back
// JSON that does not parse, or stall until the client's deadline expires.
// Those are the cases a real cloud produces at the worst possible moment and
// that no read-only test against a healthy project will ever reach.
package flexsim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Inventory is the tenant the simulator serves.
type Inventory struct {
	Project struct {
		ID     string `yaml:"id"`
		Name   string `yaml:"name"`
		Domain string `yaml:"domain"`
		Region string `yaml:"region"`
		User   string `yaml:"user"`
	} `yaml:"project"`
	Flavors []struct {
		ID    string `yaml:"id"`
		Name  string `yaml:"name"`
		VCPUs int    `yaml:"vcpus"`
		RAM   int    `yaml:"ram"`
		Disk  int    `yaml:"disk"`
	} `yaml:"flavors"`
	Images []struct {
		ID     string `yaml:"id"`
		Name   string `yaml:"name"`
		Status string `yaml:"status"`
		Size   int64  `yaml:"size"`
	} `yaml:"images"`
	Networks []struct {
		ID       string   `yaml:"id"`
		Name     string   `yaml:"name"`
		External bool     `yaml:"external"`
		Subnets  []string `yaml:"subnets"`
	} `yaml:"networks"`
	Subnets []struct {
		ID        string `yaml:"id"`
		Name      string `yaml:"name"`
		CIDR      string `yaml:"cidr"`
		NetworkID string `yaml:"network_id"`
		GatewayIP string `yaml:"gateway_ip"`
	} `yaml:"subnets"`
	VolumeTypes []struct {
		ID   string `yaml:"id"`
		Name string `yaml:"name"`
	} `yaml:"volume_types"`
	AvailabilityZones []string       `yaml:"availability_zones"`
	Quota             map[string]int `yaml:"quota"`
}

// LoadInventory reads a tenant description from disk.
func LoadInventory(path string) (*Inventory, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	inventory := &Inventory{}
	if err := yaml.Unmarshal(raw, inventory); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return inventory, nil
}

// Request is one call the CLI made.
type Request struct {
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Status int       `json:"status"`
	At     time.Time `json:"at"`
}

// fault is an injected failure, consumed as it fires.
type fault struct {
	path      string
	status    int
	malformed bool
	delay     time.Duration
	remaining int
}

// Server is the simulator.
type Server struct {
	inventory *Inventory
	baseURL   string
	verbose   bool

	mu         sync.Mutex
	requests   []Request
	faults     []*fault
	containers map[string]struct{}

	mux *http.ServeMux
}

// New builds a simulator for an inventory. baseURL is what the service catalog
// advertises, so it has to be the address the CLI will actually reach.
func New(inventory *Inventory, baseURL string, verbose bool) *Server {
	s := &Server{
		inventory:  inventory,
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		verbose:    verbose,
		containers: map[string]struct{}{},
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	mux := http.NewServeMux()

	// Keystone. Authentication lands here first; everything else is discovered
	// from the catalog this returns.
	mux.HandleFunc("/v3/auth/tokens", s.tokens)
	mux.HandleFunc("/v3/users/", s.ec2Credentials)
	mux.HandleFunc("/v3/", s.identity)

	// Nova.
	mux.HandleFunc("/v2.1/flavors/detail", s.flavorsDetail)
	mux.HandleFunc("/v2.1/os-availability-zone", s.availabilityZones)
	mux.HandleFunc("/v2.1/limits", s.limits)
	mux.HandleFunc("/v2.1/", s.notFound)

	// Glance.
	mux.HandleFunc("/image/v2/images", s.imagesList)

	// Neutron.
	mux.HandleFunc("/network/v2.0/networks", s.networksList)
	mux.HandleFunc("/network/v2.0/subnets", s.subnetsList)

	// Cinder.
	mux.HandleFunc("/volume/v3/types", s.volumeTypes)

	// Swift.
	mux.HandleFunc("/swift/", s.objectStore)

	// The bench's own control plane. It is deliberately namespaced so it can
	// never be confused with an OpenStack path.
	mux.HandleFunc("/__bench/fault", s.controlFault)
	mux.HandleFunc("/__bench/clear", s.controlClear)
	mux.HandleFunc("/__bench/requests", s.controlRequests)

	mux.HandleFunc("/", s.root)
	s.mux = mux
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/__bench/") {
		s.mux.ServeHTTP(w, r)
		return
	}

	if injected := s.takeFault(r.URL.Path); injected != nil {
		s.applyFault(w, r, injected)
		return
	}

	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.mux.ServeHTTP(recorder, r)
	s.record(r, recorder.status)
	if s.verbose {
		fmt.Printf("  %-6s %-52s %d\n", r.Method, r.URL.Path, recorder.status)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (s *Server) record(r *http.Request, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{
		Method: r.Method, Path: r.URL.Path, Status: status, At: time.Now(),
	})
	// A long run should not grow without bound; the recent history is what a
	// check ever looks at.
	if len(s.requests) > 2000 {
		s.requests = s.requests[len(s.requests)-1000:]
	}
}

// --- fault injection --------------------------------------------------------

// InjectStatus makes the next count requests whose path contains match fail
// with the given HTTP status.
func (s *Server) InjectStatus(match string, status, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, &fault{path: match, status: status, remaining: count})
}

// InjectMalformed makes matching requests return a body that is not JSON.
func (s *Server) InjectMalformed(match string, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, &fault{path: match, malformed: true, remaining: count})
}

// InjectDelay makes matching requests stall, so a client-side timeout fires.
func (s *Server) InjectDelay(match string, delay time.Duration, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, &fault{path: match, delay: delay, remaining: count})
}

// ClearFaults removes every injected fault and the recorded history.
func (s *Server) ClearFaults() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = nil
	s.requests = nil
}

// History returns the calls made so far.
func (s *Server) History() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *Server) takeFault(path string) *fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range s.faults {
		if candidate.remaining <= 0 || !strings.Contains(path, candidate.path) {
			continue
		}
		candidate.remaining--
		copied := *candidate
		return &copied
	}
	return nil
}

func (s *Server) applyFault(w http.ResponseWriter, r *http.Request, injected *fault) {
	switch {
	case injected.delay > 0:
		time.Sleep(injected.delay)
		s.record(r, 0)
		// Close without answering: a stalled request that then dies is what a
		// wedged load balancer looks like.
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		http.Error(w, "simulated timeout", http.StatusGatewayTimeout)

	case injected.malformed:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"this": "is not", "valid`))
		s.record(r, http.StatusOK)

	default:
		status := injected.status
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1")
		}
		body := map[string]any{
			"error": map[string]any{
				"code":    status,
				"title":   http.StatusText(status),
				"message": fmt.Sprintf("simulated %d from the bench", status),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
		s.record(r, status)
	}

	if s.verbose {
		fmt.Printf("  %-6s %-52s injected\n", r.Method, r.URL.Path)
	}
}

// --- control plane ----------------------------------------------------------

func (s *Server) controlFault(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path      string `json:"path"`
		Status    int    `json:"status"`
		Count     int    `json:"count"`
		Malformed bool   `json:"malformed"`
		DelayMS   int    `json:"delay_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Count == 0 {
		body.Count = 1
	}
	switch {
	case body.Malformed:
		s.InjectMalformed(body.Path, body.Count)
	case body.DelayMS > 0:
		s.InjectDelay(body.Path, time.Duration(body.DelayMS)*time.Millisecond, body.Count)
	default:
		s.InjectStatus(body.Path, body.Status, body.Count)
	}
	send(w, http.StatusOK, map[string]string{"status": "injected"})
}

func (s *Server) controlClear(w http.ResponseWriter, _ *http.Request) {
	s.ClearFaults()
	send(w, http.StatusOK, map[string]string{"status": "cleared"})
}

func (s *Server) controlRequests(w http.ResponseWriter, _ *http.Request) {
	send(w, http.StatusOK, s.History())
}

// --- OpenStack handlers -----------------------------------------------------

func send(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) endpointsFor(url string) []map[string]string {
	return []map[string]string{{
		"interface": "public",
		"region":    s.inventory.Project.Region,
		"region_id": s.inventory.Project.Region,
		"url":       url,
		"id":        "ep-" + url,
	}}
}

// tokens answers authentication and returns the service catalog. Both
// application-credential and password auth land here; the simulator accepts
// either, because its job is to exercise the CLI rather than to enforce
// identity. Rejection is done deliberately, through an injected fault.
func (s *Server) tokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.notFound(w, r)
		return
	}
	w.Header().Set("X-Subject-Token", "flex-sim-token")

	project := s.inventory.Project
	catalog := []map[string]any{
		{"type": "identity", "name": "keystone", "id": "svc-identity",
			"endpoints": s.endpointsFor(s.baseURL + "/v3")},
		{"type": "compute", "name": "nova", "id": "svc-compute",
			"endpoints": s.endpointsFor(s.baseURL + "/v2.1")},
		{"type": "image", "name": "glance", "id": "svc-image",
			"endpoints": s.endpointsFor(s.baseURL + "/image")},
		{"type": "network", "name": "neutron", "id": "svc-network",
			"endpoints": s.endpointsFor(s.baseURL + "/network")},
		{"type": "volumev3", "name": "cinderv3", "id": "svc-volumev3",
			"endpoints": s.endpointsFor(s.baseURL + "/volume/v3")},
		{"type": "block-storage", "name": "cinder", "id": "svc-blockstorage",
			"endpoints": s.endpointsFor(s.baseURL + "/volume/v3")},
		{"type": "object-store", "name": "swift", "id": "svc-objectstore",
			"endpoints": s.endpointsFor(s.baseURL + "/swift/v1/AUTH_" + project.ID)},
	}

	send(w, http.StatusCreated, map[string]any{
		"token": map[string]any{
			"methods":    []string{"password"},
			"expires_at": time.Now().Add(12 * time.Hour).UTC().Format(time.RFC3339),
			"issued_at":  time.Now().UTC().Format(time.RFC3339),
			"catalog":    catalog,
			"project": map[string]any{
				"id": project.ID, "name": project.Name,
				"domain": map[string]string{"id": "default", "name": project.Domain},
			},
			"user": map[string]any{
				"id": "sim-user-id", "name": project.User,
				"domain": map[string]string{"id": "default", "name": project.Domain},
			},
			"roles": []map[string]string{{"id": "role-member", "name": "member"}},
		},
	})
}

// ec2Credentials issues the S3-compatible key pair a backup tool uses to reach
// Swift. The values are fabricated and obvious placeholders.
func (s *Server) ec2Credentials(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/credentials/OS-EC2") {
		s.identity(w, r)
		return
	}
	credential := map[string]any{
		"access":    "flexsim0000000000000",
		"secret":    "flexsim000000000000000000000000000000000",
		"user_id":   "sim-user-id",
		"tenant_id": s.inventory.Project.ID,
		"trust_id":  nil,
		"links":     map[string]string{"self": s.baseURL + r.URL.Path},
	}
	if r.Method == http.MethodGet {
		send(w, http.StatusOK, map[string]any{"credentials": []any{credential}})
		return
	}
	send(w, http.StatusCreated, map[string]any{"credential": credential})
}

func (s *Server) identity(w http.ResponseWriter, r *http.Request) {
	project := s.inventory.Project
	switch {
	case strings.HasSuffix(r.URL.Path, "/projects"):
		send(w, http.StatusOK, map[string]any{"projects": []map[string]any{
			{"id": project.ID, "name": project.Name, "enabled": true, "domain_id": "default"},
		}})
	case strings.HasSuffix(r.URL.Path, "/domains"):
		send(w, http.StatusOK, map[string]any{"domains": []map[string]any{
			{"id": "default", "name": project.Domain, "enabled": true},
		}})
	default:
		send(w, http.StatusOK, map[string]any{
			"version": map[string]string{"id": "v3.14", "status": "stable"}})
	}
}

func (s *Server) flavorsDetail(w http.ResponseWriter, _ *http.Request) {
	list := make([]map[string]any, 0, len(s.inventory.Flavors))
	for _, flavor := range s.inventory.Flavors {
		list = append(list, map[string]any{
			"id": flavor.ID, "name": flavor.Name, "vcpus": flavor.VCPUs,
			"ram": flavor.RAM, "disk": flavor.Disk, "swap": "",
			"OS-FLV-EXT-DATA:ephemeral": 0, "os-flavor-access:is_public": true,
			"rxtx_factor": 1.0,
		})
	}
	send(w, http.StatusOK, map[string]any{"flavors": list})
}

func (s *Server) availabilityZones(w http.ResponseWriter, _ *http.Request) {
	list := make([]map[string]any, 0, len(s.inventory.AvailabilityZones))
	for _, zone := range s.inventory.AvailabilityZones {
		list = append(list, map[string]any{
			"zoneName":  zone,
			"zoneState": map[string]bool{"available": true},
			"hosts":     nil,
		})
	}
	send(w, http.StatusOK, map[string]any{"availabilityZoneInfo": list})
}

func (s *Server) limits(w http.ResponseWriter, _ *http.Request) {
	quota := s.inventory.Quota
	send(w, http.StatusOK, map[string]any{"limits": map[string]any{
		"absolute": map[string]int{
			"maxTotalInstances":  quota["instances"],
			"maxTotalCores":      quota["cores"],
			"maxTotalRAMSize":    quota["ram"],
			"totalInstancesUsed": 0, "totalCoresUsed": 0, "totalRAMUsed": 0,
		},
		"rate": []any{},
	}})
}

func (s *Server) imagesList(w http.ResponseWriter, _ *http.Request) {
	list := make([]map[string]any, 0, len(s.inventory.Images))
	for _, image := range s.inventory.Images {
		list = append(list, map[string]any{
			"id": image.ID, "name": image.Name, "status": image.Status,
			"size": image.Size, "visibility": "public", "protected": false,
			"disk_format": "qcow2", "container_format": "bare",
			"min_disk": 20, "min_ram": 512, "owner": s.inventory.Project.ID,
			"created_at": "2025-01-01T00:00:00Z", "updated_at": "2025-01-01T00:00:00Z",
			"tags": []string{},
		})
	}
	send(w, http.StatusOK, map[string]any{"images": list, "first": "/v2/images"})
}

func (s *Server) networksList(w http.ResponseWriter, _ *http.Request) {
	list := make([]map[string]any, 0, len(s.inventory.Networks))
	for _, network := range s.inventory.Networks {
		subnets := network.Subnets
		if subnets == nil {
			subnets = []string{}
		}
		list = append(list, map[string]any{
			"id": network.ID, "name": network.Name, "status": "ACTIVE",
			"admin_state_up": true, "shared": false,
			"router:external": network.External, "subnets": subnets,
			"tenant_id": s.inventory.Project.ID, "project_id": s.inventory.Project.ID,
		})
	}
	send(w, http.StatusOK, map[string]any{"networks": list})
}

func (s *Server) subnetsList(w http.ResponseWriter, _ *http.Request) {
	list := make([]map[string]any, 0, len(s.inventory.Subnets))
	for _, subnet := range s.inventory.Subnets {
		list = append(list, map[string]any{
			"id": subnet.ID, "name": subnet.Name, "cidr": subnet.CIDR,
			"network_id": subnet.NetworkID, "gateway_ip": subnet.GatewayIP,
			"ip_version": 4, "enable_dhcp": true,
			"tenant_id": s.inventory.Project.ID, "project_id": s.inventory.Project.ID,
			"allocation_pools": []map[string]string{},
			"dns_nameservers":  []string{}, "host_routes": []string{},
		})
	}
	send(w, http.StatusOK, map[string]any{"subnets": list})
}

func (s *Server) volumeTypes(w http.ResponseWriter, _ *http.Request) {
	list := make([]map[string]any, 0, len(s.inventory.VolumeTypes))
	for _, volumeType := range s.inventory.VolumeTypes {
		list = append(list, map[string]any{
			"id": volumeType.ID, "name": volumeType.Name,
			"is_public": true, "extra_specs": map[string]string{},
		})
	}
	send(w, http.StatusOK, map[string]any{"volume_types": list})
}

// objectStore answers the Swift account and container operations. Swift is
// unusual in that each verb has its own expected status: create returns 201,
// delete and metadata 204, listing 200. A blanket 200 makes the client reject
// the response, so each is answered with what the API actually specifies.
func (s *Server) objectStore(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	count := len(s.containers)
	s.mu.Unlock()

	w.Header().Set("X-Account-Container-Count", strconv.Itoa(count))
	w.Header().Set("X-Account-Object-Count", "0")
	w.Header().Set("X-Account-Bytes-Used", "0")
	w.Header().Set("X-Container-Object-Count", "0")
	w.Header().Set("X-Container-Bytes-Used", "0")

	name := strings.TrimPrefix(r.URL.Path, "/swift/v1/AUTH_"+s.inventory.Project.ID)
	name = strings.Trim(name, "/")

	switch r.Method {
	case http.MethodPut, http.MethodPost:
		if name != "" {
			s.mu.Lock()
			s.containers[name] = struct{}{}
			s.mu.Unlock()
		}
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		s.mu.Lock()
		delete(s.containers, name)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case http.MethodHead:
		w.WriteHeader(http.StatusNoContent)
	default:
		s.mu.Lock()
		list := make([]map[string]any, 0, len(s.containers))
		for container := range s.containers {
			list = append(list, map[string]any{"name": container, "count": 0, "bytes": 0})
		}
		s.mu.Unlock()
		send(w, http.StatusOK, list)
	}
}

func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.notFound(w, r)
		return
	}
	send(w, http.StatusOK, map[string]any{
		"versions": map[string]any{"values": []map[string]any{
			{"id": "v3.14", "status": "stable",
				"links": []map[string]string{{"rel": "self", "href": s.baseURL + "/v3/"}}},
		}},
	})
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	send(w, http.StatusNotFound, map[string]any{
		"error": map[string]any{"code": 404, "title": "Not Found",
			"message": "the simulator does not implement " + r.URL.Path}})
}

// CloudsYAML renders a clouds.yaml pointing at this simulator.
//
// It uses user_id rather than username: the OpenStack client rejects both
// together, and the object-storage path needs a user id to request an EC2
// credential.
func (s *Server) CloudsYAML(profile string) string {
	project := s.inventory.Project
	return fmt.Sprintf(`clouds:
  %s:
    auth:
      auth_url: %s/v3
      user_id: sim-user-id
      password: simulator
      project_id: %s
      project_name: %s
      project_domain_name: %s
    region_name: %s
    interface: public
    identity_api_version: 3
`, profile, s.baseURL, project.ID, project.Name, project.Domain, project.Region)
}

// Describe is the one-line summary the console shows when the simulator starts.
func (s *Server) Describe() string {
	inventory := s.inventory
	return fmt.Sprintf("project %s (%s) region %s · %d flavors · %d images · %d networks · %d subnets · %d volume types",
		inventory.Project.Name, inventory.Project.ID, inventory.Project.Region,
		len(inventory.Flavors), len(inventory.Images), len(inventory.Networks),
		len(inventory.Subnets), len(inventory.VolumeTypes))
}
