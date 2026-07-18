// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A hand-rolled verifier for the Google-signed OIDC ID token that Google
// Pub/Sub attaches to a push request (Authorization: Bearer <jwt>). RS256
// only; keys are fetched from Google's JWKS endpoint and cached per its
// Cache-Control max-age. It checks the signature and the iss/aud/email/
// email_verified/exp/iat claims. No new module dependency — crypto/rsa +
// net/http, mirroring gmail/client.go's hand-rolled provider I/O. Every
// rejection collapses to one opaque error; the caller answers 401 and logs
// the detail server-side (never echoed to the client).

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const googleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// oidcSkew tolerates small clock differences on exp/iat.
const oidcSkew = 2 * time.Minute

// jwksRefreshCooldown bounds JWKS refreshes across calls, not just within
// one: the header's alg/kid are read before any signature check, so an
// unauthenticated caller can force a cache miss on every request just by
// sending a never-seen kid. Without this cooldown, a burst of such tokens
// would drive one outbound HTTPS fetch (and one hold of v.mu) per request.
const jwksRefreshCooldown = time.Minute

// errOIDCRejected is the single opaque failure the verifier returns; the
// wrapped cause is for server-side logs only.
var errOIDCRejected = errors.New("oidc: push token rejected")

type googleOIDCVerifier struct {
	jwksURL        string
	audience       string
	serviceAccount string
	client         *http.Client
	now            func() time.Time

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	expires     time.Time
	nextRefresh time.Time
}

func newGoogleOIDCVerifier(jwksURL, audience, serviceAccount string) *googleOIDCVerifier {
	if jwksURL == "" {
		jwksURL = googleJWKSURL
	}
	return &googleOIDCVerifier{
		jwksURL:        jwksURL,
		audience:       audience,
		serviceAccount: serviceAccount,
		client:         &http.Client{Timeout: 30 * time.Second},
		now:            time.Now,
	}
}

func (v *googleOIDCVerifier) withHTTPClient(c *http.Client) *googleOIDCVerifier {
	v.client = c
	return v
}

func (v *googleOIDCVerifier) withClock(now func() time.Time) *googleOIDCVerifier {
	v.now = now
	return v
}

type oidcHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type oidcClaims struct {
	Iss           string `json:"iss"`
	Aud           string `json:"aud"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Exp           int64  `json:"exp"`
	Iat           int64  `json:"iat"`
}

// Verify returns nil only for a well-formed, correctly-signed Google push
// token whose claims match the configured audience and push service account.
func (v *googleOIDCVerifier) Verify(ctx context.Context, bearer string) error {
	if bearer == "" {
		return fmt.Errorf("%w: empty bearer", errOIDCRejected)
	}
	parts := strings.Split(bearer, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w: not a JWT", errOIDCRejected)
	}
	var hdr oidcHeader
	if err := decodeSegment(parts[0], &hdr); err != nil {
		return fmt.Errorf("%w: header: %v", errOIDCRejected, err)
	}
	if hdr.Alg != "RS256" {
		return fmt.Errorf("%w: alg %q not RS256", errOIDCRejected, hdr.Alg)
	}
	key, err := v.key(ctx, hdr.Kid)
	if err != nil {
		return fmt.Errorf("%w: key: %v", errOIDCRejected, err)
	}
	if err := verifyRS256(key, parts[0]+"."+parts[1], parts[2]); err != nil {
		return fmt.Errorf("%w: signature: %v", errOIDCRejected, err)
	}
	var claims oidcClaims
	if err := decodeSegment(parts[1], &claims); err != nil {
		return fmt.Errorf("%w: claims: %v", errOIDCRejected, err)
	}
	return v.checkClaims(claims)
}

func (v *googleOIDCVerifier) checkClaims(c oidcClaims) error {
	if c.Iss != "accounts.google.com" && c.Iss != "https://accounts.google.com" {
		return fmt.Errorf("%w: iss %q", errOIDCRejected, c.Iss)
	}
	if c.Aud != v.audience {
		return fmt.Errorf("%w: aud mismatch", errOIDCRejected)
	}
	if c.Email != v.serviceAccount {
		return fmt.Errorf("%w: email mismatch", errOIDCRejected)
	}
	if !c.EmailVerified {
		return fmt.Errorf("%w: email not verified", errOIDCRejected)
	}
	now := v.now()
	if c.Exp == 0 || now.After(time.Unix(c.Exp, 0).Add(oidcSkew)) {
		return fmt.Errorf("%w: expired", errOIDCRejected)
	}
	if c.Iat != 0 && now.Add(oidcSkew).Before(time.Unix(c.Iat, 0)) {
		return fmt.Errorf("%w: issued in the future", errOIDCRejected)
	}
	return nil
}

// key returns the cached public key for kid, refreshing the JWKS if the
// cache is empty, expired, or missing the kid (a rotation) — subject to
// jwksRefreshCooldown throttling refreshes across calls.
func (v *googleOIDCVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if kid == "" {
		return nil, errors.New("no kid")
	}
	if k, ok := v.lookupKey(kid); ok {
		return k, nil
	}
	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	k, ok := v.lookupKey(kid)
	if !ok {
		return nil, fmt.Errorf("unknown kid %q", kid)
	}
	return k, nil
}

// lookupKey reports the cached key for kid, if any, and whether the cache
// (as a whole) is still within its TTL.
func (v *googleOIDCVerifier) lookupKey(kid string) (*rsa.PublicKey, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok && v.now().Before(v.expires) {
		return k, true
	}
	return nil, false
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// refresh throttles JWKS fetches to at most one per jwksRefreshCooldown
// across all callers, and performs the network fetch without holding v.mu —
// only the cooldown decision and the eventual cache swap are locked.
func (v *googleOIDCVerifier) refresh(ctx context.Context) error {
	v.mu.Lock()
	now := v.now()
	if now.Before(v.nextRefresh) {
		v.mu.Unlock()
		return errors.New("jwks: refresh throttled")
	}
	v.nextRefresh = now.Add(jwksRefreshCooldown)
	v.mu.Unlock()

	keys, expires, err := v.fetchJWKS(ctx)
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.keys = keys
	v.expires = expires
	v.mu.Unlock()
	return nil
}

// fetchJWKS performs the outbound HTTPS GET and parses the key set. It takes
// no lock: it is called from refresh with v.mu already released.
func (v *googleOIDCVerifier) fetchJWKS(ctx context.Context) (map[string]*rsa.PublicKey, time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}
	//craft:ignore swallowed-errors best-effort close of the JWKS response body — the decoded keys are what matter
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("jwks: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, time.Time{}, err
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, time.Time{}, err
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		pub, err := rsaPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, time.Time{}, errors.New("jwks: no usable RSA keys")
	}
	return keys, v.now().Add(cacheTTL(resp.Header.Get("Cache-Control"))), nil
}

// cacheTTL reads max-age from a Cache-Control header, clamped to [1m, 24h]
// with a 1h default when absent — the JWKS is safe to reuse between rotations.
func cacheTTL(cacheControl string) time.Duration {
	ttl := time.Hour
	for _, part := range strings.Split(cacheControl, ",") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "max-age="); ok {
			if secs, err := strconv.Atoi(v); err == nil {
				ttl = time.Duration(secs) * time.Second
			}
		}
	}
	if ttl < time.Minute {
		ttl = time.Minute
	}
	if ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	return ttl
}

func rsaPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e == 0 {
		return nil, errors.New("jwk: zero exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

func verifyRS256(key *rsa.PublicKey, signingInput, sigB64 string) error {
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return err
	}
	h := sha256.Sum256([]byte(signingInput))
	// RS256 signature VERIFICATION per RFC 7518 §3.3 — PKCS#1 v1.5 is the
	// algorithm Google signs these tokens with; nothing is encrypted here.
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, h[:], sig) // NOSONAR(go:S5542) verification, not encryption
}

//craft:ignore naked-any out is the caller-supplied decode target — header vs claims, so its concrete type varies per call
func decodeSegment(seg string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
