// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

// The run execution machinery (PI-STATE-2): submit one queued run, poll the
// in-progress ones, expire dead in-flight markers. Called by the compose job
// workers under a workspace-bound DB; no RBAC gate here, because a run was
// admitted and row-gated when it was queued and these passes only advance it.
//
// The egress lease (PI-AC-5): every path that unseals a credential holds
// LockWriteIdentity("provider_connection", provider) in the SAME transaction
// that re-reads the execution epoch and resolves the vault ref (GetOn).
// Disconnect takes the same lock before bumping the epoch and nulling the
// ref, so the two serialize: a disconnect lands either before the worker's
// read (it sees the bumped epoch and never calls) or after it (the call was
// authorized when it left). The residual window is one already-authorized
// in-flight request; what the terminal writes below guarantee is that no
// result obtained after a disconnect is ever STORED, and no new egress
// starts.
//
// Money rule: only a definite pre-work refusal releases a hold. Once
// inflight_at is set the request may have reached the provider, so every
// unknown outcome lands in submission_unknown with the reservation HELD —
// releasing it would let the next run spend credits the customer may already
// have been charged (poolUsedThisMonth excludes only skipped and cancelled).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

const (
	// strandedSubmitAge is how long a run may sit in queued before the sweep
	// re-dispatches it: its submit job was lost (a crash between the River
	// insert being worked and the state flip) and nothing else will retry.
	strandedSubmitAge = 5 * time.Minute
	// inflightExpiry is how long submitting may stand before the sweep calls
	// the outcome unknown. Long enough for any real submission round trip;
	// after it the worker is dead and nobody will ever settle the run.
	inflightExpiry = 10 * time.Minute
)

// execLease is what the lease transaction hands the provider call: the
// unsealed credential and the frozen request, valid because the epoch was
// re-read under the same lock a disconnect must take.
type execLease struct {
	cred   provider.Credential
	req    provider.Request
	epoch  int64
	person string
}

// The pass-control sentinels: each names a reason there is honestly nothing
// for this pass to do, distinguished from a failure so a caller neither
// retries nor reports it.
var (
	// errRunVanished: the row is gone — erased under the worker.
	errRunVanished = errors.New("integrations: the run no longer exists")
	// errNoLiveConnection: absent, not connected, or holding no credential —
	// no egress may be authorized.
	errNoLiveConnection = errors.New("integrations: no connected, credentialed connection")
)

// ExecuteSubmit advances one queued run through its submission (T2). The
// provider call happens outside any transaction; the two transactions around
// it are the lease and the settlement.
func (s *Store) ExecuteSubmit(ctx context.Context, runID string) error {
	name, err := s.runProviderName(ctx, runID)
	if errors.Is(err, errRunVanished) {
		return nil
	}
	if err != nil {
		return err
	}
	adapter, err := s.registry.Adapter(name)
	if err != nil {
		return fmt.Errorf("integrations: run %s names a provider this build does not carry: %w", runID, err)
	}
	var lease execLease
	var leased bool
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		l, ok, err := s.leaseForSubmit(ctx, tx, name, runID)
		lease, leased = l, ok
		return err
	})
	if err != nil || !leased {
		return err
	}
	sub, err := adapter.Submit(ctx, lease.cred, lease.req)
	if err != nil {
		// A transport error after egress is exactly what OutcomeAmbiguous
		// names: the request may have landed. Never a retry (PI-AC-4).
		sub = provider.Submission{Outcome: provider.OutcomeAmbiguous, SafeStatusCode: "submission_error"}
	}
	return s.settleSubmit(ctx, adapter.Descriptor(), name, runID, lease, sub)
}

