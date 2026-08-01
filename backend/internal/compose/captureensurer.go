// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The adapter between capture's counterparty seam and the people module's
// auto-create engine. It lives in compose because a module never imports a
// sibling, and because what a captured counterparty is WORTH — a dossier for a
// new company, a triage read for an unjudged domain, an identity review for a
// conflicting lane — is the composition's business, not capture's.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// newCounterpartyStore builds the people store every counterparty-creation path
// shares, with the consumer-mail reader wired in. The ensure ladder has to know
// which domains can never name a company, and capture owns that list — so the
// injection happens here, where a module reaching a sibling is allowed and a
// module importing one is not.
//
// Every route into EnsureCounterpartyTx uses this: the capture sink, the
// verdict engine, and the review-queue accept. A plain people.NewStore on any
// of them would silently fall back to the shipped baseline and ignore the
// workspace's own corrections.
func newCounterpartyStore(pool *pgxpool.Pool) *people.Store {
	return people.NewStore(pool).WithConsumerMail(capture.MatcherTx)
}

// peopleEnsurer adapts the people module's auto-create engine onto
// capture's resolver seams — the mail one and the channel one. Both land in the
// same module because both must resolve through the same dedupe chokepoint; the
// contracts differ because a mail counterparty is named by an address and a
// channel counterparty by a provider identity.
type peopleEnsurer struct {
	store *people.Store
	// enrich queues the web dossier for a company this ensure just minted. It
	// lives HERE rather than in capture because capture must not know that
	// website reads exist — the seam it owns says "make the counterparty real",
	// and what a new company is then worth is the composition's business.
	enrich *autoEnrichTrigger
	// triage queues the read that decides whether a domain this ensure just met
	// deserves a company at all. It is the counterpart of enrich, one step
	// earlier: enrich asks what a company IS, triage asks whether there is one.
	triage *domainTriageTrigger
	// log reports a failed identity-review enqueue (raiseIdentityConflict) —
	// the one fault on this path that must never become a returned error,
	// because that would fail the capture that found it.
	log *slog.Logger
}

func (p peopleEnsurer) EnsureCounterparty(ctx context.Context, in capture.EnsureRequest) (capture.EnsureOutcome, error) {
	res, err := p.store.EnsureCounterparty(ctx, people.EnsureCounterpartyInput{
		Email:       in.Email,
		DisplayName: in.DisplayName,
		Domain:      in.Domain,
		OwnerID:     in.OwnerID,
		ActivityID:  ids.From[ids.ActivityKind](in.ActivityID),
		Source:      in.Source,
		CapturedBy:  in.CapturedBy,
		SuppressOrg: in.SuppressOrg,
	})
	if errors.Is(err, people.ErrCounterpartySuppressed) {
		// A13: the erased address stays dead — a deliberate no-op, not a
		// fault for the reconcile queue, and nothing was created to count.
		return capture.EnsureOutcome{}, nil
	}
	if err != nil {
		return capture.EnsureOutcome{}, err
	}
	// A NEW company is the trigger, not every captured mail: an organization
	// that already existed has either been enriched or been deliberately left
	// alone, and re-asking on every message from it would spend the day's cap on
	// companies nobody learned anything new about.
	if res.OrgCreated && res.OrganizationID != nil {
		p.enrich.organizationCaptured(ctx, *res.OrganizationID, in.Domain)
	}
	// The ensure left this domain's organization question open. Nothing is
	// created until it is answered, so queueing the read that answers it is not
	// an optimization here — it is the rest of the work.
	if res.TriagePending {
		p.triage.domainPending(ctx, res.TriageDomain)
	}
	return capture.EnsureOutcome{PersonCreated: res.PersonCreated, CompanyQueued: res.TriagePending}, nil
}

// EnsureChannelCounterparty is the same adaptation for an inbound channel
// message (telegram-oa design §6.4). No company is derived and so no web
// dossier is queued: a channel identity carries no domain, and there is nothing
// for the enrich trigger to read a website from.
func (p peopleEnsurer) EnsureChannelCounterparty(ctx context.Context, in capture.EnsureChannelRequest) (capture.EnsureOutcome, error) {
	res, err := p.store.EnsureChannelCounterparty(ctx, people.EnsureChannelCounterpartyInput{
		Identity:    in.Identity,
		DisplayName: in.DisplayName,
		ActivityID:  ids.From[ids.ActivityKind](in.ActivityID),
		Source:      in.Source,
		CapturedBy:  in.CapturedBy,
	})
	if errors.Is(err, people.ErrCounterpartySuppressed) {
		// A13 on the channel key: an erased subject stays dead — a deliberate
		// no-op, not a fault for the reconcile queue, and nothing was created
		// to count.
		return capture.EnsureOutcome{}, nil
	}
	if err != nil {
		return capture.EnsureOutcome{}, err
	}
	if res.Conflict != nil {
		p.raiseIdentityConflict(ctx, *res.Conflict, in.Source, in.CapturedBy)
	}
	return capture.EnsureOutcome{PersonCreated: res.PersonCreated}, nil
}

// raiseIdentityConflict is the D8 identity-review half of routing (design
// §7.3): the ensure above already routed the message deterministically and
// wrote nothing onto the rival, so what remains is telling a human "these two
// records may be one person." It runs in its own transaction (EnqueueIdentityConflict),
// AFTER the ensure's own commit, and deliberately swallows nothing into
// silence — a failure is logged with the pair and both lanes so it is
// actionable — but never returns an error: the message that surfaced this
// conflict is already on the timeline, and it must stay there even when this
// write does not succeed. Every later message from the same conflicting
// identity retries this call, and dedupequeue's own pair index absorbs the
// repeat (EnqueueIdentityConflict's own contract), so a transient failure
// here self-heals on the next message rather than needing a retry queue.
func (p peopleEnsurer) raiseIdentityConflict(ctx context.Context, conflict people.LaneConflict, source, capturedBy string) {
	if _, err := p.store.EnqueueIdentityConflict(ctx, conflict, source, capturedBy); err != nil {
		p.log.ErrorContext(ctx, "capture: identity-conflict review failed to enqueue",
			"routed_to", conflict.RoutedTo.String(), "routed_lane", conflict.RoutedLane,
			"rival", conflict.Rival.String(), "rival_lane", conflict.RivalLane, "err", err)
	}
}
