// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The coverage vocabulary is DECLARED twice — once by the executor that decides
// it and once by the tool that publishes it — because neither package may
// import the other. This is the one place that can see both, so this is where
// the two are held to being the same three words.
//
// A divergence would not fail anywhere else: the tool refuses a class it does
// not publish, so the mismatch reaches production as an outage rather than as
// the rename or addition it is.
//
// The two SETS are compared, not three hand-listed pairs. A pair-by-pair check
// catches a renamed class and misses an ADDED one — and an added executor class
// is exactly the case this test's own reason for existing describes.
func TestTheSurfaceAndTheExecutorAgreeOnCoverage(t *testing.T) {
	executor := slices.Sorted(slices.Values(search.CoverageClasses()))
	published := slices.Sorted(slices.Values([]string{
		agents.CoverageCompleteExact, agents.CoverageRankedSemantic, agents.CoveragePartialDegraded,
	}))
	if !slices.Equal(executor, published) {
		t.Errorf("the executor answers %v and query_workspace publishes %v; a class in one and not the "+
			"other is a refused call at runtime", executor, published)
	}
}

// The mode guard is the reason this tool is composed here rather than
// registered next to its executor: the plan runs against the NATIVE tables, and
// an overlay workspace has no rows in them. A well-formed empty answer is the
// silent break ADR-0018 forbids, so the refusal is the declared one.
func TestAnOverlayWorkspaceIsRefusedRatherThanAnsweredFromNativeTables(t *testing.T) {
	reached := false
	guarded := nativeOnlyQueryRunner(stubOverlayMode{overlay: true}, func(context.Context, json.RawMessage) (agents.QueryAnswer, error) {
		reached = true
		return agents.QueryAnswer{}, nil
	})

	_, err := guarded(t.Context(), json.RawMessage(`{"version":"v1","target":"deal"}`))
	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("err = %v, want the declared unsupported-by-SoR refusal", err)
	}
	if reached {
		t.Error("the executor ran for an overlay workspace, against tables holding none of its records")
	}
}

// An unresolved mode refuses. Defaulting to native would answer an overlay
// workspace from the wrong tables on exactly the request whose mode nobody
// could read — the case the guard exists for.
func TestAnUnreadableModeRefusesRatherThanAssumingNative(t *testing.T) {
	failed := errors.New("resolving the workspace mode")
	guarded := nativeOnlyQueryRunner(stubOverlayMode{err: failed}, func(context.Context, json.RawMessage) (agents.QueryAnswer, error) {
		t.Error("the executor ran without the mode having been resolved")
		return agents.QueryAnswer{}, nil
	})

	if _, err := guarded(t.Context(), json.RawMessage(`{}`)); !errors.Is(err, failed) {
		t.Errorf("err = %v, want the mode read's own failure", err)
	}
}

// The executor answers refs and the tool answers records, so everything that
// justifies a row has to survive the crossing. Evidence is the part that would
// be missed: a hop dropped here is a filter the caller can no longer see.
func TestTheSeamCarriesEveryReasonARowWasAdmitted(t *testing.T) {
	deal, org := ids.NewV7(), ids.NewV7()
	answer := queryAnswerOf(search.QueryResult{
		Rows: []search.QueryRow{{
			Type: "deal", ID: deal, Title: "Retrofit line", Score: 0.42,
			Evidence: []search.QueryEvidence{{
				Relation: "organization_id", Type: "organization", ID: org, Title: "Kärcher",
			}},
		}},
		Coverage:  search.CoverageRankedSemantic,
		Notes:     []search.QueryNote{{Code: search.CodeResultTruncated, Path: "limit", Detail: "more match"}},
		Narrative: "Deals at an organization in Stuttgart.",
		Limit:     25,
	})

	if len(answer.Refs) != 1 || answer.Refs[0].ID != deal || answer.Refs[0].Score != 0.42 {
		t.Fatalf("refs = %+v, want the executor's row with its score", answer.Refs)
	}
	evidence := answer.Refs[0].Evidence
	if len(evidence) != 1 || evidence[0].ID != org || evidence[0].RecordType != "organization" ||
		evidence[0].Relation != "organization_id" || evidence[0].Title != "Kärcher" {
		t.Errorf("evidence = %+v, want the hop record that admitted the row", evidence)
	}
	if len(answer.Notes) != 1 || answer.Notes[0].Code != search.CodeResultTruncated ||
		answer.Notes[0].Path != "limit" {
		t.Errorf("notes = %+v, want the executor's own note with its path", answer.Notes)
	}
	if answer.Coverage != search.CoverageRankedSemantic || answer.Narrative != "Deals at an organization in Stuttgart." {
		t.Errorf("coverage/narrative = %q/%q, want them carried verbatim", answer.Coverage, answer.Narrative)
	}
}

// An empty result crosses as empty LISTS, not nils. The tool marshals what it
// is handed, and a nil there would put `null` on the wire where a client reads
// "not computed".
func TestAnEmptyResultCrossesAsEmptyListsRatherThanNils(t *testing.T) {
	answer := queryAnswerOf(search.QueryResult{Coverage: search.CoverageCompleteExact, Limit: 25})
	if answer.Refs == nil || answer.Notes == nil {
		t.Errorf("refs=%v notes=%v, want empty lists so the wire carries [] rather than null", answer.Refs, answer.Notes)
	}
}

// The tool has to actually BE on the surface for any of the above to mean
// anything. Registration is conditional on a runner existing, so a wiring
// change that dropped it would leave every test here passing over a tool no
// client can call.
func TestQueryWorkspaceIsOnTheComposedSurface(t *testing.T) {
	spec, registered := NewRegistry(nil, SendPath{}).Spec("query_workspace")
	if !registered {
		t.Fatal("query_workspace is not registered on the composed surface")
	}
	if spec.RequiredScope != principal.ScopeRead {
		t.Errorf("required scope = %q, want the read scope — a query writes nothing", spec.RequiredScope)
	}
	if spec.Tier != mcp.TierAutoExecute {
		t.Errorf("tier = %v, want auto-execute: a read is reversible and logged", spec.Tier)
	}
}

// stubOverlayMode answers a fixed mode, which is what lets the guard's two
// branches be exercised without a database.
type stubOverlayMode struct {
	overlay bool
	err     error
}

func (s stubOverlayMode) isOverlayUncached(context.Context) (bool, error) { return s.overlay, s.err }
