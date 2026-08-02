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
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
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

// Every error the REST surface answers with a status — a client mistake, a
// governed refusal, a rate limit — must reach the agent as that verdict, not
// as an internal failure. The two are opposite instructions: an internal
// failure says "retry, it may work", while a refused call says "this cannot
// work until something changes". Getting it backwards makes a scheduled run
// spend its whole step budget re-issuing a call the server has already
// settled, and withholds the argument the agent could have fixed.
//
// The classes below are exactly those with no bespoke branch in explain:
// they prove the default branch classifies rather than shrugging.
func TestExplainClassifiesEveryCallerFacingRefusal(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantCode  string
		transient bool
	}{
		{"conflict", fmt.Errorf("create: %w", apperrors.ErrConflict), "conflict", false},
		{"seat tier", fmt.Errorf("seat: %w", apperrors.ErrSeatTierInsufficient), "seat_tier_insufficient", false},
		{"consent", fmt.Errorf("send: %w", apperrors.ErrConsentNotGranted), "consent_not_granted", false},
		{"budget", fmt.Errorf("quota: %w", apperrors.ErrBudgetExceeded), "rate_limited", true},
		{"not overlay", fmt.Errorf("mode: %w", apperrors.ErrModeNotOverlay), "mode_not_overlay", false},
		{"incumbent connected", fmt.Errorf("connect: %w", apperrors.ErrIncumbentAlreadyConnected), "incumbent_already_connected", false},
		{"flip blocked", fmt.Errorf("flip: %w", apperrors.ErrOverlayFlipBlocked), "overlay_flip_blocked", false},
		{"incumbent budget", fmt.Errorf("read: %w", apperrors.ErrIncumbentBudgetExhausted), "incumbent_budget_exhausted", true},
		{"bad fields", &datasource.FieldDecodeError{Cause: errors.New(`unknown field "subjekt"`)}, "invalid_field", false},
		{"unserved entity", &datasource.UnsupportedEntityError{Type: "invoice"}, "unsupported_entity_type", false},
		{"malformed cursor", &storekit.MalformedCursorError{}, "malformed_cursor", false},
		{"cursor sort mismatch", &storekit.CursorSortMismatchError{}, "cursor_param_mismatch", false},
		{"bad sort", &storekit.SortError{Code: "unsortable_field", Message: "cannot sort by body"}, "unsortable_field", false},
		{"bad predicate", &storekit.PredicateError{Field: "stage", Code: "unknown_filter_field", Message: "no such field"}, "unknown_filter_field", false},
		{"bad value", &values.ParseError{Field: "email", Code: "invalid_email", Message: "not an address"}, "invalid_email", false},
		{"handler validation", httperr.Validation("captured_by_kind", "invalid", "not a known kind"), "validation_error", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			srv := NewDispatcher(nil, nil, "t", "0").
				WithLogger(slog.New(slog.NewTextHandler(&logBuf, nil)))

			got := srv.explain("log_activity", tc.err)

			if strings.Contains(got, "internal reason") {
				t.Fatalf("a classified refusal was reported as an internal failure: %q", got)
			}
			if !strings.Contains(got, tc.wantCode) {
				t.Errorf("explain = %q, want it to name the machine code %q", got, tc.wantCode)
			}
			if tc.transient != strings.Contains(got, "can succeed later") {
				t.Errorf("explain = %q, want transient=%v", got, tc.transient)
			}
			// A caller's mistake is not the server's fault, so it must not
			// land in the error log an operator reads for outages.
			if strings.Contains(logBuf.String(), "level=ERROR") {
				t.Errorf("a refusal was logged as a server error: %q", logBuf.String())
			}
		})
	}
}

// The surface's own two refusal types are the agent's own mistakes: it wrote
// the arguments, and it chose the tool name. Both are fixable by the agent and
// by nobody else, so both must come back named — and neither is a server fault
// to retry into.
func TestExplainAnswersTheSurfacesOwnRefusals(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		wants []string
	}{
		{
			name:  "arguments the tool rejected",
			err:   &BadArgsError{Cause: errors.New(`json: unknown field "subjekt"`)},
			wants: []string{"subjekt", "inputSchema", "rejected again"},
		},
		{
			name:  "a tool name that is not on the surface",
			err:   &UnknownToolError{Name: "send_invoice"},
			wants: []string{"send_invoice", "tools/list", "no retry"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewDispatcher(nil, nil, "t", "0").
				WithLogger(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
			got := srv.explain("t", tc.err)
			if strings.Contains(got, "internal reason") {
				t.Fatalf("the agent's own mistake was reported as a server failure: %q", got)
			}
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Errorf("explain = %q, want it to mention %q", got, want)
				}
			}
		})
	}
}

// The name in an unknown-tool refusal is chosen by the model and echoed into a
// transcript its own later prompts read, so the type bounds it — an unbounded
// echo is an unbounded write into those prompts by an author that has already
// been shown the fence marker.
func TestUnknownToolErrorBoundsTheNameItEchoes(t *testing.T) {
	flood := strings.Repeat("A", 4096)
	got := (&UnknownToolError{Name: flood}).Error()
	if len(got) > maxToolNameEcho+len("unknown tool ")+len("…") {
		t.Errorf("Error() is %d bytes for a %d-byte name — the echo is unbounded", len(got), len(flood))
	}
}

// The reported defect, kept as a spec: log_activity with an occurred_at that
// carries no timezone offset. It is a pure argument mistake, and the agent
// must be told which value the server rejected instead of being sent to the
// workspace admin.
func TestExplainNamesTheRejectedArgument(t *testing.T) {
	srv := NewDispatcher(nil, nil, "t", "0").
		WithLogger(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	_, parseErr := time.Parse(time.RFC3339, "2026-07-31T16:35:00")
	got := srv.explain("log_activity", &datasource.FieldDecodeError{Cause: parseErr})

	for _, want := range []string{"2026-07-31T16:35:00", "request schema", "refused as issued"} {
		if !strings.Contains(got, want) {
			t.Errorf("explain = %q, want it to mention %q", got, want)
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
