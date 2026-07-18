// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package imap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// crlf joins RFC822 lines with the wire's CRLF so the parser sees a
// well-formed message regardless of this file's own line endings.
func crlf(lines ...string) []byte {
	return []byte(strings.Join(lines, "\r\n"))
}

func inboundFixture() []byte {
	return crlf(
		"From: Alice Example <alice@acme.com>",
		"To: me@myco.com",
		"Subject: Quote request",
		"Date: Wed, 04 Jun 2026 08:00:00 +0000",
		"Message-ID: <abc123@acme.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Hi, please send pricing for 10 seats.",
		"",
	)
}

func TestNormalizeMapsInboundEmailToActivity(t *testing.T) {
	c := &Connector{owner: "me@myco.com"}
	recs, err := c.Normalize(context.Background(), inboundFixture())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	rec := recs[0]

	if rec.EntityType != datasource.EntityActivity {
		t.Errorf("EntityType = %q, want activity", rec.EntityType)
	}
	if rec.NaturalKey.SourceSystem != "imap" || rec.NaturalKey.SourceID != "abc123@acme.com" {
		t.Errorf("NaturalKey = %+v, want {imap, abc123@acme.com}", rec.NaturalKey)
	}
	if rec.Source != "imap:abc123@acme.com" {
		t.Errorf("Source = %q, want imap:abc123@acme.com", rec.Source)
	}
	if rec.CapturedBy != "connector:imap" {
		t.Errorf("CapturedBy = %q, want connector:imap", rec.CapturedBy)
	}

	fields, ok := rec.Fields.(capture.ActivityFields)
	if !ok {
		t.Fatalf("Fields is %T, want capture.ActivityFields", rec.Fields)
	}
	if fields.Kind != "email" {
		t.Errorf("Kind = %q, want email", fields.Kind)
	}
	if fields.Subject != "Quote request" {
		t.Errorf("Subject = %q, want Quote request", fields.Subject)
	}
	if fields.Direction != "inbound" {
		t.Errorf("Direction = %q, want inbound (owner is the recipient)", fields.Direction)
	}
	if !strings.Contains(fields.Body, "please send pricing for 10 seats") {
		t.Errorf("Body missing the text part: %q", fields.Body)
	}
	if !strings.Contains(fields.Body, "alice@acme.com") {
		t.Errorf("Body should surface the counterparty From address: %q", fields.Body)
	}
	if fields.OccurredAt.IsZero() {
		t.Errorf("OccurredAt should be parsed from the Date header, got zero")
	}
	if got := fields.OccurredAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-06-04T08:00:00Z" {
		t.Errorf("OccurredAt = %s, want 2026-06-04T08:00:00Z", got)
	}
}

func TestNormalizeClassifiesOutboundByOwner(t *testing.T) {
	// The mailbox owner is the sender → outbound; the counterparty is the To.
	c := &Connector{owner: "me@myco.com"}
	raw := crlf(
		"From: me@myco.com",
		"To: Bob Buyer <bob@target.com>",
		"Subject: Re: Quote request",
		"Date: Wed, 04 Jun 2026 09:00:00 +0000",
		"Message-ID: <reply1@myco.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Here is your pricing.",
		"",
	)
	recs, err := c.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	fields := recs[0].Fields.(capture.ActivityFields)
	if fields.Direction != "outbound" {
		t.Errorf("Direction = %q, want outbound", fields.Direction)
	}
	if !strings.Contains(fields.Body, "bob@target.com") {
		t.Errorf("outbound counterparty (To) should be surfaced: %q", fields.Body)
	}
}

func TestNormalizeSkipsAutomatedMail(t *testing.T) {
	c := &Connector{owner: "me@myco.com"}
	cases := map[string][]byte{
		"no-reply sender": crlf(
			"From: no-reply@newsletter.com",
			"To: me@myco.com",
			"Subject: Weekly digest",
			"Message-ID: <n1@newsletter.com>",
			"Content-Type: text/plain",
			"",
			"news",
			"",
		),
		"auto-submitted header": crlf(
			"From: system@acme.com",
			"To: me@myco.com",
			"Subject: Out of office",
			"Auto-Submitted: auto-replied",
			"Message-ID: <ooo1@acme.com>",
			"Content-Type: text/plain",
			"",
			"I am away",
			"",
		),
		"no message id": crlf(
			"From: someone@acme.com",
			"To: me@myco.com",
			"Subject: hi",
			"Content-Type: text/plain",
			"",
			"body",
			"",
		),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := c.Normalize(context.Background(), raw)
			if !errors.Is(err, connector.ErrSkip) {
				t.Fatalf("want ErrSkip, got %v", err)
			}
		})
	}
}

