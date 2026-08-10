// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package apps

// The availability state machine, and the provider surface over it.
//
// Every document here is served by a real HTTP server through the real fetcher
// and the real admission check — the only stand-in is the web tier, which is the
// one boundary a unit test is entitled to replace.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/buildinfo"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// documentFor is one view's document, titled the way the catalog expects so an
// unrelated title mismatch never explains a failure here.
func documentFor(uri string) string {
	title := "Morning brief"
	if uri == RelationshipMapURI {
		title = "Who knows this contact"
	}
	return strings.Replace(cleanDocument, "<title>Morning brief</title>", "<title>"+title+"</title>", 1)
}

// webTier stands in for the origin. Each view's answer is settable, so a test
// can change what a refresh finds without racing the server it is talking to.
type webTier struct {
	mu      sync.Mutex
	answers map[string]func(http.ResponseWriter)
	served  atomic.Int64
}

func newWebTier(t *testing.T) (*webTier, *Provider) {
	t.Helper()
	tier := &webTier{answers: map[string]func(http.ResponseWriter){}}
	for _, v := range catalog {
		tier.answer(v.uri, ok(documentFor(v.uri)))
	}
	base, _ := serving(t, func(w http.ResponseWriter, r *http.Request) {
		tier.served.Add(1)
		tier.mu.Lock()
		answer, known := tier.answers[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/mcp-apps/"), ".html")]
		tier.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		answer(w)
	})
	return tier, NewProvider(NewFetcher(base), quietLogger())
}

func (tier *webTier) answer(uri string, with func(http.ResponseWriter)) {
	tier.mu.Lock()
	defer tier.mu.Unlock()
	// Keyed by the file name the fetcher will ask for, derived the same way the
	// fetcher derives it.
	tier.answers[strings.TrimSuffix(strings.TrimPrefix(uri, "ui://margince/"), ".html")] = with
}

func ok(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}
}

func broken(status int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(status) }
}

func TestPrimeHoldsEveryViewTheOriginServes(t *testing.T) {
	_, p := newWebTier(t)
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	if p.heldCount() != len(catalog) {
		t.Fatalf("primed %d of %d views", p.heldCount(), len(catalog))
	}
	for _, v := range catalog {
		if !p.Holds(v.uri) {
			t.Errorf("the view %s was served and is not held", v.uri)
		}
	}
}

func TestAViewThatFailsToFetchIsSimplyNotHeld(t *testing.T) {
	// One view missing must not take the other down. Partial availability is the
	// case that matters: the tool whose view is held keeps its panel.
	tier, p := newWebTier(t)
	tier.answer(RelationshipMapURI, broken(http.StatusInternalServerError))
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming with one view broken returned an error; only an operator-fixable condition should: %v", err)
	}
	if !p.Holds(AccountBriefURI) {
		t.Error("the view that WAS served is not held")
	}
	if p.Holds(RelationshipMapURI) {
		t.Error("a view the origin refused is being served")
	}
}

func TestARefusedDocumentIsNeverHeld(t *testing.T) {
	tier, p := newWebTier(t)
	tier.answer(AccountBriefURI, ok(documentFor(AccountBriefURI)+`<link rel="stylesheet" href="/a.css">`))
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	if p.Holds(AccountBriefURI) {
		t.Fatal("a document the admission check refused is being served")
	}
	if p.admissionFailures.Load() == 0 {
		t.Error("a refused document did not raise the admission-failure counter")
	}
}

func TestAFailedRefreshKeepsTheLastKnownGoodDocument(t *testing.T) {
	// Dropping a working view because a rolling deploy served one bad response
	// is exactly the mistake this design avoids.
	tier, p := newWebTier(t)
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	before, _ := p.served(AccountBriefURI)
	tier.answer(AccountBriefURI, broken(http.StatusBadGateway))
	p.Refresh(t.Context())
	after, holding := p.served(AccountBriefURI)
	if !holding {
		t.Fatal("one bad response during a refresh took a working view down")
	}
	if after != before {
		t.Error("a failed refresh replaced the last known-good document")
	}
}

func TestARefusedDocumentNeverReplacesAGoodOne(t *testing.T) {
	tier, p := newWebTier(t)
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	before, _ := p.served(AccountBriefURI)
	tier.answer(AccountBriefURI, ok(documentFor(AccountBriefURI)+`<script>fetch("/v1/people")</script>`))
	p.Refresh(t.Context())
	after, holding := p.served(AccountBriefURI)
	if !holding || after != before {
		t.Fatal("a document that would be refused replaced the last known-good copy")
	}
}

