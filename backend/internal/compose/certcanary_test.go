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
// "Free text" is not read off the shape of the value. Prose announces itself by
// carrying whitespace, but a subject line reading "Invoice", a display name and
// a sender address are free text spelled exactly like the enums, locales and
// hashes beside them — and those a stamp would break for reasons that have
// nothing to do with the instruction channel. plantCanary tells the two apart
// by asking the site: a single-word field is offered to the site's own Prepare
// with the marker on it, and joins the run only if the site still accepts it,
// because a closed vocabulary is precisely what refuses a value outside itself.
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
	"maps"
	"slices"
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

// codebaseOwnedFixtureKeys names, per site, the top-level fixture keys whose
// content this build mints rather than receives, with the reason each one is
// not a stranger's text. Those are left unstamped: the gate asks whether text
// somebody else wrote reaches the instruction channel, and a value this
// codebase chose is not that — stamping one would report the site's own
// vocabulary as a leak.
//
// A key without a reason is itself a finding. This is the one place the gate
// takes an answer on trust instead of deriving it, so growing it must cost an
// argument about where the value comes from in PRODUCTION, never about what
// made the test pass.
var codebaseOwnedFixtureKeys = map[string]map[string]string{
	"agent_loop/loop": {
		"tools": "the loop names its callable tools and their schemas in the instruction channel by design (runner/window.go's systemPrompt), and that catalog is the governed MCP tool surface this build registers — a scenario carries one only because a loop cannot be seeded without it. The stranger-supplied halves of the same fixture, the goal and the grounding, are stamped and must stay out.",
	},
}

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
			ours := map[string]bool{}
			for field, reason := range codebaseOwnedFixtureKeys[key] {
				if strings.TrimSpace(reason) == "" {
					t.Errorf("site %s leaves fixture field %q unstamped with no reason — say where its content comes from in production, or drop the entry", key, field)
				}
				ours[field] = true
			}
			for _, sc := range owned {
				assertNoCanaryInAnySystemPrompt(t, census, sc, ours)
			}
		})
	}
}

