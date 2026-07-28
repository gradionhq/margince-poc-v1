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
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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

// stubUnsubscribeLinker stands in for the consent module's preference-token
// mint: ok=false is how a locked (transactional) purpose answers.
type stubUnsubscribeLinker struct {
	token string
	ok    bool
	err   error
}

func (l stubUnsubscribeLinker) UnsubscribeToken(context.Context, string, string) (string, bool, error) {
	return l.token, l.ok, l.err
}

// stubMailbox stands in for the connection registry's send-grant answer.
type stubMailbox struct {
	capable bool
	err     error
}

func (m stubMailbox) SendCapable(context.Context) (bool, error) { return m.capable, m.err }

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

// RFC 8058 deliverability is derived on the send path itself, not in one
// transport: the MCP send_email tool reaches this store method directly, and
// marketing mail without a List-Unsubscribe header is what gets a domain
// filtered.
func TestSendEmailDerivesUnsubscribeHeadersForAMarketingPurpose(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	linker := stubUnsubscribeLinker{token: testUnsubscribeTok, ok: true}

	sent, err := e.store(linker).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("marketing_email"), stubConsentGate{}, stager)
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}

	wantURL := testBaseURL + "/v1/public/preferences/" + testUnsubscribeTok + "/unsubscribe?purpose=marketing_email"
	staged := stager.only(t)
	if staged.ListUnsubscribe != "<"+wantURL+">" {
		t.Fatalf("staged List-Unsubscribe = %q, want the bracketed one-click URL <%s>", staged.ListUnsubscribe, wantURL)
	}
	// Header and footer derive from the SAME token and URL, so a recipient's
	// visible link can never point somewhere the machine header does not.
	if !strings.Contains(staged.Body, wantURL) {
		t.Fatalf("staged body carries no visible unsubscribe link:\n%s", staged.Body)
	}
	if !strings.Contains(staged.Body, testBaseURL+"/v1/public/preferences/"+testUnsubscribeTok+"\n") &&
		!strings.HasSuffix(staged.Body, testBaseURL+"/v1/public/preferences/"+testUnsubscribeTok) {
		t.Fatalf("staged body carries no manage-preferences link:\n%s", staged.Body)
	}
	// The timeline records what actually went out, footer included.
	if sent.Body == nil || !strings.Contains(*sent.Body, wantURL) {
		t.Fatalf("logged activity body does not match the transmitted body: %v", sent.Body)
	}
}

// A transactional message has nothing to unsubscribe from — the linker
// declines to mint a token for a locked purpose — so it carries no header and
// its body is left exactly as the sender wrote it.
func TestSendEmailDerivesNoUnsubscribeHeadersForATransactionalPurpose(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	linker := stubUnsubscribeLinker{token: testUnsubscribeTok, ok: false}

	if _, err := e.store(linker).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager); err != nil {
		t.Fatalf("SendEmail: %v", err)
	}

	staged := stager.only(t)
	if staged.ListUnsubscribe != "" {
		t.Fatalf("transactional send carries List-Unsubscribe %q, want none", staged.ListUnsubscribe)
	}
	if staged.Body != "As discussed." {
		t.Fatalf("transactional body = %q, want the sender's text untouched", staged.Body)
	}
}

// The activity and its delivery are one fact. A staging failure that still
// left the activity behind would promise the user a send that was never
// queued, on a timeline they have no way to correct.
func TestSendEmailCommitsNoActivityWhenStagingFails(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{err: errors.New("delivery table unavailable")}

	_, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager)
	if err == nil {
		t.Fatal("SendEmail reported success though staging refused")
	}
	if n := e.outboundCount(t); n != 0 {
		t.Fatalf("%d outbound activities survived a failed staging, want 0 (one transaction, one fact)", n)
	}
}

// Accepting mail we already know cannot leave hands the user a 202 and a
// silently parked delivery they cannot see. Every mailbox connected before
// the send grant existed holds read-only access, so the check must ask about
// the GRANT — "is something connected?" would pass all of them.
func TestSendEmailRefusesWhenTheMailboxHoldsNoSendGrant(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	store := e.store(stubUnsubscribeLinker{}).WithMailbox(stubMailbox{capable: false})

	_, err := store.SendEmail(e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager)
	var refusal *MailboxNotSendCapableError
	if !errors.As(err, &refusal) {
		t.Fatalf("send with no send-capable mailbox → %v, want a MailboxNotSendCapableError", err)
	}
	if !strings.Contains(refusal.Error(), "reconnect") {
		t.Fatalf("refusal %q does not tell the user what to do about it", refusal.Error())
	}
	if len(stager.staged) != 0 || e.outboundCount(t) != 0 {
		t.Fatal("a refused send still staged a delivery or logged an activity")
	}
}

// The staged delivery names the anchor, and naming a record is a read: an
// anchor outside the caller's row scope refuses with the same answer a
// missing one gives, before anything is staged.
func TestSendEmailRefusesAnAnchorOutsideTheCallersRowScope(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	e.linkToPersonOwnedBy(t, anchor, e.other)
	stager := &recordingStager{}

	_, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeOwn), anchor, sendInput("transactional"), stubConsentGate{}, stager)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("send anchored to another rep's activity → %v, want ErrNotFound (existence-hiding)", err)
	}
	if len(stager.staged) != 0 || e.outboundCount(t) != 0 {
		t.Fatal("a send refused at the row-scope gate still staged a delivery or logged an activity")
	}
}

// Send and capture key the same column. The send writes thread_key at write
// time; capture's echo of the same natural key is an ON CONFLICT DO NOTHING
// upsert, which the log path answers by returning the existing row untouched
// — so neither leg can overwrite the other's value.
func TestReplayingASourceKeyLeavesTheStoredThreadKeyUntouched(t *testing.T) {
	e := setupSend(t)
	ctx := e.as(principal.RowScopeAll)
	store := NewStore(e.pool)
	system, sourceID := "gmail", "replayed@buyer.test"

	first, created, err := store.LogActivity(ctx, LogActivityInput{
		Kind: "email", Source: "manual", SourceSystem: &system, SourceID: &sourceID,
		ThreadKey: "root@buyer.test",
	})
	if err != nil || !created {
		t.Fatalf("first log: %v (created=%v)", err, created)
	}
	second, created, err := store.LogActivity(ctx, LogActivityInput{
		Kind: "email", Source: "gmail", SourceSystem: &system, SourceID: &sourceID,
		ThreadKey: "someone-elses-root@buyer.test",
	})
	if err != nil {
		t.Fatalf("replayed log: %v", err)
	}
	if created {
		t.Fatal("replaying a source key created a second activity")
	}
	if second.Id != first.Id {
		t.Fatalf("replay returned activity %s, want the existing %s", second.Id, first.Id)
	}
	if got := e.storedThreadKey(t, ids.UUID(first.Id)); got != "root@buyer.test" {
		t.Fatalf("stored thread_key = %q after a replay, want the value the first write set", got)
	}
}
