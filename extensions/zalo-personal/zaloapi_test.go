// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The three calls a connector makes, driven against the fake chat server in
// session_test.go. Two properties here are load-bearing rather than incidental:
// the send's parameter set is EXACT, and a send whose outcome this process
// cannot determine is tellable from one Zalo refused.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	sendPath = "/api/message/sms"
)

func TestSendTextSendsExactlyFiveParameters(t *testing.T) {
	fake := newChatServer()
	var sent map[string]any
	fake.calls[sendPath] = func(_ *testing.T, params map[string]any) string {
		sent = params
		return `{"error_code":0,"data":{"msgId":8158866752417,"clientId":0,"ts":0}}`
	}

	session := resumeAgainst(t, fake)
	receipt, err := session.SendText(t.Context(), "9876543210", "xin chào")
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	want := []string{"clientId", "imei", "message", "toid", "ttl"}
	got := make([]string, 0, len(sent))
	for k := range sent {
		got = append(got, k)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("send parameters = %v, want exactly %v", got, want)
	}

	if sent["message"] != "xin chào" || sent["toid"] != "9876543210" || sent["imei"] != vectorIMEI {
		t.Errorf("send parameters carry the wrong values: %+v", sent)
	}
	if clientID, ok := sent["clientId"].(float64); !ok || clientID <= 0 {
		t.Errorf("clientId = %v, want the injected clock's milliseconds", sent["clientId"])
	}

	// The receipt is thin because the wire is: clientId and ts come back zero,
	// so msgId is the only usable provider identifier.
	if receipt.MsgID != "8158866752417" {
		t.Errorf("receipt = %+v", receipt)
	}
}

// TestARefusedSendIsNotAnUnknownOutcome: Zalo read the request and said no, so
// retrying it is safe and the caller must not be told otherwise.
func TestARefusedSendIsNotAnUnknownOutcome(t *testing.T) {
	fake := newChatServer()
	fake.calls[sendPath] = func(_ *testing.T, _ map[string]any) string {
		return `{"error_code":114,"error_message":"Tham số không hợp lệ"}`
	}

	session := resumeAgainst(t, fake)
	_, err := session.SendText(t.Context(), "9876543210", "xin chào")

	var refusal *refusalError
	if !errors.As(err, &refusal) || refusal.Code != 114 {
		t.Fatalf("send refusal surfaced as %v, want a refusalError carrying error_code 114", err)
	}
	if errors.Is(err, errUnanswered) {
		t.Error("a refusal Zalo actually sent was reported as an unknown outcome; retrying it would be a duplicate message")
	}
}

// TestASendThatIsNeverAnsweredIsReportedAsUnknown: the request left and nothing
// came back, so the message may or may not exist. Reporting that as a failure
// loses it; reporting it as a success drops it.
func TestASendThatIsNeverAnsweredIsReportedAsUnknown(t *testing.T) {
	fake := newChatServer()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sendPath {
			// Hang up mid-request rather than answering, which is what a lost
			// upstream looks like from here.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			if err := conn.Close(); err != nil {
				t.Errorf("close hijacked connection: %v", err)
			}
			return
		}
		body, err := fake.answer(t, r)
		if err != nil {
			t.Errorf("fake chat server: %v", err)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	session, err := zaloResume(t.Context(), testSealed(), testOptions(t, srv, time.Second))
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	_, err = session.SendText(t.Context(), "9876543210", "xin chào")
	if !errors.Is(err, errUnanswered) {
		t.Fatalf("an unanswered send surfaced as %v, want it to be tellable as an unknown outcome", err)
	}
	var refusal *refusalError
	if errors.As(err, &refusal) {
		t.Error("an unanswered send was reported as a refusal Zalo sent")
	}
}

func TestACallToACapabilityThisSessionWasNotIssuedSaysSo(t *testing.T) {
	session := resumeAgainst(t, newChatServer())
	session.service = map[string][]string{}

	if _, err := session.SendText(t.Context(), "9876543210", "hi"); err == nil {
		t.Error("a send with no chat host was attempted anyway")
	}
}

// TestAServiceHostZaloDoesNotOwnIsNeverDialled: the per-capability host map is
// chosen by the provider inside an ENCRYPTED response, and every call made to
// one of its hosts carries the member's session cookies. A hostile or
// compromised answer naming its own host would collect them on the first send.
func TestAServiceHostZaloDoesNotOwnIsNeverDialled(t *testing.T) {
	refused := map[string]string{
		"a host Zalo does not own": "https://collect.attacker.example",
		"a lookalike host":         "https://tw-chat.zalo.me.attacker.example",
		"a bare public suffix":     "https://me",
	}
	for name, host := range refused {
		t.Run(name, func(t *testing.T) {
			fake := newChatServer()
			fake.chatHost = host
			fake.calls[sendPath] = func(_ *testing.T, _ map[string]any) string {
				t.Error("the send reached a host Zalo does not own")
				return `{"error_code":0,"data":{"msgId":1}}`
			}

			_, err := resumeAgainst(t, fake).SendText(t.Context(), "9876543210", "xin chào")
			if err == nil {
				t.Fatalf("a send was made to %s", host)
			}
			if !strings.Contains(err.Error(), "not a Zalo host") {
				t.Errorf("error %q does not say why the host was refused", err)
			}
		})
	}
}

