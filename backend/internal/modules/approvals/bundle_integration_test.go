// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package approvals

// One act's proposals, decided together, against a real database. Everything a
// bundle decision has to get right is about what a transaction leaves behind —
// N verdicts, N audit rows, N events, and the members it deliberately did NOT
// touch — so none of it can be shown without Postgres.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The two kinds a site read stages together, and the grants deciding each one
// takes: an organization update for the company's own facts, a lead create for
// each person the site published. They differ on purpose — that difference is
// what the authority test below turns on.
const (
	kindDeepRead = "deepread"
	kindSiteLead = "site_lead"
)

// grantsFor is a principal's object policy spelled as the tests read it.
func grantsFor(objects map[string]principal.ObjectGrant) principal.Permissions {
	return principal.Permissions{
		RoleKeys: []string{"admin"}, Objects: objects, RowScope: principal.RowScopeAll,
	}
}

// decidesEverything holds both grants a site read's bundle needs, plus the
// organization READ every member's target-visibility probe asks for.
func decidesEverything() principal.Permissions {
	return grantsFor(map[string]principal.ObjectGrant{
		tableOrganization: {Read: true, Update: true},
		tableLead:         {Create: true},
	})
}

// asHumanWith is the deciding human, with exactly the grants given.
func (e *stagingEnv) asHumanWith(perms principal.Permissions) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.rep.String(), UserID: e.rep,
		Permissions: perms,
	})
}

// organization seeds the company every member of these bundles targets: the
// staging path resolves its target's version, so an absent row would fail the
// staging for a reason that has nothing to do with bundling.
func (e *stagingEnv) organization(t *testing.T) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO organization (id, workspace_id, display_name, source, captured_by)
		VALUES ($1, $2, 'Acme', 'gmail:seed', 'connector:gmail')`, id, e.ws); err != nil {
		t.Fatalf("seeding the target organization: %v", err)
	}
	return id
}

// stageInto stages one proposal of kind into bundle, exactly as a site read does.
func (e *stagingEnv) stageInto(ctx context.Context, t *testing.T, bundle, org ids.UUID, kind, hash string) ids.ApprovalID {
	t.Helper()
	id, err := e.svc.Stage(ctx, StageInput{
		Kind:           kind,
		ProposedChange: []byte(fmt.Sprintf(`{"organization_id":%q,"note":%q}`, org.String(), hash)),
		DiffHash:       hash,
		TargetType:     tableOrganization,
		TargetID:       org,
		Summary:        "Staged by " + kind,
		JoinPending:    true,
		BundleID:       bundle,
	})
	if err != nil {
		t.Fatalf("staging %s into the bundle: %v", kind, err)
	}
	return id
}

// statusOf reads a member's stored verdict straight from the table, bypassing
// the service — what the decision LEFT is the question, not what it returned.
func (e *stagingEnv) statusOf(t *testing.T, id ids.ApprovalID) string {
	t.Helper()
	var status string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status FROM approval WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("reading the stored status: %v", err)
	}
	return status
}

// count answers one scalar count, for the audit and outbox assertions.
func (e *stagingEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("counting: %v", err)
	}
	return n
}

// outcomes indexes a decision's members by approval id.
func outcomes(members []BundleMember) map[ids.ApprovalID]BundleOutcome {
	out := make(map[ids.ApprovalID]BundleOutcome, len(members))
	for _, m := range members {
		out[m.Approval.ID] = m.Outcome
	}
	return out
}

// A bundle is decided ONCE and recorded N times. That is the whole shape R7
// settled on: the human answers one question, and the ledger still carries a
// per-effect decision, audit row and event for every proposal — because seven
// per-effect decisions are better provenance than one covering seven effects,
// and because each member's own redemption re-checks its own diff and pin.
func TestABundleIsDecidedOnceAndRecordedPerMember(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	facts := e.stageInto(ctx, t, bundle, org, kindDeepRead, "facts-hash")
	first := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-anna")
	second := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-bruno")

	members, err := e.svc.DecideBundle(ctx, bundle, true, nil)
	if err != nil {
		t.Fatalf("deciding the bundle: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("decided %d members, want the 3 the act staged", len(members))
	}
	for id, outcome := range outcomes(members) {
		if outcome != BundleDecided {
			t.Errorf("member %s outcome = %s, want %s", id, outcome, BundleDecided)
		}
	}
	for _, id := range []ids.ApprovalID{facts, first, second} {
		if status := e.statusOf(t, id); status != approvalStatusApproved {
			t.Errorf("member %s stored status = %s, want approved", id, status)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_log
		WHERE workspace_id = $1 AND entity_type = 'approval' AND action = 'approve'`, e.ws); n != 3 {
		t.Errorf("approve audit rows = %d, want one per member", n)
	}
	if n := e.count(t, `SELECT count(*) FROM event_outbox
		WHERE envelope->>'type' = 'approval.decided'
		  AND envelope->'workspace_id' IS NOT NULL AND envelope->>'workspace_id' = $1`, e.ws.String()); n != 3 {
		t.Errorf("approval.decided events = %d, want one per member", n)
	}
}

