// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package oidc

// The provider's signing keys (RFC 7517 JWK Set), cached per its own cache
// headers and keyed by `kid`. Only RSA keys usable for RS256 signature
// verification are admitted — this relying party accepts exactly one
// algorithm (verify.go), so a key it could not verify with is not a key it
// should hold.

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// keySet is the cached, kid-indexed signing keys of ONE jwks_uri. The uri
// is part of the cache so a rotated discovery document cannot leave keys
// from the previous endpoint behind.
type keySet struct {
	uri   string
	keys  map[string]*rsa.PublicKey
	until time.Time
}

// jwksResponse is the JWK Set document. Keys this build cannot verify with
// (EC, oct, an explicit non-RS256 alg) are skipped rather than rejected: a
// provider is entitled to publish keys for algorithms and uses beyond the
// one we need.
type jwksResponse struct {
	Keys []jwk `json:"keys"`
}

// jwk is one published key. Named rather than inline so a test can state a
// key set as a literal and read as a spec.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// signingKey returns the RSA public key for kid, fetching the key set when
// the cache is cold or expired. force skips a live cache — the single retry
// a token signed with a just-rotated key gets.
func (p *Provider) signingKey(ctx context.Context, kid string, force bool) (*rsa.PublicKey, error) {
	doc, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	fresh := p.keys.uri == doc.JWKSURI && p.keys.keys != nil && p.now().Before(p.keys.until)
	if fresh && !force {
		if key, ok := p.keys.keys[kid]; ok {
			return key, nil
		}
		// A kid the cache does not know is not yet a refusal: the caller
		// forces one refresh before giving up.
		return nil, errUnknownKeyID
	}

	var parsed jwksResponse
	ttl, err := p.fetchJSON(ctx, doc.JWKSURI, &parsed)
	if err != nil {
		return nil, err
	}
	keys, err := rsaKeys(parsed)
	if err != nil {
		return nil, err
	}
	p.keys = keySet{uri: doc.JWKSURI, keys: keys, until: p.now().Add(ttl)}

	key, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("%w: %w %q", ErrTokenInvalid, errUnknownKeyID, kid)
	}
	return key, nil
}

// rsaKeys converts the published set into verifiable RSA public keys. A key
// set that yields none is a provider problem, not a token problem — every
// sign-in through this issuer would fail the same way.
func rsaKeys(parsed jwksResponse) (map[string]*rsa.PublicKey, error) {
	keys := map[string]*rsa.PublicKey{}
	for _, published := range parsed.Keys {
		if published.Kty != "RSA" || published.Kid == "" {
			continue
		}
		if published.Use != "" && published.Use != "sig" {
			continue
		}
		if published.Alg != "" && published.Alg != algRS256 {
			continue
		}
		key, err := rsaPublicKey(published.N, published.E)
		if err != nil {
			// One unusable key must not discard the rest of a valid set: the
			// token at hand may well be signed by a sibling that parsed.
			continue
		}
		keys[published.Kid] = key
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: key set carries no usable RS256 signing key", ErrProviderUnavailable)
	}
	return keys, nil
}

// minRSAModulusBytes rejects an undersized modulus (< 2048-bit). A short
// key verifies happily and proves nothing.
const minRSAModulusBytes = 256

// rsaPublicKey rebuilds a public key from the JWK's base64url modulus and
// exponent.
func rsaPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, errors.New("oidc: jwk modulus is not base64url")
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, errors.New("oidc: jwk exponent is not base64url")
	}
	if len(nBytes) < minRSAModulusBytes {
		return nil, fmt.Errorf("oidc: jwk modulus is %d bytes; at least %d (2048-bit) is required", len(nBytes), minRSAModulusBytes)
	}
	if len(eBytes) == 0 || len(eBytes) > 8 {
		return nil, errors.New("oidc: jwk exponent is missing or implausibly large")
	}
	exponent := new(big.Int).SetBytes(eBytes)
	if !exponent.IsInt64() || exponent.Int64() < 3 {
		return nil, errors.New("oidc: jwk exponent is out of range")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(exponent.Int64())}, nil
}
