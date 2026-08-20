// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The ingress port over real migrated Postgres: what a unit's record actually
// becomes, and what the two authority checks do when the database is the one
// answering them.
//
// None of it is checkable without a database. The idempotency is a unique
// index, the counterparty decision is a ladder of queries inside the sink's
// transaction, the consent check is a row in extension_secret, and the member's
// authority is resolved by identity against live grants — a fake at any of
// those points would be asserting this test's own arithmetic rather than the
// pipeline's behaviour.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/extsecrets"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The probe unit, its declared source, and the provenance the two derive.
const (
	ingressUnit         = "alpha"
	ingressProbeSystem  = "probe-chat"
	ingressProbeSource  = "ext:" + ingressUnit + ":" + ingressProbeSystem
	ingressProbeCapture = "connector:ext:" + ingressUnit
	// The transport the probe unit supplies. It is SNAKE where the ingress
	// system above is kebab, and that is the rule rather than a typo: a
	// provider is a channel_provider row and satisfies that column's own
	// grammar, which is not the ingress declaration's.
	ingressProbeProvider = "probe_chat"
)

// ingressEnv is the runtime env plus everything an ingest needs to be allowed
// to happen: a unit that declared the source, a role the member holds, and a
// credential they deposited.
type ingressEnv struct {
	*extRuntimeEnv
	member ids.UUID
}

func setupIngress(t *testing.T) *ingressEnv {
	t.Helper()
	e := setupExtRuntime(t)
	composeCapturingUnit(t, ingressUnit,
		// The probe unit SUPPLIES a transport as well as capturing from one, which
		// is the shape a channel unit actually has — and what a unit may name on a
		// message is bounded by this declaration, so a set without it would leave
		// the message tests refused for a reason the test never chose.
		[]extension.Channel{{Provider: ingressProbeProvider}},
		extension.IngressSource{
			System: ingressProbeSystem, Lands: []extension.RecordKind{extension.KindActivity},
		})
	bindCaptureForTest(t, e)
	grantCapture(t, e, e.Rep1)
	depositCredential(t, e, e.Rep1)
	return &ingressEnv{extRuntimeEnv: e, member: e.Rep1}
}

// bindCaptureForTest binds the ONE capture pipeline and restores what was bound
// before, rather than clearing to nil: a test that cleared would leave a
// sibling refusing with errIngressUnwired, which names a deployment fault that
// is not there.
func bindCaptureForTest(t *testing.T, e *extRuntimeEnv) {
	t.Helper()
	previous := boundExtensionRuntime().captureSink
	BindExtensionCapture(e.Pool, CaptureConfig{})
	t.Cleanup(func() {
		extensionRuntimeDeps.mu.Lock()
		defer extensionRuntimeDeps.mu.Unlock()
		extensionRuntimeDeps.captureSink = previous
	})
}

// capturePolicy is the narrowest grant that lets a captured message land: the
// activity itself, plus the person and organization the counterparty ladder may
// mint beside it.
//
// Narrow rather than an admin document on purpose. What these tests assert is
// that the port runs on the MEMBER's authority, so the grant has to be small
// enough that taking it away is visibly the reason the next landing is refused.
const capturePolicy = `{"objects":{
	"activity":{"read":true,"create":true,"update":true},
	"person":{"read":true,"create":true,"update":true},
	"organization":{"read":true,"create":true,"update":true}},
	"row_scope":"all"}`

