// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The offer a signal produces, over a real Postgres.
//
// This is where the ScaleCommerce failure is finally closed. The mail said the
// contract ended, the record said Prospect, and nothing joined them. Now the
// signal states it, the page shows the disagreement, and this offers the fix —
// to a human, who accepts it, edits it, or says no and is not asked again.

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedAccountAtStage plants the account the whole overhaul is named for,
// filed under a stage.
func seedAccountAtStage(t *testing.T, e *integration.Env, stage string) ids.UUID {
	t.Helper()
	org := ids.NewV7()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization (id, workspace_id, display_name, lifecycle, source, captured_by)
			VALUES ($1, $2, 'ScaleCommerce', $3, 'gmail:seed', 'connector:gmail')`, org, e.WS, stage)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return org
}

// seedOpenContractEnded plants the signal the extraction site produces when a
// conversation says the relationship is over.
func seedOpenContractEnded(t *testing.T, e *integration.Env, org ids.UUID) ids.UUID {
	t.Helper()
	signal := ids.NewV7()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		// No fingerprint: these stand for signals a human filed, and the point
		// of several is that they are several rows rather than one deduped one.
		_, err := tx.Exec(context.Background(), `
			INSERT INTO signal (id, workspace_id, kind, source_channel, entity_type, entity_id,
			                    resolved_org_id, resolution_state, severity, summary, status,
			                    detected_at, source, captured_by)
			VALUES ($1, $2, 'contract_ended', 'derived', 'organization', $3, $3, 'resolved',
			        'warn', 'They wrote that the contract ends on 31 July.', 'open',
			        now(), 'signal-scan', 'agent:contract_ended')`,
			signal, e.WS, org)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return signal
}

// proposePass runs one reconciler pass and reports how many offers it staged.
func proposePass(t *testing.T, e *integration.Env) int {
	t.Helper()
	proposer := NewSignalProposer(e.Pool, approvalsServiceWithEffects(e.Pool),
		slog.New(slog.DiscardHandler))
	staged, err := proposer.RunWorkspace(e.Admin())
	if err != nil {
		t.Fatalf("RunWorkspace: %v", err)
	}
	return staged
}

// stagedOffer reads the one offer standing against an account.
func stagedOffer(t *testing.T, e *integration.Env, org ids.UUID) ids.UUID {
	t.Helper()
	var approvalID ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT id FROM approval WHERE kind = 'lifecycle_change' AND target_entity_id = $1`,
			org).Scan(&approvalID)
	}); err != nil {
		t.Fatalf("reading the staged offer: %v", err)
	}
	return approvalID
}

// accountStage reads what the record currently says it is.
func accountStage(t *testing.T, e *integration.Env, org ids.UUID) string {
	t.Helper()
	var stage string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT lifecycle FROM organization WHERE id = $1`, org).Scan(&stage)
	}); err != nil {
		t.Fatalf("reading the account's stage: %v", err)
	}
	return stage
}

// signalStatus reads where the triage of a signal stands.
func signalStatus(t *testing.T, e *integration.Env, signal ids.UUID) string {
	t.Helper()
	var status string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT status FROM signal WHERE id = $1`, signal).Scan(&status)
	}); err != nil {
		t.Fatalf("reading the signal: %v", err)
	}
	return status
}

// The whole arc: the mail ends the contract, the record still says Prospect, a
// human is asked, and their yes moves the account and settles the signal.
func TestAcceptingTheOfferMovesTheAccountAndSettlesTheSignal(t *testing.T) {
	e := integration.Setup(t)
	org := seedAccountAtStage(t, e, "prospect")
	signal := seedOpenContractEnded(t, e, org)

	if staged := proposePass(t, e); staged != 1 {
		t.Fatalf("the pass staged %d offers, want the one the contradiction deserves", staged)
	}
	// Nothing structural before the human says yes (GATE-AI-2).
	if stage := accountStage(t, e, org); stage != "prospect" {
		t.Fatalf("the account moved to %q before anyone decided", stage)
	}

	if _, err := approvalsServiceWithEffects(e.Pool).Decide(
		e.As(e.Rep1, nil, integration.AdminWithSignals),
		ids.From[ids.ApprovalKind](stagedOffer(t, e, org)), true, nil); err != nil {
		t.Fatalf("accepting the offer: %v", err)
	}

	if stage := accountStage(t, e, org); stage != "former_customer" {
		t.Errorf("the account reads %q after the accept, want former_customer", stage)
	}
	if status := signalStatus(t, e, signal); status != "acknowledged" {
		t.Errorf("the signal reads %q after the accept, want acknowledged — a page that "+
			"moved the account while still shouting the signal that moved it contradicts itself",
			status)
	}
}

