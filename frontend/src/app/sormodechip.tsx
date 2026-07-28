// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useT } from "../i18n";
import { useSorMode } from "../screens/common";

// The system-of-record mode chip: in overlay mode the whole installation
// reads from an incumbent mirror, which changes what every screen can do —
// list sort/filter dials narrow, some reads answer "not available", and
// mirrored-record writes refuse. Any seat looking at the topbar should be
// able to tell this is happening, not just the admin who is on the Settings
// card that manages it. It reads the already-cached ["me"] query (the same
// probe AuthGate already resolved before any screen mounted), so mounting it
// costs no extra request and it never renders ahead of a real answer. Native
// mode renders nothing — the chip is a state marker, not a permanent fixture.
export function SorModeChip() {
  const t = useT();
  if (useSorMode() !== "overlay") {
    return null;
  }
  return (
    <a
      href="#/settings/overlay"
      className="badge badge-accent"
      title={t("overlay.chipAria")}
      aria-label={t("overlay.chipAria")}
    >
      {t("overlay.chipLabel")}
    </a>
  );
}
