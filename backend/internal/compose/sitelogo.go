// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The logo resolve (A55): the company's mark comes out of the page the deep
// read ALREADY fetched — its og:image and its declared icons — so a face for
// every company costs no third-party logo API and no new egress beyond the
// asset itself. Candidates are tried in a fixed order and the first one that
// is recognizably a MARK wins; everything stored is normalized once here, at
// store time, never at render time. When nothing usable resolves the record
// keeps no logo at all and the render layer draws its deterministic monogram —
// the floor that makes a missing logo invisible instead of broken.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/imagenorm"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

const (
	// logoMaxEdge caps the stored square. A logo renders at avatar sizes, so
	// 300px is generous headroom for a high-DPI record header and small enough
	// that one stored variant serves every surface — no sm/md/lg fan-out to
	// keep in sync, and the browser does the only downscale that is left.
	logoMaxEdge = 300
	// logoMinEdge is the smallest mark worth storing. Below it the source
	// carries too few pixels to read as a logo at any size, and a crisp
	// monogram beats a mush of four pixels.
	logoMinEdge = 32
	// logoSquareAspect is the widest a candidate may be and still be taken as
	// THE mark on sight. Icons are square by construction; a site's og:image
	// is a 1.91:1 sharing banner about as often as it is the logo, and only
	// the shape tells them apart.
	logoSquareAspect = 1.4
	// logoMaxAspect is the widest a candidate may be at all. A wordmark is a
	// legitimate logo and letterboxes acceptably up to here; past it a picture
	// is a banner or a hero shot, which says nothing about the company at
	// avatar size.
	logoMaxAspect = 2.5
	// logoMaxCandidates bounds how many assets one resolve will ask for. The
	// chain is fetched serially and the deep-read queue is two workers wide, so
	// a page declaring a thousand icon links would otherwise let one site hold
	// a worker until its deadline. A site that has not shown its mark in the
	// first few declarations is not hiding it in the thousandth, and everything
	// past the bound is reported as dropped rather than silently ignored.
	//
	// With webread's per-asset cap this is also the lane's whole egress bound:
	// at most eight fetches of 2 MiB. That spend is NOT counted against the
	// crawl's own byte budget — the budget governs pages read, and this is the
	// one declared asset per company that the read exists to find.
	logoMaxCandidates = 8
)

// logoLaneBudget bounds the whole lane — fetch, object write, row write. It is
// counted into the deep read's job timeout (deepread.go), so the lane can never
// spend the allowance that exists to close the dossier.
const logoLaneBudget = 20 * time.Second

// logoReclaimBudget bounds one detached delete of an unreferenced object.
const logoReclaimBudget = 15 * time.Second

// organizationLogoKind is the blobstore key's entity discriminator, the peer
// of "attachment" (blobstore.WorkspaceKey).
const organizationLogoKind = "organization_logo"

// Outcomes the resolve records per candidate. They are the quality signal the
// `worker siteread` report prints: WHY the obvious logo was passed over is the
// thing you need when a company's face comes out wrong.
const (
	logoOutcomeChosen   = "chosen"
	logoOutcomeFallback = "wide, kept only as a fallback"
	logoOutcomeSkipped  = "also wide, and an earlier wide candidate is already the fallback"
)

// organizationLogoKey mints the key for ONE resolve attempt. It is
// per-attempt, not per-organization, so two resolves of the same company can
// never write the same object — an overwrite there would leave the stored
// image and the row's recorded origin describing different pictures, and would
// also write straight over a logo a person uploaded. The organization id stays
// in the key so an object is still traceable to the record it belongs to.
func organizationLogoKey(wsID ids.WorkspaceID, orgID ids.OrganizationID) string {
	return blobstore.WorkspaceKey(wsID, organizationLogoKind, orgID.String()+"/"+ids.NewV7().String())
}

// siteReadLogoKey mints the key for a mark resolved before its company exists.
// The dossier id stands where the organization id stands in its sibling: it is
// what the object is traceable to until a confirmation adopts it, and the
// per-attempt uuid keeps two resolves of one read from writing the same object.
// The bytes are never copied when the company arrives — the record simply names
// this key — so the object outlives the read that stored it.
func siteReadLogoKey(wsID ids.WorkspaceID, readID ids.UUID) string {
	return blobstore.WorkspaceKey(wsID, organizationLogoKind, "site-read/"+readID.String()+"/"+ids.NewV7().String())
}

