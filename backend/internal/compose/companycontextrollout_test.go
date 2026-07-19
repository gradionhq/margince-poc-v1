// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestCompanyContextRolloutStagesAreMonotonic(t *testing.T) {
	tests := []struct {
		name, rollout           string
		read, tasks, onboarding bool
	}{
		{name: "default", read: true, tasks: true, onboarding: true},
		{name: companyContextRolloutOff, rollout: companyContextRolloutOff},
		{name: companyContextRolloutRead, rollout: companyContextRolloutRead, read: true},
		{name: companyContextRolloutTasks, rollout: companyContextRolloutTasks, read: true, tasks: true},
		{name: companyContextRolloutOnboarding, rollout: companyContextRolloutOnboarding, read: true, tasks: true, onboarding: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if companyContextReadEnabled(test.rollout) != test.read ||
				companyContextTasksEnabled(test.rollout) != test.tasks ||
				companyContextOnboardingEnabled(test.rollout) != test.onboarding {
				t.Fatalf("rollout %q resolved to read=%v tasks=%v onboarding=%v",
					test.rollout, companyContextReadEnabled(test.rollout),
					companyContextTasksEnabled(test.rollout), companyContextOnboardingEnabled(test.rollout))
			}
		})
	}
}

func TestCompanyContextCapabilitiesDefaultToOnboarding(t *testing.T) {
	recorder := httptest.NewRecorder()
	companyHandlers{}.GetCompanyContextCapabilities(
		recorder,
		httptest.NewRequest(http.MethodGet, "/v1/company/context/capabilities", nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var got crmcontracts.CompanyContextCapabilities
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Rollout != crmcontracts.CompanyContextCapabilitiesRolloutOnboarding ||
		!got.ReadEnabled || !got.TasksEnabled || !got.OnboardingEnabled {
		t.Fatalf("default capabilities = %+v", got)
	}
}

func TestCompanySiteReadHandlersRefuseWhenRolloutIsOff(t *testing.T) {
	handlers := siteReadHandlers{companyContextRollout: companyContextRolloutOff}
	request := func() *http.Request {
		return httptest.NewRequest(http.MethodGet, "/v1/company/site-reads", nil)
	}
	tests := []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
	}{
		{name: "start", call: func(w http.ResponseWriter, r *http.Request) {
			handlers.StartCompanySiteRead(w, r, crmcontracts.StartCompanySiteReadParams{})
		}},
		{name: "get", call: func(w http.ResponseWriter, r *http.Request) {
			handlers.GetCompanySiteRead(w, r, openapi_types.UUID{})
		}},
		{name: "confirm", call: func(w http.ResponseWriter, r *http.Request) {
			handlers.ConfirmCompanySiteRead(w, r, openapi_types.UUID{}, crmcontracts.ConfirmCompanySiteReadParams{})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.call(recorder, request())
			if recorder.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotImplemented)
			}
		})
	}
}

func TestSiteReadResolutionsPreserveDecisions(t *testing.T) {
	if got := siteReadResolutions(nil); got != nil {
		t.Fatalf("nil resolutions mapped to %#v", got)
	}
	value := "Owner-entered value"
	input := []crmcontracts.CompanySiteReadResolution{{
		Key: "profile:industry", Action: crmcontracts.UseValue, Value: &value,
	}}
	got := siteReadResolutions(&input)
	if len(got) != 1 || got[0].Key != input[0].Key || got[0].Action != string(input[0].Action) ||
		got[0].Value == nil || *got[0].Value != value {
		t.Fatalf("mapped resolutions = %#v", got)
	}
}