// grantCapture gives the member a real role carrying capturePolicy, because the
// ingest runs on their LIVE authority: the harness seeds users with no grants
// at all, and a suite that built the permissions into a principal instead would
// be testing a principal the port does not construct.
func grantCapture(t *testing.T, e *extRuntimeEnv, member ids.UUID) {
	t.Helper()
	owner := integration.OwnerConn(t)
	// The role is seeded once per test database and reused; the grant is
	// per-member. Neither statement assumes the other has not run, because
	// these tests seed two different members in the same fixture.
	var roleID ids.UUID
	if err := owner.QueryRow(context.Background(),
		`WITH existing AS (
		     SELECT id FROM role WHERE key = 'ingress-probe'
		 ), created AS (
		     INSERT INTO role (key, name, permissions)
		     SELECT 'ingress-probe', 'Ingress Probe', $1::jsonb
		     WHERE NOT EXISTS (SELECT 1 FROM existing)
		     RETURNING id
		 )
		 SELECT id FROM created UNION ALL SELECT id FROM existing`,
		capturePolicy).Scan(&roleID); err != nil {
		t.Fatalf("seeding the capture role: %v", err)
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO role_assignment (role_id, user_id)
		 SELECT $1, $2
		 WHERE NOT EXISTS (
		     SELECT 1 FROM role_assignment WHERE role_id = $1 AND user_id = $2)`,
		roleID, member); err != nil {
		t.Fatalf("granting %s the capture role: %v", member, err)
	}
}

// revokeRoles takes every grant away, which is how a demotion presents to the
// port: the resolver answers, and what it answers is an authority that can no
// longer land anything.
func revokeRoles(t *testing.T, e *extRuntimeEnv, member ids.UUID) {
	t.Helper()
	owner := integration.OwnerConn(t)
	if _, err := owner.Exec(context.Background(),
		`DELETE FROM role_assignment WHERE user_id = $1`, member); err != nil {
		t.Fatalf("revoking %s's grants: %v", member, err)
	}
}

// depositCredential is the consent act, written through the REAL secret store
// rather than by inserting a row: what the port reads is a mapping row the
// store owns, and a hand-inserted one would prove nothing about the path a
// member's connect actually takes.
func depositCredential(t *testing.T, e *extRuntimeEnv, member ids.UUID) {
	t.Helper()
	ctx := e.callCtx(e.WS)
	store := extsecrets.For(ingressUnit, e.Pool, e.vault)
	if err := store.PutUser(ctx, extension.UserID(member.String()), ingressTestSecretKey, []byte("pat_probe")); err != nil {
		t.Fatalf("depositing the member's credential: %v", err)
	}
}

// ingestingRuntime mints the Runtime an unattended run holds — a job tick's —
// for the probe unit.
//
// The invocation's context stays INSIDE: a Runtime derives its tenant from the
// context it was minted with, and every call below deliberately passes a plain
// background context, which is what a handler does. Handing the invocation's
// context back would let a test pass the one context the port is designed not
// to need.
func (e *ingressEnv) ingestingRuntime() *callRuntime {
	return jobRuntimeFor(e.callCtx(e.WS), ingressUnit, "1.0.0", "job/probe", boundExtensionRuntime())
}

// aProviderRecord is one directed message, keyed the way a connector keys one.
func aProviderRecord(key, senderEmail string) extension.Record {
	return extension.Record{
		System: ingressProbeSystem,
		Key:    key,
		Activity: extension.ActivityFields{
			Kind:       "note",
			Subject:    "A Sender",
			Body:       "the message a member was directed at",
			OccurredAt: time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC),
			Direction:  extension.DirectionInbound,
		},
		ThreadKey: "probe-chat:ws-7:channel-1",
		Counterparty: extension.Counterparty{
			Email: senderEmail, DisplayName: "A Sender",
			Domain: mailDomainOf(senderEmail), Direction: extension.DirectionInbound,
		},
		Addresses: []string{senderEmail, "a@authz.test"},
		Raw:       []byte(`{"id":1042,"type":"dm"}`),
	}
}

func mailDomainOf(email string) string {
	for i := len(email) - 1; i >= 0; i-- {
		if email[i] == '@' {
			return email[i+1:]
		}
	}
	return ""
}

// The whole path, and the write shape at the end of it: a unit hands over one
// provider record and the installation gains an activity, its raw evidence, a
// ledger row and an outbox event — none of which the unit wrote or could write.
func TestAUnitsRecordLandsAsAnActivityWithEvidenceAndTheWriteShape(t *testing.T) {
	e := setupIngress(t)
	rt := e.ingestingRuntime()

	result, err := rt.Ingest(context.Background(), extension.UserID(e.member.String()),
		aProviderRecord("ws-7:1042", "outside@example.test"))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if result.Disposition != extension.DispositionAccepted {
		t.Fatalf("disposition = %q, want accepted", result.Disposition)
	}
	if result.Ref.ID == "" || result.Ref.Type == "" {
		t.Fatalf("ref = %+v, want the record the core now holds", result.Ref)
	}

	activityID := ids.MustParse(result.Ref.ID)
	var source, capturedBy, threadKey, subject string
	e.readAsWorkspace(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT source, captured_by, coalesce(thread_key, ''), coalesce(subject, '')
			   FROM activity WHERE id = $1`, activityID).Scan(&source, &capturedBy, &threadKey, &subject)
	})
	switch {
	case source != ingressProbeSource:
		t.Errorf("source = %q, want the core-derived %q — a unit does not spell its own provenance", source, ingressProbeSource)
	// The CONNECTOR and the member behind it, which is more than the record
	// carried: the unit hands over `connector:ext:<unit>` — the equality the
	// sink checks against the acting principal — and the core stamps the
	// member's id beside it, so a landed row says on whose authority it
	// arrived as well as which unit produced it.
	case capturedBy != ingressProbeCapture+":"+e.member.String():
		t.Errorf("captured_by = %q, want %q", capturedBy, ingressProbeCapture+":"+e.member.String())
	case threadKey != "probe-chat:ws-7:channel-1":
		t.Errorf("thread_key = %q, want the unit's namespaced conversation key", threadKey)
	case subject != "A Sender":
		t.Errorf("subject = %q, want the record's own", subject)
	}

	// The evidence, the ledger row and the event: the write shape, for a record
	// the unit never wrote itself.
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM raw_capture WHERE source_system = $1 AND source_id = $2`,
		ingressProbeSource, "ws-7:1042"); got != 1 {
		t.Errorf("raw_capture rows = %d, want the provider's original kept once", got)
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'activity' AND entity_id = $1`, activityID); got == 0 {
		t.Error("the landing wrote no ledger row")
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM event_outbox WHERE envelope->'entity'->>'id' = $1`, activityID.String()); got == 0 {
		t.Error("the landing published no event — an audit row with no event is the write shape half-kept")
	}
}

// aChannelMessage is the same record as a message on the unit's own transport,
// naming the account it can be answered at.
func aChannelMessage(key, senderEmail, account string) extension.Record {
	rec := aProviderRecord(key, senderEmail)
	rec.Activity.Kind = extension.ActivityKindMessage
	rec.Activity.ChannelProvider = ingressProbeProvider
	// ONE naming, never both: the core refuses a counterparty named by an
	// address AND by a channel account, because the two resolve through
	// different ladders. A channel record is named by the account it can be
	// answered at, so the address goes.
	rec.Counterparty = extension.Counterparty{
		DisplayName: "A Sender", Direction: extension.DirectionInbound,
		ChannelIdentity: extension.ChannelIdentity{
			Provider: ingressProbeProvider, ChannelUserID: account, DisplayName: "A Sender",
		},
	}
	// The addresses stay: they are a different question — every party the
	// message names, which is what the internal-colleague gate reads, and an
	// empty set disables that gate rather than passing it.
	rec.Addresses = []string{senderEmail, "a@authz.test"}
	return rec
}

// registerProbeTransport puts the unit's transport in the registry, through the
// real reconcile.
//
// It is not optional bookkeeping: activity.channel_provider is a foreign key
// into channel_provider, so a captured message naming an unregistered transport
// is refused by the database — which is exactly the failure a unit channel would
// have if the boot reconcile did not write it.
func registerProbeTransport(t *testing.T, e *ingressEnv) {
	t.Helper()
	// The reconcile sets BOTH packages' in-memory sendable snapshots to what it
	// was passed. Restore the pre-registry default rather than leaving this
	// test's set behind, or a later test in the same process sees an
	// installation with no Telegram transport — which is a failure that names
	// somebody else's code.
	t.Cleanup(func() {
		activities.SetChannelProviders([]string{capture.ProviderTelegram})
		comms.SetChannelProviders([]string{capture.ProviderTelegram})
	})
	if err := reconcileChannelProviders(context.Background(), e.Pool, []string{capture.ProviderTelegram}); err != nil {
		t.Fatalf("registering the unit's transport: %v", err)
	}
	t.Cleanup(func() {
		// In dependency order, and the order itself is the finding: the registry
		// row is a foreign-key parent of every message filed on it AND of every
		// identity bound on it, which is what stops a live transport being
		// deregistered out from under the conversations that reference it. That
		// is also why the boot reconcile never deletes.
		owner := integration.OwnerConn(t)
		for _, statement := range []string{
			`DELETE FROM activity WHERE channel_provider = $1`,
			`DELETE FROM person_channel_identity WHERE provider = $1`,
			`DELETE FROM channel_provider WHERE provider = $1`,
		} {
			if _, err := owner.Exec(context.Background(), statement, ingressProbeProvider); err != nil {
				t.Errorf("cleaning up after the probe transport (%s): %v", statement, err)
			}
		}
	})
}

// A unit's captured chat message lands as a MESSAGE on the unit's own transport,
// with the account it can be answered at bound to the person behind it.
//
// This is the whole point of the slice, and each half is separately load-bearing.
// The kind and the provider are the two axes stated separately (ADR-0107/A158):
// the send path reads the PROVIDER column, so a message filed as a `note` — which
// is what this unit landed before it supplied a channel — is a conversation
// nothing can reply on. And the identity binding is what the reply path resolves
// its recipient FROM: without it the message is repliable in principle and
// answers "nobody on this conversation can be reached" in practice.
func TestAUnitsChannelMessageLandsAsARepliableConversation(t *testing.T) {
	e := setupIngress(t)
	registerProbeTransport(t, e)
	rt := e.ingestingRuntime()

	result, err := rt.Ingest(context.Background(), extension.UserID(e.member.String()),
		aChannelMessage("ws-7:3001", "someone@gmail.com", "probe-channel-1"))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if result.Disposition != extension.DispositionAccepted {
		t.Fatalf("disposition = %q, want accepted", result.Disposition)
	}

	var kind, provider string
	e.readAsWorkspace(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT kind, coalesce(channel_provider, '') FROM activity WHERE id = $1`,
			ids.MustParse(result.Ref.ID)).Scan(&kind, &provider)
	})
	if kind != extension.ActivityKindMessage {
		t.Errorf("kind = %q, want %q — the timeline must know this was a message at all", kind, extension.ActivityKindMessage)
	}
	if provider != ingressProbeProvider {
		t.Errorf("channel_provider = %q, want %q — the send path reads this column, and an empty one is a conversation no reply can leave on",
			provider, ingressProbeProvider)
	}

	// The binding, which is what makes the recipient resolvable — and it names
	// the account the UNIT reported, not one derived from the address.
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM person_channel_identity WHERE provider = $1 AND channel_user_id = $2`,
		ingressProbeProvider, "probe-channel-1"); got != 1 {
		t.Errorf("channel identity bindings = %d, want the one the reply path resolves its recipient from", got)
	}
}

// A unit naming a transport it does NOT supply is refused, and nothing lands.
//
// This is the sharpest refusal on the write door: `telegram` is a real
// registered transport with a real workspace bot behind it, so the row would be
// a valid SEND ANCHOR for a conversation the unit does not own — a rep replying
// on it would transmit a message from the workspace's own bot to whoever the
// unit linked, the unit choosing the target and the human supplying the
// authority.
func TestAUnitCannotFileAMessageOnACoreConnectorsTransport(t *testing.T) {
	e := setupIngress(t)
	rt := e.ingestingRuntime()

	stolen := aChannelMessage("ws-7:3002", "someone@gmail.com", "probe-channel-2")
	stolen.Activity.ChannelProvider = "telegram"
	stolen.Counterparty.ChannelIdentity.Provider = "telegram"

	_, err := rt.Ingest(context.Background(), extension.UserID(e.member.String()), stolen)

	if !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("Ingest → %v, want extension.ErrInvalid — a unit may name only a transport it declared", err)
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM activity WHERE source = $1 AND source_id = $2`, ingressProbeSource, "ws-7:3002"); got != 0 {
		t.Errorf("activity rows = %d, want none — the refusal must land nothing, not land it and complain", got)
	}
}

