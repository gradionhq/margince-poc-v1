// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Pipeline and stage discovery: the read that makes the deal-shaped write
// verbs usable at all.
//
// `create_record` for a deal REQUIRES pipeline_id and stage_id, and
// `advance_deal` requires to_stage_id — and until this tool existed no verb on
// the surface yielded one. A correct refusal into a dead end is still a dead
// end: an agent was told exactly which two ids it needed and had no way to
// obtain either.
//
// ONE tool, stages nested, not a list-pipelines plus a list-stages: a stage is
// meaningless without the pipeline that owns it, and two tools would force a
// join an agent has no reason to perform. 🟢 — it reads configuration and
// changes nothing.

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Pipeline is one pipeline with its live stages, in the shape a model
// consumes: the ids it needs to write, plus enough configuration to choose
// between them.
type Pipeline struct {
	ID   ids.UUID `json:"id"`
	Name string   `json:"name"`
	// IsDefault marks the workspace's default pipeline — the one to pick when
	// the caller has no reason to prefer another.
	IsDefault bool    `json:"is_default"`
	Position  int     `json:"position"`
	Stages    []Stage `json:"stages"`
}

// Stage is one stage of a pipeline. Semantic is the load-bearing field: it is
// what tells a caller which stage a deal is born into (open) and which two
// close it (won/lost), and it is what advance_deal's tier resolver keys off —
// so a name-based guess at "the first stage" is not a substitute for reading it.
type Stage struct {
	ID             ids.UUID `json:"id"`
	Name           string   `json:"name"`
	Semantic       string   `json:"semantic"`
	WinProbability int      `json:"win_probability"`
	Position       int      `json:"position"`
}

// listPipelinesAnswer is the tool's wire shape. Named rather than assembled as a
// map[string]any: the payload is fully typed already, so the map would be an
// untyped hop that also leaves the response shape undocumented.
type listPipelinesAnswer struct {
	Pipelines []Pipeline `json:"pipelines"`
}

// PipelineLister answers "what pipelines and stages does this workspace have".
// Compose implements it over the deals module's own row-scoped config reads, so
// the `pipeline` RBAC object gates this tool exactly as it gates the HTTP
// surface.
type PipelineLister func(ctx context.Context) ([]Pipeline, error)

// RegisterPipelineTool wires the config read behind the deal write verbs. A nil
// seam registers nothing: a surface that cannot ground its answer does not
// pretend to.
func RegisterPipelineTool(r *Registry, list PipelineLister) {
	if list == nil {
		return
	}
	r.Register(listPipelinesTool{list: list})
}

// --- list_pipelines (🟢 read) ---

type listPipelinesTool struct{ list PipelineLister }

func (t listPipelinesTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "list_pipelines", Title: "List pipelines and their stages", Version: toolVersionV1,
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "listPipelines/listStages",
		// No arguments. The whole config is small, bounded by how many pipelines
		// a workspace configures, and a filter would only let a caller ask for
		// the one it cannot name yet.
		InputSchema: schema(`{"type":"object","properties":{},"description":` +
			jsonString("Every pipeline with its live stages. Call this FIRST to obtain the "+
				"`pipeline_id` and `stage_id` that create_record for a deal requires, and the "+
				"`to_stage_id` that advance_deal requires — no other tool yields them. Read each "+
				"stage's `semantic`: `open` stages are where a deal is born and moves; `won` and "+
				"`lost` close it, and advancing onto one needs a human's approval. Do not infer a "+
				"stage's meaning from its name.") + `,"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t listPipelinesTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct{}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	pipelines, err := t.list(ctx)
	if err != nil {
		return nil, err
	}
	if pipelines == nil {
		// An empty LIST, not a null. A workspace with no pipeline configured is
		// a real state — it means the deal verbs cannot be used yet — and a model
		// handed null reads it as "unknown" and hedges instead of saying so.
		pipelines = []Pipeline{}
	}
	// Every pipeline's stages normalize too: a pipeline with no live stage is
	// the case a caller most needs to see as "none", because it is the one that
	// makes create_record for a deal impossible.
	for i := range pipelines {
		if pipelines[i].Stages == nil {
			pipelines[i].Stages = []Stage{}
		}
	}
	return json.Marshal(listPipelinesAnswer{Pipelines: pipelines})
}
