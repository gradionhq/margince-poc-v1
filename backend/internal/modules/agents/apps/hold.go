// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package apps

// What this server is actually serving, right now, per view.
//
// AVAILABILITY IS PER-URI AND SHARED BY BOTH SURFACES. One immutable snapshot is
// read by resources/list, by resources/read AND by the `_meta.ui` a tool carries
// — because they are one promise. The protocol makes it a MUST that a `ui://`
// URI a tool names exists on the server, and a host is entitled to prefetch a
// view before the tool is ever called, so a tool naming an unheld document is
// not a render that fails: it is a host fetching a 404 and a panel that silently
// never appears. Partial availability works: one view missing suppresses ONE
// tool's `_meta.ui`, and that tool keeps answering in text.
//
// THE ADVERTISED SET FREEZES AFTER Prime, and that is a consequence rather than
// a preference. This server declares `resources.listChanged: false` because
// notifications/*/list_changed travels on a stream this transport does not open,
// so a view that appeared after a background retry would be invisible to any
// host that listed at connect time — a retry loop could not deliver what it
// promised. One bounded attempt at startup, then the set is fixed for the
// process lifetime. An operator restores a missing view by restarting the api,
// which is honest and observable, where a silent never-arriving retry is not.
//
// REFRESH REPLACES ONLY WHAT IS ALREADY HELD. A failed re-fetch, a non-200 or a
// failed re-validation keeps the last known-good copy and alarms. Dropping a
// working view because a rolling deploy served one bad response is the mistake
// this design exists to avoid.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/buildinfo"
)

const (
	// primeDeadline bounds the whole startup fetch. Boot does not block past it:
	// an api that will not start because a web tier is slow is a worse failure
	// than an api that starts with a view missing and says so.
	// It is a boot cost when the origin is DOWN, so it is bounded tightly: the
	// retry below exists for a tier that is still starting, not one that is
	// absent, and `make dev` now starts the web tier first.
	primeDeadline = 10 * time.Second
	// primeRetryInterval is how often the startup fetch re-attempts a view whose
	// origin has not answered YET. Only a RETRYABLE failure is re-asked — see
	// ErrPermanent, because retrying a refusal that cannot change would delay
	// every boot by the whole deadline and inflate the counters an alert reads.
	//
	// Retrying inside the deadline is not the background recovery the design
	// rules out — the advertised set is still frozen the moment Prime returns,
	// and nothing is ever added after it. What it answers is a different
	// problem: at boot the web tier is routinely not listening yet. `make dev`
	// starts the api before vite, and a rolling deploy can bring an api up ahead
	// of the tier it reads from. A single attempt against a cold dependency
	// meant both views were permanently unadvertised in every dev stack —
	// measured, not predicted.
	primeRetryInterval = time.Second
	// refreshInterval is how often a held document is re-read. The web tier and
	// the api deploy separately, so a view replaced by a deploy reaches the api
	// within this window rather than at the next restart.
	refreshInterval = 5 * time.Minute
	// logInterval rate-limits the per-view failure line. A view that has been
	// missing for a week must still be findable in the log, and must not be the
	// only thing in it.
	logInterval = 15 * time.Minute
)

// Provider publishes the views over the resource seam, serving only what it is
// currently holding.
type Provider struct {
	fetcher *Fetcher
	log     *slog.Logger
	now     func() time.Time
	// retryEvery paces the startup re-attempt. Injected rather than read from
	// the constant so a spec can drive the loop without sleeping: a real clock
	// in a unit test is flake waiting to happen, and the rest of this file
	// injects `now` for the same reason.
	retryEvery time.Duration

	// held is the whole availability answer: URI → document. It is REPLACED, never
	// mutated — resources/list, resources/read and the tool listing read it
	// concurrently, and a half-updated map would serve one view's bytes under
	// another's descriptor.
	held atomic.Pointer[map[string]string]

	fetchFailures     atomic.Uint64
	admissionFailures atomic.Uint64
	titleMismatches   atomic.Int64

	// quiet holds the last time each view's failure was logged. Guarded by a
	// mutex rather than made atomic: it is touched only on a failure path, and
	// the refresh loop is the only writer.
	quietMu sync.Mutex
	quiet   map[string]time.Time
}

