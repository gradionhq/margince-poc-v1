// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/webread"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// A catalog served as one line numbered to a SINGLE passage, so every extracted
// row could only ever cite [s0] — the evidence gate passed because there was
// nothing to disagree with. One line per model is what gives a row a passage
// that actually grounds it.
func TestCatalogPassagesGiveEachModelItsOwnPassage(t *testing.T) {
	body := `{"data":[` +
		`{"id":"vendor/a","pricing":{"prompt":"0.0000002","completion":"0.0000006"}},` +
		`{"id":"vendor/b","pricing":{"prompt":"0.0000001","completion":"0.0000003"}}` +
		`]}`
	reduced, kept, ok := catalogPassages(body, nil)
	if !ok {
		t.Fatal("a well-formed catalog must be recognised")
	}
	if kept != 2 {
		t.Fatalf("kept %d models, want 2", kept)
	}
	numbered := numberPassages(reduced)
	for _, want := range []string{"[s0]", "[s1]", "vendor/a", "vendor/b"} {
		if !strings.Contains(numbered, want) {
			t.Errorf("numbered passages missing %q:\n%s", want, numbered)
		}
	}
	if strings.Contains(numbered, "[s2]") {
		t.Errorf("two models must number to exactly two passages:\n%s", numbered)
	}
}

// The whole point of the filter: a provider catalog of hundreds becomes the
// handful this deployment actually calls, so the reply fits inside the output
// ceiling instead of truncating mid-JSON.
func TestCatalogPassagesKeepOnlyTheBoundModels(t *testing.T) {
	var entries []string
	for _, id := range []string{"vendor/a", "vendor/b", "vendor/c", "vendor/d"} {
		entries = append(entries, `{"id":"`+id+`","pricing":{"prompt":"0.000001"}}`)
	}
	body := `{"data":[` + strings.Join(entries, ",") + `]}`

	reduced, kept, ok := catalogPassages(body, map[string]bool{"vendor/b": true, "vendor/d": true})
	if !ok || kept != 2 {
		t.Fatalf("catalogPassages kept %d (ok=%v), want the 2 bound models", kept, ok)
	}
	if !strings.Contains(reduced, "vendor/b") || !strings.Contains(reduced, "vendor/d") {
		t.Errorf("the bound models must survive:\n%s", reduced)
	}
	if strings.Contains(reduced, "vendor/a") || strings.Contains(reduced, "vendor/c") {
		t.Errorf("an unbound model must not be priced — nobody reads its rate row:\n%s", reduced)
	}
}

// Empty means "nothing to filter by", which restores the previous behaviour
// rather than silently refreshing nothing.
func TestCatalogPassagesWithNoBoundSetKeepEveryModel(t *testing.T) {
	body := `{"data":[{"id":"a","x":1},{"id":"b","x":2},{"id":"c","x":3}]}`
	_, kept, ok := catalogPassages(body, map[string]bool{})
	if !ok || kept != 3 {
		t.Fatalf("kept %d (ok=%v), want every model when nothing is bound", kept, ok)
	}
}

func TestCatalogPassagesRefuseWhatIsNotACatalog(t *testing.T) {
	for _, body := range []string{
		`{"amount":1.0,"base":"EUR","rates":{"USD":1.08}}`, // an FX body: JSON, but no catalog
		`not json at all`,
		`{"data":"a string, not a list"}`,
	} {
		if _, _, ok := catalogPassages(body, nil); ok {
			t.Errorf("catalogPassages(%q) reported a catalog; it is not one", body)
		}
	}
}

// A catalog entry naming no model cannot be matched to a rate row, so it is
// dropped rather than passed to the model as an unattributable passage.
func TestCatalogPassagesDropAnEntryThatNamesNoModel(t *testing.T) {
	body := `{"data":[{"id":"vendor/a"},{"pricing":{"prompt":"1"}},{"id":"   "}]}`
	reduced, kept, ok := catalogPassages(body, nil)
	if !ok || kept != 1 {
		t.Fatalf("kept %d (ok=%v), want only the entry that names a model", kept, ok)
	}
	if strings.Count(strings.TrimSpace(reduced), "\n") != 0 {
		t.Errorf("one surviving model is one passage:\n%q", reduced)
	}
}