// declaredAssets is the visual identity one page declared in its <head>. The
// crawl carries the seed page's set forward so the resolve reads it instead of
// fetching the home page a second time.
type declaredAssets struct {
	ogImage string
	icons   []webread.IconRef
}

// assetFetcher is the slice of *webread.Fetcher the logo resolve needs.
type assetFetcher interface {
	FetchAsset(ctx context.Context, rawURL string) ([]byte, string, error)
}

// resolvedLogo is one company mark, normalized and ready to store.
type resolvedLogo struct {
	// PNG is the normalized square; nil means nothing usable resolved.
	PNG []byte
	// SourceURL is the asset the bytes came from — the logo's provenance,
	// stored as organization.logo_origin.
	SourceURL string
	// SourceWidth and SourceHeight are the source's own dimensions, for the
	// debug report: they explain the ranking decision that the outcome names.
	SourceWidth  int
	SourceHeight int
}

// logoAttempt is what became of one candidate.
type logoAttempt struct {
	URL     string
	Outcome string
}

// resolveOrganizationLogo walks the candidate chain and returns the company's
// mark, or a zero resolvedLogo when the site declared nothing usable. Every
// candidate it touched comes back too, in the order tried.
//
// The chain is ordered by how likely a candidate is to BE the logo — the
// homescreen icon first, then the favicons, then the well-known
// /favicon.ico, and the og:image last. A candidate that is square enough is
// taken immediately; a wide one is remembered and only used if nothing
// squarer turns up.
//
// The declared icons come first because they are the only assets a site
// publishes SAYING "this is us at icon size". og:image is whatever the page
// wants shown when it is shared, which is its mark on a small site and a hero
// photo, a product shot or a podcast tile on many others. Ranked first, a
// square-ish photo was taken on sight and the site's real apple-touch-icon
// was never asked for — an import of 162 companies produced several accounts
// wearing a stock photo. Wide sharing banners were already screened out by
// shape; square ones could only be screened out by asking for the icon first.
func resolveOrganizationLogo(ctx context.Context, fetch assetFetcher, seedURL string, declared declaredAssets) (resolvedLogo, []logoAttempt) {
	candidates, dropped := logoCandidates(seedURL, declared)
	attempts := make([]logoAttempt, 0, len(candidates)+1)
	var fallback resolvedLogo
	for _, candidate := range candidates {
		logo, aspect, drop := fetchLogoCandidate(ctx, fetch, candidate)
		switch {
		case drop != "":
			attempts = append(attempts, logoAttempt{URL: candidate, Outcome: drop})
		case aspect <= logoSquareAspect:
			attempts = append(attempts, logoAttempt{URL: candidate, Outcome: logoOutcomeChosen})
			return logo, attempts
		case fallback.PNG == nil:
			attempts = append(attempts, logoAttempt{URL: candidate, Outcome: logoOutcomeFallback})
			fallback = logo
		default:
			attempts = append(attempts, logoAttempt{URL: candidate, Outcome: logoOutcomeSkipped})
		}
	}
	if dropped > 0 {
		// A cap that truncates in silence reads afterwards as "the site
		// declared nothing better", which is a different fact.
		attempts = append(attempts, logoAttempt{
			URL:     fmt.Sprintf("%d further declared candidate(s)", dropped),
			Outcome: fmt.Sprintf("not tried: the chain stops at %d", logoMaxCandidates),
		})
	}
	return fallback, attempts
}

