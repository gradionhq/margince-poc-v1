// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// The race the re-key can lose, and what folding its winner in costs. Capture
// cannot learn of a send until the send is recorded, so an echo of our own
// message that arrived first already holds the identity the re-key is about to
// claim. Our row survives — it holds the delivery, the consent purpose and the
// draft outcome, none of which capture can recreate — and everything on the
// echo that a human would otherwise have to raise again moves onto it before
// the delete cascades the rest away.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedCapturedEcho writes the row the Gmail sync leaves behind when it re-reads
// this installation's own Sent copy: keyed on the identity the provider
// stamped, filed by the connector rather than by a human.
func (e *sendEnv) seedCapturedEcho(t *testing.T) ids.ActivityID {
	t.Helper()
	id := ids.New[ids.ActivityKind]()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction,
		                      source_system, source_id, source, captured_by, thread_key)
		VALUES ($1, $2, 'email', 'Re: pricing', now(), 'outbound',
		        'gmail', $3, 'gmail', 'connector:gmail', $3)`,
		id, e.ws, stampedIdentity); err != nil {
		t.Fatalf("seeding the captured echo: %v", err)
	}
	return id
}

// seedPerson writes the counterparty an auto-created record would have made.
func (e *sendEnv) seedPerson(t *testing.T, name string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by)
		VALUES ($1, $2, $3, $4, 'manual', 'human:x')`, id, e.ws, name, e.rep); err != nil {
		t.Fatalf("seeding the person: %v", err)
	}
	return id
}

// seedProject writes a project and the company it hangs off, so a project link
// has something real to point at.
func (e *sendEnv) seedProject(t *testing.T, name string) ids.UUID {
	t.Helper()
	org := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO organization (id, workspace_id, display_name, source, captured_by)
		VALUES ($1, $2, $3, 'manual', 'human:x')`, org, e.ws, name+" GmbH"); err != nil {
		t.Fatalf("seeding the company: %v", err)
	}
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO project (id, workspace_id, name, organization_id, source, captured_by)
		VALUES ($1, $2, $3, $4, 'manual', 'human:x')`, id, e.ws, name, org); err != nil {
		t.Fatalf("seeding the project: %v", err)
	}
	return id
}

// link places an activity on a record's timeline. The entity type steers the
// id into the one target column activity_link_shape permits it in — the other
// four stay NULL, which is what makes the coalesced uniqueness index mean
// "this activity, this record".
func (e *sendEnv) link(t *testing.T, activityID ids.ActivityID, entityType string, target ids.UUID) {
	t.Helper()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id, project_id)
		VALUES ($1, $2, $3,
		        CASE WHEN $3 = 'person'  THEN $4::uuid END,
		        CASE WHEN $3 = 'project' THEN $4::uuid END)`,
		e.ws, activityID, entityType, target); err != nil {
		t.Fatalf("linking the activity to a %s: %v", entityType, err)
	}
}

// queueCounterpartyReview stages the disposition capture raises for a stranger
// it could not resolve: a question waiting on a human.
func (e *sendEnv) queueCounterpartyReview(t *testing.T, activityID ids.ActivityID) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO capture_pending_counterparty
		  (id, workspace_id, email, domain, activity_id, owner_id, status, next_attempt_at)
		VALUES ($1, $2, 'buyer@example.test', 'example.test', $3, $4, 'pending', now())`,
		id, e.ws, activityID, e.rep); err != nil {
		t.Fatalf("queuing the counterparty review: %v", err)
	}
	return id
}

// reconcileAbsorbing drives the re-key and insists the collision it is about
// actually happened and was resolved: exactly one row on the stamped identity,
// and it is the survivor. Every case below asserts what the absorb did with
// some part of the echo, and each of those assertions would hold vacuously if
// the two rows had never collided at all.
func (e *sendEnv) reconcileAbsorbing(t *testing.T, survivor ids.ActivityID) {
	t.Helper()
	e.reconcile(t, survivor, stampedIdentity)

	var holders int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM activity
		 WHERE workspace_id = $1 AND source_system = 'gmail' AND source_id = $2`,
		e.ws, stampedIdentity).Scan(&holders); err != nil {
		t.Fatalf("counting activities on the stamped identity: %v", err)
	}
	if holders != 1 {
		t.Fatalf("%d activities hold the stamped identity, want exactly 1 — the duplicate row is the whole defect", holders)
	}
	if row := e.sentRow(t, survivor); row.sourceID != stampedIdentity {
		t.Fatalf("the survivor's source_id = %q, want the stamped identity %q — the echo won and our row kept a key that exists nowhere on the wire",
			row.sourceID, stampedIdentity)
	}
}

// linkedTargets reads which records an activity sits on, as the coalesced
// target the uniqueness index itself keys on.
func (e *sendEnv) linkedTargets(t *testing.T, activityID ids.ActivityID, entityType string) []ids.UUID {
	t.Helper()
	rows, err := e.owner.Query(context.Background(), `
		SELECT coalesce(person_id, organization_id, deal_id, lead_id, project_id)
		  FROM activity_link
		 WHERE activity_id = $1 AND entity_type = $2
		 ORDER BY 1`, activityID, entityType)
	if err != nil {
		t.Fatalf("reading the activity's %s links: %v", entityType, err)
	}
	defer rows.Close()
	var targets []ids.UUID
	for rows.Next() {
		var target ids.UUID
		if err := rows.Scan(&target); err != nil {
			t.Fatalf("scanning a %s link: %v", entityType, err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the activity's %s links: %v", entityType, err)
	}
	return targets
}

// The headline case: the echo won the race, so the re-key collides, absorbs it,
// and succeeds anyway. One row survives, it is OURS, and the echo's placement
// on the counterparty's record comes with it — dropping that would take the
// sent mail off the record it was sent about.
func TestReconcileAbsorbsAnEchoThatAlreadyHoldsTheStampedIdentity(t *testing.T) {
	e := setupSend(t)
	survivor := e.seedSentEmail(t, mintedIdentity)
	echo := e.seedCapturedEcho(t)
	buyer := e.seedPerson(t, "Buyer")
	e.link(t, echo, "person", buyer)

	e.reconcileAbsorbing(t, survivor)

	if row := e.sentRow(t, survivor); row.source != "manual" {
		t.Errorf("the survivor's source = %q, want manual — absorbing the echo must not relabel who wrote the message", row.source)
	}
	var echoRows int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM activity WHERE id = $1`, echo).Scan(&echoRows); err != nil {
		t.Fatalf("looking for the absorbed echo: %v", err)
	}
	if echoRows != 0 {
		t.Errorf("the absorbed echo is still on the timeline — the send appears twice")
	}
	if targets := e.linkedTargets(t, survivor, "person"); len(targets) != 1 || targets[0] != buyer {
		t.Errorf("the survivor's person links = %v, want just the buyer %s: the echo's placement on the record must move", targets, buyer)
	}
	// The delete is audited and NOT emitted: 'merge' names what happened to the
	// two rows, while the event catalog has no verb for a hard delete and
	// activity.archived would claim an archive that never happened.
	var merges int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM audit_log
		 WHERE entity_type = 'activity' AND entity_id = $1 AND action = 'merge'
		   AND after->>'merged_into_id' = $2`, echo, survivor.String()).Scan(&merges); err != nil {
		t.Fatalf("counting absorb audit rows: %v", err)
	}
	if merges != 1 {
		t.Errorf("%d merge audit rows naming the survivor, want 1 — a row deleted with no record of why is a row nobody can account for", merges)
	}
	var events int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM event_outbox
		 WHERE envelope->'entity'->>'id' = $1::text`, echo.String()).Scan(&events); err != nil {
		t.Fatalf("counting the absorbed echo's events: %v", err)
	}
	if events != 0 {
		t.Errorf("%d events about the absorbed echo, want 0 — no catalog verb says 'hard-deleted'", events)
	}
}

