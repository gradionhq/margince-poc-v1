// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The export endpoint's two gates, asserted where they run: before a
// single byte of the bundle — the whole workspace, audit log included —
// reaches the wire. Both refusals are pinned because deleting either
// line otherwise leaves the suite green.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// exportRequest builds a GET carrying the given principal.
func exportRequest(actor principal.Principal) *http.Request {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, actor)
	return httptest.NewRequest(http.MethodGet, "/v1/overlay/export", nil).WithContext(ctx)
}

func TestDownloadOverlayExportRefusesBeforeStreaming(t *testing.T) {
	// A non-nil writer over a nil pool: if either gate let a request
	// through, the handler would panic reaching for the database —
	// which is itself the assertion that nothing streams past a refusal.
	h := overlayExportHandlers{writer: NewExportWriter(nil), log: slog.New(slog.DiscardHandler)}

	t.Run("a seat without the overlay_connection grant", func(t *testing.T) {
		user := ids.NewV7()
		w := httptest.NewRecorder()
		h.DownloadOverlayExport(w, exportRequest(principal.Principal{
			Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
			SeatType: principal.SeatFull,
			Permissions: principal.Permissions{
				RoleKeys: []string{"rep"},
				Objects:  map[string]principal.ObjectGrant{"overlay_connection": {Read: true}},
				RowScope: principal.RowScopeAll,
			},
		}))
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 — the export is admin/ops config, not a rep read", w.Code)
		}
	})

	t.Run("an agent principal holding the grant", func(t *testing.T) {
		user := ids.NewV7()
		w := httptest.NewRecorder()
		h.DownloadOverlayExport(w, exportRequest(principal.Principal{
			Type: principal.PrincipalAgent, ID: "agent:exfil", UserID: user,
			SeatType: principal.SeatFull,
			Permissions: principal.Permissions{
				RoleKeys: []string{"admin"},
				Objects:  map[string]principal.ObjectGrant{"overlay_connection": {Create: true, Read: true, Update: true, Delete: true}},
				RowScope: principal.RowScopeAll,
			},
		}))
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 — a full-estate bundle is human-only even for a passport that inherits the grant", w.Code)
		}
	})
}
