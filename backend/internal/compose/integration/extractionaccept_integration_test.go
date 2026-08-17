// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The extraction accept-write (RD-T10) over real Postgres: one deals
// partial update carrying every accepted field, then one audit activity
// note per field (subject "Extraction accepted: <field>", body = the
// grounding source quote, linked to the deal). Unedited fields carry the
// machine stamp (captured_by agent:attachment-extractor); an edited field
// is the human's own write (captured_by human:<uid>, provenance human).
// Every refusal — non-deal parent, ungrounded key, missing grant,
// invisible parent — is whole-request with ZERO writes.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
)

// acceptExtractionFields is the evidence set these tests accept against: four
// deal-writable grounded fields, one grounded row reference the allowlist must
// refuse, one omitted key.
func acceptExtractionFields() []extraction.ExtractedField {
	return []extraction.ExtractedField{
		{Field: "name", Value: "Acme Renewal Q3", SourceQuote: "Subject: Acme Renewal Q3", PageOrSection: "p.1", Confidence: "high"},
		{Field: "amount_minor", Value: "150000", SourceQuote: "Total: EUR 1,500.00", PageOrSection: "p.2", Confidence: "high"},
		{Field: "currency", Value: "EUR", SourceQuote: "all amounts in EUR", PageOrSection: "p.2", Confidence: "medium"},
		{Field: "expected_close_date", Value: "2030-12-31", SourceQuote: "offer valid until 2030-12-31", PageOrSection: "p.3", Confidence: "medium"},
		{Field: "owner_id", Value: "3e0f5a9c-0000-0000-0000-000000000001", SourceQuote: "account executive", PageOrSection: "p.1", Confidence: "medium"},
		{Field: "payment_terms", Omitted: true, OmittedReason: "not_stated_in_file"},
	}
}

// seedExtractionReading writes ONE finished reading through the production
// store — start, claim, finish — and answers its id.
//
// Not a hand-inserted row: the accept resolves what a reading actually
// recorded, so a test that inserted its own would prove nothing about the
// writer that fills the column in production. It is also the only way to obtain
// an id the accept will take, which is the point of the id existing.
func seedExtractionReading(
	ctx context.Context, t *testing.T, e *Env, attachmentID ids.UUID, fields []extraction.ExtractedField,
) ids.UUID {
	t.Helper()
	store := activities.NewStore(e.DB())
	read, _, err := store.StartExtractionReadQueued(ctx, attachmentID, "seed", nil)
	if err != nil {
		t.Fatalf("StartExtractionReadQueued: %v", err)
	}
	claim, err := store.BeginExtractionRead(ctx, read.ID, activities.ExtractionReadLease)
	if err != nil {
		t.Fatalf("BeginExtractionRead: %v", err)
	}
	if claim.StartedAt == nil {
		t.Fatal("a claimed reading carries no start time")
	}
	if err := store.FinishExtractionRead(ctx, read.ID, activities.ExtractionReadOutcome{
		Status: activities.ExtractionReadDone,
		Fields: fields,
		// A reading that grounded nothing owes a reason, exactly as the
		// production run supplies one — the store refuses an unexplained empty
		// result, and a seed that dodged that would be seeding a row the writer
		// cannot produce.
		Detail: seededReadingDetail(fields),
		// The claim this outcome belongs to, exactly as the worker passes it:
		// the finish is scoped to one attempt, so a seed that omitted it would
		// be writing a row through a door production does not have.
		ClaimedAt: *claim.StartedAt,
	}); err != nil {
		t.Fatalf("FinishExtractionRead: %v", err)
	}
	return read.ID
}

// seededReadingDetail mirrors the run's own rule: say why when nothing was
// grounded, say nothing when something was.
func seededReadingDetail(fields []extraction.ExtractedField) string {
	for _, f := range fields {
		if !f.Omitted {
			return ""
		}
	}
	return "this document states none of the deal fields clearly enough to offer one"
}

// acceptEnv is one deal-scoped attachment with the fixture extractor wired
// into the accept engine, ready to accept against.
type acceptEnv struct {
	*Env
	deal ids.UUID
	att  crmcontracts.Attachment
	// reading is the id of the seeded reading these tests accept against — the
	// one a human would have been shown.
	reading ids.UUID
	engine  *compose.ExtractionAccept
}

