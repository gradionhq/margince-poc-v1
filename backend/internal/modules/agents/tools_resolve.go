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

// CodeResolutionBoundedByVisibility is the standing caveat every resolve answer
// carries: this answer is bounded by what you may read, so `unresolved` does not
// prove that no such record exists.
//
// IT IS UNCONDITIONAL, and that is the whole design rather than laziness.
// Raising it only when a record WAS withheld looks tighter and is the leak: a
// caller sends a batch of one address, and the warning's presence answers "a
// record you cannot see holds this" — restoring, one field over, exactly the
// oracle that answering `unresolved` for a withheld match exists to close. A
// signal that fires only when there is something to hide is a disclosure.
//
// Saying it always costs nothing a caller needs. What they have to act on is
// that a miss is not proof, and that is true of every call.
const CodeResolutionBoundedByVisibility = "resolution_bounded_by_visibility"

// resolveMaxCandidates bounds one call. Each candidate runs the ladder — up to
// four indexed lookups and a trigram scan — and every record it names is read
// back and charged, so the batch size is work AND spend the caller chooses.
// Twenty is well past what a business card, a signature block or a meeting note
// carries, and far short of a sweep.
const resolveMaxCandidates = 20

// resolveKinds is the closed set of record types a candidate may ask about,
// spelled with the seam's own constants so the check and the value that crosses
// the seam cannot drift apart.
var resolveKinds = map[string]bool{
	string(datasource.EntityPerson):       true,
	string(datasource.EntityOrganization): true,
}

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

// ResolveOutcome is the resolver's answer for one candidate: the records the
// ladder named, best first.
//
// It carries NO verdict. The ladder has one — it knows whether two exact lanes
// disagreed — but that word is computed over records this caller may not be able
// to read, and a decision derived from it is a decision a hidden record helped
// make. What crosses the seam is the refs; the word is this tool's, and it is
// built from the refs that survive.
type ResolveOutcome struct {
	Refs []ResolveRef
}

// ResolveRef is one record the resolver named, and what named it.
type ResolveRef struct {
	Kind string
	ID   ids.UUID
	// Exact says a unique KEY named this record rather than a name similarity.
	// It is the only thing that can make a single match actionable, and it is
	// carried per ref because the decision is computed from the refs that
	// SURVIVED this caller's visibility — see decisionFor.
	Exact      bool
	Confidence float64
	MatchedOn  string
}

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
		// The kind is validated HERE because nothing else does. The registry
		// checks required-presence on TOP-LEVEL members, id shape and numeric
		// bounds; a nested `required` and a JSON-Schema `enum` are advertised and
		// never enforced. Unchecked, an unknown kind reaches the resolver's own
		// switch and comes back as an internal fault naming the module that
		// raised it — a server error, and a module name, for what is a typo in
		// the call.
		if !resolveKinds[c.Kind] {
			return nil, &BadArgsError{Cause: fmt.Errorf(
				"`kind` takes person or organization, not %q; leads are not resolved", c.Kind)}
		}
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
	// Raised BEFORE anything is read, so it cannot depend on what was found.
	// See CodeResolutionBoundedByVisibility: a caveat that appears only when
	// something was withheld is a disclosure that something was withheld, and a
	// batch of one candidate turns that into the address oracle this tool's
	// `unresolved` answer exists to close.
	noteWarning(ctx, CodeResolutionBoundedByVisibility,
		"this answer is bounded by the records you may read, so `unresolved` does not prove that no "+
			"such record exists; a person with wider visibility may see one")
	unanswered := 0
	for i, outcome := range outcomes {
		matches, err := t.readable(ctx, outcome.Refs, served)
		if err != nil {
			return ResolveEntitiesResult{}, err
		}
		if len(matches) == 0 {
			unanswered++
		}
		result.Candidates = append(result.Candidates, ResolvedCandidate{
			Ref: labels[i], Decision: decisionFor(matches), Matches: matches,
		})
	}
	// A candidate that came back with nothing still COST the caller a question,
	// and it is the one shape the per-record charge cannot see: no record was
	// served, so nothing was stamped. Left free, `unresolved` would be an
	// unlimited lookup — ask about every address in turn and the bound never
	// moves. Charging it is also what makes the batch cap mean something.
	noteProbe(ctx, unanswered)
	return result, nil
}

// readable reads every ref and keeps the ones this caller may see.
//
// It reports nothing about what it dropped, and it has nowhere to report it to:
// the caveat above is unconditional, so a drop leaves no trace in the answer at
// all. That is the property — a withheld match and a record that never existed
// produce byte-identical results.
//
// A DENIAL AND A FAULT ARE NOT THE SAME ANSWER. Not-found and permission-denied
// are the seam saying no, which is the verdict this function exists to collect.
// Anything else is the absence of a verdict, and turning it into `unresolved`
// would tell a caller that no record names this address when what happened is
// that nothing could be read — and they would go on to create the duplicate.
func (t resolveEntities) readable(ctx context.Context, refs []ResolveRef, served *servedRecords) ([]ResolvedRecord, error) {
	out := make([]ResolvedRecord, 0, len(refs))
	for _, ref := range refs {
		record, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(ref.Kind), ID: ref.ID})
		if err != nil {
			if errors.Is(err, apperrors.ErrNotFound) || errors.Is(err, apperrors.ErrPermissionDenied) {
				continue
			}
			return nil, err
		}
		out = append(out, ResolvedRecord{
			Record: served.stamp(ctx, record), Confidence: ref.Confidence,
			MatchedOn: ref.MatchedOn, exact: ref.Exact,
		})
	}
	return out, nil
}

// decisionFor is computed from the records that SURVIVED this caller's
// visibility, and from nothing else. That is the whole rule, and it is worth
// stating as a rule because the natural implementation breaks it.
//
// Carrying the ladder's own verdict through was tried and is an oracle. The
// ladder sees the whole workspace, so a candidate mixing one address you can
// read with one phone number you are guessing comes back `ambiguous` when the
// phone belongs to a record you may not see, and `exact` when it belongs to
// nobody — with an identical single visible match either way. The DECISION WORD
// then answers a question about a hidden record, one probe at a time.
//
// Reading only the survivors closes it: both cases are one exact match, and both
// answer `matched`. What the caller loses is a warning that something out of
// their reach disagrees — which they could not act on, could not see, and were
// never entitled to. CodeResolutionBoundedByVisibility says the general form of
// it on every call, which is the honest version of the same caution.
//
// A single FUZZY match is still `ambiguous`, and that is not a visibility rule:
// the fuzzy tier is a comparison a person makes, DEDUPE_FUZZY_AUTOMERGE is
// pinned *never*, and a caller told "matched" would write against a record
// nobody confirmed.
func decisionFor(matches []ResolvedRecord) string {
	if len(matches) == 0 {
		return ResolveDecisionUnresolved
	}
	if len(matches) == 1 && matches[0].exact {
		return ResolveDecisionMatched
	}
	return ResolveDecisionAmbiguous
}
