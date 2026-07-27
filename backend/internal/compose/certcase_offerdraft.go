// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for offer_draft/draft — the one model pass that turns a
// deal's own captured context into priced offer lines.
//
// It certifies the shipped path rather than a description of it: the request
// comes from offerDraftRequest, the same builder the orchestrator calls, and the
// reply is judged by groundOfferLines, the same no-guess gate the orchestrator
// applies — price ladder included. A case that rebuilt either would measure a
// copy, and a copy stays green through the change that breaks the original.
//
// Both halves of that gate had a database inside them, and the whole gate is
// what makes this site's claim worth anything: an evidence check that stopped
// before the price would certify that a line is grounded while saying nothing
// about the number on it, which is the only part of a line that can cost money.
// So the catalogue read moved up to the orchestrator and the ladder's product
// re-read became rateCardLookup, served here from the fixture's own rate card.
//
// What the expectation MEANS here: the context items the draft must turn into
// lines, each with the price that line must carry and whether that price is
// grounded. A line's identity is the context item it cites, not its wording —
// wording is what the rubric and the judge grade, and a scenario pinning a
// description would fail a draft that said the same thing in its own words. The
// price is not prose: it is either lifted from the cited evidence, or matched to
// a rate-card product, or the honest zero sentinel, and a scenario can say which
// of the three this context supports.
//
// It is a subset claim, never an inventory: a real deal's context supports more
// lines than a scenario cares to pin, and demanding exhaustiveness would fail a
// draft for being richer than its author imagined.

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// offerDraftSite names this site in every refusal it writes, so a corpus author
// reading one knows which scenario to open.
const offerDraftSite = "offer_draft/draft"

// offerDraftFixture is ONE drafting call in exactly what the orchestrator hands
// it: the deal's assembled context, the workspace's rate-card excerpt, and the
// currency the offer being drafted is denominated in.
//
// The context arrives already flattened to {source_id, snippet} pairs and the
// rate card already narrowed to a bounded page of live products, because that is
// what reaches this call — the retrieval walk and the catalogue read that
// produced them are reads, and the certified thing is the prompt built from
// their result. What those reads guarantee about their result is enforced at
// Prepare instead.
type offerDraftFixture struct {
	Currency     string                  `json:"currency"`
	ContextItems []offerDraftContextItem `json:"context_items"`
	RateCard     []offerDraftProduct     `json:"rate_card"`
}

// offerDraftContextItem is one captured piece of the deal's context: the id a
// line must cite and the verbatim text that citation must be found in.
type offerDraftContextItem struct {
	SourceID string `json:"source_id"`
	Snippet  string `json:"snippet"`
}

