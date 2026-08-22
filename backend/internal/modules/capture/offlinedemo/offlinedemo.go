// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package offlinedemo generates a demo installation's correspondence and
// pushes it through the real capture pipeline.
//
// WHY A CONNECTOR RATHER THAN A SEEDER PHASE. Every other record type the demo
// holds is written through the product's own API. Captured mail cannot be:
// there is no create endpoint for it, by design — a message arrives through
// the capture sink or it does not exist. So the seeder had no way in, and the
// demo had companies, deals, contracts and invoices behind an empty inbox.
// The reply join, the thread view, "who on our team knows this contact" and
// every person's timeline sat blank in front of anyone being shown the
// product.
//
// Going through the sink is the point. The threads, participants, attachments,
// audit rows and outbox events are the ones the PRODUCT writes, not ones a
// seeder invented behind it — the same argument that keeps the rest of the
// seeder on the API.
//
// NOTHING IS EVER SENT. This connector has no HTTP client and implements
// neither EmailSender nor MessageSender, which a fitness test pins. The
// addresses it names are the dataset's synthesized ones, and the dataset's own
// rule is that nothing is ever delivered to them.
//
// It is inert until somebody inserts a capture_connection naming it, which
// only the demo seeder and scripts/seed-dev.sql do. An installation that never
// seeds a demo carries the code and never runs it, exactly as the finance
// mirror's offline_demo provider does.
package offlinedemo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Name is the provider id, matching the capture_connection CHECK and the
// finance mirror's own offline_demo.
const Name = "offline_demo"

// generatorVersion changes when the threads themselves change shape.
//
// It rides in the cursor, so a bump makes the next sync re-emit from the
// start; unchanged ids are refused by the natural key and only genuinely new
// messages land. Bumping never rewrites history — an existing activity is
// never updated — so a change to the templates reaches only companies that
// have not been synced yet.
// Version 2 re-dates the correspondence BACKWARD from the run. Version 1
// anchored it on the organization's created_at, which in a fresh installation
// is today — so every message landed in the future, the sink refused them all,
// and the cursor those runs wrote carries a `through` two months ahead. A
// version bump is exactly the tool for that: the cursor no longer matches, the
// generator starts from the beginning, and the natural key refuses whatever
// did land.
// 3: the correspondence is written in the account's own language rather than
// German for everybody.
//
// The bump makes the next sync REPLAY from the start rather than resume, which
// is what a changed generator needs. It does NOT rewrite what is already
// captured: the sink's natural key is ON CONFLICT DO NOTHING, so a re-emitted
// message with the same id is refused and the German row it already wrote
// stays German. An existing demo therefore keeps its German mail, and only a
// FRESH seed reads in the account's own language. Relocalising an existing one
// is a data migration this connector deliberately does not do — capture is
// append-once, and a connector that edited history would be the wrong thing to
// build for a demo.
const generatorVersion = 3

// Directory is what the connector needs to know about the installation to
// write plausible mail for it. Implemented in compose, because reading people
// and deals is not capture's business: the connector is a pure generator and
// this is the only thing it is handed.
type Directory interface {
	// Mailbox describes the seat this sync runs for.
	Mailbox(ctx context.Context, userID string) (Mailbox, error)
}

// Mailbox is one seat and the accounts it owns.
type Mailbox struct {
	UserID      string
	DisplayName string
	Email       string
	// Colleague is copied on some threads, so a CC has somebody to be.
	ColleagueEmail string
	ColleagueName  string
	Accounts       []Account
}

// Account is one company the seat owns, with the parties and facts a thread
// can be written from.
type Account struct {
	OrganizationID string
	Name           string
	Domain         string
	Lifecycle      string
	// Locale is the dataset's own answer for this company — `de`, `vi`, `ko`
	// or `en` — carried in from company-locale.json through the auth payload.
	// Empty when the installation was not seeded from a dataset, and the
	// domain suffix answers instead.
	Locale string
	People []Person
	Deals  []Deal
	// ContractEndsInDays is negative for a contract already over. Zero when
	// the account holds none.
	ContractEndsInDays int
	ContractNumber     string
	// Now is when the sync runs. The correspondence is dated BACKWARD from
	// it, because a captured message in the future is refused — and the
	// organization's own created_at is today in a fresh installation, which
	// is what made the first version generate nothing at all.
	Now time.Time
}

// Person is somebody at the account we write to.
type Person struct {
	Name  string
	Email string
	Role  string
}

// Deal is an open or closed opportunity a thread can be about.
type Deal struct {
	ID    string
	Name  string
	Stage string
}

// Connector is the offline demo capture provider.
type Connector struct {
	directory Directory
}

// New builds the connector over a directory.
func New(directory Directory) *Connector { return &Connector{directory: directory} }

