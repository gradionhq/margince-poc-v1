import { CalendarDays, Mail, Phone, StickyNote } from "lucide-react";
import type { ReactNode } from "react";
import { useState } from "react";
import type { components } from "../api/schema";
import { Badge, SegmentedControl } from "../design-system/atoms";
import { useT } from "../i18n";

// Conversation memory (concept §5.10, ADR-0097 D3).
//
// Threads and meetings as ENTITIES, condensed — what the conversation was
// about, not the transport events it was made of. The Activity tab remains the
// complete raw ledger beside it: a summary never replaces the original, and a
// withheld activity never leaks through one.

type Person360 = components["schemas"]["Person360"];
type Activity = components["schemas"]["Activity"];

const FILTERS = ["all", "email", "meetings", "calls", "notes"] as const;
type Filter = (typeof FILTERS)[number];

export function PersonMemory({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  const [filter, setFilter] = useState<Filter>("all");
  const entries = view.conversation_memory ?? [];
  // Until the thread projection is filled, the timeline the page already read
  // IS the memory: same rows, condensed the same way. It reads from what the
  // 360 assembled rather than fetching, so it cannot show a record the page
  // beside it is withholding.
  const rows =
    entries.length > 0
      ? entries.map((entry) => fromEntry(entry, t))
      : foldActivities(view, t);
  const shown = rows.filter((row) => matches(row, filter));

  return (
    <section className="pe-card pe-memory" data-testid="person-memory">
      <h3 className="pe-card-title">{t("person.memory.title")}</h3>
      <div className="pe-memory-filters">
        <SegmentedControl
          options={FILTERS}
          value={filter}
          onChange={setFilter}
          labels={{
            all: t("person.memory.all"),
            email: t("person.memory.email"),
            meetings: t("person.memory.meetings"),
            calls: t("person.memory.calls"),
            notes: t("person.memory.notes"),
          }}
        />
      </div>
      {shown.length === 0 && (
        <p className="pe-prose">{t("person.memory.empty")}</p>
      )}
      {shown.map((row) => (
        <div className="pe-memory-row" key={row.key}>
          <span className="pe-memory-date">{row.date}</span>
          <span className="pe-memory-channel">
            {channelIcon(row.channel)}
            {row.channelLabel}
          </span>
          <span>
            <span className="pe-memory-title">{row.title}</span>
            <span className="pe-memory-summary">{row.summary}</span>
          </span>
          {row.status ? (
            <Badge tone={row.tone}>{row.statusLabel}</Badge>
          ) : (
            <span />
          )}
          <span className="pe-memory-time">{row.time}</span>
        </div>
      ))}
    </section>
  );
}

type Row = {
  key: string;
  date: string;
  time: string;
  channel: string;
  channelLabel: string;
  title: string;
  summary: string;
  // `status` stays the STORED key and `statusLabel` is what the reader sees.
  // Folding them into one field would make the badge's tone depend on the
  // active locale, since tone is chosen by the same word.
  status: string | null;
  statusLabel: string;
  tone: "success" | "warn" | "accent" | undefined;
};

function fromEntry(
  entry: NonNullable<Person360["conversation_memory"]>[number],
  t: ReturnType<typeof useT>,
): Row {
  const status = entry.status ?? null;
  return {
    key: entry.key,
    date: dayMonth(entry.occurred_at),
    time: clock(entry.occurred_at),
    channel: entry.channel,
    channelLabel: labelFor(entry.channel, t),
    title: entry.title,
    summary: entry.summary,
    status,
    statusLabel: statusLabel(status, t),
    tone: toneFor(status),
  };
}

// The deterministic floor: one captured activity is one entry, its subject the
// title and its body the summary. It is what the card shows when no thread
// summary has been generated — plainer, never blank.
function foldActivities(view: Person360, t: ReturnType<typeof useT>): Row[] {
  const rows = view.activities?.data ?? [];
  return rows
    .filter((row) => !isFuture(row))
    .map((row) => {
      const status = statusOf(row, view);
      return {
        key: row.id,
        date: dayMonth(row.occurred_at),
        time: clock(row.occurred_at),
        channel: row.kind,
        channelLabel: labelFor(row.kind, t),
        title: row.subject ?? labelFor(row.kind, t),
        summary: row.body ?? "",
        status,
        statusLabel: statusLabel(status, t),
        tone: toneFor(status),
      };
    });
}

// A meeting that has not happened is not memory. It is on the strip and in the
// Today card; repeating it here as something remembered would be wrong.
function isFuture(row: Activity): boolean {
  return new Date(row.occurred_at).getTime() > Date.now();
}

// Whether anybody answered. Derived from the two directions the page already
// read, so it agrees with the strip above it.
function statusOf(row: Activity, view: Person360): string | null {
  if (row.direction === "inbound") {
    const answered =
      view.last_outbound_at != null &&
      new Date(view.last_outbound_at) > new Date(row.occurred_at);
    return answered ? "replied" : "unanswered";
  }
  return null;
}

// A status the server sends but this client has no word for is shown as it was
// stored rather than dropped: an unlabelled badge is still evidence, an absent
// one is a claim that the exchange has no status.
function statusLabel(
  status: string | null,
  t: ReturnType<typeof useT>,
): string {
  switch (status) {
    case "replied":
      return t("person.memory.replied");
    case "unanswered":
      return t("person.memory.unanswered");
    default:
      return status ?? "";
  }
}

function toneFor(
  status: string | null,
): "success" | "warn" | "accent" | undefined {
  switch (status) {
    case "replied":
      return "success";
    case "unanswered":
      return "warn";
    case "awaiting_them":
      return "accent";
    default:
      return undefined;
  }
}

function matches(row: Row, filter: Filter): boolean {
  switch (filter) {
    case "all":
      return true;
    case "email":
      return row.channel === "email";
    case "meetings":
      return row.channel === "meeting";
    case "calls":
      return row.channel === "call";
    case "notes":
      return row.channel === "note";
    default:
      return true;
  }
}

function labelFor(kind: string, t: ReturnType<typeof useT>): string {
  switch (kind) {
    case "email":
      return t("person.memory.channelEmail");
    case "meeting":
      return t("person.memory.channelMeeting");
    case "call":
      return t("person.memory.channelCall");
    case "note":
      return t("person.memory.channelNote");
    default:
      return kind;
  }
}

function channelIcon(kind: string): ReactNode {
  switch (kind) {
    case "meeting":
      return <CalendarDays size={13} aria-hidden="true" />;
    case "call":
      return <Phone size={13} aria-hidden="true" />;
    case "note":
      return <StickyNote size={13} aria-hidden="true" />;
    default:
      return <Mail size={13} aria-hidden="true" />;
  }
}

function dayMonth(at: string): string {
  return new Date(at).toLocaleDateString(undefined, {
    day: "numeric",
    month: "short",
  });
}

// The exact time, because a reader deciding whether to follow up wants the
// hour and not "recently".
function clock(at: string): string {
  return new Date(at).toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  });
}
