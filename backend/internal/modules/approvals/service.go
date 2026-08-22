// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package approvals is the 🟡 confirm-first engine (ADR-0036,
// features/07 §8): agents STAGE an action they may not perform, humans
// DECIDE it in the inbox, and the agent REDEEMS the decision by
// re-invoking the identical call. The staged row is the authority
// object — bound to the exact proposed change (diff_hash), the staging
// passport, and the target row's version, consumed exactly once.
//
// Tables owned: approval. Imports shared + platform + the generated
// contract only; never a sibling module — the agent surface stages and
// redeems through an adapter injected at the composition root.
package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type Service struct {
	// db binds the workspace this store runs for (ADR-0091 §9 step 3).
	db *database.DB
	// now is the service's clock: both expiry windows (staging TTL,
	// redemption TTL) are judged against it, so tests can prove the
	// pending→expired and approved→dead transitions without sleeping.
	now func() time.Time
	// effects are the per-kind follow-on executors an approval releases
	// (compose injects them — this module never imports the modules the
	// effects write into). An effect runs AFTER the decision transaction
	// commits, only on approve; exactly-once is the effect's own duty
	// (the redeem-then-execute discipline every 🟡 executor follows).
	effects map[string]ApprovedEffect
	// prechecks are the per-kind preflights that run BEFORE the decision
	// transaction opens, so a kind whose effect can refuse for an ordinary
	// reason answers while the approval is still pending and re-approvable.
	//
	// They exist because the post-commit effect above is deliberately
	// un-undoable: once the decision commits, a failing effect leaves an
	// approved row decideInTx will refuse to decide again. For an effect that
	// fails only on infrastructure, "approved, but …" is the honest report. For
	// one that refuses on a live answer about the WORLD — consent withdrawn, a
	// mailbox lost, a thread archived since staging — that same report strands
	// work the human could have released after fixing the cause.
	//
	// It is also where a kind states what its payload may not become: the
	// generic edit scope pins entity references, so a field that matters and is
	// not shaped like a uuid — an address, a declared purpose — has nowhere
	// else to be defended.
	prechecks map[string]ReleasePrecheck
	// declines are the mirror of effects: what runs when a human says NO.
	//
	// Most kinds need nothing here, because rejecting a PROPOSAL is simply not
	// applying it — the record was never changed, so there is nothing to undo.
	// A kind needs one when the staged subject is a thing that already exists
	// and is WAITING: rejecting then has to resolve that waiting state, or the
	// item leaves the inbox and the thing it was about carries on waiting for a
	// decision nobody will make again.
	declines map[string]DeclinedEffect
	expiries map[string]ExpiredEffect
	// quota is the volume meter an approved step-up widens (quotarelease.go).
	// Nil in a composition that serves no agents, where a step-up can never be
	// staged in the first place.
	quota QuotaReleaser
	// log carries the cause of a follow-on effect that failed AFTER its own
	// decision committed (bundle.go). The wire names which member did not land
	// and nothing about why — internals are not a client's — so this logger is
	// the one place that cause survives. Nil falls back to slog.Default(), which
	// in a process that never called SetDefault writes it where nobody is
	// reading, and that is why the composition root injects its own.
	log *slog.Logger
}

const (
	approvalStatusApproved = "approved"
	approvalStatusRejected = "rejected"
	approvalKeyKind        = "kind"
	approvalKeyReason      = "reason"
)

// ApprovedEffect executes what an approved staging of its kind proposed.
type ApprovedEffect func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error

// ReleasePrecheck answers whether this kind's effect could run right now, and
// whether the payload about to be approved is one this kind accepts.
//
// It receives BOTH the staged proposal and the human's edit, because some kinds
// have to compare them: the generic edit scope pins entity references, which
// protects any field shaped like a uuid and nothing else. A kind whose payload
// carries something equally load-bearing in another shape — an address, a
// declared purpose — has no other place to say so.
//
// edited is nil when the human approved as staged.
//
// Returning an error refuses the DECISION, so the approval is never decided and
// the human can act on the reason and approve the same row again. Returning nil
// promises nothing: the effect still runs afterwards and may still fail on state
// that moved in between, which is what the effect's own transaction is for.
//
// It runs OUTSIDE any transaction, so it may use the pool freely. It is not
// required to be side-effect free, and for a send it is not: the consent gate
// records a lawful basis it derived, exactly as it does when a rep schedules a
// message that may later be cancelled (activities.scheduleSend runs the same
// preparation and throws the result away). What it must not do is write
// anything the DECISION owns, because the decision may still refuse after it.
type ReleasePrecheck func(ctx context.Context, staged, edited json.RawMessage) error

// DeclinedEffect is what a rejection executes. It takes no diff hash: there is
// nothing to redeem, because nothing is being applied — the work is resolving
// whatever the staging left waiting.
// It runs in the DECISION's own transaction, which is the whole point: a
// rejection whose work failed afterwards would leave the card decided, the retry
// refused as already-decided, and the subject still waiting. Both commit or
// neither does.
type DeclinedEffect func(ctx context.Context, tx pgx.Tx, approvalID ids.ApprovalID, proposedChange json.RawMessage) error

// ExpiredEffect is what the clock's expiry executes, in the expiry's own
// transaction and for the reason DeclinedEffect runs in the decision's: a
// staging about a subject that is WAITING — a candidate ledger row, a held
// message — must learn that nobody answered, or it waits forever while the
// card that would have resolved it is already gone from every inbox.
type ExpiredEffect func(ctx context.Context, tx pgx.Tx, approvalID ids.ApprovalID, proposedChange json.RawMessage) error

