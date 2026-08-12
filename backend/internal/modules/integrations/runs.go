// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// FenceVerdict is the owning domain's answer on whether a subject may be
// enriched at all.
type FenceVerdict struct {
	Allowed bool
	// Reason is the skip reason to record when Allowed is false. The domain
	// picks it because only the domain knows whether the refusal was consent
	// (suppressed) or eligibility (not_eligible).
	Reason provider.SkipReason
}

// The callbacks the owning domain supplies, declared here rather than in
// shared/ports/provider because each needs a transaction and that package is
// stdlib-only. Compose wires them from modules/people.
type (
	// FenceSubjectFunc answers consent, suppression, objection and erasure for
	// one subject, inside the queueing transaction. It runs again immediately
	// before any claim is written, because a subject can be suppressed while a
	// paid run is in flight (PI-AC-7).
	FenceSubjectFunc func(ctx context.Context, tx pgx.Tx, personID string) (FenceVerdict, error)

	// DuplicateClusterFunc returns the other records the domain believes may
	// be the same human. Empty is a legitimate answer — a domain with no
	// duplicate signal degrades the fence to the single-record rule rather
	// than blocking work.
	DuplicateClusterFunc func(ctx context.Context, tx pgx.Tx, personID string) ([]string, error)

	// SubjectIdentifiersFunc resolves the minimum set of facts that may leave
	// the installation for this subject.
	SubjectIdentifiersFunc func(ctx context.Context, tx pgx.Tx, personID string) (provider.PersonIdentifiers, error)

	// EnqueueSubmitFunc commits the submit job in the SAME transaction as the
	// run row, so a crash can never leave a run nobody will ever submit. The
	// job's args type belongs to compose — this module cannot see River.
	EnqueueSubmitFunc func(ctx context.Context, tx pgx.Tx, runID, workspaceID string) error
)

// WithDomain binds the owning domain's callbacks. Without them the service
// has no way to fence a subject or to name what may be sent about them, so
// QueueRun refuses rather than guessing.
func (s *Store) WithDomain(fence FenceSubjectFunc, cluster DuplicateClusterFunc, idents SubjectIdentifiersFunc) *Store {
	s.fence, s.cluster, s.identifiers = fence, cluster, idents
	return s
}

// WithSubmitEnqueue binds the durable hand-off to the worker.
func (s *Store) WithSubmitEnqueue(fn EnqueueSubmitFunc) *Store {
	s.enqueueSubmit = fn
	return s
}

// QueueRun admits, fences, freezes and reserves in ONE transaction, then
// returns. It never calls a provider: submission is a durable job, which is
// what lets the HTTP surface answer 202 immediately and what keeps a slow
// vendor off the request path.
//
// The order below is not arbitrary. Every step that can refuse runs before
// the step that costs money, and the fingerprint is computed before the
// duplicate check because the check is defined over it.
func (s *Store) QueueRun(ctx context.Context, in provider.QueueInput) (provider.Run, error) {
	// Queueing a run spends the customer's credits on a named person, so it is
	// gated on seeing that person — the same grant that authorizes reading
	// them. The object is `person` rather than `integrations` deliberately:
	// `integrations` governs the CONNECTION (admin/ops configuration), while
	// buying data about someone is a thing a rep does in the course of their
	// own work, on records they can already see.
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return provider.Run{}, err
	}
	if s.fence == nil || s.identifiers == nil {
		return provider.Run{}, errors.New("integrations: no owning domain is bound, so no subject can be fenced")
	}
	name := in.Provider
	if name == "" {
		return provider.Run{}, provider.ErrNotConnected
	}
	desc, err := s.registry.Descriptor(name)
	if err != nil {
		return provider.Run{}, provider.ErrNotConnected
	}

	var out provider.Run
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		conn, err := s.admit(ctx, tx, name, in.Trigger)
		if err != nil {
			return err
		}
		run, err := s.queueOne(ctx, tx, desc, conn, in)
		if err != nil {
			return err
		}
		out = run
		return nil
	})
	if err != nil {
		return provider.Run{}, err
	}
	return out, nil
}

// admittedConnection is the connection state a run freezes itself against.
type admittedConnection struct {
	id         string
	version    int64
	epoch      int64
	mode       string
	autoCreate bool
	autoImport bool
	categories []string
	refreshAge *int
	dailyLimit *int
}

