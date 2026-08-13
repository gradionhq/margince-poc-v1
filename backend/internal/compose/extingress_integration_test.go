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
	composeIngressFor(t, ingressUnit, extension.IngressSource{
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
		     SELECT id FROM role WHERE workspace_id = $1 AND key = 'ingress-probe'
		 ), created AS (
		     INSERT INTO role (workspace_id, key, name, permissions)
		     SELECT $1, 'ingress-probe', 'Ingress Probe', $2::jsonb
		     WHERE NOT EXISTS (SELECT 1 FROM existing)
		     RETURNING id
		 )
		 SELECT id FROM created UNION ALL SELECT id FROM existing`,
		e.WS, capturePolicy).Scan(&roleID); err != nil {
		t.Fatalf("seeding the capture role: %v", err)
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO role_assignment (workspace_id, role_id, user_id)
		 SELECT $1, $2, $3
		 WHERE NOT EXISTS (
		     SELECT 1 FROM role_assignment WHERE workspace_id = $1 AND role_id = $2 AND user_id = $3)`,
		e.WS, roleID, member); err != nil {
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
		`DELETE FROM role_assignment WHERE workspace_id = $1 AND user_id = $2`, e.WS, member); err != nil {
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
	if err := store.PutUser(ctx, extension.UserID(member.String()), "api-token", []byte("pat_probe")); err != nil {
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
	if err == nil {
		t.Fatal("a demoted member's connection still landed a record")
	}
	if got := e.countAsWorkspace(t,
		`SELECT count(*) FROM raw_capture WHERE source_id = $1`, "ws-7:4002"); got != 0 {
		t.Errorf("raw_capture rows = %d for the refused record, want none", got)
	}
}

// A member of ANOTHER workspace is not a member here, and the answer says
// nothing about whether they exist — existence-hiding, exactly as every other
// row-scope miss answers.
func TestAMemberOfAnotherWorkspaceCannotBeActedFor(t *testing.T) {
	e := setupIngress(t)
	rt := e.ingestingRuntime()

	_, err := rt.Ingest(context.Background(), extension.UserID(ids.NewV7().String()),
		aProviderRecord("ws-7:5001", "outside@example.test"))
	if err == nil {
		t.Fatal("an ingest ran as somebody who is not a member of this workspace")
	}
}

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