// The code selects by identity and never reads, converts or rewrites a price:
// interpreting the numbers stays the model's job, behind the evidence gate and
// the confirm-first approval that follow.
func TestCatalogPassagesLeaveEveryPriceUntouched(t *testing.T) {
	body := `{"data":[{"id":"vendor/a","pricing":{"prompt":"0.00000009","completion":"0.0000006"}}]}`
	reduced, _, ok := catalogPassages(body, nil)
	if !ok {
		t.Fatal("catalogPassages refused a well-formed catalog")
	}
	if !strings.Contains(reduced, `"0.00000009"`) {
		t.Errorf("the vendor's own price string must reach the model verbatim:\n%s", reduced)
	}
	// And the passage is still the entry itself, not a re-shaped copy.
	var back struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(reduced)), &back); err != nil {
		t.Fatalf("each passage must remain a readable catalog entry: %v", err)
	}
	if back.ID != "vendor/a" {
		t.Errorf("id = %q, want vendor/a", back.ID)
	}
}

// catalogFetcher serves one body with a declared media type — the seam the
// production fetcher fills, so extract's branch on IsJSON is exercised rather
// than assumed.
type catalogFetcher struct {
	text      string
	mediaType string
}

func (f catalogFetcher) Fetch(_ context.Context, _ string) (webread.Doc, error) {
	return webread.Doc{Text: f.text, MediaType: f.mediaType}, nil
}

// catalogCaptureBrain records the request the extraction actually sent.
type catalogCaptureBrain struct {
	got   *model.Request
	reply string
}

func (b *catalogCaptureBrain) Complete(_ context.Context, req model.Request) (model.Response, error) {
	*b.got = req
	return model.Response{Text: b.reply}, nil
}

// The wiring test: a JSON catalog reaches the model as numbered PER-MODEL
// passages, narrowed to the bound set. Unit-level on purpose — the probe drives
// the certification case, which takes page text directly and therefore never
// crosses this branch.
func TestExtractSendsACatalogAsNarrowedPerModelPassages(t *testing.T) {
	var entries []string
	for _, id := range []string{"vendor/wanted", "vendor/ignored-1", "vendor/ignored-2"} {
		entries = append(entries, `{"id":"`+id+`","pricing":{"prompt":"0.0000005","completion":"0.0000015"}}`)
	}
	body := `{"data":[` + strings.Join(entries, ",") + `]}`

	var sent model.Request
	brain := &catalogCaptureBrain{got: &sent, reply: `{"models":[]}`}
	refresh := modelCostRefresh{
		fetcher: catalogFetcher{text: body, mediaType: "application/json"},
		brain:   brain,
		bound:   map[string]map[string]bool{"openai_compatible": {"vendor/wanted": true}},
		log:     slog.New(slog.DiscardHandler),
	}

	if _, err := refresh.extract(context.Background(), pricingSource{Provider: "openai_compatible", URL: "https://x.test/models"}); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(sent.Messages) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent.Messages))
	}
	payload := sent.Messages[0].Content
	if !strings.Contains(payload, "vendor/wanted") {
		t.Errorf("the bound model must reach the model:\n%s", payload)
	}
	for _, unwanted := range []string{"vendor/ignored-1", "vendor/ignored-2"} {
		if strings.Contains(payload, unwanted) {
			t.Errorf("%s is not bound and must not be priced:\n%s", unwanted, payload)
		}
	}
	if !strings.Contains(payload, "[s0]") {
		t.Errorf("the surviving model must be numbered as its own passage:\n%s", payload)
	}
	if strings.Contains(payload, "[s1]") {
		t.Errorf("one bound model is one passage:\n%s", payload)
	}
}

