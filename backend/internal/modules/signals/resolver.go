// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The signal→company resolver (B-E08.2, features/07 §9): maps a raw
// source pointer to a specific organization against the clean relational
// core (P11) — the organization_domain index, exact display name, or a
// prior-interaction email match — and records the INSPECTABLE match basis
// in signal_resolution. Three rules it never breaks (P12):
// ambiguity is surfaced as low_confidence, never silently asserted; an
// unattributable signal is dropped, never kept as a person-level dossier;
// resolved_person_id is set only for an EXISTING person under a recorded
// consent grant — the resolver creates no person rows, ever.

package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Match-basis confidences: fixed, so the same evidence always yields the
// same inspectable number (deterministic over the captured features).
const (
	confidenceDomain           = 0.95
	confidencePriorInteraction = 0.90
	confidenceName             = 0.85
)

// rawAttribution is what the resolver could extract from a raw_ref: an
// email (→ prior-interaction and domain matching), a domain (→ the
// organization_domain index), and/or a free-text mention (→ exact name).
type rawAttribution struct {
	Email  string
	Domain string
	Name   string
}

// parseRawRef extracts the attribution basis from the captured source
// pointer. Recognized shapes: "prefix:payload" capture refs, bare emails,
// URLs, bare domains, and free-text company mentions. An empty result
// means the pointer carries nothing attributable (noise → drop).
// isCaptureNamespace reports whether a "prefix:rest" split's prefix is a
// capture-source label (e.g. "inbound", "web") rather than part of the
// payload itself — a domain/email carries dots, @, or spaces, and a URL's
// "http"/"https" is a scheme, not a namespace.
func isCaptureNamespace(prefix string) bool {
	if strings.ContainsAny(prefix, ".@ ") {
		return false
	}
	return prefix != "http" && prefix != "https"
}

func parseRawRef(raw string) rawAttribution {
	payload := strings.TrimSpace(raw)
	// Capture refs arrive namespaced ("inbound:sam@acme.example",
	// "web:https://acme.example/pricing"); the payload is what resolves.
	if prefix, rest, ok := strings.Cut(payload, ":"); ok && isCaptureNamespace(prefix) {
		payload = strings.TrimSpace(rest)
	}
	if payload == "" {
		return rawAttribution{}
	}
	if at := strings.LastIndex(payload, "@"); at > 0 && !strings.Contains(payload, " ") && at < len(payload)-1 {
		email := strings.ToLower(payload)
		return rawAttribution{Email: email, Domain: registrableHost(email[strings.LastIndex(email, "@")+1:])}
	}
	if strings.HasPrefix(payload, "http://") || strings.HasPrefix(payload, "https://") {
		if u, err := url.Parse(payload); err == nil && u.Hostname() != "" {
			return rawAttribution{Domain: registrableHost(u.Hostname())}
		}
		return rawAttribution{}
	}
	if !strings.Contains(payload, " ") && strings.Contains(payload, ".") {
		return rawAttribution{Domain: registrableHost(payload)}
	}
	return rawAttribution{Name: payload}
}

// registrableHost lowercases and strips the www prefix, matching the
// organization_domain storage convention ("lowercased, no scheme, no www").
func registrableHost(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return strings.TrimPrefix(host, "www.")
}

// candidate is one plausible organization with its inspectable basis.
type candidate struct {
	OrgID      ids.OrganizationID
	MatchedOn  string // domain | name | prior_interaction
	Confidence float64
	Detail     string
}

// Resolve runs the resolver over one signal and stamps the outcome
// (B-E08.2). Replay-safe by construction: resolving is only permitted
// while the signal is unresolved or low_confidence — a terminal state
// answers 422, so a retried request cannot flap an asserted match.
func (s *Store) Resolve(ctx context.Context, signalID ids.SignalID) (crmcontracts.Signal, error) {
	if err := auth.Require(ctx, "signal", principal.ActionUpdate); err != nil {
		return crmcontracts.Signal{}, err
	}
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return crmcontracts.Signal{}, err
	}
	var out crmcontracts.Signal
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = s.resolveTx(ctx, tx, actor, signalID)
		return err
	})
	return out, err
}

