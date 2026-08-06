// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The anchor company's face (A55, PO-AC-25). It is the one company created BY a
// website read rather than enriched after one: the read runs while the
// organization still does not exist, so the mark it resolves waits on the
// dossier and the confirmation adopts it as it creates the row. Nothing else
// ever offers this company a logo — no sweep revisits it — so what these cases
// pin is the difference between the company every user meets first having a
// face and never having one.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/imagenorm"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// touchIconURL is the mark acme.example declares on its landing page.
const touchIconURL = seedURL + "/touch.png"

// onboardingLogoWorker builds the logo lane over a fake site and an in-memory
// object store — the worker as the onboarding read runs it, minus the crawl.
func onboardingLogoWorker(e *integration.Env, site *assetSite, blob blobstore.Store) *siteDeepReadWorker {
	return &siteDeepReadWorker{
		people: e.People, fetch: site, blob: blob,
		log: slog.New(slog.DiscardHandler),
	}
}

// declaringCrawl is what the seed page declared, as the crawl carries it into
// the logo lane.
func declaringCrawl() siteCrawl {
	return siteCrawl{
		SeedURL: seedURL,
		SeedAssets: declaredAssets{
			icons: []webread.IconRef{{URL: touchIconURL, Rel: webread.RelAppleTouchIcon}},
		},
	}
}

// readTheOnboardingSite starts the unbound dossier, claims it the way the
// worker does, and runs the logo lane over the seed page's declarations.
func readTheOnboardingSite(t *testing.T, e *integration.Env, w *siteDeepReadWorker) SiteDeepReadArgs {
	t.Helper()
	read, joined, err := e.People.StartOnboardingSiteRead(
		e.As(e.Rep1, nil, integration.AdminPerms), seedURL, "human:"+e.Rep1.String(), nil)
	if err != nil {
		t.Fatalf("start the onboarding read: %v", err)
	}
	if joined {
		t.Fatal("a fresh onboarding read joined an existing dossier")
	}
	args := SiteDeepReadArgs{Workspace: e.WS, SiteReadID: read.ID, RequestedBy: read.RequestedBy}
	workerCtx := deepReadWorkerCtx(context.Background(), args)
	claim, err := e.People.BeginSiteRead(workerCtx, read.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("claim the onboarding read: %v", err)
	}
	w.resolveLogo(workerCtx, args, claim, declaringCrawl())
	return args
}

// readyDossier closes the claimed read and hands back the draft a human is
// shown before confirming it.
func readyDossier(t *testing.T, e *integration.Env, args SiteDeepReadArgs) people.SiteRead {
	t.Helper()
	hash, err := siteReadProposalHash(nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("hashing the empty draft: %v", err)
	}
	if err := e.People.FinishSiteRead(deepReadWorkerCtx(context.Background(), args), args.SiteReadID,
		people.FinishSiteReadInput{
			Status:       "done",
			Pages:        []people.SiteReadPage{{URL: seedURL, Kind: "home"}},
			ProposalHash: hash,
		}); err != nil {
		t.Fatalf("finish the onboarding read: %v", err)
	}
	ready, err := e.People.GetOnboardingSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), args.SiteReadID)
	if err != nil {
		t.Fatalf("read the finished draft: %v", err)
	}
	return ready
}

// confirmTheAnchor finishes the claimed dossier and confirms it — the step that
// creates the company the installation is.
func confirmTheAnchor(t *testing.T, e *integration.Env, args SiteDeepReadArgs) people.Company {
	t.Helper()
	ready := readyDossier(t, e, args)
	website := seedURL
	company, _, err := e.People.ConfirmCompanySiteRead(e.As(e.Rep1, nil, integration.AdminPerms),
		people.ConfirmCompanySiteReadInput{
			ReadID: ready.ID, DraftVersion: ready.DraftVersion, ProposalHash: ready.ProposalHash,
			DisplayName: "Acme", Website: &website,
		}, nil)
	if err != nil {
		t.Fatalf("confirm the onboarding read: %v", err)
	}
	return company
}

