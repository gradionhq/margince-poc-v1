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
	// logoMaxCandidates bounds how many assets one resolve will ask a site
	// for. The chain is fetched serially and the deep-read queue is two
	// workers wide, so a page declaring a thousand icon links would otherwise
	// let one site hold a worker until its deadline. A site that has not shown
	// its mark in the first few declarations is not hiding it in the
	// thousandth, and everything past the bound is reported as dropped rather
	// than silently ignored.
	logoMaxCandidates = 8
)

// Outcomes the resolve records per candidate. They are the quality signal the
// `worker siteread` report prints: WHY the obvious logo was passed over is the
// thing you need when a company's face comes out wrong.
const (
	logoOutcomeChosen   = "chosen"
	logoOutcomeFallback = "wide, kept only as a fallback"
	logoOutcomeSkipped  = "skipped, a squarer candidate was already chosen"
)

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
// og:image first (a small site's og:image usually is its mark), then the
// homescreen icon, then the favicons, then the well-known /favicon.ico. A
// candidate that is square enough is taken immediately; a wide one is
// remembered and only used if nothing squarer turns up, so a site whose
// og:image is a sharing banner still ends up with its real icon.
func resolveOrganizationLogo(ctx context.Context, fetch assetFetcher, seedURL string, declared declaredAssets) (resolvedLogo, []logoAttempt) {
	candidates, dropped := logoCandidates(seedURL, declared)
	attempts := make([]logoAttempt, 0, len(candidates)+1)
	var fallback resolvedLogo
	for _, candidate := range candidates {
		if fallback.PNG != nil && candidate == fallback.SourceURL {
			continue
		}
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
func logoCandidates(seedURL string, declared declaredAssets) (candidates []string, dropped int) {
	ordered := make([]string, 0, len(declared.icons)+2)
	if declared.ogImage != "" {
		ordered = append(ordered, declared.ogImage)
	}
	ordered = append(ordered, iconURLsByRel(declared.icons, webread.RelAppleTouchIcon)...)
	ordered = append(ordered, iconURLsByRel(declared.icons, webread.RelIcon)...)
	if wellKnown, ok := wellKnownFaviconURL(seedURL); ok {
		ordered = append(ordered, wellKnown)
	}

	seen := make(map[string]bool, len(ordered))
	unique := make([]string, 0, len(ordered))
	for _, candidate := range ordered {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		unique = append(unique, candidate)
	}
	if len(unique) > logoMaxCandidates {
		return unique[:logoMaxCandidates], len(unique) - logoMaxCandidates
	}
	return unique, 0
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
// "any" means a scalable source, which says nothing about pixels.
func declaredIconEdge(sizes string) int {
	largest := 0
	for _, token := range strings.Fields(sizes) {
		width, _, found := strings.Cut(token, "x")
		if !found {
			continue
		}
		edge, err := strconv.Atoi(width)
		if err == nil && edge > largest {
			largest = edge
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
// Bytes first, row second: a failure between the two leaves an unreferenced
// object at a key derived from the organization id, which the next resolve
// overwrites. The other order would point a row at bytes that are not there,
// which is the one outcome a user would see.
func (w *siteDeepReadWorker) resolveLogo(ctx context.Context, args SiteDeepReadArgs, claim people.SiteReadClaim, crawl siteCrawl) {
	if w.blob == nil || claim.OrganizationID == nil {
		// No object store to hold the bytes, or an unbound onboarding draft
		// with no organization row to point at one yet.
		return
	}
	orgID := ids.From[ids.OrganizationKind](*claim.OrganizationID)
	// Ask BEFORE resolving anything. The stored object lives at a key derived
	// from the organization id, so writing bytes is what actually replaces a
	// person's own logo — the row guard alone would decline to change the row
	// while the bytes underneath it had already been overwritten, which is the
	// one outcome the precedence rule exists to prevent. Asking first also
	// spares a fetch and a normalize nobody would use.
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

	logo, attempts := resolveOrganizationLogo(ctx, w.fetch, claim.SeedURL, crawl.SeedAssets)
	if logo.PNG == nil {
		w.log.InfoContext(ctx, "site read resolved no logo",
			"read", args.SiteReadID.String(), "seed", claim.SeedURL,
			"candidates", logoAttemptSummary(attempts))
		return
	}

	key := blobstore.WorkspaceKey(ids.From[ids.WorkspaceKind](args.WorkspaceID), organizationLogoKind, orgID.String())
	if err := w.blob.Put(ctx, key, bytes.NewReader(logo.PNG), int64(len(logo.PNG)), imagenorm.ContentType); err != nil {
		w.log.WarnContext(ctx, "storing the resolved logo failed",
			"read", args.SiteReadID.String(), "source", logo.SourceURL, "err", err)
		return
	}
	written, err := w.people.SetOrganizationLogo(ctx, orgID, key, logo.SourceURL)
	if err != nil {
		w.log.WarnContext(ctx, "recording the resolved logo failed",
			"read", args.SiteReadID.String(), "source", logo.SourceURL, "err", err)
		return
	}
	if !written {
		w.log.InfoContext(ctx, "resolved logo left unused: a person's own logo holds the field",
			"read", args.SiteReadID.String(), "source", logo.SourceURL)
		return
	}
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

// organizationLogoKind is the blobstore key's entity discriminator, the peer
// of "attachment" (blobstore.WorkspaceKey). One key per organization: a
// re-resolve overwrites the previous mark rather than accumulating variants,
// so there is never an orphan to collect.
const organizationLogoKind = "organization_logo"

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
