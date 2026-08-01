// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Which of this human's passports may be lent to a client, answered twice from
// ONE predicate: as the list the consent screen renders, and as the locked
// re-check the consent POST commits its authorization code with. Both answer in
// SQL rather than by filtering in Go, so the exclusions cannot drift apart from
// each other or from the row scope the rest of this module enforces.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ConsentOption is one lendable passport as the consent screen sees it. Scopes
// is both what the passport carries and what a connection lending it receives:
// the client's request does not narrow the grant, so there is no second,
// smaller set to carry alongside.
type ConsentOption struct {
	ID        ids.PassportID
	Label     string
	Scopes    []principal.Scope
	ExpiresAt time.Time
}

// lendablePassportPredicate is the ONE spelling of "this human may lend this
// passport", carried by both statements that decide it: the list the consent
// screen renders (SelectablePassports) and the locked re-check the consent POST
// commits with (lockLentPassport). $1 is the human. Three exclusions, in SQL
// rather than filtered in Go, so they cannot drift apart from each other or from
// the row scope the rest of this module enforces:
//
//   - on_behalf_of = the caller: you may only lend your OWN authority.
//   - revoked_at IS NULL and unexpired: a dead credential is not a template.
//   - oauth_grant_id IS NULL: a passport already bound to a connection is not
//     lendable, or revoking one connection would appear to affect another.
//
// What the client asked for is deliberately NOT an exclusion. A passport grants
// its own scopes whatever was requested, so a passport that overlaps the
// request in nothing is still a valid — and possibly the intended — choice.
//
// Parenthesized, because one of its two callers ANDs it with a condition of its
// own: a future arm joined by OR would otherwise widen that statement's row scope
// while leaving the list correct.
const lendablePassportPredicate = `(on_behalf_of = $1 AND revoked_at IS NULL
	  AND expires_at > now() AND oauth_grant_id IS NULL)` // #nosec G101 -- a SQL predicate over passport rows, not a credential

// SelectablePassports lists the passports id may lend to a client — the
// lendablePassportPredicate above, nothing else.
//
// Human-only at the seam, not merely at the transport. Lending authority is a
// decision only the human who holds it may take, and anything that could
// enumerate this list could pick from it — so an agent principal is refused
// here, where every caller passes, rather than trusted to have been stopped by
// the contract's `x-agent-access: human-only` or by a session lookup some later
// transport might not perform.
func (s *Service) SelectablePassports(ctx context.Context, id Identity) ([]ConsentOption, error) {
	if err := auth.RequireHuman(ctx); err != nil {
		return nil, err
	}
	var out []ConsentOption
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, label, scopes, expires_at
			FROM passport
			WHERE `+lendablePassportPredicate+`
			ORDER BY created_at DESC`, id.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				option ConsentOption
				scopes []string
				// label is scanned through a pointer: the column is nullable
				// (a human may mint a passport with no label) while the wire
				// field is a required string, so NULL becomes "" rather than
				// failing the scan.
				label *string
			)
			if err := rows.Scan(&option.ID, &label, &scopes, &option.ExpiresAt); err != nil {
				return err
			}
			if label != nil {
				option.Label = *label
			}
			for _, scope := range scopes {
				option.Scopes = append(option.Scopes, principal.Scope(scope))
			}
			out = append(out, option)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// lentPassport is one resolved lend: WHICH passport the human handed to the
// client, and the scopes the connection actually receives from it. Both travel
// together because the authorization code needs the scopes and the audit trail
// needs the id — the code row has no column for the passport, so that id is
// recoverable afterwards only from what is recorded alongside it.
type lentPassport struct {
	ID     ids.PassportID
	Scopes []string
}

// lockLentPassport re-resolves the passport a consent POST offered to lend and
// LOCKS the row it resolved, inside the transaction that writes the
// authorization code (mintLentAuthorizationCode). It answers with what the
// connection actually receives: that passport's own scopes, exactly. lendable is
// false for a passport_id this human cannot lend right now — a malformed id
// included.
//
// The lend is re-queried rather than taken at the form's word. The list the
// browser rendered is seconds old, so every selectability condition — this
// human's own passport, alive, not already bound to a connection — is judged
// again against live rows; a passport revoked in another tab must not still be
// lendable.
//
// The row lock is what makes that a decision rather than a guess. A revocation
// is a plain UPDATE of this very row (revokePassportTx, passport.go), so it
// needs this same lock: arriving first it commits, and the predicate is
// re-evaluated against the revoked row, which refuses the lend; arriving second
// it waits until the code row it would have raced has committed. Without the
// lock the two transactions pass each other and the revoked passport is lent
// anyway.
//
// The client's request is not consulted. Every mainstream MCP client sends no
// scope parameter at all, so an intersection here defaulted every real
// connection to read and made the 🟡 write half of the tool surface
// unreachable. Dropping the cap widens nothing an adversary could not already
// have — a client that wants everything simply asks for everything — so the
// human's deliberate choice of a passport, on a screen built for that choice,
// is the whole answer.
func lockLentPassport(
	ctx context.Context, tx pgx.Tx, id Identity, rawID string,
) (lent lentPassport, lendable bool, err error) {
	// A malformed id refuses exactly like an unknown one: the value arrives from
	// a form, and parsing is where that boundary is crossed — an unparseable id
	// must never reach the query as a zero value, which would name a zero row.
	// It is a refusal rather than a failure: a form value that is not an id names
	// no lendable passport, exactly as an unknown id names none.
	passportID, parseErr := ids.ParseAs[ids.PassportKind](rawID)
	if parseErr != nil {
		return lentPassport{}, false, nil
	}
	var scopes []string
	err = tx.QueryRow(ctx, `
		SELECT scopes FROM passport
		WHERE id = $2 AND `+lendablePassportPredicate+`
		FOR UPDATE`, id.UserID, passportID).Scan(&scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return lentPassport{}, false, nil
	}
	if err != nil {
		return lentPassport{}, false, err
	}
	return lentPassport{ID: passportID, Scopes: scopes}, true, nil
}

// liveClient resolves client_id to the name a consent screen may show. An
// unknown, disabled, or soft-deleted client all read as apperrors.ErrNotFound
// — the same answer for all three, because which one it is would tell a
// prober something an admin's off switch is trying to hide. A genuine
// lookup failure (not "no such live client") is returned as itself, so a
// database problem is never mistaken for a client that does not exist.
func (s *Service) liveClient(ctx context.Context, clientID string) (string, error) {
	var name string
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT c.client_name FROM oauth_client c WHERE c.client_id = $1 AND `+liveClientPredicate,
			clientID).Scan(&name)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apperrors.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

