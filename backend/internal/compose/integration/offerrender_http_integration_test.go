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

	// A second render overwrites the same revision's object rather than
	// accumulating orphans, and stays 200.
	var renderedAgain renderedOffer
	if status := e.call(t, "POST", "/v1/offers/"+offerID+"/render", anyMap{}, nil, &renderedAgain); status != http.StatusOK {
		t.Fatalf("second render = %d", status)
	}
	if renderedAgain.PdfAssetRef != rendered.PdfAssetRef {
		t.Fatalf("re-rendering the same revision must reuse the same key, got %q then %q", rendered.PdfAssetRef, renderedAgain.PdfAssetRef)
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
}

func (r *raceLineAdder) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if err := r.Store.Put(ctx, key, body, size, contentType); err != nil {
		return err
	}
	var line anyMap
	status := r.e.call(r.t, "POST", "/v1/offers/"+r.offerID+"/line-items", anyMap{
		"description": "Concurrent Line", "quantity": 1.0, "unit_price_minor": 1000,
	}, nil, &line)
	if status != http.StatusCreated {
		r.t.Fatalf("inject concurrent line edit between prepare and set = %d %v", status, line)
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
	wsID, ok := offer["workspace_id"].(string)
	if !ok {
		t.Fatalf("create offer for render response carries no workspace_id: %v", offer)
	}
	race.offerID = offerID

	var problem struct {
		Code string `json:"code"`
	}
	status := e.call(t, "POST", "/v1/offers/"+offerID+"/render", anyMap{}, nil, &problem)
	if status != http.StatusConflict || problem.Code != "version_skew" {
		t.Fatalf("render racing a concurrent line edit = %d %+v, want 409 version_skew", status, problem)
	}

	// The rendered blob written just before the concurrent edit landed
	// must be reclaimed — the object store must carry NOTHING for this
	// offer's revision key.
	key := "offers/" + wsID + "/" + offerID + "/1.pdf"
	if _, _, err := race.Get(context.Background(), key); !errors.Is(err, blobstore.ErrNotFound) {
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
