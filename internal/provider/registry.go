package provider

import (
	"errors"
	"fmt"
)

// ErrUnknown is returned when a provider name has no registered implementation.
var ErrUnknown = errors.New("unknown provider")

// Registry maps provider names to implementations (the Router role). It serves
// both as the session.Manager's resolver and as the daemon's provider listing.
type Registry struct {
	order []string
	m     map[string]Provider
}

// NewRegistry builds a registry from the given providers (insertion order kept).
func NewRegistry(ps ...Provider) *Registry {
	r := &Registry{m: make(map[string]Provider, len(ps))}
	for _, p := range ps {
		if _, ok := r.m[p.Name()]; !ok {
			r.order = append(r.order, p.Name())
		}
		r.m[p.Name()] = p
	}
	return r
}

// Resolve returns the implementation for name, or ErrUnknown. Its signature
// matches session.ProviderResolver, so pass reg.Resolve directly.
func (r *Registry) Resolve(name string) (Provider, error) {
	p, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("%q: %w", name, ErrUnknown)
	}
	return p, nil
}

// List returns the registered providers in registration order.
func (r *Registry) List() []Provider {
	out := make([]Provider, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.m[n])
	}
	return out
}
