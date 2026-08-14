// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { ReactNode } from "react";

// The micro-label that sits above the thing it names: 11px, semibold, uppercase,
// tracked open, in the meta role. It was spelled out five times across three
// stylesheets — .co-part-label, .co-brief-section-label, .co-timeline-day-heading,
// .code-label and `.firmo dt` — and no two of them agreed: 11px versus 12px,
// 0.03em versus 0.04em. Every one looked right on its own.
//
// It ships as BOTH a class and a component, and that is deliberate rather than
// indecision. The declarations live exactly once, in base.css's type ramp beside
// .t-label and .t-caption, because two of the five sites are reached only by a
// selector — `.firmo dt` names a <dt> the caller never writes a class on, and a
// component cannot help there. This component is the other half: a TSX caller
// that would otherwise retype the class name gets the element choice as a prop
// instead, which is the part that actually varies.
//
// The element varies because the same look does two different jobs. Over a
// section it is a real heading and owes the document an <h3>; beside a value in
// a definition list it is a label and a heading there would invent structure a
// screen reader then has to read past. Nothing about the type says which one it
// is, so the caller does.
export type EyebrowElement = "h2" | "h3" | "h4" | "span" | "dt";

export function Eyebrow({
  as = "span",
  children,
  className,
  id,
}: Readonly<{
  // Defaults to `span`: the label use, which invents no document structure. A
  // caller that means a heading says so and takes the responsibility for where
  // it sits in the outline.
  as?: EyebrowElement;
  children: ReactNode;
  className?: string;
  id?: string;
}>) {
  const Tag = as;
  return (
    <Tag
      className={["t-eyebrow", className ?? ""].filter(Boolean).join(" ")}
      id={id}
    >
      {children}
    </Tag>
  );
}
