// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

// Table-level unit tests for the module's pure transforms and guards — the
// wire mappings (including the nullable-pointer branches), the event-type
// validation, the backoff schedule, the key decode, and the owner
// resolution — none of which need a database. The store/delivery/HTTP
// behaviour is proven end-to-end in the compose integration suite.

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestAttemptRefusesWithoutAUsableSecret(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	clock := func() time.Time { return time.Unix(0, 0).UTC() }

	// No signing key configured → refuse before any dial, never an unsigned POST.
	d := NewDeliverer(NewStore(nil, nil), nil, clock, nil, log)
	if out := d.attempt(context.Background(), attemptTarget{}); out.failure == "" {
		t.Fatal("attempt with no cipher must fail, not dial")
	}

	// Cipher present but the stored secret cannot be unsealed → refuse.
	cipher, err := NewCipher(make([]byte, WebhookKeyBytes))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	d2 := NewDeliverer(NewStore(nil, cipher), nil, clock, nil, log)
	if out := d2.attempt(context.Background(), attemptTarget{sealedSecret: "not-a-valid-sealed-secret"}); out.failure == "" {
		t.Fatal("attempt with an unsealable secret must fail, not dial")
	}
}

func TestWireSubscriptionMapsEveryFieldAndHidesNoSecret(t *testing.T) {
	archived := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	s := Subscription{
		ID:          ids.NewV7(),
		WorkspaceID: ids.NewV7(),
		OwnerID:     ids.NewV7(),
		TargetURL:   "https://ok.example/hook",
		EventTypes:  []string{"deal.created"},
		State:       "active",
		Version:     3,
		ArchivedAt:  &archived,
	}
	got := wireSubscription(s)
	if got.TargetUrl != s.TargetURL || string(got.State) != s.State || got.Version != s.Version {
		t.Fatalf("scalar fields not mapped: %+v", got)
	}
	if ids.UUID(got.Id) != s.ID || ids.UUID(got.OwnerId) != s.OwnerID || ids.UUID(got.WorkspaceId) != s.WorkspaceID {
		t.Fatal("id fields not mapped")
	}
	if got.ArchivedAt == nil || !got.ArchivedAt.Equal(archived) {
		t.Fatalf("archived_at not carried: %v", got.ArchivedAt)
	}

	// A live subscription carries a nil archived_at (the other branch).
	s.ArchivedAt = nil
	if wireSubscription(s).ArchivedAt != nil {
		t.Fatal("a live subscription must map archived_at to nil")
	}

	// wireCreated carries the one-time secret alongside the subscription.
	created := wireCreated(s, "whsec_once")
	if created.SigningSecret != "whsec_once" || created.Subscription.TargetUrl != s.TargetURL {
		t.Fatalf("wireCreated wrong: %+v", created)
	}
}

func TestWireDeliveryMapsNullableAndSetFields(t *testing.T) {
	// All nullable fields unset — the nil branch of every pointer.
	bare := wireDelivery(Delivery{
		ID: ids.NewV7(), SubscriptionID: ids.NewV7(), EventID: ids.NewV7(),
		EventType: "deal.created", Status: "pending", Attempts: 0,
	})
	if bare.LastStatusCode != nil || bare.LastError != nil || bare.NextRetryAt != nil ||
		bare.DeliveredAt != nil || bare.DeadLetteredAt != nil {
		t.Fatal("unset nullable fields must map to nil")
	}
	if string(bare.Status) != "pending" || bare.Attempts != 0 {
		t.Fatalf("scalar fields wrong: %+v", bare)
	}

	// All nullable fields set — the non-nil branch.
	code := 503
	msg := "endpoint returned 503"
	when := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	full := wireDelivery(Delivery{
		ID: ids.NewV7(), SubscriptionID: ids.NewV7(), EventID: ids.NewV7(),
		EventType: "deal.created", Status: "dead_lettered", Attempts: 6,
		LastStatusCode: &code, LastError: &msg,
		NextRetryAt: &when, DeliveredAt: &when, DeadLetteredAt: &when,
	})
	if full.LastStatusCode == nil || *full.LastStatusCode != code || full.LastError == nil || *full.LastError != msg {
		t.Fatalf("set nullable fields not carried: %+v", full)
	}
	if full.DeadLetteredAt == nil || !full.DeadLetteredAt.Equal(when) {
		t.Fatal("dead_lettered_at not carried")
	}
}

