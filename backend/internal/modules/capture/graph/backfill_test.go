// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package graph

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func TestEstimateBackfillAsksProviderForTheWindow(t *testing.T) {
	after := time.Date(2026, 1, 18, 0, 0, 0, 0, time.UTC)
	api := &fakeAPI{estimate: 4200}
	c := pinnedConn(api)

	got, err := c.EstimateBackfill(context.Background(), authBytes(t), after)
	if err != nil {
		t.Fatalf("EstimateBackfill: %v", err)
	}
	if got != 4200 {
		t.Errorf("estimate = %d, want the provider's count 4200", got)
	}
	if !api.estimateAfter.Equal(after) {
		t.Errorf("estimate window boundary = %v, want %v", api.estimateAfter, after)
	}
}

func TestBackfillPageCapturesAndCounts(t *testing.T) {
	api := &fakeAPI{
		listIDs:  []string{"m1", "m2", "m3"},
		listNext: "https://graph/messages?skiptoken=next",
		raws: map[string][]byte{
			"m1": rawMsg("m1@mail.example", "alice@acme.com"),
			"m2": []byte("not an rfc822 message at all"),
			"m3": rawMsg("m3@mail.example", "bob@acme.com"),
		},
	}
	c := pinnedConn(api)
	sink := &recordingSink{}

	res, err := c.BackfillPage(context.Background(), authBytes(t), time.Time{}, "", sink)
	if err != nil {
		t.Fatalf("BackfillPage: %v", err)
	}
	if res.NextToken != "https://graph/messages?skiptoken=next" {
		t.Errorf("NextToken = %q, want the provider's nextLink", res.NextToken)
	}
	if res.Scanned != 3 || res.Captured != 2 || res.Skipped != 1 {
		t.Errorf("tally = %+v, want scanned=3 captured=2 skipped=1 (the unparseable message is a skip)", res)
	}
	if len(sink.recs) != 2 {
		t.Errorf("sink received %d records, want 2", len(sink.recs))
	}
}

// observedProgress records every live report a page makes, in order.
type observedProgress struct{ calls [][3]int }

func (p *observedProgress) Observed(_ context.Context, scanned, captured, skipped int) {
	p.calls = append(p.calls, [3]int{scanned, captured, skipped})
}

func TestBackfillPageReportsProgressAfterEveryMessage(t *testing.T) {
	// A page is a hundred messages and minutes of provider I/O, so the walk
	// reports as it goes rather than only in its result. The numbers are the
	// page's absolute tally at each step: messages WALKED (not the page's
	// listing, which a retry would repeat) and how many of them were captured.
	api := &fakeAPI{
		listIDs: []string{"m1", "m2", "m3"},
		raws: map[string][]byte{
			// m2 is unparseable, so the second step advances scanned but not captured.
			"m1": rawMsg("m1@mail.example", "alice@acme.com"),
			"m2": []byte("not an rfc822 message at all"),
			"m3": rawMsg("m3@mail.example", "bob@acme.com"),
		},
	}
	c := pinnedConn(api)
	progress := &observedProgress{}
	ctx := connector.WithBackfillProgress(context.Background(), progress)

	res, err := c.BackfillPage(ctx, authBytes(t), time.Time{}, "", &recordingSink{})
	if err != nil {
		t.Fatalf("BackfillPage: %v", err)
	}
	want := [][3]int{{1, 1, 0}, {2, 1, 1}, {3, 2, 1}}
	if len(progress.calls) != len(want) {
		t.Fatalf("progress reported %d times, want one report per message: %v", len(progress.calls), progress.calls)
	}
	for i, w := range want {
		if progress.calls[i] != w {
			t.Fatalf("report %d = scanned %d / captured %d / skipped %d, want %d / %d / %d", i, progress.calls[i][0], progress.calls[i][1], progress.calls[i][2], w[0], w[1], w[2])
		}
	}
	if last := progress.calls[len(progress.calls)-1]; last[0] != res.Scanned || last[1] != res.Captured || last[2] != res.Skipped {
		t.Fatalf("last report = %v, want it to agree with the page result %+v", last, res)
	}
}

func TestBackfillPageWithoutAReporterWalksNormally(t *testing.T) {
	// Reporting is optional on both sides: a context carrying no reporter must
	// walk the page exactly as before, never panic on a missing one.
	api := &fakeAPI{listIDs: []string{"m1"}, raws: map[string][]byte{"m1": rawMsg("m1@mail.example", "alice@acme.com")}}

	res, err := pinnedConn(api).BackfillPage(context.Background(), authBytes(t), time.Time{}, "", &recordingSink{})
	if err != nil || res.Scanned != 1 || res.Captured != 1 {
		t.Fatalf("page = %+v, %v — want an unreported page to walk normally", res, err)
	}
}

func TestBackfillPageResumesFromToken(t *testing.T) {
	api := &fakeAPI{listIDs: nil, listNext: ""}
	c := pinnedConn(api)

	res, err := c.BackfillPage(context.Background(), authBytes(t), time.Time{}, "https://graph/messages?skiptoken=p2", &recordingSink{})
	if err != nil {
		t.Fatalf("BackfillPage: %v", err)
	}
	if api.seenPageToken != "https://graph/messages?skiptoken=p2" {
		t.Errorf("page token passed to the provider = %q, want the resume token", api.seenPageToken)
	}
	if res.NextToken != "" {
		t.Errorf("NextToken = %q, want \"\" (window exhausted)", res.NextToken)
	}
}

