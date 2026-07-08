// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package consent

// The lead arm of consent (E12.20) over a real migrated Postgres: a
// grant recorded against a lead lands lead-scoped (person_id NULL),
// stays idempotent on re-assertion, reads back through LeadConsent, is
// refused for DOI purposes (the round-trip is person-keyed), and
// authorizes the outbound gate for the lead's email.

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type leadConsentEnv struct {
	owner      *pgx.Conn
	store      *Store
	ctx        context.Context
	ws, user   ids.UUID
	newsletter ids.PurposeID
	doiNews    ids.PurposeID
	lead       ids.LeadID
	leadEmail  string
}

func setupLeadConsent(t *testing.T) *leadConsentEnv {
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

	e := &leadConsentEnv{
		owner: owner,
		ws:    ids.NewV7(), user: ids.NewV7(),
		newsletter: ids.New[ids.PurposeKind](),
		doiNews:    ids.New[ids.PurposeKind](),
		lead:       ids.New[ids.LeadKind](),
	}
	e.leadEmail = "lena-" + e.lead.String() + "@warm.example"
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'LeadConsent', $2, 'EUR')`,
		e.ws, "lc-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`,
		e.user, e.ws, "rep-"+e.user.String()+"@lc.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO consent_purpose (id, workspace_id, key, label, requires_double_opt_in)
		VALUES ($1, $3, 'newsletter', 'Newsletter', false), ($2, $3, 'doi_newsletter', 'DOI Newsletter', true)`,
		e.newsletter, e.doiNews, e.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO lead (id, workspace_id, full_name, email, status, source, captured_by)
		 VALUES ($1, $2, 'Lena Lead', lower($3), 'working', 'inbound', 'human:x')`,
		e.lead, e.ws, e.leadEmail); err != nil {
		t.Fatal(err)
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

func TestLeadScopedConsentRecordsProofAndReadsBack(t *testing.T) {
	e := setupLeadConsent(t)

	state, err := e.store.Record(e.ctx, RecordInput{
		LeadID: e.lead, PurposeID: e.newsletter, NewState: "granted",
	})
	if err != nil {
		t.Fatalf("recording a lead-scoped grant: %v", err)
	}
	if state.State != "granted" || state.PurposeKey != "newsletter" {
		t.Fatalf("recorded state = %+v", state)
	}

	// The state row is lead-scoped: lead arm set, person arm NULL.
	var personArm *ids.UUID
	var rowState string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT person_id, state FROM person_consent WHERE lead_id = $1 AND purpose_id = $2`,
		e.lead, e.newsletter).Scan(&personArm, &rowState); err != nil {
		t.Fatalf("reading the state row: %v", err)
	}
	if personArm != nil || rowState != "granted" {
		t.Fatalf("state row = (person_id=%v, state=%q), want a lead-scoped granted row", personArm, rowState)
	}

	// Re-asserting the same state appends no second proof row.
	if _, err := e.store.Record(e.ctx, RecordInput{
		LeadID: e.lead, PurposeID: e.newsletter, NewState: "granted",
	}); err != nil {
		t.Fatalf("re-asserting the grant: %v", err)
	}
	var proofRows int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM consent_event WHERE lead_id = $1 AND purpose_id = $2`,
		e.lead, e.newsletter).Scan(&proofRows); err != nil {
		t.Fatal(err)
	}
	if proofRows != 1 {
		t.Fatalf("proof rows after idempotent re-assert = %d, want 1", proofRows)
	}

	// The lead arm of the read answers granted for the purpose and the
	// honest unknown for the untouched DOI purpose.
	states, events, err := e.store.LeadConsent(e.ctx, e.lead)
	if err != nil {
		t.Fatalf("LeadConsent: %v", err)
	}
	byKey := map[string]string{}
	for _, st := range states {
		byKey[st.PurposeKey] = st.State
	}
	if byKey["newsletter"] != "granted" || byKey["doi_newsletter"] != "unknown" {
		t.Fatalf("lead consent states = %v", byKey)
	}
	if len(events) != 1 {
		t.Fatalf("lead proof log length = %d, want 1", len(events))
	}
}

func TestLeadScopedDOIGrantIsRefused(t *testing.T) {
	e := setupLeadConsent(t)
	_, err := e.store.Record(e.ctx, RecordInput{
		LeadID: e.lead, PurposeID: e.doiNews, NewState: "granted",
	})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("a DOI grant on a lead subject: got %v, want a ValidationError (the round-trip is person-keyed)", err)
	}
}

func TestOutboundGateAcceptsTheLeadArm(t *testing.T) {
	e := setupLeadConsent(t)
	gate := NewGate(e.store)

	// Default-deny before the grant…
	if err := gate.RequireGrantedForEmails(e.ctx, []string{e.leadEmail}, "newsletter"); !errors.Is(err, apperrors.ErrConsentNotGranted) {
		t.Fatalf("pre-grant gate: %v, want ErrConsentNotGranted", err)
	}
	if _, err := e.store.Record(e.ctx, RecordInput{
		LeadID: e.lead, PurposeID: e.newsletter, NewState: "granted",
	}); err != nil {
		t.Fatal(err)
	}
	// …and the lead-scoped grant authorizes exactly that purpose.
	if err := gate.RequireGrantedForEmails(e.ctx, []string{e.leadEmail}, "newsletter"); err != nil {
		t.Fatalf("post-grant gate: %v, want pass", err)
	}
	if err := gate.RequireGrantedForEmails(e.ctx, []string{e.leadEmail}, "doi_newsletter"); !errors.Is(err, apperrors.ErrConsentNotGranted) {
		t.Fatalf("a grant for one purpose authorized another: %v", err)
	}
}
