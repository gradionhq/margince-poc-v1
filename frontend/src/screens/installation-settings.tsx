import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { Card, Field, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// The installation settings surface (ADR-0090/A135): the organization's name,
// the IANA zone every reporting period is computed in, and the ISO-4217 base
// currency every roll-up converts to. Every role reads them — a rep reading
// amounts benefits from knowing which currency they are in — and only admin/ops
// may change them, so the fields are disabled rather than hidden for everyone
// else, the same gating the capture-settings card uses.
//
// Two cards: what the organization is called and when its periods start is one
// subject, and the currency every amount is re-expressed in is another — with a
// lock rule of its own that needs the room to be explained.
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
  const canManage = useCanWrite("installation_settings", "update");
  const query = useInstallationSettings();

  return (
    <QueryGate query={query}>
      {(settings) => (
        <InstallationSettingsForm settings={settings} canManage={canManage} />
      )}
    </QueryGate>
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
    // ONE form across BOTH cards, and one submit at the end of it. The two
    // cards edit ONE record through one sparse PATCH, so a save button per card
    // would promise two independent writes the server does not offer — and the
    // patch already sends only the fields that changed, which is what keeps a
    // save from touching a frozen currency nobody edited. The action sits after
    // the last card, where a reader looks for the control that commits the
    // fields above it.
    <form
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <Card
        className="card-stack"
        title={t("installationSettings.orgTitle")}
        sub={t("installationSettings.orgSub")}
      >
        <div className="form-stack">
          <Field
            label={t("installationSettings.name")}
            hint={t("installationSettings.nameHint")}
          >
            {(control) => (
              <TextInput
                {...control}
                value={draft.name}
                disabled={!canManage}
                onChange={(event) =>
                  setDraft({ ...draft, name: event.target.value })
                }
              />
            )}
          </Field>
          <Field
            label={t("installationSettings.timezone")}
            hint={t("installationSettings.timezoneHint")}
          >
            {(control) => (
              <TextInput
                {...control}
                value={draft.timezone}
                disabled={!canManage}
                onChange={(event) =>
                  setDraft({ ...draft, timezone: event.target.value })
                }
              />
            )}
          </Field>
        </div>
      </Card>

      <Card
        className="card-stack"
        title={t("installationSettings.currencyTitle")}
        sub={t("installationSettings.currencySub")}
      >
        <Field
          label={t("installationSettings.baseCurrency")}
          hint={
            settings.base_currency_locked
              ? (settings.base_currency_locked_reason ??
                t("installationSettings.baseCurrencyLocked"))
              : t("installationSettings.baseCurrencyHint")
          }
        >
          {(control) => (
            <TextInput
              {...control}
              value={draft.base_currency}
              disabled={!canManage || settings.base_currency_locked}
              onChange={(event) =>
                setDraft({ ...draft, base_currency: event.target.value })
              }
            />
          )}
        </Field>
      </Card>

      <div className="form-stack">
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
      </div>
    </form>
  );
}
