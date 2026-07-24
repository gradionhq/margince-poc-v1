// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The person + organization family: drives the payload-builder functions
// the person/organization emit sites call — promotedPersonPayload
// (promote.go), personMergedPayload (merge.go), companySaveEventPayload
// (company.go), siteReadConfirmationPayload (companysiteread.go),
// coldStartApplyPayload (coldstartprofile.go), and relationshipUpdatedPayload
// (relationship.go) — then round-trips each result through JSON exactly as
// storekit.EmitEvent marshals it into the outbox envelope's payload column.
// There is no non-integration harness in this repo that drives a Store
// method against a real Postgres (every such test lives under
// compose/integration, gated `//go:build integration`, needing db-up);
// testing the production payload-construction functions directly — the one
// place a schema/code mismatch would show up — is the honest substitute.

import (
	"encoding/json"
	"reflect"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestPromotedPersonPayload_Created(t *testing.T) {
	person := crmcontracts.Person{FullName: "Ada Lovelace"}

	payload := promotedPersonPayload(person, false, nil)

	if !reflect.DeepEqual(payload.EventType(), "person.created") {
		t.Errorf("got %v, want %v", payload.EventType(), "person.created")
	}
	if !reflect.DeepEqual(payload.EntityType(), "person") {
		t.Errorf("got %v, want %v", payload.EntityType(), "person")
	}
	created, ok := payload.(crmcontracts.PublicEventPersonCreated)
	if !ok {
		t.Error("expected the condition to be true")
	}
	if !reflect.DeepEqual(created.FullName, "Ada Lovelace") {
		t.Errorf("got %v, want %v", created.FullName, "Ada Lovelace")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventPersonCreated
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, created) {
		t.Errorf("got %v, want %v", decoded, created)
	}
}

func TestPromotedPersonPayload_Merged(t *testing.T) {
	person := crmcontracts.Person{FullName: "Grace Hopper"}
	leadID := ids.From[ids.LeadKind](ids.NewV7())

	// The merge delta is exactly what mergeLeadIntoPerson applied — the event
	// carries it verbatim (a filled title, and converted_from_lead_id only
	// when it was actually set), not a fixed map.
	mergeFields := map[string]any{"converted_from_lead_id": leadID.UUID, "title": "VP Engineering"}
	payload := promotedPersonPayload(person, true, mergeFields)

	if !reflect.DeepEqual(payload.EventType(), "person.updated") {
		t.Errorf("got %v, want %v", payload.EventType(), "person.updated")
	}
	updated, ok := payload.(crmcontracts.PublicEventPersonUpdated)
	if !ok {
		t.Error("expected the condition to be true")
	}
	if !reflect.DeepEqual(updated.ChangedFields, mergeFields) {
		t.Errorf("got %v, want %v", updated.ChangedFields, mergeFields)
	}
}

// TestPromotedPersonPayload_MergedNoChange proves a fill-only merge that
// applied nothing emits NO person.updated (a changed_fields note with no
// fields would be a false claim).
func TestPromotedPersonPayload_MergedNoChange(t *testing.T) {
	if payload := promotedPersonPayload(crmcontracts.Person{}, true, nil); payload != nil {
		t.Errorf("no-op merge produced %v, want nil (no person.updated)", payload)
	}
}

func TestPersonMergedPayload(t *testing.T) {
	sourceID := ids.From[ids.PersonKind](ids.NewV7())
	targetID := ids.From[ids.PersonKind](ids.NewV7())
	counts := relinkCounts{Emails: 2, Phones: 1, Relationships: 3, ActivityLinks: 5}

	payload := personMergedPayload(sourceID, targetID, counts)

	if !reflect.DeepEqual(payload.EventType(), "person.merged") {
		t.Errorf("got %v, want %v", payload.EventType(), "person.merged")
	}
	if !reflect.DeepEqual(payload.EntityType(), "person") {
		t.Errorf("got %v, want %v", payload.EntityType(), "person")
	}
	if !reflect.DeepEqual(payload.MergedFromId, openapi_types.UUID(sourceID.UUID)) {
		t.Errorf("got %v, want %v", payload.MergedFromId, openapi_types.UUID(sourceID.UUID))
	}
	if !reflect.DeepEqual(payload.MergedIntoId, openapi_types.UUID(targetID.UUID)) {
		t.Errorf("got %v, want %v", payload.MergedIntoId, openapi_types.UUID(targetID.UUID))
	}
	if !reflect.DeepEqual(payload.Relinked.Emails, int64(2)) {
		t.Errorf("got %v, want %v", payload.Relinked.Emails, int64(2))
	}
	if !reflect.DeepEqual(payload.Relinked.Phones, int64(1)) {
		t.Errorf("got %v, want %v", payload.Relinked.Phones, int64(1))
	}
	if !reflect.DeepEqual(payload.Relinked.Relationships, int64(3)) {
		t.Errorf("got %v, want %v", payload.Relinked.Relationships, int64(3))
	}
	if !reflect.DeepEqual(payload.Relinked.ActivityLinks, int64(5)) {
		t.Errorf("got %v, want %v", payload.Relinked.ActivityLinks, int64(5))
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventPersonMerged
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestCompanySaveEventPayload_Created(t *testing.T) {
	applied := map[string]any{"display_name": "Acme GmbH"}

	payload := companySaveEventPayload(true, applied, "human:u1")

	if !reflect.DeepEqual(payload.EventType(), "organization.created") {
		t.Errorf("got %v, want %v", payload.EventType(), "organization.created")
	}
	created, ok := payload.(crmcontracts.PublicEventOrganizationCreated)
	if !ok {
		t.Error("expected the condition to be true")
	}
	if created.Delta == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*created.Delta, applied) {
		t.Errorf("got %v, want %v", *created.Delta, applied)
	}
	if created.Source == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*created.Source, "human") {
		t.Errorf("got %v, want %v", *created.Source, "human")
	}
	if created.Anchor == nil {
		t.Fatalf("expected non-nil value")
	}
	if !(*created.Anchor) {
		t.Error("expected the condition to be true")
	}
	if created.CapturedBy == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*created.CapturedBy, "human:u1") {
		t.Errorf("got %v, want %v", *created.CapturedBy, "human:u1")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventOrganizationCreated
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, created) {
		t.Errorf("got %v, want %v", decoded, created)
	}
}

