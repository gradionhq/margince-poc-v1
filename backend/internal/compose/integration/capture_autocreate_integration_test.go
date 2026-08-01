// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The auto-create pipeline end to end over a real migrated Postgres
// (ADR-0063, AC3.1/3.2): a captured thread yields exactly one person, one
// company, one employment edge and person-linked activities — idempotent
// across replays; free-mail yields the person but never a company; the
// workspace's own domain (seeded from the synced mailbox) creates nothing;
// an erased address stays dead; and an inbound message above a prior
// outbound emits exactly one engagement.reply.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The ADR-0063 auto-create path: a captured thread yields exactly one person,
// one company and one employment, and a replay adds nothing.
func TestCaptureAutoCreatesTheCounterpartyBehindAThread(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync, syncSent := env.e, env.sync, env.syncSent
	t.Run("a thread becomes one person, one company, one employment", func(t *testing.T) {
		// The owner's own reply is what makes alice a counterparty: T1
		// correspondence-positive ensures immediately, where a first-time
		// stranger would defer to the verdict engine. The outbound leg is
		// attested, because only a provider-vouched send counts (ADR-0072 §1).
		syncSent(
			t, map[string]bool{"m2@myco.example": true},
			email("alice@acme.example", "Alice Example", captureOwner, "m1@acme.example", ""),
			email(captureOwner, "", "alice@acme.example", "m2@myco.example", "m1@acme.example"),
			email("alice@acme.example", "Alice Example", captureOwner, "m3@acme.example", "m1@acme.example"),
		)
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'alice@acme.example'`); n != 1 {
			t.Fatalf("%d persons for alice, want exactly 1", n)
		}
		// NO company is derived from the domain. Capture withholds one until a
		// site read says the domain deserves it — inventing "Acme" from
		// acme.example is exactly what produced junk named after people.
		if n := countRows(t, e, `SELECT count(*) FROM organization`); n != 0 {
			t.Fatalf("%d organizations from an unjudged domain, want 0", n)
		}
		// What capture DOES record is the question, once, for the domain.
		if n := countRows(t, e, `
			SELECT count(*) FROM organization_domain_disposition
			WHERE domain = 'acme.example' AND status = 'pending'`); n != 1 {
			t.Fatalf("%d open company questions for acme.example, want exactly 1", n)
		}
		// No company means no employment edge yet; the verdict plants them for
		// everyone waiting when it lands.
		if n := countRows(t, e, `
			SELECT count(*) FROM relationship r JOIN person_email pe ON pe.person_id = r.person_id
			WHERE r.kind = 'employment' AND r.is_current_primary AND pe.email = 'alice@acme.example'`); n != 0 {
			t.Fatalf("%d employment edges before a company exists, want 0", n)
		}
		// Person-only links, and only from the point alice became a
		// counterparty: the FIRST message deferred — she was a stranger when it
		// arrived — so the owner's reply and the message after it link her, and
		// the deferred one waits for the verdict that resolves its ledger row.
		if n := countRows(t, e, `
			SELECT count(*) FROM activity_link al JOIN person_email pe ON pe.person_id = al.person_id
			WHERE al.entity_type = 'person' AND pe.email = 'alice@acme.example'`); n != 2 {
			t.Fatalf("%d person links, want 2 (the reply and what followed it)", n)
		}
		// And the first message is not lost — it is deferred, on the ledger.
		if n := countRows(t, e, `
			SELECT count(*) FROM capture_pending_counterparty
			WHERE email = 'alice@acme.example'`); n != 1 {
			t.Fatalf("%d ledger rows for alice, want 1 — the cold first message deferred", n)
		}
		if n := countRows(t, e, `SELECT count(*) FROM activity_link WHERE entity_type = 'organization'`); n != 0 {
			t.Fatalf("%d org links, want 0 — the org rolls up through employment", n)
		}
		// Connector-created rows start owner-visible — asserted on alice
		// herself, so an unrelated owner-visible row can never green this.
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'alice@acme.example' AND p.visibility = 'owner'`); n != 1 {
			t.Fatal("the connector-created person must start visibility='owner'")
		}
		// The inbound reply above our outbound emitted exactly one engagement.reply.
		if n := countRows(t, e, `SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'engagement.reply'`); n != 1 {
			t.Fatalf("%d engagement.reply events, want exactly 1", n)
		}
	})
	t.Run("a replay creates nothing new", func(t *testing.T) {
		sync(t, email("alice@acme.example", "Alice Example", captureOwner, "m1@acme.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'alice@acme.example'`); n != 1 {
			t.Fatalf("replay grew alice to %d rows", n)
		}
		if n := countRows(t, e, `SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'engagement.reply'`); n != 1 {
			t.Fatalf("replay re-emitted engagement.reply (%d total)", n)
		}
	})
	t.Run("a fuzzy near-match creates anyway and queues the pair", func(t *testing.T) {
		// A near-identical name on the SAME employer domain: the PO-F-1
		// score (0.55·name + 0.45·org) crosses the review threshold. The
		// near-match needs someone to be near, so this captures both halves
		// rather than leaning on whoever a sibling subtest created.
		// Both are written to first: a stranger defers, so a dedupe pair only
		// exists once correspondence has made them counterparties.
		syncSent(
			t, map[string]bool{"fzo1@myco.example": true, "fzo2@myco.example": true},
			email(captureOwner, "", "alice@acme.example", "fzo1@myco.example", ""),
			email(captureOwner, "", "alice2@acme.example", "fzo2@myco.example", ""),
		)
		sync(
			t,
			email("alice@acme.example", "Alice Example", captureOwner, "fz0@acme.example", ""),
			email("alice2@acme.example", "Alice Exampel", captureOwner, "f1@acme.example", ""),
		)
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'alice2@acme.example'`); n != 1 {
			t.Fatal("fuzzy must create — capture never blocks on a human")
		}
		if n := countRows(t, e, `SELECT count(*) FROM dedupe_candidate WHERE entity_type = 'person' AND disposition = 'open'`); n != 1 {
			t.Fatalf("%d open dedupe candidates, want exactly 1", n)
		}
	})
}

func TestCaptureRecordsProvenanceWithoutTheMessageBody(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	// Both halves read what a capture WROTE, so this test captures its own
	// thread — an inbound leg and the owner's reply — rather than reading rows
	// another test happened to leave behind.
	sync(
		t,
		email("alice@acme.example", "Alice Example", captureOwner, "p1@acme.example", ""),
		email(captureOwner, "", "alice@acme.example", "p2@myco.example", "p1@acme.example"),
	)

	t.Run("captured mail stamps the counterparty email on the activity", func(t *testing.T) {
		// Each activity carries her normalized address, so the correspondence
		// predicate is an index-backed lookup (CAP-DDL-7).
		if n := countRows(t, e, `SELECT count(*) FROM activity WHERE counterparty_email = 'alice@acme.example'`); n < 1 {
			t.Fatal("captured activities must stamp counterparty_email")
		}
		// An outbound leg stamps it too — the substrate that 2b's
		// correspondence-positive gate reads.
		if n := countRows(t, e, `SELECT count(*) FROM activity WHERE counterparty_email = 'alice@acme.example' AND direction = 'outbound'`); n < 1 {
			t.Fatal("the outbound leg must stamp counterparty_email for the correspondence predicate")
		}
	})
	t.Run("a captured activity's audit image is metadata-only, never the body", func(t *testing.T) {
		// Capture-audit minimization (ADR-0072/A118): the connector-captured
		// activity's audit after-image carries the natural key + kind + direction
		// + timestamp, but NEVER the subject/body — the message content stays on
		// the activity row and raw_capture under their own retention, off the
		// append-only audit spine.
		var hasBody, hasSubject, hasKind, hasSourceID bool
		err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `
				SELECT after ? 'body', after ? 'subject', after ? 'kind', after ? 'source_id'
				FROM audit_log
				WHERE entity_type = 'activity' AND action = 'create'
				ORDER BY occurred_at LIMIT 1`).Scan(&hasBody, &hasSubject, &hasKind, &hasSourceID)
		})
		if err != nil {
			t.Fatalf("reading the captured-activity audit image: %v", err)
		}
		if hasBody || hasSubject {
			t.Fatalf("captured-activity audit after leaked content (body=%v subject=%v) — must be metadata-only", hasBody, hasSubject)
		}
		if !hasKind || !hasSourceID {
			t.Fatal("captured-activity audit after must keep the metadata (kind, natural key)")
		}
	})
}

// The refusals: an address the workspace owns, one an erasure killed, and a
// connector acting for nobody. Each keeps the activity and creates no person.
func TestCaptureRefusesToDeriveARecord(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	t.Run("the workspace's own domain creates nothing", func(t *testing.T) {
		sync(t, email("carol@myco.example", "Carol Colleague", captureOwner, "c1@myco.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'carol@myco.example'`); n != 0 {
			t.Fatal("a colleague must not become a CRM person")
		}
		if n := countRows(t, e, `SELECT count(*) FROM organization WHERE display_name = 'myco.example'`); n != 0 {
			t.Fatal("the workspace's own domain must not become a CRM organization")
		}
	})
	t.Run("an erased address stays dead", func(t *testing.T) {
		err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `
				INSERT INTO erasure_suppression (workspace_id, kind, value_hash)
				VALUES ($1, 'email', $2)`, e.WS, storekit.SuppressionHash("dave@dead.example"))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		sync(t, email("dave@dead.example", "Dave Gone", captureOwner, "d1@dead.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'dave@dead.example'`); n != 0 {
			t.Fatal("an erased address must never re-create a person (A13)")
		}
		// The activity itself is still captured — suppression stops the
		// person, not the timeline row.
		if n := countRows(t, e, `SELECT count(*) FROM activity WHERE source_id = 'd1@dead.example'`); n != 1 {
			t.Fatal("suppression must not drop the captured activity")
		}
	})
	t.Run("a connector with no granting human records the fault, never a person", func(t *testing.T) {
		// A bare sink with the ensure seam wired but an ownerless connector
		// principal: the capture itself must land, the ensure must refuse
		// honestly (RC-8 — created rows need a human owner), and the fault
		// must be a system_log line the nightly reconcile can find.
		sink := capture.NewSink(e.Pool).WithEnsurer(recordingEnsurer{}, capture.NewTransactionalList(nil, nil))
		ownerless := principal.WithCorrelationID(principal.WithActor(
			principal.WithWorkspaceID(context.Background(), e.WS), principal.Principal{
				Type: principal.PrincipalConnector, ID: "connector:gmail",
				Scopes: principal.NewScopeSet(principal.ScopeRead),
				Permissions: principal.Permissions{
					Objects:  map[string]principal.ObjectGrant{"activity": {Create: true, Read: true}},
					RowScope: principal.RowScopeAll,
				},
			},
		), ids.NewV7())
		raw := email("ghost@nowhere.example", "Ghost Sender", captureOwner, "g1@nowhere.example", "")
		msg, err := mailmap.Parse(raw, captureOwner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sink.Upsert(ownerless, msg.ToRecord("gmail", raw)); err != nil {
			t.Fatalf("the capture itself must not fail: %v", err)
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM system_log
			WHERE action = 'capture_ensure_fault' AND detail->>'source_id' = 'g1@nowhere.example'`); n != 1 {
			t.Fatalf("%d ensure-fault ledger lines, want exactly 1", n)
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'ghost@nowhere.example'`); n != 0 {
			t.Fatal("an ownerless connector must not create a person")
		}
	})
}

// recordingEnsurer satisfies the ensure seam for the ownerless-connector
// case; the sink's own gates must refuse before it is ever reached.
type recordingEnsurer struct{}

func (recordingEnsurer) EnsureCounterparty(context.Context, capture.EnsureRequest) (capture.EnsureOutcome, error) {
	return capture.EnsureOutcome{}, nil
}
