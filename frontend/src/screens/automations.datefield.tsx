// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { SelectOption } from "../design-system/select";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import { throwProblem } from "./common";

// renewal_reminder's date_field param (GH-706) names a workspace's own cf_*
// date column — a free-text box would let an operator type a column that
// does not exist, or exists but isn't a date, and the save-time validator
// deliberately does not catch that (automations_catalog.go's
// validateRenewalReminderParams checks only non-emptiness; the real
// existence/type check runs at scan time, per DESIGN.md). This picker
// closes that gap client-side by offering only real, active, date-typed
// columns for whichever object the form's own `object` param currently
// holds — the same /custom-fields list the custom-fields settings screen
// already renders (customfields.tsx), scoped and filtered here rather than
// server-side, since the endpoint's own filter vocabulary is object + status
// only (no field-type filter).

// The query param's own closed enum (listCustomFields, crm.yaml), which is
// the backend engine's allowlist — NOT customfields.logic.ts's CF_OBJECTS,
// which does not yet offer "project" in its picker. An `object` value
// outside this set can never resolve to a real /custom-fields page, so the
// picker degrades to its disabled, hinted state rather than issuing a query
// the endpoint would refuse.
const CUSTOM_FIELD_QUERY_OBJECTS = new Set([
  "person",
  "organization",
  "deal",
  "lead",
  "project",
]);

type CustomFieldQueryObject =
  | "person"
  | "organization"
  | "deal"
  | "lead"
  | "project";

function isCustomFieldQueryObject(
  value: string,
): value is CustomFieldQueryObject {
  return CUSTOM_FIELD_QUERY_OBJECTS.has(value);
}

export function DateFieldSelect({
  object,
  value,
  onChange,
  labelId,
}: Readonly<{
  object: string;
  value: string;
  onChange: (value: string) => void;
  labelId: string;
}>) {
  const t = useT();
  const known = isCustomFieldQueryObject(object);
  const fields = useQuery({
    queryKey: ["custom-fields", object],
    enabled: known,
    queryFn: async () => {
      // Narrow inside the closure, not with a cast at the call site: the
      // compiler proves the object query param is a real
      // CustomFieldQueryObject here, rather than trusting `enabled: known`
      // to have kept queryFn from ever running otherwise — a guarantee
      // that would silently stop holding if either changed later.
      if (!isCustomFieldQueryObject(object)) {
        throw new Error(
          "DateFieldSelect queryFn ran for an unsupported object",
        );
      }
      const { data, error } = await api.GET("/custom-fields", {
        params: { query: { object } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const options: SelectOption[] = known
    ? (fields.data?.data ?? [])
        .filter((field) => field.status === "active" && field.type === "date")
        .map((field) => ({ value: field.column_name, label: field.label }))
    : [];

  // Disabled whenever there is nothing real to choose from yet — no object
  // picked, the fetch itself failed, or the chosen object simply has no
  // active date field — rather than opening on an empty list a click
  // could never fill, or (worse) an empty list with no indication the
  // fetch behind it failed.
  const disabled =
    !known || fields.isError || (fields.isSuccess && options.length === 0);
  const hint = !known
    ? t("auto.dateField.needsObject")
    : fields.isError
      ? t("auto.dateField.loadError")
      : fields.isSuccess && options.length === 0
        ? t("auto.dateField.empty")
        : undefined;

  return (
    <>
      <Select
        aria-labelledby={labelId}
        options={options}
        value={value}
        onChange={onChange}
        disabled={disabled}
        placeholder={t("auto.dateField.placeholder")}
      />
      {hint && (
        <p className="t-caption" style={{ marginTop: "var(--space-1)" }}>
          {hint}
        </p>
      )}
    </>
  );
}