func TestNormalizeFallsBackToHTMLWhenNoPlainPart(t *testing.T) {
	c := &Connector{owner: "me@myco.com"}
	raw := crlf(
		"From: Carol <carol@acme.com>",
		"To: me@myco.com",
		"Subject: HTML only",
		"Message-ID: <html1@acme.com>",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>Hello <b>there</b></p>",
		"",
	)
	recs, err := c.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	fields := recs[0].Fields.(capture.ActivityFields)
	if !strings.Contains(fields.Body, "Hello") || strings.Contains(fields.Body, "<p>") {
		t.Errorf("HTML should be tag-stripped into readable text: %q", fields.Body)
	}
}

// readCapped bounds the per-message read so a hostile server can't drive an
// unbounded allocation: at or under the cap reads through, over the cap is
// rejected as too-large rather than persisted truncated.
func TestReadCappedRejectsOversizedMessages(t *testing.T) {
	small := strings.Repeat("a", 1024)
	got, err := readCapped(strings.NewReader(small))
	if err != nil {
		t.Fatalf("under-cap read should succeed: %v", err)
	}
	if len(got) != len(small) {
		t.Fatalf("under-cap read = %d bytes, want %d", len(got), len(small))
	}

	atCap, err := readCapped(strings.NewReader(strings.Repeat("a", maxRawLen)))
	if err != nil {
		t.Fatalf("exactly-at-cap read should succeed: %v", err)
	}
	if len(atCap) != maxRawLen {
		t.Fatalf("at-cap read = %d bytes, want %d", len(atCap), maxRawLen)
	}

	if _, err := readCapped(strings.NewReader(strings.Repeat("a", maxRawLen+1))); !errors.Is(err, errMessageTooLarge) {
		t.Fatalf("over-cap read should be errMessageTooLarge, got %v", err)
	}
}

// fakeSink records Upserts and can be told to skip or fail, so capture's
// accounting (the production pull path, not just Normalize) is proven directly.
type fakeSink struct {
	calls int
	skip  bool
	err   error
}

func (f *fakeSink) Upsert(_ context.Context, _ connector.NormalizedRecord) (datasource.EntityRef, error) {
	f.calls++
	if f.skip {
		return datasource.EntityRef{}, connector.ErrSkip
	}
	return datasource.EntityRef{}, f.err
}

func TestCaptureAccountsForOutcomes(t *testing.T) {
	c := &Connector{}
	newState := func() *syncState {
		return &syncState{owner: "me@myco.com", contacts: map[string]struct{}{}}
	}

	// A normal inbound message lands and tallies the counterparty.
	st := newState()
	sink := &fakeSink{}
	if err := c.capture(context.Background(), inboundFixture(), sink, st); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if st.stats.Captured != 1 || st.stats.Skipped != 0 || len(st.contacts) != 1 {
		t.Fatalf("captured=%d skipped=%d contacts=%d, want 1/0/1", st.stats.Captured, st.stats.Skipped, len(st.contacts))
	}

	// A Sink ErrSkip is a deliberate drop, counted as skipped, never fatal.
	st = newState()
	if err := c.capture(context.Background(), inboundFixture(), &fakeSink{skip: true}, st); err != nil {
		t.Fatalf("ErrSkip must not surface as an error: %v", err)
	}
	if st.stats.Captured != 0 || st.stats.Skipped != 1 {
		t.Fatalf("captured=%d skipped=%d, want 0/1", st.stats.Captured, st.stats.Skipped)
	}

	// An unparseable body is dropped without touching the Sink.
	st = newState()
	unusable := &fakeSink{}
	if err := c.capture(context.Background(), []byte("not a message"), unusable, st); err != nil {
		t.Fatalf("unparseable must not error: %v", err)
	}
	if st.stats.Skipped != 1 || unusable.calls != 0 {
		t.Fatalf("skipped=%d sinkCalls=%d, want 1/0", st.stats.Skipped, unusable.calls)
	}

	// A real write fault propagates (Sync uses it to stop the pull).
	st = newState()
	wantErr := errors.New("db down")
	if err := c.capture(context.Background(), inboundFixture(), &fakeSink{err: wantErr}, st); !errors.Is(err, wantErr) {
		t.Fatalf("write fault should propagate, got %v", err)
	}
}

// Close is safe on a connector that never authenticated (the handler defers it
// unconditionally) and is idempotent, so double teardown can't panic.
func TestCloseIsSafeWithoutSession(t *testing.T) {
	c := New()
	if err := c.Close(); err != nil {
		t.Fatalf("Close on an unauthenticated connector: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close should be a no-op: %v", err)
	}
}
