import type { components } from "../api/schema";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { useFinanceSummary } from "./common";

// A leaf for the small lookup tables the company page and its rail both need
// to draw the same enums the same way. Neither company360.tsx nor
// organizations.tsx imports from here going the OTHER direction, so both may
// import this without a cycle: it is exactly the shared home the two owe each
// other rather than each keeping its own copy that can drift.

type Organization = components["schemas"]["Organization"];
type Lifecycle = NonNullable<Organization["lifecycle"]>;

// Where the account stands with us (PO-DDL-4, ADR-0079/A124), in the words a
// reader sees rather than the wire enum.
export const LIFECYCLE_LABELS: Record<Lifecycle, MessageKey> = {
  unknown: "org.lifecycle.unknown",
  target: "org.lifecycle.target",
  prospect: "org.lifecycle.prospect",
  opportunity: "org.lifecycle.opportunity",
  customer: "org.lifecycle.customer",
  former_customer: "org.lifecycle.former_customer",
  disqualified: "org.lifecycle.disqualified",
};

// Kept in wire order so a picker built off it reads as a progression rather
// than an alphabet. Shared for the same reason LIFECYCLE_LABELS is: the list
// and the labels are the two halves of one enum, and organizations.tsx and
// the rail's own Details grid (companyraildetails.tsx) both build a
// lifecycle picker off it — two copies means the two screens can offer
// different choices for the same field.
export const LIFECYCLE_OPTIONS = [
  "unknown",
  "target",
  "prospect",
  "opportunity",
  "customer",
  "former_customer",
  "disqualified",
] as const;

// The seven wire size bands, same sharing reason as LIFECYCLE_OPTIONS above.
export const SIZE_BAND_OPTIONS = [
  "1-10",
  "11-50",
  "51-200",
  "201-500",
  "501-1000",
  "1001-5000",
  "5000+",
] as const;

// Exported so worstOf (company360.tsx) reads the same three ranks and the
// same order rather than keeping its own copy of the scale it ranks against.
export const HEALTH_RANK = ["at_risk", "good", "strong"] as const;
export type HealthRating = (typeof HEALTH_RANK)[number];

export const HEALTH_RATING_LABEL: Record<HealthRating, MessageKey> = {
  at_risk: "co.health.rating.atRisk",
  good: "co.health.rating.good",
  strong: "co.health.rating.strong",
};

export const HEALTH_DIMENSION_LABEL: Record<
  "relationship" | "commercial" | "payment",
  MessageKey
> = {
  relationship: "co.health.dim.relationship",
  commercial: "co.health.dim.commercial",
  payment: "co.health.dim.payment",
};

// How many days past due a median has to run before it reads as a habit worth
// naming. Named rather than inlined so the threshold is one number a reader
// can find and argue with.
export const PAYMENT_LATE_DAYS = 5;

/**
 * Payment health, read from the finance summary the page already fetches.
 *
 * Rated from the median days after due, which is the reading that says how they
 * PAY rather than what they owe: one late invoice is an exception, a habit is a
 * habit. Below the sample floor the server sends no median, and this returns
 * nothing: "pays on time" concluded from four invoices is a claim about a
 * habit nobody has observed yet.
 *
 * Overdue money outranks the median. An account that pays promptly and has
 * money outstanding right now is at risk today, whatever its habit.
 */
export function usePaymentHealth(orgId?: string) {
  const t = useT();
  const { data } = useFinanceSummary(orgId ?? "");
  if (!orgId || !data) {
    return undefined;
  }
  const overdue = data.overdue?.amount_minor ?? 0;
  if (overdue > 0) {
    return {
      rating: "at_risk" as const,
      reason: t("co.health.payment.overdue"),
    };
  }
  const median = data.median_days_after_due;
  if (median == null) {
    return undefined;
  }
  if (median > PAYMENT_LATE_DAYS) {
    return {
      rating: "good" as const,
      reason: t("co.health.payment.late", { days: median }),
    };
  }
  return {
    rating: "strong" as const,
    reason:
      median < 0
        ? t("finance.medianEarly", { days: Math.abs(median) })
        : t("co.health.payment.onTime"),
  };
}
