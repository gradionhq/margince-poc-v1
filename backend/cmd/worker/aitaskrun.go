// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The `aitask run` verb: drive one site's production certification case over
// operator-supplied input and keep everything the run touched.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// probeCall is one model call the case made, kept whole.
//
// aitasks.Trace carries the requests and the final output but no per-call
// usage, and widening that seam to serve a debug surface would change what the
// certification lane records. Recording here instead costs nothing and keeps
// the seam as the certification lane defines it.
type probeCall struct {
	// Request is the live request, kept for the report's sizing. It is not
	// serialized: model.Request carries a SecretStripper, an INTERFACE, which
	// marshals to an empty object and cannot be read back — a --json result
	// nothing can decode is not machine-readable. Wire carries the readable
	// projection instead.
	Request model.Request `json:"-"`
	// Wire is the request as it is ALLOWED to leave this process: projected to
	// serializable fields and then run through the SecretStripper. It is raw
	// JSON rather than a struct because stripping operates on the marshaled
	// document, and re-decoding it into a struct would only invite a later
	// edit to serialize the unstripped one by mistake.
	Wire json.RawMessage `json:"request"`
	// Response is the live reply, kept for the report's sizing and NOT
	// serialized: it carries ProviderMetadata, raw vendor JSON nothing has
	// stripped. Reply below is the projection that may be written down.
	Response model.Response `json:"-"`
	Reply    probeReply     `json:"response"`
	Route    ai.RouteInfo   `json:"route"`
	Latency  time.Duration  `json:"latency_ns"`
	// Err is the COMPLETER's own failure — a call that never completed is the
	// lane's problem, not a measurement of the reply.
	Err string `json:"error,omitempty"`
	// RedactionErr is a failure of this probe's own credential pass, kept apart
	// from Err: a stripper that could not run is a harness problem, and
	// rendering it as a failed model call would misattribute it to the provider.
	RedactionErr string `json:"redaction_error,omitempty"`
}

// probeRequest is the serializable projection of one request: everything a
// prompt edit is diffed against, and nothing that cannot be read back.
type probeRequest struct {
	System         string          `json:"system"`
	Messages       []probeMessage  `json:"messages"`
	MaxTokens      int             `json:"max_tokens"`
	ResponseSchema json.RawMessage `json:"response_schema,omitempty"`
	Tools          []string        `json:"tools,omitempty"`
}

