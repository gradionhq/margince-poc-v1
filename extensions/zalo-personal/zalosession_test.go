// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// zaloResume against a fake wpa.chat.zalo.me that performs the real handshake
// crypto: it derives the ephemeral login key from the zcid pair it was sent,
// exactly as Zalo does, and encrypts its answer under it. A fake that just
// returned plaintext would leave the one call this package cannot verify
// against a vector — the round trip — untested.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testUID      = "1234567890123456789"
	testChatHost = "https://tw-chat.zalo.me"
)

// chatServer is a fake of the authenticated surface: the handshake pair plus
// whichever session-encrypted endpoint a test wires into calls.
type chatServer struct {
	sessionKey string
	logged     bool

	// chatHost is the host the login info names for the `chat` capability.
	// A provider chooses this, so a test gets to choose it too.
	chatHost string

	// calls answers a session-encrypted endpoint. It receives the DECRYPTED
	// params so a test can assert on what was actually sent.
	calls map[string]func(t *testing.T, params map[string]any) string
}

func newChatServer() *chatServer {
	return &chatServer{
		sessionKey: vectorSessionKey,
		logged:     true,
		chatHost:   testChatHost,
		calls:      map[string]func(*testing.T, map[string]any) string{},
	}
}

func (s *chatServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := s.answer(t, r)
		if err != nil {
			t.Errorf("fake chat server: %v", err)
			http.Error(w, "fake server failed", http.StatusInternalServerError)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write %s response: %v", r.URL.Path, err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *chatServer) answer(t *testing.T, r *http.Request) (string, error) {
	t.Helper()
	switch r.URL.Path {
	case "/api/login/getLoginInfo":
		return s.loginInfo(r.URL.Query())
	case "/jr/userinfo":
		return fmt.Sprintf(`{"error_code":0,"data":{"logged":%t,"session_chat_valid":true,"info":{"name":"Ngọc Anh","avatar":"https://avatar"}}}`,
			s.logged), nil
	}

	handler, ok := s.calls[r.URL.Path]
	if !ok {
		return "", fmt.Errorf("no handler wired for %s", r.URL.Path)
	}
	params, err := s.decryptParams(r)
	if err != nil {
		return "", err
	}
	return s.seal(handler(t, params))
}

// loginInfo answers the one call encrypted under the ephemeral key. The server
// can do this because the key is DERIVED from the zcid pair the client sends —
// which is exactly why the scheme is obfuscation and not secrecy.
func (s *chatServer) loginInfo(q url.Values) (string, error) {
	enc := &paramsEncryptor{zcid: q.Get("zcid"), zcidExt: q.Get("zcid_ext"), encVer: "v2"}
	if err := enc.deriveEncryptKey(); err != nil {
		return "", fmt.Errorf("derive the client's login key: %w", err)
	}

	payload := fmt.Sprintf(
		`{"error_code":0,"data":{"uid":%q,"zpw_enk":%q,"zpw_service_map_v3":{"chat":[%q]}}}`,
		testUID, s.sessionKey, s.chatHost)

	sealed, err := encodeAESBase64(enc.encryptKey, payload)
	if err != nil {
		return "", fmt.Errorf("encrypt login info: %w", err)
	}
	return fmt.Sprintf(`{"error_code":0,"data":%q}`, sealed), nil
}

func (s *chatServer) decryptParams(r *http.Request) (map[string]any, error) {
	encrypted := r.URL.Query().Get("params")
	if encrypted == "" {
		if err := r.ParseForm(); err != nil {
			return nil, fmt.Errorf("parse form: %w", err)
		}
		encrypted = r.PostForm.Get("params")
	}
	plain, err := decodeAES(s.sessionKey, encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt request params: %w", err)
	}
	var params map[string]any
	if err := json.Unmarshal(plain, &params); err != nil {
		return nil, fmt.Errorf("parse request params: %w", err)
	}
	return params, nil
}

// seal wraps a handler's plaintext answer the way a real response arrives: an
// encrypted inner envelope inside a plaintext outer one.
func (s *chatServer) seal(inner string) (string, error) {
	sealed, err := encodeAES(s.sessionKey, inner)
	if err != nil {
		return "", fmt.Errorf("encrypt response: %w", err)
	}
	return fmt.Sprintf(`{"error_code":0,"data":%q}`, sealed), nil
}

func testSealed() zaloSealed {
	return zaloSealed{
		Cookies:   []zaloCookie{{Name: "zpw_sek", Value: "the-session-key", Domain: "chat.zalo.me", Path: "/"}},
		IMEI:      vectorIMEI,
		UserAgent: defaultUserAgent,
		Language:  "vi",
	}
}

func resumeAgainst(t *testing.T, fake *chatServer) *zaloSession {
	t.Helper()
	srv := fake.start(t)
	session, err := zaloResume(t.Context(), testSealed(), testOptions(t, srv, time.Second))
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	return session
}

func TestResumeDerivesEverythingItNeedsFromTheSealedCredential(t *testing.T) {
	session := resumeAgainst(t, newChatServer())

	if session.UID() != testUID {
		t.Errorf("uid = %q, want %q", session.UID(), testUID)
	}
	if host, err := session.serviceURL("chat"); err != nil || host != testChatHost {
		t.Errorf("chat host = %q (%v), want %q", host, err, testChatHost)
	}
	if session.UserAgent() != defaultUserAgent {
		t.Errorf("user agent = %q", session.UserAgent())
	}
}

func TestResumeRefusesACredentialThatIsMissingPartOfItself(t *testing.T) {
	cases := map[string]func(s *zaloSealed){
		"no imei":       func(s *zaloSealed) { s.IMEI = "" },
		"no cookies":    func(s *zaloSealed) { s.Cookies = nil },
		"no user agent": func(s *zaloSealed) { s.UserAgent = "" },
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			sealed := testSealed()
			damage(&sealed)
			if _, err := zaloResume(t.Context(), sealed, zaloOptions{}); err == nil {
				t.Fatal("an incomplete credential resumed a session")
			}
		})
	}
}

