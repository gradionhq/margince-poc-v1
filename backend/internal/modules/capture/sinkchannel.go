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
// to (design D2 — the person is ownerless), and no company is derived here even
// when an address rides along — a corroborating address is evidence about WHO
// this is, and reading an employer out of it would be the mail ladder's job,
// which this path deliberately bypasses.
type EnsureChannelRequest struct {
	Identity    connector.ChannelIdentity
	DisplayName string // the provider's own name for the sender — untrusted text
	// CorroboratingEmail is the sender's address where the provider knew one and
	// the source declared the email merge key (admitCounterpartyKeys). It names
	// nobody — Identity does that — and reaches only the resolution ladder and
	// the person's own address list. Empty for a transport that holds no
	// address, which is every core channel connector.
	CorroboratingEmail string
	ActivityID         ids.UUID
	Source             string
	CapturedBy         string
}

// WithChannelEnsurer returns a copy wired to the channel auto-create path. A
// nil ensurer keeps channel capture activity-only (a role that wired no
// resolver), exactly as a nil mail ensurer does.
func (s *Sink) WithChannelEnsurer(ensurer ChannelCounterpartyEnsurer) *Sink {
	c := *s
	c.channelEnsurer = ensurer
	return &c
}

// counterpartyShape is how a record NAMES its human, which is a different
// question from what evidence it carries about them — and this is the one place
// capture asks it, so the question is total rather than a boolean.
//
// A record holding both a channel identity and an address is named by the
// IDENTITY. That is precedence, not a coin toss: the identity is the key a reply
// is routed on and the one a person is bound by, while an address can only
// corroborate. Reading it the other way round would classify the record as mail,
// which binds no channel identity and — because every mail gate keys off the
// address — would record no fault either, the one capture outcome that leaves no
// breadcrumb at all. The exhaustive switch keeps that impossible to reach by
// omission.
//
// Whether the record may carry that corroborating address at all is a separate
// question, asked by admitCounterpartyKeys against the source's declaration.
// Keeping the two apart is what lets a declared and an undeclared source agree
// about who the message is with while disagreeing about what may be matched on.
type counterpartyShape int

const (
	shapeNone counterpartyShape = iota
	shapeMail
	shapeChannel
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
	provider, account := cp.ChannelIdentity.Provider, cp.ChannelIdentity.ChannelUserID
	switch {
	case provider != "" && account != "":
		// Precedence, and it is ordered first on purpose: a complete channel
		// identity names the human whether or not an address rides along.
		return shapeChannel
	case provider != "" || account != "":
		// Half an identity is malformed however much else the record carries —
		// an address alongside it cannot complete it, because the missing half
		// is what the locks and suppression keys are built from.
		return shapeHalfChannel
	case cp.Email != "":
		return shapeMail
	default:
		return shapeNone
	}
}

// admitCounterpartyShape is the ONE gate on how a record names its human, and
// it runs at the edge — before Upsert opens its transaction.
//
// Placement is the whole point. Every other switch over the shape runs
// mid-transaction, after the activity, its audit row and its captured event are
// written, so a refusal there fails the entire capture and hands the connector a
// deterministic error it retries forever — the poison pill sinkensure.go's
// savepoint exists to contain. Here a shape this module cannot classify is
// turned away having cost nothing.
//
// It takes the shape rather than the Counterparty so the walk over the enum can
// reach the arm no Counterparty can produce: a shape added to the const block
// without an arm HERE is admitted silently and then met by an error downstream,
// which is exactly the ordering this function exists to prevent.
func admitCounterpartyShape(shape counterpartyShape) error {
	switch shape {
	case shapeNone, shapeMail, shapeChannel:
		// Well-formed; the channel arm is gated again inside the transaction,
		// under the account's own erasure lock.
		return nil
	case shapeHalfChannel:
		return ErrChannelIdentityIncomplete
	default:
		return fmt.Errorf("capture: unhandled counterparty shape %d", shape)
	}
}

// admitCounterpartyKeys is the second half of admission and runs beside the
// first, at the same edge: the shape says who the record NAMES, this says what
// it may be MATCHED on.
//
// It is a sibling rather than an arm of admitCounterpartyShape because that
// function deliberately takes the shape and not the Counterparty, so its switch
// can be walked across the whole enum including arms no Counterparty can
// produce. This one has to read the record itself, and merging the two would
// cost that walk.
//
// The gate is narrow on purpose. It asks only about an address CORROBORATING a
// human already named by a channel identity — never about the address a
// mail-shaped record is named by, which is that record's identity and belongs to
// no declaration. A source that never declared the key has the record refused.
//
// This runs for every caller of Upsert, not only for units. That is the point:
// the unit-facing refusal lives in the ingress gate where it can be attributed
// to a unit's own grammar, and this one holds the invariant for a core
// connector, a fixture or a backfill that never passes through there.
func admitCounterpartyKeys(cp connector.Counterparty) error {
	if counterpartyShapeOf(cp) != shapeChannel || cp.Email == "" {
		return nil
	}
	if !cp.MayCorroborateByEmail() {
		return ErrMergeKeyNotDeclared
	}
	return nil
}

// ErrChannelIdentityIncomplete refuses half a channel identity.
// ErrMergeKeyNotDeclared refuses an address offered as matching evidence by a
// source that never vouched for one. Both are sentinels rather than bare errors
// so the refusal can be asserted on, and so a caller can tell a malformed record
// from an infrastructural failure.
var (
	ErrChannelIdentityIncomplete = errors.New("capture: a channel identity needs both a provider and a channel account id")
	ErrMergeKeyNotDeclared       = errors.New("capture: the record carries an address to match on, but its source declared no email merge key")
)

// refuseErasedChannelAccount excludes an Art. 17 erasure from the transaction
// that makes a channel record durable. The eraser holds the same advisory lock
// across its purge and its suppression arming, so taking it here means an
// inbound record lands either wholly before the erasure or wholly after it,
// never inside.
//
// Landing inside it is not a near miss. The activity would commit after the
// erasure certified the subject scrubbed, and with no person link it matches
// the link-walking selector afterwards — and a record from a transport that
// holds no address, which is every core channel connector, carries no
// counterparty_email for the mail selector to find either. So no later erasure,
// subject-access or retention pass could ever find it, while the erasure's own
// audit tombstone records a clean scrub. A corroborating address narrows that
// window where one exists; it does not close it, because the transports most
// likely to be erased on are exactly the ones with no address to carry. The
// probe in people's EnsureChannelCounterparty runs after this commit and its
// refusal is mapped to nil by design, so it is the second gate and cannot be
// the only one.
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
		Identity:           cp.ChannelIdentity,
		DisplayName:        cp.DisplayName,
		CorroboratingEmail: cp.Email,
		ActivityID:         ref.ID,
		Source:             captureSource(rec),
		CapturedBy:         decision.capturedBy,
	})
	if err != nil {
		s.logEnsureFault(ctx, rec, err)
		return
	}
	// Nil unless a backfill page is running; a webhook-driven channel ingest
	// belongs to no run.
	pageProgressFrom(ctx).counted(ctx, outcome)
}