// admit resolves the connection and refuses the triggers its policy does not
// admit. A connection that is not connected cannot spend anything, and a
// trigger the customer switched off must not spend on their behalf.
func (s *Store) admit(ctx context.Context, tx pgx.Tx, name string, trigger provider.Trigger) (admittedConnection, error) {
	var c admittedConnection
	err := tx.QueryRow(ctx, `
		SELECT id::text, version, execution_epoch, mode, automatic_individual_create,
		       automatic_import, categories, refresh_after_days, daily_run_limit
		  FROM provider_connection
		 WHERE provider = $1 AND status = 'connected'
		 FOR SHARE`, name).
		Scan(&c.id, &c.version, &c.epoch, &c.mode, &c.autoCreate,
			&c.autoImport, &c.categories, &c.refreshAge, &c.dailyLimit)
	if errors.Is(err, pgx.ErrNoRows) {
		return admittedConnection{}, provider.ErrNotConnected
	}
	if err != nil {
		return admittedConnection{}, fmt.Errorf("integrations: reading the connection: %w", err)
	}

	switch trigger {
	case provider.TriggerAutomaticCreate:
		if c.mode != "automatic_on_create" || !c.autoCreate {
			return admittedConnection{}, errTriggerNotAdmitted
		}
	case provider.TriggerAutomaticImport:
		if !c.autoImport {
			return admittedConnection{}, errTriggerNotAdmitted
		}
	}
	return c, nil
}

// errTriggerNotAdmitted reports that the saved policy does not run this
// trigger. It is not an error the caller shows anybody: the person.created
// consumer swallows it, because "auto-enrich is off" is the configuration
// working, not a failure.
var errTriggerNotAdmitted = errors.New("integrations: this trigger is not admitted by the saved policy")

// queueOne is the admission pipeline for one subject. Each refusal writes a
// skipped run rather than nothing at all: a customer asking "why was this
// person not enriched" deserves a row that answers, and a silent no-op cannot.
func (s *Store) queueOne(ctx context.Context, tx pgx.Tx, desc provider.Descriptor, conn admittedConnection, in provider.QueueInput) (provider.Run, error) {
	snapshot := freezeSnapshot(conn)
	cats := categoriesFrom(conn.categories)

	// 1. Consent, suppression, objection, erasure. Before anything else,
	//    because a subject we may not contact must not even be looked up.
	verdict, err := s.fence(ctx, tx, in.PersonID)
	if err != nil {
		return provider.Run{}, err
	}
	if !verdict.Allowed {
		return s.insertSkipped(ctx, tx, conn, in, snapshot, cats, verdict.Reason)
	}

	// 2. The rate ceiling and the freshness window (PI-PARAM-13/14). Both are
	//    reasons NOT to spend, so both precede the reservation.
	if conn.dailyLimit != nil {
		spent, err := s.runsSubmittedToday(ctx, tx, in.Provider)
		if err != nil {
			return provider.Run{}, err
		}
		if spent >= *conn.dailyLimit {
			return s.insertSkipped(ctx, tx, conn, in, snapshot, cats, provider.SkipRateLimited)
		}
	}
	if conn.refreshAge != nil && in.Trigger.Automatic() {
		fresh, err := s.hasFreshRun(ctx, tx, in.PersonID, in.Provider, *conn.refreshAge)
		if err != nil {
			return provider.Run{}, err
		}
		if fresh {
			return s.insertSkipped(ctx, tx, conn, in, snapshot, cats, provider.SkipAlreadyFresh)
		}
	}

	// 3. The identifiers, and the fingerprint over them. Computed BEFORE the
	//    duplicate check because that check is defined over the fingerprint.
	idents, err := s.identifiers(ctx, tx, in.PersonID)
	if err != nil {
		return provider.Run{}, err
	}
	fingerprint := fingerprintOf(idents, cats)

	// 4. The duplicate fence, automatic runs only. A human asking explicitly
	//    knows something the duplicate signal does not.
	if in.Trigger.Automatic() && s.cluster != nil {
		dup, err := s.duplicateAlreadyBought(ctx, tx, in, fingerprint)
		if err != nil {
			return provider.Run{}, err
		}
		if dup {
			return s.insertSkipped(ctx, tx, conn, in, snapshot, cats, provider.SkipDuplicateSubjectCandidate)
		}
	}

	// 5. The run row, under the live-run index. A duplicate trigger for the
	//    same subject and inputs returns the run already in flight instead of
	//    buying the same answer twice.
	runID, existing, err := s.insertRun(ctx, tx, conn, in, snapshot, cats, fingerprint)
	if err != nil {
		return provider.Run{}, err
	}
	if existing {
		return s.readRun(ctx, tx, runID)
	}

	// 6. The reservation: the whole worst case, up front, all pools or none.
	skip, err := s.reserve(ctx, tx, desc, conn, runID, cats)
	if err != nil {
		return provider.Run{}, err
	}
	if skip != "" {
		if err := s.markSkipped(ctx, tx, runID, skip); err != nil {
			return provider.Run{}, err
		}
		return s.readRun(ctx, tx, runID)
	}

	// 7. The durable hand-off. Committed with the run, so a crash cannot
	//    leave a queued run nobody will ever submit.
	if s.enqueueSubmit != nil {
		ws, err := s.db.Workspace(ctx)
		if err != nil {
			return provider.Run{}, fmt.Errorf("integrations: resolving the workspace for the submit job: %w", err)
		}
		if err := s.enqueueSubmit(ctx, tx, runID, ws.String()); err != nil {
			return provider.Run{}, fmt.Errorf("integrations: scheduling the submission: %w", err)
		}
	}
	if _, err := storekit.Audit(ctx, tx, "queue", "provider_run", uuidOf(&runID),
		nil, map[string]any{"provider": in.Provider, "trigger": string(in.Trigger)}); err != nil {
		return provider.Run{}, err
	}
	return s.readRun(ctx, tx, runID)
}