// NewProvider builds the provider around one fetcher.
//
// It is a CONSTRUCTED value, not a package-level var: nothing about the views
// happens at package init any more, and a process role that serves no views
// simply never calls this.
func NewProvider(fetcher *Fetcher, log *slog.Logger) *Provider {
	p := &Provider{
		fetcher: fetcher, log: log, now: time.Now,
		retryEvery: primeRetryInterval, quiet: map[string]time.Time{},
	}
	empty := map[string]string{}
	p.held.Store(&empty)
	return p
}

// Holds reports whether this server is currently serving that view. It is the
// question the tool listing asks, and the reason it is a predicate rather than a
// map handed over: a tools/list has no business holding view HTML.
func (p *Provider) Holds(uri string) bool {
	_, serving := (*p.held.Load())[uri]
	return serving
}

// served answers one held document.
func (p *Provider) served(uri string) (string, bool) {
	doc, ok := (*p.held.Load())[uri]
	return doc, ok
}

// heldCount is how many of the catalog's views are being served.
func (p *Provider) heldCount() int { return len(*p.held.Load()) }

// Prime makes the one bounded startup attempt and publishes what it admitted.
//
// It returns an error ONLY for a condition an operator must fix — a missing or
// malformed origin — and never for a view that simply did not answer. A view
// that failed is not held, which is a state this server serves correctly; a
// misconfigured origin is a state it cannot.
func (p *Provider) Prime(ctx context.Context) error {
	// Checked on the ORIGIN, not on the pointer: NewFetcher(nil) is a perfectly
	// real fetcher that can answer nothing, and without this every view would be
	// recorded as "did not answer" and the misconfiguration would never surface.
	if !p.fetcher.configured() {
		return ErrNoViewsOrigin
	}
	ctx, cancel := context.WithTimeout(ctx, primeDeadline)
	defer cancel()
	admitted, refused := p.primeUntilDeadline(ctx)
	p.held.Store(&admitted)
	// Reported once, at the END, rather than per attempt: an origin that is
	// merely still starting would otherwise log an alarming line describing a
	// state the next second resolves.
	for uri, err := range refused {
		p.report(uri, err)
	}
	p.log.Info("mcp apps: view documents primed",
		"held", len(admitted), "catalog", len(catalog), "origin", p.originForLog())
	if len(admitted) < len(catalog) {
		// Named at Warn as well as per-view above, because "some views are
		// missing" is the operator-visible fact and the per-view lines are the
		// detail. A view that silently never appears is the failure this design
		// must not have.
		p.log.Warn("mcp apps: some views are not served; the advertised set is fixed until this api restarts",
			"held", len(admitted), "catalog", len(catalog))
	}
	return nil
}

// primeUntilDeadline reads every catalog view, re-attempting the ones that have
// not answered yet until they all have or the deadline passes. It answers what
// was admitted, and the LAST failure for each view that was not.
func (p *Provider) primeUntilDeadline(ctx context.Context) (map[string]string, map[string]error) {
	admitted := make(map[string]string, len(catalog))
	refused := map[string]error{}
	permanent := map[string]bool{}
	for {
		for _, v := range catalog {
			if _, have := admitted[v.uri]; have {
				continue
			}
			if permanent[v.uri] {
				continue
			}
			doc, err := p.read(ctx, v)
			if err != nil {
				refused[v.uri] = err
				if errors.Is(err, ErrPermanent) {
					permanent[v.uri] = true
				}
				continue
			}
			delete(refused, v.uri)
			admitted[v.uri] = doc
		}
		if len(admitted)+len(permanent) == len(catalog) {
			// Nothing left that another attempt could change.
			return admitted, refused
		}
		select {
		case <-ctx.Done():
			return admitted, refused
		case <-time.After(p.retryEvery):
		}
	}
}

