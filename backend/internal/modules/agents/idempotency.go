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
// AT MOST ONCE IS THE WHOLE PROMISE, so every branch that cannot keep it
// REFUSES rather than proceeding: no claim store, a claim that errored, a
// result that cannot be recorded. The REST middleware degrades to executing
// when its claim layer hiccups, and that is right for a request whose header
// the client may not even have meant; it is wrong here, where the argument is
// the caller explicitly asking for at-most-once and the act it is asking about
// is irreversible. A surface that cannot promise it says so.
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

	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
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
	// ClaimFailed means an earlier attempt under this key ran the tool and did
	// not produce a result. It is NOT the same as never having run: see Fail.
	ClaimFailed
)

// Claim is a claim attempt's verdict, plus what the earlier attempt left behind
// — the recorded result for a replay, or the recorded reason for a failure.
type Claim struct {
	State  ClaimState
	Result json.RawMessage
	// Records is what the recorded answer was charged against the read bound
	// when it was produced. A replay of it costs the same (see chargeReplay).
	Records int
	Reason  string
}

// Idempotency is the claim store, implemented by the composition layer over the
// transport-level claim table the REST door already uses.
//
// Four verbs, because a claimed key has exactly four ends and conflating any
// two of them loses the promise. Settle records a result to replay. Fail
// records that the tool RAN and produced none — which is not a free key,
// because a handler can fail after its write committed (create_record commits,
// then reads the row back) and releasing there is how one key creates two
// records. Release gives the key back, and is only for the failures that
// provably happened BEFORE the tool ran.
type Idempotency interface {
	// Claim takes the key for this call, or reports who already holds it.
	Claim(ctx context.Context, tool, key, digest string) (Claim, error)
	// Settle records a successful result, and the number of records it hands
	// over, so a replay of it costs the caller what the call cost.
	Settle(ctx context.Context, tool, key string, result json.RawMessage, records int) error
	// Fail records that the tool ran under this key and produced no result.
	Fail(ctx context.Context, tool, key, reason string) error
	// Release gives an unrun key back, so the caller may retry it.
	Release(ctx context.Context, tool, key string) error
}

// WithIdempotency installs the claim store that makes `idempotency_key` mean
// something. A registry composed without one refuses a keyed call.
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

// claimFor takes the key for an admitted call, or answers the attempt that
// cannot run. `fresh` is true only when this attempt owns the key and must
// execute; every other verdict is already answered in out/err.
//
// It runs BEFORE the approval redemption, and that ordering is the difference
// between retry safety working on the 🟡 tools and not working there at all. An
// approval is single-use: redeeming first means the retry of an approved
// `send_email` — the exact call whose response was lost, and the most
// irreversible act on this surface — dies on the consumed approval and never
// reaches the result the first attempt recorded.
func (r *Registry) claimFor(ctx context.Context, spec mcp.ToolSpec, res reserved) (fresh bool, out json.RawMessage, err error) {
	if res.RetryKey == "" {
		return true, nil, nil
	}
	if err := r.refuseUnkeyableCall(spec); err != nil {
		return false, nil, err
	}
	claim, err := r.claims.Claim(ctx, spec.Name, res.RetryKey, res.DiffHash)
	if err != nil {
		// Refused, not degraded. The caller asked for at-most-once and this
		// surface cannot currently provide it; running anyway would make a
		// promise the retry then discovers was never kept.
		slog.ErrorContext(ctx, "the idempotency claim failed; refusing the call rather than running it unprotected",
			"tool", spec.Name, "err", err)
		return false, nil, fmt.Errorf(
			"%s could not be made safe to retry just now, so it was not run; retry the identical call: %w",
			spec.Name, apperrors.ErrConflict)
	}
	switch claim.State {
	case ClaimFresh:
		return true, nil, nil
	case ClaimReplay:
		out, err := r.replay(ctx, spec, claim)
		return false, out, err
	case ClaimInFlight:
		return false, nil, fmt.Errorf(
			"an earlier %s call with this idempotency_key has not finished yet; wait for it rather than "+
				"repeating it: %w", spec.Name, apperrors.ErrConflict)
	case ClaimMismatch:
		return false, nil, fmt.Errorf(
			"this idempotency_key was already used for a DIFFERENT %s call; send a new key to make this "+
				"call, or repeat the original arguments to read its result: %w", spec.Name, apperrors.ErrConflict)
	case ClaimFailed:
		// Honest about the one thing the caller needs to decide. The earlier
		// attempt reached the tool, so whether it took effect is exactly what
		// this surface does not know — and a fresh key here would be a second
		// attempt at something that may already have happened.
		return false, nil, fmt.Errorf(
			"an earlier %s call with this idempotency_key failed after it had already started, so it may or "+
				"may not have taken effect (%s); check the record before retrying under a NEW key: %w",
			spec.Name, claim.Reason, apperrors.ErrConflict)
	default:
		// A state this switch does not know cannot be resolved into "safe to
		// run", so it is refused rather than guessed at.
		return false, nil, fmt.Errorf("crmagents: unknown idempotency claim state %d: %w", claim.State, apperrors.ErrConflict)
	}
}

