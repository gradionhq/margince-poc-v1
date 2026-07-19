// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// The ADR-0036 §1 approval-token serialization: when issuer and
// verifier share one binary the approval ROW is the authority object;
// when they separate, the same claims travel as a
// compact JWS signed with the workspace's Ed25519 key. The token adds
// no authority the row does not have — redemption still re-checks the
// row (single-use, diff hash, target version) — it only lets a remote
// verifier reject garbage before touching the database.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ApprovalTokenClaims is the effect binding (ADR-0036): exactly one
// approval, exactly one tool + diff, dead at the approval's own TTL.
type ApprovalTokenClaims struct {
	ApprovalID  ids.ApprovalID  `json:"jti"`
	WorkspaceID ids.UUID        `json:"ws"`
	Kind        string          `json:"kind"`
	DiffHash    string          `json:"diff_hash"`
	PassportID  *ids.PassportID `json:"passport_id,omitempty"`
	// TargetType + TargetID are the polymorphic reference to the approved
	// action's target; the id stays untyped because the pair is the
	// discriminated reference, not one entity's typed id.
	TargetType    *string   `json:"target_type,omitempty"`
	TargetID      *ids.UUID `json:"target_id,omitempty"`
	TargetVersion *int64    `json:"target_version,omitempty"`
	ExpiresAt     int64     `json:"exp"`
}

type jwsHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// MintApprovalToken serializes an APPROVED staging as a compact JWS.
// Called by the approve handler so the deciding human's response
// carries the token the agent will redeem.
func (s *Service) MintApprovalToken(ctx context.Context, approvalID ids.ApprovalID) (string, error) {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return "", errors.New("crmapprovals: minting outside workspace context")
	}
	var token string
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := get(ctx, tx, approvalID)
		if err != nil {
			return err
		}
		if a.Status != approvalStatusApproved {
			return fmt.Errorf("approval %s is %s, not approved: %w", approvalID, a.Status, apperrors.ErrApprovalTokenInvalid)
		}
		claims := ApprovalTokenClaims{
			ApprovalID:    a.ID,
			WorkspaceID:   wsID,
			Kind:          a.Kind,
			DiffHash:      a.DiffHash,
			PassportID:    a.PassportID,
			TargetType:    a.TargetType,
			TargetID:      a.TargetID,
			TargetVersion: a.TargetVersion,
			ExpiresAt:     a.ExpiresAt.Unix(),
		}
		kid, key, err := signingKey(ctx, tx)
		if err != nil {
			return err
		}
		token, err = signCompact(claims, kid, key)
		return err
	})
	return token, err
}

// VerifyApprovalToken checks signature and expiry and returns the
// claims. It deliberately does NOT consume anything: redemption stays
// with the approval row, so a verified-but-already-redeemed token still
// fails exactly once, in one place.
func (s *Service) VerifyApprovalToken(ctx context.Context, token string) (ApprovalTokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ApprovalTokenClaims{}, fmt.Errorf("token is not a compact JWS: %w", apperrors.ErrApprovalTokenInvalid)
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ApprovalTokenClaims{}, badToken(err)
	}
	var header jwsHeader
	if err := json.Unmarshal(headerRaw, &header); err != nil || header.Alg != "EdDSA" || header.Kid == "" {
		return ApprovalTokenClaims{}, fmt.Errorf("unsupported JWS header: %w", apperrors.ErrApprovalTokenInvalid)
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ApprovalTokenClaims{}, badToken(err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return ApprovalTokenClaims{}, badToken(err)
	}

	var publicKey []byte
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT public_key FROM workspace_signing_key WHERE kid = $1 AND retired_at IS NULL`,
			header.Kid).Scan(&publicKey)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalTokenClaims{}, fmt.Errorf("unknown signing key %s: %w", header.Kid, apperrors.ErrApprovalTokenInvalid)
	}
	if err != nil {
		return ApprovalTokenClaims{}, err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), []byte(parts[0]+"."+parts[1]), signature) {
		return ApprovalTokenClaims{}, fmt.Errorf("signature check failed: %w", apperrors.ErrApprovalTokenInvalid)
	}

	var claims ApprovalTokenClaims
	if err := json.Unmarshal(payloadRaw, &claims); err != nil {
		return ApprovalTokenClaims{}, badToken(err)
	}
	if s.now().Unix() >= claims.ExpiresAt {
		return ApprovalTokenClaims{}, fmt.Errorf("token expired: %w", apperrors.ErrApprovalTokenInvalid)
	}
	return claims, nil
}

// signingKey loads the workspace's live key, minting one lazily on the
// first token: single-binary deployments never configure keys, and
// issuer/verifier separation works because both read the same rows.
func signingKey(ctx context.Context, tx pgx.Tx) (string, ed25519.PrivateKey, error) {
	var kid string
	var private []byte
	err := tx.QueryRow(ctx, `
		SELECT kid, private_key FROM workspace_signing_key
		WHERE retired_at IS NULL ORDER BY created_at DESC LIMIT 1`).Scan(&kid, &private)
	if err == nil {
		return kid, ed25519.PrivateKey(private), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, err
	}
	public, fresh, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, fmt.Errorf("crmapprovals: keygen: %w", err)
	}
	kid = ids.NewV7().String()
	if _, err := tx.Exec(ctx, `
		INSERT INTO workspace_signing_key (workspace_id, kid, private_key, public_key)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)`,
		kid, []byte(fresh), []byte(public)); err != nil {
		return "", nil, err
	}
	return kid, fresh, nil
}

func signCompact(claims ApprovalTokenClaims, kid string, key ed25519.PrivateKey) (string, error) {
	headerJSON, err := json.Marshal(jwsHeader{Alg: "EdDSA", Kid: kid})
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(payloadJSON)
	signature := ed25519.Sign(key, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func badToken(err error) error {
	return fmt.Errorf("malformed token (%v): %w", err, apperrors.ErrApprovalTokenInvalid)
}