// NewService builds the approvals engine over a workspace-bound handle,
// with no effects registered until compose wires them.
func NewService(db *database.DB) *Service {
	return &Service{
		db: db, now: time.Now,
		effects:   map[string]ApprovedEffect{},
		prechecks: map[string]ReleasePrecheck{},
		declines:  map[string]DeclinedEffect{},
		expiries:  map[string]ExpiredEffect{},
	}
}

// WithEffect registers the follow-on executor for one staging kind.
//
// Registering a kind twice would resolve to whichever call ran last, which is a
// wiring order nothing observes. Composition is where that can happen and where
// the whole registry is visible at once, so it is checked there
// (TestNoKindIsRegisteredTwice) rather than defended here with a panic a domain
// module has no business raising.
func (s *Service) WithEffect(kind string, effect ApprovedEffect) *Service {
	s.effects[kind] = effect
	return s
}

// WithPrecheck registers the preflight that runs before a decision on one kind.
func (s *Service) WithPrecheck(kind string, check ReleasePrecheck) *Service {
	s.prechecks[kind] = check
	return s
}

// PrecheckKinds names the kinds carrying a preflight, so a composition test can
// hold the registry to its own rules.
func (s *Service) PrecheckKinds() []string {
	kinds := make([]string, 0, len(s.prechecks))
	for kind := range s.prechecks {
		kinds = append(kinds, kind)
	}
	return kinds
}

// WithDeclinedEffect registers what runs when a human REJECTS one staging kind.
//
// Register one only where a rejection has work to do. For a proposal, "no" means
// the record stays as it was and nothing needs to happen. For a staging about a
// subject that is already waiting — a message the system stopped and is holding
// — "no" is an instruction about that subject, and without this the item leaves
// the inbox while the thing it named waits forever.
func (s *Service) WithDeclinedEffect(kind string, effect DeclinedEffect) *Service {
	s.declines[kind] = effect
	return s
}

// WithExpiredEffect registers what runs when the clock expires one staging
// kind. Register one where an expiry has work to do, on the same terms as
// WithDeclinedEffect — and on the service the sweep runs on, which is the one
// jobs_approvalexpiry.go builds.
func (s *Service) WithExpiredEffect(kind string, effect ExpiredEffect) *Service {
	s.expiries[kind] = effect
	return s
}

// WithLogger installs the mounting process's logger.
func (s *Service) WithLogger(log *slog.Logger) *Service {
	s.log = log
	return s
}

// logger is the installed logger, or the process default when a caller that
// never decides — a nightly proposer, a rematch sweep — built the service
// without one.
func (s *Service) logger() *slog.Logger {
	if s.log == nil {
		return slog.Default()
	}
	return s.log
}

// EffectKinds lists the staging kinds this service has an executor for. It
// exists so the composition root's fitness test can hold EVERY stageable kind to
// a decision-grant mapping, not just the ones that happen to be agent tools — a
// kind registered here but missing from decisionGrants stages proposals that no
// human can see or decide, which fails silently and looks like nothing happened.
func (s *Service) EffectKinds() []string {
	kinds := make([]string, 0, len(s.effects))
	for kind := range s.effects {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// audit appends this module's audit rows — same append-only table, this
// module's own writer (modules do not share store internals).
func (s *Service) audit(ctx context.Context, tx pgx.Tx, p principal.Principal, action string, entityID ids.UUID, evidence map[string]any) (ids.UUID, error) {
	raw, err := json.Marshal(evidence)
	if err != nil {
		return ids.Nil, err
	}
	id := ids.NewV7()
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (id, actor_type, actor_id, passport_id, on_behalf_of, action, entity_type, entity_id, evidence)
		 VALUES ($1, $2, $3, $4, $5, $6, 'approval', $7, $8)`,
		id, string(p.Type), p.ID, nullUUID(p.PassportID), nullUUID(p.OnBehalfOf),
		action, entityID, raw)
	return id, err
}

// emit stages one approval.* / coldstart.* event in the transactional
// outbox, complete envelope, exactly like every other module's writes:
// unlike storekit.EmitEvent, this module's entity is always "approval"
// (the staged row itself), so
// that mapping stays hardcoded here rather than sourced from the
// payload's EntityType(). eventType is sourced from payload.EventType()
// instead of a separate string parameter — a caller cannot stage the
// wrong payload for an event type without failing to compile, the same
// guarantee storekit.EmitEvent gives every other module.
func (s *Service) emit(ctx context.Context, tx pgx.Tx, p principal.Principal, auditID ids.UUID, entityID ids.UUID, payload events.Payload) error {
	correlationID, ok := principal.CorrelationID(ctx)
	if !ok {
		return errors.New("crmapprovals: no correlation id bound to context")
	}
	eventType := payload.EventType()
	env := events.Envelope{
		EventID:    ids.NewV7(),
		Type:       eventType,
		Version:    events.VersionOf(eventType),
		OccurredAt: s.now().UTC(),
		Actor: events.Actor{
			Type: string(p.Type), ID: p.ID,
			PassportID: nullUUID(p.PassportID), OnBehalfOf: nullUUID(p.OnBehalfOf),
		},
		Entity: events.EntityRef{Type: "approval", ID: entityID},
		Trace:  events.Trace{CorrelationID: correlationID, AuditLogID: auditID},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env.Payload = raw
	stream, err := events.StreamFor(eventType)
	if err != nil {
		return err
	}
	if err := env.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO event_outbox (stream, envelope) VALUES ($1, $2)`, stream, body)
	return err
}

func nullUUID(id ids.UUID) *ids.UUID {
	if id.IsZero() {
		return nil
	}
	return &id
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
