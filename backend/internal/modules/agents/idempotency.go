// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Retry safety for a mutating tool call: the same key twice means the effect
// happens once.
//
// WHAT IT IS FOR. A tools/call that times out, or whose transport drops the
// response, leaves the caller unable to tell "it did not happen" from "it
// happened and I did not hear". A model resolves that ambiguity by calling
// again, and for `send_email` the second call is a second email. The key makes
// the honest answer available: the first attempt claims it, and every later
// attempt under the same key is answered from what the first one produced.
//
// WHERE THE CLAIM LIVES. Not here. `compose` already runs an insert-first claim
// over the `idempotency_key` table for the REST transport, with a 24h replay
// window, a digest mismatch refusal and a retention sweep — so this module
// declares the seam and compose adapts its OWN claim transaction to it. One
// claim, one window, one sweep, two doors. The workspace binding and the RLS
// that make the claim tenant-safe are properties of that table, not of either
// caller.
//
// WHAT A REPLAY OWES. Everything a read owes (API-CC-8). A recorded result is a
// receipt that outlives the authority it was produced under, and handing it
// back unchecked would keep paying out records to a caller whose grant, seat or
// ownership has since been pulled — "revocation binds mid-session" would stop
// being true of the retry. The REST middleware answers this with a table of
// routes because a REST body has no common shape; a tool result does, so this
// walks the envelope's own evidence and re-reads every record in it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// ClaimState is what a claim attempt decided.
type ClaimState int

const (
	// ClaimFresh means this attempt owns the key and must execute.
	ClaimFresh ClaimState = iota
	// ClaimReplay means an earlier attempt settled and its result is carried.
	ClaimReplay
	// ClaimInFlight means an earlier attempt claimed the key and has not settled.
	ClaimInFlight
	// ClaimMismatch means the key is held against DIFFERENT arguments.
	ClaimMismatch
)

// Claim is a claim attempt's verdict, plus the recorded result when there is
// one to replay.
type Claim struct {
	State  ClaimState
	Result json.RawMessage
}

// Idempotency is the claim store, implemented by the composition layer over the
// transport-level claim table the REST door already uses.
//
// The three verbs are the whole lifecycle, and Settle/Release are separate
// rather than one "finish" taking an error: what a failed attempt owes is to
// stop holding the key, and spelling that as its own verb keeps a caller from
// recording a failure as a replayable result. Storing failures would pin a
// transient fault for the life of the window and would break the 🟡 loop, whose
// retry is the same call.
type Idempotency interface {
	// Claim takes the key for this call, or reports who already holds it.
	Claim(ctx context.Context, tool, key, digest string) (Claim, error)
	// Settle records a successful result for replay under the same key.
	Settle(ctx context.Context, tool, key string, result json.RawMessage) error
	// Release gives the key back after a failed attempt, so the caller may
	// retry it.
	Release(ctx context.Context, tool, key string) error
}

// WithIdempotency installs the claim store that makes `idempotency_key` mean
// something.
//
// A registry composed WITHOUT one refuses a keyed call rather than running it
// (see requireClaimStore). That asymmetry with the read charger — which records
// nothing when absent — is deliberate: an uncharged read is an accounting loss,
// while a key silently ignored is a promise of retry safety the surface cannot
// keep, and the act it fails to prevent is irreversible.
func WithIdempotency(claims Idempotency) RegistryOption {
	return func(r *Registry) { r.claims = claims }
}

// ReplayReader re-reads one record as the caller is now. It is the read half of
// the datasource seam, narrowed to the one verb a replay needs — the composite
// provider the tools are already composed over satisfies it, so a mirror-backed
// record is probed exactly as a live read of it would be.
type ReplayReader interface {
	Read(ctx context.Context, ref datasource.EntityRef) (datasource.Record, error)
}

// WithReplayReader installs the reader a replay re-checks its evidence through.
func WithReplayReader(reader ReplayReader) RegistryOption {
	return func(r *Registry) { r.replayReader = reader }
}

