// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// Attribution, driven through the REAL writer.
//
// Every other test in this tree calls capture.Trace with a UserID it chose,
// which proves the column stores what it is handed and nothing about what the
// pipeline hands it. That column is the access-control axis of this whole
// feature — set means one member's, NULL means a manager may read it — so the
// value has to be proven from a Sink.Upsert, not from a fixture.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// traceOwnerFor reads back who the pipeline attributed a message to.
func traceOwnerFor(ctx context.Context, t *testing.T, db *database.DB, sourceID string) (*ids.UUID, string) {
	t.Helper()
	var owner *ids.UUID
	var outcome string
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT user_id, outcome FROM capture_trace WHERE source_id = $1`, sourceID).
			Scan(&owner, &outcome)
	}); err != nil {
		t.Fatalf("reading the trace: %v", err)
	}
	return owner, outcome
}

// A personal connection's capture is attributed to the member whose credential
// produced it — the value that keeps it out of every manager's view.
func TestAPersonalCaptureIsAttributedToItsMember(t *testing.T) {
	ctx, db := bootstrapInternalMailWorkspace(t)
	ws, _ := principal.WorkspaceID(ctx)
	member := ids.NewV7()

	seedMember(ctx, t, db, member)

	sink := capture.NewSink(db)
	if _, err := sink.Upsert(memberSinkContext(ctx, ws, member),
		mailRecord("attr-1", "dana@client.io", "dana@client.io", "rep@acme.com")); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	owner, outcome := traceOwnerFor(ctx, t, db, "attr-1")
	if outcome != string(capture.TraceCaptured) && outcome != string(capture.TraceDeferred) {
		t.Errorf("outcome = %q, want the message to have landed", outcome)
	}
	if owner == nil {
		t.Fatal("user_id = NULL for a personal connection — that value is what a manager may read")
	}
	if *owner != member {
		t.Errorf("user_id = %v, want the member whose credential produced it (%v)", *owner, member)
	}
}

// The fallback exists because a principal may carry UserID without OnBehalfOf.
// If the derivation stopped using it, personal rows would silently become
// workspace-readable — which no other test would notice.
func TestAPrincipalWithOnlyAUserIdStillAttributes(t *testing.T) {
	ctx, db := bootstrapInternalMailWorkspace(t)
	ws, _ := principal.WorkspaceID(ctx)
	member := ids.NewV7()

	seedMember(ctx, t, db, member)

	sink := capture.NewSink(db)
	if _, err := sink.Upsert(userIDOnlySinkContext(ctx, ws, member),
		mailRecord("attr-2", "dana@client.io", "dana@client.io", "rep@acme.com")); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	owner, _ := traceOwnerFor(ctx, t, db, "attr-2")
	if owner == nil || *owner != member {
		t.Errorf("user_id = %v, want %v — capturePrincipal's fallback is what keeps this row personal", owner, member)
	}
}

// An internal drop is traced too, and attributed: it is the case a member most
// often comes to this screen to ask about.
func TestAnInternalDropIsTracedAndAttributed(t *testing.T) {
	ctx, db := bootstrapInternalMailWorkspace(t, "acme.com")
	ws, _ := principal.WorkspaceID(ctx)
	member := ids.NewV7()

	seedMember(ctx, t, db, member)

	sink := capture.NewSink(db)
	// Both parties on the workspace's own domain.
	_, err := sink.Upsert(memberSinkContext(ctx, ws, member),
		mailRecord("attr-3", "colleague@acme.com", "colleague@acme.com", "rep@acme.com"))
	// The sentinel, not merely non-nil: a failure in the trace write would also
	// be non-nil, and this test would then blame the read below on it.
	if !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("Upsert of colleague-only mail = %v, want a skip", err)
	}

	owner, outcome := traceOwnerFor(ctx, t, db, "attr-3")
	if outcome != string(capture.TraceInternal) {
		t.Errorf("outcome = %q, want %q", outcome, capture.TraceInternal)
	}
	if owner == nil || *owner != member {
		t.Errorf("user_id = %v, want the member whose mailbox it was (%v)", owner, member)
	}
}

// memberSinkContext is a capture connector acting FOR a member, the way every
// personal connection's ingest does: OnBehalfOf and UserID both set.
func memberSinkContext(ctx context.Context, ws, member ids.UUID) context.Context {
	return sinkContextFor(ctx, ws, member, member)
}

// seedMember makes the member real. audit_log.on_behalf_of is a foreign key, so
// a capture acting for an invented id fails on the audit row rather than on
// anything this test is about.
func seedMember(ctx context.Context, t *testing.T, db *database.DB, member ids.UUID) {
	t.Helper()
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO app_user (id, email, display_name, status)
			VALUES ($1, $2, 'Member', 'active')
			ON CONFLICT (id) DO NOTHING`, member, "member-"+member.String()+"@example.test")
		return err
	}); err != nil {
		t.Fatalf("seeding the member: %v", err)
	}
}

// userIDOnlySinkContext sets UserID and leaves OnBehalfOf zero — the shape
// capturePrincipal's fallback exists for.
func userIDOnlySinkContext(ctx context.Context, ws, member ids.UUID) context.Context {
	return sinkContextFor(ctx, ws, member, ids.Nil)
}

func sinkContextFor(ctx context.Context, ws, userID, onBehalfOf ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalConnector,
		ID:         "connector:imap",
		UserID:     userID,
		OnBehalfOf: onBehalfOf,
		Permissions: principal.Permissions{
			RoleKeys: []string{"capture"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true},
				"person":   {Create: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}
