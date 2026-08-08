// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The growth-fit HTTP surface end to end (DOSS-WIRE-2): a company with nothing
// recorded abstains and says what to go and find; recording some of the required
// inputs moves the completeness figure the abstention rests on.
//
// The counting cannot be proven anywhere but here. Completeness is decided from
// columns the sidecar readers project and from rows the real writers shape, and
// a unit test supplies both itself. Only a real database can show that the
// reader actually fetches what the counting reads.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The admin BootstrapWorkspace signs in as. Named here because this suite has
// to look the acting reader up by identity to prove the cache row is keyed to
// them rather than shared.
const bootstrappedAdminEmail = "ada@example.com"

type growthFitResponse struct {
	OrganizationID   string  `json:"organization_id"`
	Band             string  `json:"band"`
	BandCappedReason *string `json:"band_capped_reason"`
	NextStep         *string `json:"next_step"`
	GeneratedBy      string  `json:"generated_by"`
	DataCompleteness struct {
		Present  int      `json:"present"`
		Expected int      `json:"expected"`
		Missing  []string `json:"missing"`
	} `json:"data_completeness"`
}

func TestGrowthFitAbstainsAndNamesWhatToGather(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	orgID := createBareOrganization(t, e)

	// A company we hold nothing about. The panel must not read as "poor fit".
	var fresh growthFitResponse
	if status := e.Call(t, "GET", "/v1/organizations/"+orgID+"/growth-fit", nil, nil, &fresh); status != http.StatusOK {
		t.Fatalf("GET growth-fit = %d, want 200", status)
	}
	if fresh.Band != "unknown" {
		t.Errorf("band = %q, want unknown — nothing is recorded about this company", fresh.Band)
	}
	if fresh.DataCompleteness.Expected == 0 {
		t.Error("expected = 0 — a completeness figure without its denominator is not one")
	}
	if fresh.DataCompleteness.Present != 0 {
		t.Errorf("present = %d, want 0 for a company with no profile fields and no facts", fresh.DataCompleteness.Present)
	}
	if len(fresh.DataCompleteness.Missing) != fresh.DataCompleteness.Expected {
		t.Errorf("missing named %d of %d absent inputs — the next step is built from this list",
			len(fresh.DataCompleteness.Missing), fresh.DataCompleteness.Expected)
	}
	if fresh.NextStep == nil || *fresh.NextStep == "" {
		t.Error("an abstention with no next step leaves the reader nothing to act on")
	}
	if fresh.GeneratedBy != "deterministic" {
		t.Errorf("generated_by = %q, want deterministic — no model lane is wired", fresh.GeneratedBy)
	}

	// Record three of the required inputs the way a site read would — two
	// written recently, one written long ago — then force a re-assembly. Only
	// the two fresh ones may count, and the figure must MOVE, because a reader
	// that is not seeing rows the database holds looks exactly like a company
	// with nothing recorded.
	seedRequiredProfileFields(t, e, orgID)

	var enriched growthFitResponse
	if status := e.Call(t, "POST", "/v1/organizations/"+orgID+"/growth-fit", nil, nil, &enriched); status != http.StatusOK {
		t.Fatalf("POST growth-fit = %d, want 200", status)
	}
	if enriched.DataCompleteness.Present != 2 {
		t.Errorf("present = %d, want 2 — three fields were recorded and one is too old to count",
			enriched.DataCompleteness.Present)
	}
	// The stale one is still OUTSTANDING, so it must still be named as
	// something to go and get. A value too old to rest a judgment on is a gap,
	// not a fact we hold.
	if !namesMissingInput(enriched.DataCompleteness.Missing, "their industry") {
		t.Errorf("missing = %v, want it to still name the stale industry value",
			enriched.DataCompleteness.Missing)
	}
	if enriched.DataCompleteness.Expected != fresh.DataCompleteness.Expected {
		t.Errorf("expected moved from %d to %d — the denominator is the required set, which did not change",
			fresh.DataCompleteness.Expected, enriched.DataCompleteness.Expected)
	}
	// Still short of the floor, so still an abstention — and the two inputs we
	// now hold must have dropped out of what it asks for.
	if enriched.Band != "unknown" {
		t.Errorf("band = %q, want unknown — two of seven is below the abstention floor", enriched.Band)
	}
	for _, named := range enriched.DataCompleteness.Missing {
		if named == "what they offer" || named == "who they sell to" {
			t.Errorf("missing still names %q, which was just recorded", named)
		}
	}
}