// Both rows sit on the same person, which is the ordinary case: the send linked
// the counterparty and capture derived the same one. Re-pointing the echo's
// copy would raise the uniqueness violation the absorb exists to answer, so it
// is left for the delete to cascade.
func TestAbsorbDoesNotDuplicateALinkTheSurvivorAlreadyHolds(t *testing.T) {
	e := setupSend(t)
	survivor := e.seedSentEmail(t, mintedIdentity)
	echo := e.seedCapturedEcho(t)
	buyer := e.seedPerson(t, "Buyer")
	e.link(t, survivor, "person", buyer)
	e.link(t, echo, "person", buyer)

	e.reconcileAbsorbing(t, survivor)

	if targets := e.linkedTargets(t, survivor, "person"); len(targets) != 1 || targets[0] != buyer {
		t.Errorf("the survivor's person links = %v, want exactly one to the buyer %s", targets, buyer)
	}
}

// uq_activity_link_project permits ONE project link per activity whatever the
// target, and 0131's ladder decides once and never overwrites. So a survivor
// that already sits on a project keeps its own, and the echo's is dropped
// rather than moved — moving it would be the overwrite the ladder refuses.
func TestAbsorbDropsTheEchosProjectLinkWhenTheSurvivorHasOne(t *testing.T) {
	e := setupSend(t)
	survivor := e.seedSentEmail(t, mintedIdentity)
	echo := e.seedCapturedEcho(t)
	ours := e.seedProject(t, "Rollout")
	theirs := e.seedProject(t, "Migration")
	e.link(t, survivor, "project", ours)
	e.link(t, echo, "project", theirs)

	e.reconcileAbsorbing(t, survivor)

	if targets := e.linkedTargets(t, survivor, "project"); len(targets) != 1 || targets[0] != ours {
		t.Errorf("the survivor's project links = %v, want only its own project %s — the ladder never overwrites a decided link", targets, ours)
	}
}

// A survivor with NO project link takes the echo's: nothing is being
// overwritten, and dropping it would lose a placement the ladder had already
// decided on evidence the survivor never saw.
func TestAbsorbTakesTheEchosProjectLinkWhenTheSurvivorHasNone(t *testing.T) {
	e := setupSend(t)
	survivor := e.seedSentEmail(t, mintedIdentity)
	echo := e.seedCapturedEcho(t)
	theirs := e.seedProject(t, "Migration")
	e.link(t, echo, "project", theirs)

	e.reconcileAbsorbing(t, survivor)

	if targets := e.linkedTargets(t, survivor, "project"); len(targets) != 1 || targets[0] != theirs {
		t.Errorf("the survivor's project links = %v, want the echo's project %s", targets, theirs)
	}
}

// capture_pending_counterparty cascades on delete, and what it holds is a
// queued human review the survivor does not re-queue. It must move with the
// absorb, or the question about who this stranger is disappears with the row
// that raised it.
func TestAbsorbRePointsAQueuedCounterpartyReview(t *testing.T) {
	e := setupSend(t)
	survivor := e.seedSentEmail(t, mintedIdentity)
	echo := e.seedCapturedEcho(t)
	review := e.queueCounterpartyReview(t, echo)

	e.reconcileAbsorbing(t, survivor)

	var on ids.ActivityID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT activity_id FROM capture_pending_counterparty WHERE id = $1`, review).Scan(&on); err != nil {
		t.Fatalf("reading the queued review back: %v — it cascaded away with the echo", err)
	}
	if on != survivor {
		t.Errorf("the queued review points at %s, want the surviving send %s", on, survivor)
	}
}
