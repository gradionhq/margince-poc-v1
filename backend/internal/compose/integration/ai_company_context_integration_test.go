// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestCompanyContextBindsPromptCacheAndAICallTrace(t *testing.T) {
	e := Setup(t)
	ctx := e.Admin()
	offer := "Industrial heat pumps"
	icp := "European food manufacturers"
	if _, err := e.People.SaveCompany(ctx, people.SaveCompanyInput{
		DisplayName: "Acme Heat",
		Fields: map[string]*string{
			"offer_summary": &offer,
			"icp":           &icp,
		},
	}); err != nil {
		t.Fatalf("SaveCompany: %v", err)
	}

	modelPath, err := compose.NewModelPath(ai.FakeRoutingConfig(), e.Pool, false, nil)
	if err != nil {
		t.Fatalf("NewModelPath: %v", err)
	}
	req := model.Request{
		System:   "You are a governed CRM agent.",
		Messages: []model.Message{{Role: "user", Content: "Prepare the account."}},
	}
	if _, _, err := modelPath.Agent.Complete(ctx, req); err != nil {
		t.Fatalf("first agent completion: %v", err)
	}
	if _, _, err := modelPath.Agent.Complete(ctx, req); err != nil {
		t.Fatalf("cached agent completion: %v", err)
	}

	changedOffer := "Industrial heat pumps with managed installation"
	if _, err := e.People.SaveCompany(ctx, people.SaveCompanyInput{
		DisplayName: "Acme Heat",
		Fields: map[string]*string{
			"offer_summary": &changedOffer,
			"icp":           &icp,
		},
	}); err != nil {
		t.Fatalf("edit company context: %v", err)
	}
	if _, _, err := modelPath.Agent.Complete(ctx, req); err != nil {
		t.Fatalf("post-edit agent completion: %v", err)
	}

	type trace struct {
		scopes      []string
		fingerprint string
		cacheHit    bool
		requestHash string
	}
	var traces []trace
	err = database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT context_scopes, context_fingerprint, cache_hit, request_fingerprint
			FROM ai_call
			WHERE task = 'agent_loop'
			ORDER BY occurred_at, id`)
		if err != nil {
			return fmt.Errorf("query ai_call context trace: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var got trace
			if err := rows.Scan(&got.scopes, &got.fingerprint, &got.cacheHit, &got.requestHash); err != nil {
				return fmt.Errorf("scan ai_call context trace: %w", err)
			}
			traces = append(traces, got)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate ai_call context traces: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 3 {
		t.Fatalf("agent-loop traces = %d, want 3: %+v", len(traces), traces)
	}
	wantScopes := []string{"identity", "positioning", "sales", "offer"}
	for i, got := range traces {
		if !reflect.DeepEqual(got.scopes, wantScopes) || got.fingerprint == "" {
			t.Fatalf("trace %d context metadata = scopes %v fingerprint %q", i, got.scopes, got.fingerprint)
		}
	}
	if traces[0].cacheHit || !traces[1].cacheHit || traces[2].cacheHit {
		t.Fatalf("cache sequence = %v/%v/%v, want miss/hit/miss", traces[0].cacheHit, traces[1].cacheHit, traces[2].cacheHit)
	}
	if traces[0].fingerprint != traces[1].fingerprint || traces[0].requestHash != traces[1].requestHash {
		t.Fatalf("unchanged context did not reuse its binding: %+v", traces)
	}
	if traces[2].fingerprint == traces[0].fingerprint || traces[2].requestHash == traces[0].requestHash {
		t.Fatalf("company edit reused stale context/cache identity: %+v", traces)
	}
}

func TestPolicyNoneTraceCarriesEmptyCompanyContext(t *testing.T) {
	e := Setup(t)
	modelPath, err := compose.NewModelPath(ai.FakeRoutingConfig(), e.Pool, false, nil)
	if err != nil {
		t.Fatalf("NewModelPath: %v", err)
	}
	if _, err := modelPath.ColdStart.Complete(e.Admin(), model.Request{
		Messages:      []model.Message{{Role: "user", Content: "extract"}},
		ContextScopes: []string{"administrative"}, ContextFingerprint: "caller-supplied",
	}); err != nil {
		t.Fatalf("cold-start completion: %v", err)
	}
	var scopes []string
	var fingerprint string
	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	err = database.WithWorkspaceTx(wsCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(wsCtx, `
			SELECT context_scopes, context_fingerprint
			FROM ai_call WHERE task = 'cold_start'`).Scan(&scopes, &fingerprint)
	})
	if err != nil {
		t.Fatalf("read cold-start trace: %v", err)
	}
	if len(scopes) != 0 || fingerprint != "" {
		t.Fatalf("policy-none trace accepted caller context metadata: scopes %v fingerprint %q", scopes, fingerprint)
	}
}

func TestDraftReplyCarriesItsBoundedCompanyContextPolicy(t *testing.T) {
	e := Setup(t)
	offer := "Industrial heat pumps"
	if _, err := e.People.SaveCompany(e.Admin(), people.SaveCompanyInput{
		DisplayName: "Acme Heat",
		Fields:      map[string]*string{"offer_summary": &offer},
	}); err != nil {
		t.Fatalf("SaveCompany: %v", err)
	}
	modelPath, err := compose.NewModelPath(ai.FakeRoutingConfig(), e.Pool, false, nil)
	if err != nil {
		t.Fatalf("NewModelPath: %v", err)
	}
	if _, err := modelPath.DraftReply.Complete(e.Admin(), model.Request{
		Messages: []model.Message{{Role: "user", Content: "draft from this activity"}},
	}); err != nil {
		t.Fatalf("draft reply completion: %v", err)
	}
	var scopes []string
	var fingerprint string
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.Admin(), `
			SELECT context_scopes, context_fingerprint
			FROM ai_call WHERE task = 'draft_reply'`).Scan(&scopes, &fingerprint)
	})
	if err != nil {
		t.Fatalf("read draft-reply trace: %v", err)
	}
	want := []string{"positioning", "sales", "proof", "market"}
	if !reflect.DeepEqual(scopes, want) || fingerprint == "" {
		t.Fatalf("draft reply context = scopes %v fingerprint %q, want %v and non-empty", scopes, fingerprint, want)
	}
}
