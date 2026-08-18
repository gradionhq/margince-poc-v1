// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"mime"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
)

// Which routes carry FILES, and may therefore read a body wider than the JSON
// bound every other route rides.
//
// Declared here because it is route knowledge, and routes are composed here.
// The list is short on purpose and is the whole grant: a route absent from it
// cannot obtain the wider bound by any means, including claiming to be
// multipart. That is the property being protected — several handlers in this
// tree decode `r.Body` with no bound of their own, and two of those routes are
// unauthenticated (DCR and login). Widening on the sender's Content-Type alone
// would have handed them the file bound each for the price of a header.
//
// The operator sets the NUMBERS (OPS-CFG-12, `uploads` in margince.yaml); this
// function decides which route gets which one. Nobody chooses the list at run
// time, which is what keeps the paragraph above true.
//
// Paths are as the generated router mounts them, under the /v1 prefix the
// chassis sees. TestEveryUploadRouteIsDeclared derives the expected set from
// the contract, so a new upload route cannot be added without appearing here.
func uploadCeilings(limits deployconfig.UploadLimits) map[string]int64 {
	return map[string]int64{
		"/v1/attachments":             limits.Attachment,     // uploadAttachment
		"/v1/imports/sources":         limits.CSVImport,      // uploadImportSource
		"/v1/me/linkedin-connections": limits.LinkedInImport, // importLinkedInConnections
	}
}

// bodyCeilingFor is the chassis's BodyCeiling for this composition.
//
// THREE conditions, all required, because each closes a different door: the
// method, so a GET cannot carry a wide body; the route, so only a declared
// upload route can; and the media type, so an upload route handed JSON still
// rides the tight bound. A request failing any of them gets the JSON ceiling.
//
// The path is matched exactly as the router mounts it, with no normalization of
// our own. A trailing slash or an un-cleaned segment is a path the router does
// not serve, and guessing at how it would resolve one is how a grant ends up
// wider than the route list it was written from.
func bodyCeilingFor(ceilings map[string]int64) httpserver.BodyCeiling {
	return func(r *http.Request) int64 {
		if r.Method != http.MethodPost {
			return httperr.MaxBodyBytes
		}
		ceiling, declared := ceilings[r.URL.Path]
		if !declared {
			return httperr.MaxBodyBytes
		}
		// Compared as a parsed MEDIA TYPE, not as a prefix. A prefix match also
		// accepts `multipart/form-dataX`, which `ParseMultipartForm` then rejects —
		// so the chassis and the parser would disagree about what a multipart body
		// is, and the sender would pick which one won.
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			return httperr.MaxBodyBytes
		}
		return ceiling
	}
}