// acceptRequest names the seeded reading, which every accept must.
func (a acceptEnv) acceptRequest(keys ...string) crmcontracts.AcceptExtractionRequest {
	return crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(a.reading), FieldKeys: keys,
	}
}

// setupExtractionAccept seeds a deal-scoped attachment with one finished
// reading against it, the fixture every accept test in this file starts from.
func setupExtractionAccept(t *testing.T) acceptEnv {
	t.Helper()
	e := Setup(t)
	h := activities.NewHandlers(e.DB()).WithUploadLimit(uploadCeiling).WithBlobstore(blobstore.NewMemory())
	pipeline, open, _ := DealFixture(t, e)
	deal := e.SeedDeal(t, "Accept Target", pipeline, open, &e.Rep1)
	att := uploadDealAttachment(e.Admin(), t, h, deal, "quote.pdf", []byte("quote bytes"))
	return acceptEnv{
		Env:     e,
		deal:    deal,
		att:     att,
		reading: seedExtractionReading(e.Admin(), t, e, ids.UUID(att.Id), acceptExtractionFields()),
		engine:  compose.NewExtractionAccept(e.Pool),
	}
}

// acceptNoteCount counts this op's audit notes for one field, pinned on
// every stamp that matters: subject, body = the grounding quote, the deal
// link, and the exact captured_by.
func (a acceptEnv) acceptNoteCount(t *testing.T, field, body, capturedBy string) int {
	t.Helper()
	return a.WsCount(t, `
		SELECT count(*) FROM activity a
		JOIN activity_link al ON al.activity_id = a.id
		WHERE a.kind = 'note' AND a.source = 'attachment_extraction_accept'
		  AND a.subject = $1 AND a.body = $2 AND a.captured_by = $3
		  AND al.entity_type = 'deal' AND al.deal_id = $4`,
		"Extraction accepted: "+field, body, capturedBy, a.deal)
}

// totalAcceptNotes counts every note this op ever wrote — the zero-writes
// assertions pin on it.
func (a acceptEnv) totalAcceptNotes(t *testing.T) int {
	t.Helper()
	return a.WsCount(t, `SELECT count(*) FROM activity WHERE source = 'attachment_extraction_accept'`)
}

// requireUntouchedDeal asserts the refusal left the seeded deal exactly as
// born: no amount, no currency, no close date, the seed name.
func (a acceptEnv) requireUntouchedDeal(t *testing.T) {
	t.Helper()
	if n := a.WsCount(t, `
		SELECT count(*) FROM deal
		WHERE id = $1 AND name = 'Accept Target' AND amount_minor IS NULL
		  AND currency IS NULL AND expected_close_date IS NULL`, a.deal); n != 1 {
		t.Error("the refused request still mutated the deal — refusals must be whole-request with zero writes")
	}
	if notes := a.totalAcceptNotes(t); notes != 0 {
		t.Errorf("the refused request still wrote %d audit note(s), want 0", notes)
	}
}