// Refresh re-reads every view that is ALREADY held and republishes in one store.
//
// It never adds a URI Prime did not admit — see the file header on why the
// advertised set is frozen — and it never removes one: a failure keeps the last
// known-good document and alarms.
func (p *Provider) Refresh(ctx context.Context) {
	current := *p.held.Load()
	if len(current) == 0 {
		return
	}
	next := maps.Clone(current)
	for _, v := range catalog {
		if _, holding := current[v.uri]; !holding {
			continue
		}
		doc, err := p.read(ctx, v)
		if err != nil {
			p.reportRefresh(ctx, v.uri, err)
			continue
		}
		next[v.uri] = doc
	}
	// ONE store of a whole new map. Prime and this loop are the only writers and
	// the api's boot sequences them — Prime returns before RunRefresh starts — so
	// this needs no compare-and-swap; what it needs is that a reader sees either
	// the old set or the new one and never a mixture.
	p.held.Store(&next)
}

// RunRefresh re-reads the held documents until the context is cancelled. It is
// owned by the api's composition and runs in no other role: two processes
// refreshing one snapshot would be two answers to a question with one.
func (p *Provider) RunRefresh(ctx context.Context) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Refresh(ctx)
		}
	}
}

// read fetches one view and admits it, counting each failure under the class it
// belongs to: a fetch that never arrived and a document that arrived and was
// refused are different operator problems.
func (p *Provider) read(ctx context.Context, v view) (string, error) {
	doc, err := p.fetcher.Fetch(ctx, v.uri)
	if err != nil {
		p.fetchFailures.Add(1)
		return "", err
	}
	findings, titleMismatch := admit(doc, v.title)
	if len(findings) > 0 {
		p.admissionFailures.Add(1)
		return "", fmt.Errorf("%w: the document reaches beyond what a view may: %v", ErrPermanent, findings)
	}
	// REPORTED, never a refusal: two hand-spellings across a language boundary,
	// and a copy edit must not take a view down.
	if titleMismatch {
		p.titleMismatches.Add(1)
		p.log.Warn("mcp apps: the served document's title differs from the catalog's",
			"uri", v.uri, "catalog_title", v.title)
	}
	p.noteSkew(v.uri, doc)
	return doc, nil
}

// revisionPattern reads the build stamp the inliner wrote into the document.
var revisionPattern = regexp.MustCompile(`<!--\s*margince-build-revision:\s*(\S+)\s*-->`)

// noteSkew reports whether this document came from a different build than this
// api — and REPORTS is the whole of it.
//
// It does not refuse: the api and the web tier deploy separately, so a rolling
// deploy would otherwise take the views down for the length of the rollout, which
// is a self-inflicted outage in exchange for a signal. It is diagnostic
// metadata, not an integrity signature; transport integrity rests on HTTPS and
// on control of the origin.
//
// An unknown revision on EITHER side disables the comparison. A developer's
// binary is built from a dirty worktree that no commit SHA describes, so
// equality there would mean nothing and inequality would alarm on every local
// run.
func (p *Provider) noteSkew(uri, doc string) {
	theirs := documentRevision(doc)
	if !buildinfo.SkewBetween(buildinfo.Revision, theirs) {
		return
	}
	p.log.Warn("mcp apps: the served view was built from a different revision than this api; "+
		"expected during a rollout, worth alerting on once one has finished",
		"uri", uri, "api_revision", buildinfo.Revision, "document_revision", theirs)
}

// documentRevision reads the build stamp out of a document, or the empty string
// for one that carries none.
func documentRevision(doc string) string {
	found := revisionPattern.FindStringSubmatch(doc)
	if found == nil {
		return ""
	}
	return found[1]
}

