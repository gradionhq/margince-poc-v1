// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// The race the re-key can lose, and what folding its winner in costs. Capture
// cannot learn of a send until the send is recorded, so an echo of our own
// message that arrived first already holds the identity the re-key is about to
// claim. Our row survives — it holds the delivery, the consent purpose and the
// draft outcome, none of which capture can recreate — and the echo gives up its
// natural key and its place on the timeline, keeping everything else.
//
// Three properties the cases below pin, because each is one a refactor could
// undo without any of the others noticing: the survivor GAINS what the echo
// derived, the echo KEEPS the links erasure reaches its own derived rows by,
// and only a row shaped like this message's echo may be folded in at all.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedCapturedEcho writes the row the Gmail sync leaves behind when it re-reads
// this installation's own Sent copy: an outbound email keyed on the identity
// the provider stamped, with the connector's own provenance — 'gmail:<id>' and
// 'connector:gmail' are exactly what capture's sink writes for it, and they are
// what the absorb reads to tell an echo from any other row that happens to hold
// the key.
func (e *sendEnv) seedCapturedEcho(t *testing.T) ids.ActivityID {
	t.Helper()
	return e.seedEcho(t, echoSeed{
		workspace: e.ws, direction: "outbound", kind: "email",
		source: "gmail:" + stampedIdentity, capturedBy: "connector:gmail",
		counterparty: counterparty,
	})
}

// echoSeed is one candidate row on the stamped natural key. Every field it
// exposes is one the absorb's lookup reads, so a case can seed a row that holds
// the key and is NOT this message's echo.
type echoSeed struct {
	workspace    ids.UUID
	direction    string
	kind         string
	source       string
	capturedBy   string
	counterparty string
}

func (e *sendEnv) seedEcho(t *testing.T, seed echoSeed) ids.ActivityID {
	t.Helper()
	id := ids.New[ids.ActivityKind]()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction,
		                      source_system, source_id, source, captured_by, thread_key,
		                      counterparty_email)
		VALUES ($1, $2, $3, 'Re: pricing', now(), $4,
		        'gmail', $5, $6, $7, $5, NULLIF($8, ''))`,
		id, seed.workspace, seed.kind, seed.direction, stampedIdentity, seed.source, seed.capturedBy,
		seed.counterparty); err != nil {
		t.Fatalf("seeding the row on the stamped identity: %v", err)
	}
	return id
}

// absorbedEcho is what the fold leaves of the row it took in: off the timeline,
// its natural key released, everything else still there.
type absorbedEcho struct {
	sourceSystem string
	sourceID     string
	archived     bool
}

func (e *sendEnv) absorbedRow(t *testing.T, id ids.ActivityID) absorbedEcho {
	t.Helper()
	var row absorbedEcho
	if err := e.owner.QueryRow(context.Background(), `
		SELECT coalesce(source_system, ''), coalesce(source_id, ''), archived_at IS NOT NULL
		  FROM activity WHERE id = $1`, id).Scan(
		&row.sourceSystem, &row.sourceID, &row.archived); err != nil {
		t.Fatalf("reading the absorbed echo back: %v — the absorb destroyed a row it may only fold in", err)
	}
	return row
}

// seedNeighbourWorkspace is a second tenant on the same database — the only
// way to seed a row this workspace's transaction must not be able to see.
func (e *sendEnv) seedNeighbourWorkspace(t *testing.T) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Neighbour', $2, 'EUR')`,
		id, "neighbour-"+id.String()); err != nil {
		t.Fatalf("seeding the neighbouring workspace: %v", err)
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
	// The echo RELEASES the identity and goes off the timeline; it is not
	// destroyed. Deleting it would take with it every row that hangs off an
	// activity and is reachable no other way — the attachments and their
	// object-store bytes, the provenance and the embeddings — none of which
	// erasure or retention could then ever reach. A sent email is also
	// correspondence under a statutory retention floor, which a de-duplication
	// establishes no ground to cross.
	absorbed := e.absorbedRow(t, echo)
	if !absorbed.archived {
		t.Errorf("the absorbed echo is still on the timeline — the send appears twice")
	}
	if absorbed.sourceSystem != "" || absorbed.sourceID != "" {
		t.Errorf("the absorbed echo still holds (%q, %q) — the partial natural-key index would refuse the survivor",
			absorbed.sourceSystem, absorbed.sourceID)
	}
	if targets := e.linkedTargets(t, survivor, "person"); len(targets) != 1 || targets[0] != buyer {
		t.Errorf("the survivor's person links = %v, want just the buyer %s: the echo's placement on the record must move", targets, buyer)
	}
	// The archive is audited AS an archive — the verb the row's own state now
	// carries — with the folded-in identity and the row it went into as the
	// evidence of WHY. A row taken off the timeline with no record of why is a
	// row nobody can account for.
	var archives int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM audit_log
		 WHERE entity_type = 'activity' AND entity_id = $1 AND action = 'archive'
		   AND after->>'merged_into_id' = $2`, echo, survivor.String()).Scan(&archives); err != nil {
		t.Fatalf("counting absorb audit rows: %v", err)
	}
	if archives != 1 {
		t.Errorf("%d archive audit rows naming the survivor, want 1", archives)
	}
	// And the event that goes with it. A subscriber was told activity.captured
	// about this row; without activity.archived it is holding an entity nothing
	// ever tells it to stop showing.
	var archived int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM event_outbox
		 WHERE envelope->>'type' = 'activity.archived'
		   AND envelope->'entity'->>'id' = $1::text`, echo.String()).Scan(&archived); err != nil {
		t.Fatalf("counting the absorbed echo's events: %v", err)
	}
	if archived != 1 {
		t.Errorf("%d activity.archived events for the absorbed echo, want 1", archived)
	}
}