// resolveTx runs the resolver over one signal inside the caller's
// transaction: the visibility gate, the terminal-state guard, candidate
// matching narrowed to visible orgs, the state stamp, and the write shape.
func (s *Store) resolveTx(ctx context.Context, tx pgx.Tx, actor principal.Principal, signalID ids.SignalID) (crmcontracts.Signal, error) {
	if err := auth.EnsureSignalVisible(ctx, tx, signalID.UUID); err != nil {
		return crmcontracts.Signal{}, err
	}
	// The row lock makes the terminal-state pre-read and the resolution
	// stamp one race-free unit: two concurrent resolves (or a resolve
	// racing a triage edit) cannot both pass the state guard.
	if _, err := storekit.LockRow(ctx, tx, "signal", signalID.UUID, storekit.LiveOnly); err != nil {
		return crmcontracts.Signal{}, err
	}
	sig, err := readSignal(ctx, tx, signalID, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.Signal{}, err
	}
	switch sig.ResolutionState {
	case "unresolved", "low_confidence":
	default:
		return crmcontracts.Signal{}, &NotResolvableError{Reason: fmt.Sprintf("signal is already %s; resolution is terminal", sig.ResolutionState)}
	}
	if sig.RawRef == nil || strings.TrimSpace(*sig.RawRef) == "" {
		return crmcontracts.Signal{}, &RequiredFieldError{Field: "raw_ref"}
	}

	attribution := parseRawRef(*sig.RawRef)
	candidates, err := matchCandidates(ctx, tx, attribution)
	if err != nil {
		return crmcontracts.Signal{}, err
	}
	// Row-scope the attribution: a resolver may only attribute a signal
	// to an organization the caller can see. Stamping resolved_org_id
	// (a read of that org) for an org outside the caller's scope would
	// leak its existence and id, so an invisible match is dropped —
	// leaving the signal unattributable rather than disclosing it.
	if candidates, err = visibleCandidates(ctx, tx, candidates); err != nil {
		return crmcontracts.Signal{}, err
	}

	before := map[string]any{"resolution_state": sig.ResolutionState}
	after, err := stampResolution(ctx, tx, actor, signalID, attribution.Email, candidates)
	if err != nil {
		return crmcontracts.Signal{}, err
	}
	auditID, err := storekit.Audit(ctx, tx, "resolve", "signal", signalID.UUID, before, after)
	if err != nil {
		return crmcontracts.Signal{}, fmt.Errorf("audit signal resolution: %w", err)
	}
	out, err := readSignal(ctx, tx, signalID, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.Signal{}, fmt.Errorf("read resolved signal: %w", err)
	}
	if err := storekit.Emit(ctx, tx, auditID, "signal.resolved", "signal", signalID.UUID, resolvedPayload(out, candidates)); err != nil {
		return crmcontracts.Signal{}, fmt.Errorf("emit signal.resolved: %w", err)
	}
	return out, nil
}

// stampResolution applies the resolver's verdict for this candidate set and
// returns the audit after-image. The count IS the verdict (P12): zero
// candidates drop the signal and link no person; exactly one resolves it to
// that org under the consent-gated person link; several surface it as
// low_confidence for review — resolved_org_id stays NULL unless exactly one
// org matched, and no branch ever creates a person.
func stampResolution(ctx context.Context, tx pgx.Tx, actor principal.Principal, signalID ids.SignalID, email string, candidates []candidate) (map[string]any, error) {
	switch len(candidates) {
	case 0:
		return dropUnattributable(ctx, tx, actor, signalID)
	case 1:
		return resolveToOrg(ctx, tx, actor, signalID, email, candidates)
	default:
		return flagAmbiguous(ctx, tx, actor, signalID, candidates)
	}
}

// dropUnattributable is the drop-the-orphan guard (B-E08.1): an
// unattributable signal is dropped with the "why" on record, and NO person
// link. Returns the audit after-image.
func dropUnattributable(ctx context.Context, tx pgx.Tx, actor principal.Principal, signalID ids.SignalID) (map[string]any, error) {
	if err := appendMatchBasis(ctx, tx, actor, signalID, "none", nil, nil,
		`{"candidates": [], "reason": "no organization matched the raw_ref"}`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE signal SET resolution_state = 'dropped', resolution_confidence = NULL WHERE id = $1`,
		signalID); err != nil {
		return nil, fmt.Errorf("drop unattributable signal: %w", err)
	}
	return map[string]any{"resolution_state": "dropped"}, nil
}

// resolveToOrg stamps the single-candidate match: the consent-gated person
// link (only an EXISTING person, only where the org match holds, only under
// a recorded grant — never a person creation), the inspectable match basis,
// and the resolved signal row. Returns the audit after-image.
func resolveToOrg(ctx context.Context, tx pgx.Tx, actor principal.Principal, signalID ids.SignalID, email string, candidates []candidate) (map[string]any, error) {
	chosen := candidates[0]
	personID, err := consentedPerson(ctx, tx, email, chosen.OrgID)
	if err != nil {
		return nil, err
	}
	detail, err := candidateDetail(candidates, &chosen)
	if err != nil {
		return nil, err
	}
	if err := appendMatchBasis(ctx, tx, actor, signalID, chosen.MatchedOn, &chosen.OrgID, &chosen.Confidence, detail); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE signal SET resolution_state = 'resolved', resolution_confidence = $2,
		        resolved_org_id = $3, resolved_person_id = $4,
		        entity_type = COALESCE(entity_type, 'organization'),
		        entity_id = COALESCE(entity_id, $3)
		 WHERE id = $1`,
		signalID, chosen.Confidence, chosen.OrgID, personID); err != nil {
		return nil, fmt.Errorf("stamp resolved signal: %w", err)
	}
	after := map[string]any{"resolution_state": "resolved", "resolved_org_id": chosen.OrgID, "matched_on": chosen.MatchedOn}
	if personID != nil {
		after["resolved_person_id"] = *personID
	}
	return after, nil
}

