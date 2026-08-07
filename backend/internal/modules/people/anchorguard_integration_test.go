// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The installation's own company outlives every ordinary record operation
// (ADR-0082/A127, PO-AC-45).
//
// Each test asserts the SURVIVING state as well as the refusal, because the
// refusal is not the point: losing the anchor makes the company read answer
// not-found, and the application reads that as a workspace that was never
// configured and returns the whole thing to onboarding. What matters is that
// the company is still readable afterwards.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// anchorProtected reports whether err is the guard's refusal.
func anchorProtected(err error) bool {
	var protected *AnchorProtectedError
	return errors.As(err, &protected)
}

// A refusal is only useful if it names the value the caller must change and
// tells them a move that actually works. Both are easy to get wrong here: a
// merge carries two ids, so blaming the path id for a bad target points at a
// value that was fine; and because NEITHER merge direction is open when one
// side is the anchor, advice that sends the caller the other way round is
// refused one line later by the opposite guard.
func TestEachAnchorRefusalNamesItsFieldAndAWorkingMove(t *testing.T) {
	env := newAnchorEnv(t)
	other := env.newOrganization(t)

	_, archiveErr := env.store.ArchiveOrganization(env.ctx, env.anchorID)
	_, sourceErr := env.store.MergeOrganization(env.ctx, env.anchorID, other)
	_, targetErr := env.store.MergeOrganization(env.ctx, other, env.anchorID)

	for _, tc := range []struct {
		operation string
		err       error
		wantField string
	}{
		{"archiving the anchor", archiveErr, "id"},
		{"merging the anchor away", sourceErr, "id"},
		{"merging a company into the anchor", targetErr, "target_id"},
	} {
		var protected *AnchorProtectedError
		if !errors.As(tc.err, &protected) {
			t.Errorf("%s: got %v, want the anchor refusal", tc.operation, tc.err)
			continue
		}
		field, code, message := protected.FieldFault()
		if field != tc.wantField {
			t.Errorf("%s blames %q, want %q — the client must be pointed at the value it has to change", tc.operation, field, tc.wantField)
		}
		if code != "anchor_protected" {
			t.Errorf("%s: code = %q, want anchor_protected", tc.operation, code)
		}
		_, advice, found := strings.Cut(message, ". ")
		if !found {
			t.Errorf("%s: %q says what is refused but not what to do instead", tc.operation, message)
			continue
		}
		if !strings.Contains(advice, "Archive") || strings.Contains(advice, "erge") {
			t.Errorf("%s advises %q — the only move that works is archiving the duplicate; every merge direction is refused", tc.operation, advice)
		}
	}
	env.assertCompanyStillReadable(t)
}

// The guard's two non-answers, which decide what happens when it cannot say
// "this is the anchor": an absent row belongs to the caller's own not-found
// path, and an unreadable one must stop the operation rather than permit it.
func TestTheGuardPassesOnAnAbsentRowAndRefusesOnAnUnreadableOne(t *testing.T) {
	env := newAnchorEnv(t)
	missing := ids.From[ids.OrganizationKind](ids.NewV7())

	// Absent: the guard steps aside. Called through the store this branch is
	// unreachable — auth.EnsureVisible answers not-found first — so the guard is
	// called directly. Going through ArchiveOrganization would pass on the
	// visibility gate's refusal and prove nothing about this line.
	var absent error
	if err := database.WithWorkspaceTx(env.ctx, env.pool, func(tx pgx.Tx) error {
		absent = refuseIfAnchor(env.ctx, tx, missing, "id", "it cannot be archived")
		return nil
	}); err != nil {
		t.Fatalf("reading the guard against a missing organization: %v", err)
	}
	if absent != nil {
		t.Fatalf("the guard answered %v for a row that does not exist — absence is the caller's not-found path to report, not a refusal", absent)
	}

	// Unreadable: an aborted transaction makes the guard's own SELECT fail. The
	// guard must surface that, because returning nil here would PERMIT the
	// archive or merge it exists to refuse.
	var guardErr error
	if err := database.WithWorkspaceTx(env.ctx, env.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(env.ctx, `SELECT 1 / 0`); err == nil {
			t.Fatal("the poison statement succeeded — the transaction is not aborted, so this proves nothing")
		}
		guardErr = refuseIfAnchor(env.ctx, tx, env.anchorID, "id", "it cannot be archived")
		return errors.New("rolling back the poisoned transaction")
	}); err == nil {
		t.Fatal("the poisoned transaction committed")
	}
	if guardErr == nil {
		t.Fatal("the guard permitted the operation on an unreadable row — a dead connection would open the very hole it closes")
	}
	if anchorProtected(guardErr) {
		t.Fatalf("got the anchor refusal, want the read failure surfaced: %v", guardErr)
	}
	if !strings.Contains(guardErr.Error(), "reading the anchor flag") {
		t.Errorf("guard error = %v, want it to name what it could not read", guardErr)
	}
	env.assertCompanyStillReadable(t)
}