// A member somebody already answered keeps THEIR answer, and its siblings are
// still decided. The members were always independent authorities, so an
// all-or-nothing failure here would let one stale row block a whole read's
// findings — and silently re-deciding it would overwrite a human's verdict.
func TestABundleMemberAlreadyDecidedKeepsItsVerdictAndItsSiblingsStillDecide(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	rejected := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-anna")
	pending := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-bruno")
	if _, err := e.svc.Decide(ctx, rejected, false, nil); err != nil {
		t.Fatalf("rejecting one member up front: %v", err)
	}

	members, err := e.svc.DecideBundle(ctx, bundle, true, nil)
	if err != nil {
		t.Fatalf("deciding the bundle: %v", err)
	}
	got := outcomes(members)
	if got[rejected] != BundleAlreadyDecided {
		t.Errorf("the already-rejected member reported %s, want %s", got[rejected], BundleAlreadyDecided)
	}
	if got[pending] != BundleDecided {
		t.Errorf("the pending member reported %s, want %s", got[pending], BundleDecided)
	}
	if status := e.statusOf(t, rejected); status != approvalStatusRejected {
		t.Errorf("the already-rejected member is now %s — a bundle approve overwrote a human's verdict", status)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_log
		WHERE workspace_id = $1 AND entity_id = $2 AND action = 'approve'`, e.ws, rejected.UUID); n != 0 {
		t.Errorf("the already-decided member collected %d approve audit rows, want none", n)
	}
}

// An expired member is not a decision anybody owes. It reports as expired — not
// as already_decided, which would say a human answered it, and not as decided,
// which would approve a proposal staged against a world that has since moved.
func TestAnExpiredBundleMemberIsReportedRatherThanApproved(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	lapsed := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-anna")
	live := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-bruno")
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE approval SET expires_at = now() - interval '1 day' WHERE id = $1`, lapsed); err != nil {
		t.Fatalf("backdating the lapsed member: %v", err)
	}

	members, err := e.svc.DecideBundle(ctx, bundle, true, nil)
	if err != nil {
		t.Fatalf("deciding the bundle: %v", err)
	}
	got := outcomes(members)
	if got[lapsed] != BundleExpired {
		t.Errorf("the lapsed member reported %s, want %s", got[lapsed], BundleExpired)
	}
	if got[live] != BundleDecided {
		t.Errorf("the live member reported %s, want %s", got[live], BundleDecided)
	}
	if status := e.statusOf(t, lapsed); status == approvalStatusApproved {
		t.Error("the lapsed member was approved — expiry is not a decision a bundle may take for someone")
	}
}

