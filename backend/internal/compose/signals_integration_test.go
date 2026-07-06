// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// The warm-room signal spine under real Postgres (B-E08.1–.4, features/07
// §9, data-model §12.5). The invariants proven here are the ones the epic
// encodes rather than promises: a signal's row scope follows its subject
// record (existence-hiding across owners); the resolver attributes at
// COMPANY level, links a person only under a recorded consent grant, never
// creates a person row, and drops what it cannot attribute; the warm/cold
// join answers with evidence over our own contact graph; the intro path is
// a proposal that mutates nothing; and every mutation writes the audit +
// outbox pair in one transaction.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"

	"errors"
)

// signalStrengthAdapter bridges people's §4 strength to the signals seam,
// exactly as compose.signalStrength does in production — the module never
// imports its sibling.
type signalStrengthAdapter struct{ people *people.Store }

func (a signalStrengthAdapter) PersonStrength(ctx context.Context, id ids.UUID, now time.Time) (signals.RelationshipStrength, error) {
	rs, err := a.people.PersonStrength(ctx, id, now)
	if err != nil {
		return signals.RelationshipStrength{}, err
	}
	return signals.RelationshipStrength{Strength: rs.Strength, Bucket: rs.Bucket}, nil
}

func signalStore(e *searchEnv) *signals.Store {
	return signals.NewStore(e.pool, signalStrengthAdapter{people: people.NewStore(e.pool)})
}

// signalActor is a full-scope human over the entities the warm room reads
// and writes; scope selects own/team/all row visibility.
func signalActor(e *searchEnv, user ids.UUID, scope principal.RowScope, teams []ids.UUID) context.Context {
	grants := map[string]principal.ObjectGrant{}
	for _, o := range []string{"signal", "person", "organization", "deal", "lead"} {
		grants[o] = principal.ObjectGrant{Read: true, Create: true, Update: true, Delete: true}
	}
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	// The HTTP layer mints one correlation id per request; the store's Emit
	// needs it in scope to link the audit row and outbox envelope into one
	// trace, so a direct store call in a test must bind it too.
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs:     teams,
		Permissions: principal.Permissions{Objects: grants, RowScope: scope},
	})
}

func (e *searchEnv) adminSignals() context.Context {
	return signalActor(e, ids.NewV7(), principal.RowScopeAll, nil)
}

func personCount(t *testing.T, e *searchEnv) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM person WHERE workspace_id = $1`, e.ws).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// seedOrgWithDomain plants an organization (owned by rep1) and a
// registered domain the resolver's domain index can match.
func (e *searchEnv) seedOrgWithDomain(t *testing.T, name, domain string) ids.UUID {
	t.Helper()
	orgID := e.seed(t,
		`INSERT INTO organization (id, workspace_id, display_name, owner_id, source, captured_by)
		 VALUES ($1, $2, $3, $4, 'manual', 'human:x')`, name, e.rep1)
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO organization_domain (id, workspace_id, organization_id, domain, source, captured_by)
		 VALUES ($1, $2, $3, $4, 'manual', 'human:x')`, ids.NewV7(), e.ws, orgID, domain); err != nil {
		t.Fatal(err)
	}
	return orgID
}

// seedEmployedContact plants a person (owned by rep1) with a work email
// and a current employment edge at the org — the shape the prior-interaction
// match and the warm/cold join both read.
func (e *searchEnv) seedEmployedContact(t *testing.T, orgID ids.UUID, name, email string) ids.UUID {
	t.Helper()
	personID := e.seed(t,
		`INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by)
		 VALUES ($1, $2, $3, $4, 'manual', 'human:x')`, name, e.rep1)
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO person_email (id, workspace_id, person_id, email, is_primary, source, captured_by)
		 VALUES ($1, $2, $3, $4, true, 'manual', 'human:x')`, ids.NewV7(), e.ws, personID, email); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO relationship (id, workspace_id, kind, person_id, organization_id, source, captured_by)
		 VALUES ($1, $2, 'employment', $3, $4, 'manual', 'human:x')`,
		ids.NewV7(), e.ws, personID, orgID); err != nil {
		t.Fatal(err)
	}
	return personID
}

// grantConsent records a granted consent for the person, so the resolver's
// consent gate opens for the person link.
func (e *searchEnv) grantConsent(t *testing.T, personID ids.UUID) {
	t.Helper()
	purposeID := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO consent_purpose (id, workspace_id, key, label) VALUES ($1, $2, 'outreach', 'Outreach')`,
		purposeID, e.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO person_consent (id, workspace_id, person_id, purpose_id, state, source)
		 VALUES ($1, $2, $3, $4, 'granted', 'manual')`, ids.NewV7(), e.ws, personID, purposeID); err != nil {
		t.Fatal(err)
	}
}

