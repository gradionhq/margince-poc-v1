// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// logoFixture encodes a width x height opaque PNG — a stand-in for whatever a
// site publishes as its mark.
func logoFixture(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, color.NRGBA{R: 20, G: 90, B: 160, A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return out.Bytes()
}

// assetSite serves logo candidates and records what was asked for.
type assetSite struct {
	assets  map[string][]byte
	asked   []string
	failing map[string]bool
}

func (s *assetSite) FetchAsset(_ context.Context, rawURL string) ([]byte, string, error) {
	s.asked = append(s.asked, rawURL)
	if s.failing[rawURL] {
		return nil, "", errors.New("asset answered 500")
	}
	body, ok := s.assets[rawURL]
	if !ok {
		return nil, "", errors.New("asset answered 404")
	}
	return body, "image/png", nil
}

const logoSeed = "https://acme.example/"

func TestLogoCandidateOrderPrefersTheDeclarationsMostLikelyToBeTheMark(t *testing.T) {
	got, dropped := logoCandidates(logoSeed, declaredAssets{
		ogImage: "https://acme.example/share.png",
		icons: []webread.IconRef{
			{URL: "https://acme.example/icon-32.png", Rel: webread.RelIcon, Sizes: "32x32"},
			{URL: "https://acme.example/touch-120.png", Rel: webread.RelAppleTouchIcon, Sizes: "120x120"},
			{URL: "https://acme.example/icon-192.png", Rel: webread.RelIcon, Sizes: "192x192"},
			{URL: "https://acme.example/touch-180.png", Rel: webread.RelAppleTouchIcon, Sizes: "180x180"},
		},
	})
	// The declared icons lead: they are the only assets a site publishes
	// saying "this is us at icon size". og:image is whatever the page wants
	// shown when it is shared, so it goes last.
	want := []string{
		"https://acme.example/touch-180.png",
		"https://acme.example/touch-120.png",
		"https://acme.example/icon-192.png",
		"https://acme.example/icon-32.png",
		"https://acme.example/favicon.ico",
		"https://acme.example/share.png",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate order:\n got %v\nwant %v", got, want)
	}
	if dropped != 0 {
		t.Fatalf("six candidates fit under the cap, yet %d were dropped", dropped)
	}
}

func TestLogoCandidatesAreCappedAndTheDropIsReported(t *testing.T) {
	// A page can declare an unbounded number of icons; the chain is fetched
	// serially on a two-worker queue, so it stops — and says that it did.
	icons := make([]webread.IconRef, 0, 50)
	for i := range 50 {
		icons = append(icons, webread.IconRef{
			URL: fmt.Sprintf("https://acme.example/icon-%d.png", i), Rel: webread.RelIcon,
		})
	}
	got, dropped := logoCandidates(logoSeed, declaredAssets{icons: icons})
	if len(got) != logoMaxCandidates {
		t.Fatalf("tried %d candidates, want the cap of %d", len(got), logoMaxCandidates)
	}
	if dropped != 51-logoMaxCandidates {
		t.Fatalf("dropped %d, want %d (50 icons + /favicon.ico, less the cap)", dropped, 51-logoMaxCandidates)
	}

	site := &assetSite{assets: map[string][]byte{}}
	_, attempts := resolveOrganizationLogo(context.Background(), site, logoSeed, declaredAssets{icons: icons})
	if len(site.asked) != logoMaxCandidates {
		t.Fatalf("fetched %d assets, want the cap of %d", len(site.asked), logoMaxCandidates)
	}
	last := attempts[len(attempts)-1]
	if !strings.Contains(last.Outcome, "not tried") {
		t.Fatalf("the truncation must be reported, not silent: %+v", attempts)
	}
}

func TestLogoCandidatesAlwaysEndAtTheWellKnownFaviconWithoutRepeatingIt(t *testing.T) {
	bare, _ := logoCandidates(logoSeed, declaredAssets{})
	if !reflect.DeepEqual(bare, []string{"https://acme.example/favicon.ico"}) {
		t.Fatalf("a page declaring nothing must still try /favicon.ico, got %v", bare)
	}

	declared, _ := logoCandidates(logoSeed, declaredAssets{
		icons: []webread.IconRef{{URL: "https://acme.example/favicon.ico", Rel: webread.RelIcon}},
	})
	if len(declared) != 1 {
		t.Fatalf("a declared /favicon.ico must not be tried twice, got %v", declared)
	}
}

func TestDeclaredIconEdgeReadsTheLargestStatedSize(t *testing.T) {
	cases := map[string]int{
		"":              0,
		"any":           0,
		"32x32":         32,
		"16x16 48x48":   48,
		"180X180":       180,
		"notasize":      0,
		"16x16 garbage": 16,
		// A non-square declaration ranks by its longer edge.
		"32x64": 64,
		"64x32": 64,
	}
	for sizes, want := range cases {
		if got := declaredIconEdge(strings.ToLower(sizes)); got != want {
			t.Errorf("declaredIconEdge(%q) = %d, want %d", sizes, got, want)
		}
	}
}

func TestResolveLogoTakesTheFirstSquareCandidate(t *testing.T) {
	site := &assetSite{assets: map[string][]byte{
		"https://acme.example/touch.png":   logoFixture(t, 180, 180),
		"https://acme.example/favicon.ico": logoFixture(t, 32, 32),
	}}
	logo, attempts := resolveOrganizationLogo(context.Background(), site, logoSeed, declaredAssets{
		icons: []webread.IconRef{{URL: "https://acme.example/touch.png", Rel: webread.RelAppleTouchIcon}},
	})
	if logo.SourceURL != "https://acme.example/touch.png" {
		t.Fatalf("chose %q, want the apple-touch-icon", logo.SourceURL)
	}
	if logo.PNG == nil {
		t.Fatal("the chosen mark carries no normalized bytes")
	}
	if len(attempts) != 1 || attempts[0].Outcome != logoOutcomeChosen {
		t.Fatalf("attempts = %+v, want one chosen candidate", attempts)
	}
	// The chain stops at the first square candidate: /favicon.ico was never asked for.
	if len(site.asked) != 1 {
		t.Fatalf("asked for %v, want only the chosen candidate", site.asked)
	}
}

func TestResolveLogoPassesOverASharingBannerForTheRealIcon(t *testing.T) {
	// The standard og:image is 1200x630 — a banner, not a mark. The icon
	// behind it is what a company should be recognized by.
	site := &assetSite{assets: map[string][]byte{
		"https://acme.example/share.png": logoFixture(t, 1200, 630),
		"https://acme.example/touch.png": logoFixture(t, 180, 180),
	}}
	logo, attempts := resolveOrganizationLogo(context.Background(), site, logoSeed, declaredAssets{
		ogImage: "https://acme.example/share.png",
		icons:   []webread.IconRef{{URL: "https://acme.example/touch.png", Rel: webread.RelAppleTouchIcon}},
	})
	if logo.SourceURL != "https://acme.example/touch.png" {
		t.Fatalf("chose %q, want the square icon over the banner", logo.SourceURL)
	}
	// The icon is first in the chain and square, so it is taken on sight and
	// the banner is never fetched at all — one asset less of egress.
	if len(attempts) != 1 || attempts[0].Outcome != logoOutcomeChosen {
		t.Fatalf("attempts = %+v, want only the icon tried", attempts)
	}
}

// The shape screen catches a WIDE og:image. A square-ish one — a product
// shot, a podcast tile, a near-square hero photo — passes every screen there
// is, so the only thing that keeps it off the account is asking the site for
// its declared icon first. A real import produced several companies wearing a
// stock photo this way.
func TestResolveLogoPrefersADeclaredIconOverASquarishPhoto(t *testing.T) {
	site := &assetSite{assets: map[string][]byte{
		"https://acme.example/hero.jpg":  logoFixture(t, 1200, 1000),
		"https://acme.example/touch.png": logoFixture(t, 180, 180),
	}}
	logo, _ := resolveOrganizationLogo(context.Background(), site, logoSeed, declaredAssets{
		ogImage: "https://acme.example/hero.jpg",
		icons:   []webread.IconRef{{URL: "https://acme.example/touch.png", Rel: webread.RelAppleTouchIcon}},
	})
	if logo.SourceURL != "https://acme.example/touch.png" {
		t.Fatalf("chose %q, want the declared icon over the photo", logo.SourceURL)
	}
}

func TestResolveLogoKeepsAWideMarkWhenNothingSquarerExists(t *testing.T) {
	// A wordmark is a legitimate logo. With no icon to prefer, it beats a
	// monogram — so it is kept rather than dropped.
	site := &assetSite{assets: map[string][]byte{
		"https://acme.example/wordmark.png": logoFixture(t, 400, 200),
	}}
	logo, _ := resolveOrganizationLogo(context.Background(), site, logoSeed, declaredAssets{
		ogImage: "https://acme.example/wordmark.png",
	})
	if logo.SourceURL != "https://acme.example/wordmark.png" {
		t.Fatalf("chose %q, want the wordmark kept as the fallback", logo.SourceURL)
	}
}

func TestResolveLogoRefusesTheCandidatesThatWouldRenderBadly(t *testing.T) {
	cases := []struct {
		name       string
		asset      []byte
		wantReason string
	}{
		{"a 16px favicon is too coarse to read", logoFixture(t, 16, 16), "minimum edge"},
		{"a hero photo is a banner, not a mark", logoFixture(t, 1600, 400), "banner shape"},
		{"an HTML error page is not an image", []byte("<!doctype html><title>404</title>"), "not a decodable image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site := &assetSite{assets: map[string][]byte{"https://acme.example/x.png": tc.asset}}
			logo, attempts := resolveOrganizationLogo(context.Background(), site, logoSeed, declaredAssets{
				ogImage: "https://acme.example/x.png",
			})
			if logo.PNG != nil {
				t.Fatalf("%s but it was stored anyway", tc.name)
			}
			// The reason can sit behind the well-known /favicon.ico attempt,
			// which this fixture site does not serve — what matters is that
			// the refusal is reported in words, not which row carries it.
			named := false
			for _, attempt := range attempts {
				if strings.Contains(attempt.Outcome, tc.wantReason) {
					named = true
				}
			}
			if !named {
				t.Fatalf("attempts = %+v, want a reason naming %q", attempts, tc.wantReason)
			}
		})
	}
}

