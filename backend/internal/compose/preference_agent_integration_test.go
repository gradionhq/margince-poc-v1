// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The un-overridable opt-out (B-E11.32, third acceptance line): a
// withdrawal recorded through the buyer's one-click preference surface
// blocks the modules/agents MCP send path too — the suppression is the
// SAME default-deny gate both transports ride, so it is
// RBAC-/Passport-independent. The human HTTP side of the same invariant
// lives in preference_center_integration_test.go.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestPreferenceCenterOptOutBlocksAgentSend(t *testing.T) {
	e := integration.Setup(t)
	consentStore := consent.NewStore(e.Pool)
	adapter := commsAdapter{store: activities.NewStore(e.Pool), gate: consent.NewGate(consentStore)}

	admin := e.Admin()
	personID := e.SeedPerson(t, "Opt Out Target", &e.Rep1)
	addPersonEmail(t, e, personID, "target@buyer.test")

	// A non-DOI marketing purpose, granted — so the agent send is initially
	// allowed and we prove the block is the opt-out, not a missing grant.
	purpose, err := consentStore.CreatePurpose(admin, "newsletter", "Newsletter", false)
	if err != nil {
		t.Fatalf("create purpose: %v", err)
	}
	if _, err := consentStore.Record(admin, consent.RecordInput{
		PersonID: ids.From[ids.PersonKind](personID), PurposeID: purpose.ID, NewState: "granted",
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// The reply anchor the MCP send threads onto.
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

	// An agent principal with the send-adjacent grants; the gate does not
	// consult it, which is the point — suppression is principal-independent.
	agentCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	agentCtx = principal.WithCorrelationID(agentCtx, ids.NewV7())
	agentCtx = principal.WithActor(agentCtx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:optout-probe",
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true, Read: true},
				"person":   {Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})

	send := func() error {
		_, err := adapter.SendEmail(agentCtx, anchorID, agents.SendEmailArgs{
			To: []string{"target@buyer.test"}, Subject: "Hi", Body: "b", ConsentPurpose: "newsletter",
		})
		return err
	}

	// Before opt-out the agent send is allowed.
	if err := send(); err != nil {
		t.Fatalf("granted agent send → %v, want success", err)
	}

	// The buyer one-click unsubscribes through the PUBLIC preference surface,
	// exactly as the anonymous middleware binds it (system principal).
	publicCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	publicCtx = principal.WithCorrelationID(publicCtx, ids.NewV7())
	publicCtx = principal.WithActor(publicCtx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:public_preferences",
	})
	if _, err := consentStore.PublicSetConsent(publicCtx, ids.From[ids.PersonKind](personID), "newsletter", "withdrawn", nil); err != nil {
		t.Fatalf("one-click withdrawal: %v", err)
	}

	// After opt-out the SAME agent send is refused at the shared gate.
	if err := send(); !errors.Is(err, apperrors.ErrConsentNotGranted) {
		t.Fatalf("agent send after opt-out → %v, want ErrConsentNotGranted", err)
	}
}

// addPersonEmail attaches an email channel to a person as admin, so the
// consent gate can resolve a recipient address to the subject.
func addPersonEmail(t *testing.T, e *integration.Env, personID ids.UUID, email string) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO person_email (workspace_id, person_id, email, email_type, is_primary, source, captured_by)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, 'work', true, 'manual', 'human:x')`,
			personID, email)
		return err
	}); err != nil {
		t.Fatalf("add email: %v", err)
	}
}
