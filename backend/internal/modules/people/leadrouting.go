// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Lead routing (features/03 §3 AC-S5): a new lead is assigned an owner
// by the first matching rule, else round-robin across the configured
// pool — and a capacity cap is NEVER exceeded, not even by a rule hit.
// The whole decision runs inside one transaction under a per-workspace
// advisory lock, so concurrent workers rotate fairly (±1) and the cap
// check cannot race a sibling assignment. The decision reads and writes
// only the lead and the owning users — never the contact graph
// (segregation-in-scoring holds on the routing path).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// RoutingRule assigns leads whose field matches a literal value to one
// named owner — territory/segment/source routing in its V1 shape.
type RoutingRule struct {
	Field   string     `json:"field"` // one of RoutableLeadFields; an unknown name never matches
	Equals  string     `json:"equals"`
	OwnerID ids.UserID `json:"owner_id"`
}

// RoutingConfig is the assign_lead_owner automation's params blob,
// decoded. The catalog's params_schema (automation module) is the
// editor-facing mirror of this shape; both name the same knobs.
type RoutingConfig struct {
	Owners      []ids.UserID  `json:"owners"`        // round-robin pool, in rotation order
	CapPerOwner int           `json:"cap_per_owner"` // max open leads per owner; 0 = uncapped
	Rules       []RoutingRule `json:"rules"`         // evaluated in order, before round-robin
}

// RoutableLeadFields is the closed set a routing rule may match on —
// lead-local columns only (segregation-in-scoring: routing never reads
// the contact graph). leadRoutingFacts.field must resolve exactly these
// keys; the agents catalog mirrors this set for the editor schema, and a
// compose fitness test binds the two so they cannot drift.
var RoutableLeadFields = []string{"source", "company_name", "candidate_org_key"}

// ParseRoutingConfig decodes automation params into a RoutingConfig.
// Params were validated against the catalog schema at write time; this
// decode still fails loudly on a malformed blob rather than routing on
// a half-read config.
func ParseRoutingConfig(params json.RawMessage) (RoutingConfig, error) {
	if len(params) == 0 {
		return RoutingConfig{}, nil
	}
	var cfg RoutingConfig
	if err := json.Unmarshal(params, &cfg); err != nil {
		return RoutingConfig{}, fmt.Errorf("crmpeople: routing params: %w", err)
	}
	return cfg, nil
}

// Configured reports whether the instance can ever assign anyone.
func (c RoutingConfig) Configured() bool {
	return len(c.Owners) > 0 || len(c.Rules) > 0
}

// RoutingDecision is the audited outcome of one routing pass.
type RoutingDecision struct {
	Assigned bool
	OwnerID  ids.UserID
	Reason   string
}

// leadRoutingFacts is the lead-local slice a rule may look at.
type leadRoutingFacts struct {
	Source          string
	CompanyName     string
	CandidateOrgKey string
}

func (f leadRoutingFacts) field(name string) string {
	switch name {
	case "source":
		return f.Source
	case "company_name":
		return f.CompanyName
	case "candidate_org_key":
		return f.CandidateOrgKey
	}
	return ""
}

// chooseOwner is the pure routing decision: first matching rule wins if
// its owner is active and under cap; otherwise round-robin — the
// active, under-cap pool member with the fewest open leads, ties broken
// by pool order. Counting open leads (instead of a stored rotation
// pointer) is what makes the rotation self-correcting: a promoted or
// disqualified lead frees capacity, and the distribution stays within
// ±1 across eligible owners.
func chooseOwner(cfg RoutingConfig, lead leadRoutingFacts, openLoad map[ids.UserID]int, active map[ids.UserID]bool) (ids.UserID, string, bool) {
	underCap := func(id ids.UserID) bool {
		return cfg.CapPerOwner <= 0 || openLoad[id] < cfg.CapPerOwner
	}
	for i, rule := range cfg.Rules {
		if !strings.EqualFold(rule.Equals, lead.field(rule.Field)) {
			continue
		}
		// The cap outranks the rule (AC-S5: caps are never exceeded): a
		// full or inactive rule owner falls through to later rules and
		// then the round-robin pool.
		if active[rule.OwnerID] && underCap(rule.OwnerID) {
			return rule.OwnerID, fmt.Sprintf("rule:%d:%s", i, rule.Field), true
		}
	}
	var chosen ids.UserID
	for _, id := range cfg.Owners {
		if !active[id] || !underCap(id) {
			continue
		}
		if chosen.IsZero() || openLoad[id] < openLoad[chosen] {
			chosen = id
		}
	}
	if chosen.IsZero() {
		return ids.UserID{}, "", false
	}
	return chosen, "round_robin", true
}

