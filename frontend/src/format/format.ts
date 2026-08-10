import type { Locale } from "../i18n";

// The presentation edge (architecture/10 §1–3): everything here formats
// ALREADY-stored values — minor units, UTC instants, IR-provided base
// aggregates. No FX math, no live rate calls, no calendar arithmetic, and
// locale never flows back into storage. format.test.ts pins each rule.

const INTL_LOCALE: Record<Locale, string> = {
  de: "de-DE",
  en: "en-GB", // A100: unconfigured English is en-GB, not en-US
  vi: "vi-VN",
};

// Money arrives as integer minor units + ISO currency (data-semantics §1).
// The only transformation is the currency's minor-unit scaling — display,
// not arithmetic.
export function formatMoney(
  amountMinor: number,
  currency: string,
  locale: Locale,
): string {
  const formatter = new Intl.NumberFormat(INTL_LOCALE[locale], {
    style: "currency",
    currency,
  });
  const digits = formatter.resolvedOptions().maximumFractionDigits ?? 2;
  return formatter.format(amountMinor / 10 ** digits);
}

/**
 * A money figure for a KPI SLOT: €428k rather than €428,000.00.
 *
 * The strip gives six readings roughly 110px of usable width each, and a full
 * euro amount does not fit — it wraps mid-number or clips, and "€201,099.0" is
 * a different number rather than a smaller rendering of the right one. Both
 * mockups abbreviate here for the same reason.
 *
 * Only where the exact figure is not the point. The finance card below the
 * strip renders `formatMoney` in full, and it is one scroll away — this is the
 * scale of the account, that is the amount.
 *
 * Under 10,000 it stays exact: "€8,332" is as short as "€8k" and says more.
 */
export function formatMoneyCompact(
  amountMinor: number,
  currency: string,
  locale: Locale,
): string {
  const probe = new Intl.NumberFormat(INTL_LOCALE[locale], {
    style: "currency",
    currency,
  });
  const digits = probe.resolvedOptions().maximumFractionDigits ?? 2;
  const major = amountMinor / 10 ** digits;
  return new Intl.NumberFormat(INTL_LOCALE[locale], {
    style: "currency",
    currency,
    // `compact` only kicks in at 1000; below 10_000 the long form is no wider
    // and carries every digit, so the threshold is where abbreviating buys
    // something.
    notation: Math.abs(major) >= 10_000 ? "compact" : "standard",
    maximumFractionDigits: Math.abs(major) >= 10_000 ? 1 : 0,
  }).format(major);
}

export function formatNumber(value: number, locale: Locale): string {
  return new Intl.NumberFormat(INTL_LOCALE[locale]).format(value);
}

// IANA zone names only (AC-DS-TZ4): fixed offsets ("+01:00", "Etc/GMT-1")
// silently freeze DST rules — reject them loudly at the edge.
function assertIanaZone(zone: string): void {
  if (/^[+-]\d{2}:?\d{2}$/.test(zone) || /^(Etc\/)?GMT[+-]?\d*$/i.test(zone)) {
    throw new Error(
      `timezone must be an IANA name, got fixed offset "${zone}"`,
    );
  }
  // Intl itself rejects unknown names — constructing with the zone throws a
  // RangeError we let propagate; format() forces the value to be consumed.
  new Intl.DateTimeFormat("en-US", { timeZone: zone }).format();
}

// Zone-by-purpose (architecture/10 §2): personal deadlines localize to the
// USER zone, reporting-period labels bucket on the WORKSPACE zone — the
// caller picks the purpose, this helper only attaches the zone.
export function formatDate(
  utcIso: string,
  locale: Locale,
  zone: string,
): string {
  assertIanaZone(zone);
  return new Intl.DateTimeFormat(INTL_LOCALE[locale], {
    timeZone: zone,
    day: "2-digit",
    month: "2-digit",
    year: "numeric",
  }).format(new Date(utcIso));
}

export function formatDateTime(
  utcIso: string,
  locale: Locale,
  zone: string,
): string {
  assertIanaZone(zone);
  return new Intl.DateTimeFormat(INTL_LOCALE[locale], {
    timeZone: zone,
    day: "2-digit",
    month: "2-digit",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(utcIso));
}

// Idle/SLA spans display as ABSOLUTE durations (no naive calendar diff —
// architecture/10 §2): the input is a millisecond span already computed
// upstream from two UTC instants.
export function formatDuration(ms: number, locale: Locale): string {
  const days = Math.floor(ms / 86_400_000);
  const hours = Math.floor((ms % 86_400_000) / 3_600_000);
  const unit = new Intl.NumberFormat(INTL_LOCALE[locale], {
    style: "unit",
    unit: "day",
    unitDisplay: "narrow",
  });
  if (days >= 1) {
    return unit.format(days);
  }
  return new Intl.NumberFormat(INTL_LOCALE[locale], {
    style: "unit",
    unit: "hour",
    unitDisplay: "narrow",
  }).format(hours);
}

// FX lineage (ADR-0004): a converted figure ships with its contributing rows
// from the query-plan IR. The UI consumes base_value_minor VERBATIM — it
// never multiplies native × rate and never fetches a rate.
export type FxLineageRow = {
  label: string;
  nativeAmountMinor: number;
  nativeCurrency: string;
  rate: number;
  rateDate: string;
};

export type ExplainedMoney = {
  baseValueMinor: number;
  baseCurrency: string;
  rows: FxLineageRow[];
};
