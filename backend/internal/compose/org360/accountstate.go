// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The three readings the company page leads with: whose move it is, when each
// side last wrote, and how the relationship stands in parts.
//
// They sit together because they answer one question between them — is this
// account healthy, and whose turn is it — and because each replaced a piece of
// the old header: a 0-100 score nobody could scale, and a single "last touch"
// date that hid which side it belonged to.

import (
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// readLastTouch answers which direction went last, and when — the pair that
// replaced the header's 0-100 score (AC-company-2, ADR-0079 arc).
//
// Two timestamps rather than one "last touch", because which side wrote last
// IS the question: an account we mailed a fortnight ago with no reply and one
// that wrote to us this morning have the same last-touch date and opposite
// meanings.
//
// It walks the same three links the timeline does (activities.OrgLinkedActivityExists),
// so the header can never disagree with the list under it, and
// it carries the caller's activity row scope, so a rep sees the last message
// THEY may read rather than the account's true last message.
func (a *assembly) readLastTouch() error {
	if err := auth.Require(a.ctx, "activity", principal.ActionRead); err != nil {
		return err
	}
	args := []any{a.orgID.UUID}
	arg := func(v any) int { args = append(args, v); return len(args) }
	scope, err := auth.ActivityScopeClause(a.ctx, "a", arg)
	if err != nil {
		return err
	}
	where := "a.archived_at IS NULL AND " + activities.OrgLinkedActivityExists(1)
	if scope != "" {
		where += " AND " + scope
	}
	// Two ordered LIMIT-1 arms in ONE round trip, rather than two FILTERed
	// max() aggregates. An aggregate has to see every qualifying row before it
	// can answer; each arm here stops at the first, so the cost is bounded by
	// how far back the newest message of that direction is rather than by the
	// account's whole history.
	rows, err := a.tx.Query(a.ctx, `
		(SELECT 'inbound' AS direction, a.occurred_at FROM activity a
		  WHERE `+where+` AND a.direction = 'inbound'
		  ORDER BY a.occurred_at DESC LIMIT 1)
		UNION ALL
		(SELECT 'outbound', a.occurred_at FROM activity a
		  WHERE `+where+` AND a.direction = 'outbound'
		  ORDER BY a.occurred_at DESC LIMIT 1)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var direction string
		var at time.Time
		if err := rows.Scan(&direction, &at); err != nil {
			return err
		}
		// A direction with no message returns no row at all, which is how the
		// null reaches the wire: nothing of that direction was ever captured.
		when := at
		if direction == "inbound" {
			a.out.LastInboundAt = &when
			continue
		}
		a.out.LastOutboundAt = &when
	}
	return rows.Err()
}

// readStateStrip is the three readings the overview leads with (AC-company-13).
//
// The account half needs no grant beyond the organization the caller already
// read. The other two are gated independently and answer NULL rather than a
// zero when refused: "no open deals" and "you may not see the deals" are
// different facts, and only one of them is about the account.
func (a *assembly) readStateStrip() error {
	in, err := a.suggestionInputsOnce()
	if err != nil {
		return err
	}
	strip := crmcontracts.Organization360StateStrip{}
	if lc := a.out.Organization.Lifecycle; lc != nil {
		strip.Account.Lifecycle = crmcontracts.Organization360StateStripAccountLifecycle(*lc)
	}
	if types := a.out.Organization.RelationshipTypes; types != nil {
		for _, relType := range *types {
			strip.Account.RelationshipTypes = append(strip.Account.RelationshipTypes,
				crmcontracts.Organization360StateStripAccountRelationshipTypes(relType))
		}
	}

	if in.timeline {
		strip.Engagement = new(struct {
			LastInboundAt  *time.Time                                            `json:"last_inbound_at,omitempty"`
			LastOutboundAt *time.Time                                            `json:"last_outbound_at,omitempty"`
			State          crmcontracts.Organization360StateStripEngagementState `json:"state"`
		})
		strip.Engagement.LastInboundAt = a.out.LastInboundAt
		strip.Engagement.LastOutboundAt = a.out.LastOutboundAt
		strip.Engagement.State = engagementState(in, a.now)
	}
	if in.pipeline {
		strip.Commercial = new(struct {
			OpenCount    int `json:"open_count"`
			StalledCount int `json:"stalled_count"`
		})
		strip.Commercial.OpenCount = in.open.OpenCount
		strip.Commercial.StalledCount = len(in.open.Stalled)
	}
	a.out.StateStrip = &strip
	return nil
}

// readHealth decomposes the relationship into the parts a reader can act on
// (AC-company-3), replacing the single 0-100 score the header used to lead
// with. That number was PO-F-3's MAX over the contacts, so one talkative
// contact spoke for the whole account; each part here names a fact instead.
//
// Every part is null when it cannot be computed rather than zero: zero is a
// claim about the ACCOUNT, and "nobody has written" and "you may not read the
// mail" are different answers.
func (a *assembly) readHealth() error {
	if err := auth.Require(a.ctx, "person", principal.ActionRead); err != nil {
		return err
	}
	strengths, err := a.contactStrengths()
	if err != nil {
		return err
	}
	health := crmcontracts.Organization360Health{}

	if inbound := a.out.LastInboundAt; inbound != nil {
		days := int(a.now.Sub(*inbound).Hours() / 24)
		health.DaysSinceLastInbound = &days
	}

	// The account's real surface: contacts who have actually interacted, not
	// contacts on file. A roster of ten who have never replied is not ten ways
	// in.
	active := 0
	var inbound90, outbound90 int
	for _, contact := range strengths {
		if contact.Strength.LastInteraction == nil {
			continue
		}
		active++
		inbound90 += contact.Strength.Inbound90d
		outbound90 += contact.Strength.Outbound90d
	}
	health.ActiveContacts = &active
	if total := inbound90 + outbound90; total > 0 {
		balance := float32(inbound90) / float32(total)
		health.ReplyBalance = &balance
	}
	// One contact carrying the whole relationship is the one shape a rep can
	// fix before it costs them the account, so it is named rather than scored.
	if len(strengths) > 0 {
		single := active == 1
		health.SingleThreaded = &single
	}

	commitments, counted, err := openCommitments(a.ctx, a.tx, a.orgID)
	if err != nil {
		return err
	}
	if counted {
		health.OpenCommitments = &commitments
	}

	a.out.Health = &health
	return nil
}