func TestAcceptAttachmentExtractionPersistsFieldsAndAuditNotes(t *testing.T) {
	a := setupExtractionAccept(t)
	// A real seeded user: the machine-stamped notes carry the accepting
	// human on on_behalf_of, which is an FK into app_user.
	ctx := a.As(a.Rep1, []ids.UUID{a.Team1}, AdminPerms)

	resp, err := a.engine.Accept(ctx, ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(a.reading),
		FieldKeys:    []string{"name", "amount_minor", "currency", "expected_close_date"},
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if ids.UUID(resp.DealId) != a.deal {
		t.Errorf("resp.DealId = %s, want the attachment's deal %s", resp.DealId, a.deal)
	}
	if len(resp.Accepted) != 4 {
		t.Fatalf("accepted %d fields, want 4: %+v", len(resp.Accepted), resp.Accepted)
	}
	for _, f := range resp.Accepted {
		if f.Provenance != crmcontracts.AcceptedExtractionFieldProvenanceAiExtracted {
			t.Errorf("%s provenance = %s, want ai-extracted", f.Field, f.Provenance)
		}
	}

	// The deal row carries every accepted field, coerced to its column's
	// shape (amount_minor: the extractor's string → int64; the date: text →
	// a date column).
	if n := a.WsCount(t, `
		SELECT count(*) FROM deal
		WHERE id = $1 AND name = 'Acme Renewal Q3' AND amount_minor = 150000
		  AND currency = 'EUR' AND expected_close_date = DATE '2030-12-31'`, a.deal); n != 1 {
		t.Error("the accepted fields did not land on the deal row as their coerced column values")
	}

	// One audit note per field, body = the grounding quote, machine stamp.
	for field, quote := range map[string]string{
		"name":                "Subject: Acme Renewal Q3",
		"amount_minor":        "Total: EUR 1,500.00",
		"currency":            "all amounts in EUR",
		"expected_close_date": "offer valid until 2030-12-31",
	} {
		if n := a.acceptNoteCount(t, field, quote, "agent:attachment-extractor"); n != 1 {
			t.Errorf("audit notes for %s = %d, want exactly 1 (subject/body/captured_by/deal link all pinned)", field, n)
		}
	}
	if total := a.totalAcceptNotes(t); total != 4 {
		t.Errorf("total accept notes = %d, want 4", total)
	}
}

func TestAcceptAttachmentExtractionEditFlipsProvenanceAndCapturedBy(t *testing.T) {
	a := setupExtractionAccept(t)
	ctx := a.As(a.Rep1, []ids.UUID{a.Team1}, AdminPerms)

	edits := map[string]interface{}{"amount_minor": "200000"}
	resp, err := a.engine.Accept(ctx, ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(a.reading),
		FieldKeys:    []string{"amount_minor", "currency"},
		Edits:        &edits,
	})
	if err != nil {
		t.Fatalf("edited accept: %v", err)
	}
	if len(resp.Accepted) != 2 {
		t.Fatalf("accepted = %+v, want 2 fields", resp.Accepted)
	}
	if resp.Accepted[0].Provenance != crmcontracts.AcceptedExtractionFieldProvenanceHuman || resp.Accepted[0].Value != "200000" {
		t.Errorf("edited field = %+v, want value 200000 with provenance human", resp.Accepted[0])
	}
	if resp.Accepted[1].Provenance != crmcontracts.AcceptedExtractionFieldProvenanceAiExtracted {
		t.Errorf("unedited field = %+v, want provenance ai-extracted", resp.Accepted[1])
	}

	// The edited value (not the extracted one) landed on the deal.
	if n := a.WsCount(t, `SELECT count(*) FROM deal WHERE id = $1 AND amount_minor = 200000 AND currency = 'EUR'`, a.deal); n != 1 {
		t.Error("the deal row does not carry the edited amount + the extracted currency")
	}

	// The edited field's note is the human's own write; the unedited one
	// keeps the machine stamp. Both bodies stay the grounding quote — the
	// evidence is what was accepted against, whoever typed the final value.
	if n := a.acceptNoteCount(t, "amount_minor", "Total: EUR 1,500.00", "human:"+a.Rep1.String()); n != 1 {
		t.Errorf("edited-field notes with the human stamp = %d, want 1", n)
	}
	if n := a.acceptNoteCount(t, "currency", "all amounts in EUR", "agent:attachment-extractor"); n != 1 {
		t.Errorf("unedited-field notes with the machine stamp = %d, want 1", n)
	}
}

func TestAcceptAttachmentExtractionRefusesNonDealAttachment(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.DB()).WithUploadLimit(uploadCeiling).WithBlobstore(blobstore.NewMemory())
	org := e.SeedOrg(t, "Non-Deal Accept Parent", &e.Rep1)
	att := uploadTestAttachmentForOrg(e.Admin(), t, h, org, "org-notes.pdf", []byte("org bytes"))
	reading := seedExtractionReading(e.Admin(), t, e, ids.UUID(att.Id), acceptExtractionFields())
	engine := compose.NewExtractionAccept(e.Pool)

	_, err := engine.Accept(e.Admin(), ids.UUID(att.Id), crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(reading), FieldKeys: []string{"amount_minor"},
	})
	var unsupported *compose.UnsupportedEntityTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want UnsupportedEntityTypeError (only a deal-scoped attachment has a deal to write)", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM activity WHERE source = 'attachment_extraction_accept'`); n != 0 {
		t.Errorf("the refused non-deal accept still wrote %d note(s), want 0", n)
	}
}