// A non-JSON page is untouched by the catalog branch — the HTML pricing pages
// (gemini, anthropic, openai) must keep reaching the model exactly as before.
func TestExtractLeavesANonJSONPageAlone(t *testing.T) {
	var sent model.Request
	brain := &catalogCaptureBrain{got: &sent, reply: `{"models":[]}`}
	refresh := modelCostRefresh{
		fetcher: catalogFetcher{text: "Aurora Large: input $5.00 / 1M tokens.", mediaType: "text/html"},
		brain:   brain,
		bound:   map[string]map[string]bool{"aurora": {"something/else": true}},
		log:     slog.New(slog.DiscardHandler),
	}
	if _, err := refresh.extract(context.Background(), pricingSource{Provider: "aurora", URL: "https://x.test/pricing"}); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(sent.Messages[0].Content, "Aurora Large") {
		t.Errorf("an HTML page must reach the model unfiltered:\n%s", sent.Messages[0].Content)
	}
}

// JSON that is not a catalog is a source problem, and the crawl must say so
// rather than send the model a body it cannot ground anything in.
func TestExtractReportsJSONThatIsNotACatalog(t *testing.T) {
	var sent model.Request
	refresh := modelCostRefresh{
		fetcher: catalogFetcher{text: `{"amount":1.0,"base":"EUR"}`, mediaType: "application/json"},
		brain:   &catalogCaptureBrain{got: &sent, reply: `{"models":[]}`},
		log:     slog.New(slog.DiscardHandler),
	}
	if _, err := refresh.extract(context.Background(), pricingSource{Provider: "x", URL: "https://x.test/m"}); err == nil {
		t.Fatal("JSON that is not a model catalog must be reported, not silently extracted")
	}
}

// A bound model absent from its own provider's catalog must NOT read as a
// successful refresh. Those ids came from the routing file precisely because
// this deployment calls them, so a vendor rename or a mis-spelled binding would
// otherwise leave their prices stale forever while every run reported success.
func TestExtractFailsWhenNoBoundModelAppearsInItsCatalog(t *testing.T) {
	var sent model.Request
	refresh := modelCostRefresh{
		fetcher: catalogFetcher{text: `{"data":[{"id":"vendor/a"}]}`, mediaType: "application/json"},
		brain:   &catalogCaptureBrain{got: &sent, reply: `{"models":[]}`},
		bound:   map[string]map[string]bool{"x": {"vendor/absent": true}},
		log:     slog.New(slog.DiscardHandler),
	}
	_, err := refresh.extract(context.Background(), pricingSource{Provider: "x", URL: "https://x.test/m"})
	if err == nil {
		t.Fatal("a bound model missing from its catalog must be reported, not counted as a refresh")
	}
	if !strings.Contains(err.Error(), "ai-routing") {
		t.Errorf("error %q should point at the binding the operator has to fix", err)
	}
	if sent.System != "" {
		t.Error("no model call may be made when there is nothing to price")
	}
}

// A deployment with no routing at all filters by nothing, which is what it had
// before any of this existed.
func TestExtractKeepsEveryModelWhenNothingIsWired(t *testing.T) {
	var sent model.Request
	refresh := modelCostRefresh{
		fetcher: catalogFetcher{text: `{"data":[{"id":"vendor/a"},{"id":"vendor/b"}]}`, mediaType: "application/json"},
		brain:   &catalogCaptureBrain{got: &sent, reply: `{"models":[]}`},
		log:     slog.New(slog.DiscardHandler),
	}
	if _, err := refresh.extract(context.Background(), pricingSource{Provider: "x", URL: "https://x.test/m"}); err != nil {
		t.Fatalf("an unwired deployment must not fail: %v", err)
	}
	payload := sent.Messages[0].Content
	if !strings.Contains(payload, "vendor/a") || !strings.Contains(payload, "vendor/b") {
		t.Errorf("an unfiltered catalog keeps every model:\n%s", payload)
	}
}

