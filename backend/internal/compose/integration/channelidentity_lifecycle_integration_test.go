// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// person_channel_identity is a person satellite, so it owes every lifecycle
// path its siblings ride: Art. 17 erasure (plus the suppression row that makes
// the erasure stick), Art. 15 subject access, the retention anonymizer, the
// merge relink, and the archive cascade. backend/satellite_lifecycle_test.go
// proves each path WRITES the table; this suite proves the writes do the right
// thing on real rows, which is the half a source scan cannot see.
//
// Every failure mode here is silent in production: a satellite nobody archived
// keeps resolving messages onto a soft-deleted record, one nobody relinked is
// orphaned on the merged-away half, and a missing suppression row means the
// erased subject's very next message recreates them with nothing erroring.

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// telegramProvider is the only provider the 0146 CHECK admits today.
const telegramProvider = "telegram"

// seedChannelIdentity binds one provider account to a person. channelUserID
// must be unique per test: the live unique index spans (provider,
// channel_user_id) without person_id, deliberately, because one account is one
// human across the whole installation.
func seedChannelIdentity(t *testing.T, e *Env, person ids.UUID, channelUserID, username string) {
	t.Helper()
	e.WsExec(t, `
		INSERT INTO person_channel_identity
		  (workspace_id, person_id, provider, channel_user_id, username, source, captured_by)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, 'telegram', $2, $3, 'telegram', 'connector:telegram')`,
		person, channelUserID, username)
}

// liveIdentities counts the person's un-archived channel identities.
func liveIdentities(t *testing.T, e *Env, person ids.UUID) int {
	t.Helper()
	return e.WsCount(t,
		`SELECT count(*) FROM person_channel_identity WHERE person_id = $1 AND archived_at IS NULL`, person)
}

// suppressed asks the same probe the ingest paths ask: is this account an
// erased subject's?
func suppressed(t *testing.T, e *Env, channelUserID string) bool {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	var answer bool
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		var err error
		answer, err = storekit.ChannelIdentitySuppressed(ctx, tx, telegramProvider, channelUserID)
		return err
	}); err != nil {
		t.Fatalf("suppression probe: %v", err)
	}
	return answer
}

