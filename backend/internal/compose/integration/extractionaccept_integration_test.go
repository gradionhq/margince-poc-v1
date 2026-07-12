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
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

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

// acceptExtractionFixture seeds the extractor with the same evidence set
// the unit suite validates against: four deal-writable grounded fields,
// one grounded row reference the allowlist must refuse, one omitted key.
func acceptExtractionFixture(attachmentID string) extraction.FixtureExtractor {
	return extraction.FixtureExtractor{Fields: map[string][]extraction.ExtractedField{
		attachmentID: {
			{Field: "name", Value: "Acme Renewal Q3", SourceQuote: "Subject: Acme Renewal Q3", PageOrSection: "p.1", Confidence: "high"},
			{Field: "amount_minor", Value: "150000", SourceQuote: "Total: EUR 1,500.00", PageOrSection: "p.2", Confidence: "high"},
			{Field: "currency", Value: "EUR", SourceQuote: "all amounts in EUR", PageOrSection: "p.2", Confidence: "medium"},
			{Field: "expected_close_date", Value: "2030-12-31", SourceQuote: "offer valid until 2030-12-31", PageOrSection: "p.3", Confidence: "medium"},
			{Field: "owner_id", Value: "3e0f5a9c-0000-0000-0000-000000000001", SourceQuote: "account executive", PageOrSection: "p.1", Confidence: "medium"},
			{Field: "payment_terms", Omitted: true, OmittedReason: "not_stated_in_file"},
		},
	}}
}

// acceptEnv is one deal-scoped attachment with the fixture extractor wired
// into the accept engine, ready to accept against.
type acceptEnv struct {
	*Env
	deal   ids.UUID
	att    crmcontracts.Attachment
	engine *compose.ExtractionAccept
}

// setupExtractionAccept seeds a deal-scoped attachment and marks it clean:
// a fresh upload defaults to 'scanning' (0070), and the accept-write now
// scan-gates like every other path that touches an attachment's bytes
// (TestAcceptAttachmentExtractionRefusesWhileScanning/WhenBlocked pin that
// gate directly) — every OTHER test in this file is exercising grants,
// row scope, and field validation, so its fixture attachment must already
// be past the gate.
func setupExtractionAccept(t *testing.T) acceptEnv {
	t.Helper()
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	pipeline, open, _ := DealFixture(t, e)
	deal := e.SeedDeal(t, "Accept Target", pipeline, open, &e.Rep1)
	att := uploadDealAttachment(e.Admin(), t, h, deal, "quote.pdf", []byte("quote bytes"))
	markAttachmentClean(e.Admin(), t, e, ids.UUID(att.Id))
	return acceptEnv{
		Env:    e,
		deal:   deal,
		att:    att,
		engine: compose.NewExtractionAccept(e.Pool, acceptExtractionFixture(att.Id.String())),
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
		FieldKeys: []string{"name", "amount_minor", "currency", "expected_close_date"},
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
		FieldKeys: []string{"amount_minor", "currency"},
		Edits:     &edits,
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
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	org := e.SeedOrg(t, "Non-Deal Accept Parent", &e.Rep1)
	att := uploadScanTestAttachmentForOrg(e.Admin(), t, h, org, "org-notes.pdf", []byte("org bytes"))
	engine := compose.NewExtractionAccept(e.Pool, acceptExtractionFixture(att.Id.String()))

	_, err := engine.Accept(e.Admin(), ids.UUID(att.Id), crmcontracts.AcceptExtractionRequest{
		FieldKeys: []string{"amount_minor"},
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
		FieldKeys: []string{"amount_minor", "payment_terms"},
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
		FieldKeys: []string{"owner_id"},
	})
	var refused *compose.ExtractionAcceptError
	if !errors.As(err, &refused) || refused.Code != "not_deal_writable" {
		t.Fatalf("err = %v, want an ExtractionAcceptError with code not_deal_writable", err)
	}
	a.requireUntouchedDeal(t)
}

func TestAcceptAttachmentExtractionRequiresFieldKeys(t *testing.T) {
	a := setupExtractionAccept(t)

	_, err := a.engine.Accept(a.Admin(), ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{FieldKeys: []string{}})
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
		FieldKeys: []string{"amount_minor"},
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
		FieldKeys: []string{"amount_minor"},
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
		FieldKeys: []string{"amount_minor"},
		Edits:     &edits,
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
		FieldKeys: []string{"amount_minor", "currency"},
	}); err != nil {
		t.Fatalf("unedited accept under the same rep grant: %v", err)
	}
	if n := a.acceptNoteCount(t, "amount_minor", "Total: EUR 1,500.00", "agent:attachment-extractor"); n != 1 {
		t.Errorf("machine-stamped notes after the unedited accept = %d, want 1", n)
	}
}

