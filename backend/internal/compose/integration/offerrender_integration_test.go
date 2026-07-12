// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The offer render seam's store-level coverage: PrepareRender gathers the
// PDF renderer's inputs without ever opening blob storage — the buyer
// legal block mirrors SendOffer's own snapshot rule (the frozen
// buyer_snapshot once sent, the LIVE organization while still draft, nil
// when there is no buyer org at all), the locale AND layout resolve
// together through offer.template_id → offer_template, and the issuer
// name prefers the frozen issuer_snapshot the same way. SetPdfAssetRef is
// the standard audited-update write shape, fenced on the row version
// PrepareRender saw (the TOCTOU guard against a draft edit landing
// between the two calls). The HTTP-level render round trip (real blob
// write, 501 when unwired) lives in the sibling
// offerrender_http_integration_test.go.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// offerRenderDeskPerms is the deal-desk grant this suite drives the offer
// render seam under; offer_template is needed only for the locale test's
// CreateOfferTemplate call.
var offerRenderDeskPerms = principal.Permissions{
	RoleKeys: []string{"deal_desk"},
	Objects: map[string]principal.ObjectGrant{
		"deal":           {Create: true, Read: true, Update: true},
		"offer":          {Create: true, Read: true, Update: true},
		"offer_template": {Create: true, Read: true},
	},
	RowScope: principal.RowScopeAll,
}

// offerRenderReadOnlyPerms mirrors the 0072 read_only grant applied to
// the offer object specifically: read, never update. RenderOffer's write
// (it persists pdf_asset_ref) must be gated on offer-update, not merely
// offer-read, before it does any render/blob work.
var offerRenderReadOnlyPerms = principal.Permissions{
	RoleKeys: []string{"read_only"},
	Objects: map[string]principal.ObjectGrant{
		"deal":  {Read: true},
		"offer": {Read: true},
	},
	RowScope: principal.RowScopeAll,
}

// spyBlobStore wraps a real blobstore.Store and records whether Put was
// ever invoked — the test's proof that a denied render performs ZERO
// blob work, not merely that the HTTP response was a 403.
type spyBlobStore struct {
	blobstore.Store
	putCalled bool
}

func (s *spyBlobStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	s.putCalled = true
	return s.Store.Put(ctx, key, r, size, contentType)
}

// renderOneLineOffer creates a one-line draft EUR offer on dealID (EUR
// matches the harness workspace's base_currency, so Send never needs a
// seeded FX rate).
func renderOneLineOffer(ctx context.Context, t *testing.T, e *Env, dealID ids.UUID, in deals.CreateOfferInput) crmcontracts.Offer {
	t.Helper()
	if in.Currency == "" {
		in.Currency = "EUR"
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	if in.LineItems == nil {
		desc, price := "Retainer", int64(50000)
		in.LineItems = []deals.OfferLineInputRow{{Description: &desc, Quantity: "1", UnitPriceMinor: &price}}
	}
	created, err := e.Deals.CreateOffer(ctx, ids.From[ids.DealKind](dealID), in)
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	return created
}

func TestOfferRenderPrepareRender_DraftNoBuyerOrg_DefaultsLocaleAndOmitsBuyerBlock(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Render fixture deal", pipeline, open, &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerRenderDeskPerms)

	created := renderOneLineOffer(ctx, t, e, dealID, deals.CreateOfferInput{})
	offerID := ids.From[ids.OfferKind](ids.UUID(created.Id))

	ing, err := e.Deals.PrepareRender(ctx, offerID)
	if err != nil {
		t.Fatalf("prepare render: %v", err)
	}
	if ing.BuyerBlock != nil {
		t.Fatalf("a buyer-org-less draft must render with a nil buyer block, got %+v", ing.BuyerBlock)
	}
	if ing.Locale != "de-DE" {
		t.Fatalf("an offer with no template must default to locale de-DE, got %q", ing.Locale)
	}
	if ing.IssuerName == "" {
		t.Fatal("PrepareRender must resolve a non-empty live issuer name")
	}
	if len(ing.LineItems) != 1 {
		t.Fatalf("PrepareRender must carry the offer's line items, got %d", len(ing.LineItems))
	}
	if ing.Offer.NetMinor == nil || *ing.Offer.NetMinor != *created.NetMinor {
		t.Fatalf("PrepareRender's offer must carry the same server-computed totals GetOffer shows, got %+v want %d", ing.Offer.NetMinor, *created.NetMinor)
	}
}

func TestOfferRenderPrepareRender_DraftWithBuyerOrg_UsesLiveOrgNotAFrozenSnapshot(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Render live-org deal", pipeline, open, &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerRenderDeskPerms)

	org, err := e.People.CreateOrganization(e.Admin(), people.CreateOrganizationInput{DisplayName: "Acme GmbH"})
	if err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))

	created := renderOneLineOffer(ctx, t, e, dealID, deals.CreateOfferInput{BuyerOrgID: &orgID})
	offerID := ids.From[ids.OfferKind](ids.UUID(created.Id))

	ing, err := e.Deals.PrepareRender(ctx, offerID)
	if err != nil {
		t.Fatalf("prepare render: %v", err)
	}
	if ing.BuyerBlock == nil || ing.BuyerBlock["display_name"] != "Acme GmbH" {
		t.Fatalf("a draft with a buyer org must render the live org's display_name, got %+v", ing.BuyerBlock)
	}

	// Renaming the org must show up on the NEXT render — while still
	// draft, the block is the live org, never a frozen copy.
	renamed := "Acme Renamed GmbH"
	if _, err := e.People.UpdateOrganization(e.Admin(), orgID, people.UpdateOrganizationInput{DisplayName: &renamed}); err != nil {
		t.Fatalf("rename organization: %v", err)
	}
	after, err := e.Deals.PrepareRender(ctx, offerID)
	if err != nil {
		t.Fatalf("prepare render after rename: %v", err)
	}
	if after.BuyerBlock["display_name"] != renamed {
		t.Fatalf("a still-draft offer must reflect the org's LIVE name, got %+v", after.BuyerBlock)
	}
}

