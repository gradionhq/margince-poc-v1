// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The tool client sits outside the trust boundary: an error the sentinel
// taxonomy does not know (driver text, hosts, wrap chains) surfaces as a
// generic message, and the real cause goes to the server-side log only.
func TestExplainScrubsUnmappedErrors(t *testing.T) {
	var logBuf bytes.Buffer
	srv := NewDispatcher(nil, nil, "t", "0").
		WithLogger(slog.New(slog.NewTextHandler(&logBuf, nil)))

	secret := "pgx: password authentication failed for user margince_app at 10.7.0.5:5432"
	got := srv.explain("update_record", fmt.Errorf("saving record: %w", errors.New(secret)))

	if strings.Contains(got, "10.7.0.5") || strings.Contains(got, "pgx") || strings.Contains(got, "margince_app") {
		t.Fatalf("internal error text crossed the trust boundary: %q", got)
	}
	if !strings.Contains(got, "internal reason") {
		t.Errorf("generic message missing its actionable core: %q", got)
	}
	if !strings.Contains(logBuf.String(), "10.7.0.5") {
		t.Error("the real cause was not logged server-side")
	}
}

// The sentinel taxonomy stays actionable: mapped errors keep their
// guidance (and their safe, domain-authored detail) — scrubbing must not
// flatten "a human must say yes" into "something broke".
func TestExplainKeepsSentinelGuidance(t *testing.T) {
	srv := NewDispatcher(nil, nil, "t", "0")
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("advance: %w", apperrors.ErrRequiresApproval), "human approval"},
		{fmt.Errorf("scope: %w", apperrors.ErrScopeExceeded), "scope"},
		{fmt.Errorf("rbac: %w", apperrors.ErrPermissionDenied), "not permitted"},
		{fmt.Errorf("row: %w", apperrors.ErrNotFound), "No such record"},
		{fmt.Errorf("cas: %w", apperrors.ErrVersionSkew), "changed since it was read"},
		{fmt.Errorf("token: %w", apperrors.ErrApprovalTokenInvalid), "approval token"},
		// A declared capability gap must say do-not-retry: the generic branch
		// tells the agent to retry, which for a permanent refusal spends a
		// scheduled run's whole step budget re-calling the same tool.
		{fmt.Errorf("mode: %w", apperrors.ErrUnsupportedBySoR), "Do not retry"},
	}
	for _, tc := range cases {
		if got := srv.explain("t", tc.err); !strings.Contains(got, tc.want) {
			t.Errorf("explain(%v) = %q, want it to mention %q", tc.err, got, tc.want)
		}
	}
}

// A failed bind (revoked passport, dead database) tells the client only
// that its credential no longer works — never why the server could not
// check it.
func TestCallScrubsBindFailures(t *testing.T) {
	var logBuf bytes.Buffer
	cause := errors.New("dial tcp 10.7.0.5:5432: connect: connection refused")
	srv := NewDispatcher(nil, func(ctx context.Context) (context.Context, error) {
		return nil, cause
	}, "t", "0").WithLogger(slog.New(slog.NewTextHandler(&logBuf, nil)))

	out := srv.call(context.Background(), []byte(`{"name":"list_pipelines","arguments":{}}`))
	if out["isError"] != true {
		t.Fatalf("bind failure did not produce an in-band tool error: %v", out)
	}
	text := fmt.Sprint(out["content"])
	if strings.Contains(text, "10.7.0.5") || strings.Contains(text, "dial tcp") {
		t.Fatalf("bind failure leaked infrastructure detail: %q", text)
	}
	if !strings.Contains(text, "authentication failed") {
		t.Errorf("client was not told authentication failed: %q", text)
	}
	if !strings.Contains(logBuf.String(), "connection refused") {
		t.Error("the real bind failure was not logged server-side")
	}
}

// tools/list must advertise only what the caller's passport scopes could
// actually invoke. A surface that lists a tool the gate will refuse leaves the
// client no way to learn the truth except to call and be denied.
func TestToolListAdvertisesOnlyWhatTheCallersScopesAdmit(t *testing.T) {
	registry := NewRegistry(nil, nil)
	for name, scope := range map[string]principal.Scope{
		"read_tool":  principal.ScopeRead,
		"write_tool": principal.ScopeWrite,
		"send_tool":  principal.ScopeSend,
	} {
		registry.Register(&fakeTool{spec: mcp.ToolSpec{
			Name: name, RequiredScope: scope, Tier: mcp.TierAutoExecute,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}})
	}
	s := NewDispatcher(registry, bindAuthenticated, "margince-crm", "test")

	listed := func(ctx context.Context) []string {
		resp := s.handle(ctx, rpcRequest{
			JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list",
		})
		result, ok := resp.Result.(map[string]any)
		if !ok {
			t.Fatalf("result = %#v", resp.Result)
		}
		tools, ok := result["tools"].([]map[string]any)
		if !ok {
			t.Fatalf("tools = %#v", result["tools"])
		}
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			name, _ := tool[fieldName].(string)
			names = append(names, name)
		}
		slices.Sort(names)
		return names
	}

	agentCtx := func(scopes ...principal.Scope) context.Context {
		return principal.WithActor(context.Background(), principal.Principal{
			Type: principal.PrincipalAgent, ID: "agent:night", Scopes: principal.NewScopeSet(scopes...),
		})
	}

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want []string
	}{
		{"read only sees the read tool", agentCtx(principal.ScopeRead), []string{"read_tool"}},
		{
			"read+write sees both, not send",
			agentCtx(principal.ScopeRead, principal.ScopeWrite),
			[]string{"read_tool", "write_tool"},
		},
		// A human reaching the surface is bounded by RBAC at the store, not by
		// a passport scope they never carry — filtering them would hide it all.
		{"a human sees the whole surface", principal.WithActor(context.Background(), principal.Principal{
			Type: principal.PrincipalHuman, ID: "human:ada",
		}), []string{"read_tool", "send_tool", "write_tool"}},
		// Fail closed: no principal means no scopes, so nothing is advertised.
		{"an unauthenticated caller sees nothing", context.Background(), []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := listed(tc.ctx); !slices.Equal(got, tc.want) {
				t.Fatalf("tools/list = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInitializeNegotiatesTheClientsProtocolRevision(t *testing.T) {
	s := NewDispatcher(NewRegistry(nil, nil), bindAuthenticated, "margince-crm", "test")
	for _, tc := range []struct{ name, requested, want string }{
		{
			"echoes a supported revision", supportedProtocolVersions[len(supportedProtocolVersions)-1],
			supportedProtocolVersions[len(supportedProtocolVersions)-1],
		},
		{"newest when unsupported", "1999-01-01", supportedProtocolVersions[0]},
		{"newest when absent", "", supportedProtocolVersions[0]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := `{}`
			if tc.requested != "" {
				params = fmt.Sprintf(`{"protocolVersion":%q}`, tc.requested)
			}
			resp := s.handle(context.Background(), rpcRequest{
				JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
				Params: json.RawMessage(params),
			})
			result, ok := resp.Result.(map[string]any)
			if !ok {
				t.Fatalf("result = %#v", resp.Result)
			}
			if result["protocolVersion"] != tc.want {
				t.Fatalf("protocolVersion = %v, want %v", result["protocolVersion"], tc.want)
			}
		})
	}
}
