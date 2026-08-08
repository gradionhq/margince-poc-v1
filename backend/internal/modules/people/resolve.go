// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Identity resolution for a payload nobody has written yet: a name, an address,
// a phone number, a domain — which record, if any, do they already name?
//
// It IS the dedupe ladder, asked as a question instead of as a step. PO-F-1 and
// PO-F-2 already answer exactly this, on every capture and every create; what
// they have never had is a caller that only wants the answer. So there is no
// second matching implementation here — this file assembles candidates, calls
// the one ladder, and reports what it said.
//
// IT WRITES NOTHING AND MERGES NOBODY. That is the whole posture: a fuzzy match
// is a comparison a human makes (DEDUPE_FUZZY_AUTOMERGE is pinned *never*), and
// this read exists so a caller can ASK before creating a duplicate rather than
// discover one afterwards.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/freemail"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ResolveKind is the record type a candidate is asking about. Person and
// organization are the two the ladder answers; a lead is deliberately not one
// of them, because no lead-matching tier exists and inventing one here would be
// a second matching implementation — the thing this file exists not to be.
type ResolveKind string

// The two kinds this read answers.
const (
	ResolvePerson       ResolveKind = "person"
	ResolveOrganization ResolveKind = "organization"
)

// ResolveCandidate is one thing a caller is holding and cannot name yet.
type ResolveCandidate struct {
	Kind ResolveKind
	// Name is the display name — a person's full name, an organization's
	// trading name.
	Name string
	// LegalName is the registered form, read only for an organization. The same
	// company is routinely captured under two spellings and the pair collides
	// only on this axis.
	LegalName string
	Emails    []string
	Phones    []string
	// Domains are claimed company domains. An organization candidate ALSO picks
	// up the domain of each email it carries — see resolveOrganization for why
	// that derivation is here rather than expected of the caller.
	Domains []string
}

// ResolveVerdict is the ladder's answer for one candidate, in the vocabulary
// this read publishes.
type ResolveVerdict string

const (
	// VerdictExact is a unique-key hit — same address, same phone, same
	// established channel binding, same company domain. Deterministic.
	VerdictExact ResolveVerdict = "exact"
	// VerdictAmbiguous is every case a person has to settle: a near-match at or
	// above the review threshold, or two exact lanes naming different records.
	// The ladder never resolves either one, and neither does this.
	VerdictAmbiguous ResolveVerdict = "ambiguous"
	// VerdictNone is no match at or above the threshold.
	VerdictNone ResolveVerdict = "none"
)

// ResolveOutcome is one candidate's answer: the verdict and the records it
// named, best first.
//
// Refs is a LIST even for an exact hit, so a caller reads one shape whichever
// verdict came back. It is empty exactly when the verdict is VerdictNone.
type ResolveOutcome struct {
	Verdict ResolveVerdict
	Refs    []ResolveRef
}

// ResolveRef is one record the ladder named, and what named it.
type ResolveRef struct {
	Kind ResolveKind
	ID   ids.UUID
	// Exact says a unique KEY named this record — an address, a phone number,
	// an established channel binding, a company domain — rather than a name
	// similarity. It is carried rather than inferred from Confidence, because a
	// caller deciding whether a match may be acted on must not be reading a
	// float comparison: the fuzzy tier can score 1.0 on two identical names,
	// and that is still a comparison a person makes.
	Exact bool
	// Confidence is 1 for an exact key — a shared address is not a probability
	// — and the ladder's own score for a fuzzy one.
	Confidence float64
	// MatchedOn is the axis the match came from: an exact lane's name
	// ("email", "phone", "channel_identity", "domain") or the stored side of a
	// fuzzy name pairing ("full_name", "display_name", "legal_name"). It is
	// what makes a match reviewable — a pair scored on a registered name must
	// not be read as a trading-name collision.
	MatchedOn string
}

// The fuzzy axis names. The person ladder scores one name axis; the
// organization ladder reports which of its two produced the winning pairing.
const (
	axisFullName = "full_name"
	axisDomain   = "domain"
)

