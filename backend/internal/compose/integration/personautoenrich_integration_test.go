// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The person auto-enrich consumer against a real database.
//
// The consumer's whole reason to exist is ordering: a company's team page is
// read once, and the people it published are staged as proposals. A contact who
// arrives AFTERWARDS was never matched against what that page already said.
// This suite drives that arrival — a person.created envelope — and asserts the
// contact ends up carrying the page's facts with the page as their receipt.
//
// The proposals are staged through approvals.Service, the same writer
// siteleadstage.go uses, rather than inserted by hand: a row shape the real
// writer never produces would let the consumer pass against a fixture that
// cannot occur.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// sitePage is what a team page published about one person, in the shape the
// staged proposal carries it.
type sitePage struct {
	OrganizationID  ids.UUID `json:"organization_id"`
	SiteReadID      ids.UUID `json:"site_read_id"`
	NaturalKey      string   `json:"natural_key"`
	Name            string   `json:"name"`
	Role            string   `json:"role"`
	PublishedEmail  string   `json:"published_email,omitempty"`
	LinkedinURL     string   `json:"linkedin_url,omitempty"`
	EvidenceSnippet string   `json:"evidence_snippet"`
	SourceURL       string   `json:"source_url"`
}

// seedEmployedPerson creates a contact and the current-primary employment that
// makes their employer's site the only one allowed to describe them.
func seedEmployedPerson(t *testing.T, e *Env, name string) (ids.PersonID, ids.OrganizationID) {
	t.Helper()
	personID, orgID := ids.NewV7(), ids.NewV7()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO organization (id, workspace_id, owner_id, display_name, source, captured_by)
			VALUES ($1, $2, $3, 'Gitex', 'manual', 'user:seed')`, orgID, e.WS, e.Rep1); err != nil {
			return err
		}
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
			VALUES ($1, $2, $3, $4, 'manual', 'user:seed')`,
			personID, e.WS, e.Rep1, name); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO relationship (id, workspace_id, person_id, organization_id, kind,
			                          is_current_primary, source, captured_by)
			VALUES ($1, $2, $3, $4, 'employment', true, 'manual', 'user:seed')`,
			ids.NewV7(), e.WS, personID, orgID)
		return err
	}); err != nil {
		t.Fatalf("seeding the employed contact: %v", err)
	}
	return ids.From[ids.PersonKind](personID), ids.From[ids.OrganizationKind](orgID)
}

// stageSiteLead stages one published person exactly as the site-read lane does.
func stageSiteLead(t *testing.T, e *Env, orgID ids.OrganizationID, page sitePage) ids.ApprovalID {
	t.Helper()
	page.OrganizationID = orgID.UUID
	page.SiteReadID = ids.NewV7()
	page.NaturalKey = page.Name
	proposed, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshalling the proposal: %v", err)
	}
	identity, err := json.Marshal(map[string]string{"natural_key": page.NaturalKey})
	if err != nil {
		t.Fatalf("marshalling the identity: %v", err)
	}
	digest := sha256.Sum256(proposed)
	id, err := approvals.NewService(e.Pool).Stage(e.Admin(), approvals.StageInput{
		Kind:           "site_lead",
		ProposedChange: proposed,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     "organization",
		TargetID:       orgID.UUID,
		Identity:       identity,
		JoinPending:    true,
		Summary:        "Lead from the team page: " + page.Name,
	})
	if err != nil {
		t.Fatalf("staging the site lead: %v", err)
	}
	return id
}

// newEnricher builds the consumer with no search provider — the sovereign
// posture of ADR-0081, and the arm this suite exercises. Discovery has its own
// unit tests against a fake client; what is under test here is the fill from
// what the employer already published.
func newEnricher(e *Env) *compose.PersonAutoEnrich {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	return compose.NewPersonAutoEnrich(e.Pool, people.NewStore(e.Pool), approvals.NewService(e.Pool), nil, quiet)
}

// personCreated is the envelope a newly created contact reaches the consumer on.
//
// The trace is not decoration: the pass carries the correlation id through to
// whatever it writes, so the fill traces back to the event that caused it, and
// the outbox refuses an envelope without one.
func personCreated(e *Env, personID ids.PersonID) events.Envelope {
	return events.Envelope{
		Type:        "person.created",
		WorkspaceID: e.WS,
		Entity:      events.EntityRef{Type: "person", ID: personID.UUID},
		Trace:       events.Trace{CorrelationID: ids.NewV7()},
	}
}

// profileFields reads back what the pass wrote, keyed by field name.
func profileFields(t *testing.T, e *Env, personID ids.PersonID) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`SELECT field, value FROM person_profile_field WHERE person_id = $1`, personID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var field, value string
			if err := rows.Scan(&field, &value); err != nil {
				return err
			}
			out[field] = value
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading the profile fields: %v", err)
	}
	return out
}

// A contact who arrives after their employer's page was read must still end up
// carrying what that page said. This is the ordering bug the consumer exists to
// close: the site read matched nobody at the time, and nothing would ever look
// again.
func TestPersonAutoEnrichFillsAContactFromTheirEmployersStagedPage(t *testing.T) {
	e := Setup(t)
	personID, orgID := seedEmployedPerson(t, e, "Anna Muster")
	approvalID := stageSiteLead(t, e, orgID, sitePage{
		Name:            "Anna Muster",
		Role:            "Head of Delivery",
		LinkedinURL:     "https://www.linkedin.com/in/annamuster",
		EvidenceSnippet: "Anna Muster — Head of Delivery",
		SourceURL:       "https://gitex.com/team",
	})

	if err := newEnricher(e).HandleEvent(context.Background(), personCreated(e, personID)); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	fields := profileFields(t, e, personID)
	if got := fields["role"]; got != "Head of Delivery" {
		t.Errorf("role = %q, want the role the page published", got)
	}
	if got := fields["linkedin"]; got != "https://www.linkedin.com/in/annamuster" {
		t.Errorf("linkedin = %q, want the URL the page published", got)
	}

	// The proposal asked a human to create a lead for someone who is already a
	// contact. The world answered the question, so it must leave the queue.
	//
	// Withdrawal expires the row rather than moving its status — a withdrawn
	// proposal is one no live read returns, and asserting on `status` would
	// pass against a row still sitting in a rep's inbox.
	var live bool
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT status = 'pending' AND expires_at > now() FROM approval WHERE id = $1`,
			approvalID).Scan(&live)
	}); err != nil {
		t.Fatalf("reading the proposal back: %v", err)
	}
	if live {
		t.Error("the site-lead proposal is still live, so a rep is asked to create a lead for a contact that exists")
	}
}