func TestAcceptAttachmentExtractionRefusesUngroundedKeyWholeRequest(t *testing.T) {
	a := setupExtractionAccept(t)

	// amount_minor IS grounded and valid — but it must not land, because
	// the second key refuses the whole request.
	_, err := a.engine.Accept(a.Admin(), ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(a.reading),
		FieldKeys:    []string{"amount_minor", "payment_terms"},
	})
	var refused *compose.ExtractionAcceptError
	if !errors.As(err, &refused) || refused.Code != "not_grounded" {
		t.Fatalf("err = %v, want an ExtractionAcceptError with code not_grounded", err)
	}
	a.requireUntouchedDeal(t)
}

func TestAcceptAttachmentExtractionRefusesFieldOutsideAllowlist(t *testing.T) {
	a := setupExtractionAccept(t)

	_, err := a.engine.Accept(a.Admin(), ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(a.reading),
		FieldKeys:    []string{"owner_id"},
	})
	var refused *compose.ExtractionAcceptError
	if !errors.As(err, &refused) || refused.Code != "not_deal_writable" {
		t.Fatalf("err = %v, want an ExtractionAcceptError with code not_deal_writable", err)
	}
	a.requireUntouchedDeal(t)
}

func TestAcceptAttachmentExtractionRequiresFieldKeys(t *testing.T) {
	a := setupExtractionAccept(t)

	_, err := a.engine.Accept(a.Admin(), ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{ExtractionId: openapi_types.UUID(a.reading), FieldKeys: []string{}})
	var refused *compose.ExtractionAcceptError
	if !errors.As(err, &refused) || refused.Field != "field_keys" || refused.Code != "required" {
		t.Fatalf("err = %v, want field_keys/required", err)
	}
	a.requireUntouchedDeal(t)
}

func TestAcceptAttachmentExtractionHidesAnInvisibleParent(t *testing.T) {
	a := setupExtractionAccept(t)

	// Rep3 (Team2, team row scope) cannot see Rep1's deal: the attachment
	// answers the same existence-hiding 404 as every other attachment op.
	ctx := a.As(a.Rep3, []ids.UUID{a.Team2}, RepPerms)
	_, err := a.engine.Accept(ctx, ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(a.reading),
		FieldKeys:    []string{"amount_minor"},
	})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (existence-hiding)", err)
	}
	a.requireUntouchedDeal(t)
}

func TestAcceptAttachmentExtractionRequiresDealUpdateGrant(t *testing.T) {
	a := setupExtractionAccept(t)

	// Read-only sees the deal (row scope all) but holds no deal update.
	ctx := a.As(a.Rep2, nil, ReadOnlyPerms)
	_, err := a.engine.Accept(ctx, ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(a.reading),
		FieldKeys:    []string{"amount_minor"},
	})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	a.requireUntouchedDeal(t)
}

func TestAcceptAttachmentExtractionEditedAcceptRequiresActivityGrant(t *testing.T) {
	a := setupExtractionAccept(t)

	// Rep1 may update the deal but holds no activity grant: an edited
	// field's note is the human's own activity write, so the gate refuses
	// BEFORE the deal write — never after it committed.
	ctx := a.As(a.Rep1, []ids.UUID{a.Team1}, RepPerms)
	edits := map[string]interface{}{"amount_minor": "200000"}
	_, err := a.engine.Accept(ctx, ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(a.reading),
		FieldKeys:    []string{"amount_minor"},
		Edits:        &edits,
	})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	a.requireUntouchedDeal(t)

	// The same caller CAN accept unedited: those notes are the machine's
	// audit trail, not a human-authored activity. Amount and currency come
	// together — the deal was born amountless, so the resulting row needs
	// the pair.
	if _, err := a.engine.Accept(ctx, ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(a.reading),
		FieldKeys:    []string{"amount_minor", "currency"},
	}); err != nil {
		t.Fatalf("unedited accept under the same rep grant: %v", err)
	}
	if n := a.acceptNoteCount(t, "amount_minor", "Total: EUR 1,500.00", "agent:attachment-extractor"); n != 1 {
		t.Errorf("machine-stamped notes after the unedited accept = %d, want 1", n)
	}
}

