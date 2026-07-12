// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The renderOffer HTTP round trip (offers-depth arc 4a T4): with a
// blobstore wired, POST /offers/{id}/render answers 200 with pdf_asset_ref
// set, and the object it names actually exists in the store — proving the
// whole PrepareRender → RenderOfferPDF → blob.Put → SetPdfAssetRef chain
// runs over the real handler stack. The unwired-blobstore 501 shape is
// already covered by offertemplate_http_integration_test.go's
// assertRenderOfferNotImplemented (the default setup() harness wires no
// blobstore at all), so it is not repeated here.

import (
	"bytes"
	"context"
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
	offerID := offer["id"].(string)

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
