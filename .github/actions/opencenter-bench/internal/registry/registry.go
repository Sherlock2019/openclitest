// Package registry records everything a run creates, so the cleanup module has
// something better than hope to work from.
//
// The rule the registry exists to enforce: a delete command that returned zero
// is not proof of anything. Every resource carries both the command that
// removes it and the command that asks whether it is really gone, and cleanup
// is only reported as passed when the second one says so.
package registry

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// CleanupStatus is where a resource has got to.
type CleanupStatus string

const (
	// StatusPending means it still exists as far as the bench knows.
	StatusPending CleanupStatus = "pending"
	// StatusDeleted means the delete ran and the verification agreed.
	StatusDeleted CleanupStatus = "deleted"
	// StatusFailed means the delete ran and the resource is still there.
	StatusFailed CleanupStatus = "failed"
	// StatusNotFound means it was already gone when cleanup reached it, which
	// is fine — an earlier module destroyed it on purpose.
	StatusNotFound CleanupStatus = "not_found"
)

// Resource is one thing a module created.
type Resource struct {
	ID       string `json:"id"`
	ModuleID string `json:"module_id"`
	Provider string `json:"provider"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	// ExternalID is the provider's own identifier, when there is one.
	ExternalID string `json:"external_id,omitempty"`
	// Location is a path, a region, or whatever locates it.
	Location  string    `json:"location,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	// Cleanup is the command that removes it. Verify is the command that asks
	// whether it is gone; it must succeed and produce output that does NOT
	// contain Name for the resource to count as deleted.
	Cleanup []string `json:"cleanup_command,omitempty"`
	Verify  []string `json:"verify_command,omitempty"`

	Status CleanupStatus `json:"cleanup_status"`
	Detail string        `json:"cleanup_detail,omitempty"`
}

// Registry is the whole set for one run. Safe for concurrent use.
type Registry struct {
	mu        sync.Mutex
	resources []Resource
	sequence  int
}

// New returns an empty registry.
func New() *Registry { return &Registry{} }

// Add records a resource and returns the id it was given.
func (r *Registry) Add(resource Resource) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.sequence++
	if resource.ID == "" {
		resource.ID = resource.Type + "-" + itoa(r.sequence)
	}
	if resource.CreatedAt.IsZero() {
		resource.CreatedAt = time.Now()
	}
	if resource.Status == "" {
		resource.Status = StatusPending
	}
	r.resources = append(r.resources, resource)
	return resource.ID
}

// Update records what happened when cleanup reached a resource.
func (r *Registry) Update(id string, status CleanupStatus, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.resources {
		if r.resources[index].ID == id {
			r.resources[index].Status = status
			r.resources[index].Detail = detail
			return
		}
	}
}

// All returns a copy of every resource.
func (r *Registry) All() []Resource {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Resource, len(r.resources))
	copy(out, r.resources)
	return out
}

// TeardownOrder returns the resources in the order they must be removed:
// newest first, and within that, dependents before what they depend on.
//
// The ranking is the dependency order of a cloud, written down once. Deleting
// a network before the ports on it is the classic way to turn a clean teardown
// into a stuck one.
func (r *Registry) TeardownOrder() []Resource {
	rank := map[string]int{
		"kubernetes-resource": 0,
		"namespace":           1,
		"kind-cluster":        2,
		"cluster":             2,
		"server":              3,
		"instance":            3,
		"vm":                  3,
		"floating-ip":         4,
		"port":                5,
		"router":              6,
		"network":             7,
		"subnet":              7,
		"volume":              8,
		"security-group":      9,
		"keypair":             10,
		"git-branch":          11,
		"git-repository":      12,
		"process":             13,
		"lock":                14,
		"file":                15,
		"directory":           16,
		"config":              17,
	}

	resources := r.All()
	sort.SliceStable(resources, func(i, j int) bool {
		left, leftKnown := rank[resources[i].Type]
		right, rightKnown := rank[resources[j].Type]
		if !leftKnown {
			left = 50 // an unknown type is removed before the plumbing
		}
		if !rightKnown {
			right = 50
		}
		if left != right {
			return left < right
		}
		// Same kind: newest first, so a thing created inside another goes first.
		return resources[i].CreatedAt.After(resources[j].CreatedAt)
	})
	return resources
}

// Outstanding returns everything not confirmed gone.
func (r *Registry) Outstanding() []Resource {
	var out []Resource
	for _, resource := range r.All() {
		if resource.Status != StatusDeleted && resource.Status != StatusNotFound {
			out = append(out, resource)
		}
	}
	return out
}

// Save writes the registry so an interrupted run can still be cleaned up.
func (r *Registry) Save(path string) error {
	encoded, err := json.MarshalIndent(r.All(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}

// Load reads a registry saved by an earlier run.
func Load(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var resources []Resource
	if err := json.Unmarshal(raw, &resources); err != nil {
		return nil, err
	}
	registry := New()
	registry.resources = resources
	registry.sequence = len(resources)
	return registry, nil
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
