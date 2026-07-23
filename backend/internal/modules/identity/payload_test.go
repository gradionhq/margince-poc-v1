// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// TDD Step 1 of the webhooks Task 5g migration (identity family): drives
// the payload-builder functions the package's six emit sites call —
// userInvitedPayload (users.go's InviteUser), userDeactivatedPayload
// (users.go's DeactivateUser), userReactivatedPayload (users.go's
// ReactivateUser), roleChangedPayload (users.go's ChangeUserRole),
// passportRevokedPayload (passport.go's RevokePassport), and
// onboardingStateChangedPayload (onboarding.go's auditOnboardingState) —
// then round-trips each result through JSON exactly as storekit.EmitEvent
// marshals it into the outbox envelope's payload column, mirroring the
// ai voice family's TestVoice*Payload (webhooks Task 5f).
//
// Before this migration none of crmcontracts.PublicEventUserInvited/
// UserDeactivated/UserReactivated/RoleChanged/PassportRevoked/
// OnboardingStateChanged existed, and none of the builder functions
// existed (every site inlined a map[string]any), so this test failed to
// compile (RED) until public-events.yaml gained the schemas, `make gen`
// regenerated the structs, and users.go/passport.go/onboarding.go grew
// the builders.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var (
	payloadTestUserID     = ids.From[ids.UserKind](ids.MustParse("11111111-1111-1111-1111-111111111111"))
	payloadTestActorID    = ids.From[ids.UserKind](ids.MustParse("22222222-2222-2222-2222-222222222222"))
	payloadTestPassportID = ids.From[ids.PassportKind](ids.MustParse("33333333-3333-3333-3333-333333333333"))
)

