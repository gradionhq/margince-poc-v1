// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the signature-enrich case owes the certification lane: it issues the
// request the pass issues, it judges the reply with the no-guess gate and the
// acceptance floor the pass judges it with, and it separates a reply nothing
// survived from one that survived and says the wrong thing. The two want
// opposite fixes — a fabricating model is a prompt problem, a confidently-wrong
// one is a model choice — and a case that collapsed them could report neither.
//
// The window is the sharp edge here. The model is shown the trailing non-quoted
// lines, never the whole mail, so a case whose gate read the mail would accept a
// quote from a reply the model never saw — and certify a mismatch.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// signatureEnrichMail is the mail every case below reads. Its quoted history
// names a title too, and that is the point: the pass excludes quoted lines from
// the window, so a quote from Alice's mail is evidence the model was never shown.
const signatureEnrichMail = "Hi Alice,\n\n" +
	"Happy to look at the March numbers — I will send the deck tomorrow.\n\n" +
	"> On Tuesday, Alice Berger wrote:\n" +
	"> Could you send the March numbers? Our CTO Dana Weiss asked for them.\n\n" +
	"Best,\n" +
	"Bob Person\n" +
	"CTO, Acme Robotics GmbH\n" +
	"+49 30 1234567\n"

// signatureEnrichCompleterStub answers with the canned reply a run is about.
// What the model was asked reaches the assertions through the trace, which is
// where the record and the canary gate read it from too.
type signatureEnrichCompleterStub struct {
	reply string
}

func (s *signatureEnrichCompleterStub) Complete(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{Text: s.reply}, nil
}

// signatureEnrichReply is the raw text a model returns, built as text rather
// than marshalled so a malformed reply is as expressible as a well-formed one.
func signatureEnrichReply(claims ...string) string {
	return `{"fields":[` + strings.Join(claims, ",") + `]}`
}

// signatureEnrichClaim is one claimed field in the shape the prompt demands. The
// confidence sits above the §2.9 acceptance floor, so a case that measures
// grounding is never failing on a number instead.
func signatureEnrichClaim(field, value, evidence string) string {
	return signatureEnrichClaimAt(field, value, evidence, 0.9)
}

func signatureEnrichClaimAt(field, value, evidence string, confidence float64) string {
	return fmt.Sprintf(
		`{"field":%q,"value":%q,"evidence_snippet":%q,"confidence":%v}`, field, value, evidence, confidence)
}

func signatureEnrichFixtureJSON(t *testing.T, body string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(signatureEnrichFixture{
		FullName: "Bob Person",
		Email:    "bob@acme-robotics.example",
		Body:     body,
	})
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return raw
}

// signatureEnrichExpectation is what the corpus asserts, encoded as the corpus
// will carry it — beside the fixture, never inside it.
func signatureEnrichExpectation(t *testing.T, want map[string]string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("encoding the expectation: %v", err)
	}
	return raw
}

func runSignatureEnrichCase(
	t *testing.T, body string, want map[string]string, reply string,
) (aitasks.Outcome, aitasks.Trace) {
	t.Helper()
	prepared, err := signatureEnrichCases{}.Prepare(
		signatureEnrichFixtureJSON(t, body), signatureEnrichExpectation(t, want))
	if err != nil {
		t.Fatalf("preparing the case: %v", err)
	}
	trace, err := prepared.Run(context.Background(), &signatureEnrichCompleterStub{reply: reply})
	if err != nil {
		t.Fatalf("running the case: %v", err)
	}
	return prepared.Evaluate(trace), trace
}

var signatureEnrichWantTitle = map[string]string{"title": "CTO"}

