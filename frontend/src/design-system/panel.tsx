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
  footer,
  children,
  className,
}: Readonly<{
  title?: ReactNode;
  // Rendered right-aligned in the header, beside the title — a badge, a
  // button, a count. Absent leaves the title alone in its row.
  titleAction?: ReactNode;
  // A figure or a link that belongs to the SECTION rather than to any one row
  // — a lifetime total, a "see all" link — so it sits below the rows in its
  // own band rather than as one more row.
  footer?: ReactNode;
  children: ReactNode;
  className?: string;
}>) {
  return (
    <section className={["panel", className ?? ""].filter(Boolean).join(" ")}>
      {title && (
        <header className="panel-head">
          <h2>{title}</h2>
          {titleAction}
        </header>
      )}
      {children}
      {footer && <footer className="panel-foot">{footer}</footer>}
    </section>
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
