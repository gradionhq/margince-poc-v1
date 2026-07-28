// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// The one send path over a real migrated Postgres: what it stamps on the
// activity, what it hands the delivery machinery, and what it refuses.
// Every case drives Store.SendEmail rather than the HTTP handler, because
// the MCP tool surface calls the store directly — a behaviour proven only
// through the handler would be proven for one transport out of two.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	testBaseURL        = "https://crm.example.test"
	testUnsubscribeTok = "tok-1"
)

// recordingStager captures what the send path hands the delivery machinery,
// and can refuse, so a staging failure's effect on the activity is provable.
type recordingStager struct {
	staged []DeliveryRequest
	err    error
}

func (r *recordingStager) StageTx(_ context.Context, _ pgx.Tx, in DeliveryRequest) error {
	r.staged = append(r.staged, in)
	return r.err
}

// only returns the single staged request, failing the test when the send
// path staged anything other than exactly one.
func (r *recordingStager) only(t *testing.T) DeliveryRequest {
	t.Helper()
	if len(r.staged) != 1 {
		t.Fatalf("staged %d deliveries, want exactly 1", len(r.staged))
	}
	return r.staged[0]
}

type sendEnv struct {
	owner *pgx.Conn
	pool  *pgxpool.Pool
	ws    ids.UUID
	rep   ids.UUID
	other ids.UUID
}

func setupSend(t *testing.T) *sendEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	e := &sendEnv{owner: owner, ws: ids.NewV7(), rep: ids.NewV7(), other: ids.NewV7()}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Send', $2, 'EUR')`,
		e.ws, "send-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	for _, user := range []ids.UUID{e.rep, e.other} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`,
			user, e.ws, "rep-"+user.String()+"@send.test"); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.pool = pool
	return e
}

// store is the send path as compose wires it for a marketing-capable
// deployment: a preference-token linker and the boot-configured public base
// URL both live on the STORE, so the MCP transport reaches them too.
func (e *sendEnv) store(linker UnsubscribeLinker) *Store {
	return NewStore(e.pool).WithUnsubscribe(linker).WithPublicBaseURL(testBaseURL)
}

// as binds an authenticated rep at the given row scope. Sending is a human
// act: the delivery's sending identity is derived from this principal.
func (e *sendEnv) as(scope principal.RowScope) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.rep.String(), UserID: e.rep,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true, Read: true, Update: true},
				"person":   {Read: true},
			},
			RowScope: scope,
		},
	})
}

// seedAnchor writes the reply anchor as the table owner, so the send path
// reads a row it did not itself create — the shape capture leaves behind.
func (e *sendEnv) seedAnchor(t *testing.T, sourceID, threadKey string) ids.ActivityID {
	t.Helper()
	id := ids.New[ids.ActivityKind]()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction,
		                      source_system, source_id, source, captured_by, thread_key)
		VALUES ($1, $2, 'email', 'Pricing question', now(), 'inbound',
		        CASE WHEN $3 = '' THEN NULL ELSE 'gmail' END, NULLIF($3, ''),
		        'gmail', 'human:x', NULLIF($4, ''))`,
		id, e.ws, sourceID, threadKey); err != nil {
		t.Fatalf("seeding the anchor: %v", err)
	}
	return id
}

// linkToPersonOwnedBy ties the anchor to a person owned by the given user —
// the only way an activity leaves the workspace-shared default and becomes
// scoped, since an activity carries no owner_id of its own.
func (e *sendEnv) linkToPersonOwnedBy(t *testing.T, anchor ids.ActivityID, owner ids.UUID) {
	t.Helper()
	person := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by)
		 VALUES ($1, $2, 'Buyer', $3, 'manual', 'human:x')`, person, e.ws, owner); err != nil {
		t.Fatalf("seeding the linked person: %v", err)
	}
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
		 VALUES ($1, $2, 'person', $3)`, e.ws, anchor, person); err != nil {
		t.Fatalf("linking the anchor: %v", err)
	}
}

func (e *sendEnv) outboundCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM activity WHERE workspace_id = $1 AND direction = 'outbound'`,
		e.ws).Scan(&n); err != nil {
		t.Fatalf("counting outbound activities: %v", err)
	}
	return n
}

