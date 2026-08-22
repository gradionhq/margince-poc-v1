import type { components } from "../api/schema";
import { StatCard } from "../design-system/atoms";
import { StatStrip } from "../design-system/statstrip";
import { toMajorUnits } from "../format/minorunits";
import { useT } from "../i18n";

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
  consentVerdict,
}: Readonly<{
  view: Person360;
  consentVerdict: string | undefined;
}>) {
  const t = useT();
  const omitted = new Set(view.sections_omitted ?? []);
  // A withheld slot says so. Rendering it empty would read as "there is none",
  // which is a claim about the record rather than about the reader's grants —
  // and a withheld reading carries no tone, because there is no verdict to
  // colour.
  const reading = (value: string, withheld: boolean) =>
    withheld ? t("record.notShown") : value;
  const consentIsShown = !omitted.has("consent");
  return (
    <StatStrip testId="person-strip">
      <StatCard
        label={t("person.strip.lastInbound")}
        value={reading(
          relativeDays(view.last_inbound_at, t),
          omitted.has("last_touch"),
        )}
      />
      <StatCard
        label={t("person.strip.lastOutbound")}
        value={reading(
          relativeDays(view.last_outbound_at, t),
          omitted.has("last_touch"),
        )}
      />
      <StatCard
        label={t("person.strip.reciprocity")}
        value={reading(reciprocity(view, t), omitted.has("activities"))}
      />
      <StatCard
        label={t("person.strip.openDeal")}
        value={reading(openDeal(view, t), omitted.has("commercial"))}
      />
      <StatCard
        label={t("person.strip.nextMeeting")}
        value={reading(nextMeeting(view, t), omitted.has("next_meeting"))}
      />
      <StatCard
        label={t("person.strip.consent")}
        value={reading(consentWord(consentVerdict, t), !consentIsShown)}
        tone={consentIsShown ? consentTone(consentVerdict) : undefined}
        dot={consentIsShown}
      />
    </StatStrip>
  );
}

// relativeDays reads a timestamp the way a person says it. "Never" is reserved
// for a read that HAPPENED and found nothing — the caller decides that by
// passing null only when the section was readable.
function relativeDays(
  at: string | null | undefined,
  t: ReturnType<typeof useT>,
): string {
  if (!at) {
    return t("person.strip.never");
  }
  const days = Math.floor((Date.now() - new Date(at).getTime()) / 86_400_000);
  if (days <= 0) {
    return t("person.strip.today");
  }
  if (days === 1) {
    return t("person.strip.yesterday");
  }
  return t("person.strip.days", { count: days });
}

// Counts, not a score. A standalone number here would be the composite verdict
// the face deliberately does not carry (ADR-0096 D1).
function reciprocity(view: Person360, t: ReturnType<typeof useT>): string {
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
  return t("person.strip.inOut", { inbound, outbound });
}

function openDeal(view: Person360, t: ReturnType<typeof useT>): string {
  const deal = view.commercial?.deal;
  if (!deal) {
    return t("person.strip.noOpenDeal");
  }
  if (deal.amount_minor == null || !deal.currency) {
    return deal.title;
  }
  return money(deal.amount_minor, deal.currency);
}

function nextMeeting(view: Person360, t: ReturnType<typeof useT>): string {
  const meeting = view.next_meeting;
  if (!meeting) {
    return t("person.strip.noMeeting");
  }
  return new Date(meeting.starts_at).toLocaleDateString(undefined, {
    day: "numeric",
    month: "short",
  });
}

// The verdict word and its tone are read from the SERVER's verdict key, never
// from the rendered word: a translated label must not change how the slot is
// coloured.
export function consentWord(
  verdict: string | undefined,
  t: ReturnType<typeof useT>,
): string {
  switch (verdict) {
    case "allowed":
      return t("person.consent.allowedWord");
    case "blocked":
      return t("person.consent.blockedWord");
    default:
      return t("person.consent.unknownWord");
  }
}

// A verdict slot is coloured in both directions: allowed reads as allowed and
// blocked reads as blocked. An absent verdict is neither — nobody has judged
// it yet, so it takes no tone rather than borrowing one.
function consentTone(
  verdict: string | undefined,
): "good" | "danger" | undefined {
  if (verdict === "allowed") {
    return "good";
  }
  return verdict === "blocked" ? "danger" : undefined;
}

// Money arrives in MINOR units and is rendered whole: the strip shows €95k,
// not €95,000.00, because the slot is a glance and the exact figure lives on
// the deal card below.
//
// The scale is the CURRENCY's. A hard-coded /100 rendered ₫18,000,000 as
// "VND 180k" — the same hundredfold understatement the three server-side
// copies of this function carried, which is what makes this the fourth.
//
// It is still a fourth FORMATTER, and that half is not fixed here.
// format/formatMoneyCompact does this job locale-aware and currency-aware
// already, but neither this component nor personcards.tsx has a locale in
// scope, and threading one in changes the rendered string on both surfaces —
// a visual change that belongs with the frontend formatter sweep, not with a
// correctness fix to the scale. Adopting it is tracked there.
export function money(minor: number, currency: string): string {
  // The tier comes from the MAGNITUDE and the sign goes in front. Comparing the
  // signed value abbreviated only the positive half, so a credit read
  // "€-95000" with the minus inside the figure — the shape a glance slot exists
  // to avoid. The server-side sibling had the same defect and the same fix.
  const major = toMajorUnits(minor, currency);
  const sign = minor < 0 ? "-" : "";
  const magnitude = Math.abs(major);
  if (magnitude >= 1000) {
    return `${sign}${symbolFor(currency)}${Math.round(magnitude / 1000)}k`;
  }
  return `${sign}${symbolFor(currency)}${magnitude}`;
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
