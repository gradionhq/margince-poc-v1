// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The deep read's person lane end-to-end (R5, NEVER-8): a team page
// yields one thin site_lead proposal per published person — email kept
// only when the page printed it — and accepting one captures a LEAD
// through the capture Sink, idempotent on the (source page, name)
// natural key across re-reads. Rejection reaches no sink at all.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// acmeTeamSite is a two-page site whose /team page names two people: Anna
// with a printed email, Bernd without one — the person lane's fixture.
func acmeTeamSite() *fakeSite {
	return &fakeSite{pages: map[string]fakeSitePage{
		seedURL: {text: readable("Acme home.")},
		seedURL + "/team": {text: readable("Team.") + " Anna Muster is our Chief Executive Officer. " +
			"Reach her at anna@acme.example. Bernd Beispiel leads sales as Head of Sales."},
	}}
}

// deepTeamPeopleReply names both people; Bernd's claimed email is NOT
// printed on the page, so the gate must strip it while keeping him.
const deepTeamPeopleReply = `{"fields":[],"facts":[],"people":[
	{"name":"Anna Muster","role":"Chief Executive Officer","published_email":"anna@acme.example",
	 "evidence_snippet":"Anna Muster is our Chief Executive Officer","source_url":"` + seedURL + `/team","confidence":0.9},
	{"name":"Bernd Beispiel","role":"Head of Sales","published_email":"bernd@acme.example",
	 "evidence_snippet":"Bernd Beispiel leads sales as Head of Sales","source_url":"` + seedURL + `/team","confidence":0.8}],
	"legal_entities":[]}`

// runTeamDeepRead crawls acmeTeamSite with the people reply as the one
// corpus answer and returns the finished dossier.
func runTeamDeepRead(t *testing.T, e *integration.Env, org ids.UUID) (people.SiteRead, *approvals.Service) {
	t.Helper()
	worker, svc := newDeepReadTestWorker(e, acmeTeamSite(),
		ai.NewFakeClient().Script(deepTeamPeopleReply))
	read, args := startDeepRead(t, e, org)
	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("run: %v", err)
	}
	done, err := e.People.GetSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), orgIDOf(org), read.ID)
	if err != nil {
		t.Fatal(err)
	}
	return done, svc
}