// A replay is a no-op, which is the property the whole cursor rule rests on: a
// unit may re-ingest anything it is not sure about, and does.
func TestASecondIngestOfTheSameRecordLandsNothingNew(t *testing.T) {
	e := setupIngress(t)
	rt := e.ingestingRuntime()
	record := aProviderRecord("ws-7:1042", "outside@example.test")

	first, err := rt.Ingest(context.Background(), extension.UserID(e.member.String()), record)
	if err != nil {
		t.Fatalf("the first ingest: %v", err)
	}
	second, err := rt.Ingest(context.Background(), extension.UserID(e.member.String()), record)
	if err != nil {
		t.Fatalf("the replay: %v", err)
	}
	if second.Disposition != extension.DispositionAccepted {
		t.Errorf("the replay answered %q — a unit that read this as a failure would retry forever", second.Disposition)
	}
	if second.Ref.ID != first.Ref.ID {
		t.Errorf("the replay named %s, want the record the first landing created (%s)", second.Ref.ID, first.Ref.ID)
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM activity WHERE source = $1`, ingressProbeSource); got != 1 {
		t.Fatalf("activity rows = %d, want one — the natural key is what makes a replay free", got)
	}
}

// The counterparty ladder, as it actually decides — which is NOT "a person
// appears". A first-time corporate address is captured and DEFERRED to the
// pending inbox, and that is the common case for a chat connector.
func TestAFirstTimeCorporateSenderDefersItsCounterparty(t *testing.T) {
	e := setupIngress(t)
	rt := e.ingestingRuntime()

	if _, err := rt.Ingest(context.Background(), extension.UserID(e.member.String()),
		aProviderRecord("ws-7:2001", "buyer@acme-corp.test")); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM capture_pending_counterparty WHERE email = $1`, "buyer@acme-corp.test"); got != 1 {
		t.Errorf("pending counterparty rows = %d, want the deferral the ladder writes for a first-time corporate sender", got)
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM person_email WHERE email = $1`, "buyer@acme-corp.test"); got != 0 {
		t.Errorf("person rows = %d, want none — the record is captured, and who it is with is not decided yet", got)
	}
}

// The other arm of the same ladder: a freemail sender IS created, with the
// company suppressed. Both arms are asserted because a suite that pinned only
// one would describe the pipeline as doing whichever it happened to check.
func TestAFreemailSenderMintsThePerson(t *testing.T) {
	e := setupIngress(t)
	rt := e.ingestingRuntime()

	if _, err := rt.Ingest(context.Background(), extension.UserID(e.member.String()),
		aProviderRecord("ws-7:2002", "someone@gmail.com")); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM person_email WHERE email = $1`, "someone@gmail.com"); got != 1 {
		t.Errorf("person rows = %d, want the one the freemail arm creates", got)
	}
	// The SUPPRESSED half, which the person count cannot see. Capture withholds
	// a company rather than creating one, so what a corporate domain leaves
	// behind is the domain row and the OPEN QUESTION about it — and gmail.com
	// must leave neither. Without these the arm reads as asserted while a
	// change that started minting a company from a consumer mailbox passes.
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM organization_domain WHERE domain = $1`, "gmail.com"); got != 0 {
		t.Errorf("organization domain rows for gmail.com = %d, want none — a consumer mailbox is not a company", got)
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM organization_domain_disposition WHERE domain = $1`, "gmail.com"); got != 0 {
		t.Errorf("triage rows for gmail.com = %d, want none — the domain answers the question itself, so nothing is queued", got)
	}
}

