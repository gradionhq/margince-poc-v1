// The read-only custom fields on a record 360. Mirrors the firmographics card
// (organizations.tsx): a labeled section + a `dl.firmo` list, evidence-or-omit
// — a field with no stored value is absent, and a record with no custom values
// renders nothing at all rather than an empty card.

import { Card } from "../design-system/atoms";
import { useLocale, useT } from "../i18n";
import {
  customFieldDisplay,
  customFieldHref,
  useObjectCustomFields,
} from "./customfields.form";
import type { CfObject } from "./customfields.logic";

export function CustomFieldsCard({
  object,
  record,
}: Readonly<{ object: CfObject; record: Record<string, unknown> }>) {
  const t = useT();
  const { locale } = useLocale();
  const cf = useObjectCustomFields(object);
  const boolLabels = { yes: t("field.yes"), no: t("field.no") };

  const rows = cf.fields
    .map((field) => ({
      field,
      value: customFieldDisplay(field, record[field.column_name], {
        locale,
        boolLabels,
      }),
    }))
    .filter((row): row is { field: typeof row.field; value: string } =>
      Boolean(row.value),
    );

  if (rows.length === 0) {
    return null;
  }

  return (
    <Card
      title={t("cf.formSection")}
      style={{ marginBottom: "var(--space-4)" }}
    >
      <dl className="firmo">
        {rows.map(({ field, value }) => {
          // A value that IS a web address becomes a link, and everything else
          // stays text. A field holding the ticket, the wiki page or the ERP
          // entry a record belongs to is the commonest thing anybody puts in a
          // text field, and copy-and-paste was the only way to follow it. The
          // scheme check inside customFieldHref is what keeps this from turning
          // a stored string into something executable on click.
          const href = customFieldHref(value);
          return (
            <div key={field.column_name}>
              <dt className="t-eyebrow">{field.label}</dt>
              <dd>
                {href ? (
                  // `noreferrer noopener` and a new tab: the destination is a
                  // foreign origin nobody in this workspace vouched for, so it
                  // learns nothing about where the reader came from and gets no
                  // handle on the window it was opened from.
                  <a
                    className="link-button"
                    href={href}
                    target="_blank"
                    rel="noreferrer noopener"
                  >
                    {value}
                  </a>
                ) : (
                  value
                )}
              </dd>
            </div>
          );
        })}
      </dl>
    </Card>
  );
}
