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
	"net/url"
	"sort"
	"strconv"
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

// logoCandidates builds the ordered, deduplicated candidate chain from what
// the seed page declared plus the well-known favicon path every site answers
// whether it declares one or not.
//
// A candidate may live on ANOTHER host, and that is a deliberate departure
// from the crawl's off-domain rule (sitecrawlwave.go, which refuses to follow
// page content off the seed's site). It is a departure because a mark
// routinely is CDN-hosted — afs.de serves its logo from CloudFront, stripe.com
// from its asset host — and refusing those would leave exactly the companies
// with the most deliberate branding wearing a monogram.
//
// What makes the departure narrow rather than an open relay: the fetch is a
// GET of BYTES that are only ever decoded as an image, never read as content
// and never followed; the target host's own robots.txt still governs it; the
// SSRF dialer still refuses non-public addresses; and the chain is bounded to
// logoMaxCandidates fetches of maxAssetBytes each, so one read's whole asset
// egress is bounded whatever a page declares. Every candidate tried, off-host
// ones included, is named in the report.
func logoCandidates(seedURL string, declared declaredAssets) (candidates []string, dropped int) {
	seen := make(map[string]bool, len(declared.icons)+2)
	keepNew := func(into []string, urls ...string) []string {
		for _, u := range urls {
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			into = append(into, u)
		}
		return into
	}

	icons := keepNew(nil, iconURLsByRel(declared.icons, webread.RelAppleTouchIcon)...)
	icons = keepNew(icons, iconURLsByRel(declared.icons, webread.RelIcon)...)

	// The two site-level sources: what every site has whether it declared
	// anything or not, and — last — the share image, which on a small site
	// usually IS the mark.
	var candidateFallbacks []string
	if wellKnown, ok := wellKnownFaviconURL(seedURL); ok {
		candidateFallbacks = append(candidateFallbacks, wellKnown)
	}
	if declared.ogImage != "" {
		candidateFallbacks = append(candidateFallbacks, declared.ogImage)
	}

	// The cap bounds one read's asset egress, so it has to bite somewhere —
	// but it must not bite the fallbacks. They are exactly what answers when
	// the declarations are stale, and a page carrying logoMaxCandidates dead
	// touch-icon tags would otherwise spend the whole budget on them and
	// leave the company with no mark at all.
	room := logoMaxCandidates - len(candidateFallbacks)
	if len(icons) > room {
		dropped = len(icons) - room
		icons = icons[:room]
	}
	// Deduped against the icons that SURVIVED the cap, never against the ones
	// it cut: a page declaring /favicon.ico among a hundred stale tags would
	// otherwise lose the fallback to a candidate that is never fetched.
	kept := make(map[string]bool, len(icons))
	for _, icon := range icons {
		kept[icon] = true
	}
	for _, candidate := range candidateFallbacks {
		if !kept[candidate] {
			kept[candidate] = true
			icons = append(icons, candidate)
		}
	}
	return icons, dropped
}

// iconURLsByRel selects one rel's icons, largest declared size first. A page
// that declares several sizes of the same icon is telling us which is the
// detailed one; a page that declares no size at all sorts last, because a
// stated 180x180 is better evidence than a shrug.
func iconURLsByRel(icons []webread.IconRef, rel string) []string {
	matching := make([]webread.IconRef, 0, len(icons))
	for _, icon := range icons {
		if icon.Rel == rel {
			matching = append(matching, icon)
		}
	}
	sort.SliceStable(matching, func(i, j int) bool {
		return declaredIconEdge(matching[i].Sizes) > declaredIconEdge(matching[j].Sizes)
	})
	urls := make([]string, 0, len(matching))
	for _, icon := range matching {
		urls = append(urls, icon.URL)
	}
	return urls
}

// declaredIconEdge reads the largest edge out of a sizes attribute
// ("32x32", "16x16 32x32", "any"), or 0 when it states nothing usable —
// "any" means a scalable source, which says nothing about pixels. Both sides
// of each token count: a rare non-square declaration is ranked by its longer
// edge, which is what "largest" has to mean for the ordering to hold.
func declaredIconEdge(sizes string) int {
	largest := 0
	for _, token := range strings.Fields(sizes) {
		width, height, found := strings.Cut(token, "x")
		if !found {
			continue
		}
		for _, side := range []string{width, height} {
			if edge, err := strconv.Atoi(side); err == nil && edge > largest {
				largest = edge
			}
		}
	}
	return largest
}

// wellKnownFaviconURL is /favicon.ico on the seed's own origin: the icon a
// site serves whether or not it ever declared one, and the last thing worth
// asking for before falling back to the monogram.
func wellKnownFaviconURL(seedURL string) (string, bool) {
	parsed, err := url.Parse(seedURL)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	return parsed.Scheme + "://" + parsed.Host + "/favicon.ico", true
}

