// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The QR login handshake against id.zalo.me, ported from zca-js
// (src/apis/loginQR.ts). Nothing here is encrypted — ordinary form posts plus
// two long-polls — which is why this half of the protocol is cheap and the
// session half is not.
//
// The handshake is split into [zaloStartQR] and repeated [zaloPollQR] calls rather than
// one blocking login, because the two long-polls are 100s and 120s and an HTTP
// request that outlives any sensible server timeout is not a design: it is one
// held connection per connecting member. Zalo's own error_code 8 ("nothing has
// happened yet, ask again") is what makes a short bounded poll the protocol's
// idiom rather than a workaround.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// loginScriptPattern reads the login bundle version out of the login page's own
// script tag. Every later call carries it as `v` and a stale value is rejected,
// so it is read fresh on each login rather than pinned in this source.
//
// It compiles on call rather than at import for the same reason zeroIV is a
// function: a package-level var initializer that CALLS anything runs before
// anything has decided this unit may run. The pattern is a literal, so
// MustCompile cannot fail.
func loginScriptPattern() *regexp.Regexp {
	return regexp.MustCompile(`https://stc-zlogin\.zdn\.vn/main-([\d.]+)\.js`)
}

const (
	// waitingErrorCodeRetry is the "nothing yet, ask again" answer to both
	// long-polls. It is not an error.
	waitingErrorCodeRetry = 8

	// qrDeclinedErrorCode is the member tapping "no" on their phone.
	qrDeclinedErrorCode = -13

	// qrLifetime is how long a minted code stays scannable. The server enforces
	// it; tracking it here is what lets a poll answer zaloScanExpired instead of
	// long-polling a dead code.
	qrLifetime = 100 * time.Second

	// continueTarget is the destination the QR is minted for. zalo.me/pc is the
	// one that produces a code the phone accepts: the browser uses
	// chat.zalo.me, but only together with the client-generated-QR flag, and
	// pointing `continue` there alone yields a QR the app calls invalid.
	continueTarget = "https://zalo.me/pc"

	// maxQRImageBytes bounds the image this layer will pass on, matching the
	// contract's own maxLength for the field. The value crosses two more
	// boundaries after this one — a JSON response and a browser's <img src> —
	// and neither of those is the place to discover a provider sent a megabyte.
	maxQRImageBytes = 262144

	// qrImagePrefix is the ONLY form of image this layer accepts, because
	// anything else is a URL the connecting member's browser would fetch. A
	// remote src turns every login screen into a beacon for whoever answered,
	// which is unvalidated egress rather than a rendering preference.
	qrImagePrefix = "data:image/"

	// maxPollRequests bounds how many times one budget may ask. The two
	// long-polls hold the connection for 100s and 120s, so a budget of a few
	// seconds should fit ONE request — but "should" is the provider's choice,
	// not ours: a server answering error_code 8 instantly turns a time-bounded
	// loop into an unbounded request flood against id.zalo.me. A count bounds
	// it whatever the provider does.
	maxPollRequests = 4

	idReferer   = "https://id.zalo.me/account?continue=https%3A%2F%2Fzalo.me%2Fpc"
	chatReferer = "https://id.zalo.me/account?continue=https%3A%2F%2Fchat.zalo.me%2F"
)

// zaloQRCode is what the member scans.
type zaloQRCode struct {
	// ImageDataURL is the server's own "data:image/png;base64,…", passed
	// through untouched to an <img src>. This package renders no QR itself:
	// the token the server hands back under the client-generated flow is NOT
	// the whole QR payload — the real client appends locally generated
	// device-linking material — so a locally drawn code scans as plain text.
	ImageDataURL string
	ExpiresAt    time.Time
}

// zaloPending is the in-flight QR login between [zaloStartQR] and [zaloPollQR]. It holds
// the bootstrap cookie jar, which is credential material, so its caller seals
// it rather than logging it or passing it around.
type zaloPending struct {
	Cookies       []zaloCookie `json:"cookies"`
	IMEI          string       `json:"imei"`
	UserAgent     string       `json:"user_agent"`
	Language      string       `json:"language"`
	Code          string       `json:"code"`
	BundleVersion string       `json:"bundle_version"`
	ExpiresAt     time.Time    `json:"expires_at"`

	// Scanned advances the handshake: the first poll waits for a scan, every
	// poll after it waits for the confirmation. Without it a resumed poll would
	// re-ask a question the phone has already answered.
	Scanned     bool   `json:"scanned"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar"`
}