// refuseUnkeyableCall refuses a key the surface would not honour: one on a tool
// whose schema never offered it, and one on a surface with no claim store.
//
// The read-only half is the runtime side of what withRetryKey advertises. A
// read tool's schema omits the member — usually under
// `additionalProperties:false` — so accepting one anyway would be the surface
// contradicting its own schema, which is the defect A4 exists to close. It also
// keeps 24h-freezable copies of read answers from existing at all.
func (r *Registry) refuseUnkeyableCall(spec mcp.ToolSpec) error {
	if spec.ReadOnly() {
		return &BadArgsError{Cause: fmt.Errorf(
			"%s only reads, so it takes no `%s` — a read changes nothing there is anything to repeat",
			spec.Name, idempotencyKeyArg)}
	}
	if r.claims == nil {
		return &BadArgsError{Cause: fmt.Errorf(
			"this surface cannot make %s safe to retry, so `%s` is refused rather than ignored; "+
				"omit it and treat the call as at-most-once yourself", spec.Name, idempotencyKeyArg)}
	}
	return nil
}

// settleRun records what a claimed run produced, for the retry that comes after
// a lost response.
//
// Both bookkeeping calls run on a context DETACHED from the caller's
// cancellation, because the motivating case cancels it: the transport dropped,
// which is precisely why the caller will retry. Settling on the dead context
// would fail, leave the claim in flight for the whole window, and make the one
// result the retry exists to fetch permanently unreachable.
//
// A FAILED run does not release its key. A handler can fail after its write
// committed — create_record commits the row and then reads it back — so "the
// call returned an error" is not "nothing happened", and freeing the key there
// is how one key creates two records.
func (r *Registry) settleRun(ctx context.Context, spec mcp.ToolSpec, res reserved, out json.RawMessage, records int, runErr error) {
	if res.RetryKey == "" {
		return
	}
	book := context.WithoutCancel(ctx)
	if runErr != nil {
		if err := r.claims.Fail(book, spec.Name, res.RetryKey, safeFailureReason(runErr)); err != nil {
			slog.ErrorContext(book, "recording a failed call against its idempotency key failed; a retry of "+
				"this key will report it as still in flight",
				"tool", spec.Name, "err", err)
		}
		return
	}
	if err := r.claims.Settle(book, spec.Name, res.RetryKey, out, records); err != nil {
		slog.ErrorContext(book, "recording a completed call for replay failed; a retry of this key will "+
			"report it as still in flight rather than answering",
			"tool", spec.Name, "err", err)
	}
}

// safeFailureReason is what a later retry is TOLD about the earlier failure.
//
// The sentinel, never the message. A refusal's prose is written for the caller
// that provoked it and can carry a record name or a store's own words; this
// text is stored for 24h and handed to whoever presents the key next, so it
// says which KIND of failure it was and nothing about the data involved.
func safeFailureReason(err error) string {
	for _, known := range []struct {
		sentinel error
		reason   string
	}{
		{apperrors.ErrNotFound, "the record was not found"},
		{apperrors.ErrPermissionDenied, "it was not permitted"},
		{apperrors.ErrVersionSkew, "the record had changed"},
		{apperrors.ErrConflict, "it conflicted with another change"},
		{apperrors.ErrBudgetExceeded, "a budget was exhausted"},
		{apperrors.ErrConsentNotGranted, "consent was not granted"},
		{apperrors.ErrUnsupportedBySoR, "the system of record does not support it"},
	} {
		if errors.Is(err, known.sentinel) {
			return known.reason
		}
	}
	return "the call did not complete"
}

