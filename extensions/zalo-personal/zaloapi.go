// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The three authenticated calls a connector actually needs — liveness, roster,
// send — and the one request shape they share.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// errUnanswered marks a transmission whose fate this process cannot determine:
// the request left, and no answer came back that could be read. It is a
// separate condition from a refusal Zalo sent, and conflating the two either
// loses a message (treating unknown as refused and never retrying) or sends it
// twice (treating it as delivered and retrying anyway). Only the caller holding
// the idempotency record can decide which, so this layer's job is to make the
// distinction tellable — it is deliberately NOT named for the core's
// ErrSendOutcomeUnknown, which is what send.go maps this onto.
//
// It is a CONSTANT rather than an errors.New value because a unit's root
// package may hold no package-level initializer that CALLS anything: an
// initializer runs at import, before the declaration has been validated and
// before anything has decided this unit may run at all. A string-kinded error
// type is comparable, so errors.Is answers about it exactly as it does about a
// sentinel.
const errUnanswered zaloError = "zalo: the request left this process and was never answered, so what Zalo did with it is unknown"

// zaloError is one of the protocol layer's own refusal classes.
type zaloError string

func (e zaloError) Error() string { return string(e) }

// zaloAccount is the liveness answer: whether these cookies are still a session.
type zaloAccount struct {
	LoggedIn    bool
	DisplayName string
	Avatar      string
}

// fetchAccount asks chat.zalo.me whether the cookies in this client's jar are
// still a live session. It is unencrypted and cheap, which is what makes it the
// right thing to run once at the end of a login rather than a call worth
// caching.
func fetchAccount(ctx context.Context, c *client) (zaloAccount, error) {
	raw, err := c.doJSON(ctx, http.MethodGet, "https://jr.chat.zalo.me/jr/userinfo", nil, nil)
	if err != nil {
		return zaloAccount{}, fmt.Errorf("userinfo: %w", err)
	}

	var env struct {
		Data *struct {
			Logged bool `json:"logged"`
			Info   struct {
				Name   string `json:"name"`
				Avatar string `json:"avatar"`
			} `json:"info"`
		} `json:"data"`
		ErrorCode int    `json:"error_code"`
		ErrorMsg  string `json:"error_message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return zaloAccount{}, fmt.Errorf("userinfo did not answer JSON (%s): %w", truncate(string(raw), 120), err)
	}
	if env.ErrorCode != 0 || env.Data == nil {
		return zaloAccount{}, &refusalError{Endpoint: "userinfo", Code: env.ErrorCode, Message: env.ErrorMsg}
	}

	return zaloAccount{
		LoggedIn:    env.Data.Logged,
		DisplayName: env.Data.Info.Name,
		Avatar:      env.Data.Info.Avatar,
	}, nil
}

// call issues an ordinary session-encrypted API request and returns the
// decrypted `data` payload. Two envelopes have to be unwrapped, not one: the
// outer plaintext envelope can refuse before there is anything to decrypt, and
// the decrypted body carries its own error code.
func (s *zaloSession) call(ctx context.Context, method, base string, params map[string]string, form url.Values) ([]byte, error) {
	rawURL, err := makeURL(base, params, true)
	if err != nil {
		return nil, err
	}

	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	var raw []byte
	if body != nil {
		raw, err = s.c.doJSON(ctx, method, rawURL, body, nil)
	} else {
		raw, err = s.c.doJSON(ctx, method, rawURL, nil, nil)
	}
	if err != nil {
		return nil, err
	}

	var env struct {
		Data      string `json:"data"`
		ErrorCode int    `json:"error_code"`
		ErrorMsg  string `json:"error_message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%s did not answer JSON (%s): %w", base, truncate(string(raw), 120), err)
	}
	if env.ErrorCode != 0 {
		return nil, &refusalError{Endpoint: base, Code: env.ErrorCode, Message: env.ErrorMsg}
	}

	plain, err := decodeAES(s.secret, env.Data)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s response: %w", base, err)
	}

	var inner struct {
		Data      json.RawMessage `json:"data"`
		ErrorCode int             `json:"error_code"`
		ErrorMsg  string          `json:"error_message"`
	}
	if err := json.Unmarshal(plain, &inner); err != nil {
		return nil, fmt.Errorf("parse decrypted %s response: %w", base, err)
	}
	if inner.ErrorCode != 0 {
		return nil, &refusalError{Endpoint: base, Code: inner.ErrorCode, Message: inner.ErrorMsg}
	}
	return inner.Data, nil
}

