// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The quotas store suite (RD-T06): owner-XOR-team enforced BEFORE the
// insert (and re-validated on an update's MERGED state), composite
// tenant FKs answering absence for a foreign or unknown owner/team,
// optimistic If-Match updates, idempotent archive that never writes a
// second audit row, keyset listing with owner/team filters, the
// pipeline/product config RBAC posture (everyone with quota.read sees
// every target; only quota.create/update/delete mutates), and RLS
// tenant isolation.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/quotas"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// quotaAdminPerms mirrors the 0068 admin/ops grant: full quota authority.
var quotaAdminPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"quota": {Create: true, Read: true, Update: true, Delete: true},
	},
	RowScope: principal.RowScopeAll,
}

// quotaRepPerms mirrors the 0068 manager/rep/read_only grant: read only —
// a rep sees the targets, never sets one.
var quotaRepPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects:  map[string]principal.ObjectGrant{"quota": {Read: true}},
	RowScope: principal.RowScopeTeam,
}

// q1 spans 2026 Q1 — the suite's default quota period.
var q1Start, q1End = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

func ownerQuotaInput(owner ids.UUID, target int64) quotas.CreateQuotaInput {
	return quotas.CreateQuotaInput{
		OwnerID: &owner, PeriodStart: q1Start, PeriodEnd: q1End,
		TargetMinor: target, Currency: "EUR",
	}
}

func TestQuotaCreate_OwnerAndTeamQuotas(t *testing.T) {
	e := Setup(t)
	store := quotas.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	owned, err := store.CreateQuota(ctx, ownerQuotaInput(e.Rep1, 28000000))
	if err != nil {
		t.Fatalf("owner-quota create: %v", err)
	}
	if owned.OwnerId == nil || ids.UUID(*owned.OwnerId) != e.Rep1 || owned.TeamId != nil {
		t.Fatalf("owner-quota must carry owner_id=%s and no team_id, got %+v", e.Rep1, owned)
	}
	if owned.TargetMinor != 28000000 || owned.Currency != "EUR" {
		t.Fatalf("target/currency must echo the human-set values, got %+v", owned)
	}
	if owned.PeriodStart.Format(time.DateOnly) != "2026-01-01" || owned.PeriodEnd.Format(time.DateOnly) != "2026-03-31" {
		t.Fatalf("period must echo date-granular, got %s..%s", owned.PeriodStart, owned.PeriodEnd)
	}
	if owned.Version == nil || *owned.Version != 1 || owned.ArchivedAt != nil {
		t.Fatalf("a fresh quota carries version 1 and no archived_at, got %+v", owned)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'quota' AND entity_id = $1 AND action = 'create'`,
		ids.UUID(owned.Id)); n != 1 {
		t.Fatalf("create audit rows = %d, want exactly 1", n)
	}

	team, err := store.CreateQuota(ctx, quotas.CreateQuotaInput{
		TeamID: &e.Team1, PeriodStart: q1Start, PeriodEnd: q1End,
		TargetMinor: 100000000, Currency: "EUR",
	})
	if err != nil {
		t.Fatalf("team-quota create: %v", err)
	}
	if team.TeamId == nil || ids.UUID(*team.TeamId) != e.Team1 || team.OwnerId != nil {
		t.Fatalf("team-quota must carry team_id=%s and no owner_id, got %+v", e.Team1, team)
	}
}

func TestQuotaCreate_XORRefusedBeforeInsert(t *testing.T) {
	e := Setup(t)
	store := quotas.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	both := ownerQuotaInput(e.Rep1, 1000)
	both.TeamID = &e.Team1
	if _, err := store.CreateQuota(ctx, both); !isOwnerXorTeam(err) {
		t.Fatalf("both owner_id and team_id must answer OwnerXorTeamError, got %v", err)
	}

	neither := quotas.CreateQuotaInput{PeriodStart: q1Start, PeriodEnd: q1End, TargetMinor: 1000, Currency: "EUR"}
	if _, err := store.CreateQuota(ctx, neither); !isOwnerXorTeam(err) {
		t.Fatalf("neither owner_id nor team_id must answer OwnerXorTeamError, got %v", err)
	}

	// The refusal happens before the INSERT: no row, no audit residue.
	if n := e.WsCount(t, `SELECT count(*) FROM quota`); n != 0 {
		t.Fatalf("a refused create must write no quota row, found %d", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_type = 'quota'`); n != 0 {
		t.Fatalf("a refused create must write no audit row, found %d", n)
	}
}

