// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// What a logged-in personal Zalo account IS, and the handshake that makes it
// usable again after a restart.
//
// The split this file exists to hold: [zaloSealed] is the credential — cookies,
// the device identity, and the user agent the identity was derived from — and
// it is the ONLY thing worth persisting. Everything on [zaloSession] (the
// session key, the per-capability host map) is re-derived by [zaloResume] on
// every use, because both go stale and a cached value that goes stale silently
// is how a connector reports "connected" while transmitting nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// zaloSealed is the whole credential, and the only part of a session worth sealing.
type zaloSealed struct {
	Cookies   []zaloCookie `json:"cookies"`
	IMEI      string       `json:"imei"`
	UserAgent string       `json:"user_agent"`
	Language  string       `json:"language"`
}

// zaloOptions tunes a call chain. The zero value is the production configuration;
// Transport and Now are the seams every test in this package drives.
type zaloOptions struct {
	UserAgent string            // empty = the pinned default
	Language  string            // empty = "vi"
	Transport http.RoundTripper // nil = http.DefaultTransport
	Now       func() time.Time  // nil = time.Now

	// After is the one seam the message socket needs beyond a clock: the drain
	// decides the queue has gone quiet by waiting, and the ping loop fires on
	// the server's own interval. A test that had to spend real seconds on
	// either would be a test that flakes on a loaded machine, so the waiting
	// itself is injectable. nil = time.After.
	After func(time.Duration) <-chan time.Time
}

func (o zaloOptions) userAgent() string {
	if o.UserAgent == "" {
		return defaultUserAgent
	}
	return o.UserAgent
}

func (o zaloOptions) language() string {
	if o.Language == "" {
		return defaultLanguage
	}
	return o.Language
}

func (o zaloOptions) transport() http.RoundTripper {
	if o.Transport == nil {
		return http.DefaultTransport
	}
	return o.Transport
}

func (o zaloOptions) clock() func() time.Time {
	if o.Now == nil {
		return time.Now
	}
	return o.Now
}

func (o zaloOptions) afterFunc() func(time.Duration) <-chan time.Time {
	if o.After == nil {
		return time.After
	}
	return o.After
}

// zaloSession is everything zaloResume DERIVES from a zaloSealed. None of it is sealed.
type zaloSession struct {
	c        *client
	imei     string
	secret   string
	uid      string
	service  map[string][]string
	wsURLs   []string
	resumeAt time.Time

	// The two seams the message socket runs on, carried here because the drain
	// hangs off a session rather than off a constructor a test can reach:
	// [zaloSession.dial] is which websocket client opens the socket (DESIGN
	// §9.6 keeps that swappable), and after is how the drain waits.
	dial  zaloDialer
	after func(time.Duration) <-chan time.Time
}

// UID is the account's own numeric id — the `toid` a contact would address to
// reach this member.
func (s *zaloSession) UID() string { return s.uid }

// UserAgent is the string the imei was derived from, and the one Zalo matches
// the device identity against.
func (s *zaloSession) UserAgent() string { return s.c.userAgent }

// zaloResume trades a sealed credential for a working session: getLoginInfo
// yields the session key every other call encrypts with, and the map of which
// host serves which capability. It runs on every process start, and on every
// send — there is no way to skip it, which is why it makes exactly one request.
func zaloResume(ctx context.Context, sealed zaloSealed, opts zaloOptions) (*zaloSession, error) {
	if sealed.IMEI == "" || len(sealed.Cookies) == 0 || sealed.UserAgent == "" {
		return nil, fmt.Errorf("this credential is missing part of itself (imei, cookies, user agent) — the member has to scan a QR again")
	}

	// The sealed user agent wins over the caller's: the imei is derived from
	// it, so presenting a different one turns a live credential into a
	// mismatched identity the server rejects.
	opts.UserAgent = sealed.UserAgent
	if sealed.Language != "" {
		opts.Language = sealed.Language
	}

	c := newClient(opts)
	c.jar.load(sealed.Cookies)

	info, err := getLoginInfo(ctx, c, sealed.IMEI, opts.language())
	if err != nil {
		return nil, err
	}
	if info.ZPWEnk == "" {
		return nil, fmt.Errorf("login info carried no session key — the cookies are stale, the member has to scan a QR again")
	}

	return &zaloSession{
		c:        c,
		imei:     sealed.IMEI,
		secret:   info.ZPWEnk,
		uid:      info.UID,
		service:  info.ZPWServiceV3,
		wsURLs:   info.ZPWWS,
		resumeAt: c.now(),
		dial:     dialZaloSocket,
		after:    opts.afterFunc(),
	}, nil
}

type loginInfo struct {
	UID          string              `json:"uid"`
	ZPWEnk       string              `json:"zpw_enk"`
	ZPWServiceV3 map[string][]string `json:"zpw_service_map_v3"`

	// The message socket's endpoints. Zalo issues a handful and they are
	// per-session, so a hardcoded one is a connector that works until Zalo
	// rebalances its fleet.
	ZPWWS []string `json:"zpw_ws"`
}