func TestOfferRenderPrepareRender_Sent_UsesFrozenBuyerAndIssuerSnapshot(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Render sent deal", pipeline, open, &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerRenderDeskPerms)

	org, err := e.People.CreateOrganization(e.Admin(), people.CreateOrganizationInput{DisplayName: "Frozen Co"})
	if err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))

	created := renderOneLineOffer(ctx, t, e, dealID, deals.CreateOfferInput{BuyerOrgID: &orgID})
	offerID := ids.From[ids.OfferKind](ids.UUID(created.Id))

	if _, err := e.Deals.SendOffer(ctx, offerID, nil); err != nil {
		t.Fatalf("send offer: %v", err)
	}

	// Renaming the org AFTER send must not move the sent offer's block:
	// the frozen buyer_snapshot is the legal record from here on.
	renamed := "Renamed After Send"
	if _, err := e.People.UpdateOrganization(e.Admin(), orgID, people.UpdateOrganizationInput{DisplayName: &renamed}); err != nil {
		t.Fatalf("rename organization: %v", err)
	}

	// Renaming the WORKSPACE (the issuer side) after send must equally
	// not move — resolveRenderIssuerName's frozen issuer_snapshot is the
	// legal record for a sent offer, the same rule as the buyer side
	// above. This is the assertion the test's own name promises
	// ("...AndIssuerSnapshot") and that was previously never checked.
	renamedWorkspace := "Renamed Workspace After Send"
	e.WsExec(t, `UPDATE workspace SET name = $1 WHERE id = $2`, renamedWorkspace, e.WS)

	ing, err := e.Deals.PrepareRender(ctx, offerID)
	if err != nil {
		t.Fatalf("prepare render: %v", err)
	}
	if ing.BuyerBlock == nil || ing.BuyerBlock["display_name"] != "Frozen Co" {
		t.Fatalf("a sent offer must render the FROZEN buyer name, got %+v", ing.BuyerBlock)
	}
	if ing.IssuerName != "Authz" {
		t.Fatalf("a sent offer must render the FROZEN issuer_snapshot workspace name %q, not the live (renamed) workspace name, got %q", "Authz", ing.IssuerName)
	}
}