// The assembly is cached PER READER, and that is a disclosure guarantee rather
// than an optimisation: a growth fit can rest on records scoped to one reader.
// The row must therefore be keyed to the human who asked for it.
func TestTheGrowthFitCacheRowIsKeyedToTheReaderWhoAskedForIt(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	orgID := createBareOrganization(t, e)

	if status := e.Call(t, "GET", "/v1/organizations/"+orgID+"/growth-fit", nil, nil, nil); status != http.StatusOK {
		t.Fatalf("GET growth-fit = %d, want 200", status)
	}

	var cachedFor ids.UUID
	if err := e.Owner.QueryRow(context.Background(),
		`SELECT user_id FROM org_growth_fit WHERE organization_id = $1`, orgID).Scan(&cachedFor); err != nil {
		t.Fatalf("the read cached no row for this company: %v", err)
	}
	var acting ids.UUID
	if err := e.Owner.QueryRow(context.Background(),
		`SELECT id FROM app_user WHERE email = $1`, bootstrappedAdminEmail).Scan(&acting); err != nil {
		t.Fatalf("acting user lookup: %v", err)
	}
	if cachedFor != acting {
		t.Errorf("cached row user_id = %s, want the acting reader %s — a shared row would serve one reader's assembly to another",
			cachedFor, acting)
	}
}

func namesMissingInput(missing []string, want string) bool {
	for _, named := range missing {
		if named == want {
			return true
		}
	}
	return false
}

func createBareOrganization(t *testing.T, e *apptest.AppEnv) string {
	t.Helper()
	var org struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/organizations", apptest.AnyMap{
		"display_name": "Voltaq Systems GmbH", "source": "ui",
	}, nil, &org); status != http.StatusCreated {
		t.Fatalf("create organization = %d, want 201", status)
	}
	return org.ID
}

// seedRequiredProfileFields writes three of the required inputs the way the
// site-read path actually leaves them: machine-sourced, carrying the evidence
// such a row must have, and with `retrieved_at` UNSET — migration 0194 adds
// that column and nothing in this tree writes it yet, so a row carrying one is
// a shape no writer produces.
//
// Freshness therefore runs off `updated_at`, which is what production measures
// today. Two rows are written now and count; the third is backdated past the
// window and must not, which is the boundary the served figure rests on.
func seedRequiredProfileFields(t *testing.T, e *apptest.AppEnv, orgID string) {
	t.Helper()
	var workspaceID ids.UUID
	if err := e.Owner.QueryRow(context.Background(),
		`SELECT id FROM workspace WHERE slug = $1`, e.Slug).Scan(&workspaceID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	// updated_at is written explicitly because the row's own trigger stamps
	// now() on every write, so a backdated value cannot be produced by an
	// UPDATE after the fact.
	const insert = `
		INSERT INTO organization_profile_field (
			id, workspace_id, organization_id, field, value, source,
			evidence_snippet, source_url, confidence, captured_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'site_read', $6, 'https://voltaq.example/about', 0.9,
		        'site_read:seed', $7)`
	now := time.Now().UTC()
	for field, seed := range map[string]struct {
		value   string
		written time.Time
	}{
		"offer_summary": {"Load-shifting software for industrial sites", now.Add(-24 * time.Hour)},
		"icp":           {"Energy-intensive manufacturers", now.Add(-24 * time.Hour)},
		"industry":      {"Industrial software", now.Add(-400 * 24 * time.Hour)},
	} {
		if _, err := e.Owner.Exec(context.Background(), insert,
			ids.NewV7(), workspaceID, orgID, field, seed.value, seed.value, seed.written); err != nil {
			t.Fatalf("seed profile field %s: %v", field, err)
		}
	}
}
