// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { ReactNode } from "react";
import "./fieldgrid.css";

// FieldGrid lays a record's label/value pairs in two aligned columns — the
// label column shrinks to its content, the value column takes the rest. It
// is the grid around a value, not the value itself: an editable field wraps
// InlineText or InlineChoice as its children rather than this component
// reimplementing the hover-to-edit affordance those two already own.
export function FieldGrid({ children }: Readonly<{ children: ReactNode }>) {
  return <div className="fieldgrid">{children}</div>;
}

// One label/value pair. `children` is a plain node for a read-only fact,
// InlineText (which draws no visible label of its own — its `label` prop is
// screen-reader- and aria-only), or InlineChoice with `hideLabel` set (which
// otherwise draws its own visible "label: value" and would print the field's
// name twice here). `label` is always required and always drawn in this
// column: the grid's whole point is ONE left edge every value starts at, and
// a row that opts out of the label column to let a child draw its own label
// instead is the row that broke that edge.
export function FieldRow({
  label,
  align = "top",
  children,
}: Readonly<{
  label: ReactNode;
  // Where the label sits against its value. "top" — the default, and right for
  // every row whose value is text — puts both on the row's first line, so a
  // label or a value that wraps still opens beside its partner. "middle" is
  // for a value that is a BOX rather than a line of text (a lifecycle badge, a
  // chip): taller than the label naming it, and visibly hung too high when the
  // two share a top edge.
  align?: "top" | "middle";
  children: ReactNode;
}>) {
  const modifier = align === "middle" ? " fieldgrid-label--middle" : "";
  const valueModifier = align === "middle" ? " fieldgrid-value--middle" : "";
  return (
    <>
      <span className={`fieldgrid-label${modifier}`}>{label}</span>
      <span className={`fieldgrid-value${valueModifier}`}>{children}</span>
    </>
  );
}