// rates.model_pricing keys and ai-routing.yaml provider names are two
// operator-edited files coupled by exact string equality. A mismatch would
// otherwise disable the filter, send the whole catalog, and fail downstream with
// a truncated-reply error pointing nowhere near the real cause.
func TestExtractRefusesAPricingSourceTheRoutingDoesNotBind(t *testing.T) {
	var sent model.Request
	refresh := modelCostRefresh{
		fetcher: catalogFetcher{text: `{"data":[{"id":"vendor/a"}]}`, mediaType: "application/json"},
		brain:   &catalogCaptureBrain{got: &sent, reply: `{"models":[]}`},
		bound:   map[string]map[string]bool{"openai_compatible": {"vendor/a": true}},
		log:     slog.New(slog.DiscardHandler),
	}
	_, err := refresh.extract(context.Background(), pricingSource{Provider: "openrouter", URL: "https://x.test/m"})
	if err == nil {
		t.Fatal("a pricing source naming a provider the routing does not bind must be refused")
	}
	// The message has to name BOTH spellings, or the operator cannot see which
	// of the two files to change.
	if !strings.Contains(err.Error(), "openrouter") || !strings.Contains(err.Error(), "openai_compatible") {
		t.Errorf("error %q must show both spellings", err)
	}
	if sent.System != "" {
		t.Error("no model call may be made on a misconfigured source")
	}
}

// One provider's bindings must never decide what another provider's catalog is
// filtered to.
func TestExtractFiltersByTheSourcesOwnProvider(t *testing.T) {
	var sent model.Request
	refresh := modelCostRefresh{
		fetcher: catalogFetcher{
			text:      `{"data":[{"id":"vendor/mine"},{"id":"vendor/theirs"}]}`,
			mediaType: "application/json",
		},
		brain: &catalogCaptureBrain{got: &sent, reply: `{"models":[]}`},
		bound: map[string]map[string]bool{
			"mine":   {"vendor/mine": true},
			"theirs": {"vendor/theirs": true},
		},
		log: slog.New(slog.DiscardHandler),
	}
	if _, err := refresh.extract(context.Background(), pricingSource{Provider: "mine", URL: "https://x.test/m"}); err != nil {
		t.Fatalf("extract: %v", err)
	}
	payload := sent.Messages[0].Content
	if !strings.Contains(payload, "vendor/mine") {
		t.Errorf("this provider's own bound model must survive:\n%s", payload)
	}
	if strings.Contains(payload, "vendor/theirs") {
		t.Errorf("another provider's binding must not select rows from this catalog:\n%s", payload)
	}
}

// The probe reports a passage count in two places and production numbers
// passages in a third. This holds the shared counter to the numbering it
// describes, so neither can drift into reporting a different rule.
func TestCountPassagesAgreesWithTheNumbering(t *testing.T) {
	for _, body := range []string{
		"",
		"one line",
		"a\nb\nc",
		"a\n\n   \nb\n",
		`{"data":[{"id":"a"},{"id":"b"}]}`,
		"trailing\n",
		"\n\nleading",
	} {
		numbered := numberPassages(body)
		want := strings.Count(numbered, "\n")
		if got := CountPassages(body); got != want {
			t.Errorf("CountPassages(%q) = %d, but numberPassages emitted %d passages", body, got, want)
		}
	}
}

// A vendor listing the same model twice must not make the count reach the
// number of bound models while a different one is missing — the caller reads
// that count to decide whether every binding was found.
func TestCatalogPassagesCountDistinctModelsNotEntries(t *testing.T) {
	body := `{"data":[{"id":"vendor/a","n":1},{"id":"vendor/a","n":2},{"id":"vendor/c"}]}`
	reduced, kept, ok := catalogPassages(body, map[string]bool{"vendor/a": true, "vendor/b": true})
	if !ok {
		t.Fatal("a well-formed catalog must be recognised")
	}
	if kept != 1 {
		t.Errorf("kept = %d, want 1 — vendor/a twice is still one model, and vendor/b is absent", kept)
	}
	if strings.Count(reduced, "vendor/a") != 1 {
		t.Errorf("one passage per model, however often the vendor lists it:\n%s", reduced)
	}
}
