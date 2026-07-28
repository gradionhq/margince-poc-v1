// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The outbound-send composition. Three transports enter the send path — the
// HTTP handler, the MCP send_email tool, and the automation send action — and
// all three call activities.Store.SendEmail. Everything that governs a send
// therefore hangs off the STORE, and this file is the ONE place that builds
// one, so a value configured for a transport cannot be a value the other two
// silently do without.

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
)

// SendPath is the deployment configuration the send path needs and cannot
// derive from the pool: the canonical public origin a recipient's tokenized
// unsubscribe link resolves to, the delivery machinery an accepted message is
// staged with, and the mailbox pre-flight. It is a parameter rather than an
// option so that a process role which supplies none of it says so at the call
// site — the previous shape let a transport be composed with a bare store and
// look identical to a configured one.
type SendPath struct {
	// PublicBaseURL is configured at boot, never derived from a request. Empty
	// means a marketing send refuses (the store's fail-loud branch) rather
	// than emitting a forgeable link.
	PublicBaseURL string
	// Delivery records an accepted message for transmission. Nil means the
	// send path refuses rather than log an activity nothing will carry.
	Delivery activities.DeliveryStager
	// Mailbox pre-flights the sender's send grant. Nil skips the advisory
	// check; transmission still refuses on a missing grant.
	Mailbox activities.MailboxAuthority
}

// sendStore builds the activities store every send transport shares. The
// unsubscribe linker needs nothing but the pool, so it is wired here rather
// than carried in SendPath: a deployment cannot forget to pass it.
func sendStore(pool *pgxpool.Pool, send SendPath) *activities.Store {
	return activities.NewStore(pool).
		WithUnsubscribe(preferenceLinkAdapter{store: consent.NewStore(pool)}).
		WithPublicBaseURL(send.PublicBaseURL).
		WithMailbox(send.Mailbox)
}

// newCommsAdapter builds the comms seam both the MCP tool registry and the
// automation executors receive. Both call THIS — a second construction site
// with its own store is how the tool surface came to transmit marketing mail
// with no List-Unsubscribe header while the HTTP transport carried one.
func newCommsAdapter(pool *pgxpool.Pool, drafter activities.EmailDrafter, send SendPath) commsAdapter {
	return commsAdapter{
		store:  sendStore(pool, send),
		gate:   consent.NewGate(consent.NewStore(pool)),
		draft:  drafter,
		stager: send.Delivery,
	}
}
