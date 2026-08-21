// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The claim surface over HTTP (ADR-0105). The service-level rules are proven in
// identity's own suite; what these cases add is the edge — that the routes are
// reachable with no organization at all, that each refusal arrives as the status
// a client can act on, and that nothing here needs a session.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
)

// unprovisionedServer is an installation with no organization and one
// outstanding setup token — the state a first boot leaves behind.
func unprovisionedServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	e := apptest.SetupApp(t)
	ctx := context.Background()
	if _, err := e.Owner.Exec(ctx, `UPDATE workspace SET archived_at = now() WHERE archived_at IS NULL`); err != nil {
		t.Fatalf("clearing the harness organization: %v", err)
	}
	token, err := identity.NewService(e.Pool).MintSetupToken(ctx)
	if err != nil {
		t.Fatalf("minting the setup token: %v", err)
	}
	srv := httptest.NewServer(compose.New(e.Pool, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv, token
}

// claimStatus posts and returns only the status. The request is issued here
// rather than in a shared helper that hands a live response back: most cases
// below assert a status and nothing else, and bodyclose can only see the close
// when it sits with the call.
func claimStatus(t *testing.T, srv *httptest.Server, body string) int {
	t.Helper()
	resp, err := srv.Client().Post(srv.URL+"/setup/claim", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /setup/claim: %v", err)
	}
	t.Cleanup(func() { apptest.CloseBody(t, resp) })
	return resp.StatusCode
}

func claimBody(token string) string {
	return `{"setup_token":"` + token + `","organization_name":"Claimed Co","timezone":"Europe/Berlin",` +
		`"base_currency":"EUR","admin_email":"ops@claimed.test","admin_name":"Ops","admin_password":"a bootstrap password!"}`
}

func TestSetupStatusIsReachableWithNoOrganization(t *testing.T) {
	srv, _ := unprovisionedServer(t)

	// Every /v1 route answers 503 in this state. The claim surface must not,
	// or the installation could never be claimed at all.
	resp, err := srv.Client().Get(srv.URL + "/setup/status")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { apptest.CloseBody(t, resp) })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /setup/status answered %d, want 200 — the pre-tenant surface is behind the tenant gate", resp.StatusCode)
	}
	var body struct {
		Claimable bool `json:"claimable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Claimable {
		t.Error("an installation with an outstanding token reported claimable=false")
	}
}

func TestAClaimOverHTTPProvisionsTheInstallation(t *testing.T) {
	srv, token := unprovisionedServer(t)

	resp, err := srv.Client().Post(srv.URL+"/setup/claim", "application/json", strings.NewReader(claimBody(token)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { apptest.CloseBody(t, resp) })
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("claim answered %d, want 201", resp.StatusCode)
	}
	var created struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.WorkspaceID == "" {
		t.Error("the claim response names no organization, so a client cannot tell what it created")
	}

	// And the surface closes behind it: status flips, and a replay of the same
	// body is refused as a conflict rather than creating a second organization.
	status, err := srv.Client().Get(srv.URL + "/setup/status")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { apptest.CloseBody(t, status) })
	var body struct {
		Claimable bool `json:"claimable"`
	}
	if err := json.NewDecoder(status.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Claimable {
		t.Error("the installation still reports claimable after being claimed")
	}
	if got := claimStatus(t, srv, claimBody(token)); got != http.StatusConflict {
		t.Errorf("replaying the claim answered %d, want 409", got)
	}
}

func TestAClaimWithTheWrongTokenIsUnauthorized(t *testing.T) {
	srv, _ := unprovisionedServer(t)

	if got := claimStatus(t, srv, claimBody("not-the-token")); got != http.StatusUnauthorized {
		t.Fatalf("a wrong token answered %d, want 401", got)
	}
	// A missing token is the same answer, not a different one: distinguishing
	// them tells an unauthenticated caller whether guessing is worthwhile.
	if got := claimStatus(t, srv, `{"organization_name":"X"}`); got != http.StatusUnauthorized {
		t.Errorf("an absent token answered %d, want 401", got)
	}
}

func TestAClaimWithAWeakPasswordIsRefusedAsValidation(t *testing.T) {
	srv, token := unprovisionedServer(t)

	body := `{"setup_token":"` + token + `","organization_name":"Weak Co","timezone":"Europe/Berlin",` +
		`"base_currency":"EUR","admin_email":"ops@weak.test","admin_name":"Ops","admin_password":""}`
	if got := claimStatus(t, srv, body); got != http.StatusUnprocessableEntity {
		t.Fatalf("an empty admin password answered %d, want 422 — this is the field the caller must fix, not a server fault", got)
	}
	// The token survives, so a mistyped password is not how an operator loses
	// the installation.
	if got := claimStatus(t, srv, claimBody(token)); got != http.StatusCreated {
		t.Errorf("the token no longer claims after a rejected attempt: %d", got)
	}
}

func TestAMalformedClaimBodyIsRefusedWithoutTouchingTheToken(t *testing.T) {
	srv, token := unprovisionedServer(t)

	if got := claimStatus(t, srv, `{"setup_token":`); got != http.StatusUnprocessableEntity {
		t.Errorf("a truncated body answered %d, want 422", got)
	}
	// An unknown field is refused too, rather than silently ignored: a client
	// sending admin_pasword would otherwise get a passwordless account.
	if got := claimStatus(t, srv, `{"setup_token":"x","admin_pasword":"typo"}`); got != http.StatusUnprocessableEntity {
		t.Errorf("an unknown field answered %d, want 422", got)
	}
	if got := claimStatus(t, srv, claimBody(token)); got != http.StatusCreated {
		t.Errorf("the token no longer claims after malformed attempts: %d", got)
	}
}

// TestAnUnprovisionedBootMintsAndAnnouncesTheToken covers the boot half: an
// empty database with no configured bootstrap_admin must not fail to start, and
// must leave the operator a credential they can actually find.
func TestAnUnprovisionedBootMintsAndAnnouncesTheToken(t *testing.T) {
	e := apptest.SetupApp(t)
	ctx := context.Background()
	if _, err := e.Owner.Exec(ctx, `UPDATE workspace SET archived_at = now() WHERE archived_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	// The token file is written relative to the process working directory, so
	// the test owns one for the duration.
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Errorf("restoring the working directory: %v", err)
		}
	})

	var logged strings.Builder
	log := slog.New(slog.NewTextHandler(&logged, nil))
	// No BootstrapAdmin: this is the claim path.
	cfg := deployconfig.Config{Version: 1}
	if err := compose.EnsureInstallation(ctx, e.Pool, log, cfg); err != nil {
		t.Fatalf("an unprovisioned boot with no bootstrap_admin failed: %v — it must mint a token and serve, not refuse to start", err)
	}

	outstanding, err := identity.NewService(e.Pool).SetupTokenOutstanding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !outstanding {
		t.Fatal("boot minted no setup token, so the installation cannot be claimed")
	}

	// Both channels, because either alone can be lost: a log pipeline that
	// dropped the line, or a container with no writable config directory.
	tokenPath := filepath.Join(dir, "config", "margince-setup-token")
	raw, err := os.ReadFile(tokenPath) // #nosec G304 -- a path this test just built under t.TempDir()
	if err != nil {
		t.Fatalf("reading the announced token file: %v", err)
	}
	if len(raw) == 0 {
		t.Error("the token file is empty")
	}
	// The mode is the point, not incidental: this file is a credential that
	// claims the installation, and a test asserting only its contents passes
	// just as happily when it is world-readable.
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("the setup token file is mode %#o — group or other can read the credential that claims this installation", perm)
	}
	// The log names the file; it does not repeat what the file holds. Asserted
	// on the token VALUE rather than on the attribute key, because a line that
	// logs the same secret under a different key passes a key-shaped assertion.
	if strings.Contains(logged.String(), string(raw)) {
		t.Error("the boot log carries the setup token even though the file write succeeded — the log is read by everyone the log store admits and keeps the credential in a searchable index long after the claim")
	}
	if !strings.Contains(logged.String(), tokenPath) {
		t.Errorf("the boot log does not name the token file %q, so an operator reading the log cannot find the credential at all", tokenPath)
	}

	// A second boot must not replace it: the operator may already have read and
	// handed on the first.
	if err := compose.EnsureInstallation(ctx, e.Pool, log, cfg); err != nil {
		t.Fatalf("a second unprovisioned boot failed: %v", err)
	}
	again, err := os.ReadFile(filepath.Join(dir, "config", "margince-setup-token"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(raw) {
		t.Error("a restart replaced the outstanding setup token, invalidating the one already handed to an operator")
	}
}

// TestTheSetupTokenReachesTheLogOnlyWhenTheFileCouldNotBeWritten is the other
// direction of the two-channel rule, and the reason the channel exists: with the
// file unavailable the log is the operator's only way back into their own
// installation, so it carries the credential itself.
//
// The logged value is proven to BE the credential by claiming with it, not by
// matching the attribute key. A test that greps for "setup_token" passes against
// a line that logs a truncated value, a placeholder, or the hash.
func TestTheSetupTokenReachesTheLogOnlyWhenTheFileCouldNotBeWritten(t *testing.T) {
	e := apptest.SetupApp(t)
	ctx := context.Background()
	if _, err := e.Owner.Exec(ctx, `UPDATE workspace SET archived_at = now() WHERE archived_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	// Get to the path first, which is what makes the write fail: the writer
	// opens O_EXCL and refuses any existing final component.
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "margince-setup-token"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}

	var logged strings.Builder
	log := slog.New(slog.NewJSONHandler(&logged, nil))
	if err := compose.EnsureInstallation(ctx, e.Pool, log, deployconfig.Config{Version: 1}); err != nil {
		t.Fatalf("a boot whose token file could not be written must still serve, so the operator can claim: %v", err)
	}

	announced := loggedAttr(t, logged.String(), "setup_token")
	if announced == "" {
		t.Fatal("the token file could not be written and the log carries no token either — the installation is unclaimable")
	}
	if !strings.Contains(logged.String(), "write_error") {
		t.Error("the log discloses the token without the write failure that forced it, so a reader cannot tell a fallback from a leak")
	}

	srv := httptest.NewServer(compose.New(e.Pool, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	if status := claimStatus(t, srv, claimBody(announced)); status != http.StatusCreated {
		t.Errorf("claiming with the token the log announced returned %d, want 201 — the fallback logged something that is not the credential", status)
	}
}

// loggedAttr answers the value of a top-level attribute across JSON log records,
// so an assertion can be made about what was logged rather than about the text
// the handler happened to render.
func loggedAttr(t *testing.T, records, key string) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(records), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("the boot log is not JSON: %v", err)
		}
		if value, ok := record[key].(string); ok {
			return value
		}
	}
	return ""
}
