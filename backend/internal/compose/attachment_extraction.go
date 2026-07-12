// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// attachmentExtractionHandlers backs the extraction-accept write. It is
// compose orchestration, not a single module's handler set (the coldstart
// readback precedent): persisting accepted fields onto a deal, flipping
// provenance, and auditing one activity note per field crosses the
// attachments/activities and deals module boundary in one request
// (RD-T10) — no module may own that write alone (ADR-0054 §3, "a module
// never imports a sibling"). Stays an explicit 501 until that
// orchestration is wired.
type attachmentExtractionHandlers struct{}

func (attachmentExtractionHandlers) AcceptAttachmentExtraction(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "AcceptAttachmentExtraction")
}
