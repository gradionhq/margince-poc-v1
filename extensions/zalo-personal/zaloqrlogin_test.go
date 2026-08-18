// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The QR handshake driven against a fake id.zalo.me. Every terminal state the
// phone can reach is covered here, because the states are what a connecting
// member's screen is made of and a state this package cannot report is a screen
// that hangs.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testBundleVersion = "2606161423"
	testQRCode        = "qr-code-abc"
	testQRImage       = "data:image/png;base64,iVBORw0KGgo="
)

// loginServer is a fake id.zalo.me. Each poll answer is popped from a script,
// so a test says "waiting, then scanned" as a list rather than as timing.
//
// Its state is mutated by net/http's handler goroutines and read by the test
// goroutine, so it is guarded. An unguarded fake reports races in the code under
// test, which is the most expensive kind of false lead: the flake looks like the
// thing being tested.
type loginServer struct {
	mu sync.Mutex

	// qrImage is what the generate call answers with; empty means the ordinary
	// inline PNG. A provider chooses this string, so a test gets to too.
	qrImage string

	// scanCeiling makes the fake stop saying "nothing yet" after that many
	// scan polls. A fake that says it forever would hang a broken poll loop
	// instead of failing it, and a test that hangs on a regression tells
	// whoever is watching CI nothing at all.
	scanCeiling int
	scanned     int

	scanAnswers    []string
	confirmAnswers []string
	logged         bool
	displayName    string
	calls          []string
}

// callPaths is the guarded read of what the fake has been asked, for a test
// goroutine that is not the one the handlers run on.
func (s *loginServer) callPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *loginServer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// pop assumes the caller holds the lock.
func (s *loginServer) pop(answers *[]string, path string) string {
	if len(*answers) == 0 {
		return fmt.Sprintf(`{"error_code":%d,"error_message":"still waiting on %s"}`, waitingErrorCodeRetry, path)
	}
	next := (*answers)[0]
	*answers = (*answers)[1:]
	return next
}

