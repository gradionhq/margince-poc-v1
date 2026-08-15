// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// WHICH agent scheduled a message, and what the record says when there is no
// human behind it (ADR-0055, core 0260).
//
// Separate from the scheduling mechanics because the subject is different: not
// whether a deferred message fires correctly, but whether the audit trail can
// name the actor that produced it. The row stores the authorizing human either
// way — what these prove is that the human's id is never used to manufacture an
// agent identity, in either direction.
//
// There were two wrong answers available, and both were briefly written here:
// copying the principal's UserID into the "human behind this" column (which for
// a passport-less agent is an AGENT's own app_user row), and storing nothing at
// all (which makes a new row indistinguishable from a pre-0260 one, so the fire
// path invents the identity again). The tests below pin the third answer.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// scheduleAsAgent defers a message with an agent as the actor, through the same
// SendOrSchedule the tool surface calls.
func (p *preflightEnv) scheduleAsAgent(t *testing.T, actor principal.Principal, at time.Time) ids.UUID {
	t.Helper()
	anchor, err := ids.Parse(p.activityID)
	if err != nil {
		t.Fatalf("the fixture activity id does not parse: %v", err)
	}
	out, err := compose.ScheduleAsAgentForTest(context.Background(), p.Pool, p.workspaceID(t), actor,
		ids.From[ids.ActivityKind](anchor),
		activities.SendEmailInput{
			Recipients:     []string{"buyer@preflight.test"},
			Subject:        "Monday morning",
			Body:           "Written the night before, by a tool.",
			ConsentPurpose: "transactional",
		}, at)
	if err != nil {
		t.Fatalf("scheduling as an agent: %v", err)
	}
	if out.Scheduled == nil {
		t.Fatal("scheduling as an agent sent immediately instead of deferring")
	}
	return out.Scheduled.ID
}

// auditActor reads the actor an audit row names for one record.
func (p *preflightEnv) auditActor(t *testing.T, entityType string, entityID ids.UUID, action string) (string, string) {
	t.Helper()
	var actorType, actorID string
	if err := apptest.InWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT actor_type, actor_id FROM audit_log
			  WHERE entity_type = $1 AND entity_id = $2 AND action = $3
			  ORDER BY occurred_at DESC LIMIT 1`,
			entityType, entityID, action).Scan(&actorType, &actorID)
	}); err != nil {
		t.Fatalf("reading the %s audit row for %s: %v", action, entityID, err)
	}
	return actorType, actorID
}

// An agent-scheduled message fires as the agent that scheduled it — by name.
//
// The human's id is in the row already, and rebuilding an agent identity from it
// produces `agent:<human-uuid>`: an actor that never existed, and the same one
// for every agent and every passport acting for that person. The release audit
// row, the activity's captured_by and the outbox envelope then cannot say which
// agent produced the message, which is the attribution ADR-0055 rests on
// (#1258).
//
// What must NOT change with it is the ceiling: the grants are still the human's,
// re-read live at fire, so preserving the identity cannot widen what the message
// may do.
func TestAnAgentScheduledSendFiresUnderTheAgentThatScheduledIt(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	// A REAL passport row, not an invented id: the fire path re-authenticates
	// the stored passport, so an agent carrying one that was never issued is
	// correctly refused. Scheduling under a live credential is what the case
	// under test actually is.
	passport := p.issuePassport(t)
	agent := principal.Principal{
		Type:       principal.PrincipalAgent,
		ID:         "agent:" + passport.String(),
		PassportID: passport,
		UserID:     uuidOf(t, p.user),
		OnBehalfOf: uuidOf(t, p.user),
		SeatType:   principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true, Read: true, Update: true},
				"person":   {Create: true, Read: true, Update: true},
			},
			RowScope: principal.RowScopeAll,
		},
	}

	id := p.scheduleAsAgent(t, agent, time.Now().Add(2*time.Hour))
	p.makeDue(t, id)
	p.fire(t, id)

	if !p.sent(t, id) {
		status, reason := p.scheduledStatus(t, id)
		t.Fatalf("an agent-scheduled message did not fire: %q/%q", status, reason)
	}

	// The release audit row is where an investigation looks for who acted.
	actorType, actorID := p.auditActor(t, "activity", p.releasedActivity(t, id), "create")
	if actorType != "agent" {
		t.Errorf("the fired message is audited as %q, want agent — a tool-written message recorded as a human's act", actorType)
	}
	if actorID != agent.ID {
		t.Errorf("the fired message names actor %q, want %q — the audit cannot say which agent produced it",
			actorID, agent.ID)
	}
	// The specific shape of the old defect: an identity derived from the human.
	if actorID == "agent:"+p.user {
		t.Error("the actor id was rebuilt from the human's id — every agent acting for this person collapses into one invented actor")
	}
}

// An agent that names no human still records WHICH agent it was.
//
// Two wrong answers were available here, and this test pins the third.
//
// Falling back to the principal's UserID for `agent_on_behalf_of` is wrong: a
// passport-less agent principal's UserID is an AGENT's own app_user row
// (compose/extjobsrun.go selects it `WHERE id = $1 AND is_agent`), so it writes
// an agent's id into a column meaning "the human behind this". The fire path
// hands that to actor.OnBehalfOf, which auth.Admit reads to derive seat and
// RBAC — a fabricated authority.
//
// Storing NOTHING is also wrong, and less obviously so: a new row with no actor
// id is indistinguishable from a pre-0260 row, and the fire path reads that as
// "cannot say which agent" and falls back to `agent:<human-uuid>` — restoring
// the invented identity for exactly the actor whose real id was in hand.
//
// So the row records the agent and leaves the human NULL. The absence is the
// honest record of an actor that named nobody, and the identity survives.
func TestAnAgentWithNoHumanBehindItStillRecordsWhichAgent(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	// No OnBehalfOf and no passport — the extension-tick shape. UserID is the
	// agent's OWN row, which is exactly what must not be copied.
	agent := principal.Principal{
		Type:     principal.PrincipalAgent,
		ID:       "agent:extension-tick",
		UserID:   uuidOf(t, p.user),
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true, Read: true, Update: true},
				"person":   {Create: true, Read: true, Update: true},
			},
			RowScope: principal.RowScopeAll,
		},
	}

	id := p.scheduleAsAgent(t, agent, time.Now().Add(2*time.Hour))

	actorID, passport, behalf := p.storedProvenance(t, id)
	if actorID == nil || *actorID != agent.ID {
		t.Fatalf("the row records actor %v, want %q — an agent whose id was in hand was stored as unknowable",
			actorID, agent.ID)
	}
	if behalf != nil {
		t.Errorf("the row names human %v — this agent named none, and inventing one feeds auth.Admit a fabricated authority", behalf)
	}
	if passport != nil {
		t.Errorf("the row names passport %v — this agent presented none", passport)
	}

	// And the identity survives the fire: the point of storing it is that the
	// audit says which agent, not that a column is populated.
	p.makeDue(t, id)
	p.fire(t, id)
	if !p.sent(t, id) {
		status, reason := p.scheduledStatus(t, id)
		t.Fatalf("the message did not fire: %q/%q", status, reason)
	}
	_, auditActor := p.auditActor(t, "activity", p.releasedActivity(t, id), "create")
	if auditActor != agent.ID {
		t.Errorf("the fired message is audited as %q, want %q — a row that recorded its agent still fired as an invented one",
			auditActor, agent.ID)
	}
}

// storedProvenance reads the three scheduling-time agent columns.
func (p *preflightEnv) storedProvenance(t *testing.T, id ids.UUID) (actorID *string, passport, behalf *ids.UUID) {
	t.Helper()
	if err := apptest.InWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT agent_actor_id, agent_passport_id, agent_on_behalf_of
			   FROM scheduled_send WHERE id = $1`, id).Scan(&actorID, &passport, &behalf)
	}); err != nil {
		t.Fatalf("reading the stored provenance for %s: %v", id, err)
	}
	return actorID, passport, behalf
}

