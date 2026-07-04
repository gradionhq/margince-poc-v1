// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The run_report tool (interfaces.md §2.1, 🟢): reads aggregate rows
// through the compiled report engine. The engine lives above the
// modules (it queries across domain tables), so the composition root
// injects it here as a function — the tool owns the surface contract,
// never the SQL.

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// ReportRunner executes one named report with the request's plan
// arguments and returns the contract-shaped result JSON.
type ReportRunner func(ctx context.Context, report string, planArgs json.RawMessage) (json.RawMessage, error)

// RegisterReportTool joins run_report to the surface once the engine
// exists — the same conditional-registration pattern the other
// verb-gated tools follow.
func RegisterReportTool(r *Registry, run ReportRunner) {
	r.Register(runReport{run: run})
}

type runReport struct {
	run ReportRunner
}

func (t runReport) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "run_report", Version: "1.0.0",
		RequiredScope: principal.ScopeRead, Tier: mcp.TierGreen,
		OpenAPIOp: "runReport",
		InputSchema: schema(`{"type":"object","required":["report"],"properties":{
			"report":{"type":"string","description":"Prebuilt report key (e.g. open-deals-per-company, deals-by-stage, activities-by-kind)"},
			"filters":{"type":"object","description":"Typed predicates; keys must be in the report's vocabulary"},
			"group_by":{"type":"array","items":{"type":"string"}},
			"aggregates":{"type":"array","items":{"type":"object","required":["fn"],"properties":{
				"fn":{"type":"string","enum":["count","sum","avg","min","max"]},
				"field":{"type":"string"},"as":{"type":"string"}},"additionalProperties":false}}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t runReport) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Report string          `json:"report"`
		Rest   json.RawMessage `json:"-"`
	}
	if err := decodeReportArgs(in, &args.Report, &args.Rest); err != nil {
		return nil, err
	}
	return t.run(ctx, args.Report, args.Rest)
}

// decodeReportArgs pops the report key and forwards the remaining plan
// arguments verbatim — the engine validates the vocabulary.
func decodeReportArgs(in json.RawMessage, report *string, rest *json.RawMessage) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(in, &m); err != nil {
		return &BadArgsError{Cause: err}
	}
	raw, ok := m["report"]
	if !ok || json.Unmarshal(raw, report) != nil || *report == "" {
		return &BadArgsError{Cause: errMissingReport}
	}
	delete(m, "report")
	remaining, err := json.Marshal(m)
	if err != nil {
		return err
	}
	*rest = remaining
	return nil
}

var errMissingReport = jsonError("a report key is required")

type jsonError string

func (e jsonError) Error() string { return string(e) }
