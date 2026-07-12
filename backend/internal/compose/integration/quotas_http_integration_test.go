// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// HTTP-level coverage for the six /quotas operations (RD-T06): the
// handler (modules/quotas.Handlers) and its wire mapping that
// quotas_integration_test.go/quotas_attainment_integration_test.go never
// drive — those suites call the store directly, so the transport's error
// mapping (owner_xor_team_required, the two attainment 422s, the
// currency CHECK's constraint_violated) and the JSON shape only exist at
// this layer. Rides the same real-handler-stack e2e harness as
// e2e_integration_test.go (TLS httptest server, session cookie, workspace
// header).
//
// There is no /v1/teams or role-management endpoint yet (both are
// fast-follow per crm.yaml's own NET-NEW comment) — the team fixture and
// the rep-role demotion for the 403 scenario use the owner connection
// directly, the same technique e2e_integration_test.go's
// setWorkspaceSeat already uses for seat_type.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// quotaProblem decodes the RFC 7807 shapes this suite's error scenarios
// produce: a bare sentinel code (version_skew, permission_denied, the two
// attainment refusals) or the validation_error shape with its
// details.errors[] list.
type quotaProblem struct {
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	Details struct {
		Errors []struct {
			Field   string `json:"field"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"details"`
}

// seedQuotaTeam creates one team directly via the owner connection —
// there is no /v1/teams endpoint yet.
func seedQuotaTeam(t *testing.T, e *env, name string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := e.owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()

	var wsID, teamID string
	if err := tx.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO team (workspace_id, name) VALUES ($1, $2) RETURNING id`, wsID, name).Scan(&teamID); err != nil {
		t.Fatalf("insert team: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return teamID
}

// demoteToRep flips the bootstrap admin's role assignment from admin to
// rep (0068's grant: quota.read only) via the owner connection, so the
// 403 scenario proves the REST-side enforcement of the same object gate
// quotas_integration_test.go's TestQuotaRBAC_RepReadsButNeverMutates
// proves at the store. Irreversible for the rest of this env — callers
// run it last.
func demoteToRep(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()

	var wsID string
	if err := tx.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID); err != nil {
		t.Fatalf("set guc: %v", err)
	}
	var userID, repRoleID string
	if err := tx.QueryRow(ctx,
		`SELECT id FROM app_user WHERE workspace_id = $1 ORDER BY created_at LIMIT 1`, wsID).Scan(&userID); err != nil {
		t.Fatalf("admin lookup: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT id FROM role WHERE workspace_id = $1 AND key = 'rep'`, wsID).Scan(&repRoleID); err != nil {
		t.Fatalf("rep role lookup: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM role_assignment WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("clear role assignment: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO role_assignment (workspace_id, role_id, user_id) VALUES ($1, $2, $3)`,
		wsID, repRoleID, userID); err != nil {
		t.Fatalf("assign rep role: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// createAndCloseQuotaDeal opens a deal owned by ownerID and advances it
// straight to the seeded pipeline's won stage, so its amount counts
// toward an owner-quota's attainment.
func createAndCloseQuotaDeal(t *testing.T, e *env, stages seededStages, ownerID string, amountMinor int64, currency string) string {
	t.Helper()
	var deal anyMap
	status := e.call(t, "POST", "/v1/deals", anyMap{
		"name": "Quota Attainment Deal", "amount_minor": amountMinor, "currency": currency,
		"pipeline_id": stages.pipelineID, "stage_id": stages.open, "owner_id": ownerID, "source": "ui",
	}, nil, &deal)
	if status != http.StatusCreated {
		t.Fatalf("create quota deal = %d %v", status, deal)
	}
	dealID := deal["id"].(string)
	status = e.call(t, "POST", "/v1/deals/"+dealID+"/advance", anyMap{"to_stage_id": stages.won}, nil, &deal)
	if status != http.StatusOK || deal["status"] != "won" {
		t.Fatalf("advance quota deal to won = %d %v", status, deal)
	}
	return dealID
}

func TestQuotasHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var me anyMap
	if status := e.call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("/me = %d", status)
	}
	adminID := me["user"].(anyMap)["id"].(string)
	teamID := seedQuotaTeam(t, e, "Quota HTTP Team")

	var ownerQuotaID, teamQuotaID string
	t.Run("201 create owner and team quota", func(t *testing.T) {
		ownerQuotaID, teamQuotaID = assertQuotaCreate201(t, e, adminID, teamID)
	})
	t.Run("422 owner_xor_team both set", func(t *testing.T) {
		assertQuotaXOR422(t, e, &adminID, &teamID)
	})
	t.Run("422 owner_xor_team neither set", func(t *testing.T) {
		assertQuotaXOR422(t, e, nil, nil)
	})
	t.Run("200 get quota, 404 unknown id", func(t *testing.T) {
		assertQuotaGet(t, e, ownerQuotaID)
	})
	t.Run("200 list with owner/team filters", func(t *testing.T) {
		assertQuotaList(t, e, ownerQuotaID, teamQuotaID, adminID, teamID)
	})
	t.Run("200 list sorted, 422 unknown sort field", func(t *testing.T) {
		assertQuotaListSort(t, e, ownerQuotaID, teamQuotaID)
	})
	t.Run("update: 200 happy, 409 stale If-Match, 422 malformed If-Match", func(t *testing.T) {
		assertQuotaUpdate(t, e, ownerQuotaID)
	})
	t.Run("200 archive returns the full entity, stays gettable", func(t *testing.T) {
		assertQuotaArchive(t, e, adminID)
	})
	t.Run("422 negative target on create and patch", func(t *testing.T) {
		assertQuotaNegativeTarget(t, e, adminID)
	})
	t.Run("422 invalid currency (CHECK violation)", func(t *testing.T) {
		assertQuotaInvalidCurrency(t, e, adminID)
	})
	t.Run("422 inverted period (CHECK violation)", func(t *testing.T) {
		assertQuotaInvertedPeriod(t, e, adminID)
	})
	t.Run("200 attainment happy path — golden numbers", func(t *testing.T) {
		assertQuotaAttainmentHappy(t, e, adminID)
	})
	t.Run("422 attainment_target_zero", func(t *testing.T) {
		assertQuotaAttainmentTargetZero(t, e, adminID)
	})
	t.Run("422 attainment_computation_failed (missing fx rate)", func(t *testing.T) {
		assertQuotaAttainmentComputationFailed(t, e, adminID)
	})
	t.Run("403 rep create (0068 grants)", func(t *testing.T) {
		assertQuotaRepCannotCreate(t, e, adminID)
	})
}

// assertQuotaCreate201 creates one owner-quota and one team-quota through
// the real write path, checking the XOR shape lands correctly on each
// side and the fresh-row invariants (version 1, no archived_at).
func assertQuotaCreate201(t *testing.T, e *env, ownerID, teamID string) (ownerQuotaID, teamQuotaID string) {
	t.Helper()
	var owned anyMap
	status := e.call(t, "POST", "/v1/quotas", anyMap{
		"owner_id": ownerID, "period_start": "2020-01-01", "period_end": "2030-12-31",
		"target_minor": 1000000, "currency": "EUR",
	}, nil, &owned)
	if status != http.StatusCreated {
		t.Fatalf("create owner quota = %d %v", status, owned)
	}
	if owned["owner_id"] != ownerID || owned["team_id"] != nil {
		t.Errorf("owner quota = %+v, want owner_id=%s and no team_id", owned, ownerID)
	}
	if owned["target_minor"].(float64) != 1000000 || owned["currency"] != "EUR" {
		t.Errorf("owner quota target/currency = %+v", owned)
	}
	if owned["version"].(float64) != 1 || owned["archived_at"] != nil {
		t.Errorf("a fresh quota carries version 1 and no archived_at, got %+v", owned)
	}

	var team anyMap
	status = e.call(t, "POST", "/v1/quotas", anyMap{
		"team_id": teamID, "period_start": "2026-01-01", "period_end": "2026-03-31",
		"target_minor": 5000000, "currency": "EUR",
	}, nil, &team)
	if status != http.StatusCreated {
		t.Fatalf("create team quota = %d %v", status, team)
	}
	if team["team_id"] != teamID || team["owner_id"] != nil {
		t.Errorf("team quota = %+v, want team_id=%s and no owner_id", team, teamID)
	}
	return owned["id"].(string), team["id"].(string)
}

// assertQuotaXOR422 drives createQuota with the given owner_id/team_id
// request-body values — nil omits the key entirely — and checks the
// contract's exact owner_xor_team_required details.errors shape for both
// the both-set and neither-set violations.
func assertQuotaXOR422(t *testing.T, e *env, ownerID, teamID *string) {
	t.Helper()
	body := anyMap{"period_start": "2026-01-01", "period_end": "2026-03-31", "target_minor": 1000, "currency": "EUR"}
	if ownerID != nil {
		body["owner_id"] = *ownerID
	}
	if teamID != nil {
		body["team_id"] = *teamID
	}
	var problem quotaProblem
	status := e.call(t, "POST", "/v1/quotas", body, nil, &problem)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("owner-xor-team violation = %d, want 422: %+v", status, problem)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "owner_id" ||
		problem.Details.Errors[0].Code != "owner_xor_team_required" {
		t.Fatalf("details.errors = %+v, want exactly [{owner_id owner_xor_team_required}]", problem.Details.Errors)
	}
}

func assertQuotaGet(t *testing.T, e *env, id string) {
	t.Helper()
	var got anyMap
	if status := e.call(t, "GET", "/v1/quotas/"+id, nil, nil, &got); status != http.StatusOK || got["id"] != id {
		t.Fatalf("get quota = %d %+v, want 200 id=%s", status, got, id)
	}
	if status := e.call(t, "GET", "/v1/quotas/"+ids.NewV7().String(), nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("get unknown quota = %d, want 404", status)
	}
}

func assertQuotaList(t *testing.T, e *env, ownerQuotaID, teamQuotaID, ownerID, teamID string) {
	t.Helper()
	var byOwner struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/quotas?owner_id="+ownerID, nil, nil, &byOwner); status != http.StatusOK {
		t.Fatalf("list by owner_id = %d", status)
	}
	if len(byOwner.Data) != 1 || byOwner.Data[0]["id"] != ownerQuotaID {
		t.Fatalf("owner_id filter = %+v, want exactly the owner quota", byOwner.Data)
	}
	var byTeam struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/quotas?team_id="+teamID, nil, nil, &byTeam); status != http.StatusOK {
		t.Fatalf("list by team_id = %d", status)
	}
	if len(byTeam.Data) != 1 || byTeam.Data[0]["id"] != teamQuotaID {
		t.Fatalf("team_id filter = %+v, want exactly the team quota", byTeam.Data)
	}
}

// assertQuotaListSort proves the contract's Sort parameter is honored on
// listQuotas: the owner quota's period (2020..2030) brackets the team
// quota's (2026 Q1), so sort=period_start puts the owner quota first and
// sort=-period_start reverses that — while a field outside the quota
// vocabulary answers the established 422 sort_field_not_allowed shape.
func assertQuotaListSort(t *testing.T, e *env, ownerQuotaID, teamQuotaID string) {
	t.Helper()
	var asc struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/quotas?sort=period_start", nil, nil, &asc); status != http.StatusOK {
		t.Fatalf("sort=period_start = %d", status)
	}
	if len(asc.Data) != 2 || asc.Data[0]["id"] != ownerQuotaID || asc.Data[1]["id"] != teamQuotaID {
		t.Fatalf("sort=period_start order = %+v, want [owner %s, team %s]", asc.Data, ownerQuotaID, teamQuotaID)
	}
	var desc struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/quotas?sort=-period_start", nil, nil, &desc); status != http.StatusOK {
		t.Fatalf("sort=-period_start = %d", status)
	}
	if len(desc.Data) != 2 || desc.Data[0]["id"] != teamQuotaID || desc.Data[1]["id"] != ownerQuotaID {
		t.Fatalf("sort=-period_start order = %+v, want [team %s, owner %s]", desc.Data, teamQuotaID, ownerQuotaID)
	}

	var problem quotaProblem
	status := e.call(t, "GET", "/v1/quotas?sort=banana", nil, nil, &problem)
	if status != http.StatusUnprocessableEntity || problem.Code != "validation_error" {
		t.Fatalf("sort=banana = %d %+v, want 422 validation_error", status, problem)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "sort" ||
		problem.Details.Errors[0].Code != "sort_field_not_allowed" {
		t.Fatalf("details.errors = %+v, want [{sort sort_field_not_allowed}]", problem.Details.Errors)
	}
}

// assertQuotaUpdate drives the merge-PATCH happy path (version 1→2),
// then the same If-Match value replayed (now stale, 409 version_skew),
// then a non-numeric If-Match (422 validation_error/malformed_if_match).
func assertQuotaUpdate(t *testing.T, e *env, id string) {
	t.Helper()
	var updated anyMap
	status := e.call(t, "PATCH", "/v1/quotas/"+id, anyMap{"target_minor": 2000000},
		map[string]string{"If-Match": "1"}, &updated)
	if status != http.StatusOK || updated["target_minor"].(float64) != 2000000 || updated["version"].(float64) != 2 {
		t.Fatalf("update = %d %+v, want 200 target=2000000 version=2", status, updated)
	}

	var stale quotaProblem
	status = e.call(t, "PATCH", "/v1/quotas/"+id, anyMap{"target_minor": 3000000},
		map[string]string{"If-Match": "1"}, &stale)
	if status != http.StatusConflict || stale.Code != "version_skew" {
		t.Fatalf("stale If-Match = %d %+v, want 409 version_skew", status, stale)
	}

	var malformed quotaProblem
	status = e.call(t, "PATCH", "/v1/quotas/"+id, anyMap{"target_minor": 3000000},
		map[string]string{"If-Match": "not-a-version"}, &malformed)
	if status != http.StatusUnprocessableEntity || malformed.Code != "validation_error" {
		t.Fatalf("malformed If-Match = %d %+v, want 422 validation_error", status, malformed)
	}
}

// assertQuotaArchive archives a dedicated fresh quota (not one reused by
// the other scenarios) and checks the 200-with-entity shape plus the
// house single-get convention: an archived quota stays fetchable by id.
func assertQuotaArchive(t *testing.T, e *env, ownerID string) {
	t.Helper()
	var created anyMap
	status := e.call(t, "POST", "/v1/quotas", anyMap{
		"owner_id": ownerID, "period_start": "2026-01-01", "period_end": "2026-03-31",
		"target_minor": 100, "currency": "EUR",
	}, nil, &created)
	if status != http.StatusCreated {
		t.Fatalf("create quota to archive = %d %v", status, created)
	}
	id := created["id"].(string)

	var archived anyMap
	status = e.call(t, "DELETE", "/v1/quotas/"+id, nil, nil, &archived)
	if status != http.StatusOK || archived["archived_at"] == nil || archived["id"] != id {
		t.Fatalf("archive = %d %+v, want 200 + the full entity with archived_at set", status, archived)
	}

	var stillGettable anyMap
	if status := e.call(t, "GET", "/v1/quotas/"+id, nil, nil, &stillGettable); status != http.StatusOK || stillGettable["archived_at"] == nil {
		t.Fatalf("get archived quota = %d %+v, want 200 with archived_at set", status, stillGettable)
	}
}

// assertQuotaInvalidCurrency drives a lowercase currency straight past
// the (nonexistent) request-schema validation into the quota_currency_check
// CHECK constraint — the defense-in-depth net writeQuotaErr shares with
// deals/handlers.go.
// assertQuotaNegativeTarget drives a below-minimum target_minor into
// both write paths: the contract's minimum (0) is enforced by neither
// the generated decode nor merge-PATCH, so the store's refusal is what
// keeps a negative revenue target out — on create and on the patched
// merged state alike.
func assertQuotaNegativeTarget(t *testing.T, e *env, ownerID string) {
	t.Helper()
	assertNegativeTarget422 := func(status int, problem quotaProblem, path string) {
		t.Helper()
		if status != http.StatusUnprocessableEntity || problem.Code != "validation_error" {
			t.Fatalf("negative target on %s = %d %+v, want 422 validation_error", path, status, problem)
		}
		if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "target_minor" ||
			problem.Details.Errors[0].Code != "minimum" {
			t.Fatalf("details.errors = %+v, want [{target_minor minimum}]", problem.Details.Errors)
		}
	}

	var problem quotaProblem
	status := e.call(t, "POST", "/v1/quotas", anyMap{
		"owner_id": ownerID, "period_start": "2026-01-01", "period_end": "2026-03-31",
		"target_minor": -1, "currency": "EUR",
	}, nil, &problem)
	assertNegativeTarget422(status, problem, "create")

	var created anyMap
	if status := e.call(t, "POST", "/v1/quotas", anyMap{
		"owner_id": ownerID, "period_start": "2027-01-01", "period_end": "2027-03-31",
		"target_minor": 1000, "currency": "EUR",
	}, nil, &created); status != http.StatusCreated {
		t.Fatalf("seed quota for the patch scenario = %d %+v", status, created)
	}
	var patched quotaProblem
	status = e.call(t, "PATCH", "/v1/quotas/"+created["id"].(string), anyMap{"target_minor": -1},
		map[string]string{"If-Match": "1"}, &patched)
	assertNegativeTarget422(status, patched, "patch")
}

func assertQuotaInvalidCurrency(t *testing.T, e *env, ownerID string) {
	t.Helper()
	var problem quotaProblem
	status := e.call(t, "POST", "/v1/quotas", anyMap{
		"owner_id": ownerID, "period_start": "2026-01-01", "period_end": "2026-03-31",
		"target_minor": 1000, "currency": "eur",
	}, nil, &problem)
	if status != http.StatusUnprocessableEntity || problem.Code != "validation_error" {
		t.Fatalf("lowercase currency = %d %+v, want 422 validation_error", status, problem)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "quota_currency_check" ||
		problem.Details.Errors[0].Code != "constraint_violated" {
		t.Fatalf("details.errors = %+v, want [{quota_currency_check constraint_violated}]", problem.Details.Errors)
	}
}

// assertQuotaInvertedPeriod drives a period_end before period_start into
// the quota_period_valid CHECK (0067) — a quota measuring a negative
// window is refused with the same typed 422 as the currency CHECK, never
// stored or answered with an opaque 500.
func assertQuotaInvertedPeriod(t *testing.T, e *env, ownerID string) {
	t.Helper()
	var problem quotaProblem
	status := e.call(t, "POST", "/v1/quotas", anyMap{
		"owner_id": ownerID, "period_start": "2026-03-31", "period_end": "2026-01-01",
		"target_minor": 1000, "currency": "EUR",
	}, nil, &problem)
	if status != http.StatusUnprocessableEntity || problem.Code != "validation_error" {
		t.Fatalf("inverted period = %d %+v, want 422 validation_error", status, problem)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "quota_period_valid" ||
		problem.Details.Errors[0].Code != "constraint_violated" {
		t.Fatalf("details.errors = %+v, want [{quota_period_valid constraint_violated}]", problem.Details.Errors)
	}
}

// assertQuotaAttainmentHappy closes one deal for the target amount and
// checks every golden number the contract's worked example names:
// closed_won_minor, target_minor/currency, the uncapped attainment_pct,
// the signed gap, the band, and the per-deal decomposition.
func assertQuotaAttainmentHappy(t *testing.T, e *env, ownerID string) {
	t.Helper()
	var quota anyMap
	status := e.call(t, "POST", "/v1/quotas", anyMap{
		"owner_id": ownerID, "period_start": "2020-01-01", "period_end": "2030-12-31",
		"target_minor": 1000000, "currency": "EUR",
	}, nil, &quota)
	if status != http.StatusCreated {
		t.Fatalf("create attainment quota = %d %v", status, quota)
	}
	quotaID := quota["id"].(string)

	stages := discoverSeededPipeline(t, e)
	dealID := createAndCloseQuotaDeal(t, e, stages, ownerID, 1_500_000, "EUR")

	var att anyMap
	status = e.call(t, "GET", "/v1/quotas/"+quotaID+"/attainment", nil, nil, &att)
	if status != http.StatusOK {
		t.Fatalf("attainment = %d %v", status, att)
	}
	if att["quota_id"] != quotaID {
		t.Errorf("quota_id = %v, want %s", att["quota_id"], quotaID)
	}
	if att["closed_won_minor"].(float64) != 1_500_000 {
		t.Errorf("closed_won_minor = %v, want 1500000", att["closed_won_minor"])
	}
	if att["target_minor"].(float64) != 1_000_000 || att["currency"] != "EUR" {
		t.Errorf("target_minor/currency = %v/%v, want 1000000/EUR", att["target_minor"], att["currency"])
	}
	if att["attainment_pct"].(float64) != 150 {
		t.Errorf("attainment_pct = %v, want 150", att["attainment_pct"])
	}
	if att["gap_minor"].(float64) != 500_000 {
		t.Errorf("gap_minor = %v, want 500000", att["gap_minor"])
	}
	if att["band"] != "met" {
		t.Errorf("band = %v, want met", att["band"])
	}
	deals, ok := att["contributing_deals"].([]any)
	if !ok || len(deals) != 1 {
		t.Fatalf("contributing_deals = %v, want exactly one entry", att["contributing_deals"])
	}
	first, _ := deals[0].(anyMap)
	if first["deal_id"] != dealID || first["base_value_minor"].(float64) != 1_500_000 {
		t.Errorf("contributing_deals[0] = %+v, want deal_id=%s base_value_minor=1500000", first, dealID)
	}
	if s, _ := att["as_of_date"].(string); s == "" {
		t.Error("as_of_date missing from the attainment envelope")
	}
}

func assertQuotaAttainmentTargetZero(t *testing.T, e *env, ownerID string) {
	t.Helper()
	var quota anyMap
	status := e.call(t, "POST", "/v1/quotas", anyMap{
		"owner_id": ownerID, "period_start": "2026-01-01", "period_end": "2026-03-31",
		"target_minor": 0, "currency": "EUR",
	}, nil, &quota)
	if status != http.StatusCreated {
		t.Fatalf("create zero-target quota = %d %v", status, quota)
	}
	var problem quotaProblem
	status = e.call(t, "GET", "/v1/quotas/"+quota["id"].(string)+"/attainment", nil, nil, &problem)
	if status != http.StatusUnprocessableEntity || problem.Code != "attainment_target_zero" {
		t.Fatalf("zero-target attainment = %d %+v, want 422 attainment_target_zero", status, problem)
	}
}

// assertQuotaAttainmentComputationFailed uses a foreign-currency quota
// with no stored fx_rate row and checks the wire detail names the
// failure honestly without leaking the from/to currency pair the store's
// wrapped sentinel carries for the server log.
func assertQuotaAttainmentComputationFailed(t *testing.T, e *env, ownerID string) {
	t.Helper()
	var quota anyMap
	status := e.call(t, "POST", "/v1/quotas", anyMap{
		"owner_id": ownerID, "period_start": "2026-01-01", "period_end": "2026-03-31",
		"target_minor": 1000, "currency": "GBP",
	}, nil, &quota)
	if status != http.StatusCreated {
		t.Fatalf("create foreign-currency quota = %d %v", status, quota)
	}
	var problem quotaProblem
	status = e.call(t, "GET", "/v1/quotas/"+quota["id"].(string)+"/attainment", nil, nil, &problem)
	if status != http.StatusUnprocessableEntity || problem.Code != "attainment_computation_failed" {
		t.Fatalf("missing-fx attainment = %d %+v, want 422 attainment_computation_failed", status, problem)
	}
	if problem.Detail == "" {
		t.Error("attainment_computation_failed detail missing")
	}
	if strings.Contains(problem.Detail, "GBP") || strings.Contains(problem.Detail, "EUR") {
		t.Errorf("computation-failed detail leaked the FX pair onto the wire: %q", problem.Detail)
	}
}

// assertQuotaRepCannotCreate demotes the session's own user to rep
// (0068: read-only on quota) and checks the object gate refuses a create
// over REST exactly as it does at the store.
func assertQuotaRepCannotCreate(t *testing.T, e *env, ownerID string) {
	t.Helper()
	demoteToRep(t, e)
	var problem quotaProblem
	status := e.call(t, "POST", "/v1/quotas", anyMap{
		"owner_id": ownerID, "period_start": "2026-01-01", "period_end": "2026-03-31",
		"target_minor": 1000, "currency": "EUR",
	}, nil, &problem)
	if status != http.StatusForbidden || problem.Code != "permission_denied" {
		t.Fatalf("rep create = %d %+v, want 403 permission_denied", status, problem)
	}
}