func TestARefreshPublishesADocumentThatChanged(t *testing.T) {
	// The other direction, because "keep the last known good" is only correct if
	// a GOOD new document still gets through.
	tier, p := newWebTier(t)
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	updated := strings.Replace(documentFor(AccountBriefURI), "border:1px", "border:2px", 1)
	tier.answer(AccountBriefURI, ok(updated))
	p.Refresh(t.Context())
	if got, _ := p.served(AccountBriefURI); got != updated {
		t.Fatal("a refresh did not publish the new document the origin served")
	}
}

func TestARefreshNeverAddsAViewPrimeDidNotAdmit(t *testing.T) {
	// The advertised set is FROZEN after Prime, because this server declares
	// resources.listChanged:false — a view that appeared later would be invisible
	// to any host that listed at connect time, so a silent recovery would be a
	// promise the transport cannot keep.
	tier, p := newWebTier(t)
	tier.answer(RelationshipMapURI, broken(http.StatusInternalServerError))
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	tier.answer(RelationshipMapURI, ok(documentFor(RelationshipMapURI)))
	p.Refresh(t.Context())
	if p.Holds(RelationshipMapURI) {
		t.Fatal("a refresh advertised a view Prime did not admit; no host that already listed would ever be told")
	}
}

func TestTheHeldSnapshotIsReplacedAtomically(t *testing.T) {
	// Run under -race. A reader must see either the old set or the new one and
	// never a half-updated map.
	tier, p := newWebTier(t)
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if got := p.Resources(context.Background()); len(got) != len(catalog) {
						t.Errorf("a concurrent read saw %d views, want %d", len(got), len(catalog))
						return
					}
					_, _ = p.served(AccountBriefURI)
				}
			}
		}()
	}
	for i := range 20 {
		tier.answer(AccountBriefURI, ok(strings.Replace(documentFor(AccountBriefURI),
			"border:1px", "border:"+string(rune('1'+i%8))+"px", 1)))
		p.Refresh(t.Context())
	}
	close(stop)
	readers.Wait()
}

func TestPrimeRefusesADeploymentWithNoOriginRatherThanPollingNothing(t *testing.T) {
	// The one condition Prime reports: an operator has to fix it, where a view
	// that did not answer is a state this server serves correctly.
	p := NewProvider(NewFetcher(nil), quietLogger())
	if err := p.Prime(t.Context()); !errors.Is(err, ErrNoViewsOrigin) {
		t.Fatalf("Prime with no origin answered %v, want %v", err, ErrNoViewsOrigin)
	}
}

func TestAViewIsNotAdvertisedBeforeItIsPrimed(t *testing.T) {
	// A provider that has not primed serves nothing rather than everything: the
	// window between construction and Prime must not advertise a document no
	// host can read.
	_, p := newWebTier(t)
	if got := p.Resources(context.Background()); len(got) != 0 {
		t.Fatalf("an unprimed provider advertised %d views", len(got))
	}
	if _, err := p.ReadResource(context.Background(), AccountBriefURI); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an unprimed provider answered %v for a view, want the not-found sentinel", err)
	}
}

// A URI this provider does not serve answers the DECLARED not-found sentinel, so
// the dispatcher's existence-hiding applies to a view exactly as to any other
// document. An error of another kind would surface as a 500 and tell a caller
// that something is there.
func TestAnUnknownViewAnswersTheNotFoundSentinel(t *testing.T) {
	_, p := newWebTier(t)
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	_, err := p.ReadResource(context.Background(), "ui://margince/not-a-view.html")
	if err == nil {
		t.Fatal("a URI this provider does not serve was answered anyway")
	}
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("an unserved view answered %v, want the declared not-found sentinel", err)
	}
}

// The policy travels WITH the bytes, from the same function the catalogue entry
// took it from — so a host that read a document without listing first sandboxes
// it under exactly the rules it would have been told. With two providers on one
// URI, a policy read from the CATALOGUE would label the wrong document.
func TestTheRealProviderSendsItsPolicyWithTheDocument(t *testing.T) {
	_, p := newWebTier(t)
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	contents, err := p.ReadResource(context.Background(), AccountBriefURI)
	if err != nil {
		t.Fatalf("reading a held view: %v", err)
	}
	if contents.UI == nil {
		t.Fatal("a view was read with no sandbox policy attached to the bytes")
	}
	if !contents.UI.CSP.Empty() || contents.UI.Permissions != (mcp.ResourcePermissions{}) {
		t.Errorf("the read policy widens the sandbox: %+v", contents.UI)
	}
}