func TestQuotaCreate_UnknownOwnerOrTeamAnswersNotFound(t *testing.T) {
	e := Setup(t)
	store := quotas.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	ghost := ids.NewV7()
	if _, err := store.CreateQuota(ctx, ownerQuotaInput(ghost, 1000)); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an owner_id with no app_user in this workspace must answer ErrNotFound (composite FK), got %v", err)
	}
	if _, err := store.CreateQuota(ctx, quotas.CreateQuotaInput{
		TeamID: &ghost, PeriodStart: q1Start, PeriodEnd: q1End, TargetMinor: 1000, Currency: "EUR",
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("a team_id with no team in this workspace must answer ErrNotFound (composite FK), got %v", err)
	}
}

func TestQuotaGet_LiveArchivedAndAbsent(t *testing.T) {
	e := Setup(t)
	store := quotas.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	created, err := store.CreateQuota(ctx, ownerQuotaInput(e.Rep1, 1000))
	if err != nil {
		t.Fatal(err)
	}
	id := ids.UUID(created.Id)

	got, err := store.GetQuota(ctx, id, storekit.LiveOnly)
	if err != nil || ids.UUID(got.Id) != id {
		t.Fatalf("GetQuota(live) = %+v, %v", got, err)
	}
	if _, err := store.GetQuota(ctx, ids.NewV7(), storekit.LiveOnly); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an unknown id must answer ErrNotFound, got %v", err)
	}

	if _, err := store.ArchiveQuota(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetQuota(ctx, id, storekit.LiveOnly); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an archived quota reads as absent under LiveOnly, got %v", err)
	}
	archived, err := store.GetQuota(ctx, id, storekit.IncludeArchived)
	if err != nil || archived.ArchivedAt == nil {
		t.Fatalf("an archived quota stays fetchable by id under IncludeArchived, got %+v, %v", archived, err)
	}
}

func TestQuotaUpdate_HappyVersionSkewAndMergedXOR(t *testing.T) {
	e := Setup(t)
	store := quotas.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	created, err := store.CreateQuota(ctx, ownerQuotaInput(e.Rep1, 28000000))
	if err != nil {
		t.Fatal(err)
	}
	id := ids.UUID(created.Id)

	v1 := int64(1)
	newTarget := int64(31000000)
	updated, err := store.UpdateQuota(ctx, id, quotas.UpdateQuotaInput{TargetMinor: &newTarget, IfVersion: &v1})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.TargetMinor != newTarget || updated.Version == nil || *updated.Version != 2 {
		t.Fatalf("update must apply the new target and bump version to 2, got %+v", updated)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'quota' AND entity_id = $1 AND action = 'update'`,
		id); n != 1 {
		t.Fatalf("update audit rows = %d, want exactly 1", n)
	}

	// The stale If-Match answers version skew, and applies nothing.
	if _, err := store.UpdateQuota(ctx, id, quotas.UpdateQuotaInput{TargetMinor: &newTarget, IfVersion: &v1}); !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("a stale If-Match must answer ErrVersionSkew, got %v", err)
	}

	// XOR re-validated on the MERGED state: this quota already has an
	// owner, so patching a team onto it would leave both set.
	if _, err := store.UpdateQuota(ctx, id, quotas.UpdateQuotaInput{TeamID: &e.Team1}); !isOwnerXorTeam(err) {
		t.Fatalf("patching team_id onto an owner-quota must answer OwnerXorTeamError, got %v", err)
	}

	// Reassigning within the same side stays valid — and an owner not in
	// this workspace answers absence, exactly like create.
	reassigned, err := store.UpdateQuota(ctx, id, quotas.UpdateQuotaInput{OwnerID: &e.Rep2})
	if err != nil || reassigned.OwnerId == nil || ids.UUID(*reassigned.OwnerId) != e.Rep2 {
		t.Fatalf("owner reassignment = %+v, %v", reassigned, err)
	}
	ghost := ids.NewV7()
	if _, err := store.UpdateQuota(ctx, id, quotas.UpdateQuotaInput{OwnerID: &ghost}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("reassigning to an unknown owner must answer ErrNotFound (composite FK), got %v", err)
	}

	// An empty patch is a no-op: current entity back, no version bump,
	// no audit row.
	unchanged, err := store.UpdateQuota(ctx, id, quotas.UpdateQuotaInput{})
	if err != nil || *unchanged.Version != *reassigned.Version {
		t.Fatalf("an empty patch must return the row unchanged, got %+v, %v", unchanged, err)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'quota' AND entity_id = $1 AND action = 'update'`,
		id); n != 2 {
		t.Fatalf("update audit rows after no-op = %d, want 2 (the two real updates)", n)
	}
}

