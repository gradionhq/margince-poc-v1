// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The offer_template CRUD store suite (offers-depth arc 4a, T3): the two
// pre-checked 409s (a live name collision, a same-locale is_default
// collision — poc-1 REJECTS a second default, it never auto-demotes the
// incumbent), keyset listing with a locale filter and include_archived,
// optimistic If-Match updates on the full-replace PUT, idempotent
// archive that never writes a second audit row, the Product/Quota
// config RBAC posture (rep create+read+update, read_only read-only, no
// row-scope probe), and RLS tenant isolation. Rides the same Env/e.As
// harness as quotas_integration_test.go; the HTTP-level coverage (the
// two named 409 shapes and the six-operation wire surface) lives in the
// sibling offertemplate_http_integration_test.go, mirroring how
// quotas_integration_test.go/quotas_http_integration_test.go split.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// offerTemplateAdminPerms mirrors the 0072 admin/ops/manager grant: full
// offer_template authority.
var offerTemplateAdminPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects:  map[string]principal.ObjectGrant{"offer_template": {Create: true, Read: true, Update: true, Delete: true}},
	RowScope: principal.RowScopeAll,
}

// offerTemplateRepPerms mirrors the 0072 rep grant: create/read/update,
// no delete.
var offerTemplateRepPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects:  map[string]principal.ObjectGrant{"offer_template": {Create: true, Read: true, Update: true}},
	RowScope: principal.RowScopeTeam,
}

// offerTemplateReadOnlyPerms mirrors the 0072 read_only grant: read only.
var offerTemplateReadOnlyPerms = principal.Permissions{
	RoleKeys: []string{"read_only"},
	Objects:  map[string]principal.ObjectGrant{"offer_template": {Read: true}},
	RowScope: principal.RowScopeAll,
}

func basicTemplateInput(name string) deals.CreateOfferTemplateInput {
	return deals.CreateOfferTemplateInput{Name: name, Layout: map[string]any{"logo_url": "https://example.test/logo.png"}}
}

func TestOfferTemplateCreate_DefaultsAndRoundTrip(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, nil, offerTemplateAdminPerms)

	in := basicTemplateInput("Standard")
	created, err := e.Deals.CreateOfferTemplate(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Locale != "de-DE" {
		t.Fatalf("empty locale must default to de-DE, got %q", created.Locale)
	}
	if created.IsDefault {
		t.Fatalf("is_default must default to false, got true")
	}
	if created.Name != "Standard" || created.Layout["logo_url"] != "https://example.test/logo.png" {
		t.Fatalf("create must echo name/layout, got %+v", created)
	}
	if created.Version == nil || *created.Version != 1 || created.ArchivedAt != nil {
		t.Fatalf("a fresh template carries version 1 and no archived_at, got %+v", created)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'offer_template' AND entity_id = $1 AND action = 'create'`,
		ids.UUID(created.Id)); n != 1 {
		t.Fatalf("create audit rows = %d, want exactly 1", n)
	}
	// events.md §5 defines no offer_template.* type — the write is
	// audit-only (writeshape_test.go's ratified exception), so no outbox
	// row rides alongside the audit row.
	if n := e.WsCount(t, `SELECT count(*) FROM event_outbox WHERE envelope->'entity'->>'type' = 'offer_template'`); n != 0 {
		t.Fatalf("offer_template writes are audit-only, found %d outbox rows", n)
	}

	got, err := e.Deals.GetOfferTemplate(ctx, ids.From[ids.OfferTemplateKind](ids.UUID(created.Id)), storekit.LiveOnly)
	if err != nil || ids.UUID(got.Id) != ids.UUID(created.Id) {
		t.Fatalf("get = %+v, %v", got, err)
	}

	// en-US, explicit non-default: locale is honored verbatim when given.
	en := basicTemplateInput("English Standard")
	en.Locale = "en-US"
	enCreated, err := e.Deals.CreateOfferTemplate(ctx, en)
	if err != nil || enCreated.Locale != "en-US" {
		t.Fatalf("explicit locale must be honored, got %+v, %v", enCreated, err)
	}
}

func TestOfferTemplateCreate_DuplicateNameConflict(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, nil, offerTemplateAdminPerms)

	first, err := e.Deals.CreateOfferTemplate(ctx, basicTemplateInput("Standard DE"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.Deals.CreateOfferTemplate(ctx, basicTemplateInput("Standard DE"))
	var dup *deals.DuplicateTemplateNameError
	if !errors.As(err, &dup) {
		t.Fatalf("a second live template with the same name must answer DuplicateTemplateNameError, got %v", err)
	}
	if dup.ExistingID.UUID != ids.UUID(first.Id) {
		t.Fatalf("DuplicateTemplateNameError.ExistingID = %s, want the first template's id %s", dup.ExistingID, first.Id)
	}
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("DuplicateTemplateNameError must answer true to errors.Is(apperrors.ErrConflict), got %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM offer_template WHERE name = 'Standard DE'`); n != 1 {
		t.Fatalf("a refused duplicate create must leave exactly the first row, found %d", n)
	}

	// offer_template_name_unique (0071) is NOT partial — it spans
	// archived rows too, so archiving the incumbent does NOT free the
	// name for reuse; the pre-check must still answer the named 409
	// (never let a false negative through to the raw 23505).
	if _, err := e.Deals.ArchiveOfferTemplate(ctx, ids.From[ids.OfferTemplateKind](ids.UUID(first.Id))); err != nil {
		t.Fatal(err)
	}
	_, err = e.Deals.CreateOfferTemplate(ctx, basicTemplateInput("Standard DE"))
	if !errors.As(err, &dup) {
		t.Fatalf("the name must stay blocked after archiving the incumbent, got %v", err)
	}
	if dup.ExistingID.UUID != ids.UUID(first.Id) {
		t.Fatalf("DuplicateTemplateNameError.ExistingID = %s, want the archived incumbent's id %s", dup.ExistingID, first.Id)
	}
}