// The SKIP arm, which the port calls load-bearing and which nothing exercised
// end to end until this test: the core drops a wholly-internal message on
// purpose, and the seam reports that as a SUCCESS carrying no reference.
//
// It matters because of what the unit does next. Mapped to an error, a
// deliberate drop is a failure a connector retries on every poll, forever —
// re-fetching the same message and re-committing capture's breadcrumb each
// time — while the member's cursor never moves past it.
//
// The installation has to have registered its own domain for the gate to fire
// at all: on a fresh install nothing is internal, which is a real arm of the
// same gate and the reason this test seeds a verified domain rather than
// assuming one.
func TestAWhollyInternalMessageIsSkippedAsASuccess(t *testing.T) {
	e := setupIngress(t)
	registerOwnDomain(t, e.extRuntimeEnv, "authz.test")
	rt := e.ingestingRuntime()

	// Both ends on the installation's own domain: colleagues talking, which is
	// not evidence of a customer relationship.
	internal := aProviderRecord("ws-7:7001", "colleague@authz.test")
	internal.Addresses = []string{"colleague@authz.test", "a@authz.test"}

	result, err := rt.Ingest(context.Background(), extension.UserID(e.member.String()), internal)
	if err != nil {
		t.Fatalf("Ingest: %v — a deliberate drop reported as a failure is one a connector retries forever", err)
	}
	if result.Disposition != extension.DispositionSkipped {
		t.Fatalf("disposition = %q, want skipped", result.Disposition)
	}
	if result.Ref.ID != "" {
		t.Errorf("ref = %+v, want none — nothing was kept", result.Ref)
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM activity WHERE source = $1`, ingressProbeSource); got != 0 {
		t.Errorf("activity rows = %d, want none — the message was dropped on purpose", got)
	}
}

// registerOwnDomain gives the installation a verified mail domain, which is
// what makes the internal-only gate have anything to compare against.
func registerOwnDomain(t *testing.T, e *extRuntimeEnv, domain string) {
	t.Helper()
	owner := integration.OwnerConn(t)
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO workspace_email_domain (domain, verified)
		 VALUES ($1, true)`, domain); err != nil {
		t.Fatalf("registering %s as an own domain: %v", domain, err)
	}
}

