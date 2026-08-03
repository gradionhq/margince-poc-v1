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
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
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
// the one place capture asks which it holds — so the question is total rather
// than a boolean. A record carrying BOTH is malformed: classifying it as mail
// would bind no channel identity and, because every mail gate keys off the
// address, would record no fault either, making it the one capture outcome that
// leaves no breadcrumb at all. The exhaustive switch is what keeps that case
// impossible to reach by omission.
type counterpartyShape int

const (
	shapeNone counterpartyShape = iota
	shapeMail
	shapeChannel
	shapeAmbiguous
	shapeHalfChannel

	// shapeCount bounds the enum so a walk over it derives rather than repeats
	// the list. A shape appended above this line joins every such walk on its
	// own, which is what keeps the switches that must all answer for it from
	// drifting apart silently.
	shapeCount
)

// A channel identity needs BOTH halves, and shapeChannel means both are present.
// Provider is not cosmetic: it is hashed into the advisory lock key and the
// suppression key, so a provider-less identity would lock and probe a different
// key space than the eraser's and the gate below would pass while the eraser was
// mid-purge — the mutex would be decorative. people's ensure refuses the same
// half-identity; refusing it here keeps the two in step.
func counterpartyShapeOf(cp connector.Counterparty) counterpartyShape {
	hasMail := cp.Email != ""
	provider, account := cp.ChannelIdentity.Provider, cp.ChannelIdentity.ChannelUserID
	switch {
	case hasMail && (provider != "" || account != ""):
		return shapeAmbiguous
	case provider != "" && account != "":
		return shapeChannel
	case provider != "" || account != "":
		return shapeHalfChannel
	case hasMail:
		return shapeMail
	default:
		return shapeNone
	}
}

// ErrCounterpartyNamedTwice refuses a record naming its human both by an address
// and by a channel identity. ErrChannelIdentityIncomplete refuses half a channel
// identity. Both are sentinels rather than bare errors so the refusal can be
// asserted on, and so a caller can tell a malformed record from an
// infrastructural failure.
var (
	ErrCounterpartyNamedTwice    = errors.New("capture: a counterparty is named by an address or by a channel identity, never both")
	ErrChannelIdentityIncomplete = errors.New("capture: a channel identity needs both a provider and a channel account id")
)

// refuseErasedChannelAccount excludes an Art. 17 erasure from the transaction
// that makes a channel record durable. The eraser holds the same advisory lock
// across its purge and its suppression arming, so taking it here means an
// inbound record lands either wholly before the erasure or wholly after it,
// never inside.
//
// Landing inside it is not a near miss. The activity would commit after the
// erasure certified the subject scrubbed, and with no person link and no
// counterparty_email it matches neither erasure selector afterwards — so no
// later erasure, subject-access or retention pass could ever find it, while the
// erasure's own audit tombstone records a clean scrub. The probe in people's
// EnsureChannelCounterparty runs after this commit and its refusal is mapped to
// nil by design, so it is the second gate and cannot be the only one.
//
// The refusal deliberately names NO identifier. For a channel record the
// natural key embeds the account id itself (a private chat's id is the user's
// own id), so naming it would re-state in a log exactly what the erasure
// removed — the sibling mail guards can quote their natural key because a
// message-id is not the subject.
func (s *Sink) refuseErasedChannelAccount(ctx context.Context, tx pgx.Tx, cp connector.Counterparty) error {
	if counterpartyShapeOf(cp) != shapeChannel {
		return nil
	}
	ci := cp.ChannelIdentity
	if err := storekit.LockChannelIdentities(ctx, tx, []storekit.ChannelIdentityKey{
		{Provider: ci.Provider, ChannelUserID: ci.ChannelUserID},
	}); err != nil {
		return err
	}
	suppressed, err := storekit.ChannelIdentitySuppressed(ctx, tx, ci.Provider, ci.ChannelUserID)
	if err != nil {
		return err
	}
	if suppressed {
		return fmt.Errorf("capture: the record's channel account is on the erasure suppression list: %w", connector.ErrSkip)
	}
	return nil
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
