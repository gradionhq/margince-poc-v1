import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { useUnsavedGuard } from "../app/unsaved";
// Shared with every upload form, which reads the ceiling off this same record.
// One query, one key: two hooks on one key would let whichever mounted first
// decide how a failure behaves for the other.
import {
  INSTALLATION_SETTINGS_KEY,
  useInstallationSettings,
} from "../app/uploadlimit";
import {
  Button,
  Field,
  type FieldControl,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { ToastRegion, useToast } from "../design-system/toast";
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// The installation settings surface (ADR-0090/A135): the organization's name,
// the IANA zone every reporting period is computed in, and the ISO-4217 base
// currency every roll-up converts to. Every role reads them — a rep reading
// amounts benefits from knowing which currency they are in — and only admin/ops
// may change them, so the fields are disabled rather than hidden for everyone
// else, and the reason is stated where the save action would otherwise be.
// Disabling without a reason is the failure mode this avoids: it is
// indistinguishable from a bug, and a reader cannot act on it either way.
//
// ONE panel with two sections, not two cards. The two subjects — what the
// organization is called and when its periods start, then the currency every
// amount is re-expressed in — edit ONE record through one sparse PATCH, so
// there is one save; and a save that sits after the last card is a control a
// reader has to scroll past a card boundary to find, having been told nothing
// about which fields it commits. Inside one panel the action band sits under
// the fields it writes.
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

function useUpdateInstallationSettings(onSaved: () => void) {
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
      queryClient.setQueryData(INSTALLATION_SETTINGS_KEY, data);
      onSaved();
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
  const toast = useToast();
  // The save's only visible answer was the button going disabled, because the
  // draft now matched the server. A control losing its affordance reads as the
  // form having given up, not as the write having landed — and on this form the
  // patch is SPARSE, so an operator who changed one field of three had no way to
  // tell which of them the installation now holds.
  const update = useUpdateInstallationSettings(() =>
    toast.show(t("settings.saved")),
  );
  const [draft, setDraft] = useState(settings);
  const seeded = useRef(serverSignature(settings));

  // Re-seed when the server answers with DIFFERENT values than the draft was
  // built from — another admin's change arriving — and only then.
  //
  // The identity of `settings` is not that question. Every refetch mints a new
  // object, and `refetchOnWindowFocus` means a reader who tabs away to look up
  // their IANA zone and tabs back triggers one: the values come back
  // unchanged, the object does not, and the effect used to throw away
  // everything they had typed. Comparing what the server SAID, rather than
  // which object said it, leaves an unsaved draft alone across every refetch
  // that changes nothing.
  useEffect(() => {
    const signature = serverSignature(settings);
    if (seeded.current === signature) {
      return;
    }
    seeded.current = signature;
    setDraft(settings);
  }, [settings]);

  // The denial, said once and POINTED AT. Disabling a row of fields and leaving
  // the reason to float below the last of them tells a screen reader nothing:
  // the explanation is read only if the reader happens to land on that
  // paragraph, which is not where a refused control leaves them. `Switch`'s
  // `reason` prop is the pattern (design-system README) — render it AND name it
  // on the control — and `aria-describedby` is that same wiring for a field.
  // It is ADDED to the field's own hint rather than replacing it, because "what
  // to type here" and "why you cannot" are two different things a reader needs
  // and neither is the other's substitute.
  const denialId = useId();
  const describe = (control: FieldControl): string | undefined =>
    canManage
      ? control["aria-describedby"]
      : [control["aria-describedby"], denialId].filter(Boolean).join(" ");

  const dirty =
    draft.name !== settings.name ||
    draft.timezone !== settings.timezone ||
    draft.base_currency !== settings.base_currency;
  // The claim the Save button's own condition was already making privately.
  useUnsavedGuard(dirty);

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
    // ONE form, ONE panel, ONE submit — the patch already sends only the fields
    // that changed, which is what keeps a save from touching a frozen currency
    // nobody edited. The action rides in the panel's own action band, directly
    // under the last field it commits.
    <form
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <Panel
        title={t("installationSettings.orgTitle")}
        actions={
          <SaveAction
            canManage={canManage}
            dirty={dirty}
            pending={update.isPending}
            denialId={denialId}
          />
        }
      >
        <PanelBody className="form-stack">
          <p className="t-caption">{t("installationSettings.orgSub")}</p>
          <Field
            label={t("installationSettings.name")}
            hint={t("installationSettings.nameHint")}
          >
            {(control) => (
              <TextInput
                {...control}
                aria-describedby={describe(control)}
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
                aria-describedby={describe(control)}
                value={draft.timezone}
                disabled={!canManage}
                onChange={(event) =>
                  setDraft({ ...draft, timezone: event.target.value })
                }
              />
            )}
          </Field>

          {/* A section INSIDE the panel's own section: the currency rule needs
              the room to be explained, and level 3 is what keeps that from
              minting a second heading at the panel's own rank. */}
          <SectionHeader
            level={3}
            title={t("installationSettings.currencyTitle")}
            sub={t("installationSettings.currencySub")}
          />
          <Field
            label={t("installationSettings.baseCurrency")}
            // A frozen currency is frozen for an admin too, so the lock reason
            // replaces the advice about what to type — that advice is about a
            // value nobody can set. The permission denial rides on top of it
            // through `describe`, since the two refusals are different and a
            // reader can be under both.
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
                aria-describedby={describe(control)}
                value={draft.base_currency}
                disabled={!canManage || settings.base_currency_locked}
                onChange={(event) =>
                  setDraft({ ...draft, base_currency: event.target.value })
                }
              />
            )}
          </Field>

          {update.isError ? (
            <Callout tone="danger" live="alert">
              {update.error instanceof Error
                ? update.error.message
                : t("common.error")}
            </Callout>
          ) : null}
          <ToastRegion toast={toast} />
        </PanelBody>
      </Panel>
    </form>
  );
}

// The save action, or the reason there is none. A row of fields that are all
// disabled with nothing saying why is the reader's problem to solve: they
// cannot tell a permission from a bug, from a value that has not loaded. It is
// said here because this is where a reader looks for the control that commits
// the fields above — and the same words are what every disabled field points
// at, so the explanation is announced with the control as well as read beside
// it.
function SaveAction({
  canManage,
  dirty,
  pending,
  denialId,
}: Readonly<{
  canManage: boolean;
  dirty: boolean;
  pending: boolean;
  denialId: string;
}>) {
  const t = useT();
  if (!canManage) {
    return (
      <p className="t-small" id={denialId}>
        {t("installationSettings.readOnly")}
      </p>
    );
  }
  return (
    <Button type="submit" variant="primary" disabled={!dirty || pending}>
      {pending ? t("common.saving") : t("installationSettings.save")}
    </Button>
  );
}

// What the server SAID, as one comparable string. Used to tell a refetch that
// changed nothing from one that did — see the re-seed effect above.
function serverSignature(settings: InstallationSettings): string {
  return JSON.stringify([
    settings.name,
    settings.timezone,
    settings.base_currency,
  ]);
}