// runProviderName resolves which provider a run belongs to.
func (s *Store) runProviderName(ctx context.Context, runID string) (string, error) {
	var name string
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT provider FROM provider_run WHERE id = $1`, runID).Scan(&name)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errRunVanished
	}
	if err != nil {
		return "", fmt.Errorf("integrations: reading the run's provider: %w", err)
	}
	return name, nil
}

// leaseForSubmit flips queued→submitting under the egress lease and hands
// back the unsealed credential and the frozen request. ok=false with a nil
// error means there is honestly nothing to submit — the run moved on, or the
// connection was withdrawn before any hold was at risk (still queued, so the
// cancel it commits releases nothing anyone paid).
func (s *Store) leaseForSubmit(ctx context.Context, tx pgx.Tx, name, runID string) (execLease, bool, error) {
	none := execLease{}
	if err := storekit.LockWriteIdentity(ctx, tx, "provider_connection", name); err != nil {
		return none, false, err
	}
	var state, corr string
	var person *string
	var runEpoch int64
	var cats []string
	err := tx.QueryRow(ctx, `
		SELECT state, person_id::text, connection_epoch, external_correlation_id::text, requested_categories
		  FROM provider_run WHERE id = $1 FOR UPDATE`, runID).
		Scan(&state, &person, &runEpoch, &corr, &cats)
	if errors.Is(err, pgx.ErrNoRows) {
		return none, false, nil
	}
	if err != nil {
		return none, false, fmt.Errorf("integrations: locking the run: %w", err)
	}
	if state != string(provider.RunQueued) || person == nil {
		return none, false, nil
	}
	conn, err := s.readLiveConnection(ctx, tx, name)
	withdrawn := errors.Is(err, errNoLiveConnection)
	if err != nil && !withdrawn {
		return none, false, err
	}
	if withdrawn || conn.epoch != runEpoch {
		// Withdrawn before inflight_at was ever set: nothing was sent and
		// nothing was held against a submission, so cancelling is honest.
		if _, err := tx.Exec(ctx, `
			UPDATE provider_run SET state = 'cancelled', completed_at = now(), updated_at = now()
			 WHERE id = $1`, runID); err != nil {
			return none, false, fmt.Errorf("integrations: cancelling the withdrawn run: %w", err)
		}
		return none, false, nil
	}
	req, err := s.frozenRequest(ctx, tx, name, *person, corr, cats)
	if err != nil {
		return none, false, err
	}
	cred, err := s.unseal(ctx, tx, conn.credentialRef)
	if err != nil {
		return none, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE provider_run SET state = 'submitting', inflight_at = now(),
		       submitted_at = now(), updated_at = now()
		 WHERE id = $1`, runID); err != nil {
		return none, false, fmt.Errorf("integrations: marking the run in flight: %w", err)
	}
	return execLease{cred: cred, epoch: runEpoch, person: *person, req: req}, true, nil
}

// frozenRequest builds what may leave the installation for this run: the
// subject's identifiers resolved through the owning domain, and exactly the
// categories and cascades the frozen policy paid for.
func (s *Store) frozenRequest(ctx context.Context, tx pgx.Tx, name, person, corr string, cats []string) (provider.Request, error) {
	if s.identifiers == nil {
		return provider.Request{}, errors.New("integrations: no owning domain is bound, so the submission cannot name its subject")
	}
	idents, err := s.identifiers(ctx, tx, person)
	if err != nil {
		return provider.Request{}, err
	}
	desc, err := s.registry.Descriptor(name)
	if err != nil {
		return provider.Request{}, err
	}
	requested := categoriesFrom(cats)
	return provider.Request{
		CorrelationID: corr,
		Identifiers:   idents,
		Categories:    requested,
		Cascades:      cascadesFor(desc, requested),
	}, nil
}

// liveConnection is the connection state the lease reads under its lock.
type liveConnection struct {
	epoch         int64
	credentialRef string
}

// readLiveConnection answers errNoLiveConnection when the connection cannot
// authorize egress: absent, not connected, or holding no credential.
func (s *Store) readLiveConnection(ctx context.Context, tx pgx.Tx, name string) (liveConnection, error) {
	var status string
	var epoch int64
	var ref *string
	err := tx.QueryRow(ctx, `
		SELECT status, execution_epoch, credential_ref FROM provider_connection WHERE provider = $1`,
		name).Scan(&status, &epoch, &ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return liveConnection{}, errNoLiveConnection
	}
	if err != nil {
		return liveConnection{}, fmt.Errorf("integrations: reading the connection for egress: %w", err)
	}
	if status != "connected" || ref == nil {
		return liveConnection{}, errNoLiveConnection
	}
	return liveConnection{epoch: epoch, credentialRef: *ref}, nil
}

// unseal resolves the credential INSIDE the lease transaction (GetOn), which
// is what serializes it against the disconnect that would destroy it.
func (s *Store) unseal(ctx context.Context, tx pgx.Tx, ref string) (provider.Credential, error) {
	ws, err := s.db.Workspace(ctx)
	if err != nil {
		return nil, fmt.Errorf("integrations: resolving the workspace for the credential: %w", err)
	}
	raw, err := s.vault.GetOn(ctx, tx, ws, keyvault.Ref(ref))
	if err != nil {
		return nil, fmt.Errorf("integrations: unsealing the credential: %w", err)
	}
	return provider.Credential(raw), nil
}

// cascadesFor narrows the descriptor's cascades to the ones the frozen
// category set actually permits: a cascade whose category was not requested
// was not reserved, and issuing it would spend an unheld credit.
func cascadesFor(desc provider.Descriptor, requested []provider.Category) []provider.Cascade {
	in := map[provider.Category]bool{}
	for _, c := range requested {
		in[c] = true
	}
	var out []provider.Cascade
	for _, c := range desc.Cascades {
		if in[c.Category] {
			out = append(out, c)
		}
	}
	return out
}