// zaloScanState is how far the phone has got.
type zaloScanState string

const (
	zaloScanWaiting   zaloScanState = "waiting"
	zaloScanScanned   zaloScanState = "scanned"
	zaloScanConfirmed zaloScanState = "confirmed"
	zaloScanDeclined  zaloScanState = "declined"
	zaloScanExpired   zaloScanState = "expired"
)

// zaloPollResult is what one bounded poll learned.
type zaloPollResult struct {
	State       zaloScanState
	DisplayName string // set from zaloScanScanned onward
	Avatar      string
	Sealed      *zaloSealed // non-nil ONLY on zaloScanConfirmed
}

type qrEnvelope struct {
	Data      json.RawMessage `json:"data"`
	ErrorCode int             `json:"error_code"`
	ErrorMsg  string          `json:"error_message"`
}

// zaloStartQR registers this client as a device and mints a QR for it.
func zaloStartQR(ctx context.Context, opts zaloOptions) (zaloPending, zaloQRCode, error) {
	c := newClient(opts)

	imei, err := newIMEI(c.userAgent)
	if err != nil {
		return zaloPending{}, zaloQRCode{}, err
	}

	version, err := loginBundleVersion(ctx, c)
	if err != nil {
		return zaloPending{}, zaloQRCode{}, err
	}

	// The first two posts are here for their cookies, not their bodies: they
	// are what registers this client as a device before a QR may be minted.
	base := url.Values{"continue": {continueTarget}, "v": {version}}
	if _, err := postForm(ctx, c, "https://id.zalo.me/account/logininfo", base, idReferer); err != nil {
		return zaloPending{}, zaloQRCode{}, fmt.Errorf("logininfo: %w", err)
	}
	verify := url.Values{"type": {"device"}, "continue": {continueTarget}, "v": {version}}
	if _, err := postForm(ctx, c, "https://id.zalo.me/account/verify-client", verify, idReferer); err != nil {
		return zaloPending{}, zaloQRCode{}, fmt.Errorf("verify-client: %w", err)
	}

	gen, err := generateQR(ctx, c, version)
	if err != nil {
		return zaloPending{}, zaloQRCode{}, err
	}

	pending := zaloPending{
		Cookies:       c.jar.export(),
		IMEI:          imei,
		UserAgent:     c.userAgent,
		Language:      opts.language(),
		Code:          gen.Code,
		BundleVersion: version,
		ExpiresAt:     c.now().Add(qrLifetime),
	}
	return pending, zaloQRCode{ImageDataURL: gen.Image, ExpiresAt: pending.ExpiresAt}, nil
}

type qrGenerated struct {
	Code  string `json:"code"`
	Image string `json:"image"`
}

func generateQR(ctx context.Context, c *client, version string) (qrGenerated, error) {
	form := url.Values{"continue": {continueTarget}, "v": {version}}
	env, err := postForm(ctx, c, "https://id.zalo.me/account/authen/qr/generate", form, idReferer)
	if err != nil {
		return qrGenerated{}, fmt.Errorf("generate QR: %w", err)
	}
	if env.ErrorCode != 0 || len(env.Data) == 0 {
		return qrGenerated{}, &refusalError{Endpoint: "generate QR", Code: env.ErrorCode, Message: env.ErrorMsg}
	}

	var gen qrGenerated
	if err := json.Unmarshal(env.Data, &gen); err != nil {
		return qrGenerated{}, fmt.Errorf("decode QR payload: %w", err)
	}
	if gen.Code == "" || gen.Image == "" {
		return qrGenerated{}, fmt.Errorf("generate QR answered without a code or an image, so there is nothing to show the member")
	}
	if !strings.HasPrefix(gen.Image, qrImagePrefix) {
		return qrGenerated{}, fmt.Errorf("generate QR answered with an image this layer will not pass on: it must be an inline %s… URL, and a remote one would make the member's browser fetch it", qrImagePrefix)
	}
	if len(gen.Image) > maxQRImageBytes {
		return qrGenerated{}, fmt.Errorf("generate QR answered with a %d-byte image, past the %d-byte bound the contract declares", len(gen.Image), maxQRImageBytes)
	}
	return gen, nil
}

