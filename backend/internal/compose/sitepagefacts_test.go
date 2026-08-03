// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The page-fact gate's contract — the no-guess rules restated over
// snippet citations: closed vocabulary, the value's name in the cited
// passage, people published-only, entities only from shallow legal
// pages, and every refusal recorded with its reason.

import (
	"slices"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
)

func pageFixture(kind crmcontracts.SiteReadPageKind, url, text string) (crawlPage, pageMenu, snippetIndex) {
	page := crawlPage{URL: url, Kind: kind, Text: text}
	menu, ok := menuForKind(kind)
	if !ok {
		panic("fixture kind has no menu")
	}
	return page, menu, newSnippetIndex([]crawlPage{page})
}

func dropReasons(dropped []droppedFinding) map[string]string {
	out := map[string]string{}
	for _, d := range dropped {
		out[d.Field] = d.Reason
	}
	return out
}

func TestFactFieldNamesAreGloballyUniqueAcrossCategories(t *testing.T) {
	// The compact reply names no category — the field implies it, which
	// only works while no field name appears in two categories.
	seen := map[string]string{}
	for category, fields := range people.OrganizationFactFields {
		for _, field := range fields {
			if prior, dup := seen[field]; dup {
				t.Fatalf("fact field %q lives in both %s and %s — the category inference breaks", field, prior, category)
			}
			seen[field] = category
		}
	}
}

func TestMenuForKindRoutesFactBearingKindsOnly(t *testing.T) {
	if _, ok := menuForKind(crmcontracts.SiteReadPageKindOther); ok {
		t.Fatal("unclassified pages must make no call")
	}
	menu, ok := menuForKind(crmcontracts.SiteReadPageKindImpressum)
	if !ok || !menu.entities || menu.people {
		t.Fatalf("impressum menu = %+v, want company fields + entities", menu)
	}
	menu, ok = menuForKind(crmcontracts.SiteReadPageKindServices)
	if !ok || menu.entities || menu.people {
		t.Fatalf("services menu = %+v, want offering fields only", menu)
	}
	found := false
	for _, f := range menu.factFields {
		if f == "technology" {
			found = true
		}
	}
	if !found {
		t.Fatal("catalog pages must be allowed to name technologies")
	}
	for _, expected := range []string{people.FactService, people.FactProduct, people.FactServedIndustry} {
		if !slices.Contains(menu.factFields, expected) {
			t.Fatalf("catalog menu must include %q: %+v", expected, menu.factFields)
		}
	}
	home, ok := menuForKind(crmcontracts.SiteReadPageKindHome)
	if !ok || !slices.Contains(home.factFields, people.FactProduct) || !slices.Contains(home.factFields, people.FactCompanySize) {
		t.Fatalf("home pages must capture headline offers and markets: %+v", home)
	}
}

// Every passage in this prompt is the page's own words, and so is the URL
// beside it — a crawl path can carry a readable sentence. The only thing
// keeping any of it out of the instruction region is a marker minted for
// THIS call and named in THIS call's system prompt.
func TestPageFactsRequestFencesThePageUnderTheMarkerItDeclares(t *testing.T) {
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindServices, seedURL+"/services",
		"Cloud Cost Audit\nA line-by-line review of cloud spend identifying waste across compute and storage budgets.")

	req := pageFactsRequest(menu, idx)

	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		t.Fatalf("the page-facts system prompt declares no data boundary: %q", req.System)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("got %d messages, want the single user turn", len(req.Messages))
	}
	// Containment is not a question of membership: a prompt that keeps the
	// fence and ALSO repeats the text beside it puts that copy in the
	// instruction region while "is it inside?" stays true. So the assertion
	// is on what the prompt says in its OWN voice.
	instructions := outsideEverySpan(req.Messages[0].Content, marker)
	for _, ref := range idx.refs {
		if strings.Contains(instructions, ref.passage) {
			t.Errorf("passage %q reaches the instruction region:\n%s", ref.passage, instructions)
		}
	}
	if strings.Contains(instructions, page.URL) {
		t.Errorf("the page URL %q reaches the instruction region:\n%s", page.URL, instructions)
	}
}