func TestCompanySaveEventPayload_Updated(t *testing.T) {
	applied := map[string]any{"display_name": "Acme GmbH"}

	payload := companySaveEventPayload(false, applied, "human:u1")

	if !reflect.DeepEqual(payload.EventType(), "organization.updated") {
		t.Errorf("got %v, want %v", payload.EventType(), "organization.updated")
	}
	updated, ok := payload.(crmcontracts.PublicEventOrganizationUpdated)
	if !ok {
		t.Error("expected the condition to be true")
	}
	if !reflect.DeepEqual(updated.ChangedFields["delta"], applied) {
		t.Errorf("got %v, want %v", updated.ChangedFields["delta"], applied)
	}
	if !reflect.DeepEqual(updated.ChangedFields["source"], "human") {
		t.Errorf("got %v, want %v", updated.ChangedFields["source"], "human")
	}
	if !reflect.DeepEqual(updated.ChangedFields["anchor"], true) {
		t.Errorf("got %v, want %v", updated.ChangedFields["anchor"], true)
	}
	if !reflect.DeepEqual(updated.ChangedFields["captured_by"], "human:u1") {
		t.Errorf("got %v, want %v", updated.ChangedFields["captured_by"], "human:u1")
	}
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

	if !reflect.DeepEqual(payload.EventType(), "organization.created") {
		t.Errorf("got %v, want %v", payload.EventType(), "organization.created")
	}
	created, ok := payload.(crmcontracts.PublicEventOrganizationCreated)
	if !ok {
		t.Error("expected the condition to be true")
	}
	if created.Delta == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual((*created.Delta)["fields"], confirmation.appliedSite) {
		t.Errorf("got %v, want %v", (*created.Delta)["fields"], confirmation.appliedSite)
	}
	if created.SourceUrl == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*created.SourceUrl, "https://acme.example") {
		t.Errorf("got %v, want %v", *created.SourceUrl, "https://acme.example")
	}
	if created.SiteReadId == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*created.SiteReadId, openapi_types.UUID(read.ID)) {
		t.Errorf("got %v, want %v", *created.SiteReadId, openapi_types.UUID(read.ID))
	}
	if created.CapturedBy == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*created.CapturedBy, companySiteReadCapturedBy) {
		t.Errorf("got %v, want %v", *created.CapturedBy, companySiteReadCapturedBy)
	}
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

	if !reflect.DeepEqual(payload.EventType(), "organization.updated") {
		t.Errorf("got %v, want %v", payload.EventType(), "organization.updated")
	}
	updated, ok := payload.(crmcontracts.PublicEventOrganizationUpdated)
	if !ok {
		t.Error("expected the condition to be true")
	}
	if !reflect.DeepEqual(updated.ChangedFields["source_url"], "https://acme.example") {
		t.Errorf("got %v, want %v", updated.ChangedFields["source_url"], "https://acme.example")
	}
	if !reflect.DeepEqual(updated.ChangedFields["site_read_id"], read.ID) {
		t.Errorf("got %v, want %v", updated.ChangedFields["site_read_id"], read.ID)
	}
}