func TestOfferTemplateCreate_DefaultConflictRejectedNotAutoDemoted(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, nil, offerTemplateAdminPerms)

	firstIn := basicTemplateInput("DE Default One")
	firstIn.IsDefault = true
	first, err := e.Deals.CreateOfferTemplate(ctx, firstIn)
	if err != nil {
		t.Fatal(err)
	}
	if !first.IsDefault {
		t.Fatalf("the first default must be persisted as default, got %+v", first)
	}

	secondIn := basicTemplateInput("DE Default Two")
	secondIn.IsDefault = true
	_, err = e.Deals.CreateOfferTemplate(ctx, secondIn)
	var conflict *deals.DefaultConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("a second is_default=true for the same locale must answer DefaultConflictError, got %v", err)
	}
	if conflict.Locale != "de-DE" || conflict.ExistingID.UUID != ids.UUID(first.Id) {
		t.Fatalf("DefaultConflictError = %+v, want locale=de-DE existing_id=%s", conflict, first.Id)
	}
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("DefaultConflictError must answer true to errors.Is(apperrors.ErrConflict), got %v", err)
	}

	// The rejected create wrote NO row at all — poc-1 parity: the store
	// never auto-demotes the incumbent default as a side effect.
	if n := e.WsCount(t, `SELECT count(*) FROM offer_template WHERE name = 'DE Default Two'`); n != 0 {
		t.Fatalf("a refused default-conflict create must write no row, found %d", n)
	}
	stillDefault, err := e.Deals.GetOfferTemplate(ctx, ids.From[ids.OfferTemplateKind](ids.UUID(first.Id)), storekit.LiveOnly)
	if err != nil || !stillDefault.IsDefault {
		t.Fatalf("the incumbent default must remain is_default=true after the rejected conflict, got %+v, %v", stillDefault, err)
	}

	// A different locale may carry its own, independent default.
	enIn := basicTemplateInput("EN Default")
	enIn.Locale = "en-US"
	enIn.IsDefault = true
	enDefault, err := e.Deals.CreateOfferTemplate(ctx, enIn)
	if err != nil || !enDefault.IsDefault || enDefault.Locale != "en-US" {
		t.Fatalf("a default for a different locale must succeed independently, got %+v, %v", enDefault, err)
	}
}