// BOTH halves of this call are the page kind's: the prompt asks for the lanes
// that kind carries, and the schema's field enum offers exactly the fields it
// may answer with. A pinned prompt or a pinned schema would ask a catalog page
// for a legal entity, and would offer a legal notice none of the fields it is
// actually there to state.
func TestPageFactsRequestAsksOnlyWhatThisPageKindsMenuOffers(t *testing.T) {
	_, legalMenu, legalIdx := pageFixture(crmcontracts.SiteReadPageKindImpressum, seedURL+"/impressum",
		"Impressum. Acme Robotics GmbH, Werkstrasse 1, 70435 Stuttgart. Telefon 0711 123456. HRB 123456.")
	_, catalogMenu, catalogIdx := pageFixture(crmcontracts.SiteReadPageKindServices, seedURL+"/services",
		"Cloud Cost Audit\nA line-by-line review of cloud spend identifying waste across compute and storage budgets.")

	legal := pageFactsRequest(legalMenu, legalIdx)
	catalog := pageFactsRequest(catalogMenu, catalogIdx)

	if !strings.Contains(legal.System, `"entities"`) {
		t.Errorf("a legal notice must be asked for its entities: %q", legal.System)
	}
	if strings.Contains(catalog.System, `"entities"`) {
		t.Errorf("a catalog page must not be asked for entities: %q", catalog.System)
	}
	for _, tc := range []struct {
		name    string
		schema  string
		offered string
		absent  string
	}{
		{name: "legal notice", schema: string(legal.ResponseSchema), offered: people.FactPhone, absent: people.FactService},
		{name: "catalog page", schema: string(catalog.ResponseSchema), offered: people.FactService, absent: people.FactPhone},
	} {
		if !strings.Contains(tc.schema, `"`+tc.offered+`"`) {
			t.Errorf("the %s schema does not offer %q, which its menu carries: %s", tc.name, tc.offered, tc.schema)
		}
		if strings.Contains(tc.schema, `"`+tc.absent+`"`) {
			t.Errorf("the %s schema offers %q, which its menu never carries: %s", tc.name, tc.absent, tc.schema)
		}
	}
	if !strings.Contains(string(legal.ResponseSchema), `"entities"`) {
		t.Errorf("the legal notice schema carries no entities lane: %s", legal.ResponseSchema)
	}
	if strings.Contains(string(catalog.ResponseSchema), `"entities"`) {
		t.Errorf("the catalog schema carries an entities lane its menu never asks for: %s", catalog.ResponseSchema)
	}
}

// A fence's scope is one call. A marker a previous page was shown is a marker
// whoever publishes that page can spell, so reusing one would give away the
// only thing they cannot forge.
func TestPageFactsRequestMintsAFreshMarkerPerCall(t *testing.T) {
	_, menu, idx := pageFixture(crmcontracts.SiteReadPageKindServices, seedURL+"/services",
		"Cloud Cost Audit\nA line-by-line review of cloud spend identifying waste across compute and storage budgets.")

	first, declared := promptfence.MarkerIn(pageFactsRequest(menu, idx).System)
	if !declared {
		t.Fatal("the first page-facts system prompt declares no data boundary")
	}
	second, declared := promptfence.MarkerIn(pageFactsRequest(menu, idx).System)
	if !declared {
		t.Fatal("the second page-facts system prompt declares no data boundary")
	}
	if first == second {
		t.Errorf("two page-facts requests share the boundary %q", first)
	}
}

