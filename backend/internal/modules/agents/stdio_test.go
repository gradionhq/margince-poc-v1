// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// The tool client sits outside the trust boundary: an error the sentinel
// taxonomy does not know (driver text, hosts, wrap chains) surfaces as a
// generic message, and the real cause goes to the server-side log only.
func TestExplainScrubsUnmappedErrors(t *testing.T) {
	var logBuf bytes.Buffer
	srv := NewStdioServer(nil, nil, "t", "0").
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
	srv := NewStdioServer(nil, nil, "t", "0")
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
	srv := NewStdioServer(nil, func(ctx context.Context) (context.Context, error) {
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