// settleSubmit records the submission's outcome (T2's second transaction).
// The epoch is re-read once more: a disconnect that landed while the call was
// out means the answer must not be stored — the run parks in
// submission_unknown with its reservation held, PI-AC-4's exact state.
func (s *Store) settleSubmit(ctx context.Context, desc provider.Descriptor, name, runID string, lease execLease, sub provider.Submission) error {
	var completed *provider.Result
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := storekit.LockWriteIdentity(ctx, tx, "provider_connection", name); err != nil {
			return err
		}
		state, err := s.lockRunState(ctx, tx, runID)
		if err != nil || state != string(provider.RunSubmitting) {
			return err
		}
		conn, err := s.readLiveConnection(ctx, tx, name)
		withdrawn := errors.Is(err, errNoLiveConnection)
		if err != nil && !withdrawn {
			return err
		}
		if withdrawn || conn.epoch != lease.epoch {
			return s.parkUnknown(ctx, tx, runID, "disconnected_in_flight")
		}
		switch sub.Outcome {
		case provider.OutcomeAccepted:
			if _, err := tx.Exec(ctx, `
				UPDATE provider_run SET state = 'in_progress', provider_job_id = $2, updated_at = now()
				 WHERE id = $1`, runID, sub.ProviderJobID); err != nil {
				return fmt.Errorf("integrations: recording the accepted submission: %w", err)
			}
			return nil
		case provider.OutcomeCompleted, provider.OutcomeNoMatch:
			// A synchronous provider answered in the submit call itself.
			completed = sub.Result
			return s.terminalize(ctx, tx, desc, name, runID, pollFrom(sub))
		case provider.OutcomeAmbiguous:
			return s.parkUnknown(ctx, tx, runID, sub.SafeStatusCode)
		default:
			return s.recordRefusal(ctx, tx, desc, name, runID, sub.Outcome, sub.SafeStatusCode)
		}
	})
	if err != nil {
		return err
	}
	if completed != nil {
		return s.handoffClaims(ctx, runID, lease.person, name, completed.Claims)
	}
	return nil
}

// lockRunState takes the run's row lock and answers its current state.
func (s *Store) lockRunState(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	var state string
	err := tx.QueryRow(ctx, `SELECT state FROM provider_run WHERE id = $1 FOR UPDATE`, runID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("integrations: locking the run: %w", err)
	}
	return state, nil
}

// parkUnknown is the one honest answer for a request whose fate is unknown:
// terminal, reservation held, inflight_at standing as the fact it carries.
func (s *Store) parkUnknown(ctx context.Context, tx pgx.Tx, runID, safeCode string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE provider_run SET state = 'submission_unknown', completed_at = now(),
		       last_safe_status_code = $2, updated_at = now()
		 WHERE id = $1`, runID, safeCode); err != nil {
		return fmt.Errorf("integrations: parking the unknown submission: %w", err)
	}
	return nil
}

// recordRefusal handles a definite pre-work refusal: the provider named a
// reason and did no work, so the hold is released and inflight_at cleared —
// the ONE case that may release, because it is the one case that is provably
// not a charge. Credential and credit refusals write through to the
// connection status so the settings card tells the truth without a re-probe.
func (s *Store) recordRefusal(ctx context.Context, tx pgx.Tx, desc provider.Descriptor, name, runID string, outcome provider.Outcome, safeCode string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE provider_run SET state = 'failed', completed_at = now(), inflight_at = NULL,
		       last_safe_status_code = $2, updated_at = now()
		 WHERE id = $1`, runID, safeCode); err != nil {
		return fmt.Errorf("integrations: recording the refusal: %w", err)
	}
	if err := s.reconcile(ctx, tx, desc, runID, nil, false); err != nil {
		return err
	}
	status, writeThrough := map[provider.Outcome]string{
		provider.OutcomeInvalidCredentials:  "invalid_credentials",
		provider.OutcomeInsufficientCredits: "insufficient_credits",
		provider.OutcomeRateLimited:         "rate_limited",
		provider.OutcomeProviderError:       "provider_error",
	}[outcome]
	if !writeThrough {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE provider_connection SET status = $2, last_safe_status_code = $3, updated_at = now()
		 WHERE provider = $1 AND status = 'connected'`, name, status, safeCode); err != nil {
		return fmt.Errorf("integrations: writing the refusal through to the connection: %w", err)
	}
	return nil
}
