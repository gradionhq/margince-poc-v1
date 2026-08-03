// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's page-parallel fact lane: one SMALL call per
// fact-bearing page, each with a kind-routed field menu and the page's
// numbered passages, answered in compact records that cite a snippet id
// instead of quoting ({"f","v","e"} ≈ 30 output tokens per fact). Both
// the field name and the snippet id are SCHEMA ENUMS, so an unknown
// field or an uncitable id cannot even be generated; the gate resolves
// each citation and demands the value's name in the cited passage —
// the no-guess property, at a fraction of the tokens. The calls are
// independent, so the orchestrator fans them out concurrently on the
// fast routing tier: their latency IS the deep read's wall clock.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

// pageMenu is what one page kind is asked for: the fact fields it may
// answer, and whether its call carries the people or legal-entity lanes.
type pageMenu struct {
	factFields []string
	people     bool
	entities   bool
}

// menuForKind routes a page kind to its menu; ok=false means the page
// makes NO call (boilerplate and unclassified pages state few facts and
// their calls would dominate cost, not quality).
func menuForKind(kind crmcontracts.SiteReadPageKind) (pageMenu, bool) {
	company := people.OrganizationFactFields[companyWord]
	offeringAndMarket := factFields("offering", "market")
	switch kind {
	case crmcontracts.SiteReadPageKindImpressum:
		return pageMenu{factFields: company, entities: true}, true
	case crmcontracts.SiteReadPageKindContact:
		return pageMenu{factFields: company}, true
	case crmcontracts.SiteReadPageKindServices, crmcontracts.SiteReadPageKindProducts:
		return pageMenu{factFields: append(offeringAndMarket, people.FactTechnology)}, true
	case crmcontracts.SiteReadPageKindHome, crmcontracts.SiteReadPageKindAbout:
		// These pages keep the people lane: an about page's founders and
		// named staff are exactly the contacts worth having. What they must
		// not yield is the testimonial wall, and the published-email floor
		// is what separates the two — a company prints an address for the
		// person you should talk to, and never for the customer it is
		// quoting.
		return pageMenu{factFields: append(factFields("offering", "market", "signal"), people.FactLocation), people: true}, true
	case crmcontracts.SiteReadPageKindTeam:
		return pageMenu{people: true}, true
	default:
		return pageMenu{}, false
	}
}

func factFields(categories ...string) []string {
	var out []string
	for _, category := range categories {
		out = append(out, people.OrganizationFactFields[category]...)
	}
	return out
}

// factCategoryByField inverts the closed vocabulary: fact field names
// are globally unique (fitness-tested), so the model never states a
// category and the gate derives it.
var factCategoryByField = invertFactFields()

func invertFactFields() map[string]string {
	byField := map[string]string{}
	for category, fields := range people.OrganizationFactFields {
		for _, field := range fields {
			byField[field] = category
		}
	}
	return byField
}

// pageFactsSystem is the per-page prompt. Small on purpose: the field
// menu and the guidance line are the whole instruction. The fence is the
// one wrapping the passages in the SAME call — its rule closes the prompt,
// because the boundary is minted per call and the model has to be told
// which marker it is looking at.
func pageFactsSystem(menu pageMenu, fence promptfence.Fence) string {
	var b strings.Builder
	b.WriteString("You extract company facts from ONE page of a company's website for a CRM. The page is given as numbered passages [s0], [s1], ….\n")
	b.WriteString(`Return ONLY a JSON object: {"facts":[...]`)
	if menu.people {
		b.WriteString(`,"people":[...]`)
	}
	if menu.entities {
		b.WriteString(`,"entities":[...]`)
	}
	b.WriteString("}.\n")
	if len(menu.factFields) > 0 {
		fmt.Fprintf(&b, "facts — one entry per distinct item: {\"f\":field,\"v\":value,\"e\":passage id}. Allowed fields: %s. %s\n",
			strings.Join(menu.factFields, ", "), menuGuidance(menu.factFields))
		b.WriteString("For list fields spell v as the item's name, then ' — ', then a short description when the page gives one. The item's NAME must appear in the passage you cite.\n")
	} else {
		b.WriteString("facts must be empty for this page.\n")
	}
	if menu.people {
		b.WriteString("people — ONLY people this page itself publishes: {\"n\":full name,\"r\":stated role,\"m\":email,\"l\":linkedin url,\"e\":passage id}. Include m or l ONLY when the page prints that exact address or URL — omit otherwise, NEVER guess. Name and role must appear in the cited passage.\n")
	}
	if menu.entities {
		b.WriteString("entities — EVERY distinct legal entity this legal page names: {\"n\":entity name,\"a\":registered address,\"r\":registration/VAT/tax number,\"e\":passage id}. " +
			"A legal notice states each entity as a block: give the address and the registration number printed WITH that entity's name, copied exactly as printed. " +
			"For r copy ONE complete legal identifier exactly as printed, preferring a commercial-register number over a VAT/tax number; never combine several identifiers. " +
			"a and r are ALWAYS present in your answer — use an empty string when the page states none for that entity, and never carry one entity's detail onto another. " +
			"A market, office or brand label (\"Acme Singapore\", \"DACH\") is NOT an entity: the entity is the registered company name printed under that label (\"Acme Pte. Ltd.\"). List every entity.\n")
	}
	b.WriteString("Cite the passage id that states each item. OMIT anything the page does not state — never guess.\n")
	b.WriteString(fence.Rule("page"))
	return b.String()
}

