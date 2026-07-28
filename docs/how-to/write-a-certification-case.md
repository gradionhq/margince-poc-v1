# Write a certification case (and its scenarios)

Step 6 of [add-an-ai-task.md](add-an-ai-task.md), on its own page because it is
where the thinking is. A **certification case** binds one invocation site to the
production code that serves it, so a certification run measures the request the
product actually sends, judged by the validator the product actually applies.

The rule under everything here: **call production, never re-create it.** A case
that rebuilds the request or re-implements the validator is testing a copy rather
than the real code — and when someone later breaks the real builder or validator,
that copy keeps passing, because it was never exercising the thing that broke.

Two files per site:

```text
internal/compose/certcase_<site>.go              the case
internal/compose/aicert/corpus/<task>/*.yaml     one or more scenarios
```

Both `.go` files you add here (the case and its test) need the two-line BUSL-1.1
SPDX header every hand-written Go file in this repo carries — see
[AGENTS.md § License headers](../../AGENTS.md); `make check` fails a file that
skips it.

## What a reply can be — the four outcomes

Read this first: everything below is written in these four words. `Evaluate`
returns exactly one of them, and they stay distinct because they fail for
different reasons and want different fixes:

| Outcome | Means |
|---|---|
| `accepted` | the production validator accepted the reply **and** it is the answer the fixture expects |
| `wrong_answer` | a well-formed reply the validator accepted, saying something else — a measurement of the model, not a defect |
| `invalid` | the production validator **refused** the reply: the deterministic signal that the model produced something unusable |
| `abstained` | the reply survived the validator and carries nothing, **and** the site treats that as completed work |

`invalid` and `abstained` are the pair worth getting right. A validator that
refused everything a reply claimed and a reply that claimed nothing both leave
zero rows — and they are opposite events: the first is a model fabricating past a
gate, the second is a model declining to fabricate. Where an empty result *is* the
failure — cold-start field extraction turns one into the unreadable-source message
a human is shown — report `invalid` instead.

## The loop: scenario first, then the case, then spend

Certification is a paid, network-bound lane, so the whole loop is designed to be
driven offline first. Work in this order and you will not spend anything until
the thing you are measuring is already known to work.

1. **Write the scenario.** The fixture is the input production is given, and
   `expect` is what a right answer looks like. Writing it first forces the
   question the case has to answer: *what, exactly, separates a correct reply
   here from a plausible one?*
2. **Write the case skeleton** — the four methods below. `make check` is red
   until the census line and the case agree; that is the intended state.
3. **Drive it offline with canned replies.** Write `certcase_<site>_test.go`
   beside the case with a stub completer that answers with a fixed string, and
   run `Prepare → Run → Evaluate` against it. Assert the outcome word for a
   correct reply, for each way the validator can refuse one, and for the
   well-formed-but-wrong answer. This is the test that pins the case, it needs no
   network, and it is where a case's bugs are cheap to find:

   ```go
   type letterheadStub struct{ answer string }

   func (s *letterheadStub) Complete(context.Context, model.Request) (model.Response, error) {
       return model.Response{Text: s.answer}, nil
   }

   prepared, err := letterheadCases{}.Prepare(fixture, expected)
   // …
   trace, err := prepared.Run(context.Background(), &letterheadStub{answer: wellFormedButWrong})
   if got := prepared.Evaluate(trace); got.Result != aitasks.OutcomeWrongAnswer {
       t.Errorf("a well-formed wrong answer reports %q, want wrong_answer", got.Result)
   }
   ```

4. **`make check`.** The corpus gates now run your scenario against your case
   without a model: `TestEveryCorpusScenarioPreparesAgainstItsSite` catches a
   fixture of the wrong shape or an expectation the validator could never
   satisfy, and `TestEachAbstentionScenarioCatchesTheFabricationItTargets` runs
   every abstention scenario twice — once with the answer it calls correct and
   once with the fabrication it exists to refuse — because a scenario that passes
   whatever the model does is worse than no scenario.
5. **Then spend**: `make e2e-ai TASK=<task>`, and read the band with
   `make e2e-ai-report`.

## The interface

`aitasks.CaseFactory` (`internal/compose/aitasks/case.go`), one implementation
per site:

