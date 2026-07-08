// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Lead→person promotion must carry lead-scoped consent through
// (data-model §7: subject re-pointed, proof preserved), applying the
// merge.go precedent where the target person already holds a state:
// withdrawal wins with an appended proof event, an existing person state
// stands, and untouched purposes re-point wholesale. Proven over a real
// migrated Postgres — the constraint interplay (both unique keys, the
// subject CHECK) is the thing under test.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// promoteConsentEnv is this suite's fixture over the already-migrated
// database: one fresh workspace, one user, two consent purposes, and the
// store under an unbounded principal.
type promoteConsentEnv struct {
	owner               *pgx.Conn
	store               *Store
	ctx                 context.Context
	ws, user            ids.UUID
	newsletter, updates ids.UUID
}

func setupPromoteConsent(t *testing.T) *promoteConsentEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	e := &promoteConsentEnv{
		owner: owner,
		ws:    ids.NewV7(), user: ids.NewV7(),
		newsletter: ids.NewV7(), updates: ids.NewV7(),
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'PromoteConsent', $2, 'EUR')`,
		e.ws, "pc-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`,
		e.user, e.ws, "rep-"+e.user.String()+"@pc.test"); err != nil {
		t.Fatal(err)
	}
	for id, key := range map[ids.UUID]string{e.newsletter: "newsletter", e.updates: "product_updates"} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO consent_purpose (id, workspace_id, key, label) VALUES ($1, $2, $3, $3)`,
			id, e.ws, key); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.store = NewStore(pool)

	opCtx := principal.WithWorkspaceID(context.Background(), e.ws)
	opCtx = principal.WithCorrelationID(opCtx, ids.NewV7())
	e.ctx = principal.WithActor(opCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.user.String(), UserID: e.user,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"lead":   {Create: true, Read: true, Update: true, Delete: true},
				"person": {Create: true, Read: true, Update: true, Delete: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return e
}

func (e *promoteConsentEnv) seedLead(t *testing.T, email string) ids.LeadID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO lead (id, workspace_id, full_name, email, status, source, captured_by)
		 VALUES ($1, $2, 'Lena Lead', lower($3), 'working', 'inbound', 'human:x')`,
		id, e.ws, email); err != nil {
		t.Fatal(err)
	}
	return ids.From[ids.LeadKind](id)
}

func (e *promoteConsentEnv) seedLeadConsent(t *testing.T, lead ids.LeadID, purpose ids.UUID, state string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO person_consent (workspace_id, lead_id, purpose_id, state, captured_at, source)
		 VALUES ($1, $2, $3, $4, $5, 'form')`,
		e.ws, lead, purpose, state, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO consent_event (workspace_id, lead_id, purpose_id, new_state, source, policy_text, policy_version, captured_at, captured_by)
		 VALUES ($1, $2, $3, $4, 'form', 'seeded wording', 'v1', $5, 'human:x')`,
		e.ws, lead, purpose, state, now); err != nil {
		t.Fatal(err)
	}
}

// consentRow reads the subject's state row for one purpose straight off
// the table (the people package may not import the consent module).
func (e *promoteConsentEnv) consentRow(t *testing.T, column string, subject ids.UUID, purpose ids.UUID) (state string, found bool) {
	t.Helper()
	err := e.owner.QueryRow(context.Background(),
		`SELECT state FROM person_consent WHERE `+column+` = $1 AND purpose_id = $2`,
		subject, purpose).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return state, true
}

