// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// previewRowLimit is pure, so it is proven here rather than behind a database.
// The integration suite covers the refusal reaching the wire as a 422 naming
// `limit`; what belongs in this lane is the boundary table — the default, both
// ends of the published range, and both sides of each end.

import (
	"errors"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

func TestPreviewRowLimitTakesTheRangeTheContractPublishes(t *testing.T) {
	limit := func(n int) *int { return &n }

	for _, c := range []struct {
		name      string
		requested *int
		want      int
		refused   bool
	}{
		// Absent is the documented default, and it is NOT the same answer as an
		// out-of-range value: a caller who named no size gets a glance, a caller
		// who named an impossible one gets told.
		{"absent", nil, filterPreviewDefaultRows, false},
		{"the floor", limit(1), 1, false},
		{"the ceiling", limit(filterPreviewMaxRows), filterPreviewMaxRows, false},
		{"just inside the ceiling", limit(filterPreviewMaxRows - 1), filterPreviewMaxRows - 1, false},
		{"zero", limit(0), 0, true},
		{"negative", limit(-1), 0, true},
		{"just past the ceiling", limit(filterPreviewMaxRows + 1), 0, true},
		{"far past the ceiling", limit(5000), 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := previewRowLimit(c.requested)
			if c.refused {
				if err == nil {
					t.Fatalf("limit %v was accepted as %d; the contract calls it invalid", c.requested, got)
				}
				// A refusal has to reach the caller as a 422 naming `limit`, not as
				// a 500 — so the error carries both, and this checks both rather
				// than settling for "an error happened".
				var detailed *httperr.DetailedError
				if !errors.As(err, &detailed) {
					t.Fatalf("err = %#v, want a *httperr.DetailedError so the transport can answer 422", err)
				}
				if detailed.Status != http.StatusUnprocessableEntity {
					t.Errorf("status = %d, want 422", detailed.Status)
				}
				if len(detailed.Fields) != 1 || detailed.Fields[0].Field != "limit" {
					t.Errorf("fields = %+v, want exactly one naming limit", detailed.Fields)
				}
				return
			}
			if err != nil {
				t.Fatalf("limit %v refused: %v", c.requested, err)
			}
			if got != c.want {
				t.Errorf("limit %v = %d, want %d", c.requested, got, c.want)
			}
		})
	}
}
