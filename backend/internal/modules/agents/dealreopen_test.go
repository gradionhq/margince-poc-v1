// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The defect, written before the fix.
//
// The tier resolver read the TARGET stage's semantic and nothing else, so a move
// ONTO an open stage was auto-execute whatever the deal was before. A won deal
// moved back to Proposal is a reopen: it clears closed_at, the lost reason and
// the FX rate frozen at close, and it takes revenue out of a reported quarter.
// That is the same money and the same irreversibility the 🟡 tier exists for,
// approached from the other end — and it ran ungated.
//
// Every existing test could only supply a target semantic, so all of them passed
// against this.
func TestReopeningAClosedDealNeedsAHumanJustAsClosingOneDoes(t *testing.T) {
	for name, in := range map[string]mcp.TierResolverInput{
		"a won deal moved back to an open stage":  {SourceStageSemantic: "won", TargetStageSemantic: "open"},
		"a lost deal moved back to an open stage": {SourceStageSemantic: "lost", TargetStageSemantic: "open"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := advanceDealTier(in); got != mcp.TierConfirmationRequired {
				t.Errorf("tier = %v, want confirm-first — reopening a closed deal clears its "+
					"closed_at, its lost reason and the FX rate frozen at close", got)
			}
		})
	}
}

// The rule is EITHER endpoint, so the two moves that were already gated stay
// gated and the one that was always free stays free. Without this the fix could
// pass by refusing everything, which would put a human in front of every routine
// stage move on the surface.
func TestOnlyMovesBetweenOpenStagesRunUnattended(t *testing.T) {
	for name, tc := range map[string]struct {
		in   mcp.TierResolverInput
		want mcp.RiskTier
	}{
		"open to open is the routine move":  {mcp.TierResolverInput{SourceStageSemantic: "open", TargetStageSemantic: "open"}, mcp.TierAutoExecute},
		"open to won closes the deal":       {mcp.TierResolverInput{SourceStageSemantic: "open", TargetStageSemantic: "won"}, mcp.TierConfirmationRequired},
		"open to lost closes the deal":      {mcp.TierResolverInput{SourceStageSemantic: "open", TargetStageSemantic: "lost"}, mcp.TierConfirmationRequired},
		"won to lost rewrites the outcome":  {mcp.TierResolverInput{SourceStageSemantic: "won", TargetStageSemantic: "lost"}, mcp.TierConfirmationRequired},
		"an unreadable source raises":       {mcp.TierResolverInput{SourceStageSemantic: "", TargetStageSemantic: "open"}, mcp.TierConfirmationRequired},
		"an unreadable target still raises": {mcp.TierResolverInput{SourceStageSemantic: "open", TargetStageSemantic: "archived"}, mcp.TierConfirmationRequired},
	} {
		t.Run(name, func(t *testing.T) {
			if got := advanceDealTier(tc.in); got != tc.want {
				t.Errorf("tier = %v, want %v", got, tc.want)
			}
		})
	}
}

// The resolver can only decide what ResolverInput hands it, so the deal's
// CURRENT stage has to be read and passed. Both dynamic tools share the
// resolver, and this is the half that feeds it.
func TestResolverInputCarriesTheDealsCurrentStageSemantic(t *testing.T) {
	deal := ids.NewV7()
	target := ids.NewV7()
	provider := &reopenProbeProvider{stageID: ids.NewV7()}
	stages := &reopenProbeStages{semantics: map[ids.UUID]string{
		provider.stageID: "won",
		target:           "open",
	}}
	for name, tool := range map[string]dynamicTool{
		"advance_deal":  advanceDeal{p: provider, stages: stages},
		"progress_deal": progressDeal{p: provider, stages: stages},
	} {
		t.Run(name, func(t *testing.T) {
			in, err := tool.ResolverInput(context.Background(),
				[]byte(`{"deal_id":"`+deal.String()+`","to_stage_id":"`+target.String()+`"}`))
			if err != nil {
				t.Fatalf("resolving: %v", err)
			}
			if in.SourceStageSemantic != "won" {
				t.Errorf("source semantic = %q, want the deal's current stage — without it the "+
					"resolver cannot see a reopen", in.SourceStageSemantic)
			}
			if in.TargetStageSemantic != "open" {
				t.Errorf("target semantic = %q, want the stage being moved to", in.TargetStageSemantic)
			}
		})
	}
}

// reopenProbeProvider answers a deal sitting in one known stage, so a resolver
// test can say what the deal WAS without a database.
type reopenProbeProvider struct {
	datasource.SystemOfRecordProvider
	stageID ids.UUID
	// fields overrides the record body, so a test can hand back a deal whose
	// stage_id is not readable as an id.
	fields  []byte
	readErr error
}

func (p *reopenProbeProvider) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	if p.readErr != nil {
		return datasource.Record{}, p.readErr
	}
	fields := p.fields
	if fields == nil {
		fields = []byte(`{"stage_id":"` + p.stageID.String() + `"}`)
	}
	return datasource.Record{
		Ref:    datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()},
		Fields: fields,
	}, nil
}

