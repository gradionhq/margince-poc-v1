// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A tool call that outlives the response deadline. The api role arms a
// server-wide WriteTimeout, and a dynamic tool call may legitimately run past
// it, so the transport extends the deadline for its own route — an extension
// that only reaches the connection if every ResponseWriter wrapper in the
// chain exposes Unwrap.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// expiredWriteDeadline is a server-wide write deadline already in the past by
// the time any handler runs. It stands in for the api role's 30s WriteTimeout
// (cmd/api/main.go): what a slow tool call has to survive is the DEADLINE, so
// compressing it to nothing proves the same property in milliseconds instead
// of making the suite wait out half a minute of real time.
const expiredWriteDeadline = time.Nanosecond

// heldDraft is the model lane behind draft_email, holding one completion open
// until the test releases it. It is the injected signal that makes a tool call
// outlive the response deadline: a sleep would demonstrate the same thing by
// waiting, which is the one way a test may not do it.
type heldDraft struct {
	entered chan struct{}
	release chan struct{}
}

func newHeldDraft() *heldDraft {
	return &heldDraft{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (h *heldDraft) Complete(ctx context.Context, _ model.Request) (model.Response, error) {
	// A non-blocking announcement: the drafter calls this once per draft, and
	// a second call (a retry path this test does not take) must not wedge the
	// very request being measured.
	select {
	case h.entered <- struct{}{}:
	default:
	}
	select {
	case <-h.release:
		return model.Response{Text: `{"subject":"Re: Renewal terms","body":"Happy to walk through the renewal."}`}, nil
	case <-ctx.Done():
		// The request was abandoned. Returning the cancellation lets the
		// drafter fall back and the test fail on its assertion, rather than
		// hanging until the package timeout says nothing useful.
		return model.Response{}, ctx.Err()
	}
}

// TestASlowToolCallOutlivesTheServersWriteDeadline pins the two walls a
// long-running tool call has to clear on this transport. The route extends the
// write deadline for its own response (a dynamic call can legitimately block
// on a model call for longer than the server-wide WriteTimeout allows), and
// that extension only reaches the connection if every ResponseWriter wrapper
// between the mux and the handler exposes Unwrap — the access-log recorder and
// the connector edge's status recorder both wrap it. Lose either and this call
// dies mid-write with a response the client can never complete.
func TestASlowToolCallOutlivesTheServersWriteDeadline(t *testing.T) {
	held := newHeldDraft()
	e := setupConnectorWith(t, compose.WithReplyDraft(held))

	// The same composed handler, behind a server that HAS the deadline. The
	// harness's own server has none, so the wall under test would not exist
	// there; setup runs against that one because a deadline this tight
	// refuses every ordinary response, including the ones that seed this test.
	strict := httptest.NewUnstartedServer(e.TS.Config.Handler)
	strict.Config.WriteTimeout = expiredWriteDeadline
	strict.Start()
	t.Cleanup(strict.Close)
	client := strict.Client()

	// Control: the deadline is armed. /healthz extends nothing, so its answer
	// cannot reach a client here — without this the test would pass just as
	// happily against a server that has no deadline at all.
	if resp, err := client.Get(strict.URL + "/healthz"); err == nil {
		apptest.CloseBody(t, resp)
		t.Fatalf("GET /healthz answered %d although the server write deadline had expired: the wall under test is not armed", resp.StatusCode)
	}

	// The thread the draft replies to, and a passport that may draft on it.
	var thread struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/activities", apptest.AnyMap{
		"kind": "email", "subject": "Renewal terms", "body": "What would renewal look like?",
		"direction": "inbound",
	}, nil, &thread); status != http.StatusCreated {
		t.Fatalf("log the thread being replied to → %d", status)
	}
	var minted struct {
		Token string `json:"token"`
	}
	if status := e.Call(t, "POST", "/v1/passports", apptest.AnyMap{
		"label": "slow caller", "scopes": []string{"read", "draft"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}

	// The call is released only once it is provably in flight, so the response
	// is written after a hold this test controls rather than after a duration
	// it guessed at.
	finished := make(chan struct{})
	t.Cleanup(func() { close(finished) })
	go func() {
		select {
		case <-held.entered:
			close(held.release)
		case <-finished:
		}
	}()

	req, err := http.NewRequest(http.MethodPost, strict.URL+"/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"draft_email",`+
			`"arguments":{"activity_id":"`+thread.ID+`","intent":"answer the renewal question"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+minted.Token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the held tools/call never delivered a response: %v", err)
	}
	defer apptest.CloseBody(t, resp)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the held tools/call response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("held tools/call → %d %s", resp.StatusCode, body)
	}
	// rpcResult decodes the envelope whole, so a response cut off mid-write
	// fails here instead of passing on a prefix that happened to look fine.
	if text := toolText(t, rpcResult(t, string(body))); !strings.Contains(text, "Re: Renewal terms") {
		t.Fatalf("the held draft answered %q, not the draft the released model returned", text)
	}
}
