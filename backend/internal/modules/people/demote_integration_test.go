// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Demotion is the audited reverse of promotion (formulas §26). Its two
// branches are decided from the promote AUDIT ROW, and its hard block reads
// the relationship graph — both live in Postgres, so a unit test would only
// prove that a fake said what the test told it to.

import (
	"context"
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// personState reads the two columns a demotion touches on the person side.
func (e *promoteConsentEnv) personState(t *testing.T, id ids.UUID) (archived bool, convertedFrom *ids.UUID) {
	t.Helper()
	if err := e.owner.QueryRow(context.Background(),
		`SELECT archived_at IS NOT NULL, converted_from_lead_id FROM person WHERE id = $1`, id,
	).Scan(&archived, &convertedFrom); err != nil {
		t.Fatal(err)
	}
	return archived, convertedFrom
}

// A promotion that CREATED the person unwinds fully: the lead is back at
// work, the person it minted is archived, and the lineage is gone both ways.
func TestDemoteReversesAPromotionThatCreatedThePerson(t *testing.T) {
	e := setupPromoteConsent(t)
	lead := e.seedLead(t, "fresh@example.test")
	person, merged, err := e.store.PromoteLead(e.ctx, lead, PromoteLeadInput{Trigger: "human_qualify"})
	if err != nil || merged {
		t.Fatalf("promote: err=%v merged=%t, want a created person", err, merged)
	}

	out, err := e.store.DemoteLead(e.ctx, lead, "promoted the wrong prospect")
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if out.Unwind != crmcontracts.DemoteUnwindReversed {
		t.Errorf("unwind = %q, want reversed: the promotion created this person", out.Unwind)
	}
	if out.Lead.Status != crmcontracts.LeadStatusWorking || out.Lead.ArchivedAt != nil || out.Lead.PromotedPersonId != nil {
		t.Errorf("restored lead = status %q archived=%v promoted_person=%v; want working, live, unlinked",
			out.Lead.Status, out.Lead.ArchivedAt, out.Lead.PromotedPersonId)
	}
	archived, from := e.personState(t, ids.UUID(person.Id))
	if !archived || from != nil {
		t.Errorf("created person archived=%t converted_from=%v; want archived with the lineage nulled", archived, from)
	}

	// The undo of an undo is not a thing: a second demote finds nothing to reverse.
	var notPromoted *NotPromotedError
	if _, err := e.store.DemoteLead(e.ctx, lead, "again"); !errors.As(err, &notPromoted) {
		t.Errorf("second demote err = %v, want NotPromotedError (409)", err)
	}
}

// A promotion that MERGED into a person we already had leaves that person
// exactly as they are — only the lineage pointer is nulled.
func TestDemoteOfAMergeTouchesOnlyTheLineage(t *testing.T) {
	e := setupPromoteConsent(t)
	const shared = "known@example.test"
	existing, err := e.store.CreatePerson(e.ctx, CreatePersonInput{
		FullName: "Known Contact",
		Emails:   []PersonEmailInput{{Email: shared, EmailType: "work", IsPrimary: true}},
		Source:   "manual",
	})
	if err != nil {
		t.Fatalf("seed the incumbent person: %v", err)
	}
	lead := e.seedLead(t, shared)
	if _, merged, err := e.store.PromoteLead(e.ctx, lead, PromoteLeadInput{Trigger: "human_qualify"}); err != nil || !merged {
		t.Fatalf("promote: err=%v merged=%t, want a merge", err, merged)
	}
	if _, from := e.personState(t, ids.UUID(existing.Id)); from == nil {
		t.Fatal("promotion did not record the lineage on the incumbent; the test cannot see it removed")
	}

	out, err := e.store.DemoteLead(e.ctx, lead, "not the same person after all")
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if out.Unwind != crmcontracts.DemoteUnwindMergeLineageOnly {
		t.Errorf("unwind = %q, want merge_lineage_only", out.Unwind)
	}
	archived, from := e.personState(t, ids.UUID(existing.Id))
	if archived || from != nil {
		t.Errorf("incumbent archived=%t converted_from=%v; want live and unlinked — a merge is never un-merged", archived, from)
	}
	if out.Lead.Status != crmcontracts.LeadStatusWorking || out.Lead.ArchivedAt != nil {
		t.Errorf("restored lead = %q archived=%v; want working and live", out.Lead.Status, out.Lead.ArchivedAt)
	}
}

// A person merge repoints the lead's outcome pointer to the survivor while
// the promote audit row still says "created". Archiving that survivor would
// destroy the merged-in person's history, so the demote downgrades to
// lineage-only — whatever the promotion originally did.
func TestDemoteNeverArchivesAMergeSurvivor(t *testing.T) {
	e := setupPromoteConsent(t)
	lead := e.seedLead(t, "created@example.test")
	created, merged, err := e.store.PromoteLead(e.ctx, lead, PromoteLeadInput{Trigger: "human_qualify"})
	if err != nil || merged {
		t.Fatalf("promote: err=%v merged=%t, want a created person", err, merged)
	}
	other, err := e.store.CreatePerson(e.ctx, CreatePersonInput{
		FullName: "Other Contact",
		Emails:   []PersonEmailInput{{Email: "other@example.test", EmailType: "work", IsPrimary: true}},
		Source:   "manual",
	})
	if err != nil {
		t.Fatalf("seed the other person: %v", err)
	}
	// The created person survives the merge; the other's history now lives on it.
	if _, err := e.store.MergePerson(e.ctx, ids.From[ids.PersonKind](ids.UUID(other.Id)), ids.From[ids.PersonKind](ids.UUID(created.Id))); err != nil {
		t.Fatalf("merge: %v", err)
	}

	out, err := e.store.DemoteLead(e.ctx, lead, "wrong prospect")
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if out.Unwind != crmcontracts.DemoteUnwindMergeLineageOnly {
		t.Errorf("unwind = %q, want merge_lineage_only: the person is a merge survivor", out.Unwind)
	}
	archived, from := e.personState(t, ids.UUID(created.Id))
	if archived || from != nil {
		t.Errorf("survivor archived=%t converted_from=%v; want live and unlinked", archived, from)
	}
}

// The hard block: a person who is a stakeholder on a live deal keeps their
// commercial state. Nothing moves, and the refusal names the rule.
func TestDemoteRefusesWhenThePersonIsOnALiveDeal(t *testing.T) {
	e := setupPromoteConsent(t)
	lead := e.seedLead(t, "buyer@example.test")
	person, _, err := e.store.PromoteLead(e.ctx, lead, PromoteLeadInput{Trigger: "meeting_held"})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	ctx := context.Background()
	pipeline, stage, deal := ids.NewV7(), ids.NewV7(), ids.NewV7()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO pipeline (id, name, is_default, position) VALUES ($1, 'Sales', true, 0)`, []any{pipeline}},
		{`INSERT INTO stage (id, pipeline_id, name, position, semantic, win_probability) VALUES ($1, $2, 'Qualify', 0, 'open', 10)`, []any{stage, pipeline}},
		{`INSERT INTO deal (id, owner_id, name, pipeline_id, stage_id, source, captured_by) VALUES ($1, $2, 'Rollout', $3, $4, 'manual', 'human:x')`, []any{deal, e.user, pipeline, stage}},
		{`INSERT INTO relationship (kind, person_id, deal_id, source, captured_by) VALUES ('deal_stakeholder', $1, $2, 'manual', 'human:x')`, []any{ids.UUID(person.Id), deal}},
	} {
		if _, err := e.owner.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed deal graph: %v", err)
		}
	}

	var hasDeal *PersonHasDealError
	if _, err := e.store.DemoteLead(e.ctx, lead, "oops"); !errors.As(err, &hasDeal) {
		t.Fatalf("demote err = %v, want PersonHasDealError (422 person_has_deal)", err)
	}
	archived, from := e.personState(t, ids.UUID(person.Id))
	if archived || from == nil {
		t.Errorf("a refused demote must change nothing: person archived=%t converted_from=%v", archived, from)
	}
	var status string
	if err := e.owner.QueryRow(ctx, `SELECT status FROM lead WHERE id = $1`, lead).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "promoted" {
		t.Errorf("lead status = %q after a refused demote, want promoted", status)
	}
}

// The preview runs the promotion's own ladder without writing, so it names
// the outcome the promotion will take — and leaves no trace of having asked.
func TestPromotePreviewNamesTheOutcomeWithoutWriting(t *testing.T) {
	e := setupPromoteConsent(t)
	const shared = "known@example.test"
	existing, err := e.store.CreatePerson(e.ctx, CreatePersonInput{
		FullName: "Known Contact",
		Emails:   []PersonEmailInput{{Email: shared, EmailType: "work", IsPrimary: true}},
		Source:   "manual",
	})
	if err != nil {
		t.Fatalf("seed the incumbent person: %v", err)
	}
	fresh := e.seedLead(t, "nobody@example.test")
	known := e.seedLead(t, shared)

	preview, err := e.store.PreviewLeadPromotion(e.ctx, fresh)
	if err != nil {
		t.Fatalf("preview fresh: %v", err)
	}
	if preview.Outcome != crmcontracts.PromoteLeadPreviewOutcomeCreate || preview.Person != nil {
		t.Errorf("fresh lead preview = %+v, want create with no person", preview)
	}
	preview, err = e.store.PreviewLeadPromotion(e.ctx, known)
	if err != nil {
		t.Fatalf("preview known: %v", err)
	}
	if preview.Outcome != crmcontracts.PromoteLeadPreviewOutcomeMerge || preview.Person == nil || preview.Person.Id != existing.Id {
		t.Errorf("known lead preview = %+v, want merge into %v", preview, existing.Id)
	}

	var status string
	var promotedPerson *ids.UUID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status, promoted_person_id FROM lead WHERE id = $1`, known).Scan(&status, &promotedPerson); err != nil {
		t.Fatal(err)
	}
	if status != "working" || promotedPerson != nil {
		t.Errorf("preview wrote to the lead: status=%q promoted_person=%v", status, promotedPerson)
	}
	// After the real promotion the preview has nothing to say.
	if _, _, err := e.store.PromoteLead(e.ctx, known, PromoteLeadInput{Trigger: "human_qualify"}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	var already *AlreadyPromotedError
	if _, err := e.store.PreviewLeadPromotion(e.ctx, known); !errors.As(err, &already) {
		t.Errorf("preview of a promoted lead err = %v, want AlreadyPromotedError (409)", err)
	}
}

// withGrants answers a context for the same user with exactly the object
// grants given — how a role narrower than admin reaches the store.
func (e *promoteConsentEnv) withGrants(objects map[string]principal.ObjectGrant) context.Context {
	opCtx := principal.WithWorkspaceID(context.Background(), e.ws)
	opCtx = principal.WithCorrelationID(opCtx, ids.NewV7())
	return principal.WithActor(opCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.user.String(), UserID: e.user,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects:  objects,
			RowScope: principal.RowScopeAll,
		},
	})
}

