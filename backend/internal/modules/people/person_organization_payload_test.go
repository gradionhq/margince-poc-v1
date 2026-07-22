// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// TDD Step 1 of the webhooks Task 5b-personorg migration (person +
// organization family): drives the payload-builder functions the person/
// organization emit sites call — promotedPersonPayload (promote.go),
// personMergedPayload (merge.go), companySaveEventPayload (company.go),
// siteReadConfirmationPayload (companysiteread.go), coldStartApplyPayload
// (coldstartprofile.go), and relationshipUpdatedPayload
// (relationship.go) — then round-trips each result through JSON exactly
// as storekit.EmitEvent marshals it into the outbox envelope's payload
// column. There is no non-integration harness in this repo that drives a
// Store method against a real Postgres (every such test lives under
// compose/integration, gated `//go:build integration`, needing db-up);
// testing the production payload-construction functions directly — the
// one place a schema/code mismatch would show up — is the honest
// substitute, mirroring the deal family's
// TestDealStageChangedEmitsTypedPayload (webhooks Task 5a-i).
//
// Before this migration none of crmcontracts.WebhookPayloadPersonCreated/
// Archived/Merged/Updated/Restored or WebhookPayloadOrganizationCreated/
// Archived/Merged/Updated existed, and none of the builder functions
// existed, so this test failed to compile (RED) until public-events.yaml
// gained the schemas, `make gen` regenerated the structs, and
// promote.go/merge.go/company.go/companysiteread.go/coldstartprofile.go/
// relationship.go grew the builders.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestPromotedPersonPayload_Created(t *testing.T) {
	person := crmcontracts.Person{FullName: "Ada Lovelace"}
	leadID := ids.From[ids.LeadKind](ids.NewV7())

	payload := promotedPersonPayload(person, false, leadID)

	require.Equal(t, "person.created", payload.EventType())
	require.Equal(t, "person", payload.EntityType())
	created, ok := payload.(crmcontracts.WebhookPayloadPersonCreated)
	require.True(t, ok)
	require.Equal(t, "Ada Lovelace", created.FullName)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadPersonCreated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, created, decoded)
}

func TestPromotedPersonPayload_Merged(t *testing.T) {
	person := crmcontracts.Person{FullName: "Grace Hopper"}
	leadID := ids.From[ids.LeadKind](ids.NewV7())

	payload := promotedPersonPayload(person, true, leadID)

	require.Equal(t, "person.updated", payload.EventType())
	updated, ok := payload.(crmcontracts.WebhookPayloadPersonUpdated)
	require.True(t, ok)
	require.Equal(t, leadID, updated.ChangedFields["converted_from_lead_id"])
}

func TestPersonMergedPayload(t *testing.T) {
	sourceID := ids.From[ids.PersonKind](ids.NewV7())
	targetID := ids.From[ids.PersonKind](ids.NewV7())
	counts := relinkCounts{Emails: 2, Phones: 1, Relationships: 3, ActivityLinks: 5}

	payload := personMergedPayload(sourceID, targetID, counts)

	require.Equal(t, "person.merged", payload.EventType())
	require.Equal(t, "person", payload.EntityType())
	require.Equal(t, openapi_types.UUID(sourceID.UUID), payload.MergedFromId)
	require.Equal(t, openapi_types.UUID(targetID.UUID), payload.MergedIntoId)
	require.Equal(t, int64(2), payload.Relinked.Emails)
	require.Equal(t, int64(1), payload.Relinked.Phones)
	require.Equal(t, int64(3), payload.Relinked.Relationships)
	require.Equal(t, int64(5), payload.Relinked.ActivityLinks)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadPersonMerged
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestCompanySaveEventPayload_Created(t *testing.T) {
	applied := map[string]any{"display_name": "Acme GmbH"}

	payload := companySaveEventPayload(true, applied, "human:u1")

	require.Equal(t, "organization.created", payload.EventType())
	created, ok := payload.(crmcontracts.WebhookPayloadOrganizationCreated)
	require.True(t, ok)
	require.NotNil(t, created.Delta)
	require.Equal(t, applied, *created.Delta)
	require.NotNil(t, created.Source)
	require.Equal(t, "human", *created.Source)
	require.NotNil(t, created.Anchor)
	require.True(t, *created.Anchor)
	require.NotNil(t, created.CapturedBy)
	require.Equal(t, "human:u1", *created.CapturedBy)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadOrganizationCreated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, created, decoded)
}

func TestCompanySaveEventPayload_Updated(t *testing.T) {
	applied := map[string]any{"display_name": "Acme GmbH"}

	payload := companySaveEventPayload(false, applied, "human:u1")

	require.Equal(t, "organization.updated", payload.EventType())
	updated, ok := payload.(crmcontracts.WebhookPayloadOrganizationUpdated)
	require.True(t, ok)
	require.Equal(t, applied, updated.ChangedFields["delta"])
	require.Equal(t, "human", updated.ChangedFields["source"])
	require.Equal(t, true, updated.ChangedFields["anchor"])
	require.Equal(t, "human:u1", updated.ChangedFields["captured_by"])
}

