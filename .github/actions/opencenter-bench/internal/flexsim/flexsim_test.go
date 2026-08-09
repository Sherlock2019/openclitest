package flexsim

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))

	inventory, err := LoadInventory(filepath.Join(root, "config", "flex-sim.yaml"))
	if err != nil {
		t.Fatalf("LoadInventory: %v", err)
	}

	simulator := New(inventory, "http://placeholder", false)
	httpServer := httptest.NewServer(simulator)
	t.Cleanup(httpServer.Close)

	// The service catalog has to advertise the address the client will really
	// reach, which is only known once the test server is listening.
	simulator.baseURL = httpServer.URL
	return simulator, httpServer
}

func TestAuthenticationReturnsACatalog(t *testing.T) {
	_, server := newTestServer(t)

	response, err := http.Post(server.URL+"/v3/auth/tokens", "application/json",
		strings.NewReader(`{"auth":{}}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusCreated {
		t.Errorf("got status %d, want 201", response.StatusCode)
	}
	if response.Header.Get("X-Subject-Token") == "" {
		t.Error("no X-Subject-Token header, so no client can authenticate")
	}

	var body struct {
		Token struct {
			Catalog []struct {
				Type      string `json:"type"`
				Endpoints []struct {
					URL string `json:"url"`
				} `json:"endpoints"`
			} `json:"catalog"`
		} `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	wanted := map[string]bool{"identity": false, "compute": false, "image": false,
		"network": false, "volumev3": false, "object-store": false}
	for _, service := range body.Token.Catalog {
		if _, ok := wanted[service.Type]; ok {
			wanted[service.Type] = true
		}
		for _, endpoint := range service.Endpoints {
			if !strings.HasPrefix(endpoint.URL, server.URL) {
				t.Errorf("service %s advertises %q, which is not this server", service.Type, endpoint.URL)
			}
		}
	}
	for service, present := range wanted {
		if !present {
			t.Errorf("the catalog does not offer %s", service)
		}
	}
}

func TestInventoryIsServed(t *testing.T) {
	_, server := newTestServer(t)

	for _, probe := range []struct {
		path string
		key  string
	}{
		{"/v2.1/flavors/detail", "flavors"},
		{"/image/v2/images", "images"},
		{"/network/v2.0/networks", "networks"},
		{"/network/v2.0/subnets", "subnets"},
		{"/volume/v3/types", "volume_types"},
		{"/v2.1/os-availability-zone", "availabilityZoneInfo"},
	} {
		response, err := http.Get(server.URL + probe.path)
		if err != nil {
			t.Fatalf("get %s: %v", probe.path, err)
		}
		var body map[string]any
		err = json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if err != nil {
			t.Fatalf("decode %s: %v", probe.path, err)
		}
		list, ok := body[probe.key].([]any)
		if !ok {
			t.Errorf("%s did not return a %s list: %v", probe.path, probe.key, body)
			continue
		}
		if len(list) == 0 {
			t.Errorf("%s returned an empty %s list", probe.path, probe.key)
		}
	}
}

func TestInjectedStatusIsConsumedOnce(t *testing.T) {
	simulator, server := newTestServer(t)
	simulator.InjectStatus("/v2.1/flavors", http.StatusTooManyRequests, 2)

	for attempt := 1; attempt <= 3; attempt++ {
		response, err := http.Get(server.URL + "/v2.1/flavors/detail")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		response.Body.Close()

		want := http.StatusTooManyRequests
		if attempt == 3 {
			want = http.StatusOK
		}
		if response.StatusCode != want {
			t.Errorf("attempt %d returned %d, want %d", attempt, response.StatusCode, want)
		}
		if attempt < 3 && response.Header.Get("Retry-After") == "" {
			t.Error("a 429 with no Retry-After tells a client nothing about when to come back")
		}
	}
}

func TestInjectedFaultOnlyMatchesItsPath(t *testing.T) {
	simulator, server := newTestServer(t)
	simulator.InjectStatus("/network/", http.StatusServiceUnavailable, 5)

	response, err := http.Get(server.URL + "/v2.1/flavors/detail")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("a fault aimed at the network service hit compute: got %d", response.StatusCode)
	}

	response, err = http.Get(server.URL + "/network/v2.0/networks")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("the injected fault did not fire: got %d", response.StatusCode)
	}
}

func TestMalformedResponseDoesNotParse(t *testing.T) {
	simulator, server := newTestServer(t)
	simulator.InjectMalformed("/v2.1/", 1)

	response, err := http.Get(server.URL + "/v2.1/flavors/detail")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()

	var into any
	if err := json.Unmarshal(body, &into); err == nil {
		t.Errorf("the malformed fault returned valid JSON: %s", body)
	}
}

func TestDelayStalls(t *testing.T) {
	simulator, server := newTestServer(t)
	simulator.InjectDelay("/v2.1/", 300*time.Millisecond, 1)

	client := &http.Client{Timeout: 5 * time.Second}
	started := time.Now()
	response, err := client.Get(server.URL + "/v2.1/flavors/detail")
	elapsed := time.Since(started)
	if err == nil {
		response.Body.Close()
	}
	if elapsed < 250*time.Millisecond {
		t.Errorf("the request came back in %s, so the delay did not apply", elapsed)
	}
}

func TestHistoryRecordsWhatWasAsked(t *testing.T) {
	simulator, server := newTestServer(t)
	simulator.ClearFaults()

	if _, err := http.Get(server.URL + "/v2.1/flavors/detail"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := http.Post(server.URL+"/v3/auth/tokens", "application/json",
		strings.NewReader("{}")); err != nil {
		t.Fatalf("post: %v", err)
	}

	history := simulator.History()
	if len(history) < 2 {
		t.Fatalf("history holds %d entries, want at least 2", len(history))
	}
	var sawPost bool
	for _, request := range history {
		if request.Method == http.MethodPost && request.Path == "/v3/auth/tokens" {
			sawPost = true
			if request.Status != http.StatusCreated {
				t.Errorf("authentication recorded status %d, want 201", request.Status)
			}
		}
	}
	if !sawPost {
		t.Error("the authentication call was not recorded")
	}
}

func TestCloudsYAMLPointsAtTheSimulator(t *testing.T) {
	simulator, server := newTestServer(t)
	clouds := simulator.CloudsYAML("flex-sim")

	for _, wanted := range []string{"clouds:", "flex-sim:", server.URL + "/v3", "identity_api_version: 3"} {
		if !strings.Contains(clouds, wanted) {
			t.Errorf("clouds.yaml does not contain %q:\n%s", wanted, clouds)
		}
	}
	// A person reading a generated credential file should be in no doubt that
	// it is fabricated.
	if !strings.Contains(clouds, "sim-user-id") {
		t.Error("the generated credential does not identify itself as the simulator's")
	}
}

func TestControlPlaneIsNotRecordedAsAnAPICall(t *testing.T) {
	simulator, server := newTestServer(t)
	simulator.ClearFaults()

	response, err := http.Post(server.URL+"/__bench/fault", "application/json",
		strings.NewReader(`{"path":"/v2.1/","status":403,"count":1}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	response.Body.Close()

	for _, request := range simulator.History() {
		if strings.HasPrefix(request.Path, "/__bench/") {
			t.Errorf("the bench's own control call was recorded as an API call: %s", request.Path)
		}
	}
}
