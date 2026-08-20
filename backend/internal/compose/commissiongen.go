// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cg:commissions consumer: a won deal becomes what its partner earned.
//
// The trigger is the event, not the writer. deal.stage_changed reaches the
// outbox because the write shape puts it there, so every path that can win a
// deal — a rep on the board, an approved agent proposal, an import — lands here
// without any of them knowing this consumer exists. Asking each writer to
// remember to accrue would guarantee one of them forgets, and the one that
// forgot would be invisible: nothing looks different about a partner who was
// never paid.
//
// It lives in compose because the call crosses three modules. deals owns the
// event, people owns the partner's margin tier, commissions owns the ledger,
// and a module never imports a sibling — so the edge is injected here.
//
// EVERYTHING IS READ FROM THE EVENT, not from the deal. A reopened deal clears
// its frozen FX rate, and its attribution can be edited after the win, so a
// read-back would price the accrual on facts that changed after the moment
// being paid for. The payload carries what was true when the deal was won,
// which is the only reading that stays true.
//
// Replay is handled by the ledger, not here: commission_entry.trigger_event_id
// is unique, so a redelivered event fails its insert instead of paying twice.
// The events.Dedupe wrapper is a cache in front of that, never the guarantee —
// it marks AFTER the effect runs, so a crash in that window replays.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/commissions"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// commissionEntityDeal is the outbox entity type this consumer reacts to.
const commissionEntityDeal = "deal"

// The deal statuses the accrual cares about. A win accrues; leaving won
// reverses what the win produced.
const (
	dealStatusWon = "won"
)

// CommissionGen turns won deals into ledger entries, and reverses them when a
// win is undone.
type CommissionGen struct {
	pool    *pgxpool.Pool
	ledger  *commissions.Store
	partner *people.Store
	log     *slog.Logger
}

// NewCommissionGen builds the accrual consumer over the ledger and the partner
// store that prices it.
func NewCommissionGen(pool *pgxpool.Pool, ledger *commissions.Store, partner *people.Store, log *slog.Logger) *CommissionGen {
	return &CommissionGen{pool: pool, ledger: ledger, partner: partner, log: log}
}

// HandleEvent routes one envelope. An event this consumer does not care about
// answers nil, so the group keeps flowing rather than wedging on somebody
// else's traffic.
func (g *CommissionGen) HandleEvent(ctx context.Context, env events.Envelope) error {
	if env.Entity.ID == ids.Nil || env.Entity.Type != commissionEntityDeal ||
		env.Type != deals.EventDealStageChanged {
		return nil
	}
	moved, err := deals.DecodeStageChanged(env.Payload)
	if err != nil {
		return err
	}
	// The workspace is this consumer's own; the envelope carries none.
	ws, err := InstallationDB(g.pool).Workspace(ctx)
	if err != nil {
		return err
	}
	ctx = g.accrualContext(ctx, env, ws.UUID)

	switch {
	case moved.ToStatus == dealStatusWon:
		return g.accrue(ctx, env, moved)
	case moved.FromStatus == dealStatusWon:
		// The win that produced an entry has been undone, so the money it
		// recorded is undone too — as a reversal beside the original, never by
		// deleting what a partner was already told they had earned.
		return g.reverse(ctx, env.Entity.ID, moved.ToStatus)
	}
	return nil
}

// accrue prices one win and records it.
func (g *CommissionGen) accrue(ctx context.Context, env events.Envelope, moved deals.StageChanged) error {
	if moved.PartnerOrgID == nil || moved.PartnerAttribution == nil {
		return nil
	}
	if moved.AmountMinor == nil || moved.Currency == nil {
		// A deal won without a figure earns a percentage of nothing. Skipping
		// is the honest answer; a zero-amount entry would state durably that
		// the partner earned nothing on a deal whose value nobody recorded.
		g.log.InfoContext(ctx, "commission: a won partner deal carries no amount, so nothing accrued",
			"deal", env.Entity.ID.String())
		return nil
	}

	tier, err := g.partner.MarginTierOf(ctx, ids.From[ids.OrganizationKind](*moved.PartnerOrgID))
	if err != nil {
		return err
	}
	_, err = g.ledger.Accrue(ctx, commissions.AccrueInput{
		DealID:         ids.From[ids.DealKind](env.Entity.ID),
		PartnerOrgID:   ids.From[ids.OrganizationKind](*moved.PartnerOrgID),
		TriggerEventID: &env.EventID,
		Attribution:    *moved.PartnerAttribution,
		MarginTier:     tier,
		RateBps:        commissions.RateBpsForTier(tier),
		BasisMinor:     *moved.AmountMinor,
		Currency:       *moved.Currency,
		FxRateToBase:   moved.FxRateToBase,
	})
	switch {
	case errors.Is(err, commissions.ErrAlreadyAccrued):
		// A replay, or a second live entry the ledger refuses. Neither is a
		// fault, and retrying would never succeed.
		return nil
	case errors.Is(err, commissions.ErrNotAccruable):
		// The ordinary "this deal earns nothing" outcome: an influenced deal,
		// or a partner whose tier was never set. Recorded rather than retried —
		// returning an error here would wedge the group forever on a deal that
		// will never become accruable by itself.
		g.log.InfoContext(ctx, "commission: a won partner deal accrued nothing",
			"deal", env.Entity.ID.String(),
			"attribution", *moved.PartnerAttribution, "tier_set", tier != nil)
		return nil
	case err != nil:
		return err
	}
	return nil
}

// reverse voids whatever live entry the undone win produced.
func (g *CommissionGen) reverse(ctx context.Context, deal ids.UUID, toStatus string) error {
	reversed, err := g.ledger.ReverseForDeal(ctx, ids.From[ids.DealKind](deal),
		"the deal left won ("+toStatus+"), so the commission it earned is undone")
	if err != nil {
		return err
	}
	if reversed > 0 {
		g.log.InfoContext(ctx, "commission: a reopened deal's entries were reversed",
			"deal", deal.String(), "entries", reversed)
	}
	return nil
}

// accrualContext binds the workspace, the trace, and the system actor this
// consumer runs as. A subscriber carries none of them: without this the first
// governed call would fail for want of an actor, and the ledger row would carry
// no correlation back to the win that caused it.
func (g *CommissionGen) accrualContext(ctx context.Context, env events.Envelope, ws ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithCorrelationID(ctx, env.Trace.CorrelationID)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:commission_accrual",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
}
