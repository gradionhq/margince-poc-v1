// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The channel counterparty ensure over a real Postgres, one writer at a time.
// Every claim here is a SQL fact a mock could only assert about itself: that
// the created person row carries no owner, that no email satellite exists
// beside the identity one, that the suppression list refuses a resurrection,
// and that the handle the provider reports is refreshed rather than frozen at
// first contact. What the same path does against a competing transaction is
// ensurechannel_contention_integration_test.go.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// channelCapturedBy is the workspace-channel connector principal's audit
// identity — never the connecting admin (design §4.1).
const channelCapturedBy = "connector:telegram"

// asChannelConnector is the principal the ingest worker mints
// (compose/telegramingest.go): a connector acting for NO human, permitted to
// create the activity it captures and the person that activity names, and
// workspace-wide because a single bot serves the whole workspace. The ensure
// runs under exactly this principal in production, so these tests do too — the
// identity satellite stamps its provenance from the acting actor, and a human
// stand-in would prove something the ingress never does.
func (e *dedupeEnv) asChannelConnector() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector,
		ID:   channelCapturedBy,
		Permissions: principal.Permissions{
			RoleKeys: []string{"channel"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true},
				"person":   {Create: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

// channelEnsureInput is a well-formed inbound channel counterparty, with the
// activity it links already captured; tests perturb the identity and the name.
func (e *dedupeEnv) channelEnsureInput(ctx context.Context, t *testing.T, ci connector.ChannelIdentity, display string) EnsureChannelCounterpartyInput {
	t.Helper()
	activityID := ids.New[ids.ActivityKind]()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO activity (id, workspace_id, kind, body, direction, source_system, source_id, source, captured_by)
			VALUES ($1, $2, 'telegram', 'hello', 'inbound', 'telegram', $3, 'telegram:seed', $4)`,
			activityID, e.ws, activityID.String(), channelCapturedBy)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return EnsureChannelCounterpartyInput{
		Identity: ci, DisplayName: display, ActivityID: activityID,
		Source: "telegram:" + activityID.String(), CapturedBy: channelCapturedBy,
	}
}

func TestEnsureChannelCounterpartyCreatesAnOwnerlessPerson(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "880101", Username: "annahandle"}

	// The display name embeds an address on a foreign domain — the tell that
	// quarantines a MAIL counterparty. It cannot mean the same thing here:
	// there is no domain the message arrived from for the name to contradict.
	in := e.channelEnsureInput(ctx, t, ci, "ceo@real-corp.example")
	res, err := e.store.EnsureChannelCounterparty(ctx, in)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if !res.PersonCreated {
		t.Fatalf("ensure = %+v, want a person created for an unmatched sender", res)
	}

	var (
		ownerless   bool
		visibility  string
		quarantined bool
		fullName    string
		capturedBy  string
	)
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT owner_id IS NULL, visibility, quarantined_at IS NOT NULL, full_name, captured_by
			  FROM person WHERE id = $1`, res.PersonID).
			Scan(&ownerless, &visibility, &quarantined, &fullName, &capturedBy)
	}); err != nil {
		t.Fatal(err)
	}
	if !ownerless {
		t.Fatal("the person carries an owner; a workspace bot has no granting human for a record to belong to (design D2)")
	}
	if visibility != "workspace" {
		t.Fatalf("visibility = %q, want workspace — an ownerless owner-visible row is visible to nobody", visibility)
	}
	if quarantined {
		t.Fatal("the person landed quarantined; every tell that flag records is a statement about a mail domain this record has none of")
	}
	if fullName != in.DisplayName {
		t.Fatalf("full_name = %q, want the provider's own name %q", fullName, in.DisplayName)
	}
	if capturedBy != channelCapturedBy {
		t.Fatalf("captured_by = %q, want the workspace-channel principal %q", capturedBy, channelCapturedBy)
	}

	// The identity satellite is the resolution key, and the activity is on the
	// person's timeline — a record nobody can find the conversation on is not
	// what D1 promises.
	if n := e.countInWorkspace(ctx, t, `
		SELECT count(*) FROM person_channel_identity
		 WHERE person_id = $1 AND provider = $2 AND channel_user_id = $3
		   AND username = $4 AND captured_by = $5 AND archived_at IS NULL`,
		res.PersonID, ci.Provider, ci.ChannelUserID, ci.Username, channelCapturedBy); n != 1 {
		t.Fatalf("%d live identity satellites, want exactly 1", n)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM activity_link WHERE activity_id = $1 AND entity_type = 'person' AND person_id = $2`,
		in.ActivityID, res.PersonID); n != 1 {
		t.Fatalf("%d activity links, want exactly 1", n)
	}

	// The same sender writing again resolves through the identity lane onto the
	// row they already have; nobody gets a twin for their second message.
	second, err := e.store.EnsureChannelCounterparty(ctx, e.channelEnsureInput(ctx, t, ci, "Anna Schmidt"))
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if second.PersonCreated || second.PersonID != res.PersonID {
		t.Fatalf("second ensure = %+v, want the incumbent %s reused", second, res.PersonID)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_channel_identity WHERE channel_user_id = $1 AND archived_at IS NULL`,
		ci.ChannelUserID); n != 1 {
		t.Fatalf("%d live bindings for one Telegram user, want 1", n)
	}
}

