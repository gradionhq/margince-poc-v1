// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The retention-authoring HTTP surface over a real pool (GCS-WIRE-1..5).
//
// The store's own integration test proves the gate, the audit row and the
// database's uniqueness. This one proves the WIRE: the status codes the SPA
// branches on, and the JSON shape it decodes. Those are the contract the
// frontend was built against, and a handler that returned the right data under
// the wrong status would pass every store test while breaking every client.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func retentionHumanCtx(e *integration.Env, grant principal.ObjectGrant) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"retention_policy": grant},
			RowScope: principal.RowScopeAll,
		},
	})
}

// TestRetentionPolicyHandlersSpeakTheContract walks the five statuses the SPA
// branches on: 200 list, 201 create, 409 duplicate scope, 422 unauthorable
// scope, 200 patch, 204 delete, 404 after.
func TestRetentionPolicyHandlersSpeakTheContract(t *testing.T) {
	e := integration.Setup(t)
	integration.SeedRetentionPolicies(t, e)
	h := privacy.NewHandlers(e.DB(), NewSettingsStore(e.Pool))

	admin := retentionHumanCtx(e, principal.ObjectGrant{Create: true, Read: true, Update: true, Delete: true})

	// GET the seeded ladder.
	rec := httptest.NewRecorder()
	h.ListRetentionPolicies(rec,
		httptest.NewRequest(http.MethodGet, "/v1/retention-policies", nil).WithContext(admin),
		crmcontracts.ListRetentionPoliciesParams{})
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var page struct {
		Data []crmcontracts.RetentionPolicy `json:"data"`
		Page crmcontracts.PageInfo          `json:"page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(page.Data) == 0 {
		t.Fatal("the seeded ladder came back empty")
	}
	if page.Page.HasMore {
		t.Error("has_more is true on a collection bounded by the scope count")
	}
	// The wire carries scope AND its split halves, so a client can group rows
	// without parsing the enum.
	for _, p := range page.Data {
		if p.ObjectType == "" || string(p.Scope) == "" {
			t.Errorf("policy %s carries no scope/object_type: %+v", p.Id, p)
		}
	}

	// POST the seven-year won-deal policy — #695's actual customer requirement.
	created := createRetentionPolicy(admin, t, h,
		`{"scope":"deal/won","retain_days":2555,"action":"archive","lawful_basis":"contract"}`,
		http.StatusCreated)
	if created.RetainDays != 2555 || created.Action != "archive" {
		t.Fatalf("created policy is not what was posted: %+v", created)
	}
	if created.SuppressedByPosture {
		t.Error("an archive policy reads as suppressed with no posture held")
	}

	// The same scope again is a CONFLICT, not an upsert: the SPA branches on 409
	// to say "edit the existing row".
	createRetentionPolicy(admin, t, h,
		`{"scope":"deal/won","retain_days":30,"action":"archive"}`, http.StatusConflict)

	// A scope the evaluator cannot act on is 422, not 400 or 500 — the frontend
	// relays the server's own sentence, so the classification has to be right.
	createRetentionPolicy(admin, t, h,
		`{"scope":"deal/abandoned","retain_days":30,"action":"archive"}`, http.StatusUnprocessableEntity)

	// PATCH is sparse: an omitted field is unchanged.
	patchRec := httptest.NewRecorder()
	h.UpdateRetentionPolicy(patchRec,
		httptest.NewRequest(http.MethodPatch, "/v1/retention-policies/"+created.Id.String(),
			strings.NewReader(`{"enabled":false}`)).WithContext(admin),
		crmcontracts.Id(created.Id))
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200 (body %s)", patchRec.Code, patchRec.Body)
	}
	var patched crmcontracts.RetentionPolicy
	if err := json.Unmarshal(patchRec.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patched.Enabled {
		t.Error("enabled:false did not disable the policy")
	}
	if patched.RetainDays != 2555 {
		t.Errorf("a sparse patch changed retain_days to %d — an omitted field must be unchanged",
			patched.RetainDays)
	}

	// DELETE is 204, and the row is gone rather than merely disabled.
	delRec := httptest.NewRecorder()
	h.DeleteRetentionPolicy(delRec,
		httptest.NewRequest(http.MethodDelete, "/v1/retention-policies/"+created.Id.String(), nil).
			WithContext(admin),
		crmcontracts.Id(created.Id))
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 (body %s)", delRec.Code, delRec.Body)
	}
	againRec := httptest.NewRecorder()
	h.UpdateRetentionPolicy(againRec,
		httptest.NewRequest(http.MethodPatch, "/v1/retention-policies/"+created.Id.String(),
			strings.NewReader(`{"enabled":true}`)).WithContext(admin),
		crmcontracts.Id(created.Id))
	if againRec.Code != http.StatusNotFound {
		t.Errorf("patching a deleted policy = %d, want 404", againRec.Code)
	}
}

// createRetentionPolicy posts one policy, asserts the status, and decodes the
// body on success.
func createRetentionPolicy(ctx context.Context, t *testing.T, h privacy.Handlers,
	body string, wantStatus int,
) crmcontracts.RetentionPolicy {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/retention-policies", strings.NewReader(body)).
		WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	h.CreateRetentionPolicy(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("create %s: status = %d, want %d (body %s)", body, rec.Code, wantStatus, rec.Body)
	}
	if wantStatus != http.StatusCreated {
		return crmcontracts.RetentionPolicy{}
	}
	var out crmcontracts.RetentionPolicy
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return out
}

// TestRetentionPolicyHandlersRefuseARoleWithoutTheGrant is the negative half:
// the object is admin/ops-only on every verb, READ included, so a manager or rep
// gets 403 rather than a readable ladder.
func TestRetentionPolicyHandlersRefuseARoleWithoutTheGrant(t *testing.T) {
	e := integration.Setup(t)
	integration.SeedRetentionPolicies(t, e)
	h := privacy.NewHandlers(e.DB(), NewSettingsStore(e.Pool))
	rep := retentionHumanCtx(e, principal.ObjectGrant{})

	listRec := httptest.NewRecorder()
	h.ListRetentionPolicies(listRec,
		httptest.NewRequest(http.MethodGet, "/v1/retention-policies", nil).WithContext(rep),
		crmcontracts.ListRetentionPoliciesParams{})
	if listRec.Code != http.StatusForbidden {
		t.Errorf("list without a grant = %d, want 403", listRec.Code)
	}

	createRetentionPolicy(rep, t, h,
		`{"scope":"deal/won","retain_days":2555,"action":"archive"}`, http.StatusForbidden)

	settingsRec := httptest.NewRecorder()
	h.GetRetentionSettings(settingsRec,
		httptest.NewRequest(http.MethodGet, "/v1/retention/settings", nil).WithContext(rep))
	if settingsRec.Code != http.StatusForbidden {
		t.Errorf("posture read without a grant = %d, want 403", settingsRec.Code)
	}
}

// TestRetentionSettingsHandlersToggleThePostureAndReportIt covers GCS-WIRE-5,
// including the sparse-patch case a client retry produces.
func TestRetentionSettingsHandlersToggleThePostureAndReportIt(t *testing.T) {
	e := integration.Setup(t)
	integration.SeedRetentionPolicies(t, e)
	h := privacy.NewHandlers(e.DB(), NewSettingsStore(e.Pool))
	admin := retentionHumanCtx(e, principal.ObjectGrant{Create: true, Read: true, Update: true, Delete: true})

	// An installation nobody has configured reads as the registered default —
	// storage limitation on, which is what "compliant out of the box" means.
	if got := readRetentionPosture(admin, t, h); got {
		t.Error("a fresh installation defaults to retain-only; it must default to enforcing")
	}

	patchRec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/v1/retention/settings",
		strings.NewReader(`{"retain_only":true}`)).WithContext(admin)
	req.Header.Set("Content-Type", "application/json")
	h.UpdateRetentionSettings(patchRec, req)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("posture patch = %d, want 200 (body %s)", patchRec.Code, patchRec.Body)
	}
	if !readRetentionPosture(admin, t, h) {
		t.Fatal("the posture did not survive its own write")
	}

	// A patch naming no field is a no-op that answers with the current posture:
	// an idempotent client retry must not be an error, and must not clear it.
	emptyRec := httptest.NewRecorder()
	emptyReq := httptest.NewRequest(http.MethodPatch, "/v1/retention/settings",
		strings.NewReader(`{}`)).WithContext(admin)
	emptyReq.Header.Set("Content-Type", "application/json")
	h.UpdateRetentionSettings(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusOK {
		t.Fatalf("empty posture patch = %d, want 200 (body %s)", emptyRec.Code, emptyRec.Body)
	}
	if !readRetentionPosture(admin, t, h) {
		t.Error("an empty patch cleared the posture")
	}

	// Every destructive seeded policy now reports itself suppressed on the wire,
	// which is the reading the screen renders.
	listRec := httptest.NewRecorder()
	h.ListRetentionPolicies(listRec,
		httptest.NewRequest(http.MethodGet, "/v1/retention-policies", nil).WithContext(admin),
		crmcontracts.ListRetentionPoliciesParams{})
	var page struct {
		Data []crmcontracts.RetentionPolicy `json:"data"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	var destructive, suppressed int
	for _, p := range page.Data {
		if p.Action == "anonymize" || p.Action == "erase" {
			destructive++
			if p.SuppressedByPosture {
				suppressed++
			}
		} else if p.SuppressedByPosture {
			t.Errorf("%s reads as suppressed, but archiving retains", p.Scope)
		}
	}
	if destructive == 0 || suppressed != destructive {
		t.Errorf("%d of %d destructive policies report suppressed_by_posture", suppressed, destructive)
	}
}

// readRetentionPosture GETs the posture and decodes it.
func readRetentionPosture(ctx context.Context, t *testing.T, h privacy.Handlers) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	h.GetRetentionSettings(rec,
		httptest.NewRequest(http.MethodGet, "/v1/retention/settings", nil).WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("posture read = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var out crmcontracts.RetentionSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode posture: %v", err)
	}
	return out.RetainOnly
}
