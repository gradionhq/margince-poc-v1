// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The dossier and the receipts behind it, over the real handler stack.
//
// These two surfaces are only honest together. The dossier's whole claim is
// that a reader can check any sentence it writes, and that claim is worth
// exactly as much as the receipt endpoint's ability to answer for the record it
// cited. Proving them apart proves neither.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type dossierResponse struct {
	OrganizationID string `json:"organization_id"`
	GeneratedBy    string `json:"generated_by"`
	NeedsRefresh   *bool  `json:"needs_refresh"`
	Sections       []struct {
		Kind      string `json:"kind"`
		Sentences []struct {
			Text     string `json:"text"`
			Nature   string `json:"nature"`
			Evidence []struct {
				EntityType string `json:"entity_type"`
				EntityID   string `json:"entity_id"`
			} `json:"evidence"`
		} `json:"sentences"`
	} `json:"sections"`
}

type receiptResponse struct {
	EntityType string         `json:"entity_type"`
	EntityID   string         `json:"entity_id"`
	SourceKind string         `json:"source_kind"`
	Label      string         `json:"label"`
	Value      string         `json:"value"`
	Excerpt    *string        `json:"excerpt"`
	Identity   map[string]any `json:"identity"`
	ProducedBy string         `json:"produced_by"`
	Gaps       []string       `json:"gaps"`
}

// Every sentence the dossier writes must be openable, and this is the only
// place that can be shown: the citation is minted by one endpoint and resolved
// by another, and a unit test on either side supplies its own ids.
func TestEverySentenceTheDossierWritesCanBeOpened(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	orgID := createBareOrganization(t, e)
	seedRequiredProfileFields(t, e, orgID)

	var dossier dossierResponse
	if status := e.Call(t, "GET", "/v1/organizations/"+orgID+"/dossier", nil, nil, &dossier); status != http.StatusOK {
		t.Fatalf("GET dossier = %d, want 200", status)
	}
	if dossier.GeneratedBy != "deterministic" {
		t.Errorf("generated_by = %q, want deterministic — no model lane is wired", dossier.GeneratedBy)
	}
	if len(dossier.Sections) == 0 {
		t.Fatal("no sections: three recorded profile fields describe something")
	}

	opened := 0
	for _, section := range dossier.Sections {
		for _, sentence := range section.Sentences {
			if len(sentence.Evidence) == 0 {
				t.Fatalf("a sentence carries no evidence: %q", sentence.Text)
			}
			for _, cited := range sentence.Evidence {
				var receipt receiptResponse
				path := "/v1/organizations/" + orgID + "/evidence/" + cited.EntityType + "/" + cited.EntityID
				if status := e.Call(t, "GET", path, nil, nil, &receipt); status != http.StatusOK {
					t.Errorf("the dossier cited %s %s and the receipt answered %d",
						cited.EntityType, cited.EntityID, status)
					continue
				}
				if receipt.Value == "" || receipt.ProducedBy == "" {
					t.Errorf("receipt for %s is missing its value or its capturer: %+v",
						cited.EntityType, receipt)
				}
				opened++
			}
		}
	}
	if opened == 0 {
		t.Fatal("no citation was resolved, so nothing was actually proven openable")
	}
}

// A record this company does not hold is absent, whether it belongs to another
// company or to nobody. Both answer 404: the existence of a record the reader
// may not see is itself a disclosure.
func TestAReceiptForARecordThisCompanyDoesNotHoldIsNotFound(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	orgID := createBareOrganization(t, e)
	seedRequiredProfileFields(t, e, orgID)
	other := createBareOrganization(t, e)

	var mine dossierResponse
	if status := e.Call(t, "GET", "/v1/organizations/"+orgID+"/dossier", nil, nil, &mine); status != http.StatusOK {
		t.Fatalf("GET dossier = %d, want 200", status)
	}
	// Named rather than indexed into: an empty dossier here means the setup
	// stopped describing the company, and that must read as the setup failing
	// rather than as an index panic in the disclosure check below.
	if len(mine.Sections) == 0 || len(mine.Sections[0].Sentences) == 0 ||
		len(mine.Sections[0].Sentences[0].Evidence) == 0 {
		t.Fatalf("the dossier cited nothing, so there is no record to ask for: %+v", mine.Sections)
	}
	cited := mine.Sections[0].Sentences[0].Evidence[0]

	// The SAME record id, asked for under a company that does not hold it.
	path := "/v1/organizations/" + other + "/evidence/" + cited.EntityType + "/" + cited.EntityID
	if status := e.Call(t, "GET", path, nil, nil, nil); status != http.StatusNotFound {
		t.Errorf("a record of another company answered %d, want 404", status)
	}

	// And a kind that has no receipt to write.
	orgPath := "/v1/organizations/" + orgID + "/evidence/organization/" + orgID
	if status := e.Call(t, "GET", orgPath, nil, nil, nil); status != http.StatusNotFound {
		t.Errorf("the organization citation answered %d, want 404 — it carries no provenance", status)
	}
}

// A dossier assembled from sources read long ago still renders, and says so.
// Hiding it would leave the reader with nothing rather than something dated.
func TestADossierOverOldSourcesRendersAndSaysItIsStale(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	orgID := createBareOrganization(t, e)
	seedProfileFieldWrittenAt(t, e, orgID, "offer_summary",
		"Load-shifting software", time.Now().UTC().Add(-400*24*time.Hour))

	var dossier dossierResponse
	if status := e.Call(t, "GET", "/v1/organizations/"+orgID+"/dossier", nil, nil, &dossier); status != http.StatusOK {
		t.Fatalf("GET dossier = %d, want 200", status)
	}

	if len(dossier.Sections) == 0 {
		t.Fatal("a stale dossier rendered nothing; it is more useful than none")
	}
	if dossier.NeedsRefresh == nil || !*dossier.NeedsRefresh {
		t.Error("a dossier whose only source was read over a year ago did not say it is stale")
	}
}

// A company nobody has described is not an error, and a refresh over it still
// answers rather than failing.
func TestARefreshOverACompanyWithNothingRecordedStillAnswers(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	orgID := createBareOrganization(t, e)

	var refreshed dossierResponse
	if status := e.Call(t, "POST", "/v1/organizations/"+orgID+"/dossier", nil, nil, &refreshed); status != http.StatusOK {
		t.Fatalf("POST dossier = %d, want 200", status)
	}
	if len(refreshed.Sections) != 0 {
		t.Errorf("sections = %+v, want none — nothing is recorded about this company", refreshed.Sections)
	}
	if refreshed.NeedsRefresh != nil && *refreshed.NeedsRefresh {
		t.Error("a company with no source at all was reported stale; undated is not stale")
	}
}

// seedProfileFieldWrittenAt records one machine-read value with an explicit
// write time, which is what freshness is measured from today.
func seedProfileFieldWrittenAt(
	t *testing.T, e *apptest.AppEnv, orgID, field, value string, written time.Time,
) {
	t.Helper()
	var workspaceID ids.UUID
	if err := e.Owner.QueryRow(context.Background(),
		`SELECT id FROM workspace WHERE slug = $1`, e.Slug).Scan(&workspaceID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	if _, err := e.Owner.Exec(context.Background(), `
		INSERT INTO organization_profile_field (
			id, workspace_id, organization_id, field, value, source,
			evidence_snippet, source_url, confidence, captured_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'site_read', $6, 'https://voltaq.example/about', 0.9,
		        'site_read:seed', $7)`,
		ids.NewV7(), workspaceID, orgID, field, value, value, written); err != nil {
		t.Fatalf("seed profile field %s: %v", field, err)
	}
}