// TestASessionStillCallsTheHostsZaloActuallyIssues keeps the guard above from
// being a refusal of everything: the live service map hands out `*.chat.zalo.me`
// and `*.zaloapp.com`, and both have to work.
func TestASessionStillCallsTheHostsZaloActuallyIssues(t *testing.T) {
	for _, host := range []string{"https://tt-chat3-wpa.chat.zalo.me", "https://wpa.zaloapp.com"} {
		t.Run(host, func(t *testing.T) {
			fake := newChatServer()
			fake.chatHost = host
			var reached bool
			fake.calls[sendPath] = func(_ *testing.T, _ map[string]any) string {
				reached = true
				return `{"error_code":0,"data":{"msgId":8158866752417}}`
			}

			if _, err := resumeAgainst(t, fake).SendText(t.Context(), "9876543210", "xin chào"); err != nil {
				t.Fatalf("send to a host Zalo issues: %v", err)
			}
			if !reached {
				t.Error("the send never arrived")
			}
		})
	}
}

// TestARefusalInsideTheEncryptedBodyIsStillARefusal: an ordinary call answers
// HTTP 200 with error_code 0 and puts the real verdict INSIDE the encrypted
// payload, so a reader that stops at the outer envelope reports a refused send
// as a success and hands back an empty receipt.
func TestARefusalInsideTheEncryptedBodyIsStillARefusal(t *testing.T) {
	fake := newChatServer()
	fake.calls[sendPath] = func(_ *testing.T, _ map[string]any) string {
		return `{"error_code":114,"error_message":"Tham số không hợp lệ"}`
	}

	_, err := resumeAgainst(t, fake).SendText(t.Context(), "9876543210", "xin chào")

	var refusal *refusalError
	if !errors.As(err, &refusal) || refusal.Code != 114 {
		t.Fatalf("an encrypted refusal surfaced as %v, want a refusalError carrying error_code 114", err)
	}
	if errors.Is(err, errUnanswered) {
		t.Error("a refusal Zalo encrypted and sent was reported as an unknown outcome")
	}
}

// TestAChallengePageInPlaceOfAnAnswerIsReportedAsOne: Zalo serves HTML when it
// dislikes the request's shape, and a JSON parse error 200 lines from the cause
// is what that looks like unless the endpoint is named.
func TestAChallengePageInPlaceOfAnAnswerIsReportedAsOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte("<html>are you a robot?</html>")); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	session := resumeAgainst(t, newChatServer())
	session.c = newClient(testOptions(t, srv, time.Second))

	_, err := session.SendText(t.Context(), "9876543210", "xin chào")
	if err == nil {
		t.Fatal("an HTML challenge page was read as a send receipt")
	}
	if !strings.Contains(err.Error(), "/api/message/sms") {
		t.Errorf("error %q does not name the endpoint that answered HTML", err)
	}

	if _, err := fetchAccount(t.Context(), session.c); err == nil {
		t.Fatal("an HTML challenge page was read as a liveness answer")
	}
}

// TestASendWithNoMessageIDIsUnansweredRatherThanSuccessful.
//
// An absent or empty `msgId` unmarshals cleanly into json.Number("") with no
// error, so nothing before this check notices. What it produces is a receipt
// with no provider id at all — and MsgID is the only anchor a later inbound
// echo could be matched against, so "success" here is a message this
// installation can never recognise as its own.
//
// It has to be UNANSWERED specifically. Zalo accepted the request, so the
// message may well have been delivered; an ordinary error has the core retry
// and send a customer the same reply twice.
func TestASendWithNoMessageIDIsUnansweredRatherThanSuccessful(t *testing.T) {
	answers := map[string]string{
		"an empty msgId":   `{"error_code":0,"data":{"msgId":"","clientId":0,"ts":0}}`,
		"a missing msgId":  `{"error_code":0,"data":{"clientId":0,"ts":0}}`,
		"a null msgId":     `{"error_code":0,"data":{"msgId":null}}`,
		"an empty payload": `{"error_code":0,"data":{}}`,
	}
	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			fake := newChatServer()
			fake.calls[sendPath] = func(_ *testing.T, _ map[string]any) string { return answer }

			receipt, err := resumeAgainst(t, fake).SendText(t.Context(), "9876543210", "xin chào")
			if err == nil {
				t.Fatalf("a send with no message id was reported as the success %+v", receipt)
			}
			if !errors.Is(err, errUnanswered) {
				t.Errorf("error %v is not tellable as an unknown outcome, so the core would retry it and send the message twice", err)
			}
			if receipt.MsgID != "" {
				t.Errorf("receipt = %+v, want nothing", receipt)
			}
		})
	}
}

// TestNoSendErrorNamesTheRecipient: these errors reach the operator log and the
// delivery record. Naming who a message was for retains a person's Zalo account
// identifier outside the message store, which is the one place it belongs.
func TestNoSendErrorNamesTheRecipient(t *testing.T) {
	const recipient = "9876543210"

	answers := map[string]func(*testing.T, map[string]any) string{
		"a refusal":     func(*testing.T, map[string]any) string { return `{"error_code":114,"error_message":"invalid"}` },
		"no message id": func(*testing.T, map[string]any) string { return `{"error_code":0,"data":{}}` },
	}
	for name, handler := range answers {
		t.Run(name, func(t *testing.T) {
			fake := newChatServer()
			fake.calls[sendPath] = handler

			_, err := resumeAgainst(t, fake).SendText(t.Context(), recipient, "xin chào")
			if err == nil {
				t.Fatal("the send succeeded, so this test proves nothing")
			}
			if strings.Contains(err.Error(), recipient) {
				t.Errorf("the error names the recipient: %v", err)
			}
		})
	}

	// The unanswered-transport path too, which is the one that carried it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	session := resumeAgainst(t, newChatServer())
	session.c = newClient(testOptions(t, srv, time.Second))
	if _, err := session.SendText(t.Context(), recipient, "xin chào"); err == nil {
		t.Fatal("the send succeeded, so this test proves nothing")
	} else if strings.Contains(err.Error(), recipient) {
		t.Errorf("the unanswered-send error names the recipient: %v", err)
	}
}
