// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The one decision whose trace cannot ride the capture transaction.
//
// skipInvisibleIncumbent is returned as an ERROR from inside that transaction,
// so anything written there rolls back with it — and this is precisely an
// outcome a member needs explained, because from their side a message sitting
// in their own mailbox simply never arrives.

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

func TestAnInvisibleIncumbentIsTracedOnItsOwnTransaction(t *testing.T) {
	ctx, db := bootstrapInternalMailWorkspace(t)
	sink := capture.NewSink(db)
	ws, _ := principal.WorkspaceID(ctx)

	// First landing: an ordinary capture, which creates the incumbent.
	rec := mailRecord("m-incumbent", "dana@client.io", "dana@client.io", "rep@acme.com")
	if _, err := sink.Upsert(ctx, rec); err != nil {
		t.Fatalf("first capture: %v", err)
	}
	// Move it out of every reader's reach by linking it to a person somebody
	// else captured privately, then replay: the replay resolves onto a row this principal
	// cannot see, which is the refusal under test.
	hideIncumbent(ctx, t, db, "m-incumbent")

	// Replayed by a connector acting for a member whose row scope is their OWN
	// records. The first capture ran unbounded, which is how the incumbent came
	// to exist at all; what this asserts is the reader that cannot see it.
	_, err := sink.Upsert(ownScopedSinkContext(ctx, ws), rec)
	if !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("replay error = %v, want a skip", err)
	}

	// The capture transaction rolled back. The trace must have survived it.
	var outcome, reason string
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT outcome, coalesce(reason, '') FROM capture_trace
			 WHERE source_id = 'm-incumbent' AND outcome = 'fault'`).Scan(&outcome, &reason)
	}); err != nil {
		t.Fatalf("reading the trace: %v — a trace written inside the doomed transaction would be gone", err)
	}
	if reason != capture.TraceReasonInvisibleIncumbent {
		t.Errorf("reason = %q, want %q", reason, capture.TraceReasonInvisibleIncumbent)
	}
}

// hideIncumbent links a captured activity to a stranger's capture-private
// person (visibility='owner'), which is what puts it outside this principal's
// row scope: a person is workspace-readable identity, so ownership alone hides
// nothing, and an activity has no owner of its own — it inherits the
// sensitivity of what it attaches to.
func hideIncumbent(ctx context.Context, t *testing.T, db *database.DB, sourceID string) {
	t.Helper()
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		stranger, personID := ids.NewV7(), ids.NewV7()
		if _, err := tx.Exec(ctx, `
			INSERT INTO app_user (id, email, display_name, status)
			VALUES ($1, $2, 'Stranger', 'active')`, stranger, "stranger-"+stranger.String()+"@example.test"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO person (id, full_name, owner_id, visibility, source, captured_by)
			VALUES ($1, 'Stranger', $2, 'owner', 'manual', 'human:test')`,
			personID, stranger); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO activity_link (activity_id, entity_type, person_id)
			SELECT id, 'person', $2 FROM activity WHERE source_id = $1`,
			sourceID, personID)
		return err
	}); err != nil {
		t.Fatalf("hiding the incumbent: %v", err)
	}
}

// ownScopedSinkContext is a capture connector acting for a member who may see
// only their own records — the ordinary rep seat, and the one for which an
// incumbent linked to somebody else's capture-private person is out of reach.
func ownScopedSinkContext(ctx context.Context, ws ids.UUID) context.Context {
	member := ids.NewV7()
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalConnector,
		ID:         "connector:imap",
		UserID:     member,
		OnBehalfOf: member,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true, Read: true},
				"person":   {Create: true, Read: true},
			},
			RowScope: principal.RowScopeOwn,
		},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}
