// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Specs for qualify_lead's A15 contract: fill ONLY empty fields whose
// value is deterministically inferable (with the evidence that grounds
// it), report everything else as a gap — never overwrite, never guess.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// fakeSoR fakes the true provider boundary for the intent-tool specs:
// canned reads, recorded writes. Verbs a test never exercises stay on
// the embedded nil interface and would fail loudly if reached.
type fakeSoR struct {
	datasource.SystemOfRecordProvider
	records   map[datasource.EntityRef]datasource.Record
	updates   []datasource.UpdateInput
	creates   []datasource.CreateInput
	advances  []datasource.AdvanceDealInput
	createRef datasource.EntityRef
}

func (f *fakeSoR) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	rec, ok := f.records[ref]
	if !ok {
		return datasource.Record{}, apperrors.ErrNotFound
	}
	return rec, nil
}

func (f *fakeSoR) Update(_ context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	f.updates = append(f.updates, in)
	return in.Ref, nil
}

func (f *fakeSoR) Create(_ context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	f.creates = append(f.creates, in)
	return f.createRef, nil
}

func (f *fakeSoR) AdvanceDeal(_ context.Context, in datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	f.advances = append(f.advances, in)
	return datasource.EntityRef{Type: datasource.EntityDeal, ID: in.DealID}, nil
}

func leadFixture(t *testing.T, id ids.UUID, fields string, version int64) *fakeSoR {
	t.Helper()
	ref := datasource.EntityRef{Type: datasource.EntityLead, ID: id}
	return &fakeSoR{records: map[datasource.EntityRef]datasource.Record{
		ref: {Ref: ref, Fields: json.RawMessage(fields), Version: version},
	}}
}

type qualifyWire struct {
	RecordID string `json:"record_id"`
	Filled   map[string]struct {
		Value    string `json:"value"`
		Evidence []struct {
			Source  string `json:"source"`
			Snippet string `json:"snippet"`
		} `json:"evidence"`
	} `json:"filled"`
	Gaps []string `json:"gaps"`
}

func qualify(t *testing.T, p *fakeSoR, id ids.UUID) qualifyWire {
	t.Helper()
	raw, err := qualifyLead{p: p}.Handle(context.Background(),
		json.RawMessage(`{"record_id":"`+id.String()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out qualifyWire
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestQualifyLeadFillsOnlyInferableEmptyFieldsWithEvidence(t *testing.T) {
	leadID := ids.NewV7()
	p := leadFixture(t, leadID,
		`{"email":"jane@acme-corp.io","full_name":"Jane Doe","company_name":"","title":null,"source":"webform"}`, 3)

	out := qualify(t, p, leadID)

	company, ok := out.Filled["company_name"]
	if !ok || company.Value != "Acme Corp" {
		t.Fatalf("filled = %+v, want company_name \"Acme Corp\" inferred from the email domain", out.Filled)
	}
	if len(company.Evidence) != 1 || company.Evidence[0].Source != "lead.email" || company.Evidence[0].Snippet != "jane@acme-corp.io" {
		t.Fatalf("company_name evidence = %+v, want the grounding email", company.Evidence)
	}
	if len(out.Filled) != 1 {
		t.Fatalf("filled %d fields, want exactly the one inferable gap: %+v", len(out.Filled), out.Filled)
	}
	if len(out.Gaps) != 1 || out.Gaps[0] != "title" {
		t.Fatalf("gaps = %v, want the one still-empty qualification field [title]", out.Gaps)
	}

	if len(p.updates) != 1 {
		t.Fatalf("provider saw %d updates, want 1", len(p.updates))
	}
	patchRaw, err := datasource.RawFields(p.updates[0].Patch)
	if err != nil {
		t.Fatal(err)
	}
	var patch map[string]string
	if err := json.Unmarshal(patchRaw, &patch); err != nil {
		t.Fatal(err)
	}
	if len(patch) != 1 || patch["company_name"] != "Acme Corp" {
		t.Fatalf("patch = %v — a fill-empty-only patch carries nothing but the filled field", patch)
	}
	if p.updates[0].IfVersion == nil || *p.updates[0].IfVersion != 3 {
		t.Fatalf("IfVersion = %v, want the version the fill was decided on (3)", p.updates[0].IfVersion)
	}
	if p.updates[0].Source != toolSource {
		t.Fatalf("update source = %q, want the tool surface's provenance channel %q", p.updates[0].Source, toolSource)
	}
}

func TestQualifyLeadReportsGapsInsteadOfGuessing(t *testing.T) {
	// A freemail domain names a mailbox host, not a company: nothing is
	// inferable, so nothing is written and the gap is surfaced.
	leadID := ids.NewV7()
	p := leadFixture(t, leadID,
		`{"email":"jane@gmail.com","full_name":"","company_name":"","title":"","source":"import"}`, 1)

	out := qualify(t, p, leadID)

	if len(out.Filled) != 0 {
		t.Fatalf("filled = %+v, want nothing — a freemail domain grounds no company", out.Filled)
	}
	if len(p.updates) != 0 {
		t.Fatalf("provider saw %d updates, want none when there is nothing evidenced to fill", len(p.updates))
	}
	want := []string{"full_name", "company_name", "title"}
	if len(out.Gaps) != len(want) {
		t.Fatalf("gaps = %v, want %v", out.Gaps, want)
	}
	for i, g := range want {
		if out.Gaps[i] != g {
			t.Fatalf("gaps = %v, want %v in the fixed qualification order", out.Gaps, want)
		}
	}
}

func TestQualifyLeadNeverOverwritesAnExistingValue(t *testing.T) {
	leadID := ids.NewV7()
	p := leadFixture(t, leadID,
		`{"email":"jane@acme-corp.io","full_name":"Jane Doe","company_name":"ACME Corporation GmbH","title":"CFO","source":"webform"}`, 7)

	out := qualify(t, p, leadID)

	if len(out.Filled) != 0 || len(p.updates) != 0 {
		t.Fatalf("filled=%+v updates=%d — a populated field is never touched, whatever the email would infer", out.Filled, len(p.updates))
	}
	if len(out.Gaps) != 0 {
		t.Fatalf("gaps = %v, want none for a fully qualified lead", out.Gaps)
	}
}