func TestPromotionRepointsLeadConsentToTheNewPerson(t *testing.T) {
	e := setupPromoteConsent(t)
	lead := e.seedLead(t, "lena@fresh.example")
	e.seedLeadConsent(t, lead, e.newsletter, "granted")

	person, merged, err := e.store.PromoteLead(e.ctx, lead, PromoteLeadInput{Trigger: "human_qualify"})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if merged {
		t.Fatal("no person existed; promotion must create, not merge")
	}
	personID := ids.UUID(person.Id)

	// The state re-pointed: person arm set, lead arm cleared — the carried
	// consent no longer rides the retired lead's lifecycle.
	state, found := e.consentRow(t, "person_id", personID, e.newsletter)
	if !found || state != "granted" {
		t.Fatalf("carried consent = (%q, %v), want granted on the person", state, found)
	}
	if _, still := e.consentRow(t, "lead_id", lead.UUID, e.newsletter); still {
		t.Fatal("the lead-scoped state row must re-point, not duplicate")
	}
	var leadArm *ids.UUID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT lead_id FROM person_consent WHERE person_id = $1 AND purpose_id = $2`,
		personID, e.newsletter).Scan(&leadArm); err != nil {
		t.Fatal(err)
	}
	if leadArm != nil {
		t.Fatal("lead_id must clear on re-point (a lead deletion would cascade the person's consent away)")
	}

	// Proof preserved: the historical lead-scoped event stays AS WRITTEN.
	var leadEvents int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM consent_event WHERE lead_id = $1 AND person_id IS NULL AND new_state = 'granted'`,
		lead).Scan(&leadEvents); err != nil {
		t.Fatal(err)
	}
	if leadEvents != 1 {
		t.Fatalf("historical lead-scoped proof events = %d, want 1 untouched", leadEvents)
	}
}

func TestPromotionMergeAppliesWithdrawalWinsAndStateStands(t *testing.T) {
	e := setupPromoteConsent(t)
	email := "lena@collision.example"
	lead := e.seedLead(t, email)
	// The lead withdrew newsletter and granted product updates.
	e.seedLeadConsent(t, lead, e.newsletter, "withdrawn")
	e.seedLeadConsent(t, lead, e.updates, "granted")

	// A live person already holds the email — promotion will merge — and
	// already has newsletter granted.
	personID := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO person (id, workspace_id, full_name, source, captured_by)
		 VALUES ($1, $2, 'Lena Person', 'manual', 'human:x')`, personID, e.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO person_email (workspace_id, person_id, email, email_type, is_primary, position, source, captured_by)
		 VALUES ($1, $2, lower($3), 'work', true, 1, 'manual', 'human:x')`, e.ws, personID, email); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO person_consent (workspace_id, person_id, purpose_id, state, captured_at, source)
		 VALUES ($1, $2, $3, 'granted', now(), 'manual')`, e.ws, personID, e.newsletter); err != nil {
		t.Fatal(err)
	}

	person, merged, err := e.store.PromoteLead(e.ctx, lead, PromoteLeadInput{Trigger: "inbound_reply"})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if !merged || ids.UUID(person.Id) != personID {
		t.Fatalf("promotion must merge into the existing person: merged=%v id=%s", merged, person.Id)
	}

	// Withdrawal wins: the person's newsletter grant flips, with proof.
	state, found := e.consentRow(t, "person_id", personID, e.newsletter)
	if !found || state != "withdrawn" {
		t.Fatalf("newsletter after merge = (%q, %v), want the lead's withdrawal to win", state, found)
	}
	var proof int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM consent_event
		 WHERE person_id = $1 AND purpose_id = $2 AND new_state = 'withdrawn' AND source = 'promotion'`,
		personID, e.newsletter).Scan(&proof); err != nil {
		t.Fatal(err)
	}
	if proof != 1 {
		t.Fatalf("withdrawal-wins proof events = %d, want exactly 1 (a state change without proof breaks Art. 7(1))", proof)
	}

	// The purpose the person had no row for travels over.
	state, found = e.consentRow(t, "person_id", personID, e.updates)
	if !found || state != "granted" {
		t.Fatalf("product_updates after merge = (%q, %v), want the lead's grant carried", state, found)
	}
	// No lead-scoped state rows remain on the retired lead.
	var leftovers int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM person_consent WHERE lead_id = $1`, lead).Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if leftovers != 0 {
		t.Fatalf("lead-scoped state rows left behind = %d, want 0", leftovers)
	}
}