// RouteLead runs one routing decision for an unowned lead and persists
// the assignment with audit + lead.updated in the same transaction. A
// lead that is gone, already owned, or terminal is left alone; an
// unroutable lead (everyone capped or inactive) stays unowned and the
// decision is still audit-logged — AC-S5 wants the decision on record,
// and no event fires because nothing on the lead changed.
func (s *Store) RouteLead(ctx context.Context, leadID ids.LeadID, cfg RoutingConfig) (RoutingDecision, error) {
	if err := auth.Require(ctx, "lead", principal.ActionUpdate); err != nil {
		return RoutingDecision{}, err
	}
	var decision RoutingDecision
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// One routing decision at a time per workspace: the advisory lock
		// serializes rotation (fairness stays ±1 under concurrent workers)
		// and makes the cap count race-free — the count and the assignment
		// commit as one unit.
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended('lead_routing:' || $1::text, 0))`,
			storekit.MustWorkspace(ctx)); err != nil {
			return err
		}

		lock, err := storekit.LockRow(ctx, tx, "lead", leadID.UUID, storekit.LiveOnly)
		if errors.Is(err, apperrors.ErrNotFound) {
			decision = RoutingDecision{Reason: "lead_gone"} // archived or erased before routing ran
			return nil
		}
		if err != nil {
			return err
		}

		var currentOwner *ids.UserID
		var status string
		var facts leadRoutingFacts
		if err := tx.QueryRow(ctx, `
			SELECT owner_id, status, source, coalesce(company_name, ''), coalesce(candidate_org_key, '')
			  FROM lead WHERE id = $1`,
			leadID).Scan(&currentOwner, &status, &facts.Source, &facts.CompanyName, &facts.CandidateOrgKey); err != nil {
			return err
		}
		if currentOwner != nil {
			decision = RoutingDecision{Reason: "already_owned"} // a human assignment outranks routing
			return nil
		}
		if !LeadStatus(status).Open() {
			decision = RoutingDecision{Reason: "terminal_status"}
			return nil
		}

		candidates := candidateOwners(cfg)
		active, openLoad, err := ownerCapacity(ctx, tx, candidates)
		if err != nil {
			return err
		}

		chosen, reason, ok := chooseOwner(cfg, facts, openLoad, active)
		if !ok {
			decision = RoutingDecision{Reason: "no_capacity"}
			_, err := storekit.Audit(ctx, tx, "assign", "lead", leadID.UUID,
				map[string]any{"owner_id": nil},
				map[string]any{"owner_id": nil, "routed": false, "reason": decision.Reason})
			return err
		}

		p := storekit.NewPatch()
		p.Set("owner_id", nil, chosen)
		if err := p.ApplyLocked(ctx, tx, lock); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "assign", "lead", leadID.UUID,
			map[string]any{"owner_id": nil},
			map[string]any{"owner_id": chosen, "routed": true, "reason": reason})
		if err != nil {
			return err
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, leadID.UUID, crmcontracts.WebhookPayloadLeadUpdated{
			ChangedFields: map[string]any{"delta": map[string]any{"owner_id": chosen}},
		}); err != nil {
			return err
		}
		decision = RoutingDecision{Assigned: true, OwnerID: chosen, Reason: reason}
		return nil
	})
	return decision, err
}

// candidateOwners is the deduplicated union of the pool and every rule
// target — the set whose activity and load the decision needs.
func candidateOwners(cfg RoutingConfig) []ids.UserID {
	seen := map[ids.UserID]bool{}
	var out []ids.UserID
	add := func(id ids.UserID) {
		if !id.IsZero() && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range cfg.Owners {
		add(id)
	}
	for _, rule := range cfg.Rules {
		add(rule.OwnerID)
	}
	return out
}

// ownerCapacity answers, for each candidate, whether the user can take
// work (active, unarchived) and how many open leads they already hold —
// the cap counts new/working, live leads, however they were assigned.
func ownerCapacity(ctx context.Context, tx pgx.Tx, candidates []ids.UserID) (active map[ids.UserID]bool, openLoad map[ids.UserID]int, err error) {
	active = map[ids.UserID]bool{}
	openLoad = map[ids.UserID]int{}
	if len(candidates) == 0 {
		return active, openLoad, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT u.id, count(l.id)
		  FROM app_user u
		  LEFT JOIN lead l ON l.workspace_id = u.workspace_id AND l.owner_id = u.id
		       AND l.status IN ('new','working') AND l.archived_at IS NULL
		 WHERE u.id = ANY($1) AND u.status = 'active' AND u.archived_at IS NULL
		 GROUP BY u.id`, candidates)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id ids.UserID
		var load int
		if err := rows.Scan(&id, &load); err != nil {
			return nil, nil, err
		}
		active[id] = true
		openLoad[id] = load
	}
	return active, openLoad, rows.Err()
}