func TestColdStartApplyPayload_Created(t *testing.T) {
	in := ApplyColdStartProfileInput{
		SourceURL: "https://acme.example",
		Fields:    []ColdStartFieldInput{{Field: "legal_name", Value: "Acme GmbH"}},
	}

	payload := coldStartApplyPayload(true, in, "acme.example", "agent:coldstart", nil)

	if !reflect.DeepEqual(payload.EventType(), "organization.created") {
		t.Errorf("got %v, want %v", payload.EventType(), "organization.created")
	}
	created, ok := payload.(crmcontracts.PublicEventOrganizationCreated)
	if !ok {
		t.Error("expected the condition to be true")
	}
	if created.DisplayName == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*created.DisplayName, "Acme GmbH") {
		t.Errorf("got %v, want %v", *created.DisplayName, "Acme GmbH")
	}
	if created.PrimaryDomain == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*created.PrimaryDomain, "acme.example") {
		t.Errorf("got %v, want %v", *created.PrimaryDomain, "acme.example")
	}
	if created.CapturedBy == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*created.CapturedBy, "agent:coldstart") {
		t.Errorf("got %v, want %v", *created.CapturedBy, "agent:coldstart")
	}
}

func TestColdStartApplyPayload_CreatedFromDomainFallback(t *testing.T) {
	// No legal_name accepted: the created event must publish the domain-derived
	// display name ("Docusign"), never the raw host ("eu.docusign.net") or an
	// empty name — matching the value resolveOrCreateColdStartOrg stores.
	in := ApplyColdStartProfileInput{
		SourceURL: "https://eu.docusign.net",
		Fields:    []ColdStartFieldInput{{Field: "industry", Value: "software"}},
	}

	payload := coldStartApplyPayload(true, in, "eu.docusign.net", "agent:coldstart", nil)

	created, ok := payload.(crmcontracts.PublicEventOrganizationCreated)
	if !ok {
		t.Fatal("expected an organization.created payload")
	}
	if created.DisplayName == nil || *created.DisplayName != "Docusign" {
		t.Fatalf("display_name = %v, want the domain-derived \"Docusign\"", created.DisplayName)
	}
}

func TestColdStartApplyPayload_Updated(t *testing.T) {
	in := ApplyColdStartProfileInput{SourceURL: "https://acme.example"}
	applied := map[string]any{"industry": "software"}

	payload := coldStartApplyPayload(false, in, "acme.example", "agent:coldstart", applied)

	if !reflect.DeepEqual(payload.EventType(), "organization.updated") {
		t.Errorf("got %v, want %v", payload.EventType(), "organization.updated")
	}
	updated, ok := payload.(crmcontracts.PublicEventOrganizationUpdated)
	if !ok {
		t.Error("expected the condition to be true")
	}
	if !reflect.DeepEqual(updated.ChangedFields["delta"], applied) {
		t.Errorf("got %v, want %v", updated.ChangedFields["delta"], applied)
	}
	if !reflect.DeepEqual(updated.ChangedFields["source_url"], "https://acme.example") {
		t.Errorf("got %v, want %v", updated.ChangedFields["source_url"], "https://acme.example")
	}
}

func TestRelationshipUpdatedPayload_PerAnchor(t *testing.T) {
	delta := map[string]any{"delta": map[string]any{"relationship": map[string]any{"action": "create"}}}

	dealPayload := relationshipUpdatedPayload("deal", delta)
	if !reflect.DeepEqual(dealPayload.EventType(), "deal.updated") {
		t.Errorf("got %v, want %v", dealPayload.EventType(), "deal.updated")
	}
	if !reflect.DeepEqual(dealPayload.(crmcontracts.PublicEventDealUpdated).ChangedFields, delta) {
		t.Errorf("got %v, want %v", dealPayload.(crmcontracts.PublicEventDealUpdated).ChangedFields, delta)
	}

	personPayload := relationshipUpdatedPayload("person", delta)
	if !reflect.DeepEqual(personPayload.EventType(), "person.updated") {
		t.Errorf("got %v, want %v", personPayload.EventType(), "person.updated")
	}
	if !reflect.DeepEqual(personPayload.(crmcontracts.PublicEventPersonUpdated).ChangedFields, delta) {
		t.Errorf("got %v, want %v", personPayload.(crmcontracts.PublicEventPersonUpdated).ChangedFields, delta)
	}

	orgPayload := relationshipUpdatedPayload("organization", delta)
	if !reflect.DeepEqual(orgPayload.EventType(), "organization.updated") {
		t.Errorf("got %v, want %v", orgPayload.EventType(), "organization.updated")
	}
	if !reflect.DeepEqual(orgPayload.(crmcontracts.PublicEventOrganizationUpdated).ChangedFields, delta) {
		t.Errorf("got %v, want %v", orgPayload.(crmcontracts.PublicEventOrganizationUpdated).ChangedFields, delta)
	}
}