func TestSignatureEnrichCaseSeparatesTheThreeThingsAReplyCanBe(t *testing.T) {
	titleClaim := signatureEnrichClaim("title", "CTO", "CTO, Acme Robotics GmbH")

	cases := []struct {
		name       string
		want       map[string]string
		reply      string
		wantResult string
		wantDetail string
	}{
		{
			name:       "the expected field, quoted from the signature",
			want:       signatureEnrichWantTitle,
			reply:      signatureEnrichReply(titleClaim),
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			// The gate's own refusal, reported in its own words. An invented quote is
			// the shape a fabricating model takes, and nothing survives it here.
			name:       "every claim quotes something the signature never said",
			want:       signatureEnrichWantTitle,
			reply:      signatureEnrichReply(signatureEnrichClaim("title", "VP Sales", "VP Sales, Acme Robotics")),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: dropEvidenceNotOnPage,
		},
		{
			name:       "a reply that is not the required JSON",
			want:       signatureEnrichWantTitle,
			reply:      "There is no signature in this email.",
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: dropUnparseableReply,
		},
		{
			name:       "a field outside the §2.9 vocabulary",
			want:       signatureEnrichWantTitle,
			reply:      signatureEnrichReply(signatureEnrichClaim("employer", "Acme Robotics GmbH", "Acme Robotics GmbH")),
			wantResult: aitasks.OutcomeInvalid,
			wantDetail: dropUnknownField,
		},
		{
			// Omission is what this prompt asks for when there is nothing to quote, so
			// a reply that claims nothing is a usable answer that misses the fact the
			// scenario pinned — not a broken reply. The pass agrees: it applies
			// nothing and picks the person up again next cycle.
			name:       "a reply that claims nothing at all",
			want:       signatureEnrichWantTitle,
			reply:      signatureEnrichReply(),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "no surviving title",
		},
		{
			// Grounded, usable, and not the value the scenario pinned: a measurement
			// of the model, not a defect in the reply.
			name:       "a grounded field the scenario disagrees with",
			want:       signatureEnrichWantTitle,
			reply:      signatureEnrichReply(signatureEnrichClaim("title", "Chief Technology Officer", "CTO, Acme")),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: `expects "CTO"`,
		},
		{
			// The floor is the pass's acceptance rule, not a hint: a field below it is
			// never written, so a scenario that counted it would certify a fill the
			// product does not perform.
			name:       "the expected field, hedged below the acceptance floor",
			want:       signatureEnrichWantTitle,
			reply:      signatureEnrichReply(signatureEnrichClaimAt("title", "CTO", "CTO, Acme Robotics GmbH", 0.4)),
			wantResult: aitasks.OutcomeWrongAnswer,
			wantDetail: "below the",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, _ := runSignatureEnrichCase(t, signatureEnrichMail, tc.want, tc.reply)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
			if !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("Detail = %q, want it to name %q", outcome.Detail, tc.wantDetail)
			}
		})
	}
}

// The model is shown a window, not a mail: quoted history is excluded and only
// the trailing lines survive. A case whose gate read the whole body would accept
// a quote from text the model was never given — certifying a claim about a
// prompt nobody sent.
func TestSignatureEnrichCaseGatesAgainstTheWindowTheModelWasShown(t *testing.T) {
	t.Run("a quote from the quoted history is not evidence", func(t *testing.T) {
		outcome, trace := runSignatureEnrichCase(t, signatureEnrichMail, signatureEnrichWantTitle,
			signatureEnrichReply(signatureEnrichClaim("title", "CTO", "Our CTO Dana Weiss asked for them")))

		if outcome.Result != aitasks.OutcomeInvalid {
			t.Fatalf("Result = %q (%s), want the quoted history to ground nothing", outcome.Result, outcome.Detail)
		}
		if strings.Contains(trace.Requests[0].Messages[0].Content, "Dana Weiss") {
			t.Error("the quoted history reached the prompt, so the window is not what the pass builds")
		}
	})

	t.Run("a quote from above the window is not evidence", func(t *testing.T) {
		var body strings.Builder
		body.WriteString("Head of Sales at Globex, as I said in January.\n")
		for i := range signatureLineCount {
			fmt.Fprintf(&body, "prose line %d\n", i)
		}
		body.WriteString("Bob Person\nCTO, Acme Robotics GmbH\n")

		outcome, _ := runSignatureEnrichCase(t, body.String(), signatureEnrichWantTitle,
			signatureEnrichReply(signatureEnrichClaim("title", "Head of Sales", "Head of Sales at Globex")))

		if outcome.Result != aitasks.OutcomeInvalid {
			t.Fatalf("Result = %q (%s), want a line outside the window to ground nothing", outcome.Result, outcome.Detail)
		}
	})
}