// A view has to be served under the profile a host dispatches on, and it has to
// be a document a parser will read in standards mode. Both were asserted over
// the assembled bytes before the build moved; they are asserted over the HELD
// bytes now, which is the same claim about the thing actually served.
func TestEveryHeldViewIsAWellFormedAppDocument(t *testing.T) {
	_, p := newWebTier(t)
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	published := p.Resources(context.Background())
	if len(published) == 0 {
		t.Fatal("the provider published no view, so this sweep proved nothing")
	}
	for _, r := range published {
		if !strings.HasPrefix(r.URI, mcp.AppURIScheme) {
			t.Errorf("the view %s is published outside %s, so no host will treat it as one", r.URI, mcp.AppURIScheme)
		}
		contents, err := p.ReadResource(context.Background(), r.URI)
		if err != nil {
			t.Fatalf("reading %s: %v", r.URI, err)
		}
		if contents.MIMEType != mcp.AppMIMEType {
			t.Errorf("the view %s is read as %q, want %q", r.URI, contents.MIMEType, mcp.AppMIMEType)
		}
		if !strings.HasPrefix(contents.Text, "<!doctype html>") {
			t.Errorf("the view %s does not begin with a doctype, which puts the parser in quirks mode", r.URI)
		}
		if !strings.Contains(contents.Text, "<title>"+r.Title+"</title>") {
			t.Errorf("the view %s does not carry its catalogue title %q", r.URI, r.Title)
		}
	}
}

func TestATitleMismatchIsReportedAndTheViewStillServes(t *testing.T) {
	tier, p := newWebTier(t)
	tier.answer(AccountBriefURI, ok(strings.Replace(documentFor(AccountBriefURI),
		"<title>Morning brief</title>", "<title>Mornning brief</title>", 1)))
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	if !p.Holds(AccountBriefURI) {
		t.Fatal("a title mismatch took the view down; it is diagnostic, not an integrity check")
	}
	if p.titleMismatches.Load() != 1 {
		t.Errorf("the title mismatch counter is %d, want 1", p.titleMismatches.Load())
	}
}

func TestTheFailureLineIsRateLimitedButTheCounterIsNot(t *testing.T) {
	// A view missing for a week must still be findable in the log, and must not
	// be the only thing in it. The counter is what a dashboard reads; the line
	// is what a human reads.
	tier, p := newWebTier(t)
	var lines atomic.Int64
	p.log = slog.New(slog.NewTextHandler(countingWriter{&lines}, &slog.HandlerOptions{Level: slog.LevelError}))
	frozen := time.Now()
	p.now = func() time.Time { return frozen }
	// Primed HEALTHY first and broken after: an unheld view is never re-read at
	// all (the advertised set is frozen), so the repeated-failure path only
	// exists for a view that was working and stopped.
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	tier.answer(AccountBriefURI, broken(http.StatusBadGateway))
	for range 5 {
		p.Refresh(t.Context())
	}
	if got := lines.Load(); got != 1 {
		t.Errorf("five failures of one view logged %d error line(s), want 1 inside the quiet window", got)
	}
	// Past the window, the same failure is reported again rather than going
	// permanently silent.
	p.now = func() time.Time { return frozen.Add(logInterval + time.Second) }
	p.Refresh(t.Context())
	if got := lines.Load(); got != 2 {
		t.Errorf("after the quiet window elapsed the failure logged %d line(s), want 2", got)
	}
}

type countingWriter struct{ n *atomic.Int64 }

func (c countingWriter) Write(p []byte) (int, error) {
	c.n.Add(1)
	return len(p), nil
}

func TestTheMetricsSectionNamesEachViewSeparately(t *testing.T) {
	// A gauge per URI rather than a total: the failure that matters is one view
	// missing while the other is fine, and "1 of 2" is a number nobody can act on.
	tier, p := newWebTier(t)
	tier.answer(RelationshipMapURI, broken(http.StatusInternalServerError))
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	var out strings.Builder
	p.WriteMetrics(&out)
	body := out.String()
	for want, why := range map[string]string{
		`margince_mcp_app_view_held{uri="` + AccountBriefURI + `"} 1`:    "the held view reads as held",
		`margince_mcp_app_view_held{uri="` + RelationshipMapURI + `"} 0`: "the missing view reads as missing",
		"margince_mcp_app_fetch_failures_total 1":                        "the fetch failure is counted",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the metrics section does not say %s (%q missing)\n---\n%s", why, want, body)
		}
	}
}

