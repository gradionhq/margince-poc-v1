// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package aitasks is the census of this build's AI invocation sites: which
// task each one serves, what the contract calls it, and how it invokes the
// model.
//
// It deliberately claims nothing about HOW a site works. A task is not one
// prompt (rate_extract has two, cold_start four), a site's answer schema may
// be built per call, and an agent loop has no single buildable request at all
// — so an interface promising build-then-parse would force honest
// implementations into adapters that lie. The shared invariant is only that a
// registered shipped site exists, the contract declares it, and it can be
// located.
//
// Registration is composition-time, like automation's RegisterWorkflow: a
// process role that builds no model path registers nothing, and a test builds
// its own registry rather than mutating a global.
package aitasks

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// Site is one registered model-invocation site. Variant matches a name in the
// task's contract sites[]; Kind must match the kind the contract declares for
// it, so a stateful loop can never be registered as a one-shot request.
type Site struct {
	Task    ai.Task
	Variant string
	Kind    string
}

func (s Site) key() string { return string(s.Task) + "/" + s.Variant }

// Registry collects the sites one composition registers.
type Registry struct {
	sites map[string]Site
	dupes []string
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry { return &Registry{sites: map[string]Site{}} }

// Register adds one site. A duplicate key is recorded rather than overwritten
// silently — Validate reports it, so two implementations of one site cannot
// resolve to whichever happened to register last.
func (r *Registry) Register(s Site) {
	if _, exists := r.sites[s.key()]; exists {
		r.dupes = append(r.dupes, s.key())
		return
	}
	r.sites[s.key()] = s
}

// All returns every registered site, ordered by task then variant.
func (r *Registry) All() []Site {
	out := make([]Site, 0, len(r.sites))
	for _, s := range r.sites {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// Lookup finds one registered site.
func (r *Registry) Lookup(task ai.Task, variant string) (Site, bool) {
	s, ok := r.sites[string(task)+"/"+variant]
	return s, ok
}

// Validate holds the registered set to the contract: no duplicates, every
// registered site declared with the kind the contract gives it, every shipped
// task's sites all present, and no site on a planned task. It reports every
// problem at once — a wiring fix wants the whole list.
func (r *Registry) Validate() error {
	var problems []string
	for _, key := range r.dupes {
		problems = append(problems, fmt.Sprintf("site %s is registered twice", key))
	}

	declared := map[string]string{} // "task/variant" -> kind
	for _, task := range ai.AllTasks() {
		for _, site := range ai.SitesFor(task) {
			declared[string(task)+"/"+site.Name] = site.Kind
		}
	}

	for _, s := range r.All() {
		kind, ok := declared[s.key()]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"site %s is registered but the contract declares no such site (add it to sites[] in ai-tasks.yaml, or delete the registration)", s.key()))
			continue
		}
		if s.Kind != kind {
			problems = append(problems, fmt.Sprintf(
				"site %s is registered as kind %q but the contract declares %q", s.key(), s.Kind, kind))
		}
	}

	for _, task := range ai.AllTasks() {
		switch ai.Status(task) {
		case ai.StatusShipped:
			for _, site := range ai.SitesFor(task) {
				if _, ok := r.Lookup(task, site.Name); !ok {
					problems = append(problems, fmt.Sprintf(
						"task %s is shipped but its site %q is not registered", task, site.Name))
				}
			}
		case ai.StatusPlanned:
			for _, s := range r.All() {
				if s.Task == task {
					problems = append(problems, fmt.Sprintf(
						"task %s is planned but site %q is registered — mark it shipped in the contract, or drop the registration", task, s.Variant))
				}
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("aitasks: census does not match the task contract:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// The two things a certification run can actually cover.
const (
	// ScopeFullInvocation: the whole production invocation is one request, so
	// certifying the request certifies the site.
	ScopeFullInvocation = "full_invocation"
	// ScopeSingleTurn: the scenario seeds the window and grades ONE reply. The
	// surrounding conversation or tool loop is supplied, not exercised.
	ScopeSingleTurn = "single_turn"
)

// CertifiedScope reports how much of this site a certification run covers.
//
// A one-shot site's whole invocation IS one request, so a scored request is a
// scored site. A multi-turn or agent-loop site is different in kind: its
// committed scenarios seed the prior turns (an agent scenario supplies the tool
// result as context) and grade the single reply that follows. That is a real
// measurement — it is what the shipped prompt does with that window — but it is
// not the loop, and a record that does not distinguish them claims more than it
// tested.
func (s Site) CertifiedScope() string {
	if s.Kind == ai.SiteKindOneShot {
		return ScopeFullInvocation
	}
	return ScopeSingleTurn
}