// offlineRequested is all the consent screen takes from the client's scope
// parameter: whether it asked to stay connected without asking again. The access
// scopes in it are not read, because they decide nothing the screen renders — a
// lend grants the chosen passport's own scopes — while offline_access is about
// the connection's lifetime, which the human is approving and so must see.
//
// Unlike parseOAuthScopes this never errors: an unknown scope has already been
// refused on the authorize request this screen is rendering.
func offlineRequested(raw string) bool {
	return slices.Contains(strings.Fields(raw), scopeOfflineAccess)
}

// consentRequestPayload maps the read model onto the generated wire shape.
func consentRequestPayload(
	clientName string, offline bool, options []ConsentOption,
) crmcontracts.ConsentRequest {
	passports := make([]crmcontracts.ConsentPassportOption, 0, len(options))
	for _, option := range options {
		scopes := make([]crmcontracts.ConsentPassportOptionScopes, 0, len(option.Scopes))
		for _, scope := range option.Scopes {
			scopes = append(scopes, crmcontracts.ConsentPassportOptionScopes(scope))
		}
		passports = append(passports, crmcontracts.ConsentPassportOption{
			Id:        openapi_types.UUID(option.ID.UUID),
			Label:     option.Label,
			Scopes:    scopes,
			ExpiresAt: option.ExpiresAt,
		})
	}
	return crmcontracts.ConsentRequest{
		ClientName: clientName,
		Offline:    offline,
		Passports:  passports,
	}
}

// GetConsentRequest implements GET /oauth/consent-request. Human-only: an
// agent must never read or drive a consent screen (the generated agent
// policy enforces this from the contract's x-agent-access: human-only, but
// the check here is what a session-authenticated human actually needs).
func (h Handlers) GetConsentRequest(w http.ResponseWriter, r *http.Request, params crmcontracts.GetConsentRequestParams) {
	id, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "the consent screen belongs to the signed-in human whose authority the agent will borrow")
		return
	}
	clientName, err := h.svc.liveClient(r.Context(), params.ClientId)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	options, err := h.svc.SelectablePassports(r.Context(), id)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	// The consent nonce is deliberately NOT read here. The cookie that carries it
	// is Path=/oauth/authorize, so a browser never sends it to this endpoint; the
	// redirect hands the nonce to the screen in the fragment instead, and the POST
	// still proves possession of the cookie. An endpoint that read it would 404
	// every real browser while a test setting the header by hand passed.
	httperr.WriteJSON(w, http.StatusOK,
		consentRequestPayload(clientName, offlineRequested(params.Scope), options))
}