// stampedBrief is the account brief's document carrying a build revision, the
// way the inliner writes one.
func stampedBrief(revision string) string {
	return strings.Replace(documentFor(AccountBriefURI), "-->",
		"-->\n<!-- margince-build-revision: "+revision+" -->", 1)
}

func TestAStampMismatchIsReportedAndTheViewStillServes(t *testing.T) {
	// A rolling deploy puts the api and the web tier on different revisions for
	// the length of the rollout. Refusing there would be a self-inflicted outage
	// in exchange for a signal that is diagnostic, not an integrity check.
	restore := buildinfo.Revision
	t.Cleanup(func() { buildinfo.Revision = restore })
	buildinfo.Revision = "aaaaaaaa"

	tier, p := newWebTier(t)
	tier.answer(AccountBriefURI, ok(stampedBrief("bbbbbbbb")))
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	if !p.Holds(AccountBriefURI) {
		t.Fatal("a build-revision mismatch took the view down")
	}
	var out strings.Builder
	p.WriteMetrics(&out)
	body := out.String()
	// Per URI, because a rollout replaces one document before the other: a
	// single process-wide reading would be whichever view was read last.
	if !strings.Contains(body, `margince_mcp_app_build_skew{uri="`+AccountBriefURI+`"} 1`) {
		t.Errorf("the metrics section does not report the skewed view:\n%s", body)
	}
	if !strings.Contains(body, `margince_mcp_app_build_skew{uri="`+RelationshipMapURI+`"} 0`) {
		t.Errorf("the unstamped view is reported as skewed:\n%s", body)
	}
}

func TestAMatchingStampClearsTheSkewGauge(t *testing.T) {
	// A gauge, not a counter: the rollout ends and the reading has to follow it
	// down, or an alert on it never clears.
	restore := buildinfo.Revision
	t.Cleanup(func() { buildinfo.Revision = restore })
	buildinfo.Revision = "aaaaaaaa"

	tier, p := newWebTier(t)
	tier.answer(AccountBriefURI, ok(stampedBrief("bbbbbbbb")))
	if err := p.Prime(t.Context()); err != nil {
		t.Fatalf("priming: %v", err)
	}
	tier.answer(AccountBriefURI, ok(stampedBrief("aaaaaaaa")))
	p.Refresh(t.Context())
	var out strings.Builder
	p.WriteMetrics(&out)
	if !strings.Contains(out.String(), `margince_mcp_app_build_skew{uri="`+AccountBriefURI+`"} 0`) {
		t.Fatalf("the skew gauge stayed raised after the rollout finished:\n%s", out.String())
	}
}

func TestAnUnknownStampOnEitherSideSkipsTheComparison(t *testing.T) {
	// A developer's binary is built from a dirty worktree that no commit SHA
	// describes, so equality would mean nothing and inequality would alarm on
	// every local run.
	for _, tc := range []struct{ name, api, document string }{
		{"neither side stamped", "", ""},
		{"only the api stamped", "aaaaaaaa", ""},
		{"only the document stamped", "", "bbbbbbbb"},
		{"the api is a local build", buildinfo.Unknown, "bbbbbbbb"},
		{"the document is a local build", "aaaaaaaa", buildinfo.Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := buildinfo.Revision
			t.Cleanup(func() { buildinfo.Revision = restore })
			buildinfo.Revision = tc.api

			tier, p := newWebTier(t)
			body := documentFor(AccountBriefURI)
			if tc.document != "" {
				body = stampedBrief(tc.document)
			}
			tier.answer(AccountBriefURI, ok(body))
			if err := p.Prime(t.Context()); err != nil {
				t.Fatalf("priming: %v", err)
			}
			if !p.Holds(AccountBriefURI) {
				t.Fatal("an unknown revision took the view down")
			}
			var out strings.Builder
			p.WriteMetrics(&out)
			if !strings.Contains(out.String(), `margince_mcp_app_build_skew{uri="`+AccountBriefURI+`"} 0`) {
				t.Errorf("an unknown revision on one side raised the skew gauge:\n%s", out.String())
			}
		})
	}
}