// TestAcceptAttachmentExtractionRefusesAReadingOfAnotherDocument pins the
// pairing the accept's id argument exists for: a reading is a reading OF one
// document, and naming one that belongs to another attachment resolves to
// nothing rather than to its fields.
//
// Without the pairing the id would be a decoration — a caller could hold any
// reading id and spend it against any document they can see, which is a way to
// write one document's values onto another document's deal.
func TestAcceptAttachmentExtractionRefusesAReadingOfAnotherDocument(t *testing.T) {
	a := setupExtractionAccept(t)
	h := activities.NewHandlers(a.DB()).WithBlobstore(blobstore.NewMemory())
	other := uploadDealAttachment(a.Admin(), t, h, a.deal, "other.pdf", []byte("other bytes"))
	elsewhere := seedExtractionReading(a.Admin(), t, a.Env, ids.UUID(other.Id), acceptExtractionFields())

	_, err := a.engine.Accept(a.Admin(), ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(elsewhere), FieldKeys: []string{"amount_minor"},
	})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (that reading belongs to another document)", err)
	}
	a.requireUntouchedDeal(t)
}

// TestAcceptAttachmentExtractionRefusesWhenNothingHasReadTheDocument pins what
// an accept finds when no reading exists: nothing to accept, and no write.
func TestAcceptAttachmentExtractionRefusesWhenNothingHasReadTheDocument(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.DB()).WithUploadLimit(uploadCeiling).WithBlobstore(blobstore.NewMemory())
	pipeline, open, _ := DealFixture(t, e)
	deal := e.SeedDeal(t, "Accept Target", pipeline, open, &e.Rep1)
	att := uploadDealAttachment(e.Admin(), t, h, deal, "quote.pdf", []byte("quote bytes"))

	_, err := compose.NewExtractionAccept(e.Pool).Accept(e.Admin(), ids.UUID(att.Id),
		crmcontracts.AcceptExtractionRequest{
			ExtractionId: openapi_types.UUID(ids.NewV7()), FieldKeys: []string{"amount_minor"},
		})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (nothing has read this document)", err)
	}
	acceptEnv{Env: e, deal: deal}.requireUntouchedDeal(t)
}

// TestExtractionAcceptDealUpdateAndNotesShareOneTransaction pins the
// accept-write's atomicity (the non-atomic write was the second finding
// this suite closes): UpdateDealTx and LogActivityTx must run INSIDE the
// caller's own transaction, not open (and durably commit) transactions of
// their own. It drives the exact write phase Accept() runs — a deal
// update followed by one note — through one database.WithWorkspaceTx,
// then forces that shared transaction to roll back. If either Tx-suffixed
// method still opened its own transaction under the hood (the pre-fix
// shape, where the deal update committed before the notes ran), the
// forced rollback below would arrive too late — both rows would already
// be durable. Neither is: the rollback discards both.
func TestExtractionAcceptDealUpdateAndNotesShareOneTransaction(t *testing.T) {
	a := setupExtractionAccept(t)
	ctx := a.As(a.Rep1, []ids.UUID{a.Team1}, AdminPerms)

	forced := errors.New("forced rollback to prove the shared transaction")
	// The catalog read belongs above the transaction, as it does in Accept().
	active, err := a.Deals.ActiveDealColumns(ctx)
	if err != nil {
		t.Fatalf("reading the deal's active custom columns: %v", err)
	}
	err = database.WithWorkspaceTx(ctx, a.Pool, func(tx pgx.Tx) error {
		if _, err := a.Deals.UpdateDealTx(ctx, tx, ids.From[ids.DealKind](a.deal), deals.UpdateDealInput{
			Name: strPtr("Rolled Back Name"),
		}, active); err != nil {
			return err
		}
		if _, _, err := a.Activities.LogActivityTx(ctx, tx, activities.LogActivityInput{
			Kind:   string(crmcontracts.ActivityKindNote),
			Body:   strPtr("should never persist past the rollback"),
			Links:  []activities.ActivityLinkInput{{EntityType: acceptDealEntityForTest, EntityID: a.deal}},
			Source: "atomic_tx_probe",
		}); err != nil {
			return err
		}
		return forced
	})
	if !errors.Is(err, forced) {
		t.Fatalf("err = %v, want the forced rollback error", err)
	}

	if n := a.WsCount(t, `SELECT count(*) FROM deal WHERE id = $1 AND name = 'Rolled Back Name'`, a.deal); n != 0 {
		t.Error("the deal update persisted despite the shared transaction rolling back — UpdateDealTx is not honoring the caller's transaction")
	}
	if n := a.WsCount(t, `SELECT count(*) FROM activity WHERE source = 'atomic_tx_probe'`); n != 0 {
		t.Error("the note persisted despite the shared transaction rolling back — LogActivityTx is not honoring the caller's transaction")
	}
}