func createRaw(t *testing.T, store *signals.Store, ctx context.Context, rawRef string) ids.UUID {
	t.Helper()
	sig, err := store.CreateSignal(ctx, signals.CreateSignalInput{
		Kind: "buying_intent", SourceChannel: "inbound", RawRef: &rawRef,
		Summary: "inbound interest", Source: "connector:imap:msg-1",
	})
	if err != nil {
		t.Fatalf("create raw signal: %v", err)
	}
	return ids.UUID(sig.Id)
}

// A signal's visibility follows the record it is ABOUT: a signal on a
// person another team owns does not exist for a team-scoped rep (404,
// existence-hiding), while the workspace admin sees it.
func TestSignalRowScopeFollowsSubjectEntity(t *testing.T) {
	e := setupSearch(t)
	store := signalStore(e)

	foreignPerson := e.seed(t,
		`INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by)
		 VALUES ($1, $2, 'Foreign Contact', $3, 'manual', 'human:x')`, e.rep3)
	personType := "person"
	pid := ids.UUID(foreignPerson)
	sig, err := store.CreateSignal(e.adminSignals(), signals.CreateSignalInput{
		Kind: "risk", EntityType: &personType, EntityID: &pid,
		Summary: "subject-bound signal", Source: "derived",
	})
	if err != nil {
		t.Fatalf("admin create: %v", err)
	}

	rep := signalActor(e, e.rep1, principal.RowScopeTeam, []ids.UUID{e.team1})
	if _, err := store.GetSignal(rep, ids.UUID(sig.Id), 0); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("team1 rep read of a team2-subject signal = %v, want ErrNotFound (existence-hiding)", err)
	}
	if _, err := store.GetSignal(e.adminSignals(), ids.UUID(sig.Id), 0); err != nil {
		t.Fatalf("admin read of the same signal = %v, want it visible", err)
	}
}

