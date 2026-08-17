import {
  CalendarDays,
  CheckSquare,
  FileText,
  Mail,
  MessageSquare,
  Phone,
  StickyNote,
} from "lucide-react";
import type { ReactNode } from "react";
import { useT } from "../i18n";
import { useProviderLabel } from "./channelproviders";

// How a captured interaction is drawn and named, in ONE place.
//
// Since ADR-0107/A158 the KIND (what sort of interaction happened) and the
// TRANSPORT (what carried it) are separate axes, and reading one off the other
// is what drew an envelope beside every chat message on a contact page — on
// contacts who have no email address at all. So the icon comes from the kind,
// the transport name comes from the directory, and neither is inferred from the
// other.
//
// It lives beside the screens rather than in the design system because both
// halves are domain reads: the kind vocabulary is the activity contract's, and
// the label resolves through the installation's own transport directory.

// interactionIcon draws the kind, and ONLY the kind. Every case is a kind the
// activity contract declares; the fallback is deliberately a plain record mark,
// because an icon for a kind this build has never heard of can only be a guess
// about the transport, and the envelope that used to stand there was the wrong
// guess for every kind but one.
export function interactionIcon(
  kind: string | null | undefined,
  size = 13,
): ReactNode {
  switch (kind) {
    case "email":
      return <Mail size={size} aria-hidden="true" />;
    case "meeting":
      return <CalendarDays size={size} aria-hidden="true" />;
    case "call":
      return <Phone size={size} aria-hidden="true" />;
    case "note":
      return <StickyNote size={size} aria-hidden="true" />;
    case "task":
      return <CheckSquare size={size} aria-hidden="true" />;
    case "message":
      return <MessageSquare size={size} aria-hidden="true" />;
    default:
      return <FileText size={size} aria-hidden="true" />;
  }
}

// useInteractionLabel names a captured interaction for a reader: the transport
// when one carried it, the kind otherwise.
//
// A transport is named by the DIRECTORY, never by a table in this file — which
// providers exist is a deployment fact, so a switch here could only ever be
// right for the providers this build happens to ship with. The resolver falls
// back to the raw provider id, so a transport an extension unit added still
// reads as something rather than blanking.
export function useInteractionLabel(): (
  kind: string | null | undefined,
  provider?: string | null,
) => string {
  const t = useT();
  const providerLabel = useProviderLabel();
  return (kind, provider) => {
    if (provider) {
      return providerLabel(provider);
    }
    switch (kind) {
      case "email":
        return t("person.memory.channelEmail");
      case "meeting":
        return t("person.memory.channelMeeting");
      case "call":
        return t("person.memory.channelCall");
      case "note":
        return t("person.memory.channelNote");
      case "task":
        return t("person.memory.channelTask");
      case "message":
        // A message the contract says must name a transport, arriving without
        // one. Naming it plainly is the honest read; inventing a transport for
        // it would be the defect this module exists to stop.
        return t("person.memory.channelMessage");
      default:
        return kind ?? "";
    }
  };
}