// Resolve answers a batch of candidates in ONE transaction.
//
// One transaction rather than one per candidate, because a business card, an
// email signature or a meeting note names several parties at once and they must
// be resolved against the same snapshot: two candidates answered across a write
// could name a record the other was told does not exist.
//
// THE IDS IT ANSWERS ARE NOT ROW-SCOPED TO THE CALLER, and a caller that serves
// them onward owes that scoping itself. This is not an oversight: the ladder is
// workspace-wide on purpose, because a duplicate is a duplicate whoever is
// looking, and a match set that narrowed per caller would let the same payload
// create a second record for one user and not another. So what comes back is
// "which records exist" and never "which records you may see" — every id must
// be read back through a row-scoped read before it reaches anyone, which is
// what the resolve_entities tool does through the datasource seam.
func (s *Store) Resolve(ctx context.Context, candidates []ResolveCandidate) ([]ResolveOutcome, error) {
	if err := requireResolveAuthority(ctx, candidates); err != nil {
		return nil, err
	}
	out := make([]ResolveOutcome, 0, len(candidates))
	err := s.tx(ctx, func(tx pgx.Tx) error {
		consumerMail, err := s.consumerMailMatcher(ctx, tx)
		if err != nil {
			return err
		}
		out = out[:0]
		for _, candidate := range candidates {
			outcome, err := resolveOne(ctx, tx, candidate, consumerMail)
			if err != nil {
				return err
			}
			out = append(out, outcome)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// requireResolveAuthority takes the OBJECT grant for every kind the batch asks
// about, before any of it runs.
//
// Before, not per candidate: a batch that ran the person half and then refused
// on the organization half would have told the caller which addresses exist
// before deciding they were not allowed to ask. The row scope is a separate
// obligation and belongs to whoever serves these ids onward — see Resolve.
func requireResolveAuthority(ctx context.Context, candidates []ResolveCandidate) error {
	asked := map[ResolveKind]struct{}{}
	for _, c := range candidates {
		asked[c.Kind] = struct{}{}
	}
	for kind, object := range map[ResolveKind]string{
		ResolvePerson:       entityPerson,
		ResolveOrganization: entityOrganization,
	} {
		if _, wanted := asked[kind]; !wanted {
			continue
		}
		if err := auth.Require(ctx, object, principal.ActionRead); err != nil {
			return err
		}
	}
	return nil
}

// resolveOne routes to the ladder that answers this candidate's kind. An
// unknown kind is an error rather than an empty answer: "no match" and "I was
// never asked" are different facts, and only one of them says something about
// the workspace.
func resolveOne(ctx context.Context, tx pgx.Tx, c ResolveCandidate, consumerMail *freemail.Matcher) (ResolveOutcome, error) {
	switch c.Kind {
	case ResolvePerson:
		return resolvePerson(ctx, tx, c, consumerMail)
	case ResolveOrganization:
		return resolveOrganization(ctx, tx, c, consumerMail)
	default:
		return ResolveOutcome{}, fmt.Errorf("people: resolve: %q is not a record kind this read answers", c.Kind)
	}
}

// resolvePerson runs PO-F-1 and translates its result.
//
// A LANE CONFLICT IS AMBIGUITY, and this is the one translation worth reading
// twice. The ladder ROUTES a conflict — it has to, because an inbound message
// with nowhere to land is worse than one on the record whose binding was
// established first — and it reports the rival alongside. A read has no message
// to land, so routing past a disagreement would hand a caller one id and hide
// that another key says someone else. Both records come back, the routed one
// first, and the verdict says a person decides.
func resolvePerson(ctx context.Context, tx pgx.Tx, c ResolveCandidate, consumerMail *freemail.Matcher) (ResolveOutcome, error) {
	match, err := DedupePerson(ctx, tx, PersonCandidate{
		FullName:     c.Name,
		Emails:       c.Emails,
		Phones:       c.Phones,
		ConsumerMail: consumerMail,
	})
	if err != nil {
		return ResolveOutcome{}, err
	}
	return personOutcome(match), nil
}

// personOutcome is the translation, kept apart from the query so the rules it
// encodes can be held to without a database — they are the part of this file a
// reader has to be convinced by.
func personOutcome(match PersonResolution) ResolveOutcome {
	switch match.Decision {
	case DecisionExactCollision:
		routed := ResolveRef{
			Kind: ResolvePerson, ID: match.PersonID.UUID, Exact: true,
			Confidence: 1, MatchedOn: match.MatchedLane,
		}
		if match.Conflict == nil {
			return ResolveOutcome{Verdict: VerdictExact, Refs: []ResolveRef{routed}}
		}
		return ResolveOutcome{Verdict: VerdictAmbiguous, Refs: []ResolveRef{routed, {
			Kind: ResolvePerson, ID: match.Conflict.Rival.UUID, Exact: true,
			Confidence: 1, MatchedOn: match.Conflict.RivalLane,
		}}}
	case DecisionFuzzyReview:
		// Never VerdictExact, however high the score: the fuzzy tier is a
		// comparison a person makes, and a caller told "this is them" would
		// write against a record nobody confirmed.
		return ResolveOutcome{Verdict: VerdictAmbiguous, Refs: []ResolveRef{{
			Kind: ResolvePerson, ID: match.PersonID.UUID,
			Confidence: match.Confidence, MatchedOn: axisFullName,
		}}}
	default:
		return ResolveOutcome{Verdict: VerdictNone}
	}
}

// resolveOrganization runs PO-F-2 and translates its result.
//
// The domain list is WIDENED with each email's own domain, minus the consumer
// ones. A caller holding a business card has an address, not a domain, and the
// exact tier is keyed on domain — so expecting the caller to split the address
// themselves would make the difference between an exact hit and a name guess
// depend on how much string handling the caller happened to do. Filtering the
// consumer domains is not optional: the ladder's own contract says a free-mail
// domain must never reach it, and one that did would collide every private
// address onto whichever company first claimed that provider.
func resolveOrganization(ctx context.Context, tx pgx.Tx, c ResolveCandidate, consumerMail *freemail.Matcher) (ResolveOutcome, error) {
	match, err := DedupeOrganization(ctx, tx, OrganizationCandidate{
		DisplayName: c.Name,
		LegalName:   c.LegalName,
		Domains:     companyDomains(c, consumerMail),
	})
	if err != nil {
		return ResolveOutcome{}, err
	}
	return organizationOutcome(match), nil
}

// organizationOutcome is PO-F-2's translation, split from its query for the same
// reason personOutcome is.
func organizationOutcome(match OrganizationMatch) ResolveOutcome {
	switch match.Decision {
	case DecisionExactCollision:
		return ResolveOutcome{Verdict: VerdictExact, Refs: []ResolveRef{{
			Kind: ResolveOrganization, ID: match.OrganizationID.UUID, Exact: true,
			Confidence: 1, MatchedOn: axisDomain,
		}}}
	case DecisionFuzzyReview:
		refs := make([]ResolveRef, 0, len(match.Ranked))
		for _, scored := range match.Ranked {
			refs = append(refs, ResolveRef{
				Kind: ResolveOrganization, ID: scored.OrganizationID.UUID,
				Confidence: scored.Confidence, MatchedOn: scored.MatchedField,
			})
		}
		return ResolveOutcome{Verdict: VerdictAmbiguous, Refs: refs}
	default:
		return ResolveOutcome{Verdict: VerdictNone}
	}
}

// companyDomains is the candidate's claimed domains plus each email's own,
// normalized, de-duplicated and with every consumer-mail domain dropped.
func companyDomains(c ResolveCandidate, consumerMail *freemail.Matcher) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(c.Domains)+len(c.Emails))
	add := func(domain string) {
		domain = normalizeDomain(domain)
		if domain == "" || consumerMail.IsConsumer(domain) {
			return
		}
		if _, dup := seen[domain]; dup {
			return
		}
		seen[domain] = struct{}{}
		out = append(out, domain)
	}
	for _, domain := range c.Domains {
		add(domain)
	}
	for _, email := range c.Emails {
		if _, after, found := strings.Cut(email, "@"); found {
			add(after)
		}
	}
	return out
}