// Descriptor is static metadata. Read-only scopes and the auto-execute tier,
// because generating correspondence into the local database is a capture, not
// an outbound action.
func (c *Connector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name:     Name,
		Version:  "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierAutoExecute, // read-only capture
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

// Authenticate returns a fixed non-secret token. There is nothing to
// authenticate against: the generator reads the local database and talks to
// nobody, so a credential would be a secret protecting an empty room.
func (c *Connector) Authenticate(_ context.Context, req connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth(req.Payload), nil
}

// HealthCheck always succeeds — there is no remote to be unhealthy.
func (c *Connector) HealthCheck(context.Context, connector.Auth) error { return nil }

// authPayload is what the seeder writes into capture_connection.auth.
//
// The seat id alone was enough until the correspondence had to be written in
// the account's own language. That answer lives in the DATASET
// (datasets/v1/company-locale.json, hand-checked, because a domain suffix gets
// it wrong for a fifth of the Automation World list — vuletech.com is
// Vietnamese and dacell.com is Korean, both on .com). The seeder reads that
// file; this connector runs in the worker and has no dataset directory, so the
// answer has to travel, and `auth` is the channel that already exists between
// exactly these two.
type authPayload struct {
	UserID string `json:"user_id"`
	// Locales maps a lowercased domain to `de`, `vi`, `ko` or `en`. A domain
	// absent from it falls back to the suffix rule, which is right for the
	// German majority and is all an installation without a seeded dataset has.
	Locales map[string]string `json:"locales,omitempty"`
}

// readAuth parses the credential bundle, tolerating the bare seat id.
//
// scripts/seed-dev.sql writes the user id as a plain string, and so did every
// seeder before the locale map. A payload that is not JSON is therefore not an
// error: it is the older form, and treating it as one keeps a dev database
// seeded by either route working.
func readAuth(auth connector.Auth) authPayload {
	raw := strings.TrimSpace(string(auth))
	if raw == "" {
		return authPayload{}
	}
	var payload authPayload
	if err := json.Unmarshal([]byte(raw), &payload); err == nil && payload.UserID != "" {
		return payload
	}
	return authPayload{UserID: raw}
}

// cursorState is what a completed sync remembers.
type cursorState struct {
	Version int    `json:"v"`
	Gen     int    `json:"gen"`
	Through string `json:"through"` // RFC3339 of the newest message emitted
}

// Sync generates this mailbox's correspondence and hands it to the sink.
//
// The cursor is what keeps a two-minute sweep cheap. On the first pass the
// cursor is empty and everything is emitted; afterwards only messages newer
// than `through` are, which for a static generator is none — so the steady
// state is a read of the directory and no upserts at all.
func (c *Connector) Sync(ctx context.Context, auth connector.Auth, cursor connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	// The seat rides in Auth, the way imap carries its mailbox owner: Auth is
	// an opaque credential bundle and the connector decides what is in it.
	// The seeder writes the seat id and the dataset's locale map there with no
	// vault ref, so nothing here needs a keyvault to resolve a secret that does
	// not exist.
	payload := readAuth(auth)
	if payload.UserID == "" {
		return cursor, fmt.Errorf("offline demo sync has no seat to generate for")
	}
	userID := payload.UserID

	state, since := readCursor(cursor)

	mailbox, err := c.directory.Mailbox(ctx, userID)
	if err != nil {
		return cursor, fmt.Errorf("reading the mailbox for %s: %w", userID, err)
	}
	newest := since
	emitted, skipped := 0, 0
	for _, account := range mailbox.Accounts {
		account.Locale = payload.Locales[strings.ToLower(account.Domain)]
		for _, msg := range generate(mailbox, account) {
			if !since.IsZero() && !msg.OccurredAt.After(since) {
				skipped++
				continue
			}
			if _, err := sink.Upsert(ctx, msg.record()); err != nil {
				// One refused message must not cost the rest of the mailbox:
				// the sink drops an internal-only record deliberately, and a
				// link to a row this seat cannot see is refused by design.
				//
				// It is LOGGED rather than swallowed. A silent continue here is
				// what made an earlier bug invisible — the generator produced
				// six mails per customer and the database stayed empty with
				// nothing to read anywhere.
				slog.WarnContext(ctx, "offline demo: the sink refused a message",
					"id", msg.MessageID, "kind", msg.Kind, "error", err)
				continue
			}
			emitted++
			if msg.OccurredAt.After(newest) {
				newest = msg.OccurredAt
			}
		}
	}

	slog.InfoContext(ctx, "offline demo: generated correspondence",
		"seat", mailbox.Email, "accounts", len(mailbox.Accounts),
		"emitted", emitted, "already_seen", skipped)
	if !newest.IsZero() {
		state.Through = newest.Format(time.RFC3339)
	}
	next, err := json.Marshal(state)
	if err != nil {
		return cursor, fmt.Errorf("encoding the cursor: %w", err)
	}
	return connector.Cursor(next), nil
}

// readCursor is where a sync resumes from.
//
// A cursor written by a DIFFERENT generator version is discarded rather than
// honoured: version 1 dated its messages in the future and left a `through`
// two months ahead, which would skip everything forever. Discarding restarts
// the generator, and the natural key refuses whatever already landed.
func readCursor(cursor connector.Cursor) (cursorState, time.Time) {
	state := cursorState{Version: 1, Gen: generatorVersion}
	if len(cursor) == 0 {
		return state, time.Time{}
	}
	var prior cursorState
	if err := json.Unmarshal(cursor, &prior); err != nil || prior.Gen != generatorVersion {
		return state, time.Time{}
	}
	state = prior
	if state.Through == "" {
		return state, time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, state.Through)
	if err != nil {
		return state, time.Time{}
	}
	return state, parsed
}

// Normalize re-parses one stored raw message. The generator writes its own
// JSON into Raw, so this is the inverse of that and nothing more.
func (c *Connector) Normalize(_ context.Context, raw connector.RawRecord) ([]connector.NormalizedRecord, error) {
	var msg message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("re-reading a generated message: %w", err)
	}
	return []connector.NormalizedRecord{msg.record()}, nil
}

// assert the connector satisfies the port.
var _ connector.Connector = (*Connector)(nil)

// capture is imported for ActivityFields, which the records carry.
var _ = capture.ActivityFields{}