// menuGuidance narrows categoryGuidance to the fields the menu offers.
func menuGuidance(fields []string) string {
	present := map[string]bool{}
	for _, f := range fields {
		present[f] = true
	}
	var parts []string
	for _, category := range []string{companyWord, "offering", "signal"} {
		for _, f := range people.OrganizationFactFields[category] {
			if present[f] {
				parts = append(parts, categoryGuidance[category])
				break
			}
		}
	}
	return strings.Join(parts, " ")
}

// pageFactsSchema pins the reply shape at generation: the field AND the
// snippet id are enums of exactly what this page offers.
func pageFactsSchema(menu pageMenu, snippetIDs []string) json.RawMessage {
	props := map[string]schema.Node{}
	required := []string{"facts"}
	factItem := map[string]schema.Node{
		"v": schema.String().Describe("The item's value."),
		"e": schema.Enum(snippetIDs...).Describe("The passage id that states it."),
	}
	if len(menu.factFields) > 0 {
		factItem["f"] = schema.Enum(menu.factFields...).Describe("Which fact field this is.")
		props["facts"] = schema.Array(schema.Object(factItem, "f", "v", "e"))
	} else {
		// The lane key stays present (one envelope shape for the shared
		// validator) but can only hold nothing.
		props["facts"] = schema.Array(schema.Object(factItem, "v", "e"))
	}
	if menu.people {
		props["people"] = schema.Array(schema.Object(map[string]schema.Node{
			"n": schema.String().Describe("The person's full name as printed."),
			"r": schema.String().Describe("The person's stated role."),
			"m": schema.String().Describe("An email ONLY if this page prints it verbatim."),
			"l": schema.String().Describe("A LinkedIn URL ONLY if this page prints it verbatim."),
			"e": schema.Enum(snippetIDs...).Describe("The passage id naming the person."),
		}, "n", "r", "e"))
		required = append(required, "people")
	}
	if menu.entities {
		props["entities"] = schema.Array(schema.Object(map[string]schema.Node{
			"n": schema.String().Describe("The legal entity's name as printed."),
			"a": schema.String().Describe("Its registered address exactly as printed for THIS entity; empty string if the page states none."),
			"r": schema.String().Describe("Its registration, VAT, UID or tax number exactly as printed for THIS entity; empty string if the page states none."),
			"e": schema.Enum(snippetIDs...).Describe("The passage id naming it."),
		}, "n", "a", "r", "e"))
		required = append(required, "entities")
	}
	return schema.Must(schema.Object(props, required...))
}

// pageFactsReply is the compact JSON shape every page call answers in.
type pageFactsReply struct {
	Facts []struct {
		F string `json:"f"`
		V string `json:"v"`
		E string `json:"e"`
	} `json:"facts"`
	People []struct {
		N string `json:"n"`
		R string `json:"r"`
		M string `json:"m"`
		L string `json:"l"`
		E string `json:"e"`
	} `json:"people"`
	Entities []struct {
		N string `json:"n"`
		A string `json:"a"`
		R string `json:"r"`
		E string `json:"e"`
	} `json:"entities"`
}

// pageFactsShapeValid is the retry pipeline's parse check.
func pageFactsShapeValid(text string) error {
	var parsed pageFactsReply
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &parsed); err != nil {
		return fmt.Errorf("output must be {\"facts\":[...]} (+people/entities where asked): %w", err)
	}
	return nil
}

// pageFactsResult is one page's gate-surviving findings.
type pageFactsResult struct {
	url      string
	kind     crmcontracts.SiteReadPageKind
	facts    []people.DeepReadFact
	people   []sitePerson
	entities []corpusLegalEntity
}