// encryptedParams renders a payload the way every session call takes it: one
// `params` value, JSON encrypted under the session key.
func (s *zaloSession) encryptedParams(payload map[string]any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode params: %w", err)
	}
	encrypted, err := encodeAES(s.secret, string(raw))
	if err != nil {
		return "", fmt.Errorf("encrypt params: %w", err)
	}
	return encrypted, nil
}

// serviceURL resolves a capability to its host. The map is issued per session,
// so a hardcoded host is a session that works until Zalo rebalances.
//
// The host is checked against the allowlist HERE rather than when the map
// arrives, because the map names dozens of capabilities this unit never calls
// and one unrecognised entry among them is no reason to refuse a member's
// session. What must never happen is dialling an unrecognised host WITH the
// member's cookies attached, and that is exactly this function's moment.
func (s *zaloSession) serviceURL(name string) (string, error) {
	urls, ok := s.service[name]
	if !ok || len(urls) == 0 {
		return "", fmt.Errorf("this session was issued no %q service endpoint, so the call has no host to go to", name)
	}

	u, err := url.Parse(urls[0])
	if err != nil {
		return "", fmt.Errorf("this session's %q service endpoint is not a URL: %w", name, err)
	}
	if !isZaloHost(u.Hostname()) {
		return "", fmt.Errorf("refusing to call the %q service at %s: it is not a Zalo host, and the request would carry the member's session", name, u.Hostname())
	}
	return urls[0], nil
}

// zaloReceipt is what a successful send returns, and it is one field because
// the wire gives one: `clientId` and `ts` come back as zero, so the clientId we
// sent is NOT echoed and the receipt cannot correlate our request to Zalo's id
// for us. MsgID is the only usable provider identifier.
type zaloReceipt struct {
	MsgID string
}

// SendText sends a plain text message to a 1:1 thread.
func (s *zaloSession) SendText(ctx context.Context, toUID, body string) (zaloReceipt, error) {
	base, err := s.serviceURL("chat")
	if err != nil {
		return zaloReceipt{}, err
	}

	// EXACTLY these five keys and no others. `visibility` and `grid` are
	// group-only, and a group key on a 1:1 thread is REFUSED (error_code 114,
	// "invalid parameter") rather than ignored — with no hint that the params
	// were the problem, which is why the key set is pinned by a test.
	encrypted, err := s.encryptedParams(map[string]any{
		"message":  body,
		"clientId": s.c.now().UnixMilli(),
		"imei":     s.imei,
		"ttl":      0,
		"toid":     toUID,
	})
	if err != nil {
		return zaloReceipt{}, err
	}

	data, err := s.call(ctx, http.MethodPost, base+"/api/message/sms",
		map[string]string{"nretry": "0"}, url.Values{"params": {encrypted}})
	if err != nil {
		var transport *transportError
		if errors.As(err, &transport) {
			return zaloReceipt{}, fmt.Errorf("%w: to %s: %w", errUnanswered, toUID, err)
		}
		return zaloReceipt{}, err
	}

	var receipt struct {
		MsgID json.Number `json:"msgId"`
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		return zaloReceipt{}, fmt.Errorf("parse send receipt: %w", err)
	}
	return zaloReceipt{MsgID: receipt.MsgID.String()}, nil
}