func (s *loginServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls = append(s.calls, r.URL.Path)
		body := s.answer(r.URL.Path, w)
		s.mu.Unlock()
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write %s response: %v", r.URL.Path, err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// answer assumes the caller holds the lock.
func (s *loginServer) answer(path string, w http.ResponseWriter) string {
	switch path {
	case "/account":
		return `<html><script src="https://stc-zlogin.zdn.vn/main-` + testBundleVersion + `.js"></script></html>`
	case "/account/logininfo", "/account/verify-client":
		http.SetCookie(w, &http.Cookie{Name: "zlogin", Value: "bootstrap", Path: "/"})
		return `{"error_code":0,"data":{}}`
	case "/account/authen/qr/generate":
		image := s.qrImage
		if image == "" {
			image = testQRImage
		}
		return fmt.Sprintf(`{"error_code":0,"data":{"code":%q,"image":%q}}`, testQRCode, image)
	case "/account/authen/qr/waiting-scan":
		s.scanned++
		if s.scanCeiling > 0 && s.scanned > s.scanCeiling {
			return `{"error_code":-99,"error_message":"this fake refuses to be asked forever"}`
		}
		return s.pop(&s.scanAnswers, path)
	case "/account/authen/qr/waiting-confirm":
		return s.pop(&s.confirmAnswers, path)
	case "/account/checksession":
		// checksession does not set the session itself: it REDIRECTS to
		// chat.zalo.me, and the hop that lands there is what sets the cookie.
		// The fake models the hop rather than short-cutting it, because a
		// cookie's scope is only legitimate relative to the host that set it —
		// short-cutting it here would be a fake proving something the wire does
		// not do.
		w.Header().Set("Location", "https://chat.zalo.me/index.html")
		w.WriteHeader(http.StatusFound)
		return ""
	case "/index.html":
		// The one response that matters for its headers rather than its body.
		http.SetCookie(w, &http.Cookie{Name: "zpw_sek", Value: "the-session-key", Domain: "chat.zalo.me", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "zpw_sek", Value: "EXPIRED", Domain: ".zalo.me", Path: "/", MaxAge: -1})
		return "<html>the web client</html>"
	case "/jr/userinfo":
		return fmt.Sprintf(`{"error_code":0,"data":{"logged":%t,"session_chat_valid":true,"info":{"name":%q,"avatar":"https://avatar"}}}`,
			s.logged, s.displayName)
	default:
		return `{"error_code":404,"error_message":"no such endpoint in this fake"}`
	}
}

func scannedAnswer(name string) string {
	return fmt.Sprintf(`{"error_code":0,"data":{"display_name":%q,"avatar":"https://scanner-avatar"}}`, name)
}

func startPending(t *testing.T, srv *httptest.Server) (zaloPending, zaloQRCode) {
	t.Helper()
	pending, qr, err := zaloStartQR(t.Context(), testOptions(t, srv, time.Second))
	if err != nil {
		t.Fatalf("start QR: %v", err)
	}
	return pending, qr
}

func TestStartQRMintsAScannableCodeAndCarriesTheBootstrapJar(t *testing.T) {
	fake := &loginServer{}
	srv := fake.start(t)

	pending, qr, err := zaloStartQR(t.Context(), testOptions(t, srv, time.Second))
	if err != nil {
		t.Fatalf("start QR: %v", err)
	}

	if qr.ImageDataURL != testQRImage {
		t.Errorf("image = %q, want the server's data URL passed through unchanged", qr.ImageDataURL)
	}
	if qr.ExpiresAt.Before(fixedTime) || qr.ExpiresAt.After(fixedTime.Add(2*qrLifetime)) {
		t.Errorf("expiry %s is not roughly one QR lifetime after the fake clock's start", qr.ExpiresAt)
	}
	if pending.Code != testQRCode || pending.BundleVersion != testBundleVersion {
		t.Errorf("pending carries code %q / version %q", pending.Code, pending.BundleVersion)
	}
	if pending.IMEI == "" || pending.UserAgent != defaultUserAgent || pending.Language != defaultLanguage {
		t.Errorf("pending is missing its device identity: %+v", pending)
	}
	if len(pending.Cookies) == 0 {
		t.Error("pending carries no bootstrap cookies, so the poll would start a second device registration")
	}
	if pending.Scanned {
		t.Error("a freshly minted QR is already marked scanned")
	}

	// The device registration is not optional: skipping either post yields a
	// QR the phone reports as invalid.
	want := []string{"/account", "/account/logininfo", "/account/verify-client", "/account/authen/qr/generate"}
	got := fake.callPaths()
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i, path := range want {
		if got[i] != path {
			t.Fatalf("call %d = %s, want %s", i, got[i], path)
		}
	}
}

func TestStartQRSaysSoWhenTheLoginPageIsNotTheLoginPage(t *testing.T) {
	fake := &loginServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/account" {
			if _, err := w.Write([]byte("<html>are you a robot?</html>")); err != nil {
				t.Errorf("write challenge page: %v", err)
			}
			return
		}
		if _, err := w.Write([]byte(fake.answer(r.URL.Path, w))); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	if _, _, err := zaloStartQR(t.Context(), testOptions(t, srv, time.Second)); err == nil {
		t.Fatal("a challenge page in place of the login page produced a QR")
	}
}