// resolveLogo gives the organization its face: resolve the mark from what the
// seed page declared, store the normalized bytes, then point the row at them.
//
// It is best-effort throughout, and deliberately so — a logo is polish on a
// read whose real product is evidenced facts, so nothing here may fail that
// read. Every outcome is logged instead, and a company with no resolved logo
// renders its deterministic monogram, which is a clean face rather than a gap.
//
// Bytes first, row second: the other order would point a row at bytes that are
// not there, which is the one outcome a user would see. Each attempt writes its
// OWN key and the row write hands back the one it superseded, so the stored
// image and the recorded origin always describe the same picture even when two
// resolves of one company overlap — and a person's uploaded logo, which lives
// at a key of its own, is never written over at all. What that costs is an
// unreferenced object when a crash lands between the two steps; the reclaim
// below collects the ordinary case.
func (w *siteDeepReadWorker) resolveLogo(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim, crawl siteCrawl) {
	if w.blob == nil || claim.OrganizationID == nil {
		// No object store to hold the bytes, or an unbound onboarding draft
		// with no organization row to point at one yet.
		return
	}
	orgID := ids.From[ids.OrganizationKind](*claim.OrganizationID)
	// Ask before resolving anything: a field a person holds is not going to be
	// written, so fetching and normalizing a mark for it is work nobody uses.
	// The write applies the rule again under the row lock — this is the cheap
	// path, never the authority.
	held, err := w.people.LogoHeldByHuman(ctx, orgID)
	if err != nil {
		w.log.WarnContext(ctx, "reading the organization's logo provenance failed",
			"read", args.SiteReadID.String(), "err", err)
		return
	}
	if held {
		w.log.InfoContext(ctx, "logo resolve skipped: a person's own logo holds the field",
			"read", args.SiteReadID.String())
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
	// Bounding the writes is only safe because the reclaim below is DETACHED:
	// a deadline landing between the two writes still gets its object
	// collected, instead of stranding one at a per-attempt key no row ever
	// named — which nothing else could find to collect later.
	ctx, cancel := context.WithTimeout(ctx, logoLaneBudget)
	defer cancel()

	// The spelling that ANSWERED, not the one asked for: after the fallback
	// ladder recovers a site on www or over http, /favicon.ico under the
	// original seed is a guess at a host that served nothing.
	seedURL := crawl.SeedURL
	if seedURL == "" {
		seedURL = claim.SeedURL
	}
	logo, attempts := resolveOrganizationLogo(ctx, w.fetch, seedURL, crawl.SeedAssets)
	if logo.PNG == nil {
		w.log.InfoContext(ctx, "site read resolved no logo",
			"read", args.SiteReadID.String(), "seed", claim.SeedURL,
			"candidates", logoAttemptSummary(attempts))
		return
	}

	key := organizationLogoKey(ids.From[ids.WorkspaceKind](args.WorkspaceID), orgID)
	if err := w.blob.Put(ctx, key, bytes.NewReader(logo.PNG), int64(len(logo.PNG)), imagenorm.ContentType); err != nil {
		// A failed Put can still have left a partial object, and no row names
		// this key, so collecting it is unambiguously safe.
		w.reclaimLogoObject(ctx, args.SiteReadID, &key)
		w.log.WarnContext(ctx, "storing the resolved logo failed",
			"read", args.SiteReadID.String(), "source", logo.SourceURL, "err", err)
		return
	}
	written, superseded, err := w.people.SetOrganizationLogo(ctx, orgID, key, logo.SourceURL)
	if err != nil {
		// Deliberately NOT reclaimed. An error here does not mean the write
		// did not happen: a transaction can commit and still fail the caller
		// on the way back — a cancelled context, a dropped connection. If it
		// did commit, the row names these bytes, and deleting them would show
		// a broken image. An orphan costs storage; that costs the user their
		// company's face, so the ambiguous case keeps the bytes.
		w.log.WarnContext(ctx, "recording the resolved logo failed; its bytes are left in place because the write's outcome is unknown",
			"read", args.SiteReadID.String(), "source", logo.SourceURL, "key", key, "err", err)
		return
	}
	if !written {
		w.reclaimLogoObject(ctx, args.SiteReadID, &key)
		w.log.InfoContext(ctx, "resolved logo left unused: a person's own logo holds the field",
			"read", args.SiteReadID.String(), "source", logo.SourceURL)
		return
	}
	w.reclaimLogoObject(ctx, args.SiteReadID, superseded)
	w.log.InfoContext(ctx, "site read resolved the organization logo",
		"read", args.SiteReadID.String(), "source", logo.SourceURL,
		"source_size", fmt.Sprintf("%dx%d", logo.SourceWidth, logo.SourceHeight),
		"stored_bytes", len(logo.PNG), "candidates", logoAttemptSummary(attempts))
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
// successful write superseded, or this attempt's own bytes when the write did
// not happen. Best-effort like the rest of the lane — a failure here costs
// storage, never correctness, so it is logged and the read carries on.
//
// It runs on a DETACHED context, for the same reason finish() does: this is
// the answer to work that has already happened, and the most likely reason to
// be reclaiming at all is that the work ran out of time. Reusing the context
// that just expired would skip exactly the deletes that matter — and an
// object at a per-attempt key that no row ever named is one nothing else can
// find to collect later.
func (w *siteDeepReadWorker) reclaimLogoObject(ctx context.Context, readID ids.UUID, key *string) {
	if key == nil || *key == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), logoReclaimBudget)
	defer cancel()
	if err := w.blob.Delete(ctx, *key); err != nil {
		w.log.WarnContext(ctx, "reclaiming a superseded logo object failed",
			"read", readID.String(), "key", *key, "err", err)
	}
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
