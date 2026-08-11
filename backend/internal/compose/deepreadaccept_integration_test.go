// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// What ACCEPTANCE does to the record, which is a different question from
// whether the read ran. The lifecycle suite next door proves the worker
// crawls, extracts, stages one proposal and records an honest outcome; these
// prove what the organization becomes once a human answers that proposal.
//
// Three rules, each a place where a careless apply would look identical to a
// correct one on the dossier alone: offerings dedupe on their value key and
// never overwrite what a human already put there, an accepted employee_range
// fills the size band only where the mapping is unambiguous, and a rejection
// lands nothing at all.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestDeepReadOfferingsDedupeOnValueKeyAndAcceptRespectsHumanPrecedence(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	done, svc := runServicesDeepRead(t, e, org)

	// The staged payload carries ONE service row — the higher-confidence
	// spelling of the shared value_key — plus the product.
	var proposedChange []byte
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT proposed_change FROM approval WHERE id = $1`, done.ProposalIDs[0]).Scan(&proposedChange)
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := people.UnmarshalDeepRead(proposedChange)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Facts) != 2 {
		t.Fatalf("staged facts = %+v, want the deduped service + the product", proposal.Facts)
	}
	service := proposal.Facts[0]
	// The citation gate is binary (no model confidence), so a value_key
	// duplicate keeps its FIRST spelling — deterministic, page-ordered.
	if service.Field != "service" || service.ValueKey != "crm rollout" || service.Value != "CRM Rollout — implementation projects" {
		t.Fatalf("staged service = %+v, want the first-seen spelling under value_key 'crm rollout'", service)
	}

	// A human has since claimed the service fact; the accept must land the
	// product beside it and leave the human's row untouched.
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization_fact
			  (workspace_id, organization_id, category, field, value, value_key,
			   evidence_snippet, source_url, confidence, source, captured_by)
			VALUES ($1, $2, 'offering', 'service', 'CRM Rollout (human curated)', 'crm rollout',
			        'set by hand', '', 1, 'human', $3)`,
			e.WS, org, "human:"+e.Rep1.String())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](done.ProposalIDs[0]), true, nil); err != nil {
		t.Fatalf("accept: %v", err)
	}

	var factRows int
	var serviceValue, serviceCapturedBy, productValue string
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM organization_fact WHERE organization_id = $1`, org).Scan(&factRows); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT value, captured_by FROM organization_fact
			 WHERE organization_id = $1 AND field = 'service' AND value_key = 'crm rollout'`,
			org).Scan(&serviceValue, &serviceCapturedBy); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT coalesce(max(value), '') FROM organization_fact
			 WHERE organization_id = $1 AND field = 'product'`, org).Scan(&productValue)
	})
	if err != nil {
		t.Fatal(err)
	}
	if factRows != 2 {
		t.Fatalf("%d organization_fact rows after accept, want 2 (the human's service + the landed product)", factRows)
	}
	if serviceValue != "CRM Rollout (human curated)" || serviceCapturedBy != "human:"+e.Rep1.String() {
		t.Fatalf("service row = %q by %q — the accept overwrote a human-claimed fact", serviceValue, serviceCapturedBy)
	}
	if productValue != "Margince — our CRM product" {
		t.Fatalf("product row = %q, want the staged product landed beside the human's row", productValue)
	}
}

func TestAcceptedEmployeeRangeFactFillsSizeBandWhenUnambiguous(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	employeeRangeFact := func(value string) []people.DeepReadFact {
		return []people.DeepReadFact{{
			Category: "company", Field: "employee_range", Value: value,
			EvidenceSnippet: "our team of " + value, SourceURL: "https://acme.example/about", Confidence: 0.9,
		}}
	}
	readSizeBand := func(org ids.UUID) *string {
		var sizeBand *string
		if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`SELECT size_band FROM organization WHERE id = $1`, org).Scan(&sizeBand)
		}); err != nil {
			t.Fatalf("reading size_band: %v", err)
		}
		return sizeBand
	}

	// A cleanly-phrased range fills the chip's column on accept.
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	if err := store.ApplyDeepRead(ctx, people.DeepReadProposal{
		OrganizationID: ids.From[ids.OrganizationKind](org),
		SourceURL:      "https://acme.example",
		Facts:          employeeRangeFact("25 to 50"),
	}); err != nil {
		t.Fatalf("ApplyDeepRead: %v", err)
	}
	if got := readSizeBand(org); got == nil || *got != "11-50" {
		t.Fatalf("size_band after accept = %v, want 11-50", got)
	}

	// A later read never overwrites the standing value — fill-once.
	if err := store.ApplyDeepRead(ctx, people.DeepReadProposal{
		OrganizationID: ids.From[ids.OrganizationKind](org),
		SourceURL:      "https://acme.example",
		Facts:          employeeRangeFact("about 300 people"),
	}); err != nil {
		t.Fatalf("second ApplyDeepRead: %v", err)
	}
	if got := readSizeBand(org); got == nil || *got != "11-50" {
		t.Fatalf("size_band after re-accept = %v, want the first fill kept", got)
	}

	// A range spanning two bands abstains: the fact lands as evidence, the
	// column stays empty rather than holding a guess.
	vague := insertOrg(t, e, e.Rep1, "vague.example", "")
	if err := store.ApplyDeepRead(ctx, people.DeepReadProposal{
		OrganizationID: ids.From[ids.OrganizationKind](vague),
		SourceURL:      "https://vague.example",
		Facts:          employeeRangeFact("50-200 employees"),
	}); err != nil {
		t.Fatalf("ambiguous ApplyDeepRead: %v", err)
	}
	if got := readSizeBand(vague); got != nil {
		t.Fatalf("an ambiguous range filled size_band = %q, want NULL", *got)
	}
	var factValue string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT value FROM organization_fact
			  WHERE organization_id = $1 AND field = 'employee_range'`, vague).Scan(&factValue)
	}); err != nil {
		t.Fatalf("reading the fact row: %v", err)
	}
	if factValue != "50-200 employees" {
		t.Fatalf("fact row = %q, want the raw stated range kept as evidence", factValue)
	}

	// A human-claimed employee_range fact blocks the whole promotion: the
	// upsert refuses the agent's fact, so the column must not contradict the
	// human's standing statement either.
	claimed := insertOrg(t, e, e.Rep1, "claimed.example", "")
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization_fact
			  (workspace_id, organization_id, category, field, value, value_key,
			   evidence_snippet, source_url, confidence, source, captured_by)
			VALUES ($1, $2, 'company', 'employee_range', '11-50', '',
			        'set by hand', '', 1, 'human', $3)`,
			e.WS, claimed, "human:"+e.Rep1.String())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyDeepRead(ctx, people.DeepReadProposal{
		OrganizationID: ids.From[ids.OrganizationKind](claimed),
		SourceURL:      "https://claimed.example",
		Facts:          employeeRangeFact("about 300 people"),
	}); err != nil {
		t.Fatalf("ApplyDeepRead against a human-claimed fact: %v", err)
	}
	if got := readSizeBand(claimed); got != nil {
		t.Fatalf("a refused fact still promoted size_band = %q, want NULL", *got)
	}
}

func TestDeepReadRejectionLandsNothing(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	done, svc := runServicesDeepRead(t, e, org)

	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](done.ProposalIDs[0]), false, nil); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM organization_fact`); n != 0 {
		t.Fatalf("%d organization_fact rows after a rejection, want 0", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM organization_profile_field`); n != 0 {
		t.Fatalf("%d profile-field rows after a rejection, want 0", n)
	}
}

// fakeInserter stands in for the insert-only River client so handler
// tests can count what start enqueues.
type fakeInserter struct {
	inserts []river.JobArgs
	err     error
}

func (f *fakeInserter) EnqueueTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) error {
	if f.err != nil {
		return f.err
	}
	f.inserts = append(f.inserts, args)
	return nil
}

func newDeepReadTestEngine(e *integration.Env, inserter *fakeInserter) *deepReadEngine {
	return &deepReadEngine{
		people:  e.People,
		enqueue: inserter,
	}
}

// postDeepRead drives the start handler as the given caller and decodes
// the 202 handle (or fails the test on any other status when want202).
func postDeepRead(t *testing.T, e *integration.Env, engine *deepReadEngine, caller ids.UUID, org ids.UUID) (*httptest.ResponseRecorder, crmcontracts.SiteReadStarted) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+org.String()+"/deep-read", nil).
		WithContext(e.As(caller, nil, integration.AdminPerms))
	rec := httptest.NewRecorder()
	engine.start(rec, req, openapi_types.UUID(org))
	var started crmcontracts.SiteReadStarted
	if rec.Code == http.StatusAccepted {
		if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
			t.Fatalf("decoding SiteReadStarted: %v", err)
		}
	}
	return rec, started
}
