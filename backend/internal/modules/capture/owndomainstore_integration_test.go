// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The administrator's own-domain surface (CAP-WIRE-2a). What this set contains
// decides whether correspondence is stored, so its writes are proven against
// the real schema and the RBAC gate rather than a mock's bookkeeping.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// adminOwnDomainContext binds an administrator who may change capture settings.
func adminOwnDomainContext(ctx context.Context, ws ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman,
		ID:   "human:" + ids.NewV7().String(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"capture_settings": {Read: true, Update: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

func ownDomainWorkspace(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	owner, pool := setupCaptureDB(t)
	ctx := context.Background()
	ws := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Own Domains', $2, 'EUR')`,
		ws, "own-domains-"+ws.String()); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
	return adminOwnDomainContext(ctx, ws), pool
}

// An administrator adding a domain IS the human vouching for it, so it takes
// effect without a second confirmation — and it is what makes the drop fire.
func TestAnAdministratorsDomainIsVerifiedAndSuppressesMail(t *testing.T) {
	ctx, pool := ownDomainWorkspace(t)
	store := capture.NewOwnDomainStore(pool)

	added, err := store.Add(ctx, "Acme.COM")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if added.Domain != "acme.com" {
		t.Errorf("stored domain = %q, want it folded to acme.com", added.Domain)
	}
	if !added.Verified || added.Source != "admin" {
		t.Errorf("stored source=%q verified=%v, want admin/true", added.Source, added.Verified)
	}

	// The consequence, not just the row: mail among that domain's addresses
	// stops being stored.
	// The sink runs as the connector, never as the human who configured it.
	ws, _ := principal.WorkspaceID(ctx)
	sink := capture.NewSink(pool)
	if _, err := sink.Upsert(mailSinkContext(context.Background(), ws),
		mailRecord("admin-dom-1", "boss@acme.com",
			"boss@acme.com", "rep@acme.com")); !isSkip(err) {
		t.Fatalf("after an admin registers acme.com the mail must be dropped: %v", err)
	}
}

// Adding a domain a mailbox already contributed confirms it rather than failing
// — that is the whole point of the candidate row.
func TestAddingACandidateDomainConfirmsIt(t *testing.T) {
	ctx, pool := ownDomainWorkspace(t)
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO workspace_email_domain (workspace_id, domain, source, verified)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'acme.com', 'mailbox', false)`)
		return err
	}); err != nil {
		t.Fatalf("seeding the candidate: %v", err)
	}
	store := capture.NewOwnDomainStore(pool)

	added, err := store.Add(ctx, "acme.com")
	if err != nil {
		t.Fatalf("Add over a candidate: %v", err)
	}
	if !added.Verified || added.Source != "admin" {
		t.Errorf("source=%q verified=%v, want the candidate confirmed", added.Source, added.Verified)
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Domains) != 1 {
		t.Errorf("%d rows, want the one domain confirmed rather than duplicated", len(list.Domains))
	}
}

// Removing a domain stops the drop from the next message on.
func TestRemovingADomainLetsItsMailBeCapturedAgain(t *testing.T) {
	ctx, pool := ownDomainWorkspace(t)
	store := capture.NewOwnDomainStore(pool)
	if _, err := store.Add(ctx, "acme.com"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Remove(ctx, "acme.com"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	ws, _ := principal.WorkspaceID(ctx)
	sink := capture.NewSink(pool)
	if _, err := sink.Upsert(mailSinkContext(context.Background(), ws),
		mailRecord("removed-dom-1", "boss@acme.com",
			"boss@acme.com", "rep@acme.com")); err != nil {
		t.Fatalf("once the domain is removed the mail must be kept: %v", err)
	}
	// Removing one that was never there is not an error: the caller asked for a
	// state, and that state already holds.
	if err := store.Remove(ctx, "never-registered.example"); err != nil {
		t.Errorf("removing an unregistered domain: %v", err)
	}
}

// The list reports what the company itself claims separately from the registry:
// those domains are in force but are changed on the company page, so offering
// them as removable rows would promise an action this surface cannot perform.
func TestTheListSeparatesTheCompanysOwnClaimFromTheRegistry(t *testing.T) {
	ctx, pool := ownDomainWorkspace(t)
	ws, _ := principal.WorkspaceID(ctx)
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		orgID := ids.NewV7()
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization (id, workspace_id, display_name, is_anchor, source, captured_by)
			VALUES ($1, $2, 'Our Company', true, 'manual', 'human:test')`, orgID, ws); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
			VALUES ($1, $2, 'ourcompany.example', true, 'manual', 'human:test')`, ws, orgID)
		return err
	}); err != nil {
		t.Fatalf("seeding the anchor company: %v", err)
	}
	store := capture.NewOwnDomainStore(pool)
	if _, err := store.Add(ctx, "acme.com"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Domains) != 1 || list.Domains[0].Domain != "acme.com" {
		t.Errorf("registry = %+v, want only the registered domain", list.Domains)
	}
	if len(list.AnchorDomains) != 1 || list.AnchorDomains[0] != "ourcompany.example" {
		t.Errorf("company claim = %v, want ourcompany.example reported apart from the registry", list.AnchorDomains)
	}
}

// A value that is not a bare domain is refused with what to do about it. The
// set decides whether mail is stored, so folding a mistyped value into
// something that silently matches nothing would be the worse failure.
func TestAValueThatIsNotADomainIsRefused(t *testing.T) {
	ctx, pool := ownDomainWorkspace(t)
	store := capture.NewOwnDomainStore(pool)

	for _, bad := range []string{"", "   ", "rep@acme.com", "https://acme.com", "acme", "a b.com"} {
		if _, err := store.Add(ctx, bad); err == nil {
			t.Errorf("Add(%q) was accepted, want a refusal naming the problem", bad)
		}
	}
	// A leading @ is a shape people type, not an error.
	if _, err := store.Add(ctx, "@acme.com"); err != nil {
		t.Errorf("Add(\"@acme.com\"): %v", err)
	}
}

// Without the capture-settings grant the surface is closed, whichever verb.
func TestOwnDomainsRefuseACallerWithoutTheGrant(t *testing.T) {
	ctx, pool := ownDomainWorkspace(t)
	ws, _ := principal.WorkspaceID(ctx)
	ungranted := principal.WithActor(principal.WithWorkspaceID(context.Background(), ws),
		principal.Principal{
			Type:        principal.PrincipalHuman,
			ID:          "human:" + ids.NewV7().String(),
			Permissions: principal.Permissions{RoleKeys: []string{"rep"}, RowScope: principal.RowScopeOwn},
		})
	store := capture.NewOwnDomainStore(pool)

	if _, err := store.List(ungranted); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("List without the grant: got %v, want permission denied", err)
	}
	if _, err := store.Add(ungranted, "acme.com"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Add without the grant: got %v, want permission denied", err)
	}
	if err := store.Remove(ungranted, "acme.com"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Remove without the grant: got %v, want permission denied", err)
	}
}