// rowVersion reads the scheduled row's optimistic-concurrency version.

// A revoked passport stops the message, even though the human is untouched.
//
// This is the case the live re-read of the HUMAN's authority cannot see. The
// rep is still active with the same grants, so EffectiveAuthority answers
// exactly as it did at schedule time — and a message an operator revoked an
// agent's credential to stop would go out anyway, under a credential nobody
// honours any more.
//
// The stored passport therefore has to be re-authenticated at fire, not merely
// restored onto the principal. AuthenticateAgentByID re-runs the same liveness
// rules the token path runs (revocation, expiry, the granting human's status),
// which is what makes "may it still go" a question asked now rather than at
// schedule time.
func TestARevokedPassportHoldsTheMessageItWasScheduledUnder(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	passport := p.issuePassport(t)
	agent := principal.Principal{
		Type:       principal.PrincipalAgent,
		ID:         "agent:" + passport.String(),
		PassportID: passport,
		UserID:     uuidOf(t, p.user),
		OnBehalfOf: uuidOf(t, p.user),
		SeatType:   principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true, Read: true, Update: true},
				"person":   {Create: true, Read: true, Update: true},
			},
			RowScope: principal.RowScopeAll,
		},
	}

	id := p.scheduleAsAgent(t, agent, time.Now().Add(2*time.Hour))
	before := p.countDeliveries(t)

	// The operator revokes the credential. The HUMAN is deliberately left
	// alone: that is what makes this the case the human-authority re-read
	// cannot catch.
	p.revokePassport(t, passport)

	p.makeDue(t, id)
	p.fire(t, id)

	status, reason := p.scheduledStatus(t, id)
	if status != activities.ScheduledStatusHeld {
		t.Fatalf("a message scheduled under a revoked passport reads %q/%q — it sent under a credential nobody honours",
			status, reason)
	}
	if reason != activities.HeldPassportRevoked {
		t.Errorf("held for %q, want %q — a rep told their account is inactive would look in the wrong place",
			reason, activities.HeldPassportRevoked)
	}
	if after := p.countDeliveries(t); after != before {
		t.Errorf("deliveries went %d → %d — the message reached the machinery despite the hold", before, after)
	}
}

// issuePassport mints a live passport for the fixture human and returns its id.
func (p *preflightEnv) issuePassport(t *testing.T) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if err := apptest.InWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO passport (id, workspace_id, on_behalf_of, granted_by, label, scopes, token_hash, expires_at)
			 VALUES ($1, (SELECT id FROM workspace WHERE slug = $2), $3, $3, 'scheduled-send probe',
			         ARRAY['read','write'], $4, now() + interval '30 days')`,
			id, p.Slug, uuidOf(t, p.user), "probe-"+id.String())
		return err
	}); err != nil {
		t.Fatalf("issuing a passport: %v", err)
	}
	return id
}

// revokePassport stamps revoked_at, which is the one field the agent liveness
// query filters on.
func (p *preflightEnv) revokePassport(t *testing.T, passport ids.UUID) {
	t.Helper()
	if err := apptest.InWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		tag, err := tx.Exec(context.Background(),
			`UPDATE passport SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, passport)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("revoking %s affected %d rows, want 1", passport, tag.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("revoking the passport: %v", err)
	}
}