func TestSiteReadConfirmationPayload_Created(t *testing.T) {
	read := SiteRead{ID: ids.NewV7(), SeedURL: "https://acme.example"}
	confirmation := siteReadConfirmation{
		created:      true,
		appliedSite:  map[string]any{"legal_name": "Acme GmbH"},
		appliedHuman: map[string]any{},
		appliedFacts: []map[string]any{{"category": "contact"}},
	}

	payload := siteReadConfirmationPayload(read, confirmation)

	require.Equal(t, "organization.created", payload.EventType())
	created, ok := payload.(crmcontracts.WebhookPayloadOrganizationCreated)
	require.True(t, ok)
	require.NotNil(t, created.Delta)
	require.Equal(t, confirmation.appliedSite, (*created.Delta)["fields"])
	require.NotNil(t, created.SourceUrl)
	require.Equal(t, "https://acme.example", *created.SourceUrl)
	require.NotNil(t, created.SiteReadId)
	require.Equal(t, openapi_types.UUID(read.ID), *created.SiteReadId)
	require.NotNil(t, created.CapturedBy)
	require.Equal(t, companySiteReadCapturedBy, *created.CapturedBy)
}

func TestSiteReadConfirmationPayload_Updated(t *testing.T) {
	read := SiteRead{ID: ids.NewV7(), SeedURL: "https://acme.example"}
	confirmation := siteReadConfirmation{
		created:      false,
		appliedSite:  map[string]any{"legal_name": "Acme GmbH"},
		appliedHuman: map[string]any{},
		appliedFacts: []map[string]any{{"category": "contact"}},
	}

	payload := siteReadConfirmationPayload(read, confirmation)

	require.Equal(t, "organization.updated", payload.EventType())
	updated, ok := payload.(crmcontracts.WebhookPayloadOrganizationUpdated)
	require.True(t, ok)
	require.Equal(t, "https://acme.example", updated.ChangedFields["source_url"])
	require.Equal(t, read.ID, updated.ChangedFields["site_read_id"])
}

func TestColdStartApplyPayload_Created(t *testing.T) {
	in := ApplyColdStartProfileInput{
		SourceURL: "https://acme.example",
		Fields:    []ColdStartFieldInput{{Field: "legal_name", Value: "Acme GmbH"}},
	}

	payload := coldStartApplyPayload(true, in, "acme.example", "agent:coldstart", nil)

	require.Equal(t, "organization.created", payload.EventType())
	created, ok := payload.(crmcontracts.WebhookPayloadOrganizationCreated)
	require.True(t, ok)
	require.NotNil(t, created.DisplayName)
	require.Equal(t, "Acme GmbH", *created.DisplayName)
	require.NotNil(t, created.PrimaryDomain)
	require.Equal(t, "acme.example", *created.PrimaryDomain)
	require.NotNil(t, created.CapturedBy)
	require.Equal(t, "agent:coldstart", *created.CapturedBy)
}

func TestColdStartApplyPayload_Updated(t *testing.T) {
	in := ApplyColdStartProfileInput{SourceURL: "https://acme.example"}
	applied := map[string]any{"industry": "software"}

	payload := coldStartApplyPayload(false, in, "acme.example", "agent:coldstart", applied)

	require.Equal(t, "organization.updated", payload.EventType())
	updated, ok := payload.(crmcontracts.WebhookPayloadOrganizationUpdated)
	require.True(t, ok)
	require.Equal(t, applied, updated.ChangedFields["delta"])
	require.Equal(t, "https://acme.example", updated.ChangedFields["source_url"])
}

func TestRelationshipUpdatedPayload_PerAnchor(t *testing.T) {
	delta := map[string]any{"delta": map[string]any{"relationship": map[string]any{"action": "create"}}}

	dealPayload := relationshipUpdatedPayload("deal", delta)
	require.Equal(t, "deal.updated", dealPayload.EventType())
	require.Equal(t, delta, dealPayload.(crmcontracts.WebhookPayloadDealUpdated).ChangedFields)

	personPayload := relationshipUpdatedPayload("person", delta)
	require.Equal(t, "person.updated", personPayload.EventType())
	require.Equal(t, delta, personPayload.(crmcontracts.WebhookPayloadPersonUpdated).ChangedFields)

	orgPayload := relationshipUpdatedPayload("organization", delta)
	require.Equal(t, "organization.updated", orgPayload.EventType())
	require.Equal(t, delta, orgPayload.(crmcontracts.WebhookPayloadOrganizationUpdated).ChangedFields)
}
