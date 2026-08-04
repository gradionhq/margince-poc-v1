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
	Request  model.Request  `json:"-"`
	Wire     probeRequest   `json:"request"`
	Response model.Response `json:"response"`
	Route    ai.RouteInfo   `json:"route"`
	Latency  time.Duration  `json:"latency_ns"`
	// Err is the completer's own failure. A call that never completed is the
	// lane's problem, not a measurement of the reply, so it is kept apart from
	// the outcome the validator reports.
	Err string `json:"error,omitempty"`
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

type probeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func wireRequest(req model.Request) probeRequest {
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
	call := probeCall{Request: req, Wire: wireRequest(req), Response: resp, Route: route, Latency: time.Since(started)}
	if err != nil {
		call.Err = err.Error()
	}
	c.calls = append(c.calls, call)
	return resp, err
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
		res.Failure = err.Error()
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
	res.Output = trace.Output
	if runErr != nil {
		// The calls recorded so far still ship: a failure on the third of four
		// calls is diagnosed from the two that worked.
		res.Failure = runErr.Error()
		return finishProbe(stdout, cfg, res, runErr)
	}
	outcome := prepared.Evaluate(trace)
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
		return fmt.Errorf("aitask run %s: %w", res.Site, failure)
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
