// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for rate_extract/fx.
//
// It certifies the shipped path rather than a description of it: the request
// comes from fxExtractRequest, the same builder the refresh calls, and the reply
// is judged by collect — the refresh's own method, its no-guess gate, its anchor
// and its rate arithmetic together. A case that rebuilt either would measure a
// copy, and a copy stays green through the change that breaks the original.
//
// What the expectation MEANS here: the rates the refresh carries to its diff, one
// per foreign currency, each expressed as 1 unit of that currency in the
// workspace base. That map is the whole product of this site — what reaches an
// administrator's approval queue is a per-currency rate against the base — and it
// is where a misread costs the most: the prompt forbids the model any arithmetic
// and tells it to report the direction the page shows, so a page that states
// "1 USD = 0.92 EUR" is read correctly only if that direction survives intact to
// the anchor that inverts it.
//
// It is a subset claim, never an inventory: a rates page prices more currencies
// than a scenario cares to pin.
//
// Two things the expectation deliberately cannot name. A PASSAGE ID: the gate
// already requires that a pair cite one, and which passage grounds a rate is a
// fidelity question for the rubric — pinning an id would couple every scenario to
// the fixture's own line numbering, where one blank line renumbers everything
// after it. A DIRECTION: the page states one and the anchor removes it, so the
// corpus asserts the anchored rate the sheet stores and never the spelling the
// page happened to use.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// rateFxFixture is ONE crawled rates page in exactly what the refresh hands the
// extraction and the collection: the fetched text, the workspace base every rate
// is anchored to, and the currencies the sheet already tracks. The URL is
// deliberately absent — it selects which page is fetched and never reaches the
// model.
type rateFxFixture struct {
	PageText          string   `json:"page_text"`
	BaseCurrency      string   `json:"base_currency"`
	TrackedCurrencies []string `json:"tracked_currencies"`
}

// rateFxExpectation is one expected rate, held twice: as the scenario wrote it,
// for a disagreement a human reads, and at the sheet's own precision, which is
// the form collect returns. Converting once at Prepare is what lets Evaluate
// compare without a conversion that could fail there.
type rateFxExpectation struct {
	stated string
	rate   string
}

// rateFxCases serves the one site that reads foreign-exchange rates off a rates
// page.
type rateFxCases struct{}

func (rateFxCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskRateExtract,
		Variant: "fx",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one rates page, the sheet it is being read for, and the rates the
// scenario expects from it into a runnable case.
//
// The base is normalized once here rather than carried raw: fxAnchor upper-cases
// and trims it again for every pair, so this is the same base production anchors
// against, and it is the form an expectation's currency is checked against.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (rateFxCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f rateFxFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("rate_extract/fx: the fixture is not the shape this site takes: %w", err)
	}
	base := strings.ToUpper(strings.TrimSpace(f.BaseCurrency))
	want := trackedCurrencySet(f.TrackedCurrencies)
	if err := refuseUnrefreshableSheet(f, base, want); err != nil {
		return nil, err
	}
	var stated map[string]string
	if err := json.Unmarshal(expected, &stated); err != nil {
		return nil, fmt.Errorf(
			"rate_extract/fx: the expected answer is not a map of currency code to its rate against the base: %w", err)
	}
	if len(stated) == 0 {
		return nil, errors.New(
			"rate_extract/fx: the scenario expects no currency, so no reply could disagree with it")
	}
	rates, err := convertExpectedRates(stated, want)
	if err != nil {
		return nil, err
	}
	return &rateFxCase{pageText: f.PageText, base: base, want: want, expected: rates}, nil
}

// refuseUnrefreshableSheet names a fixture the refresh could never have produced.
// A run with nothing tracked no-ops before it reaches the model; plan drops the
// base from the currencies it asks for, and fxAnchor refuses a pair whose sides
// are the same, so a sheet tracking its own base describes a call this build does
// not make. A page whose lines are all blank is numbered into nothing, leaving
// the model no passage to cite and therefore no pair that could pass the gate.
func refuseUnrefreshableSheet(f rateFxFixture, base string, want map[string]bool) error {
	switch {
	case numberPassages(f.PageText) == "":
		return errors.New("rate_extract/fx: the fixture supplies no page text, so no passage could ground a rate")
	case base == "":
		return errors.New("rate_extract/fx: the fixture names no base currency, and every rate is anchored to one")
	case len(want) == 0:
		return errors.New("rate_extract/fx: the fixture tracks no currency, and a refresh with none to price never calls the model")
	case want[""]:
		return errors.New("rate_extract/fx: the fixture tracks an entry that names no currency")
	case want[base]:
		return fmt.Errorf(
			"rate_extract/fx: the fixture tracks its own base %q, which the refresh never asks a page to price", base)
	}
	return nil
}

