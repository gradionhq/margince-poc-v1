// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The synthesis pass's contract: it may correct or fill company fields
// ONLY with evidence a shown page actually carries, and any failure
// degrades to the per-page merge — never to a lost read.

import (
	"context"
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func synthesisFixturePages() []crawlPage {
	return []crawlPage{
		{
			URL: seedURL, Kind: crmcontracts.SiteReadPageKindHome,
			Text: "Acme ships robots. Trusted by manufacturers across Europe.",
		},
		{
			URL: seedURL + "/impressum", Kind: crmcontracts.SiteReadPageKindImpressum,
			Text: "Impressum. Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart.",
		},
	}
}

func TestSynthesisCorrectsAndFillsOnlyWithOnPageEvidence(t *testing.T) {
	merged := []evidencedField{
		{Field: "legal_name", Value: "Acme (guessed)", EvidenceSnippet: "Acme ships robots", SourceURL: seedURL, Confidence: 0.9},
	}
	// One correction with real Impressum evidence, one fill with real
	// evidence, one fill whose snippet no shown page carries.
	brain := ai.NewFakeClient().Script(`{"fields":[
		{"field":"legal_name","value":"Acme Robotics GmbH","evidence_snippet":"Acme Robotics GmbH","source_url":"` + seedURL + `/impressum","confidence":0.9},
		{"field":"registered_address","value":"Werkstr. 1, 70435 Stuttgart","evidence_snippet":"Werkstr. 1, 70435 Stuttgart","source_url":"` + seedURL + `/impressum","confidence":0.85},
		{"field":"history","value":"Founded 1998","evidence_snippet":"since 1998 a family business","source_url":"` + seedURL + `","confidence":0.9}]}`)

	var dropped []droppedFinding
	x := evidenceExtractor{brain: brain, drops: func(_ string, d droppedFinding) { dropped = append(dropped, d) }}
	got := synthesizeSiteFields(context.Background(), x, synthesisFixturePages(), merged, false)

	byField := map[string]evidencedField{}
	for _, f := range got {
		byField[f.Field] = f
	}
	if byField["legal_name"].Value != "Acme Robotics GmbH" || byField["legal_name"].SourceURL != seedURL+"/impressum" {
		t.Fatalf("the evidenced correction did not replace the guess: %+v", byField["legal_name"])
	}
	if byField["registered_address"].Value != "Werkstr. 1, 70435 Stuttgart" {
		t.Fatalf("the evidenced fill is missing: %+v", got)
	}
	if _, leaked := byField["history"]; leaked {
		t.Fatalf("an unevidenced synthesis claim survived: %+v", byField["history"])
	}
	if len(dropped) != 1 || dropped[0].Reason != dropEvidenceNotOnPage || dropped[0].Lane != "synthesis" {
		t.Fatalf("the unevidenced claim left no drop record: %+v", dropped)
	}
}

// failingBrain errors on every call — the degrade path's stand-in.
type failingBrain struct{}

func (failingBrain) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("provider down")
}

func TestSynthesisFailureKeepsThePerPageMerge(t *testing.T) {
	merged := []evidencedField{
		{Field: "legal_name", Value: "Acme Robotics GmbH", EvidenceSnippet: "Acme Robotics GmbH", SourceURL: seedURL + "/impressum", Confidence: 0.7},
	}
	got := synthesizeSiteFields(context.Background(), evidenceExtractor{brain: failingBrain{}}, synthesisFixturePages(), merged, false)
	if len(got) != 1 || got[0].Value != "Acme Robotics GmbH" {
		t.Fatalf("a synthesis failure cost the read its merged fields: %+v", got)
	}
}

func TestSynthesisUnderALegalConflictCannotReintroduceTheLegalTrio(t *testing.T) {
	merged := []evidencedField{
		{Field: "display_name", Value: "Acme", EvidenceSnippet: "Acme ships robots", SourceURL: seedURL, Confidence: 0.9},
	}
	// The reply corrects display_name (allowed) and tries to fill
	// legal_name with perfectly valid on-page evidence — which the
	// multi-entity conflict must still refuse.
	brain := ai.NewFakeClient().Script(`{"fields":[
		{"field":"display_name","value":"Acme Robotics","evidence_snippet":"Acme Robotics GmbH","source_url":"` + seedURL + `/impressum","confidence":0.9},
		{"field":"legal_name","value":"Acme Robotics GmbH","evidence_snippet":"Acme Robotics GmbH","source_url":"` + seedURL + `/impressum","confidence":0.9}]}`)

	var dropped []droppedFinding
	x := evidenceExtractor{brain: brain, drops: func(_ string, d droppedFinding) { dropped = append(dropped, d) }}
	got := synthesizeSiteFields(context.Background(), x, synthesisFixturePages(), merged, true)

	for _, f := range got {
		if f.Field == "legal_name" {
			t.Fatalf("the legal conflict was overridden by synthesis: %+v", f)
		}
	}
	if len(got) != 1 || got[0].Value != "Acme Robotics" {
		t.Fatalf("the non-legal correction should still apply: %+v", got)
	}
	var conflictDrop bool
	for _, d := range dropped {
		if d.Field == "legal_name" && d.Reason == dropLegalConflict {
			conflictDrop = true
		}
	}
	if !conflictDrop {
		t.Fatalf("the refused legal_name left no legal_conflict drop record: %+v", dropped)
	}
}

func TestSynthesisSkipsWhenNothingWasExtracted(t *testing.T) {
	// An empty merge means the per-page gate evidenced nothing; the
	// synthesis call must not run and become a second extraction pass.
	brain := ai.NewFakeClient()
	got := synthesizeSiteFields(context.Background(), evidenceExtractor{brain: brain}, synthesisFixturePages(), nil, false)
	if got != nil {
		t.Fatalf("synthesis over an empty merge returned %+v", got)
	}
	if calls := brain.Calls(); len(calls) != 0 {
		t.Fatalf("synthesis called the model over an empty merge: %d calls", len(calls))
	}
}