func TestOfferTemplateUpdate_HappyVersionSkewAndDefaultConflict(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, nil, offerTemplateAdminPerms)

	created, err := e.Deals.CreateOfferTemplate(ctx, basicTemplateInput("Editable"))
	if err != nil {
		t.Fatal(err)
	}
	id := ids.From[ids.OfferTemplateKind](ids.UUID(created.Id))

	v1 := int64(1)
	updated, err := e.Deals.UpdateOfferTemplate(ctx, id, deals.UpdateOfferTemplateInput{
		Name: "Editable v2", Locale: "de-DE", IsDefault: false,
		Layout: map[string]any{"footer_text": "v2"}, IfVersion: &v1,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "Editable v2" || updated.Layout["footer_text"] != "v2" || updated.Version == nil || *updated.Version != 2 {
		t.Fatalf("update must apply the full replacement and bump version to 2, got %+v", updated)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'offer_template' AND entity_id = $1 AND action = 'update'`,
		ids.UUID(created.Id)); n != 1 {
		t.Fatalf("update audit rows = %d, want exactly 1", n)
	}

	// The stale If-Match answers version skew, and applies nothing.
	if _, err := e.Deals.UpdateOfferTemplate(ctx, id, deals.UpdateOfferTemplateInput{
		Name: "Editable v3", Locale: "de-DE", Layout: map[string]any{}, IfVersion: &v1,
	}); !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("a stale If-Match must answer ErrVersionSkew, got %v", err)
	}

	// Setting is_default=true onto a locale another live template already
	// defaults is rejected on update exactly like create.
	otherDefaultIn := basicTemplateInput("Other Default")
	otherDefaultIn.IsDefault = true
	otherDefault, err := e.Deals.CreateOfferTemplate(ctx, otherDefaultIn)
	if err != nil {
		t.Fatal(err)
	}
	v2 := int64(2)
	_, err = e.Deals.UpdateOfferTemplate(ctx, id, deals.UpdateOfferTemplateInput{
		Name: "Editable v2", Locale: "de-DE", IsDefault: true, Layout: map[string]any{}, IfVersion: &v2,
	})
	var conflict *deals.DefaultConflictError
	if !errors.As(err, &conflict) || conflict.ExistingID.UUID != ids.UUID(otherDefault.Id) {
		t.Fatalf("patching is_default=true onto a conflicting locale must answer DefaultConflictError, got %v", err)
	}

	// Renaming to a name a DIFFERENT live template already holds is
	// rejected too — updating one's own unchanged name is not a conflict.
	_, err = e.Deals.UpdateOfferTemplate(ctx, id, deals.UpdateOfferTemplateInput{
		Name: "Other Default", Locale: "de-DE", Layout: map[string]any{}, IfVersion: &v2,
	})
	var dup *deals.DuplicateTemplateNameError
	if !errors.As(err, &dup) {
		t.Fatalf("renaming onto a live sibling's name must answer DuplicateTemplateNameError, got %v", err)
	}
	unchanged, err := e.Deals.UpdateOfferTemplate(ctx, id, deals.UpdateOfferTemplateInput{
		Name: "Editable v2", Locale: "de-DE", Layout: map[string]any{"footer_text": "v2"}, IfVersion: &v2,
	})
	if err != nil || *unchanged.Version != 3 {
		t.Fatalf("keeping the row's own name/layout must succeed as a real PUT (version bumps to 3), got %+v, %v", unchanged, err)
	}
}

func TestOfferTemplateArchive_IdempotentAndAuditedOnce(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, nil, offerTemplateAdminPerms)

	created, err := e.Deals.CreateOfferTemplate(ctx, basicTemplateInput("Archivable"))
	if err != nil {
		t.Fatal(err)
	}
	id := ids.From[ids.OfferTemplateKind](ids.UUID(created.Id))

	first, err := e.Deals.ArchiveOfferTemplate(ctx, id)
	if err != nil || first.ArchivedAt == nil {
		t.Fatalf("archive must return the full entity with archived_at set, got %+v, %v", first, err)
	}
	repeat, err := e.Deals.ArchiveOfferTemplate(ctx, id)
	if err != nil || repeat.ArchivedAt == nil || !repeat.ArchivedAt.Equal(*first.ArchivedAt) {
		t.Fatalf("a repeat archive is a no-op returning the same entity, got %+v, %v", repeat, err)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'offer_template' AND entity_id = $1 AND action = 'archive'`,
		ids.UUID(created.Id)); n != 1 {
		t.Fatalf("archive audit rows = %d, want exactly 1 — a repeat archive writes nothing", n)
	}
	if _, err := e.Deals.GetOfferTemplate(ctx, id, storekit.LiveOnly); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an archived template reads as absent under LiveOnly, got %v", err)
	}
	if _, err := e.Deals.GetOfferTemplate(ctx, id, storekit.IncludeArchived); err != nil {
		t.Fatalf("an archived template stays fetchable by id under IncludeArchived, got %v", err)
	}
	if _, err := e.Deals.ArchiveOfferTemplate(ctx, ids.From[ids.OfferTemplateKind](ids.NewV7())); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("archiving an unknown id must answer ErrNotFound, got %v", err)
	}
}