// A credential under a key the unit no longer declares is not consent to
// anything the current manifest describes — extension_secret keeps the mapping
// row after a declaration changes, and what an operator can read has to be what
// the core acts on.
func TestACredentialUnderAnUndeclaredKeyIsNotConsent(t *testing.T) {
	e := setupIngress(t)
	ctx := e.callCtx(e.WS)
	store := extsecrets.For(ingressUnit, e.Pool, e.vault)
	if err := store.DeleteUser(ctx, extension.UserID(e.member.String()), ingressTestSecretKey); err != nil {
		t.Fatalf("removing the declared credential: %v", err)
	}
	// The member still holds a credential with this unit — under a key it does
	// not declare, which is the state a removed or renamed declaration leaves.
	if err := store.PutUser(ctx, extension.UserID(e.member.String()), "a-key-the-unit-no-longer-declares", []byte("pat_probe")); err != nil {
		t.Fatalf("depositing the stale credential: %v", err)
	}

	_, err := e.ingestingRuntime().Ingest(context.Background(), extension.UserID(e.member.String()),
		aProviderRecord("ws-7:8001", "outside@example.test"))
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden — a key the manifest does not describe authorizes nothing", err)
	}
}

// The consent check, against the real store: a member who has deposited nothing
// with this unit cannot be acted for, whatever the unit passes.
func TestAMemberWhoDepositedNothingCannotBeActedFor(t *testing.T) {
	e := setupIngress(t)
	grantCapture(t, e.extRuntimeEnv, e.Rep2)
	rt := e.ingestingRuntime()

	_, err := rt.Ingest(context.Background(), extension.UserID(e.Rep2.String()),
		aProviderRecord("ws-7:3001", "outside@example.test"))
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden — depositing a credential IS the consent, and Rep2 deposited none", err)
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM activity WHERE source = $1`, ingressProbeSource); got != 0 {
		t.Errorf("activity rows = %d, want none — a refused ingest must land nothing", got)
	}
}

// The authority is LIVE, and this is the assertion that says so: the same unit,
// the same member, the same record — refused after the member's grants are
// taken away, with no restart and nothing re-bound.
func TestADemotedMemberLandsNothingFromTheNextCallOn(t *testing.T) {
	e := setupIngress(t)
	rt := e.ingestingRuntime()

	if _, err := rt.Ingest(context.Background(), extension.UserID(e.member.String()),
		aProviderRecord("ws-7:4001", "outside@example.test")); err != nil {
		t.Fatalf("the first ingest, while granted: %v", err)
	}
	revokeRoles(t, e.extRuntimeEnv, e.member)

	_, err := rt.Ingest(context.Background(), extension.UserID(e.member.String()),
		aProviderRecord("ws-7:4002", "outside@example.test"))
	// The class, not merely an error: an unwired pool or any later fault
	// satisfies `err != nil` just as well, and the live-authority ceiling this
	// test is named for would go untested. ErrForbidden is what the ceiling
	// answers — the member still exists, and no longer may.
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden — a demoted member's connection still landed a record", err)
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM raw_capture WHERE source_id = $1`, "ws-7:4002"); got != 0 {
		t.Errorf("raw_capture rows = %d for the refused record, want none", got)
	}
}

