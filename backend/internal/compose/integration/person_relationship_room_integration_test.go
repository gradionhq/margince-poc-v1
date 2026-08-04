// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The person Relationship Room against a real database: the composite read's
// per-section refusals, the correction ledger's promise that a human's answer
// survives re-derivation, and the local graph's per-arm row scope.
//
// These are the claims that cannot be proven without Postgres. Every one of
// them is about what a caller is REFUSED, and a unit test with a fake store
// would prove only that the fake refuses.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/person360"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// roomFixedNow pins the clock so a decayed strength score cannot flake between
// seeding and reading.
var roomFixedNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

// roomPerms is a bounded rep holding every grant the person page asks for. The
// scope must be team-level: an unbounded admin short-circuits the row-scope
// clauses these tests exist to prove.
var roomPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"person":       {Create: true, Read: true, Update: true},
		"organization": {Read: true},
		"relationship": {Read: true},
		"activity":     {Create: true, Read: true, Update: true},
		"deal":         {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

func personRoomService(e *Env) *person360.Service {
	return person360.NewService(e.Pool, e.People, consent.NewStore(e.Pool),
		ai.NewFeedbackStore(e.Pool), func() time.Time { return roomFixedNow })
}

// A contact outside the caller's row scope must be a NOT FOUND, never an empty
// page. An empty page confirms the record exists and only its contents are
// withheld, which is the disclosure existence-hiding is for.
func TestPerson360RefusesAContactOutsideTheCallersRowScope(t *testing.T) {
	e := Setup(t)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)

	_, err := personRoomService(e).Assemble(rep, ids.From[ids.PersonKind](theirs))
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("Assemble on another team's contact → %v, want ErrNotFound", err)
	}
}

// A section the caller may not read is NAMED, not returned empty. Empty and
// forbidden are different facts, and a page that renders them the same way
// tells the reader a relationship is cold when it is only invisible.
func TestPerson360NamesTheSectionsACallerMayNotRead(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "My Contact", &e.Rep1)

	// Every grant except activity: the timeline, next steps, last touch and
	// since-last-visit all hang off it.
	perms := roomPerms
	perms.Objects = map[string]principal.ObjectGrant{
		"person":       {Read: true},
		"organization": {Read: true},
		"relationship": {Read: true},
	}
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, perms)

	page, err := personRoomService(e).Assemble(rep, ids.From[ids.PersonKind](mine))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	omitted := map[string]bool{}
	for _, s := range page.SectionsOmitted {
		omitted[string(s)] = true
	}
	for _, want := range []string{"activities", "next_steps", "last_touch", "since_last_visit"} {
		if !omitted[want] {
			t.Errorf("section %q was not named as omitted; the caller has no activity grant", want)
		}
	}
	if page.Activities != nil {
		t.Error("a withheld section was also returned as data")
	}
	// The root read still succeeded, so the page is served rather than refused.
	if page.Person.Id.String() == "" {
		t.Error("the page lost its root record along with the withheld sections")
	}
}

