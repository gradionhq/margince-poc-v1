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
