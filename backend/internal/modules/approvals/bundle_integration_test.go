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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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
//
// And the refusal is only ever shown to someone who could decide the bundle. To
// a caller who could not, an oversized bundle is as absent as any other — a 422
// where they would otherwise get a 404 tells them the act exists, which is the
// oracle every other read on this table closes.
func TestABundleTooLargeToDecideIsRefusedAndStillHiddenFromOutsiders(t *testing.T) {
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

	ungranted := e.asHumanWith(grantsFor(map[string]principal.ObjectGrant{}))
	if _, err := e.svc.DecideBundle(ungranted, bundle, true, nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound — the size of a bundle they cannot see is not theirs to learn", err)
	}
}

// competingTx opens the OTHER side of a race: a transaction this test drives by
// hand, to hold a row or commit a verdict while the code under test runs.
//
// It binds app.workspace_id even though the owner connection is RLS-exempt
// wherever this suite runs today. The binding is what keeps that from being
// load-bearing: FORCE row-level security does not reach a superuser or a
// BYPASSRLS role, so an unbound write here works on every machine that has one
// and filters to zero rows on any that does not — and a race probe that quietly
// writes nothing is a test that passes having proved the opposite of what it
// says.
func (e *stagingEnv) competingTx(t *testing.T) pgx.Tx {
	t.Helper()
	ctx := context.Background()
	tx, err := e.owner.Begin(ctx)
	if err != nil {
		t.Fatalf("opening the competing transaction: %v", err)
	}
	t.Cleanup(func() {
		//craft:ignore swallowed-errors a rollback after the test's own Commit is a designed no-op; the Commit itself is asserted
		_ = tx.Rollback(context.Background())
	})
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, e.ws.String()); err != nil {
		t.Fatalf("binding the competing transaction to the workspace: %v", err)
	}
	return tx
}

