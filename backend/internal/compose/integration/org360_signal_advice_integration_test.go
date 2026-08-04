// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Signal-derived advice end to end, over a real database: the conflict between
// what the record says and what its own correspondence says, and the one signal
// the state strip has room to state.
//
// Every fixture sets its timestamps explicitly. The read's clock is pinned to
// org360Clock while the database's now() is not, so a fixture on now() would
// land on the wrong side of a stale-thread window by accident.

import (
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The failure this whole surface was named for: an account holding a mail that
// ends the contract, filed under a stage that says the relationship is live.
//
// Both facts were already in the record and nothing put them next to each
// other. The page states the disagreement and leaves which side is wrong to
// the reader — but it must state it FIRST, because acting on a stage that is
// wrong is worse than not acting at all.
func TestTheRecordAndItsOwnMailAreShownToDisagree(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360SignalPerms)

	e.WsExec(t, `UPDATE organization SET lifecycle = 'customer' WHERE id = $1`, org.UUID)
	seedSignal(t, e, org.UUID, "contract_ended", "warn",
		"They wrote that the contract ends on 31 July.", "2026-05-20T09:00:00Z")
	// Advice that would otherwise lead, so leading is a choice this makes and
	// not the only thing left standing.
	seedUnansweredOutbound(t, e, org.UUID)

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	found := *view.Suggestions
	if len(found) == 0 || string(found[0].Kind) != "lifecycle_conflict" {
		t.Fatalf("the card leads with %v, want lifecycle_conflict — the record contradicts "+
			"its own correspondence, and no other advice on it can be trusted until that is settled",
			kindsOf(found))
	}
	if !strings.Contains(found[0].Reason, "customer") {
		t.Errorf("the reason is %q, want it to name the stage the mail contradicts — "+
			"a conflict a reader cannot see both sides of is not one they can settle",
			found[0].Reason)
	}
}

// A stage that already reads as over is not in conflict with the mail that
// says so; it is that mail's conclusion. Firing there would hand every closed
// account a permanent card nobody can clear.
func TestAnEndedContractDoesNotContradictAnEndedRelationship(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360SignalPerms)

	e.WsExec(t, `UPDATE organization SET lifecycle = 'former_customer' WHERE id = $1`, org.UUID)
	seedSignal(t, e, org.UUID, "contract_ended", "warn",
		"They wrote that the contract ends on 31 July.", "2026-05-20T09:00:00Z")

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	for _, suggestion := range *view.Suggestions {
		if string(suggestion.Kind) == "lifecycle_conflict" {
			t.Fatalf("the card claims a conflict on an account already filed as former_customer")
		}
	}
}

// kindsOf names what a card actually offered, so a failure says what was there
// instead of only what was missing.
func kindsOf(found []crmcontracts.Organization360Suggestion) []string {
	kinds := make([]string, 0, len(found))
	for _, suggestion := range found {
		kinds = append(kinds, string(suggestion.Kind))
	}
	return kinds
}

// A caller who may not read signals is told nothing about them, not told there
// is nothing. The rule reads a record the reader has no right to, so it stays
// silent rather than leaking its existence through advice.
func TestTheConflictStaysSilentWithoutTheSignalGrant(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))

	e.WsExec(t, `UPDATE organization SET lifecycle = 'customer' WHERE id = $1`, org.UUID)
	seedSignal(t, e, org.UUID, "contract_ended", "warn",
		"They wrote that the contract ends on 31 July.", "2026-05-20T09:00:00Z")

	// org360RepPerms carries no signal grant, which is the point.
	view, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms), org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	for _, suggestion := range *view.Suggestions {
		if string(suggestion.Kind) == "lifecycle_conflict" {
			t.Fatalf("a caller without the signal grant was shown advice derived from one")
		}
	}
}

// seedSignal writes one open derived signal at a severity and an instant.
func seedSignal(t *testing.T, e *Env, org ids.UUID, kind, severity, summary, at string) {
	t.Helper()
	SeedRow(t, OwnerConn(t), `INSERT INTO signal
		(id, workspace_id, kind, source_channel, entity_type, entity_id, resolved_org_id,
		 resolution_state, severity, summary, status, detected_at, source, captured_by)
		VALUES ($1, $2, '`+kind+`', 'derived', 'organization', '`+org.String()+`',
		        '`+org.String()+`', 'resolved', '`+severity+`', '`+summary+`', 'open',
		        '`+at+`', 'signal-scan', 'agent:`+kind+`')`, e.WS)
}

// The strip has room for one signal and the account may have several. It
// states the most serious, and among equals the newest — an old warning that
// has been sitting there is less news than the one that just arrived.
func TestTheStripStatesTheWorstOpenSignal(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360SignalPerms)

	seedSignal(t, e, org.UUID, "commitment_made", "info",
		"They promised volumes by Friday.", "2026-05-20T09:00:00Z")
	seedSignal(t, e, org.UUID, "ghosted_thread", "warn",
		"We wrote 20 days ago and nobody has answered.", "2026-05-01T09:00:00Z")
	seedSignal(t, e, org.UUID, "contract_ended", "warn",
		"They wrote that the contract ends on 31 July.", "2026-05-21T09:00:00Z")

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.StateStrip == nil || view.StateStrip.Signal == nil {
		t.Fatal("the strip states no signal on an account carrying three")
	}
	if view.StateStrip.Signal.Kind != "contract_ended" {
		t.Errorf("the strip leads with %q, want the newest of the two warnings",
			view.StateStrip.Signal.Kind)
	}
	if view.StateStrip.Signal.Summary != "They wrote that the contract ends on 31 July." {
		t.Errorf("the strip says %q, want the producer's own sentence",
			view.StateStrip.Signal.Summary)
	}
}

// A caller who may not read signals is told nothing about them — not told
// there is nothing. The tile is absent, and the difference is what the signals
// card is for.
func TestTheStripStatesNoSignalWithoutTheGrant(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))

	seedSignal(t, e, org.UUID, "contract_ended", "warn",
		"They wrote that the contract ends on 31 July.", "2026-05-21T09:00:00Z")

	// org360RepPerms carries no signal grant, which is the point.
	view, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms), org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.StateStrip != nil && view.StateStrip.Signal != nil {
		t.Fatalf("the strip stated a signal to a caller with no grant to read one")
	}
}