// releaseUnrunKey gives a key back after a failure that provably happened
// BEFORE the tool ran — today, a refused approval redemption. Nothing else may
// use it: for any failure at or after the handler, whether an effect landed is
// exactly what this surface cannot know.
func (r *Registry) releaseUnrunKey(ctx context.Context, spec mcp.ToolSpec, res reserved) {
	if res.RetryKey == "" {
		return
	}
	book := context.WithoutCancel(ctx)
	if err := r.claims.Release(book, spec.Name, res.RetryKey); err != nil {
		slog.ErrorContext(book, "releasing an unrun call's idempotency claim failed; the key stays held "+
			"until the retention sweep reaches it", "tool", spec.Name, "err", err)
	}
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
func (r *Registry) replay(ctx context.Context, spec mcp.ToolSpec, claim Claim) (json.RawMessage, error) {
	return r.ServeRecorded(ctx, spec.Name, claim.Result, claim.Records)
}

// ServeRecorded answers a stored envelope to the caller AS THEY ARE NOW: it
// re-proves every record the document names and re-charges what the original
// call cost, then hands the bytes back unchanged.
//
// It exists because there are now TWO surfaces holding a recorded answer — the
// idempotency claim, and an MCP task whose completed result a client may poll
// for the life of its handle — and both are receipts that outlive the authority
// they were produced under. One gate, called twice: a second implementation
// would be a second answer to "may this caller still see this", and the two
// would drift the first time either moved.
//
// The refusal is ErrNotFound, the existence-hiding answer a live read would
// give. The count is the ORIGINAL call's, never derived from the evidence list
// — see chargeReplay for why that arithmetic would make a repeat cheaper than
// the call.
func (r *Registry) ServeRecorded(ctx context.Context, tool string, recorded json.RawMessage, records int) (json.RawMessage, error) {
	spec, ok := r.Spec(tool)
	if !ok {
		// A tool that has left the surface cannot have its answer re-proven
		// against the schema and counters it was produced under.
		return nil, apperrors.ErrNotFound
	}
	evidence, err := replayEvidence(recorded)
	if err != nil {
		slog.ErrorContext(ctx, "a recorded tool result could not be read back as an envelope; withholding it",
			"tool", tool, "err", err)
		return nil, apperrors.ErrNotFound
	}
	if err := r.ensureReplayVisible(ctx, evidence); err != nil {
		return nil, err
	}
	if err := r.chargeReplay(ctx, spec, records); err != nil {
		return nil, err
	}
	return recorded, nil
}

// ensureReplayVisible re-reads every record the recorded answer names, through
// the same seam a fresh read of it would use.
//
// AN ANSWER THAT NAMES NOTHING IS REFUSED, and that is not the same claim as
// "it carries nothing". Admission checked scope, tier and seat; object RBAC and
// row scope live inside the handler, which a replay never enters — so the only
// authority a replay can re-check is the one attached to a record it can name.
// A recorded document citing no evidence is therefore unprovable rather than
// harmless, and an unprovable document is not served. Every mutating tool
// answers with the record it changed, so this refuses nothing the surface
// produces today; a tool that stopped doing so would lose replay rather than
// quietly lose its gate.
//
// THE READ IS LIVE, and one consequence is worth stating rather than
// discovering: a tool whose effect REMOVES its own evidence trades its receipt
// for that. `archive_record` names exactly the record it archived, so its retry
// is refused rather than replayed — the effect still happens once, which is the
// promise, but the answer is gone. An include-archived probe would return the
// receipt and is the wrong trade: Art. 17 erasure anonymizes a row IN PLACE and
// stamps archived_at, so the same relaxation would replay pre-erasure names and
// e-mail addresses out of a 24h-old snapshot that every live read path now
// refuses. Held by TestAnArchivesReceiptIsRefusedAndItsEffectStillHappensOnce.
func (r *Registry) ensureReplayVisible(ctx context.Context, evidence []EvidenceRef) error {
	// Before the emptiness question, because a surface with no reader cannot
	// prove anything about any document — the composition root is the only place
	// that could have wired one, and a missing dependency must never pay out.
	if r.replayReader == nil || len(evidence) == 0 {
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

// chargeReplay bills a served replay against the caller's READ counter.
//
// Only that counter. A replay hands the same records over again, so it is a
// read; it does NOT perform the action again, so charging the write/send/egress
// counter chargeAnswer adds would bill a caller for an effect that did not
// happen twice — and would spend a Passport's send ceiling on a document.
//
// The count is what the ORIGINAL call was charged, recorded with the result —
// not the length of the evidence list. The two differ whenever an answer hands
// over more than it can name (a probe, a record cited twice), and the evidence
// list is the shorter of the two every time, so deriving the charge from it
// would make retrying an answer cheaper than asking for it. One rule, no
// arithmetic: a replay costs what the call cost.
//
// A charge that cannot be recorded WITHHOLDS the replay, with no exception for
// the write the recording came from — which is why this does not go through
// r.charge. That helper serves an uncountable write anyway because its effect
// already happened; on a replay nothing happens, so withholding costs the
// caller a document it can ask for again, while serving it would leak an
// uncountable read once per retry for the life of the window.
func (r *Registry) chargeReplay(ctx context.Context, spec mcp.ToolSpec, records int) error {
	if r.quota == nil || records <= 0 {
		return nil
	}
	if err := r.quota.Consume(ctx, agentquota.Reads, records); err != nil {
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
