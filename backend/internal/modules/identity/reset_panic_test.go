// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The reset-mail goroutine's recover: it outlives the request, so the
// chassis recovery middleware can never see a panic inside it, and an
// unrecovered panic on an unauthenticated endpoint is a one-request denial
// of service. A nil pool is enough to trigger a real panic on this path —
// database.WithWorkspaceTx calls pool.Begin without a nil check — so this
// proves the guard without a database: the request still answers 202 and
// the process survives a send that panics.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestRequestPasswordResetRecoversAPanicInTheSendGoroutine(t *testing.T) {
	// A workspace-bound context with a nil-pool Service reaches the mint's
	// WithWorkspaceTx call and panics there — before the mailer is ever
	// invoked. That is exactly what the recover in RequestPasswordReset
	// exists to survive.
	svc := &Service{}
	h := NewHandlers(svc).WithPasswordReset(nopMailer{}, "https://crm.example.test")
	done := make(chan struct{})
	h.resetSendStarted = func() { close(done) }

	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/forgot-password",
		strings.NewReader(`{"email":"a@b.test"}`)).WithContext(ctx)

	// The call itself must return, and the surviving test process is the
	// proof the panic never left the goroutine: an unrecovered panic here
	// would crash the test binary, not merely fail an assertion.
	h.RequestPasswordReset(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 — the response leaves before the panicking work starts", rec.Code)
	}
	<-done
}