func TestEnsureChannelCounterpartyWritesNoEmailRow(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "880201", Username: "noaddress"}

	res, err := e.store.EnsureChannelCounterparty(ctx, e.channelEnsureInput(ctx, t, ci, "No Address Here"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// A fabricated address would be a lie the dedupe ladder then treats as an
	// exact key — the identity satellite is this record's only key.
	if n := e.countInWorkspace(ctx, t, `SELECT count(*) FROM person_email WHERE person_id = $1`, res.PersonID); n != 0 {
		t.Fatalf("%d email rows on a channel-created person, want 0", n)
	}
	// And no company: a channel identity carries no domain, so there is nothing
	// an employer could honestly be derived from.
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM relationship WHERE person_id = $1 AND kind = 'employment'`, res.PersonID); n != 0 {
		t.Fatalf("%d employment edges on a channel-created person, want 0", n)
	}
}

func TestEnsureChannelCounterpartyRespectsTheSuppressionLedger(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	erased := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "880301", Username: "gone"}
	other := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "880302", Username: "present"}

	// What an Art. 17 erasure leaves behind: the hash of the identity, and
	// nothing else about the subject.
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO erasure_suppression (workspace_id, kind, value_hash) VALUES ($1, 'channel_identity', $2)`,
			e.ws, storekit.ChannelIdentityHash(erased.Provider, erased.ChannelUserID))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := e.store.EnsureChannelCounterparty(ctx, e.channelEnsureInput(ctx, t, erased, "Erased Subject")); !errors.Is(err, ErrCounterpartySuppressed) {
		t.Fatalf("ensure of an erased identity = %v, want ErrCounterpartySuppressed — deletion sticks", err)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_channel_identity WHERE channel_user_id = $1`, erased.ChannelUserID); n != 0 {
		t.Fatalf("%d identity rows for an erased subject, want 0", n)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person WHERE full_name = 'Erased Subject'`); n != 0 {
		t.Fatalf("%d people re-created for an erased subject, want 0", n)
	}

	// The list suppresses one identity, not the channel: the next sender is
	// still a customer.
	if _, err := e.store.EnsureChannelCounterparty(ctx, e.channelEnsureInput(ctx, t, other, "Still Welcome")); err != nil {
		t.Fatalf("ensure of an unsuppressed identity: %v", err)
	}
}

// handleAuditCount counts the person-scoped audit rows a handle refresh left.
func (e *dedupeEnv) handleAuditCount(ctx context.Context, t *testing.T, personID ids.PersonID) int {
	t.Helper()
	return e.countInWorkspace(ctx, t, `
		SELECT count(*) FROM audit_log
		 WHERE entity_type = 'person' AND entity_id = $1 AND action = 'update'
		   AND after->'channel_username' IS NOT NULL`, personID)
}

