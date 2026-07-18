// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The dedupe review queue store (DH-DDL-1, DH-EXT-1/2) over a real
// Postgres: a manual fuzzy create leaves an OPEN candidate the queue
// lists; dismissal suppresses and undoes; merge runs the ONE merge verb
// and cannot be undone; input the queue refuses stays a typed 422; and
// row scope hides a pair whose side the caller cannot see.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedPersonPair creates an incumbent and a fuzzy near-duplicate through
// the real manual-create path — the create itself records the open
// candidate (the PR-12a fold-in under test).
func seedPersonPair(ctx context.Context, t *testing.T, e *dedupeEnv, incumbentName, incumbentEmail, dupName, dupEmail, domain string) (incumbent ids.UUID, created ids.UUID) {
	t.Helper()
	inc, _ := e.seedEmployedPerson(ctx, t, incumbentName, incumbentEmail, "Org "+incumbentName, domain)
	dup, err := e.store.CreatePerson(ctx, CreatePersonInput{
		FullName: dupName, Source: "manual",
		Emails: []PersonEmailInput{{Email: dupEmail, EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("fuzzy create must not block: %v", err)
	}
	return inc.UUID, ids.UUID(dup.Id)
}

func openCandidates(ctx context.Context, t *testing.T, e *dedupeEnv, entityType string) []DedupeCandidateRow {
	t.Helper()
	rows, _, err := e.store.ListDedupeCandidates(ctx, DedupeQueueInput{EntityType: entityType})
	if err != nil {
		t.Fatalf("ListDedupeCandidates: %v", err)
	}
	return rows
}

func TestManualFuzzyCreateLeavesAnOpenDedupeCandidate(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	incumbent, created := seedPersonPair(ctx, t, e, "John Doe", "john@queue.test", "Jon Doe", "jon@queue.test", "queue.test")

	rows := openCandidates(ctx, t, e, "person")
	if len(rows) != 1 {
		t.Fatalf("open queue holds %d candidates, want 1", len(rows))
	}
	c := rows[0]
	if c.Disposition != "open" {
		t.Fatalf("disposition = %s, want open", c.Disposition)
	}
	// Canonical pair: lower id left, whichever side that is.
	got := map[string]bool{c.LeftID.String(): true, c.RightID.String(): true}
	if !got[incumbent.String()] || !got[created.String()] {
		t.Fatalf("pair {%s,%s} does not name incumbent %s + created %s", c.LeftID, c.RightID, incumbent, created)
	}
	if c.Confidence < dedupeReviewThreshold {
		t.Fatalf("confidence %.4f below the review threshold", c.Confidence)
	}
	// The detection-time snapshot names both sides — the queue renders it
	// verbatim, so it must carry the colliding names.
	if ev := string(c.Evidence); !strings.Contains(ev, "Jon Doe") || !strings.Contains(ev, "John Doe") {
		t.Fatalf("evidence %s does not carry both names", ev)
	}

	one, err := e.store.GetDedupeCandidate(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetDedupeCandidate: %v", err)
	}
	if one.ID != c.ID || one.EntityType != "person" {
		t.Fatalf("get returned %s/%s, want %s/person", one.ID, one.EntityType, c.ID)
	}

	if _, err := e.store.GetDedupeCandidate(ctx, ids.NewV7()); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("unknown candidate = %v, want ErrNotFound", err)
	}
}

func TestDedupeQueueRefusesBadInput(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	_, _ = seedPersonPair(ctx, t, e, "Erin Example", "erin@badinput.test", "Eryn Example", "eryn@badinput.test", "badinput.test")
	c := openCandidates(ctx, t, e, "person")[0]

	var input *DedupeInputError
	if _, err := e.store.DisposeDedupeCandidate(ctx, c.ID, "bogus", nil); !errors.As(err, &input) {
		t.Fatalf("bogus disposition = %v, want DedupeInputError", err)
	}
	if _, err := e.store.DisposeDedupeCandidate(ctx, c.ID, "merge", nil); !errors.As(err, &input) {
		t.Fatalf("merge without winner = %v, want DedupeInputError", err)
	}
	stranger := ids.NewV7()
	if _, err := e.store.DisposeDedupeCandidate(ctx, c.ID, "merge", &stranger); !errors.As(err, &input) {
		t.Fatalf("winner outside the pair = %v, want DedupeInputError", err)
	}
	if _, _, err := e.store.ListDedupeCandidates(ctx, DedupeQueueInput{Cursor: "not-base64!"}); !errors.As(err, &input) {
		t.Fatalf("malformed cursor = %v, want DedupeInputError", err)
	}
}

func TestDedupeDismissSuppressesAndUndoReopens(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	_, _ = seedPersonPair(ctx, t, e, "Max Muster", "max@dismiss.test", "Marx Muster", "marx@dismiss.test", "dismiss.test")
	c := openCandidates(ctx, t, e, "person")[0]

	dismissed, err := e.store.DisposeDedupeCandidate(ctx, c.ID, "not_a_duplicate", nil)
	if err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if dismissed.Disposition != "not_a_duplicate" || dismissed.DisposedBy == nil || dismissed.DisposedAt == nil {
		t.Fatalf("dismissed row = %+v, want not_a_duplicate with disposer + timestamp", dismissed)
	}
	if rows := openCandidates(ctx, t, e, "person"); len(rows) != 0 {
		t.Fatalf("dismissed pair still lists open (%d rows)", len(rows))
	}
	byStatus, _, err := e.store.ListDedupeCandidates(ctx, DedupeQueueInput{Status: "not_a_duplicate"})
	if err != nil || len(byStatus) != 1 {
		t.Fatalf("status filter found %d rows (err %v), want the dismissed pair", len(byStatus), err)
	}

	// A decided pair refuses a second decision — conflict, never a
	// silent double-merge.
	if _, err := e.store.DisposeDedupeCandidate(ctx, c.ID, "not_a_duplicate", nil); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("second dispose = %v, want ErrConflict", err)
	}

	reopened, err := e.store.UndoDedupeDisposition(ctx, c.ID)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if reopened.Disposition != "open" || reopened.DisposedBy != nil {
		t.Fatalf("reopened row = %+v, want open with no disposer", reopened)
	}
	// Undoing an already-open pair is a conflict, not a no-op success.
	if _, err := e.store.UndoDedupeDisposition(ctx, c.ID); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("undo on open = %v, want ErrConflict", err)
	}
}

