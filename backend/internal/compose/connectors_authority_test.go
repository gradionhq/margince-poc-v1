// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The consent round-trip spans minutes of a human clicking through a provider,
// and the signed state says who STARTED it, not who they still are. The
// callback therefore re-resolves the granting human's live authority — and it
// does so before the code exchange, because a token minted for someone who may
// no longer hold it has to be revoked, not merely dropped.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
)

// liveAuthority is identity's resolver answering for a human who still holds
// their seat — the default for handler fixtures whose subject is something
// other than liveness.
type liveAuthority struct{}

func (liveAuthority) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return authz.RBAC{}, nil
}

func (liveAuthority) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return principal.SeatFull, nil
}

// deadAuthority is identity's resolver answering for a human who no longer
// resolves — deactivated, suspended, or gone.
type deadAuthority struct{}

func (deadAuthority) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return authz.RBAC{}, apperrors.ErrNotFound
}

func (deadAuthority) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return "", apperrors.ErrNotFound
}

func TestCallbackRefusesADeadGrantorBeforeExchangingTheCode(t *testing.T) {
	oauth := &recordingOAuth{}
	h := connectorHandlers{
		registry:      capture.NewRegistry(nil, nil, deadAuthority{}, nil),
		authority:     deadAuthority{},
		oauth:         oauth,
		gmailAPI:      stubGmailAPI{},
		signer:        newStateSigner([]byte(testStateKey)),
		publicBaseURL: "https://app.test",
		apiBaseURL:    "https://api.test",
	}
	const nonce = "csrf-nonce-value"
	state := h.signer.sign(connectState{
		Workspace: ids.MustParse("11111111-1111-1111-1111-111111111111"),
		User:      ids.MustParse("22222222-2222-2222-2222-222222222222"),
		Provider:  providerGmail,
		Nonce:     nonce,
		Version:   stateVersionNamespacedCSRF,
	}, time.Now().Add(connectStateTTL))

	code := "the-code"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/connectors/gmail/callback", nil)
	req.AddCookie(&http.Cookie{Name: csrfCookieName(providerGmail), Value: nonce})
	h.ConnectorOAuthCallback(rec, req, "gmail", crmcontracts.ConnectorOAuthCallbackParams{State: state, Code: &code})

	if oauth.exchanged {
		t.Error("the code was exchanged for a credential the granting human can no longer hold")
	}
	if got, want := rec.Header().Get("Location"), "https://app.test/#/onboarding/connect/error"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}
