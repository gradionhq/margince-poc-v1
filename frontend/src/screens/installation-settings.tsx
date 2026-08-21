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
  Modal,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { ToastRegion, useToast } from "../design-system/toast";
import { useT } from "../i18n";
import {
  problemFieldErrorsOf,
  problemMessageOf,
  QueryGate,
  throwProblem,
} from "./common";

// The installation settings surface (ADR-0090/A135): the organization's name,
// the IANA zone every reporting period is computed in, and the ISO-4217 base
// currency every roll-up converts to. Every role reads them — a rep reading
// amounts benefits from knowing which currency they are in — and only admin/ops
// may change them, so the three facts are READ on the card for everyone and the
// verb that changes them is refused with a reason for everyone else. Refusing
// without a reason is the failure mode this avoids: it is indistinguishable
// from a bug, and a reader cannot act on it either way.
//
// THREE ROWS AND ONE FORM. The card is a list of decisions — what the
// organization is called, when its periods start, which currency every amount
// is re-expressed in — so each is a row that shows its own answer, which is
// what lets a reader audit the installation by travelling one column. The
// EDITING is one act: the server takes ONE sparse PATCH, so the three fields
// are submitted together with one Save, and that belongs in a dialog rather
// than on the card (design-system README, `SettingList` / `SettingRow`: a
// control needing two inputs submitted together goes behind a verb, which
// keeps every row an answer). Each row's Edit opens that one dialog with its
// own field focused, so the verb beside a fact leads to the fact.
//
// The base currency is a fourth state: it stops being changeable once a deal
// has frozen a conversion rate against it (ADR-0085 §7). The server reports
// that as a flag and a reason, so the row and the field both carry the reason
// — an operator learns why before typing a value they cannot save, rather than
// discovering it from a 422.

// Both shapes come from the generated contract rather than being restated
// here: a hand-written copy would drift the first time the contract gains a
// field, and drift silently, since nothing compares the two.
type InstallationSettings = components["schemas"]["InstallationSettings"];
type Patch = components["schemas"]["UpdateInstallationSettingsRequest"];

// Which fact the reader pressed Edit on. The dialog always edits all three —
// one record, one sparse PATCH, one Save — so this decides nothing about what
// is written, only where focus lands when the dialog opens.
type EditedFact = "name" | "timezone" | "base_currency";