func TestStartQRSurfacesARefusalFromTheGenerateCall(t *testing.T) {
	fake := &loginServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := fake.answer(r.URL.Path, w)
		if r.URL.Path == "/account/authen/qr/generate" {
			body = `{"error_code":-2,"error_message":"too many devices"}`
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	_, _, err := zaloStartQR(t.Context(), testOptions(t, srv, time.Second))
	var refusal *refusalError
	if !errors.As(err, &refusal) || refusal.Code != -2 {
		t.Fatalf("generate refusal surfaced as %v, want a refusalError carrying error_code -2", err)
	}
}

// TestPollReportsWaitingWhileTheCodeIsUnscanned pins error_code 8: it is the
// protocol's "ask again", not a failure, and a poll that spends its budget on
// it must come back with waiting rather than an error.
func TestPollReportsWaitingWhileTheCodeIsUnscanned(t *testing.T) {
	fake := &loginServer{}
	srv := fake.start(t)
	pending, _ := startPending(t, srv)

	result, next, err := zaloPollQR(t.Context(), pending, testOptions(t, srv, time.Second), 5*time.Second)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if result.State != zaloScanWaiting {
		t.Errorf("state = %s, want %s", result.State, zaloScanWaiting)
	}
	if result.Sealed != nil {
		t.Error("an unscanned QR produced a sealed credential")
	}
	if next.Scanned {
		t.Error("an unscanned QR advanced the handshake")
	}
}

func TestPollReportsTheScannerBeforeTheConfirmation(t *testing.T) {
	fake := &loginServer{scanAnswers: []string{scannedAnswer("Ngọc Anh")}}
	srv := fake.start(t)
	pending, _ := startPending(t, srv)

	result, next, err := zaloPollQR(t.Context(), pending, testOptions(t, srv, time.Second), 5*time.Second)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if result.State != zaloScanScanned || result.DisplayName != "Ngọc Anh" {
		t.Errorf("result = %+v, want a scanned state naming the scanner", result)
	}
	if result.Sealed != nil {
		t.Error("a scanned-but-unconfirmed QR produced a sealed credential")
	}
	if !next.Scanned || next.DisplayName != "Ngọc Anh" {
		t.Errorf("pending did not advance to the confirm poll: %+v", next)
	}
}

func TestPollSealsTheCredentialOnlyOnceThePhoneConfirms(t *testing.T) {
	fake := &loginServer{
		scanAnswers:    []string{scannedAnswer("Ngọc Anh")},
		confirmAnswers: []string{`{"error_code":0,"data":{}}`},
		logged:         true,
		displayName:    "Ngọc Anh",
	}
	srv := fake.start(t)
	pending, _ := startPending(t, srv)

	opts := testOptions(t, srv, time.Second)
	scanResult, pending, err := zaloPollQR(t.Context(), pending, opts, 5*time.Second)
	if err != nil {
		t.Fatalf("scan poll: %v", err)
	}
	if scanResult.Sealed != nil {
		t.Fatal("the scan poll sealed a credential")
	}

	result, _, err := zaloPollQR(t.Context(), pending, opts, 5*time.Second)
	if err != nil {
		t.Fatalf("confirm poll: %v", err)
	}
	if result.State != zaloScanConfirmed {
		t.Fatalf("state = %s, want %s", result.State, zaloScanConfirmed)
	}
	if result.Sealed == nil {
		t.Fatal("a confirmed login sealed nothing")
	}
	if result.Sealed.IMEI == "" || result.Sealed.UserAgent != defaultUserAgent {
		t.Errorf("sealed credential is missing its device identity: %+v", *result.Sealed)
	}

	// The session cookie the redirect chain set is the credential; the clear
	// that arrived in the same response must not be sealed beside it.
	var sessionKeys int
	for _, c := range result.Sealed.Cookies {
		if c.Name == "zpw_sek" {
			sessionKeys++
			if c.Value != "the-session-key" {
				t.Errorf("sealed zpw_sek = %q", c.Value)
			}
		}
	}
	if sessionKeys != 1 {
		t.Errorf("sealed %d zpw_sek cookies, want exactly 1", sessionKeys)
	}
}

// TestPollReportsADeclineRatherThanAnError: tapping "no" on the phone is an
// answer, and a member who declined needs to be told that, not shown a failure.
func TestPollReportsADeclineRatherThanAnError(t *testing.T) {
	fake := &loginServer{
		scanAnswers:    []string{scannedAnswer("Ngọc Anh")},
		confirmAnswers: []string{fmt.Sprintf(`{"error_code":%d,"error_message":"declined"}`, qrDeclinedErrorCode)},
	}
	srv := fake.start(t)
	pending, _ := startPending(t, srv)

	opts := testOptions(t, srv, time.Second)
	_, pending, err := zaloPollQR(t.Context(), pending, opts, 5*time.Second)
	if err != nil {
		t.Fatalf("scan poll: %v", err)
	}

	result, _, err := zaloPollQR(t.Context(), pending, opts, 5*time.Second)
	if err != nil {
		t.Fatalf("confirm poll returned an error for a decline: %v", err)
	}
	if result.State != zaloScanDeclined {
		t.Errorf("state = %s, want %s", result.State, zaloScanDeclined)
	}
	if result.Sealed != nil {
		t.Error("a declined login sealed a credential")
	}
}

// TestPollReportsExpiryWithoutAskingTheServer: a dead code is dead, and
// long-polling it just holds a connection for 100 seconds to learn that.
func TestPollReportsExpiryWithoutAskingTheServer(t *testing.T) {
	fake := &loginServer{}
	srv := fake.start(t)
	pending, _ := startPending(t, srv)

	before := fake.callCount()
	pending.ExpiresAt = fixedTime.Add(-time.Minute)

	result, _, err := zaloPollQR(t.Context(), pending, testOptions(t, srv, time.Second), 5*time.Second)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if result.State != zaloScanExpired {
		t.Errorf("state = %s, want %s", result.State, zaloScanExpired)
	}
	if after := fake.callCount(); after != before {
		t.Errorf("an expired code was polled anyway: %v", fake.callPaths()[before:])
	}
}

func TestPollSurfacesARefusalNeitherPollUnderstands(t *testing.T) {
	fake := &loginServer{scanAnswers: []string{`{"error_code":-99,"error_message":"unknown"}`}}
	srv := fake.start(t)
	pending, _ := startPending(t, srv)

	_, _, err := zaloPollQR(t.Context(), pending, testOptions(t, srv, time.Second), 5*time.Second)
	var refusal *refusalError
	if !errors.As(err, &refusal) || refusal.Code != -99 {
		t.Fatalf("unknown scan refusal surfaced as %v, want a refusalError carrying error_code -99", err)
	}
}

// TestAConfirmedQRThatLeavesNoSessionIsNotReportedAsSuccess covers the honest
// hard case: the phone said yes and the redirect chain still produced nothing
// usable. Sealing that credential would give a connector a session that reports
// connected and transmits nothing.
func TestAConfirmedQRThatLeavesNoSessionIsNotReportedAsSuccess(t *testing.T) {
	fake := &loginServer{
		scanAnswers:    []string{scannedAnswer("Ngọc Anh")},
		confirmAnswers: []string{`{"error_code":0,"data":{}}`},
		logged:         false,
	}
	srv := fake.start(t)
	pending, _ := startPending(t, srv)

	opts := testOptions(t, srv, time.Second)
	_, pending, err := zaloPollQR(t.Context(), pending, opts, 5*time.Second)
	if err != nil {
		t.Fatalf("scan poll: %v", err)
	}

	result, _, err := zaloPollQR(t.Context(), pending, opts, 5*time.Second)
	if err == nil {
		t.Fatalf("a confirmed QR with no live session was reported as %+v", result)
	}
	if result.Sealed != nil {
		t.Error("a confirmed QR with no live session still sealed a credential")
	}
}

// TestAQRImageThatIsNotInlineIsRefused: the image string is chosen by the
// provider and ends up as the `src` of an <img> on the connecting member's
// screen. A remote URL there makes every login a beacon to whoever answered —
// unvalidated egress, which the layer that receives the value has to stop
// because none of the boundaries after it will.
func TestAQRImageThatIsNotInlineIsRefused(t *testing.T) {
	hostile := map[string]string{
		"a remote URL":            `https://collect.attacker.example/px?m=login`,
		"a protocol-relative URL": `//collect.attacker.example/px`,
		"a bare path":             `/qr/12345.png`,
	}
	for name, image := range hostile {
		t.Run(name, func(t *testing.T) {
			fake := &loginServer{qrImage: image}
			srv := fake.start(t)

			_, _, err := zaloStartQR(t.Context(), testOptions(t, srv, time.Second))
			if err == nil {
				t.Fatal("a QR image that is not an inline data: URL was passed on to the member's browser")
			}
			if !strings.Contains(err.Error(), "data:image/") {
				t.Errorf("error %q does not say what shape the image had to be", err)
			}
		})
	}
}

// TestAQRImagePastTheContractsBoundIsRefused: the field the contract declares is
// capped at 256 KiB, and the response is the one place that cap can be applied
// before the value is copied into a JSON body and then into a browser.
func TestAQRImagePastTheContractsBoundIsRefused(t *testing.T) {
	fake := &loginServer{qrImage: qrImagePrefix + "png;base64," + strings.Repeat("A", maxQRImageBytes)}
	srv := fake.start(t)

	_, _, err := zaloStartQR(t.Context(), testOptions(t, srv, time.Second))
	if err == nil {
		t.Fatal("an oversized QR image was passed on")
	}
	if !strings.Contains(err.Error(), "bound") {
		t.Errorf("error %q does not say the image was too large", err)
	}
}

// TestAProviderThatNeverLongPollsCannotTurnOneBudgetIntoAFlood is the test the
// time-only bound could not fail. The budget assumes each request is a
// long-poll the provider holds open for ~100s — but the provider decides that,
// and answering "nothing yet" instantly is enough to turn five seconds of
// budget into as many requests as the machine can issue.
func TestAProviderThatNeverLongPollsCannotTurnOneBudgetIntoAFlood(t *testing.T) {
	// The ceiling is well above the bound under test, so it only ever catches
	// a poll loop that has already broken its own.
	fake := &loginServer{scanCeiling: 10 * maxPollRequests}
	srv := fake.start(t)
	pending, _ := startPending(t, srv)

	// A clock that does not move: the time bound can never be reached, so only
	// the request bound can stop the loop.
	frozen := zaloOptions{Transport: testOptions(t, srv, 0).Transport, Now: func() time.Time { return fixedTime }}

	before := fake.callCount()
	result, _, err := zaloPollQR(t.Context(), pending, frozen, time.Hour)

	asked := fake.callCount() - before
	if asked > maxPollRequests {
		t.Fatalf("one poll issued %d requests against id.zalo.me, past the %d-request bound", asked, maxPollRequests)
	}
	if asked == 0 {
		t.Fatal("the poll asked nothing at all")
	}
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if result.State != zaloScanWaiting {
		t.Errorf("state = %s, want %s", result.State, zaloScanWaiting)
	}
}

// TestAFailedLoginStillHandsBackTheCookiesItMinted is the guard on the worst
// outcome this handshake can produce: a LIVE Zalo session nobody can withdraw.
//
// checksession is what mints the chat.zalo.me session, and it runs BEFORE the
// liveness check that decides whether the login succeeded. So a login that
// fails after that hop has still created a session against a real person's
// account. If the pending that comes back carries only the bootstrap cookies,
// nothing upstream can persist what was minted, and therefore nothing can ever
// revoke it — the member cannot even see it exists.
func TestAFailedLoginStillHandsBackTheCookiesItMinted(t *testing.T) {
	fake := &loginServer{
		scanAnswers:    []string{scannedAnswer("Ngọc Anh")},
		confirmAnswers: []string{`{"error_code":0,"data":{}}`},
		// The session is minted and then the liveness check refuses it, which
		// is the shape of every failure after checksession.
		logged: false,
	}
	srv := fake.start(t)
	pending, _ := startPending(t, srv)

	opts := testOptions(t, srv, time.Second)
	_, pending, err := zaloPollQR(t.Context(), pending, opts, 5*time.Second)
	if err != nil {
		t.Fatalf("scan poll: %v", err)
	}

	result, next, err := zaloPollQR(t.Context(), pending, opts, 5*time.Second)
	if err == nil {
		t.Fatalf("a login with no live session was reported as %+v", result)
	}
	if result.Sealed != nil {
		t.Error("a failed login sealed a credential")
	}

	var minted bool
	for _, c := range next.Cookies {
		if c.Name == "zpw_sek" && c.Value == "the-session-key" {
			minted = true
		}
	}
	if !minted {
		t.Fatalf("the failed login stranded the session it minted; the pending carries only %+v", next.Cookies)
	}
}

// TestABudgetThatExpiresMidRequestIsAskAgainRatherThanAVerdict: the poll's two
// calls are long-polls Zalo holds for 100 and 120 seconds, so a budget that
// only gates the DECISION to ask lets one stalled request run far past it. When
// our own deadline is what ended the request, the member has not declined and
// the code has not expired — the only honest answer is "ask again".
func TestABudgetThatExpiresMidRequestIsAskAgainRatherThanAVerdict(t *testing.T) {
	// A handler that never answers, and a context the test cancels for it, is
	// the whole point: no sleep, and the request outlives the budget.
	blocked := make(chan struct{})
	fake := &loginServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/account/authen/qr/waiting-scan" {
			<-blocked
			return
		}
		if _, err := w.Write([]byte(fake.answer(r.URL.Path, w))); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer func() { close(blocked); srv.Close() }()

	pending, _ := startPending(t, srv)

	// A tiny budget, spent inside the request rather than before it.
	result, next, err := zaloPollQR(t.Context(), pending, testOptions(t, srv, 0), time.Millisecond)
	if err != nil {
		t.Fatalf("a poll whose own budget expired reported a failure: %v", err)
	}
	if result.State != zaloScanWaiting {
		t.Errorf("state = %s, want %s", result.State, zaloScanWaiting)
	}
	if result.Sealed != nil {
		t.Error("a timed-out poll sealed a credential")
	}
	if next.Scanned {
		t.Error("a timed-out poll advanced the handshake")
	}
}

// TestACallersOwnCancellationIsTheirAnswerToHave: only OUR budget converts to
// "ask again". A caller who cancelled — a closed request, a shutting-down
// worker — gets the error, because pretending their poll is still waiting would
// have them ask again forever.
func TestACallersOwnCancellationIsTheirAnswerToHave(t *testing.T) {
	blocked := make(chan struct{})
	fake := &loginServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/account/authen/qr/waiting-scan" {
			<-blocked
			return
		}
		if _, err := w.Write([]byte(fake.answer(r.URL.Path, w))); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer func() { close(blocked); srv.Close() }()

	pending, _ := startPending(t, srv)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, _, err := zaloPollQR(ctx, pending, testOptions(t, srv, time.Second), 5*time.Second)
	if err == nil {
		t.Fatal("a cancelled poll was reported as waiting")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v does not carry the caller's own cancellation", err)
	}
}