// runOnce executes an admitted call, at most once per retry key.
//
// A call with no key is the surface as it was: run, seal, charge. Everything
// below is what a key adds.
func (r *Registry) runOnce(ctx context.Context, t mcp.Tool, spec mcp.ToolSpec, res reserved) (json.RawMessage, error) {
	if res.RetryKey == "" {
		return r.handle(ctx, t, spec, res.Args)
	}
	if err := r.requireClaimStore(spec); err != nil {
		return nil, err
	}
	claim, err := r.claims.Claim(ctx, spec.Name, res.RetryKey, res.DiffHash)
	if err != nil {
		// Degraded, not refused — the same posture the REST middleware takes,
		// and for the same reason: idempotency is a retry-safety LAYER, so
		// refusing a call because the layer itself hiccupped would leave the
		// caller retrying a request that is now less protected than one sent
		// with no key at all.
		slog.ErrorContext(ctx, "idempotency claim failed; running the tool without replay protection",
			"tool", spec.Name, "err", err)
		return r.handle(ctx, t, spec, res.Args)
	}
	switch claim.State {
	case ClaimReplay:
		return r.replay(ctx, spec, claim.Result)
	case ClaimInFlight:
		return nil, fmt.Errorf(
			"an earlier %s call with this idempotency_key has not finished yet; wait for it rather than "+
				"repeating it: %w", spec.Name, apperrors.ErrConflict)
	case ClaimMismatch:
		return nil, fmt.Errorf(
			"this idempotency_key was already used for a DIFFERENT %s call; send a new key to make this "+
				"call, or repeat the original arguments to read its result: %w", spec.Name, apperrors.ErrConflict)
	case ClaimFresh:
		return r.runAndSettle(ctx, t, spec, res)
	default:
		// A state this switch does not know cannot be resolved into "safe to
		// run", so it is refused rather than guessed at.
		return nil, fmt.Errorf("crmagents: unknown idempotency claim state %d: %w", claim.State, apperrors.ErrConflict)
	}
}

// runAndSettle executes the call this attempt claimed and records what it
// produced.
//
// The claim is RELEASED on failure, so the caller may retry the same key. Only
// a produced result is recorded, and recording it is best-effort: by the time
// it runs the effect has committed, and reporting a completed act as a failure
// because the bookkeeping did not stick is the one outcome worse than an
// unreplayable key — the caller would retry, and the key it retries under no
// longer holds.
func (r *Registry) runAndSettle(ctx context.Context, t mcp.Tool, spec mcp.ToolSpec, res reserved) (json.RawMessage, error) {
	out, err := r.handle(ctx, t, spec, res.Args)
	if err != nil {
		if relErr := r.claims.Release(ctx, spec.Name, res.RetryKey); relErr != nil {
			slog.ErrorContext(ctx, "releasing a failed call's idempotency claim failed; the key stays held "+
				"until the retention sweep reaches it",
				"tool", spec.Name, "err", relErr)
		}
		return nil, err
	}
	if setErr := r.claims.Settle(ctx, spec.Name, res.RetryKey, out); setErr != nil {
		slog.ErrorContext(ctx, "recording a completed call for replay failed; a retry of this key will "+
			"execute again",
			"tool", spec.Name, "err", setErr)
	}
	return out, nil
}

// requireClaimStore refuses a keyed call on a surface that cannot claim it.
func (r *Registry) requireClaimStore(spec mcp.ToolSpec) error {
	if r.claims != nil {
		return nil
	}
	return &BadArgsError{Cause: fmt.Errorf(
		"this surface cannot make %s safe to retry, so `idempotency_key` is refused rather than ignored; "+
			"omit it and treat the call as at-most-once yourself", spec.Name)}
}