// Saying no means the reader judged the record right and the reading wrong.
// The signal that produced the offer stays open forever, so without a durable
// memory of the refusal the same question comes back every hour.
func TestARefusedOfferIsNotAskedAgain(t *testing.T) {
	e := integration.Setup(t)
	org := seedAccountAtStage(t, e, "customer")
	signal := seedOpenContractEnded(t, e, org)

	proposePass(t, e)
	if _, err := approvalsServiceWithEffects(e.Pool).Decide(
		e.As(e.Rep1, nil, integration.AdminWithSignals),
		ids.From[ids.ApprovalKind](stagedOffer(t, e, org)), false, nil); err != nil {
		t.Fatalf("declining the offer: %v", err)
	}

	// Twice, because the refusal must hold on every later pass rather than
	// only the one that immediately follows the decline.
	proposePass(t, e)
	proposePass(t, e)

	if n := e.WsCount(t, `
		SELECT count(*) FROM approval
		 WHERE kind = 'lifecycle_change' AND target_entity_id = $1`, org); n != 1 {
		t.Errorf("%d offers after a decline, want the one that was declined", n)
	}
	if stage := accountStage(t, e, org); stage != "customer" {
		t.Errorf("the account reads %q after a decline, want the stage the reader kept", stage)
	}
	// The reader said the RECORD was right, not that the mail never happened.
	if status := signalStatus(t, e, signal); status != "open" {
		t.Errorf("the signal reads %q after a decline, want it left open — declining the "+
			"consequence is not a claim that the correspondence said something else", status)
	}
}

// An hourly reconciler must not stack the same question in the inbox.
func TestTheSameContradictionIsOfferedOnce(t *testing.T) {
	e := integration.Setup(t)
	org := seedAccountAtStage(t, e, "opportunity")
	seedOpenContractEnded(t, e, org)

	if standing := proposePass(t, e); standing != 1 {
		t.Fatalf("the first pass left %d offers standing, want 1", standing)
	}
	// The second pass finds the same contradiction and joins the offer it made
	// last time, so one offer still stands — and only one row exists.
	if standing := proposePass(t, e); standing != 1 {
		t.Errorf("a second pass reports %d offers standing on an unchanged account", standing)
	}
	if n := e.WsCount(t, `
		SELECT count(*) FROM approval
		 WHERE kind = 'lifecycle_change' AND target_entity_id = $1`, org); n != 1 {
		t.Errorf("the inbox carries %d copies of one question", n)
	}
}

// A human who fixed the stage by hand while the offer waited has already
// answered it. The offer is pinned to the record's version, so accepting it is
// REFUSED rather than silently spent — the decider is told the record moved
// under them, which is the one thing they need to know before deciding again.
func TestAnAcceptOverACorrectedRecordIsRefused(t *testing.T) {
	e := integration.Setup(t)
	org := seedAccountAtStage(t, e, "prospect")
	seedOpenContractEnded(t, e, org)
	proposePass(t, e)
	approvalID := stagedOffer(t, e, org)

	// Someone reads the same mail and files the account as disqualified.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE organization SET lifecycle = 'disqualified' WHERE id = $1`, org)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, err := approvalsServiceWithEffects(e.Pool).Decide(
		e.As(e.Rep1, nil, integration.AdminWithSignals),
		ids.From[ids.ApprovalKind](approvalID), true, nil)
	if !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("accepting a stale offer returned %v, want a version conflict — a decider "+
			"acting on a record that moved under them must be told, not obeyed", err)
	}
	if stage := accountStage(t, e, org); stage != "disqualified" {
		t.Errorf("the account reads %q, want the stage the human set — a stale offer must "+
			"not overwrite the edit that answered it", stage)
	}
}

// An account already filed as over is not in conflict with the mail that says
// so, and there is nothing to offer.
func TestNoOfferIsMadeOnAnAccountAlreadyFiledAsOver(t *testing.T) {
	e := integration.Setup(t)
	org := seedAccountAtStage(t, e, "former_customer")
	seedOpenContractEnded(t, e, org)

	if staged := proposePass(t, e); staged != 0 {
		t.Fatalf("the pass staged %d offers on an account already filed as former_customer", staged)
	}
}

// An account can carry several signals saying the same thing — three
// conversations that each mention the contract ending are three rows. The
// human answering the proposal has answered the question all of them ask.
//
// Settling only the quoted row leaves the rest open forever: the record has
// left the stage the reconciler looks for, so no later pass considers them and
// nothing else will ever close them.
func TestAcceptingSettlesEveryOpenContradictionOnTheAccount(t *testing.T) {
	e := integration.Setup(t)
	org := seedAccountAtStage(t, e, "customer")
	first := seedOpenContractEnded(t, e, org)
	second := seedOpenContractEnded(t, e, org)
	third := seedOpenContractEnded(t, e, org)

	proposePass(t, e)
	if _, err := approvalsServiceWithEffects(e.Pool).Decide(
		e.As(e.Rep1, nil, integration.AdminWithSignals),
		ids.From[ids.ApprovalKind](stagedOffer(t, e, org)), true, nil); err != nil {
		t.Fatalf("accepting the offer: %v", err)
	}

	for _, signal := range []ids.UUID{first, second, third} {
		if status := signalStatus(t, e, signal); status != "acknowledged" {
			t.Errorf("signal %s reads %q after the accept, want acknowledged — the "+
				"account has left the stage this rule looks for, so nothing else "+
				"would ever close it", signal, status)
		}
	}
}