// A rep who may work leads and create contacts — the grants promotion itself
// runs under — can also undo a promotion, without holding person.delete.
func TestDemoteNeedsNoArchiveGrantToUndoACreatedPerson(t *testing.T) {
	e := setupPromoteConsent(t)
	rep := e.withGrants(map[string]principal.ObjectGrant{
		"lead":   {Create: true, Read: true, Update: true, Delete: true},
		"person": {Create: true, Read: true, Update: true},
	})
	lead := e.seedLead(t, "undo@example.test")
	person, _, err := e.store.PromoteLead(rep, lead, PromoteLeadInput{Trigger: "human_qualify"})
	if err != nil {
		t.Fatalf("promote as rep: %v", err)
	}
	out, err := e.store.DemoteLead(rep, lead, "clicked the wrong row")
	if err != nil {
		t.Fatalf("demote as rep: %v — the reversal must run under the same authority as the promotion", err)
	}
	if out.Unwind != crmcontracts.DemoteUnwindReversed {
		t.Errorf("unwind = %q, want reversed", out.Unwind)
	}
	if archived, _ := e.personState(t, ids.UUID(person.Id)); !archived {
		t.Error("the created person must be archived by the reversal")
	}
}

// A person that a SECOND lead has since promoted into is no longer only what
// the first promotion minted: archiving it would strand the second lead's
// contact, so the first lead's demote unwinds lineage-only.
func TestDemoteKeepsAPersonAnotherLeadPromotedInto(t *testing.T) {
	e := setupPromoteConsent(t)
	const shared = "twice@example.test"
	first := e.seedLead(t, shared)
	person, merged, err := e.store.PromoteLead(e.ctx, first, PromoteLeadInput{Trigger: "human_qualify"})
	if err != nil || merged {
		t.Fatalf("first promote: err=%v merged=%t, want a created person", err, merged)
	}
	second := e.seedLead(t, shared)
	if _, merged, err := e.store.PromoteLead(e.ctx, second, PromoteLeadInput{Trigger: "inbound_reply"}); err != nil || !merged {
		t.Fatalf("second promote: err=%v merged=%t, want a merge into the first's person", err, merged)
	}

	out, err := e.store.DemoteLead(e.ctx, first, "the first capture was wrong")
	if err != nil {
		t.Fatalf("demote first: %v", err)
	}
	if out.Unwind != crmcontracts.DemoteUnwindMergeLineageOnly {
		t.Errorf("unwind = %q, want merge_lineage_only: another lead depends on this person", out.Unwind)
	}
	if archived, _ := e.personState(t, ids.UUID(person.Id)); archived {
		t.Error("the person the second lead promoted into must stay live")
	}
	var secondStatus string
	var secondPerson *ids.UUID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status, promoted_person_id FROM lead WHERE id = $1`, second).Scan(&secondStatus, &secondPerson); err != nil {
		t.Fatal(err)
	}
	if secondStatus != "promoted" || secondPerson == nil || *secondPerson != ids.UUID(person.Id) {
		t.Errorf("second lead = %q -> %v; its promotion must be untouched", secondStatus, secondPerson)
	}
}

// The preview returns a person, so it needs the person READ grant as well as
// the lead's: a role that may work leads but not read contacts is told the
// outcome (merge) and nothing about who.
func TestPromotePreviewWithholdsThePersonWithoutThePersonReadGrant(t *testing.T) {
	e := setupPromoteConsent(t)
	const shared = "known@example.test"
	if _, err := e.store.CreatePerson(e.ctx, CreatePersonInput{
		FullName: "Known Contact",
		Emails:   []PersonEmailInput{{Email: shared, EmailType: "work", IsPrimary: true}},
		Source:   "manual",
	}); err != nil {
		t.Fatalf("seed the incumbent person: %v", err)
	}
	lead := e.seedLead(t, shared)
	leadsOnly := e.withGrants(map[string]principal.ObjectGrant{
		"lead": {Create: true, Read: true, Update: true, Delete: true},
	})
	preview, err := e.store.PreviewLeadPromotion(leadsOnly, lead)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Outcome != crmcontracts.PromoteLeadPreviewOutcomeMerge {
		t.Errorf("outcome = %q, want merge — the outcome is a fact about the lead", preview.Outcome)
	}
	if preview.Person != nil || preview.PersonWithheld == nil || !*preview.PersonWithheld {
		t.Errorf("person=%v withheld=%v; a caller without person.read must get no person and be told so",
			preview.Person, preview.PersonWithheld)
	}
}
