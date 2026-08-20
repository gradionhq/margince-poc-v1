import { Bot, CircleUser, Cog, type LucideIcon, Plug } from "lucide-react";
import type { components } from "../api/schema";
import { Badge } from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import type { MessageKey } from "../i18n/en";
import { useLocale, useT } from "../i18n";
import "./audit.css";

// Reusable audit attribution — turns a raw AuditLogEntry into a line a person
// can read, and specifically into a line that names A PERSON: the wire carries
// opaque ids (`human:<uuid>`, `agent:<passport>`) plus the display names the
// read path resolves for them. Shared by every audit surface (the settings
// audit log, the custom-fields change rail) so attribution reads the same
// everywhere.

type AuditLogEntry = components["schemas"]["AuditLogEntry"];
type ActorFields = Pick<
  AuditLogEntry,
  | "actor_type"
  | "actor_id"
  | "actor_name"
  | "on_behalf_of"
  | "on_behalf_of_name"
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

// The phrase that says a machine did the typing, qualifying the person who
// authorised it. Only the two delegated actor kinds have one: a human needs no
// qualifier and `system` is not acting for anybody.
const MACHINE_QUALIFIER: Partial<
  Record<AuditLogEntry["actor_type"], "audit.viaAgent" | "audit.viaConnector">
> = {
  agent: "audit.viaAgent",
  connector: "audit.viaConnector",
};

// HUMAN_ACTOR_PREFIX is how the wire spells a human actor: audit_log.actor_id
// carries the typed principal id, not a bare uuid. Comparing the raw column to
// a bare user id is how "You" silently stopped rendering — the strings could
// never be equal.
const HUMAN_ACTOR_PREFIX = "human:";

// actorAttribution decides WHO a row is attributed to, as the label a reader
// sees and the qualifier under it.
//
// The person comes first and the machine second (PD-002). An agent's own
// identifier is never the label: attribution exists so somebody can be asked
// about a change, and a passport uuid cannot be asked anything. `identifier` is
// the machine's id, carried only when it is the one thing left to show.
function actorAttribution(
  entry: ActorFields,
  meUserId: string | undefined,
): {
  labelKey?: MessageKey;
  name?: string;
  qualifierKey?: MessageKey;
  identifier?: string;
} {
  if (entry.actor_type === "human") {
    if (meUserId && entry.actor_id === HUMAN_ACTOR_PREFIX + meUserId) {
      return { labelKey: "audit.you" };
    }
    // No app_user resolved: the account is gone, or the row carries an id no
    // directory entry matches. Neither is a name, and neither is a uuid worth
    // putting in front of a reader.
    return entry.actor_name
      ? { name: entry.actor_name }
      : { labelKey: "audit.unknownMember" };
  }
  if (entry.actor_type === "system") {
    return { labelKey: "audit.system" };
  }

  const qualifierKey = MACHINE_QUALIFIER[entry.actor_type];
  // A machine acting under a human's authority READS AS THAT HUMAN.
  if (entry.on_behalf_of && meUserId && entry.on_behalf_of === meUserId) {
    return { labelKey: "audit.you", qualifierKey };
  }
  if (entry.on_behalf_of_name) {
    return { name: entry.on_behalf_of_name, qualifierKey };
  }
  if (entry.actor_type === "agent") {
    // Every passport is granted by a specific human, so an agent row with no
    // authority is a gap in what the write carried. It says so — it does NOT
    // fall back to "System", which is reserved for a change that genuinely has
    // nobody behind it. Absorbing the gap would hide it on the one surface
    // that exists to expose it.
    return { labelKey: "audit.noHumanAuthority", identifier: entry.actor_id };
  }
  // A bare connector is not that gap: some have no connect flow and so no
  // granting human by design. Its id is a readable, workspace-chosen name.
  return { identifier: entry.actor_id };
}

// ActorTag renders WHO acted, naming the PERSON and saying a machine did the
// typing second (PD-002). A rep working through a passport reads as the rep,
// qualified "via an agent" — never as the agent with the rep in a footnote.
// Shared by every audit surface so attribution reads the same everywhere.
export function ActorTag({
  entry,
  meUserId,
}: Readonly<{ entry: ActorFields; meUserId?: string }>) {
  const t = useT();
  const Icon = ACTOR_ICON[entry.actor_type];
  const { labelKey, name, qualifierKey, identifier } = actorAttribution(
    entry,
    meUserId,
  );

  return (
    <span className="audit-actor">
      <Icon aria-hidden />
      {name ?? (labelKey ? t(labelKey) : null)}
      {identifier && (
        <span className="t-mono audit-actor-id">{identifier}</span>
      )}
      {qualifierKey && <span className="audit-behalf">{t(qualifierKey)}</span>}
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