func TestValidateEventTypes(t *testing.T) {
	tests := []struct {
		name    string
		types   []string
		wantErr bool
	}{
		{"empty", nil, true},
		{"unknown", []string{"nonsense.happened"}, true},
		{"pipeline entity-less", []string{"capture.received"}, true},
		{"one valid", []string{"deal.created"}, false},
		{"valid then pipeline", []string{"deal.created", "capture.skipped"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEventTypes(tc.types)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateEventTypes(%v) err=%v, wantErr=%v", tc.types, err, tc.wantErr)
			}
			if err != nil {
				var bad *BadInputError
				if !errors.As(err, &bad) || bad.Field != fieldEventTypes {
					t.Fatalf("want a BadInputError on %q, got %v", fieldEventTypes, err)
				}
			}
		})
	}
}

func TestBadInputErrorMessage(t *testing.T) {
	e := &BadInputError{Field: "target_url", Reason: "must be an https:// URL"}
	if got, want := e.Error(), "target_url: must be an https:// URL"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestBackoffScheduleAndCap(t *testing.T) {
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, backoffCap},  // 32s — the stated ceiling, reached exactly
		{7, backoffCap},  // beyond the budget: capped, never a negative shift
		{99, backoffCap}, // extreme shift overflows to <=0 → still the cap
	}
	for _, tc := range tests {
		if got := backoff(tc.attempts); got != tc.want {
			t.Errorf("backoff(%d) = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}

func TestDecodeKeyAndCipherKeyLength(t *testing.T) {
	// A valid base64 string decodes to its bytes.
	raw := make([]byte, WebhookKeyBytes)
	encoded := base64.StdEncoding.EncodeToString(raw)
	got, err := DecodeKey(encoded)
	if err != nil || len(got) != WebhookKeyBytes {
		t.Fatalf("DecodeKey(valid) = %d bytes, err=%v", len(got), err)
	}
	// Non-base64 is a decode error, not a panic.
	if _, err := DecodeKey("not base64!!!"); err == nil {
		t.Fatal("DecodeKey must reject non-base64")
	}
	// The cipher enforces the 32-byte key length (AES-256).
	if _, err := NewCipher(make([]byte, 16)); err == nil {
		t.Fatal("NewCipher must reject a key that is not 32 bytes")
	}
	if _, err := NewCipher(raw); err != nil {
		t.Fatalf("NewCipher(32 bytes) unexpected error: %v", err)
	}
}

func TestGenerateSecretIsPrefixedAndFresh(t *testing.T) {
	a, err := generateSecret()
	if err != nil {
		t.Fatalf("generateSecret: %v", err)
	}
	b, err := generateSecret()
	if err != nil {
		t.Fatalf("generateSecret: %v", err)
	}
	if len(a) <= len(secretPrefix) || a[:len(secretPrefix)] != secretPrefix {
		t.Fatalf("secret %q missing %q prefix", a, secretPrefix)
	}
	if a == b {
		t.Fatal("two generated secrets must differ")
	}
}

func TestGuardedClientCapsRedirects(t *testing.T) {
	c := NewGuardedClient()
	if c.CheckRedirect == nil {
		t.Fatal("the guarded client must set CheckRedirect")
	}
	if err := c.CheckRedirect(nil, make([]*http.Request, maxRedirects-1)); err != nil {
		t.Fatalf("a chain within the limit must be allowed: %v", err)
	}
	if err := c.CheckRedirect(nil, make([]*http.Request, maxRedirects)); err == nil {
		t.Fatal("a chain at the redirect limit must be refused")
	}
}

func TestOwnerCanSeeEarlyReturns(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := NewDeliverer(NewStore(nil, nil), nil, nil, nil, log) // nil resolver

	// An entity-less envelope names no subject to scope by → not visible.
	entityless := kevents.Envelope{WorkspaceID: ids.NewV7()}
	if ok, err := d.ownerCanSee(context.Background(), entityless, ids.NewV7()); ok || err != nil {
		t.Fatalf("entity-less ownerCanSee = (%v, %v), want (false, nil)", ok, err)
	}

	// An entity present but no resolver configured → a loud error, never a
	// silent allow.
	withEntity := kevents.Envelope{WorkspaceID: ids.NewV7(), Entity: kevents.EntityRef{Type: "deal", ID: ids.NewV7()}}
	if ok, err := d.ownerCanSee(context.Background(), withEntity, ids.NewV7()); ok || err == nil {
		t.Fatalf("nil-resolver ownerCanSee = (%v, %v), want (false, err)", ok, err)
	}
}

// TestEntityVisibleToClassification pins the fan-out visibility classifier
// for the branches that resolve WITHOUT touching the pool: the event-keyed
// deferral (mirror.*), the workspace-level allow-list, the entity-keyed
// deferral (retention telemetry), and the fail-closed default. A nil pool
// is deliberate — any case that reached a row-scope probe would panic, so
// this also proves the event-first ordering short-circuits before the
// object_class collision could route a mirror.* subject into a probe.
func TestEntityVisibleToClassification(t *testing.T) {
	s := NewStore(nil, nil)
	for _, tc := range []struct {
		name       string
		eventType  string
		entityType string
		want       bool
	}{
		// mirror.* is deferred by EVENT even when its runtime object_class
		// collides with a row-scoped entity name — caught before any probe.
		{"mirror.conflict over deal object_class", "mirror.conflict", "deal", false},
		{"mirror.budget_degraded over person object_class", "mirror.budget_degraded", "person", false},
		{"mirror.deleted over organization object_class", "mirror.deleted", "organization", false},
		{"mirror.write_rejected over lead object_class", "mirror.write_rejected", "lead", false},
		// retention telemetry subjects are deferred by ENTITY.
		{"retention.applied over ai_call", "retention.applied", "ai_call", false},
		{"retention.applied over ai_call_payload", "retention.applied", "ai_call_payload", false},
		// Workspace-level subjects deliver to any live owner. The runtime
		// entity strings are the emit sites' — not the dotted event prefix.
		{"user (user.* / role.changed)", "role.changed", "user", true},
		{"passport", "passport.revoked", "passport", true},
		{"onboarding wizard state", "onboarding.state_changed", "onboarding_wizard_state", true},
		{"incumbent connection", "incumbent.connected", "incumbent_connection", true},
		{"audit ledger", "audit.appended", "audit", true},
		{"pipeline config", "pipeline.created", "pipeline", true},
		{"stage config", "stage.updated", "stage", true},
		// approval.*/coldstart.* (entity "approval") is intentionally absent
		// here: it is target-visibility gated (approvalVisibleTo) and needs
		// the pool, so it is proven in the integration lane, not this
		// DB-free classification test.
		// An unclassified subject is fail-closed — never delivered.
		{"unclassified subject", "made.up", "widget", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.entityVisibleTo(context.Background(), tc.eventType, tc.entityType, ids.NewV7())
			if err != nil {
				t.Fatalf("entityVisibleTo(%q, %q) errored: %v", tc.eventType, tc.entityType, err)
			}
			if got != tc.want {
				t.Fatalf("entityVisibleTo(%q, %q) = %v, want %v", tc.eventType, tc.entityType, got, tc.want)
			}
		})
	}
}

// TestRowScopedSubjectsRouteToProbes guards the note that consent.changed
// (person|lead) and retention.applied (person|lead|deal|activity) must
// still hit their row-scope probes: their runtime entity type is NEITHER
// deferred NOR workspace-level, so entityVisibleTo falls through to the
// probe switch rather than short-circuiting. Asserting the map memberships
// (not the DB probe itself — that is the integration lane's job) keeps this
// a pure, DB-free guard against a future edit that would silently reclassify
// a row-scoped subject into fan-out-to-everyone or a blanket deferral.
func TestRowScopedSubjectsRouteToProbes(t *testing.T) {
	if _, deferred := deferredDeliveryEvents["consent.changed"]; deferred {
		t.Error("consent.changed must NOT be event-deferred — its person/lead subject is row-scope probed")
	}
	if _, deferred := deferredDeliveryEvents["retention.applied"]; deferred {
		t.Error("retention.applied must NOT be event-deferred — its person/lead/deal/activity subjects are row-scope probed")
	}
	for _, entity := range []string{"person", "organization", "deal", "lead", "activity", "voice_profile", "signal", "offer", "approval"} {
		if _, ws := workspaceLevelEntities[entity]; ws {
			t.Errorf("row-scoped subject %q must not be in workspaceLevelEntities (would fan out to everyone)", entity)
		}
		if _, def := deferredDeliveryEntities[entity]; def {
			t.Errorf("row-scoped subject %q must not be in deferredDeliveryEntities (would never deliver)", entity)
		}
	}
}

// TestRowScopedSubjectRequiresObjectReadCapability pins the P0 gate: a
// fan-out owner whose LIVE role no longer grants <entity>.read must NOT
// receive a row-scoped payload, even if a lingering row scope would still
// match. entityVisibleTo mirrors the read path (auth.Require AND
// auth.EnsureVisible), so an owner lacking the read grant is denied BEFORE
// any pool probe — the nil pool here proves the short-circuit: were the
// object-read half removed, RowScopeAll would drive EnsureVisible into the
// nil pool and panic.
func TestRowScopedSubjectRequiresObjectReadCapability(t *testing.T) {
	s := NewStore(nil, nil) // nil pool: reaching a row-scope probe would panic

	noRead := principal.Principal{
		Type:   principal.PrincipalHuman,
		UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			// deal.update but explicitly NOT deal.read; row_scope=all would
			// otherwise make every deal row visible.
			Objects:  map[string]principal.ObjectGrant{"deal": {Update: true}},
			RowScope: principal.RowScopeAll,
		},
	}
	ctx := principal.WithActor(context.Background(), noRead)
	if ok, err := s.entityVisibleTo(ctx, "deal.updated", "deal", ids.NewV7()); ok || err != nil {
		t.Fatalf("entityVisibleTo without deal.read = (%v, %v), want (false, nil)", ok, err)
	}

	// The same owner WITH deal.read passes the object-read half and proceeds
	// to the row-scope probe, which on a nil pool fails loudly (a panic or an
	// error) — anything but the clean (false, nil) deny above. This proves the
	// earlier denial came from the missing read grant, not from some other
	// short-circuit that would also block a properly-granted owner.
	withRead := noRead
	withRead.Permissions.Objects = map[string]principal.ObjectGrant{"deal": {Read: true}}
	func() {
		//craft:ignore swallowed-errors recover's value is deliberately discarded: a nil-pool probe panic is itself proof the read gate admitted the call and reached the probe
		defer func() { _ = recover() }()
		ctx := principal.WithActor(context.Background(), withRead)
		if ok, err := s.entityVisibleTo(ctx, "deal.updated", "deal", ids.NewV7()); !ok && err == nil {
			t.Fatal("with deal.read, entityVisibleTo returned a clean deny — the read gate must have admitted it and reached the row-scope probe")
		}
	}()
}

// TestApprovalTargetRequiresObjectReadCapability pins the same P0 invariant on
// the sibling approval-target path: an approval's envelope discloses the
// target's details, so a fan-out owner whose LIVE role no longer grants
// <target>.read must NOT receive it, even when a lingering row scope would
// still match the target. approvalTargetVisible now mirrors entityVisibleTo
// (object-read AND row-scope), so the missing read grant denies BEFORE any pool
// probe — the nil pool proves the short-circuit: were the object-read half
// dropped, RowScopeAll would drive the row-scope probe into the nil pool and
// panic (the exact leak cubic flagged via the target path).
func TestApprovalTargetRequiresObjectReadCapability(t *testing.T) {
	s := NewStore(nil, nil) // nil pool: reaching a row-scope probe would panic

	noRead := principal.Principal{
		Type:   principal.PrincipalHuman,
		UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			// deal.update but explicitly NOT deal.read; row_scope=all would
			// otherwise make every deal target visible.
			Objects:  map[string]principal.ObjectGrant{"deal": {Update: true}},
			RowScope: principal.RowScopeAll,
		},
	}
	ctx := principal.WithActor(context.Background(), noRead)
	if ok, err := s.approvalTargetVisible(ctx, "deal", ids.NewV7()); ok || err != nil {
		t.Fatalf("approvalTargetVisible without deal.read = (%v, %v), want (false, nil)", ok, err)
	}

	// The same owner WITH deal.read passes the object-read half and proceeds to
	// the row-scope probe, which on a nil pool fails loudly — anything but the
	// clean (false, nil) deny above — proving the earlier denial came from the
	// missing read grant, not some other short-circuit.
	withRead := noRead
	withRead.Permissions.Objects = map[string]principal.ObjectGrant{"deal": {Read: true}}
	func() {
		//craft:ignore swallowed-errors recover's value is deliberately discarded: a nil-pool probe panic is itself proof the read gate admitted the call and reached the probe
		defer func() { _ = recover() }()
		ctx := principal.WithActor(context.Background(), withRead)
		if ok, err := s.approvalTargetVisible(ctx, "deal", ids.NewV7()); !ok && err == nil {
			t.Fatal("with deal.read, approvalTargetVisible returned a clean deny — the read gate must have admitted it and reached the row-scope probe")
		}
	}()
}