// reopenProbeStages resolves each stage id to the semantic the test gave it, so
// the source and the target can differ — which is the whole point.
type reopenProbeStages struct {
	semantics map[ids.UUID]string
	pipeline  ids.UUID
	// failFor names the stages this resolver cannot answer for, so a test can
	// fail the SOURCE lookup while the target still resolves — which is the only
	// way to reach a path that runs second.
	failFor map[ids.UUID]bool
}

func (s *reopenProbeStages) StageSemantic(_ context.Context, stageID ids.UUID) (string, ids.UUID, error) {
	if s.failFor[stageID] {
		return "", ids.UUID{}, errors.New("stage lookup failed")
	}
	return s.semantics[stageID], s.pipeline, nil
}

// The three ways reading the deal's current stage can fail. Each is reported
// rather than swallowed: the resolver would treat an empty semantic as
// not-open and raise, which is SAFE — but it would ask a human to approve a
// move against a deal this server could not read, and the human would have no
// way to know that is what they were being asked.
func TestAnUnreadableCurrentStageIsReportedRatherThanRaisedSilently(t *testing.T) {
	target, currentStage := ids.NewV7(), ids.NewV7()
	args := []byte(`{"deal_id":"` + ids.NewV7().String() + `","to_stage_id":"` + target.String() + `"}`)

	for name, tool := range map[string]dynamicTool{
		"the deal cannot be read": advanceDeal{
			p:      &reopenProbeProvider{readErr: errors.New("deal.read: permission denied")},
			stages: &reopenProbeStages{semantics: map[ids.UUID]string{target: "open"}},
		},
		"the deal has no readable stage": advanceDeal{
			p:      &reopenProbeProvider{fields: []byte(`{"stage_id":42}`)},
			stages: &reopenProbeStages{semantics: map[ids.UUID]string{target: "open"}},
		},
		"the deal's CURRENT stage has no resolvable semantic": advanceDeal{
			p: &reopenProbeProvider{stageID: currentStage},
			// The target resolves, the SOURCE does not — an error on every call
			// would have fired on the target lookup, which runs first, and this
			// case would have passed without reaching what it is named for.
			stages: &reopenProbeStages{
				semantics: map[ids.UUID]string{target: "open"},
				failFor:   map[ids.UUID]bool{currentStage: true},
			},
		},
		"the deal has no stage at all": advanceDeal{
			p:      &reopenProbeProvider{fields: []byte(`{}`)},
			stages: &reopenProbeStages{semantics: map[ids.UUID]string{target: "open"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.ResolverInput(context.Background(), args); err == nil {
				t.Error("the tier was resolved against a deal whose current stage could not be read")
			}
		})
	}
}

// The sentence a human is asked to approve names the move by BOTH endpoints,
// because the two that reach this gate are opposites. "Close deal Acme as open"
// is what a reopen used to read as — a summary that describes neither what is
// happening nor what it costs, to someone whose whole view of the decision is
// that line.
func TestTheApprovalSummaryNamesTheMoveItActuallyIs(t *testing.T) {
	current, target := ids.NewV7(), ids.NewV7()
	stages := &reopenProbeStages{semantics: map[ids.UUID]string{target: "open"}}
	record := func(stage ids.UUID) datasource.Record {
		return datasource.Record{Fields: []byte(`{"name":"Acme renewal","stage_id":"` + stage.String() + `"}`)}
	}

	for name, tc := range map[string]struct {
		source, target, want string
	}{
		"a reopen says so, and says what it costs": {"won", "open", "REOPEN"},
		"a close still reads as a close":           {"open", "won", "Close deal"},
		"one outcome rewritten as another":         {"won", "lost", "Change deal"},
		// Reachable even though the tier gate calls it 🟢: a per-field precedence
		// split stages an otherwise auto-execute call, and the human then reads
		// this line about a routine move.
		"a routine move is not a close": {"open", "open", "another open stage"},
	} {
		t.Run(name, func(t *testing.T) {
			stages.semantics[current] = tc.source
			stages.semantics[target] = tc.target
			got := dealMoveSummary(context.Background(), stages, record(current), tc.target)
			if !strings.Contains(got, tc.want) {
				t.Errorf("summary = %q, want it to name the act (%q)", got, tc.want)
			}
		})
	}
	// The table above leaves the map on its last case, so the reopen is set up
	// again explicitly rather than inherited from whichever ran last.
	stages.semantics[current], stages.semantics[target] = "won", "open"
	if got := dealMoveSummary(context.Background(), stages, record(current), "open"); !strings.Contains(got, "close date") {
		t.Errorf("summary = %q, want a reopen to say what approving costs", got)
	}
	// A stage it cannot resolve degrades to naming the destination rather than
	// failing — the approval is already the safe answer.
	blank := datasource.Record{Fields: []byte(`{"name":"Acme renewal"}`)}
	if got := dealMoveSummary(context.Background(), stages, blank, "open"); !strings.HasPrefix(got, "Move deal") {
		t.Errorf("summary = %q, want the destination named when the source cannot be read", got)
	}
}