// getLoginInfo is the one call encrypted under the ephemeral login key rather
// than the session key — the session key is what it returns.
func getLoginInfo(ctx context.Context, c *client, imei, language string) (*loginInfo, error) {
	now := c.now().UnixMilli()

	enc, err := newParamsEncryptor(apiType, imei, now)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(map[string]any{
		"computer_name": "Web",
		"imei":          imei,
		"language":      language,
		"ts":            now,
	})
	if err != nil {
		return nil, fmt.Errorf("encode login params: %w", err)
	}

	encrypted, err := encodeAESBase64(enc.encryptKey, string(payload))
	if err != nil {
		return nil, fmt.Errorf("encrypt login params: %w", err)
	}

	params := map[string]string{
		"zcid":           enc.zcid,
		"zcid_ext":       enc.zcidExt,
		"enc_ver":        enc.encVer,
		"params":         encrypted,
		"type":           fmt.Sprint(apiType),
		"client_version": fmt.Sprint(apiVersion),
	}
	// signkey is computed over the params BEFORE nretry joins them, because
	// that is the set the server signs too.
	params["signkey"] = getSignKey("getlogininfo", params)
	params["nretry"] = "0"

	rawURL, err := makeURL("https://wpa.chat.zalo.me/api/login/getLoginInfo", params, true)
	if err != nil {
		return nil, err
	}

	raw, err := c.doJSON(ctx, http.MethodGet, rawURL, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("getLoginInfo: %w", err)
	}
	return parseLoginInfo(raw, enc.encryptKey)
}

func parseLoginInfo(raw []byte, encryptKey string) (*loginInfo, error) {
	var env struct {
		Data      string `json:"data"`
		ErrorCode int    `json:"error_code"`
		ErrorMsg  string `json:"error_message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("getLoginInfo did not answer JSON (%s): %w", truncate(string(raw), 120), err)
	}
	if env.ErrorCode != 0 {
		return nil, &refusalError{Endpoint: "getLoginInfo", Code: env.ErrorCode, Message: env.ErrorMsg}
	}

	plain, err := decryptLoginResp(encryptKey, env.Data)
	if err != nil {
		return nil, fmt.Errorf("decrypt login info: %w", err)
	}

	var body struct {
		Data      loginInfo `json:"data"`
		ErrorCode int       `json:"error_code"`
		ErrorMsg  string    `json:"error_message"`
	}
	if err := json.Unmarshal(plain, &body); err != nil {
		return nil, fmt.Errorf("parse decrypted login info: %w", err)
	}
	if body.ErrorCode != 0 {
		return nil, &refusalError{Endpoint: "getLoginInfo", Code: body.ErrorCode, Message: body.ErrorMsg}
	}
	return &body.Data, nil
}

// zaloServerSettings is the socket configuration the server hands out
// separately from the session key.
type zaloServerSettings struct {
	Features struct {
		Socket struct {
			PingInterval int `json:"ping_interval"`
		} `json:"socket"`
	} `json:"features"`
}

// pingInterval is how often the message socket expects a keep-alive. The
// fallback is not a guess at Zalo's value but a floor: pinging more often than
// asked keeps a socket alive, whereas pinging less often loses it, so an
// answer we could not read must err on the frequent side.
func (s *zaloServerSettings) pingInterval() time.Duration {
	if s == nil || s.Features.Socket.PingInterval <= 0 {
		return time.Minute
	}
	return time.Duration(s.Features.Socket.PingInterval) * time.Millisecond
}

// getServerInfo returns the socket settings. It is deliberately NOT part of
// zaloResume: resume runs on every send, and a send has no use for a ping
// interval, so folding this in would double the request cost of the hot path.
// The message drain calls it because it is the one caller that reads the
// answer — which is also why the call exists again at all (issue #1644 removed
// it as unread surface when nothing held a socket).
//
// It is unencrypted and signed over a DIFFERENT parameter set than every other
// call — the signature covers four keys and the query carries those four plus
// the signature, with no version pair — which is the kind of inconsistency a
// port has to reproduce exactly rather than tidy up.
func getServerInfo(ctx context.Context, c *client, imei string) (*zaloServerSettings, error) {
	signed := map[string]string{
		"imei":           imei,
		"type":           fmt.Sprint(apiType),
		"client_version": fmt.Sprint(apiVersion),
		"computer_name":  "Web",
	}
	params := make(map[string]string, len(signed)+1)
	for k, v := range signed {
		params[k] = v
	}
	params["signkey"] = getSignKey("getserverinfo", signed)

	rawURL, err := makeURL("https://wpa.chat.zalo.me/api/login/getServerInfo", params, false)
	if err != nil {
		return nil, err
	}

	raw, err := c.doJSON(ctx, http.MethodGet, rawURL, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("getServerInfo: %w", err)
	}
	return parseServerInfo(raw)
}

func parseServerInfo(raw []byte) (*zaloServerSettings, error) {
	var env struct {
		Data *struct {
			Settings *zaloServerSettings `json:"settings"`
			// Zalo misspells the key; both spellings are in the wild, and a
			// reader that knows only the correct one silently gets the
			// fallback interval on live traffic.
			Setttings *zaloServerSettings `json:"setttings"`
		} `json:"data"`
		ErrorCode int    `json:"error_code"`
		ErrorMsg  string `json:"error_message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("getServerInfo did not answer JSON (%s): %w", truncate(string(raw), 120), err)
	}
	if env.ErrorCode != 0 || env.Data == nil {
		return nil, &refusalError{Endpoint: "getServerInfo", Code: env.ErrorCode, Message: env.ErrorMsg}
	}

	if env.Data.Settings != nil {
		return env.Data.Settings, nil
	}
	if env.Data.Setttings != nil {
		return env.Data.Setttings, nil
	}
	// Neither spelling present is not an error: the caller's only use for this
	// answer is the ping interval, and the zero value reports the floor.
	return &zaloServerSettings{}, nil
}
