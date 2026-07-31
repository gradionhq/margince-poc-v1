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
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// acmeTeamSite is a two-page site whose /team page names two people: Anna
// with a printed email, Bernd without one: only Anna is proposed, because a
// lead nobody can contact is not a lead.
func acmeTeamSite() *fakeSite {
	return &fakeSite{pages: map[string]fakeSitePage{
		seedURL: {text: readable("Acme home.")},
		seedURL + "/team": {text: readable("Team.") + " Anna Muster is our Chief Executive Officer. " +
			"Reach her at anna@acme.example. Bernd Beispiel leads sales as Head of Sales."},
	}}
}

// teamDeepBrain names both people on the team page; Bernd's claimed
// email is NOT printed on the page, so the gate must strip it while
// keeping him. The profile lane grounds nothing.
func teamDeepBrain() laneFake {
	return laneFake{
		profileReply: `{"fields":[]}`,
		pageReplies: map[string]string{
			seedURL + "/team": `{"facts":[],"people":[
				{"n":"Anna Muster","r":"Chief Executive Officer","m":"anna@acme.example","e":"s0"},
				{"n":"Bernd Beispiel","r":"Head of Sales","m":"bernd@acme.example","e":"s0"}]}`,
		},
	}
}

// reflowedTeamSite is the same team page after a redesign reprinted Anna's
// name with different casing and spacing — the same person, respelled.
func reflowedTeamSite() *fakeSite {
	return &fakeSite{pages: map[string]fakeSitePage{
		seedURL: {text: readable("Acme home.")},
		seedURL + "/team": {text: readable("Team.") + " anna   MUSTER is our Chief Executive Officer. " +
			"Reach her at anna@acme.example."},
	}}
}

func reflowedTeamBrain() laneFake {
	return laneFake{
		profileReply: `{"fields":[]}`,
		pageReplies: map[string]string{
			seedURL + "/team": `{"facts":[],"people":[
				{"n":"anna   MUSTER","r":"Chief Executive Officer","m":"anna@acme.example","e":"s0"}]}`,
		},
	}
}

// seedRequesterCanReadPeople gives the deep read's requester a REAL role
// granting person/lead read, through the identity tables.
//
// The already-on-file probe runs under the requester's live grants, resolved
// from role + role_assignment rather than from whatever permissions a test
// context claims — that is the whole point of narrowing the worker's system
// authority. Without a role row the requester can lend no scope, the probe is
// skipped, and every published person is proposed.
func seedRequesterCanReadPeople(t *testing.T, e *integration.Env, user ids.UUID) {
	t.Helper()
	ctx := context.Background()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		var roleID ids.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO role (workspace_id, key, name, permissions)
			 VALUES ($1, 'site_read_requester', 'Site Read Requester',
			         '{"objects":{"person":{"read":true},"lead":{"read":true}},"row_scope":"all"}'::jsonb)
			 RETURNING id`, e.WS).Scan(&roleID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO role_assignment (workspace_id, role_id, user_id) VALUES ($1, $2, $3)`,
			e.WS, roleID, user)
		return err
	})
	if err != nil {
		t.Fatalf("granting the requester person/lead read: %v", err)
	}
}

// runTeamDeepRead crawls acmeTeamSite with the people reply as the one
// corpus answer and returns the finished dossier.
func runTeamDeepRead(t *testing.T, e *integration.Env, org ids.UUID) (people.SiteRead, *approvals.Service) {
	t.Helper()
	return runTeamDeepReadOn(t, e, org, acmeTeamSite(), teamDeepBrain())
}

