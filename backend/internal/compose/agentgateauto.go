// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The 🟢 arm on the REST door: how a call the gate admitted at the auto-execute
// tier is dispatched, and what its write is conditioned on.
//
// Split from agentgate.go on the 500-line cap, along the boundary that file
// already draws for the other answer: agentgate.go decides whether a call is
// ADMITTED, agentgatestaging.go is what happens to a 🟡, and this is what
// happens to a 🟢. The two arms are deliberately readable side by side — both
// redeem a presented X-Approval-Token through the same consumePresentedToken,
// both charge their call ceiling once, and where they DIFFER (which version
// conditions the write, and at which moment the charge falls) each says why
// against the other.

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

const ifMatchHeader = "If-Match"

// runAutoExecuted dispatches a call the gate admitted at the auto-execute tier.
//
// An X-Approval-Token on such a call is CONSUMED, whatever tool it names. A
// staged 🟡 call can resolve 🟢 on its retry — the record moved, or the
// per-field split staged an otherwise auto-execute patch — and the asserted
// authority is then still an assertion: left unread, the approval stays pending
// in a human's inbox for work that already happened, and an invalid or replayed
// token is accepted by being ignored. The MCP door has always redeemed on this
// arm (registry.go, runClaimed); this door redeemed for update_record alone
// (gradionhq/margince-poc-v1#812).
//
// A REDEEMED call goes straight to the handler: it carries exactly the change a
// human released, whose identity the diff_hash already bound, so re-running the
// per-field ownership split over it would stage a second approval for the
// overwrite that was just approved.
func runAutoExecuted(w http.ResponseWriter, r *http.Request, next http.Handler, outcome admissionOutcome) {
	redemption, redeemed, ok := consumePresentedToken(w, r, outcome.staging, outcome.pol, outcome.body)
	if redeemed && !ok {
		return
	}
	if redeemed {
		// Charged HERE and not at the door's own charge point, for the reason
		// that point states: a token that opens nothing runs nothing, and
		// counting it would let a caller suspend its own Passport with replays.
		// Absorbed rather than refused, because the approval has already
		// committed — the same asymmetry the MCP door takes at redeemPresented.
		outcome.registry.ChargeRedeemedCall(r.Context(), outcome.spec)
	}
	if !pinAutoExecutedWrite(w, r, redemption, redeemed) {
		return
	}
	if redeemed {
		// WithContext shares the header map the pin was just set on, so the
		// released marker and the precondition travel together.
		r = r.WithContext(redemption.released) //nolint:contextcheck // released is derived from r.Context() inside consumePresentedToken
	}
	// The effect is charged on BOTH arms — the field split forwards to the
	// same handler through its own path — and on NEITHER when the handler
	// refused. A quota counts what an agent did, so a rejected mutation that
	// spent a write would let a caller exhaust its own allowance on requests
	// that changed nothing, which is a bound nobody wrote.
	// The meter sits OUTSIDE the recorder, so the handler's WriteJSON finds
	// it by a plain assertion while the recorder still sees every status.
	// A mutation that answers with the row it changed handed over a record,
	// and the MCP door charges that record at chargeAnswer whatever the tool
	// kind — a read-back free on one door and charged on the other is the
	// same asymmetry this gate exists to close.
	//
	// The CALL itself has been charged exactly once by the time this runs — at
	// the door for a plain admission, at the redemption above for a call that
	// asserted one — so nothing charges it here.
	performed := &effectRecorder{ResponseWriter: w}
	metered := &servedMeter{ResponseWriter: performed, r: r, reg: outcome.registry, mayRefuse: theEffectAlreadyLanded}
	if !redeemed && outcome.pol.Tool == toolUpdateRecord && !actionShapedUpdateOps[outcome.pol.Op] {
		splitHumanOwnedUpdate(metered, r, next,
			splitUpdateDeps{staging: outcome.staging, commands: outcome.commands, ownership: outcome.ownership},
			outcome.pol, outcome.body)
	} else {
		next.ServeHTTP(metered, r)
	}
	if performed.done() {
		outcome.registry.ChargeEffect(r.Context(), outcome.spec)
	}
}

