// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migration

import (
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// ReasonIncumbentUnreachable is the blocking reason OVA-AC-6(a) names in
// prose: the incumbent connection is revoked/error, so no live read can
// pass. The flip preflight returns it in blocking[], and the direct
// importer is blocked by the same guard for the same reason — one
// constant, two callers, so the two paths can never drift apart.
const ReasonIncumbentUnreachable = "incumbent_unreachable"

// incumbentConnectionActive is the only connection status a live-read
// import may run under (OVA-WIRE-1 pins active|revoked|error).
const incumbentConnectionActive = "active"

// GuardIncumbentSource refuses a run that must read the incumbent over
// its API while the connection cannot serve one (OVA-AC-6 a): the caller
// gets a 409-mapped refusal naming the reason, and nothing is partially
// migrated because nothing starts.
func GuardIncumbentSource(connectionStatus string) error {
	if connectionStatus == incumbentConnectionActive {
		return nil
	}
	return fmt.Errorf("%s: the incumbent connection is %q, a live-read import cannot run: %w",
		ReasonIncumbentUnreachable, connectionStatus, apperrors.ErrConflict)
}