// A reply that grounds what the scenario asked for while fabricating something
// else is not the clean run it would otherwise look like, so every gate refusal
// reaches the Detail whatever the result. A record that hid them would report a
// fabricating model as a healthy one.
func TestSignatureEnrichCaseReportsGateDropsEvenWhenItAccepts(t *testing.T) {
	outcome, _ := runSignatureEnrichCase(t, signatureEnrichMail, signatureEnrichWantTitle, signatureEnrichReply(
		signatureEnrichClaim("title", "CTO", "CTO, Acme Robotics GmbH"),
		signatureEnrichClaim("phone", "+49 170 9999999", "Mobile: +49 170 9999999"),
	))

	if outcome.Result != aitasks.OutcomeAccepted {
		t.Fatalf("Result = %q (%s), want accepted", outcome.Result, outcome.Detail)
	}
	if !strings.Contains(outcome.Detail, dropEvidenceNotOnPage) {
		t.Errorf("Detail = %q, want it to name the fabricated claim the gate dropped", outcome.Detail)
	}
}

// A fixture is what PRODUCTION is given; an expectation is what the CORPUS
// asserts. Keeping them apart is what lets a gate rewrite every free-text field
// of a fixture — the canary sweep does exactly that — without rewriting an
// assertion, and it is what keeps the minted activity id out of the corpus.
func TestSignatureEnrichFixtureCarriesOnlyWhatProductionIsGiven(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(signatureEnrichFixtureJSON(t, signatureEnrichMail), &fields); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	given := map[string]bool{"full_name": true, "email": true, "body": true}
	for name := range fields {
		if !given[name] {
			t.Errorf("the fixture carries %q, which the candidate read does not put in this prompt", name)
		}
	}
	for name := range given {
		if _, present := fields[name]; !present {
			t.Errorf("the fixture drops %q, which every signature call carries", name)
		}
	}
}

// The fixture supplies no activity id, and Prepare mints a fresh one per
// preparation. Production reads it from the linked mail's ledger row, which no
// sender has written, so a fixture carrying one would put a product-side
// identifier in the hands of whoever authored the mail.
func TestSignatureEnrichCaseMintsTheActivityIDRatherThanReadingIt(t *testing.T) {
	first := signatureSourceIDIn(t, runSignatureEnrichTrace(t))
	second := signatureSourceIDIn(t, runSignatureEnrichTrace(t))

	if first == "" {
		t.Fatal("the signature window carries no source id")
	}
	if first == second {
		t.Errorf("two preparations share the activity id %q", first)
	}
}

func runSignatureEnrichTrace(t *testing.T) model.Request {
	t.Helper()
	_, trace := runSignatureEnrichCase(t, signatureEnrichMail, signatureEnrichWantTitle,
		signatureEnrichReply(signatureEnrichClaim("title", "CTO", "CTO, Acme Robotics GmbH")))
	if len(trace.Requests) != 1 {
		t.Fatalf("the trace carries %d requests, want the one this site issues", len(trace.Requests))
	}
	return trace.Requests[0]
}

// signatureSourceIDIn reads the id off the signature span, anchored on the
// boundary this request's own system prompt declares.
func signatureSourceIDIn(t *testing.T, req model.Request) string {
	t.Helper()
	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		t.Fatalf("the traced request declares no data boundary: %q", req.System)
	}
	open := "<" + marker + ` source_id="`
	content := req.Messages[0].Content
	openAt := strings.Index(content, open)
	if openAt < 0 {
		t.Fatalf("the signature window is not opened under the declared boundary:\n%s", content)
	}
	rest := content[openAt+len(open):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("the source id attribute never closes:\n%s", content)
	}
	return rest[:end]
}

// The trace is what the canary gate and the record read. A case that ran the
// production request but recorded nothing would certify a request nobody can
// inspect.
func TestSignatureEnrichCaseTraceCarriesTheRequestItIssued(t *testing.T) {
	req := runSignatureEnrichTrace(t)

	if !strings.Contains(req.System, "You extract contact fields from ONE email signature") {
		t.Errorf("the traced request is not the signature prompt: %q", req.System)
	}
	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		t.Fatalf("the traced request declares no data boundary: %q", req.System)
	}
	if !strings.Contains(req.Messages[0].Content, "CTO, Acme Robotics GmbH\n+49 30 1234567</"+marker+">") {
		t.Errorf("the signature window is not wrapped in the declared boundary:\n%s", req.Messages[0].Content)
	}
}

