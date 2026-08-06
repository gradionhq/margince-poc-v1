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
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// anchorProtected reports whether err is the guard's refusal.
func anchorProtected(err error) bool {
	var protected *AnchorProtectedError
	return errors.As(err, &protected)
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
	other := env.newOrganization(t, "Brandt GmbH")

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
	other := env.newOrganization(t, "Brandt GmbH")

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
	env.newOrganization(t, "Brandt GmbH")

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

// newOrganization creates an ordinary customer company.
func (e *anchorEnv) newOrganization(t *testing.T, name string) ids.OrganizationID {
	t.Helper()
	org, err := e.store.CreateOrganization(e.ctx, CreateOrganizationInput{DisplayName: name})
	if err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	return ids.OrganizationID{UUID: ids.UUID(org.Id)}
}

func (e *anchorEnv) assertCompanyStillReadable(t *testing.T) {
	t.Helper()
	if _, err := e.store.GetCompany(e.ctx); err != nil {
		t.Fatalf("the company read must survive a refused operation, got %v — a workspace whose anchor is gone reads as one that was never set up", err)
	}
}

// openapiUUID renders a typed id in the wire model's uuid type, so a
// comparison against a contract struct reads as one.
func openapiUUID(id ids.OrganizationID) openapi_types.UUID {
	return openapi_types.UUID(id.UUID)
}
