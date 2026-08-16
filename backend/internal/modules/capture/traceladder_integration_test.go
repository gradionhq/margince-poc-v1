// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// Who can reach whose rungs.
//
// `capture_trace` rows are the member's alone and no grant widens them, so the
// answers below are the feature's access-control axis rather than a detail of
// it. Every one of them is a SQL predicate, which means it fails silently: this
// module has no RLS behind it, and a lookup that forgot its user clause would
// return a colleague's mailbox traffic with nothing raising.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestOneMembersLadderIsNotReachableByAnother(t *testing.T) {
	ctx, db := traceWorkspace(t)
	mine, theirs := ids.NewV7(), ids.NewV7()

	entry := mailTrace("m-ladder-mine", capture.TraceCaptured)
	entry.UserID = mine
	writeTrace(ctx, t, db, entry, false)
	traceID := firstTraceID(ctx, t, db, "m-ladder-mine")

	store := capture.NewTraceStore(db)
	if _, err := store.LadderByTraceID(asMember(ctx, mine), traceID, false); err != nil {
		t.Fatalf("the owner could not read their own ladder: %v", err)
	}
	// The colleague gets NOT FOUND, not a refusal: confirming the row exists
	// would already disclose that a message reached somebody's mailbox.
	_, err := store.LadderByTraceID(asMember(ctx, theirs), traceID, false)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("a colleague reading another member's trace got %v, want ErrNotFound", err)
	}
}

func TestTheSharedChannelGrantWidensToNullOwnersAndNothingElse(t *testing.T) {
	ctx, db := traceWorkspace(t)
	member := ids.NewV7()

	// A workspace-owned binding: no member owns the row.
	shared := mailTrace("m-ladder-shared", capture.TraceCaptured)
	shared.UserID = ids.Nil
	writeTrace(ctx, t, db, shared, false)
	sharedID := firstTraceID(ctx, t, db, "m-ladder-shared")

	// And one that IS a member's, which the grant must never reach.
	personal := mailTrace("m-ladder-personal", capture.TraceCaptured)
	personal.UserID = ids.NewV7()
	writeTrace(ctx, t, db, personal, false)
	personalID := firstTraceID(ctx, t, db, "m-ladder-personal")

	store := capture.NewTraceStore(db)
	if _, err := store.LadderByTraceID(asMember(ctx, member), sharedID, false); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("a seat without the grant read a shared-channel ladder: %v", err)
	}
	if _, err := store.LadderByTraceID(asManager(ctx, member), sharedID, false); err != nil {
		t.Errorf("a holder of capture_trace could not read the shared-channel ladder: %v", err)
	}
	if _, err := store.LadderByTraceID(asManager(ctx, member), personalID, false); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("the capture_trace grant reached another member's own row: %v", err)
	}
}

func TestALadderCarriesEveryRungIncludingTheOnesTheWindowHides(t *testing.T) {
	// The window read filters to funnel stages; this read must not. A rung the
	// window hides is exactly what a member opens the drawer to see.
	ctx, db := traceWorkspace(t)
	member := ids.NewV7()

	ladder := mailTrace("m-ladder-two-rungs", capture.TraceCaptured)
	ladder.UserID = member
	writeTrace(ctx, t, db, ladder, false)

	fault := mailTrace("m-ladder-two-rungs", capture.TraceFault)
	fault.UserID = member
	fault.Stage = pipelinetrace.StageActivityWrite
	fault.Reason = capture.TraceReasonInvisibleIncumbent
	writeTrace(ctx, t, db, fault, false)

	got, err := capture.NewTraceStore(db).LadderByTraceID(asMember(ctx, member),
		firstTraceID(ctx, t, db, "m-ladder-two-rungs"), false)
	if err != nil {
		t.Fatalf("reading the ladder: %v", err)
	}
	if len(got.Rungs) != 2 {
		t.Fatalf("rungs = %d, want both stages of this one message", len(got.Rungs))
	}
	stages := map[string]bool{}
	for _, r := range got.Rungs {
		stages[r.Stage] = true
	}
	if !stages[string(pipelinetrace.StageTierLadder)] || !stages[string(pipelinetrace.StageActivityWrite)] {
		t.Errorf("stages = %v, want both the ladder and the activity-write rung", stages)
	}
}

