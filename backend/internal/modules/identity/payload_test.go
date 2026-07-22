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
// Before this migration none of crmcontracts.WebhookPayloadUserInvited/
// UserDeactivated/UserReactivated/RoleChanged/PassportRevoked/
// OnboardingStateChanged existed, and none of the builder functions
// existed (every site inlined a map[string]any), so this test failed to
// compile (RED) until public-events.yaml gained the schemas, `make gen`
// regenerated the structs, and users.go/passport.go/onboarding.go grew
// the builders.

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/stretchr/testify/require"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var (
	payloadTestUserID     = ids.From[ids.UserKind](ids.UUID(uuid.MustParse("11111111-1111-1111-1111-111111111111")))
	payloadTestActorID    = ids.From[ids.UserKind](ids.UUID(uuid.MustParse("22222222-2222-2222-2222-222222222222")))
	payloadTestPassportID = ids.From[ids.PassportKind](ids.UUID(uuid.MustParse("33333333-3333-3333-3333-333333333333")))
)

func TestUserInvitedPayload(t *testing.T) {
	payload := userInvitedPayload(payloadTestUserID, "manager", payloadTestActorID)

	require.Equal(t, "user.invited", payload.EventType())
	require.Equal(t, "user", payload.EntityType())
	require.Equal(t, openapi_types.UUID(payloadTestUserID.UUID), payload.UserId)
	require.Equal(t, "manager", payload.Role)
	require.Equal(t, openapi_types.UUID(payloadTestActorID.UUID), payload.By)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadUserInvited
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestUserDeactivatedPayload_WithReason(t *testing.T) {
	reason := "policy violation"
	payload := userDeactivatedPayload(payloadTestUserID, payloadTestActorID, &reason)

	require.Equal(t, "user.deactivated", payload.EventType())
	require.Equal(t, "user", payload.EntityType())
	require.Equal(t, openapi_types.UUID(payloadTestUserID.UUID), payload.UserId)
	require.Equal(t, openapi_types.UUID(payloadTestActorID.UUID), payload.By)
	require.NotNil(t, payload.Reason)
	require.Equal(t, "policy violation", *payload.Reason)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadUserDeactivated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestUserDeactivatedPayload_NoReason(t *testing.T) {
	payload := userDeactivatedPayload(payloadTestUserID, payloadTestActorID, nil)

	require.Nil(t, payload.Reason)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "reason")
}

func TestUserReactivatedPayload(t *testing.T) {
	payload := userReactivatedPayload(payloadTestUserID, payloadTestActorID)

	require.Equal(t, "user.reactivated", payload.EventType())
	require.Equal(t, "user", payload.EntityType())
	require.Equal(t, openapi_types.UUID(payloadTestUserID.UUID), payload.UserId)
	require.Equal(t, openapi_types.UUID(payloadTestActorID.UUID), payload.By)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadUserReactivated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestRoleChangedPayload_WithFromRole(t *testing.T) {
	fromRole := "member"
	payload := roleChangedPayload(payloadTestUserID, "manager", payloadTestActorID, &fromRole)

	require.Equal(t, "role.changed", payload.EventType())
	require.Equal(t, "user", payload.EntityType())
	require.Equal(t, openapi_types.UUID(payloadTestUserID.UUID), payload.UserId)
	require.Equal(t, "manager", payload.ToRole)
	require.Equal(t, openapi_types.UUID(payloadTestActorID.UUID), payload.By)
	require.NotNil(t, payload.FromRole)
	require.Equal(t, "member", *payload.FromRole)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadRoleChanged
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestRoleChangedPayload_NoFromRole(t *testing.T) {
	payload := roleChangedPayload(payloadTestUserID, "manager", payloadTestActorID, nil)

	require.Nil(t, payload.FromRole)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "from_role")
}

func TestPassportRevokedPayload(t *testing.T) {
	payload := passportRevokedPayload(payloadTestPassportID, payloadTestActorID)

	require.Equal(t, "passport.revoked", payload.EventType())
	require.Equal(t, "passport", payload.EntityType())
	require.Equal(t, openapi_types.UUID(payloadTestPassportID.UUID), payload.PassportId)
	require.Equal(t, openapi_types.UUID(payloadTestActorID.UUID), payload.By)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadPassportRevoked
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestOnboardingStateChangedPayload(t *testing.T) {
	payload := onboardingStateChangedPayload(payloadTestUserID.UUID, "website", "connect", 3, true, false, false)

	require.Equal(t, "onboarding.state_changed", payload.EventType())
	require.Equal(t, "onboarding_wizard_state", payload.EntityType())
	require.Equal(t, openapi_types.UUID(payloadTestUserID.UUID), payload.UserId)
	require.Equal(t, "website", payload.Path)
	require.Equal(t, "connect", payload.Step)
	require.Equal(t, int64(3), payload.Version)
	require.True(t, payload.VoiceSkipped)
	require.False(t, payload.ConnectSkipped)
	require.False(t, payload.Completed)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadOnboardingStateChanged
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}