// runTeamDeepReadOn is runTeamDeepRead over a caller-chosen site and corpus
// answer, for the reads that need the page to say something different.
func runTeamDeepReadOn(t *testing.T, e *integration.Env, org ids.UUID, site *fakeSite, brain laneFake) (people.SiteRead, *approvals.Service) {
	t.Helper()
	worker, svc := newDeepReadTestWorker(e, site, brain)
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
	// with fact_count 0 and one staging for the person the page published an
	// address for. Bernd is named on the same page and dropped: a lead
	// nobody can contact asks a human to confirm a name they cannot act on.
	if done.Status != "done" || done.FactCount != 0 {
		t.Fatalf("dossier = %+v, want done with fact_count 0 (people are not facts)", done)
	}
	if len(done.ProposalIDs) != 1 {
		t.Fatalf("proposal_ids = %v, want one site_lead per CONTACTABLE published person", done.ProposalIDs)
	}

	annaSummary, anna, annaRaw := siteLeadProposalRow(t, e, done.ProposalIDs[0])
	if annaSummary != "Lead from https://acme.example: Anna Muster — Chief Executive Officer" {
		t.Fatalf("summary = %q, want the site + name — role spelling", annaSummary)
	}
	if anna.Name != "Anna Muster" || anna.Role != "Chief Executive Officer" ||
		anna.PublishedEmail != "anna@acme.example" ||
		anna.OrganizationID != org || anna.SiteReadID != done.ID ||
		anna.SourceURL != seedURL+"/team" {
		t.Fatalf("Anna's payload = %+v, want the page's published identity with provenance", anna)
	}
	// Reference evidence: the stored snippet is the page's OWN passage
	// (resolved from the cited id), which must carry the naming sentence.
	if !strings.Contains(anna.EvidenceSnippet, "Anna Muster is our Chief Executive Officer") {
		t.Fatalf("Anna's evidence = %q, want the page's passage naming her", anna.EvidenceSnippet)
	}

	// The NEVER-8 boundary and the contactability floor meet on Bernd: the
	// model claimed an email the page never printed, so the claim is stripped
	// — and a person with no published address is not proposed at all. A lead
	// nobody can contact asks a human to confirm a name they cannot act on.
	if len(done.ProposalIDs) != 1 {
		t.Fatalf("%d proposals, want only the person the page published an address for", len(done.ProposalIDs))
	}
	if strings.Contains(string(annaRaw), "bernd@") {
		t.Fatalf("a payload carries an email the page never published: %s", annaRaw)
	}
}

// A lead is filed under the company it was read from, but creating it reads
// nothing off that company. The staging used to pin the organization's version
// anyway, and any unrelated write to the company — the very enrichment run
// that discovers the leads writes its profile fields — bumped that version and
// made the lead permanently un-acceptable. Worse than a failed click: the
// decision commits before the effect runs, so the approval was left approved
// and unredeemed with no lead created, and the retry answered "already
// decided". The lead was gone.
func TestSiteLeadStaysAcceptableAfterAnUnrelatedWriteToItsCompany(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	done, svc := runTeamDeepRead(t, e, org)

	// Anything at all that touches the company. The version trigger fires on
	// every UPDATE, so the narrowest possible write is the honest test.
	if err := database.WithWorkspaceTx(e.As(e.Rep1, nil, integration.AdminPerms), e.Pool,
		func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(),
				`UPDATE organization SET industry = 'Manufacturing' WHERE id = $1`, org)
			return err
		}); err != nil {
		t.Fatalf("touch the company: %v", err)
	}

	for _, id := range done.ProposalIDs {
		if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms),
			ids.From[ids.ApprovalKind](id), true, nil); err != nil {
			t.Fatalf("accept %s after an unrelated write to its company: %v", id, err)
		}
	}

	var leads int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM lead`).Scan(&leads)
	}); err != nil {
		t.Fatal(err)
	}
	if leads != 2 {
		t.Fatalf("%d leads after accepting both, want 2 — the accept was refused and the lead lost", leads)
	}
}

func TestSiteLeadAcceptCapturesALeadIdempotentAcrossReReads(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	seedRequesterCanReadPeople(t, e, e.Rep1)
	done, svc := runTeamDeepRead(t, e, org)

	// Accepting Anna captures her as a LEAD via the Sink, with her published
	// email.
	for _, id := range done.ProposalIDs {
		if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](id), true, nil); err != nil {
			t.Fatalf("accept %s: %v", id, err)
		}
	}
	var leads int
	var annaEmail, annaTitle, annaSource, annaCapturedBy string
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM lead`).Scan(&leads); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT email, title, source_system, captured_by FROM lead WHERE full_name = 'Anna Muster'`).
			Scan(&annaEmail, &annaTitle, &annaSource, &annaCapturedBy)
	})
	if err != nil {
		t.Fatal(err)
	}
	if leads != 1 {
		t.Fatalf("%d leads, want 1 — only the person the page published an address for", leads)
	}
	if annaEmail != "anna@acme.example" || annaTitle != "Chief Executive Officer" ||
		annaSource != "siteread" || annaCapturedBy != "agent:siteread" {
		t.Fatalf("Anna's lead = %s/%s from %s by %s, want her published identity captured as agent:siteread",
			annaEmail, annaTitle, annaSource, annaCapturedBy)
	}

	// A FRESH read of the same site finds Anna on the page again — and asks
	// nobody about her. She is on file now, so the question is already
	// answered; re-staging it would spend a human decision on a confirmation
	// that could only land on the lead row that already exists.
	again, _ := runTeamDeepRead(t, e, org)
	if len(again.ProposalIDs) != 0 {
		t.Fatalf("re-read proposal_ids = %v, want none — Anna is already on file", again.ProposalIDs)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM lead`); n != 1 {
		t.Fatalf("%d leads after the re-read, want still 1", n)
	}
}