// zaloPollQR runs ONE bounded poll of the handshake and returns what it learned,
// together with the zaloPending to carry into the next call. A zaloScanWaiting or
// zaloScanScanned result means "ask again"; the other three are terminal.
//
// It is bounded twice, by TIME and by REQUEST COUNT, and it needs both: the
// budget assumes each request is a long-poll the provider holds open, and the
// provider is the one who decides that. Answering "nothing yet" instantly is
// enough to turn a time-only bound into as many requests as the machine can
// issue in five seconds.
func zaloPollQR(ctx context.Context, p zaloPending, opts zaloOptions, budget time.Duration) (zaloPollResult, zaloPending, error) {
	opts.UserAgent = p.UserAgent
	c := newClient(opts)
	c.jar.load(p.Cookies)

	deadline := c.now().Add(budget)
	for asked := 0; asked < maxPollRequests && c.now().Before(deadline); asked++ {
		if !p.ExpiresAt.IsZero() && !c.now().Before(p.ExpiresAt) {
			return zaloPollResult{State: zaloScanExpired, DisplayName: p.DisplayName, Avatar: p.Avatar}, p, nil
		}

		// The budget has to bound the REQUEST, not just the decision to make
		// one. These calls are long-polls Zalo holds for 100 and 120 seconds,
		// so a loop that only checks the clock between requests runs a whole
		// long-poll past its budget and keeps a caller's connection with it.
		reqCtx, cancel := context.WithTimeout(ctx, deadline.Sub(c.now()))
		result, done, err := pollOnce(reqCtx, c, &p)
		cancel()

		// EVERY return after this point carries the jar, including the error
		// ones. A request that failed may still have minted cookies — the
		// checksession hop inside completeLogin is exactly that — and a live
		// Zalo session this process forgets is one the member cannot withdraw,
		// because nothing upstream ever learns it exists.
		p.Cookies = c.jar.export()

		if err != nil {
			// Our own budget running out mid-request is not a verdict. The
			// member has not declined and the code has not expired; the caller
			// simply has to ask again, which is the same answer as "nothing
			// yet". A caller who cancelled for their own reasons gets the
			// error, because that is their answer to have.
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				break
			}
			return zaloPollResult{}, p, err
		}
		if done {
			return result, p, nil
		}
	}

	p.Cookies = c.jar.export()
	state := zaloScanWaiting
	if p.Scanned {
		state = zaloScanScanned
	}
	return zaloPollResult{State: state, DisplayName: p.DisplayName, Avatar: p.Avatar}, p, nil
}

// pollOnce issues one long-poll. done=false means the server answered "nothing
// yet" and the caller may spend more of its budget asking again.
func pollOnce(ctx context.Context, c *client, p *zaloPending) (zaloPollResult, bool, error) {
	if !p.Scanned {
		return pollScan(ctx, c, p)
	}
	return pollConfirm(ctx, c, p)
}