func TestGatePageFactsDemandsTheNameInTheCitedPassage(t *testing.T) {
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindServices, seedURL+"/services",
		"Cloud Cost Audit\nA line-by-line review of cloud spend identifying waste across compute, storage and networking budgets.")
	reply := `{"facts":[
		{"f":"service","v":"Cloud Cost Audit — line-by-line review","e":"s0"},
		{"f":"service","v":"Phishing Simulation — never on this page","e":"s0"},
		{"f":"founded_year","v":"1998","e":"s0"},
		{"f":"service","v":"","e":"s0"}]}`
	res, dropped := gatePageFacts(reply, page, menu, idx)
	if len(res.facts) != 1 || factName(res.facts[0].Value) != "Cloud Cost Audit" {
		t.Fatalf("only the cited-and-named service may survive: %+v", res.facts)
	}
	// The stored evidence is the resolved passage and carries the name
	// (the adjacent-join recovery has its own proof in sitesnippet_test).
	if !strings.Contains(res.facts[0].EvidenceSnippet, "Cloud Cost Audit") {
		t.Fatalf("evidence must carry the item name: %q", res.facts[0].EvidenceSnippet)
	}
	if res.facts[0].Confidence != gatedConfidence {
		t.Fatalf("reference-evidence facts carry the fixed gate confidence, got %v", res.facts[0].Confidence)
	}
	byReason := map[string]int{}
	for _, d := range dropped {
		byReason[d.Reason]++
	}
	if byReason[dropValueNotInSnippet] != 1 || byReason[dropEmptyValue] != 1 || byReason[dropUnknownField] != 1 {
		t.Fatalf("drops = %+v, want one uncited service, one empty value, one off-menu field", dropped)
	}
}

func TestGatePageFactsPeopleStayPublishedOnly(t *testing.T) {
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindTeam, seedURL+"/team",
		"Anna Muster is our Chief Executive Officer and founded the automation practice. Reach her at anna@acme.example for partnership topics.")
	reply := `{"facts":[],"people":[
		{"n":"Anna Muster","r":"Chief Executive Officer","m":"anna@acme.example","l":"https://linkedin.com/in/anna","e":"s0"},
		{"n":"Carla Invented","r":"CTO","e":"s0"}]}`
	res, dropped := gatePageFacts(reply, page, menu, idx)
	if len(res.people) != 1 || res.people[0].Name != "Anna Muster" {
		t.Fatalf("only the published person may survive: %+v", res.people)
	}
	if res.people[0].PublishedEmail != "anna@acme.example" || res.people[0].LinkedinURL != "" {
		t.Fatalf("printed email kept, unprinted linkedin stripped: %+v", res.people[0])
	}
	if reasons := dropReasons(dropped); reasons["Carla Invented"] != dropValueNotInSnippet {
		t.Fatalf("the invented person must drop: %+v", dropped)
	}
}

func TestGatePageFactsEntitiesOnlyFromShallowLegalPages(t *testing.T) {
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindImpressum, seedURL+"/impressum",
		"Impressum. Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart. Registergericht Stuttgart HRB 12345, USt-ID DE123456789.")
	reply := `{"facts":[],"entities":[
		{"n":"Acme Robotics GmbH","e":"s0"},
		{"n":"Hallucinated Holding AG","e":"s0"}]}`
	res, dropped := gatePageFacts(reply, page, menu, idx)
	if len(res.entities) != 1 || res.entities[0].Name != "Acme Robotics GmbH" {
		t.Fatalf("only the named entity may pass the census: %+v", res.entities)
	}
	if reasons := dropReasons(dropped); reasons["Hallucinated Holding AG"] != dropValueNotInSnippet {
		t.Fatalf("a hallucinated entity must drop: %+v", dropped)
	}

	// A deep legal path never testifies, whatever it names.
	deepPage := crawlPage{
		URL: seedURL + "/customers/other/legal", Kind: crmcontracts.SiteReadPageKindImpressum,
		Text: "Other Co AG imprint for a customer project hosted under a deep path with plenty of text.",
	}
	deepIdx := newSnippetIndex([]crawlPage{deepPage})
	res, dropped = gatePageFacts(`{"facts":[],"entities":[{"n":"Other Co AG","e":"s0"}]}`, deepPage, menu, deepIdx)
	if len(res.entities) != 0 {
		t.Fatalf("a deep legal page testified: %+v", res.entities)
	}
	if reasons := dropReasons(dropped); reasons["Other Co AG"] != dropLegalNotFromLegalPage {
		t.Fatalf("want legal_field_not_from_legal_page: %+v", dropped)
	}
}

