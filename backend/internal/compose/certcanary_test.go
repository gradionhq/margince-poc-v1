// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose_test

// The canary gate: no site lets fixture text reach the instruction channel.
//
// Every request a site issues has two channels, and the whole prompt-injection
// posture rests on which one carries what. The system prompt is this codebase
// speaking; the user turn is data, fenced, and the model is told so. A site that
// interpolated a captured subject line, a crawled page, or an inbound mail body
// into its system prompt would hand an attacker the one channel the model is
// instructed to obey — and it would still pass every other test in the tree,
// because the request would be well-formed and the answer would look right.
//
// So the property is proved rather than reviewed: every free-text field of every
// site's own committed fixture is stamped with a string that occurs nowhere in
// this repository, the site's real Prepare/Run is driven with it, and the
// instruction channel of every request it issues must not contain that string.
//
// The fixtures come from the committed corpus rather than being written here.
// They are what production is actually handed — a hand-written per-site fixture
// would be one more thing to keep true, and would drift toward whatever shape
// made the test pass.
//
// What this gate deliberately does NOT assert is that a site's system prompt is
// the same for every fixture. That is false today and correctly so:
// pageFactsSystem varies with the page's kind, onboardingActSystem with the act
// and the locale, and the reply drafter has a voiced and an unvoiced variant.
// Each of those inputs is chosen by this codebase, not supplied by a stranger,
// and a gate that forbade them would be forbidding legitimate work.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// canary is the marker planted in every free-text fixture field. It is
// deliberately unpronounceable and occurs nowhere else in the tree, so a
// substring match on it can only mean the fixture's own text arrived.
const canary = "qzvxCANARY7413zjw"

// canaryReply is what the stand-in model answers. A site's validator will
// mostly refuse it, which costs this gate nothing: refusal happens after the
// request was already issued, and the request is the whole subject here.
const canaryReply = "{}"

func TestNoFixtureTextReachesASystemPrompt(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	// The corpus lives beside the harness that runs it; this test reads it from
	// the composition layer's own directory.
	scenarios, err := aicert.LoadCorpus("aicert/corpus", census)
	if err != nil {
		t.Fatalf("loading the corpus: %v", err)
	}
	bySite := map[string][]aicert.Scenario{}
	for _, sc := range scenarios {
		bySite[sc.Task+"/"+sc.Site] = append(bySite[sc.Task+"/"+sc.Site], sc)
	}

	// The obligation is derived from the census, not from the corpus: a site
	// registered tomorrow is enrolled in this gate the moment it is bound.
	for _, site := range census.All() {
		key := string(site.Task) + "/" + site.Variant
		t.Run(key, func(t *testing.T) {
			owned := bySite[key]
			if len(owned) == 0 {
				t.Fatalf("site %s has no corpus scenario, so its instruction channel is never exercised", key)
			}
			for _, sc := range owned {
				assertNoCanaryInAnySystemPrompt(t, census, sc)
			}
		})
	}
}

