// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The renderOffer HTTP round trip: with a blobstore wired, POST
// /offers/{id}/render answers 200 with pdf_asset_ref set, and the object
// it names actually exists in the store — proving the whole
// PrepareRender → RenderOfferPDF → blob.Put → SetPdfAssetRef chain runs
// over the real handler stack, including the TOCTOU fence between the
// prepare read and the set write. The unwired-blobstore 501 shape is
// already covered by offertemplate_http_integration_test.go's
// assertRenderOfferNotImplementedWithoutBlobstore (the default setup()
// harness wires no blobstore at all), so it is not repeated here.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
)

// setupWithBlobstore boots the e2e harness with an in-memory object store
// wired in, returning both the harness and the SAME store instance so the
// test can independently verify the object the render handler wrote.
func setupWithBlobstore(t *testing.T) (*env, blobstore.Store) {
	t.Helper()
	blob := blobstore.NewMemory()
	e := setupWithOptions(t, compose.WithBlobstore(blob))
	return e, blob
}

type renderedOffer struct {
	ID          string `json:"id"`
	PdfAssetRef string `json:"pdf_asset_ref"`
}

func TestOfferRenderHTTP_PostRenderReturns200WithPdfAssetRefAndTheBlobExists(t *testing.T) {
	e, blob := setupWithBlobstore(t)
	e.bootstrapWorkspace(t)
	dealID := offerFixture(t, e)

	var offer anyMap
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "manual",
		"line_items": []anyMap{{"description": "Retainer", "quantity": 1, "unit_price_minor": 500000, "tax_rate": 19.0}},
	}, nil, &offer); status != http.StatusCreated {
		t.Fatalf("create offer for render = %d %v", status, offer)
	}
	offerID, ok := offer["id"].(string)
	if !ok {
		t.Fatalf("create offer for render response carries no id: %v", offer)
	}

	var rendered renderedOffer
	if status := e.call(t, "POST", "/v1/offers/"+offerID+"/render", anyMap{}, nil, &rendered); status != http.StatusOK {
		t.Fatalf("render offer = %d %+v, want 200", status, rendered)
	}
	if rendered.ID != offerID {
		t.Fatalf("render response id = %q, want %q", rendered.ID, offerID)
	}
	if rendered.PdfAssetRef == "" {
		t.Fatal("render response must carry a non-empty pdf_asset_ref")
	}

	// The named object must actually exist in the store the render
	// handler was given (never a ref pointing at nothing).
	rc, obj, err := blob.Get(context.Background(), rendered.PdfAssetRef)
	if err != nil {
		t.Fatalf("get the rendered blob at %q: %v", rendered.PdfAssetRef, err)
	}
	data, err := io.ReadAll(rc)
	if cerr := rc.Close(); cerr != nil {
		t.Errorf("close the rendered blob reader: %v", cerr)
	}
	if err != nil {
		t.Fatalf("read the rendered blob: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		t.Fatalf("the rendered blob does not look like a PDF, starts with %q", data[:min(16, len(data))])
	}
	if obj.ContentType != "application/pdf" {
		t.Fatalf("the rendered blob's content type = %q, want application/pdf", obj.ContentType)
	}

	// A second render gets its OWN per-attempt key (never the first
	// render's key — that is what closes the dangling-ref race two
	// concurrent renders sharing one key could hit), but a successful
	// re-render still reclaims its now-superseded PREVIOUS ref rather than
	// accumulating orphans, and stays 200.
	var renderedAgain renderedOffer
	if status := e.call(t, "POST", "/v1/offers/"+offerID+"/render", anyMap{}, nil, &renderedAgain); status != http.StatusOK {
		t.Fatalf("second render = %d", status)
	}
	if renderedAgain.PdfAssetRef == rendered.PdfAssetRef {
		t.Fatalf("each render attempt must get its own key, got the same %q twice", rendered.PdfAssetRef)
	}
	if _, _, err := blob.Get(context.Background(), rendered.PdfAssetRef); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("the superseded first render's blob must be reclaimed after a successful re-render, got err=%v", err)
	}
	rc2, _, err := blob.Get(context.Background(), renderedAgain.PdfAssetRef)
	if err != nil {
		t.Fatalf("get the re-rendered blob at %q: %v", renderedAgain.PdfAssetRef, err)
	}
	if cerr := rc2.Close(); cerr != nil {
		t.Errorf("close the re-rendered blob reader: %v", cerr)
	}
}

