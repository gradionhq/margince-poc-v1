// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// HTTP-level coverage for GET /organizations/{id}/hierarchy-rollup: the
// handler (compose.orgRollupHandlers.GetOrganizationHierarchyRollup) and
// its wire mapping (orgRollupToWire) that orgrollup_integration_test.go
// never drives — that suite calls compose.OrgHierarchyRollup directly, so
// the query-default/validation branches and the JSON shape (Money
// envelopes, the restricted_excluded null-vs-empty distinction) only
// exist at the transport. This suite rides the same real-handler-stack
// e2e harness as e2e_integration_test.go (TLS httptest server, session
// cookie, workspace header) and reuses fieldhistory_http_integration_test.go's
// fieldHistoryProblem/assertFieldHistoryValidation422 for the shared
// httperr.Validation 422 shape, since the scope-enum rejection rides the
// exact same wire contract as every other bad query input.
//
// The tree/self fixture creates its org tree and its one open deal
// through the real HTTP write paths (organizations accept parent_org_id
// on create), never raw SQL — unlike orgrollup_integration_test.go, this
// suite has no need for controlled win-probability stages or frozen FX
// rates, so the workspace-seeded default pipeline is enough.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// orgRollupMoneyWire mirrors the contract's Money schema.
type orgRollupMoneyWire struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

// orgRollupRestrictedWire mirrors one restricted_excluded item's wire shape.
type orgRollupRestrictedWire struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// orgRollupResponseWire mirrors the contract's OrganizationHierarchyRollup
// field by field, decoded loosely so a wire-shape regression fails the
// assertions below instead of silently zeroing. RestrictedExcluded is
// decoded as a plain slice on purpose: encoding/json leaves it nil for a
// JSON `null` and non-nil (possibly zero-length) for a JSON `[]`, which is
// exactly the null-vs-empty distinction the happy path must assert on.
type orgRollupResponseWire struct {
	RootID                 string                    `json:"root_id"`
	Scope                  string                    `json:"scope"`
	WeightedPipeline       orgRollupMoneyWire        `json:"weighted_pipeline"`
	ClosedWon              orgRollupMoneyWire        `json:"closed_won"`
	ActivityCount30d       int                       `json:"activity_count_30d"`
	AggregatedAccountCount int                       `json:"aggregated_account_count"`
	RestrictedExcluded     []orgRollupRestrictedWire `json:"restricted_excluded"`
	ComputedAt             string                    `json:"computed_at"`
}

// orgRollupFxProblem is the RFC 7807 body the handler's FXRateUnavailableError
// mapping produces: fx_rate_unavailable carries the offending currency and
// the as-of day, not a per-field validation error list.
type orgRollupFxProblem struct {
	Code    string `json:"code"`
	Details struct {
		Currency string `json:"currency"`
		AsOf     string `json:"as_of"`
	} `json:"details"`
}

// createOrgRollupOrg creates one organization through the real write path,
// optionally hanging it under parentID (empty = a root).
func createOrgRollupOrg(t *testing.T, e *env, name, parentID string) string {
	t.Helper()
	body := anyMap{"display_name": name, "source": "ui"}
	if parentID != "" {
		body["parent_org_id"] = parentID
	}
	var org anyMap
	if status := e.call(t, "POST", "/v1/organizations", body, nil, &org); status != http.StatusCreated {
		t.Fatalf("create organization %q = %d %v", name, status, org)
	}
	return org["id"].(string)
}

