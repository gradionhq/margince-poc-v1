// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The set-password-link handler's pre-database refusals, and the gate ORDER
// between them. Order is the behaviour under test as much as the refusals are:
// a non-admin who reaches the configuration gates learns the installation's
// email posture, and one who reaches the limiter can spend another member's
// issuance budget without ever being told they are not an admin.
//
// All of it is provable against a zero Service — nothing here should reach a
// database, and a panic on a nil pool is how this file would say otherwise.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// linkRequest issues one POST /users/{id}/password-link as the given roles.
func linkRequest(h Handlers, target ids.UserID, roles ...string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/users/"+target.String()+"/password-link", nil)
	actor := Identity{UserID: ids.UserID{UUID: ids.NewV7()}, Roles: roles}
	r = r.WithContext(withIdentity(r.Context(), actor))
	h.IssueUserPasswordLink(rec, r, crmcontracts.Id(target.UUID))
	return rec
}

// emailLessInstallation is the posture the whole feature exists for: no mailer,
// a configured public base URL.
func emailLessInstallation() Handlers {
	return NewHandlers(&Service{}).WithPasswordLinkBase("https://crm.example.test")
}

func TestPasswordLinkRefusesANonAdminBeforeAnythingElse(t *testing.T) {
	target := ids.UserID{UUID: ids.NewV7()}
	// A mailer IS wired, so the configuration gate would answer 409 if it ran
	// first. A non-admin must get 403 instead — otherwise the refusal itself
	// discloses that this installation has an email channel.
	h := NewHandlers(&Service{}).WithPasswordReset(nopMailer{}).WithPasswordLinkBase("https://crm.example.test")

	for _, roles := range [][]string{{"rep"}, {"manager"}, {"ops"}, {"read_only"}, nil} {
		rec := linkRequest(h, target, roles...)
		if rec.Code != http.StatusForbidden {
			t.Errorf("roles %v = %d, want 403 (and never a 409 disclosing email posture)", roles, rec.Code)
		}
	}
}

func TestPasswordLinkNonAdminCannotSpendTheTargetsIssuanceBudget(t *testing.T) {
	h := emailLessInstallation()
	target := ids.UserID{UUID: ids.NewV7()}

	// Well past the 5/hour per-target ceiling. Every one of these must be
	// refused as unauthorized WITHOUT touching the limiter — a rep able to
	// drain another member's budget is a denial-of-recovery primitive.
	for range 12 {
		if rec := linkRequest(h, target, "rep"); rec.Code != http.StatusForbidden {
			t.Fatalf("non-admin attempt = %d, want 403", rec.Code)
		}
	}
	// The target's budget must be untouched by all of that: an admin asking
	// straight afterwards is answered on the merits (here 404, since the test
	// context binds no workspace) and never 429.
	if rec := linkRequest(h, target, "admin"); rec.Code == http.StatusTooManyRequests {
		t.Fatal("admin was rate-limited — the non-admin attempts consumed the target's issuance budget")
	}
}

func TestPasswordLinkRefusesWhenTheInstallationMailsInstead(t *testing.T) {
	h := NewHandlers(&Service{}).WithPasswordReset(nopMailer{}).WithPasswordLinkBase("https://crm.example.test")
	rec := linkRequest(h, ids.UserID{UUID: ids.NewV7()}, "admin")
	if rec.Code != http.StatusConflict {
		t.Fatalf("mailer wired = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "email_channel_configured") {
		t.Fatalf("refusal body = %s, want the email_channel_configured code", rec.Body)
	}
}

func TestPasswordLinkRefusesWithoutAPublicBaseURL(t *testing.T) {
	// No mailer AND no base: nothing could deliver a link, and nothing could
	// build one either. The refusal names the operator's missing setting rather
	// than failing later with an unusable "/#/reset-password?token=..." link.
	rec := linkRequest(NewHandlers(&Service{}), ids.UserID{UUID: ids.NewV7()}, "admin")
	if rec.Code != http.StatusConflict {
		t.Fatalf("no base URL = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "public_base_url_unset") {
		t.Fatalf("refusal body = %s, want the public_base_url_unset code", rec.Body)
	}
}

func TestRedemptionIsReachableOnAnEmailLessInstallation(t *testing.T) {
	// The regression this whole feature turned on: ResetPassword used to answer
	// 501 whenever no mailer was wired, which is exactly the installation an
	// admin-issued link exists for — so the link was unredeemable by design.
	h := emailLessInstallation()
	rec := post(h.ResetPassword, "/v1/auth/reset-password", `{"token":"x","new_password":"twelve chars!"}`)
	if rec.Code == http.StatusNotImplemented {
		t.Fatal("reset-password answered 501 on an email-less installation — an admin-issued link cannot be redeemed")
	}
	// And with NO delivery configuration at all, because a token lives seven
	// days: an operator who clears the base URL after handing a link over must
	// not strand the human holding it. Possession of the token is the authority.
	bare := post(NewHandlers(&Service{}).ResetPassword,
		"/v1/auth/reset-password", `{"token":"x","new_password":"twelve chars!"}`)
	if bare.Code == http.StatusNotImplemented {
		t.Fatal("reset-password 501s with no delivery configuration — a link already delivered would be stranded")
	}

	// Asking for a reset BY EMAIL stays unavailable there: without a mailer
	// there is genuinely nothing to send. The two are separate capabilities.
	if rec := post(h.RequestPasswordReset, "/v1/auth/forgot-password", `{"email":"a@b.test"}`); rec.Code != http.StatusNotImplemented {
		t.Fatalf("forgot-password without a mailer = %d, want 501", rec.Code)
	}
	// And the login UI must still be told not to offer self-service recovery.
	capabilities := httptest.NewRecorder()
	h.GetAuthCapabilities(capabilities, httptest.NewRequest(http.MethodGet, "/v1/auth/capabilities", nil))
	if !strings.Contains(capabilities.Body.String(), `"password_reset":false`) {
		t.Fatalf("capabilities = %s, want password_reset:false", capabilities.Body)
	}
}

func TestMeAdvertisesTheLinkActionOnlyToAnAdminWhoCanUseIt(t *testing.T) {
	admin := Identity{Roles: []string{"admin"}}
	rep := Identity{Roles: []string{"rep"}}

	emailLess := emailLessInstallation()
	if !emailLess.canIssuePasswordLink(admin) {
		t.Error("admin on an email-less installation with a base URL: want the action advertised")
	}
	// A rep must not learn the installation's email posture from /me, which is
	// why this is a caller capability and not a deployment-posture flag.
	if emailLess.canIssuePasswordLink(rep) {
		t.Error("rep: want the action hidden")
	}
	if NewHandlers(&Service{}).canIssuePasswordLink(admin) {
		t.Error("admin with no public base URL: want the action hidden, since it could only 409")
	}
	mailed := NewHandlers(&Service{}).WithPasswordReset(nopMailer{}).WithPasswordLinkBase("https://crm.example.test")
	if mailed.canIssuePasswordLink(admin) {
		t.Error("admin where email is configured: want the action hidden, since the invite mails the link")
	}
}