| Method | What it owes |
|---|---|
| `Site()` | the same task / variant / kind the census line claims — a disagreement is reported, not silently resolved |
| `Prepare(fixture, expected)` | parse the fixture into the shape **production** is handed, refuse an expectation this site's validator could never satisfy, and return a `PreparedCase` closed over both |
| `Run(ctx, completer)` | issue the production invocation, and return every request issued in the `Trace` |
| `Evaluate(trace)` | apply the production validator, then compare against the expectation, and report an `Outcome` |
| `CertifiedScope()` *(optional)* | narrow the claim when a run covers less than the site does |

Two rules that pay for themselves:

- **Refuse an unreachable expectation in `Prepare`.** A label outside the closed
  set, a count that cannot match, a fixture longer than the read truncates —
  naming it here costs a parse; finding it after a paid run costs money and
  leaves a band that measured nothing.
- **`Prepare` takes the fixture and the expectation separately.** The fixture is
  what production is given; the expectation is what the corpus asserts about the
  reply. Folding them into one blob lets any gate that rewrites a fixture rewrite
  an assertion by accident.

## By kind

The kind the contract declares decides what `Run` can honestly do.

### `one_shot` — build a request, read a reply

The majority. `Run` builds the production request and makes one call:

```go
func (c *classifyCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
    req := classifyRequest(c.batch)                    // the production builder
    trace := aitasks.Trace{Requests: []model.Request{req}}
    resp, err := completer.Complete(ctx, req)
    if err != nil {
        return trace, fmt.Errorf("capture_classify/classify: %w", err)
    }
    trace.Output = resp.Text
    return trace, nil
}
```

`Evaluate` then runs the engine's own checks in the engine's own order — parse,
production validator, and only then the comparison against the expectation. The
order is the meaning: a reply the validator refused has no answer to disagree
with.

**Send it bare.** Production may wrap the same request in a shape-retry, or re-ask
a below-floor item on the next rung; a case that did either would certify the
answer a model gives *after being told to try again* rather than the answer it
gives. Every case declines that one, identically. Reference:
`certcase_captureclassify.go`.

### `multi_turn` — one turn inside a supplied conversation

The fixture carries the conversation the caller would have built (`history`, the
incoming `message`, whatever context the turn is assembled from), and `Run`
builds the request for **that one turn**:

```go
req, err := onboardingCompanyAnswerRequest(c.message, c.history, c.conversation, c.locale, c.selection)
```

The surrounding conversation is *supplied, not exercised*, which is exactly what
`single_turn` scope says. Derive the validator's gate in `Prepare` from the same
message/history/context the request is built from — that is the whole reason
`Prepare` exists. If your validator cannot see the same data the fixture supplies,
it is a looser check than the real one — it will pass replies production would
reject, while still claiming to stand for it. Reference: `certcase_companymessage.go`, whose
scenario turns on `next_required_field` — the same bare reply in a conversation
that asked nothing would be a change nobody requested.

### `agent_loop` — a cumulative, tool-fed window

There is no single buildable request, and forcing one would make the case lie
about what it exercises. Instead `Run` drives the **real loop** with a recording
brain, and `Evaluate` replays the reply through the same loop to see which step
it took:

```go
recorder := &agentLoopRecorder{completer: completer}
job := c.job
job.Budget = agentLoopTurnBudget()          // one turn, so the run stays measurable
_, err := runner.New(agentLoopToolSurface{specs: c.specs}, recorder).Run(ctx, job)
trace := aitasks.Trace{Requests: recorder.requests}
// … recorder.failed (the call never completed) and err (the run never reached a
// reply) are distinct failures and reported separately …
trace.Output = recorder.reply
```

The expectation is the step the turn should take, and `Prepare` refuses one no
tool in the fixture's surface could produce. Reference: `certcase_agentloop.go`.

## The scenario file

`internal/compose/aicert/corpus/<task>/<name>.yaml`:

```yaml
name: meeting_request_from_reply
task: capture_classify
site: classify                     # which registered site is under certification
source: hand_authored              # must be this — `extracted:` is refused
sanitized_by: hand_authored/<who>  # who reviewed it for sensitive content
fixture:                           # the DATA production is given — never a prompt
  - subject: 'Re: pricing walkthrough'
    body: |
      Could we grab 30 minutes Thursday afternoon?
expect:
  outcome: accepted                # accepted | wrong_answer | invalid | abstained
  answer: [meeting]                # in the SITE's vocabulary — read its Prepare
  rubric: >
    What the grader is told to score, and why it matters to the product.
  bands: {certified_min: 70, degraded_min: 50, floor: 40}   # required
  caps: {max_tokens: 400, p95_latency_ms: 6000}             # optional ceilings
```