// fetchLogoCandidate fetches one candidate and normalizes it, or reports in
// plain words why it is not a usable mark. A drop is never an error the caller
// has to handle: the chain simply moves on, and the reason is what the debug
// report prints.
func fetchLogoCandidate(ctx context.Context, fetch assetFetcher, rawURL string) (logo resolvedLogo, aspect float64, drop string) {
	body, _, err := fetch.FetchAsset(ctx, rawURL)
	if err != nil {
		return resolvedLogo{}, 0, fmt.Sprintf("could not be fetched: %v", err)
	}
	// The declared content type is not consulted: a server mislabels an image
	// (or serves an HTML error page as one) often enough that the bytes are the
	// only honest answer, and the decoder reads those.
	img, err := imagenorm.Decode(body)
	if err != nil {
		return resolvedLogo{}, 0, fmt.Sprintf("is not a decodable image: %v", err)
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	short, long := min(width, height), max(width, height)
	if short < logoMinEdge {
		return resolvedLogo{}, 0, fmt.Sprintf("is %dx%d, under the %dpx minimum edge", width, height, logoMinEdge)
	}
	aspect = float64(long) / float64(short)
	if aspect > logoMaxAspect {
		return resolvedLogo{}, 0, fmt.Sprintf("is %dx%d — a banner shape, not a mark", width, height)
	}
	png, err := imagenorm.SquarePNG(img, logoMaxEdge)
	if err != nil {
		return resolvedLogo{}, 0, fmt.Sprintf("could not be normalized: %v", err)
	}
	return resolvedLogo{PNG: png, SourceURL: rawURL, SourceWidth: width, SourceHeight: height}, aspect, ""
}

// resolveLogo gives the company its face: resolve the mark from what the seed
// page declared, store the normalized bytes, then point a row at them.
//
// WHICH row depends on whether the company exists yet. An enrichment read has
// its organization and names it directly. An onboarding read does not — it
// reads the installation's own website to propose the anchor a human then
// confirms into being — so the reference waits on the dossier until that
// confirmation claims it (recordDossierLogo). Both resolve from the same
// declarations on the same page, because the alternative for the anchor is no
// logo at all: nothing else ever offers this company one.
//
// It is best-effort throughout, and deliberately so — a logo is polish on a
// read whose real product is evidenced facts, so nothing here may fail that
// read. Every outcome is logged instead, and a company with no resolved logo
// renders its deterministic monogram, which is a clean face rather than a gap.
func (w *siteDeepReadWorker) resolveLogo(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim, crawl siteCrawl) {
	if w.blob == nil {
		// No object store to hold the bytes.
		return
	}
	if claim.OrganizationID != nil &&
		!w.logoWorthResolving(ctx, args.SiteReadID, ids.From[ids.OrganizationKind](*claim.OrganizationID)) {
		return
	}

	// ONE deadline over the whole lane — the fetching, the object write and the
	// row write alike. Every one of them can block on something this process
	// does not control: eight fetch timeouts against a dead host, an object
	// store that stopped answering, a row lock another transaction is holding.
	// The time they would spend is the time the job budget reserves for CLOSING
	// the dossier, and a read cancelled before finish() records its outcome
	// stays running forever, squatting the organization's one in-flight slot.
	// A logo is never worth that: past the deadline the lane stops and the
	// record keeps its monogram. logoLaneBudget is counted into Timeout, so
	// this spend is declared rather than borrowed.
	//
	// Bounding the writes is only safe because the reclaim is DETACHED: a
	// deadline landing between the two writes still gets its object collected,
	// instead of stranding one at a per-attempt key no row ever named — which
	// nothing else could find to collect later.
	ctx, cancel := context.WithTimeout(ctx, logoLaneBudget)
	defer cancel()

	// claim.SeedURL is the spelling that ANSWERED — the deep read replaces it
	// with the crawl's own once the crawl returns, so /favicon.ico is never
	// guessed under a host that served nothing.
	logo, attempts := resolveOrganizationLogo(ctx, w.fetch, claim.SeedURL, crawl.SeedAssets)
	if logo.PNG == nil {
		w.log.InfoContext(ctx, "site read resolved no logo",
			"read", args.SiteReadID.String(), "seed", claim.SeedURL,
			"candidates", logoAttemptSummary(attempts))
		return
	}

	// Bytes first, row second: the other order would point a row at bytes that
	// are not there, which is the one outcome a user would see.
	key := w.storeResolvedLogo(ctx, args, claim, logo)
	if key == "" {
		return
	}
	if claim.OrganizationID == nil {
		w.recordDossierLogo(ctx, args.SiteReadID, claim, key, logo, attempts)
		return
	}
	w.recordOrganizationLogo(ctx, args.SiteReadID,
		ids.From[ids.OrganizationKind](*claim.OrganizationID), key, logo, attempts)
}

// storeResolvedLogo writes the normalized bytes under a key unique to THIS
// attempt and answers with it, or with "" when the object store refused. Each
// attempt writing its own key is what keeps the stored image and the recorded
// origin describing the same picture when two resolves overlap — and what keeps
// a logo a person uploaded, which lives at a key of its own, from being written
// over at all.
func (w *siteDeepReadWorker) storeResolvedLogo(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim, logo resolvedLogo) string {
	wsID := ids.From[ids.WorkspaceKind](args.Workspace)
	key := siteReadLogoKey(wsID, args.SiteReadID)
	if claim.OrganizationID != nil {
		key = organizationLogoKey(wsID, ids.From[ids.OrganizationKind](*claim.OrganizationID))
	}
	if err := w.blob.Put(ctx, key, bytes.NewReader(logo.PNG), int64(len(logo.PNG)), imagenorm.ContentType); err != nil {
		// A failed Put can still have left a partial object, and no row names
		// this key, so collecting it is unambiguously safe.
		w.reclaimLogoObject(ctx, args.SiteReadID, &key)
		w.log.WarnContext(ctx, "storing the resolved logo failed",
			"read", args.SiteReadID.String(), "source", logo.SourceURL, "err", err)
		return ""
	}
	return key
}

// recordOrganizationLogo points the organization row at bytes that are already
// stored, and collects whatever that write left unreferenced.
func (w *siteDeepReadWorker) recordOrganizationLogo(ctx context.Context, readID ids.UUID, orgID ids.OrganizationID, key string, logo resolvedLogo, attempts []logoAttempt) {
	written, superseded, err := w.people.SetOrganizationLogo(ctx, orgID, key, logo.SourceURL)
	if err != nil {
		// Deliberately NOT reclaimed. An error here does not mean the write
		// did not happen: a transaction can commit and still fail the caller
		// on the way back — a cancelled context, a dropped connection. If it
		// did commit, the row names these bytes, and deleting them would show
		// a broken image. An orphan costs storage; that costs the user their
		// company's face, so the ambiguous case keeps the bytes.
		w.log.WarnContext(ctx, "recording the resolved logo failed; its bytes are left in place because the write's outcome is unknown",
			"read", readID.String(), "source", logo.SourceURL, "key", key, "err", err)
		return
	}
	if !written {
		w.reclaimLogoObject(ctx, readID, &key)
		w.log.InfoContext(ctx, "resolved logo left unused: a person's own logo holds the field",
			"read", readID.String(), "source", logo.SourceURL)
		return
	}
	w.reclaimLogoObject(ctx, readID, superseded)
	w.log.InfoContext(ctx, "site read resolved the organization logo",
		"read", readID.String(), "source", logo.SourceURL,
		"source_size", fmt.Sprintf("%dx%d", logo.SourceWidth, logo.SourceHeight),
		"stored_bytes", len(logo.PNG), "candidates", logoAttemptSummary(attempts))
}

// recordDossierLogo parks the mark on the read that resolved it, for a company
// that does not exist yet. The confirmation binds it as it creates the anchor,
// under the same human-precedence rule an organization write obeys; a read
// nobody confirms simply never hands it over.
//
// The park carries the lease this attempt claimed the read under, so a dossier
// that has moved on — ended, or reclaimed by a replacement attempt — refuses
// the reference. The bytes stored for a refused park are collected right here:
// an object no row names is one nothing can find to collect later, so the
// attempt that stored it is the last chance it gets.
func (w *siteDeepReadWorker) recordDossierLogo(ctx context.Context, readID ids.UUID, claim people.SiteReadClaim, key string, logo resolvedLogo, attempts []logoAttempt) {
	recorded, superseded, err := w.people.RecordSiteReadLogo(ctx, readID, claim.ClaimedAt, key, logo.SourceURL)
	if err != nil {
		// Kept for the same reason the organization write keeps its bytes: a
		// failed call is not a write that did not happen.
		w.log.WarnContext(ctx, "recording the resolved logo on the dossier failed; its bytes are left in place because the write's outcome is unknown",
			"read", readID.String(), "source", logo.SourceURL, "key", key, "err", err)
		return
	}
	if !recorded {
		w.reclaimLogoObject(ctx, readID, &key)
		w.log.InfoContext(ctx, "resolved logo left unused: the website read is past taking one — it has its company, it has already reported, or another attempt holds it now",
			"read", readID.String(), "source", logo.SourceURL)
		return
	}
	w.reclaimLogoObject(ctx, readID, superseded)
	w.log.InfoContext(ctx, "site read resolved the logo the confirmed company will wear",
		"read", readID.String(), "source", logo.SourceURL,
		"source_size", fmt.Sprintf("%dx%d", logo.SourceWidth, logo.SourceHeight),
		"stored_bytes", len(logo.PNG), "candidates", logoAttemptSummary(attempts))
}

// reclaimParkedLogo collects the mark a read parked and can no longer hand to
// anybody. The onboarding lane stores its bytes while the page is still in
// hand, long before the confirmation that would adopt them exists; a read that
// ends without a dossier never reaches that confirmation, and the reference on
// the dossier row is the only thing that can still find the object.
//
// Best-effort like the rest of the lane, and for a sharper reason here: the
// read has already failed, and storage is not worth failing it a second time.
// The store answers only with a key no record names, so nothing on this path
// can delete bytes a company wears.
func (w *siteDeepReadWorker) reclaimParkedLogo(ctx context.Context, readID ids.UUID) {
	if w.blob == nil {
		return
	}
	key, err := w.people.DiscardSiteReadLogo(ctx, readID)
	if err != nil {
		w.log.WarnContext(ctx, "dropping the logo parked on a read that ended without a company failed",
			"read", readID.String(), "err", err)
		return
	}
	w.reclaimLogoObject(ctx, readID, key)
}

// logoWorthResolving asks before resolving anything: a field a person holds is
// not going to be written, so fetching and normalizing a mark for it is work
// nobody uses. The write applies the rule again under the row lock — this is
// the cheap path, never the authority. A provenance read that fails leaves the
// field's owner unknown, and the lane stands down rather than guess.
func (w *siteDeepReadWorker) logoWorthResolving(ctx context.Context, readID ids.UUID, orgID ids.OrganizationID) bool {
	held, err := w.people.LogoHeldByHuman(ctx, orgID)
	if err != nil {
		w.log.WarnContext(ctx, "reading the organization's logo provenance failed",
			"read", readID.String(), "err", err)
		return false
	}
	if held {
		w.log.InfoContext(ctx, "logo resolve skipped: a person's own logo holds the field",
			"read", readID.String())
		return false
	}
	return true
}

// debugLogo projects a resolve onto the debug report's shape.
func debugLogo(logo resolvedLogo, attempts []logoAttempt) DebugLogo {
	out := DebugLogo{Candidates: make([]DebugLogoAttempt, 0, len(attempts))}
	for _, attempt := range attempts {
		out.Candidates = append(out.Candidates, DebugLogoAttempt(attempt))
	}
	if logo.PNG != nil {
		out.SourceURL = logo.SourceURL
		out.SourceSize = fmt.Sprintf("%dx%d", logo.SourceWidth, logo.SourceHeight)
		out.StoredBytes = len(logo.PNG)
	}
	return out
}

// reclaimLogoObject deletes an object nothing references any more: the mark a
// successful write superseded, this attempt's own bytes when the write did not
// happen, or the mark a confirmation declined to adopt. Best-effort like the
// rest of the lane — a failure here costs storage, never correctness, so it is
// logged and the caller carries on.
//
// It runs on a DETACHED context, for the same reason finish() does: this is
// the answer to work that has already happened, and the most likely reason to
// be reclaiming at all is that the work ran out of time. Reusing the context
// that just expired would skip exactly the deletes that matter — and an
// object at a per-attempt key that no row ever named is one nothing else can
// find to collect later.
//
// A nil store is a role that holds no objects; it never reaches a key worth
// collecting, because the row-writing calls that report one are guarded by the
// same fact.
func deleteUnreferencedLogo(ctx context.Context, blob blobstore.Store, log *slog.Logger, readID ids.UUID, key *string) {
	if blob == nil || key == nil || *key == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), logoReclaimBudget)
	defer cancel()
	if err := blob.Delete(ctx, *key); err != nil {
		log.WarnContext(ctx, "reclaiming an unreferenced logo object failed",
			"read", readID.String(), "key", *key, "err", err)
	}
}

// reclaimLogoObject binds the worker's own object store and logger to the
// collection every write path in this lane ends with.
func (w *siteDeepReadWorker) reclaimLogoObject(ctx context.Context, readID ids.UUID, key *string) {
	deleteUnreferencedLogo(ctx, w.blob, w.log, readID, key)
}

// logoAttemptSummary renders the attempts as one log-friendly line, so a
// resolve that produced no logo says which candidates it considered and why
// each was passed over.
func logoAttemptSummary(attempts []logoAttempt) string {
	if len(attempts) == 0 {
		return "the page declared no logo candidate"
	}
	lines := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		lines = append(lines, attempt.URL+" "+attempt.Outcome)
	}
	return strings.Join(lines, "; ")
}
