import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { SectionHeader } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// The installation settings card (ADR-0090/A135): the organization's name, the
// IANA zone every reporting period is computed in, and the ISO-4217 base
// currency every roll-up converts to. Every role reads them — a rep reading
// amounts benefits from knowing which currency they are in — and only admin/ops
// may change them, so the fields are disabled rather than hidden for everyone
// else, the same gating the capture-settings card uses.
//
// The base currency is a fourth state: it stops being changeable once a deal
// has frozen a conversion rate against it (ADR-0085 §7). The server reports
// that as a flag and a reason, so the field renders read-only WITH the reason
// beside it — an operator learns why before typing a value they cannot save,
// rather than discovering it from a 422.

// Both shapes come from the generated contract rather than being restated
// here: a hand-written copy would drift the first time the contract gains a
// field, and drift silently, since nothing compares the two.
type InstallationSettings = components["schemas"]["InstallationSettings"];
type Patch = components["schemas"]["UpdateInstallationSettingsRequest"];

function useInstallationSettings() {
  return useQuery({
    queryKey: ["installation-settings"],
    queryFn: async () => {
      const { data, error, response } = await api.GET("/installation/settings");
      if (error || !response.ok) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

function useUpdateInstallationSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (patch: Patch) => {
      const { data, error } = await api.PATCH("/installation/settings", {
        body: patch,
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(["installation-settings"], data);
    },
    // A refused patch can still have committed nothing OR something: the
    // server applies the fields in one transaction, but a validation refusal
    // on one field is reported after others were accepted in an earlier
    // request. Refetching on failure means the form shows what the server
    // actually holds rather than the draft the user typed.
    onError: () => {
      void queryClient.invalidateQueries({
        queryKey: ["installation-settings"],
      });
    },
  });
}

export function InstallationSettingsCard() {
  const t = useT();
  const canManage = useCanWrite("installation_settings", "update");
  const query = useInstallationSettings();

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader
        title={t("installationSettings.title")}
        sub={t("installationSettings.sub")}
      />
      <QueryGate query={query}>
        {(settings) => (
          <InstallationSettingsForm settings={settings} canManage={canManage} />
        )}
      </QueryGate>
    </section>
  );
}

function InstallationSettingsForm({
  settings,
  canManage,
}: {
  settings: InstallationSettings;
  canManage: boolean;
}) {
  const t = useT();
  const update = useUpdateInstallationSettings();
  const [draft, setDraft] = useState(settings);

  // Re-seed when the server answers with different values than the draft was
  // built from — a refetch, or another admin's change arriving. Without this
  // the form would keep showing stale text after the query updated beneath it.
  useEffect(() => {
    setDraft(settings);
  }, [settings]);

  const dirty =
    draft.name !== settings.name ||
    draft.timezone !== settings.timezone ||
    draft.base_currency !== settings.base_currency;

  // Only changed fields are sent: the patch is sparse, and sending an
  // unchanged base currency would ask the server to write a value that may be
  // frozen — refused, for a field the operator never touched.
  const submit = () => {
    const patch: Patch = {};
    if (draft.name !== settings.name) patch.name = draft.name;
    if (draft.timezone !== settings.timezone) patch.timezone = draft.timezone;
    if (draft.base_currency !== settings.base_currency) {
      patch.base_currency = draft.base_currency;
    }
    update.mutate(patch);
  };

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      style={{ display: "grid", gap: "var(--space-3)" }}
    >
      <Field
        id="installation-name"
        label={t("installationSettings.name")}
        hint={t("installationSettings.nameHint")}
        value={draft.name}
        disabled={!canManage}
        onChange={(name) => setDraft({ ...draft, name })}
      />
      <Field
        id="installation-timezone"
        label={t("installationSettings.timezone")}
        hint={t("installationSettings.timezoneHint")}
        value={draft.timezone}
        disabled={!canManage}
        onChange={(timezone) => setDraft({ ...draft, timezone })}
      />
      <Field
        id="installation-base-currency"
        label={t("installationSettings.baseCurrency")}
        hint={
          settings.base_currency_locked
            ? (settings.base_currency_locked_reason ??
              t("installationSettings.baseCurrencyLocked"))
            : t("installationSettings.baseCurrencyHint")
        }
        value={draft.base_currency}
        disabled={!canManage || settings.base_currency_locked}
        onChange={(base_currency) => setDraft({ ...draft, base_currency })}
      />

      {update.isError ? (
        <p role="alert" className="form-error">
          {update.error instanceof Error
            ? update.error.message
            : t("common.error")}
        </p>
      ) : null}

      {canManage ? (
        <div>
          <button
            type="submit"
            className="btn btn-primary"
            disabled={!dirty || update.isPending}
          >
            {update.isPending
              ? t("common.saving")
              : t("installationSettings.save")}
          </button>
        </div>
      ) : null}
    </form>
  );
}

function Field({
  id,
  label,
  hint,
  value,
  disabled,
  onChange,
}: {
  id: string;
  label: string;
  hint: string;
  value: string;
  disabled: boolean;
  onChange: (next: string) => void;
}) {
  return (
    <div style={{ display: "grid", gap: "var(--space-1)" }}>
      <label htmlFor={id}>{label}</label>
      <input
        id={id}
        type="text"
        value={value}
        disabled={disabled}
        aria-describedby={`${id}-hint`}
        onChange={(e) => onChange(e.target.value)}
      />
      <small id={`${id}-hint`} className="muted">
        {hint}
      </small>
    </div>
  );
}