func TestOwnerResolvesHumanBehindTheCall(t *testing.T) {
	user := ids.NewV7()
	onBehalf := ids.NewV7()

	// A human call is owned by the acting user.
	ctx := principal.WithActor(context.Background(), principal.Principal{Type: principal.PrincipalHuman, UserID: user})
	if got, err := owner(ctx); err != nil || got != user {
		t.Fatalf("owner(human) = %v, err=%v, want %v", got, err, user)
	}

	// An agent call is owned by the human it acts on behalf of.
	ctx = principal.WithActor(context.Background(), principal.Principal{Type: principal.PrincipalAgent, OnBehalfOf: onBehalf})
	if got, err := owner(ctx); err != nil || got != onBehalf {
		t.Fatalf("owner(agent) = %v, err=%v, want %v", got, err, onBehalf)
	}

	// A principal with no human identity cannot own integration config.
	ctx = principal.WithActor(context.Background(), principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
	if _, err := owner(ctx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("owner(system) err = %v, want ErrPermissionDenied", err)
	}
}

func TestWriteErrMapsTypedFaults(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"bad input → 422", &BadInputError{Field: "target_url", Reason: "must be https"}, http.StatusUnprocessableEntity},
		{"not configured → 503", ErrNotConfigured, http.StatusServiceUnavailable},
		{"unknown → 500", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/webhook-subscriptions", nil)
			writeErr(rec, req, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("writeErr(%v) → %d, want %d", tc.err, rec.Code, tc.status)
			}
		})
	}
}
