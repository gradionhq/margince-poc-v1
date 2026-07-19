// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"net/http"
	"testing"
)

type onboardingStateDTO struct {
	Path             string         `json:"path"`
	Step             string         `json:"step"`
	SourceMode       *string        `json:"source_mode"`
	CompanyDraft     map[string]any `json:"company_draft"`
	SelectedFactKeys []string       `json:"selected_fact_keys"`
	VoiceSkipped     bool           `json:"voice_skipped"`
	ConnectSkipped   bool           `json:"connect_skipped"`
	Version          int            `json:"version"`
}

func onboardingStateBody(version int, step string) anyMap {
	return anyMap{
		"expected_version":   version,
		"step":               step,
		"source_mode":        "manual",
		"company_draft":      anyMap{"display_name": "Acme draft"},
		"selected_fact_keys": []string{},
		"voice_skipped":      false,
		"connect_skipped":    false,
	}
}

func onboardingHeaders(key string) map[string]string {
	return map[string]string{"Idempotency-Key": key}
}

func TestOnboardingStateResumesAndRejectsStaleTabs(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	if status := e.call(t, http.MethodGet, "/v1/onboarding/state", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("initial state status = %d, want 404", status)
	}

	var created onboardingStateDTO
	status := e.call(t, http.MethodPut, "/v1/onboarding/state",
		onboardingStateBody(0, "read"), onboardingHeaders("onboarding-create"), &created)
	if status != http.StatusOK {
		t.Fatalf("create state status = %d, want 200", status)
	}
	if created.Path != "creator" || created.Step != "read" || created.Version != 1 {
		t.Fatalf("created state = %+v, want creator/read/v1", created)
	}

	var resumed onboardingStateDTO
	if status := e.call(t, http.MethodGet, "/v1/onboarding/state", nil, nil, &resumed); status != http.StatusOK {
		t.Fatalf("resume state status = %d, want 200", status)
	}
	if resumed.Version != created.Version || resumed.CompanyDraft["display_name"] != "Acme draft" {
		t.Fatalf("resumed state = %+v, want the persisted draft", resumed)
	}

	var blocked anyMap
	status = e.call(t, http.MethodPut, "/v1/onboarding/state",
		onboardingStateBody(1, "voice"), onboardingHeaders("onboarding-blocked"), &blocked)
	if status != http.StatusConflict || blocked["code"] != "conflict" {
		t.Fatalf("creator bypass = %d %+v, want 409 conflict", status, blocked)
	}

	if status := e.call(t, http.MethodPut, "/v1/company", wellFormedCompany(), nil, nil); status != http.StatusOK {
		t.Fatalf("saving minimum company = %d, want 200", status)
	}

	var advanced onboardingStateDTO
	status = e.call(t, http.MethodPut, "/v1/onboarding/state",
		onboardingStateBody(1, "voice"), onboardingHeaders("onboarding-advance"), &advanced)
	if status != http.StatusOK || advanced.Path != "creator" || advanced.Version != 2 {
		t.Fatalf("advanced state = %d %+v, want creator/v2", status, advanced)
	}

	var stale anyMap
	status = e.call(t, http.MethodPut, "/v1/onboarding/state",
		onboardingStateBody(1, "connect"), onboardingHeaders("onboarding-stale"), &stale)
	if status != http.StatusConflict || stale["code"] != "version_skew" {
		t.Fatalf("stale tab = %d %+v, want 409 version_skew", status, stale)
	}

	var unchanged onboardingStateDTO
	if status := e.call(t, http.MethodGet, "/v1/onboarding/state", nil, nil, &unchanged); status != http.StatusOK {
		t.Fatalf("state after stale write = %d, want 200", status)
	}
	if unchanged.Step != "voice" || unchanged.Version != 2 {
		t.Fatalf("stale write changed state: %+v", unchanged)
	}
}
