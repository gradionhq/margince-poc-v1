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
		// A read that failed mid-body is a transport fact — timed-out sockets
		// carry host and port — so the caller is told what to do about it and
		// the operator's half stays in the log.
		slog.WarnContext(r.Context(), "reading request body", "method", r.Method, "path", r.URL.Path, "err", err)
		Write(w, r, Validation("body", "malformed_json",
			"the request body could not be read to the end; resend the request with a complete body"))
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
		Write(w, r, bodyDecodeRefusal(r, raw, into, err))
		return false
	}
	if dec.More() {
		Write(w, r, Validation("body", "malformed_json", "trailing content after the JSON value"))
		return false
	}
	stashPresentFields(r, raw)
	return true
}

// presentFieldsKey carries the decoded body's top-level keys, so a handler can
// tell an explicit `null` from an absent field.
type presentFieldsKey struct{}

// stashPresentFields records which top-level keys the body actually carried.
//
// A sparse patch needs this and a decoded struct cannot supply it: a nullable
// contract field decodes to a nil pointer whether the caller sent `null` or said
// nothing, and those are opposite instructions — "clear this" against "leave it
// alone". A handler that cannot tell them apart either refuses every clear or
// performs one nobody asked for.
//
// Best-effort by design: a body that is not a JSON object records nothing, and
// PresentField then answers "absent", which is the safe reading.
func stashPresentFields(r *http.Request, raw json.RawMessage) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return
	}
	*r = *r.WithContext(context.WithValue(r.Context(), presentFieldsKey{}, fields))
}

// PresentField answers whether the request body carried this key, and the raw
// value if it did. `present` false means the caller did not mention the field;
// `present` true with a nil value means they sent an explicit null.
func PresentField(r *http.Request, name string) (value json.RawMessage, present bool) {
	fields, ok := r.Context().Value(presentFieldsKey{}).(map[string]json.RawMessage)
	if !ok {
		return nil, false
	}
	raw, found := fields[name]
	if !found {
		return nil, false
	}
	if string(raw) == "null" {
		return nil, true
	}
	return raw, true
}

// bodyDecodeRefusal is the 422 for a body the decoder rejected. Everything
// reaching it came from encoding/json filling a contract struct — unknown keys
// were already refused above — so a shape RestateDecodeError cannot name is
// decoder internals: the caller gets a sentence that says what to check, and
// the decoder's own words go to the log rather than nowhere.
//
// Which FIELD comes first, through the same seam function the provider surface
// uses. A contract type carrying additionalProperties decodes field-by-field
// under a generated UnmarshalJSON, so encoding/json's own error names no path
// and the sentence left to say is "the payload must be a JSON object" about a
// payload that is one. Naming the field is what makes the rest of it useful, and
// a body reaches these structs by either route — the localization has to be on
// both or the two surfaces disagree about the same mistake.
//
//craft:ignore naked-any mirror of Decode's seam target
func bodyDecodeRefusal(r *http.Request, raw json.RawMessage, into any, err error) error {
	if refusal := datasource.LocalizeFieldFault(raw, into, err, bodyProbe); refusal != nil {
		detail, withheld := fieldShapeDetail(refusal)
		if withheld {
			slog.WarnContext(r.Context(), "unnamed request-body field decode failure",
				"method", r.Method, "path", r.URL.Path, "field", refusal.Field, "err", err)
		}
		return Validation("body", "malformed_json", detail)
	}
	safe, withheld := SafeDecodeError(err)
	if withheld {
		slog.WarnContext(r.Context(), "unnamed request-body decode failure",
			"method", r.Method, "path", r.URL.Path, "err", err)
	}
	return Validation("body", "malformed_json", safe.Error())
}

// bodyProbe is the decoder Decode itself runs, so a localization probe asks the
// question the real decode asked. It is deliberately NOT strict about unknown
// keys: RejectNonCanonicalKeys refused those earlier and by name, and a probe
// that re-refused them here would blame a field for a key already reported.
//
//craft:ignore naked-any mirror of Decode's seam target
func bodyProbe(single json.RawMessage, probe any) error {
	return json.NewDecoder(bytes.NewReader(single)).Decode(probe)
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