// confirmTheAnchorAsTheAPIDoes confirms through the transport that serves the
// confirmation. The store can only REPORT the mark its anchor declined; the
// collection is this side's half of that contract, so a case about bytes has to
// come through here rather than call the store on its own.
func confirmTheAnchorAsTheAPIDoes(t *testing.T, e *integration.Env, engine *deepReadEngine, args SiteDeepReadArgs) ids.OrganizationID {
	t.Helper()
	ready := readyDossier(t, e, args)
	offer, icp, website := "Employee onboarding software", "Growing RevOps teams", seedURL
	request := onboardingPOST(e.As(e.Rep1, nil, integration.AdminPerms), t,
		"/v1/company/site-reads/"+ready.ID.String()+"/confirm",
		crmcontracts.ConfirmCompanySiteReadRequest{
			DraftVersion: ready.DraftVersion, ProposalHash: ready.ProposalHash,
			Profile: crmcontracts.CompanyProfileInput{
				DisplayName: "Acme", OfferSummary: &offer, Icp: &icp, Website: &website,
			},
		})
	recorder := httptest.NewRecorder()
	engine.confirmCompanySiteRead(recorder, request, openapi_types.UUID(ready.ID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("confirm → %d %s, want 200", recorder.Code, recorder.Body.String())
	}
	var confirmed crmcontracts.CompanyProfile
	if err := json.Unmarshal(recorder.Body.Bytes(), &confirmed); err != nil {
		t.Fatalf("decoding the confirmed company: %v", err)
	}
	return ids.From[ids.OrganizationKind](ids.UUID(confirmed.OrganizationId))
}

// parkedLogo answers what the dossier is holding for the confirmation.
func parkedLogo(t *testing.T, e *integration.Env, readID ids.UUID) (key, origin *string) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT logo_object_key, logo_origin FROM site_read WHERE id = $1`, readID).Scan(&key, &origin)
	}); err != nil {
		t.Fatalf("reading the dossier's logo: %v", err)
	}
	return key, origin
}

func TestOnboardingReadResolvesTheLogoTheConfirmedAnchorWears(t *testing.T) {
	e := integration.Setup(t)
	site := &assetSite{assets: map[string][]byte{touchIconURL: logoFixture(t, 512, 512)}}
	blob := blobstore.NewMemory()
	args := readTheOnboardingSite(t, e, onboardingLogoWorker(e, site, blob))

	key, origin := parkedLogo(t, e, args.SiteReadID)
	if key == nil || *key == "" || origin == nil || *origin != touchIconURL {
		t.Fatalf("the unbound dossier parked no mark: key %v origin %v", key, origin)
	}
	stored, object, err := blob.Get(context.Background(), *key)
	if err != nil {
		t.Fatalf("the parked key names no object: %v", err)
	}
	if err := stored.Close(); err != nil {
		t.Fatalf("closing the stored object: %v", err)
	}
	if object.ContentType != imagenorm.ContentType {
		t.Fatalf("stored content type %q, want the normalized %q", object.ContentType, imagenorm.ContentType)
	}

	company := confirmTheAnchor(t, e, args)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	boundKey, err := e.People.OrganizationLogoKey(ctx, company.OrganizationID)
	if err != nil {
		t.Fatalf("the confirmed anchor has no logo: %v", err)
	}
	if boundKey != *key {
		t.Fatalf("the anchor names %q, want the object the read stored at %q", boundKey, *key)
	}
	org, err := e.People.GetOrganization(ctx, company.OrganizationID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read the anchor: %v", err)
	}
	wantURL := "/v1/organizations/" + company.OrganizationID.String() + "/logo"
	if org.LogoUrl == nil || *org.LogoUrl != wantURL {
		t.Fatalf("logo_url = %v, want %q — the face the SPA renders", org.LogoUrl, wantURL)
	}

	// The mark is the site read's, never the confirming human's: provenance is
	// written once, and a machine mark filed under a person would make the
	// human-precedence guard refuse every later resolve.
	var capturedBy string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT captured_by FROM field_provenance
			WHERE object_type = 'organization' AND object_id = $1 AND field_name = 'logo'
			ORDER BY captured_at DESC, id DESC LIMIT 1`, company.OrganizationID).Scan(&capturedBy)
	}); err != nil {
		t.Fatalf("reading the logo's provenance: %v", err)
	}
	if capturedBy != "agent:site-read" {
		t.Fatalf("logo captured_by = %q, want the site read", capturedBy)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log
		WHERE entity_type = 'organization' AND entity_id = $1 AND after->'fields' ? 'logo'`,
		company.OrganizationID); n != 1 {
		t.Fatalf("the logo write left %d audit rows, want exactly 1", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM event_outbox
		WHERE envelope->>'type' = 'organization.updated'
		  AND envelope->'entity'->>'id' = $1
		  AND envelope->'payload'->'changed_fields'->>'source_url' = $2`,
		company.OrganizationID.String(), touchIconURL); n != 1 {
		t.Fatalf("the logo write published %d organization.updated events for the mark, want 1", n)
	}
}

