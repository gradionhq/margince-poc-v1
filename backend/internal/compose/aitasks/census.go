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
	"slices"
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

// Registry collects the sites one composition registers, and the certification
// case each site is served by.
type Registry struct {
	sites     map[string]Site
	cases     map[string]binding
	dupes     []string
	caseDupes []string
}

// binding is one certification case together with the site it was bound under.
// Both are kept because they are separate claims: the composition says this
// case serves that site, and the case says which site it serves. Validate is
// what holds the two to each other.
type binding struct {
	site    Site
	factory CaseFactory
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{sites: map[string]Site{}, cases: map[string]binding{}}
}

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

// BindCase attaches the certification case that serves one site. The site is
// named here rather than taken from the factory so the composition's claim and
// the case's own can differ — which is what makes a disagreement between them
// reportable instead of silently deciding where the case lands.
//
// A second bind on one site is recorded rather than overwritten, exactly as
// Register records a second registration: which of two cases certifies a site
// decides what its record measures, and that must never fall to whichever line
// ran last.
func (r *Registry) BindCase(s Site, c CaseFactory) {
	if _, bound := r.cases[s.key()]; bound {
		r.caseDupes = append(r.caseDupes, s.key())
		return
	}
	r.cases[s.key()] = binding{site: s, factory: c}
}

