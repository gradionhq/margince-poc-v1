// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Every account-scoped producer, run against the shape capture actually writes.
//
// This is a fitness test, not a feature test: it owns no behaviour of its own
// and asserts nothing about what any producer concludes. It seeds ONE workspace
// the way a connector does — mail filed against a PERSON, the account reachable
// only through that person's employment, and no direct organization link
// anywhere — and requires that each producer still finds the account.
//
// A fixture that hand-writes a link no connector emits proves the producer
// against data that does not exist: it passes while the producer finds nothing
// on every real workspace, and nothing about the green tells anyone. Seeding
// what the writer writes is the only way a test can fail for the right reason.
//
// A producer added later belongs in this list. The cost of joining it is a
// couple of lines; the cost of staying out of it is a feature that looks
// finished, passes review, and does nothing.

import (
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// captureShapeClock pins the fixture's instants so a conversation lands on the
// settled side of every window by construction rather than by whatever the wall
// clock says when CI runs.
var captureShapeClock = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// seedAccountAsCaptureWould builds the one fixture this file is about: an
// account worth working, a contact who works there, and a conversation filed
// against that contact and nobody else.
func seedAccountAsCaptureWould(t *testing.T, e *Env) ids.UUID {
	t.Helper()
	org := e.SeedOrg(t, "Capture Shape Co", &e.Rep1)
	e.WsExec(t, `UPDATE organization SET lifecycle = 'opportunity' WHERE id = $1`, org)
	contact := employeeOf(t, e, org, "Ada at Capture Shape Co")
	// Old enough for the ghosted rule's fortnight and settled for the
	// extractor's six hours, so one fixture serves every producer.
	seedMessage(t, e, contact, "thread-capture-shape", "Proposal",
		"Sending our proposal over.", "outbound", captureShapeClock.AddDate(0, 0, -30))

	var direct int
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM activity_link
			 WHERE entity_type = 'organization'`).Scan(&direct)
	}); err != nil {
		t.Fatalf("count the direct account links: %v", err)
	}
	if direct != 0 {
		t.Fatalf("the fixture wrote %d direct organization links; capture writes none, "+
			"and a producer proved against one is proved against nothing", direct)
	}
	return org
}

// The deterministic producer reaches an account it can only see through
// employment. It needs no model, so this half must work on an installation
// that bought none.
func TestTheGhostedRuleReachesAnAccountCaptureLinkedThroughAPerson(t *testing.T) {
	e := Setup(t)
	org := seedAccountAsCaptureWould(t, e)

	pass := ghostedPass(t, e, captureShapeClock)
	if pass.Considered == 0 {
		t.Fatal("the rule considered no account at all: the walk from a captured " +
			"message to its account resolves nothing")
	}
	if pass.Raised != 1 {
		t.Fatalf("the rule wrote %d signals, want the one unanswered tail", pass.Raised)
	}
	if kinds := openSignalKinds(t, e, org); len(kinds) != 1 || kinds[0] != "ghosted_thread" {
		t.Fatalf("the account carries %v, want the ghosted_thread the comparison found", kinds)
	}
}

// The model producer is offered the same conversation. What it concludes is
// the model's business and no assertion is made about it — that the
// conversation reaches the queue at all is the invariant under test.
func TestTheExtractorIsOfferedAConversationCaptureLinkedThroughAPerson(t *testing.T) {
	e := Setup(t)
	seedAccountAsCaptureWould(t, e)

	brain := &scriptedBrain{reply: `{"events": []}`}
	extractor := compose.NewSignalExtractor(e.Pool, brain,
		func() time.Time { return captureShapeClock }, slog.Default())
	pass, err := extractor.RunWorkspace(e.Admin(), ids.From[ids.WorkspaceKind](e.WS))
	if err != nil {
		t.Fatalf("signal extract: %v", err)
	}
	if pass.Due == 0 {
		t.Fatal("the queue offered no conversation: the walk from a captured " +
			"message to its account resolves nothing")
	}
	if brain.calls != 1 {
		t.Fatalf("the model was asked %d times, want the one settled conversation", brain.calls)
	}
}

// The reader-facing side of the same walk. The producers and the timeline the
// account's page shows must agree about which messages belong to it: a signal
// about correspondence the reader cannot find on the page is unanswerable.
//
// It asks activities.OrgLinkedActivityExists rather than a copy of the three
// arms. A hand-spelled walk here would keep passing against whatever the arms
// used to be, which is the failure this whole file exists to prevent, wearing
// a test's clothes.
func TestTheAccountTimelineCountsMailCaptureLinkedThroughAPerson(t *testing.T) {
	e := Setup(t)
	org := seedAccountAsCaptureWould(t, e)

	var reached int
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM activity a
			 WHERE a.archived_at IS NULL AND `+activities.OrgLinkedActivityExists(1),
			org).Scan(&reached)
	}); err != nil {
		t.Fatalf("count the account's reachable mail: %v", err)
	}
	if reached != 1 {
		t.Fatalf("the account reaches %d messages, want the one captured against its contact", reached)
	}
}
