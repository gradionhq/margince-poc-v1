// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The quick site read: the given page plus the well-known legal-notice page,
// merged so legal facts come from the page that legally states them. German
// sites keep legal_name/VAT/address on the Impressum — a read that never
// leaves the landing page cannot ground them.

import (
	"context"
	"errors"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// hostPages serves a distinct fixture per URL — the multi-page twin of
// fixturePage. Unknown paths 404 like a real site.
type hostPages map[string]string

func (p hostPages) Fetch(_ context.Context, rawURL string) (string, error) {
	if text, ok := p[rawURL]; ok {
		return text, nil
	}
	return "", errNotFound
}

var errNotFound = errors.New("fixture: no such page")

func TestExtractReadsTheImpressumForLegalFacts(t *testing.T) {
	home := strings.Repeat("Acme builds robots. ", 10) + "Built for RevOps leaders."
	impressum := strings.Repeat("Impressum. ", 10) + "Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart. USt-IdNr. DE811234567."

	fetch := hostPages{
		"https://acme.example":           home,
		"https://acme.example/impressum": impressum,
	}
	// Call order is deterministic (seed first, then the found probe), so the
	// scripted queue addresses each page's extraction in turn.
	fake := ai.NewFakeClient().Script(
		`{"fields":[
			{"field":"icp","value":"RevOps leaders","evidence_snippet":"Built for RevOps leaders","confidence":0.8},
			{"field":"legal_name","value":"Acme (guessed)","evidence_snippet":"Acme builds robots.","confidence":0.95}]}`,
		`{"fields":[
			{"field":"legal_name","value":"Acme Robotics GmbH","evidence_snippet":"Acme Robotics GmbH","confidence":0.7},
			{"field":"register_vat","value":"DE811234567","evidence_snippet":"USt-IdNr. DE811234567","confidence":0.9}]}`,
	)
	x := evidenceExtractor{fetch: fetch, brain: fakeModelPath(t, fake).ColdStart}

	fields, err := x.extract(fakeWorkspaceCtx(), "https://acme.example", coldStartFieldValid)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]evidencedField{}
	for _, f := range fields {
		byName[f.Field] = f
	}
	// The Impressum wins the legal facts even at LOWER model confidence: the
	// page that legally states a fact outranks a landing-page guess.
	legal := byName[string(crmcontracts.LegalName)]
	if legal.Value != "Acme Robotics GmbH" || legal.SourceURL != "https://acme.example/impressum" {
		t.Fatalf("legal_name = %q from %s, want the Impressum's value", legal.Value, legal.SourceURL)
	}
	// A fact only the Impressum grounds arrives with the Impressum's URL.
	if vat := byName[string(crmcontracts.RegisterVat)]; vat.Value != "DE811234567" || vat.SourceURL != "https://acme.example/impressum" {
		t.Fatalf("register_vat = %q from %s, want the Impressum's value WITH its provenance", vat.Value, vat.SourceURL)
	}
	// A positioning fact stays the landing page's.
	if icp := byName[string(crmcontracts.Icp)]; icp.SourceURL != "https://acme.example" {
		t.Fatalf("icp came from %s, want the landing page", icp.SourceURL)
	}
}

func TestExtractWithoutAnImpressumStaysASinglePageRead(t *testing.T) {
	home := strings.Repeat("Acme builds robots. ", 10)
	fetch := hostPages{"https://acme.example": home}
	fake := ai.NewFakeClient().Script(
		`{"fields":[{"field":"icp","value":"x","evidence_snippet":"Acme builds robots.","confidence":0.8}]}`,
	)
	x := evidenceExtractor{fetch: fetch, brain: fakeModelPath(t, fake).ColdStart}

	fields, err := x.extract(fakeWorkspaceCtx(), "https://acme.example", coldStartFieldValid)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || len(fake.Calls()) != 1 {
		t.Fatalf("fields=%d model calls=%d, want 1/1 — a missing legal page must not cost a second call", len(fields), len(fake.Calls()))
	}
}

