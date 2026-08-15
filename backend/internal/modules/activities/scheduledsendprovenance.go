// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// WHO scheduled a message, frozen at the moment they asked for it (core 0260).
//
// Separate from the scheduling mechanics because it answers a different
// question. The rest of the module decides whether a deferred message may be
// written and when it fires; this decides what the record says about the actor
// that asked — which is what an incident investigation reads, and the one fact
// the fire path cannot reconstruct later.

import (
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// agentProvenance is WHO scheduled a message when the actor was not a human,
// frozen at schedule time.
//
// The human's id is already in `scheduled_by` and is not enough: two agents, or
// two passports, acting for the same person are the same human and different
// actors, and an audit trail has to tell them apart. Rebuilding an agent
// identity from the human's id at fire invents an actor that never existed,
// which is the attribution chain ADR-0055 rests on.
//
// This is a RECORD, not authority. The fire path still re-reads the human's
// live seat and grants and holds the message if they no longer permit it — a
// revoked passport must stop the send, and a stored one must never resurrect
// it. What is stored answers "who asked for this"; what is read live answers
// "may it still go".
type agentProvenance struct {
	ActorID    *string
	PassportID *ids.UUID
	OnBehalfOf *ids.UUID
}

// provenanceOf captures the acting agent, or nothing at all for a human.
//
// agent_on_behalf_of is the HUMAN behind the agent, and it is taken only from
// OnBehalfOf — never from UserID, which is a different fact.
//
// On the agent path that can reach a send the two hold the same value anyway:
// identity mints the principal from one field (AgentIdentity.Principal sets
// UserID and OnBehalfOf from the same a.OnBehalfOf). But a passport-less agent
// principal exists in this tree whose UserID is an AGENT's own app_user row —
// compose/extjobsrun.go selects it `WHERE id = $1 AND is_agent` — and copying
// that into a column meaning "the human behind this" would write an agent's id
// where a human's belongs. The fire path then hands it to actor.OnBehalfOf,
// which auth.Admit reads to derive seat and RBAC: a fabricated authority, which
// is the same class of defect this whole record exists to end.
//
// An agent with no OnBehalfOf therefore stores its id and NO human, rather than
// either a guess or nothing at all. Storing nothing would be worse than the
// guess: a new row with no actor id is indistinguishable from a pre-0260 row,
// and the fire path reads that as "this row cannot say which agent it was" and
// falls back to the derived `agent:<human-uuid>` — putting back the invented
// identity for exactly the actor whose real id was in hand.
func provenanceOf(p principal.Principal) agentProvenance {
	if p.Type == principal.PrincipalHuman {
		return agentProvenance{}
	}
	actorID := p.ID
	out := agentProvenance{ActorID: &actorID}
	if !p.OnBehalfOf.IsZero() {
		behalf := p.OnBehalfOf
		out.OnBehalfOf = &behalf
	}
	if !p.PassportID.IsZero() {
		// A passport is how an agent's scopes were granted, so an action taken
		// under one names it, and NULL says none was involved rather than that
		// one was lost.
		//
		// Every agent that can reach a send today carries one — the tool surface
		// is the only agent door to SendOrSchedule, and its principals are
		// passport-minted. This stays conditional rather than
		// assuming the passport is always there.
		out.PassportID = &p.PassportID
	}
	return out
}