func TestQuotaUpdate_PeriodAndCurrency(t *testing.T) {
	e := Setup(t)
	store := quotas.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	created, err := store.CreateQuota(ctx, ownerQuotaInput(e.Rep1, 1000))
	if err != nil {
		t.Fatal(err)
	}
	q2Start, q2End := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	usd := "USD"
	updated, err := store.UpdateQuota(ctx, ids.UUID(created.Id), quotas.UpdateQuotaInput{
		PeriodStart: &q2Start, PeriodEnd: &q2End, Currency: &usd,
	})
	if err != nil {
		t.Fatalf("period/currency update: %v", err)
	}
	if updated.PeriodStart.Format(time.DateOnly) != "2026-04-01" ||
		updated.PeriodEnd.Format(time.DateOnly) != "2026-06-30" || updated.Currency != "USD" {
		t.Fatalf("period/currency must apply date-granular, got %+v", updated)
	}
}

func TestQuotaArchive_IdempotentAndAuditedOnce(t *testing.T) {
	e := Setup(t)
	store := quotas.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	created, err := store.CreateQuota(ctx, ownerQuotaInput(e.Rep1, 1000))
	if err != nil {
		t.Fatal(err)
	}
	id := ids.UUID(created.Id)

	first, err := store.ArchiveQuota(ctx, id)
	if err != nil || first.ArchivedAt == nil {
		t.Fatalf("archive must return the full entity with archived_at set, got %+v, %v", first, err)
	}
	repeat, err := store.ArchiveQuota(ctx, id)
	if err != nil || repeat.ArchivedAt == nil {
		t.Fatalf("a repeat archive is a no-op returning the same entity, got %+v, %v", repeat, err)
	}
	if !repeat.ArchivedAt.Equal(*first.ArchivedAt) {
		t.Fatalf("a repeat archive must not move archived_at: %v then %v", first.ArchivedAt, repeat.ArchivedAt)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'quota' AND entity_id = $1 AND action = 'archive'`,
		id); n != 1 {
		t.Fatalf("archive audit rows = %d, want exactly 1 — a repeat archive writes nothing", n)
	}
	if _, err := store.ArchiveQuota(ctx, ids.NewV7()); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("archiving an unknown id must answer ErrNotFound, got %v", err)
	}
}

func TestQuotaList_KeysetFiltersAndArchived(t *testing.T) {
	e := Setup(t)
	store := quotas.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	for _, owner := range []ids.UUID{e.Rep1, e.Rep2, e.Rep3} {
		if _, err := store.CreateQuota(ctx, ownerQuotaInput(owner, 1000)); err != nil {
			t.Fatal(err)
		}
	}
	team, err := store.CreateQuota(ctx, quotas.CreateQuotaInput{
		TeamID: &e.Team1, PeriodStart: q1Start, PeriodEnd: q1End, TargetMinor: 5000, Currency: "EUR",
	})
	if err != nil {
		t.Fatal(err)
	}
	tombstone, err := store.CreateQuota(ctx, ownerQuotaInput(e.Rep1, 9000))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ArchiveQuota(ctx, ids.UUID(tombstone.Id)); err != nil {
		t.Fatal(err)
	}

	// Keyset walk at limit 2: 4 live rows arrive in 2+2 with no overlap.
	limit := 2
	first, page, err := store.ListQuotas(ctx, quotas.ListQuotasInput{Limit: &limit})
	if err != nil || len(first) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("page 1 = %d rows, page %+v, %v — want 2 rows and a next cursor", len(first), page, err)
	}
	second, page2, err := store.ListQuotas(ctx, quotas.ListQuotasInput{Limit: &limit, Cursor: &page.NextCursor})
	if err != nil || len(second) != 2 || page2.HasMore {
		t.Fatalf("page 2 = %d rows, page %+v, %v — want the final 2 rows", len(second), page2, err)
	}
	seen := map[ids.UUID]bool{}
	for _, q := range append(first, second...) {
		if seen[ids.UUID(q.Id)] {
			t.Fatalf("keyset pages overlap on %s", q.Id)
		}
		seen[ids.UUID(q.Id)] = true
	}
	if seen[ids.UUID(tombstone.Id)] {
		t.Fatal("the archived quota must not appear in a default list")
	}

	// Filters narrow to the named owner/team.
	mine, _, err := store.ListQuotas(ctx, quotas.ListQuotasInput{OwnerID: &e.Rep1})
	if err != nil || len(mine) != 1 || mine[0].OwnerId == nil || ids.UUID(*mine[0].OwnerId) != e.Rep1 {
		t.Fatalf("owner_id filter = %d rows (%v), want exactly Rep1's live quota", len(mine), err)
	}
	teams, _, err := store.ListQuotas(ctx, quotas.ListQuotasInput{TeamID: &e.Team1})
	if err != nil || len(teams) != 1 || ids.UUID(teams[0].Id) != ids.UUID(team.Id) {
		t.Fatalf("team_id filter = %d rows (%v), want exactly the Team1 quota", len(teams), err)
	}

	// include_archived surfaces the tombstone again.
	all, _, err := store.ListQuotas(ctx, quotas.ListQuotasInput{IncludeArchived: true})
	if err != nil || len(all) != 5 {
		t.Fatalf("include_archived list = %d rows (%v), want all 5", len(all), err)
	}
}

func TestQuotaList_SortVocabulary(t *testing.T) {
	e := Setup(t)
	store := quotas.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	// Three owner-quotas whose period_start order deliberately disagrees
	// with insertion (= created_at) order, so a sorted list is
	// distinguishable from the default ordering.
	starts := []time.Time{
		time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}
	targets := []int64{2000, 3000, 1000}
	for i, owner := range []ids.UUID{e.Rep1, e.Rep2, e.Rep3} {
		if _, err := store.CreateQuota(ctx, quotas.CreateQuotaInput{
			OwnerID: &owner, PeriodStart: starts[i], PeriodEnd: starts[i].AddDate(0, 3, 0),
			TargetMinor: targets[i], Currency: "EUR",
		}); err != nil {
			t.Fatal(err)
		}
	}

	asc := "period_start"
	sorted, _, err := store.ListQuotas(ctx, quotas.ListQuotasInput{Sort: &asc})
	if err != nil {
		t.Fatalf("sort=period_start: %v", err)
	}
	if len(sorted) != 3 {
		t.Fatalf("sorted list = %d rows, want 3", len(sorted))
	}
	for i := 1; i < len(sorted); i++ {
		if sorted[i].PeriodStart.Time.Before(sorted[i-1].PeriodStart.Time) {
			t.Fatalf("sort=period_start out of order at %d: %s after %s",
				i, sorted[i].PeriodStart.Format(time.DateOnly), sorted[i-1].PeriodStart.Format(time.DateOnly))
		}
	}

	descTarget := "-target_minor"
	byTarget, _, err := store.ListQuotas(ctx, quotas.ListQuotasInput{Sort: &descTarget})
	if err != nil {
		t.Fatalf("sort=-target_minor: %v", err)
	}
	for i := 1; i < len(byTarget); i++ {
		if byTarget[i].TargetMinor > byTarget[i-1].TargetMinor {
			t.Fatalf("sort=-target_minor out of order at %d: %d after %d",
				i, byTarget[i].TargetMinor, byTarget[i-1].TargetMinor)
		}
	}

	// A sorted keyset walk continues the SAME order across pages.
	limit := 2
	page1, page, err := store.ListQuotas(ctx, quotas.ListQuotasInput{Sort: &asc, Limit: &limit})
	if err != nil || len(page1) != 2 || !page.HasMore {
		t.Fatalf("sorted page 1 = %d rows, page %+v, %v — want 2 rows and a next cursor", len(page1), page, err)
	}
	page2, _, err := store.ListQuotas(ctx, quotas.ListQuotasInput{Sort: &asc, Limit: &limit, Cursor: &page.NextCursor})
	if err != nil || len(page2) != 1 {
		t.Fatalf("sorted page 2 = %d rows, %v — want the final row", len(page2), err)
	}
	if page2[0].PeriodStart.Time.Before(page1[1].PeriodStart.Time) {
		t.Fatalf("sorted keyset continuation broke the order: %s after %s",
			page2[0].PeriodStart.Format(time.DateOnly), page1[1].PeriodStart.Format(time.DateOnly))
	}

	// Outside the closed vocabulary — including any cf_ column: quota is
	// not a custom-field object — the refusal is typed, never a guess.
	for _, garbage := range []string{"banana", "cf_region"} {
		spec := garbage
		_, _, err := store.ListQuotas(ctx, quotas.ListQuotasInput{Sort: &spec})
		var sortErr *storekit.SortError
		if !errors.As(err, &sortErr) || sortErr.Code != storekit.CodeSortFieldNotAllowed {
			t.Fatalf("sort=%s must answer SortError %s, got %v", garbage, storekit.CodeSortFieldNotAllowed, err)
		}
	}
}

func TestQuotaRBAC_RepReadsButNeverMutates(t *testing.T) {
	e := Setup(t)
	store := quotas.NewStore(e.Pool)
	admin := e.As(e.Rep1, nil, quotaAdminPerms)
	rep := e.As(e.Rep2, []ids.UUID{e.Team1}, quotaRepPerms)

	created, err := store.CreateQuota(admin, ownerQuotaInput(e.Rep3, 1000))
	if err != nil {
		t.Fatal(err)
	}
	id := ids.UUID(created.Id)

	// Read side: quota follows the pipeline/product config posture —
	// workspace-shared, so the rep sees a target measured on ANOTHER
	// team's member (Rep3 is in Team2, the rep in Team1).
	if _, err := store.GetQuota(rep, id, storekit.LiveOnly); err != nil {
		t.Fatalf("a rep with quota.read must see every workspace target, got %v", err)
	}
	listed, _, err := store.ListQuotas(rep, quotas.ListQuotasInput{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("rep list = %d rows (%v), want the one workspace quota", len(listed), err)
	}

	// Mutation side: every verb is refused at the object gate.
	if _, err := store.CreateQuota(rep, ownerQuotaInput(e.Rep2, 1000)); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("rep create must answer ErrPermissionDenied, got %v", err)
	}
	target := int64(2000)
	if _, err := store.UpdateQuota(rep, id, quotas.UpdateQuotaInput{TargetMinor: &target}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("rep update must answer ErrPermissionDenied, got %v", err)
	}
	if _, err := store.ArchiveQuota(rep, id); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("rep archive must answer ErrPermissionDenied, got %v", err)
	}
	// The denials wrote nothing.
	if n := e.WsCount(t, `SELECT count(*) FROM quota`); n != 1 {
		t.Fatalf("denied mutations must leave the single admin-created quota, found %d rows", n)
	}
}

func TestQuotaRLS_TenantIsolation(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	store := quotas.NewStore(e.Pool)
	ctxA := e.As(e.Rep1, nil, quotaAdminPerms)

	created, err := store.CreateQuota(ctxA, ownerQuotaInput(e.Rep1, 1000))
	if err != nil {
		t.Fatal(err)
	}

	// Tenant B, full quota authority — RLS, not RBAC, is what walls it off.
	wsB := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Quota Tenant B', $2, 'EUR')`,
		wsB, "quota-b-"+wsB.String()[:8]); err != nil {
		t.Fatal(err)
	}
	ctxB := principal.WithWorkspaceID(context.Background(), wsB)
	ctxB = principal.WithCorrelationID(ctxB, ids.NewV7())
	ctxB = principal.WithActor(ctxB, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(),
		UserID: ids.NewV7(), Permissions: quotaAdminPerms,
	})

	if _, err := store.GetQuota(ctxB, ids.UUID(created.Id), storekit.IncludeArchived); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("tenant B must not resolve tenant A's quota, got %v", err)
	}
	listed, _, err := store.ListQuotas(ctxB, quotas.ListQuotasInput{IncludeArchived: true})
	if err != nil || len(listed) != 0 {
		t.Fatalf("tenant B's list = %d rows (%v), want empty", len(listed), err)
	}
	// Tenant A still resolves its own row.
	if _, err := store.GetQuota(ctxA, ids.UUID(created.Id), storekit.LiveOnly); err != nil {
		t.Fatalf("tenant A must keep seeing its quota, got %v", err)
	}
}

// isOwnerXorTeam asserts the typed XOR refusal the transport will map to
// the contract's 422 owner_xor_team_required shape.
func isOwnerXorTeam(err error) bool {
	var xor *quotas.OwnerXorTeamError
	return errors.As(err, &xor)
}
