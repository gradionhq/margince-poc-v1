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
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type overlayExportHandlers struct {
	writer *ExportWriter
	log    *slog.Logger
}

func newOverlayExportHandlers(pool *pgxpool.Pool, log *slog.Logger) overlayExportHandlers {
	return overlayExportHandlers{writer: NewExportWriter(pool), log: log}
}

// DownloadOverlayExport streams the bundle straight to the response:
// the estate can be large (it carries the whole audit log and, in
// overlay mode, the whole mirror), and buffering it in the API process
// would put the workspace's size between a caller and every other
// request this process is serving.
//
// Streaming costs the ability to answer a problem document once the
// first byte is out. That trade is deliberate and the failure is still
// honest: the gate runs BEFORE any header is written, so a refusal is a
// clean 4xx, and a mid-stream failure aborts the connection — the
// client sees a truncated, invalid zip rather than a 200 that claims a
// completeness it does not have. It is logged server-side; writing a
// problem body after the body has begun would only corrupt the archive
// further.
func (h overlayExportHandlers) DownloadOverlayExport(w http.ResponseWriter, r *http.Request) {
	if h.writer == nil {
		httperr.NotImplemented(w, r, "downloadOverlayExport")
		return
	}
	if err := auth.Require(r.Context(), "overlay_connection", principal.ActionUpdate); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="margince-export.zip"`)
	if _, err := h.writer.WriteBundle(r.Context(), w); err != nil {
		h.log.ErrorContext(r.Context(), "overlay export: the bundle failed mid-stream; the client's download is truncated", "err", err)
	}
}
