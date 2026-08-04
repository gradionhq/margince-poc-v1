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
		bound:   map[string]bool{"vendor/wanted": true},
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
		bound:   map[string]bool{"something/else": true},
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

// Nothing bound appearing in the catalog is a configuration answer, not a crawl
// failure: it yields no models and no error, so the run is not retried forever.
func TestExtractIsQuietWhenTheCatalogCarriesNothingBound(t *testing.T) {
	var sent model.Request
	refresh := modelCostRefresh{
		fetcher: catalogFetcher{text: `{"data":[{"id":"vendor/a"}]}`, mediaType: "application/json"},
		brain:   &catalogCaptureBrain{got: &sent, reply: `{"models":[]}`},
		bound:   map[string]bool{"vendor/absent": true},
		log:     slog.New(slog.DiscardHandler),
	}
	models, err := refresh.extract(context.Background(), pricingSource{Provider: "x", URL: "https://x.test/m"})
	if err != nil {
		t.Fatalf("a catalog with none of the bound models is not a failure: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("extracted %d models, want none", len(models))
	}
	if sent.System != "" {
		t.Error("no model call may be made when there is nothing to price")
	}
}
