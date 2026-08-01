// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The LinkedIn export upload (ADR-0078 §2.1b).

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// uploaderID is whose network this upload is. The matcher is scoped to them so
// the counts reported back describe THIS upload rather than every unmatched
// ghost in the workspace.
func uploaderID(ctx context.Context) ids.UUID {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return ids.Nil
	}
	return actor.UserID
}

// maxLinkedInExportBytes bounds the upload. A large personal network is a few
// thousand rows of short text — well under a megabyte — so 8 MB is generous
// for the real file and still refuses a mis-picked video before it reaches
// the CSV reader.
const maxLinkedInExportBytes = 8 << 20

// ImportLinkedInConnections implements POST /me/linkedin-connections.
//
// `/me/`, not `/users/{id}/`: a LinkedIn network is personal, and the owner is
// the authenticated caller rather than a path segment. There is deliberately
// no way to upload someone else's network on their behalf — it would let a
// person attribute a stranger's connections to a colleague, and the whole
// point of the feature is that "Lars knows them" means Lars.
func (h Handlers) ImportLinkedInConnections(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLinkedInExportBytes)
	//nolint:gosec // r.Body is bounded by http.MaxBytesReader above, so total parse size is capped; the arg only sets the in-memory/spill threshold.
	if err := r.ParseMultipartForm(maxLinkedInExportBytes); err != nil {
		httperr.Write(w, r, httperr.Validation("file", "invalid_multipart",
			"upload the Connections.csv as multipart/form-data, within the size limit"))
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		httperr.Write(w, r, httperr.Validation("file", "required",
			"a file part is required — the Connections.csv from LinkedIn's data export"))
		return
	}
	// The context is passed IN rather than captured: the request's context is
	// cancelled by the time a deferred close runs on some paths, and a log
	// line that silently drops because its context is done is a log line that
	// does not exist. (Same shape as the attachment upload.)
	defer func(ctx context.Context) {
		// Logged, not ignored, and not returned: by the time this runs the
		// import has either committed or failed on its own terms, and a close
		// error changes neither. It still has to be visible — the upload is a
		// temp file the multipart reader owns, and failing to close it leaks a
		// descriptor per request, which is a slow outage rather than a loud
		// one. (Same handling as the attachment upload.)
		if cerr := file.Close(); cerr != nil {
			slog.WarnContext(ctx, "closing uploaded LinkedIn export", "err", cerr)
		}
	}(r.Context())

	result, err := h.store.ImportLinkedInConnections(r.Context(), file)
	if err != nil {
		// A file this importer cannot read at all is the user's mistake and
		// they can fix it — they picked the wrong file, or edited it in a
		// spreadsheet until the header no longer parses. Answering 500 would
		// send them to support for something a sentence can solve.
		var format *LinkedInFormatError
		if errors.As(err, &format) {
			httperr.Write(w, r, httperr.Validation("file", "unreadable_export", format.Reason))
			return
		}
		writeStoreErr(w, r, err)
		return
	}
	// Matching runs here rather than on a schedule so the response can say
	// what the upload actually achieved. An import that answered "3,000
	// stored" and left the matches for an invisible nightly pass would look
	// like it had done nothing.
	matched, err := h.store.MatchLinkedInConnections(r.Context(), uploaderID(r.Context()))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.LinkedInImportSummary{
		Rows:      result.Rows,
		Imported:  result.Imported,
		Skipped:   result.Skipped,
		Confirmed: matched.Confirmed,
		Suggested: matched.Suggested,
	})
}

// GetMyLinkedInAccount implements GET /me/linkedin-account.
func (h Handlers) GetMyLinkedInAccount(w http.ResponseWriter, r *http.Request) {
	account, err := h.store.GetMyLinkedInAccount(r.Context())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, linkedInAccountWire(account))
}

// SaveMyLinkedInAccount implements PUT /me/linkedin-account.
func (h Handlers) SaveMyLinkedInAccount(w http.ResponseWriter, r *http.Request) {
	var body crmcontracts.SaveLinkedInAccountRequest
	if !httperr.Decode(w, r, &body) {
		return
	}
	account, err := h.store.SaveMyLinkedInAccount(r.Context(), SaveMyLinkedInAccountInput{
		ProfileURL: derefString(body.ProfileUrl),
		Connected:  body.Connected != nil && *body.Connected,
	})
	if err != nil {
		var input *DedupeInputError
		if errors.As(err, &input) {
			httperr.Write(w, r, httperr.Validation(input.Field, "invalid_profile_url", input.Msg))
			return
		}
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, linkedInAccountWire(account))
}

// linkedInAccountWire is the one place the account crosses to the wire, so the
// two handlers cannot describe the same row differently.
func linkedInAccountWire(a LinkedInAccount) crmcontracts.LinkedInAccount {
	return crmcontracts.LinkedInAccount{
		Connected:   a.ConnectedAt != nil,
		ConnectedAt: a.ConnectedAt,
		ProfileUrl:  a.ProfileURL,
		Connections: a.Connections,
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
