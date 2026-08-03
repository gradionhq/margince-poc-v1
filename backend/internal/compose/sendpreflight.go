// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The request-time send pre-flight, over the SAME capture registry the connect
// flows write to (telegram-oa design §8.1). It sits apart from the transmit lane
// in commsjobs.go because it answers a different question at a different moment:
// not "which credential carries this delivery", but "is there a credential at
// all", asked while the person who can do something about the answer is still on
// the screen.
//
// Which table holds that credential is the whole subtlety, and it is why this is
// one type with one branch rather than two authorities: a mailbox is one human's
// OAuth grant in capture_connection, a bot is the workspace's binding in
// channel_connection, and a pre-flight that asked the mailbox question about a bot
// refuses every channel reply with a 422 about a mailbox nobody mentioned.

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// mailboxGrants is the credential-free half of the capture lookup the transmit
// lane uses. The pre-flight deliberately does NOT take mailboxSenders: resolving
// the credential would unseal a secret to answer a question about it, spending
// a vault round trip on every send request and turning a keyvault blip into a
// user-facing refusal — where the delivery path classifies the identical fault
// as transient and retries it.
type mailboxGrants interface {
	GrantedScopesFor(ctx context.Context, userID ids.UserID, provider string) ([]string, error)
	// ChannelSendCapable is the same question for a WORKSPACE-level channel
	// binding, which lives in a different table and is keyed on no user at all
	// — so it cannot be answered by the scope lookup above, and a pre-flight
	// that tried would report every channel send as ungranted.
	ChannelSendCapable(ctx context.Context, provider string) (bool, error)
}

var _ mailboxGrants = (*capture.Registry)(nil)

// mailboxAuthority answers the request-time pre-flight over the SAME registry
// the connect flow writes to, so what the operator just connected is what the
// check reads.
//
// The provider comes from the CALLER, not from this value, because one
// installation sends through both kinds of credential and the two are held
// differently: a mailbox is one human's OAuth grant, a bot is bound once for the
// whole workspace. A provider fixed at construction answered the wrong question
// for whichever transport it was not built for.
//
// For a mailbox it asks about the GRANT, not the connection. Every mailbox
// connected before the send scope existed holds read-only access until its owner
// reconnects, so a check that only asked "is something connected?" would pass all
// of them and then park every send. For a bot there is no grant to ask about —
// the token IS the authority — so the live binding is the whole answer.
//
// It also carries the one fact the registry cannot supply: whether this
// DEPLOYMENT configured a mail app for the provider at all. A grant is the
// user's, the app that transmits under it is the operator's, and a mailbox
// whose app was never configured reports its send scope exactly as a working
// one does — so without this the pre-flight admits a send that can only park.
type mailboxAuthority struct {
	grants mailboxGrants
	// mailAppConfigured answers that deployment question. It is deliberately
	// NOT "is a mail connector registered on this process role": the api
	// self-gates its Gmail transport on a state key it does not need to
	// transmit, so a deployment whose worker sends perfectly well has no
	// api-side gmail connector — and keying the refusal on the role's registry
	// would refuse every Gmail send there.
	mailAppConfigured func(provider string) bool
}

var _ activities.SendAuthority = mailboxAuthority{}

// mailAppConfigured is the Server's answer to that question. It reads the field
// at CALL time rather than snapshotting it, so the two places that install the
// pre-flight need no ordering rule against the option that records the fact.
//
// A provider with no field is not configured, which is also the only honest
// answer: comms.SendScopeFor gives a send scope to gmail alone, so no other
// provider reaches this at all.
func (s *Server) mailAppConfigured(provider string) bool {
	return provider == providerGmail && s.gmailAppConfigured
}

// installSendPreflight installs the pre-flight over whichever capture registry
// the caller has just ensured exists — the SAME one the connect flow writes to,
// never a second construction. Both installation sites go through here so the
// registry and the deployment fact cannot be paired up two different ways.
func installSendPreflight(s *Server, pool *pgxpool.Pool) {
	WithSendAuthority(mailboxAuthority{
		grants:            s.connectorHandlers.registry,
		mailAppConfigured: s.mailAppConfigured,
	})(s, pool)
}

func (m mailboxAuthority) SendCapable(ctx context.Context, provider string) (bool, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		// Sending is a human act (comms.Store.StageTx enforces the same
		// rule) on either transport: the workspace's bot transmits, but a
		// principal with no app_user identity is nobody's reply, and is told so
		// here rather than at transmission.
		return false, nil
	}
	scope, capability := comms.SendScopeFor(provider)
	switch capability {
	case comms.CannotSend:
		return false, nil
	case comms.SendsWithoutScope:
		// The credential is the whole authority and it belongs to the
		// workspace, so the question is whether a bot is bound — asked of the
		// table that holds one. Reading the per-user grant here is what would
		// refuse every channel send: there is no capture_connection behind a
		// workspace binding to find.
		return m.grants.ChannelSendCapable(ctx, provider)
	}
	if !m.mailAppConfigured(provider) {
		// No grant this user holds can transmit through an app the deployment
		// does not have, so the grant is not even read: the answer is the same
		// either way, and the refusal names the mailbox while the operator is
		// still on the screen instead of parking the message later.
		return false, nil
	}
	granted, err := m.grants.GrantedScopesFor(ctx, ids.From[ids.UserKind](actor.UserID), provider)
	switch {
	case errors.Is(err, capture.ErrNoConnection):
		return false, nil
	case err != nil:
		// A pre-flight that cannot ask must not answer. Reporting the fault
		// refuses the send loudly instead of asserting a grant nobody read.
		return false, err
	}
	return slices.Contains(granted, scope), nil
}