// AIRT-AC-9, end to end: a suppressed claim is not surfaced again, and a
// corrected one shows the human's value. The claim key is the claim's PATH, so
// this has to survive the row being re-derived rather than re-read.
func TestCorrectionLedgerSurvivesRederivation(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	SeedRow(t, owner, `INSERT INTO person_profile_field
		(id, workspace_id, person_id, field, value, evidence_snippet, source_ref, source, captured_by)
		VALUES ($1, $2, '`+mine.String()+`', 'title', 'Business Development Manager',
		        'Anna Weber, Business Development Manager', 'site_read:https://example.test/team', 'site_read', 'agent:enrich')`, e.WS)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	svc := personRoomService(e)
	personID := ids.From[ids.PersonKind](mine)

	before, err := svc.ProfileFields(rep, personID)
	if err != nil {
		t.Fatalf("ProfileFields: %v", err)
	}
	if len(before) != 1 || before[0].Value != "Business Development Manager" {
		t.Fatalf("seeded field did not read back: %+v", before)
	}
	if before[0].ClaimKey == nil || *before[0].ClaimKey == "" {
		t.Fatal("the field carries no claim key, so nothing could ever correct it")
	}

	corrected := "Head of Business Development"
	if err := ai.NewFeedbackStore(e.Pool).Record(rep, ai.RecordInput{
		SubjectType: "person", SubjectID: mine, ClaimKind: ai.ClaimProfileField,
		ClaimPath: *before[0].ClaimKey, Verdict: ai.VerdictCorrected, CorrectedValue: &corrected,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// The standalone sidecar AND the composite read are two paths to the same
	// rows. A verdict honoured on one and not the other would leave the
	// machine's rejected value on a surface nobody thought to check.
	after, err := svc.ProfileFields(rep, personID)
	if err != nil {
		t.Fatalf("ProfileFields after the correction: %v", err)
	}
	if after[0].Value != corrected {
		t.Errorf("sidecar value = %q, want the human's %q", after[0].Value, corrected)
	}
	if after[0].Verdict == nil || string(*after[0].Verdict) != ai.VerdictCorrected {
		t.Error("the sidecar rendered the human's value with no marker saying it was corrected")
	}
	page, err := svc.Assemble(rep, personID)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if page.ProfileFields == nil || (*page.ProfileFields)[0].Value != corrected {
		t.Error("the composite read did not honour the correction the sidecar did")
	}
}

// The write is gated on the SUBJECT's own grant. A caller who may read a
// contact but not edit them cannot overrule what the system says about them.
func TestCorrectionLedgerRefusesAWriteWithoutTheSubjectsUpdateGrant(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)

	readOnly := roomPerms
	readOnly.Objects = map[string]principal.ObjectGrant{"person": {Read: true}}
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, readOnly)

	err := ai.NewFeedbackStore(e.Pool).Record(rep, ai.RecordInput{
		SubjectType: "person", SubjectID: mine, ClaimKind: ai.ClaimProfileField,
		ClaimPath: "profile_field:title", Verdict: ai.VerdictSuppressed,
	})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Record without person:update → %v, want ErrPermissionDenied", err)
	}
}

// A verdict about another team's contact is a not-found, so the endpoint
// cannot be used to probe which record ids exist.
func TestCorrectionLedgerRefusesASubjectOutsideRowScope(t *testing.T) {
	e := Setup(t)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)

	err := ai.NewFeedbackStore(e.Pool).Record(rep, ai.RecordInput{
		SubjectType: "person", SubjectID: theirs, ClaimKind: ai.ClaimProfileField,
		ClaimPath: "profile_field:title", Verdict: ai.VerdictSuppressed,
	})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("Record on another team's contact → %v, want ErrNotFound", err)
	}
}

// Re-deciding replaces rather than appends: a verdict is the current answer to
// "has a human decided this", and two answers is no answer.
func TestCorrectionLedgerKeepsOneVerdictPerClaim(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	store := ai.NewFeedbackStore(e.Pool)

	first := "Head of Sales"
	if err := store.Record(rep, ai.RecordInput{
		SubjectType: "person", SubjectID: mine, ClaimKind: ai.ClaimProfileField,
		ClaimPath: "profile_field:title", Verdict: ai.VerdictCorrected, CorrectedValue: &first,
	}); err != nil {
		t.Fatalf("first Record: %v", err)
	}
	if err := store.Record(rep, ai.RecordInput{
		SubjectType: "person", SubjectID: mine, ClaimKind: ai.ClaimProfileField,
		ClaimPath: "profile_field:title", Verdict: ai.VerdictSuppressed,
	}); err != nil {
		t.Fatalf("second Record: %v", err)
	}

	var rows int
	var verdict string
	if err := database.WithWorkspaceTx(rep, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT count(*), max(verdict) FROM ai_feedback
			 WHERE subject_type = 'person' AND subject_id = $1`, mine).Scan(&rows, &verdict)
	}); err != nil {
		t.Fatalf("reading the ledger: %v", err)
	}
	if rows != 1 {
		t.Errorf("two decisions left %d rows, want 1 — a verdict is the current answer, not a log", rows)
	}
	if verdict != ai.VerdictSuppressed {
		t.Errorf("verdict = %q, want the later decision %q", verdict, ai.VerdictSuppressed)
	}
	// The superseded correction's value must not survive: the ledger stores
	// the human's CURRENT answer, and a suppressed claim carries none.
	var value *string
	if err := database.WithWorkspaceTx(rep, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT corrected_value FROM ai_feedback WHERE subject_id = $1`, mine).Scan(&value)
	}); err != nil {
		t.Fatalf("reading the superseded value: %v", err)
	}
	if value != nil {
		t.Errorf("the superseded correction's value survived as %q", *value)
	}
}

