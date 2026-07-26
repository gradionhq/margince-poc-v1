// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Where a consent round-trip lands and what the human is told when it did not
// work. The callback never renders: it redirects to a hash route the SPA maps
// to copy, so the vocabulary of outcomes IS the contract with the frontend, and
// picking the right one is the difference between advice a person can act on
// and a shrug.

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/capture/googleconn"
	"github.com/gradionhq/margince/backend/internal/modules/capture/oauthflow"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// landingURL is the OAuth-return deep link. The SPA is hash-routed, so the
// outcome rides the route — the landing surface reads it and renders success,
// the honest denial, or the honest failure. returnTo names WHICH surface, and
// is resolved through a closed set: it is an enum, never a URL, so no caller
// input ever reaches the Location header. Anything unrecognized — including an
// absent value and a URL-shaped one — lands on onboarding.
//
// The provider rides the route too, because a workspace can hold several
// connected mailboxes and the landing surface has no other way to tell which
// one this round-trip was for — it would otherwise offer the import for
// whichever mailbox the roster happens to list first. It is likewise a closed
// enum: only a provider this deployment has a registered OAuth app for ever
// reaches here.
func (h connectorHandlers) landingURL(outcome, returnTo, provider string) string {
	route := "/#/onboarding/connect/"
	if returnTo == returnToSettings {
		route = "/#/settings/integrations/"
	}
	return strings.TrimRight(h.publicBaseURL, "/") + route + outcome + "/" + provider
}

const (
	returnToOnboarding = "onboarding"
	returnToSettings   = "settings"
)

// The OAuth-return outcomes the landing surface renders. They are a closed set
// the SPA maps to copy, never rendering a raw route segment for one it does not
// know (Settings shows nothing; onboarding falls back to its generic failure).
// They exist because "it failed, try again" is only honest for SOME failures: a
// declined consent is not a failure at all, a refused credential needs its human
// to reconnect, and a provider API that was never enabled for the deployment
// needs an administrator — retrying that one forever cannot work.
const (
	outcomeOK            = "ok"
	outcomeDenied        = "denied"
	outcomeRejected      = "rejected"
	outcomeMisconfigured = "misconfigured"
	outcomeError         = "error"
)

// disabledProviderAPI reports whether the failure is a provider API that was
// never enabled for this deployment. The reason vocabulary is Google's, so the
// question is only asked of the Google connectors — a Microsoft failure that
// happened to reuse one of those code names must not be answered with Google's
// remedy.
func disabledProviderAPI(provider string, err error) bool {
	if provider != providerGmail && provider != providerGcal {
		return false
	}
	return googleconn.Misconfigured(err)
}

// connectFailureOutcome picks the landing outcome for a failed consent
// completion, so what the human reads matches what they can actually do about
// it. Two distinct deployment faults land on misconfigured — a provider API that
// was never enabled, and an OAuth client the provider refused to authenticate
// (a wrong client id/secret, which is provider-independent) — because a human
// re-consenting clears neither. Anything we cannot attribute to the provider
// stays the generic error: an honest "we don't know yet", never a guess at whose
// fault it is.
func connectFailureOutcome(provider string, err error) string {
	switch {
	case disabledProviderAPI(provider, err), oauthflow.Misconfigured(err):
		return outcomeMisconfigured
	case errors.Is(err, connector.ErrAuthRejected):
		return outcomeRejected
	default:
		return outcomeError
	}
}

// logConnectFailure records a failed consent completion for the operator. The
// two deployment faults each get a line naming their OWN remedy: a machine
// reason code ("accessNotConfigured", "invalid_client") says what the provider
// refused but not what to go do about it, and the two remedies are different
// places. The generic line deliberately does not name a step — the failure can
// come from the token exchange, the refresh, or the owner lookup, and
// ProviderError.Op already carries which call it actually was.
func logConnectFailure(ctx context.Context, provider string, err error) {
	switch {
	case disabledProviderAPI(provider, err):
		slog.ErrorContext(ctx,
			"connector callback: this provider's API is not enabled for the deployment — "+
				"enable it in the Google Cloud project behind the OAuth client, then reconnect",
			"err", err, "provider", provider)
	case oauthflow.Misconfigured(err):
		slog.ErrorContext(ctx,
			"connector callback: the provider refused this deployment's OAuth client — "+
				"check the configured client id and secret for this connector",
			"err", err, "provider", provider)
	default:
		slog.ErrorContext(ctx, "connector callback: completing consent failed",
			"err", err, "provider", provider)
	}
}