func TestUserInvitedPayload(t *testing.T) {
	payload := userInvitedPayload(payloadTestUserID, "manager", payloadTestActorID)

	if !reflect.DeepEqual(payload.EventType(), "user.invited") {
		t.Errorf("got %v, want %v", payload.EventType(), "user.invited")
	}
	if !reflect.DeepEqual(payload.EntityType(), "user") {
		t.Errorf("got %v, want %v", payload.EntityType(), "user")
	}
	if !reflect.DeepEqual(payload.UserId, openapi_types.UUID(payloadTestUserID.UUID)) {
		t.Errorf("got %v, want %v", payload.UserId, openapi_types.UUID(payloadTestUserID.UUID))
	}
	if !reflect.DeepEqual(payload.Role, "manager") {
		t.Errorf("got %v, want %v", payload.Role, "manager")
	}
	if !reflect.DeepEqual(payload.By, openapi_types.UUID(payloadTestActorID.UUID)) {
		t.Errorf("got %v, want %v", payload.By, openapi_types.UUID(payloadTestActorID.UUID))
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventUserInvited
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestUserDeactivatedPayload_WithReason(t *testing.T) {
	reason := "policy violation"
	payload := userDeactivatedPayload(payloadTestUserID, payloadTestActorID, &reason)

	if !reflect.DeepEqual(payload.EventType(), "user.deactivated") {
		t.Errorf("got %v, want %v", payload.EventType(), "user.deactivated")
	}
	if !reflect.DeepEqual(payload.EntityType(), "user") {
		t.Errorf("got %v, want %v", payload.EntityType(), "user")
	}
	if !reflect.DeepEqual(payload.UserId, openapi_types.UUID(payloadTestUserID.UUID)) {
		t.Errorf("got %v, want %v", payload.UserId, openapi_types.UUID(payloadTestUserID.UUID))
	}
	if !reflect.DeepEqual(payload.By, openapi_types.UUID(payloadTestActorID.UUID)) {
		t.Errorf("got %v, want %v", payload.By, openapi_types.UUID(payloadTestActorID.UUID))
	}
	if payload.Reason == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.Reason, "policy violation") {
		t.Errorf("got %v, want %v", *payload.Reason, "policy violation")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventUserDeactivated
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestUserDeactivatedPayload_NoReason(t *testing.T) {
	payload := userDeactivatedPayload(payloadTestUserID, payloadTestActorID, nil)

	if payload.Reason != nil {
		t.Errorf("expected nil, got %v", payload.Reason)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(raw), "reason") {
		t.Errorf("%q should not contain %q", string(raw), "reason")
	}
}

func TestUserReactivatedPayload(t *testing.T) {
	payload := userReactivatedPayload(payloadTestUserID, payloadTestActorID)

	if !reflect.DeepEqual(payload.EventType(), "user.reactivated") {
		t.Errorf("got %v, want %v", payload.EventType(), "user.reactivated")
	}
	if !reflect.DeepEqual(payload.EntityType(), "user") {
		t.Errorf("got %v, want %v", payload.EntityType(), "user")
	}
	if !reflect.DeepEqual(payload.UserId, openapi_types.UUID(payloadTestUserID.UUID)) {
		t.Errorf("got %v, want %v", payload.UserId, openapi_types.UUID(payloadTestUserID.UUID))
	}
	if !reflect.DeepEqual(payload.By, openapi_types.UUID(payloadTestActorID.UUID)) {
		t.Errorf("got %v, want %v", payload.By, openapi_types.UUID(payloadTestActorID.UUID))
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventUserReactivated
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestRoleChangedPayload_WithFromRole(t *testing.T) {
	fromRole := "member"
	payload := roleChangedPayload(payloadTestUserID, "manager", payloadTestActorID, &fromRole)

	if !reflect.DeepEqual(payload.EventType(), "role.changed") {
		t.Errorf("got %v, want %v", payload.EventType(), "role.changed")
	}
	if !reflect.DeepEqual(payload.EntityType(), "user") {
		t.Errorf("got %v, want %v", payload.EntityType(), "user")
	}
	if !reflect.DeepEqual(payload.UserId, openapi_types.UUID(payloadTestUserID.UUID)) {
		t.Errorf("got %v, want %v", payload.UserId, openapi_types.UUID(payloadTestUserID.UUID))
	}
	if !reflect.DeepEqual(payload.ToRole, "manager") {
		t.Errorf("got %v, want %v", payload.ToRole, "manager")
	}
	if !reflect.DeepEqual(payload.By, openapi_types.UUID(payloadTestActorID.UUID)) {
		t.Errorf("got %v, want %v", payload.By, openapi_types.UUID(payloadTestActorID.UUID))
	}
	if payload.FromRole == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.FromRole, "member") {
		t.Errorf("got %v, want %v", *payload.FromRole, "member")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventRoleChanged
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestRoleChangedPayload_NoFromRole(t *testing.T) {
	payload := roleChangedPayload(payloadTestUserID, "manager", payloadTestActorID, nil)

	if payload.FromRole != nil {
		t.Errorf("expected nil, got %v", payload.FromRole)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(raw), "from_role") {
		t.Errorf("%q should not contain %q", string(raw), "from_role")
	}
}

func TestPassportRevokedPayload(t *testing.T) {
	payload := passportRevokedPayload(payloadTestPassportID, payloadTestActorID)

	if !reflect.DeepEqual(payload.EventType(), "passport.revoked") {
		t.Errorf("got %v, want %v", payload.EventType(), "passport.revoked")
	}
	if !reflect.DeepEqual(payload.EntityType(), "passport") {
		t.Errorf("got %v, want %v", payload.EntityType(), "passport")
	}
	if !reflect.DeepEqual(payload.PassportId, openapi_types.UUID(payloadTestPassportID.UUID)) {
		t.Errorf("got %v, want %v", payload.PassportId, openapi_types.UUID(payloadTestPassportID.UUID))
	}
	if !reflect.DeepEqual(payload.By, openapi_types.UUID(payloadTestActorID.UUID)) {
		t.Errorf("got %v, want %v", payload.By, openapi_types.UUID(payloadTestActorID.UUID))
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventPassportRevoked
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestOnboardingStateChangedPayload(t *testing.T) {
	payload := onboardingStateChangedPayload(payloadTestUserID.UUID, "website", "connect", 3, true, false, false)

	if !reflect.DeepEqual(payload.EventType(), "onboarding.state_changed") {
		t.Errorf("got %v, want %v", payload.EventType(), "onboarding.state_changed")
	}
	if !reflect.DeepEqual(payload.EntityType(), "onboarding_wizard_state") {
		t.Errorf("got %v, want %v", payload.EntityType(), "onboarding_wizard_state")
	}
	if !reflect.DeepEqual(payload.UserId, openapi_types.UUID(payloadTestUserID.UUID)) {
		t.Errorf("got %v, want %v", payload.UserId, openapi_types.UUID(payloadTestUserID.UUID))
	}
	if !reflect.DeepEqual(payload.Path, "website") {
		t.Errorf("got %v, want %v", payload.Path, "website")
	}
	if !reflect.DeepEqual(payload.Step, "connect") {
		t.Errorf("got %v, want %v", payload.Step, "connect")
	}
	if !reflect.DeepEqual(payload.Version, int64(3)) {
		t.Errorf("got %v, want %v", payload.Version, int64(3))
	}
	if !payload.VoiceSkipped {
		t.Error("expected the condition to be true")
	}
	if payload.ConnectSkipped {
		t.Error("expected the condition to be false")
	}
	if payload.Completed {
		t.Error("expected the condition to be false")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventOnboardingStateChanged
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}
