// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The signed `state` for the connector OAuth handshake. The callback is
// session-less (SameSite=Strict means the crm_session cookie is not sent on
// the provider's cross-site redirect), so `state` is the only trustworthy
// carrier of WHO started the flow. We HMAC-sign (workspace, user, provider,
// expiry) with a server-only key: the callback recovers that tuple from a
// state it can verify it minted, sets the workspace GUC + a human principal
// from it, and persists the connection. Forgery (e.g. swapping in a victim's
// user id) fails the MAC; replay past the TTL fails the expiry; replay within
// the TTL is bounded by the provider's own single-use authorization code.

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// connectState is the tuple bound into a signed OAuth state parameter.
type connectState struct {
	Workspace ids.UUID
	User      ids.UUID
	Provider  string
}

// wireState is the JSON form actually signed — ids.UUID as strings, plus the
// expiry, so the payload is self-describing and version-tolerant.
type wireState struct {
	Workspace string `json:"ws"`
	User      string `json:"u"`
	Provider  string `json:"p"`
	Exp       int64  `json:"exp"` // unix seconds
}

// stateSigner mints and verifies signed state tokens with an HMAC key.
type stateSigner struct{ key []byte }

func newStateSigner(key []byte) stateSigner { return stateSigner{key: key} }

// sign returns `base64url(payload).base64url(hmac(payload))`, binding the
// tuple until exp.
func (s stateSigner) sign(st connectState, exp time.Time) string {
	payload, _ := json.Marshal(wireState{ //nolint:errchkjson // string/int-only struct never errors
		Workspace: st.Workspace.String(),
		User:      st.User.String(),
		Provider:  st.Provider,
		Exp:       exp.Unix(),
	})
	enc := base64.RawURLEncoding.EncodeToString(payload)
	return enc + "." + base64.RawURLEncoding.EncodeToString(s.mac(enc))
}

// verify checks the signature and expiry against now and returns the bound
// tuple. Every failure mode is an error, never a partial result.
func (s stateSigner) verify(token string, now time.Time) (connectState, error) {
	enc, macPart, ok := strings.Cut(token, ".")
	if !ok {
		return connectState{}, errors.New("connector state: malformed token")
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(macPart)
	if err != nil {
		return connectState{}, fmt.Errorf("connector state: bad signature encoding: %w", err)
	}
	if subtle.ConstantTimeCompare(gotMAC, s.mac(enc)) != 1 {
		return connectState{}, errors.New("connector state: signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return connectState{}, fmt.Errorf("connector state: bad payload encoding: %w", err)
	}
	var w wireState
	if err := json.Unmarshal(payload, &w); err != nil {
		return connectState{}, fmt.Errorf("connector state: bad payload: %w", err)
	}
	if now.Unix() > w.Exp {
		return connectState{}, errors.New("connector state: expired")
	}
	ws, err := ids.Parse(w.Workspace)
	if err != nil {
		return connectState{}, fmt.Errorf("connector state: bad workspace id: %w", err)
	}
	user, err := ids.Parse(w.User)
	if err != nil {
		return connectState{}, fmt.Errorf("connector state: bad user id: %w", err)
	}
	return connectState{Workspace: ws, User: user, Provider: w.Provider}, nil
}

func (s stateSigner) mac(enc string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(enc))
	return m.Sum(nil)
}
