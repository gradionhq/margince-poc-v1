// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The cold-start read-back: the no-guess gate drops everything the
// model cannot evidence VERBATIM from the fetched page, the surviving
// fields stage a 🟡 approval (nothing touches real records), and the
// decision echoes the coldstart lifecycle events.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type fixturePage string

func (p fixturePage) Fetch(context.Context, string) (string, error) { return string(p), nil }

const acmePage = fixturePage(`Acme GmbH — Onboard your team in minutes, not weeks. ` +
	`Built for RevOps leaders at scaling SaaS companies. ` +
	`Registered in Berlin, HRB 12345.`)

func TestColdStartStagesOnlyEvidencedFields(t *testing.T) {
	e := integration.Setup(t)
	brain := ai.NewFakeClient().Script(`{"fields":[
		{"field":"value_proposition","value":"Fast onboarding","evidence_snippet":"Onboard your team in minutes, not weeks","confidence":0.9},
		{"field":"icp","value":"RevOps at SaaS scale-ups","evidence_snippet":"Built for RevOps leaders at scaling SaaS companies","confidence":0.7},
		{"field":"legal_name","value":"Acme GmbH","evidence_snippet":"this text is NOT on the page","confidence":0.9},
		{"field":"industry","value":"Software","evidence_snippet":"Acme GmbH","confidence":1.7},
		{"field":"made_up_field","value":"x","evidence_snippet":"Acme GmbH","confidence":0.5}]}`)
	engine := &coldStartEngine{extract: evidenceExtractor{fetch: acmePage, brain: brain}, approvals: approvals.NewService(e.Pool)}

	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.SchedulerPerms)
	proposal, err := engine.Propose(ctx, "https://acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Fields) != 2 {
		t.Fatalf("no-guess gate let %d fields through, want 2 (hallucinated evidence, bad confidence and unknown names must drop): %+v",
			len(proposal.Fields), proposal.Fields)
	}
	if proposal.Status != "staged" || proposal.ProposalId.String() == ids.Nil.String() {
		t.Fatalf("proposal not staged: %+v", proposal)
	}

	// The staged approval row is the proposal; the staging emitted both
	// the approval.requested and the coldstart lifecycle event.
	var kind, status string
	var eventCount int
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT kind, status FROM approval WHERE id = $1`, ids.From[ids.ApprovalKind](ids.UUID(proposal.ProposalId))).Scan(&kind, &status); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM event_outbox WHERE envelope->>'type' IN ('approval.requested', 'coldstart.read_back_proposed')`).Scan(&eventCount)
	})
	if err != nil {
		t.Fatal(err)
	}
	if kind != "coldstart" || status != "pending" || eventCount != 2 {
		t.Fatalf("staging landed as kind=%s status=%s events=%d, want coldstart/pending/2", kind, status, eventCount)
	}

	// Accepting needs organization.update (the effect the proposal
	// writes on acceptance) — the admin has it; the decision echoes
	// coldstart.accepted.
	svc := approvals.NewService(e.Pool)
	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](ids.UUID(proposal.ProposalId)), true, nil); err != nil {
		t.Fatalf("accepting the proposal: %v", err)
	}
	var accepted int
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'coldstart.accepted'`).Scan(&accepted)
	})
	if err != nil || accepted != 1 {
		t.Fatalf("coldstart.accepted events = %d (%v), want 1", accepted, err)
	}
}

// The paste-text fallback (B-E01.2b): the same model+gate seam as the url
// path, grounded in the pasted text itself — source_kind=text, no source_url,
// and the char offset of each evidence snippet for highlight-back. Evidence
// the paste does not contain is dropped, and the result stages 🟡 exactly
// like a url read-back.
func TestColdStartTextInputGroundsFieldsInThePaste(t *testing.T) {
	e := integration.Setup(t)
	pasted := "Acme GmbH helps RevOps teams onboard in minutes. Built for scaling B2B SaaS companies."
	brain := ai.NewFakeClient().Script(`{"fields":[
		{"field":"value_proposition","value":"Fast onboarding","evidence_snippet":"onboard in minutes","confidence":0.8},
		{"field":"icp","value":"Scaling B2B SaaS","evidence_snippet":"scaling B2B SaaS companies","confidence":0.7},
		{"field":"legal_name","value":"Acme GmbH","evidence_snippet":"registered as Acme GmbH in Berlin","confidence":0.9}]}`)
	engine := &coldStartEngine{extract: evidenceExtractor{brain: brain}, approvals: approvals.NewService(e.Pool)}

	proposal, err := engine.ProposeText(e.As(e.Rep1, []ids.UUID{e.Team1}, integration.SchedulerPerms), pasted)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.SourceKind != crmcontracts.ColdStartProposalSourceKindText || proposal.SourceUrl != nil {
		t.Fatalf("proposal source = %s/%v, want text with no source_url", proposal.SourceKind, proposal.SourceUrl)
	}
	if proposal.Status != "staged" || proposal.ProposalId.String() == ids.Nil.String() {
		t.Fatalf("text proposal not staged: %+v", proposal)
	}
	if len(proposal.Fields) != 2 {
		t.Fatalf("gate kept %d fields, want 2 (evidence not in the paste must drop): %+v", len(proposal.Fields), proposal.Fields)
	}
	for _, f := range proposal.Fields {
		if f.SourceKind != crmcontracts.ColdStartFieldSourceKindText || f.SourceUrl != nil {
			t.Fatalf("field %s source = %s/%v, want text with no source_url", f.Field, f.SourceKind, f.SourceUrl)
		}
		if f.EvidenceOffset == nil {
			t.Fatalf("field %s carries no evidence_offset, want the snippet's char position", f.Field)
		}
		if want := strings.Index(pasted, f.EvidenceSnippet); *f.EvidenceOffset != want {
			t.Fatalf("field %s evidence_offset = %d, want %d", f.Field, *f.EvidenceOffset, want)
		}
	}
}

// The self-description input (B-E01.13): a field is grounded in the user's
// own statement — its evidence IS that statement's words — and a field the
// statement does not support is ABSENT (the no-guess gate). No fetch, no
// source_url, and it stages 🟡 like every other kind.
func TestColdStartSelfDescriptionGroundsOnlyWhatTheStatementSupports(t *testing.T) {
	e := integration.Setup(t)
	statement := "We sell fractional CFO services to seed-stage German startups."
	brain := ai.NewFakeClient().Script(`{"fields":[
		{"field":"icp","value":"Seed-stage German startups","evidence_snippet":"seed-stage German startups","confidence":0.8},
		{"field":"value_proposition","value":"Fractional CFO services","evidence_snippet":"We sell fractional CFO services","confidence":0.9},
		{"field":"industry","value":"Financial consulting","evidence_snippet":"a leading financial consultancy","confidence":0.9}]}`)
	engine := &coldStartEngine{extract: evidenceExtractor{brain: brain}, approvals: approvals.NewService(e.Pool)}
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.SchedulerPerms)

	proposal, err := engine.ProposeSelfDescription(ctx, statement)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.SourceKind != crmcontracts.ColdStartProposalSourceKindSelfDescription || proposal.SourceUrl != nil {
		t.Fatalf("proposal source = %s/%v, want self_description with no source_url", proposal.SourceKind, proposal.SourceUrl)
	}
	if proposal.Status != "staged" || proposal.ProposalId.String() == ids.Nil.String() {
		t.Fatalf("self-description proposal not staged: %+v", proposal)
	}
	// icp and value_proposition are the user's own words; the industry claim
	// is not in the statement — absent, never fabricated.
	if len(proposal.Fields) != 2 {
		t.Fatalf("gate kept %d fields, want 2 (the unsupported industry must be absent): %+v", len(proposal.Fields), proposal.Fields)
	}
	for _, f := range proposal.Fields {
		if f.SourceKind != crmcontracts.ColdStartFieldSourceKindSelfDescription || f.SourceUrl != nil {
			t.Fatalf("field %s source = %s/%v, want self_description with no source_url", f.Field, f.SourceKind, f.SourceUrl)
		}
		if !strings.Contains(statement, f.EvidenceSnippet) {
			t.Fatalf("field %s evidence %q is not the user's own words", f.Field, f.EvidenceSnippet)
		}
	}

	// A statement that supports nothing yields zero fields — refused, not
	// padded (the same honest degradation as an unreadable page).
	unsupported := ai.NewFakeClient().Script(`{"fields":[
		{"field":"icp","value":"guessed","evidence_snippet":"enterprise Fortune-500 buyers","confidence":0.9}]}`)
	empty := &coldStartEngine{extract: evidenceExtractor{brain: unsupported}, approvals: approvals.NewService(e.Pool)}
	var unreadable *unreadableError
	if _, err := empty.ProposeSelfDescription(ctx, statement); !errors.As(err, &unreadable) {
		t.Fatalf("unsupported statement → %v, want unreadable (no-guess)", err)
	}
}

func TestColdStartRefusesWhenNothingSurvivesTheGate(t *testing.T) {
	e := integration.Setup(t)
	brain := ai.NewFakeClient().Script(
		`{"fields":[{"field":"icp","value":"guessed","evidence_snippet":"nowhere on the page","confidence":0.9}]}`,
		`not even JSON`)
	engine := &coldStartEngine{extract: evidenceExtractor{fetch: acmePage, brain: brain}, approvals: approvals.NewService(e.Pool)}
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.SchedulerPerms)

	var unreadable *unreadableError
	if _, err := engine.Propose(ctx, "https://acme.example"); !errors.As(err, &unreadable) {
		t.Fatalf("all-hallucinated extraction → %v, want unreadable", err)
	}
	if _, err := engine.Propose(ctx, "https://acme.example"); !errors.As(err, &unreadable) {
		t.Fatalf("unparseable model output → %v, want unreadable", err)
	}
	// A page below the readable floor never reaches the model.
	tiny := &coldStartEngine{extract: evidenceExtractor{fetch: fixturePage("hi"), brain: brain}, approvals: approvals.NewService(e.Pool)}
	if _, err := tiny.Propose(ctx, "https://acme.example"); !errors.As(err, &unreadable) {
		t.Fatalf("tiny page → %v, want unreadable", err)
	}
}

// The ACCEPT executor (features/07 §1): a human approval WRITES the
// accepted fields — org resolved/created by the source domain, empty
// columns filled, evidence rows landed, human-set values untouched,
// exactly once even if the decision path re-fires.
func TestColdStartAcceptWritesProfileOntoOrganization(t *testing.T) {
	e := integration.Setup(t)
	extraction := `{"fields":[
		{"field":"legal_name","value":"Acme GmbH","evidence_snippet":"Acme GmbH","confidence":0.95},
		{"field":"industry","value":"SaaS tooling","evidence_snippet":"scaling SaaS companies","confidence":0.6},
		{"field":"icp","value":"RevOps at SaaS scale-ups","evidence_snippet":"Built for RevOps leaders at scaling SaaS companies","confidence":0.7}]}`
	brain := ai.NewFakeClient().Script(extraction, extraction)

	// The org already exists with a HUMAN-set industry: acceptance may
	// fill what is empty, never overwrite a human's value.
	admin := e.Admin()
	orgID := seedAcmeOrgWithHumanIndustry(t, e, admin)

	svc := approvals.NewService(e.Pool)
	svc.WithEffect("coldstart", coldstartAcceptEffect(svc, people.NewStore(e.Pool)))
	engine := &coldStartEngine{extract: evidenceExtractor{fetch: acmePage, brain: brain}, approvals: svc}

	proposal, err := engine.Propose(e.As(e.Rep1, []ids.UUID{e.Team1}, integration.SchedulerPerms), "https://www.acme.example/about")
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Fields) != 3 {
		t.Fatalf("gate kept %d fields, want 3", len(proposal.Fields))
	}

	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](ids.UUID(proposal.ProposalId)), true, nil); err != nil {
		t.Fatalf("accept: %v", err)
	}

	assertAcceptFilledOnlyEmptyColumns(t, e, admin, orgID)

	// The approval is consumed; deciding again is refused and applies
	// nothing twice.
	var consumed bool
	err = database.WithWorkspaceTx(admin, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT consumed_at IS NOT NULL FROM approval WHERE id = $1`, ids.From[ids.ApprovalKind](ids.UUID(proposal.ProposalId))).Scan(&consumed)
	})
	if err != nil || !consumed {
		t.Fatalf("approval not redeemed by the effect (consumed=%v err=%v)", consumed, err)
	}
	var already *approvals.AlreadyDecidedError
	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](ids.UUID(proposal.ProposalId)), true, nil); !errors.As(err, &already) {
		t.Fatalf("re-decide → %v, want AlreadyDecided", err)
	}

	// A REJECTED proposal writes nothing: stage a second one and reject.
	proposal2, err := engine.Propose(e.As(e.Rep1, []ids.UUID{e.Team1}, integration.SchedulerPerms), "https://other.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](ids.UUID(proposal2.ProposalId)), false, nil); err != nil {
		t.Fatalf("reject: %v", err)
	}
	var orgs int
	err = database.WithWorkspaceTx(admin, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM organization`).Scan(&orgs)
	})
	if err != nil || orgs != 1 {
		t.Fatalf("reject still wrote an organization (%d rows, err=%v)", orgs, err)
	}
}

// seedAcmeOrgWithHumanIndustry plants the pre-existing acme.example
// organization with a HUMAN-set industry, so acceptance can prove it
// fills only empty columns.
func seedAcmeOrgWithHumanIndustry(t *testing.T, e *integration.Env, admin context.Context) ids.UUID {
	t.Helper()
	orgID := ids.NewV7()
	err := database.WithWorkspaceTx(admin, e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO organization (id, workspace_id, display_name, industry, source, captured_by)
			VALUES ($1, $2, 'Acme', 'Handcrafted Industry', 'manual', 'human:owner')`, orgID, e.WS); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
			VALUES ($1, $2, 'acme.example', true, 'manual', 'human:owner')`, e.WS, orgID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return orgID
}

// assertAcceptFilledOnlyEmptyColumns proves the accept executor's write
// discipline: org resolved (not duplicated), empty legal_name filled,
// the human-set industry untouched, and the evidence rows landed as the
// coldstart agent.
func assertAcceptFilledOnlyEmptyColumns(t *testing.T, e *integration.Env, admin context.Context, orgID ids.UUID) {
	t.Helper()
	var legalName, industry, capturedBy string
	var profileRows, orgs int
	err := database.WithWorkspaceTx(admin, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT coalesce(legal_name, ''), industry FROM organization WHERE id = $1`, orgID).Scan(&legalName, &industry); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM organization`).Scan(&orgs); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `
			SELECT count(*), max(captured_by) FROM organization_profile_field WHERE organization_id = $1`,
			orgID).Scan(&profileRows, &capturedBy)
	})
	if err != nil {
		t.Fatal(err)
	}
	if orgs != 1 {
		t.Fatalf("accept created a duplicate org (%d rows) instead of resolving acme.example", orgs)
	}
	if legalName != "Acme GmbH" {
		t.Fatalf("empty legal_name not filled: %q", legalName)
	}
	if industry != "Handcrafted Industry" {
		t.Fatalf("accept OVERWROTE a human-set industry: %q", industry)
	}
	if profileRows != 3 || capturedBy != "agent:coldstart" {
		t.Fatalf("evidence rows = %d captured_by=%q, want 3 rows as agent:coldstart", profileRows, capturedBy)
	}
}
