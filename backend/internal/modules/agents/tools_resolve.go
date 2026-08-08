// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The resolve_entities tool (🟢): the question every capture flow has to answer
// before it can propose anything — do these names, addresses and numbers
// already name records here?
//
// WHY IT IS NOT A SEARCH. Search matches text and ranks it. Identity is decided
// by keys: a shared address, a phone number, an established channel binding, a
// company domain. The two give different answers to the same string, and only
// one of them can be acted on — a caller that creates a person because a search
// found nothing has created the duplicate this tool exists to prevent.
//
// IT DECIDES NOTHING AND MERGES NOBODY. A near-match is a comparison a person
// makes, and this answers `ambiguous` however high the score. Merging stays 🟡
// and goes through merge_records, with a human in it.
//
// WHAT THIS TOOL OWNS is the same half query_workspace owns: the resolver
// answers ids over a workspace-wide ladder, and every one of them is READ BACK
// through the datasource seam before it reaches the caller. That is where this
// caller's own object RBAC and row scope are applied, where the trust tier is
// stamped, and where the record is charged against MCP-SESS-READS.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The decisions this tool publishes, which are the resolver's verdicts read
// through what the CALLER may see.
const (
	// ResolveDecisionMatched: one record, reached by a unique key. Act on it.
	ResolveDecisionMatched = "matched"
	// ResolveDecisionAmbiguous: more than one record could be meant, or one
	// could be meant on a similarity nobody has confirmed. A person decides.
	ResolveDecisionAmbiguous = "ambiguous"
	// ResolveDecisionUnresolved: nothing here names this candidate.
	ResolveDecisionUnresolved = "unresolved"
)

// CodeResolutionRowScoped is the call-level warning that row scope narrowed an
// answer. It carries no count and names no candidate, for the reason the whole
// design turns on: how many records a caller may not see is the size of the
// hidden set.
const CodeResolutionRowScoped = "resolution_narrowed_by_row_scope"

// resolveMaxCandidates bounds one call. Each candidate runs the ladder — up to
// four indexed lookups and a trigram scan — and every record it names is read
// back and charged, so the batch size is work AND spend the caller chooses.
// Twenty is well past what a business card, a signature block or a meeting note
// carries, and far short of a sweep.
const resolveMaxCandidates = 20

// EntityResolver answers which records a batch of payloads already names.
//
// It answers REFS, never records: the people module cannot shape a wire record
// and this package cannot import it. Hydration is the tool's job, and doing it
// here is what puts every named record through this surface's own read path.
//
// The refs it returns are workspace-wide and NOT scoped to the caller — that is
// deliberate on the resolver's side (a duplicate is a duplicate whoever is
// looking), and it is precisely why this tool may not serve one unread.
type EntityResolver func(ctx context.Context, in []ResolveCandidate) ([]ResolveOutcome, error)

// ResolveCandidate is one payload to resolve, as it crosses the seam.
type ResolveCandidate struct {
	Kind      string
	Name      string
	LegalName string
	Emails    []string
	Phones    []string
	Domains   []string
}

// ResolveOutcome is the resolver's answer for one candidate.
type ResolveOutcome struct {
	// Verdict is the LADDER's word — "exact", "ambiguous" or "none" — before
	// this caller's visibility has been applied to it.
	Verdict string
	Refs    []ResolveRef
}

// ResolveRef is one record the resolver named, and what named it.
type ResolveRef struct {
	Kind       string
	ID         ids.UUID
	Confidence float64
	MatchedOn  string
}

// The ladder verdicts, as the seam spells them. They are restated here rather
// than imported because the module that defines them is a sibling, and a
// mismatch would not fail anything at runtime — it would silently read every
// exact hit as ambiguous. TestTheSurfaceAndTheResolverAgreeOnVerdicts in the
// composition layer, the one place that can see both, fails on a divergence.
const (
	ResolveVerdictExact     = "exact"
	ResolveVerdictAmbiguous = "ambiguous"
	ResolveVerdictNone      = "none"
)

// RegisterResolveTool joins resolve_entities to the surface once a resolver
// exists — the same conditional registration the other injected-engine tools
// take.
func RegisterResolveTool(r *Registry, p datasource.SystemOfRecordProvider, resolve EntityResolver) {
	if resolve == nil {
		return
	}
	r.Register(resolveEntities{p: p, resolve: resolve})
}

type resolveEntities struct {
	p       datasource.SystemOfRecordProvider
	resolve EntityResolver
}