// pinAutoExecutedWrite conditions an auto-executed agent write on the version
// its authority was taken at, by forwarding that version as the request's own
// If-Match. It reports whether the request may proceed; a refusal has already
// been written.
//
// The two calls this arm carries are pinned by DIFFERENT rules, because only one
// of them has something irreversible behind it.
//
// A REDEEMED call takes the server's version and takes it over anything the
// caller sent — the approval's own pin where it carried one, the version the
// tier gate read otherwise. That is redeemIfPresented's rule
// (agentgatestaging.go) on the other arm, adopted here for the reason that arm
// gives: this door hashes no If-Match into the identity a human's decision bound
// (canonicalHeaders, agentgatecanon.go), so a caller header is a version nothing
// proved while the server's pin names the state that was judged. Refusing the
// disagreement instead would be strictly worse and was, briefly, what this
// function did — consumePresentedToken has already SPENT the approval by the
// time this runs, so a refusal here destroys a human's one-shot yes on a call
// that never ran and can never be redeemed again. Forwarding is no weaker: the
// store re-checks the pin inside the transaction that mutates and refuses there
// unless the row is at the version the approval was granted against.
//
// An UNREDEEMED call is the ordinary 🟢 write, and there the CALLER's own
// If-Match is honoured — but only when it names the version this gate read, and
// that is CHECKED. The caller controls the header, so a version the gate never
// saw is a version nothing proved: a caller naming the version the racing close
// will PRODUCE walks straight through, because the store's compare then passes
// on precisely the record the tier decision does not describe. Preferring the
// caller unchecked would turn a coin-toss race into an armable one, and nothing
// has been consumed at that point, so refusing costs the caller only a retry.
//
// The precedence — caller, then released, then admitted — is agents.pinForWrite's
// (approvals.go), with ONE difference, named here because the two doors are
// meant to be comparable: pinForWrite silently prefers the released pin where it
// disagrees with the admitted one, and the branch below refuses instead. That
// branch is a defensive assertion rather than a case either door meets. Through
// the real stager a disagreement is unreachable — approvals pins server-side
// only for a concrete, version-checkable target, versions are monotonic, and the
// gate's read precedes the redemption's, so admitted ≤ current = released, and a
// row that really moved fails validateRedemptionTarget inside the redemption
// first. Whether refusing is even the right answer for a disagreement BOTH doors
// could only discover after the approval is consumed is
// gradionhq/margince-poc-v1#1069; until that is settled, neither door's
// behaviour here is load-bearing.
// redeemed is passed rather than read back off the redemption, so this function
// and its caller cannot come to different answers about whether an approval was
// spent — the one fact both the charge above and the rule below turn on.
func pinAutoExecutedWrite(w http.ResponseWriter, r *http.Request, redemption tokenRedemption, redeemed bool) bool {
	admitted, gateRead := auth.AutoExecutePin(r.Context())
	if redemption.pinned {
		if gateRead && redemption.pin != admitted {
			httperr.Write(w, r, fmt.Errorf(
				"the approval released this record at version %d and the tier gate read it at %d — it moved "+
					"between the two, so neither is the version that was judged; re-read it and retry: %w",
				redemption.pin, admitted, apperrors.ErrVersionSkew))
			return false
		}
		r.Header.Set(ifMatchHeader, strconv.FormatInt(redemption.pin, 10))
		return true
	}
	if !gateRead {
		// Nothing read a version: a static tier, and an approval that carried no
		// pin. A header invented here would refuse writes for a reason nobody can
		// act on, and a caller's own is left for the store to adjudicate.
		return true
	}
	if redeemed {
		// Redeemed against an approval that carried no pin of its own. The gate's
		// read is then the only proved version, and it overrides the caller's
		// header for the same reason the released pin does above: the approval is
		// already spent, so a refusal here would destroy it.
		r.Header.Set(ifMatchHeader, strconv.FormatInt(admitted, 10))
		return true
	}
	if caller := r.Header.Get(ifMatchHeader); caller != "" {
		// Compared as the numbers they are: the contract's If-Match is a bare
		// integer version, and two spellings of one number must not read as
		// disagreement. A caller header this parser refuses is left for the
		// handler's own IfMatchVersion to answer, which is where that message
		// already lives — and the handler behind THIS branch has one, because
		// gateRead is true only for an operation whose tier was decided by
		// reading a record (auth.Admit pins nothing for a static tier), which is
		// the dynamic-tier set, and a dynamic tier is resolved from a version the
		// operation's own handler is conditioned on. A handler that read no
		// If-Match at all would not be pinned by the well-formed header this
		// function sets either, so a malformed one is not what would leave it
		// unpinned.
		if got, err := strconv.ParseInt(caller, 10, 64); err != nil || got == admitted {
			return true
		}
		httperr.Write(w, r, fmt.Errorf(
			"If-Match %s is not the version this record was read at (%d) — re-read it and retry: %w",
			caller, admitted, apperrors.ErrVersionSkew))
		return false
	}
	r.Header.Set(ifMatchHeader, strconv.FormatInt(admitted, 10))
	return true
}
