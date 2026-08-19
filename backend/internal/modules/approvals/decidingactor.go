// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Who is behind a decision, and what their credential may spend on it.
//
// authority.go answers whether an approval is decidable AT ALL by the grants
// and row scope its release needs — the same question for a person in their own
// seat and for an agent acting on their behalf. This file answers the other one,
// which only exists because a decision can now arrive on a lent credential: is
// there a person behind this call, and did they lend it enough to release THIS.
//
// The split is the difference between authority and admission. A passport
// carries its human's grants, so nothing in authority.go needs to know it is a
// passport; what it cannot see is that a credential is a bounded loan, and a
// bound the lender set is not one the borrower may lift.

package approvals

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// actingForAHuman guards the inbox and the decision. A decision is a human's,
// and the question this answers is whether one is behind THIS call — not which
// transport it arrived on.
//
// A passport carries the human it was minted by: AgentIdentity.Principal sets
// UserID, OnBehalfOf, the seat, the teams and the permissions from that person,
// so an agent call is already bounded by everything a decision is bounded by —
// the RBAC the staged effect needs, row-scope visibility of its target, and the
// licensing ceiling. A person answering in a chat window is the same person
// answering in a browser tab (ADR-0055), and the decision happens when they give
// the instruction.
//
// So what is refused here is a call with NO human behind it: the system
// principal, a connector, and an agent principal carrying no on_behalf_of —
// which is a credential nobody lent, whatever else it holds.
//
// This is deliberately not the whole answer for a decision. What a passport may
// RELEASE is bounded by the caps it was granted, which is agentReleaseSpends
// below; being somebody's agent is admission, not authority.
func actingForAHuman(ctx context.Context) error {
	p, ok := principal.Actor(ctx)
	if !ok {
		return errors.New("crmapprovals: no actor bound to context")
	}
	switch p.Type {
	case principal.PrincipalHuman:
		return nil
	case principal.PrincipalAgent:
		if p.OnBehalfOf.IsZero() {
			return fmt.Errorf("this credential names no person it acts for, so it decides nothing: %w",
				apperrors.ErrPermissionDenied)
		}
		return nil
	default:
		return fmt.Errorf("approvals are decided by people, not by %s principals: %w",
			p.Type, apperrors.ErrPermissionDenied)
	}
}

// sendingKinds are the kinds whose APPROVAL puts a message on the wire at the
// moment of decision, rather than releasing a retry the caller performs itself.
//
// The distinction is what makes this list short, and it is the reason it cannot
// be derived from the kind's name. A staging an agent made is redeemed by that
// agent re-issuing its own call, which the admission gate re-admits against the
// passport's caps — so a send_email an agent staged is already bounded by `send`
// at the moment it is actually sent, and needs nothing here. These two are
// staged by the SERVER: approving them IS the send, and this is the only place
// a cap can bound it.
var sendingKinds = map[string]bool{
	// An automation-composed reply, held for its rep. redeem.go's own entry for
	// this kind says the release CREATES the outbound activity.
	kindHeldDraft: true,
	// A scheduled message the system stopped and is holding (ADR-0104 §5);
	// approving it lets it go.
	KindScheduledSendHeld: true,
}

// agentReleaseSpends bounds what a PASSPORT may release, given that
// actingForAHuman has already admitted it as somebody's agent.
//
// A human decides on the strength of their seat and their grants; an agent
// decides on the strength of a credential a human minted with a fixed set of
// caps, and "acting as the user" must not mean spending caps the user withheld.
// The caps a decision spends are the ones the release spends: `write`, because a
// decision is a durable change to somebody else's queue, and `send` on top of it
// where approving is the send.
//
// Only on approve. A rejection of a held draft releases nothing outward — it
// cancels a message — so demanding the send cap to cancel a send would leave a
// credential able to start work it cannot stop.
//
// A human principal carries no ScopeSet at all (scopes are a passport's shape,
// not a seat's), so this answers nothing for them and must not be asked.
func agentReleaseSpends(p principal.Principal, kind string, approve bool) error {
	if p.Type != principal.PrincipalAgent {
		return nil
	}
	// A step-up is a question ABOUT this credential — how much of what it may
	// already read it may be handed (§2.4) — and it is the one decision no
	// on_behalf_of makes safe to delegate. The window exists because a human set
	// it; a passport that can lift its own window has none. Both verdicts, not
	// just the release: the lender is who this card was raised for, and an agent
	// answering it at all takes the question away from them.
	if kind == KindQuotaRelease {
		return fmt.Errorf("a volume step-up is answered by the person who lent this credential, not by it: %w",
			apperrors.ErrPermissionDenied)
	}
	if !p.Scopes.Has(principal.ScopeWrite) {
		return fmt.Errorf("deciding a staged action spends the write cap, which this credential does not carry: %w",
			apperrors.ErrPermissionDenied)
	}
	if approve && sendingKinds[kind] && !p.Scopes.Has(principal.ScopeSend) {
		return fmt.Errorf("approving a %s proposal sends the message it holds, which spends the send cap "+
			"this credential does not carry: %w", kind, apperrors.ErrPermissionDenied)
	}
	return nil
}