// flagAmbiguous surfaces ambiguity rather than asserting it: several
// plausible orgs flag the signal for review, resolved_org_id stays NULL,
// and no person is linked. Returns the audit after-image.
func flagAmbiguous(ctx context.Context, tx pgx.Tx, actor principal.Principal, signalID ids.SignalID, candidates []candidate) (map[string]any, error) {
	top := candidates[0]
	detail, err := candidateDetail(candidates, nil)
	if err != nil {
		return nil, err
	}
	if err := appendMatchBasis(ctx, tx, actor, signalID, top.MatchedOn, nil, &top.Confidence, detail); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE signal SET resolution_state = 'low_confidence', resolution_confidence = $2 WHERE id = $1`,
		signalID, top.Confidence); err != nil {
		return nil, fmt.Errorf("flag ambiguous signal: %w", err)
	}
	return map[string]any{"resolution_state": "low_confidence", "candidates": len(candidates)}, nil
}

// matchCandidates gathers the distinct plausible organizations, best
// basis per org, ordered by confidence then id (deterministic).
func matchCandidates(ctx context.Context, tx pgx.Tx, a rawAttribution) ([]candidate, error) {
	byOrg := map[ids.OrganizationID]candidate{}
	consider := func(c candidate) {
		if have, ok := byOrg[c.OrgID]; !ok || c.Confidence > have.Confidence {
			byOrg[c.OrgID] = c
		}
	}

	if a.Domain != "" {
		rows, err := tx.Query(ctx,
			`SELECT organization_id FROM organization_domain WHERE domain = $1 AND archived_at IS NULL`, a.Domain)
		if err != nil {
			return nil, fmt.Errorf("domain match: %w", err)
		}
		if err := eachID(rows, func(orgID ids.OrganizationID) {
			consider(candidate{OrgID: orgID, MatchedOn: "domain", Confidence: confidenceDomain,
				Detail: "domain " + a.Domain + " is registered to the organization"})
		}); err != nil {
			return nil, err
		}
	}
	if a.Email != "" {
		// Prior interaction: the sender is already a person in our graph,
		// currently employed at the org — our own relational core, no
		// external profiling.
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT r.organization_id
			FROM person_email pe
			JOIN relationship r ON r.person_id = pe.person_id
			 AND r.kind = 'employment' AND r.ended_at IS NULL AND r.archived_at IS NULL
			WHERE pe.email = $1 AND r.organization_id IS NOT NULL`, a.Email)
		if err != nil {
			return nil, fmt.Errorf("prior-interaction match: %w", err)
		}
		if err := eachID(rows, func(orgID ids.OrganizationID) {
			consider(candidate{OrgID: orgID, MatchedOn: "prior_interaction", Confidence: confidencePriorInteraction,
				Detail: "the sender is a known contact currently at the organization"})
		}); err != nil {
			return nil, err
		}
	}
	if a.Name != "" {
		rows, err := tx.Query(ctx,
			`SELECT id FROM organization WHERE lower(display_name) = lower($1) AND archived_at IS NULL`, a.Name)
		if err != nil {
			return nil, fmt.Errorf("name match: %w", err)
		}
		if err := eachID(rows, func(orgID ids.OrganizationID) {
			consider(candidate{OrgID: orgID, MatchedOn: "name", Confidence: confidenceName,
				Detail: "display name matches the mention exactly"})
		}); err != nil {
			return nil, err
		}
	}

	out := make([]candidate, 0, len(byOrg))
	for _, c := range byOrg {
		out = append(out, c)
	}
	sortCandidates(out)
	return out, nil
}