func TestTheAnchorCannotBeArchived(t *testing.T) {
	env := newAnchorEnv(t)

	_, err := env.store.ArchiveOrganization(env.ctx, env.anchorID)
	if !anchorProtected(err) {
		t.Fatalf("ArchiveOrganization on the anchor: got %v, want the anchor refusal", err)
	}
	env.assertCompanyStillReadable(t)
}

func TestTheAnchorCannotBeMergedAway(t *testing.T) {
	env := newAnchorEnv(t)
	other := env.newOrganization(t)

	_, err := env.store.MergeOrganization(env.ctx, env.anchorID, other)
	if !anchorProtected(err) {
		t.Fatalf("merging the anchor away: got %v, want the anchor refusal", err)
	}
	env.assertCompanyStillReadable(t)
}

// The target side matters for a different reason: merging a customer INTO the
// anchor leaves the anchor's own row untouched, so no constraint on it would
// fire — the damage is a customer's people, deals and history relinked onto the
// installation's own company with no way to tell them apart afterwards.
func TestNothingCanBeMergedIntoTheAnchor(t *testing.T) {
	env := newAnchorEnv(t)
	other := env.newOrganization(t)

	_, err := env.store.MergeOrganization(env.ctx, other, env.anchorID)
	if !anchorProtected(err) {
		t.Fatalf("merging a company into the anchor: got %v, want the anchor refusal", err)
	}
	env.assertCompanyStillReadable(t)
}

// The schema refuses it too, so a writer that never learned about the guard —
// a future maintenance job, a direct store path — cannot reopen the hole. The
// service check exists to give a human a sentence; this is the guarantee.
func TestTheSchemaRefusesToRetireTheAnchor(t *testing.T) {
	env := newAnchorEnv(t)

	err := database.WithWorkspaceTx(env.ctx, env.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(env.ctx,
			`UPDATE organization SET archived_at = now() WHERE id = $1`, env.anchorID)
		return err
	})
	if err == nil {
		t.Fatal("a direct archive of the anchor was accepted — the schema must refuse it")
	}
	env.assertCompanyStillReadable(t)
}

// The anchor is excluded from the list that answers "which companies are we
// selling to", and present when a caller asks for it (PO-AC-43).
func TestTheAnchorIsAbsentFromTheCompanyListUntilAskedFor(t *testing.T) {
	env := newAnchorEnv(t)
	env.newOrganization(t)

	listed, _, err := env.store.ListOrganizations(env.ctx, ListOrganizationsInput{})
	if err != nil {
		t.Fatalf("ListOrganizations: %v", err)
	}
	for _, org := range listed {
		if org.Id == openapiUUID(env.anchorID) {
			t.Fatal("the workspace's own company must not appear among the accounts it sells to")
		}
	}

	withAnchor, _, err := env.store.ListOrganizations(env.ctx,
		ListOrganizationsInput{IncludeAnchor: true})
	if err != nil {
		t.Fatalf("ListOrganizations(include_anchor): %v", err)
	}
	var found bool
	for _, org := range withAnchor {
		if org.Id == openapiUUID(env.anchorID) {
			found = true
			if org.IsAnchor == nil || !*org.IsAnchor {
				t.Error("the anchor must be identifiable on the wire — a caller offering company actions has to tell it apart")
			}
		}
	}
	if !found {
		t.Error("include_anchor must make the own company reachable, not merely unhidden")
	}
	if len(withAnchor) != len(listed)+1 {
		t.Errorf("include_anchor changed the result by %d rows, want exactly the anchor", len(withAnchor)-len(listed))
	}
}

// anchorEnv is one workspace whose own company exists, plus an admin context.
type anchorEnv struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	store    *Store
	anchorID ids.OrganizationID
}

