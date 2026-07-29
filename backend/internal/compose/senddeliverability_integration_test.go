// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Deliverability is a property of the COMPOSITION, not of one transport. The
// MCP send_email tool sends through the comms seam newCommsAdapter builds, so
// this suite drives that seam and asserts a marketing send leaves it carrying
// the RFC 8058 header and the visible footer. A suite that only drove the HTTP
// handlers' store would pass while the tool surface transmitted bulk mail with
// no unsubscribe surface at all.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

const toolSurfaceBaseURL = "https://mail.example.test"

func TestToolSurfaceSendCarriesTheUnsubscribeSurface(t *testing.T) {
	e := integration.Setup(t)
	consentStore := consent.NewStore(e.Pool)
	admin := e.Admin()

	person := e.SeedPerson(t, "Newsletter Reader", &e.Rep1)
	addPersonEmail(t, e, person, "reader@buyer.test")
	// Two granted purposes: a marketing one, which carries an unsubscribe
	// surface, and the locked transactional one, which by definition does not.
	for _, key := range []string{"newsletter", "transactional"} {
		purpose, err := consentStore.CreatePurpose(admin, key, key, false)
		if err != nil {
			t.Fatalf("create purpose %s: %v", key, err)
		}
		if _, err := consentStore.Record(admin, consent.RecordInput{
			PersonID: ids.From[ids.PersonKind](person), PurposeID: purpose.ID, NewState: "granted",
		}); err != nil {
			t.Fatalf("grant %s: %v", key, err)
		}
	}

	anchorID := ids.NewV7()
	if err := database.WithWorkspaceTx(admin, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
			VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'email', 'Pricing question', now(), 'manual', 'human:x')`, anchorID)
		return err
	}); err != nil {
		t.Fatalf("seed anchor: %v", err)
	}

	stager := &recordingStager{}
	// The SAME constructor registry.go builds the tool surface's seam with,
	// carrying the SAME configuration the api role supplies.
	adapter := newCommsAdapter(e.Pool, nil, SendPath{
		PublicBaseURL: toolSurfaceBaseURL,
		Delivery:      stager,
	})

	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.SchedulerPerms)
	if _, err := adapter.SendEmail(ctx, anchorID, agents.SendEmailArgs{
		To: []string{"reader@buyer.test"}, Subject: "Monthly news", Body: "Here is the news.",
		ConsentPurpose: "newsletter",
	}); err != nil {
		t.Fatalf("marketing send through the tool surface: %v", err)
	}

	if len(stager.staged) != 1 {
		t.Fatalf("staged %d deliveries, want 1", len(stager.staged))
	}
	staged := stager.staged[0]
	wantPrefix := "<" + toolSurfaceBaseURL + "/v1/public/preferences/"
	if !strings.HasPrefix(staged.ListUnsubscribe, wantPrefix) || !strings.Contains(staged.ListUnsubscribe, "purpose=newsletter") {
		t.Fatalf("List-Unsubscribe = %q, want a one-click URL on the configured base (%s…) — an unconfigured store yields the empty string here",
			staged.ListUnsubscribe, wantPrefix)
	}
	if !strings.Contains(staged.Body, "Unsubscribe: "+toolSurfaceBaseURL+"/v1/public/preferences/") {
		t.Fatalf("no visible unsubscribe footer on the transmitted body:\n%s", staged.Body)
	}
	// The minted identity is qualified by the configured origin, not by the
	// reserved fallback an unconfigured store mints under.
	if !strings.HasSuffix(staged.MessageID, "@mail.example.test") {
		t.Fatalf("message id = %q, want it qualified by the configured public origin", staged.MessageID)
	}

	// A transactional purpose has nothing to unsubscribe from, so the same
	// configured seam adds no header and leaves the body as written.
	if _, err := adapter.SendEmail(ctx, anchorID, agents.SendEmailArgs{
		To: []string{"reader@buyer.test"}, Subject: "Your invoice", Body: "Attached.",
		ConsentPurpose: "transactional",
	}); err != nil {
		t.Fatalf("transactional send through the tool surface: %v", err)
	}
	if len(stager.staged) != 2 {
		t.Fatalf("staged %d deliveries, want 2", len(stager.staged))
	}
	if transactional := stager.staged[1]; transactional.ListUnsubscribe != "" || transactional.Body != "Attached." {
		t.Fatalf("transactional send carried List-Unsubscribe %q / body %q, want neither",
			transactional.ListUnsubscribe, transactional.Body)
	}
}