// A person the workspace already reaches by email — captured months earlier
// through the mail connector, long before any crawl — is not a decision: the
// site read finds a name that is already a live contact and must not put it
// back in front of a human.
func TestAPersonAlreadyOnFileIsNotStagedAgain(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	seedRequesterCanReadPeople(t, e, e.Rep1)

	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	if _, err := e.People.CreatePerson(ctx, people.CreatePersonInput{
		FullName: "Anna Muster",
		Emails:   []people.PersonEmailInput{{Email: "anna@acme.example", EmailType: "work", IsPrimary: true}},
		Source:   "email",
	}); err != nil {
		t.Fatalf("seeding the contact who already emails us: %v", err)
	}

	done, _ := runTeamDeepRead(t, e, org)
	if done.Status != "done" {
		t.Fatalf("dossier status = %q, want done — a fully known roster is not a failure", done.Status)
	}
	if len(done.ProposalIDs) != 0 {
		t.Fatalf("proposal_ids = %v, want none — the page named nobody the workspace does not already have", done.ProposalIDs)
	}
}

// Two reads before anyone decides leave ONE question, not two. The payload
// carries the read id and the page's own reflowed passage, so every crawl
// hashes differently; the proposal's identity is the person at the company,
// and the newer staging supersedes the older one.
func TestASecondReadSupersedesTheUndecidedFirst(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")

	first, _ := runTeamDeepRead(t, e, org)
	second, svc := runTeamDeepRead(t, e, org)
	if len(first.ProposalIDs) != 1 || len(second.ProposalIDs) != 1 {
		t.Fatalf("proposals = %v then %v, want one per read", first.ProposalIDs, second.ProposalIDs)
	}
	if first.ProposalIDs[0] == second.ProposalIDs[0] {
		t.Fatalf("both reads named approval %s — the second read carries a fresh payload and must stage its own",
			first.ProposalIDs[0])
	}
	live := e.WsCount(t, `SELECT count(*) FROM approval
		 WHERE kind = 'site_lead' AND status = 'pending' AND expires_at > now()`)
	if live != 1 {
		t.Fatalf("%d live site_lead questions after two reads, want 1 — one Anna Muster, one decision", live)
	}
	// The identity is the natural key, so it survives the site reprinting the
	// same person's name differently. A raw-name identity passes every
	// assertion above and still stacks a second question here.
	reflowed, _ := runTeamDeepReadOn(t, e, org, reflowedTeamSite(), reflowedTeamBrain())
	if len(reflowed.ProposalIDs) != 1 {
		t.Fatalf("reflowed re-read proposal_ids = %v, want its own fresh staging", reflowed.ProposalIDs)
	}
	live = e.WsCount(t, `SELECT count(*) FROM approval
		 WHERE kind = 'site_lead' AND status = 'pending' AND expires_at > now()`)
	if live != 1 {
		t.Fatalf("%d live site_lead questions after the page reflowed her name, want 1 — casing and spacing are not a new person", live)
	}

	// The survivor is the newest one, and it still accepts. The reads it
	// superseded do not: a superseded offer is expired, and deciding it would
	// be acting on a question the inbox has already replaced.
	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms),
		ids.From[ids.ApprovalKind](second.ProposalIDs[0]), true, nil); err == nil {
		t.Fatal("the superseded proposal still accepted, want it refused as expired")
	}
	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms),
		ids.From[ids.ApprovalKind](reflowed.ProposalIDs[0]), true, nil); err != nil {
		t.Fatalf("accepting the surviving proposal: %v", err)
	}
	// One lead, under the name the page printed the FIRST time — the natural
	// key the reflowed proposal was staged under is the one the accept lands
	// on, so a respelling cannot mint a second Anna.
	if n := e.WsCount(t, `SELECT count(*) FROM lead`); n != 1 {
		t.Fatalf("%d leads after accepting the survivor of three reads, want exactly 1", n)
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