func newAnchorEnv(t *testing.T) *anchorEnv {
	t.Helper()
	base := setupCapturePrivacy(t)
	ctx := principal.WithCorrelationID(
		principal.WithWorkspaceID(context.Background(), base.ws), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + base.admin.String(), UserID: base.admin,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"organization": {Create: true, Read: true, Update: true, Delete: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})

	company, err := base.store.SaveCompany(ctx, SaveCompanyInput{DisplayName: "Our Company"})
	if err != nil {
		t.Fatalf("seeding the anchor company: %v", err)
	}
	return &anchorEnv{
		ctx: ctx, pool: base.store.pool, store: base.store,
		anchorID: company.OrganizationID,
	}
}

// newOrganization creates an ordinary customer company — the other side of
// every refusal here, and the thing the anchor must stay distinguishable from.
func (e *anchorEnv) newOrganization(t *testing.T) ids.OrganizationID {
	t.Helper()
	org, err := e.store.CreateOrganization(e.ctx, CreateOrganizationInput{DisplayName: "Brandt GmbH"})
	if err != nil {
		t.Fatalf("creating the customer company: %v", err)
	}
	return ids.OrganizationID{UUID: ids.UUID(org.Id)}
}

// assertCompanyStillReadable checks the durable row, not only that the read
// answers. A refusal returned from inside the transaction rolls back by
// construction, so asserting the read alone would pass for a guard that refused
// AFTER a partial write; the row's own state is what would catch that.
func (e *anchorEnv) assertCompanyStillReadable(t *testing.T) {
	t.Helper()
	if _, err := e.store.GetCompany(e.ctx); err != nil {
		t.Fatalf("the company read must survive a refused operation, got %v — a workspace whose anchor is gone reads as one that was never set up", err)
	}
	var intact bool
	if err := database.WithWorkspaceTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `
			SELECT is_anchor AND archived_at IS NULL AND merged_into_id IS NULL
			  FROM organization WHERE id = $1`, e.anchorID).Scan(&intact)
	}); err != nil {
		t.Fatalf("reading the anchor row: %v", err)
	}
	if !intact {
		t.Fatal("the anchor row was touched by a refused operation")
	}
}

// The trigger is the half of the guard no service call can reach: a merge
// refused by the store never writes merged_into_id, so only a direct write
// proves the trigger exists, fires on the column, and names the constraint the
// error classifier reads.
func TestTheSchemaRefusesAMergeIntoTheAnchor(t *testing.T) {
	env := newAnchorEnv(t)
	other := env.newOrganization(t)

	err := database.WithWorkspaceTx(env.ctx, env.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(env.ctx,
			`UPDATE organization SET merged_into_id = $1 WHERE id = $2`, env.anchorID, other)
		return err
	})
	if err == nil {
		t.Fatal("a direct merge into the anchor was accepted — the trigger must refuse it")
	}
	name, ok := storekit.CheckViolation(err)
	if !ok {
		t.Fatalf("got %v, want a check violation the error classifier can answer 422 for", err)
	}
	if name != "organization_anchor_is_permanent" {
		t.Errorf("constraint = %q, want organization_anchor_is_permanent — the name is what the classifier reads", name)
	}
	env.assertCompanyStillReadable(t)
}

// Clearing is_anchor and archiving in one statement satisfies the CHECK, which
// only reads the row's final state — so the demotion arm of the trigger is what
// stands between a direct writer and an installation left with no anchor at all.
func TestTheSchemaRefusesDemotingTheAnchor(t *testing.T) {
	env := newAnchorEnv(t)

	err := database.WithWorkspaceTx(env.ctx, env.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(env.ctx,
			`UPDATE organization SET is_anchor = false, archived_at = now() WHERE id = $1`, env.anchorID)
		return err
	})
	if err == nil {
		t.Fatal("clearing is_anchor while archiving was accepted — the workspace would read as never configured")
	}
	name, ok := storekit.CheckViolation(err)
	if !ok {
		t.Fatalf("got %v, want a check violation the error classifier can answer 422 for", err)
	}
	if name != "organization_anchor_is_permanent" {
		t.Errorf("constraint = %q, want organization_anchor_is_permanent — the name is what the classifier reads", name)
	}
	env.assertCompanyStillReadable(t)
}

// openapiUUID renders a typed id in the wire model's uuid type, so a
// comparison against a contract struct reads as one.
func openapiUUID(id ids.OrganizationID) openapi_types.UUID {
	return openapi_types.UUID(id.UUID)
}