function useUpdateInstallationSettings(onSaved: () => void) {
  const t = useT();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (patch: Patch) => {
      const { data, error } = await api.PATCH("/installation/settings", {
        body: patch,
      });
      if (error) {
        // `throwProblem`, not `new Error(problemMessage(...))`. The wrapped form
        // flattened the server's answer to one sentence, which discarded
        // `details.errors[]` — so the per-field assertions the API already sends
        // on a 422 were unreachable, and every refusal on this three-field form
        // arrived as one paragraph at the bottom that named no field. It also
        // stopped being a ProblemError, which is what the global failure sink
        // uses to tell a server refusal from a bug worth logging.
        throwProblem(error, t);
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

// A frozen currency is frozen for an admin too, so the lock reason replaces the
// advice about what to type — that advice is about a value nobody can set. The
// row and the field inside the dialog say the same thing, from one expression,
// because two copies of this rule would answer differently the day the server
// stops sending a reason.
function currencyNote(
  settings: InstallationSettings,
  t: ReturnType<typeof useT>,
): string {
  if (!settings.base_currency_locked) {
    return t("installationSettings.baseCurrencyHint");
  }
  return (
    settings.base_currency_locked_reason ??
    t("installationSettings.baseCurrencyLocked")
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
  const [editing, setEditing] = useState<EditedFact | null>(null);
  // The save's only visible answer was the button going disabled, because the
  // draft now matched the server. A control losing its affordance reads as the
  // form having given up, not as the write having landed — and on this form the
  // patch is SPARSE, so an operator who changed one field of three had no way to
  // tell which of them the installation now holds.
  const update = useUpdateInstallationSettings(() => {
    toast.show(t("settings.saved"));
    setEditing(null);
  });
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

  // The denial, said once and POINTED AT, from every control it refuses. It
  // refuses three Edit verbs on the card and — if the grant is withdrawn while
  // the dialog is open, which /me's focus refetch can do — the three fields
  // inside it. `Button`'s `reasonId` and a field's `aria-describedby` are the
  // same wiring: name the sentence once, point every refused control at it.
  // Printing it beside each of them would state one fact three times.
  const denialId = useId();
  const describe = (control: FieldControl): string | undefined =>
    canManage
      ? control["aria-describedby"]
      : [control["aria-describedby"], denialId].filter(Boolean).join(" ");

  // The server's per-field assertions, put on the FIELDS. A 422 on this form
  // names which of the three values it refused — an unknown IANA zone, a
  // currency that is not a currency — and until now all of that arrived as one
  // paragraph under the last field, so a reader had to guess which input to fix.
  // `Field`'s `error` slot has existed the whole time; nothing routed anything
  // into it, on any settings page.
  //
  // Keyed by the wire field name because that is what the server asserts. The
  // patch is sparse and built from the same names, so the two cannot drift
  // without the request itself being wrong.
  const refused = new Map(
    problemFieldErrorsOf(update.error).map((problem) => [
      problem.field,
      problem.message,
    ]),
  );

  const dirty =
    draft.name !== settings.name ||
    draft.timezone !== settings.timezone ||
    draft.base_currency !== settings.base_currency;
  // The claim the Save button's own condition was already making privately.
  // It outlives the dialog on purpose: dismissing the dialog does not destroy
  // what was typed (the draft is still here, and reopening Edit shows it), so
  // an unsaved edit is still an unsaved edit on the way out of the page.
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

  const editVerb = (fact: EditedFact, field: string) => (
    <Button
      small
      variant="ghost"
      // Named by the fact it changes, not "Edit": three rows offering three
      // identically-named buttons make a screen reader's user count them.
      aria-label={t("installationSettings.editField", { field })}
      reasonId={canManage ? undefined : denialId}
      onClick={() => setEditing(fact)}
    >
      {t("installationSettings.edit")}
    </Button>
  );

  return (
    <Panel title={t("installationSettings.orgTitle")}>
      <PanelBody>
        <p className="settings-panel-sub">{t("installationSettings.orgSub")}</p>
        {!canManage && (
          <p className="t-caption" id={denialId}>
            {t("installationSettings.readOnly")}
          </p>
        )}
        <SettingList>
          <SettingRow
            label={t("installationSettings.name")}
            description={t("installationSettings.nameHint")}
            value={settings.name}
            control={editVerb("name", t("installationSettings.name"))}
          />
          <SettingRow
            label={t("installationSettings.timezone")}
            description={t("installationSettings.timezoneHint")}
            value={settings.timezone}
            control={editVerb("timezone", t("installationSettings.timezone"))}
          />
          <SettingRow
            label={t("installationSettings.baseCurrency")}
            description={currencyNote(settings, t)}
            value={settings.base_currency}
            control={editVerb(
              "base_currency",
              t("installationSettings.baseCurrency"),
            )}
          />
        </SettingList>
        {editing !== null && (
          <InstallationProfileDialog
            settings={settings}
            draft={draft}
            focus={editing}
            canManage={canManage}
            dirty={dirty}
            pending={update.isPending}
            refused={refused}
            blanketError={
              update.isError && refused.size === 0
                ? problemMessageOf(update.error, t)
                : null
            }
            describe={describe}
            onChange={setDraft}
            onClose={() => setEditing(null)}
            onSubmit={submit}
          />
        )}
        <ToastRegion toast={toast} />
      </PanelBody>
    </Panel>
  );
}

// The one form the three rows lead to: ONE dialog, ONE submit — the patch
// already sends only the fields that changed, which is what keeps a save from
// touching a frozen currency nobody edited. A field per row's Edit would
// promise three independent writes the server does not offer.
function InstallationProfileDialog({
  settings,
  draft,
  focus,
  canManage,
  dirty,
  pending,
  refused,
  blanketError,
  describe,
  onChange,
  onClose,
  onSubmit,
}: Readonly<{
  settings: InstallationSettings;
  draft: InstallationSettings;
  /** Which field the reader pressed Edit on, and so where focus lands. */
  focus: EditedFact;
  canManage: boolean;
  dirty: boolean;
  pending: boolean;
  refused: Map<string, string>;
  blanketError: string | null;
  describe: (control: FieldControl) => string | undefined;
  onChange: (next: InstallationSettings) => void;
  onClose: () => void;
  onSubmit: () => void;
}>) {
  const t = useT();
  const titleId = useId();
  // Focus lands on the field whose Edit was pressed — programmatic rather than
  // the `autoFocus` attribute, the same way the sign-in page does it, so the
  // a11y lint's blanket rule against autofocus stays intact.
  const asked = useRef<HTMLInputElement>(null);
  useEffect(() => {
    asked.current?.focus();
  }, []);
  return (
    <Modal open onClose={onClose} labelledBy={titleId}>
      <h2 id={titleId} className="t-h2 modal-title">
        {t("installationSettings.orgTitle")}
      </h2>
      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          onSubmit();
        }}
      >
        <Field
          label={t("installationSettings.name")}
          hint={t("installationSettings.nameHint")}
          error={refused.get("name")}
        >
          {(control) => (
            <TextInput
              {...control}
              aria-describedby={describe(control)}
              ref={focus === "name" ? asked : undefined}
              value={draft.name}
              disabled={!canManage}
              onChange={(event) =>
                onChange({ ...draft, name: event.target.value })
              }
            />
          )}
        </Field>
        <Field
          label={t("installationSettings.timezone")}
          hint={t("installationSettings.timezoneHint")}
          error={refused.get("timezone")}
        >
          {(control) => (
            <TextInput
              {...control}
              aria-describedby={describe(control)}
              ref={focus === "timezone" ? asked : undefined}
              value={draft.timezone}
              disabled={!canManage}
              onChange={(event) =>
                onChange({ ...draft, timezone: event.target.value })
              }
            />
          )}
        </Field>

        {/* A section INSIDE the dialog's own heading: the currency rule needs
            the room to be explained, and level 3 is what keeps that from
            minting a second heading at the dialog's own rank. */}
        <SectionHeader
          level={3}
          title={t("installationSettings.currencyTitle")}
          sub={t("installationSettings.currencySub")}
        />
        <Field
          label={t("installationSettings.baseCurrency")}
          hint={currencyNote(settings, t)}
          error={refused.get("base_currency")}
        >
          {(control) => (
            <TextInput
              {...control}
              aria-describedby={describe(control)}
              ref={focus === "base_currency" ? asked : undefined}
              value={draft.base_currency}
              disabled={!canManage || settings.base_currency_locked}
              onChange={(event) =>
                onChange({ ...draft, base_currency: event.target.value })
              }
            />
          )}
        </Field>

        {/* Only what no field claimed. A refusal shown BOTH on the input and
            again in a paragraph below states one problem twice, and the
            paragraph is the copy a reader stops reading. */}
        {blanketError !== null ? (
          <Callout tone="danger" live="alert">
            {blanketError}
          </Callout>
        ) : null}
        <div className="form-actions">
          <Button small variant="ghost" type="button" onClick={onClose}>
            {t("create.cancel")}
          </Button>
          <Button
            small
            type="submit"
            variant="primary"
            disabled={!canManage || !dirty || pending}
          >
            {pending ? t("common.saving") : t("installationSettings.save")}
          </Button>
        </div>
      </form>
    </Modal>
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
