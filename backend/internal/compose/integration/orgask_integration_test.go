// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Ask Margince, against a real database.
//
// The prompts and the deterministic answers are proved without one. What needs
// a real read is what the prepared questions inherit rather than implement:
// they are written from the CALLER's own 360, so an answer can only describe
// records that caller could open — and an account they cannot read has no
// answer rather than a refusal that confirms it exists.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TestOrganizationAskAnswersFromTheModelAndReportsTheWriter is the shipped
// path: one prepared question, one model call, the answer labelled with who
// wrote it and echoing the question it answers.
func TestOrganizationAskAnswersFromTheModelAndReportsTheWriter(t *testing.T) {
	e := Setup(t)
	pipeline, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	deal := e.SeedDeal(t, "Fleet retrofit", pipeline, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, deal, org.UUID)

	lane := &countingLane{reply: `{"sentences":[{"text":"One open deal, in Discovery.","evidence":[{"entity_type":"deal","entity_id":"` + deal.String() + `"}]}]}`}
	svc := briefService(e, lane, "routing-1")

	answer, err := svc.Ask(e.As(e.Rep1, nil, briefReaderPerms), org, crmcontracts.WhatsOpen)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if lane.calls != 1 {
		t.Errorf("model calls = %d, want 1", lane.calls)
	}
	if answer.Question != crmcontracts.WhatsOpen {
		t.Errorf("question = %q, want the one that was asked", answer.Question)
	}
	if answer.GeneratedBy != crmcontracts.Model {
		t.Errorf("generated_by = %q, want model", answer.GeneratedBy)
	}
	if len(answer.Sentences) != 1 || answer.Sentences[0].Text != "One open deal, in Discovery." {
		t.Fatalf("sentences = %+v, want the one the lane wrote", answer.Sentences)
	}
	if answer.Sentences[0].Evidence[0].EntityId != crmcontracts.Id(deal) {
		t.Errorf("evidence = %+v, want the deal it was written from", answer.Sentences[0].Evidence)
	}
	// Nothing is cached: the question is asked once and read once, so no row
	// may outlive the request.
	if rows := e.WsCount(t, `SELECT count(*) FROM org_brief`); rows != 0 {
		t.Errorf("org_brief rows = %d after a question, want 0 — an answer is not a brief", rows)
	}
}

// An answer citing a record this input never carried is dropped, and the
// deterministic floor answers instead. The grounding filter is what stands
// between the reader and a sentence about a record they cannot open, so the
// answer path must run it rather than trusting the reply.
func TestOrganizationAskFallsBackWhenTheReplyCitesNothingReal(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	// A well-formed reply citing a deal that is not this account's.
	lane := &countingLane{reply: `{"sentences":[{"text":"A deal is closing.","evidence":[{"entity_type":"deal","entity_id":"` + ids.NewV7().String() + `"}]}]}`}
	svc := briefService(e, lane, "routing-1")

	answer, err := svc.Ask(e.As(e.Rep1, nil, briefReaderPerms), org, crmcontracts.MeetingPrep)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if answer.GeneratedBy != crmcontracts.Deterministic {
		t.Errorf("generated_by = %q, want the deterministic floor after an ungrounded reply", answer.GeneratedBy)
	}
	for _, sentence := range answer.Sentences {
		if strings.Contains(sentence.Text, "closing") {
			t.Errorf("the invented sentence survived: %q", sentence.Text)
		}
	}
}

// Without a model lane the questions still answer, from the same input. A
// deployment that runs no model is a supported posture, not a broken one.
func TestOrganizationAskServesTheFloorWithoutALane(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	svc := briefService(e, nil, "")

	answer, err := svc.Ask(e.As(e.Rep1, nil, briefReaderPerms), org, crmcontracts.MeetingPrep)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if answer.GeneratedBy != crmcontracts.Deterministic {
		t.Errorf("generated_by = %q with no lane wired, want deterministic", answer.GeneratedBy)
	}
	if len(answer.Sentences) == 0 {
		t.Error("the floor answered nothing about an account it can see")
	}
	for _, sentence := range answer.Sentences {
		if len(sentence.Evidence) == 0 {
			t.Errorf("floor sentence %q carries no citation", sentence.Text)
		}
	}
}

// A question about an account outside the caller's row scope is a read of that
// account: it must answer 404, not 403, or the refusal confirms it exists.
func TestOrganizationAskHidesAnAccountOutOfRowScope(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Theirs", &e.Rep3))
	lane := &countingLane{reply: `{"sentences":[]}`}
	svc := briefService(e, lane, "routing-1")

	teamScoped := briefReaderPerms
	teamScoped.RowScope = principal.RowScopeTeam
	_, err := svc.Ask(e.As(e.Rep1, []ids.UUID{e.Team1}, teamScoped), org, crmcontracts.WhatsOpen)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("ask on an out-of-scope account → %v, want ErrNotFound", err)
	}
	if lane.calls != 0 {
		t.Errorf("model calls = %d, want the refusal to come before any call", lane.calls)
	}
}

// An agent asking about an account has the records themselves; letting it
// spend the human's model budget on prose is not what a passport is for.
func TestOrganizationAskRefusesAnAgent(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	lane := &countingLane{reply: `{"sentences":[]}`}
	svc := briefService(e, lane, "routing-1")

	_, err := svc.Ask(AgentWithOrgRead(e), org, crmcontracts.WhatsOpen)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("agent question → %v, want ErrPermissionDenied", err)
	}
	if lane.calls != 0 {
		t.Errorf("model calls = %d for an agent, want 0", lane.calls)
	}
}

// A question the endpoint does not prepare is a 422, not a default: silently
// answering a different question than the one asked is indistinguishable from
// answering the one asked badly.
func TestOrganizationAskTransportRefusesAnUnpreparedQuestion(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	lane := &countingLane{reply: `{"sentences":[]}`}
	handlers := orgbrief.NewHandlers(briefService(e, lane, "routing-1"), nativeMode)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+org.String()+"/ask",
		strings.NewReader(`{"question":"why_did_they_ghost_me"}`))
	req.Header.Set("Content-Type", "application/json")
	handlers.AskAboutOrganization(rec,
		req.WithContext(e.As(e.Rep1, nil, briefReaderPerms)), crmcontracts.Id(org.UUID))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d for an unprepared question, want 422; body %s", rec.Code, rec.Body.String())
	}
	if lane.calls != 0 {
		t.Errorf("model calls = %d, want the refusal to come first", lane.calls)
	}
}

// The 360 refuses an overlay workspace, so prose written from its reads must
// refuse too — otherwise an overlay workspace gets answers about native rows
// while its own company page refuses to render.
func TestOrganizationAskTransportRefusesAnOverlayWorkspace(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	lane := &countingLane{reply: `{"sentences":[]}`}
	handlers := orgbrief.NewHandlers(briefService(e, lane, "routing-1"),
		func(context.Context) (bool, error) { return true, nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+org.String()+"/ask",
		strings.NewReader(`{"question":"whats_open"}`))
	req.Header.Set("Content-Type", "application/json")
	handlers.AskAboutOrganization(rec,
		req.WithContext(e.As(e.Rep1, nil, briefReaderPerms)), crmcontracts.Id(org.UUID))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d in overlay mode, want 422; body %s", rec.Code, rec.Body.String())
	}
	if lane.calls != 0 {
		t.Errorf("model calls = %d in overlay mode, want the refusal to come first", lane.calls)
	}
}