// The resolver may attribute only to an organization the caller can see:
// a team-scoped rep resolving a signal whose only domain match is an org
// another team owns gets an unattributable drop, not a stamped
// resolved_org_id that would leak the foreign org's id/existence.
func TestResolverDoesNotAttributeToAnInvisibleOrg(t *testing.T) {
	e := setupSearch(t)
	store := signalStore(e)

	// An org (with a matching domain) owned by rep3 — outside team1's scope.
	foreignOrg := e.seed(t,
		`INSERT INTO organization (id, workspace_id, display_name, owner_id, source, captured_by)
		 VALUES ($1, $2, 'Foreign Co', $3, 'manual', 'human:x')`, e.rep3)
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO organization_domain (id, workspace_id, organization_id, domain, source, captured_by)
		 VALUES ($1, $2, $3, 'foreign.example', 'manual', 'human:x')`, ids.NewV7(), e.ws, ids.UUID(foreignOrg)); err != nil {
		t.Fatal(err)
	}

	// The raw signal carries no subject entity, so a team-scoped rep can
	// see and resolve it — the gate must bite on the ATTRIBUTION, not the read.
	admin := e.adminSignals()
	sigID := createRaw(t, store, admin, "inbound:hi@foreign.example")

	rep := signalActor(e, e.rep1, principal.RowScopeTeam, []ids.UUID{e.team1})
	resolved, err := store.Resolve(rep, sigID)
	if err != nil {
		t.Fatalf("resolve by team-scoped rep: %v", err)
	}
	if string(resolved.ResolutionState) != "dropped" {
		t.Fatalf("resolution_state = %q, want dropped (the only match is invisible)", resolved.ResolutionState)
	}
	if resolved.ResolvedOrgId != nil {
		t.Fatalf("resolved_org_id = %v, want nil — an invisible org must never be stamped", resolved.ResolvedOrgId)
	}

	// The admin, who CAN see the org, resolves the same class of signal to it.
	adminSig := createRaw(t, store, admin, "inbound:hi@foreign.example")
	adminResolved, err := store.Resolve(admin, adminSig)
	if err != nil {
		t.Fatalf("admin resolve: %v", err)
	}
	if adminResolved.ResolvedOrgId == nil || ids.UUID(*adminResolved.ResolvedOrgId) != ids.UUID(foreignOrg) {
		t.Fatalf("admin resolved_org_id = %v, want %v", adminResolved.ResolvedOrgId, ids.UUID(foreignOrg))
	}
}

// Domain match with no known contact: the signal resolves to the
// organization and stays company-level — no person link, and no person
// row is invented.
func TestResolverAttributesToOrgWithoutCreatingAPerson(t *testing.T) {
	e := setupSearch(t)
	store := signalStore(e)
	orgID := e.seedOrgWithDomain(t, "Acme", "acme.example")

	before := personCount(t, e)
	admin := e.adminSignals()
	sigID := createRaw(t, store, admin, "inbound:hello@acme.example")

	resolved, err := store.Resolve(admin, sigID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(resolved.ResolutionState) != "resolved" {
		t.Fatalf("resolution_state = %q, want resolved", resolved.ResolutionState)
	}
	if resolved.ResolvedOrgId == nil || ids.UUID(*resolved.ResolvedOrgId) != orgID {
		t.Fatalf("resolved_org_id = %v, want %v", resolved.ResolvedOrgId, orgID)
	}
	if resolved.ResolvedPersonId != nil {
		t.Fatalf("resolved_person_id = %v, want nil (no consented contact)", resolved.ResolvedPersonId)
	}
	if after := personCount(t, e); after != before {
		t.Fatalf("person count %d → %d — the resolver must NEVER create a person", before, after)
	}
}

// A person link is set only where the match holds AND a consent grant is
// on record; a matching contact WITHOUT consent stays company-level.
func TestResolverPersonLinkIsConsentGated(t *testing.T) {
	e := setupSearch(t)
	store := signalStore(e)
	admin := e.adminSignals()

	// Consented contact → linked.
	orgA := e.seedOrgWithDomain(t, "Consenting Co", "consent.example")
	contact := e.seedEmployedContact(t, orgA, "Sam Consent", "sam@consent.example")
	e.grantConsent(t, contact)
	withConsent := createRaw(t, store, admin, "inbound:sam@consent.example")
	got, err := store.Resolve(admin, withConsent)
	if err != nil {
		t.Fatalf("resolve consented: %v", err)
	}
	if got.ResolvedPersonId == nil || ids.UUID(*got.ResolvedPersonId) != contact {
		t.Fatalf("resolved_person_id = %v, want %v (consent on record)", got.ResolvedPersonId, contact)
	}

	// Matching contact, no consent → org only.
	orgB := e.seedOrgWithDomain(t, "Silent Co", "silent.example")
	e.seedEmployedContact(t, orgB, "Pat Silent", "pat@silent.example")
	noConsent := createRaw(t, store, admin, "inbound:pat@silent.example")
	got, err = store.Resolve(admin, noConsent)
	if err != nil {
		t.Fatalf("resolve unconsented: %v", err)
	}
	if got.ResolvedOrgId == nil || ids.UUID(*got.ResolvedOrgId) != orgB {
		t.Fatalf("resolved_org_id = %v, want %v", got.ResolvedOrgId, orgB)
	}
	if got.ResolvedPersonId != nil {
		t.Fatalf("resolved_person_id = %v, want nil (no consent grant)", got.ResolvedPersonId)
	}
}

// An unattributable raw_ref is dropped with the reason on record — never
// kept as a person-level dossier, never linked to anyone.
func TestResolverDropsTheUnattributable(t *testing.T) {
	e := setupSearch(t)
	store := signalStore(e)
	admin := e.adminSignals()

	before := personCount(t, e)
	sigID := createRaw(t, store, admin, "inbound:nobody@nowhere.invalid")
	got, err := store.Resolve(admin, sigID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(got.ResolutionState) != "dropped" {
		t.Fatalf("resolution_state = %q, want dropped", got.ResolutionState)
	}
	if got.ResolvedOrgId != nil || got.ResolvedPersonId != nil {
		t.Fatalf("dropped signal carries org=%v person=%v, want both nil", got.ResolvedOrgId, got.ResolvedPersonId)
	}
	if after := personCount(t, e); after != before {
		t.Fatalf("person count changed on a dropped signal (%d → %d)", before, after)
	}
}

// The warm/cold branch classifies by our own contact graph: an org where
// we hold a live contact is warm (routes to the warm room) and answers
// with the contact evidence; an org with no contact is cold.
func TestWarmthClassifiesByOwnContactGraph(t *testing.T) {
	e := setupSearch(t)
	store := signalStore(e)
	admin := e.adminSignals()

	warmOrg := e.seedOrgWithDomain(t, "Warm Co", "warm.example")
	contact := e.seedEmployedContact(t, warmOrg, "Wanda Warm", "wanda@warm.example")
	warmSig := createRaw(t, store, admin, "warm.example")
	if _, err := store.Resolve(admin, warmSig); err != nil {
		t.Fatalf("resolve warm: %v", err)
	}
	warmth, err := store.Warmth(admin, warmSig, time.Now().UTC())
	if err != nil {
		t.Fatalf("warmth: %v", err)
	}
	if !warmth.Warm || string(warmth.Routing) != "warm_room" {
		t.Fatalf("warm=%v routing=%q, want warm/warm_room", warmth.Warm, warmth.Routing)
	}
	if len(warmth.ContactIds) != 1 || ids.UUID(warmth.ContactIds[0]) != contact {
		t.Fatalf("contact evidence = %v, want [%v]", warmth.ContactIds, contact)
	}

	// Seed for its side effect only: the org must exist so cold.example
	// resolves to it, but the test asserts on the resolution, not the org.
	e.seedOrgWithDomain(t, "Cold Co", "cold.example")
	coldSig := createRaw(t, store, admin, "cold.example")
	if _, err := store.Resolve(admin, coldSig); err != nil {
		t.Fatalf("resolve cold: %v", err)
	}
	cold, err := store.Warmth(admin, coldSig, time.Now().UTC())
	if err != nil {
		t.Fatalf("cold warmth: %v", err)
	}
	if cold.Warm || string(cold.Routing) != "cold_queue" {
		t.Fatalf("warm=%v routing=%q, want cold/cold_queue", cold.Warm, cold.Routing)
	}
}

// The intro path is a proposal: it names the route-in contact and drafts a
// message carrying the Art. 50 disclosure, and it mutates nothing (the
// signal's version does not move).
func TestIntroPathProposesWithoutMutating(t *testing.T) {
	e := setupSearch(t)
	store := signalStore(e)
	admin := e.adminSignals()

	org := e.seedOrgWithDomain(t, "Intro Co", "intro.example")
	contact := e.seedEmployedContact(t, org, "Ivy Intro", "ivy@intro.example")
	sigID := createRaw(t, store, admin, "intro.example")
	if _, err := store.Resolve(admin, sigID); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var versionBefore int64
	if err := e.owner.QueryRow(context.Background(),
		`SELECT version FROM signal WHERE id = $1`, sigID).Scan(&versionBefore); err != nil {
		t.Fatal(err)
	}

	path, err := store.IntroPath(admin, sigID, time.Now().UTC())
	if err != nil {
		t.Fatalf("intro path: %v", err)
	}
	if ids.UUID(path.ContactId) != contact {
		t.Fatalf("intro contact = %v, want %v", path.ContactId, contact)
	}
	if !strings.Contains(path.NextMove.DraftBody, "Art. 50") {
		t.Fatalf("draft body missing the Art. 50 disclosure: %q", path.NextMove.DraftBody)
	}

	var versionAfter int64
	if err := e.owner.QueryRow(context.Background(),
		`SELECT version FROM signal WHERE id = $1`, sigID).Scan(&versionAfter); err != nil {
		t.Fatal(err)
	}
	if versionAfter != versionBefore {
		t.Fatalf("signal version moved %d → %d — intro path must mutate nothing", versionBefore, versionAfter)
	}
}

// Every mutation commits the audit + outbox pair in one transaction: a
// create emits signal.detected, a resolve emits signal.resolved, each with
// its audit row.
func TestSignalMutationsWriteTheAuditOutboxPair(t *testing.T) {
	e := setupSearch(t)
	store := signalStore(e)
	admin := e.adminSignals()
	e.seedOrgWithDomain(t, "Audit Co", "audit.example")

	sigID := createRaw(t, store, admin, "audit.example")
	assertAuditAndOutbox(t, e, sigID, "create", "signal.detected")

	if _, err := store.Resolve(admin, sigID); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assertAuditAndOutbox(t, e, sigID, "resolve", "signal.resolved")
}

func assertAuditAndOutbox(t *testing.T, e *searchEnv, sigID ids.UUID, action, eventType string) {
	t.Helper()
	var audits int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE entity_type = 'signal' AND entity_id = $1 AND action = $2`,
		sigID, action).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("audit rows for %s = %d, want 1", action, audits)
	}
	var events int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = $1 AND envelope->'entity'->>'id' = $2::text`,
		eventType, sigID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("outbox rows for %s = %d, want 1", eventType, events)
	}
}