// TestAcceptAttachmentExtractionEmptySeamGroundsNothing pins the unwired
// default: with no extractor, no key can ever be grounded — the accept
// refuses rather than writing an unevidenced value.
func TestAcceptAttachmentExtractionEmptySeamGroundsNothing(t *testing.T) {
	a := setupExtractionAccept(t)
	unwired := compose.NewExtractionAccept(a.Pool, nil)

	_, err := unwired.Accept(a.Admin(), ids.UUID(a.att.Id), crmcontracts.AcceptExtractionRequest{
		FieldKeys: []string{"amount_minor"},
	})
	var refused *compose.ExtractionAcceptError
	if !errors.As(err, &refused) || refused.Code != "not_grounded" {
		t.Fatalf("err = %v, want not_grounded (the NoOp seam grounds nothing)", err)
	}
	a.requireUntouchedDeal(t)
}

// TestAcceptAttachmentExtractionRefusesWhileScanning proves the
// accept-write's defense-in-depth scan gate (RD-T05): a fresh upload
// defaults to 'scanning' (0070) and the accept must refuse it — the same
// sentinel the raw-byte download and the extraction read answer — BEFORE
// the extractor ever sees the bytes, with zero writes.
func TestAcceptAttachmentExtractionRefusesWhileScanning(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	pipeline, open, _ := DealFixture(t, e)
	deal := e.SeedDeal(t, "Accept Target", pipeline, open, &e.Rep1)
	att := uploadDealAttachment(e.Admin(), t, h, deal, "quote.pdf", []byte("quote bytes"))
	// Left at the upload default ('scanning') — never marked clean.
	engine := compose.NewExtractionAccept(e.Pool, acceptExtractionFixture(att.Id.String()))

	_, err := engine.Accept(e.Admin(), ids.UUID(att.Id), crmcontracts.AcceptExtractionRequest{
		FieldKeys: []string{"amount_minor"},
	})
	if !errors.Is(err, activities.ErrScanPending) {
		t.Fatalf("err = %v, want ErrScanPending", err)
	}
	acceptEnv{Env: e, deal: deal}.requireUntouchedDeal(t)
}

// TestAcceptAttachmentExtractionRefusesWhenBlocked mirrors the scanning
// case for a quarantined verdict — terminal, never accepted.
func TestAcceptAttachmentExtractionRefusesWhenBlocked(t *testing.T) {
	e := Setup(t)
	h := activities.NewHandlers(e.Pool).WithBlobstore(blobstore.NewMemory())
	pipeline, open, _ := DealFixture(t, e)
	deal := e.SeedDeal(t, "Accept Target", pipeline, open, &e.Rep1)
	att := uploadDealAttachment(e.Admin(), t, h, deal, "quote.pdf", []byte("quote bytes"))
	if _, err := e.Activities.MarkScanResult(e.Admin(), ids.UUID(att.Id), activities.FakeScanner{Result: "blocked"}); err != nil {
		t.Fatalf("MarkScanResult(blocked): %v", err)
	}
	engine := compose.NewExtractionAccept(e.Pool, acceptExtractionFixture(att.Id.String()))

	_, err := engine.Accept(e.Admin(), ids.UUID(att.Id), crmcontracts.AcceptExtractionRequest{
		FieldKeys: []string{"amount_minor"},
	})
	if !errors.Is(err, activities.ErrAttachmentBlocked) {
		t.Fatalf("err = %v, want ErrAttachmentBlocked", err)
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
	err := database.WithWorkspaceTx(ctx, a.Pool, func(tx pgx.Tx) error {
		if _, err := a.Deals.UpdateDealTx(ctx, tx, ids.From[ids.DealKind](a.deal), deals.UpdateDealInput{
			Name: strPtr("Rolled Back Name"),
		}); err != nil {
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
