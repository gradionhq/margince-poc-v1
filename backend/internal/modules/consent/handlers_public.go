// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

// The no-login preference-center transport (B-E11.32). The public
// middleware has already resolved the token to (workspace, person) and
// bound the workspace GUC plus the system principal; each handler
// re-resolves the token for the person id (the same infra read) and then
// drives the consent engine. An unknown or revoked token reads as absent
// (404) — the surface is never a consent-state oracle, and a GET/prefetch
// on the unsubscribe path never withdraws (only POST is routed to it).

import (
	"net/http"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// GetPreferenceCenter implements (GET /public/preferences/{token}): the
// recipient's per-purpose consent state, recognized without any login.
func (h Handlers) GetPreferenceCenter(w http.ResponseWriter, r *http.Request, token string) {
	ref, err := h.store.ResolvePreferenceToken(r.Context(), token)
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	choices, err := h.store.PublicPurposeStates(r.Context(), ref.PersonID)
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"purposes": wirePurposeChoices(choices)})
}

// OneClickUnsubscribe implements (POST /public/preferences/{token}/unsubscribe):
// the RFC 8058 one-click endpoint. No login, no confirmation page, a fixed
// body. When a purpose is named only that purpose is withdrawn (the one
// the message was sent under); otherwise every withdrawable purpose is.
// Idempotent — re-asserting a withdrawal writes no second proof row.
func (h Handlers) OneClickUnsubscribe(w http.ResponseWriter, r *http.Request, token string, params crmcontracts.OneClickUnsubscribeParams) {
	ref, err := h.store.ResolvePreferenceToken(r.Context(), token)
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	var withdrawn []string
	if params.Purpose != nil && strings.TrimSpace(*params.Purpose) != "" {
		key := strings.ToLower(strings.TrimSpace(*params.Purpose))
		if _, err := h.store.PublicSetConsent(r.Context(), ref.PersonID, key, "withdrawn", nil); err != nil {
			writeConsentErr(w, r, err)
			return
		}
		withdrawn = append(withdrawn, key)
	} else {
		choices, err := h.store.PublicPurposeStates(r.Context(), ref.PersonID)
		if err != nil {
			writeConsentErr(w, r, err)
			return
		}
		for _, c := range choices {
			if c.Locked || c.State != "granted" {
				continue
			}
			if _, err := h.store.PublicSetConsent(r.Context(), ref.PersonID, c.Key, "withdrawn", nil); err != nil {
				writeConsentErr(w, r, err)
				return
			}
			withdrawn = append(withdrawn, c.Key)
		}
	}
	if withdrawn == nil {
		withdrawn = []string{}
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"unsubscribed": withdrawn})
}

// maxPreferenceChoices bounds a single granular save. The consent purpose
// catalog is a small closed set; anything beyond a generous ceiling is
// abuse, not a real preference update.
const maxPreferenceChoices = 64

// UpdatePreferences implements (PUT /public/preferences/{token}): the
// granular save. Each choice is recorded per purpose (a withdrawal of one
// never touches another — per-purpose default-deny) with the exact wording
// shown, stored verbatim as proof.
func (h Handlers) UpdatePreferences(w http.ResponseWriter, r *http.Request, token string) {
	var req struct {
		Choices []struct {
			PurposeKey string  `json:"purpose_key"`
			State      string  `json:"state"`
			Wording    *string `json:"wording"`
		} `json:"choices"`
	}
	if !httperr.Decode(w, r, &req) {
		return
	}
	if len(req.Choices) == 0 {
		httperr.Write(w, r, httperr.Validation("choices", "required", "at least one per-purpose choice is required"))
		return
	}
	// A legitimate save carries at most one choice per tracked purpose;
	// the catalog is a small closed set. Cap the array so a valid token
	// cannot amplify a single 1 MiB body into tens of thousands of serial
	// per-choice transactions.
	if len(req.Choices) > maxPreferenceChoices {
		httperr.Write(w, r, httperr.Validation("choices", "too_many", "more choices than there are tracked purposes"))
		return
	}
	ref, err := h.store.ResolvePreferenceToken(r.Context(), token)
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	for _, c := range req.Choices {
		if c.State != "granted" && c.State != "withdrawn" {
			httperr.Write(w, r, httperr.Validation("state", "invalid", "must be granted or withdrawn"))
			return
		}
		if _, err := h.store.PublicSetConsent(r.Context(), ref.PersonID, c.PurposeKey, c.State, c.Wording); err != nil {
			writeConsentErr(w, r, err)
			return
		}
	}
	choices, err := h.store.PublicPurposeStates(r.Context(), ref.PersonID)
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"purposes": wirePurposeChoices(choices)})
}

func wirePurposeChoices(choices []PurposeChoice) []map[string]any {
	out := make([]map[string]any, 0, len(choices))
	for _, c := range choices {
		out = append(out, map[string]any{
			"key":    c.Key,
			"label":  c.Label,
			"state":  c.State,
			"locked": c.Locked,
		})
	}
	return out
}