func TestOfferTemplateList_KeysetLocaleAndArchived(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, nil, offerTemplateAdminPerms)

	for _, name := range []string{"DE One", "DE Two", "DE Three", "DE Four"} {
		if _, err := e.Deals.CreateOfferTemplate(ctx, basicTemplateInput(name)); err != nil {
			t.Fatal(err)
		}
	}
	enIn := basicTemplateInput("EN One")
	enIn.Locale = "en-US"
	if _, err := e.Deals.CreateOfferTemplate(ctx, enIn); err != nil {
		t.Fatal(err)
	}
	tombstone, err := e.Deals.CreateOfferTemplate(ctx, basicTemplateInput("DE Tombstone"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Deals.ArchiveOfferTemplate(ctx, ids.From[ids.OfferTemplateKind](ids.UUID(tombstone.Id))); err != nil {
		t.Fatal(err)
	}

	// Keyset walk at limit 2: 4 live de-DE rows arrive in 2+2, no overlap.
	limit := 2
	deLocale := "de-DE"
	first, page, err := e.Deals.ListOfferTemplates(ctx, deals.ListOfferTemplatesInput{Limit: &limit, Locale: &deLocale})
	if err != nil || len(first) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("page 1 = %d rows, page %+v, %v — want 2 rows and a next cursor", len(first), page, err)
	}
	second, page2, err := e.Deals.ListOfferTemplates(ctx, deals.ListOfferTemplatesInput{
		Limit: &limit, Locale: &deLocale, Cursor: &page.NextCursor,
	})
	if err != nil || len(second) != 2 || page2.HasMore {
		t.Fatalf("page 2 = %d rows, page %+v, %v — want the final 2 rows", len(second), page2, err)
	}
	seen := map[ids.UUID]bool{}
	for _, tpl := range append(first, second...) {
		if seen[ids.UUID(tpl.Id)] {
			t.Fatalf("keyset pages overlap on %s", tpl.Id)
		}
		seen[ids.UUID(tpl.Id)] = true
		if tpl.Locale != "de-DE" {
			t.Fatalf("locale filter leaked a %s row", tpl.Locale)
		}
	}
	if seen[ids.UUID(tombstone.Id)] {
		t.Fatal("the archived template must not appear in a default (live) list")
	}

	enOnly, _, err := e.Deals.ListOfferTemplates(ctx, deals.ListOfferTemplatesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(enOnly) != 5 {
		t.Fatalf("unfiltered live list = %d rows, want the 4 de-DE + 1 en-US live rows (5)", len(enOnly))
	}

	all, _, err := e.Deals.ListOfferTemplates(ctx, deals.ListOfferTemplatesInput{IncludeArchived: true})
	if err != nil || len(all) != 6 {
		t.Fatalf("include_archived list = %d rows (%v), want all 6", len(all), err)
	}
}

func TestOfferTemplateRBAC_RepCreatesReadOnlyCannot(t *testing.T) {
	e := Setup(t)
	admin := e.As(e.Rep1, nil, offerTemplateAdminPerms)
	rep := e.As(e.Rep2, []ids.UUID{e.Team1}, offerTemplateRepPerms)
	readOnly := e.As(e.Rep3, []ids.UUID{e.Team2}, offerTemplateReadOnlyPerms)

	seeded, err := e.Deals.CreateOfferTemplate(admin, basicTemplateInput("RBAC Seed"))
	if err != nil {
		t.Fatal(err)
	}
	id := ids.From[ids.OfferTemplateKind](ids.UUID(seeded.Id))

	// A rep may create, read, and update — offer_template is the offer's
	// own branding input, not a locked-down schema surface (0072).
	repCreated, err := e.Deals.CreateOfferTemplate(rep, basicTemplateInput("Rep Made"))
	if err != nil {
		t.Fatalf("rep create must be allowed, got %v", err)
	}
	if _, err := e.Deals.GetOfferTemplate(rep, id, storekit.LiveOnly); err != nil {
		t.Fatalf("rep read must be allowed, got %v", err)
	}
	repV1 := int64(1)
	if _, err := e.Deals.UpdateOfferTemplate(rep,
		ids.From[ids.OfferTemplateKind](ids.UUID(repCreated.Id)),
		deals.UpdateOfferTemplateInput{Name: "Rep Made Renamed", Locale: "de-DE", Layout: map[string]any{}, IfVersion: &repV1},
	); err != nil {
		t.Fatalf("rep update must be allowed, got %v", err)
	}

	// A read_only principal reads but never mutates — same object gate
	// every store method opens with.
	if _, err := e.Deals.GetOfferTemplate(readOnly, id, storekit.LiveOnly); err != nil {
		t.Fatalf("read_only read must be allowed, got %v", err)
	}
	if _, err := e.Deals.CreateOfferTemplate(readOnly, basicTemplateInput("Read Only Attempt")); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("read_only create must answer ErrPermissionDenied, got %v", err)
	}
	roV1 := int64(1)
	if _, err := e.Deals.UpdateOfferTemplate(readOnly, id,
		deals.UpdateOfferTemplateInput{Name: "Hijacked", Locale: "de-DE", Layout: map[string]any{}, IfVersion: &roV1},
	); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("read_only update must answer ErrPermissionDenied, got %v", err)
	}
	if _, err := e.Deals.ArchiveOfferTemplate(readOnly, id); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("read_only archive must answer ErrPermissionDenied, got %v", err)
	}
	// The rep has no delete grant (0072) either.
	if _, err := e.Deals.ArchiveOfferTemplate(rep, id); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("rep archive (no delete grant) must answer ErrPermissionDenied, got %v", err)
	}
	// The denials wrote nothing beyond the two legitimate creates (seed + rep).
	if n := e.WsCount(t, `SELECT count(*) FROM offer_template`); n != 2 {
		t.Fatalf("denied mutations must leave exactly the seed + rep rows, found %d", n)
	}
}

