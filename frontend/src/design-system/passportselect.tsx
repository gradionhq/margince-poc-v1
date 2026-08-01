import { useT } from "../i18n";

// Shared between the tool console's passport filter and the OAuth consent
// screen (Task 7) — extracted so the two surfaces cannot drift into
// rendering "which passport, which scopes" differently. A caller supplies
// its own already-localized `label` per option (e.g. the tool console's
// "Reachable by {name}" phrasing); this component only lays the list out.
export type PassportOption = {
  id: string;
  label: string;
  scopes: string[];
  granted?: string[];
};

// A scope chip row. `dim` names the scopes the current selection does not
// grant — used by the consent screen to show a requested scope the human is
// about to add versus one already covered. The distinction reads through a
// dashed, unfilled outline (an --unfilled shape, not a faded one) plus a
// screen-reader-only label, deliberately NOT an opacity drop: a struck-through
// passport row elsewhere in this file already established that dimming a
// small chip can push it under the AA contrast floor (B-EP09.21), and text
// colour stays the same --textMeta the filled chip uses either way.
export function ScopeChips({
  scopes,
  dim,
}: Readonly<{
  scopes: string[];
  dim?: ReadonlySet<string>;
}>) {
  const t = useT();
  return (
    <>
      {scopes.map((scope) => {
        const isDim = dim?.has(scope) ?? false;
        return (
          <span key={scope} className={isDim ? "badge badge-dim" : "badge"}>
            {scope}
            {isDim && (
              <span className="sr-only">{` ${t("passport.scopeNotGranted")}`}</span>
            )}
          </span>
        );
      })}
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
    <select
      className="input"
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
    </select>
  );
}
