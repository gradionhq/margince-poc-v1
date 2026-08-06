// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useT } from "../i18n";
import { Select } from "./atoms";

// Shared between the tool console's passport filter and the OAuth consent
// screen (Task 7) — extracted so the two surfaces cannot drift into
// rendering "which passport, which scopes" differently. A caller supplies
// its own already-localized `label` per option (e.g. the tool console's
// "Reachable by {name}" phrasing); this component only lays the list out.
export type PassportOption = {
  id: string;
  label: string;
  scopes: string[];
};

// A scope chip row: every chip a scope the passport carries. There is no
// "granted versus not" distinction to draw — a connection receives the scopes
// of the passport lent to it, and the tool console lists a whole passport's
// scopes — so a chip means one thing on both surfaces.
export function ScopeChips({ scopes }: Readonly<{ scopes: string[] }>) {
  return (
    <>
      {scopes.map((scope) => (
        <span key={scope} className="badge">
          {scope}
        </span>
      ))}
    </>
  );
}

export function PassportSelect({
  options,
  value,
  onChange,
  allowEmpty,
  emptyLabel,
  ariaLabel,
}: Readonly<{
  options: readonly PassportOption[];
  value: string;
  onChange: (id: string) => void;
  // The tool console offers an "all passports" choice (no passport picked
  // means every tool row reads as reachable); the consent screen always
  // requires one, so it leaves this unset.
  allowEmpty?: boolean;
  emptyLabel?: string;
  // Falls back to the generic catalog label; the tool console passes its own
  // ("All passports") so its existing accessible name doesn't move.
  ariaLabel?: string;
}>) {
  const t = useT();
  return (
    <Select
      aria-label={ariaLabel ?? t("passport.select")}
      value={value}
      onChange={(event) => onChange(event.target.value)}
    >
      {allowEmpty && (
        <option value="">{emptyLabel ?? t("passport.noneOption")}</option>
      )}
      {options.map((option) => (
        <option key={option.id} value={option.id}>
          {option.label}
        </option>
      ))}
    </Select>
  );
}
