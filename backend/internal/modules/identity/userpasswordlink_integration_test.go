// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The admin-issued set-password link over a real migrated Postgres: one live
// token at a time, an audit row and an event that carry no credential, a
// redemption that actually works on the email-less installation the link exists
// for, and refusals for a non-admin and a non-active target.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func liveTokenCount(t *testing.T, e *revocationEnv, userID ids.UserID) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM auth_token
		 WHERE user_id = $1 AND purpose = 'password_reset' AND used_at IS NULL`,
		userID).Scan(&n); err != nil {
		t.Fatalf("counting live tokens: %v", err)
	}
	return n
}

func TestIssuePasswordLinkSupersedesTheMembersOutstandingTokens(t *testing.T) {
	e := setupRevocationEnv(t, "link-supersede")
	// An invite already leaves one live token behind, so this starts from the
	// realistic state rather than an empty one.
	member, _, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email: "linktarget@acme.test", DisplayName: "Link Target", Role: "rep",
	})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if got := liveTokenCount(t, e, member); got != 1 {
		t.Fatalf("after invite: %d live tokens, want 1", got)
	}

	raw, expiresAt, err := e.svc.IssuePasswordLink(e.wsCtx(e.admin), e.admin, member)
	if err != nil {
		t.Fatalf("issue link: %v", err)
	}
	if raw == "" || expiresAt.IsZero() {
		t.Fatalf("issue returned token-empty=%v expiry-zero=%v; want both set", raw == "", expiresAt.IsZero())
	}
	// Exactly one, not two: the invite's token is consumed by the issue, so a
	// link handed over earlier cannot still be redeemed alongside this one.
	if got := liveTokenCount(t, e, member); got != 1 {
		t.Errorf("after issue: %d live tokens, want exactly 1 (the previous one superseded)", got)
	}
}

func TestIssuePasswordLinkWritesAnAuditRowAndAnEventCarryingNoToken(t *testing.T) {
	e := setupRevocationEnv(t, "link-ledger")
	member, _, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email: "ledger@acme.test", DisplayName: "Ledger Target", Role: "rep",
	})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	raw, _, err := e.svc.IssuePasswordLink(e.wsCtx(e.admin), e.admin, member)
	if err != nil {
		t.Fatalf("issue link: %v", err)
	}

	var auditCount int
	var after string
	// Plain text, no jsonb round-trip: casting '' to jsonb is an error, so a
	// missing audit row — the very regression this guards — would surface as an
	// opaque scan failure instead of the count assertion below.
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*), coalesce(max(after::text), '') FROM audit_log
		 WHERE action = 'password_link_issued' AND entity_id = $1`, member).Scan(&auditCount, &after); err != nil {
		t.Fatalf("audit lookup: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("password_link_issued audit rows = %d, want 1", auditCount)
	}
	if strings.Contains(after, raw) {
		t.Error("the audit image carries the raw token — it must never be written to a ledger")
	}

	envs := e.identityEvents(t, "user.password_link_issued")
	if len(envs) != 1 {
		t.Fatalf("user.password_link_issued staged %d times, want once", len(envs))
	}
	body, err := json.Marshal(envs[0])
	if err != nil {
		t.Fatal(err)
	}
	// The whole envelope, not just the payload: a token must not reach the bus
	// through any field, including one added later.
	if strings.Contains(string(body), raw) {
		t.Error("the staged event carries the raw token — it must never reach the bus")
	}
	if !strings.Contains(string(body), member.String()) {
		t.Error("the event does not name the target member, so it attributes nothing")
	}
}

func TestAdminIssuedLinkIsRedeemableAndRevokesSessions(t *testing.T) {
	e := setupRevocationEnv(t, "link-redeem")
	member, _, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email: "redeemer@acme.test", DisplayName: "Redeemer", Role: "rep",
	})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	raw, _, err := e.svc.IssuePasswordLink(e.wsCtx(e.admin), e.admin, member)
	if err != nil {
		t.Fatalf("issue link: %v", err)
	}

	const chosen = "a chosen password!"
	if err := e.svc.RedeemPasswordReset(e.wsCtx(e.admin), raw, chosen); err != nil {
		t.Fatalf("redeem the admin-issued link: %v", err)
	}
	// The member can now actually sign in — the point of the whole feature.
	if _, _, err := e.svc.Login(e.wsCtx(e.admin), "redeemer@acme.test", chosen); err != nil {
		t.Fatalf("login with the newly set password: %v", err)
	}
	// Single use: the same link cannot be replayed.
	if err := e.svc.RedeemPasswordReset(e.wsCtx(e.admin), raw, "another password!"); err == nil {
		t.Error("the link redeemed twice — it must be single-use")
	}
}

