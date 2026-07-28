// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The outbound-send composition. Two transports enter the send path — the
// HTTP handler and the MCP send_email tool — and both call
// activities.Store.SendEmail, so everything that governs a send hangs off the
// STORE. (A deterministic automation cannot send: its send_email action stages
// an approval, and the automation module's own Comms seam declares DraftEmail
// alone — automation/seams.go.)
//
// There are TWO stores, not one, and no single constructor can build both: the
// HTTP handlers carry their own (server.go's activities.NewHandlers, which
// also wires the public-booking seams no tool surface has), while sendStore
// below builds the one the tool surface sends through.
//
// SendPath is what keeps them from forking. It is the ONE record of how this
// role sends: every option writes only to it, sendStore reads only from it,
// and applySendPath projects it onto the HTTP handlers once the options have
// finished. A send value configured anywhere else — set directly on one
// store — reaches one transport and not the others, which is exactly the
// drift this shape exists to make impossible.

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

// applySendPath projects the assembled send configuration onto the HTTP
// handlers' own store, once every option has run. It is the reconciliation the
// file comment above describes: the options record onto s.send, and this is
// where the transport that does NOT go through sendStore picks the same values
// up. Running it after the loop rather than inside each option is what makes
// the two stores agree by construction instead of by three options each
// remembering to do it twice.
func (s *Server) applySendPath() {
	s.activitiesHandlers = s.activitiesHandlers.
		WithPublicBaseURL(s.send.PublicBaseURL).
		WithDelivery(s.send.Delivery).
		WithMailbox(s.send.Mailbox)
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
// automation executors receive. Both call THIS: a second construction site
// with its own store would let the tool surface transmit marketing mail with
// no List-Unsubscribe header while the HTTP transport carried one.
//
// The automation executors pass a zero SendPath, which is a statement rather
// than an omission: only DraftEmail is reachable through automation.Comms, so
// that surface has no send to configure.
func newCommsAdapter(pool *pgxpool.Pool, drafter activities.EmailDrafter, send SendPath) commsAdapter {
	return commsAdapter{
		store:  sendStore(pool, send),
		gate:   consent.NewGate(consent.NewStore(pool)),
		draft:  drafter,
		stager: send.Delivery,
	}
}
