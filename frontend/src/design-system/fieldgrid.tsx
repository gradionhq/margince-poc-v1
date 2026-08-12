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
  children,
}: Readonly<{ label: ReactNode; children: ReactNode }>) {
  return (
    <>
      <span className="fieldgrid-label">{label}</span>
      <span className="fieldgrid-value">{children}</span>
    </>
  );
}
