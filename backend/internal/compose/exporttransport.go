// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// GET /overlay/export — the workspace export bundle's HTTP surface, and
// the flip's pre-flip export producer (B-E18.26's "honest-scope export
// available" gate reads the audit row this writes).
//
// It lives on the overlay lifecycle path deliberately: the general
// export-run lifecycle (IEM-WIRE-1/2 — enqueue, poll, fetch from the
// blob store) is the import-export-migration chapter's own unminted
// contract surface, and inventing it here would pre-empt that
// extension. This op is the cutover operator's bundle producer, gated
// like the rest of the lifecycle (overlay_connection UPDATE, admin/ops)
// and streamed inline rather than staged.

import (
	"bytes"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type overlayExportHandlers struct {
	writer *ExportWriter
}

func newOverlayExportHandlers(pool *pgxpool.Pool) overlayExportHandlers {
	return overlayExportHandlers{writer: NewExportWriter(pool)}
}

// DownloadOverlayExport streams the bundle. The body is written directly
// to the response, so a mid-stream failure cannot be turned into a
// problem document — the bundle is assembled into the ResponseWriter
// only after the gate passes, and a write failure surfaces as a
// truncated download plus a server-side error, never a 200 that claims
// completeness it does not have.
func (h overlayExportHandlers) DownloadOverlayExport(w http.ResponseWriter, r *http.Request) {
	if h.writer == nil {
		httperr.NotImplemented(w, r, "downloadOverlayExport")
		return
	}
	if err := auth.Require(r.Context(), "overlay_connection", principal.ActionUpdate); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var buf bytes.Buffer
	if _, err := h.writer.WriteBundle(r.Context(), &buf); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="margince-export.zip"`)
	if _, err := w.Write(buf.Bytes()); err != nil {
		httperr.Write(w, r, err)
	}
}
