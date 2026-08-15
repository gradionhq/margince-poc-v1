// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { ReactNode } from "react";
import { useT } from "../i18n";
import { Button, Skeleton } from "./atoms";
import { Eyebrow } from "./eyebrow";
import "./surfacestate.css";

/**
 * SectionState is what a surface actually knows, and the four base cases are
 * deliberately not collapsed:
 *
 *   ready       — the section came back with rows.
 *   empty       — the section came back, and there are none. A FACT.
 *   withheld    — the caller's role cannot read it; the payload says so.
 *   unavailable — the section is missing and nobody said why: the read
 *                 failed, or the server sent a payload this client does not
 *                 fully understand.
 *
 * empty is the only one that may say "there is none", because it is the only
 * one that knows. Rendering the other three as empty states a fact the page
 * does not have — the reader sees "no open deals" and stops looking.
 *
 * The §7 matrix adds four more, each of which would otherwise be drawn as one
 * of the above and lose what makes it different:
 *
 *   unsupported — this MODE cannot serve the section (an overlay-only
 *                 installation and a native composite section). Distinct from
 *                 unavailable: nothing is broken and retrying changes nothing.
 *   failed      — the read failed and can be retried. `onRetry` is what makes
 *                 it a different state from unavailable rather than a
 *                 differently-worded one.
 *   stale       — the last known value, with when it was last true. It is
 *                 shown rather than withheld, because a figure from this
 *                 morning beats a blank, but never without its `as of`.
 *   partial     — some of the rows, with the count that is missing and a way
 *                 to the full list. Never a silent truncation.
 */
export type SectionState =
  | "ready"
  | "empty"
  | "withheld"
  | "unavailable"
  | "loading"
  | "unsupported"
  | "failed"
  | "stale"
  | "partial";

/**
 * Withholding is the shape of a payload that names the sections its reader may
 * not have: a composite read answers with the sections it could serve plus the
 * list of the ones a grant refused.
 *
 * It is generic in the section name rather than typed as `string[]`, so a
 * caller passing a record whose omissions are a closed union keeps the
 * compile-time check that the section it asks about is one of them. A typo'd
 * section name silently reads as "not withheld", which is the worst possible
 * failure for this function: it answers "you may see it" about something the
 * server never mentioned.
 */
export type Withholding<Section extends string = string> = Readonly<{
  sections_omitted?: readonly Section[];
}>;

/**
 * omitted reports whether the caller's role withheld one section.
 *
 * A payload with no list at all names nothing, so nothing reads as
 * withheld — the section then falls through to its empty state, which is
 * the safe display: "there is none" understates rather than inventing
 * content the caller cannot see.
 */
export function omitted<Section extends string>(
  view: Withholding<Section>,
  section: Section,
): boolean {
  return (view.sections_omitted ?? []).includes(section);
}

/**
 * sectionState classifies one section of a composite read. `present` is
 * whether the payload carried it at all, which is a different question from
 * whether it had rows.
 *
 * No payload at all is two different facts, not one, and `loading` is what
 * tells them apart: the composite read still in flight and the composite
 * read that failed both hand every card an undefined `view`, and without
 * `loading` this function could not see the difference — every section on
 * every record page reads UNAVAILABLE, "some of this page could not be
 * loaded", for as long as the read is still running. On a slow connection
 * that is not a flash, it is the page calling itself broken while its own
 * data is on the way. The cards take an optional view for exactly this
 * reason: a fabricated empty payload would have to claim an as_of it does
 * not have, and would be indistinguishable from a real answer one refactor
 * later.
 */
export function sectionState<Section extends string>(
  view: Withholding<Section> | undefined,
  section: Section,
  present: boolean,
  count: number,
  // Whether the read that would carry `view` is still running. Defaults to
  // false rather than being required so a caller wired to a query that has
  // no pending state of its own (both only ever call this once their OWN
  // guard has already proven `view` defined) is not forced to invent one; a
  // caller reading straight off a composite `view` MUST pass its query's own
  // `isPending`.
  loading = false,
): SectionState {
  if (!view) {
    return loading ? "loading" : "unavailable";
  }
  if (omitted(view, section)) {
    return "withheld";
  }
  if (!present) {
    return "unavailable";
  }
  return count === 0 ? "empty" : "ready";
}

/**
 * SectionDetail is what the four §7 states need in order to say something a
 * reader can act on rather than a differently-worded "not here".
 */
export type SectionDetail = {
  // `failed` without a retry is `unavailable` with extra words.
  onRetry?: () => void;
  // Already formatted by the caller: this tier holds no locale or zone.
  staleAsOf?: string;
  // How many rows the caller is NOT seeing. A truncation nobody states reads
  // as the whole list.
  remaining?: number;
  // Which mode limitation this is, in the caller's words. The generic
  // sentence is the floor, not the target.
  unsupportedReason?: string;
};

/**
 * SurfaceState renders ONE surface's body in whichever of the nine states it
 * is in. A card with two independently-governed sections renders two of
 * these, so neither half's state can speak for the other.
 *
 * `label` names the part, and a card with more than one part MUST pass it:
 * "hidden from you" under a heading covering two sections says which of the
 * two it is only if the part is named. A single-section card leaves it out —
 * the card's own title is already the name.
 *
 * The one string this holds is the state vocabulary itself, keyed `state.*`.
 * A primitive carries no copy about any particular surface, which is why
 * `emptyLabel` is a required prop rather than a ninth key: only the caller
 * knows what there is none OF.
 */
export function SurfaceState({
  label,
  state,
  emptyLabel,
  detail,
  children,
}: Readonly<{
  label?: string;
  state: SectionState;
  emptyLabel: string;
  // What the four §7 states need in order to be honest. Each is read by
  // exactly one state and ignored by the rest; a state whose detail is absent
  // still renders, one sentence shorter.
  detail?: SectionDetail;
  children: ReactNode;
}>) {
  const t = useT();
  const body = (
    <>
      {state === "ready" && children}
      {state === "empty" && <p className="surfacestate-empty">{emptyLabel}</p>}
      {state === "withheld" && (
        <p className="surfacestate-withheld">{t("state.withheld")}</p>
      )}
      {state === "unavailable" && (
        <p className="surfacestate-withheld">{t("state.unavailable")}</p>
      )}
      {state === "loading" && <Skeleton width="100%" height={32} />}
      {state === "unsupported" && (
        <p className="surfacestate-withheld">
          {detail?.unsupportedReason ?? t("state.unsupported")}
        </p>
      )}
      {state === "failed" && (
        <div className="surfacestate-failed">
          <p className="surfacestate-withheld">{t("state.failed")}</p>
          {detail?.onRetry && (
            <Button small onClick={detail.onRetry}>
              {t("state.retry")}
            </Button>
          )}
        </div>
      )}
      {/* The value first, then when it was last true. Reversing them buries
          the caveat under the figure a reader has already taken as current. */}
      {state === "stale" && (
        <>
          <p className="surfacestate-stale">
            {detail?.staleAsOf
              ? t("state.staleAsOf", { when: detail.staleAsOf })
              : t("state.stale")}
          </p>
          {children}
        </>
      )}
      {state === "partial" && (
        <>
          {children}
          <p className="surfacestate-empty">
            {detail?.remaining
              ? t("state.partialCount", { count: detail.remaining })
              : t("state.partial")}
          </p>
        </>
      )}
    </>
  );
  if (!label) {
    return body;
  }
  return (
    <section className="surfacestate-part" aria-label={label}>
      <Eyebrow as="h3">{label}</Eyebrow>
      {body}
    </section>
  );
}