Field-by-field reference, including what each one is validated against:
[explanation/ai-runtime.md § Every field in a scenario file](../explanation/ai-runtime.md#every-field-in-a-scenario-file).
The rules that decide whether a scenario is worth having:

- **A scenario holds the input, not the prompt.** The site's case builds the
  request. A scenario carrying a prompt certifies a copy — and could not
  reproduce the request anyway: the product mints a fresh, unguessable marker per
  call to fence untrusted data, so no fixed text can stand in for a real prompt.
- **`expect.caps` is a real gate, not documentation.** Breaching one fails the
  run exactly like a bad reply. `max_tokens` budgets the model's **answer**
  alone — not your fixture's input, which the model cannot shrink — so a
  rich-input scenario with a tight cap tests drafting within budget rather than
  prompt size.
- **`expect.answer` has no common shape.** A bare token, a list, a map, a
  `{min,max}` band: each site owns its vocabulary, because what separates a right
  answer from a wrong one differs per site. Read that site's `Prepare` before
  authoring one.
- **`expect.outcome` need not be `accepted`.** A run passes when the site's
  validator reports the outcome the scenario named — which is what lets a
  scenario whose right answer is *silence* exist at all.
- **A rubric may only ask for what the site's reply envelope can carry.** A
  rubric scoring a field the schema does not declare measures nothing: the model
  cannot produce it however well it answers, so the clause can only mark a
  correct reply down. Read the request builder and the answer schema first.
- **Fixtures are synthetic.** No real company, deal or person data under this
  tree.

Aim a scenario at one thing that can go wrong. The scenarios that have earned
their place are the ones with an adversarial edge — an injected instruction
inside evidence, a page that grounds nothing, two sources that disagree on a
price, a tight token cap — because a fixture the model handles trivially reports
a band nobody learns from.

## Scope: what a run may claim

A record names how much of the site the run covered, from most to least:

| Scope | Means |
|---|---|
| `full_invocation` | the run drives the whole production invocation — certifying it certifies the site |
| `single_turn` | the window is seeded and one reply graded; the surrounding conversation or tool loop is supplied, not exercised |
| `single_call` | the run makes ONE of the calls the site makes per invocation — the answer production serves is assembled from calls the run never made, and the fold that assembles them is unmeasured too |

Scope defaults from the kind (`one_shot` → `full_invocation`, otherwise
`single_turn`), and a case may only ever **narrow** it — widening is refused by
`TestOnlyTheCasesThatMeasureLessNarrowWhatTheyCertify`. Declare `single_call`
when the site re-asks a below-floor item, asks again after an unreadable answer,
or fans out over pages and merges the replies:

```go
func (captureClassifyCases) CertifiedScope() string { return aitasks.ScopeSingleCall }
```

A narrowing also needs its entry in that test's `narrowedSites` map with the
reason — the list costs an explanation to grow, which is what stops it becoming
the place unmeasured sites go to be forgotten. The model runtime's shape-retry is
deliberately *not* a reason: every case declines it identically, so a word true of
all nineteen sites would tell a reader nothing about any of them.

## When the band surprises you

Read the **payload trace** before touching the prompt. Every candidate and judge
call is dumped to `.tmp/aicert/*.jsonl` (on by default, gitignored,
post-secret-stripper), one object per call carrying `role`, `scenario`, `run`,
`call` and the request/response as the product would have captured them. The
typical find is not a quality problem but a reply the site's own validator
refuses — a paraphrased evidence snippet where the gate demands a verbatim span.

The run knobs (`MODEL=`, `RUNS=`, `MARGINCE_AI_ROUTING=`, `TRACE=`), the verdict
math, and how to read a record are all in
[certify-an-ai-model.md](certify-an-ai-model.md).

## The two gates unique to what you wrote here

Everything a case or scenario can get wrong is caught by
[add-an-ai-task.md § step 7](add-an-ai-task.md#steps), which lists the full
checklist. Two are worth knowing by name while authoring, because they read the
*content* of a scenario rather than its presence:

| Gate | Refuses |
|---|---|
| `TestEveryCorpusScenarioPreparesAgainstItsSite` | a fixture that is not the shape the site takes, or an expectation its validator could never satisfy — caught without a model, before a paid run |
| `TestEachAbstentionScenarioCatchesTheFabricationItTargets` | an abstention scenario that grades the right answer and the fabrication it exists to catch the same way, and so would pass whatever the model did |