// waitForRowLockWaiter blocks until something is waiting on a lock THIS TEST's
// competing transaction holds.
//
// It asks pg_blocking_pids rather than "is anyone in this database waiting":
// the integration lane runs a dozen packages against one server, so a waiter
// belonging to another package would satisfy the looser question, the competing
// transaction would commit before the decision ever reached the contested row,
// and the run would sail past the interleaving it claims to exercise — passing
// having proved nothing, which is the one outcome a race test must not produce.
// Blocked-BY-this-connection is exact: nothing else in the lane can satisfy it.
//
// blocker is the backend id of the transaction holding the row, read from the
// connection it runs on.
func waitForRowLockWaiter(t *testing.T, e *stagingEnv, blocker int, done <-chan struct{}) {
	t.Helper()
	const maxProbes = 20_000
	for probe := 0; probe < maxProbes; probe++ {
		var waiting bool
		if err := e.owner.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM pg_stat_activity a
			  WHERE $1 = ANY (pg_blocking_pids(a.pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-done:
			t.Fatal("the bundle decision finished without ever blocking on the contested member — it never reached the row the competing transaction was holding, so this run proved nothing")
		default:
		}
	}
	t.Fatalf("nothing waited on backend %d within %d probes — the decision never reached the row it should have blocked on", blocker, maxProbes)
}

// backendPID is the server-side backend id of the transaction on tx, which is
// what pg_blocking_pids names when something waits on a row it holds.
func backendPID(t *testing.T, tx pgx.Tx) int {
	t.Helper()
	var pid int
	if err := tx.QueryRow(context.Background(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("reading the competing transaction's backend id: %v", err)
	}
	return pid
}

// The race the pre-check cannot answer: a member is pending when the bundle
// reads it and decided by someone else by the time the bundle reaches it.
//
// decideInTx takes the row lock, so this call waits for the competing decision
// and then finds a verdict it must not overwrite — and reports it as an ERROR.
// Absorbing that error is what keeps one person's click from turning another
// person's whole bundle into a failed request, and re-reading the row is what
// makes the answer say which verdict actually won.
func TestABundleMemberDecidedMidFlightIsAbsorbedRatherThanFailingTheBundle(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	// Oldest first is the order the decision walks, so the uncontested member is
	// already decided when the contested one blocks.
	uncontested := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-anna")
	contested := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-bruno")

	bg := context.Background()
	competing := e.competingTx(t)
	var locked ids.ApprovalID
	if err := competing.QueryRow(bg,
		`SELECT id FROM approval WHERE id = $1 FOR UPDATE`, contested).Scan(&locked); err != nil {
		t.Fatalf("holding the contested member: %v", err)
	}

	done := make(chan struct{})
	var members []BundleMember
	var decideErr error
	go func() {
		defer close(done)
		members, decideErr = e.svc.DecideBundle(ctx, bundle, true, nil)
	}()
	waitForRowLockWaiter(t, e, backendPID(t, competing), done)

	if _, err := competing.Exec(bg,
		`UPDATE approval SET status = 'rejected', decided_by = $2, decided_at = now() WHERE id = $1`,
		contested, e.rep); err != nil {
		t.Fatalf("recording the competing verdict: %v", err)
	}
	if err := competing.Commit(bg); err != nil {
		t.Fatalf("committing the competing verdict: %v", err)
	}
	<-done

	if decideErr != nil {
		t.Fatalf("the bundle failed over one contested member: %v", decideErr)
	}
	got := outcomes(members)
	if got[contested] != BundleAlreadyDecided {
		t.Errorf("the contested member reported %s, want %s", got[contested], BundleAlreadyDecided)
	}
	if got[uncontested] != BundleDecided {
		t.Errorf("its sibling reported %s, want %s", got[uncontested], BundleDecided)
	}
	if status := e.statusOf(t, contested); status != approvalStatusRejected {
		t.Errorf("the contested member is %s, want the competing rejection to stand", status)
	}
	for _, m := range members {
		if m.Approval.ID == contested && m.Approval.Status != approvalStatusRejected {
			t.Errorf("the answer reports the contested member as %s, want the verdict that won", m.Approval.Status)
		}
	}
}

// bundleOf reads a row's grouping straight from the table.
func (e *stagingEnv) bundleOf(t *testing.T, id ids.ApprovalID) *ids.UUID {
	t.Helper()
	var bundle *ids.UUID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT bundle_id FROM approval WHERE id = $1`, id).Scan(&bundle); err != nil {
		t.Fatalf("reading the stored bundle: %v", err)
	}
	return bundle
}

// The two re-proposals that must NOT move a row. An act carrying no bundle has
// no claim to orphan a proposal from the act that did group it — a nightly
// sweep re-proposing one lead would otherwise strip it out of the site read's
// bundle and leave that bundle quietly short a member. And re-proposing into
// the SAME bundle changes nothing, so it must not write an audit row saying
// something moved.
func TestARestagedProposalKeepsItsBundleWhenTheActHasNoneOrTheSameOne(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	member := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-anna")

	if _, err := e.svc.Stage(ctx, StageInput{
		Kind:           kindSiteLead,
		ProposedChange: []byte(`{"organization_id":"` + org.String() + `","note":"lead-anna"}`),
		DiffHash:       "lead-anna",
		TargetType:     tableOrganization,
		TargetID:       org,
		Summary:        "Re-proposed by an unbundled act",
		JoinPending:    true,
	}); err != nil {
		t.Fatalf("re-proposing without a bundle: %v", err)
	}
	if got := e.bundleOf(t, member); got == nil || *got != bundle {
		t.Errorf("an unbundled re-proposal left the row in %v, want it still in %s", got, bundle)
	}

	e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-anna")
	if n := e.count(t, `SELECT count(*) FROM audit_log
		WHERE entity_id = $1 AND evidence->>'rebundled' = 'true'`, member.UUID); n != 0 {
		t.Errorf("re-proposing into the same bundle wrote %d rebundle audit rows, want none — nothing moved", n)
	}
}

// A target that leaves this human's world while the decision waits takes the
// whole bundle with it — as an ABSENCE, not as a half-decision.
//
// The members are locked as they are read, so the decision blocks before it has
// decided anything; by the time it runs, the filter finds nothing this human may
// decide and answers the same not-found any unreadable bundle answers. What
// matters is the other half: no verdict was written on the way to that answer.
func TestABundleWhoseTargetLeavesMidFlightDecidesNothingAtAll(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	first := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-anna")
	second := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-bruno")

	bg := context.Background()
	competing := e.competingTx(t)
	var locked ids.ApprovalID
	if err := competing.QueryRow(bg,
		`SELECT id FROM approval WHERE id = $1 FOR UPDATE`, second).Scan(&locked); err != nil {
		t.Fatalf("holding a member: %v", err)
	}

	done := make(chan struct{})
	var decideErr error
	go func() {
		defer close(done)
		_, decideErr = e.svc.DecideBundle(ctx, bundle, true, nil)
	}()
	waitForRowLockWaiter(t, e, backendPID(t, competing), done)

	if _, err := competing.Exec(bg,
		`UPDATE organization SET archived_at = now() WHERE id = $1`, org); err != nil {
		t.Fatalf("archiving the target: %v", err)
	}
	if err := competing.Commit(bg); err != nil {
		t.Fatalf("committing the archive: %v", err)
	}
	<-done

	if !errors.Is(decideErr, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound — nothing about this bundle is decidable any more", decideErr)
	}
	for _, id := range []ids.ApprovalID{first, second} {
		if status := e.statusOf(t, id); status != statusPending {
			t.Errorf("member %s is %s, want pending — a refused decision writes no verdict", id, status)
		}
	}
}

// steppingClock answers t0 for the first reading and t1 for every one after,
// which is the boundary a bundle decision straddles: the loop judges every
// member's status against one reading of the clock, and each member's own
// decision re-judges it against a later one.
func steppingClock(t0, t1 time.Time) func() time.Time {
	var read atomic.Int64
	return func() time.Time {
		if read.Add(1) == 1 {
			return t0
		}
		return t1
	}
}

// A member that lapses BETWEEN those two readings is reported expired — not
// already_decided, which would tell a human somebody answered it, and not
// decided, which would approve a proposal the later reading says is dead.
//
// This is the only way a member's own decision can disagree with the loop that
// selected it: membership is locked for the life of the transaction, so no other
// decision can slip in, and the clock is what is left.
func TestAMemberThatLapsesBetweenTheTwoClockReadingsIsReportedExpired(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()
	lapsing := e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-anna")

	var expiresAt time.Time
	if err := e.owner.QueryRow(context.Background(),
		`SELECT expires_at FROM approval WHERE id = $1`, lapsing).Scan(&expiresAt); err != nil {
		t.Fatalf("reading the member's expiry: %v", err)
	}
	e.svc.now = steppingClock(expiresAt.Add(-time.Second), expiresAt.Add(time.Second))

	members, err := e.svc.DecideBundle(ctx, bundle, true, nil)
	if err != nil {
		t.Fatalf("deciding the bundle: %v", err)
	}
	if got := outcomes(members)[lapsing]; got != BundleExpired {
		t.Errorf("the lapsed member reported %s, want %s", got, BundleExpired)
	}
	if status := e.statusOf(t, lapsing); status != statusPending {
		t.Errorf("stored status = %s, want it left pending — expiry is not a verdict", status)
	}
}

// A member that leaves for a fresher act's bundle while the decision is in
// flight is NOT decided by the bundle it left.
//
// A re-proposal joins the pending row and re-points it (rebundleJoinedInTx), so
// without holding the membership it read, this decision would answer the OLD
// bundle's question by deciding a row that had already become part of the new
// one — and the fresh act's bundle would then carry a member nobody decided
// there. Locking the members as they are read makes the re-read see the move and
// leave the row alone.
func TestABundleDoesNotDecideAMemberThatMovedToAnotherBundleMidFlight(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	leaving, arriving := ids.NewV7(), ids.NewV7()
	member := e.stageInto(ctx, t, leaving, org, kindSiteLead, "lead-anna")

	bg := context.Background()
	competing := e.competingTx(t)
	var locked ids.ApprovalID
	if err := competing.QueryRow(bg,
		`SELECT id FROM approval WHERE id = $1 FOR UPDATE`, member).Scan(&locked); err != nil {
		t.Fatalf("holding the member: %v", err)
	}

	done := make(chan struct{})
	var decideErr error
	go func() {
		defer close(done)
		_, decideErr = e.svc.DecideBundle(ctx, leaving, true, nil)
	}()
	waitForRowLockWaiter(t, e, backendPID(t, competing), done)

	if _, err := competing.Exec(bg,
		`UPDATE approval SET bundle_id = $2 WHERE id = $1`, member, arriving); err != nil {
		t.Fatalf("re-pointing the member at the fresh act: %v", err)
	}
	if err := competing.Commit(bg); err != nil {
		t.Fatalf("committing the move: %v", err)
	}
	<-done

	if !errors.Is(decideErr, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound — the bundle it left holds nothing", decideErr)
	}
	if status := e.statusOf(t, member); status != statusPending {
		t.Errorf("the moved member is %s, want pending — it belongs to the fresh act's question now", status)
	}
	if bundle := e.bundleOf(t, member); bundle == nil || *bundle != arriving {
		t.Errorf("the moved member sits in %v, want the fresh act's %s", bundle, arriving)
	}
}

// A proposal settled while a re-proposal is joining it must not be re-pointed at
// the fresh act: a decided row moved into a new bundle carries somebody's
// finished decision into a question that was never asked, and the act that made
// the move ends up with no live member at all.
//
// The join and the move are one act under the row lock, so the re-proposal
// re-reads the row after the verdict commits, finds nothing live to join, and
// creates the member it meant to.
func TestAProposalDecidedWhileARePropositionJoinsItIsNotRebundled(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	settled, fresh := ids.NewV7(), ids.NewV7()
	original := e.stageInto(ctx, t, settled, org, kindSiteLead, "lead-anna")

	bg := context.Background()
	deciding := e.competingTx(t)
	if _, err := deciding.Exec(bg,
		`UPDATE approval SET status = 'rejected', decided_by = $2, decided_at = now() WHERE id = $1`,
		original, e.rep); err != nil {
		t.Fatalf("recording the competing verdict: %v", err)
	}

	type staged struct {
		id  ids.ApprovalID
		err error
	}
	done := make(chan staged, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		id, err := e.svc.Stage(ctx, StageInput{
			Kind:           kindSiteLead,
			ProposedChange: []byte(`{"organization_id":"` + org.String() + `","note":"lead-anna"}`),
			DiffHash:       "lead-anna",
			TargetType:     tableOrganization,
			TargetID:       org,
			Summary:        "Re-proposed while the first was being decided",
			JoinPending:    true,
			BundleID:       fresh,
		})
		done <- staged{id: id, err: err}
	}()
	waitForRowLockWaiter(t, e, backendPID(t, deciding), finished)
	if err := deciding.Commit(bg); err != nil {
		t.Fatalf("committing the verdict: %v", err)
	}

	restaged := <-done
	if restaged.err != nil {
		t.Fatalf("re-proposing over a settled proposal: %v", restaged.err)
	}
	if restaged.id == original {
		t.Fatal("the re-proposal joined a proposal that had just been decided")
	}
	if status := e.statusOf(t, original); status != approvalStatusRejected {
		t.Errorf("the settled proposal is %s, want the verdict to stand", status)
	}
	if bundle := e.bundleOf(t, original); bundle == nil || *bundle != settled {
		t.Errorf("the settled proposal moved to %v — a finished decision was carried into a fresh act", bundle)
	}
	if bundle := e.bundleOf(t, restaged.id); bundle == nil || *bundle != fresh {
		t.Errorf("the new member sits in %v, want the fresh act's %s", bundle, fresh)
	}
}