// newestHandleImages reads the newest handle audit row's before/after pair, so
// a test can say the trail recorded the actual rename and not merely that some
// row exists. A NULL handle survives as nil — "this account has none" is a
// state the image must be able to express.
func (e *dedupeEnv) newestHandleImages(ctx context.Context, t *testing.T, personID ids.PersonID) (before, after *string) {
	t.Helper()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT before->'channel_username'->>'username',
			       after->'channel_username'->>'username'
			  FROM audit_log
			 WHERE entity_type = 'person' AND entity_id = $1 AND action = 'update'
			   AND after->'channel_username' IS NOT NULL
			 ORDER BY occurred_at DESC, id DESC
			 LIMIT 1`, personID).Scan(&before, &after)
	}); err != nil {
		t.Fatalf("reading the newest handle audit images for %s: %v", personID, err)
	}
	return before, after
}

// handleText renders a handle image for a failure message: an account with no
// handle at all reads as (none), and a present one as itself rather than as the
// address of the pointer holding it.
func handleText(handle *string) string {
	if handle == nil {
		return "(none)"
	}
	return *handle
}

// The handle is display data the provider lets its users change at will
// (design §4.2). The bind deliberately does not update on conflict — that is
// how a caller learns it lost the identity race — so the refresh is its own
// write, and without it every renamed sender would show the name they had the
// day they first wrote. Being its own write, it also owes its own trail: no
// enclosing person mutation covers a rename that arrives on a message from
// someone the workspace already knows.
func TestChannelUsernameRefreshesOnEveryInboundMessage(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "880401", Username: "old_handle"}

	first, err := e.store.EnsureChannelCounterparty(ctx, e.channelEnsureInput(ctx, t, ci, "Renaming Rita"))
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}

	renamed := ci
	renamed.Username = "new_handle"
	second, err := e.store.EnsureChannelCounterparty(ctx, e.channelEnsureInput(ctx, t, renamed, "Renaming Rita"))
	if err != nil {
		t.Fatalf("ensure after the rename: %v", err)
	}
	if second.PersonID != first.PersonID {
		t.Fatalf("the rename moved the conversation from %s to %s; the id is the key, the handle is not",
			first.PersonID, second.PersonID)
	}

	var stored string
	var version int64
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT username, version FROM person_channel_identity
			 WHERE provider = $1 AND channel_user_id = $2 AND archived_at IS NULL`,
			ci.Provider, ci.ChannelUserID).Scan(&stored, &version)
	}); err != nil {
		t.Fatal(err)
	}
	if stored != renamed.Username {
		t.Fatalf("stored handle = %q, want the one this message reported (%q)", stored, renamed.Username)
	}

	// The first ensure bound the handle inside the person create, whose own
	// audit row covers it; only the rename is a change of its own, so the
	// trail holds exactly one row and it names both handles.
	if n := e.handleAuditCount(ctx, t, first.PersonID); n != 1 {
		t.Fatalf("%d handle audit rows after one rename, want exactly 1", n)
	}
	if was, is := e.newestHandleImages(ctx, t, first.PersonID); was == nil || *was != ci.Username || is == nil || *is != renamed.Username {
		t.Fatalf("handle audit recorded %s → %s, want %q → %q",
			handleText(was), handleText(is), ci.Username, renamed.Username)
	}

	// A third message reporting the same handle changes nothing, so an ordinary
	// conversation does not bump the row on every message.
	if _, err := e.store.EnsureChannelCounterparty(ctx, e.channelEnsureInput(ctx, t, renamed, "Renaming Rita")); err != nil {
		t.Fatalf("third ensure: %v", err)
	}
	var settled int64
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT version FROM person_channel_identity
			 WHERE provider = $1 AND channel_user_id = $2 AND archived_at IS NULL`,
			ci.Provider, ci.ChannelUserID).Scan(&settled)
	}); err != nil {
		t.Fatal(err)
	}
	if settled != version {
		t.Fatalf("version moved from %d to %d on an unchanged handle; the refresh must be a no-op when nothing changed",
			version, settled)
	}
	if n := e.handleAuditCount(ctx, t, first.PersonID); n != 1 {
		t.Fatalf("%d handle audit rows after an unchanged handle, want still 1 — the trail records changes, not messages", n)
	}
}
