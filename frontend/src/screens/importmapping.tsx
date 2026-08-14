// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Select } from "../design-system/select";
import { useT } from "../i18n";
import type { ImportColumn, ImportProfile } from "./importtypes";
import { DONT_IMPORT } from "./importtypes";

// The mapping surface (IEM-AC-OPEN-2): one row per column of the uploaded file,
// carrying what a human needs to decide where it goes — the name as the file
// spells it, how often it is filled, and a few of its actual values.
//
// The fill rate is the number that earns its place. A column at 4% is usually a
// mapping mistake waiting to happen, and it looks exactly like a column at 98%
// if all you are shown is its name.
//
// The destination defaults to the server's suggestion, which is deliberately
// timid: it matches on normalized names only, so an unmatched column arrives
// here as "don't import" rather than as a guess the human must first notice.

export function ImportMappingTable({
  profile,
  mapping,
  locked,
  onChange,
}: Readonly<{
  profile: ImportProfile;
  mapping: Record<string, string>;
  // While the mapping is being validated it stops accepting changes: a choice
  // made now cannot reach the request already in flight, and a table showing a
  // destination the run does not carry is the screen lying about the import.
  locked: boolean;
  onChange: (column: string, target: string) => void;
}>) {
  const t = useT();
  const options = [
    { value: DONT_IMPORT, label: t("import.dontImport") },
    ...profile.targets.map((target) => ({ value: target, label: target })),
  ];

  return (
    <div className="import__mapping">
      <p className="import__hint">
        {t("import.profiled", { rows: profile.rows_profiled })}
      </p>
      <div className="import__scroll">
        <table className="import__table">
          <thead>
            <tr>
              <th scope="col">{t("import.col.column")}</th>
              <th scope="col">{t("import.col.filled")}</th>
              <th scope="col">{t("import.col.samples")}</th>
              <th scope="col">{t("import.col.destination")}</th>
            </tr>
          </thead>
          <tbody>
            {profile.columns.map((column) => (
              <tr key={column.header}>
                <th scope="row" className="import__colName">
                  {column.header}
                </th>
                <td className="import__fill">{fillLabel(column)}</td>
                <td className="import__samples">
                  {column.samples.length > 0 ? (
                    column.samples.join(", ")
                  ) : (
                    <span className="import__empty">
                      {t("import.noSamples")}
                    </span>
                  )}
                </td>
                <td>
                  <Select
                    options={options}
                    value={mapping[column.header] ?? DONT_IMPORT}
                    onChange={(target) => onChange(column.header, target)}
                    disabled={locked}
                    placeholder={t("import.dontImport")}
                    aria-label={t("import.destinationFor", {
                      column: column.header,
                    })}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// fillLabel renders the rate as a percentage of the rows actually profiled.
// Rounded to whole numbers: the decision it informs is "is this column worth
// mapping", and nobody decides that differently at 4.2% than at 4%.
function fillLabel(column: ImportColumn): string {
  return `${Math.round(column.fill_rate * 100)}%`;
}
