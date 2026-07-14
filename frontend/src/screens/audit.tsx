import { Bot, CircleUser, Cog, type LucideIcon, Plug } from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { Badge } from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import "./audit.css";

// Reusable audit attribution — turns a raw AuditLogEntry (opaque uuids like
// `human:u1` / `custom_field cf-1`) into a line a person can read. Shared by
// every audit surface (the settings audit log, the custom-fields change rail,
// and any future record-history view) so attribution reads the same everywhere.

type AuditLogEntry = components["schemas"]["AuditLogEntry"];
type ActorFields = Pick<
  AuditLogEntry,
  "actor_type" | "actor_id" | "on_behalf_of"
>;

// A snake_case enum value from the wire (an action / entity_type) is data, not
// UI copy — de-underscore it into a readable phrase so the reader sees "custom
// field", not "custom_field". The value itself is not a localizable string.
export function humanizeToken(token: string): string {
  return token.replace(/_/g, " ");
}

const ACTOR_ICON: Record<AuditLogEntry["actor_type"], LucideIcon> = {
  human: CircleUser,
  agent: Bot,
  system: Cog,
  connector: Plug,
};

// ActorTag renders WHO acted in human terms. A human actor is an opaque uuid on
// the wire, so it reads as "You" (the viewer) or "A teammate" — never the raw
// id. Agents and connectors carry a readable slug (sdr, gmail), shown with a
// typed icon; an agent also names the human it acted on behalf of.
export function ActorTag({
  entry,
  meUserId,
}: Readonly<{ entry: ActorFields; meUserId?: string }>) {
  const t = useT();
  const Icon = ACTOR_ICON[entry.actor_type];

  let label: ReactNode;
  if (entry.actor_type === "human") {
    label =
      meUserId && entry.actor_id === meUserId
        ? t("audit.you")
        : t("audit.teammate");
  } else if (entry.actor_type === "system") {
    label = t("audit.system");
  } else {
    // agent / connector — the id is a readable, workspace-chosen slug
    label = <span className="t-mono">{entry.actor_id}</span>;
  }

  const onBehalf =
    entry.actor_type === "agent" && entry.on_behalf_of
      ? entry.on_behalf_of === meUserId
        ? t("audit.onBehalfOfYou")
        : t("audit.onBehalfOfTeammate")
      : null;

  return (
    <span className="audit-actor">
      <Icon aria-hidden />
      {label}
      {onBehalf && <span className="audit-behalf">{onBehalf}</span>}
    </span>
  );
}

// AuditEntryLine is the one-line rendering of an audit entry: who, what, on
// which kind of thing, and when. The entity's uuid is deliberately dropped —
// it is opaque to a reader and carries no meaning without a name lookup.
export function AuditEntryLine({
  entry,
  meUserId,
}: Readonly<{ entry: AuditLogEntry; meUserId?: string }>) {
  const { locale } = useLocale();
  // Audit times read in the viewer's own timezone, not a fixed one — an
  // investigator in any region sees the moment in their local wall-clock.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return (
    <div className="audit-line">
      <ActorTag entry={entry} meUserId={meUserId} />
      <Badge tone="accent">{humanizeToken(entry.action)}</Badge>
      <span className="audit-entity">{humanizeToken(entry.entity_type)}</span>
      <time className="audit-when" dateTime={entry.occurred_at}>
        {formatDateTime(entry.occurred_at, locale, zone)}
      </time>
    </div>
  );
}
