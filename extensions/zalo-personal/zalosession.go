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

// zaloSession is everything zaloResume DERIVES from a zaloSealed. None of it is sealed.
type zaloSession struct {
	c        *client
	imei     string
	secret   string
	uid      string
	service  map[string][]string
	sealed   zaloSealed
	resumeAt time.Time
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
		sealed:   sealed,
		resumeAt: c.now(),
	}, nil
}

type loginInfo struct {
	UID          string              `json:"uid"`
	ZPWEnk       string              `json:"zpw_enk"`
	ZPWServiceV3 map[string][]string `json:"zpw_service_map_v3"`
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
