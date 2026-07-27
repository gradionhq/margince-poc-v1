// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aitasks_test

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestRegistryRefusesASiteTheContractDoesNotDeclare(t *testing.T) {
	r := aitasks.NewRegistry()
	r.Register(aitasks.Site{Task: ai.TaskRateExtract, Variant: "invented", Kind: ai.SiteKindOneShot})
	err := r.Validate()
	if err == nil {
		t.Fatal("an undeclared site validated; the contract is the authority on what exists")
	}
	if !strings.Contains(err.Error(), "invented") {
		t.Errorf("error does not name the offending site: %v", err)
	}
}

func TestRegistryRefusesADuplicateSite(t *testing.T) {
	r := aitasks.NewRegistry()
	site := aitasks.Site{Task: ai.TaskRateExtract, Variant: "pricing", Kind: ai.SiteKindOneShot}
	r.Register(site)
	r.Register(site)
	if err := r.Validate(); err == nil {
		t.Fatal("a duplicate registration validated; two implementations of one site is a wiring defect")
	}
}

func TestRegistryRefusesAMismatchedKind(t *testing.T) {
	r := aitasks.NewRegistry()
	r.Register(aitasks.Site{Task: ai.TaskAgentLoop, Variant: "loop", Kind: ai.SiteKindOneShot})
	if err := r.Validate(); err == nil {
		t.Fatal("a site registered with the wrong kind validated; an agent loop is not a request factory")
	}
}

func TestRegistryRefusesASiteOnAPlannedTask(t *testing.T) {
	r := aitasks.NewRegistry()
	r.Register(aitasks.Site{Task: ai.TaskSummarize, Variant: "summary", Kind: ai.SiteKindOneShot})
	if err := r.Validate(); err == nil {
		t.Fatal("a planned task accepted a site; planned means no implementation")
	}
}

func TestRegistryRefusesAnIncompleteShippedTask(t *testing.T) {
	r := aitasks.NewRegistry()
	// rate_extract declares two sites; register only one.
	r.Register(aitasks.Site{Task: ai.TaskRateExtract, Variant: "pricing", Kind: ai.SiteKindOneShot})
	err := r.Validate()
	if err == nil {
		t.Fatal("a partly-registered shipped task validated")
	}
	if !strings.Contains(err.Error(), "fx") {
		t.Errorf("error does not name the unregistered site: %v", err)
	}
}

// Every shipped site owes a certification case. A site nobody can certify is a
// site whose record could only ever be a claim about a hand-written prompt, so
// the missing binding is named at the same place the missing site is.
func TestRegistryRefusesAShippedSiteWithNoCase(t *testing.T) {
	r := aitasks.NewRegistry()
	pricing := aitasks.Site{Task: ai.TaskRateExtract, Variant: "pricing", Kind: ai.SiteKindOneShot}
	fx := aitasks.Site{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot}
	r.Register(pricing)
	r.Register(fx)
	r.BindCase(stubCase{site: pricing})

	err := r.Validate()
	if err == nil {
		t.Fatal("a shipped site with no certification case validated")
	}
	if !strings.Contains(err.Error(), "rate_extract/fx") {
		t.Errorf("the error does not name the uncertifiable site: %v", err)
	}
	if strings.Contains(err.Error(), "rate_extract/pricing") {
		t.Errorf("the error complains about a site that has a case: %v", err)
	}
}

// A site the contract does not admit — a planned task's, or one it never
// declared — is answered by the problem that names THAT defect. Demanding a
// case for it as well would point at the wrong fix: the registration goes, the
// case was never owed.
func TestRegistryDoesNotDemandACaseForASiteTheContractDeniesExists(t *testing.T) {
	for _, tc := range []struct {
		name string
		site aitasks.Site
	}{
		{"planned task", aitasks.Site{Task: ai.TaskSummarize, Variant: "summary", Kind: ai.SiteKindOneShot}},
		{"undeclared variant", aitasks.Site{Task: ai.TaskRateExtract, Variant: "invented", Kind: ai.SiteKindOneShot}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := aitasks.NewRegistry()
			r.Register(tc.site)
			err := r.Validate()
			if err == nil {
				t.Fatal("a site the contract does not declare validated")
			}
			if strings.Contains(err.Error(), "no certification case is bound") {
				t.Errorf("the error asks for a case the site was never owed: %v", err)
			}
		})
	}
}

func TestLookupFindsARegisteredSite(t *testing.T) {
	r := aitasks.NewRegistry()
	want := aitasks.Site{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot}
	r.Register(want)
	got, ok := r.Lookup(ai.TaskRateExtract, "fx")
	if !ok || got != want {
		t.Fatalf("Lookup = %+v, %t; want %+v, true", got, ok, want)
	}
	if _, ok := r.Lookup(ai.TaskRateExtract, "pricing"); ok {
		t.Error("Lookup found a site that was never registered")
	}
}

// An agent loop's committed scenarios seed a window and grade ONE turn; the
// loop itself is never exercised. A record that does not say so overstates
// what was certified, so the scope is derived from the kind rather than left
// to a reader to infer.
func TestAgentLoopCertifiesOneTurnNotTheLoop(t *testing.T) {
	loop := aitasks.Site{Task: ai.TaskAgentLoop, Variant: "loop", Kind: ai.SiteKindAgentLoop}
	if got := loop.CertifiedScope(); got != aitasks.ScopeSingleTurn {
		t.Errorf("CertifiedScope() = %q, want %q — a seeded-window turn is not a loop run", got, aitasks.ScopeSingleTurn)
	}

	oneShot := aitasks.Site{Task: ai.TaskRateExtract, Variant: "fx", Kind: ai.SiteKindOneShot}
	if got := oneShot.CertifiedScope(); got != aitasks.ScopeFullInvocation {
		t.Errorf("CertifiedScope() = %q, want %q — a one-shot site's whole invocation is one request", got, aitasks.ScopeFullInvocation)
	}

	multi := aitasks.Site{Task: ai.TaskColdStart, Variant: "acts", Kind: ai.SiteKindMultiTurn}
	if got := multi.CertifiedScope(); got != aitasks.ScopeSingleTurn {
		t.Errorf("CertifiedScope() = %q, want %q — replayed history still grades one reply", got, aitasks.ScopeSingleTurn)
	}
}
