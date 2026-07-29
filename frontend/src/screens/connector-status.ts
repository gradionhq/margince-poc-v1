// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";
import type { MessageKey } from "../i18n/en";

// The shared connector-status vocabulary: Settings (the connected-inboxes
// card) and home's digest both render connector health, and they must never
// describe the same state differently. Seeded by extracting the inline
// statusTone/statusLabel that shipped with connectors.tsx (RC-8) so the two
// surfaces stay on one definition rather than two copies drifting apart.

type CaptureConnection = components["schemas"]["CaptureConnection"];

export type ConnectorStatus = CaptureConnection["status"];

// The one scope that grants sending today. The server pre-flights a send
// against this same string (comms.SendScopeFor), so the badge below and the
// 422 it exists to pre-empt cannot disagree about which mailbox may send.
const GMAIL_SEND_SCOPE = "https://www.googleapis.com/auth/gmail.send";

/** Whether this connection captures mail it will never be allowed to send.
 *  Google will not widen an existing refresh token, so every mailbox
 *  connected before sending shipped holds read-only access until its owner
 *  reconnects — a fact the connection's `status` cannot express, since it is
 *  genuinely connected and genuinely capturing. Only the granted scopes say
 *  it, which is why this reads them rather than the status.
 *
 *  Gmail is the only provider that sends at all (one `if`, matching the
 *  server: a registry with a single entry is an abstraction with no second
 *  caller). A calendar or IMAP connection is not "cannot send" — it is not a
 *  sending mailbox in the first place, and badging it would be a refusal
 *  nobody could act on. */
export function missingSendGrant(
  connection: Pick<CaptureConnection, "provider" | "scopes">,
): boolean {
  return (
    connection.provider === "gmail" &&
    !connection.scopes.includes(GMAIL_SEND_SCOPE)
  );
}

/** Each of the four contract statuses gets its own Badge tone. Collapsing
 *  reauth_required and error into the same tone is what made a dead mailbox
 *  and a merely-stale one indistinguishable at a glance. `disconnected` gets
 *  no tone (the shipped card's neutral, undecorated row). */
export function statusTone(
  status: ConnectorStatus,
): "success" | "warn" | "danger" | undefined {
  switch (status) {
    case "connected":
      return "success";
    case "reauth_required":
      return "warn";
    case "error":
      return "danger";
    case "disconnected":
      return undefined;
  }
}

/** The status label shown beside the tone. */
export function statusLabel(status: ConnectorStatus): MessageKey {
  switch (status) {
    case "connected":
      return "connectors.statusConnected";
    case "reauth_required":
      return "connectors.statusReauth";
    case "error":
      return "connectors.statusError";
    case "disconnected":
      return "connectors.statusDisconnected";
  }
}

/** The contract states that only the error CLASS crosses the wire — detail
 *  lives in system_log. So each class gets one fixed sentence and we never
 *  invent more. The enum can widen server-side ahead of this client, so an
 *  unrecognized class degrades to an honest generic rather than rendering a
 *  raw identifier. */
export function errorClassKey(cls: string | null | undefined): MessageKey {
  switch (cls) {
    case "rate_limited":
      return "connectors.errRateLimited";
    case "unreachable":
      return "connectors.errUnreachable";
    case "auth":
      return "connectors.errAuth";
    case "history_gone":
      return "connectors.errHistoryGone";
    case "internal":
      return "connectors.errInternal";
    default:
      return "connectors.errUnknown";
  }
}

/** Home surfaces a connector line only when something needs the user's
 *  attention: a healthy connector is not news, and a deliberately
 *  disconnected mailbox (the headline disconnect flow's own result) is not
 *  a fault — it is quiet on purpose, matching Settings, which filters
 *  `disconnected` rows out of its list entirely. Only a genuinely broken
 *  connection (`error` or `reauth_required`) is news. */
export function isUnhealthy(status: ConnectorStatus): boolean {
  return status === "error" || status === "reauth_required";
}