// assertNoCanaryInAnySystemPrompt drives one scenario's real case over a
// canary-stamped copy of its fixture and reads every request the case issued.
func assertNoCanaryInAnySystemPrompt(t *testing.T, census *aitasks.Registry, sc aicert.Scenario) {
	t.Helper()

	factory, bound := census.CaseFor(ai.Task(sc.Task), sc.Site)
	if !bound {
		t.Fatalf("scenario %s names site %s/%s, which no case is bound to", sc.Name, sc.Task, sc.Site)
	}
	stamped, planted, err := plantCanary(json.RawMessage(sc.Fixture))
	if err != nil {
		t.Fatalf("scenario %s: stamping the fixture: %v", sc.Name, err)
	}
	prepared, err := factory.Prepare(stamped, json.RawMessage(sc.Expect.Answer))
	if err != nil {
		t.Fatalf("scenario %s: the site refused its own fixture once stamped: %v", sc.Name, err)
	}

	completer := &recordingCompleter{reply: canaryReply}
	if _, runErr := prepared.Run(context.Background(), completer); runErr != nil {
		// A case may refuse the stand-in reply, or stop the loop it drives.
		// That is its validator working and says nothing about where the
		// fixture's text went — the requests already issued are what this gate
		// reads, so the refusal is reported rather than treated as a failure.
		t.Logf("scenario %s: the case refused the stand-in reply (%v); reading the %d request(s) it had already issued",
			sc.Name, runErr, len(completer.requests))
	}
	if len(completer.requests) == 0 {
		t.Fatalf("scenario %s issued no request at all, so nothing about its prompt was measured", sc.Name)
	}

	reachedTheModel := false
	for i, req := range completer.requests {
		if strings.Contains(req.System, canary) {
			t.Errorf("scenario %s: request %d interpolates fixture text into its SYSTEM prompt — the instruction channel is this codebase's own voice, and text from a captured mail, a crawled page or a stranger's message belongs in the fenced user turn",
				sc.Name, i+1)
		}
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, canary) {
				reachedTheModel = true
			}
		}
	}
	// A gate that planted nothing would pass on a site that leaks everything.
	// Where the fixture carries free text, that text must be visible in the
	// data channel, which is what proves the stamping took effect.
	if planted > 0 && !reachedTheModel {
		t.Errorf("scenario %s: %d free-text field(s) were stamped, but none of the %d request(s) carries the marker — this scenario proves nothing about where its text goes",
			sc.Name, planted, len(completer.requests))
	}
}

// recordingCompleter is the stand-in model: it keeps every request it is handed
// and answers the same way each time. The requests are read from HERE rather
// than from the returned Trace because this is what the site actually sent — a
// case that under-recorded its own trace would hide exactly the leak this gate
// exists to find.
type recordingCompleter struct {
	requests []model.Request
	reply    string
}

func (c *recordingCompleter) Complete(_ context.Context, req model.Request) (model.Response, error) {
	c.requests = append(c.requests, req)
	return model.Response{Text: c.reply}, nil
}

// plantCanary returns a copy of a fixture with the canary appended to every
// free-text field, and how many fields it stamped.
//
// Free text is taken to be a string carrying whitespace. That is what prose is,
// and what the structured tokens beside it in a fixture — an enum, a URL, a
// locale, a currency code, a hash, an id the expectation names — never are.
// Stamping one of those would make Prepare refuse the fixture for a reason that
// has nothing to do with the instruction channel, and the gate would measure
// the refusal instead of the prompt.
//
// The marker is APPENDED rather than substituted so the fixture still says what
// it said: a site whose validator gates on evidence present in the source text
// keeps finding it, and the scenario's expected answer stays reachable. It is
// glued to the field's last word rather than added as a new one, so a fixture
// that declares its own text's length still agrees with itself.
//
// The walk goes through the empty interface because the shape belongs to the
// site, not to this test — the same reason the corpus loader decodes a fixture
// that way.
func plantCanary(fixture json.RawMessage) (json.RawMessage, int, error) {
	var decoded any
	if err := json.Unmarshal(fixture, &decoded); err != nil {
		return nil, 0, err
	}
	planted := 0
	var stamp func(any) any
	stamp = func(v any) any {
		switch typed := v.(type) {
		case string:
			if strings.ContainsAny(typed, " \t\n") && strings.TrimSpace(typed) != "" {
				planted++
				// Glued to the last word, inside whatever trailing whitespace
				// the field already had: a fixture may declare its own text's
				// word count, and a marker that arrived as a separate word
				// would contradict it.
				body := strings.TrimRight(typed, " \t\n")
				return body + canary + typed[len(body):]
			}
			return typed
		case []any:
			for i, item := range typed {
				typed[i] = stamp(item)
			}
			return typed
		case map[string]any:
			for k, item := range typed {
				typed[k] = stamp(item)
			}
			return typed
		default:
			return v
		}
	}
	stamped, err := json.Marshal(stamp(decoded))
	if err != nil {
		return nil, 0, err
	}
	return stamped, planted, nil
}
