// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Avatar } from "./atoms";
import "./avatarstack.css";

// AvatarStack draws a group of people as overlapping monograms — coverage as
// faces rather than a count, up to `max`, with the remainder folded into a
// "+N" chip so a committee of fifteen still reads as one compact shape. The
// remainder is digits and a `+`, not prose — the one count this design system
// draws without a translated sentence around it, the same reasoning that
// keeps a currency figure or a version number out of the copy gate.
export function AvatarStack({
  people,
  max = 5,
}: Readonly<{
  people: readonly { name: string }[];
  max?: number;
}>) {
  const shown = people.slice(0, max);
  const rest = people.length - shown.length;
  return (
    <span className="avatar-stack">
      {shown.map((person) => (
        <span className="avatar-stack-item" key={person.name}>
          <Avatar name={person.name} tinted />
        </span>
      ))}
      {rest > 0 && <span className="avatar-stack-more">+{rest}</span>}
    </span>
  );
}