func pollScan(ctx context.Context, c *client, p *zaloPending) (zaloPollResult, bool, error) {
	form := url.Values{"code": {p.Code}, "continue": {"https://chat.zalo.me/"}, "v": {p.BundleVersion}}
	env, err := postForm(ctx, c, "https://id.zalo.me/account/authen/qr/waiting-scan", form, chatReferer)
	if err != nil {
		return zaloPollResult{}, false, fmt.Errorf("waiting-scan: %w", err)
	}
	if env.ErrorCode == waitingErrorCodeRetry {
		return zaloPollResult{}, false, nil
	}
	if env.ErrorCode != 0 || len(env.Data) == 0 {
		return zaloPollResult{}, false, &refusalError{Endpoint: "waiting-scan", Code: env.ErrorCode, Message: env.ErrorMsg}
	}

	var scanned struct {
		Avatar      string `json:"avatar"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(env.Data, &scanned); err != nil {
		return zaloPollResult{}, false, fmt.Errorf("decode scan payload: %w", err)
	}

	p.Scanned = true
	p.DisplayName = scanned.DisplayName
	p.Avatar = scanned.Avatar
	return zaloPollResult{State: zaloScanScanned, DisplayName: scanned.DisplayName, Avatar: scanned.Avatar}, true, nil
}

func pollConfirm(ctx context.Context, c *client, p *zaloPending) (zaloPollResult, bool, error) {
	form := url.Values{
		"code":     {p.Code},
		"gToken":   {""},
		"gAction":  {"CONFIRM_QR"},
		"continue": {"https://chat.zalo.me/"},
		"v":        {p.BundleVersion},
	}
	env, err := postForm(ctx, c, "https://id.zalo.me/account/authen/qr/waiting-confirm", form, chatReferer)
	if err != nil {
		return zaloPollResult{}, false, fmt.Errorf("waiting-confirm: %w", err)
	}

	switch env.ErrorCode {
	case waitingErrorCodeRetry:
		return zaloPollResult{}, false, nil
	case qrDeclinedErrorCode:
		return zaloPollResult{State: zaloScanDeclined, DisplayName: p.DisplayName, Avatar: p.Avatar}, true, nil
	case 0:
		sealed, account, err := completeLogin(ctx, c, p)
		if err != nil {
			return zaloPollResult{}, false, err
		}
		return zaloPollResult{
			State:       zaloScanConfirmed,
			DisplayName: account.DisplayName,
			Avatar:      account.Avatar,
			Sealed:      &sealed,
		}, true, nil
	default:
		return zaloPollResult{}, false, &refusalError{Endpoint: "waiting-confirm", Code: env.ErrorCode, Message: env.ErrorMsg}
	}
}

// completeLogin converts the confirmed QR into chat.zalo.me session cookies.
// checksession's body is worthless; its Set-Cookie headers are the entire
// credential, which is why this reads a response it never parses.
func completeLogin(ctx context.Context, c *client, p *zaloPending) (zaloSealed, zaloAccount, error) {
	resp, err := c.do(ctx, http.MethodGet,
		"https://id.zalo.me/account/checksession?continue=https%3A%2F%2Fchat.zalo.me%2Findex.html", nil,
		map[string]string{"Accept": "text/html,application/xhtml+xml,*/*;q=0.8", "Referer": idReferer})
	if err != nil {
		return zaloSealed{}, zaloAccount{}, fmt.Errorf("checksession: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return zaloSealed{}, zaloAccount{}, fmt.Errorf("checksession: close body: %w", err)
	}

	account, err := fetchAccount(ctx, c)
	if err != nil {
		return zaloSealed{}, zaloAccount{}, err
	}
	if !account.LoggedIn {
		return zaloSealed{}, zaloAccount{}, fmt.Errorf("the phone confirmed the QR but chat.zalo.me does not see a logged-in session — the handshake left no usable cookies")
	}

	sealed := zaloSealed{
		Cookies:   c.jar.export(),
		IMEI:      p.IMEI,
		UserAgent: p.UserAgent,
		Language:  p.Language,
	}
	if account.DisplayName == "" {
		account.DisplayName = p.DisplayName
	}
	return sealed, account, nil
}

// loginBundleVersion loads the login page and reads the bundle version out of
// it.
func loginBundleVersion(ctx context.Context, c *client) (string, error) {
	body, err := c.doJSON(ctx, http.MethodGet, "https://id.zalo.me/account?continue=https%3A%2F%2Fchat.zalo.me%2F", nil,
		map[string]string{
			"Accept":  "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			"Referer": "https://chat.zalo.me/",
		})
	if err != nil {
		return "", fmt.Errorf("login page: %w", err)
	}

	match := loginScriptPattern().FindSubmatch(body)
	if match == nil {
		return "", fmt.Errorf("no login bundle version in the login page — Zalo changed the page, or served a challenge instead of it")
	}
	return string(match[1]), nil
}

func postForm(ctx context.Context, c *client, rawURL string, form url.Values, referer string) (*qrEnvelope, error) {
	raw, err := c.doJSON(ctx, http.MethodPost, rawURL, strings.NewReader(form.Encode()), map[string]string{
		"Origin":  "https://id.zalo.me",
		"Referer": referer,
	})
	if err != nil {
		return nil, err
	}

	var env qrEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%s did not answer JSON (%s): %w", rawURL, truncate(string(raw), 120), err)
	}
	return &env, nil
}