// visibleCandidates drops matches the caller cannot see under row-scope,
// preserving order. A reference stamped onto the signal (resolved_org_id)
// is a read of that org; auth.EnsureLinkTarget is the one visibility probe
// shared with every other cross-record link, so the resolver never
// attributes to — nor discloses — an org outside the caller's scope.
func visibleCandidates(ctx context.Context, tx pgx.Tx, in []candidate) ([]candidate, error) {
	out := in[:0]
	for _, c := range in {
		switch err := auth.EnsureLinkTarget(ctx, tx, "organization", c.OrgID.UUID); {
		case err == nil:
			out = append(out, c)
		case errors.Is(err, apperrors.ErrNotFound):
			// invisible to this caller — not an attribution they may learn
		default:
			return nil, err
		}
	}
	return out, nil
}

// sortCandidates orders by confidence, id as the tie-breaker — the same
// evidence always lists (and reports) candidates identically.
func sortCandidates(cs []candidate) {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Confidence != cs[j].Confidence {
			return cs[i].Confidence > cs[j].Confidence
		}
		return cs[i].OrgID.String() < cs[j].OrgID.String()
	})
}

func eachID(rows pgx.Rows, fn func(ids.OrganizationID)) error {
	defer rows.Close()
	for rows.Next() {
		var id ids.OrganizationID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		fn(id)
	}
	return rows.Err()
}

// consentedPerson returns the id of an EXISTING person the signal may be
// linked to: the raw email must belong to a person currently employed at
// the matched org, AND that person must hold a recorded consent grant
// (person_consent.state='granted'). Anything less stays company-level —
// and no person is ever created here (P12).
func consentedPerson(ctx context.Context, tx pgx.Tx, email string, orgID ids.OrganizationID) (*ids.PersonID, error) {
	if email == "" {
		return nil, nil
	}
	var personID ids.PersonID
	err := tx.QueryRow(ctx, `
		SELECT pe.person_id
		FROM person_email pe
		JOIN relationship r ON r.person_id = pe.person_id
		 AND r.kind = 'employment' AND r.organization_id = $2
		 AND r.ended_at IS NULL AND r.archived_at IS NULL
		WHERE pe.email = $1
		  AND EXISTS (SELECT 1 FROM person_consent pc
		              WHERE pc.person_id = pe.person_id AND pc.state = 'granted')
		ORDER BY pe.is_primary DESC, pe.person_id
		LIMIT 1`, email, orgID).Scan(&personID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("consent-gated person match: %w", err)
	}
	// resolved_person_id is a read of that person: only link one the caller
	// can see under row-scope, else the signal stays company-level.
	switch err := auth.EnsureLinkTarget(ctx, tx, "person", personID.UUID); {
	case err == nil:
		return &personID, nil
	case errors.Is(err, apperrors.ErrNotFound):
		return nil, nil
	default:
		return nil, err
	}
}

// appendMatchBasis writes the append-only inspectable match record.
func appendMatchBasis(ctx context.Context, tx pgx.Tx, actor principal.Principal, signalID ids.SignalID,
	matchedOn string, orgID *ids.OrganizationID, confidence *float64, detail string) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO signal_resolution (id, workspace_id, signal_id, matched_on, matched_org_id, match_confidence, match_detail, source, captured_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		ids.NewV7(), storekit.MustWorkspace(ctx), signalID, matchedOn, orgID, confidence, detail,
		"resolver", actor.ID); err != nil {
		return fmt.Errorf("append match basis: %w", err)
	}
	return nil
}

// candidateDetail renders the inspectable {candidates, chosen, reason}
// jsonb payload.
func candidateDetail(cs []candidate, chosen *candidate) (string, error) {
	entries := make([]map[string]any, len(cs))
	for i, c := range cs {
		entries[i] = map[string]any{
			"org_id":     c.OrgID,
			"matched_on": c.MatchedOn,
			"confidence": c.Confidence,
			"reason":     c.Detail,
		}
	}
	detail := map[string]any{"candidates": entries}
	if chosen != nil {
		detail["chosen"] = chosen.OrgID
		detail["reason"] = chosen.Detail
	} else {
		detail["reason"] = "multiple plausible organizations — flagged for review, never silently asserted"
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return "", fmt.Errorf("marshal match detail: %w", err)
	}
	return string(raw), nil
}

// resolvedPayload is the events.md §5.11 signal.resolved shape.
func resolvedPayload(sig crmcontracts.Signal, candidates []candidate) map[string]any {
	payload := map[string]any{
		"signal_id":        sig.Id,
		"resolution_state": sig.ResolutionState,
	}
	if sig.ResolvedOrgId != nil {
		payload["resolved_org_id"] = *sig.ResolvedOrgId
	}
	if sig.ResolvedPersonId != nil {
		payload["resolved_person_id"] = *sig.ResolvedPersonId
	}
	if len(candidates) > 0 {
		payload["matched_on"] = candidates[0].MatchedOn
		payload["match_confidence"] = candidates[0].Confidence
	}
	return payload
}