// acceptDealEntityForTest mirrors compose's unexported acceptDealEntity
// ("deal") — this white-box probe lives in the integration package, not
// compose itself, so it cannot reach that constant.
const acceptDealEntityForTest = "deal"

// TestAcceptAttachmentExtractionWritesTheReadingItWasShownNotTheNewest is the
// gate on RD-AC-N-5, and it is the reason the reading is stored at all.
//
// The shape it refuses: a human is shown a reading, a SECOND reading of the
// same document lands before they click accept, and the accept resolves the
// second one. Everything about that is quiet — both readings are real, both
// grounded a value with a genuine quote, and neither looks wrong — but the
// number written to the deal is not the number anyone agreed to, and the audit
// note quotes evidence the human never saw.
//
// This is why the accept takes an extraction_id rather than resolving the
// latest. Under the shape that re-ran the extractor per accept, and under one
// that resolved the newest stored reading, this test fails.
func TestAcceptAttachmentExtractionWritesTheReadingItWasShownNotTheNewest(t *testing.T) {
	a := setupExtractionAccept(t)
	shown := a.reading
	// A REAL seeded user: the accept's audit note carries on_behalf_of, which is
	// a foreign key, so a synthetic principal cannot reach the write phase this
	// test is about.
	ctx := a.As(a.Rep1, []ids.UUID{a.Team1}, AdminPerms)

	// A second reading of the same document, grounding a different amount off a
	// different quote — the revised page, a re-read after an upload, a colleague
	// pressing the button. Nothing here is an error.
	// It grounds the SAME fields, so the accept below would succeed against
	// either reading. That is deliberate: a second reading missing a field would
	// make this test pass for the wrong reason — refused as ungrounded rather
	// than resolved from the right reading — and would go on passing after the
	// pairing was removed.
	later := seedExtractionReading(ctx, t, a.Env, ids.UUID(a.att.Id), []extraction.ExtractedField{
		{Field: "amount_minor", Value: "999900", SourceQuote: "Revised total: EUR 9,999.00", PageOrSection: "p.2", Confidence: "high"},
		{Field: "currency", Value: "EUR", SourceQuote: "Revised total: EUR 9,999.00", PageOrSection: "p.2", Confidence: "high"},
	})
	if later == shown {
		t.Fatal("precondition: the second reading must be a different reading")
	}

	// Both money fields, because a deal holds an amount and its currency
	// together or not at all — the divergence being proven is about WHICH
	// reading supplies them, not about accepting a lone number.
	if _, err := a.engine.Accept(ctx, ids.UUID(a.att.Id),
		a.acceptRequest("amount_minor", "currency")); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	var amount *int64
	if err := a.Pool.QueryRow(ctx, `SELECT amount_minor FROM deal WHERE id = $1`, a.deal).Scan(&amount); err != nil {
		t.Fatalf("read back deal: %v", err)
	}
	if amount == nil {
		t.Fatal("deal amount_minor is unset — the accept wrote nothing")
	}
	if *amount != 150000 {
		t.Fatalf("deal amount_minor = %d, want 150000 — the accept wrote a reading the human was never shown", *amount)
	}
	// The evidence has to match the value: a note quoting the newer reading
	// beside the older reading's number is the same defect, half-landed.
	if n := a.acceptNoteCount(t, "amount_minor", "Total: EUR 1,500.00", "agent:attachment-extractor"); n != 1 {
		t.Errorf("audit notes quoting the SHOWN reading's evidence = %d, want 1", n)
	}
}
