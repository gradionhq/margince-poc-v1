import type { LucideIcon } from "lucide-react";
import { Mail, MessageSquare, PenLine } from "lucide-react";

import type { components } from "../api/schema";
import { useT } from "../i18n";
import { useProviderLabel } from "./channelproviders";

// How this contact can be written to, and what a control that opens the writing
// surface may therefore promise.
//
// It lives beside the person screens rather than inside one of them because the
// answer has two readers: the composer, which OFFERS the transports, and the
// record page's lead verb, which NAMES the one the composer would pick. Those
// two read the same reachability or the button misdescribes what pressing it
// does: a lead verb reading `Email` on a contact who has no address names a
// transport the record does not carry.

type Person360 = components["schemas"]["Person360"];

export type Transport = {
  // `email` or a channel_provider id. The two live in one namespace because the
  // composer picks one of them, and `email` is not a provider grammar match so
  // it cannot collide with one.
  id: string;
  label: string;
  // Absent for mail, which opens its own conversation. Present for every
  // channel, because a channel reply is anchored by contract.
  anchorId?: string;
};

// Which ways this person can be written to, right now, in the order the
// composer offers them.
//
// Mail leads when there is an address, because it is the one transport that can
// OPEN a conversation: `POST /emails` names its addressee. A channel cannot —
// `send-message` resolves the recipient from the conversation being answered
// and there is deliberately no account-started twin — so a channel is offered
// only when this person already has a conversation on it, and the most recent
// one is the anchor a reply would continue.
//
// Reachability and the anchor are BOTH required, and they answer different
// questions: reachability says the identity is live and unblocked, the anchor
// says there is something to continue. A transport with one and not the other
// would be a choice that fails at the send.
export function transportsFor(
  view: Person360,
  providerLabel: (provider: string) => string,
  t: ReturnType<typeof useT>,
): Transport[] {
  const out: Transport[] = [];
  if (view.person.emails?.[0]?.email) {
    out.push({ id: "email", label: t("person.composer.transportEmail") });
  }
  const reachable = new Set(
    (view.person.reachability ?? [])
      .filter((channel) => channel.reachable)
      .map((channel) => channel.provider),
  );
  // Most recent first, so the first row seen for a provider IS its latest
  // conversation — the one a rep means when they pick that transport.
  const newestFirst = [...(view.activities?.data ?? [])].sort(
    (a, b) =>
      new Date(b.occurred_at).getTime() - new Date(a.occurred_at).getTime(),
  );
  for (const activity of newestFirst) {
    const provider = activity.channel_provider;
    if (
      activity.kind !== "message" ||
      !provider ||
      !reachable.has(provider) ||
      out.some((transport) => transport.id === provider)
    ) {
      continue;
    }
    out.push({
      id: provider,
      label: providerLabel(provider),
      anchorId: activity.id,
    });
  }
  return out;
}

// The same answer for a caller that holds only the record.
//
// Both readers need the transport directory and the reader's own language to
// name a provider, and both would otherwise reach for the two hooks themselves:
// one place to forget one of them is enough.
export function useTransports(view: Person360): Transport[] {
  const t = useT();
  const providerLabel = useProviderLabel();
  return transportsFor(view, providerLabel, t);
}

// What a control that opens the composer may call itself, and the mark it wears.
export type TransportAction = {
  label: string;
  icon: LucideIcon;
};

// The lead verb for a record whose transport is chosen adaptively.
//
// With exactly ONE transport the verb names it, because that is what pressing
// the button will do: mail for a contact with an address, the provider's own
// name for one reachable only on a chat channel.
//
// With more than one it must NOT name any of them — the composer opens a picker,
// and a button reading `Email` that lands on a chooser promised a transport it
// then asked about. With none it cannot name one either; the caller disables it,
// and the neutral verb is what a disabled control says while it explains why.
export function primaryTransportAction(
  transports: readonly Transport[],
  t: ReturnType<typeof useT>,
): TransportAction {
  if (transports.length !== 1) {
    return { label: t("person.action.write"), icon: PenLine };
  }
  const only = transports[0];
  if (only.id === "email") {
    return { label: t("person.action.email"), icon: Mail };
  }
  return {
    label: t("person.action.messageOn", { transport: only.label }),
    icon: MessageSquare,
  };
}