// A member of ANOTHER workspace is not a member here, and the answer says
// nothing about whether they exist.
//
// The CLASS is asserted rather than non-nil-ness, because the sentence above is
// a claim about which refusal comes back and a non-nil check cannot see it: an
// unwired pool, or any later failure, satisfies `err != nil` just as well while
// the property this test is named for goes untested.
//
// ErrForbidden is the existence-hiding answer HERE, which is the reverse of the
// usual reading and so is worth stating. It is reached before anything looks
// the member up: what it reports is that no credential is on deposit with THIS
// unit under that id, which is equally true of a member of another workspace,
// an archived one, and an id belonging to nobody at all. A not-found would be
// the DISCLOSING answer, because only a lookup can tell those three apart.
func TestAMemberOfAnotherWorkspaceCannotBeActedFor(t *testing.T) {
	e := setupIngress(t)
	rt := e.ingestingRuntime()

	_, err := rt.Ingest(context.Background(), extension.UserID(ids.NewV7().String()),
		aProviderRecord("ws-7:5001", "outside@example.test"))
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden — an ingest either ran as somebody who is not a member "+
			"of this workspace, or answered something about whether they exist", err)
	}
}

// A suite here used to pin behaviour that only a SECOND workspace could produce.
// ADR-0091 §8 phase D took the tenant column off app_user, and an installation
// serves one organization (ADR-0061), so the fixture it needed is a state the
// product cannot reach — the guarantee has no subject rather than a weaker one.