// raceLineAdder wraps a real blobstore.Store and, right after Put writes
// the rendered PDF bytes, adds a line item to the SAME offer over the
// ordinary HTTP surface — bumping the offer's row version via the normal
// AddOfferLineItem write path. Put lands at the EXACT instant between the
// handler's PrepareRender call and its subsequent SetPdfAssetRef call, so
// this deterministically reproduces the concurrent-edit race (another
// line edit, a regenerate, a sibling render) without goroutines or a
// sleep — the TOCTOU offer_render.go's SetPdfAssetRef fences against.
type raceLineAdder struct {
	blobstore.Store
	t       *testing.T
	e       *env
	offerID string
	// putKey records the exact per-attempt key the render handler wrote —
	// each render mints a fresh key, so the test cannot reconstruct it from
	// the offer's workspace/id/revision alone the way the shared
	// per-revision key it replaced once allowed.
	putKey string
	// hookErr captures a failure from the injected call below. Put runs on
	// the outer render request's own handler goroutine, so a t.Fatalf here
	// would Goexit that goroutine before it writes the render's HTTP
	// response — hanging the test goroutine's e.call for the render POST
	// forever. Recording the failure and asserting it from the test
	// goroutine, after the outer call returns, avoids that deadlock.
	hookErr error
}

func (r *raceLineAdder) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if err := r.Store.Put(ctx, key, body, size, contentType); err != nil {
		return err
	}
	r.putKey = key
	var line anyMap
	status := r.e.call(r.t, "POST", "/v1/offers/"+r.offerID+"/line-items", anyMap{
		"description": "Concurrent Line", "quantity": 1.0, "unit_price_minor": 1000,
	}, nil, &line)
	if status != http.StatusCreated {
		r.hookErr = fmt.Errorf("inject concurrent line edit between prepare and set = %d %v", status, line)
	}
	return nil
}

// TestOfferRenderHTTP_ConcurrentLineEditBetweenPrepareAndSetRejectsWithVersionSkewAndReclaimsTheBlob
// is the TOCTOU fence's end-to-end proof: a line lands on the offer
// between the handler's internal PrepareRender read and its
// SetPdfAssetRef write (raceLineAdder injects it at the one point in the
// real flow where that gap is observable — the blob.Put call). The
// request must answer 409 version_skew rather than silently persisting
// pdf_asset_ref against stale lines, AND the PDF bytes already written
// must be reclaimed — no orphan object survives a rejected render.
func TestOfferRenderHTTP_ConcurrentLineEditBetweenPrepareAndSetRejectsWithVersionSkewAndReclaimsTheBlob(t *testing.T) {
	race := &raceLineAdder{Store: blobstore.NewMemory()}
	e := setupWithOptions(t, compose.WithBlobstore(race))
	race.t, race.e = t, e
	e.bootstrapWorkspace(t)
	dealID := offerFixture(t, e)

	var offer anyMap
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "manual",
		"line_items": []anyMap{{"description": "Retainer", "quantity": 1, "unit_price_minor": 500000, "tax_rate": 19.0}},
	}, nil, &offer); status != http.StatusCreated {
		t.Fatalf("create offer for render = %d %v", status, offer)
	}
	offerID, ok := offer["id"].(string)
	if !ok {
		t.Fatalf("create offer for render response carries no id: %v", offer)
	}
	race.offerID = offerID

	var problem struct {
		Code string `json:"code"`
	}
	status := e.call(t, "POST", "/v1/offers/"+offerID+"/render", anyMap{}, nil, &problem)
	if race.hookErr != nil {
		t.Fatal(race.hookErr)
	}
	if status != http.StatusConflict || problem.Code != "version_skew" {
		t.Fatalf("render racing a concurrent line edit = %d %+v, want 409 version_skew", status, problem)
	}

	// The rendered blob written just before the concurrent edit landed
	// must be reclaimed — the object store must carry NOTHING at the
	// exact per-attempt key this rejected render wrote.
	if race.putKey == "" {
		t.Fatal("the race hook never observed a Put — the render must have failed before writing any blob")
	}
	if _, _, err := race.Get(context.Background(), race.putKey); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("the render blob must be reclaimed after a version conflict, got err=%v", err)
	}

	// The rejected render must not have moved pdf_asset_ref at all.
	var got anyMap
	if status := e.call(t, "GET", "/v1/offers/"+offerID, nil, nil, &got); status != http.StatusOK {
		t.Fatalf("get offer after rejected render = %d", status)
	}
	if got["pdf_asset_ref"] != nil {
		t.Fatalf("a rejected render must not persist any pdf_asset_ref, got %+v", got["pdf_asset_ref"])
	}
}