func TestAdoptingTheParkedMarkLeavesTheCompanyItsOnlyReference(t *testing.T) {
	// The confirmation HANDS the object over; it does not share it. Two rows
	// naming one key would let the next resolve of this organization supersede
	// that key and collect the bytes, while the confirmed dossier still pointed
	// at an object nothing could serve.
	e := integration.Setup(t)
	site := &assetSite{assets: map[string][]byte{touchIconURL: logoFixture(t, 512, 512)}}
	args := readTheOnboardingSite(t, e, onboardingLogoWorker(e, site, blobstore.NewMemory()))
	parked, _ := parkedLogo(t, e, args.SiteReadID)
	if parked == nil {
		t.Fatal("the read parked no mark; this case has nothing to hand over")
	}
	company := confirmTheAnchor(t, e, args)

	left, leftOrigin := parkedLogo(t, e, args.SiteReadID)
	if left != nil {
		t.Fatalf("the confirmed dossier still names %q, want the company to hold the only reference", *left)
	}
	if leftOrigin != nil {
		t.Fatalf("the confirmed dossier still names the asset URL %q it handed over", *leftOrigin)
	}
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	boundKey, err := e.People.OrganizationLogoKey(ctx, company.OrganizationID)
	if err != nil {
		t.Fatalf("the confirmed anchor has no logo: %v", err)
	}
	if boundKey != *parked {
		t.Fatalf("the anchor names %q, want the adopted object at %q", boundKey, *parked)
	}

	// A later resolve of the same company supersedes the adopted object and
	// hands its key back for collection. Nothing may still be pointing at those
	// bytes by the time the lane deletes them.
	next := blobstore.WorkspaceKey(ids.From[ids.WorkspaceKind](e.WS), "organization_logo",
		company.OrganizationID.String()+"/"+ids.NewV7().String())
	written, superseded, err := e.People.SetOrganizationLogo(ctx, company.OrganizationID, next, seedURL+"/newer.png")
	if err != nil {
		t.Fatalf("re-resolve the anchor's logo: %v", err)
	}
	if !written {
		t.Fatal("the re-resolve reported no change to a mark the site read captured")
	}
	if superseded == nil || *superseded != *parked {
		t.Fatalf("superseded key = %v, want the adopted object at %q", superseded, *parked)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM site_read WHERE logo_object_key = $1`, *parked); n != 0 {
		t.Fatalf("%d dossier row(s) still name the collected object at %q", n, *parked)
	}
}

// publishedSeq answers the insert order the outbox will ship one organization's
// two confirmation events in — the anchor's own creation, and the update the
// adopted mark publishes.
func publishedSeq(t *testing.T, e *integration.Env, orgID ids.OrganizationID) (created, mark int64) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `SELECT seq FROM event_outbox
			WHERE envelope->>'type' = 'organization.created'
			  AND envelope->'entity'->>'id' = $1`, orgID.String()).Scan(&created); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `SELECT seq FROM event_outbox
			WHERE envelope->>'type' = 'organization.updated'
			  AND envelope->'entity'->>'id' = $1
			  AND envelope->'payload'->'changed_fields'->>'source_url' = $2`,
			orgID.String(), touchIconURL).Scan(&mark)
	}); err != nil {
		t.Fatalf("reading the confirmation's published events: %v", err)
	}
	return created, mark
}

func TestConfirmingAnOnboardingReadPublishesTheAnchorBeforeItsMark(t *testing.T) {
	// One confirmation publishes two events about one organization: the
	// creation that mints the anchor, and the update the adopted logo writes.
	// The relay ships an entity's rows in insert order, so the creation has to
	// be the earlier one — an update arriving first describes a record the
	// consumer has never been told exists, and the creation behind it carries
	// no logo to repair the gap.
	e := integration.Setup(t)
	site := &assetSite{assets: map[string][]byte{touchIconURL: logoFixture(t, 512, 512)}}
	args := readTheOnboardingSite(t, e, onboardingLogoWorker(e, site, blobstore.NewMemory()))
	company := confirmTheAnchor(t, e, args)

	created, mark := publishedSeq(t, e, company.OrganizationID)
	if created >= mark {
		t.Fatalf("organization.created published at seq %d, the logo's organization.updated at %d — the anchor must come first", created, mark)
	}
}

func TestConfirmingAnOnboardingReadSurvivesALogoThatNeverResolved(t *testing.T) {
	// An air-gapped install, a site that declares nothing, an asset that will
	// not answer: the company still has to come into being. A logo is polish on
	// a read whose product is the company itself.
	e := integration.Setup(t)
	site := &assetSite{failing: map[string]bool{touchIconURL: true}}
	args := readTheOnboardingSite(t, e, onboardingLogoWorker(e, site, blobstore.NewMemory()))

	if key, origin := parkedLogo(t, e, args.SiteReadID); key != nil || origin != nil {
		t.Fatalf("a resolve that fetched nothing parked key %v origin %v", key, origin)
	}
	company := confirmTheAnchor(t, e, args)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	if _, err := e.People.OrganizationLogoKey(ctx, company.OrganizationID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an anchor with no resolved logo answers %v, want not-found so the monogram renders", err)
	}
	org, err := e.People.GetOrganization(ctx, company.OrganizationID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read the anchor: %v", err)
	}
	if org.LogoUrl != nil {
		t.Fatalf("logo_url = %q, want none", *org.LogoUrl)
	}
}

func TestAReadThatFailsGivesBackTheLogoItParked(t *testing.T) {
	// The mark is stored while the page is in hand, before anything the model
	// lanes produced is judged — so a read that dies afterwards has already paid
	// for bytes no confirmation will ever adopt: only a done or partial read
	// binds a company. The dossier's reference is the last thing that can find
	// them, so the collection happens where the read goes terminal.
	e := integration.Setup(t)
	site := &assetSite{assets: map[string][]byte{touchIconURL: logoFixture(t, 512, 512)}}
	blob := blobstore.NewMemory()
	w := onboardingLogoWorker(e, site, blob)
	args := readTheOnboardingSite(t, e, w)

	key, _ := parkedLogo(t, e, args.SiteReadID)
	if key == nil {
		t.Fatal("the read parked no mark; this case has nothing to reclaim")
	}
	cause := errors.New("every extraction lane died")
	if err := w.fail(deepReadWorkerCtx(context.Background(), args), args.SiteReadID, cause); !errors.Is(err, cause) {
		t.Fatalf("failing the read answered %v, want the cause it was given", err)
	}

	if left, origin := parkedLogo(t, e, args.SiteReadID); left != nil || origin != nil {
		t.Fatalf("the failed dossier still names key %v origin %v", left, origin)
	}
	if _, _, err := blob.Get(context.Background(), *key); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("the parked object answers %v, want it collected", err)
	}
}

func TestAFailedReadKeepsTheBytesACompanyIsAlreadyWearing(t *testing.T) {
	// Deleting bytes is irreversible, and an adoption leaves the record and the
	// dossier naming ONE object: whatever else the read's collection may take, a
	// key a company wears is not it. Here the read fails after that adoption —
	// the record's face must survive it, reference and bytes alike.
	e := integration.Setup(t)
	site := &assetSite{assets: map[string][]byte{touchIconURL: logoFixture(t, 512, 512)}}
	blob := blobstore.NewMemory()
	w := onboardingLogoWorker(e, site, blob)
	args := readTheOnboardingSite(t, e, w)

	key, _ := parkedLogo(t, e, args.SiteReadID)
	if key == nil {
		t.Fatal("the read parked no mark; this case has nothing to protect")
	}
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	saved, err := e.People.SaveCompany(human, people.SaveCompanyInput{DisplayName: "Acme"})
	if err != nil {
		t.Fatalf("describe the company: %v", err)
	}
	if _, _, err := e.People.SetOrganizationLogo(human, saved.OrganizationID, *key, touchIconURL); err != nil {
		t.Fatalf("give the company the resolved mark: %v", err)
	}

	if err := w.fail(deepReadWorkerCtx(context.Background(), args), args.SiteReadID,
		errors.New("every extraction lane died")); err == nil {
		t.Fatal("failing the read answered no cause")
	}

	stored, _, err := blob.Get(context.Background(), *key)
	if err != nil {
		t.Fatalf("the object the company wears answers %v, want it kept", err)
	}
	if err := stored.Close(); err != nil {
		t.Fatalf("closing the stored object: %v", err)
	}
	// The reference is kept too: dropping it is what would leave the bytes
	// unreachable by anything that could collect them later.
	if left, _ := parkedLogo(t, e, args.SiteReadID); left == nil || *left != *key {
		t.Fatalf("the dossier now names %v, want the key the company shares at %q", left, *key)
	}
}

// recordingBlobstore remembers what the lane stored and what it collected. A
// resolve mints its key from a fresh uuid, so this is the only way a case can
// name bytes whose key the attempt that stored them never handed to anybody.
type recordingBlobstore struct {
	blobstore.Store
	mu sync.Mutex
	// refuseDelete is the object store declining to collect. Set it to make a
	// case about what survives a collection that cannot run.
	refuseDelete error
	stored       []string
	deleted      map[string]bool
}

func newRecordingBlobstore() *recordingBlobstore {
	return &recordingBlobstore{Store: blobstore.NewMemory(), deleted: map[string]bool{}}
}

func (b *recordingBlobstore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if err := b.Store.Put(ctx, key, r, size, contentType); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stored = append(b.stored, key)
	return nil
}

func (b *recordingBlobstore) Delete(ctx context.Context, key string) error {
	if b.refuseDelete != nil {
		return b.refuseDelete
	}
	if err := b.Store.Delete(ctx, key); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleted[key] = true
	return nil
}

// account answers what the lane stored, and which of it was never collected.
func (b *recordingBlobstore) account() (stored, outstanding []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	stored = append(stored, b.stored...)
	for _, key := range stored {
		if !b.deleted[key] {
			outstanding = append(outstanding, key)
		}
	}
	return stored, outstanding
}

// claimedOnboardingRead starts an unbound dossier and claims it the way the
// worker does, answering both what the job carries and the lease the claim
// stamped.
func claimedOnboardingRead(t *testing.T, e *integration.Env) (SiteDeepReadArgs, people.SiteReadClaim) {
	t.Helper()
	read, _, err := e.People.StartOnboardingSiteRead(
		e.As(e.Rep1, nil, integration.AdminPerms), seedURL, "human:"+e.Rep1.String(), nil)
	if err != nil {
		t.Fatalf("start the onboarding read: %v", err)
	}
	args := SiteDeepReadArgs{Workspace: e.WS, SiteReadID: read.ID, RequestedBy: read.RequestedBy}
	claim, err := e.People.BeginSiteRead(deepReadWorkerCtx(context.Background(), args), read.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("claim the onboarding read: %v", err)
	}
	return args, claim
}

// endedOnboardingRead starts an unbound dossier, claims it the way the worker
// does, and closes it — the row a resolve still in flight comes back to when the
// reclaim window (BeginSiteRead) has handed the read to another attempt.
func endedOnboardingRead(t *testing.T, e *integration.Env, status string) (SiteDeepReadArgs, people.SiteReadClaim) {
	t.Helper()
	args, claim := claimedOnboardingRead(t, e)
	if err := e.People.FinishSiteRead(deepReadWorkerCtx(context.Background(), args),
		args.SiteReadID, people.FinishSiteReadInput{Status: status}); err != nil {
		t.Fatalf("end the onboarding read as %s: %v", status, err)
	}
	return args, claim
}

func TestADossierThatEndedRefusesALateParkedMark(t *testing.T) {
	// Whatever the read ended as, its mark is settled. A read that ended without
	// a company already had its parked object collected on the way to terminal,
	// and nothing runs that collection twice; a read that ended with a report is
	// a draft under review, whose face must not change underneath the reviewer.
	// Taking the reference either way records bytes nothing adopts and nothing
	// finds again.
	for _, status := range []string{"failed", "cancelled", "done", "partial"} {
		t.Run(status, func(t *testing.T) {
			e := integration.Setup(t)
			args, claim := endedOnboardingRead(t, e, status)
			workerCtx := deepReadWorkerCtx(context.Background(), args)
			late := siteReadLogoKey(ids.From[ids.WorkspaceKind](e.WS), args.SiteReadID)
			recorded, superseded, err := e.People.RecordSiteReadLogo(workerCtx, args.SiteReadID, claim.ClaimedAt, late, touchIconURL)
			if err != nil {
				t.Fatalf("parking a mark on a %s read: %v", status, err)
			}
			if recorded {
				t.Fatalf("the %s dossier took the reference; nothing would ever adopt or collect it", status)
			}
			if superseded != nil {
				t.Fatalf("the refused park named %q as superseded, having superseded nothing", *superseded)
			}
			if key, origin := parkedLogo(t, e, args.SiteReadID); key != nil || origin != nil {
				t.Fatalf("the %s dossier now names key %v origin %v", status, key, origin)
			}
		})
	}
}

func TestAResolveThatLandsAfterTheReadEndedCollectsItsOwnBytes(t *testing.T) {
	// Bytes first, row second — so a refused park leaves an object no row names,
	// and a per-attempt key nobody else was ever told. The attempt that stored it
	// is the last thing that can still find it, which is why the collection
	// happens there rather than being left to a sweep that has nothing to sweep.
	e := integration.Setup(t)
	site := &assetSite{assets: map[string][]byte{touchIconURL: logoFixture(t, 512, 512)}}
	blob := newRecordingBlobstore()
	w := onboardingLogoWorker(e, site, blob)
	args, claim := endedOnboardingRead(t, e, "failed")

	// The claim a stalled attempt still holds: unbound, seeded from the page it
	// crawled before the dossier was closed under it.
	w.resolveLogo(deepReadWorkerCtx(context.Background(), args), args, claim, declaringCrawl())

	stored, outstanding := blob.account()
	if len(stored) == 0 {
		t.Fatal("the late resolve stored nothing; this case has no bytes to account for")
	}
	if len(outstanding) != 0 {
		t.Fatalf("the refused resolve left %v stored; no row names those bytes and nothing can find them again", outstanding)
	}
	if key, origin := parkedLogo(t, e, args.SiteReadID); key != nil || origin != nil {
		t.Fatalf("the failed dossier took the late mark: key %v origin %v", key, origin)
	}
}

// reclaimTheRead takes a still-running dossier over the way a replacement
// worker does. The lease it presents has lapsed by the time the statement
// reaches the database — the reclaim the worker performs once its own,
// minutes-long grace has run out, with none of the waiting.
func reclaimTheRead(t *testing.T, e *integration.Env, args SiteDeepReadArgs) people.SiteReadClaim {
	t.Helper()
	claim, err := e.People.BeginSiteRead(deepReadWorkerCtx(context.Background(), args),
		args.SiteReadID, time.Microsecond)
	if err != nil {
		t.Fatalf("reclaim the stalled read: %v", err)
	}
	return claim
}

func TestOnlyTheAttemptHoldingTheReadParksItsMark(t *testing.T) {
	// A reclaim puts a running read back into running under a NEW attempt, so
	// the status alone cannot tell the two apart. The stalled attempt resuming
	// afterwards must be refused: parking would replace the mark the holder just
	// recorded, and the superseded key it would be handed back is the holder's
	// own object — which its caller then deletes.
	e := integration.Setup(t)
	args, stalled := claimedOnboardingRead(t, e)
	workerCtx := deepReadWorkerCtx(context.Background(), args)
	current := reclaimTheRead(t, e, args)

	held := siteReadLogoKey(ids.From[ids.WorkspaceKind](e.WS), args.SiteReadID)
	recorded, superseded, err := e.People.RecordSiteReadLogo(workerCtx, args.SiteReadID, current.ClaimedAt, held, touchIconURL)
	if err != nil {
		t.Fatalf("the holding attempt parking its mark: %v", err)
	}
	if !recorded || superseded != nil {
		t.Fatalf("the holding attempt's park recorded %v superseding %v, want it taken and superseding nothing", recorded, superseded)
	}

	late := siteReadLogoKey(ids.From[ids.WorkspaceKind](e.WS), args.SiteReadID)
	recorded, superseded, err = e.People.RecordSiteReadLogo(workerCtx, args.SiteReadID, stalled.ClaimedAt, late, seedURL+"/stale.png")
	if err != nil {
		t.Fatalf("the stalled attempt parking its mark: %v", err)
	}
	if recorded {
		t.Fatal("the stalled attempt parked its mark on a read another attempt holds")
	}
	if superseded != nil {
		t.Fatalf("the refused park named %q as superseded — the holder's object, which the caller would collect", *superseded)
	}
	key, origin := parkedLogo(t, e, args.SiteReadID)
	if key == nil || origin == nil {
		t.Fatal("the dossier holds no mark at all; the refused park cleared the holder's")
	}
	if *key != held || *origin != touchIconURL {
		t.Fatalf("the dossier names key %q origin %q, want the holding attempt's mark at %q", *key, *origin, held)
	}
}

func TestAResolveThatLandsAfterAnotherAttemptTookTheReadKeepsThatAttemptsBytes(t *testing.T) {
	// The whole lane over the same hand-off: the refused attempt collects the
	// bytes IT stored, and leaves standing the object the holder's mark names.
	// Collecting that one would leave the dossier — and the company its
	// confirmation creates — pointing at bytes nothing can serve.
	e := integration.Setup(t)
	site := &assetSite{assets: map[string][]byte{touchIconURL: logoFixture(t, 512, 512)}}
	blob := newRecordingBlobstore()
	w := onboardingLogoWorker(e, site, blob)
	args, stalled := claimedOnboardingRead(t, e)
	workerCtx := deepReadWorkerCtx(context.Background(), args)

	w.resolveLogo(workerCtx, args, reclaimTheRead(t, e, args), declaringCrawl())
	held, _ := parkedLogo(t, e, args.SiteReadID)
	if held == nil {
		t.Fatal("the holding attempt parked no mark; this case has nothing to protect")
	}

	w.resolveLogo(workerCtx, args, stalled, declaringCrawl())

	after, origin := parkedLogo(t, e, args.SiteReadID)
	if after == nil || origin == nil {
		t.Fatal("the dossier holds no mark at all; the refused resolve cleared the holder's")
	}
	if *after != *held || *origin != touchIconURL {
		t.Fatalf("the dossier names key %q origin %q, want the holding attempt's mark at %q", *after, *origin, *held)
	}
	stored, outstanding := blob.account()
	if len(stored) != 2 {
		t.Fatalf("the two attempts stored %v, want one object each", stored)
	}
	if len(outstanding) != 1 || outstanding[0] != *held {
		t.Fatalf("%v was left stored; want only the mark the dossier names at %q", outstanding, *held)
	}
}

// anchorWearingAPersonsMark describes the company by hand and gives it a logo a
// person chose, bytes and all — the field a confirmation must not touch, and
// the object a collection must not take.
func anchorWearingAPersonsMark(t *testing.T, e *integration.Env, blob blobstore.Store) string {
	t.Helper()
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	saved, err := e.People.SaveCompany(human, people.SaveCompanyInput{DisplayName: "Acme"})
	if err != nil {
		t.Fatalf("describe the company by hand: %v", err)
	}
	uploaded := blobstore.WorkspaceKey(ids.From[ids.WorkspaceKind](e.WS), organizationLogoKind,
		saved.OrganizationID.String()+"/uploaded")
	chosen := logoFixture(t, 64, 64)
	if err := blob.Put(context.Background(), uploaded, bytes.NewReader(chosen),
		int64(len(chosen)), imagenorm.ContentType); err != nil {
		t.Fatalf("store the person's own mark: %v", err)
	}
	if _, _, err := e.People.SetOrganizationLogo(human, saved.OrganizationID, uploaded,
		seedURL+"/chosen-by-a-person.png"); err != nil {
		t.Fatalf("record the person's own logo: %v", err)
	}
	return uploaded
}

// rowsNaming counts every row that could still lead something back to an
// object — the dossiers that parked it and the companies that wear it.
func rowsNaming(t *testing.T, e *integration.Env, key string) int {
	t.Helper()
	return e.WsCount(t, `SELECT count(*) FROM site_read WHERE logo_object_key = $1`, key) +
		e.WsCount(t, `SELECT count(*) FROM organization WHERE logo_object_key = $1`, key)
}

// readTheAnchorsSiteFor runs the logo lane over the seed page and answers the
// mark the unbound dossier parked for the confirmation.
func readTheAnchorsSiteFor(t *testing.T, e *integration.Env, blob blobstore.Store) (SiteDeepReadArgs, string) {
	t.Helper()
	site := &assetSite{assets: map[string][]byte{touchIconURL: logoFixture(t, 512, 512)}}
	args := readTheOnboardingSite(t, e, onboardingLogoWorker(e, site, blob))
	parked, _ := parkedLogo(t, e, args.SiteReadID)
	if parked == nil {
		t.Fatal("the read parked no mark; this case has nothing to account for")
	}
	return args, *parked
}

func TestConfirmingAnOnboardingReadKeepsTheLogoAPersonGaveTheAnchor(t *testing.T) {
	// A person's own mark holds the field, so the read's mark is adopted by
	// nobody — and the confirmed dossier would be the last thing naming it,
	// forever. The confirmation reports that key instead and the transport
	// collects the bytes, while the logo the person chose is left alone: the
	// object a company wears is never what a collection takes.
	e := integration.Setup(t)
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	blob := newRecordingBlobstore()
	uploaded := anchorWearingAPersonsMark(t, e, blob)
	args, parked := readTheAnchorsSiteFor(t, e, blob)

	engine := &deepReadEngine{people: e.People, blob: blob, log: slog.New(slog.DiscardHandler)}
	orgID := confirmTheAnchorAsTheAPIDoes(t, e, engine, args)

	boundKey, err := e.People.OrganizationLogoKey(human, orgID)
	if err != nil {
		t.Fatalf("the anchor lost its logo: %v", err)
	}
	if boundKey != uploaded {
		t.Fatalf("the anchor now names %q, want the logo the person set at %q", boundKey, uploaded)
	}
	stored, _, err := blob.Get(context.Background(), uploaded)
	if err != nil {
		t.Fatalf("the mark the person chose answers %v, want it kept", err)
	}
	if err := stored.Close(); err != nil {
		t.Fatalf("closing the stored object: %v", err)
	}
	if _, _, err := blob.Get(context.Background(), parked); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("the unadopted object at %q answers %v, want it collected", parked, err)
	}
	if n := rowsNaming(t, e, parked); n != 0 {
		t.Fatalf("%d row(s) still name the collected object at %q", n, parked)
	}
}

func TestConfirmingAnOnboardingReadCollectsNothingWhenTheAnchorAdoptsTheMark(t *testing.T) {
	// The ordinary path: the anchor is created with no logo, so it takes the
	// one its own read resolved. Nothing is unadopted, and a collection here
	// would delete the face the company is now wearing.
	e := integration.Setup(t)
	blob := newRecordingBlobstore()
	args, parked := readTheAnchorsSiteFor(t, e, blob)

	engine := &deepReadEngine{people: e.People, blob: blob, log: slog.New(slog.DiscardHandler)}
	orgID := confirmTheAnchorAsTheAPIDoes(t, e, engine, args)

	boundKey, err := e.People.OrganizationLogoKey(e.As(e.Rep1, nil, integration.AdminPerms), orgID)
	if err != nil {
		t.Fatalf("the confirmed anchor has no logo: %v", err)
	}
	if boundKey != parked {
		t.Fatalf("the anchor names %q, want the adopted object at %q", boundKey, parked)
	}
	stored, outstanding := blob.account()
	if len(stored) != len(outstanding) {
		t.Fatalf("the adoption collected %d of the %d objects it stored; the company wears them",
			len(stored)-len(outstanding), len(stored))
	}
}

func TestAConfirmationSurvivesAnObjectStoreThatWillNotCollect(t *testing.T) {
	// The collection runs after the commit, so it can only ever cost storage:
	// the company exists, its face is decided, and onboarding is not failed a
	// second time over bytes nobody can see.
	e := integration.Setup(t)
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	blob := newRecordingBlobstore()
	blob.refuseDelete = errors.New("object store unavailable")
	uploaded := anchorWearingAPersonsMark(t, e, blob)
	args, parked := readTheAnchorsSiteFor(t, e, blob)

	var logs bytes.Buffer
	engine := &deepReadEngine{
		people: e.People, blob: blob, log: slog.New(slog.NewTextHandler(&logs, nil)),
	}
	orgID := confirmTheAnchorAsTheAPIDoes(t, e, engine, args)

	boundKey, err := e.People.OrganizationLogoKey(human, orgID)
	if err != nil {
		t.Fatalf("the anchor lost its logo: %v", err)
	}
	if boundKey != uploaded {
		t.Fatalf("the anchor now names %q, want the logo the person set at %q", boundKey, uploaded)
	}
	if n := rowsNaming(t, e, parked); n != 0 {
		t.Fatalf("%d row(s) still name %q — the confirmation's transaction released it", n, parked)
	}
	if !strings.Contains(logs.String(), "reclaiming an unreferenced logo object failed") {
		t.Fatalf("the refused collection was not observable: %s", logs.String())
	}
}

func TestAConfirmationWithNoObjectStoreKeepsTheUnadoptedReference(t *testing.T) {
	// The parked key embeds a per-attempt uuid nothing else recorded, so it is
	// the object's last handle. A caller that owns no object store cannot
	// delete those bytes, and dropping the reference for it would turn a
	// findable orphan into an unfindable one.
	e := integration.Setup(t)
	blob := newRecordingBlobstore()
	anchorWearingAPersonsMark(t, e, blob)
	args, parked := readTheAnchorsSiteFor(t, e, blob)

	engine := &deepReadEngine{people: e.People, log: slog.New(slog.DiscardHandler)}
	confirmTheAnchorAsTheAPIDoes(t, e, engine, args)

	left, origin := parkedLogo(t, e, args.SiteReadID)
	if left == nil || *left != parked || origin == nil {
		t.Fatalf("the confirmed dossier names key %v origin %v, want the unadopted mark at %q",
			left, origin, parked)
	}
	if _, _, err := blob.Get(context.Background(), parked); err != nil {
		t.Fatalf("the unadopted object answers %v, want it kept for the reference that still names it", err)
	}
}
