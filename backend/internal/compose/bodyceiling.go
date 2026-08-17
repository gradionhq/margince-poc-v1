// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"mime"
	"net/http"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// Which routes carry FILES, and may therefore read a body wider than the JSON
// bound every other route rides.
//
// Declared here because it is route knowledge, and routes are composed here.
// The list is short on purpose and is the whole grant: a route absent from it
// cannot obtain the wider bound by any means, including claiming to be
// multipart. That is the property being protected — several handlers in this
// tree decode `r.Body` with no bound of their own and rely entirely on the
// chassis for one, and two of those routes are unauthenticated (DCR and login).
// Widening on the sender's Content-Type alone would have handed them 25 MiB
// each for the price of a header.
//
// Paths are as the generated router mounts them, under the /v1 prefix the
// chassis sees. Keep this in step with the multipart handlers themselves;
// TestEveryMultipartRouteIsDeclared derives the expected set from the tree so a
// new upload route cannot be added without either appearing here or failing.
var fileUploadRoutes = map[string]struct{}{
	"/v1/attachments":             {}, // uploadAttachment
	"/v1/imports/sources":         {}, // uploadImportSource
	"/v1/me/linkedin-connections": {}, // importLinkedInConnections
}

// bodyCeiling is the chassis's BodyCeiling for this composition.
//
// THREE conditions, all required, because each closes a different door: the
// method, so a GET cannot carry a wide body; the route, so only a declared
// upload route can; and the media type, so an upload route handed JSON still
// rides the tight bound. A request failing any of them gets the JSON ceiling.
func bodyCeiling(r *http.Request) int64 {
	if r.Method != http.MethodPost {
		return httperr.MaxBodyBytes
	}
	if _, ok := fileUploadRoutes[strings.TrimSuffix(r.URL.Path, "/")]; !ok {
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
	return httperr.MaxMultipartBodyBytes
}