// assertNoCanaryInAnySystemPrompt drives one scenario's real case over a
// canary-stamped copy of its fixture and reads every request the case issued.
func assertNoCanaryInAnySystemPrompt(t *testing.T, census *aitasks.Registry, sc aicert.Scenario, codebaseOwned map[string]bool) {
	t.Helper()

	factory, bound := census.CaseFor(ai.Task(sc.Task), sc.Site)
	if !bound {
		t.Fatalf("scenario %s names site %s/%s, which no case is bound to", sc.Name, sc.Task, sc.Site)
	}
	accepts := func(candidate json.RawMessage) bool {
		_, err := factory.Prepare(candidate, json.RawMessage(sc.Expect.Answer))
		return err == nil
	}
	// The probe below reads a refusal as "this field is a closed vocabulary",
	// so a site that refuses the fixture BEFORE anything is stamped would read
	// every one of its fields that way and stamp none of them — the gate would
	// pass by measuring nothing.
	if !accepts(json.RawMessage(sc.Fixture)) {
		t.Fatalf("scenario %s: the site refuses its own unstamped fixture, so nothing can be learned from what it refuses stamped", sc.Name)
	}
	stamped, planted, err := plantCanary(json.RawMessage(sc.Fixture), accepts, codebaseOwned)
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
// field whose text a stranger could write, and how many of those carried prose.
//
// Two kinds of field get stamped, and they are separated because only one of
// them can be recognised by looking at it:
//
// A field carrying WHITESPACE is prose, and prose is always stamped. That is
// what a mail body, a crawled page or a chat turn looks like, and it is never
// what an enum, a URL, a locale, a currency code or a hash looks like.
//
// A SINGLE-WORD field is the hard case, and it is the one a whitespace rule
// alone lets through: a mail subject reading "Invoice", a display name, a
// company name are all free text a stranger chose, and they sit in a fixture
// spelled exactly like the closed-vocabulary tokens beside them. Stamping those
// tokens is what the whitespace rule was avoiding — an enum or a locale with a
// marker glued on makes Prepare refuse the fixture for a reason that has
// nothing to do with the instruction channel, and the gate would then measure
// the refusal instead of the prompt.
//
// So the two are told apart by asking the site rather than by guessing: each
// single-word field is stamped ALONE and offered to the site's own Prepare, and
// it joins the run only if the site still accepts it. A closed vocabulary is
// exactly the thing that refuses a value outside it, and the site's validator
// is the only authority on which of its fields those are.
//
// What this does NOT establish is that every stamped single-word field reached
// the model at all. A field the site reads for its own purposes and never sends
// — a lookup id, a flag — is stamped, refuses nothing, and shows up in no
// request; the prose count returned here is what the caller's reachability
// assertion rests on, because prose is the part whose arrival can be proved.
// The gate over single-word fields is therefore one-sided: it can catch one
// reaching the instruction channel, and it cannot certify that one which never
// appears was truly never sent.
//
// The marker is APPENDED rather than substituted so the fixture still says what
// it said: a site whose validator gates on evidence present in the source text
// keeps finding it, and the scenario's expected answer stays reachable. It is
// glued to the field's last word rather than added as a new one, so a fixture
// that declares its own text's length still agrees with itself.
func plantCanary(fixture json.RawMessage, accepts func(json.RawMessage) bool, codebaseOwned map[string]bool) (json.RawMessage, int, error) {
	var leaves []fixtureLeaf
	if _, err := stampStringLeaves(fixture, func(leaf fixtureLeaf) bool {
		leaves = append(leaves, leaf)
		return false
	}); err != nil {
		return nil, 0, err
	}

	prose := map[int]bool{}
	var words []int
	for i, leaf := range leaves {
		switch {
		case codebaseOwned[leaf.field], strings.TrimSpace(leaf.value) == "":
		case strings.ContainsAny(leaf.value, " \t\n"):
			prose[i] = true
		default:
			words = append(words, i)
		}
	}

	freeWords := map[int]bool{}
	for _, at := range words {
		probe, err := stampStringLeaves(fixture, func(leaf fixtureLeaf) bool { return leaf.index == at })
		if err != nil {
			return nil, 0, err
		}
		if accepts(probe) {
			freeWords[at] = true
		}
	}

	stamped, err := stampStringLeaves(fixture, func(leaf fixtureLeaf) bool {
		return prose[leaf.index] || freeWords[leaf.index]
	})
	if err != nil {
		return nil, 0, err
	}
	return stamped, len(prose), nil
}

// fixtureLeaf is one string somewhere inside a fixture: its position in the
// walk, the top-level field whose subtree it belongs to, and its text.
type fixtureLeaf struct {
	index int
	field string
	value string
}

// stampStringLeaves returns a copy of a fixture with the canary appended to
// every string leaf keep selects. Leaves are visited in a fixed order — array
// order, and map keys sorted — so one leaf's index means the same thing on the
// probing pass and on the stamping pass that follows it.
//
// The walk goes through the empty interface because the shape belongs to the
// site, not to this test — the same reason the corpus loader decodes a fixture
// that way.
func stampStringLeaves(fixture json.RawMessage, keep func(fixtureLeaf) bool) (json.RawMessage, error) {
	var decoded any
	if err := json.Unmarshal(fixture, &decoded); err != nil {
		return nil, err
	}
	index := 0
	var stamp func(value any, field string) any
	stamp = func(v any, field string) any {
		switch typed := v.(type) {
		case string:
			leaf := fixtureLeaf{index: index, field: field, value: typed}
			index++
			if !keep(leaf) || strings.TrimSpace(typed) == "" {
				return typed
			}
			// Glued to the last word, inside whatever trailing whitespace
			// the field already had: a fixture may declare its own text's
			// word count, and a marker that arrived as a separate word
			// would contradict it.
			body := strings.TrimRight(typed, " \t\n")
			return body + canary + typed[len(body):]
		case []any:
			for i, item := range typed {
				typed[i] = stamp(item, field)
			}
			return typed
		case map[string]any:
			for _, key := range slices.Sorted(maps.Keys(typed)) {
				// The field a leaf is attributed to is the OUTERMOST key it
				// sits under, so naming one covers its whole subtree — a
				// fixture's tool catalog is one field however deeply its
				// schemas nest.
				under := field
				if under == "" {
					under = key
				}
				typed[key] = stamp(typed[key], under)
			}
			return typed
		default:
			return v
		}
	}
	stamped, err := json.Marshal(stamp(decoded, ""))
	if err != nil {
		return nil, err
	}
	return stamped, nil
}
