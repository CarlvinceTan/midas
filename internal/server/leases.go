package server

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Lease serialises a shared resource between agents. The browser and any user
// device are single-owner resources: two agents driving one tab is how work gets
// lost, so the second waits or is refused rather than interleaving.
type Lease struct {
	Resource  string `json:"resource"`
	Holder    string `json:"holder"`
	Note      string `json:"note,omitempty"`
	At        int64  `json:"at"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
}

// LeaseRegistry hands out leases.
type LeaseRegistry struct {
	mu     sync.Mutex
	leases map[string]Lease
	now    func() time.Time
	// defaultTTL bounds a lease so a crashed agent cannot hold a resource forever.
	defaultTTL time.Duration
}

// NewLeases creates an empty registry.
func NewLeases(defaultTTL time.Duration) *LeaseRegistry {
	if defaultTTL <= 0 {
		defaultTTL = 5 * time.Minute
	}
	return &LeaseRegistry{leases: map[string]Lease{}, now: time.Now, defaultTTL: defaultTTL}
}

var (
	// ErrLeaseHeld means someone else holds it, and names them so the caller can
	// ask rather than retry blindly.
	ErrLeaseHeld = errors.New("server: the resource is leased by another agent")
	// ErrNoLease means there was nothing to release.
	ErrNoLease = errors.New("server: no such lease")
)

// Grant takes a lease for a resource, or reports who holds it.
func (r *LeaseRegistry) Grant(resource, holder, note string, ttl time.Duration) (Lease, error) {
	resource, holder = strings.TrimSpace(resource), strings.TrimSpace(holder)
	if resource == "" || holder == "" {
		return Lease{}, errors.New("server: a lease needs a resource and a holder")
	}
	if ttl <= 0 {
		ttl = r.defaultTTL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked()
	if existing, held := r.leases[resource]; held && existing.Holder != holder {
		return existing, fmt.Errorf("%w: %s", ErrLeaseHeld, existing.Holder)
	}
	lease := Lease{Resource: resource, Holder: holder, Note: note, At: r.now().UnixMilli(), ExpiresAt: r.now().Add(ttl).UnixMilli()}
	r.leases[resource] = lease
	return lease, nil
}

// Release drops a lease, and only the holder may drop it.
func (r *LeaseRegistry) Release(resource, holder string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked()
	existing, held := r.leases[strings.TrimSpace(resource)]
	if !held {
		return ErrNoLease
	}
	if existing.Holder != strings.TrimSpace(holder) {
		return fmt.Errorf("%w: held by %s", ErrLeaseHeld, existing.Holder)
	}
	delete(r.leases, existing.Resource)
	return nil
}

// Holders lists the leases in force, ordered by resource.
func (r *LeaseRegistry) Holders() []Lease {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked()
	leases := make([]Lease, 0, len(r.leases))
	for _, lease := range r.leases {
		leases = append(leases, lease)
	}
	slices.SortStableFunc(leases, func(a, b Lease) int { return cmp.Compare(a.Resource, b.Resource) })
	return leases
}

// Held reports whether a holder owns a resource.
func (r *LeaseRegistry) Held(resource, holder string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked()
	lease, ok := r.leases[strings.TrimSpace(resource)]
	return ok && lease.Holder == strings.TrimSpace(holder)
}

// expireLocked drops leases that ran out, so a crashed agent frees its resource
// without an operator.
func (r *LeaseRegistry) expireLocked() {
	now := r.now().UnixMilli()
	for resource, lease := range r.leases {
		if lease.ExpiresAt > 0 && lease.ExpiresAt < now {
			delete(r.leases, resource)
		}
	}
}