func (t resolveEntities) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "resolve_entities", Title: "Resolve people and companies", Version: toolVersionV1,
		Description:   resolveEntitiesCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		InputSchema: schema(`{"type":"object","required":["candidates"],"properties":{
			"candidates":{"type":"array","minItems":1,"maxItems":20,"items":{
				"type":"object","required":["kind"],"properties":{
					"kind":{"type":"string","enum":["person","organization"],"description":"Which record type this payload is asking about. Leads are not resolved."},
					"ref":{"type":"string","description":"Your own label for this candidate, echoed back on its answer so a batch can be lined up. Any string; it is never stored."},
					"name":{"type":"string","description":"Full name for a person, trading name for a company."},
					"legal_name":{"type":"string","description":"The registered company name, when it differs from the trading name. Read for an organization only."},
					"emails":{"type":"array","items":{"type":"string"},"description":"Every address on the payload, not just the primary one. For an organization each address also contributes its domain, unless it is a consumer mail domain."},
					"phones":{"type":"array","items":{"type":"string"},"description":"Phone numbers in E.164 form; one that does not normalize is not a key and is ignored."},
					"domains":{"type":"array","items":{"type":"string"},"description":"Company domains claimed by the payload. Read for an organization only."}},
				"additionalProperties":false}}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[ResolveEntitiesResult](),
	}
}

func (t resolveEntities) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Candidates []struct {
			Kind      string   `json:"kind"`
			Ref       string   `json:"ref"`
			Name      string   `json:"name"`
			LegalName string   `json:"legal_name"`
			Emails    []string `json:"emails"`
			Phones    []string `json:"phones"`
			Domains   []string `json:"domains"`
		} `json:"candidates"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	if len(args.Candidates) == 0 {
		return nil, &BadArgsError{Cause: errors.New("`candidates` is required and takes at least one payload to resolve")}
	}
	if len(args.Candidates) > resolveMaxCandidates {
		return nil, &BadArgsError{Cause: fmt.Errorf(
			"`candidates` takes at most %d payloads per call and this one carries %d; split the batch",
			resolveMaxCandidates, len(args.Candidates))}
	}
	seam := make([]ResolveCandidate, 0, len(args.Candidates))
	labels := make([]string, 0, len(args.Candidates))
	for _, c := range args.Candidates {
		seam = append(seam, ResolveCandidate{
			Kind: c.Kind, Name: c.Name, LegalName: c.LegalName,
			Emails: c.Emails, Phones: c.Phones, Domains: c.Domains,
		})
		labels = append(labels, c.Ref)
	}
	outcomes, err := t.resolve(ctx, seam)
	if err != nil {
		return nil, err
	}
	if len(outcomes) != len(seam) {
		return nil, fmt.Errorf("crmagents: the resolver answered %d of %d candidates", len(outcomes), len(seam))
	}
	return marshalResult(t.hydrate(ctx, labels, outcomes))
}

// hydrate turns each outcome's refs into records through the datasource seam,
// and turns the ladder's verdict into the decision this caller is owed.
//
// THE READ IS WHAT MAKES THE ANSWER THE CALLER'S. The ladder is workspace-wide,
// so a ref may name a record this caller may not see; serving it would disclose
// a record by id, and disclosing it AS A MATCH would additionally confirm that
// the address or number they sent belongs to it.
func (t resolveEntities) hydrate(ctx context.Context, labels []string, outcomes []ResolveOutcome) (ResolveEntitiesResult, error) {
	result := ResolveEntitiesResult{Candidates: make([]ResolvedCandidate, 0, len(outcomes))}
	// One bookkeeping for the whole batch, because two candidates routinely name
	// ONE record — a card carrying two addresses, or a name and a phone number
	// that belong to the same person. Stamping it per candidate would charge the
	// caller twice for a record they were shown once, against a bound that
	// measures what was handed over.
	served := newServedRecords()
	narrowed := false
	for i, outcome := range outcomes {
		matches, dropped, err := t.readable(ctx, outcome.Refs, served)
		if err != nil {
			return ResolveEntitiesResult{}, err
		}
		narrowed = narrowed || dropped
		result.Candidates = append(result.Candidates, ResolvedCandidate{
			Ref: labels[i], Decision: decisionFor(outcome.Verdict, matches), Matches: matches,
		})
	}
	if narrowed {
		// ONE warning for the whole call, with no count and no candidate named.
		// Per-candidate it would be a probe: send one address at a time and the
		// warning answers "a record you cannot see holds this address" — the
		// disclosure the drop exists to prevent, restored one call later.
		noteWarning(ctx, CodeResolutionRowScoped,
			"at least one record this payload could have named is outside your visibility and was "+
				"left out; an answer of `unresolved` here does not prove no such record exists")
	}
	return result, nil
}

// readable reads every ref and keeps the ones this caller may see, reporting
// whether any were dropped.
//
// A DENIAL AND A FAULT ARE NOT THE SAME ANSWER. Not-found and permission-denied
// are the seam saying no, which is the verdict this function exists to collect.
// Anything else is the absence of a verdict, and turning it into `unresolved`
// would tell a caller that no record names this address when what happened is
// that nothing could be read — and they would go on to create the duplicate.
func (t resolveEntities) readable(ctx context.Context, refs []ResolveRef, served *servedRecords) ([]ResolvedRecord, bool, error) {
	out := make([]ResolvedRecord, 0, len(refs))
	dropped := false
	for _, ref := range refs {
		record, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(ref.Kind), ID: ref.ID})
		if err != nil {
			if errors.Is(err, apperrors.ErrNotFound) || errors.Is(err, apperrors.ErrPermissionDenied) {
				dropped = true
				continue
			}
			return nil, false, err
		}
		out = append(out, ResolvedRecord{
			Record: served.stamp(ctx, record), Confidence: ref.Confidence, MatchedOn: ref.MatchedOn,
		})
	}
	return out, dropped, nil
}

// decisionFor reads the ladder's verdict through what survived hydration.
//
// TWO RULES, AND THEY PULL IN OPPOSITE DIRECTIONS ON PURPOSE.
//
// An answer with nothing left is `unresolved` — the SAME word a genuine miss
// gets. A distinct "resolved to something you cannot see" would be a probe: a
// caller could learn that an address belongs to a record they are not allowed
// to know exists, one candidate at a time.
//
// An ambiguous verdict STAYS ambiguous even when only one record survived.
// Collapsing it to `matched` would resolve a disagreement using the fact that
// the rival is hidden — telling the caller "this is definitely them" precisely
// because the record that contradicts it is out of their reach.
func decisionFor(verdict string, matches []ResolvedRecord) string {
	if len(matches) == 0 {
		return ResolveDecisionUnresolved
	}
	if verdict == ResolveVerdictExact && len(matches) == 1 {
		return ResolveDecisionMatched
	}
	return ResolveDecisionAmbiguous
}