// assignLeadOwnerName is the catalog key compose registers this handler
// under. Named "assign_lead_owner" — NOT "route_lead" — per AUTO-NOTE-2
// (§3.5): this handler ASSIGNS AN OWNER, a different act from "route a
// new lead to a task" (the automation module's own route_lead starter,
// handlers_event.go), which the spec's user-reachable vocabulary
// (AUTO-PARAM-5) reserves for the create_task reading of those words.
// Duplicated as a bare literal in automation's own catalog entry
// (automations_catalog.go) rather than imported: a module never
// imports a sibling (ADR-0054 §9), so the two sides of "same handler
// name, two modules" are kept in lockstep by CatalogEntry.Key's own
// invariant (equal the backing handler's Spec().Name), proven by a
// compose-level fitness test, not by a shared symbol.
const assignLeadOwnerName = "assign_lead_owner"

// LeadRoutingWorkflow returns the assign_lead_owner handler compose
// registers as a catalog automation (instance-gated, parameterized):
// the params carry the pool, caps, and rules; the transactional
// decision lives in the store so the count-and-assign is one atomic
// unit.
func LeadRoutingWorkflow(store *Store) workflow.Handler {
	return leadRouting{store: store}
}

type leadRouting struct {
	store *Store
}

func (leadRouting) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    assignLeadOwnerName,
		Trigger: workflow.Trigger{EventType: "lead.created"},
		Tier:    mcp.TierGreen,
	}
}

// Match declines an unconfigured instance: with no pool and no rules
// there is no decision to make or record.
func (leadRouting) Match(_ context.Context, ev workflow.Event) (bool, error) {
	cfg, err := ParseRoutingConfig(ev.Params)
	if err != nil {
		return false, err
	}
	return cfg.Configured(), nil
}

// Plan declares the assignment; the concrete owner is chosen inside the
// Apply transaction, because a pick made outside it could exceed a cap
// by the time it lands.
func (leadRouting) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionAssignOwner, Target: ev.Entity, Args: ev.Params,
	}}}, nil
}

func (w leadRouting) Apply(ctx context.Context, ev workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	cfg, err := ParseRoutingConfig(ev.Params)
	if err != nil {
		return workflow.RunResult{}, err
	}
	decision, err := w.store.RouteLead(ctx, ids.From[ids.LeadKind](ev.Entity.ID), cfg)
	if err != nil {
		return workflow.RunResult{}, err
	}
	if !decision.Assigned {
		return workflow.RunResult{}, nil
	}
	return workflow.RunResult{Applied: eff.Actions}, nil
}

// IdempotencyKey is the LEAD, not the envelope: a redelivered
// lead.created must not re-route.
func (leadRouting) IdempotencyKey(ev workflow.Event) string {
	return assignLeadOwnerName + ":" + ev.Entity.ID.String()
}