func TestBackfillPageStopsOnFetchFault(t *testing.T) {
	api := &fakeAPI{listIDs: []string{"m1"}, getErr: ErrUnreachable}
	c := pinnedConn(api)

	if _, err := c.BackfillPage(context.Background(), authBytes(t), time.Time{}, "", &recordingSink{}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("a fetch fault should stop the page, got %v", err)
	}
}

func TestBackfillMalformedAuthRejected(t *testing.T) {
	c := pinnedConn(&fakeAPI{})
	if _, err := c.EstimateBackfill(context.Background(), connector.Auth("{broken"), time.Time{}); err == nil {
		t.Fatal("EstimateBackfill with malformed auth must fail")
	}
	if _, err := c.BackfillPage(context.Background(), connector.Auth("{broken"), time.Time{}, "", &recordingSink{}); err == nil {
		t.Fatal("BackfillPage with malformed auth must fail")
	}
}

// The Graph half of the T1 correspondence evidence (ADR-0072 §1). Only the
// backfill can supply it: the incremental delta reads the inbox folder alone,
// while the backfill walks /me/messages across the whole mailbox, Sent Items
// included. Attestation needs BOTH halves, so each fixture below withholds a
// different one and only the third carries both.
func TestBackfillAttestsOnlyMailFiledInSentItemsAndWrittenByTheOwner(t *testing.T) {
	api := &fakeAPI{
		listIDs: []string{"m-in", "m-forged", "m-out"},
		sentIDs: map[string]bool{"m-out": true},
		raws: map[string][]byte{
			// Neither half: a stranger's mail, filed in the inbox.
			"m-in": rawMsg("in@mail.example", "alice@acme.com"),
			// Authorship claimed, placement absent — what a spoofer sends: the
			// From header names the owner, yet Graph filed it in the inbox
			// because the owner never sent it.
			"m-forged": rawMsg("forged@mail.example", owner),
			// Both halves.
			"m-out": rawMsg("out@mail.example", owner),
		},
	}
	sink := &recordingSink{}
	if _, err := pinnedConn(api).BackfillPage(context.Background(), authBytes(t), time.Time{}, "", sink); err != nil {
		t.Fatalf("BackfillPage: %v", err)
	}
	if len(sink.recs) != 3 {
		t.Fatalf("sink received %d records, want 3", len(sink.recs))
	}
	attested := map[string]bool{}
	for _, rec := range sink.recs {
		attested[rec.NaturalKey.SourceID] = rec.Counterparty.SentByOwner()
	}
	if attested["in@mail.example"] {
		t.Error("an inbox-filed stranger's message attested the owner's authorship")
	}
	if attested["forged@mail.example"] {
		t.Error("a forged From:owner message attested on authorship alone — the folder must be load-bearing")
	}
	if !attested["out@mail.example"] {
		t.Error("a Sent-Items-filed message the owner wrote did not attest")
	}
}

// A page that cannot tell sent mail from received must capture nothing rather
// than stamp its whole window un-attested: the activity natural key would make
// that guess permanent, silently costing the mailbox its T1 evidence. The
// engine retries the page from its committed token, as with any provider fault.
func TestBackfillStopsWhenTheSentFolderCannotBeResolved(t *testing.T) {
	api := &fakeAPI{
		listIDs:       []string{"m-out"},
		sentIDs:       map[string]bool{"m-out": true},
		sentFolderErr: ErrUnreachable,
		raws:          map[string][]byte{"m-out": rawMsg("out@mail.example", owner)},
	}
	sink := &recordingSink{}
	if _, err := pinnedConn(api).BackfillPage(context.Background(), authBytes(t), time.Time{}, "", sink); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("BackfillPage = %v, want the page to stop on the unresolved folder", err)
	}
	if len(sink.recs) != 0 {
		t.Fatalf("%d records captured, want none — nothing may land with guessed provenance", len(sink.recs))
	}
}

// An oversized message is a deliberate per-message drop, and the backfill must
// step over it exactly as the incremental pull does. Failing the page instead
// would send the engine back to the same committed token, onto the same
// message, forever — one unreadable mail would wedge the whole backfill.
func TestBackfillSkipsAnUnreadableMessageAndKeepsGoing(t *testing.T) {
	api := &fakeAPI{
		listIDs: []string{"m-huge", "m-ok"},
		raws:    map[string][]byte{"m-ok": rawMsg("ok@mail.example", "alice@acme.com")},
		skipIDs: map[string]bool{"m-huge": true},
	}
	sink := &recordingSink{}
	res, err := pinnedConn(api).BackfillPage(context.Background(), authBytes(t), time.Time{}, "", sink)
	if err != nil {
		t.Fatalf("BackfillPage must not fail on a skippable message: %v", err)
	}
	if res.Captured != 1 || res.Skipped != 1 || res.Scanned != 2 {
		t.Fatalf("tally = %+v, want scanned=2 captured=1 skipped=1", res)
	}
	if len(sink.recs) != 1 {
		t.Fatalf("sink received %d records, want the readable one", len(sink.recs))
	}
}