func TestResolveLogoSurvivesASiteThatServesNoAssetAtAll(t *testing.T) {
	site := &assetSite{
		assets:  map[string][]byte{},
		failing: map[string]bool{"https://acme.example/favicon.ico": true},
	}
	logo, attempts := resolveOrganizationLogo(context.Background(), site, logoSeed, declaredAssets{})
	if logo.PNG != nil {
		t.Fatal("nothing resolved, yet bytes came back")
	}
	if len(attempts) != 1 || !strings.Contains(attempts[0].Outcome, "could not be fetched") {
		t.Fatalf("attempts = %+v, want the fetch failure recorded", attempts)
	}
	if summary := logoAttemptSummary(attempts); !strings.Contains(summary, "favicon.ico") {
		t.Fatalf("the summary must name the candidate it tried: %q", summary)
	}
	if summary := logoAttemptSummary(nil); summary == "" {
		t.Fatal("a resolve with no candidates must still say so")
	}
}

func TestResolveLogoNormalizesToOneSquareUnderTheStoredCeiling(t *testing.T) {
	site := &assetSite{assets: map[string][]byte{
		"https://acme.example/touch.png": logoFixture(t, 1024, 1024),
	}}
	logo, _ := resolveOrganizationLogo(context.Background(), site, logoSeed, declaredAssets{
		icons: []webread.IconRef{{URL: "https://acme.example/touch.png", Rel: webread.RelAppleTouchIcon}},
	})
	stored, err := png.Decode(bytes.NewReader(logo.PNG))
	if err != nil {
		t.Fatalf("the stored bytes are not a PNG: %v", err)
	}
	bounds := stored.Bounds()
	if bounds.Dx() != logoMaxEdge || bounds.Dy() != logoMaxEdge {
		t.Fatalf("stored %v, want a %dx%d square", bounds, logoMaxEdge, logoMaxEdge)
	}
	if logo.SourceWidth != 1024 || logo.SourceHeight != 1024 {
		t.Fatalf("the source size must be reported as fetched, got %dx%d", logo.SourceWidth, logo.SourceHeight)
	}
}

