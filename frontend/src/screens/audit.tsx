import {
  Bot,
  CircleUser,
  Cog,
  type LucideIcon,
  Plug,
  UserRoundCheck,
} from "lucide-react";
import type { components } from "../api/schema";
import { Badge } from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
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
  | "passport_id"
> & {
  // The tool's registered name, when the write came through an OAuth grant.
  // Optional because the compliance log's own entry does not carry it yet —
  // record history does, and both surfaces render through this one component.
  agent_client?: string | null;
};

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
  // A person, not a machine: a buyer decided this. The icon differs from a
  // member's so a reader can tell at a glance that the actor sits outside the
  // organization and will not be found in the member directory.
  buyer: UserRoundCheck,
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
  // The tool's own name, when the row records which one. Mutually exclusive
  // with qualifierKey: a named client replaces the generic phrase rather than
  // appending to it.
  qualifierName?: string;
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
  // A Deal Room participant. The read path resolves actor_name from app_user
  // for humans only, and a buyer holds no seat, so no name arrives today and
  // this renders the kind rather than inventing one. Naming the participant
  // means resolving actor_id against deal_room_participant on the read path —
  // when that lands, the name replaces the label here.
  if (entry.actor_type === "buyer") {
    return {
      labelKey: "audit.unknownBuyer",
      qualifierKey: "audit.viaDealRoom",
    };
  }

  const qualifierKey = MACHINE_QUALIFIER[entry.actor_type];
  // The tool's own name beats the generic phrase. "via Claude" tells a reader
  // which connection wrote the row; "via an agent" tells them what they could
  // already see from the icon.
  const qualifier = entry.agent_client
    ? { qualifierName: entry.agent_client }
    : { qualifierKey };
  // A machine acting under a human's authority READS AS THAT HUMAN.
  if (entry.on_behalf_of && meUserId && entry.on_behalf_of === meUserId) {
    return { labelKey: "audit.you", ...qualifier };
  }
  if (entry.on_behalf_of_name) {
    return { name: entry.on_behalf_of_name, ...qualifier };
  }
  if (entry.passport_id) {
    // A GRANT was presented and yet no human resolved behind it. That is a
    // gap: a passport's granting human is recorded when the grant is made, so
    // the row failed to carry what existed. It says so, and does NOT fall back
    // to "System" — system is reserved for a change that genuinely has nobody
    // behind it, and absorbing the gap would hide it on the one surface that
    // exists to expose it.
    return {
      labelKey: "audit.noHumanAuthority",
      identifier: machineIdentifier(entry),
      ...qualifier,
    };
  }
  // No grant was presented, so there is no human to name and no gap to report:
  // a background pass nobody's context ran, or a connector with no connect
  // flow. The deciding question is whether a grant was presented — not which
  // machine word actor_type happens to carry. The id is the readable,
  // workspace-chosen name of the thing that acted.
  return { identifier: machineIdentifier(entry), ...qualifier };
}

// machineIdentifier is the machine's own id, with a typed fallback for the one
// state the contract permits and no writer should produce: actor_id is declared
// as a bare `string` with no minimum length, and an empty one would render an
// icon with no label beside it — a row attributed to nothing at all.
function machineIdentifier(entry: ActorFields): string {
  // A registered client name, when there is one: `agent:01a0216f-…` is the
  // truth and unreadable, and a reader asking "what changed my record" is not
  // helped by a uuid they cannot look up.
  if (entry.agent_client) {
    return entry.agent_client;
  }
  return entry.actor_id === "" ? entry.actor_type : entry.actor_id;
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
  const { labelKey, name, qualifierKey, qualifierName, identifier } =
    actorAttribution(entry, meUserId);

  return (
    <span className="audit-actor">
      <Icon aria-hidden />
      {/* The person is its own element, not a bare text node: it is the
          primary half of the attribution, and a reader (or a test) should be
          able to address who acted separately from what they acted through. */}
      <span className="audit-actor-name">
        {name ?? (labelKey ? t(labelKey) : null)}
      </span>
      {identifier && (
        <span className="t-mono audit-actor-id">{identifier}</span>
      )}
      {qualifierName && (
        <span className="audit-behalf">
          {t("audit.viaNamed", { client: qualifierName })}
        </span>
      )}
      {!qualifierName && qualifierKey && (
        <span className="audit-behalf">{t(qualifierKey)}</span>
      )}
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
  return (
    <div className="audit-line">
      <ActorTag entry={entry} meUserId={meUserId} />
      <Badge tone="accent">{humanizeToken(entry.action)}</Badge>
      <span className="audit-entity">{humanizeToken(entry.entity_type)}</span>
      {/* An audit entry is a fact in the organization's book, like the change
          history beside it, so it reads on the organization's clock. On the
          viewer's clock an entry at 18:00Z is 21 August to a reader in Berlin
          and 22 August to one in Ho Chi Minh City: two investigators quoting
          the same line quote different days, which is the failure a shared
          record clock exists to prevent. `dateTime` carries the unambiguous
          instant for anyone who has to convert. */}
      <time className="audit-when" dateTime={entry.occurred_at}>
        {formatDateTime(entry.occurred_at, locale, RECORD_ZONE)}
      </time>
    </div>
  );
}
