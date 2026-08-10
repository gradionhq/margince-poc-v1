import { CalendarDays, Mail, Phone, StickyNote } from "lucide-react";
import type { ReactNode } from "react";
import { useState } from "react";
import type { components } from "../api/schema";
import { Badge, SegmentedControl } from "../design-system/atoms";

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

const FILTER_LABELS: Readonly<Record<Filter, string>> = {
  all: "All",
  email: "Email",
  meetings: "Meetings",
  calls: "Calls",
  notes: "Notes",
};

export function PersonMemory({ view }: Readonly<{ view: Person360 }>) {
  const [filter, setFilter] = useState<Filter>("all");
  const entries = view.conversation_memory ?? [];
  // Until the thread projection is filled, the timeline the page already read
  // IS the memory: same rows, condensed the same way. It reads from what the
  // 360 assembled rather than fetching, so it cannot show a record the page
  // beside it is withholding.
  const rows = entries.length > 0 ? entries.map(fromEntry) : foldActivities(view);
  const shown = rows.filter((row) => matches(row, filter));

  return (
    <section className="pe-card pe-memory" data-testid="person-memory">
      <h3 className="pe-card-title">Conversation memory</h3>
      <div className="pe-memory-filters">
        <SegmentedControl
          options={FILTERS}
          value={filter}
          onChange={setFilter}
          labels={FILTER_LABELS}
        />
      </div>
      {shown.length === 0 && (
        <p className="pe-prose">Nothing captured on this channel yet.</p>
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
          {row.status ? <Badge tone={row.tone}>{row.status}</Badge> : <span />}
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
  status: string | null;
  tone: "success" | "warn" | "accent" | undefined;
};

function fromEntry(
  entry: NonNullable<Person360["conversation_memory"]>[number],
): Row {
  return {
    key: entry.key,
    date: dayMonth(entry.occurred_at),
    time: clock(entry.occurred_at),
    channel: entry.channel,
    channelLabel: labelFor(entry.channel),
    title: entry.title,
    summary: entry.summary,
    status: entry.status ?? null,
    tone: toneFor(entry.status ?? null),
  };
}

// The deterministic floor: one captured activity is one entry, its subject the
// title and its body the summary. It is what the card shows when no thread
// summary has been generated — plainer, never blank.
function foldActivities(view: Person360): Row[] {
  const rows = view.activities?.data ?? [];
  return rows
    .filter((row) => !isFuture(row))
    .map((row) => ({
      key: row.id,
      date: dayMonth(row.occurred_at),
      time: clock(row.occurred_at),
      channel: row.kind,
      channelLabel: labelFor(row.kind),
      title: row.subject ?? labelFor(row.kind),
      summary: row.body ?? "",
      status: statusOf(row, view),
      tone: toneFor(statusOf(row, view)),
    }));
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
    return answered ? "Replied" : "Unanswered";
  }
  return null;
}

function toneFor(
  status: string | null,
): "success" | "warn" | "accent" | undefined {
  switch (status) {
    case "Replied":
    case "replied":
      return "success";
    case "Unanswered":
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

function labelFor(kind: string): string {
  switch (kind) {
    case "email":
      return "Email";
    case "meeting":
      return "Meeting";
    case "call":
      return "Call";
    case "note":
      return "Note";
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