// deadlineBlob records deletes and reports whether the context it was handed
// was already done — the condition that decides whether a reclaim happens at
// all when the lane ran out of time.
type deadlineBlob struct {
	blobstore.Store
	deletedLive []string
	deletedDead []string
}

func (b *deadlineBlob) Delete(ctx context.Context, key string) error {
	if ctx.Err() != nil {
		b.deletedDead = append(b.deletedDead, key)
		return ctx.Err()
	}
	b.deletedLive = append(b.deletedLive, key)
	return nil
}

func TestReclaimSurvivesTheDeadlineThatCausedIt(t *testing.T) {
	// The likeliest reason to be reclaiming is that the work ran out of time,
	// so the delete must not inherit the context that just expired. An object
	// at a per-attempt key that no row ever named is unreachable by anything
	// that could collect it later — skipping this delete strands it forever.
	blob := &deadlineBlob{Store: blobstore.NewMemory()}
	w := &siteDeepReadWorker{blob: blob, log: slog.New(slog.DiscardHandler)}

	expired, cancel := context.WithCancel(context.Background())
	cancel()

	key := "ws/organization_logo/org/attempt"
	w.reclaimLogoObject(expired, ids.NewV7(), &key)

	if len(blob.deletedDead) != 0 {
		t.Fatalf("the reclaim ran on the expired context: %v", blob.deletedDead)
	}
	if len(blob.deletedLive) != 1 || blob.deletedLive[0] != key {
		t.Fatalf("deleted %v, want the unreferenced object %q", blob.deletedLive, key)
	}

	// Nothing to reclaim is not a delete.
	w.reclaimLogoObject(context.Background(), ids.NewV7(), nil)
	empty := ""
	w.reclaimLogoObject(context.Background(), ids.NewV7(), &empty)
	if len(blob.deletedLive) != 1 {
		t.Fatalf("a nil or empty key must delete nothing, got %v", blob.deletedLive)
	}
}

