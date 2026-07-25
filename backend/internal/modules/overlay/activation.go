// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// Connect's two branches — inserting a fresh incumbent_connection row
// (insertConnection, connection.go) and reviving a revoked one
// (reconnectConnection, reconnect.go) — end at the same place: an active
// connection, audited, announced, and reflected in the workspace's mode.
// That destination is one invariant regardless of which path the row took
// to get there, so activateConnection spells it once rather than letting
// the two branches carry their own copy that could quietly drift apart.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// activateConnection finishes Connect's write-shape transaction for
// whichever branch called it: audit the row, emit incumbent.connected, and
// flip the workspace into overlay mode, then hand back the Connection the
// caller reports. action is the one thing a fresh insert and a revived row
// genuinely disagree about for the audit trail ("create" vs "update");
// before is the audit before-snapshot — nil for a first connect (there is
// no prior state to capture) or the revoked row's previous
// incumbent/region/status for a reconnect, since only the caller that just
// read that row FOR UPDATE knows it.
func activateConnection(ctx context.Context, tx pgx.Tx, id ids.UUID, in ConnectInput, connectedAt time.Time, action string, before map[string]any) (Connection, error) {
	after := map[string]any{
		auditFieldIncumbent: in.Incumbent,
		auditFieldRegion:    in.Region,
		"scopes":            leastPrivilegeHubSpotScopes,
		auditFieldStatus:    statusActive,
	}
	auditID, auditErr := storekit.Audit(ctx, tx, action, "incumbent_connection", id, before, after)
	if auditErr != nil {
		return Connection{}, fmt.Errorf("overlay: auditing the incumbent connection: %w", auditErr)
	}
	if emitErr := storekit.EmitEvent(ctx, tx, auditID, id,
		incumbentConnectedPayload(in.Incumbent, in.Region, leastPrivilegeHubSpotScopes, statusActive)); emitErr != nil {
		return Connection{}, fmt.Errorf("overlay: emitting incumbent.connected: %w", emitErr)
	}
	if _, updErr := tx.Exec(ctx, `
		UPDATE workspace SET x_sor_mode = 'overlay', x_incumbent = $1
		WHERE id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`,
		in.Incumbent); updErr != nil {
		return Connection{}, fmt.Errorf("overlay: flipping the workspace to overlay mode: %w", updErr)
	}
	return Connection{
		Incumbent:   in.Incumbent,
		Region:      in.Region,
		Status:      statusActive,
		ConnectedAt: connectedAt,
		Scopes:      leastPrivilegeHubSpotScopes,
	}, nil
}