func TestOfferTemplateRLS_TenantIsolation(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	ctxA := e.As(e.Rep1, nil, offerTemplateAdminPerms)

	created, err := e.Deals.CreateOfferTemplate(ctxA, basicTemplateInput("Tenant A Template"))
	if err != nil {
		t.Fatal(err)
	}

	wsB := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Template Tenant B', $2, 'EUR')`,
		wsB, "template-b-"+wsB.String()[:8]); err != nil {
		t.Fatal(err)
	}
	ctxB := principal.WithWorkspaceID(context.Background(), wsB)
	ctxB = principal.WithCorrelationID(ctxB, ids.NewV7())
	ctxB = principal.WithActor(ctxB, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(),
		UserID: ids.NewV7(), Permissions: offerTemplateAdminPerms,
	})

	id := ids.From[ids.OfferTemplateKind](ids.UUID(created.Id))
	if _, err := e.Deals.GetOfferTemplate(ctxB, id, storekit.IncludeArchived); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("tenant B must not resolve tenant A's template, got %v", err)
	}
	listed, _, err := e.Deals.ListOfferTemplates(ctxB, deals.ListOfferTemplatesInput{IncludeArchived: true})
	if err != nil || len(listed) != 0 {
		t.Fatalf("tenant B's list = %d rows (%v), want empty", len(listed), err)
	}
	// Tenant B may still mint its OWN template with the identical name —
	// offer_template_name_unique is scoped to (workspace_id, name).
	if _, err := e.Deals.CreateOfferTemplate(ctxB, basicTemplateInput("Tenant A Template")); err != nil {
		t.Fatalf("tenant B minting the same name in its own workspace must succeed, got %v", err)
	}
	if _, err := e.Deals.GetOfferTemplate(ctxA, id, storekit.LiveOnly); err != nil {
		t.Fatalf("tenant A must keep seeing its own template, got %v", err)
	}
}
