// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

// Shared transport mechanics for module handlers: request decode, JSON
// response writing, and the If-Match optimistic-concurrency header —
// wire concerns every module transport spells identically.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// MaxBodyBytes bounds every JSON request body (1 MiB): no contract
// payload is legitimately larger, and an unbounded read is free memory
// amplification on the cheapest endpoints.
const MaxBodyBytes = 1 << 20

// Decode parses the request body, answering the validation problem shape
// on malformed JSON. The body is size-capped and must contain exactly
// one JSON value — trailing tokens are malformed, not ignored. Returns
// false when the response has been written.
//
//craft:ignore naked-any the JSON deserialization seam: the decode target is whichever contract request struct the handler owns
func Decode(w http.ResponseWriter, r *http.Request, into any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			Write(w, r, &DetailedError{Status: http.StatusRequestEntityTooLarge,
				Code: "body_too_large", Detail: "request body exceeds the 1 MiB cap"})
			return false
		}
		Write(w, r, Validation("body", "malformed_json", err.Error()))
		return false
	}
	// A field key that only case-folds onto a contract field (or is
	// unknown) is refused rather than matched by encoding/json's
	// case-insensitive fallback — the same gate the provider seam applies,
	// so REST and MCP agree on which keys are a field patch.
	if kErr := datasource.RejectNonCanonicalKeys(raw, into); kErr != nil {
		Write(w, r, Validation("body", "unknown_field", kErr.Error()))
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(into); err != nil {
		Write(w, r, Validation("body", "malformed_json", err.Error()))
		return false
	}
	if dec.More() {
		Write(w, r, Validation("body", "malformed_json", "trailing content after the JSON value"))
		return false
	}
	return true
}

// WriteJSON writes a JSON response with the given status.
//
//craft:ignore naked-any the JSON serialization seam: body is whichever contract response struct the handler produced
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//craft:ignore swallowed-errors WriteHeader already committed the response — nothing can report an encode failure to the client anymore
	_ = json.NewEncoder(w).Encode(body)
}

// CustomFieldFilters collects a list request's cf_* query parameters —
// the custom-column equality filters (data-model §13.5 / CF-T05). They
// are dynamic per workspace, so the OpenAPI contract cannot declare them
// as typed parameters; the store validates each against the ACTIVE
// column catalog (422 on an unknown/retired name or a malformed value).
// nil when the request carries none, so the zero request costs nothing.
func CustomFieldFilters(r *http.Request) map[string]string {
	var filters map[string]string
	for key, values := range r.URL.Query() {
		if !strings.HasPrefix(key, "cf_") || len(values) == 0 {
			continue
		}
		if filters == nil {
			filters = make(map[string]string)
		}
		filters[key] = values[0]
	}
	return filters
}

// StreamedObject is what StreamObject needs from any byte-store read —
// blobstore.Store.Get's and activities' OpenAttachment's return shapes
// both satisfy it without either package importing the other.
type StreamedObject struct {
	Body        io.ReadCloser
	ContentType string
	// Filename, given, sets Content-Disposition; empty omits the header
	// entirely (Content-Type alone still tells the browser how to render
	// the bytes).
	Filename string
	// Inline renders in the browser (e.g. a PDF preview tab); the
	// default, attachment, always downloads instead.
	Inline bool
	// Size <= 0 omits Content-Length — the caller doesn't always have it
	// upfront.
	Size int64
}

// StreamObject writes a byte-store object's bytes as the response body —
// the one spelling of "set Content-Type/-Disposition/-Length, copy, log a
// mid-stream copy failure" every handler that streams stored bytes back
// to a client needs (activities' DownloadAttachment, deals'
// DownloadOfferPdf). The status is already 200 once bytes start flowing,
// so a copy failure (usually a client disconnect mid-download) can only
// be logged, never re-reported to the client.
func StreamObject(w http.ResponseWriter, r *http.Request, obj StreamedObject, logLabel string) {
	defer func(ctx context.Context) {
		if cerr := obj.Body.Close(); cerr != nil {
			slog.WarnContext(ctx, "closing streamed object reader", "object", logLabel, "err", cerr)
		}
	}(r.Context())

	contentType := obj.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if obj.Filename != "" {
		disposition := "attachment"
		if obj.Inline {
			disposition = "inline"
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, obj.Filename))
	}
	if obj.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	}
	if _, err := io.Copy(w, obj.Body); err != nil {
		slog.WarnContext(r.Context(), "streaming object download", "object", logLabel, "err", err)
	}
}

// IfMatchVersion reads the optional If-Match row version (data-model
// §1.3a: a bare integer, not a quoted ETag). Malformed input is a client
// error, not last-write-wins.
func IfMatchVersion(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	raw := r.Header.Get("If-Match")
	if raw == "" {
		return nil, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 {
		Write(w, r, Validation("If-Match", "malformed_if_match", "If-Match carries the last-seen integer version"))
		return nil, false
	}
	return &v, true
}