// Bundling is not a way to release an effect sideways. A member this human could
// not decide on its own is neither shown to them nor decided by them, and the
// grants are checked per member exactly as a single decision checks them.
func TestABundleMemberOutsideTheCallersAuthorityIsNeitherShownNorDecided(t *testing.T) {
	e := setupStaging(t)
	staging := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	facts := e.stageInto(staging, t, bundle, org, kindDeepRead, "facts-hash")
	lead := e.stageInto(staging, t, bundle, org, kindSiteLead, "lead-anna")

	// This human may update the company but may not create a lead, so exactly
	// one of the two proposals is theirs to answer.
	deciding := e.asHumanWith(grantsFor(map[string]principal.ObjectGrant{
		tableOrganization: {Read: true, Update: true},
	}))
	members, err := e.svc.DecideBundle(deciding, bundle, true, nil)
	if err != nil {
		t.Fatalf("deciding the bundle: %v", err)
	}
	if len(members) != 1 || members[0].Approval.ID != facts {
		t.Fatalf("the decision covered %d members, want only the one this human may decide", len(members))
	}
	if status := e.statusOf(t, lead); status != statusPending {
		t.Errorf("the lead proposal is now %s — a bundle decided an effect its caller could not perform", status)
	}
}

// A bundle nobody may decide reads as absent, not as forbidden. Anything else
// makes the bundle id a lookup oracle: "403 here, 404 there" tells a caller
// which acts exist and which do not, which is the same leak the inbox filter and
// Get already close.
func TestABundleWithNoDecidableMemberReadsAsAbsent(t *testing.T) {
	e := setupStaging(t)
	staging := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	e.stageInto(staging, t, bundle, org, kindSiteLead, "lead-anna")

	ungranted := e.asHumanWith(grantsFor(map[string]principal.ObjectGrant{}))
	if _, err := e.svc.DecideBundle(ungranted, bundle, true, nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound — an undecidable bundle must read as absent", err)
	}
	if _, err := e.svc.DecideBundle(staging, ids.NewV7(), true, nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for a bundle id nothing points at", err)
	}
}

// A re-proposal JOINS the row already pending, and that row MOVES onto the fresh
// act's bundle. Without the move a second read of the same site produces a bundle
// holding only the part that happened to be new, and whoever reviews "what this
// read proposed" reviews a fraction of it with nothing saying so.
func TestARestagedProposalMovesOntoTheFreshBundle(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	first, second := ids.NewV7(), ids.NewV7()
	original := e.stageInto(ctx, t, first, org, kindSiteLead, "lead-anna")

	rejoined := e.stageInto(ctx, t, second, org, kindSiteLead, "lead-anna")
	if rejoined != original {
		t.Fatalf("the re-proposal created %s instead of joining %s", rejoined, original)
	}
	var bundle ids.UUID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT bundle_id FROM approval WHERE id = $1`, original).Scan(&bundle); err != nil {
		t.Fatalf("reading the joined row's bundle: %v", err)
	}
	if bundle != second {
		t.Errorf("the joined row is still in bundle %s, want the fresh act's %s", bundle, second)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_log
		WHERE entity_id = $1 AND evidence->>'rebundled' = 'true'`, original.UUID); n != 1 {
		t.Errorf("rebundle audit rows = %d, want exactly one for the move", n)
	}
	// The fresh bundle now answers for the whole act; the emptied one holds
	// nothing, so it cannot be decided at all.
	members, err := e.svc.DecideBundle(ctx, second, true, nil)
	if err != nil {
		t.Fatalf("deciding the fresh bundle: %v", err)
	}
	if len(members) != 1 || members[0].Approval.ID != original {
		t.Fatalf("the fresh bundle covered %d members, want the joined proposal", len(members))
	}
	if _, err := e.svc.DecideBundle(ctx, first, true, nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound — the emptied bundle has no members left", err)
	}
}