// CaseFor finds the case bound to one site.
//
//nolint:ireturn // CaseFactory IS the seam: one implementation per site behind the one interface the cert lane takes.
func (r *Registry) CaseFor(task ai.Task, variant string) (CaseFactory, bool) {
	b, ok := r.cases[string(task)+"/"+variant]
	if !ok {
		return nil, false
	}
	return b.factory, true
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

// Validate holds the registered set to the contract: no site and no case
// claimed twice, every
// registered site declared with the kind the contract gives it, every shipped
// task's sites all present, no site on a planned task, no certification case
// bound to a site nobody registered, a case bound to every shipped site, and
// no case whose own Site disagrees with the one it was bound under. It reports
// every problem at once — a wiring fix wants the whole list.
func (r *Registry) Validate() error {
	var problems []string
	for _, key := range r.dupes {
		problems = append(problems, fmt.Sprintf("site %s is registered twice", key))
	}
	for _, key := range r.caseDupes {
		problems = append(problems, fmt.Sprintf(
			"site %s has a second certification case bound (keep the one that certifies the request this build sends, and delete the other)", key))
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

	for key := range r.cases {
		if _, registered := r.sites[key]; !registered {
			problems = append(problems, fmt.Sprintf(
				"a certification case is bound to site %s, which is not registered (register the site, or delete the case)", key))
		}
	}

	problems = append(problems, r.statusProblems()...)
	problems = append(problems, r.caseProblems()...)
	problems = append(problems, r.bindingProblems()...)
	problems = append(problems, r.scopeProblems()...)

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("aitasks: census does not match the task contract:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// statusProblems holds the registered set to what the contract says each task's
// lifecycle is: a shipped task owes every site it declares, and a planned task
// owes none — a site registered against one is code the contract has not yet
// admitted exists.
func (r *Registry) statusProblems() []string {
	var problems []string
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
	return problems
}

// caseProblems holds every shipped site to its certification obligation: a
// site nobody can certify is a site whose record could only ever be a claim
// about a hand-written prompt, never about the request this build sends.
//
// The obligation is derived from the contract's shipped sites rather than from
// the registration, so a site the contract denies exists — a planned task's, or
// a variant it never declared — is answered by the problem that names THAT
// defect. Asking for its case too would point at the wrong fix: the
// registration goes, and the case was never owed.
func (r *Registry) caseProblems() []string {
	var problems []string
	for _, task := range ai.AllTasks() {
		if ai.Status(task) != ai.StatusShipped {
			continue
		}
		for _, site := range ai.SitesFor(task) {
			key := (Site{Task: task, Variant: site.Name}).key()
			if _, registered := r.sites[key]; !registered {
				continue // the absent registration is already a problem of its own.
			}
			if _, bound := r.cases[key]; !bound {
				problems = append(problems, fmt.Sprintf(
					"site %s is registered but no certification case is bound (bind one in NewTaskCensus, or the site ships uncertifiable)", key))
			}
		}
	}
	return problems
}

// bindingProblems holds every case to the site it was bound under. The cert
// lane reads a site back off the FACTORY — its kind decides the certified
// scope a record claims — while the census is read as the composition's list
// of what ships. A case that disagrees with the line it sits on makes those
// two readings say different things, and neither one is checkable against the
// contract on its own.
func (r *Registry) bindingProblems() []string {
	var problems []string
	for _, b := range r.cases {
		claimed := b.factory.Site()
		if claimed == b.site {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"site %s (kind %q) is bound to a certification case claiming site %s (kind %q) — bind each case under the site its own Site() names",
			b.site.key(), b.site.Kind, claimed.key(), claimed.Kind))
	}
	return problems
}

// scopeProblems holds every declared scope to the vocabulary and to the site it
// is declared on. A word no record can report leaves a run's coverage
// unreadable, and a declaration BROADER than the site's kind is a case claiming
// to have driven a conversation nobody drove — a declaration exists to narrow,
// and narrowing is the only direction it may travel.
func (r *Registry) scopeProblems() []string {
	var problems []string
	for _, b := range r.cases {
		scoped, declares := b.factory.(ScopedCase)
		if !declares {
			continue
		}
		declared := scoped.CertifiedScope()
		if !KnownScope(declared) {
			problems = append(problems, fmt.Sprintf(
				"site %s declares the certified scope %q, which is not one a record can report", b.site.key(), declared))
			continue
		}
		if kind := b.site.CertifiedScope(); scopeRank(declared) < scopeRank(kind) {
			problems = append(problems, fmt.Sprintf(
				"site %s declares the certified scope %q, which claims more than its kind's %q — a case may only narrow what its site allows",
				b.site.key(), declared, kind))
		}
	}
	return problems
}

// The things a certification run can actually cover.
const (
	// ScopeFullInvocation: the run drives the whole production invocation, so
	// certifying it certifies the site.
	ScopeFullInvocation = "full_invocation"
	// ScopeSingleTurn: the scenario seeds the window and grades ONE reply. The
	// surrounding conversation or tool loop is supplied, not exercised.
	ScopeSingleTurn = "single_turn"
	// ScopeSingleCall: the run makes ONE of the calls the site's own code makes
	// for one invocation and grades that reply — where the site re-asks a
	// below-floor item solo on the next routing rung, asks again after an
	// unreadable answer, or fans out over pages and folds the replies together.
	// The answer the product serves is assembled from calls the run never made,
	// and the fold that assembles it is unmeasured too.
	//
	// It does NOT mark the model runtime's shape-retry, which is a different
	// thing: every case declines that one for every site, deliberately and
	// identically, because a retried call certifies the answer a model gives
	// after being told to try again rather than the answer it gives. A word true
	// of all nineteen sites would tell a reader nothing about any of them.
	ScopeSingleCall = "single_call"
)

// scopeOrder lists the vocabulary from the most of a production invocation to
// the least, so a word added to it is ordered on the day it is added rather than
// in whichever comparison someone remembers to update.
//
// ScopeSingleTurn sits above ScopeSingleCall because what it leaves out is
// SUPPLIED to the run: the graded turn is the real turn, in the window the
// conversation would have built, and the turns it does not cover are their own
// answers. ScopeSingleCall leaves out calls that are folded INTO the graded
// answer, so what the product serves can be something the record never saw.
var scopeOrder = []string{ScopeFullInvocation, ScopeSingleTurn, ScopeSingleCall}

// KnownScope reports whether scope is one a record can report.
func KnownScope(scope string) bool { return slices.Contains(scopeOrder, scope) }

// scopeRank is a scope's place in the fold: its position in scopeOrder, and past
// the end of it for a word in no vocabulary — an unreadable claim must never be
// the one that widens a record.
func scopeRank(scope string) int {
	if i := slices.Index(scopeOrder, scope); i >= 0 {
		return i
	}
	return len(scopeOrder)
}

// NarrowerScope folds two certified scopes to the less complete of the two, so a
// record pooling several runs claims only what every one of them proved. The
// empty string is the accumulator before its first fold: it carries no claim and
// yields to the other.
func NarrowerScope(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case scopeRank(b) > scopeRank(a):
		return b
	default:
		return a
	}
}

// ScopedCase is the narrowing a certification case may declare when it covers
// LESS of its site than the site's kind implies — a case that sends one request
// where the site can send two, or leaves the site's own gate on the reply
// unspent. A case that declares nothing is read at its kind's scope.
//
// The declaration is the CASE's rather than a field on Site because a site is
// what the contract ships and a scope is what this build's case measures. Those
// are two claims, and folding the second into the first would leave the
// composition's registration and the case's own coverage unable to disagree —
// which is the disagreement Validate exists to report.
type ScopedCase interface {
	CaseFactory
	CertifiedScope() string
}

// ScopeOf reports how much of a site the case bound to it covers: what the case
// declares, or its site's kind-derived scope when it declares nothing.
func ScopeOf(f CaseFactory) string {
	if scoped, ok := f.(ScopedCase); ok {
		return scoped.CertifiedScope()
	}
	return f.Site().CertifiedScope()
}

// Scopes reports the certified scope of every bound case, keyed by site. A
// reader that has only the census — the readiness report, before any record
// exists — needs it to say what the MOST a run could cover is, and the site
// alone no longer answers that.
func (r *Registry) Scopes() map[string]string {
	out := make(map[string]string, len(r.cases))
	for key, b := range r.cases {
		out[key] = ScopeOf(b.factory)
	}
	return out
}

// CertifiedScope reports how much of this site a run covers when its case
// declares nothing.
//
// A one-shot site's whole invocation IS one request, so a scored request is a
// scored site. A multi-turn or agent-loop site is different in kind: its
// committed scenarios seed the prior turns (an agent scenario supplies the tool
// result as context) and grade the single reply that follows. That is a real
// measurement — it is what the shipped prompt does with that window — but it is
// not the loop, and a record that does not distinguish them claims more than it
// tested.
//
// The kind is a floor, not the answer: it says what a site of this shape could
// be covered to, and a case that measures less than its whole path says so
// itself through ScopedCase.
func (s Site) CertifiedScope() string {
	if s.Kind == ai.SiteKindOneShot {
		return ScopeFullInvocation
	}
	return ScopeSingleTurn
}