// The match is narrow on purpose. A page person who is not unmistakably this
// contact must leave the record alone rather than be settled by a sweep.
func TestPersonAutoEnrichLeavesAContactAloneWhenThePageNamesSomebodyElse(t *testing.T) {
	e := Setup(t)
	personID, orgID := seedEmployedPerson(t, e, "Anna Muster")
	stageSiteLead(t, e, orgID, sitePage{
		Name:            "Bernd Schulz",
		Role:            "Head of Procurement",
		EvidenceSnippet: "Bernd Schulz — Head of Procurement",
		SourceURL:       "https://gitex.com/team",
	})

	if err := newEnricher(e).HandleEvent(context.Background(), personCreated(e, personID)); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	if fields := profileFields(t, e, personID); len(fields) != 0 {
		t.Errorf("the contact carries %v, but the page named a different person", fields)
	}
}

// No employer means no site may describe this contact, and the pass must stop
// before it reads anything. A contact with a staged page at a company they do
// not work for is the case that would silently cross records.
func TestPersonAutoEnrichStopsAtAContactWithNoEmployer(t *testing.T) {
	e := Setup(t)
	personID := ids.From[ids.PersonKind](ids.NewV7())
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by)
			VALUES ($1, $2, $3, 'Anna Muster', 'manual', 'user:seed')`, personID, e.WS, e.Rep1)
		return err
	}); err != nil {
		t.Fatalf("seeding the unemployed contact: %v", err)
	}

	if err := newEnricher(e).HandleEvent(context.Background(), personCreated(e, personID)); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	if fields := profileFields(t, e, personID); len(fields) != 0 {
		t.Errorf("the contact carries %v with no employer to have published it", fields)
	}
}

// A merge names the merged-AWAY person as its entity. Enriching that row fills a
// record no read returns, and the survivor — which is the one that actually
// became newly matchable, inheriting the source's emails and employer — would be
// missed entirely.
func TestPersonAutoEnrichFollowsAMergeToTheSurvivor(t *testing.T) {
	e := Setup(t)
	survivor, orgID := seedEmployedPerson(t, e, "Anna Muster")
	mergedAway := ids.From[ids.PersonKind](ids.NewV7())
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO person (id, workspace_id, owner_id, full_name, source, captured_by, archived_at)
			VALUES ($1, $2, $3, 'A. Muster', 'manual', 'user:seed', now())`, mergedAway, e.WS, e.Rep1)
		return err
	}); err != nil {
		t.Fatalf("seeding the merged-away contact: %v", err)
	}
	stageSiteLead(t, e, orgID, sitePage{
		Name:            "Anna Muster",
		Role:            "Head of Delivery",
		EvidenceSnippet: "Anna Muster — Head of Delivery",
		SourceURL:       "https://gitex.com/team",
	})

	payload, err := json.Marshal(map[string]string{"merged_into_id": survivor.String()})
	if err != nil {
		t.Fatalf("marshalling the merge payload: %v", err)
	}
	if err := newEnricher(e).HandleEvent(context.Background(), events.Envelope{
		Type:        "person.merged",
		WorkspaceID: e.WS,
		Entity:      events.EntityRef{Type: "person", ID: mergedAway.UUID},
		Trace:       events.Trace{CorrelationID: ids.NewV7()},
		Payload:     payload,
	}); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	if got := profileFields(t, e, survivor)["role"]; got != "Head of Delivery" {
		t.Errorf("the survivor's role = %q, want the page's — the merge went to the archived row", got)
	}
	if fields := profileFields(t, e, mergedAway); len(fields) != 0 {
		t.Errorf("the merged-away row carries %v, which no read will ever return", fields)
	}
}
