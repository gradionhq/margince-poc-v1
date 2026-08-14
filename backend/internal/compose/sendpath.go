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
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
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
	// Delivery records an accepted message for transmission, in either shape a
	// message takes. Nil means the send path refuses rather than log an
	// activity nothing will carry.
	Delivery DeliveryMachinery
	// SendAuthority pre-flights the credential the transport is about to use —
	// the sender's own mail grant, or the workspace's bot binding. Nil skips
	// the advisory check; transmission still refuses on a missing credential.
	SendAuthority activities.SendAuthority
	// ChannelRecipients resolves who a channel conversation is with. Like
	// DraftOutcome below, nil is NOT "off": withPoolDefaults substitutes the
	// pool-backed resolver, because the question is answerable from the
	// database alone and a role that could omit it would refuse every reply
	// with a wiring fault.
	ChannelRecipients activities.ChannelReachability
	// DraftOutcome closes the voice learning loop for a send that carries a
	// served draft's reference. Unlike its neighbours, nil is NOT "off": both
	// projections below substitute the pool-backed recorder, because the
	// recorder already answers every case it must refuse — an unknown
	// reference, an erased signal, a non-human sender — with silence. An
	// absent one is silent in exactly the same way, so "unwired" is not a
	// safer deployment, only an indistinguishable one.
	DraftOutcome activities.DraftOutcomeRecorder
	// ScheduleTimer wakes a message the rep chose to send later. Nil means this
	// role refuses to defer a send rather than accept a moment nothing will
	// wake at — the same fail-closed rule Delivery follows, for the same
	// reason: a promise this surface cannot keep is worse than a refusal.
	ScheduleTimer activities.ScheduleTimer
}

// withPoolDefaults fills in what a role does not configure but the pool alone
// can supply. Both projections start from it, so they agree on the default the
// same way they agree on the configuration.
//
// The default is resolved HERE rather than by the process roles for the reason
// the unsubscribe linker is wired in sendStore below: the recorder needs
// nothing but the pool, so no role should be able to omit it, and one that
// hand-builds a SendPath (cmd/mcp, cmd/worker) never has to know the voice
// learning loop exists.
func (p SendPath) withPoolDefaults(pool *pgxpool.Pool) SendPath {
	if p.DraftOutcome == nil {
		p.DraftOutcome = ai.NewVoiceStore(InstallationDB(pool))
	}
	if p.ChannelRecipients == nil {
		p.ChannelRecipients = channelReachability{}
	}
	return p
}

// applySendPath projects the assembled send configuration onto the HTTP
// handlers' own store, once every option has run. It is the reconciliation the
// file comment above describes: the options record onto s.send, and this is
// where the transport that does NOT go through sendStore picks the same values
// up. Running it after the loop rather than inside each option is what makes
// the two stores agree by construction instead of by three options each
// remembering to do it twice.
//
// It takes the pool for the same reason sendStore does: the draft-outcome
// recorder is DERIVED from the pool rather than configured, so this side of
// the reconciliation must be able to build one.
func (s *Server) applySendPath(pool *pgxpool.Pool) {
	send := s.send.withPoolDefaults(pool)
	s.activitiesHandlers = s.activitiesHandlers.
		WithPublicBaseURL(send.PublicBaseURL).
		WithDelivery(send.Delivery).
		// One machinery, both staging shapes: a nil Delivery converts to a nil
		// channel stager too, so the reply surface fails closed exactly as the
		// mail surface does.
		WithChannelDelivery(send.Delivery).
		WithChannelReachability(send.ChannelRecipients).
		// The timer rides SendPath for the reason this file exists: scheduling
		// wired at one call site and not the other would be "send later" that
		// works on one transport and silently 500s on the next.
		WithScheduleTimer(send.ScheduleTimer).
		// Wired unconditionally, like the unsubscribe linker below: it needs
		// nothing but the caller's transaction, so a deployment cannot forget
		// it and leave an account-started send unable to resolve anyone.
		WithRecipientDirectory(recipientDirectory{}).
		WithSendAuthority(send.SendAuthority).
		WithDraftOutcome(send.DraftOutcome)
	decisions := s.approvalsHandlers
	for kind, late := range lateApprovalEffects {
		store, gate := sendStore(pool, send), consentGateFor(pool)
		decisions = decisions.WithLateEffect(kind,
			func(svc *approvals.Service) approvals.ApprovedEffect {
				return late.effect(svc, store, gate, send.Delivery)
			},
			late.precheck(store, gate, send.Delivery))
	}
	s.approvalsHandlers = decisions
}

