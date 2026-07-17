// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep-read transport: queue a whole-site crawl for an organization and
// poll its report. The handlers are wired in stages — the store and the crawl
// job land behind them — so an unwired role answers the repo's standard
// explicit 501, never a silent no-op.

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// siteReadHandlers shadows the generated DeepReadCompany / GetSiteRead stubs.
// Both fields nil until the deep-read job wiring lands (WithDeepRead).
type siteReadHandlers struct {
	start  func(w http.ResponseWriter, r *http.Request, id openapi_types.UUID)
	report func(w http.ResponseWriter, r *http.Request, id, readID openapi_types.UUID)
}

func (h siteReadHandlers) DeepReadCompany(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if h.start == nil {
		httperr.NotImplemented(w, r, "deepReadCompany (no crawl runner configured)")
		return
	}
	h.start(w, r, id)
}

func (h siteReadHandlers) GetSiteRead(w http.ResponseWriter, r *http.Request, id, readID openapi_types.UUID) {
	if h.report == nil {
		httperr.NotImplemented(w, r, "getSiteRead (no crawl runner configured)")
		return
	}
	h.report(w, r, id, readID)
}