// The cap bounds one read's asset egress, so it has to bite somewhere — but
// the two site-level sources are exactly what answers when the declarations
// are stale, and a page with a cap's worth of dead touch-icon tags would
// otherwise spend the whole budget on them.
func TestLogoCandidatesSpendTheCapOnDeclarationsNotOnTheFallbacks(t *testing.T) {
	icons := make([]webread.IconRef, 0, logoMaxCandidates*2)
	for i := range logoMaxCandidates * 2 {
		icons = append(icons, webread.IconRef{
			URL: fmt.Sprintf("https://acme.example/stale-%d.png", i), Rel: webread.RelAppleTouchIcon,
		})
	}
	got, dropped := logoCandidates(logoSeed, declaredAssets{
		icons: icons, ogImage: "https://acme.example/share.png",
	})
	if len(got) != logoMaxCandidates {
		t.Fatalf("tried %d candidates, want the cap of %d", len(got), logoMaxCandidates)
	}
	if dropped == 0 {
		t.Fatal("declarations were dropped to make room; the drop must be reported")
	}
	for _, want := range []string{"https://acme.example/favicon.ico", "https://acme.example/share.png"} {
		if !slices.Contains(got, want) {
			t.Errorf("candidates %v dropped %s — the fallbacks must survive the cap", got, want)
		}
	}
}

// A declared /favicon.ico that the cap CUT must not take the site-level
// fallback with it: the fallback exists for exactly the page whose
// declarations are stale, and a hundred dead tags would otherwise consume it
// in a candidate that is never fetched.
func TestLogoCandidatesKeepTheFallbackACutDeclarationAlsoNamed(t *testing.T) {
	icons := make([]webread.IconRef, 0, logoMaxCandidates*2)
	for i := range logoMaxCandidates * 2 {
		icons = append(icons, webread.IconRef{
			URL: fmt.Sprintf("https://acme.example/stale-%d.png", i), Rel: webread.RelAppleTouchIcon,
		})
	}
	// Declared last, so the cap cuts it.
	icons = append(icons, webread.IconRef{
		URL: "https://acme.example/favicon.ico", Rel: webread.RelIcon,
	})
	got, _ := logoCandidates(logoSeed, declaredAssets{icons: icons})
	if !slices.Contains(got, "https://acme.example/favicon.ico") {
		t.Errorf("candidates %v lost /favicon.ico to a declaration the cap dropped", got)
	}
}

// A fallback one of the surviving declarations already named needs no slot of
// its own, so the reserve it was holding goes back to the next declaration
// rather than shortening the chain.
func TestLogoCandidatesSpendTheWholeBudgetWhenAFallbackIsAlreadyDeclared(t *testing.T) {
	icons := []webread.IconRef{{URL: "https://acme.example/favicon.ico", Rel: webread.RelIcon}}
	for i := range logoMaxCandidates * 2 {
		icons = append(icons, webread.IconRef{
			URL: fmt.Sprintf("https://acme.example/icon-%d.png", i), Rel: webread.RelIcon,
		})
	}
	got, _ := logoCandidates(logoSeed, declaredAssets{
		icons: icons, ogImage: "https://acme.example/share.png",
	})
	if len(got) != logoMaxCandidates {
		t.Fatalf("tried %d candidates, want the whole budget of %d: %v", len(got), logoMaxCandidates, got)
	}
	if len(slices.Compact(slices.Sorted(slices.Values(got)))) != len(got) {
		t.Errorf("candidates repeat: %v", got)
	}
}
