// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useT } from "../i18n";
import { useSorMode } from "../screens/common";
import { useHoldsOperatorSeat } from "./capability";

// The system-of-record mode chip: in overlay mode the whole installation
// reads from an incumbent mirror, which changes what every screen can do —
// list sort/filter dials narrow, some reads answer "not available", and
// mirrored-record writes refuse. Any seat looking at the topbar should be
// able to tell this is happening, not just the admin who is on the Settings
// card that manages it. It reads the already-cached ["me"] query (the same
// probe AuthGate already resolved before any screen mounted), so mounting it
// costs no extra request and it never renders ahead of a real answer. Native
// mode renders nothing — the chip is a state marker, not a permanent fixture.
//
// The MODE is for every seat; the DESTINATION is not. Integrations lives in
// Admin settings, which only an operator seat reaches, so a link offered to a
// rep would land them on the Account fallback — a chip that lied about where it
// went. So the fact keeps its place for everyone and only an operator gets the
// affordance, which is the same rule the rail's license chip follows.
export function SorModeChip() {
  const t = useT();
  const mode = useSorMode();
  const operator = useHoldsOperatorSeat();
  if (mode !== "overlay") {
    return null;
  }
  // Same element, same tokens, same words either way: what changes is whether
  // it is a link. A `<span>` with a title and an accessible name still reports
  // the mode to a screen reader; what it no longer does is promise a page.
  const label = t("overlay.chipLabel");
  const explanation = t("overlay.chipAria");
  return operator ? (
    <a
      // Integrations, not Connections: the mirror that is answering every read is
      // installation-wide wiring, and the personal Connections entry now holds
      // only a reader's own mailbox and network.
      href="#/settings/admin/integrations"
      className="badge badge-accent"
      title={explanation}
      aria-label={explanation}
    >
      {label}
    </a>
  ) : (
    <span className="badge badge-accent" title={explanation}>
      {label}
      {/* The explanation as text rather than as `aria-label`: a plain span has
          no role for that attribute to belong to, so a screen reader is not
          obliged to read it. Visually hidden text is announced with the chip
          and the `title` above still serves a hover. */}
      <span className="sr-only"> {explanation}</span>
    </span>
  );
}