// probeReply is the serializable projection of one reply: the usage numbers and
// the stripped text, and nothing a vendor put there that nothing has stripped.
type probeReply struct {
	Text         string `json:"text"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	ServedModel  string `json:"served_model,omitempty"`
}

type probeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// stripRequest projects a request to what may be written down and runs the
// credential pass over it.
//
// This is NOT free: model.Request carries a SecretStripper but nothing has run
// it yet — stripping happens inside each provider adapter as it marshals its
// own wire payload, and it never mutates the request. So a dump built straight
// from req would be PRE-strip, whatever a comment claimed. A site that declares
// no stripper still gets one here rather than an unstripped dump.
func stripRequest(ctx context.Context, req model.Request) (json.RawMessage, error) {
	doc, err := json.Marshal(projectRequest(req))
	if err != nil {
		return nil, fmt.Errorf("aitask: rendering the request: %w", err)
	}
	return stripDoc(ctx, req.SecretStripper, doc)
}

func stripDoc(ctx context.Context, stripper model.SecretStripper, doc []byte) (json.RawMessage, error) {
	if stripper == nil {
		stripper = ai.NewSecretStripper()
	}
	stripped, _, err := stripper.Strip(ctx, doc)
	if err != nil {
		return nil, fmt.Errorf("aitask: stripping credentials: %w", err)
	}
	return json.RawMessage(stripped), nil
}

// redact is the best-effort credential pass for a message the probe is about to
// write down. Unlike the request and reply paths it cannot refuse to emit —
// these strings ARE the diagnosis — so a stripper that fails yields the empty
// string plus a marker rather than the unstripped original.
func redact(ctx context.Context, text string) string {
	out, err := stripText(ctx, nil, text)
	if err != nil {
		return "(withheld: the credential pass could not run over this message)"
	}
	return out
}

// stripText runs the credential pass over a bare string, returning what may be
// written down in its place.
func stripText(ctx context.Context, stripper model.SecretStripper, text string) (string, error) {
	if text == "" {
		return "", nil
	}
	doc, err := json.Marshal(text)
	if err != nil {
		return "", fmt.Errorf("aitask: rendering text: %w", err)
	}
	stripped, err := stripDoc(ctx, stripper, doc)
	if err != nil {
		return "", err
	}
	var out string
	if err := json.Unmarshal(stripped, &out); err != nil {
		return "", fmt.Errorf("aitask: reading stripped text: %w", err)
	}
	return out, nil
}

func projectRequest(req model.Request) probeRequest {
	out := probeRequest{
		System:         req.System,
		MaxTokens:      req.MaxTokens,
		ResponseSchema: json.RawMessage(req.ResponseSchema),
		Messages:       make([]probeMessage, 0, len(req.Messages)),
	}
	for _, msg := range req.Messages {
		out.Messages = append(out.Messages, probeMessage{Role: msg.Role, Content: msg.Content})
	}
	// The tool NAMES, not their schemas: an agent-loop turn is explained by
	// which tools it could reach, and the schemas would dwarf the rest.
	for _, tool := range req.Tools {
		out.Tools = append(out.Tools, tool.Name)
	}
	return out
}

// recordingCompleter wraps the completer the case reasons through and keeps
// every call. It never alters a request or a reply: what the site sent is what
// the report shows.
type recordingCompleter struct {
	inner compose.TaskProbeCompleter
	calls []probeCall
}

func (c *recordingCompleter) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	started := time.Now()
	resp, route, err := c.inner(ctx, req)
	call := probeCall{Request: req, Response: resp, Route: route, Latency: time.Since(started)}
	if err != nil {
		call.Err = err.Error()
	}
	// A request that cannot be stripped is not written down at all: a dump is a
	// convenience, and no convenience is worth emitting a credential. The
	// failure is recorded on the call so it is visible rather than silent.
	call.Reply = probeReply{
		InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens, ServedModel: resp.ServedModel,
	}
	wire, stripErr := stripRequest(ctx, req)
	if stripErr != nil {
		call.RedactionErr = stripErr.Error()
	} else {
		call.Wire = wire
	}
	if text, err := stripText(ctx, req.SecretStripper, resp.Text); err == nil {
		call.Reply.Text = text
	} else {
		call.RedactionErr = strings.TrimSpace(call.RedactionErr + " " + err.Error())
	}
	c.calls = append(c.calls, call)
	// The caller gets the UNALTERED reply: stripping guards what is written
	// down, never what the case under probe reasons about.
	return resp, err
}

// stripper is the credential pass the probed site declared, taken from the last
// request it actually issued. Nil when no call was made or the site declares
// none, which stripText reads as "use the default rules".
//
//nolint:ireturn // SecretStripper IS the seam: the site chooses the implementation and this only carries it.
func (c *recordingCompleter) stripper() model.SecretStripper {
	for i := len(c.calls) - 1; i >= 0; i-- {
		if s := c.calls[i].Request.SecretStripper; s != nil {
			return s
		}
	}
	return nil
}

// probeResult is one probe, whole: what was asked, what each call did, and what
// the production validator made of the reply.
type probeResult struct {
	Site    string `json:"site"`
	Kind    string `json:"kind"`
	Scope   string `json:"scope"`
	Ladder  string `json:"ladder"`
	Binding string `json:"binding"`
	// ContextCaveat names the company context this lane could not assemble, or
	// says the site declares none. It is never empty: a probe that silently
	// omitted it would read as more coverage than it bought.
	ContextCaveat string `json:"context_caveat"`
	FixtureBytes  int    `json:"fixture_bytes"`
	// HasExpectation is false when the run supplied no expected answer, which
	// is what makes a "wrong_answer" impossible rather than absent.
	HasExpectation bool             `json:"has_expectation"`
	Calls          []probeCall      `json:"calls"`
	Outcome        *aitasks.Outcome `json:"outcome,omitempty"`
	Output         string           `json:"output,omitempty"`
	// Failure is a harness failure — a refused fixture, a dead model — as
	// distinct from an outcome, which is a measurement.
	Failure string `json:"failure,omitempty"`
}

// probeInput is the two halves a case is prepared from, plus the site they
// belong to. They arrive separately because they are different kinds of thing:
// the fixture is what production is given, the expectation is what the operator
// asserts about the reply.
type probeInput struct {
	site     aitasks.Site
	fixture  json.RawMessage
	expected json.RawMessage
}

func runProbe(ctx context.Context, stdout io.Writer, census *aitasks.Registry, cfg aiTaskFlags) error {
	in, err := loadProbeInput(census, cfg)
	if err != nil {
		return err
	}
	factory, ok := census.CaseFor(in.site.Task, in.site.Variant)
	if !ok {
		return fmt.Errorf("aitask: %s registers no certification case, so there is no production code to probe it with", siteKey(in.site))
	}

	res := probeResult{
		Site:           siteKey(in.site),
		Kind:           in.site.Kind,
		Scope:          in.site.CertifiedScope(),
		Ladder:         ladderOf(in.site.Task),
		ContextCaveat:  contextCaveat(in.site.Task),
		FixtureBytes:   len(in.fixture),
		HasExpectation: len(in.expected) > 0,
	}

	complete, binding, err := probeCompleter(cfg, in.site.Task)
	if err != nil {
		return err
	}
	res.Binding = binding

	prepared, err := factory.Prepare(in.fixture, in.expected)
	if err != nil {
		// The case's own refusal is passed through verbatim — it already names
		// what it wanted. The one line added is the one the case cannot know:
		// that an expectation was never supplied.
		// Prepare is handed the RAW fixture, so a case that quotes what it
		// rejected would put operator content — and any credential in it —
		// straight into stdout and the --json artifact.
		res.Failure = redact(ctx, err.Error())
		if !res.HasExpectation {
			res.Failure += "\n          (no expectation was supplied; this site validates one — use --expect or --scenario)"
		}
		return finishProbe(stdout, cfg, res, err)
	}

	recorder := &recordingCompleter{inner: complete}
	// The router refuses a call outside a workspace context. This lane has no
	// database to resolve a real one from, so it mints a fixed DB-less id — the
	// same thing the certification runner and the siteread debug lane do.
	trace, runErr := prepared.Run(principal.WithWorkspaceID(ctx, ids.NewV7()), recorder)
	res.Calls = recorder.calls
	// The final output is written to --json like everything else, so it passes
	// the same credential gate the per-call replies do.
	// The site's own stripper, not the defaults: a site may declare rules the
	// default pass does not carry, and the final output must not be redacted
	// more weakly than the per-call replies it was assembled from.
	output, stripErr := stripText(ctx, recorder.stripper(), trace.Output)
	if stripErr != nil {
		// Reported like any other harness failure, and through the SAME exit —
		// a bare return here would discard the report, the json result and
		// every call already recorded.
		res.Failure = stripErr.Error() // the stripper's OWN failure carries no probed content
		return finishProbe(stdout, cfg, res, stripErr)
	}
	res.Output = output
	if runErr != nil {
		// The calls recorded so far still ship: a failure on the third of four
		// calls is diagnosed from the two that worked.
		res.Failure = redact(ctx, runErr.Error())
		return finishProbe(stdout, cfg, res, runErr)
	}
	outcome := prepared.Evaluate(trace)
	// Evaluate is handed the raw reply and the validators quote it, so the
	// detail is content too — it reaches stdout and --json like everything else.
	outcome.Detail = redact(ctx, outcome.Detail)
	res.Outcome = &outcome
	return finishProbe(stdout, cfg, res, nil)
}

// finishProbe renders the result every way the flags asked for, then reports
// whether the PROBE failed — never whether the model answered well. An
// unwelcome outcome is a measurement and exits 0; a refused fixture or a dead
// model is a failure and does not.
func finishProbe(stdout io.Writer, cfg aiTaskFlags, res probeResult, failure error) error {
	if err := writeProbeReport(stdout, res); err != nil {
		return err
	}
	if cfg.jsonPath != "" {
		if err := writeProbeJSON(stdout, cfg.jsonPath, res); err != nil {
			return err
		}
	}
	if cfg.dumpDir != "" {
		if err := dumpProbeRequests(cfg.dumpDir, res); err != nil {
			return err
		}
	}
	if failure != nil {
		// res.Failure, not the original: main prints the returned error to
		// stderr, so returning the raw one would undo every redaction above.
		return fmt.Errorf("aitask run %s: %s", res.Site, res.Failure)
	}
	return nil
}

// loadProbeInput reads the fixture and expectation from whichever spelling the
// flags chose, and binds them to a registered site.
func loadProbeInput(census *aitasks.Registry, cfg aiTaskFlags) (probeInput, error) {
	if cfg.scenarioPath != "" {
		return loadScenarioInput(census, cfg.scenarioPath)
	}
	site, err := resolveSite(census, cfg.siteRef())
	if err != nil {
		return probeInput{}, err
	}
	fixture, err := readJSONFile(cfg.fixturePath, "fixture")
	if err != nil {
		return probeInput{}, err
	}
	in := probeInput{site: site, fixture: fixture}
	if cfg.expectPath != "" {
		expected, err := readJSONFile(cfg.expectPath, "expectation")
		if err != nil {
			return probeInput{}, err
		}
		in.expected = expected
	}
	return in, nil
}

// loadScenarioInput reads one scenario file in the committed corpus format.
// It deliberately does NOT apply the corpus's admission rules — provenance
// (source, sanitized_by) gates what may ENTER the corpus, and a scratch
// scenario is not entering it. Everything that says what to RUN is still
// checked: the site must be one this build registers.
func loadScenarioInput(census *aitasks.Registry, path string) (probeInput, error) {
	sc, err := aicert.LoadScenarioFile(path, census)
	if err != nil {
		return probeInput{}, err
	}
	site, err := resolveSite(census, sc.Task+"/"+sc.Site)
	if err != nil {
		return probeInput{}, err
	}
	return probeInput{
		site:     site,
		fixture:  json.RawMessage(sc.Fixture),
		expected: json.RawMessage(sc.Expect.Answer),
	}, nil
}

func readJSONFile(path, what string) (json.RawMessage, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- an operator-named file is the whole point of the flag
	if err != nil {
		return nil, fmt.Errorf("aitask: reading the %s: %w", what, err)
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("aitask: %s is not valid JSON: %s", what, path)
	}
	return json.RawMessage(raw), nil
}

// probeCompleter binds the site's task to the model that will answer it.
//
// The router itself is built by compose, not here: backend/arch_test.go pins
// the model-path assembly seam to exactly two files, and a cmd/ process role
// constructing its own would be a third gate.
func probeCompleter(cfg aiTaskFlags, task ai.Task) (compose.TaskProbeCompleter, string, error) {
	complete, banner, err := compose.TaskProbeBrain(cfg.routingPath, cfg.modelSpec, cfg.fakeBrain, task)
	if err != nil {
		return nil, "", fmt.Errorf("aitask run: %w", err)
	}
	return complete, banner, nil
}

func ladderOf(task ai.Task) string {
	tiers := ai.TaskLadder(task)
	names := make([]string, 0, len(tiers))
	for _, tier := range tiers {
		names = append(names, string(tier))
	}
	return strings.Join(names, ",")
}

// contextCaveat states what this lane could not assemble. Production prepends a
// company-context block to some tasks; the probe has no database to build one
// from, so a site that declares one is being probed WITHOUT part of its real
// prompt. Saying so is not optional.
func contextCaveat(task ai.Task) string {
	policy, declared := ai.CompanyContextFor(task)
	if !declared || len(policy.Scopes) == 0 {
		return "company context not declared for this site"
	}
	return fmt.Sprintf("company context declared (%s) but NOT assembled — this lane has no database",
		strings.Join(policy.Scopes, ", "))
}

func writeProbeJSON(stdout io.Writer, path string, res probeResult) error {
	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fmt.Errorf("aitask: rendering the result: %w", err)
	}
	if path == "-" {
		_, err = fmt.Fprintf(stdout, "\n%s\n", out)
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

// dumpProbeRequests writes each request the case issued, post-SecretStripper,
// as its own file — the artifact a prompt edit is diffed against.
func dumpProbeRequests(dir string, res probeResult) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("aitask: %w", err)
	}
	for i, call := range res.Calls {
		if len(call.Wire) == 0 {
			// Nothing was safe to write; a "null" file would read as a dump.
			continue
		}
		out, err := json.MarshalIndent(call.Wire, "", "  ")
		if err != nil {
			return fmt.Errorf("aitask: rendering request %d: %w", i+1, err)
		}
		name := filepath.Join(dir, fmt.Sprintf("%s.%d.request.json", strings.ReplaceAll(res.Site, "/", "_"), i+1))
		if err := os.WriteFile(name, append(out, '\n'), 0o600); err != nil {
			return fmt.Errorf("aitask: %w", err)
		}
	}
	return nil
}
