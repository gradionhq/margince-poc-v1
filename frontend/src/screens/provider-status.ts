// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";
import type { MessageKey } from "../i18n/en";

// The licensed-data-provider status vocabulary, in one place because two
// surfaces render it: the Settings card says whether the connection works,
// and the person page says what happened to one subject's enrichment. The
// same word must mean the same thing on both — a card reading "connected"
// beside a person page reading "not connected" is a bug the reader cannot
// diagnose.
//
// Extracted as a pure module, like connector-status.ts, so the mapping can be
// tested without rendering anything.

type ProviderConnection = components["schemas"]["ProviderConnection"];
type PersonProviderProfile = components["schemas"]["PersonProviderProfile"];

export type ProviderConnectionStatus = ProviderConnection["status"];
export type ProviderProfileState = PersonProviderProfile["state"];

/** The tone a status carries, in the design system's own Badge vocabulary so
 *  a caller passes it straight through rather than mapping twice.
 *
 *  `undefined` is the neutral tone and is deliberately NOT `danger`: a
 *  provider nobody connected is a configuration, not a fault, and colouring
 *  it red tells an operator to fix something that is not broken. */
export type StatusTone = "success" | "warn" | "danger" | undefined;

const CONNECTION_TONE: Record<ProviderConnectionStatus, StatusTone> = {
  connected: "success",
  disconnected: undefined,
  validating: undefined,
  // The key was refused. Danger rather than warning: nothing will work until
  // a human rotates it, and no amount of waiting helps.
  invalid_credentials: "danger",
  // Recoverable provider conditions. The connection is intact and the key is
  // good; the vendor is refusing this moment's work.
  insufficient_credits: "warn",
  rate_limited: "warn",
  provider_error: "warn",
};

const CONNECTION_LABEL: Record<ProviderConnectionStatus, MessageKey> = {
  connected: "provider.status.connected",
  disconnected: "provider.status.disconnected",
  validating: "provider.status.validating",
  invalid_credentials: "provider.status.invalidCredentials",
  insufficient_credits: "provider.status.insufficientCredits",
  rate_limited: "provider.status.rateLimited",
  provider_error: "provider.status.providerError",
};

export function connectionTone(status: ProviderConnectionStatus): StatusTone {
  return CONNECTION_TONE[status];
}

export function connectionLabel(status: ProviderConnectionStatus): MessageKey {
  return CONNECTION_LABEL[status];
}

// The person page's fourteen states. Three of them mean "nothing here" and
// they are deliberately three: nobody connected a provider, this person is
// not eligible for one, and nobody has asked yet are different answers to
// "why is this empty", and only one of them is something the reader can act
// on.
const PROFILE_TONE: Record<ProviderProfileState, StatusTone> = {
  not_connected: undefined,
  not_eligible: undefined,
  never_run: undefined,
  queued: undefined,
  in_progress: undefined,
  completed: "success",
  no_match: undefined,
  // The data is real but old enough that the platform will not vouch for it —
  // or the provider was disconnected and it can no longer be refreshed.
  stale: "warn",
  invalid_credentials: "danger",
  insufficient_credits: "warn",
  rate_limited: "warn",
  provider_error: "warn",
  // The outcome was never learned, and the run may have been charged for.
  submission_unknown: "warn",
  // Paid, and the values never reached the record. Its own state because it
  // is neither a success nor a failure: somebody was charged and has nothing
  // to show for it, which a person needs to SEE rather than discover as
  // missing data.
  completed_claims_unwritten: "warn",
};

const PROFILE_LABEL: Record<ProviderProfileState, MessageKey> = {
  not_connected: "provider.profile.notConnected",
  not_eligible: "provider.profile.notEligible",
  never_run: "provider.profile.neverRun",
  queued: "provider.profile.queued",
  in_progress: "provider.profile.inProgress",
  completed: "provider.profile.completed",
  no_match: "provider.profile.noMatch",
  stale: "provider.profile.stale",
  invalid_credentials: "provider.profile.invalidCredentials",
  insufficient_credits: "provider.profile.insufficientCredits",
  rate_limited: "provider.profile.rateLimited",
  provider_error: "provider.profile.providerError",
  submission_unknown: "provider.profile.submissionUnknown",
  completed_claims_unwritten: "provider.profile.claimsUnwritten",
};

export function profileTone(state: ProviderProfileState): StatusTone {
  return PROFILE_TONE[state];
}

export function profileLabel(state: ProviderProfileState): MessageKey {
  return PROFILE_LABEL[state];
}

/** Whether this state means a run is still moving, so the page should keep
 *  asking. Everything else is terminal: polling a completed or refused run
 *  spends requests to learn nothing. */
export function isRunning(state: ProviderProfileState): boolean {
  return state === "queued" || state === "in_progress";
}

/** Whether an "enrich now" button should be offered. Not while one is
 *  running — a second click cannot buy a second answer, the live-run index
 *  refuses it — and not where no provider is connected, since there is
 *  nothing to ask. */
export function canEnrichNow(state: ProviderProfileState): boolean {
  return (
    state !== "not_connected" && state !== "not_eligible" && !isRunning(state)
  );
}
