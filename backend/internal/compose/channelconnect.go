// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The /channel-connections wiring (telegram-oa design §5): the workspace-level
// Telegram bot binding. It needs two things from two options — this
// installation's public origin (WithChannelWebhookBase, which composes the
// transport) and the vault that seals the bot token (WithKeyvault, which hands
// it to the transport already composed) — so the origin option must come first.
// That is the same ordering contract WithOverlayBackfillLimit already states,
// and cmd/api holds it.
//
// A role that declares no origin composes no channel transport at all, and the
// whole surface answers an honest 503 — the same declared-by-omission posture
// the OAuth connector transport takes on a role with no OAuth app.

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
)

// WithChannelWebhookBase composes the channel-connection transport over this
// installation's externally-reachable origin — the base the provider webhook URL
// is built on. cmd/api sources it from --api-base-url (the api's own origin)
// falling back to --public-base-url, the same rule the OAuth callback URL
// follows, because both must resolve to where the api actually serves.
//
// An EMPTY base is deliberately still composed: the read surface then lists what
// is bound, and connect refuses by name (`channel_public_base_url_unset`) rather
// than deriving an address from the request's Host header. A bot registered
// against an origin that does not reach us reads `connected` and then simply
// falls quiet, which is indistinguishable from a healthy channel nobody is
// messaging — so guessing is the one thing this path must never do.
//
// It must be applied BEFORE WithKeyvault, which supplies the credential
// custodian this transport seals with; cmd/api orders them that way.
func WithChannelWebhookBase(base string) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.channelHandlers = capture.NewChannelHandlers(
			capture.NewChannelStore(pool, nil, telegram.NewAPI(nil, ""), base, s.log))
	}
}
