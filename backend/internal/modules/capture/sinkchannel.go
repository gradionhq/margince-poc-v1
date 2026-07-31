// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The channel half of the counterparty auto-create follow-up (telegram-oa
// design §6.4). An inbound channel message names its human by a provider
// identity rather than an address, and the workspace bot that received it acts
// for no one human, so it needs its own seam and its own decision — not a flag
// on the mail contract.
//
// Capture still touches no person SQL: this is the same shape as the mail
// resolver seam, and compose injects the same people module behind both.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// ChannelCounterpartyEnsurer is the channel twin of CounterpartyEnsurer: after
// a captured channel activity commits, the pipeline ensures the human behind it
// exists — person only — through the ONE dedupe chokepoint.
type ChannelCounterpartyEnsurer interface {
	EnsureChannelCounterparty(ctx context.Context, req EnsureChannelRequest) (EnsureOutcome, error)
}

// EnsureChannelRequest names one inbound channel message's counterparty for the
// resolver. It carries no OwnerID and no SuppressOrg, and the omissions are the
// design: a workspace bot has no granting human for anything created to belong
// to (design D2 — the person is ownerless), and a channel identity carries no
// mail domain a company could be derived from in the first place.
type EnsureChannelRequest struct {
	Identity    connector.ChannelIdentity
	DisplayName string // the provider's own name for the sender — untrusted text
	ActivityID  ids.UUID
	Source      string
	CapturedBy  string
}

// WithChannelEnsurer returns a copy wired to the channel auto-create path. A
// nil ensurer keeps channel capture activity-only (a role that wired no
// resolver), exactly as a nil mail ensurer does.
func (s *Sink) WithChannelEnsurer(ensurer ChannelCounterpartyEnsurer) *Sink {
	c := *s
	c.channelEnsurer = ensurer
	return &c
}

// counterpartyShape is how a record names its human. connector.Counterparty
// documents an address and a channel identity as mutually exclusive, and this is
// the one place capture asks which it holds — so the question is asked totally.
// A two-term boolean answered "not a channel record" for a record carrying BOTH,
// which then ran the mail ladder: the channel identity was never bound, and
// because every mail gate keys off the address it produced no fault row either.
// A silent misroute is the one outcome in this pipeline that leaves no
// breadcrumb, so the malformed shape gets a name and is refused by the caller.
type counterpartyShape int

const (
	shapeNone counterpartyShape = iota
	shapeMail
	shapeChannel
	shapeAmbiguous
)

func counterpartyShapeOf(cp connector.Counterparty) counterpartyShape {
	hasMail, hasChannel := cp.Email != "", cp.ChannelIdentity.ChannelUserID != ""
	switch {
	case hasMail && hasChannel:
		return shapeAmbiguous
	case hasChannel:
		return shapeChannel
	case hasMail:
		return shapeMail
	default:
		return shapeNone
	}
}

// decideChannelCounterparty settles a channel record's derivation, and unlike
// the mail ladder it records nothing inside the capture transaction: the
// disposition ledger is address-keyed, and there is no ambiguous class to
// defer. A human opening a conversation with the workspace's own bot IS the
// affirmative intent the T1 tier goes looking for evidence of — nobody messages
// a company's bot by accident, and a bot cannot be cold-mailed.
func (s *Sink) decideChannelCounterparty(ctx context.Context) counterpartyDecision {
	if s.channelEnsurer == nil {
		return counterpartyDecision{}
	}
	// The granting human the mail path owns its rows through is deliberately
	// dropped: a channel connection's connected_by is audit-only (design §4.1),
	// and reusing that admin here is exactly what would produce the owned record
	// D2 refuses. owner stays zero, and the created person stays ownerless.
	actor, _ := capturePrincipal(ctx)
	return counterpartyDecision{create: true, channel: true, capturedBy: actor.ID}
}

// ensureChannelCounterparty is the auto-create follow-up for one freshly
// captured channel activity. Like its mail sibling it runs after the capture
// transaction committed and NEVER fails the capture — a fault lands in
// system_log for the nightly reconcile, and the link-less connector activity is
// the retry marker.
func (s *Sink) ensureChannelCounterparty(ctx context.Context, rec connector.NormalizedRecord, ref datasource.EntityRef, decision counterpartyDecision) {
	cp := rec.Counterparty
	outcome, err := s.channelEnsurer.EnsureChannelCounterparty(ctx, EnsureChannelRequest{
		Identity:    cp.ChannelIdentity,
		DisplayName: cp.DisplayName,
		ActivityID:  ref.ID,
		Source:      captureSource(rec),
		CapturedBy:  decision.capturedBy,
	})
	if err != nil {
		s.logEnsureFault(ctx, rec, err)
		return
	}
	// Nil unless a backfill page is running; a webhook-driven channel ingest
	// belongs to no run.
	pageProgressFrom(ctx).counted(ctx, outcome)
}