// orgRollupOpenStage resolves the workspace-seeded default pipeline's
// open stage and its live win probability, so the happy-path assertion
// below can compute the expected weighted-pipeline figure exactly rather
// than asserting a mere non-zero value.
func orgRollupOpenStage(t *testing.T, e *env) (pipelineID, stageID string, winProbability int) {
	t.Helper()
	var pipelines struct {
		Data []struct {
			ID     string `json:"id"`
			Stages []struct {
				ID             string `json:"id"`
				Semantic       string `json:"semantic"`
				WinProbability int    `json:"win_probability"`
			} `json:"stages"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK {
		t.Fatalf("list pipelines = %d", status)
	}
	if len(pipelines.Data) != 1 {
		t.Fatalf("want exactly one seeded pipeline: %+v", pipelines.Data)
	}
	pipelineID = pipelines.Data[0].ID
	for _, s := range pipelines.Data[0].Stages {
		if s.Semantic == "open" {
			return pipelineID, s.ID, s.WinProbability
		}
	}
	t.Fatalf("seeded pipeline carries no open stage: %+v", pipelines.Data[0])
	return "", "", 0
}

// createOrgRollupOpenDeal opens one deal on org through the real write
// path; amountMinor is the caller's choice so the rollup's weighted-value
// rounding stays exact arithmetic in the caller's test, not a guess.
func createOrgRollupOpenDeal(t *testing.T, e *env, pipelineID, stageID, orgID string, amountMinor int64, currency string) {
	t.Helper()
	var deal anyMap
	status := e.call(t, "POST", "/v1/deals", anyMap{
		"name":            "Rollup HTTP Deal",
		"amount_minor":    amountMinor,
		"currency":        currency,
		"pipeline_id":     pipelineID,
		"stage_id":        stageID,
		"organization_id": orgID,
		"source":          "ui",
	}, nil, &deal)
	if status != http.StatusCreated {
		t.Fatalf("create deal = %d %v", status, deal)
	}
}

// orgRollupHTTPDealAmountMinor is 10,000.00 in minor units: chosen so
// amount × win_probability / 100 divides exactly for ANY 0-100
// win_probability the seeded default pipeline happens to carry, so the
// happy-path assertion is exact arithmetic rather than a guess at the
// seed's stage configuration.
const orgRollupHTTPDealAmountMinor = 1_000_000

// assertOrgRollupTreeHappyPath drives the tree-scope GET on root and checks
// the full wire envelope: the weighted-pipeline figure is exact arithmetic
// over the seeded deal and the seeded stage's live win probability, both
// money objects always carry the workspace base currency, and
// restricted_excluded decodes as a real (non-nil) empty array.
func assertOrgRollupTreeHappyPath(t *testing.T, e *env, root string, winProbability int) {
	t.Helper()
	var rollup orgRollupResponseWire
	status := e.call(t, "GET", "/v1/organizations/"+root+"/hierarchy-rollup", nil, nil, &rollup)
	if status != http.StatusOK {
		t.Fatalf("rollup status = %d, want 200: %+v", status, rollup)
	}
	if rollup.RootID != root || rollup.Scope != "tree" {
		t.Errorf("envelope = {root %q scope %q}, want {%s tree}", rollup.RootID, rollup.Scope, root)
	}
	wantWeighted := orgRollupHTTPDealAmountMinor / 100 * int64(winProbability)
	if rollup.WeightedPipeline.AmountMinor != wantWeighted || rollup.WeightedPipeline.Currency != "EUR" {
		t.Errorf("weighted_pipeline = %+v, want {%d EUR} (the workspace base currency, matching the deal's own EUR)",
			rollup.WeightedPipeline, wantWeighted)
	}
	if rollup.ClosedWon.AmountMinor != 0 || rollup.ClosedWon.Currency != "EUR" {
		t.Errorf("closed_won = %+v, want {0 EUR} — a real money object, never a raw zero-value with no currency", rollup.ClosedWon)
	}
	if rollup.AggregatedAccountCount != 2 {
		t.Errorf("aggregated_account_count = %d, want 2 (root + child)", rollup.AggregatedAccountCount)
	}
	if rollup.RestrictedExcluded == nil {
		t.Error("restricted_excluded decoded nil — the wire sent JSON null, want a real empty array")
	}
	if len(rollup.RestrictedExcluded) != 0 {
		t.Errorf("restricted_excluded = %+v, want empty (nothing is caller-unreadable here)", rollup.RestrictedExcluded)
	}
	if rollup.ComputedAt == "" {
		t.Error("computed_at missing from the envelope")
	}
}

// assertOrgRollupSelfScope drives scope=self on child and checks it
// reports the child alone — the parent's deal never rolls down.
func assertOrgRollupSelfScope(t *testing.T, e *env, child string) {
	t.Helper()
	var rollup orgRollupResponseWire
	status := e.call(t, "GET", "/v1/organizations/"+child+"/hierarchy-rollup?scope=self", nil, nil, &rollup)
	if status != http.StatusOK {
		t.Fatalf("self rollup status = %d, want 200: %+v", status, rollup)
	}
	if rollup.RootID != child || rollup.Scope != "self" {
		t.Errorf("envelope = {root %q scope %q}, want {%s self}", rollup.RootID, rollup.Scope, child)
	}
	if rollup.AggregatedAccountCount != 1 {
		t.Errorf("self aggregated_account_count = %d, want 1 (the child alone, its parent's deal never rolls down)", rollup.AggregatedAccountCount)
	}
	if rollup.WeightedPipeline.AmountMinor != 0 {
		t.Errorf("self weighted_pipeline = %d, want 0 (the child carries no deal of its own)", rollup.WeightedPipeline.AmountMinor)
	}
}

// assertOrgRollupFXUnavailable seeds a fresh root with an open USD deal and
// no stored USD->EUR rate, and checks the wire shape of the resulting
// fx_rate_unavailable refusal.
func assertOrgRollupFXUnavailable(t *testing.T, e *env, pipelineID, stageID string) {
	t.Helper()
	fxRoot := createOrgRollupOrg(t, e, "Rollup HTTP FX Root", "")
	createOrgRollupOpenDeal(t, e, pipelineID, stageID, fxRoot, 10_000, "USD") // no USD->EUR rate on file

	var problem orgRollupFxProblem
	status := e.call(t, "GET", "/v1/organizations/"+fxRoot+"/hierarchy-rollup", nil, nil, &problem)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("fx-unavailable status = %d, want 422: %+v", status, problem)
	}
	if problem.Code != "fx_rate_unavailable" {
		t.Errorf("code = %q, want fx_rate_unavailable", problem.Code)
	}
	if problem.Details.Currency != "USD" {
		t.Errorf("details.currency = %q, want USD", problem.Details.Currency)
	}
	if problem.Details.AsOf == "" {
		t.Error("details.as_of missing from the fx_rate_unavailable problem")
	}
}

func TestOrgRollupHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	pipelineID, stageID, winProbability := orgRollupOpenStage(t, e)

	root := createOrgRollupOrg(t, e, "Rollup HTTP Root", "")
	child := createOrgRollupOrg(t, e, "Rollup HTTP Child", root)
	createOrgRollupOpenDeal(t, e, pipelineID, stageID, root, orgRollupHTTPDealAmountMinor, "EUR")

	t.Run("200 tree happy path", func(t *testing.T) {
		assertOrgRollupTreeHappyPath(t, e, root, winProbability)
	})

	t.Run("200 self scope", func(t *testing.T) {
		assertOrgRollupSelfScope(t, e, child)
	})

	t.Run("422 invalid scope", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.call(t, "GET", "/v1/organizations/"+root+"/hierarchy-rollup?scope=bogus", nil, nil, &problem)
		assertFieldHistoryValidation422(t, status, problem, "scope", "invalid_enum")
	})

	t.Run("422 fx_rate_unavailable", func(t *testing.T) {
		assertOrgRollupFXUnavailable(t, e, pipelineID, stageID)
	})

	t.Run("404 nonexistent organization", func(t *testing.T) {
		status := e.call(t, "GET", "/v1/organizations/"+ids.NewV7().String()+"/hierarchy-rollup", nil, nil, nil)
		if status != http.StatusNotFound {
			t.Fatalf("nonexistent org rollup status = %d, want 404", status)
		}
	})
}