// extractPageFacts runs one page's call on the fact lane and gates the
// reply. Pages whose kind has no menu return empty without a call.
func (x evidenceExtractor) extractPageFacts(ctx context.Context, page crawlPage) (pageFactsResult, error) {
	menu, ok := menuForKind(page.Kind)
	if !ok {
		return pageFactsResult{url: page.URL, kind: page.Kind}, nil
	}
	idx := newSnippetIndex([]crawlPage{page})
	if len(idx.refs) == 0 {
		return pageFactsResult{url: page.URL, kind: page.Kind}, nil
	}
	req := pageFactsRequest(menu, idx)
	brain := x.factCompleter()
	var resp model.Response
	var err error
	if structured, ok := brain.(validatedBrain); ok {
		resp, err = structured.CompleteValidated(ctx, req, pageFactsShapeValid)
	} else {
		resp, err = brain.Complete(ctx, req)
	}
	if err != nil {
		return pageFactsResult{}, err
	}
	result, dropped := gatePageFacts(resp.Text, page, menu, idx)
	x.reportDrops(ctx, page.URL, dropped)
	return result, nil
}

// pageFactsRequest builds the ONE call one page's fact lane sends. It is a
// pure function of the page's menu and its numbered passages, so the same
// request can be issued outside the deep read — by the certification lane —
// without re-creating it, because a re-creation certifies a copy rather than
// the prompt that ships.
//
// BOTH halves of the call are the menu's: the system prompt asks only for the
// lanes this page kind carries, and the schema's field enum offers only the
// fields it may answer with. Neither can be fixed — a pinned prompt would ask
// a catalog page for a legal entity, and a pinned schema would offer a legal
// notice fields its own menu never named.
//
// The id enum is this call's index for the same reason it is in the profile
// lane: the gate resolves every citation against the SAME numbering the model
// was shown, so one index feeds the prompt, the schema and the gate alike.
//
// The fence is minted here, per request: a boundary reused across calls is one
// some crawled site has already been shown, and every passage in this prompt is
// a site's own writing.
func pageFactsRequest(menu pageMenu, idx snippetIndex) model.Request {
	fence := promptfence.New()
	return model.Request{
		System: pageFactsSystem(menu, fence),
		Messages: []model.Message{{
			Role: chatRoleUser,
			// renderNumbered names the page inside the fence; repeating the URL
			// here would put the site's own text in the prompt's voice.
			Content: idx.renderNumbered(fence),
		}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		ResponseSchema: pageFactsSchema(menu, idx.ids()),
		SecretStripper: ai.NewSecretStripper(),
	}
}

// zeroedStat rejects a measurable claim whose measurement is zero. Sites
// animate their headline numbers up from 0, and a fetched page carries
// the pre-animation DOM — so "$10B+ GMV enabled", the figure a human
// sees, reaches extraction as "0 B + GMV enabled". It cites its passage
// honestly, which is why the citation gate passes it, and it is still a
// claim the company never made. Only quantified_outcome is affected:
// zero is meaningless for a stat and meaningful nowhere else.
func zeroedStat(field, value string) bool {
	if field != people.FactQuantifiedOutcome {
		return false
	}
	digits := strings.IndexFunc(value, unicode.IsDigit)
	if digits < 0 {
		return false // a claim with no number at all is not a zeroed one
	}
	for _, r := range value {
		if unicode.IsDigit(r) && r != '0' {
			return false
		}
	}
	return true
}

// gatePageFacts is the no-guess gate for one page's compact reply:
// closed vocabulary (schema-enforced, re-checked), resolvable citation,
// the value's NAME in the cited passage (±1 same-page join), people
// published-only, entities only from shallow legal pages. The stored
// evidence is the resolved passage — our own text, never the model's.
func gatePageFacts(modelText string, page crawlPage, menu pageMenu, idx snippetIndex) (pageFactsResult, []droppedFinding) {
	out := pageFactsResult{url: page.URL, kind: page.Kind}
	var parsed pageFactsReply
	if err := json.Unmarshal([]byte(ai.Unfence(modelText)), &parsed); err != nil {
		return out, []droppedFinding{{Lane: lanePageFacts, Reason: dropUnparseableReply}}
	}
	var dropped []droppedFinding
	drop := func(lane, field, value, reason string) {
		dropped = append(dropped, droppedFinding{Lane: lane, Field: field, Value: value, Reason: reason})
	}
	out.facts = gatePageFactList(parsed, page, menu, idx, drop)
	if menu.people {
		out.people = gatePagePeople(parsed, page, idx, drop)
	}
	if menu.entities {
		out.entities = gatePageEntities(parsed, page, idx, drop)
	}
	return out, dropped
}

func gatePageFactList(parsed pageFactsReply, page crawlPage, menu pageMenu, idx snippetIndex, drop func(lane, field, value, reason string)) []people.DeepReadFact {
	allowed := map[string]bool{}
	for _, f := range menu.factFields {
		allowed[f] = true
	}
	var out []people.DeepReadFact
	factIndex := map[string]int{}
	for _, f := range parsed.Facts {
		category := factCategoryByField[f.F]
		switch {
		case !allowed[f.F] || category == "":
			drop(lanePageFacts, f.F, f.V, dropUnknownField)
			continue
		case strings.TrimSpace(f.V) == "":
			drop(lanePageFacts, f.F, f.V, dropEmptyValue)
			continue
		}
		evidence, cited := idx.nameInCited(f.E, factName(f.V))
		if !cited {
			drop(lanePageFacts, f.F, f.V, dropValueNotInSnippet)
			continue
		}
		if zeroedStat(f.F, f.V) {
			drop(lanePageFacts, f.F, f.V, dropZeroedStat)
			continue
		}
		valueKey := ""
		if people.OrganizationFactMultiValue[f.F] {
			valueKey = people.NormalizeFactValueKey(f.V)
			if valueKey == "" {
				drop(lanePageFacts, f.F, f.V, dropEmptyValueKey)
				continue
			}
		}
		fact := people.DeepReadFact{
			Category: category, Field: f.F, Value: strings.TrimSpace(f.V), ValueKey: valueKey,
			EvidenceSnippet: evidence, SourceURL: page.URL, Confidence: gatedConfidence,
		}
		if _, dup := factIndex[factKey(fact)]; dup {
			drop(lanePageFacts, f.F, f.V, dropDuplicate)
			continue
		}
		factIndex[factKey(fact)] = len(out)
		out = append(out, fact)
	}
	return out
}

func gatePagePeople(parsed pageFactsReply, page crawlPage, idx snippetIndex, drop func(lane, field, value, reason string)) []sitePerson {
	var out []sitePerson
	personIndex := map[string]int{}
	for _, p := range parsed.People {
		name := strings.TrimSpace(p.N)
		role := strings.TrimSpace(p.R)
		if name == "" || role == "" {
			drop(lanePeople, p.N, p.R, dropEmptyValue)
			continue
		}
		evidence, namedOK := idx.nameInCited(p.E, name)
		if !namedOK {
			drop(lanePeople, name, role, dropValueNotInSnippet)
			continue
		}
		if !strings.Contains(normalizeEvidence(evidence), normalizeEvidence(role)) {
			// The passage must ASSOCIATE this name with this role, not
			// merely name the person — otherwise one person's name pairs
			// with another's role.
			drop(lanePeople, name, role, dropNameRoleUnlinked)
			continue
		}
		// A lead nobody can contact is not a lead. The page has to have
		// PRINTED an address: without one the proposal asks a human to
		// confirm a name they then have no way to act on, and every one of
		// those crowds the queue that real proposals share.
		//
		// This gates what we PROPOSE, not what a lead may be — a lead
		// created by any other route may still carry no email (LEADS-DDL,
		// uq_lead_email_dedupe is partial for exactly that reason).
		publishedEmail := verbatimOrEmpty(p.M, page.Text)
		if publishedEmail == "" {
			drop(lanePeople, name, role, dropNoPublishedEmail)
			continue
		}
		person := sitePerson{
			Name:            name,
			Role:            role,
			PublishedEmail:  publishedEmail,
			LinkedinURL:     verbatimOrEmpty(p.L, page.Text),
			EvidenceSnippet: evidence,
			SourceURL:       page.URL,
			Confidence:      gatedConfidence,
		}
		key := normalizedPersonName(name)
		if _, dup := personIndex[key]; dup {
			drop(lanePeople, name, role, dropDuplicate)
			continue
		}
		personIndex[key] = len(out)
		out = append(out, person)
	}
	return out
}

// factName is the dedupe/containment identity of a multi-value fact's
// value: the part before the " — " separator, the whole value otherwise.
func factName(value string) string {
	name, _, found := strings.Cut(value, factValueSeparator)
	if !found {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(name)
}

// factValueSeparator mirrors the people module's value spelling
// ("Name — short description").
const factValueSeparator = " — "

// gatedConfidence is the fixed confidence stamped on reference-evidence
// findings: the gate is binary (the citation resolves and carries the
// name, or the finding is dropped), so the model no longer self-grades —
// and the DB's (0,1] CHECK stays satisfied without a schema change.
const gatedConfidence = 1.0
