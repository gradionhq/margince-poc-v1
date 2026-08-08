// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// How a refused tool call is EXPLAINED to the agent that made it, split out of
// dispatch.go when that file hit the 500-line cap. The boundary is real:
// dispatch.go routes a JSON-RPC method to a handler, and this decides what an
// agent is told when the handler says no.
//
// The distinction every line here serves: an agent's next move depends on WHICH
// kind of no it got. "You may never" and "a human must say yes" and "you typed
// the id wrong" are three different instructions, and collapsing them into a
// retry is how a scheduled run spends its whole step budget re-issuing one
// rejected call.

import (
	"errors"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// explain turns the sentinel taxonomy into messages an agent can act on —
// the distinction between "you may never" and "a human must say yes"
// changes what the agent should do next. Anything outside the taxonomy
// is an internal failure whose text (driver errors, hosts, wrap chains)
// must not cross the trust boundary to the tool client: it surfaces
// generically and the real cause is logged server-side.
//
// The branches below are an ENRICHMENT, not the completeness guarantee:
// each one exists because its prose beats anything derivable from a status
// code. Completeness lives in the default branch, which asks the one shared
// taxonomy — so a class with no branch here degrades to a plainer message,
// never to a false "something broke".
func (s *Dispatcher) explain(tool string, err error) string {
	var (
		badArgs     *BadArgsError
		unknownTool *UnknownToolError
		steppedUp   *StepUpStagedError
		overQuota   *auth.QuotaExceededError
	)
	switch {
	case errors.As(err, &steppedUp):
		// A step-up that reached a human. It is its own branch rather than a
		// wording variant of the one below because the INSTRUCTION differs: wait
		// for the person who connected this agent, then repeat the call
		// unchanged — and specifically do not present an approval_id, which is
		// the 🟡 loop's move and cannot work for a kind no tool redeems.
		return "This agent has reached a volume limit for this window, and the person who connected it has been " +
			"asked whether it may continue. Do not send an approval_id: once they approve, repeat this call unchanged. (" +
			steppedUp.Error() + ")"
	case errors.As(err, &overQuota):
		// A hard stop. Naming the window as the only thing that ends it is the
		// whole value of this branch: an agent told to "ask a human" about a
		// send ceiling waits for an approval nobody can grant.
		return "This agent has reached a volume limit for this window that no approval lifts. Stop calling this tool " +
			"and tell the user what is blocking it; the same call can succeed after the window rolls. (" +
			overQuota.Error() + ")"
	case errors.Is(err, apperrors.ErrRequiresApproval):
		return "This is a confirm-first (🟡) action: it needs human approval before it runs. " +
			"Ask the user to perform it in the CRM, or wait for the approval flow. Nothing was changed. (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrScopeExceeded):
		return "The passport this session runs under does not grant the scope this tool needs. (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrPermissionDenied):
		return "The human this passport acts for is not permitted to do this — the agent inherits exactly their access. (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrNotFound):
		return "No such record in this workspace (or it is outside the acting user's row scope). (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrVersionSkew):
		return "The record changed since it was read; re-read it and retry with the new version. (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrApprovalTokenInvalid):
		return "The approval token was not accepted — it may be consumed, expired, or for a different call. " +
			"Ask for a fresh approval and retry. (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrUnsupportedBySoR):
		// A DECLARED capability gap (AC-OV-2), not a fault: this workspace's
		// records live in a system that cannot answer this tool. Saying so —
		// and saying do-not-retry — is the whole point of declaring it;
		// falling through to the generic branch would tell the agent to retry
		// a permanent refusal and burn a scheduled run's whole step budget on
		// it.
		return "This workspace's system of record cannot serve this tool, and no retry will change that. " +
			"Do not retry it; use another tool, or tell the user this capability is unavailable here. (" + err.Error() + ")"
	case errors.As(err, &unknownTool):
		// A name that is not on the surface at all — the model invented it, or
		// is working from a stale tool list. Saying so discloses nothing: the
		// name came from the caller, and tools/list already names every tool
		// its passport admits, while a real tool it may not use answers
		// ErrScopeExceeded above. What the generic branch cost instead was
		// real — "retry" against a tool that will never exist is a loop.
		//
		// Worth a line for operators, at the level it is: a client working
		// from a stale tool list is a client mistake, not our outage.
		s.log.Warn("mcp: tool call refused", "tool", tool, "code", "unknown_tool", "err", unknownTool)
		return unknownTool.Error() + ". Nothing was changed, and no retry will make this name resolve. " +
			"Call tools/list and use a name from it."
	case errors.As(err, &badArgs):
		// The tool surface's own validation refusal: a misspelled argument, a
		// value outside a closed vocabulary, an empty message body. The agent
		// wrote those arguments, so it is the one party that can fix them —
		// which it cannot do unless we say which one was wrong.
		//
		// Echoing the detail is safe by construction rather than by luck, and
		// the construction is specifically that BadArgsError separates
		// provenance: its Cause — the half that quotes the caller's own JSON
		// back into a transcript later prompts of this run will read — is
		// bounded and escaped at maxBadArgsDetail. Its Guidance is NOT bounded,
		// and must therefore never carry caller-influenced text; it exists for
		// the fixed vocabularies reflected off the contract.
		return "The arguments were rejected before the tool ran; nothing was changed. (" + badArgs.Error() + ") " +
			"Correct them against the tool's inputSchema and call again — re-sending the same arguments will be rejected again."
	default:
		return s.explainClassified(tool, err)
	}
}

// internalFaultAdvice is what an agent is told when nothing in the taxonomy
// recognises the error. Named because it is the one answer on this surface that
// is unactionable by construction — it withholds the argument the agent could
// have fixed and offers a retry that cannot help — so a gate can assert a
// refusal did NOT land here without keeping a second copy of the sentence.
const internalFaultAdvice = "The tool failed for an internal reason; nothing may have changed. " +
	"Retry, and if it keeps failing contact the workspace admin."

// explainClassified answers every error with no bespoke branch above by
// asking the ONE taxonomy (httperr.Classify) instead of repeating the
// judgement locally. Whatever the REST surface answers with a 4xx is the
// caller's own mistake or a governed refusal, and reporting one of those to
// an agent as an internal failure is wrong twice over: it withholds the
// argument the agent could have fixed, and the "retry" it offers instead
// cannot ever succeed — a scheduled run spends its whole step budget
// re-issuing the same rejected call.
//
// Only an error outside the taxonomy is a server fault, and only its
// existence crosses the trust boundary; the cause stays in the log.
func (s *Dispatcher) explainClassified(tool string, err error) string {
	fault, ok := httperr.Classify(err)
	if !ok {
		s.log.Error("mcp: tool call failed", "tool", tool, "err", err)
		return internalFaultAdvice
	}

	// A classified refusal is not a fault of ours, so it is not logged as
	// one — except where a sentinel wrapped a driver failure, which is. In
	// that case Classify has already withheld the driver's text from Detail,
	// so the operator's half is logged and the agent still sees only the
	// sentinel's own words.
	if fault.InfraCause != nil {
		s.log.Error("mcp: tool call failed", "tool", tool, "err", fault.InfraCause)
	} else {
		s.log.Warn("mcp: tool call refused", "tool", tool, "code", fault.Code, "err", err)
	}

	// The detail can be the caller's own text (a decode error quotes the field
	// name it refused), so it gets the same treatment as every other echo on
	// this surface rather than relying on the classes that produce it to be
	// short and well-behaved.
	detail := echoSafe(fault.Detail, maxBadArgsDetail)
	if fault.Transient() {
		return "This tool is temporarily unavailable — nothing was changed. (" + faultCodes(fault) + ": " + detail + ") " +
			"The same call can succeed later; wait before retrying, and tell the user if they are waiting on it."
	}
	return "This call was refused as issued and nothing was changed; repeating it unchanged will be refused the same way. (" +
		faultCodes(fault) + ": " + detail + ") Correct the arguments and call again — or, if this is a governed refusal " +
		"rather than a mistake, do not retry: tell the user what is blocking it."
}

// faultCodes renders the machine codes an agent can branch on. A validation
// fault's outer code is the same word for every bad input ("validation_error");
// the per-field codes underneath are the ones that say WHICH argument and why,
// so they are what the agent gets — the same breakdown the REST body carries,
// rather than the flattened summary the outer code alone would give.
//
// Escaped like every other echo on this surface, not because a field name is
// caller text today — every agent-reachable FieldFault names a fixed argument —
// but because nothing in the taxonomy PROMISES that. A field slot fed from a
// caller-chosen key is one new fault type away, and a newline in it forges a
// frame in the transcript exactly as one in Detail would. The bound belongs to
// the position, not to the current occupants.
func faultCodes(fault httperr.Fault) string {
	if len(fault.Fields) == 0 {
		return echoSafe(fault.Code, maxBadArgsDetail)
	}
	parts := make([]string, 0, len(fault.Fields))
	for _, f := range fault.Fields {
		parts = append(parts, echoSafe(f.Field, maxBadArgsDetail)+"="+echoSafe(f.Code, maxBadArgsDetail))
	}
	return echoSafe(fault.Code, maxBadArgsDetail) + " " + strings.Join(parts, ", ")
}
