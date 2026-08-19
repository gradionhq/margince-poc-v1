// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { LucideIcon } from "lucide-react";
import type { JSX } from "react";
import { useTruncationTooltip } from "./tooltip";
import "./breadcrumb.css";

/**
 * One stop on the trail.
 *
 * `href` absent means this stop is not a link — the page the reader is on, or
 * an ancestor with no page of its own (a grouping level the product names but
 * does not serve). That is a real state and not an omission, which is why the
 * type says so rather than asking a caller to pass an empty string.
 *
 * `lang` is a BCP 47 tag for a label that is not in the page's language — the
 * same obligation `Select`'s options carry (WCAG 2.2 AA 3.1.2), and the reason
 * a record named in German reads correctly on an English page.
 */
export type Crumb = {
  readonly label: string;
  readonly href?: string;
  readonly icon?: LucideIcon;
  readonly lang?: string;
};

/**
 * The trail that says where the reader is and how they got here.
 *
 * It generalises the two-segment record crumb the page head drew by hand
 * (the shell's own record crumb: the list, a slash, the record's name), which could only ever
 * be those two segments and hard-coded the fact that the second one was the
 * page. Every decision that one made by position is a rule here: the LAST item
 * is the current page and is never a link even when it carries an `href`,
 * because a link to the page you are already on is a control that does nothing;
 * and it is the only item that gives way under pressure, because ancestors are
 * short nav labels of the product's own choosing while the last one is user
 * data of unbounded length.
 *
 * Separators are `<span aria-hidden>` INSIDE the list item they lead — never
 * list items of their own, which would make a three-stop trail announce as five
 * things, and never CSS `content`, which some screen readers speak and others
 * do not, so the same page reads differently to two readers.
 *
 * It accepts no `className` or `style`: a primitive owns its chrome, and a
 * trail restyled per screen is how the head and the record page came to draw
 * two different crumbs.
 */
export function Breadcrumb({
  items,
  // The landmark's accessible name, translated by the caller. A page can carry
  // more than one navigation landmark, and unnamed ones are indistinguishable
  // in a screen reader's landmark list.
  label,
}: Readonly<{
  items: readonly Crumb[];
  label: string;
}>): JSX.Element | null {
  // Nothing to lead back to. A landmark announcing an empty list is worse than
  // no landmark: it is a place the reader can navigate to and find nothing.
  if (items.length === 0) {
    return null;
  }
  return (
    <nav aria-label={label} className="crumbs">
      <ol>
        {stopsOf(items).map((stop) => (
          <li key={stop.path}>
            {stop.follows && (
              <span aria-hidden="true" className="crumbs-sep">
                /
              </span>
            )}
            <CrumbStop crumb={stop.crumb} current={stop.current} />
          </li>
        ))}
      </ol>
    </nav>
  );
}

/**
 * The trail with each stop's identity and role resolved once, before anything
 * is drawn.
 *
 * A stop's identity is the whole path down to it rather than its own label or
 * its position: two levels can legitimately carry the same name (a section and
 * the entry inside it, a company and the deal named after it), and a position
 * alone re-keys every stop the moment the trail gains a level, which throws
 * away the state of the ones that did not move.
 */
type Stop = {
  readonly crumb: Crumb;
  readonly current: boolean;
  readonly follows: boolean;
  readonly path: string;
};

function stopsOf(items: readonly Crumb[]): readonly Stop[] {
  const lastIndex = items.length - 1;
  return items.map((crumb, index) => ({
    crumb,
    // The current page, which is never a link and is the one stop that gives
    // way when the row runs out of room.
    current: index === lastIndex,
    // Something to separate this stop FROM, which is what earns the slash.
    follows: index > 0,
    path: items
      .slice(0, index + 1)
      .map((stop) => stop.label)
      .join(" / "),
  }));
}

/** The element a stop is drawn as, which is what the three cases differ in. */
function CrumbStop({
  crumb,
  current,
}: Readonly<{ crumb: Crumb; current: boolean }>) {
  if (current) {
    return (
      <span aria-current="page" className="crumbs-current">
        <CrumbFace crumb={crumb} />
      </span>
    );
  }
  if (crumb.href) {
    return (
      <a className="crumbs-link" href={crumb.href}>
        <CrumbFace crumb={crumb} />
      </a>
    );
  }
  return (
    <span className="crumbs-step">
      <CrumbFace crumb={crumb} />
    </span>
  );
}

/**
 * A stop's glyph and its label, with the tip that carries a label the row had
 * to cut.
 *
 * Its own component because the tip is a hook and a hook cannot be called from
 * inside a map. The ref goes on the LABEL rather than on the link or the span
 * around it, because the label is the box that actually clips — measuring the
 * wrapper would ask whether the icon and the text together overflowed, which is
 * a different question and answers "no" exactly when the text is cut.
 */
function CrumbFace({ crumb }: Readonly<{ crumb: Crumb }>) {
  const tip = useTruncationTooltip<HTMLSpanElement>(crumb.label);
  const Glyph = crumb.icon;
  return (
    <>
      {Glyph && <Glyph size={14} strokeWidth={1.8} aria-hidden="true" />}
      <span
        className="crumbs-label"
        lang={crumb.lang}
        ref={tip.ref}
        {...tip.trigger}
      >
        {crumb.label}
        {tip.tip}
      </span>
    </>
  );
}
