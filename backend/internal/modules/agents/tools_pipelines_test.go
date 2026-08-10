// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// list_pipelines is thin over its seam, so what is worth pinning is the
// decisions around it: the tier and scope (it reads configuration), what an
// empty workspace answers, and that the answer never carries a JSON null where
// a caller was promised a list.

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

func TestListPipelinesIsReadTierAndNeedsOnlyReadScope(t *testing.T) {
	spec := listPipelinesTool{}.Spec()
	if spec.Tier != mcp.TierAutoExecute {
		t.Errorf("tier = %v, want auto-execute — it reads configuration and changes nothing", spec.Tier)
	}
	if spec.RequiredScope != principal.ScopeRead {
		t.Errorf("RequiredScope = %v, want read", spec.RequiredScope)
	}
	// The description is the whole reason the tool is discoverable: it is what
	// tells an agent that the two ids create_record refuses without are HERE.
	// A tool that answered correctly and did not say what it is for would leave
	// the dead end exactly where it was. It is read off Description rather than
	// off the input schema because the tool takes no arguments: there was
	// nowhere else to put this before the spec carried written copy.
	// Each name is looked for as a WORD. `stage_id` is a substring of
	// `to_stage_id`, so a contains-check would report the standalone argument as
	// named by a description that only ever mentions the other one.
	named := regexp.MustCompile(`[a-z_]+`).FindAllString(spec.Description, -1)
	for _, want := range []string{"pipeline_id", "stage_id", "to_stage_id", "semantic"} {
		if !slices.Contains(named, want) {
			t.Errorf("the description names no %s — an agent reading tools/list cannot tell "+
				"this is the tool that unblocks the deal verbs", want)
		}
	}
}

func TestAnAbsentPipelineSeamRegistersNoTool(t *testing.T) {
	// A role wired without the seam must not advertise a tool that always errors.
	r := NewRegistry(nil, nil)
	RegisterPipelineTool(r, nil)
	if _, found := r.Spec("list_pipelines"); found {
		t.Error("list_pipelines registered with no seam behind it")
	}
}

func TestAWorkspaceWithNoPipelinesAnswersAnEmptyListNotNull(t *testing.T) {
	// "This workspace has no pipelines configured" is a true and useful answer —
	// it is the answer that says the deal verbs cannot be used yet. A JSON null
	// reads to a model as "unknown", which is a different claim.
	tool := listPipelinesTool{list: func(context.Context) ([]Pipeline, error) { return nil, nil }}
	out, err := tool.Handle(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("an unconfigured workspace answered an error: %v", err)
	}
	if !strings.Contains(string(out), `"pipelines":[]`) {
		t.Errorf("payload = %s, want an empty pipelines array", out)
	}
}

func TestAPipelineWithNoLiveStagesAnswersAnEmptyStageListNotNull(t *testing.T) {
	// The case a caller most needs to see as "none": a pipeline whose stages are
	// all archived cannot host a deal, and `"stages":null` would leave a model
	// unsure whether it simply had not been told.
	tool := listPipelinesTool{list: func(context.Context) ([]Pipeline, error) {
		return []Pipeline{{ID: ids.NewV7(), Name: "Sales", IsDefault: true}}, nil
	}}
	out, err := tool.Handle(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(string(out), `"stages":[]`) {
		t.Errorf("payload = %s, want an empty stages array on the stageless pipeline", out)
	}
}

func TestListPipelinesRefusesAnUnknownArgument(t *testing.T) {
	// The seam is never reached with arguments the tool does not take: a filter
	// silently ignored would let a caller believe it had narrowed the answer.
	reached := false
	tool := listPipelinesTool{list: func(context.Context) ([]Pipeline, error) {
		reached = true
		return nil, nil
	}}
	if _, err := tool.Handle(context.Background(), json.RawMessage(`{"pipeline_id":"x"}`)); err == nil {
		t.Error("an unknown argument was accepted")
	}
	if reached {
		t.Error("the seam was reached despite an unknown argument")
	}
}

// A tool that REQUIRES a pipeline or stage id must say where one comes from.
//
// This is the defect U2 closes, stated as an invariant rather than as three
// fixes: create_record for a deal and both stage-move tools named the ids they
// needed and nothing on the surface yielded one, so a correct refusal was a dead
// end. Derived from the registry, so a tool added later that takes a stage id
// inherits the obligation instead of quietly reopening the dead end.
func TestEveryToolNeedingAPipelineOrStageIDPointsAtListPipelines(t *testing.T) {
	const source = "list_pipelines"
	checked := 0
	for _, spec := range fullRegistry(t).Specs() {
		// The source itself owes no pointer to itself.
		if spec.Name == source {
			continue
		}
		schemaText := string(spec.InputSchema)
		if !strings.Contains(schemaText, "stage_id") && !strings.Contains(schemaText, "pipeline_id") {
			continue
		}
		checked++
		if !strings.Contains(schemaText, source) {
			t.Errorf("%s takes a pipeline or stage id and never names %s — a caller is told what it "+
				"needs with nowhere to get it, which is the dead end this tool exists to close", spec.Name, source)
		}
	}
	if checked == 0 {
		t.Fatal("no tool declares a pipeline or stage id — this walk passed vacuously, so either the " +
			"argument was renamed or the registry it reads is not the product's")
	}
}

func TestListPipelinesCarriesEveryStageFieldACallerNeeds(t *testing.T) {
	// The four fields the tool exists to deliver. `semantic` is the load-bearing
	// one — it is what says which stage a deal is born into and which two close
	// it — and `win_probability` and `position` are what let a caller choose
	// between open stages without reading their names.
	stage := Stage{ID: ids.NewV7(), Name: "Discovery", Semantic: "open", WinProbability: 25, Position: 2}
	tool := listPipelinesTool{list: func(context.Context) ([]Pipeline, error) {
		return []Pipeline{{ID: ids.NewV7(), Name: "Sales", Position: 0, Stages: []Stage{stage}}}, nil
	}}
	out, err := tool.Handle(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var payload struct {
		Pipelines []struct {
			ID     ids.UUID `json:"id"`
			Stages []Stage  `json:"stages"`
		} `json:"pipelines"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unreadable payload: %v", err)
	}
	if len(payload.Pipelines) != 1 || len(payload.Pipelines[0].Stages) != 1 {
		t.Fatalf("payload = %s, want one pipeline carrying one stage", out)
	}
	if got := payload.Pipelines[0].Stages[0]; got != stage {
		t.Errorf("stage round-tripped as %+v, want %+v", got, stage)
	}
}