// offerDraftProduct is one rate-card entry, reduced to the three fields this
// call reaches: the name and price the model is shown, and the currency the
// ladder refuses to price across.
//
// It carries no id. Production takes a product's id from the catalogue row, and
// a fixture that supplied one would put it in the hands of whoever authored the
// expected reply — a model echoing back an id it was handed is exactly what the
// rate-card rung must not reward, so Prepare mints the ids and the expectation
// names lines by the context item they cite instead.
type offerDraftProduct struct {
	Name           string `json:"name"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
	Currency       string `json:"currency"`
}

// offerDraftExpectedLine is one line as the corpus asserts it: the price the
// staged line must carry and whether the ladder must have grounded it. Both,
// because they are different claims — a scenario pinning only the number would
// pass a draft that guessed it, and price_grounded is what the offer shows a
// human as the difference between an evidenced price and a placeholder.
type offerDraftExpectedLine struct {
	UnitPriceMinor int64 `json:"unit_price_minor"`
	PriceGrounded  bool  `json:"price_grounded"`
}

// offerDraftCases serves the one site that drafts priced offer lines.
type offerDraftCases struct{}

func (offerDraftCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskOfferDraft,
		Variant: "draft",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one deal's context and the lines the scenario expects from it
// into a runnable case, minting the rate card's ids exactly as the catalogue the
// orchestrator reads already carries them.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (offerDraftCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f offerDraftFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("%s: the fixture is not the shape this site takes: %w", offerDraftSite, err)
	}
	if err := refuseUndraftableDeal(f); err != nil {
		return nil, err
	}
	// A draft differs from a guess in which context item each line cites and what
	// price that line carries, so the expectation IS that mapping rather than a
	// wrapper around it.
	var want map[string]offerDraftExpectedLine
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf(
			"%s: the expected answer is not a map of context source id to the line it grounds: %w",
			offerDraftSite, err)
	}
	if len(want) == 0 {
		return nil, fmt.Errorf(
			"%s: the scenario expects no line, so no draft could disagree with it", offerDraftSite)
	}
	dealContext := make([]dealContextItem, 0, len(f.ContextItems))
	for _, item := range f.ContextItems {
		// The fixture's own type exists to carry the corpus's field names; what it
		// holds is the assembly's item, so the conversion is the whole mapping.
		dealContext = append(dealContext, dealContextItem(item))
	}
	catalog, card := mintOfferDraftCatalog(f.RateCard)
	if err := refuseUnstageableExpectation(want, dealContext, catalog, f.Currency); err != nil {
		return nil, err
	}
	return &offerDraftCase{
		drafter:     offerDrafter{rateCard: card},
		dealContext: dealContext,
		catalog:     catalog,
		currency:    f.Currency,
		expected:    want,
	}, nil
}

// refuseUndraftableDeal names a fixture the orchestrator could never have
// handed this call, and so a prompt the product never sends. Every clause is a
// bound the drafting path already holds: gatherDealContext skips an evidence
// item missing either half, the two reads are capped, a citation names one
// source, and an offer is always denominated.
func refuseUndraftableDeal(f offerDraftFixture) error {
	if strings.TrimSpace(f.Currency) == "" {
		return fmt.Errorf(
			"%s: the fixture names no currency, and the ladder reads a price in the offer's own denomination",
			offerDraftSite)
	}
	if len(f.ContextItems) == 0 {
		return fmt.Errorf(
			"%s: the fixture captures no deal context, so no line could ever be evidenced", offerDraftSite)
	}
	if len(f.ContextItems) > offerDraftContextItems || len(f.RateCard) > offerDraftCatalogItems {
		return fmt.Errorf(
			"%s: the fixture supplies %d context items and %d products, and this call is handed at most %d and %d",
			offerDraftSite, len(f.ContextItems), len(f.RateCard), offerDraftContextItems, offerDraftCatalogItems)
	}
	seen := make(map[string]bool, len(f.ContextItems))
	for _, item := range f.ContextItems {
		switch {
		case strings.TrimSpace(item.SourceID) == "" || strings.TrimSpace(item.Snippet) == "":
			return fmt.Errorf(
				"%s: the fixture supplies a context item without both an id and its text, and the assembly drops one",
				offerDraftSite)
		case seen[item.SourceID]:
			return fmt.Errorf(
				"%s: two context items share the id %q, and a citation names one source",
				offerDraftSite, item.SourceID)
		}
		seen[item.SourceID] = true
	}
	for _, product := range f.RateCard {
		if strings.TrimSpace(product.Name) == "" || strings.TrimSpace(product.Currency) == "" {
			return fmt.Errorf(
				"%s: the fixture supplies a rate-card entry without a name or a currency, which no stored product is",
				offerDraftSite)
		}
	}
	return nil
}

// refuseUnstageableExpectation names an expectation this site's own gate could
// never let a draft satisfy: a citation the context does not carry is dropped on
// every reply, an ungrounded line is priced at zero on every reply, and a
// grounded price is only ever the one the cited evidence states or the one a
// rate-card product in the offer's currency charges. Each would measure nothing
// for as long as it stayed in the corpus. Naming it here costs a parse; finding
// it later costs a paid run.
//
// Sorted so an expectation with two offences names the same one every time.
func refuseUnstageableExpectation(
	want map[string]offerDraftExpectedLine,
	dealContext []dealContextItem,
	catalog []crmcontracts.Product,
	currency string,
) error {
	captured := make(map[string]string, len(dealContext))
	for _, item := range dealContext {
		captured[item.SourceID] = item.Snippet
	}
	for _, sourceID := range slices.Sorted(maps.Keys(want)) {
		line := want[sourceID]
		snippet, known := captured[sourceID]
		switch {
		case !known:
			return fmt.Errorf(
				"%s: the scenario expects a line citing %q, which the fixture never captures",
				offerDraftSite, sourceID)
		case !line.PriceGrounded && line.UnitPriceMinor != 0:
			return fmt.Errorf(
				"%s: the scenario expects %q priced %d and ungrounded, and the ladder prices every ungrounded line at zero",
				offerDraftSite, sourceID, line.UnitPriceMinor)
		case line.PriceGrounded && !groundablePrice(line.UnitPriceMinor, snippet, catalog, currency):
			return fmt.Errorf(
				"%s: the scenario expects %q grounded at %d, which neither the cited context states nor any %s "+
					"rate-card product charges", offerDraftSite, sourceID, line.UnitPriceMinor, currency)
		}
	}
	return nil
}

// groundablePrice asks the ladder's own question of a scenario: is there a rung
// that could reach this price at all? The conversation rung needs the amount
// inside the text a line cites — and a cited snippet is a substring of that
// text, so the price appearing nowhere in the item means it appears in no
// citation of it — and the rate-card rung needs a live product charging it in
// the offer's own currency.
func groundablePrice(priceMinor int64, snippet string, catalog []crmcontracts.Product, currency string) bool {
	if priceEvidencedInSnippet(snippet, priceMinor, currency) {
		return true
	}
	for _, product := range catalog {
		if product.Currency == currency && product.UnitPriceMinor == priceMinor {
			return true
		}
	}
	return false
}

// mintOfferDraftCatalog turns the fixture's rate card into the products the
// catalogue read returns, minting each id the way the products table already
// carries one, and keys them for the ladder's lookup — the same rows, reached
// the two ways the drafting path reaches them.
func mintOfferDraftCatalog(entries []offerDraftProduct) ([]crmcontracts.Product, offerDraftRateCard) {
	catalog := make([]crmcontracts.Product, 0, len(entries))
	card := make(offerDraftRateCard, len(entries))
	for _, entry := range entries {
		id := ids.New[ids.ProductKind]()
		product := crmcontracts.Product{
			Id:             openapi_types.UUID(id.UUID),
			Name:           entry.Name,
			UnitPriceMinor: entry.UnitPriceMinor,
			Currency:       entry.Currency,
			Active:         true,
		}
		catalog = append(catalog, product)
		card[id] = product
	}
	return catalog, card
}

// offerDraftRateCard serves the price ladder's one lookup from the products the
// catalogue read returned. Anything else is not found, which is what the store
// answers for a product id a model invented — and the ladder's answer to an
// invented id is exactly what this case exists to certify.
type offerDraftRateCard map[ids.ProductID]crmcontracts.Product

// GetProduct ignores the archived filter because this rate card holds only the
// live products the catalogue read returns, so LiveOnly and IncludeArchived are
// the same question of it.
func (r offerDraftRateCard) GetProduct(
	_ context.Context, id ids.ProductID, _ storekit.ArchivedFilter,
) (crmcontracts.Product, error) {
	product, live := r[id]
	if !live {
		return crmcontracts.Product{}, fmt.Errorf("no product %s: %w", id, apperrors.ErrNotFound)
	}
	return product, nil
}

// offerDraftCase is one deal's context ready to be drafted from, closed over the
// rate card the ladder re-reads and the lines the scenario expects.
type offerDraftCase struct {
	drafter     offerDrafter
	dealContext []dealContextItem
	catalog     []crmcontracts.Product
	currency    string
	expected    map[string]offerDraftExpectedLine
}

// Run issues the one request this site sends. It sends it bare: production wraps
// the same request in the shape-retry when the brain supports one, and a case
// that retried would certify the answer a model gives after being told it got
// the shape wrong rather than the answer it gives.
func (c *offerDraftCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := offerDraftRequest(c.dealContext, c.catalog)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("%s: %w", offerDraftSite, err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the orchestrator's own checks in its own order — read the
// envelope, then the no-guess gate with its price ladder — and only then asks
// whether what staged is priced the way the scenario says this context prices
// it. The order is the meaning: a line the gate refused is not a price to
// disagree with.
//
// Nothing staging is OutcomeInvalid because the staged lines are the whole
// product of this site: a reply from which none survive leaves the offer exactly
// as the mechanical clone left it and the human with nothing to review, whatever
// the model wrote. A reply that staged something is usable whatever else it
// claimed, so a missing or misgrounded line is a wrong answer, named as such.
func (c *offerDraftCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	candidates, err := parseOfferDraftLines(trace.Output)
	if err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	staged, refusals := c.gate(candidates)
	if len(staged) == 0 {
		if len(refusals) == 0 {
			refusals = []string{"the model drafted no line at all"}
		}
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: strings.Join(refusals, "; ")}
	}
	// Every refusal reaches the Detail whatever the result: a draft that staged
	// the expected lines while inventing three ungrounded ones is not the clean
	// run it would otherwise look like.
	if disagreements := c.disagreements(staged); len(disagreements) > 0 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: strings.Join(append(disagreements, refusals...), "; "),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted, Detail: strings.Join(refusals, "; ")}
}

// gate runs the production no-guess gate over the drafted candidates one at a
// time, and names what it refused. One at a time because the gate reports a drop
// by absence, so asking about a single candidate is the only way to learn WHICH
// one it dropped; it holds no state across candidates, so what this collects is
// what one batched call stages, in the order it stages it.
func (c *offerDraftCase) gate(candidates []offerLineCandidate) ([]deals.StagedOfferLineInput, []string) {
	var (
		staged   []deals.StagedOfferLineInput
		refusals []string
	)
	for i := range candidates {
		// The gate's only I/O is the rate-card lookup, which this case serves from
		// the fixture's own products, so it needs nothing from the run's context.
		lines, err := c.drafter.groundOfferLines(
			context.Background(), candidates[i:i+1], c.dealContext, c.currency)
		switch {
		case err != nil:
			// A lookup fault is not a grounding verdict — the ladder propagates it
			// rather than calling the line ungrounded, and so does this.
			refusals = append(refusals, fmt.Sprintf(
				"the rate-card lookup faulted on %s: %v", offerLineName(candidates[i], i), err))
		case len(lines) == 0:
			refusals = append(refusals, "the gate refused "+offerLineName(candidates[i], i))
		default:
			staged = append(staged, lines...)
		}
	}
	return staged, refusals
}

// offerLineName says which drafted line a refusal is about. A line with no
// description is one the gate drops for exactly that, so it is named by its
// position instead.
func offerLineName(candidate offerLineCandidate, index int) string {
	if description := strings.TrimSpace(candidate.Description); description != "" {
		return fmt.Sprintf("the line %q", description)
	}
	return fmt.Sprintf("line %d, which describes nothing", index+1)
}

// disagreements names every expected line the staged draft does not carry. All
// of them, not the first: a draft that priced one line right and two wrong is
// not the near miss one line would read as.
//
// A line is identified by the context item it cites, and ANY staged line citing
// that item can satisfy the scenario — every staged line reaches the offer, so
// there is no first-wins rule to mirror here.
//
// Sorted so a run with two disagreements names them in the same order every time.
func (c *offerDraftCase) disagreements(staged []deals.StagedOfferLineInput) []string {
	var out []string
	for _, sourceID := range slices.Sorted(maps.Keys(c.expected)) {
		want := c.expected[sourceID]
		var priced []string
		satisfied := false
		for _, line := range staged {
			switch {
			case line.Evidence.SourceID != sourceID:
			case line.UnitPriceMinor == want.UnitPriceMinor && line.PriceGrounded == want.PriceGrounded:
				satisfied = true
			default:
				priced = append(priced, offerPriceLine(line.UnitPriceMinor, line.PriceGrounded))
			}
		}
		switch {
		case satisfied:
		case len(priced) == 0:
			out = append(out, fmt.Sprintf("no staged line cites %q, which the scenario expects priced %s",
				sourceID, offerPriceLine(want.UnitPriceMinor, want.PriceGrounded)))
		default:
			out = append(out, fmt.Sprintf("the line citing %q is priced %s where the scenario expects %s",
				sourceID, strings.Join(priced, " and "), offerPriceLine(want.UnitPriceMinor, want.PriceGrounded)))
		}
	}
	return out
}

// offerPriceLine renders a price for a human reading a disagreement, in the
// minor units the offer stores and the ladder's own two words for where the
// number came from.
func offerPriceLine(priceMinor int64, grounded bool) string {
	if grounded {
		return fmt.Sprintf("%d minor units, grounded", priceMinor)
	}
	return fmt.Sprintf("%d minor units, ungrounded", priceMinor)
}
