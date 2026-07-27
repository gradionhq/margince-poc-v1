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