func TestDedupeMergeRunsTheOneMergeVerbAndStandsForever(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	incumbent, created := seedPersonPair(ctx, t, e, "Ada Lovelace", "ada@merge.test", "Ada Lovelance", "adal@merge.test", "merge.test")
	c := openCandidates(ctx, t, e, "person")[0]

	winner := incumbent
	merged, err := e.store.DisposeDedupeCandidate(ctx, c.ID, "merge", &winner)
	if err != nil {
		t.Fatalf("merge dispose: %v", err)
	}
	if merged.Disposition != "merged" {
		t.Fatalf("disposition = %s, want merged", merged.Disposition)
	}
	// The loser carries merged_into_id — the merge verb really ran.
	var mergedInto *ids.UUID
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT merged_into_id FROM person WHERE id = $1`, created).Scan(&mergedInto)
	}); err != nil {
		t.Fatalf("reading loser: %v", err)
	}
	if mergedInto == nil || *mergedInto != winner {
		t.Fatalf("loser merged_into_id = %v, want %s", mergedInto, winner)
	}
	// PO-AC-M6: merge reversal does not exist — the queue must not
	// pretend otherwise.
	if _, err := e.store.UndoDedupeDisposition(ctx, c.ID); !errors.Is(err, ErrNotUndoable) {
		t.Fatalf("undo on merged = %v, want ErrNotUndoable", err)
	}
}

func TestDedupeOrgMergeArm(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	incumbent, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Globex Corporation GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "globex.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Globex Corporation Inc", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "globex-us.test", IsPrimary: true}},
	}); err != nil {
		t.Fatalf("fuzzy org create must not block: %v", err)
	}

	rows := openCandidates(ctx, t, e, "organization")
	if len(rows) != 1 {
		t.Fatalf("open org queue holds %d candidates, want 1", len(rows))
	}
	winner := ids.UUID(incumbent.Id)
	merged, err := e.store.DisposeDedupeCandidate(ctx, rows[0].ID, "merge", &winner)
	if err != nil {
		t.Fatalf("org merge dispose: %v", err)
	}
	if merged.Disposition != "merged" {
		t.Fatalf("disposition = %s, want merged", merged.Disposition)
	}
}

func TestDedupeQueuePagesByConfidence(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	_, _ = seedPersonPair(ctx, t, e, "Kim Page", "kim@page.test", "Kym Page", "kym@page.test", "page.test")
	_, _ = seedPersonPair(ctx, t, e, "Lee Cursor", "lee@cursor.test", "Leigh Cursor", "leigh@cursor.test", "cursor.test")

	first, next, err := e.store.ListDedupeCandidates(ctx, DedupeQueueInput{Limit: 1})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first) != 1 || next == "" {
		t.Fatalf("page 1 = %d rows, cursor %q — want 1 row and a cursor", len(first), next)
	}
	second, _, err := e.store.ListDedupeCandidates(ctx, DedupeQueueInput{Limit: 1, Cursor: next})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(second) != 1 || second[0].ID == first[0].ID {
		t.Fatalf("page 2 re-served the first row")
	}
	// Confidence-descending: the keyset order is the queue's contract.
	if second[0].Confidence > first[0].Confidence {
		t.Fatalf("page order broken: %.4f after %.4f", second[0].Confidence, first[0].Confidence)
	}
}

// asAgent is a non-human principal — the disposition verbs are human-only
// whatever the transport claims.
func (e *dedupeEnv) asAgent() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:test", UserID: e.rep,
		Scopes: principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite),
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"person":       {Create: true, Read: true, Update: true},
				"organization": {Create: true, Read: true, Update: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

func TestDedupeDispositionIsHumanOnly(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	_, _ = seedPersonPair(ctx, t, e, "Pat Human", "pat@human.test", "Patt Human", "patt@human.test", "human.test")
	c := openCandidates(ctx, t, e, "person")[0]

	if _, err := e.store.DisposeDedupeCandidate(e.asAgent(), c.ID, "not_a_duplicate", nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("agent dispose = %v, want ErrPermissionDenied", err)
	}
	if _, err := e.store.UndoDedupeDisposition(e.asAgent(), c.ID); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("agent undo = %v, want ErrPermissionDenied", err)
	}
}

// asOwnScoped is a different human whose row scope is own-only: the pair
// belongs to e.rep, so every candidate read must hide it.
func (e *dedupeEnv) asOwnScoped(other ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + other.String(), UserID: other,
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"person":       {Read: true, Update: true},
				"organization": {Read: true, Update: true},
			},
			RowScope: principal.RowScopeOwn,
		},
	})
}

func TestDedupeQueueHidesPairsOutsideTheCallersRowScope(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	_, _ = seedPersonPair(ctx, t, e, "Vis Owner", "vis@scope.test", "Viz Owner", "viz@scope.test", "scope.test")
	c := openCandidates(ctx, t, e, "person")[0]

	// The pair's people carry no owner_id (ownerless rows are
	// workspace-shared), so first bind them to e.rep to make them private.
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE person SET owner_id = $1`, e.rep)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	other := e.asOwnScoped(ids.NewV7())
	if rows := openCandidates(other, t, e, "person"); len(rows) != 0 {
		t.Fatalf("own-scoped stranger sees %d candidates, want 0", len(rows))
	}
	if _, err := e.store.GetDedupeCandidate(other, c.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("out-of-scope get = %v, want ErrNotFound (existence-hiding)", err)
	}
	// The owner still sees the pair.
	if rows := openCandidates(ctx, t, e, "person"); len(rows) != 1 {
		t.Fatalf("owner sees %d candidates, want 1", len(rows))
	}
}
