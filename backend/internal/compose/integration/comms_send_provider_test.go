// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The stubbed Google the outbound suite next door transmits through — one
// suite, two files by concept, because the whole point of the assertions there
// is that everything BUT this is the production object.
//
// A provider is more than an endpoint that accepts bytes. It keeps its own copy
// of what it accepted, and it is free to have rewritten the message identity
// before storing it — which Gmail does. The send path reads that copy back, and
// the sync later re-reads it as the Sent-folder echo, so both halves of the
// reconcile are reading whatever this stub decided to keep. That is why the
// rewrite lives here rather than in any one test: the tests derive the echo from
// what the provider stored, instead of restating what the rewrite should have
// produced.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

// sendingMailbox is the address the stubbed Google reports as the connected
// mailbox owner. It is the From: line the connector stamps, so it is also the
// owner mailmap must be given when the same bytes come back — a different one
// would read the message as inbound.
const sendingMailbox = "sender@fable.test"

// sentMail holds the base64url RFC822 one transmission handed to Gmail.
type sentMail struct{ raw string }

// gmailSendStub answers the endpoints one connect-and-transmit touches and
// keeps the raw MIME it was asked to send.
//
// stampAs is the Message-ID the provider puts on the copy it STORES. Empty
// means the provider honoured the client's — the well-behaved case, and the one
// every test but the reconcile's own is about.
func gmailSendStub(t *testing.T, captured *sentMail, stampAs string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// The read-back of the message just sent. Without it the send path's
	// messages.get 404s and the reconcile never runs, so every test through
	// this stub would exercise a path the product does not take.
	mux.HandleFunc("/messages/", func(w http.ResponseWriter, _ *http.Request) {
		if captured.raw == "" {
			t.Error("the sent message was read back before anything was sent")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		WriteJSON(w, map[string]any{
			"raw":      restamp(t, captured.raw, stampAs),
			"labelIds": []string{"SENT"},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("decoding the token request form: %v", err)
			return
		}
		body := map[string]any{"access_token": "access-tok", "expires_in": 3599}
		if r.Form.Get("grant_type") == "authorization_code" {
			body["refresh_token"] = "refresh-tok"
		}
		WriteJSON(w, body)
	})
	mux.HandleFunc("/profile", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, map[string]any{"emailAddress": sendingMailbox, "historyId": "1001"})
	})
	mux.HandleFunc("/messages/send", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Raw string `json:"raw"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the send body: %v", err)
			return
		}
		captured.raw = body.Raw
		WriteJSON(w, map[string]any{"id": "gmail-msg-1", "threadId": "gmail-thread-1"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// messageIDLine matches the one header a provider is made to rewrite below.
// In-Reply-To and References carry bracketed identities too, which is why the
// pattern keys on the header name — and it is anchored to the START of a line,
// so a body quoting a Message-ID cannot absorb the rewrite and leave the real
// header standing. A rewrite that silently did not rewrite would make every
// test here pass while proving nothing.
var messageIDLine = regexp.MustCompile(`(?im)^Message-ID: <[^>]*>\r\n`)

// storedCopy renders the message as the provider KEPT it: the transmitted MIME
// with its Message-ID replaced by the one the provider minted. An empty stampAs
// hands the bytes back untouched, which is a provider that honoured the
// identity it was given.
func storedCopy(t *testing.T, mime []byte, stampAs string) []byte {
	t.Helper()
	if stampAs == "" {
		return mime
	}
	rewritten := messageIDLine.ReplaceAll(mime, []byte("Message-ID: <"+stampAs+">\r\n"))
	if bytes.Equal(rewritten, mime) {
		t.Fatalf("no Message-ID header to rewrite in the transmitted message:\n%s", mime)
	}
	return rewritten
}

// restamp is storedCopy over the base64url the provider stub speaks.
func restamp(t *testing.T, rawBase64URL, stampAs string) string {
	t.Helper()
	mime, err := base64.URLEncoding.DecodeString(rawBase64URL)
	if err != nil {
		t.Fatalf("the connector did not hand Gmail base64url: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(storedCopy(t, mime, stampAs))
}
