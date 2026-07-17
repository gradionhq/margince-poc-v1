// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testPushEmail = "rep@ws.example"

func pushBody(t *testing.T) []byte {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"emailAddress": testPushEmail, "historyId": 12345})
	env := map[string]any{
		"message":      map[string]any{"data": base64.StdEncoding.EncodeToString(data)},
		"subscription": "projects/p/subscriptions/s",
	}
	b, _ := json.Marshal(env)
	return b
}

func TestDecodePushEmail(t *testing.T) {
	email, err := decodePushEmail(bytes.NewReader(pushBody(t)))
	if err != nil {
		t.Fatalf("decodePushEmail: %v", err)
	}
	if email != testPushEmail {
		t.Errorf("email = %q, want %s", email, testPushEmail)
	}
}

func TestDecodePushEmailMalformed(t *testing.T) {
	if _, err := decodePushEmail(bytes.NewReader([]byte("not json"))); err == nil {
		t.Fatal("decodePushEmail(garbage) = nil error, want error")
	}
}

func TestBearerToken(t *testing.T) {
	if got := bearerToken("Bearer abc.def.ghi"); got != "abc.def.ghi" {
		t.Errorf("bearerToken = %q, want abc.def.ghi", got)
	}
	if got := bearerToken("Basic xyz"); got != "" {
		t.Errorf("bearerToken(Basic) = %q, want empty", got)
	}
}

func TestPushHandlerUnwiredReturns501(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/hooks/gmail/push", bytes.NewReader(pushBody(t)))
	gmailPushHandler{}.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

func TestPushHandlerRejectsBadToken(t *testing.T) {
	rig := newOIDCTestRig(t)
	h := gmailPushHandler{
		verifier: newTestVerifier(rig),
		enqueue:  func(context.Context, string) error { t.Fatal("must not enqueue on bad token"); return nil },
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/hooks/gmail/push", bytes.NewReader(pushBody(t)))
	req.Header.Set("Authorization", "Bearer not.a.validtoken")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPushHandlerEnqueuesOnValidToken(t *testing.T) {
	rig := newOIDCTestRig(t)
	tok := rig.mint(t, testKID, "RS256", nil)
	var got string
	h := gmailPushHandler{
		verifier: newTestVerifier(rig),
		enqueue:  func(_ context.Context, email string) error { got = email; return nil },
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/hooks/gmail/push", bytes.NewReader(pushBody(t)))
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got != testPushEmail {
		t.Errorf("enqueued email = %q, want %s", got, testPushEmail)
	}
}