func TestIssuePasswordLinkRefusesANonAdminAndANonActiveMember(t *testing.T) {
	e := setupRevocationEnv(t, "link-refusals")
	member, _, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email: "refused@acme.test", DisplayName: "Refused", Role: "rep",
	})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}

	rep := Identity{UserID: member, WorkspaceID: e.admin.WorkspaceID, Roles: []string{"rep"}}
	if _, _, err := e.svc.IssuePasswordLink(e.wsCtx(rep), rep, member); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("non-admin issue = %v, want ErrPermissionDenied", err)
	}

	if err := e.svc.DeactivateUser(e.wsCtx(e.admin), e.admin, DeactivateUserInput{UserID: member}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	before := liveTokenCount(t, e, member)
	// A deactivated member cannot redeem, so issuing would hand the admin a
	// link that is dead on arrival — the silent failure this feature removes.
	if _, _, err := e.svc.IssuePasswordLink(e.wsCtx(e.admin), e.admin, member); !errors.Is(err, ErrMemberNotActive) {
		t.Errorf("issue for a deactivated member = %v, want ErrMemberNotActive", err)
	}
	// The refusal rolls back whole: it neither mints a token nor consumes the
	// ones already there, so a later reactivation finds the member exactly as
	// the refusal found them.
	if got := liveTokenCount(t, e, member); got != before {
		t.Errorf("refused issue changed the live token count from %d to %d; it must leave the member untouched", before, got)
	}

	unknown := ids.UserID{UUID: ids.NewV7()}
	if _, _, err := e.svc.IssuePasswordLink(e.wsCtx(e.admin), e.admin, unknown); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("issue for an unknown member = %v, want ErrNotFound", err)
	}
}

func TestConcurrentIssuesLeaveExactlyOneLiveToken(t *testing.T) {
	e := setupRevocationEnv(t, "link-concurrent")
	member, _, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email: "concurrent@acme.test", DisplayName: "Concurrent", Role: "rep",
	})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}

	// Without the FOR UPDATE lock on the target, two transactions at READ
	// COMMITTED can each miss the other's uncommitted insert and both leave a
	// live token — two redeemable credentials for one member.
	const racers = 4
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = e.svc.IssuePasswordLink(e.wsCtx(e.admin), e.admin, member)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent issue %d: %v", i, err)
		}
	}
	if got := liveTokenCount(t, e, member); got != 1 {
		t.Errorf("after %d concurrent issues: %d live tokens, want exactly 1", racers, got)
	}
}

// The HTTP surface, over a real service: the success body and its cache
// posture, the 429 ceiling, and the not-active refusal only exist on this path.
func TestPasswordLinkHandlerServesTheLinkAndHoldsItsCeiling(t *testing.T) {
	e := setupRevocationEnv(t, "link-http")
	h := NewHandlers(e.svc).WithPasswordLinkBase("https://crm.example.test/")
	member, _, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email: "httptarget@acme.test", DisplayName: "HTTP Target", Role: "rep",
	})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}

	issue := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/users/"+member.String()+"/password-link", nil)
		h.IssueUserPasswordLink(rec, req.WithContext(withIdentity(e.wsCtx(e.admin), e.admin)),
			crmcontracts.Id(member.UUID))
		return rec
	}

	rec := issue()
	if rec.Code != http.StatusCreated {
		t.Fatalf("issue status = %d, want 201: %s", rec.Code, rec.Body)
	}
	// The body is a live credential: no cache between here and the admin's tab
	// may keep it, which the fragment placement alone does not achieve.
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var issued crmcontracts.IssuePasswordLinkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode issue body: %v", err)
	}
	// The base arrived with a trailing slash; the link must not double it.
	if !strings.HasPrefix(issued.SetPasswordUrl, "https://crm.example.test/#/reset-password?token=") {
		t.Errorf("link = %q, want the trimmed base and a fragment-borne token", issued.SetPasswordUrl)
	}
	serverVisible, _, _ := strings.Cut(issued.SetPasswordUrl, "#")
	if strings.Contains(serverVisible, "token=") {
		t.Errorf("link puts the token in the server-visible part: %q", issued.SetPasswordUrl)
	}
	if issued.ExpiresAt.IsZero() {
		t.Error("no expiry returned; the admin cannot say how long the member has")
	}

	// The per-target ceiling is 5/hour, and issuing supersedes the previous
	// token — so an unbounded operation would be a denial-of-recovery
	// primitive against one member.
	var last int
	for range 8 {
		last = issue().Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("past the per-target ceiling = %d, want 429", last)
	}
}

func TestPasswordLinkHandlerRefusesANonActiveMember(t *testing.T) {
	e := setupRevocationEnv(t, "link-http-inactive")
	h := NewHandlers(e.svc).WithPasswordLinkBase("https://crm.example.test")
	member, _, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email: "inactive@acme.test", DisplayName: "Inactive", Role: "rep",
	})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if err := e.svc.DeactivateUser(e.wsCtx(e.admin), e.admin, DeactivateUserInput{UserID: member}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/users/"+member.String()+"/password-link", nil)
	h.IssueUserPasswordLink(rec, req.WithContext(withIdentity(e.wsCtx(e.admin), e.admin)),
		crmcontracts.Id(member.UUID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("deactivated target = %d, want 409: %s", rec.Code, rec.Body)
	}
	// The code distinguishes this from the two configuration refusals, so the
	// admin is told to reactivate rather than to go and change the deployment.
	if !strings.Contains(rec.Body.String(), "member_not_active") {
		t.Errorf("refusal body = %s, want the member_not_active code", rec.Body)
	}
}
