// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

// The problem-detail boundary: a sentinel match carries its crafted
// domain detail onto the wire, but never the text of an infrastructure
// failure that happened to be wrapped into the same chain — that text
// (SQL fragments, hosts, ports) is operator material and goes to the
// server log instead. And a malformed keyset cursor is the client's
// fault: 422, never a 500.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

func writeAndDecode(t *testing.T, err error) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	Write(rec, req, err)
	var body map[string]any
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
		t.Fatalf("decoding problem body %q: %v", rec.Body.String(), decodeErr)
	}
	return rec.Code, body
}

func TestWrite_craftedDomainDetailFlows(t *testing.T) {
	err := fmt.Errorf("approval expired 15m0s after decision: %w", apperrors.ErrConflict)
	status, body := writeAndDecode(t, err)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if detail := body["detail"]; detail != err.Error() {
		t.Errorf("detail = %q, want the crafted domain message %q", detail, err.Error())
	}
}

func TestWrite_infrastructureCauseNeverReachesTheWire(t *testing.T) {
	cases := map[string]error{
		"postgres": fmt.Errorf("%w: %w", apperrors.ErrConflict,
			&pgconn.PgError{Severity: "ERROR", Code: "23505", Message: "duplicate key on host db-internal:5432"}),
		"network": fmt.Errorf("%w: %w", apperrors.ErrConflict,
			&fakeNetError{msg: "dial tcp 10.0.0.7:5432: connection refused"}),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			status, body := writeAndDecode(t, err)
			if status != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (the sentinel still maps)", status)
			}
			detail, _ := body["detail"].(string)
			if detail != apperrors.ErrConflict.Error() {
				t.Errorf("detail = %q, want the sentinel's canonical text %q", detail, apperrors.ErrConflict.Error())
			}
			if strings.Contains(detail, "5432") || strings.Contains(detail, "10.0.0.7") {
				t.Errorf("infrastructure text leaked onto the wire: %q", detail)
			}
		})
	}
}

func TestWrite_malformedCursorIsAClientFault(t *testing.T) {
	_, err := storekit.DecodeCursor("garbage!!")
	if err == nil {
		t.Fatal("garbage cursor decoded")
	}
	status, body := writeAndDecode(t, fmt.Errorf("listing people: %w", err))
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", status)
	}
	if body["code"] != "validation_error" {
		t.Errorf("code = %v, want validation_error", body["code"])
	}
}

// fakeNetError satisfies net.Error without opening a socket.
type fakeNetError struct{ msg string }

func (e *fakeNetError) Error() string   { return e.msg }
func (e *fakeNetError) Timeout() bool   { return false }
func (e *fakeNetError) Temporary() bool { return false }
