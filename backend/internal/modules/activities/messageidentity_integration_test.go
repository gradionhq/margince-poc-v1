// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// What a re-key does to a timeline row, and what it deliberately leaves alone.
// A root send re-roots onto the identity the world will actually reply to; a
// reply keeps the conversation root it joined; and neither stops being a
// message a human wrote. The harness is email_integration_test.go's sendEnv —
// the same workspace, the same owner connection seeding rows the store did not
// write.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The two identities every case here is about: what this system minted and
// staged the message under, and what the provider actually put on the wire.
const (
	mintedIdentity  = "019fad38-minted@margince.test"
	stampedIdentity = "CAFAR1txEuKW@mail.gmail.com"
)

// sentRow is the timeline row as the re-key leaves it. version is read with
// the rest because the no-op case is about the row being untouched, and an
// UPDATE that wrote the same values back would still bump it (the
// set_updated_at_bump_version trigger fires on every UPDATE, not on a change).
type sentRow struct {
	sourceSystem string
	sourceID     string
	threadKey    string
	source       string
	version      int64
}

// seedSentEmail writes the row the send path leaves behind: outbound, filed
// under the provider whose echo must collapse onto it, keyed on the identity
// this system minted, and authored by a human ('manual').
func (e *sendEnv) seedSentEmail(t *testing.T, sourceID, threadKey string) ids.ActivityID {
	t.Helper()
	id := ids.New[ids.ActivityKind]()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction,
		                      source_system, source_id, source, captured_by, thread_key)
		VALUES ($1, $2, 'email', 'Re: pricing', now(), 'outbound',
		        'gmail', $3, 'manual', 'human:x', NULLIF($4, ''))`,
		id, e.ws, sourceID, threadKey); err != nil {
		t.Fatalf("seeding the sent email: %v", err)
	}
	return id
}

func (e *sendEnv) sentRow(t *testing.T, id ids.ActivityID) sentRow {
	t.Helper()
	var row sentRow
	if err := e.owner.QueryRow(context.Background(), `
		SELECT coalesce(source_system, ''), coalesce(source_id, ''),
		       coalesce(thread_key, ''), source, version
		  FROM activity WHERE id = $1`, id).Scan(
		&row.sourceSystem, &row.sourceID, &row.threadKey, &row.source, &row.version); err != nil {
		t.Fatalf("reading the sent email back: %v", err)
	}
	return row
}

// asSendWorker binds the scope the dispatch job runs the reconcile under: the
// system completing a send a human already authorized, with the correlation id
// storekit.Emit requires. It is NOT the sender's seat — a seat revoked between
// staging and transmit must not strand the message's identity.
func (e *sendEnv) asSendWorker() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:comms-send",
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// reconcile drives the seam the way comms drives it: inside a workspace-bound
// transaction the caller owns.
func (e *sendEnv) reconcile(t *testing.T, id ids.ActivityID, previous, stamped string) {
	t.Helper()
	ctx := e.asSendWorker()
	store := NewStore(e.pool)
	if err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		return store.ReconcileMessageIdentityTx(ctx, tx, id, previous, stamped)
	}); err != nil {
		t.Fatalf("ReconcileMessageIdentityTx: %v", err)
	}
}

// A root send's thread_key IS its own identity, so both move together. A root
// that kept the minted key would be invisible to the reply that quotes the
// stamped one — reply detection joins outbound activities on thread_key.
func TestReconcileReKeysARootSendOntoTheStampedIdentity(t *testing.T) {
	e := setupSend(t)
	id := e.seedSentEmail(t, mintedIdentity, mintedIdentity)

	e.reconcile(t, id, mintedIdentity, stampedIdentity)

	row := e.sentRow(t, id)
	if row.sourceID != stampedIdentity {
		t.Errorf("source_id = %q, want the identity the provider stamped (%q) — the echo collapses on this key",
			row.sourceID, stampedIdentity)
	}
	if row.threadKey != stampedIdentity {
		t.Errorf("thread_key = %q, want %q: a root re-roots onto the identity the world will reply to",
			row.threadKey, stampedIdentity)
	}
	// The write shape: domain row + audit_log row + event_outbox row, one
	// transaction. Without the audit row the operator has no record that this
	// mailbox rewrites identities at all.
	var audits int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM audit_log
		 WHERE entity_type = 'activity' AND entity_id = $1 AND action = 'update'
		   AND before->>'source_id' = $2 AND after->>'source_id' = $3`,
		id, mintedIdentity, stampedIdentity).Scan(&audits); err != nil {
		t.Fatalf("counting audit rows: %v", err)
	}
	if audits != 1 {
		t.Errorf("%d audit rows naming both identities, want 1", audits)
	}
	var events int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM event_outbox
		 WHERE envelope->>'type' = 'activity.updated'
		   AND envelope->'entity'->>'id' = $1::text`, id.String()).Scan(&events); err != nil {
		t.Fatalf("counting outbox events: %v", err)
	}
	if events != 1 {
		t.Errorf("%d activity.updated events, want 1 — a read model still pointing at the dead identity is a 404", events)
	}
}

// thread_key != source_id means this message JOINED a conversation. Moving it
// would file that conversation under this message and break the join the
// thread key exists for.
func TestReconcileLeavesAReplysThreadKeyOnItsAnchorsRoot(t *testing.T) {
	e := setupSend(t)
	const root = "root@buyer.test"
	id := e.seedSentEmail(t, mintedIdentity, root)

	e.reconcile(t, id, mintedIdentity, stampedIdentity)

	row := e.sentRow(t, id)
	if row.sourceID != stampedIdentity {
		t.Errorf("source_id = %q, want %q — a reply is re-keyed like any other message", row.sourceID, stampedIdentity)
	}
	if row.threadKey != root {
		t.Errorf("thread_key = %q, want the anchor's conversation root %q", row.threadKey, root)
	}
}

// Re-keying the transport identity does not change who wrote the message.
// Rewriting source would relabel the workspace's own correspondence as
// connector-ingested.
func TestReconcileLeavesSourceManual(t *testing.T) {
	e := setupSend(t)
	id := e.seedSentEmail(t, mintedIdentity, mintedIdentity)

	e.reconcile(t, id, mintedIdentity, stampedIdentity)

	row := e.sentRow(t, id)
	if row.source != "manual" {
		t.Errorf("source = %q, want manual — a human wrote this message before and after the re-key", row.source)
	}
	if row.sourceSystem != "gmail" {
		t.Errorf("source_system = %q, want gmail: the echo's natural key names the system, and it did not change", row.sourceSystem)
	}
}

// stamped == previous is the provider that honoured the identity, which is
// every provider but this one. Nothing happened, so nothing may be written —
// an UPDATE writing the same values back would still bump version and emit an
// event about a change nobody made.
func TestReconcileIsANoOpWhenTheProviderHonouredTheIdentity(t *testing.T) {
	e := setupSend(t)
	id := e.seedSentEmail(t, mintedIdentity, mintedIdentity)
	before := e.sentRow(t, id)

	e.reconcile(t, id, mintedIdentity, mintedIdentity)

	if after := e.sentRow(t, id); after != before {
		t.Errorf("row = %+v, want it untouched (%+v)", after, before)
	}
	var writes int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE entity_type = 'activity' AND entity_id = $1`,
		id).Scan(&writes); err != nil {
		t.Fatalf("counting audit rows: %v", err)
	}
	if writes != 0 {
		t.Errorf("%d audit rows for a re-key that changed nothing, want 0", writes)
	}
}
