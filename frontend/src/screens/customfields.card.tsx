// The read-only custom fields on a record 360. Mirrors the firmographics card
// (organizations.tsx): a labeled section + a `dl.firmo` list, evidence-or-omit
// — a field with no stored value is absent, and a record with no custom values
// renders nothing at all rather than an empty card.

import { SectionHeader } from "../design-system/atoms";
import { useLocale, useT } from "../i18n";
import { customFieldDisplay, useObjectCustomFields } from "./customfields.form";
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
    <section className="card" style={{ marginBottom: 16 }}>
      <SectionHeader title={t("cf.formSection")} />
      <dl className="firmo">
        {rows.map(({ field, value }) => (
          <div key={field.column_name}>
            <dt>{field.label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
