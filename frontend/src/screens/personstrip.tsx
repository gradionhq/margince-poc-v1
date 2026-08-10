import {
  ArrowUpRight,
  CalendarDays,
  CircleDollarSign,
  MoveHorizontal,
  ShieldCheck,
  TrendingDown,
} from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";

// The relationship state strip (concept §5.3): six facts that change how a
// reader interprets everything below them.
//
// The two DIRECTIONS are separate slots and never folded into one "last
// touch". Which way the last message went is the whole question — a contact we
// mailed a fortnight ago with no reply and one who wrote to us this morning
// have the same last-touch date and opposite meanings.
//
// Every slot distinguishes three states, and the difference matters: a value,
// a fact that there is none ("None", "Never"), and a section the caller may
// not read. Only the last renders as withheld — "no open deal" is an answer.

type Person360 = components["schemas"]["Person360"];

export function PersonStrip({
  view,
  consent,
}: Readonly<{
  view: Person360;
  consent: string | null;
}>) {
  const omitted = new Set(view.sections_omitted ?? []);
  return (
    <div className="pe-strip" data-testid="person-strip">
      <Slot
        icon={<TrendingDown size={18} aria-hidden="true" />}
        label="Last inbound"
        value={relativeDays(view.last_inbound_at)}
        withheld={omitted.has("last_touch")}
      />
      <Slot
        icon={<ArrowUpRight size={18} aria-hidden="true" />}
        label="Last outbound"
        value={relativeDays(view.last_outbound_at)}
        withheld={omitted.has("last_touch")}
      />
      <Slot
        icon={<MoveHorizontal size={18} aria-hidden="true" />}
        label="Reciprocity"
        value={reciprocity(view)}
        withheld={omitted.has("activities")}
      />
      <Slot
        icon={<CircleDollarSign size={18} aria-hidden="true" />}
        label="Open deal"
        value={openDeal(view)}
        withheld={omitted.has("commercial")}
      />
      <Slot
        icon={<CalendarDays size={18} aria-hidden="true" />}
        label="Next meeting"
        value={nextMeeting(view)}
        withheld={omitted.has("next_meeting")}
      />
      <Slot
        icon={<ShieldCheck size={18} aria-hidden="true" />}
        label="Consent"
        value={consent ?? "Unknown"}
        tone={consentTone(consent)}
        withheld={omitted.has("consent")}
      />
    </div>
  );
}

function Slot({
  icon,
  label,
  value,
  tone,
  withheld,
}: Readonly<{
  icon: ReactNode;
  label: string;
  value: string;
  tone?: "good" | "muted";
  withheld?: boolean;
}>) {
  // A withheld slot says so. Rendering it empty would read as "there is none",
  // which is a claim about the record rather than about the reader's grants.
  const shown = withheld ? "Not shown" : value;
  const toneClass = withheld ? "muted" : tone;
  return (
    <div className="pe-slot">
      <span className="pe-slot-icon">{icon}</span>
      <span className="pe-slot-body">
        <span className="pe-slot-label">{label}</span>
        <span
          className={
            toneClass ? `pe-slot-value pe-slot-value-${toneClass}` : "pe-slot-value"
          }
          title={shown}
        >
          {shown}
        </span>
      </span>
    </div>
  );
}

// relativeDays reads a timestamp the way a person says it. "Never" is reserved
// for a read that HAPPENED and found nothing — the caller decides that by
// passing null only when the section was readable.
function relativeDays(at: string | null | undefined): string {
  if (!at) {
    return "Never";
  }
  const days = Math.floor((Date.now() - new Date(at).getTime()) / 86_400_000);
  if (days <= 0) {
    return "Today";
  }
  if (days === 1) {
    return "Yesterday";
  }
  return `${days} days`;
}

// Counts, not a score. A standalone number here would be the composite verdict
// the face deliberately does not carry (ADR-0096 D1).
function reciprocity(view: Person360): string {
  const rows = view.activities?.data ?? [];
  let inbound = 0;
  let outbound = 0;
  for (const row of rows) {
    if (row.direction === "inbound") {
      inbound += 1;
    }
    if (row.direction === "outbound") {
      outbound += 1;
    }
  }
  return `${inbound} in · ${outbound} out`;
}

function openDeal(view: Person360): string {
  const deal = view.commercial?.deal;
  if (!deal) {
    return "No open deal";
  }
  if (deal.amount_minor == null || !deal.currency) {
    return deal.title;
  }
  return money(deal.amount_minor, deal.currency);
}

function nextMeeting(view: Person360): string {
  const meeting = view.next_meeting;
  if (!meeting) {
    return "None";
  }
  return new Date(meeting.starts_at).toLocaleDateString(undefined, {
    day: "numeric",
    month: "short",
  });
}

function consentTone(consent: string | null): "good" | "muted" | undefined {
  if (consent === "Allowed") {
    return "good";
  }
  return consent ? undefined : "muted";
}

// Money arrives in MINOR units and is rendered whole: the strip shows €95k,
// not €95,000.00, because the slot is a glance and the exact figure lives on
// the deal card below.
export function money(minor: number, currency: string): string {
  const major = minor / 100;
  if (major >= 1000) {
    return `${symbolFor(currency)}${Math.round(major / 1000)}k`;
  }
  return `${symbolFor(currency)}${major}`;
}

function symbolFor(currency: string): string {
  switch (currency) {
    case "EUR":
      return "€";
    case "USD":
      return "$";
    case "GBP":
      return "£";
    default:
      return `${currency} `;
  }
}
