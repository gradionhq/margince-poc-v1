// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The A107 boot bootstrap end to end over its CONFIGURED shape: the
// deployment file's seeds section drives the pipeline, the consent
// catalog, and the starter toggles — and the singleton invariant holds
// at the HTTP surface: a second active workspace turns every request
// into the operator-facing 503 availability state, never an auth error.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func boolPtr(v bool) *bool { return &v }

func TestBootstrapSeedsFollowTheDeploymentConfiguration(t *testing.T) {
	e := setup(t)
	pwFile := filepath.Join(t.TempDir(), "admin-password")
	if err := os.WriteFile(pwFile, []byte("correct-horse-battery"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := deployconfig.Config{
		Version:      1,
		Organization: deployconfig.Organization{Name: "Configured Org", BaseCurrency: "USD", Timezone: "Europe/Berlin"},
		BootstrapAdmin: &deployconfig.BootstrapAdmin{
			Email: "ops@configured.test", DisplayName: "Ops", PasswordFile: pwFile,
		},
		Seeds: deployconfig.Seeds{
			Pipeline: &deployconfig.PipelineSeed{
				Name: "Projects",
				Stages: []deployconfig.PipelineStage{
					{Name: "Scoping", Probability: 20},
					{Name: "Delivery", Probability: 80},
				},
			},
			ConsentPurposes: []deployconfig.ConsentPurpose{
				{Key: "newsletter", Label: "Newsletter", DoubleOptIn: true},
			},
			StarterAutomations: boolPtr(false),
			BookingPage:        boolPtr(false),
		},
	}
	if err := compose.EnsureInstallation(context.Background(), e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	ctx := context.Background()

	// The configured pipeline: the two open stages plus the module-owned
	// Won/Lost terminal pair, in order.
	rows, err := e.owner.Query(ctx,
		`SELECT s.name, s.semantic FROM stage s JOIN pipeline p ON p.id = s.pipeline_id
		 WHERE p.name = 'Projects' ORDER BY s.position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name, semantic string
		if err := rows.Scan(&name, &semantic); err != nil {
			t.Fatal(err)
		}
		got = append(got, name+"/"+semantic)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"Scoping/open", "Delivery/open", "Won/won", "Lost/lost"}
	if len(got) != len(want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stages = %v, want %v", got, want)
		}
	}

	// The consent catalog: the module-invariant transactional lane plus
	// exactly the configured purpose.
	var purposes int
	if err := e.owner.QueryRow(ctx,
		`SELECT count(*) FROM consent_purpose WHERE key IN ('transactional','newsletter')`).Scan(&purposes); err != nil {
		t.Fatal(err)
	}
	var total, automations, bookingPages int
	if err := e.owner.QueryRow(ctx, `SELECT count(*) FROM consent_purpose`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if purposes != 2 || total != 2 {
		t.Fatalf("consent catalog holds %d/%d rows, want exactly transactional + newsletter", purposes, total)
	}

	// The toggles: no starter automations, no booking page.
	if err := e.owner.QueryRow(ctx, `SELECT count(*) FROM automation`).Scan(&automations); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.QueryRow(ctx, `SELECT count(*) FROM booking_page`).Scan(&bookingPages); err != nil {
		t.Fatal(err)
	}
	if automations != 0 || bookingPages != 0 {
		t.Fatalf("starter_automations=%d booking_pages=%d, want both 0 (toggled off)", automations, bookingPages)
	}

	// The organization carries the configured currency; the admin signs in
	// through the normal login.
	var currency string
	if err := e.owner.QueryRow(ctx, `SELECT base_currency FROM workspace WHERE name = 'Configured Org'`).Scan(&currency); err != nil {
		t.Fatal(err)
	}
	if currency != "USD" {
		t.Fatalf("base_currency = %q, want the configured USD", currency)
	}
	if status := e.call(t, "POST", "/v1/auth/login", anyMap{
		"email": "ops@configured.test", "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("configured admin login → %d", status)
	}
}

func TestSecondActiveWorkspaceTurnsTheSurfaceUnavailable(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A second active workspace violates the single-organization
	// invariant. A server binding AFTER that point (fresh process, no
	// cached singleton) must answer 503 on every request — an operator
	// condition, never an auth failure.
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Rogue Second', 'rogue-second', 'EUR')`,
		ids.NewV7()); err != nil {
		t.Fatal(err)
	}
	fresh := httptest.NewServer(compose.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(fresh.Close)

	resp, err := fresh.Client().Get(fresh.URL + "/v1/auth/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeBody(t, resp) })
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("multi-workspace surface answered %d, want 503 (availability, not auth)", resp.StatusCode)
	}
}
