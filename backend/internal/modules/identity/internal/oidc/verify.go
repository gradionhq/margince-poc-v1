// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package oidc

// ID-token validation (OpenID Connect Core §3.1.3.7). Everything here is a
// refusal: the token arrived through the browser, so no claim in it is
// worth anything until the signature and every bound value check out.

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// algRS256 is the ONE signature algorithm this relying party accepts.
// Pinning it is the whole defence against algorithm confusion: `none`
// would make an unsigned token valid, and an HMAC alg would let the
// provider's PUBLIC key double as the verification secret.
const algRS256 = "RS256"

// clockSkew is the tolerance applied to both expiry and issued-at. Small
// on purpose: an ID token is redeemed seconds after it is minted.
const clockSkew = time.Minute

// maxTokenAge bounds `iat`: a correctly-signed token from last week is
// still not a fresh authentication.
const maxTokenAge = 10 * time.Minute

// Identity is what a validated ID token proves. Issuer+Subject are the
// permanent binding key; Email is the one-time claim allowlist and the
// display fallback; HostedDomain is the provider's own organization claim,
// which outranks parsing the email.
type Identity struct {
	Issuer       string
	Subject      string
	Email        string
	Name         string
	HostedDomain string
}

// idTokenClaims is the claim subset this validation reads.
type idTokenClaims struct {
	Issuer        string          `json:"iss"`
	Subject       string          `json:"sub"`
	Audience      json.RawMessage `json:"aud"`
	AuthorizedTo  string          `json:"azp"`
	ExpiresAt     int64           `json:"exp"`
	IssuedAt      int64           `json:"iat"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified *bool           `json:"email_verified"`
	Name          string          `json:"name"`
	HostedDomain  string          `json:"hd"`
}

// jwsHeader is the protected header. `crit` is read only to refuse it: a
// token that declares an extension we must understand, and we do not.
type jwsHeader struct {
	Algorithm string   `json:"alg"`
	KeyID     string   `json:"kid"`
	Type      string   `json:"typ"`
	Critical  []string `json:"crit"`
}

// VerifyIDToken validates the token in full and returns the identity it
// proves. expectedNonce is the value stored with the login state; a token
// whose nonce does not match it is a replay, not an authentication.
func (p *Provider) VerifyIDToken(ctx context.Context, rawToken, expectedNonce string) (Identity, error) {
	signingInput, header, payload, err := splitJWS(rawToken)
	if err != nil {
		return Identity{}, err
	}
	if header.Algorithm != algRS256 {
		return Identity{}, fmt.Errorf("%w: signature algorithm %q is not %s", ErrTokenInvalid, header.Algorithm, algRS256)
	}
	if len(header.Critical) > 0 {
		return Identity{}, fmt.Errorf("%w: token declares critical extensions this relying party does not implement", ErrTokenInvalid)
	}
	if header.Type != "" && !strings.EqualFold(header.Type, "JWT") {
		return Identity{}, fmt.Errorf("%w: token type %q is not JWT", ErrTokenInvalid, header.Type)
	}
	if header.KeyID == "" {
		return Identity{}, fmt.Errorf("%w: token names no signing key", ErrTokenInvalid)
	}

	if err := p.verifySignature(ctx, header.KeyID, signingInput, payload.signature); err != nil {
		return Identity{}, err
	}

	var claims idTokenClaims
	if err := json.Unmarshal(payload.claims, &claims); err != nil {
		return Identity{}, fmt.Errorf("%w: claim set does not decode", ErrTokenInvalid)
	}
	if err := p.validateClaims(claims, expectedNonce); err != nil {
		return Identity{}, err
	}
	return Identity{
		Issuer:       claims.Issuer,
		Subject:      claims.Subject,
		Email:        strings.ToLower(claims.Email),
		Name:         claims.Name,
		HostedDomain: strings.ToLower(claims.HostedDomain),
	}, nil
}

// verifySignature checks the RS256 signature, refreshing the key set once
// if the token names a key the cache predates (a rotation mid-flight is
// ordinary provider behaviour, not an attack).
func (p *Provider) verifySignature(ctx context.Context, kid, signingInput string, signature []byte) error {
	key, err := p.signingKey(ctx, kid, false)
	if errors.Is(err, errUnknownKeyID) {
		key, err = p.signingKey(ctx, kid, true)
	}
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return fmt.Errorf("%w: signature does not verify against the provider's key %q", ErrTokenInvalid, kid)
	}
	return nil
}

// validateClaims applies every binding check §10.3 requires, in three
// groups. The order is deliberate — who minted the token and for whom, then
// when, then which attempt and which human — because a token for another
// relying party must be refused as such rather than as an expired one.
func (p *Provider) validateClaims(claims idTokenClaims, expectedNonce string) error {
	if err := p.validateProvenance(claims); err != nil {
		return err
	}
	if err := p.validateFreshness(claims); err != nil {
		return err
	}
	return validateSubjectBinding(claims, expectedNonce)
}

// validateProvenance answers who minted the token and for whom: issuer,
// audience, authorized party, and that it names a subject at all.
func (p *Provider) validateProvenance(claims idTokenClaims) error {
	if claims.Issuer != p.cfg.Issuer {
		return fmt.Errorf("%w: issuer %q is not the configured provider", ErrTokenInvalid, claims.Issuer)
	}
	audiences, err := audienceList(claims.Audience)
	if err != nil {
		return err
	}
	if !slices.Contains(audiences, p.cfg.ClientID) {
		return fmt.Errorf("%w: token is not audienced to this client", ErrTokenInvalid)
	}
	if len(audiences) > 1 && claims.AuthorizedTo != p.cfg.ClientID {
		// A multi-audience token must name THIS client as the authorized
		// party (Core §3.1.3.7 rule 4/5); otherwise it was minted for
		// someone else who merely listed us.
		return fmt.Errorf("%w: multi-audience token authorizes a different party", ErrTokenInvalid)
	}
	if claims.AuthorizedTo != "" && claims.AuthorizedTo != p.cfg.ClientID {
		return fmt.Errorf("%w: token authorizes a different party", ErrTokenInvalid)
	}
	if claims.Subject == "" {
		return fmt.Errorf("%w: token carries no subject", ErrTokenInvalid)
	}
	return nil
}

// validateFreshness answers when: not expired, not from the future, and
// recent enough to be THIS sign-in rather than a replayed old one.
func (p *Provider) validateFreshness(claims idTokenClaims) error {
	now := p.now()
	if claims.ExpiresAt == 0 || !now.Add(-clockSkew).Before(time.Unix(claims.ExpiresAt, 0)) {
		return fmt.Errorf("%w: token has expired", ErrTokenInvalid)
	}
	if claims.IssuedAt == 0 {
		return fmt.Errorf("%w: token carries no issued-at", ErrTokenInvalid)
	}
	issuedAt := time.Unix(claims.IssuedAt, 0)
	if issuedAt.After(now.Add(clockSkew)) {
		return fmt.Errorf("%w: token is issued in the future", ErrTokenInvalid)
	}
	if now.Sub(issuedAt) > maxTokenAge+clockSkew {
		return fmt.Errorf("%w: token was issued too long ago to be a fresh authentication", ErrTokenInvalid)
	}
	return nil
}

// validateSubjectBinding answers which attempt and which human: the nonce
// ties the token to the login state this server minted, and the email is
// usable only if the provider says it verified it.
func validateSubjectBinding(claims idTokenClaims, expectedNonce string) error {
	// Constant-time comparison is not the point here (the nonce is not a
	// stored secret an attacker probes; it is a value they must already
	// have to reach this line) — matching it at all is.
	if expectedNonce == "" || claims.Nonce != expectedNonce {
		return fmt.Errorf("%w: nonce does not match the initiating request", ErrTokenInvalid)
	}
	if claims.Email == "" {
		return fmt.Errorf("%w: token carries no email claim", ErrTokenInvalid)
	}
	if claims.EmailVerified == nil || !*claims.EmailVerified {
		// An unverified address proves control of nothing, and the address
		// is exactly what the first binding is matched on. Its own sentinel:
		// the human can fix this one at the provider.
		return ErrEmailUnverified
	}
	return nil
}

// jwsPayload carries the two decoded halves the caller needs after the
// split: the raw claim bytes and the signature.
type jwsPayload struct {
	claims    []byte
	signature []byte
}

// splitJWS decodes a compact JWS into its signing input, header, and
// payload. It never returns partially-decoded material: a malformed token
// is one refusal.
func splitJWS(raw string) (signingInput string, header jwsHeader, payload jwsPayload, err error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", jwsHeader{}, jwsPayload{}, fmt.Errorf("%w: not a compact JWS", ErrTokenInvalid)
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", jwsHeader{}, jwsPayload{}, fmt.Errorf("%w: header is not base64url", ErrTokenInvalid)
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return "", jwsHeader{}, jwsPayload{}, fmt.Errorf("%w: header does not decode", ErrTokenInvalid)
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", jwsHeader{}, jwsPayload{}, fmt.Errorf("%w: claim set is not base64url", ErrTokenInvalid)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", jwsHeader{}, jwsPayload{}, fmt.Errorf("%w: signature is not base64url", ErrTokenInvalid)
	}
	return parts[0] + "." + parts[1], header, jwsPayload{claims: claims, signature: signature}, nil
}

// audienceList reads `aud`, which RFC 7519 allows as either one string or
// an array of them.
func audienceList(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: token carries no audience", ErrTokenInvalid)
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil || len(many) == 0 {
		return nil, fmt.Errorf("%w: audience is neither a string nor a non-empty array", ErrTokenInvalid)
	}
	return many, nil
}
