// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
)

func TestTheViewsOriginFallsBackToThePublicBaseURL(t *testing.T) {
	got, err := mcpAppsOrigin(apiConfig{publicBaseURL: "https://crm.example.com"}, true)
	if err != nil {
		t.Fatalf("resolving the views origin: %v", err)
	}
	if got == nil || got.String() != "https://crm.example.com" {
		t.Fatalf("the views origin resolved to %v, want the public base URL", got)
	}
}

func TestAnExplicitViewsOriginWinsOverThePublicBaseURL(t *testing.T) {
	// The setting exists precisely so the two can differ: the value must be
	// API-reachable, which is not the same as publicly reachable.
	cfg := apiConfig{publicBaseURL: "https://crm.example.com", mcpAppsBaseURL: "http://10.10.0.7:8080"}
	got, err := mcpAppsOrigin(cfg, true)
	if err != nil {
		t.Fatalf("resolving the views origin: %v", err)
	}
	if got == nil || got.String() != "http://10.10.0.7:8080" {
		t.Fatalf("the views origin resolved to %v, want the explicit setting", got)
	}
}

func TestNoViewsOriginIsResolvedWhenTheConnectorIsOff(t *testing.T) {
	// compose returns before it composes any resource provider when the gate is
	// off, so a fetcher built here would poll a web tier this installation never
	// asked to depend on.
	cfg := apiConfig{publicBaseURL: "https://crm.example.com", mcpAppsBaseURL: "https://views.example.com"}
	got, err := mcpAppsOrigin(cfg, false)
	if err != nil {
		t.Fatalf("resolving with the connector off: %v", err)
	}
	if got != nil {
		t.Fatalf("the connector is off and the views origin resolved to %v", got)
	}
}

func TestAMalformedViewsOriginIsABootErrorNamingTheSettingTyped(t *testing.T) {
	for _, bad := range []string{
		"https://views.example.com/apps",
		"ftp://views.example.com",
		"https://",
		"https://views.example.com?x=1",
		"not a url at all\n",
	} {
		cfg := apiConfig{publicBaseURL: "https://crm.example.com", mcpAppsBaseURL: bad}
		_, err := mcpAppsOrigin(cfg, true)
		if err == nil {
			t.Errorf("mcpAppsOrigin(%q) = nil, want a refusal", bad)
			continue
		}
		if !strings.Contains(err.Error(), "--mcp-apps-base-url") {
			t.Errorf("the refusal for %q does not name the setting the operator typed: %v", bad, err)
		}
	}
}

func TestACleartextOriginTheFetchWouldRefuseIsABootErrorInstead(t *testing.T) {
	// The two-readings failure this closes: flag parsing admitted any bare
	// origin while the fetcher refuses cleartext to anything but a literal local
	// address, so an installation started cleanly and served no views at all,
	// with the reason only in a log line nobody was watching.
	cfg := apiConfig{mcpAppsBaseURL: "http://web.internal:8080"}
	_, err := mcpAppsOrigin(cfg, true)
	if err == nil {
		t.Fatal("an origin every fetch would refuse was accepted at boot")
	}
	if !strings.Contains(err.Error(), "--mcp-apps-base-url") {
		t.Errorf("the refusal does not name the setting the operator typed: %v", err)
	}
}

func TestALocalCleartextOriginIsAccepted(t *testing.T) {
	// `make dev` and any single-host stack: refusing these would refuse the
	// normal development shape.
	for _, raw := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "http://192.168.1.9"} {
		if _, err := mcpAppsOrigin(apiConfig{mcpAppsBaseURL: raw}, true); err != nil {
			t.Errorf("mcpAppsOrigin(%q) = %v, want it accepted", raw, err)
		}
	}
}

func TestAViewsOriginCarryingACredentialIsRefusedWithoutQuotingIt(t *testing.T) {
	// Same posture validatePublicBaseURL takes: a boot error that echoed the
	// value would put the password in every log line that copies it.
	const secret = "hunter2correcthorse"
	cfg := apiConfig{mcpAppsBaseURL: "https://admin:" + secret + "@web.internal"}
	_, err := mcpAppsOrigin(cfg, true)
	if err == nil {
		t.Fatal("an origin carrying userinfo was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the refusal quoted the credential: %v", err)
	}
}