// TestResumePresentsTheSealedUserAgentRatherThanTheCallers is the guard on the
// imei binding: the identity is MD5 of the agent that minted it, so a caller
// overriding it would present a mismatched device.
func TestResumePresentsTheSealedUserAgentRatherThanTheCallers(t *testing.T) {
	fake := newChatServer()
	srv := fake.start(t)

	sealed := testSealed()
	sealed.UserAgent = "Mozilla/5.0 (the agent this credential was minted under)"

	opts := testOptions(t, srv, time.Second)
	opts.UserAgent = "Mozilla/5.0 (something the caller preferred)"

	session, err := zaloResume(t.Context(), sealed, opts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if session.UserAgent() != sealed.UserAgent {
		t.Errorf("session presents %q, want the sealed agent %q", session.UserAgent(), sealed.UserAgent)
	}
}

// TestResumeReadsBothSpellingsOfTheSettingsKey: Zalo misspells `settings` as
// `setttings`, and reading only one silently falls back to a default ping
// interval that is three times too fast.
func TestStaleCookiesAreReportedAsStaleRatherThanAsAnEmptyKey(t *testing.T) {
	fake := newChatServer()
	fake.sessionKey = ""
	srv := fake.start(t)

	_, err := zaloResume(t.Context(), testSealed(), testOptions(t, srv, time.Second))
	if err == nil {
		t.Fatal("a login info answer with no session key produced a session")
	}
	if !strings.Contains(err.Error(), "scan a QR again") {
		t.Errorf("error %q does not tell the operator what to do about it", err)
	}
}

func TestARefusedHandshakeSurfacesAsARefusal(t *testing.T) {
	for _, path := range []string{"/api/login/getLoginInfo"} {
		t.Run(path, func(t *testing.T) {
			fake := newChatServer()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := `{"error_code":102,"error_message":"the session key was improperly submitted"}`
				if r.URL.Path != path {
					var err error
					if body, err = fake.answer(t, r); err != nil {
						t.Errorf("fake chat server: %v", err)
						return
					}
				}
				if _, err := w.Write([]byte(body)); err != nil {
					t.Errorf("write response: %v", err)
				}
			}))
			defer srv.Close()

			_, err := zaloResume(t.Context(), testSealed(), testOptions(t, srv, time.Second))
			var refusal *refusalError
			if !errors.As(err, &refusal) || refusal.Code != 102 {
				t.Fatalf("refusal surfaced as %v, want a refusalError carrying error_code 102", err)
			}
		})
	}
}

// TestSealedIsReReadFromTheJarSoARotatedCookieIsKept: the login chain and every
// later call may rotate a cookie, and re-sealing the credential a caller
// started with would persist one the server has already retired.