// siteLeadProposalRow loads one staged site_lead approval's summary and
// payload by its id.
func siteLeadProposalRow(t *testing.T, e *integration.Env, id ids.UUID) (string, siteLeadProposal, []byte) {
	t.Helper()
	var summary string
	var raw []byte
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT summary, proposed_change FROM approval WHERE id = $1 AND kind = 'site_lead'`,
			id).Scan(&summary, &raw)
	})
	if err != nil {
		t.Fatalf("loading site_lead approval %s: %v", id, err)
	}
	var proposal siteLeadProposal
	if err := json.Unmarshal(raw, &proposal); err != nil {
		t.Fatalf("decoding site_lead payload: %v", err)
	}
	return summary, proposal, raw
}

func TestDeepReadTeamPageStagesOneThinSiteLeadPerPublishedPerson(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	done, _ := runTeamDeepRead(t, e, org)

	// People are proposals, not facts: the dossier reports an honest done
	// with fact_count 0 and the two per-person stagings.
	if done.Status != "done" || done.FactCount != 0 {
		t.Fatalf("dossier = %+v, want done with fact_count 0 (people are not facts)", done)
	}
	if len(done.ProposalIDs) != 2 {
		t.Fatalf("proposal_ids = %v, want one site_lead per published person", done.ProposalIDs)
	}

	annaSummary, anna, _ := siteLeadProposalRow(t, e, done.ProposalIDs[0])
	if annaSummary != "Lead from https://acme.example: Anna Muster — Chief Executive Officer" {
		t.Fatalf("summary = %q, want the site + name — role spelling", annaSummary)
	}
	if anna.Name != "Anna Muster" || anna.Role != "Chief Executive Officer" ||
		anna.PublishedEmail != "anna@acme.example" ||
		anna.OrganizationID != org || anna.SiteReadID != done.ID ||
		anna.SourceURL != seedURL+"/team" {
		t.Fatalf("Anna's payload = %+v, want the page's published identity with provenance", anna)
	}
	if anna.EvidenceSnippet != "Anna Muster is our Chief Executive Officer" {
		t.Fatalf("Anna's evidence = %q, want the page's verbatim snippet", anna.EvidenceSnippet)
	}

	// The NEVER-8 boundary: the model claimed an email for Bernd the page
	// never printed — the staged payload must not carry it ANYWHERE.
	_, bernd, berndRaw := siteLeadProposalRow(t, e, done.ProposalIDs[1])
	if bernd.Name != "Bernd Beispiel" || bernd.Role != "Head of Sales" {
		t.Fatalf("Bernd's payload = %+v, want the page's published identity", bernd)
	}
	if bernd.PublishedEmail != "" || strings.Contains(string(berndRaw), "bernd@") {
		t.Fatalf("Bernd's payload %s carries an email the page never published", berndRaw)
	}
}

func TestSiteLeadAcceptCapturesALeadIdempotentAcrossReReads(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	done, svc := runTeamDeepRead(t, e, org)

	// Accepting Anna captures her as a LEAD via the Sink — with her
	// published email; accepting Bernd proves the empty-email path.
	for _, id := range done.ProposalIDs {
		if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](id), true, nil); err != nil {
			t.Fatalf("accept %s: %v", id, err)
		}
	}
	var leads int
	var annaEmail, annaTitle, annaSource, annaCapturedBy string
	var berndEmail *string
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM lead`).Scan(&leads); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT email, title, source_system, captured_by FROM lead WHERE full_name = 'Anna Muster'`).
			Scan(&annaEmail, &annaTitle, &annaSource, &annaCapturedBy); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT email FROM lead WHERE full_name = 'Bernd Beispiel'`).Scan(&berndEmail)
	})
	if err != nil {
		t.Fatal(err)
	}
	if leads != 2 {
		t.Fatalf("%d leads after accepting both people, want 2", leads)
	}
	if annaEmail != "anna@acme.example" || annaTitle != "Chief Executive Officer" ||
		annaSource != "siteread" || annaCapturedBy != "agent:siteread" {
		t.Fatalf("Anna's lead = %s/%s from %s by %s, want her published identity captured as agent:siteread",
			annaEmail, annaTitle, annaSource, annaCapturedBy)
	}
	if berndEmail != nil {
		t.Fatalf("Bernd's lead email = %v, want NULL — the page published none", *berndEmail)
	}

	// A FRESH read of the same site stages fresh proposals; accepting the
	// same person again resolves to the same natural key — no second lead.
	again, svc2 := runTeamDeepRead(t, e, org)
	if len(again.ProposalIDs) != 2 {
		t.Fatalf("re-read proposal_ids = %v, want the two people staged again", again.ProposalIDs)
	}
	if _, err := svc2.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](again.ProposalIDs[0]), true, nil); err != nil {
		t.Fatalf("re-accept after re-read: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM lead`); n != 2 {
		t.Fatalf("%d leads after re-accepting Anna from a re-read, want still 2 (same natural key)", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM lead WHERE full_name = 'Anna Muster'`); n != 1 {
		t.Fatalf("%d Anna leads after the re-read accept, want exactly 1", n)
	}
}

func TestSiteLeadRejectionCapturesNothing(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	done, svc := runTeamDeepRead(t, e, org)

	for _, id := range done.ProposalIDs {
		if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](id), false, nil); err != nil {
			t.Fatalf("reject %s: %v", id, err)
		}
	}
	if n := e.WsCount(t, `SELECT count(*) FROM lead`); n != 0 {
		t.Fatalf("%d leads after rejecting every site_lead, want 0", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM raw_capture`); n != 0 {
		t.Fatalf("%d raw_capture rows after rejections, want 0 — a rejection reaches no sink", n)
	}
}
