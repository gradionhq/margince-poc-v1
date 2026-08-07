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

	// The public suffixes are the dangerous half: they pass every shape check —
	// they have a dot and no stray characters — and each would make every
	// company beneath it internal, silently and unrecoverably.
	for _, bad := range []string{
		"", "   ", "rep@acme.com", "https://acme.com", "acme", "a b.com",
		"com", "co.uk", "com.br", "de",
	} {
		if _, err := store.Add(ctx, bad); err == nil {
			t.Errorf("Add(%q) was accepted, want a refusal naming the problem", bad)
		}
	}
	// A leading @ is a shape people type, not an error.
	if _, err := store.Add(ctx, "@acme.com"); err != nil {
		t.Errorf("Add(\"@acme.com\"): %v", err)
	}
}

// A rep may SEE which domains suppress mail — they are the ones who notice a
// thread missing — and may not change them. The read/update split is the actual
// control, so it is asserted against a rep-shaped grant rather than a principal
// holding nothing at all.
func TestARepReadsTheDomainsAndCannotChangeThem(t *testing.T) {
	ctx, pool := ownDomainWorkspace(t)
	ws, _ := principal.WorkspaceID(ctx)
	rep := principal.WithActor(principal.WithWorkspaceID(context.Background(), ws),
		principal.Principal{
			Type: principal.PrincipalHuman,
			ID:   "human:" + ids.NewV7().String(),
			Permissions: principal.Permissions{
				RoleKeys: []string{"rep"},
				Objects: map[string]principal.ObjectGrant{
					"capture_settings": {Read: true},
				},
				RowScope: principal.RowScopeOwn,
			},
		})
	store := capture.NewOwnDomainStore(pool)

	if _, err := store.List(rep); err != nil {
		t.Errorf("a rep must be able to read the set: %v", err)
	}
	if _, err := store.Add(rep, "acme.com"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Add as a rep: got %v, want permission denied", err)
	}
	if err := store.Remove(rep, "acme.com"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Remove as a rep: got %v, want permission denied", err)
	}
	// "@" normalizes to nothing. The grant is still what decides.
	if err := store.Remove(rep, "@"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Remove(\"@\") as a rep: got %v, want permission denied", err)
	}
}

// One workspace's removal leaves another's identical row alone. The DELETE
// carries no workspace predicate and relies entirely on RLS, so the isolation
// is asserted rather than assumed.
func TestRemovingADomainLeavesAnotherWorkspacesAlone(t *testing.T) {
	first, pool := ownDomainWorkspace(t)
	second, _ := ownDomainWorkspace(t)
	store := capture.NewOwnDomainStore(pool)

	for _, ctx := range []context.Context{first, second} {
		if _, err := store.Add(ctx, "shared.example"); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if err := store.Remove(first, "shared.example"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	list, err := store.List(second)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Domains) != 1 || list.Domains[0].Domain != "shared.example" {
		t.Errorf("the other workspace's set = %+v, want its own row untouched", list.Domains)
	}
}

// The registry is workspace CONFIGURATION, not a record, so its writes are
// audit-only: no outbox envelope, no public event. That makes audit_log the
// ONLY place a change is recorded, and an unaudited write here would leave a
// change to what mail the installation stores with nothing to read it back
// from. Both verbs are checked, because either one alters the set.
//
// Each verb is asserted on the SIDE it writes, not on "either side matches": a
// registration names the domain in `after` and leaves `before` as JSON null,
// and a removal names it in `before` and leaves `after` unset. A predicate that
// accepted either would pass for a row that recorded the wrong direction.
func TestBothWritesLeaveAnAuditRowNamingTheDomain(t *testing.T) {
	ctx, pool := ownDomainWorkspace(t)
	store := capture.NewOwnDomainStore(pool)

	if _, err := store.Add(ctx, "acme.com"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// A first registration claims NO prior state, and says so the way every
	// other audit row does: SQL NULL. It used to store JSON null instead,
	// because a nil map handed to an `any` parameter is not an untyped nil —
	// which made this row invisible to the obvious "before IS NULL" query.
	assertOwnDomainAudited(ctx, t, pool, ownDomainAuditRow{
		action: "update", domain: "acme.com",
		images: "before IS NULL AND after->>'own_email_domain' = $3",
	})

	if err := store.Remove(ctx, "acme.com"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	assertOwnDomainAudited(ctx, t, pool, ownDomainAuditRow{
		action: "archive", domain: "acme.com",
		images: "before->>'own_email_domain' = $3 AND after IS NULL",
	})

	// Audit-only means exactly that: an event would make workspace configuration
	// look like a record change to every subscriber.
	var events int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM event_outbox
			  WHERE envelope->>'entity_type' = 'capture_settings'`).Scan(&events)
	}); err != nil {
		t.Fatalf("counting outbox rows: %v", err)
	}
	if events != 0 {
		t.Errorf("own-domain writes emitted %d outbox rows, want 0 — this surface is audit-only", events)
	}
}

// ownDomainAuditRow is one expected audit row: the verb, the domain it must
// name, and the SQL that pins which image carries it.
type ownDomainAuditRow struct {
	action string
	domain string
	images string
}

// assertOwnDomainAudited fails unless exactly one audit row records that verb
// against this workspace, written by this human, with the images the verb is
// supposed to leave.
func assertOwnDomainAudited(ctx context.Context, t *testing.T, pool *pgxpool.Pool, want ownDomainAuditRow) {
	t.Helper()
	actor, ok := principal.Actor(ctx)
	if !ok {
		t.Fatal("the test context carries no actor, so this would query for a zero id and pass or fail for the wrong reason")
	}
	var rows int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM audit_log
			 WHERE entity_type = 'capture_settings' AND action = $1 AND actor_id = $2
			   AND `+want.images, want.action, actor.ID, want.domain).Scan(&rows)
	}); err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	if rows != 1 {
		t.Errorf("audit rows for %s %s = %d, want exactly 1 — audit_log is the only record this write leaves", want.action, want.domain, rows)
	}
}
