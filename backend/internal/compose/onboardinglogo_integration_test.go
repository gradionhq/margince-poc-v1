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
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
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

// confirmTheAnchor finishes the claimed dossier and confirms it — the step that
// creates the company the installation is.
func confirmTheAnchor(t *testing.T, e *integration.Env, args SiteDeepReadArgs) people.Company {
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
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	ready, err := e.People.GetOnboardingSiteRead(ctx, args.SiteReadID)
	if err != nil {
		t.Fatalf("read the finished draft: %v", err)
	}
	website := seedURL
	company, err := e.People.ConfirmCompanySiteRead(ctx, people.ConfirmCompanySiteReadInput{
		ReadID: ready.ID, DraftVersion: ready.DraftVersion, ProposalHash: ready.ProposalHash,
		DisplayName: "Acme", Website: &website,
	}, nil)
	if err != nil {
		t.Fatalf("confirm the onboarding read: %v", err)
	}
	return company
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
	mu      sync.Mutex
	stored  []string
	deleted map[string]bool
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

// endedOnboardingRead starts an unbound dossier, claims it the way the worker
// does, and closes it — the row a resolve still in flight comes back to when the
// reclaim window (BeginSiteRead) has handed the read to another attempt.
func endedOnboardingRead(t *testing.T, e *integration.Env, status string) SiteDeepReadArgs {
	t.Helper()
	read, _, err := e.People.StartOnboardingSiteRead(
		e.As(e.Rep1, nil, integration.AdminPerms), seedURL, "human:"+e.Rep1.String(), nil)
	if err != nil {
		t.Fatalf("start the onboarding read: %v", err)
	}
	args := SiteDeepReadArgs{Workspace: e.WS, SiteReadID: read.ID, RequestedBy: read.RequestedBy}
	workerCtx := deepReadWorkerCtx(context.Background(), args)
	if _, err := e.People.BeginSiteRead(workerCtx, read.ID, 10*time.Minute); err != nil {
		t.Fatalf("claim the onboarding read: %v", err)
	}
	if err := e.People.FinishSiteRead(workerCtx, read.ID, people.FinishSiteReadInput{Status: status}); err != nil {
		t.Fatalf("end the onboarding read as %s: %v", status, err)
	}
	return args
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
			args := endedOnboardingRead(t, e, status)
			workerCtx := deepReadWorkerCtx(context.Background(), args)
			late := siteReadLogoKey(ids.From[ids.WorkspaceKind](e.WS), args.SiteReadID)
			recorded, superseded, err := e.People.RecordSiteReadLogo(workerCtx, args.SiteReadID, late, touchIconURL)
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
	args := endedOnboardingRead(t, e, "failed")

	// The claim a stalled attempt still holds: unbound, seeded from the page it
	// crawled before the dossier was closed under it.
	w.resolveLogo(deepReadWorkerCtx(context.Background(), args), args,
		people.SiteReadClaim{TargetKind: people.TargetKindOnboarding, SeedURL: seedURL}, declaringCrawl())

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

func TestConfirmingAnOnboardingReadKeepsTheLogoAPersonGaveTheAnchor(t *testing.T) {
	e := integration.Setup(t)
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	saved, err := e.People.SaveCompany(human, people.SaveCompanyInput{DisplayName: "Acme"})
	if err != nil {
		t.Fatalf("describe the company by hand: %v", err)
	}
	uploaded := blobstore.WorkspaceKey(ids.From[ids.WorkspaceKind](e.WS), "organization_logo",
		saved.OrganizationID.String()+"/uploaded")
	if _, _, err := e.People.SetOrganizationLogo(human, saved.OrganizationID, uploaded,
		seedURL+"/chosen-by-a-person.png"); err != nil {
		t.Fatalf("record the person's own logo: %v", err)
	}

	site := &assetSite{assets: map[string][]byte{touchIconURL: logoFixture(t, 512, 512)}}
	args := readTheOnboardingSite(t, e, onboardingLogoWorker(e, site, blobstore.NewMemory()))
	if key, _ := parkedLogo(t, e, args.SiteReadID); key == nil {
		t.Fatal("the read resolved no mark; this case has nothing to refuse")
	}
	company := confirmTheAnchor(t, e, args)

	boundKey, err := e.People.OrganizationLogoKey(human, company.OrganizationID)
	if err != nil {
		t.Fatalf("the anchor lost its logo: %v", err)
	}
	if boundKey != uploaded {
		t.Fatalf("the anchor now names %q, want the logo the person set at %q", boundKey, uploaded)
	}
}
