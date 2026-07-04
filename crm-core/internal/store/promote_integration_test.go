//go:build integration

package store

// The features/01 §6.4 acceptance criteria for lead→person promotion:
// non-lossy graduation carrying provenance, merge-not-duplicate via the
// §1.3 email path, the one-transaction audit+event shape, and the scope
// rules a merge inherits from being a read.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/fable-poc/crmctx"
	"github.com/gradionhq/fable-poc/kernel/errs"
	"github.com/gradionhq/fable-poc/kernel/ids"
)

func seedLead(t *testing.T, e *authzEnv, name, email string, owner *ids.UUID) ids.UUID {
	t.Helper()
	in := CreateLeadInput{Source: "import", OwnerID: owner}
	if name != "" {
		in.FullName = &name
	}
	if email != "" {
		in.Email = &email
	}
	l, _, err := e.store.CreateLead(e.admin(), in)
	if err != nil {
		t.Fatalf("seeding lead %s: %v", name, err)
	}
	return ids.UUID(l.Id)
}

func TestPromoteCreatesAPersonCarryingProvenance(t *testing.T) {
	e := setupAuthz(t)
	leadID := seedLead(t, e, "Ada Prospect", "ada@prospect.test", &e.rep1)
	admin := e.admin()

	person, merged, err := e.store.PromoteLead(admin, leadID, PromoteLeadInput{
		Trigger: "inbound_reply", EvidenceNote: strPtr("replied to outreach"),
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if merged {
		t.Error("fresh email should create, not merge")
	}
	if person.ConvertedFromLeadId == nil || ids.UUID(*person.ConvertedFromLeadId) != leadID {
		t.Error("person lost the converted_from_lead_id origin pointer")
	}
	if person.OwnerId == nil || ids.UUID(*person.OwnerId) != e.rep1 {
		t.Error("promotion dropped the lead's owner")
	}
	if person.Source != "import" {
		t.Errorf("promotion rewrote provenance source to %q; the capture channel must survive", person.Source)
	}
	if person.Emails == nil || len(*person.Emails) != 1 || string((*person.Emails)[0].Email) != "ada@prospect.test" {
		t.Error("promotion lost the lead's email")
	}

	// The lead is graduated: promoted, stamped with the outcome, archived
	// off the lead list — but still resolvable by id for the audit trail.
	lead, err := e.store.GetLead(admin, leadID, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(lead.Status) != "promoted" || lead.PromotedPersonId == nil || lead.ArchivedAt == nil {
		t.Errorf("lead after promote: status=%s promoted_person_id=%v archived_at=%v", lead.Status, lead.PromotedPersonId, lead.ArchivedAt)
	}

	// Exactly one lead.promoted with the §5.5 payload, plus the caused
	// person.created — same correlation, same audit row.
	owner, err := pgx.Connect(context.Background(), os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(context.Background()) }()
	var payload json.RawMessage
	var promotedAudit, personAudit string
	if err := owner.QueryRow(context.Background(),
		`SELECT envelope->'payload', envelope->'trace'->>'audit_log_id' FROM event_outbox
		 WHERE envelope->>'type' = 'lead.promoted'`).Scan(&payload, &promotedAudit); err != nil {
		t.Fatalf("lead.promoted not staged: %v", err)
	}
	var p struct {
		PromotedPersonID ids.UUID `json:"promoted_person_id"`
		DedupeOutcome    string   `json:"dedupe_outcome"`
		Trigger          string   `json:"trigger"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.PromotedPersonID != ids.UUID(person.Id) || p.DedupeOutcome != "created" || p.Trigger != "inbound_reply" {
		t.Errorf("lead.promoted payload %s", payload)
	}
	if err := owner.QueryRow(context.Background(),
		`SELECT envelope->'trace'->>'audit_log_id' FROM event_outbox
		 WHERE envelope->>'type' = 'person.created' AND envelope->'entity'->>'id' = $1`,
		person.Id.String()).Scan(&personAudit); err != nil {
		t.Fatalf("person.created not staged: %v", err)
	}
	if promotedAudit != personAudit {
		t.Error("promotion split across audit rows; the spec demands one transaction, one audit entry")
	}

	// Promotion happens once: the replay answers the typed 409 with the
	// outcome pointer, never a second person.
	_, _, err = e.store.PromoteLead(admin, leadID, PromoteLeadInput{Trigger: "human_qualify"})
	var already *AlreadyPromotedError
	if !errors.As(err, &already) {
		t.Fatalf("re-promote → %v, want AlreadyPromotedError", err)
	}
	if already.PersonID != ids.UUID(person.Id) {
		t.Error("409 lost the promoted_person_id pointer")
	}
}

func TestPromoteMergesIntoAnExistingPersonNotADuplicate(t *testing.T) {
	e := setupAuthz(t)
	admin := e.admin()
	existing, err := e.store.CreatePerson(admin, CreatePersonInput{
		FullName: "Grace Known", OwnerID: &e.rep1, Source: "manual",
		Emails: []PersonEmailInput{{Email: "grace@known.test", EmailType: "work", IsPrimary: true, Position: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	leadID := seedLead(t, e, "G. Known", "grace@known.test", &e.rep2)

	person, merged, err := e.store.PromoteLead(admin, leadID, PromoteLeadInput{Trigger: "meeting_booked"})
	if err != nil {
		t.Fatalf("promote-with-match: %v", err)
	}
	if !merged || ids.UUID(person.Id) != ids.UUID(existing.Id) {
		t.Fatalf("merged=%v into %s, want merge into the one existing person %s", merged, person.Id, existing.Id)
	}
	if person.ConvertedFromLeadId == nil || ids.UUID(*person.ConvertedFromLeadId) != leadID {
		t.Error("merge did not record the lead origin")
	}
	if person.FullName != "Grace Known" {
		t.Errorf("merge overwrote the human-curated name with %q", person.FullName)
	}

	owner, err := pgx.Connect(context.Background(), os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(context.Background()) }()
	var people int
	if err := owner.QueryRow(context.Background(),
		`SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
		 WHERE pe.email = 'grace@known.test' AND p.archived_at IS NULL`).Scan(&people); err != nil {
		t.Fatal(err)
	}
	if people != 1 {
		t.Fatalf("%d live people hold the email after promotion, want exactly 1 (merge-not-duplicate)", people)
	}
}

func TestPromoteDoesNotDiscloseAnOutOfScopeMergeTarget(t *testing.T) {
	e := setupAuthz(t)
	if _, err := e.store.CreatePerson(e.admin(), CreatePersonInput{
		FullName: "Foreign Match", OwnerID: &e.rep3, Source: "manual",
		Emails: []PersonEmailInput{{Email: "match@foreign.test", EmailType: "work", IsPrimary: true, Position: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	leadID := seedLead(t, e, "Mine", "match@foreign.test", &e.rep1)

	rep := e.as(e.rep1, []ids.UUID{e.team1}, repPermsWithCapture())
	if _, _, err := e.store.PromoteLead(rep, leadID, PromoteLeadInput{Trigger: "inbound_reply"}); !errors.Is(err, errs.ErrConflict) {
		t.Errorf("promote into an out-of-scope match → %v, want bare ErrConflict (a merge is a read)", err)
	}
}

func TestPromoteRequiresBothLeadAndPersonGrants(t *testing.T) {
	e := setupAuthz(t)
	leadID := seedLead(t, e, "Gated", "gated@x.test", &e.rep1)

	// Lead grants but no person.create: leads may be worked, contacts may
	// not be minted through the promotion door.
	perms := repPermsWithCapture()
	perms.Objects["person"] = crmctx.ObjectGrant{Read: true, Update: true}
	rep := e.as(e.rep1, []ids.UUID{e.team1}, perms)
	if _, _, err := e.store.PromoteLead(rep, leadID, PromoteLeadInput{Trigger: "human_qualify"}); !errors.Is(err, errs.ErrPermissionDenied) {
		t.Errorf("promote without person.create → %v, want ErrPermissionDenied", err)
	}
}