func TestOfferRenderPrepareRender_TemplateLocaleAndLayoutResolveFromOfferTemplate(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Render locale deal", pipeline, open, &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerRenderDeskPerms)

	tmpl, err := e.Deals.CreateOfferTemplate(ctx, deals.CreateOfferTemplateInput{
		Name: "English Standard", Locale: "en-US",
		Layout: map[string]any{"header_text": "English Standard header", "footer_text": "English Standard footer"},
	})
	if err != nil {
		t.Fatalf("create offer template: %v", err)
	}
	templateID := ids.From[ids.OfferTemplateKind](ids.UUID(tmpl.Id))

	created := renderOneLineOffer(ctx, t, e, dealID, deals.CreateOfferInput{TemplateID: &templateID})
	offerID := ids.From[ids.OfferKind](ids.UUID(created.Id))
	if created.TemplateId == nil || ids.UUID(*created.TemplateId) != templateID.UUID {
		t.Fatalf("the created offer must echo template_id, got %+v", created.TemplateId)
	}

	ing, err := e.Deals.PrepareRender(ctx, offerID)
	if err != nil {
		t.Fatalf("prepare render: %v", err)
	}
	if ing.Locale != "en-US" {
		t.Fatalf("PrepareRender must resolve locale via the offer's template, got %q want en-US", ing.Locale)
	}
	if ing.Layout["header_text"] != "English Standard header" || ing.Layout["footer_text"] != "English Standard footer" {
		t.Fatalf("PrepareRender must resolve the offer's template LAYOUT alongside its locale, got %+v", ing.Layout)
	}

	// An unknown template_id is refused up front — never a raw FK 500.
	bogus := ids.New[ids.OfferTemplateKind]()
	if _, err := e.Deals.CreateOffer(ctx, ids.From[ids.DealKind](dealID), deals.CreateOfferInput{
		Currency: "EUR", Source: "manual", TemplateID: &bogus,
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("create offer with an unknown template_id = %v, want ErrNotFound", err)
	}
}

func TestOfferRenderSetPdfAssetRef_PersistsAndAuditsExactlyOnce(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Render asset-ref deal", pipeline, open, &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerRenderDeskPerms)

	created := renderOneLineOffer(ctx, t, e, dealID, deals.CreateOfferInput{})
	offerID := ids.From[ids.OfferKind](ids.UUID(created.Id))

	before := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_type = 'offer' AND action = 'update'`)

	ref := "offers/" + e.WS.String() + "/" + ids.UUID(created.Id).String() + "/1.pdf"
	updated, err := e.Deals.SetPdfAssetRef(ctx, offerID, ref, *created.Version)
	if err != nil {
		t.Fatalf("set pdf asset ref: %v", err)
	}
	if updated.PdfAssetRef == nil || *updated.PdfAssetRef != ref {
		t.Fatalf("SetPdfAssetRef must persist the given ref, got %+v want %q", updated.PdfAssetRef, ref)
	}
	if updated.Version == nil || *updated.Version != *created.Version+1 {
		t.Fatalf("SetPdfAssetRef must bump version like every other offer update, got %+v", updated.Version)
	}
	after := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_type = 'offer' AND action = 'update'`)
	if after != before+1 {
		t.Fatalf("SetPdfAssetRef must write exactly one audit row, before=%d after=%d", before, after)
	}

	got, err := e.Deals.GetOffer(ctx, offerID, storekit.LiveOnly)
	if err != nil || got.PdfAssetRef == nil || *got.PdfAssetRef != ref {
		t.Fatalf("the persisted pdf_asset_ref must survive a fresh read, got %+v, %v", got.PdfAssetRef, err)
	}
}