func TestOneMessageToTwoColleaguesKeepsTheirRungsApart(t *testing.T) {
	// One email addressed to two colleagues is two trace rows under the SAME
	// natural key with different owners — the routine case, which is why
	// user_id is in the unique index at all.
	//
	// readRungs keys on that natural key, so without its owner predicate a
	// member's ladder splices in their colleague's rungs: their decline reasons,
	// and under the payload posture their stored sender and subject. findRung
	// takes the first match, so the rung one member reads could be the other's
	// decision presented as their own.
	ctx, db := traceWorkspace(t)
	mine, theirs := ids.NewV7(), ids.NewV7()

	for owner, outcome := range map[ids.UUID]capture.TraceOutcome{
		mine:   capture.TraceCaptured,
		theirs: capture.TraceSuppressed,
	} {
		entry := mailTrace("m-two-colleagues", outcome)
		entry.UserID = owner
		entry.Reason = ""
		if outcome == capture.TraceSuppressed {
			entry.Reason = "transactional_infra"
		}
		writeTrace(ctx, t, db, entry, false)
	}

	store := capture.NewTraceStore(db)
	got, err := store.LadderByTraceID(asMember(ctx, mine), myTraceID(ctx, t, db, "m-two-colleagues", mine), false)
	if err != nil {
		t.Fatalf("reading my own ladder: %v", err)
	}
	if len(got.Rungs) != 1 {
		t.Fatalf("rungs = %d, want only my own — the other row belongs to a colleague", len(got.Rungs))
	}
	if got.Rungs[0].Outcome != string(capture.TraceCaptured) {
		t.Errorf("outcome = %q, want my own (captured), not my colleague's", got.Rungs[0].Outcome)
	}
}

// myTraceID reads back the id of the row belonging to one member, so a test
// with two rows under one natural key can ask for the right one.
func myTraceID(ctx context.Context, t *testing.T, db *database.DB, sourceID string, owner ids.UUID) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id FROM capture_trace WHERE source_id = $1 AND user_id = $2`,
			sourceID, owner).Scan(&id)
	}); err != nil {
		t.Fatalf("reading back %s for %s: %v", sourceID, owner, err)
	}
	return id
}

func TestAnInvocationWithNoMemberReadsNoLadder(t *testing.T) {
	// A job tick has no member. Answering "the caller's own rows" for a caller
	// with no id would be answering for whoever owns a NULL-owner row.
	ctx, db := traceWorkspace(t)
	if _, err := capture.NewTraceStore(db).LadderByTraceID(ctx, ids.NewV7(), false); err == nil {
		t.Error("an invocation with no member read a personal ladder")
	}
}

// firstTraceID reads back the id of a row this test wrote, so the ladder is
// asked for by the same handle the screen would use.
func firstTraceID(ctx context.Context, t *testing.T, db *database.DB, sourceID string) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id FROM capture_trace WHERE source_id = $1 ORDER BY occurred_at LIMIT 1`,
			sourceID).Scan(&id)
	}); err != nil {
		t.Fatalf("reading back the trace id for %s: %v", sourceID, err)
	}
	return id
}

func asMember(ctx context.Context, user ids.UUID) context.Context {
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		Permissions: principal.Permissions{RoleKeys: []string{"rep"}, RowScope: principal.RowScopeOwn},
	})
}

// asManager holds the capture_trace object, which is the ONE grant that reaches
// a shared-channel binding's rows — and, per 0258, reaches no member's own.
func asManager(ctx context.Context, user ids.UUID) context.Context {
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		Permissions: principal.Permissions{
			RoleKeys: []string{"manager"},
			Objects:  map[string]principal.ObjectGrant{"capture_trace": {Read: true}},
			RowScope: principal.RowScopeTeam,
		},
	})
}