// reportRefresh logs a REFRESH failure, which is a different fact from a view
// that is not served: the last known-good document is still being handed out.
// Saying "not being served" here would send an operator looking for an outage
// that is not happening.
//
// A cancelled context is not reported at all — that is this process shutting
// down, and an error line on the way out reads as a fault.
func (p *Provider) reportRefresh(ctx context.Context, uri string, err error) {
	if ctx.Err() != nil {
		return
	}
	p.logQuietly(uri, func() {
		p.log.Warn("mcp apps: a view could not be re-read; the last known-good document is still being served",
			"uri", uri, "err", err)
	})
}

// report logs one view's failure at most once per logInterval.
func (p *Provider) report(uri string, err error) {
	p.logQuietly(uri, func() {
		p.log.Error("mcp apps: a view document is not being served", "uri", uri, "err", err)
	})
}

// logQuietly runs one line at most once per logInterval per view.
func (p *Provider) logQuietly(uri string, write func()) {
	p.quietMu.Lock()
	last, seen := p.quiet[uri]
	now := p.now()
	if seen && now.Sub(last) < logInterval {
		p.quietMu.Unlock()
		return
	}
	p.quiet[uri] = now
	p.quietMu.Unlock()
	write()
}

func (p *Provider) originForLog() string {
	if p.fetcher == nil || p.fetcher.base == nil {
		return ""
	}
	return p.fetcher.base.Redacted()
}

// WriteMetrics renders this provider's section of the exposition.
//
// A gauge PER URI rather than a total, because the failure that matters is one
// view missing while the other is fine — a total of 1 out of 2 is a number
// nobody can act on. It is wired into /metrics through the same `extra` slot the
// AI counters use, so no seventh argument is added to the handler that already
// says it has enough.
func (p *Provider) WriteMetrics(w io.Writer) {
	_, _ = fmt.Fprintf(w, "# HELP margince_mcp_app_view_held Whether this api is serving each MCP App view document.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_mcp_app_view_held gauge\n")
	for _, v := range catalog {
		serving := 0
		if p.Holds(v.uri) {
			serving = 1
		}
		_, _ = fmt.Fprintf(w, "margince_mcp_app_view_held{uri=%q} %d\n", v.uri, serving)
	}
	_, _ = fmt.Fprintf(w, "# HELP margince_mcp_app_fetch_failures_total View document fetches that did not arrive.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_mcp_app_fetch_failures_total counter\n")
	_, _ = fmt.Fprintf(w, "margince_mcp_app_fetch_failures_total %d\n", p.fetchFailures.Load())
	_, _ = fmt.Fprintf(w, "# HELP margince_mcp_app_admission_failures_total View documents that arrived and were refused.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_mcp_app_admission_failures_total counter\n")
	_, _ = fmt.Fprintf(w, "margince_mcp_app_admission_failures_total %d\n", p.admissionFailures.Load())
	_, _ = fmt.Fprintf(w, "# HELP margince_mcp_app_title_mismatches_total Served documents whose title differs from the catalog's.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_mcp_app_title_mismatches_total counter\n")
	_, _ = fmt.Fprintf(w, "margince_mcp_app_title_mismatches_total %d\n", p.titleMismatches.Load())
	// DERIVED from the documents currently held rather than recorded as one
	// process-wide flag. Skew is per-view — a rollout replaces one document
	// before the other — and a single reading would be whichever view was read
	// last, which is a number that changes for reasons nobody can trace.
	_, _ = fmt.Fprintf(w, "# HELP margince_mcp_app_build_skew The held view was built from a different revision than this api.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_mcp_app_build_skew gauge\n")
	for _, v := range catalog {
		doc, holding := p.served(v.uri)
		if !holding {
			continue
		}
		skewed := 0
		if buildinfo.SkewBetween(buildinfo.Revision, documentRevision(doc)) {
			skewed = 1
		}
		_, _ = fmt.Fprintf(w, "margince_mcp_app_build_skew{uri=%q} %d\n", v.uri, skewed)
	}
}