// THE ERASURE REACH, and the reason the echo is archived rather than deleted.
// Subject-scoped Art. 17 erasure walks from a person to an activity's
// attachments, provenance and embeddings THROUGH activity_link. An archived row
// stripped of its links is therefore a row whose derived evidence that walk no
// longer finds — the same defect as the hard delete, in a smaller shape. So the
// placement is COPIED: the survivor gains it, the echo does not lose it. The
// archived row shows on no timeline, so keeping the link cannot show the
// message twice.
func TestAbsorbLeavesTheEchoTheLinksErasureReachesItBy(t *testing.T) {
	e := setupSend(t)
	survivor := e.seedSentEmail(t, mintedIdentity)
	echo := e.seedCapturedEcho(t)
	buyer := e.seedPerson(t, "Buyer")
	e.link(t, echo, "person", buyer)

	e.reconcileAbsorbing(t, survivor)

	if targets := e.linkedTargets(t, echo, "person"); len(targets) != 1 || targets[0] != buyer {
		t.Errorf("the absorbed echo's person links = %v, want its own link to %s kept — erasure reaches this row's attachments and embeddings through it",
			targets, buyer)
	}
	if targets := e.linkedTargets(t, survivor, "person"); len(targets) != 1 || targets[0] != buyer {
		t.Errorf("the survivor's person links = %v, want the placement on its own record too", targets)
	}
}

// Both rows sit on the same person, which is the ordinary case: the send linked
// the counterparty and capture derived the same one. Copying the echo's link
// would raise the uniqueness violation the absorb exists to answer, so the
// survivor keeps the one it has and the echo keeps its own.
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
// that already sits on a project keeps its own and the echo's is not copied —
// copying it could not even be written, and taking it would be the overwrite
// the ladder refuses.
func TestAbsorbLeavesTheEchosProjectLinkBehindWhenTheSurvivorHasOne(t *testing.T) {
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
	// The echo KEEPS the link that was not copied. Links are copied rather than
	// moved precisely so the archived echo stays reachable by the subject-scoped
	// erasure join; a project link left on neither row would put the echo's
	// attachments and provenance out of Art. 17's reach.
	if targets := e.linkedTargets(t, echo, "project"); len(targets) != 1 || targets[0] != theirs {
		t.Errorf("the echo's project links = %v, want its own project %s retained — erasure reaches the archived echo through its links", targets, theirs)
	}
}

// A survivor with NO project link gets the echo's: nothing is being
// overwritten, and skipping it would lose a placement the ladder had already
// decided on evidence the survivor never saw.
func TestAbsorbGivesTheSurvivorTheEchosProjectLinkWhenItHasNone(t *testing.T) {
	e := setupSend(t)
	survivor := e.seedSentEmail(t, mintedIdentity)
	echo := e.seedCapturedEcho(t)
	theirs := e.seedProject(t, "Migration")
	e.link(t, echo, "project", theirs)

	e.reconcileAbsorbing(t, survivor)

	if targets := e.linkedTargets(t, survivor, "project"); len(targets) != 1 || targets[0] != theirs {
		t.Errorf("the survivor's project links = %v, want the echo's project %s", targets, theirs)
	}
	if targets := e.linkedTargets(t, echo, "project"); len(targets) != 1 || targets[0] != theirs {
		t.Errorf("the absorbed echo's project links = %v, want its own kept: the placement is copied, not taken", targets)
	}
}

