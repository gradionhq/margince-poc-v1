// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package consent

// The channel arm of the default-deny outbound gate over a real migrated
// Postgres. A channel recipient carries no address, so before this arm existed
// the gate could only be handed an empty list for a channel send — and a
// default-deny gate asked about nobody refuses nobody, which is a whole
// transport silently exempted from suppression.

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
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

type channelConsentEnv struct {
	owner      *pgx.Conn
	store      *Store
	ctx        context.Context
	ws, user   ids.UUID
	newsletter ids.PurposeID
	doiNews    ids.PurposeID
	person     ids.PersonID
	account    string
}

func setupChannelConsent(t *testing.T) *channelConsentEnv {
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

	e := &channelConsentEnv{
		owner: owner,
		ws:    ids.NewV7(), user: ids.NewV7(),
		newsletter: ids.New[ids.PurposeKind](),
		doiNews:    ids.New[ids.PurposeKind](),
		person:     ids.New[ids.PersonKind](),
	}
	// A digit run, as Telegram reports it, and unique per run so two runs of
	// this suite cannot collide on the identity's uniqueness index.
	e.account = e.person.String()[:8]
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'ChannelConsent', $2, 'EUR')`,
		e.ws, "cc-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`,
		e.user, e.ws, "rep-"+e.user.String()+"@cc.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO consent_purpose (id, workspace_id, key, label, requires_double_opt_in)
		VALUES ($1, $3, 'newsletter', 'Newsletter', false), ($2, $3, 'doi_newsletter', 'DOI Newsletter', true)`,
		e.newsletter, e.doiNews, e.ws); err != nil {
		t.Fatal(err)
	}
	// A Telegram-only subject: no person_email row at all, which is exactly the
	// person the address-shaped gate could never answer about.
	if _, err := owner.Exec(ctx,
		`INSERT INTO person (id, workspace_id, full_name, source, captured_by)
		 VALUES ($1, $2, 'Tilda Telegram', 'connector:telegram', 'connector:telegram')`,
		e.person, e.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO person_channel_identity
		  (workspace_id, person_id, provider, channel_user_id, username, source, captured_by)
		VALUES ($1, $2, 'telegram', $3, 'tilda', 'connector:telegram', 'connector:telegram')`,
		e.ws, e.person, e.account); err != nil {
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
				"person": {Create: true, Read: true, Update: true, Delete: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return e
}

func (e *channelConsentEnv) recipient() connector.Recipient {
	return connector.Recipient{Channel: &connector.ChannelIdentity{
		Provider: "telegram", ChannelUserID: e.account, Username: "tilda",
	}}
}

func TestConsentGateRefusesAChannelRecipientWithoutAGrant(t *testing.T) {
	e := setupChannelConsent(t)
	gate := NewGate(e.store)
	rs := []connector.Recipient{e.recipient()}

	// Default-deny: the identity resolves to a real person, and that is not
	// consent.
	if err := gate.RequireGrantedForRecipients(e.ctx, rs, "newsletter"); !errors.Is(err, apperrors.ErrConsentNotGranted) {
		t.Fatalf("pre-grant channel gate: %v, want ErrConsentNotGranted", err)
	}

	if _, err := e.store.Record(e.ctx, RecordInput{
		PersonID: e.person, PurposeID: e.newsletter, NewState: "granted",
	}); err != nil {
		t.Fatal(err)
	}

	// The grant reaches the channel because it is the PERSON's grant, and the
	// channel identity resolves to that person.
	if err := gate.RequireGrantedForRecipients(e.ctx, rs, "newsletter"); err != nil {
		t.Fatalf("post-grant channel gate: %v, want pass", err)
	}
	// …and only for the purpose it was given for.
	if err := gate.RequireGrantedForRecipients(e.ctx, rs, "doi_newsletter"); !errors.Is(err, apperrors.ErrConsentNotGranted) {
		t.Fatalf("a grant for one purpose authorized another: %v", err)
	}
	// An identity nobody bound resolves to no subject, so it is refused however
	// many grants the workspace holds.
	unknown := []connector.Recipient{{Channel: &connector.ChannelIdentity{
		Provider: "telegram", ChannelUserID: e.account + "0",
	}}}
	if err := gate.RequireGrantedForRecipients(e.ctx, unknown, "newsletter"); !errors.Is(err, apperrors.ErrConsentNotGranted) {
		t.Fatalf("an unbound channel identity: %v, want ErrConsentNotGranted", err)
	}
}

// Withdrawal binds on the channel exactly as it binds on mail: this gate IS the
// suppression mechanism, and it is asked after the subject could have changed
// their mind.
func TestConsentGateRefusesAChannelRecipientAfterWithdrawal(t *testing.T) {
	e := setupChannelConsent(t)
	gate := NewGate(e.store)
	rs := []connector.Recipient{e.recipient()}

	if _, err := e.store.Record(e.ctx, RecordInput{
		PersonID: e.person, PurposeID: e.newsletter, NewState: "granted",
	}); err != nil {
		t.Fatal(err)
	}
	if err := gate.RequireGrantedForRecipients(e.ctx, rs, "newsletter"); err != nil {
		t.Fatalf("granted channel gate: %v", err)
	}
	if _, err := e.store.Record(e.ctx, RecordInput{
		PersonID: e.person, PurposeID: e.newsletter, NewState: "withdrawn",
	}); err != nil {
		t.Fatal(err)
	}
	if err := gate.RequireGrantedForRecipients(e.ctx, rs, "newsletter"); !errors.Is(err, apperrors.ErrConsentNotGranted) {
		t.Fatalf("post-withdrawal channel gate: %v, want ErrConsentNotGranted", err)
	}
}

// A recipient naming neither subject, or both, cannot be put to the gate at all.
// It is reported as the FAULT it is rather than as a withdrawal: dressed up as
// ErrConsentNotGranted, a bug that named nobody would park the send with a reason
// an operator reads as a customer's choice.
func TestConsentGateRefusesAMalformedRecipientAsAFault(t *testing.T) {
	e := setupChannelConsent(t)
	gate := NewGate(e.store)

	for _, tc := range []struct {
		name string
		r    connector.Recipient
	}{
		{"neither arm", connector.Recipient{}},
		{"both arms", connector.Recipient{Email: "buyer@example.com", Channel: e.recipient().Channel}},
		{"half a channel identity", connector.Recipient{Channel: &connector.ChannelIdentity{Provider: "telegram"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := gate.RequireGrantedForRecipients(e.ctx, []connector.Recipient{tc.r}, "newsletter")
			if !errors.Is(err, connector.ErrRecipientShape) {
				t.Fatalf("gate = %v, want ErrRecipientShape", err)
			}
			if errors.Is(err, apperrors.ErrConsentNotGranted) {
				t.Fatal("a malformed recipient was reported as an absent consent grant")
			}
		})
	}
}

// The address-shaped entry point must stay a THIN WRAPPER: mail's own gate is
// unchanged by the generalization, and the one rule now serves both.
func TestRequireGrantedForEmailsStillAnswersThroughTheSharedRule(t *testing.T) {
	e := setupChannelConsent(t)
	gate := NewGate(e.store)
	address := "tilda-" + e.person.String() + "@example.test"
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO person_email (workspace_id, person_id, email, is_primary, source, captured_by)
		 VALUES ($1, $2, lower($3), true, 'test', 'human:x')`,
		e.ws, e.person, address); err != nil {
		t.Fatal(err)
	}

	if err := gate.RequireGrantedForEmails(e.ctx, []string{address}, "newsletter"); !errors.Is(err, apperrors.ErrConsentNotGranted) {
		t.Fatalf("pre-grant mail gate: %v, want ErrConsentNotGranted", err)
	}
	if _, err := e.store.Record(e.ctx, RecordInput{
		PersonID: e.person, PurposeID: e.newsletter, NewState: "granted",
	}); err != nil {
		t.Fatal(err)
	}
	if err := gate.RequireGrantedForEmails(e.ctx, []string{address}, "newsletter"); err != nil {
		t.Fatalf("post-grant mail gate: %v, want pass", err)
	}
	if err := gate.RequireGrantedForEmails(e.ctx, nil, "newsletter"); !errors.Is(err, apperrors.ErrConsentNotGranted) {
		t.Fatalf("an empty address list: %v, want ErrConsentNotGranted", err)
	}
}