func TestErasurePurgesTheChannelIdentityAndSuppressesTheAccount(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()
	person := e.SeedPerson(t, "Tomas Telegram", nil)
	seedChannelIdentity(t, e, person, "10101", "tomas")

	// Art. 15 hands the binding back while it is held — asserted BEFORE the
	// erasure, so the emptiness afterwards measures the erasure and not a
	// section that never worked.
	pkg, err := privacy.AssembleSAR(admin, e.Pool, personIDOf(person))
	if err != nil {
		t.Fatalf("AssembleSAR: %v", err)
	}
	if len(pkg.ChannelIdentities) != 1 {
		t.Fatalf("SAR exported %d channel identities, want the subject's 1 — Art. 15 owes what is held",
			len(pkg.ChannelIdentities))
	}
	if got := pkg.ChannelIdentities[0]["channel_user_id"]; got != "10101" {
		t.Errorf("SAR channel_user_id = %v, want 10101", got)
	}
	if got := pkg.ChannelIdentities[0]["username"]; got != "tomas" {
		t.Errorf("SAR username = %v, want tomas", got)
	}

	// The probe must be honest before the erasure, or "suppressed" afterwards
	// proves nothing.
	if suppressed(t, e, "10101") {
		t.Fatal("a live account already reads as suppressed — the probe cannot detect an erasure")
	}

	if err := privacy.NewEraser(e.Pool).ErasePerson(admin, person, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	if n := e.WsCount(t,
		`SELECT count(*) FROM person_channel_identity WHERE person_id = $1`, person); n != 0 {
		t.Errorf("%d channel identity rows survived the erasure", n)
	}
	if !suppressed(t, e, "10101") {
		t.Error("the erased account is not suppressed — the subject's next message would recreate them, silently")
	}
	// The list is per account, not per provider: erasing one subject must not
	// lock every other Telegram user out of the workspace.
	if suppressed(t, e, "20202") {
		t.Error("an unrelated account reads as suppressed — the suppression key is too coarse")
	}

	// The tombstone counts what it suppressed without re-storing the id it
	// hashed: a tombstone that named the account would re-hold the identifier
	// it certifies gone.
	if n := e.WsCount(t, `
		SELECT count(*) FROM audit_log
		 WHERE action = 'erase' AND entity_id = $1
		   AND (evidence->>'channel_identities_suppressed')::int = 1`, person); n != 1 {
		t.Errorf("%d erase tombstones carry a channel-identity count of 1, want exactly 1", n)
	}
	if n := e.WsCount(t, `
		SELECT count(*) FROM audit_log
		 WHERE action = 'erase' AND entity_id = $1 AND evidence::text LIKE '%10101%'`, person); n != 0 {
		t.Error("the erasure tombstone re-stores the channel account id it certifies gone")
	}
}

// A retention anonymize is not an Art. 17 request: the clock ran out, the
// subject did not ask, and they may lawfully come back. So the rows go and the
// suppression list stays empty — suppressing here would silently bar a person
// the workspace is free to re-capture.
func TestRetentionAnonymizeDropsTheChannelIdentityWithoutSuppressingIt(t *testing.T) {
	e := Setup(t)
	seedRetentionPolicies(t, e)
	person := e.SeedPerson(t, "Otto Overage", nil)
	seedChannelIdentity(t, e, person, "30303", "otto")
	// Past the seeded person/no_consent_no_deal window (730 days), with no
	// granted consent and no deal stake — the selector's own conditions.
	e.WsExec(t, `UPDATE person SET created_at = now() - interval '800 days' WHERE id = $1`, person)

	svc := privacy.NewRetentionService(e.Pool, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.Evaluate(context.Background()); err != nil {
		t.Fatalf("retention pass: %v", err)
	}

	if n := e.WsCount(t,
		`SELECT count(*) FROM person_channel_identity WHERE person_id = $1`, person); n != 0 {
		t.Errorf("%d channel identity rows survived the anonymize — inbound messages would keep binding to the wiped record", n)
	}
	if suppressed(t, e, "30303") {
		t.Error("a retention anonymize armed the suppression list — the subject may lawfully return")
	}
}

func TestMergeRelinksTheChannelIdentityOntoTheSurvivor(t *testing.T) {
	e := Setup(t)
	source := e.SeedPerson(t, "Ada Source", nil)
	target := e.SeedPerson(t, "Ada Target", nil)
	seedChannelIdentity(t, e, source, "40404", "ada")

	survivor, err := e.People.MergePerson(e.Admin(), personIDOf(source), personIDOf(target))
	if err != nil {
		t.Fatalf("MergePerson: %v", err)
	}
	if personIDOf(ids.UUID(survivor.Id)) != personIDOf(target) {
		t.Fatalf("survivor = %s, want the target %s", survivor.Id, target)
	}

	if n := liveIdentities(t, e, source); n != 0 {
		t.Errorf("%d channel identities stayed on the merged-away source — the human behind them writes into a record nobody reads", n)
	}
	if n := liveIdentities(t, e, target); n != 1 {
		t.Errorf("survivor holds %d channel identities, want the relinked 1", n)
	}
}

func TestArchivingAPersonArchivesTheChannelIdentity(t *testing.T) {
	e := Setup(t)
	person := e.SeedPerson(t, "Vera Vanished", nil)
	seedChannelIdentity(t, e, person, "50505", "vera")

	if _, err := e.People.ArchivePerson(e.Admin(), personIDOf(person)); err != nil {
		t.Fatalf("ArchivePerson: %v", err)
	}

	if n := liveIdentities(t, e, person); n != 0 {
		t.Errorf("%d channel identities stayed LIVE under an archived person — the next message would resolve onto the soft-deleted record", n)
	}
	// Archived, not deleted: the binding is history the SAR still owes, and the
	// row is what a later erasure hashes onto the suppression list.
	if n := e.WsCount(t,
		`SELECT count(*) FROM person_channel_identity WHERE person_id = $1`, person); n != 1 {
		t.Errorf("%d channel identity rows remain, want the 1 archived row", n)
	}
}

// seedTelegramMessageRaw plants one raw_capture row shaped like a real
// Telegram message update: the sender id lives at message.from.id, the exact
// path capture/telegram's Normalize reads it from and purgeChannelRawCapture
// (privacy/erasure_channels.go) matches against. messageID doubles as the
// update_id — irrelevant to the fixture, but it is itself a second place a
// digit run can appear in the payload, which is exactly what the
// over-deletion guard test below needs.
func seedTelegramMessageRaw(t *testing.T, e *Env, sourceID string, messageID int64, senderID, text string) {
	t.Helper()
	e.WsExec(t, `
		INSERT INTO raw_capture (workspace_id, source_system, source_id, payload)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'telegram', $1,
		  jsonb_build_object('update_id', $2::bigint,
		    'message', jsonb_build_object(
		      'message_id', $2::bigint,
		      'chat', jsonb_build_object('id', 555),
		      'from', jsonb_build_object('id', $3::bigint, 'username', 'seed'),
		      'date', 1700000000,
		      'text', $4::text)))`,
		sourceID, messageID, senderID, text)
}

// seedTelegramMembershipRaw plants one raw_capture row shaped like a real
// my_chat_member update (a block/unblock report): the sender id lives at
// my_chat_member.new_chat_member.user.id, the second of the two shapes
// purgeChannelRawCapture and the SAR raw-capture section both match — a
// Telegram-only subject who only ever blocked the bot, and never sent a
// message, must still be reachable.
func seedTelegramMembershipRaw(t *testing.T, e *Env, sourceID string, updateID int64, senderID, status string) {
	t.Helper()
	e.WsExec(t, `
		INSERT INTO raw_capture (workspace_id, source_system, source_id, payload)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'telegram', $1,
		  jsonb_build_object('update_id', $2::bigint,
		    'my_chat_member', jsonb_build_object(
		      'new_chat_member', jsonb_build_object(
		        'user', jsonb_build_object('id', $3::bigint, 'username', 'seed'),
		        'status', $4::text))))`,
		sourceID, updateID, senderID, status)
}

// rawCaptureSurvives reports whether exactly one raw_capture row still holds
// the given source_id — the harness's WsCount wrapped for readability at the
// call sites below.
func rawCaptureSurvives(t *testing.T, e *Env, sourceID string) bool {
	t.Helper()
	return e.WsCount(t,
		`SELECT count(*) FROM raw_capture WHERE source_system = 'telegram' AND source_id = $1`, sourceID) == 1
}

// TestErasureOfAChannelOnlySubjectPurgesRawCaptureWithoutOverreaching closes
// design §10's gap: a Telegram-created Person carries no email at all, so
// purgeDerivedTraces' email-ILIKE lane (erasure.go) never runs for them, and
// before this task their raw captures — the verbatim update JSON, including
// display name, username, numeric id and full message text — survived Art. 17
// erasure forever. It also proves the guard the design calls out explicitly:
// the match is a typed JSONB path equality on the sender id, never a
// substring search, so a DIFFERENT subject's row and a row that merely
// mentions the erased subject's digit sequence somewhere OTHER than the
// sender id both survive. An over-deleting erasure — another subject's
// evidence gone — would be the catastrophic failure mode here, worse than the
// under-deleting one this task fixes.
func TestErasureOfAChannelOnlySubjectPurgesRawCaptureWithoutOverreaching(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()

	subject := e.SeedPerson(t, "Ilya Channel-Only", nil)
	seedChannelIdentity(t, e, subject, "10101", "ilya")
	seedTelegramMessageRaw(t, e, "erase-message", 1, "10101", "hello there")
	seedTelegramMembershipRaw(t, e, "erase-membership", 2, "10101", "kicked")

	other := e.SeedPerson(t, "Petra Other Subject", nil)
	seedChannelIdentity(t, e, other, "20202", "petra")
	seedTelegramMessageRaw(t, e, "other-subjects-message", 3, "20202", "unrelated message")

	// The decoy: the erased subject's own digit run, "10101", appears TWICE
	// in this payload — as the update/message id and inside the free text —
	// but the sender id (the only field a correct match reads) is neither
	// subject above. An ILIKE-over-payload purge would delete this row; the
	// typed path match must not.
	seedTelegramMessageRaw(t, e, "decoy-message", 10101, "30303", "reach me at 10101 anytime")

	if err := privacy.NewEraser(e.Pool).ErasePerson(admin, subject, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	if rawCaptureSurvives(t, e, "erase-message") {
		t.Error("the erased subject's message-shaped raw capture survived erasure")
	}
	if rawCaptureSurvives(t, e, "erase-membership") {
		t.Error("the erased subject's my_chat_member-shaped raw capture survived erasure")
	}
	if !rawCaptureSurvives(t, e, "other-subjects-message") {
		t.Error("a DIFFERENT subject's raw capture was deleted — the channel-identity match is too broad")
	}
	if !rawCaptureSurvives(t, e, "decoy-message") {
		t.Error("a row merely CONTAINING the erased subject's digit sequence (not as the sender id) was deleted — " +
			"the match is matching by substring somewhere, not the typed JSONB sender id path")
	}

	if n := e.WsCount(t, `
		SELECT count(*) FROM audit_log
		 WHERE action = 'erase' AND entity_id = $1
		   AND (evidence->>'raw_rows_purged')::int = 2`, subject); n != 1 {
		t.Errorf("%d erase tombstones carry a raw_rows_purged count of 2, want exactly 1 — "+
			"the tombstone's own count should reflect both channel-matched rows", n)
	}
}

// TestSARIncludesAChannelOnlySubjectsRawCapture closes the Art. 15 half of
// design §10's gap: sarSections' RawCapture query matched by email only, so a
// Telegram-only subject's export silently omitted their entire raw-capture
// history — an Art. 15 failure as real as erasure's, and just as silent,
// because nothing about assembling an incomplete package errors or logs.
func TestSARIncludesAChannelOnlySubjectsRawCapture(t *testing.T) {
	e := Setup(t)
	subject := e.SeedPerson(t, "Nadia Channel-Only", nil)
	seedChannelIdentity(t, e, subject, "40404", "nadia")
	seedTelegramMessageRaw(t, e, "sar-message", 5, "40404", "hi from telegram")
	seedTelegramMembershipRaw(t, e, "sar-membership", 6, "40404", "kicked")

	// A row that must NOT appear: a different subject's message.
	unrelated := e.SeedPerson(t, "Boris Unrelated", nil)
	seedChannelIdentity(t, e, unrelated, "50506", "boris")
	seedTelegramMessageRaw(t, e, "sar-unrelated-message", 7, "50506", "not the subject")

	pkg, err := privacy.AssembleSAR(e.Admin(), e.Pool, personIDOf(subject))
	if err != nil {
		t.Fatalf("AssembleSAR: %v", err)
	}

	got := map[string]bool{}
	for _, row := range pkg.RawCapture {
		got[row["source_id"].(string)] = true
	}
	if !got["sar-message"] {
		t.Error("SAR omitted the subject's message-shaped raw capture")
	}
	if !got["sar-membership"] {
		t.Error("SAR omitted the subject's my_chat_member-shaped raw capture")
	}
	if got["sar-unrelated-message"] {
		t.Error("SAR included a DIFFERENT subject's raw capture — the channel-identity match is too broad")
	}
}
