// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The LinkedIn export upload (ADR-0078 §2.1b).

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
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
	if _, err := h.store.MatchLinkedInConnections(r.Context(), uploaderID(r.Context())); err != nil {
		writeStoreErr(w, r, err)
		return
	}
	// The TOTALS, not this pass's delta. The matcher only considers ghosts
	// nobody has decided on, so re-importing the same export truthfully
	// reports zero new matches while twenty-six sit in the database — and a
	// card labelled "Matched to a contact" showing 0 in that state is wrong.
	confirmed, suggested, err := h.store.MyLinkedInMatchTotals(r.Context())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.LinkedInImportSummary{
		Rows:      result.Rows,
		Imported:  result.Imported,
		Skipped:   result.Skipped,
		Confirmed: confirmed,
		Suggested: suggested,
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

// ListMyLinkedInConnections implements GET /me/linkedin-connections.
func (h Handlers) ListMyLinkedInConnections(w http.ResponseWriter, r *http.Request, params crmcontracts.ListMyLinkedInConnectionsParams) {
	in := ListMyLinkedInConnectionsInput{Cursor: params.Cursor, Limit: params.Limit}
	if params.MatchStatus != nil {
		status := string(*params.MatchStatus)
		in.MatchStatus = &status
	}
	rows, page, err := h.store.ListMyLinkedInConnections(r.Context(), in)
	if err != nil {
		writeLinkedInReviewErr(w, r, err)
		return
	}
	data := make([]crmcontracts.LinkedInConnection, 0, len(rows))
	for _, row := range rows {
		data = append(data, linkedInConnectionWire(row))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.LinkedInConnectionListResponse{
		Data: data,
		Page: linkedInPageWire(page),
	})
}

// ConfirmLinkedInMatch implements POST /me/linkedin-connections/{id}/confirm.
func (h Handlers) ConfirmLinkedInMatch(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	// The body is OPTIONAL — an empty request accepts the matcher's own
	// suggestion, which is the common case and should not require a payload.
	var body crmcontracts.ConfirmLinkedInMatchRequest
	// ContentLength is -1 on a chunked request, so testing for > 0 silently
	// dropped the caller's person_id and confirmed the matcher's guess instead.
	// Anything other than a declared-empty body is decoded.
	if r.ContentLength != 0 && !httperr.Decode(w, r, &body) {
		return
	}
	var person ids.UUID
	if body.PersonId != nil {
		person = ids.UUID(*body.PersonId)
	}
	decision, err := h.store.ConfirmLinkedInMatch(r.Context(), ids.UUID(id), person)
	if err != nil {
		writeLinkedInReviewErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, linkedInDecisionWire(decision))
}

// RejectLinkedInMatch implements POST /me/linkedin-connections/{id}/reject.
func (h Handlers) RejectLinkedInMatch(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	decision, err := h.store.RejectLinkedInMatch(r.Context(), ids.UUID(id))
	if err != nil {
		writeLinkedInReviewErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, linkedInDecisionWire(decision))
}

// GetMyLinkedInReach implements GET /me/linkedin-reach.
func (h Handlers) GetMyLinkedInReach(w http.ResponseWriter, r *http.Request, params crmcontracts.GetMyLinkedInReachParams) {
	reach, err := h.store.MyLinkedInReach(r.Context(), params.Limit)
	if err != nil {
		writeLinkedInReviewErr(w, r, err)
		return
	}
	accounts := make([]crmcontracts.LinkedInReachAccount, 0, len(reach.Accounts))
	for _, a := range reach.Accounts {
		accounts = append(accounts, crmcontracts.LinkedInReachAccount{
			OrganizationId: openapi_types.UUID(a.OrganizationID),
			DisplayName:    a.DisplayName,
			Connections:    a.Connections,
			ContactsOnFile: a.ContactsOnFile,
		})
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.LinkedInReachResponse{
		Accounts:              accounts,
		AccountsTotal:         reach.AccountsTotal,
		UnresolvedConnections: reach.UnresolvedConnections,
	})
}

// writeLinkedInReviewErr maps the review paths' input errors to 422 and leaves
// everything else to the shared store mapping. A bad match_status or a
// connection with nothing to confirm is the caller's mistake and they can fix
// it from the message; answering 500 would send them to support.
func writeLinkedInReviewErr(w http.ResponseWriter, r *http.Request, err error) {
	var input *DedupeInputError
	if errors.As(err, &input) {
		httperr.Write(w, r, httperr.Validation(input.Field, "invalid_value", input.Msg))
		return
	}
	writeStoreErr(w, r, err)
}

// linkedInConnectionWire is the one place a ghost crosses to the wire, so no
// handler can describe one differently — and so the fields that must NOT cross
// (the folded name and company the matcher compares on) are absent in exactly
// one place rather than remembered in four.
func linkedInConnectionWire(row LinkedInConnectionRow) crmcontracts.LinkedInConnection {
	out := crmcontracts.LinkedInConnection{
		Id:                openapi_types.UUID(row.ID),
		FullName:          row.FullName,
		Position:          row.Position,
		CompanyName:       row.CompanyName,
		Email:             row.Email,
		MatchStatus:       crmcontracts.LinkedInConnectionMatchStatus(row.MatchStatus),
		MatchedPersonName: row.MatchedPersonName,
		MatchedOrgName:    row.MatchedOrgName,
	}
	if row.MatchedPerson != nil {
		id := openapi_types.UUID(*row.MatchedPerson)
		out.MatchedPersonId = &id
	}
	if row.MatchedOrg != nil {
		id := openapi_types.UUID(*row.MatchedOrg)
		out.MatchedOrgId = &id
	}
	if row.ConnectedOn != nil {
		out.ConnectedOn = &openapi_types.Date{Time: *row.ConnectedOn}
	}
	return out
}

// linkedInPageWire renders the keyset page. An absent cursor is OMITTED rather
// than sent as an empty string: the contract types it as nullable, and a client
// that reads "" as a cursor would ask for a page that does not exist.
func linkedInPageWire(page storekit.Page) crmcontracts.PageInfo {
	out := crmcontracts.PageInfo{HasMore: page.HasMore}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	return out
}

func linkedInDecisionWire(d LinkedInMatchDecision) crmcontracts.LinkedInMatchDecision {
	return crmcontracts.LinkedInMatchDecision{
		Connection:        linkedInConnectionWire(d.Connection),
		ProfileUrlWritten: d.ProfileURLWritten,
	}
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