// Art. 17 has to reach the enrichment sidecar. Anonymize-in-place leaves the
// person row standing, so nothing cascades: without the explicit statement the
// subject's title, employer and the verbatim sentence naming them survive an
// erasure the controller certified complete.
func TestErasureReachesTheEnrichmentSidecarAndTheLedger(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	SeedRow(t, owner, `INSERT INTO person_profile_field
		(id, workspace_id, person_id, field, value, evidence_snippet, source_ref, source, captured_by)
		VALUES ($1, $2, '`+mine.String()+`', 'title', 'Head of Procurement',
		        'Anna Weber — Head of Procurement at ScaleCommerce', 'site_read:https://example.test/team', 'site_read', 'agent:enrich')`, e.WS)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	if err := ai.NewFeedbackStore(e.Pool).Record(rep, ai.RecordInput{
		SubjectType: "person", SubjectID: mine, ClaimKind: ai.ClaimProfileField,
		ClaimPath: "profile_field:title", Verdict: ai.VerdictSuppressed,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), mine, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	for _, tc := range []struct{ table, where string }{
		{"person_profile_field", "person_id = $1"},
		{"ai_feedback", "subject_type = 'person' AND subject_id = $1"},
	} {
		var left int
		if err := owner.QueryRow(context.Background(),
			`SELECT count(*) FROM `+tc.table+` WHERE `+tc.where, mine).Scan(&left); err != nil {
			t.Fatalf("counting %s: %v", tc.table, err)
		}
		if left != 0 {
			t.Errorf("%s kept %d row(s) about an erased subject", tc.table, left)
		}
	}
}

// The whole page, populated. The refusal tests above prove what a caller does
// not get; this proves the sections actually assemble from real rows — a page
// that refuses correctly and renders nothing is not a working page.
func TestPerson360AssemblesEverySectionFromRealRows(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	org := e.SeedOrg(t, "ScaleCommerce", &e.Rep1)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)

	SeedRow(t, owner, `INSERT INTO relationship
		(id, workspace_id, kind, person_id, organization_id, role, is_current_primary, source, captured_by)
		VALUES ($1, $2, 'employment', '`+mine.String()+`', '`+org.String()+`',
		        'Head of Procurement', true, 'manual', 'human:x')`, e.WS)

	// One inbound message and one open task: the timeline, the last-touch
	// pair and the next-steps section each read a different slice of these.
	inbound := SeedRow(t, owner, `INSERT INTO activity
		(id, workspace_id, kind, subject, body, occurred_at, direction, source, captured_by)
		VALUES ($1, $2, 'email', 'Re: pricing', 'body', '2026-08-01T09:00:00Z',
		        'inbound', 'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, inbound, "person", mine)
	task := SeedRow(t, owner, `INSERT INTO activity
		(id, workspace_id, kind, subject, occurred_at, due_at, is_done, source, captured_by)
		VALUES ($1, $2, 'task', 'Send the quote', '2026-07-28T09:00:00Z', '2026-07-30T09:00:00Z',
		        false, 'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, task, "person", mine)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	page, err := personRoomService(e).Assemble(rep, ids.From[ids.PersonKind](mine))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(page.SectionsOmitted) != 0 {
		t.Errorf("a fully-granted caller lost sections: %v", page.SectionsOmitted)
	}
	if page.Employments == nil || len(page.Employments.Data) != 1 {
		t.Error("the employment edge did not reach the page")
	} else if page.Employments.Data[0].Role == nil || *page.Employments.Data[0].Role != "Head of Procurement" {
		t.Error("the employment edge lost the role it records")
	}
	if page.Activities == nil || len(page.Activities.Data) == 0 {
		t.Error("the timeline is empty on a contact with a captured message")
	}
	if page.NextSteps == nil || len(page.NextSteps.Data) != 1 {
		t.Error("the open task did not reach next steps")
	}
	// The two directions are read separately and never folded: an account we
	// mailed a fortnight ago with no reply and one that wrote this morning
	// have the same last-touch date and opposite meanings.
	if page.LastInboundAt == nil {
		t.Error("last_inbound_at is absent on a contact who wrote to us")
	}
	if page.LastOutboundAt != nil {
		t.Error("last_outbound_at is set though nothing outbound was captured")
	}
	if page.Strength == nil {
		t.Error("the relationship score did not assemble")
	}
	if page.RelationshipChanges == nil {
		t.Error("the derived changes section is absent entirely, which is different from empty")
	}
	if page.Moments == nil {
		t.Fatal("the moments section is absent entirely")
	}
	// An unanswered inbound and an overdue task are both true here, and the
	// order is the editorial judgment: owing a reply outranks late work.
	kinds := make([]string, 0, len(*page.Moments))
	for _, m := range *page.Moments {
		kinds = append(kinds, string(m.Kind))
	}
	if len(kinds) < 2 || kinds[0] != "unanswered_inbound" || kinds[1] != "task_overdue" {
		t.Errorf("moments = %v, want unanswered_inbound before task_overdue", kinds)
	}
	if page.SinceLastVisit == nil {
		t.Error("since-last-visit is absent for a caller who has never visited")
	}
}

// A dismissed moment stays dismissed across a re-derivation. The verdict is
// keyed on the moment's PATH, so this has to survive the evidence changing —
// a key derived from the evidence would resurface it on the next mail.
func TestDismissedMomentDoesNotComeBack(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	inbound := SeedRow(t, owner, `INSERT INTO activity
		(id, workspace_id, kind, subject, body, occurred_at, direction, source, captured_by)
		VALUES ($1, $2, 'email', 'Re: pricing', 'body', '2026-07-30T09:00:00Z',
		        'inbound', 'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, inbound, "person", mine)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	svc := personRoomService(e)
	personID := ids.From[ids.PersonKind](mine)

	page, err := svc.Assemble(rep, personID)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if page.Moments == nil || len(*page.Moments) == 0 {
		t.Fatal("an unanswered inbound message produced no moment")
	}
	claim := (*page.Moments)[0].ClaimKey

	if err := ai.NewFeedbackStore(e.Pool).Record(rep, ai.RecordInput{
		SubjectType: "person", SubjectID: mine, ClaimKind: ai.ClaimSignal,
		ClaimPath: claim, Verdict: ai.VerdictSuppressed,
	}); err != nil {
		t.Fatalf("dismissing: %v", err)
	}

	after, err := svc.Assemble(rep, personID)
	if err != nil {
		t.Fatalf("Assemble after the dismissal: %v", err)
	}
	for _, m := range *after.Moments {
		if m.ClaimKey == claim {
			t.Fatalf("the dismissed moment %q came back", claim)
		}
	}
}

// personChanges runs the Tx-scoped derivation in a transaction of its own.
// There is no pool-level variant, and adding one for a test would be an
// entry point with no production caller.
func personChanges(t *testing.T, e *Env, ctx context.Context, personID ids.PersonID) ([]relstrength.Change, error) {
	t.Helper()
	var out []relstrength.Change
	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		var err error
		out, err = e.People.PersonRelationshipChangesTx(ctx, tx, personID, roomFixedNow)
		return err
	})
	return out, err
}

// The derivation folds the same §4 curve over a window ending in the past, so
// what it reports comes from the timeline rather than from a stored number —
// which is the whole reason there is no table.
func TestRelationshipChangesAreDerivedFromTheTimeline(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)

	// A long silence, then their reply. roomFixedNow is 2026-08-04, so the
	// silence the reply broke is 48 days and the reply itself is 3 days old.
	for _, at := range []string{"2026-06-14T09:00:00Z", "2026-08-01T09:00:00Z"} {
		id := SeedRow(t, owner, `INSERT INTO activity
			(id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
			VALUES ($1, $2, 'email', 'thread', '`+at+`', 'inbound', 'manual', 'human:x')`, e.WS)
		LinkActivity(t, owner, e.WS, id, "person", mine)
	}

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	changes, err := personChanges(t, e, rep, ids.From[ids.PersonKind](mine))
	if err != nil {
		t.Fatalf("PersonRelationshipChangesTx: %v", err)
	}
	var replied bool
	for _, c := range changes {
		if c.Kind == relstrength.ChangeRepliedAfterGap {
			replied = true
			if c.Days != 48 {
				t.Errorf("gap = %d days, want 48 — measured to the interaction the reply broke", c.Days)
			}
		}
	}
	if !replied {
		t.Errorf("a reply after a seven-week silence was not derived; got %+v", changes)
	}
}

// A contact nobody has ever spoken to has not gone quiet — they were never
// loud. Saying otherwise turns every dormant record into an alert.
func TestRelationshipChangesSayNothingAboutAContactWithNoHistory(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)

	changes, err := personChanges(t, e, rep, ids.From[ids.PersonKind](mine))
	if err != nil {
		t.Fatalf("PersonRelationshipChangesTx: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("a contact with no interactions produced %d change(s): %+v", len(changes), changes)
	}
}

// The changes explain a score, and both are reads of the same record — so a
// contact outside the caller's row scope is a not-found here too.
func TestRelationshipChangesRefuseAContactOutsideRowScope(t *testing.T) {
	e := Setup(t)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)

	if _, err := personChanges(t, e, rep, ids.From[ids.PersonKind](theirs)); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("changes for another team's contact → %v, want ErrNotFound", err)
	}
}