// replay answers a repeated call from what the first one produced — after
// re-checking, against the caller AS THEY ARE NOW, every record the recorded
// answer rests on.
//
// The evidence list is what makes this generic. Every sealed envelope carries
// one, collected where records become tool output, so the question "which
// records is this document about to hand over" is answered by the document
// itself rather than by a table of tools someone keeps current.
//
// ALL OR NOTHING. One unreadable record refuses the whole replay: the recorded
// bytes are a single document and there is no honest way to serve part of it.
// The refusal is ErrNotFound, the same existence-hiding answer a live read of
// that record would give — a distinct "you could see this yesterday" would be
// the oracle row scope exists to close.
func (r *Registry) replay(ctx context.Context, spec mcp.ToolSpec, recorded json.RawMessage) (json.RawMessage, error) {
	evidence, err := replayEvidence(recorded)
	if err != nil {
		// The recorded bytes are not an envelope this surface can read, so it
		// cannot show the caller may still see what is in them. Fail closed:
		// serving a document on the strength of a parse failure is exactly what
		// this check exists to prevent.
		slog.ErrorContext(ctx, "a recorded tool result could not be read back as an envelope; refusing the replay",
			"tool", spec.Name, "err", err)
		return nil, apperrors.ErrNotFound
	}
	if err := r.ensureReplayVisible(ctx, evidence); err != nil {
		return nil, err
	}
	// Charged BEFORE the result goes back, for the reason a fresh answer is:
	// records this surface cannot count are records it does not hand over. A
	// replay that skipped the charge would be the cheapest bulk read on the
	// surface — free, and repeatable for the life of the window.
	if err := r.chargeReplay(ctx, spec, len(evidence)); err != nil {
		return nil, err
	}
	return recorded, nil
}

// ensureReplayVisible re-reads every record the recorded answer names, through
// the same seam a fresh read of it would use.
//
// A surface with no reader wired cannot prove the caller may still see the
// records, so it refuses — the composition root is the only place that could
// have wired one, and a missing dependency must not pay out.
func (r *Registry) ensureReplayVisible(ctx context.Context, evidence []EvidenceRef) error {
	if len(evidence) == 0 {
		// A result resting on no record — pipeline configuration, a report
		// catalog — has nothing to re-check and nothing to withhold.
		return nil
	}
	if r.replayReader == nil {
		return apperrors.ErrNotFound
	}
	for _, ref := range evidence {
		if _, err := r.replayReader.Read(ctx, datasource.EntityRef{Type: ref.RecordType, ID: ref.RecordID}); err != nil {
			if errors.Is(err, apperrors.ErrNotFound) || errors.Is(err, apperrors.ErrPermissionDenied) {
				// Both answer the same way. A caller who has lost the object
				// grant learns no more than one who has lost the row.
				return apperrors.ErrNotFound
			}
			return err
		}
	}
	return nil
}

// chargeReplay bills a served replay against the caller's read bound.
//
// The count is the evidence list's length, which is the DEDUPED set — and
// deliberately not the `served` count a fresh call is charged. The two answer
// different questions: a fresh call is billed for what its handler pulled (a
// page read twice is two), a replay for what the frozen document actually hands
// over, which is each distinct record once.
//
// A charge that cannot be recorded WITHHOLDS the replay, with no exception for
// the write the recording came from. The asymmetry chargeReads draws — a write
// is served uncounted because its effect already happened — does not reach
// here: on a replay nothing happens, so withholding costs the caller a document
// it can ask for again, and serving it would leak an uncountable read once per
// retry for 24h.
func (r *Registry) chargeReplay(ctx context.Context, spec mcp.ToolSpec, records int) error {
	if r.reads == nil || records <= 0 {
		return nil
	}
	if err := r.reads.Consume(ctx, records); err != nil {
		slog.ErrorContext(ctx, "recording a replayed result against the read bound failed",
			"tool", spec.Name, "records", records, "err", err)
		return fmt.Errorf(
			"crmagents: replaying %s would hand over %d records that could not be counted against this "+
				"agent's read bound, so the answer is withheld: %w",
			spec.Name, records, apperrors.ErrBudgetExceeded)
	}
	return nil
}

// replayEvidence reads the record references out of a recorded envelope.
func replayEvidence(recorded json.RawMessage) ([]EvidenceRef, error) {
	var envelope struct {
		Evidence []EvidenceRef `json:"evidence"`
	}
	if err := json.Unmarshal(recorded, &envelope); err != nil {
		return nil, err
	}
	for _, ref := range envelope.Evidence {
		if ref.RecordType == "" || ref.RecordID.IsZero() {
			// A reference naming nothing cannot be probed, and treating it as
			// "nothing to check" would let an unreadable record ride back inside
			// a document whose evidence merely failed to describe it.
			return nil, fmt.Errorf("a recorded evidence reference names no record (%q/%s)", ref.RecordType, ref.RecordID)
		}
	}
	return envelope.Evidence, nil
}