// The nesting refusal, on a POOL OF ONE — which is the configuration where the
// defect it guards is not a failure but a hang: the ingest would wait for the
// only connection, which the unit's own transaction is holding.
func TestAnIngestInsideAUnitsTransactionIsRefusedOnAPoolOfOne(t *testing.T) {
	e := setupIngress(t)
	single := singleConnectionPool(t)
	rt := jobRuntimeFor(e.callCtx(e.WS), ingressUnit, "1.0.0", "job/probe",
		extensionRuntimeBinding{pool: single, vault: e.vault, captureSink: boundExtensionRuntime().captureSink})

	// A wall clock, because the failure this guards is a HANG: without the
	// refusal this call waits for a connection that cannot come back, and a
	// test with no deadline would hang with it.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := rt.Tx(ctx, func(inner context.Context, _ extension.Tx) error {
		_, ingestErr := rt.Ingest(inner, extension.UserID(e.member.String()),
			aProviderRecord("ws-7:6001", "outside@example.test"))
		return ingestErr
	})
	if !errors.Is(err, extension.ErrNestedIngest) {
		t.Fatalf("err = %v, want ErrNestedIngest — on this pool the alternative is not a failure, it is a hang", err)
	}
}

// singleConnectionPool is the app pool with exactly one connection — the
// configuration in which a second acquire inside a held transaction cannot ever
// be satisfied, which is the difference between a check that fails and a
// process that stops.
func singleConnectionPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_APP_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parsing the app DSN: %v", err)
	}
	cfg.MaxConns, cfg.MinConns = 1, 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("opening the single-connection pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// readAsWorkspace runs one read inside a workspace-bound transaction, because
// every table below carries forced row-level security: the same query outside
// one matches nothing at all, and a count of zero read that way looks exactly
// like a write that never happened.
func (e *ingressEnv) readAsWorkspace(t *testing.T, read func(context.Context, pgx.Tx) error) {
	t.Helper()
	ctx := e.callCtx(e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error { return read(ctx, tx) }); err != nil {
		t.Fatalf("reading back what the ingest wrote: %v", err)
	}
}

func (e *ingressEnv) countAsWorkspace(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var count int
	e.readAsWorkspace(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(&count)
	})
	return count
}

// principalOfIngest is not asserted directly anywhere above, and that is
// deliberate: what an ingest runs as is only meaningful through what it can
// WRITE, which is what the demotion test measures. A test reading the principal
// back would be reading this package's own construction.
var _ = principal.PrincipalConnector
