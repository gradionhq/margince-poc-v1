// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

import (
	"errors"
	"fmt"
	"sort"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// ErrUnknownProvider reports a provider name this build does not carry. The
// registered set is closed (PI-PARAM-1): a name absent here is a 404, never a
// call to something we have not reviewed.
var ErrUnknownProvider = errors.New("integrations: unknown provider")

// Registry is the closed set of adapters compiled into this build. Compose
// decides what goes in it — the real Surfe adapter, or the deterministic fake
// for a dev stack and the test lane.
//
// An empty registry is a supported configuration, not a broken one: with no
// adapter registered, every domain surface renders "no provider connected"
// and nothing can reach the network (PI-AC-9).
type Registry struct {
	adapters map[string]provider.Adapter
}

// NewRegistry builds a registry from the adapters compose supplies. It
// refuses a descriptor whose billing basis the platform cannot honour: the
// alternative is discovering at reservation time that we cannot price a run
// we have already admitted.
func NewRegistry(adapters ...provider.Adapter) (*Registry, error) {
	r := &Registry{adapters: map[string]provider.Adapter{}}
	for _, a := range adapters {
		d := a.Descriptor()
		if d.Name == "" {
			return nil, errors.New("integrations: an adapter declared no provider name")
		}
		if _, dup := r.adapters[d.Name]; dup {
			return nil, fmt.Errorf("integrations: provider %q registered twice", d.Name)
		}
		if _, err := d.WorstCase(d.Categories); err != nil {
			return nil, fmt.Errorf("integrations: provider %q cannot be registered: %w", d.Name, err)
		}
		if d.EgressHost == "" {
			return nil, fmt.Errorf("integrations: provider %q declared no egress host", d.Name)
		}
		r.adapters[d.Name] = a
	}
	return r, nil
}

// Names returns the registered providers in a stable order, so a settings
// card does not reshuffle between reads.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.adapters))
	for n := range r.adapters {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Adapter resolves one provider.
func (r *Registry) Adapter(name string) (provider.Adapter, error) {
	a, ok := r.adapters[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownProvider, name)
	}
	return a, nil
}

// Descriptor resolves one provider's static declaration.
func (r *Registry) Descriptor(name string) (provider.Descriptor, error) {
	a, err := r.Adapter(name)
	if err != nil {
		return provider.Descriptor{}, err
	}
	return a.Descriptor(), nil
}

// Empty reports whether anything is registered at all.
func (r *Registry) Empty() bool { return len(r.adapters) == 0 }