// consentGateFor is the ONE spelling of the send path's consent authority.
//
// Every send in this process must ask the same gate the same way: a second
// construction that differed — a different store, a different db handle — would
// be a second answer to "may this person be written to", and the surface that
// got the wrong one would look identical to the one that got the right one.
//
// The concrete gate rather than the activities.ConsentGate seam it satisfies:
// this is composition naming a dependency, every caller assigns it into the
// seam itself, and widening here would only hide which gate was built.
func consentGateFor(pool *pgxpool.Pool) *consent.Gate {
	return consent.NewGate(consent.NewStore(InstallationDB(pool)))
}

// lateApprovalEffect is one kind's approve-side pair: the executor that runs
// after the decision commits, and the preflight that runs before it does.
//
// They travel together because they are two readings of ONE question — can this
// be released — and a kind that registered the executor without the preflight
// would answer it only where the answer is too late to act on.
type lateApprovalEffect struct {
	effect   func(*approvals.Service, *activities.Store, activities.ConsentGate, activities.DeliveryStager) approvals.ApprovedEffect
	precheck func(*activities.Store, activities.ConsentGate, activities.DeliveryStager) approvals.ReleasePrecheck
}

// lateApprovalEffects are the approve-side pairs that cannot be registered with
// the others, because they send.
//
// The list beside the rest (approvalsServiceWithEffects) runs when the approvals
// surface is built, which is BEFORE server options assemble the send path — so
// an executor registered there would send through a store with no signature, no
// unsubscribe linker and no send authority. It would work, and put out mail
// visibly worse than the same human's own send, with nothing failing to say so.
//
// A table rather than a call, so the things that must agree cannot drift:
// applySendPath registers exactly these kinds and the version-pin census gate
// enumerates exactly these keys. A waiver for a late-registered kind would
// otherwise read as a waiver for a kind nothing stages.
var lateApprovalEffects = map[string]lateApprovalEffect{
	automation.HeldDraftKind: {
		effect:   heldDraftReleaseEffect,
		precheck: heldDraftPrecheck,
	},
}

// sendStore builds the activities store the tool surface sends through — one
// of the two the file comment names, not a shared one. The unsubscribe linker
// needs nothing but the pool, so it is wired here rather than carried in
// SendPath: a deployment cannot forget to pass it. The draft-outcome recorder
// is the same kind of dependency, which is why SendPath's field for it is an
// override and never the source of the default.
func sendStore(pool *pgxpool.Pool, send SendPath) *activities.Store {
	send = send.withPoolDefaults(pool)
	return activities.NewStore(InstallationDB(pool)).
		WithUnsubscribe(preferenceLinkAdapter{store: consent.NewStore(InstallationDB(pool))}).
		WithPublicBaseURL(send.PublicBaseURL).
		WithSendAuthority(send.SendAuthority).
		WithChannelReachability(send.ChannelRecipients).
		WithRecipientDirectory(recipientDirectory{}).
		// The sender's sign-off, wired here for the same reason the unsubscribe
		// linker is: a human reaching this store through a passport (ADR-0055
		// makes one a REST credential too) must not lose their signature merely
		// because the request arrived on the tool surface. An agent principal
		// still signs nothing — signedBody decides that, not this wiring.
		WithSignature(people.NewStore(InstallationDB(pool))).
		WithSenderName(identity.NewServiceFor(InstallationDB(pool))).
		WithDraftOutcome(send.DraftOutcome)
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
		store:         sendStore(pool, send),
		gate:          consentGateFor(pool),
		draft:         drafter,
		stager:        send.Delivery,
		channelStager: send.Delivery,
		timer:         send.ScheduleTimer,
	}
}