func (e *sendEnv) storedThreadKey(t *testing.T, id ids.UUID) string {
	t.Helper()
	var key string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT coalesce(thread_key, '') FROM activity WHERE id = $1`, id).Scan(&key); err != nil {
		t.Fatalf("reading the stored thread key: %v", err)
	}
	return key
}

func (e *sendEnv) storedCounterparty(t *testing.T, id ids.UUID) (string, bool) {
	t.Helper()
	var email string
	var attested bool
	if err := e.owner.QueryRow(context.Background(),
		`SELECT coalesce(counterparty_email, ''), counterparty_outbound_attested FROM activity WHERE id = $1`,
		id).Scan(&email, &attested); err != nil {
		t.Fatalf("reading the stored counterparty: %v", err)
	}
	return email, attested
}

func sendInput(purpose string) SendEmailInput {
	return SendEmailInput{
		Recipients:     []string{"buyer@example.test", "boss@example.test"},
		Cc:             []string{"boss@example.test"},
		Subject:        "Re: pricing",
		Body:           "As discussed.",
		ConsentPurpose: purpose,
	}
}

// The key the send writes IS the key capture derives from the provider's own
// copy of the sent message. Bracketed, or filed under a different system, the
// two never collide and every outbound email lands on the timeline twice.
func TestSendEmailStampsTheUnbracketedMessageIDAsTheSourceKey(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}

	sent, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager)
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}

	if sent.SourceSystem == nil || *sent.SourceSystem != "gmail" {
		t.Fatalf("activity source_system = %v, want gmail (the system whose echo must collapse onto this row)", sent.SourceSystem)
	}
	if sent.SourceId == nil {
		t.Fatal("activity carries no source_id; the captured sent copy would create a second timeline row")
	}
	if strings.ContainsAny(*sent.SourceId, "<>") {
		t.Fatalf("activity source_id = %q; capture strips the angle brackets, so a bracketed key never matches", *sent.SourceId)
	}
	staged := stager.only(t)
	if staged.MessageID != *sent.SourceId {
		t.Fatalf("staged message id %q != activity source_id %q; the transmitted identity must be the stored key",
			staged.MessageID, *sent.SourceId)
	}
	if staged.ActivityID.UUID != ids.UUID(sent.Id) {
		t.Fatalf("staged delivery anchors activity %s, want the activity just written (%s)", staged.ActivityID, sent.Id)
	}
	if staged.Provider != "gmail" {
		t.Fatalf("staged provider = %q, want gmail", staged.Provider)
	}
	if len(staged.Recipients) != 1 || staged.Recipients[0] != "buyer@example.test" {
		t.Fatalf("staged To: = %v, want the merged consent list minus the cc'd address", staged.Recipients)
	}
	if len(staged.Cc) != 1 || staged.Cc[0] != "boss@example.test" {
		t.Fatalf("staged Cc: = %v", staged.Cc)
	}
}

// The provider's echo of the sent copy is an ON CONFLICT DO NOTHING upsert,
// so it updates nothing: whatever threading this send does not write at write
// time stays unwritten, and reply detection — which joins an inbound reply
// against outbound activities on thread_key — never fires for this mail.
func TestSendEmailStampsTheThreadKeyFromTheAnchor(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "parent@buyer.test", "root@buyer.test")
	stager := &recordingStager{}

	sent, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager)
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}

	staged := stager.only(t)
	if staged.ThreadKey != "root@buyer.test" {
		t.Fatalf("staged thread key = %q, want the anchor's conversation identity", staged.ThreadKey)
	}
	if got := e.storedThreadKey(t, ids.UUID(sent.Id)); got != "root@buyer.test" {
		t.Fatalf("stored thread_key = %q, want the anchor's — the echo will not fill it in later", got)
	}
	if staged.InReplyTo != "parent@buyer.test" {
		t.Fatalf("staged In-Reply-To = %q, want the anchor's own message identity", staged.InReplyTo)
	}
	// The recipient's reply roots at References[0]; capture derives its
	// thread_key the same way. If that root were not this message's stored
	// thread_key, the reply would key a conversation this send is not part of.
	if len(staged.References) == 0 || staged.References[0] != staged.ThreadKey {
		t.Fatalf("staged References = %v, want a chain rooted at the thread key %q", staged.References, staged.ThreadKey)
	}
	if staged.References[len(staged.References)-1] != "parent@buyer.test" {
		t.Fatalf("staged References = %v, want the anchor's identity last (oldest first)", staged.References)
	}
}

// An anchor filed into a conversation but carrying no message identity of its
// own — a CRM-side row, not a captured message — still roots the chain at the
// conversation, and sends no In-Reply-To because there is no single message it
// answers. The recipient's reply then keys the same conversation, which is all
// the join needs.
func TestSendEmailThreadsAnAnchorThatHasAKeyButNoMessageIdentity(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "root@buyer.test")
	stager := &recordingStager{}

	if _, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager); err != nil {
		t.Fatalf("SendEmail: %v", err)
	}

	staged := stager.only(t)
	if staged.InReplyTo != "" {
		t.Fatalf("In-Reply-To = %q, want empty — the anchor names no message to reply to", staged.InReplyTo)
	}
	if len(staged.References) != 1 || staged.References[0] != "root@buyer.test" {
		t.Fatalf("References = %v, want the conversation root alone", staged.References)
	}
	if staged.ThreadKey != "root@buyer.test" {
		t.Fatalf("thread key = %q, want the anchor's conversation", staged.ThreadKey)
	}
}

// A send composed in the CRM is the ONLY record that this workspace wrote to
// this address: the provider's echo of the same message collides with this row
// and writes nothing. Capture's correspondence-positive gate reads exactly
// these two columns, so a send that leaves them unset makes a prospect the CRM
// emailed a stranger when they reply.
func TestSendEmailRecordsTheOutboundCorrespondenceEvidence(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}

	in := sendInput("transactional")
	in.Recipients = []string{"Buyer@Example.test", "boss@example.test"}
	in.Cc = []string{"boss@example.test"}
	sent, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), anchor, in, stubConsentGate{}, stager)
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}

	email, attested := e.storedCounterparty(t, ids.UUID(sent.Id))
	// Normalized on the way in, like capture's own write: the same address in
	// different casing is the same correspondence.
	if email != "buyer@example.test" {
		t.Fatalf("counterparty_email = %q, want the normalized primary recipient", email)
	}
	if !attested {
		t.Fatal("a send the workspace composed did not attest its own outbound correspondence")
	}
}

// A send with no conversation behind it starts one: no In-Reply-To, no
// References, and the message is its own thread root — which is exactly the
// key mailmap derives for a root message read back from the mailbox.
func TestSendEmailWithoutAnchorContextRootsANewThread(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}

	sent, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager)
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}

	staged := stager.only(t)
	if staged.InReplyTo != "" || len(staged.References) != 0 {
		t.Fatalf("new conversation staged In-Reply-To %q / References %v, want both empty", staged.InReplyTo, staged.References)
	}
	if staged.ThreadKey != staged.MessageID {
		t.Fatalf("thread key = %q, want this message's own identity %q (a root is its own key)", staged.ThreadKey, staged.MessageID)
	}
	if got := e.storedThreadKey(t, ids.UUID(sent.Id)); got != staged.MessageID {
		t.Fatalf("stored thread_key = %q, want %q", got, staged.MessageID)
	}
}
