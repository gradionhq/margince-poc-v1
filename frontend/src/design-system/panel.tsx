// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { ReactNode } from "react";
import "./panel.css";

// Panel is the titled-card shape Card does not offer: a fixed-height header
// row (a title alone, a title with a badge, or a title with a button all read
// the same height), full-bleed rows under it, and an optional footer for a
// figure that belongs to the whole panel rather than to any one row.
//
// The header and the body are two different rhythms living in one box — the
// header's own 48px band versus the body's padded content versus a row that
// wants to touch the panel's own edges — which is why the padded content is a
// separate `PanelBody` rather than a prop: a caller who needs both padded text
// and full-bleed rows in the same panel nests `PanelBody` and `PanelRow` as
// siblings instead of fighting one slot that tries to be both.
export function Panel({
  title,
  titleAction,
  tone,
  actions,
  footer,
  children,
  className,
}: Readonly<{
  title?: ReactNode;
  // Rendered right-aligned in the header, beside the title — a badge, a
  // button, a count. Absent leaves the title alone in its row.
  titleAction?: ReactNode;
  // "accent" is the one card on a page that ASKS FOR A MOVE rather than
  // reporting state: an accent border, a tinted header band, and the title at
  // reading size so a reader finds it before the panels around it. There is
  // exactly one tone and it is not a palette — a second tinted panel on the
  // same page is two leads, which is none.
  //
  // It is a prop rather than a class a screen sheet adds because the tint has
  // to reach `.panel-head` and `.panel-foot`, which are this component's own
  // internals: a screen reaching into them is a second author for a rhythm
  // this file owns, and the two drift the first time either moves.
  tone?: "accent";
  // Verbs that CHANGE this panel, in their own band under the body — not one
  // more row, and not a footer, which reports rather than acts. A caller
  // renders them only when the panel's content is real: an "add a deal"
  // button under a section whose read failed offers a write nobody can say
  // makes sense.
  actions?: ReactNode;
  // A figure or a link that belongs to the SECTION rather than to any one row
  // — a lifetime total, a "see all" link — so it sits below the rows in its
  // own band rather than as one more row.
  footer?: ReactNode;
  children: ReactNode;
  className?: string;
}>) {
  return (
    <section
      className={[
        "panel",
        tone === "accent" ? "panel-accent" : "",
        className ?? "",
      ]
        .filter(Boolean)
        .join(" ")}
    >
      {title && (
        <header className="panel-head">
          <h2>{title}</h2>
          {titleAction}
        </header>
      )}
      {children}
      {actions && <div className="panel-actions">{actions}</div>}
      {footer && <footer className="panel-foot">{footer}</footer>}
    </section>
  );
}

// PanelPlate is the recessed plate inside a panel, inset from its edges: what
// IS, set apart from what to DO. The device is the whole point of it — the
// rows below run full-bleed on the panel's own ground and read as pressable,
// the plate does not, and a reader can tell the two halves apart before
// reading a word of either. It holds context, never a control.
export function PanelPlate({
  children,
  className,
}: Readonly<{ children: ReactNode; className?: string }>) {
  return (
    <div className={["panel-plate", className ?? ""].filter(Boolean).join(" ")}>
      {children}
    </div>
  );
}

// PanelBody is the padded content slot: text, a form, a FieldGrid — anything
// that is not a row and wants the panel's inner margin. Rows are passed as
// Panel's direct children instead, so they can run full-bleed against the
// panel's own edges.
export function PanelBody({
  children,
  className,
}: Readonly<{ children: ReactNode; className?: string }>) {
  return (
    <div className={["panel-body", className ?? ""].filter(Boolean).join(" ")}>
      {children}
    </div>
  );
}

// PanelRow is the full-bleed hairline row every list inside a panel wants: a
// top border against the row above (none on the first), a hover fill, and
// content that runs edge to edge rather than sitting in the body's padding.
export function PanelRow({
  children,
  className,
}: Readonly<{ children: ReactNode; className?: string }>) {
  return (
    <div className={["panel-row", className ?? ""].filter(Boolean).join(" ")}>
      {children}
    </div>
  );
}