// TestOfferRenderSetPdfAssetRef_StalePreparedVersionRejectsWithVersionSkew
// is the TOCTOU fence proof: PrepareRender snapshots the offer's version,
// but a concurrent draft edit (here, AddOfferLineItem — the same write a
// real regenerate/line-edit race would perform) bumps that version before
// the render's SetPdfAssetRef call lands. Fencing the write on the
// prepared version means it is REJECTED rather than silently pointing
// pdf_asset_ref at a PDF that no longer matches the offer's current
// lines — and the rejected write must leave pdf_asset_ref untouched.
func TestOfferRenderSetPdfAssetRef_StalePreparedVersionRejectsWithVersionSkew(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Render fence deal", pipeline, open, &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerRenderDeskPerms)

	created := renderOneLineOffer(ctx, t, e, dealID, deals.CreateOfferInput{})
	offerID := ids.From[ids.OfferKind](ids.UUID(created.Id))

	ing, err := e.Deals.PrepareRender(ctx, offerID)
	if err != nil {
		t.Fatalf("prepare render: %v", err)
	}
	preparedVersion := *ing.Offer.Version

	// The race: a second line lands on the SAME offer after PrepareRender
	// already read it, bumping the row's version — exactly what a
	// concurrent regenerate, another render, or a sibling line edit would
	// do between this handler's PrepareRender and SetPdfAssetRef calls.
	desc, price := "Concurrent Line", int64(1000)
	if _, err := e.Deals.AddOfferLineItem(ctx, offerID, deals.OfferLineInputRow{
		Description: &desc, Quantity: "1", UnitPriceMinor: &price,
	}); err != nil {
		t.Fatalf("inject concurrent line edit: %v", err)
	}

	ref := "offers/" + e.WS.String() + "/" + ids.UUID(created.Id).String() + "/stale.pdf"
	if _, err := e.Deals.SetPdfAssetRef(ctx, offerID, ref, preparedVersion); !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("SetPdfAssetRef against a stale prepared version = %v, want ErrVersionSkew", err)
	}

	got, err := e.Deals.GetOffer(ctx, offerID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read offer after rejected render: %v", err)
	}
	if got.PdfAssetRef != nil {
		t.Fatalf("a rejected SetPdfAssetRef must not persist any ref, got %+v", got.PdfAssetRef)
	}
}