func TestGatePageFactsValueKeysAndDuplicates(t *testing.T) {
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindHome, seedURL,
		"Offices in Stuttgart and Hanoi serve industrial customers across Europe and Asia with automation projects.")
	reply := `{"facts":[
		{"f":"location","v":"Stuttgart","e":"s0"},
		{"f":"location","v":"Hanoi","e":"s0"},
		{"f":"location","v":"Stuttgart","e":"s0"}]}`
	res, dropped := gatePageFacts(reply, page, menu, idx)
	if len(res.facts) != 2 {
		t.Fatalf("two distinct locations survive, the repeat drops: %+v", res.facts)
	}
	for _, f := range res.facts {
		if f.ValueKey == "" {
			t.Fatalf("multi-value fact without value_key: %+v", f)
		}
	}
	dupSeen := false
	for _, d := range dropped {
		if d.Reason == dropDuplicate {
			dupSeen = true
		}
	}
	if !dupSeen {
		t.Fatalf("the repeated location left no duplicate drop: %+v", dropped)
	}
}

func TestGatePageFactsDropsZeroedStats(t *testing.T) {
	// Sites animate headline numbers up from zero, so the fetched DOM
	// states "0 B + GMV enabled" where a visitor reads "$10B+". The
	// citation gate cannot catch it — the passage really does say that —
	// and recording it would publish a claim the company never made.
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindHome, seedURL,
		"Delivery at scale: $ 0 B + GMV enabled 0 M + tasks automated monthly and 97% client satisfaction across deployments.")
	reply := `{"facts":[
		{"f":"quantified_outcome","v":"0 B + GMV enabled","e":"s0"},
		{"f":"quantified_outcome","v":"0 M + tasks automated monthly","e":"s0"},
		{"f":"quantified_outcome","v":"97% client satisfaction","e":"s0"}]}`
	res, dropped := gatePageFacts(reply, page, menu, idx)
	if len(res.facts) != 1 || res.facts[0].Value != "97% client satisfaction" {
		t.Fatalf("only the real measurement survives: %+v", res.facts)
	}
	zeroed := 0
	for _, d := range dropped {
		if d.Reason == dropZeroedStat {
			zeroed++
		}
	}
	if zeroed != 2 {
		t.Fatalf("both zeroed counters must drop as %s: %+v", dropZeroedStat, dropped)
	}
}

func TestZeroedStatOnlyJudgesMeasurements(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		value string
		want  bool
	}{
		{"animated counter", "quantified_outcome", "0 B + GMV enabled", true},
		{"real measurement", "quantified_outcome", "$10B+ GMV enabled", false},
		{"zero inside a real number", "quantified_outcome", "20 million tasks monthly", false},
		{"claim with no number", "quantified_outcome", "market leading uptime", false},
		{"a zero belongs in other fields", "product", "Product 0", false},
	} {
		if got := zeroedStat(tc.field, tc.value); got != tc.want {
			t.Errorf("%s: zeroedStat(%q, %q) = %v, want %v", tc.name, tc.field, tc.value, got, tc.want)
		}
	}
}

// A legal notice states one block per entity. Everything printed inside
// that block — the address, the register number — is what the confirm
// step later offers as a choice, so it carries the same no-guess rule as
// every other value: on the page, or absent.
func TestGatePageEntitiesKeepsThePrintedBlock(t *testing.T) {
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindImpressum, seedURL+"/imprint",
		"Imprint. Acme Robotics GmbH, Deliusstrasse 7, 24114 Kiel, Germany. Registergericht HRB 12345. "+
			"Acme Pte. Ltd., 77 High Street, Singapore (179433). Business Profile: 201629357M.")
	reply := `{"facts":[],"entities":[
		{"n":"Acme Robotics GmbH","a":"Deliusstrasse 7, 24114 Kiel, Germany","r":"HRB 12345","e":"s0"},
		{"n":"Acme Pte. Ltd.","a":"77 High Street, Singapore 179433","r":"201629357M","e":"s0"}]}`
	res, _ := gatePageEntities2(t, reply, page, menu, idx)
	if len(res) != 2 {
		t.Fatalf("both entities must survive: %+v", res)
	}
	if res[0].RegisteredAddress != "Deliusstrasse 7, 24114 Kiel, Germany" || res[0].RegisterNumber != "HRB 12345" {
		t.Errorf("the first block lost its details: %+v", res[0])
	}
	// The page prints "Singapore (179433)" and the model answered
	// "Singapore 179433": the same address with its punctuation
	// rearranged, which must not cost the human the field.
	if res[1].RegisteredAddress != "77 High Street, Singapore 179433" {
		t.Errorf("punctuation drift dropped a printed address: %+v", res[1])
	}
}