// raceDoubleRenderer wraps a real blobstore.Store and, right after Put
// writes the OUTER render's PDF bytes, drives a second, COMPLETE render of
// the same offer over the ordinary HTTP surface — reproducing two renders
// racing each other rather than a differently-shaped write like a line
// edit. The nested render's own PrepareRender still sees the offer at the
// version the outer render prepared against (the outer request has not
// committed yet), so the nested render wins the version fence and commits
// its own fresh per-attempt key; only once it returns does control resume
// in the outer Put, so the outer request's later SetPdfAssetRef loses the
// fence and reclaims. nested guards against the nested render's own Put
// re-triggering the same hook.
type raceDoubleRenderer struct {
	blobstore.Store
	t       *testing.T
	e       *env
	offerID string
	nested  bool
	// hookErr captures a failure from the nested render below. Put runs on
	// the outer render request's own handler goroutine, so a t.Fatalf here
	// would Goexit that goroutine before it writes the outer render's HTTP
	// response — hanging the test goroutine's e.call for the outer render
	// POST forever. Recording the failure and asserting it from the test
	// goroutine, after the outer call returns, avoids that deadlock.
	hookErr error
}

func (r *raceDoubleRenderer) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if err := r.Store.Put(ctx, key, body, size, contentType); err != nil {
		return err
	}
	if r.nested {
		return nil
	}
	r.nested = true
	var winner renderedOffer
	status := r.e.call(r.t, "POST", "/v1/offers/"+r.offerID+"/render", anyMap{}, nil, &winner)
	if status != http.StatusOK {
		r.hookErr = fmt.Errorf("nested concurrent render = %d %+v, want 200", status, winner)
	}
	return nil
}

// TestOfferRenderHTTP_ConcurrentDoubleRenderLoserReclaimsOnlyItsOwnBlob is
// the dangling-ref regression's end-to-end proof: two renders of the SAME
// offer race for the version fence (raceDoubleRenderer injects the second,
// complete render at the one point in the real flow where the gap between
// PrepareRender and SetPdfAssetRef is observable — the blob.Put call). A
// render blob key shared across concurrent renders of the same offer (keyed
// only by workspace/offer/revision) would have the LOSER's reclaim delete
// the WINNER's just-committed object — a dangling pdf_asset_ref a later
// download would 404 on. Per-attempt keys (this offer's render key includes
// a fresh id minted per render call) mean the loser only ever reclaims the
// blob it itself wrote, so the winner's committed blob must survive.
func TestOfferRenderHTTP_ConcurrentDoubleRenderLoserReclaimsOnlyItsOwnBlob(t *testing.T) {
	race := &raceDoubleRenderer{Store: blobstore.NewMemory()}
	e := setupWithOptions(t, compose.WithBlobstore(race))
	race.t, race.e = t, e
	e.bootstrapWorkspace(t)
	dealID := offerFixture(t, e)

	var offer anyMap
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "manual",
		"line_items": []anyMap{{"description": "Retainer", "quantity": 1, "unit_price_minor": 500000, "tax_rate": 19.0}},
	}, nil, &offer); status != http.StatusCreated {
		t.Fatalf("create offer for render = %d %v", status, offer)
	}
	offerID, ok := offer["id"].(string)
	if !ok {
		t.Fatalf("create offer for render response carries no id: %v", offer)
	}
	race.offerID = offerID

	var problem struct {
		Code string `json:"code"`
	}
	status := e.call(t, "POST", "/v1/offers/"+offerID+"/render", anyMap{}, nil, &problem)
	if race.hookErr != nil {
		t.Fatal(race.hookErr)
	}
	if status != http.StatusConflict || problem.Code != "version_skew" {
		t.Fatalf("outer render racing a nested concurrent render = %d %+v, want 409 version_skew", status, problem)
	}

	var got anyMap
	if status := e.call(t, "GET", "/v1/offers/"+offerID, nil, nil, &got); status != http.StatusOK {
		t.Fatalf("get offer after the race = %d", status)
	}
	winnerRef, ok := got["pdf_asset_ref"].(string)
	if !ok || winnerRef == "" {
		t.Fatalf("the offer must carry the WINNING nested render's pdf_asset_ref, got %+v", got["pdf_asset_ref"])
	}

	rc, _, err := race.Get(context.Background(), winnerRef)
	if err != nil {
		t.Fatalf("the winning render's committed blob must still exist after the loser's reclaim ran, get %q: %v", winnerRef, err)
	}
	if cerr := rc.Close(); cerr != nil {
		t.Errorf("close the winning blob reader: %v", cerr)
	}
}