// An expectation the gate can never satisfy would measure nothing for as long as
// it stayed in the corpus. Prepare is where that gets named, while it is still a
// wiring error rather than a paid run of zeros.
func TestSignatureEnrichCaseRefusesAnUnreachableExpectation(t *testing.T) {
	cases := []struct {
		name       string
		want       map[string]string
		wantReason string
	}{
		{
			name:       "a field name the prompt never offers the model",
			want:       map[string]string{"employer": "Acme Robotics GmbH"},
			wantReason: "never offers",
		},
		{
			name:       "an empty value, which the gate drops from every reply",
			want:       map[string]string{"title": "  "},
			wantReason: "empty value",
		},
		{
			name:       "no expectation at all",
			want:       map[string]string{},
			wantReason: "expects no field",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := signatureEnrichCases{}.Prepare(
				signatureEnrichFixtureJSON(t, signatureEnrichMail), signatureEnrichExpectation(t, tc.want))
			if err == nil {
				t.Fatalf("a scenario expecting %v prepared", tc.want)
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Errorf("the refusal does not say why it is unreachable: %v", err)
			}
		})
	}
}

// A mail whose non-quoted tail is empty is one the pass never sends: it returns
// before the model call, having nothing to read. A case that called anyway would
// certify a request production does not issue.
func TestSignatureEnrichCaseRefusesAMailWithNoSignatureBlock(t *testing.T) {
	_, err := signatureEnrichCases{}.Prepare(
		signatureEnrichFixtureJSON(t, "> On Tuesday, Alice wrote:\n> Could you send the March numbers?\n"),
		signatureEnrichExpectation(t, signatureEnrichWantTitle))
	if err == nil {
		t.Fatal("a mail with no signature block prepared")
	}
	if !strings.Contains(err.Error(), "signature block") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// A scenario shaped like something else asserts nothing about the reply, and a
// case that ran it anyway would report a number nobody wrote a claim for.
func TestSignatureEnrichCaseRefusesAnExpectationItCannotRead(t *testing.T) {
	for _, expected := range []json.RawMessage{nil, json.RawMessage(`["title"]`), json.RawMessage(`7`)} {
		_, err := signatureEnrichCases{}.Prepare(signatureEnrichFixtureJSON(t, signatureEnrichMail), expected)
		if err == nil {
			t.Fatalf("a scenario expecting %s prepared", expected)
		}
		if !strings.Contains(err.Error(), "field to value") {
			t.Errorf("the refusal does not say what an expectation must be: %v", err)
		}
	}
}

// A fixture shaped like something else is not a candidate, and running it would
// certify a prompt built from whatever survived the decode.
func TestSignatureEnrichCaseRefusesAFixtureItCannotRead(t *testing.T) {
	_, err := signatureEnrichCases{}.Prepare(
		json.RawMessage(`"Best, Bob"`), signatureEnrichExpectation(t, signatureEnrichWantTitle))
	if err == nil {
		t.Fatal("a fixture that is not a candidate prepared")
	}
	if !strings.Contains(err.Error(), "the shape this site takes") {
		t.Errorf("the refusal does not say what a fixture must be: %v", err)
	}
}

// The case must be reachable through the same registry the census validates, or
// the site is registered and served by nothing.
func TestTaskCensusBindsTheSignatureEnrichCase(t *testing.T) {
	registry, err := NewTaskCensus()
	if err != nil {
		t.Fatalf("the census does not validate: %v", err)
	}
	site := signatureEnrichCases{}.Site()
	bound, ok := registry.CaseFor(site.Task, site.Variant)
	if !ok {
		t.Fatalf("no certification case is bound to %s/%s", site.Task, site.Variant)
	}
	if bound.Site() != site {
		t.Errorf("the bound case serves %+v, want %+v", bound.Site(), site)
	}
}