func TestGatePageEntitiesRefusesDetailsThePageNeverPrinted(t *testing.T) {
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindImpressum, seedURL+"/imprint",
		"Imprint. Acme Robotics GmbH, Kiel, Germany. This notice states no register number at all.")
	reply := `{"facts":[],"entities":[
		{"n":"Acme Robotics GmbH","a":"Baker Street 221B, London","r":"HRB 99999","e":"s0"}]}`
	res, dropped := gatePageEntities2(t, reply, page, menu, idx)
	if len(res) != 1 {
		t.Fatalf("the entity itself is printed and must survive: %+v", res)
	}
	if res[0].RegisteredAddress != "" || res[0].RegisterNumber != "" {
		t.Errorf("an invented address or register number reached the block: %+v", res[0])
	}
	reasons := dropReasons(dropped)
	if reasons[fieldRegisteredAddress] != dropValueNotInSnippet || reasons[fieldRegisterVat] != dropValueNotInSnippet {
		t.Errorf("both inventions must be REPORTED, not dropped in silence: %+v", dropped)
	}
}

// gatePageEntities2 runs the entity lane and returns its drops.
func gatePageEntities2(t *testing.T, reply string, page crawlPage, menu pageMenu, idx snippetIndex) ([]corpusLegalEntity, []droppedFinding) {
	t.Helper()
	res, dropped := gatePageFacts(reply, page, menu, idx)
	return res.entities, dropped
}

// A register number is a legal identity. A model that answers with part of
// one — or with a number printed for a DIFFERENT company on the same
// notice — must not have it accepted: the value would be offered as the
// selected entity's identifier and confirmed into the CRM as fact.
func TestGroundedDetailRefusesPartialAndForeignIdentifiers(t *testing.T) {
	block := normalizeEvidence("Acme GmbH, Deliusstrasse 7, 24114 Kiel. Registergericht HRB 123456.")
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{"printed verbatim", "HRB 123456", "HRB 123456"},
		{"punctuation rearranged", "Deliusstrasse 7 24114 Kiel", "Deliusstrasse 7 24114 Kiel"},
		{"truncated identifier", "1234", ""},
		{"identifier with an extra digit", "HRB 1234567", ""},
		{"a street the block never printed", "Baker Street 221B, Kiel", ""},
		// Both tokens ARE in the block — "24114" from the postcode, "HRB"
		// from the register line — but never together. A set test would
		// vouch for this invented identifier.
		{"recombined from unrelated tokens", "HRB 24114", ""},
		{"printed tokens in the wrong order", "123456 HRB", ""},
		{"nothing claimed", "", ""},
	} {
		if got := groundedDetail(block, tc.value); got != tc.want {
			t.Errorf("%s: groundedDetail(%q) = %q, want %q", tc.name, tc.value, got, tc.want)
		}
	}
}

// Details are judged against the cited block, so a sibling company's
// address elsewhere on the same legal page cannot attach to this entity.
// The blocks below are long enough that the passage packer keeps them
// apart — which is exactly the condition under which this scoping can
// protect anything, and the honest limit of it.
func TestGatePageEntitiesRefusesASiblingBlocksAddress(t *testing.T) {
	german := "Acme GmbH, Deliusstrasse 7, 24114 Kiel, Germany. " + strings.Repeat("Vertreten durch die Geschaeftsfuehrung. ", 8)
	singapore := "Acme Pte. Ltd., 77 High Street, Singapore. " + strings.Repeat("Business registration details follow here. ", 8)
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindImpressum, seedURL+"/imprint",
		german+"\n"+singapore)
	if len(idx.refs) < 2 {
		t.Fatalf("fixture must split into separate blocks, got %d", len(idx.refs))
	}
	// s0 is the German block; the Singapore address belongs to another.
	reply := `{"facts":[],"entities":[{"n":"Acme GmbH","a":"77 High Street, Singapore","r":"","e":"s0"}]}`
	res, dropped := gatePageEntities2(t, reply, page, menu, idx)
	if len(res) != 1 {
		t.Fatalf("the entity is printed and survives: %+v", res)
	}
	if res[0].RegisteredAddress != "" {
		t.Errorf("a sibling block's address must not become this entity's: %+v", res[0])
	}
	if dropReasons(dropped)[fieldRegisteredAddress] != dropValueNotInSnippet {
		t.Errorf("the cross-block grab must be reported: %+v", dropped)
	}
}

