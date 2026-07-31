// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The channel-connection READ surface. It is split from the lifecycle files
// (channelconn.go, channelconnedit.go) because it holds the one posture they do
// not: every role may read whether the channel is live, while binding, rotating
// and disconnecting are admin-only. Neither read touches a vault ref — the shape
// they return leaves both refs at their zero value.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// List returns the workspace's live channel connections, newest first. Read is
// granted to every role: a rep needs to see whether the channel is live, the
// same as an overlay connection's status.
func (s *ChannelStore) List(ctx context.Context) ([]ChannelConnection, error) {
	if err := auth.Require(ctx, channelConnectionObject, principal.ActionRead); err != nil {
		return nil, err
	}
	var out []ChannelConnection
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+channelConnectionColumns+
			` FROM channel_connection WHERE archived_at IS NULL ORDER BY created_at DESC, id DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			conn, err := scanChannelConnection(rows)
			if err != nil {
				return err
			}
			out = append(out, conn)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: listing channel connections: %w", err)
	}
	return out, nil
}

// Get returns one live channel connection. An archived, absent, or
// other-workspace row reads as ErrNotFound — existence-hiding, and an archived
// connection is no longer a connection.
func (s *ChannelStore) Get(ctx context.Context, id ids.UUID) (ChannelConnection, error) {
	if err := auth.Require(ctx, channelConnectionObject, principal.ActionRead); err != nil {
		return ChannelConnection{}, err
	}
	row, err := s.readChannelRow(ctx, id)
	if err != nil {
		return ChannelConnection{}, err
	}
	return row.ChannelConnection, nil
}
