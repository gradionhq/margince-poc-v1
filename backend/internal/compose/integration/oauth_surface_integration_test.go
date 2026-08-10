// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// What the minted Bearer is once the handshake is done: a passport with real
// authority on the governed surfaces, and nothing more. Its 🟡 refusals stage
// a signed, effect-bound approval token (ADR-0036), and its authority dies on
// the next call after revocation — asserted on the hosted MCP transport, the
// wire an agent client actually holds it on.

func TestApprovalTokenIsASignedEffectBoundJWS(t *testing.T) {
	o := setupOAuth(t)

	code := o.authorize(t, nil)
	_, body := o.exchange(t, url.Values{"code": {code}})
	agentBearer := map[string]string{"Authorization": "Bearer " + body["access_token"].(string)}

	var person struct {
		ID string `json:"id"`
	}
	if status := o.Call(t, "POST", "/v1/people", apptest.AnyMap{"full_name": "JWS Target"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if status := o.Call(t, "DELETE", "/v1/people/"+person.ID, nil, agentBearer, &problem); status != http.StatusForbidden {
		t.Fatalf("agent archive → %d, want staged 403", status)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	var approved struct {
		ApprovalToken *string `json:"approval_token"`
	}
	if status := o.Call(t, "POST", "/v1/approvals/"+approvalID+"/approve", apptest.AnyMap{}, nil, &approved); status != http.StatusOK {
		t.Fatalf("approve → %d", status)
	}
	if approved.ApprovalToken == nil || strings.Count(*approved.ApprovalToken, ".") != 2 {
		t.Fatalf("approve response lacks a compact JWS: %+v", approved.ApprovalToken)
	}

	pool, err := database.NewPool(context.Background(), envDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var wsRaw string
	if err := o.Owner.QueryRow(context.Background(), `SELECT id FROM workspace WHERE slug = $1`, o.Slug).Scan(&wsRaw); err != nil {
		t.Fatal(err)
	}
	wsID, err := ids.Parse(wsRaw)
	if err != nil {
		t.Fatal(err)
	}
	wsCtx := principal.WithWorkspaceID(context.Background(), wsID)

	svc := approvals.NewService(pool)
	claims, err := svc.VerifyApprovalToken(wsCtx, *approved.ApprovalToken)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.ApprovalID.String() != approvalID || claims.Kind != "archive_record" ||
		claims.TargetID == nil || claims.DiffHash == "" || claims.PassportID == nil {
		t.Fatalf("claims not effect-bound: %+v", claims)
	}

	// One flipped payload byte is fatal.
	parts := strings.Split(*approved.ApprovalToken, ".")
	tampered := parts[0] + "." + flipLastChar(parts[1]) + "." + parts[2]
	if _, err := svc.VerifyApprovalToken(wsCtx, tampered); !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
		t.Fatalf("tampered token → %v, want ErrApprovalTokenInvalid", err)
	}
}

func TestHostedMCPTransportSharesTheGovernedSurface(t *testing.T) {
	o := setupOAuth(t)
	code := o.authorize(t, nil)
	_, body := o.exchange(t, url.Values{"code": {code}})
	token := body["access_token"].(string)

	pool, err := database.NewPool(context.Background(), envDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	authSvc := identity.NewService(pool)
	registry := compose.NewRegistry(pool, compose.SendPath{})
	authenticate := func(r *http.Request) (context.Context, error) {
		wsID, err := authSvc.InstallationWorkspace(r.Context())
		if err != nil {
			return nil, err
		}
		ctx := principal.WithWorkspaceID(r.Context(), wsID.UUID)
		agent, err := authSvc.AuthenticateAgent(ctx, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil {
			return nil, err
		}
		return principal.WithCorrelationID(principal.WithActor(ctx, agent.Principal()), ids.NewV7()), nil
	}
	hosted := httptest.NewServer(agents.NewHTTPHandler(registry, authenticate,
		agents.ResourceMetadataChallenge, "margince-crm", "test",
		slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(hosted.Close)

	rpc := func(bearer, payload string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, hosted.URL, strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer apptest.CloseBody(t, resp)
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	status, out := rpc(token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if status != http.StatusOK || !strings.Contains(out, `"search_records"`) {
		t.Fatalf("hosted tools/list → %d %s", status, out)
	}
	status, out = rpc(token, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_record","arguments":{"record_type":"person","fields":{"full_name":"Hosted Agent Person"}}}}`)
	if status != http.StatusOK || !strings.Contains(out, "Hosted Agent Person") {
		t.Fatalf("hosted tools/call → %d %s", status, out)
	}

	// Revocation binds between two calls: kill the passport via the
	// session surface, the next hosted call answers 401 + RFC 9728.
	var passportID string
	if err := o.Owner.QueryRow(context.Background(),
		`SELECT id FROM passport WHERE token_hash = $1`, sha256Hex(token)).Scan(&passportID); err != nil {
		t.Fatal(err)
	}
	if status := o.Call(t, "DELETE", "/v1/passports/"+passportID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoke → %d", status)
	}
	req, _ := http.NewRequest(http.MethodPost, hosted.URL, strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer apptest.CloseBody(t, resp)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "oauth-protected-resource") {
		t.Fatalf("revoked bearer → %d %q, want 401 + RFC 9728 pointer", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

func envDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_APP_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set")
	}
	return dsn
}

func flipLastChar(s string) string {
	last := s[len(s)-1]
	replacement := byte('A')
	if last == 'A' {
		replacement = 'B'
	}
	return s[:len(s)-1] + string(replacement)
}