func TestGatePageEntitiesJoinsALegalBlockContinuation(t *testing.T) {
	first := "Acme GmbH, Deliusstrasse 7, 24114 Kiel, Germany. " + strings.Repeat("Represented by management. ", 9)
	continuation := "Commercial register Amtsgericht Kiel, HRB 123456. VAT ID DE123456789. " + strings.Repeat("Legal notice detail. ", 7)
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindImpressum, seedURL+"/imprint", first+"\n"+continuation)
	if len(idx.refs) < 2 {
		t.Fatalf("fixture must create a name and continuation passage: %d", len(idx.refs))
	}
	reply := `{"facts":[],"entities":[{"n":"Acme GmbH","a":"Deliusstrasse 7, 24114 Kiel, Germany","r":"HRB 123456","e":"s0"}]}`
	res, _ := gatePageEntities2(t, reply, page, menu, idx)
	if len(res) != 1 || res[0].RegisterNumber != "HRB 123456" {
		t.Fatalf("a legal block's adjacent register line must survive: %+v", res)
	}
}

func TestATestimonialDoesNotBecomeALead(t *testing.T) {
	// A home page's "what our clients say" wall names people who work
	// ELSEWHERE, and filing them as contacts at the company whose site it is
	// contradicts their own quoted job title on the same line.
	//
	// The published-email floor is what separates them from the founders and
	// staff on the same pages, which are worth having: a company prints an
	// address for the person you should talk to, and never for the customer
	// it is quoting.
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindHome, seedURL,
		"What our clients say: Marc Costea, CEO at Qilin.Cloud, calls it amazing. "+
			"Our founder Anna Muster runs the practice, anna@acme.example.")
	if !menu.people {
		t.Fatal("a home page still asks for people — its founders are worth having")
	}
	reply := `{"facts":[],"people":[
		{"n":"Marc Costea","r":"CEO","e":"s0"},
		{"n":"Anna Muster","r":"founder","m":"anna@acme.example","e":"s0"}]}`
	res, dropped := gatePageFacts(reply, page, menu, idx)
	if len(res.people) != 1 || res.people[0].Name != "Anna Muster" {
		t.Fatalf("only the company's own contactable person survives: %+v", res.people)
	}
	if reasons := dropReasons(dropped); reasons["Marc Costea"] != dropNoPublishedEmail {
		t.Fatalf("the quoted customer must drop: %+v", dropped)
	}
}

func TestAPersonWithNoPublishedEmailIsNotProposed(t *testing.T) {
	// A lead nobody can contact is not a lead: the proposal would ask a human
	// to confirm a name they then have no way to act on.
	page, menu, idx := pageFixture(crmcontracts.SiteReadPageKindTeam, seedURL+"/team",
		"Bernd Beispiel leads sales as Head of Sales. Anna Muster is our Chief Executive Officer, anna@acme.example.")
	reply := `{"facts":[],"people":[
		{"n":"Anna Muster","r":"Chief Executive Officer","m":"anna@acme.example","e":"s0"},
		{"n":"Bernd Beispiel","r":"Head of Sales","e":"s0"}]}`
	res, dropped := gatePageFacts(reply, page, menu, idx)
	if len(res.people) != 1 || res.people[0].Name != "Anna Muster" {
		t.Fatalf("only the contactable person may be proposed: %+v", res.people)
	}
	if reasons := dropReasons(dropped); reasons["Bernd Beispiel"] != dropNoPublishedEmail {
		t.Fatalf("Bernd must drop for having no published address: %+v", dropped)
	}
}