// A rejection releases nothing. The follow-on effect is what a decision lets
// happen, so a rejected bundle must leave every executor untouched — otherwise
// "no" costs exactly what "yes" does.
func TestARejectedBundleRunsNoEffect(t *testing.T) {
	e := setupStaging(t)
	ran := 0
	e.svc.WithEffect(kindSiteLead, func(context.Context, ids.ApprovalID, json.RawMessage, string) error {
		ran++
		return nil
	})
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	member := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-anna")

	reason := "not our market"
	members, err := e.svc.DecideBundle(ctx, bundle, false, &reason)
	if err != nil {
		t.Fatalf("rejecting the bundle: %v", err)
	}
	if len(members) != 1 || members[0].Outcome != BundleDecided {
		t.Fatalf("members = %+v, want the one member decided", members)
	}
	if status := e.statusOf(t, member); status != approvalStatusRejected {
		t.Errorf("stored status = %s, want rejected", status)
	}
	if ran != 0 {
		t.Errorf("the effect ran %d times on a rejection — a no must cost nothing", ran)
	}
}

// An effect that fails is that member's outcome and no one else's. The decisions
// are committed by then, so the verdict stands, its sibling still lands, and the
// caller is told which one did not.
func TestABundleMemberWhoseEffectFailsIsReportedAlone(t *testing.T) {
	e := setupStaging(t)
	e.svc.WithEffect(kindSiteLead, func(_ context.Context, _ ids.ApprovalID, _ json.RawMessage, _ string) error {
		return errors.New("the capture sink refused this lead")
	})
	e.svc.WithEffect(kindDeepRead, func(context.Context, ids.ApprovalID, json.RawMessage, string) error {
		return nil
	})
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	facts := e.stageInto(ctx, t, bundle, org, kindDeepRead, "facts-hash")
	lead := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-anna")

	members, err := e.svc.DecideBundle(ctx, bundle, true, nil)
	if err != nil {
		t.Fatalf("deciding the bundle: %v", err)
	}
	got := outcomes(members)
	if got[lead] != BundleEffectFailed {
		t.Errorf("the failing member reported %s, want %s", got[lead], BundleEffectFailed)
	}
	if got[facts] != BundleDecided {
		t.Errorf("its sibling reported %s, want %s", got[facts], BundleDecided)
	}
	if status := e.statusOf(t, lead); status != approvalStatusApproved {
		t.Errorf("the failing member's stored status = %s, want approved — the decision was committed before the effect ran", status)
	}
}

// A bundle larger than one decision covers is REFUSED, not applied to a prefix.
// A partial decision reported as a whole one is the silent half-effect the whole
// per-member design exists to prevent.
func TestABundleTooLargeToDecideIsRefused(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	// Inserted directly: staging one past the cap through the service would
	// prove nothing this test is about and would cost a transaction each.
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO approval (workspace_id, kind, proposed_by, on_behalf_of, target_entity_type,
		                      target_entity_id, proposed_change, diff_hash, expires_at, bundle_id)
		SELECT $1, $2, 'human:seed', $3, $4, $5, '{}'::jsonb, 'hash-' || n, now() + interval '1 day', $6
		FROM generate_series(1, $7) AS n`,
		e.ws, kindSiteLead, e.rep, tableOrganization, org, bundle, bundleDecisionCap+1); err != nil {
		t.Fatalf("seeding an oversized bundle: %v", err)
	}

	var oversized *BundleTooLargeError
	_, err := e.svc.DecideBundle(ctx, bundle, true, nil)
	if !errors.As(err, &oversized) {
		t.Fatalf("err = %v, want BundleTooLargeError", err)
	}
	if n := e.count(t, `SELECT count(*) FROM approval WHERE bundle_id = $1 AND status <> 'pending'`, bundle); n != 0 {
		t.Errorf("%d members were decided by a refused call — the refusal must leave the bundle untouched", n)
	}
}
