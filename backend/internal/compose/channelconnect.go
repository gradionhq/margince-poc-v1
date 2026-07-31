// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The /channel-connections wiring (telegram-oa design v2 §4): the workspace-level
// Telegram bot binding. Ingress PULLS, so this surface needs nothing about where
// this installation can be reached — only the vault that seals the bot token,
// which WithKeyvault hands to the transport composed here.
//
// WithChannelSurface must therefore be applied BEFORE WithKeyvault, the same
// ordering contract WithOverlayBackfillLimit states, and cmd/api holds it.

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
)

// WithChannelSurface composes the channel-connection transport. It takes no
// deployment configuration at all: connecting a bot needs a token and a vault,
// and a poll dials out, so there is nothing about this installation's own address
// for an operator to get wrong.
//
// The vault arrives later, through WithKeyvault — until then the read surface
// lists what is bound and every mutating path refuses by name
// (`channel_credentials_not_configured`) rather than sealing a token nothing
// could unseal.
func WithChannelSurface() Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.channelHandlers = capture.NewChannelHandlers(
			capture.NewChannelStore(pool, nil, telegram.NewAPI(nil, ""), s.log))
	}
}
