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

// ResolveOutcome is one candidate's answer: the records its keys or its name
// named, in ladder precedence.
//
// There is no verdict word here. Whether an answer is a match, an ambiguity or a
// miss depends on which of these records the ASKING caller may read, and this
// read does not know that — it is workspace-wide by design. The refs are the
// facts; the word is the caller's, and the tool derives it.
type ResolveOutcome struct {
	Refs []ResolveRef
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

// resolvePerson answers which people this payload names.
//
// IT ASKS THE EXACT LANES ONE KEY AT A TIME, and that is the one place this read
// deliberately diverges from what the write paths do. `DedupePerson` ROUTES: its
// email lane takes `ORDER BY person_id LIMIT 1` across every address, because a
// message must land somewhere and landing it on the record whose binding was
// established first is better than deferring it to a human. A read has nothing
// to land. Handed a card carrying two addresses that belong to two different
// people, the routed answer is one id chosen by uuid order, reported with
// certainty — and the tool above publishes that as "act on this".
//
// So the lanes are asked per key and every distinct owner is kept. No matching
// logic is written here: these are the ladder's own lane functions, asked a
// question the routing answer cannot express.
//
// The FUZZY tier is untouched and still the ladder's, reached only when no key
// matched at all.
func resolvePerson(ctx context.Context, tx pgx.Tx, c ResolveCandidate, consumerMail *freemail.Matcher) (ResolveOutcome, error) {
	keyed, err := exactPersonOwners(ctx, tx, c)
	if err != nil {
		return ResolveOutcome{}, err
	}
	if len(keyed) > 0 {
		return ResolveOutcome{Refs: keyed}, nil
	}
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

// exactPersonOwners is every DISTINCT person the candidate's keys name, in
// ladder precedence: addresses first, then phone numbers.
//
// A PHONE HIT IS NOT MARKED EXACT, and this is the module's own policy rather
// than a judgement made here: `resolvecreate.go` refuses a create on an exact
// collision *unless the lane was the phone one*, because households, reception
// desks and switchboards share numbers. A read that published a lone phone match
// as actionable would contradict the write path one file over.
func exactPersonOwners(ctx context.Context, tx pgx.Tx, c ResolveCandidate) ([]ResolveRef, error) {
	var out []ResolveRef
	seen := map[ids.PersonID]bool{}
	// The first lane to name someone keeps them: precedence is the ladder's, and
	// a person found by both their address and their phone is an address match.
	keep := func(id ids.PersonID, found bool, lane string, exact bool) {
		if !found || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, ResolveRef{
			Kind: ResolvePerson, ID: id.UUID, Exact: exact, Confidence: 1, MatchedOn: lane,
		})
	}
	for _, email := range c.Emails {
		hit, found, err := exactPersonByEmail(ctx, tx, []string{email})
		if err != nil {
			return nil, err
		}
		keep(hit, found, laneEmail, true)
	}
	for _, phone := range c.Phones {
		hit, found, err := exactPersonByPhone(ctx, tx, []string{phone})
		if err != nil {
			return nil, err
		}
		keep(hit, found, lanePhone, false)
	}
	return out, nil
}

// personOutcome translates the ladder's FUZZY answer, kept apart from the query
// so the rule it encodes can be held to without a database.
//
// It never answers an exact hit: exactPersonOwners has already run and found
// none by the time this is reached.
func personOutcome(match PersonResolution) ResolveOutcome {
	if match.Decision != DecisionFuzzyReview {
		return ResolveOutcome{}
	}
	// Not Exact, however high the score: the fuzzy tier is a comparison a person
	// makes, and a caller told "this is them" would write against a record
	// nobody confirmed.
	return ResolveOutcome{Refs: []ResolveRef{{
		Kind: ResolvePerson, ID: match.PersonID.UUID,
		Confidence: match.Confidence, MatchedOn: axisFullName,
	}}}
}

// resolveOrganization answers which companies this payload names.
//
// It asks the domain lane PER DOMAIN, for the reason exactPersonOwners does:
// `exactOrgByDomain` takes the lowest id across every domain it is handed, so a
// card carrying two employers' addresses would resolve to one company chosen by
// uuid order and be published as certain.
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
	domains := companyDomains(c, consumerMail)
	keyed, err := exactOrganizationOwners(ctx, tx, domains)
	if err != nil {
		return ResolveOutcome{}, err
	}
	if len(keyed) > 0 {
		return ResolveOutcome{Refs: keyed}, nil
	}
	match, err := DedupeOrganization(ctx, tx, OrganizationCandidate{
		DisplayName: c.Name,
		LegalName:   c.LegalName,
		Domains:     domains,
	})
	if err != nil {
		return ResolveOutcome{}, err
	}
	return organizationOutcome(match), nil
}

// exactOrganizationOwners is every DISTINCT organization the candidate's domains
// name. A domain belongs to one company, so each hit is exact; two domains
// naming two companies is a contradiction the caller has to see.
func exactOrganizationOwners(ctx context.Context, tx pgx.Tx, domains []string) ([]ResolveRef, error) {
	var out []ResolveRef
	seen := map[ids.OrganizationID]bool{}
	for _, domain := range domains {
		hit, found, err := exactOrgByDomain(ctx, tx, []string{domain}, nil)
		if err != nil {
			return nil, err
		}
		if !found || seen[hit] {
			continue
		}
		seen[hit] = true
		out = append(out, ResolveRef{
			Kind: ResolveOrganization, ID: hit.UUID, Exact: true, Confidence: 1, MatchedOn: axisDomain,
		})
	}
	return out, nil
}

// organizationOutcome translates PO-F-2's FUZZY answer, split from its query for
// the same reason personOutcome is, and reached only when no domain matched.
func organizationOutcome(match OrganizationMatch) ResolveOutcome {
	if match.Decision != DecisionFuzzyReview {
		return ResolveOutcome{}
	}
	refs := make([]ResolveRef, 0, len(match.Ranked))
	for _, scored := range match.Ranked {
		refs = append(refs, ResolveRef{
			Kind: ResolveOrganization, ID: scored.OrganizationID.UUID,
			Confidence: scored.Confidence, MatchedOn: scored.MatchedField,
		})
	}
	return ResolveOutcome{Refs: refs}
}

// companyDomains is the candidate's claimed domains plus each email's own,
// normalized, de-duplicated and with every consumer-mail domain dropped.
func companyDomains(c ResolveCandidate, consumerMail *freemail.Matcher) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(c.Domains)+len(c.Emails))
	add := func(domain string) {
		// The canonical parser, not a lowercase: a model handed "company
		// domains" passes what is on the card, which is routinely
		// `https://www.acme.example/`. Hostname strips the scheme, the path and
		// the subdomain down to the registrable name the organization's own
		// domain rows are stored under — so the difference between an exact hit
		// and a name guess stops depending on how the caller happened to type it.
		domain, ok := freemail.Hostname(domain)
		if !ok || consumerMail.IsConsumer(domain) {
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