func TestExtractSkipsAProbeServingTheSamePage(t *testing.T) {
	// SPA catch-alls answer every path with the landing page; extracting that
	// twice would double the model spend for zero new evidence.
	home := strings.Repeat("Acme builds robots. ", 10)
	fetch := hostPages{
		"https://acme.example":           home,
		"https://acme.example/impressum": home,
	}
	fake := ai.NewFakeClient().Script(
		`{"fields":[{"field":"icp","value":"x","evidence_snippet":"Acme builds robots.","confidence":0.8}]}`,
	)
	x := evidenceExtractor{fetch: fetch, brain: fakeModelPath(t, fake).ColdStart}

	if _, err := x.extract(fakeWorkspaceCtx(), "https://acme.example", coldStartFieldValid); err != nil {
		t.Fatal(err)
	}
	if calls := len(fake.Calls()); calls != 1 {
		t.Fatalf("model called %d times, want 1 — the duplicate page must be skipped", calls)
	}
}

func TestExtractOffersDisplayNameToTheModel(t *testing.T) {
	// The most obvious form field must be fillable by the read-back: the
	// vocabulary handed to the model (and its schema enum) includes
	// display_name, and a grounded one survives the gate.
	home := strings.Repeat("Filler text. ", 10) + "Acme — robots for the mid-market."
	fetch := hostPages{"https://acme.example": home}
	fake := ai.NewFakeClient().Script(
		`{"fields":[{"field":"display_name","value":"Acme","evidence_snippet":"Acme — robots for the mid-market.","confidence":0.9}]}`,
	)
	x := evidenceExtractor{fetch: fetch, brain: fakeModelPath(t, fake).ColdStart}

	fields, err := x.extract(fakeWorkspaceCtx(), "https://acme.example", coldStartFieldValid)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || fields[0].Field != string(crmcontracts.DisplayName) {
		t.Fatalf("fields = %+v, want the grounded display_name", fields)
	}
	if !strings.Contains(string(fake.Calls()[0].Payload), "display_name") {
		t.Fatal("the extraction prompt never offered display_name to the model")
	}
}

// failsAfter yields scripted responses, then errors: the legal-page extraction
// failing must surface, not silently reduce the read to one page.
type failsAfter struct {
	inner *ai.FakeClient
	calls int
	limit int
}

func (f *failsAfter) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	f.calls++
	if f.calls > f.limit {
		return model.Response{}, errors.New("model lane down")
	}
	return f.inner.Complete(ctx, req)
}

func TestExtractSurfacesALegalPageModelFailure(t *testing.T) {
	home := strings.Repeat("Acme builds robots. ", 10)
	impressum := strings.Repeat("Impressum. ", 10) + "Acme Robotics GmbH."
	fetch := hostPages{
		"https://acme.example":           home,
		"https://acme.example/impressum": impressum,
	}
	// failsAfter injects a per-call failure the router's fake wiring has no
	// seam for (ai.WithFakeClient hands the router a *FakeClient directly,
	// not an arbitrary completer): this test's subject is evidenceExtractor's
	// OWN error propagation, not routing, so it stays directly on the fake.
	brain := &failsAfter{
		inner: ai.NewFakeClient().Script(
			`{"fields":[{"field":"icp","value":"x","evidence_snippet":"Acme builds robots.","confidence":0.8}]}`,
		),
		limit: 1,
	}

	x := evidenceExtractor{fetch: fetch, brain: brain}
	if _, err := x.extract(context.Background(), "https://acme.example", coldStartFieldValid); err == nil {
		t.Fatal("a failed legal-page extraction was silently swallowed — the read reported success on half its input")
	}
}

func TestExtractNeverProbesFromAPathHostedSeed(t *testing.T) {
	// On a path-hosted site (sites.example.com/company/) the HOST ROOT's
	// /impressum belongs to a different party — and the merge's legal-page
	// preference would let whoever controls it override the company's legal
	// identity. A non-root seed therefore reads single-page.
	seed := "https://sites.example/company/"
	fetch := hostPages{
		seed: strings.Repeat("Company page text. ", 10),
		// The attacker-controlled root impressum: reachable, never fetched.
		"https://sites.example/impressum": strings.Repeat("Evil Corp Ltd. ", 10),
	}
	fake := ai.NewFakeClient().Script(
		`{"fields":[{"field":"icp","value":"x","evidence_snippet":"Company page text.","confidence":0.8}]}`,
	)
	x := evidenceExtractor{fetch: fetch, brain: fakeModelPath(t, fake).ColdStart}

	if _, err := x.extract(fakeWorkspaceCtx(), seed, coldStartFieldValid); err != nil {
		t.Fatal(err)
	}
	if calls := len(fake.Calls()); calls != 1 {
		t.Fatalf("model called %d times for a path-hosted seed, want 1 — the root probe must not fire", calls)
	}
}
