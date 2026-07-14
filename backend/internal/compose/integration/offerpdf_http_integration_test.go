// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The downloadOfferPdf HTTP round trip: before any render, GET
// /offers/{id}/pdf 404s (the existence-hiding posture — an offer that
// exists but has never been rendered answers exactly like one that
// doesn't); after a render, it streams the SAME bytes the render call
// wrote at pdf_asset_ref, with Content-Type application/pdf. The 501
// blobstore-unwired shape is not repeated here: without a wired
// blobstore, render itself always 501s and pdf_asset_ref never gets set,
// so download can never observe anything but the already-covered 404
// path in that configuration.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestOfferPdfHTTP_DownloadBeforeRenderReturns404(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	dealID := offerFixture(t, e)

	var offer anyMap
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "manual",
		"line_items": []anyMap{{"description": "Retainer", "quantity": 1, "unit_price_minor": 500000, "tax_rate": 19.0}},
	}, nil, &offer); status != http.StatusCreated {
		t.Fatalf("create offer for pdf download = %d %v", status, offer)
	}
	offerID, ok := offer["id"].(string)
	if !ok {
		t.Fatalf("create offer for pdf download response carries no id: %v", offer)
	}

	if status := e.call(t, "GET", "/v1/offers/"+offerID+"/pdf", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("download pdf before any render = %d, want 404", status)
	}
}

func TestOfferPdfHTTP_DownloadNonexistentOfferReturns404(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	if status := e.call(t, "GET", "/v1/offers/"+ids.NewV7().String()+"/pdf", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("download pdf for an offer that was never created = %d, want 404", status)
	}
}

func TestOfferPdfHTTP_DownloadWhenTheBlobIsGoneReturns404(t *testing.T) {
	e, blob := setupWithBlobstore(t)
	e.bootstrapWorkspace(t)
	dealID := offerFixture(t, e)

	var offer anyMap
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "manual",
		"line_items": []anyMap{{"description": "Retainer", "quantity": 1, "unit_price_minor": 500000, "tax_rate": 19.0}},
	}, nil, &offer); status != http.StatusCreated {
		t.Fatalf("create offer for pdf download = %d %v", status, offer)
	}
	offerID, ok := offer["id"].(string)
	if !ok {
		t.Fatalf("create offer for pdf download response carries no id: %v", offer)
	}

	var rendered renderedOffer
	if status := e.call(t, "POST", "/v1/offers/"+offerID+"/render", anyMap{}, nil, &rendered); status != http.StatusOK {
		t.Fatalf("render offer = %d %+v, want 200", status, rendered)
	}

	// The offer's row still names this ref, but the object itself is gone —
	// e.g. purged out from under a stale pdf_asset_ref. The download must
	// answer the same 404 as "never rendered", never a raw blobstore error.
	if err := blob.Delete(context.Background(), rendered.PdfAssetRef); err != nil {
		t.Fatalf("delete the rendered blob out from under its ref: %v", err)
	}

	if status := e.call(t, "GET", "/v1/offers/"+offerID+"/pdf", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("download pdf after the blob is gone = %d, want 404", status)
	}
}

func TestOfferPdfHTTP_DownloadAfterRenderReturnsTheRenderedBytes(t *testing.T) {
	e, _ := setupWithBlobstore(t)
	e.bootstrapWorkspace(t)
	dealID := offerFixture(t, e)

	var offer anyMap
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "manual",
		"line_items": []anyMap{{"description": "Retainer", "quantity": 1, "unit_price_minor": 500000, "tax_rate": 19.0}},
	}, nil, &offer); status != http.StatusCreated {
		t.Fatalf("create offer for pdf download = %d %v", status, offer)
	}
	offerID, ok := offer["id"].(string)
	if !ok {
		t.Fatalf("create offer for pdf download response carries no id: %v", offer)
	}

	var rendered renderedOffer
	if status := e.call(t, "POST", "/v1/offers/"+offerID+"/render", anyMap{}, nil, &rendered); status != http.StatusOK {
		t.Fatalf("render offer = %d %+v, want 200", status, rendered)
	}

	req, err := http.NewRequest(http.MethodGet, e.ts.URL+"/v1/offers/"+offerID+"/pdf", nil)
	if err != nil {
		t.Fatalf("building pdf download request: %v", err)
	}
	req.Header.Set("X-Workspace-Slug", e.slug)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("GET pdf: %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download pdf after render = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("download pdf Content-Type = %q, want application/pdf", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading pdf download body: %v", err)
	}
	if !bytes.HasPrefix(body, []byte("%PDF")) {
		t.Fatalf("downloaded body does not look like a PDF, starts with %q", body[:min(16, len(body))])
	}
}