// seedOfferRenderWorkspaceB provisions a SECOND, genuinely separate
// workspace (its own row, own admin user, own pipeline/deal) and renders
// one draft offer inside it — the real cross-tenant fixture
// TestOfferRenderPrepareRender_RBACDeniedAndCrossTenantNotFound needs to
// prove a row that actually EXISTS in another tenant is still invisible,
// not merely that a random unknown id 404s (a stand-in for "no such row
// anywhere", not for "this exact row belongs to someone else").
func seedOfferRenderWorkspaceB(t *testing.T, e *Env, owner *pgx.Conn) ids.OfferID {
	t.Helper()
	ws, user := ids.NewV7(), ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Tenant B Render', $2, 'EUR')`,
		ws, "render-b-"+ws.String()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'B Admin')`,
		user, ws, "b@render-b.test"); err != nil {
		t.Fatal(err)
	}
	ctxB := principal.WithWorkspaceID(context.Background(), ws)
	ctxB = principal.WithCorrelationID(ctxB, ids.NewV7())
	ctxB = principal.WithActor(ctxB, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(),
		UserID: user,
		// offerRenderDeskPerms plus "pipeline" — this actor also seeds
		// tenant B's default pipeline, which the main harness's fixtures
		// otherwise get for free from e.Admin().
		Permissions: principal.Permissions{
			RoleKeys: []string{"deal_desk"},
			Objects: map[string]principal.ObjectGrant{
				"pipeline":       {Create: true, Read: true},
				"deal":           {Create: true, Read: true, Update: true},
				"offer":          {Create: true, Read: true, Update: true},
				"offer_template": {Create: true, Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})

	if err := e.Deals.SeedDefaults(ctxB); err != nil {
		t.Fatal(err)
	}
	p, err := e.Deals.DefaultPipeline(ctxB)
	if err != nil {
		t.Fatal(err)
	}
	var openStage ids.StageID
	for _, st := range *p.Stages {
		if st.Semantic == "open" {
			openStage = ids.From[ids.StageKind](ids.UUID(st.Id))
			break
		}
	}
	deal, err := e.Deals.CreateDeal(ctxB, deals.CreateDealInput{
		Name: "Tenant B deal", PipelineID: ids.From[ids.PipelineKind](ids.UUID(p.Id)), StageID: openStage,
	})
	if err != nil {
		t.Fatal(err)
	}
	created := renderOneLineOffer(ctxB, t, e, ids.UUID(deal.Id), deals.CreateOfferInput{})
	return ids.From[ids.OfferKind](ids.UUID(created.Id))
}

func TestOfferRenderPrepareRender_RBACDeniedAndCrossTenantNotFound(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Render rbac deal", pipeline, open, &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerRenderDeskPerms)

	created := renderOneLineOffer(ctx, t, e, dealID, deals.CreateOfferInput{})
	offerID := ids.From[ids.OfferKind](ids.UUID(created.Id))

	noOfferGrant := principal.Permissions{
		RoleKeys: []string{"no_offer"},
		Objects:  map[string]principal.ObjectGrant{"deal": {Read: true}},
		RowScope: principal.RowScopeAll,
	}
	denied := e.As(e.Rep2, []ids.UUID{e.Team1}, noOfferGrant)
	if _, err := e.Deals.PrepareRender(denied, offerID); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("PrepareRender without the offer read grant = %v, want ErrPermissionDenied", err)
	}

	if _, err := e.Deals.PrepareRender(ctx, ids.From[ids.OfferKind](ids.NewV7())); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("PrepareRender of an unknown offer id = %v, want ErrNotFound", err)
	}

	// A REAL offer that exists — just in another workspace — must answer
	// the identical existence-hiding 404, never a 403 or a leak.
	owner := OwnerConn(t)
	otherOfferID := seedOfferRenderWorkspaceB(t, e, owner)
	if _, err := e.Deals.PrepareRender(ctx, otherOfferID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("PrepareRender of a real cross-workspace offer = %v, want ErrNotFound (existence-hiding)", err)
	}
}

// TestOfferRenderPrepareRender_ReadOnlyOfferGrantDenied is the store-level
// half of the read-only-role proof: RenderOffer's write (it persists
// pdf_asset_ref) is gated on offer-UPDATE from the very first line of
// PrepareRender, not only in the later SetPdfAssetRef call — a principal
// holding offer-read but not offer-update must never get past PrepareRender
// at all, so the render handler never reaches RenderOfferPDF or the blob
// store for such a caller.
func TestOfferRenderPrepareRender_ReadOnlyOfferGrantDenied(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Render read-only deal", pipeline, open, &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerRenderDeskPerms)

	created := renderOneLineOffer(ctx, t, e, dealID, deals.CreateOfferInput{})
	offerID := ids.From[ids.OfferKind](ids.UUID(created.Id))

	readOnly := e.As(e.Rep2, []ids.UUID{e.Team1}, offerRenderReadOnlyPerms)
	if _, err := e.Deals.PrepareRender(readOnly, offerID); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("PrepareRender with offer read but not update = %v, want ErrPermissionDenied", err)
	}
}

// TestOfferRenderHandler_ReadOnlyOfferGrantDeniedBeforeAnyBlobWrite drives
// the actual HTTP handler (deals.Handlers.RenderOffer, the code the finding
// named) for a read-only-offer-grant principal: the 403 must land BEFORE
// the PDF is rendered and BEFORE anything reaches the blob store — a
// read-only role must never cause an orphan blob write just to be told no.
func TestOfferRenderHandler_ReadOnlyOfferGrantDeniedBeforeAnyBlobWrite(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Render read-only handler deal", pipeline, open, &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerRenderDeskPerms)

	created := renderOneLineOffer(ctx, t, e, dealID, deals.CreateOfferInput{})
	offerID := ids.From[ids.OfferKind](ids.UUID(created.Id))

	blob := &spyBlobStore{Store: blobstore.NewMemory()}
	h := deals.NewHandlers(e.Pool).WithBlobstore(blob)

	readOnly := e.As(e.Rep2, []ids.UUID{e.Team1}, offerRenderReadOnlyPerms)
	req := httptest.NewRequest(http.MethodPost, "/v1/offers/"+created.Id.String()+"/render", nil).WithContext(readOnly)
	rec := httptest.NewRecorder()
	h.RenderOffer(rec, req, created.Id, crmcontracts.RenderOfferParams{})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("render as a read-only offer grant = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if blob.putCalled {
		t.Fatal("a denied render must never reach the blob write — that is the whole point of gating update up front")
	}
	key := fmt.Sprintf("offers/%s/%s/%d.pdf", e.WS, offerID.UUID, *created.Revision)
	if _, _, err := blob.Get(context.Background(), key); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("the render key must carry no object after a denied render, got err=%v", err)
	}
}