// runsSubmittedToday counts what actually reached the provider today. A run
// that never left `queued` cost nothing and must not consume the ceiling.
func (s *Store) runsSubmittedToday(ctx context.Context, tx pgx.Tx, name string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM provider_run
		 WHERE provider = $1
		   AND state <> 'queued' AND state <> 'skipped' AND state <> 'cancelled'
		   AND created_at >= date_trunc('day', now() AT TIME ZONE 'UTC')`, name).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("integrations: counting today's runs: %w", err)
	}
	return n, nil
}

// hasFreshRun reports whether this subject's newest completed run is still
// inside the refresh window.
func (s *Store) hasFreshRun(ctx context.Context, tx pgx.Tx, personID, name string, days int) (bool, error) {
	var fresh bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM provider_run
		   WHERE person_id = $1 AND provider = $2 AND state = 'completed'
		     AND completed_at > now() - make_interval(days => $3))`,
		personID, name, days).Scan(&fresh)
	if err != nil {
		return false, fmt.Errorf("integrations: checking result freshness: %w", err)
	}
	return fresh, nil
}

// duplicateAlreadyBought asks whether any record the domain considers the same
// human already holds a completed or live run at this fingerprint.
//
// The advisory lock serializes two automatic runs racing across a duplicate
// pair: without it both would look, both would see nothing, and both would
// buy. It is keyed on the cluster's stable minimum id so both racers hash to
// the same lock whichever side they started from.
func (s *Store) duplicateAlreadyBought(ctx context.Context, tx pgx.Tx, in provider.QueueInput, fingerprint string) (bool, error) {
	cluster, err := s.cluster(ctx, tx, in.PersonID)
	if err != nil {
		return false, err
	}
	if len(cluster) == 0 {
		return false, nil
	}
	key := append([]string{in.PersonID}, cluster...)
	sort.Strings(key)
	if err := storekit.LockWriteIdentity(ctx, tx, "provider_run_cluster", key[0]); err != nil {
		return false, err
	}

	var bought bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM provider_run
		   WHERE person_id = ANY($1) AND provider = $2 AND input_fingerprint = $3
		     AND state IN ('completed','queued','submitting','in_progress','submission_unknown'))`,
		cluster, in.Provider, fingerprint).Scan(&bought)
	if err != nil {
		return false, fmt.Errorf("integrations: checking the duplicate cluster: %w", err)
	}
	return bought, nil
}

// freezeSnapshot captures what this run is allowed to do, so a later settings
// change cannot widen it (PI-AC-2).
func freezeSnapshot(c admittedConnection) provider.Snapshot {
	return provider.Snapshot{
		Mode:             c.mode,
		Categories:       categoriesFrom(c.categories),
		AutomaticCreate:  c.autoCreate,
		AutomaticImport:  c.autoImport,
		RefreshAfterDays: c.refreshAge,
		DailyRunLimit:    c.dailyLimit,
	}
}

// fingerprintOf hashes exactly what will be SENT plus what was asked for. Two
// runs share a fingerprint when they would produce the same purchase, which is
// what makes the live-run index a duplicate-spend guard rather than a
// coincidence.
func fingerprintOf(id provider.PersonIdentifiers, cats []provider.Category) string {
	names := make([]string, 0, len(cats))
	for _, c := range cats {
		names = append(names, string(c))
	}
	sort.Strings(names)
	payload := struct {
		ID   provider.PersonIdentifiers `json:"identifiers"`
		Cats []string                   `json:"categories"`
	}{ID: id, Cats: names}
	raw, err := json.Marshal(payload)
	if err != nil {
		// Marshalling two plain structs cannot fail. If it ever did, a
		// fingerprint nothing else can equal is safer than one everything
		// matches: it would queue a fresh run rather than silently reuse one.
		raw = []byte(id.LinkedInURL + id.FirstName + id.LastName + time.Now().String())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