// WHO the absorb may fold in is not "whoever holds the identity". The identity
// is parsed out of a remote provider's response, so a hostile or corrupted
// answer would otherwise get to nominate any Gmail-captured row in the
// workspace to be archived. A candidate that is not shaped like this message's
// own echo is a fault: the reconcile reports it, the caller rolls its
// best-effort transaction back to one duplicate row, and NOTHING is touched.
func TestAbsorbRefusesARowThatIsNotThisMessagesEcho(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed echoSeed
	}{
		// A message this mailbox RECEIVED. Whatever put the send's stamped
		// identity on it, capture never derived it from a copy of this send.
		{"inbound", echoSeed{direction: "inbound", kind: "email", source: "gmail:x", capturedBy: "connector:gmail", counterparty: counterparty}},
		// A row a human filed. The connector provenance is what capture's sink
		// validates against the acting connector before it writes anything;
		// without it there is no evidence the provider produced this row at all.
		{"human-authored", echoSeed{direction: "outbound", kind: "email", source: "manual", capturedBy: "human:x", counterparty: counterparty}},
		// Not a message. The natural key spans every kind, so a calendar event
		// filed under the same source system can hold it.
		{"not an email", echoSeed{direction: "outbound", kind: "meeting", source: "gmail:x", capturedBy: "connector:gmail", counterparty: counterparty}},
		// THE SAME-SHAPED BYSTANDER, and the reason the counterparty is in the
		// predicate at all: a connector-captured outbound Gmail message, filed
		// after this send, satisfying every OTHER condition — but addressed to
		// somebody else. Without that column this row would be archived on a
		// remote provider's say-so, and every case above would still pass.
		{"another recipient", echoSeed{
			direction: "outbound", kind: "email", source: "gmail:x",
			capturedBy: "connector:gmail", counterparty: "someone.else@example.test",
		}},
		// The same row with no counterparty recorded at all. NULL matches
		// nothing, which is the safe direction: an unaddressed row is one
		// nothing shows to be this message's echo.
		{"no recipient recorded", echoSeed{
			direction: "outbound", kind: "email", source: "gmail:x", capturedBy: "connector:gmail",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setupSend(t)
			survivor := e.seedSentEmail(t, mintedIdentity)
			tc.seed.workspace = e.ws
			bystander := e.seedEcho(t, tc.seed)

			if err := e.reconcileExpectingRefusal(t, survivor); err == nil {
				t.Fatal("the reconcile absorbed a row nothing shows to be this message's echo")
			}

			if row := e.sentRow(t, survivor); row.sourceID != mintedIdentity {
				t.Errorf("the survivor's source_id = %q, want the minted identity %q left alone — a refused absorb writes nothing",
					row.sourceID, mintedIdentity)
			}
			untouched := e.absorbedRow(t, bystander)
			if untouched.archived {
				t.Error("the bystander was archived — a provider's string chose which row leaves the timeline")
			}
			if untouched.sourceID != stampedIdentity {
				t.Errorf("the bystander's source_id = %q, want it untouched (%q)", untouched.sourceID, stampedIdentity)
			}
		})
	}
}

// The natural-key index is per workspace, so another tenant holding the same
// identity does not even collide — and must not be readable, let alone
// archivable, from this one. The re-key simply succeeds.
func TestAbsorbNeverReachesAnotherWorkspacesRowOnTheSameIdentity(t *testing.T) {
	e := setupSend(t)
	survivor := e.seedSentEmail(t, mintedIdentity)
	neighbour := e.seedNeighbourWorkspace(t)
	// Shaped exactly like this message's own echo, so the workspace bound is
	// the only thing keeping it out of reach.
	theirs := e.seedEcho(t, echoSeed{
		workspace: neighbour, direction: "outbound", kind: "email",
		source: "gmail:" + stampedIdentity, capturedBy: "connector:gmail",
		counterparty: counterparty,
	})

	e.reconcile(t, survivor, stampedIdentity)

	if row := e.sentRow(t, survivor); row.sourceID != stampedIdentity {
		t.Errorf("the survivor's source_id = %q, want the stamped identity %q: another tenant's row is not a collision",
			row.sourceID, stampedIdentity)
	}
	untouched := e.absorbedRow(t, theirs)
	if untouched.archived || untouched.sourceID != stampedIdentity {
		t.Errorf("the neighbouring workspace's row = %+v, want it untouched — one tenant's send must not reach another's timeline", untouched)
	}
}

// What capture_pending_counterparty holds is a queued human review the survivor
// does not re-queue. It must move with the absorb, or the question about who
// this stranger is stays attached to a message the workspace can no longer see.
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