// convertExpectedRates names an expectation the refresh can never satisfy: a code
// that is not the currency a collected rate is keyed by, a currency this sheet
// does not track — collect drops a rate nobody asked for — and a rate the sheet's
// own converter cannot store. Each would measure nothing for as long as it stayed
// in the corpus. Naming it here costs a parse; finding it later costs a paid run.
//
// Sorted so an expectation with two offences names the same one every time.
func convertExpectedRates(stated map[string]string, want map[string]bool) (map[string]rateFxExpectation, error) {
	out := make(map[string]rateFxExpectation, len(stated))
	for _, code := range slices.Sorted(maps.Keys(stated)) {
		switch {
		case strings.TrimSpace(code) == "":
			return nil, errors.New("rate_extract/fx: the scenario expects an entry that names no currency")
		case strings.ToUpper(strings.TrimSpace(code)) != code:
			return nil, fmt.Errorf(
				"rate_extract/fx: the scenario expects %q, and the anchor upper-cases and trims every currency code before it is compared",
				code)
		case !want[code]:
			return nil, fmt.Errorf(
				"rate_extract/fx: the scenario expects %q, which this sheet does not track, so the refresh drops it unread",
				code)
		}
		rate, err := fxRateString(stated[code], false)
		if err != nil {
			return nil, fmt.Errorf(
				"rate_extract/fx: the scenario expects %s for %q, which is not a rate the sheet can store: %w",
				stated[code], code, err)
		}
		out[code] = rateFxExpectation{stated: stated[code], rate: rate}
	}
	return out, nil
}

// rateFxCase is one rates page ready to be read, closed over the base every pair
// is anchored to, the currencies this sheet asked for, and the rates the scenario
// expects.
type rateFxCase struct {
	pageText string
	base     string
	want     map[string]bool
	expected map[string]rateFxExpectation
}

// Run issues the one request this site sends, bare — and so does the refresh:
// the extraction goes over the plain completer seam, never asking for the
// shape-retry, so one refresh is one call there and here alike.
func (c *rateFxCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := fxExtractRequest(c.pageText)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("rate_extract/fx: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the refresh's own steps in the refresh's own order — parse,
// then collect, which gates, anchors and normalizes in one pass — and only then
// asks whether what survived is priced the way the scenario says the page prices
// it. The order is the meaning: a pair collect dropped is not a rate to disagree
// with.
//
// Nothing surviving is OutcomeInvalid and NOT an abstention. There is no silence
// to report here: collect is driven by the workspace's tracked currencies and
// names every one the page left unpriced, so a run that fetched nothing always
// carries a refusal per tracked currency — the gate spoke even when the model
// did not. A reply that grounded something is usable whatever else it claimed, so
// a missing or misread rate is a wrong answer, named as such.
func (c *rateFxCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	pairs, err := parseFxExtraction(trace.Output)
	if err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	var refused fxRefusalLog
	// collect reads nothing of the refresh but its logger; every other field
	// belongs to the fetch and to the staging, neither of which a case runs.
	fetched := fxRefresh{log: refused.logger()}.collect(c.base, pairs, c.want)
	// Every refusal reaches the Detail whatever the result: a reply that read the
	// expected rates while inventing three ungrounded ones is not the clean run it
	// would otherwise look like. A run with nothing fetched always carries one,
	// because collect names every tracked currency the page left unpriced.
	detail := refused.refusals()
	if len(fetched) == 0 {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: strings.Join(detail, "; ")}
	}
	if disagreements := c.disagreements(fetched); len(disagreements) > 0 {
		return aitasks.Outcome{
			Result: aitasks.OutcomeWrongAnswer,
			Detail: strings.Join(append(disagreements, detail...), "; "),
		}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted, Detail: strings.Join(detail, "; ")}
}

// disagreements names every expected currency the fetched rates do not price the
// way the scenario says. All of them, not the first: a run that read one currency
// right and two wrong is not the near miss one line would read as.
//
// Rates compare under sameRate, the product's own numeric equality of two decimal
// strings — so a scenario neither fails on a trailing zero nor passes on a
// rounding.
//
// Sorted so a run with two disagreements names them in the same order every time.
func (c *rateFxCase) disagreements(fetched map[string]string) []string {
	var out []string
	for _, code := range slices.Sorted(maps.Keys(c.expected)) {
		want := c.expected[code]
		got, priced := fetched[code]
		switch {
		case !priced:
			out = append(out, fmt.Sprintf("no rate for %q, which the scenario expects", code))
		case !sameRate(got, want.rate):
			out = append(out, fmt.Sprintf("%q is priced %s against the base where the scenario expects %s",
				code, got, want.stated))
		}
	}
	return out
}

// fxRefusalLog reads collect's own account of what it dropped. collect returns
// only what survived and reports every drop to its logger, so the logger is where
// each refusal is already named — in the refresh's words, at the moment it was
// decided. Reading it there is what keeps the case from re-deciding beside
// collect, and a second copy of a decision is the copy that stops failing when
// the first one changes.
type fxRefusalLog struct{ recorded strings.Builder }

// logger hands collect somewhere to say why. The clock and the severity are
// dropped because neither is a reason, and a wall clock in a Detail would make
// two identical runs read as different ones.
func (l *fxRefusalLog) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&l.recorded, &slog.HandlerOptions{
		Level: slog.LevelWarn,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey || a.Key == slog.LevelKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

// refusals is one line per drop, exactly as the refresh recorded it.
func (l *fxRefusalLog) refusals() []string {
	recorded := strings.TrimSpace(l.recorded.String())
	if recorded == "" {
		return nil
	}
	return strings.Split(recorded, "\n")
}
