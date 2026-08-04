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
