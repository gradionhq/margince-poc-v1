import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import { useCanWrite } from "../app/capability";
import { isOption } from "../app/options";
import { Badge, Button, Field, Modal, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody } from "../design-system/panel";
import { Select } from "../design-system/select";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { Switch } from "../design-system/switch";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryGate, throwProblem } from "./common";
import {
  LEAD_DISQUALIFY_REASONS_KEY,
  LEAD_SOURCES_KEY,
  type LeadDisqualifyReason,
  type LeadSource,
  type LeadSourceIntent,
  sourceKeyLabel,
  useLeadDisqualifyReasons,
  useLeadSettings,
  useLeadSources,
  useUpdateLeadSettings,
} from "./leadsources";
import "./leadvocab.css";

// Settings › Data model: the two administered lead vocabularies and the
// lead-handling posture. Every role reads them — the leads list needs the
// labels and the SLA switch to know what to render — and only a seat holding
// the custom_field write verbs changes them, the same posture as the
// custom-field catalog beside them. The server stays the RBAC authority; the
// controls disable with the reason rather than hide.

const INTENTS = ["high", "neutral", "low"] as const;

// The contract promises the arrays; a body that lost one is treated as the
// empty list it claims rather than a crash in the render that reads it.
function rowsOf<Row>(rows: readonly Row[] | null | undefined): readonly Row[] {
  return Array.isArray(rows) ? rows : [];
}

// The first-response target's bounds, the same the server enforces
// (15 minutes to 7 days); checked here so a refusal never leaves the page.
const TARGET_MIN_MINUTES = 15;
const TARGET_MAX_MINUTES = 7 * 24 * 60;
const intentLabel: Record<LeadSourceIntent, MessageKey> = {
  high: "leadSources.intent.high",
  neutral: "leadSources.intent.neutral",
  low: "leadSources.intent.low",
};

function useSourceMutations() {
  const queryClient = useQueryClient();
  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: LEAD_SOURCES_KEY });
    // The list and the create form render labels off this list.
    void queryClient.invalidateQueries({ queryKey: ["leads"] });
  };
  const create = useMutation({
    mutationFn: async (body: {
      label: string;
      key?: string;
      intent: LeadSourceIntent;
    }) => {
      const { data, error } = await api.POST("/lead-sources", { body });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: invalidate,
  });
  const update = useMutation({
    mutationFn: async ({
      id,
      ...body
    }: {
      id: string;
      label?: string;
      intent?: LeadSourceIntent;
      active?: boolean;
      sort_order?: number;
    }) => {
      const { data, error } = await api.PATCH("/lead-sources/{id}", {
        params: { path: { id } },
        body,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE("/lead-sources/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: invalidate,
  });
  return { create, update, remove };
}

// A label that commits on Enter or blur, and gives the typed text back when
// the save is refused so the reader can fix it rather than retype it.
function RenameField({
  label,
  value,
  canEdit,
  onSave,
}: Readonly<{
  label: string;
  value: string;
  canEdit: boolean;
  onSave: (next: string) => void;
}>) {
  const [draft, setDraft] = useState(value);
  const [known, setKnown] = useState(value);
  if (known !== value) {
    setKnown(value);
    setDraft(value);
  }
  const commit = () => {
    const next = draft.trim();
    if (next !== "" && next !== value) {
      onSave(next);
    } else {
      setDraft(value);
    }
  };
  // The row's own words name the field (the list is the label column), so
  // the control carries its name for assistive tech only.
  return (
    <TextInput
      aria-label={label}
      className="lead-vocab-rename"
      value={draft}
      disabled={!canEdit}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          commit();
        }
        if (e.key === "Escape") {
          setDraft(value);
        }
      }}
    />
  );
}

function LeadSourceRow({
  source,
  canEdit,
  canRemove,
  onUpdate,
  onRemove,
}: Readonly<{
  source: LeadSource;
  canEdit: boolean;
  canRemove: boolean;
  onUpdate: (patch: {
    label?: string;
    intent?: LeadSourceIntent;
    active?: boolean;
  }) => void;
  onRemove: () => void;
}>) {
  const t = useT();
  const count = source.lead_count ?? 0;
  const builtIn = source.system === true;
  // Built-ins and in-use sources deactivate instead: the server answers 409
  // to the delete, so the control says so instead of offering it.
  const removable = canRemove && !builtIn && count === 0;
  return (
    <li className="lead-vocab-row" data-testid={`lead-source-${source.key}`}>
      <RenameField
        label={t("leadSources.labelFor", { key: source.key })}
        value={source.label}
        canEdit={canEdit}
        onSave={(label) => onUpdate({ label })}
      />
      <span className="t-mono t-caption lead-vocab-key">{source.key}</span>
      <Select
        aria-label={t("leadSources.intentFor", { label: source.label })}
        value={source.intent}
        disabled={!canEdit}
        onChange={(value) => {
          if (isOption(value, INTENTS) && value !== source.intent) {
            onUpdate({ intent: value });
          }
        }}
        options={INTENTS.map((value) => ({
          value,
          label: t(intentLabel[value]),
        }))}
      />
      <span className="t-caption lead-vocab-count">
        {t("leadSources.leadCount", { count })}
      </span>
      <span className="lead-vocab-flags">
        {builtIn && <Badge>{t("leadSources.builtIn")}</Badge>}
        <Switch
          label={t("leadSources.activeFor", { label: source.label })}
          labelHidden
          checked={source.active}
          disabled={!canEdit}
          onChange={(next) => onUpdate({ active: next })}
        />
        {removable ? (
          <Button small variant="danger" onClick={onRemove}>
            {t("leadSources.remove")}
          </Button>
        ) : (
          canRemove && (
            <span
              className="t-caption"
              title={
                builtIn
                  ? t("leadSources.builtInKept")
                  : t("leadSources.inUse", { count })
              }
            >
              {t("leadSources.deactivateInstead")}
            </span>
          )
        )}
      </span>
    </li>
  );
}

// A label and a weight, committed together — the settings page's dialog case
// rather than its row case, and mounted only while it is open so a half-typed
// source is gone the next time the verb is pressed rather than waiting there
// under an intent nobody re-chose.
function AddSourceDialog({
  create,
  onClose,
}: Readonly<{
  create: ReturnType<typeof useSourceMutations>["create"];
  onClose: () => void;
}>) {
  const t = useT();
  const titleId = useId();
  const [label, setLabel] = useState("");
  const [intent, setIntent] = useState<LeadSourceIntent>("neutral");
  const ready = label.trim() !== "";
  return (
    <Modal open onClose={onClose} labelledBy={titleId}>
      {/* A real form, so Enter commits it: two fields a reader types into and
          then has to reach for a button is not how anyone adds a list entry. */}
      <form
        className="form-stack"
        onSubmit={(e) => {
          e.preventDefault();
          if (!ready || create.isPending) return;
          create.mutate(
            { label: label.trim(), intent },
            { onSuccess: onClose },
          );
        }}
      >
        <h2 className="t-h3 modal-title" id={titleId}>
          {t("leadSources.newLabel")}
        </h2>
        {create.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(create.error, t)}
          </Callout>
        )}
        <Field label={t("leadSources.labelField")}>
          {(control) => (
            <TextInput
              {...control}
              data-testid="lead-source-new-label"
              placeholder={t("leadSources.newPlaceholder")}
              value={label}
              onChange={(e) => setLabel(e.target.value)}
            />
          )}
        </Field>
        {/* The hint rides on the field it qualifies rather than sitting under
            the form as a loose paragraph: what the weight DOES is the thing a
            reader needs while choosing it. */}
        <Field
          label={t("leadSources.intent")}
          hint={t("leadSources.intentHint")}
        >
          {(control) => (
            <Select
              {...control}
              value={intent}
              onChange={(value) => {
                if (isOption(value, INTENTS)) setIntent(value);
              }}
              options={INTENTS.map((value) => ({
                value,
                label: t(intentLabel[value]),
              }))}
            />
          )}
        </Field>
        <div className="form-actions">
          <Button variant="ghost" onClick={onClose}>
            {t("deals.cancel")}
          </Button>
          {/* Two facts, two props: `!ready` is a form with nothing in it yet
              and `isPending` is a write already on its way, and one `disabled`
              covering both draws them the same. */}
          <Button
            type="submit"
            variant="primary"
            disabled={!create.isPending && !ready}
            pending={create.isPending}
          >
            {t("leadSources.add")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function LeadSourcesCard() {
  const t = useT();
  const canCreate = useCanWrite("custom_field", "create");
  const canEdit = useCanWrite("custom_field", "update");
  const canRemove = useCanWrite("custom_field", "delete");
  const query = useLeadSources();
  const { create, update, remove } = useSourceMutations();
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<LeadSource | null>(null);
  // `remove` is not here: its refusal belongs to the confirm dialog that asked
  // for it, and a sentence in both places reads as two failures. What is left
  // are the writes with nowhere else to speak — a rename, a re-weight, a switch
  // flip, and the one-click adopt of a discovered value.
  const failure = [create, update].find((m) => m.isError);
  // Read off the settled answer rather than through a second QueryGate: the
  // discovered values are a row of their own, and one query cannot be gated
  // twice without drawing its wait twice.
  const administered = rowsOf(query.data?.data);
  const discovered = rowsOf(query.data?.discovered);
  return (
    <Panel title={t("leadSources.title")}>
      {/* No `form-stack` on the body: the description already pays for its own
          interval to the rows (`.settings-panel-sub`), and a stack's gap on top
          of that margin — margins do not collapse in a flex container — put 28px
          under a line every other settings card sets 16px below. The blocks that
          are NOT rows take their interval from `.lead-vocab-notices`. */}
      <PanelBody>
        <p className="settings-panel-sub">{t("leadSources.sub")}</p>
        <SettingList>
          {/* The sources are the SUBJECT of this card rather than an answer to
              a question beside them, so they take the row's full width. */}
          <SettingRow
            label={t("leadSources.listLabel")}
            layout="stack"
            control={
              <QueryGate query={query}>
                {(list) => (
                  <ul
                    className="lead-vocab-list"
                    data-testid="lead-source-list"
                  >
                    {rowsOf(list.data).map((source) => (
                      <LeadSourceRow
                        key={source.id}
                        source={source}
                        canEdit={canEdit}
                        canRemove={canRemove}
                        onUpdate={(patch) =>
                          update.mutate({ id: source.id, ...patch })
                        }
                        onRemove={() => setRemoving(source)}
                      />
                    ))}
                  </ul>
                )}
              </QueryGate>
            }
          />
          {/* Absent until there is one: a value seen on a lead but missing from
              the list is a fact about the integrations this installation runs,
              and a row that said "none" would be reporting on nothing. */}
          {discovered.length > 0 && (
            <SettingRow
              label={t("leadSources.discovered")}
              description={t("leadSources.discoveredSub")}
              layout="stack"
              control={
                <ul
                  className="lead-vocab-list"
                  data-testid="lead-source-discovered"
                >
                  {discovered.map((found) => (
                    <li key={found.key} className="lead-vocab-row">
                      <span>{sourceKeyLabel(found.key, administered, t)}</span>
                      <span className="t-mono t-caption lead-vocab-key">
                        {found.key}
                      </span>
                      <span className="t-caption lead-vocab-count">
                        {t("leadSources.leadCount", {
                          count: found.lead_count,
                        })}
                      </span>
                      <span className="lead-vocab-flags">
                        {canCreate && (
                          <Button
                            small
                            onClick={() =>
                              create.mutate({
                                key: found.key,
                                label: sourceKeyLabel(
                                  found.key,
                                  administered,
                                  t,
                                ),
                                intent: "neutral",
                              })
                            }
                          >
                            {t("leadSources.adopt")}
                          </Button>
                        )}
                      </span>
                    </li>
                  ))}
                </ul>
              }
            />
          )}
          {/* Two inputs submitted together, so the form lives behind this verb
              and the rows above stay a list of answers. Absent without the
              create grant, exactly as the inline form it replaces was. */}
          {canCreate && (
            <SettingRow
              label={t("leadSources.newLabel")}
              control={
                <Button variant="ghost" onClick={() => setAdding(true)}>
                  {t("leadSources.addOpen")}
                </Button>
              }
            />
          )}
        </SettingList>
        {/* The card's own band under the rows, not one more row: the posture is
            said once for the whole card rather than on each of a dozen refused
            controls — the boundary is the same for every row in the list — and a
            refused write belongs to the card the row it failed on sits in. */}
        {(!canEdit || failure) && (
          <div className="lead-vocab-notices">
            {!canEdit && <p className="t-small">{t("leadSources.readOnly")}</p>}
            {failure && (
              <Callout tone="danger" live="alert">
                {problemMessageOf(failure.error, t)}
              </Callout>
            )}
          </div>
        )}
        {/* Closing clears the refusal with the form that carried it: the
            dialog's own Callout and the card's read the same sentence, and
            leaving it behind would report a failed add over a card the reader
            has already walked away from. */}
        {adding && (
          <AddSourceDialog
            create={create}
            onClose={() => {
              create.reset();
              setAdding(false);
            }}
          />
        )}
        <ConfirmModal
          open={removing !== null}
          onClose={() => {
            remove.reset();
            setRemoving(null);
          }}
          title={t("leadSources.removeTitle")}
          confirmLabel={t("leadSources.remove")}
          confirmVariant="danger"
          pending={remove.isPending}
          error={remove.isError ? problemMessageOf(remove.error, t) : null}
          onConfirm={() => {
            if (removing) {
              remove.mutate(removing.id, {
                onSuccess: () => setRemoving(null),
              });
            }
          }}
        >
          <p className="t-small">
            {t("leadSources.removeBody", { label: removing?.label ?? "" })}
          </p>
        </ConfirmModal>
      </PanelBody>
    </Panel>
  );
}

function useReasonMutations() {
  const queryClient = useQueryClient();
  const invalidate = () => {
    void queryClient.invalidateQueries({
      queryKey: LEAD_DISQUALIFY_REASONS_KEY,
    });
  };
  const create = useMutation({
    mutationFn: async (body: { label: string }) => {
      const { data, error } = await api.POST("/lead-disqualify-reasons", {
        body,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: invalidate,
  });
  const update = useMutation({
    mutationFn: async ({
      id,
      ...body
    }: {
      id: string;
      label?: string;
      active?: boolean;
    }) => {
      const { data, error } = await api.PATCH("/lead-disqualify-reasons/{id}", {
        params: { path: { id } },
        body,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE("/lead-disqualify-reasons/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: invalidate,
  });
  return { create, update, remove };
}

export function LeadDisqualifyReasonsCard() {
  const t = useT();
  const canCreate = useCanWrite("custom_field", "create");
  const canEdit = useCanWrite("custom_field", "update");
  const canRemove = useCanWrite("custom_field", "delete");
  const query = useLeadDisqualifyReasons();
  const { create, update, remove } = useReasonMutations();
  const [label, setLabel] = useState("");
  const [removing, setRemoving] = useState<LeadDisqualifyReason | null>(null);
  // `remove` speaks in its own confirm dialog, so it is not repeated here —
  // see LeadSourcesCard, which draws the same distinction for the same reason.
  const failure = [create, update].find((m) => m.isError);
  return (
    <Panel title={t("leadReasons.title")}>
      {/* Plain body, for the reason the sources card above carries in full. */}
      <PanelBody>
        <p className="settings-panel-sub">{t("leadReasons.sub")}</p>
        <SettingList>
          {/* The reasons are the subject of this card, so they take the row's
              full width — the same shape the sources list above takes, which is
              what makes a reason row and a source row line up. */}
          <SettingRow
            label={t("leadReasons.listLabel")}
            layout="stack"
            control={
              <QueryGate query={query}>
                {(reasons) => (
                  <ul
                    className="lead-vocab-list"
                    data-testid="lead-reason-list"
                  >
                    {rowsOf(reasons).map((reason) => {
                      const count = reason.lead_count ?? 0;
                      const builtIn = reason.system === true;
                      const removable = canRemove && !builtIn && count === 0;
                      return (
                        <li
                          key={reason.id}
                          className="lead-vocab-row"
                          data-testid={`lead-reason-${reason.id}`}
                        >
                          <RenameField
                            label={t("leadReasons.labelFor", {
                              label: reason.label,
                            })}
                            value={reason.label}
                            canEdit={canEdit}
                            onSave={(next) =>
                              update.mutate({ id: reason.id, label: next })
                            }
                          />
                          <span className="t-caption lead-vocab-count">
                            {t("leadReasons.leadCount", { count })}
                          </span>
                          <span className="lead-vocab-flags">
                            {builtIn && (
                              <Badge>{t("leadSources.builtIn")}</Badge>
                            )}
                            <Switch
                              label={t("leadSources.activeFor", {
                                label: reason.label,
                              })}
                              labelHidden
                              checked={reason.active}
                              disabled={!canEdit}
                              onChange={(next) =>
                                update.mutate({ id: reason.id, active: next })
                              }
                            />
                            {removable ? (
                              <Button
                                small
                                variant="danger"
                                onClick={() => setRemoving(reason)}
                              >
                                {t("leadSources.remove")}
                              </Button>
                            ) : (
                              canRemove && (
                                <span
                                  className="t-caption"
                                  title={
                                    builtIn
                                      ? t("leadSources.builtInKept")
                                      : t("leadReasons.inUse", { count })
                                  }
                                >
                                  {t("leadSources.deactivateInstead")}
                                </span>
                              )
                            )}
                          </span>
                        </li>
                      );
                    })}
                  </ul>
                )}
              </QueryGate>
            }
          />
          {/* ONE input and the verb that submits it, so it stays a row: a
              reason is a sentence and nothing else, and a dialog for a single
              text box would ask the reader to open a door to type a word. */}
          {canCreate && (
            <SettingRow
              label={t("leadReasons.newLabel")}
              control={(control) => (
                <form
                  className="lead-vocab-add settingrow-measure"
                  onSubmit={(e) => {
                    e.preventDefault();
                    if (label.trim() === "") return;
                    create.mutate(
                      { label: label.trim() },
                      { onSuccess: () => setLabel("") },
                    );
                  }}
                >
                  <TextInput
                    {...control}
                    data-testid="lead-reason-new-label"
                    value={label}
                    onChange={(e) => setLabel(e.target.value)}
                  />
                  <Button
                    small
                    type="submit"
                    variant="primary"
                    disabled={create.isPending}
                  >
                    {t("leadReasons.add")}
                  </Button>
                </form>
              )}
            />
          )}
        </SettingList>
        {(!canEdit || failure) && (
          <div className="lead-vocab-notices">
            {!canEdit && <p className="t-small">{t("leadSources.readOnly")}</p>}
            {failure && (
              <Callout tone="danger" live="alert">
                {problemMessageOf(failure.error, t)}
              </Callout>
            )}
          </div>
        )}
        <ConfirmModal
          open={removing !== null}
          onClose={() => {
            remove.reset();
            setRemoving(null);
          }}
          title={t("leadReasons.removeTitle")}
          confirmLabel={t("leadSources.remove")}
          confirmVariant="danger"
          pending={remove.isPending}
          error={remove.isError ? problemMessageOf(remove.error, t) : null}
          onConfirm={() => {
            if (removing) {
              remove.mutate(removing.id, {
                onSuccess: () => setRemoving(null),
              });
            }
          }}
        >
          <p className="t-small">
            {t("leadReasons.removeBody", { label: removing?.label ?? "" })}
          </p>
        </ConfirmModal>
      </PanelBody>
    </Panel>
  );
}

// The first-response target: off by default, and the number is the
// installation's own. The switch writes when flipped; the target commits on
// Enter or blur like a rename, only once the value is a whole number in range.
//
// Two settings, two rows — this card is the simple posture on the tab, and it
// holds no list, no form and no dialog. A switch and a bounded number each
// ANSWER their row's question, so both sit in the right column and the reader
// audits the pair down one column.
export function LeadHandlingCard() {
  const t = useT();
  const canEdit = useCanWrite("custom_field", "update");
  const query = useLeadSettings();
  const update = useUpdateLeadSettings();
  const [draft, setDraft] = useState<string | null>(null);
  const [targetError, setTargetError] = useState<string | null>(null);
  // Minted unconditionally: a hook may not depend on whether the value in the
  // box is currently refused.
  const targetErrorId = useId();
  return (
    <Panel title={t("leadHandling.title")}>
      {/* Plain body, for the reason the sources card carries in full. */}
      <PanelBody>
        <p className="settings-panel-sub">{t("leadHandling.sub")}</p>
        <QueryGate query={query}>
          {(settings) => {
            const shown =
              draft ?? String(settings.first_response_target_minutes);
            const commit = () => {
              const minutes = Number(shown);
              if (minutes === settings.first_response_target_minutes) {
                setDraft(null);
                setTargetError(null);
                return;
              }
              if (
                !Number.isInteger(minutes) ||
                minutes < TARGET_MIN_MINUTES ||
                minutes > TARGET_MAX_MINUTES
              ) {
                // The typed value stays so it can be corrected, and the
                // field says what it wants.
                setTargetError(t("leadHandling.targetOutOfRange"));
                return;
              }
              setTargetError(null);
              update.mutate(
                { first_response_target_minutes: minutes },
                // A refused write keeps the draft for the reader to fix; a
                // landed one clears it so the field reads the stored value.
                { onSuccess: () => setDraft(null) },
              );
            };
            return (
              <SettingList>
                {/* The posture comes first: the number below is only a
                    judgement the switch above it makes readable. */}
                <SettingRow
                  label={t("leadHandling.firstResponse")}
                  description={t("leadHandling.firstResponseHint")}
                  control={(control) => (
                    // The switch keeps its own hidden label — it owns its
                    // accessible name by design, and pointing it at the row's
                    // span as well would name it twice — but it takes the row's
                    // DESCRIPTION, or the sentence saying what the setting does
                    // reaches nobody who cannot see it. `reason` refuses the
                    // flip AND says why, which is what a stateful control a
                    // permission denies owes its reader.
                    <Switch
                      describedBy={control["aria-describedby"]}
                      label={t("leadHandling.firstResponse")}
                      labelHidden
                      checked={settings.first_response_enabled}
                      pending={update.isPending}
                      reason={canEdit ? undefined : t("leadSources.readOnly")}
                      testId="lead-first-response-switch"
                      onChange={(next) =>
                        update.mutate({ first_response_enabled: next })
                      }
                    />
                  )}
                />
                <SettingRow
                  label={t("leadHandling.targetMinutes")}
                  description={t("leadHandling.targetHint")}
                  control={(control) => (
                    <div className="settingrow-measure lead-handling-target">
                      <TextInput
                        {...control}
                        // The row already describes the field; a value out of
                        // range ADDS the refusal to that description rather
                        // than replacing it, so a reader hears the rule and how
                        // they broke it.
                        aria-describedby={
                          [
                            control["aria-describedby"],
                            targetError === null ? null : targetErrorId,
                          ]
                            .filter(Boolean)
                            .join(" ") || undefined
                        }
                        aria-invalid={targetError === null ? undefined : true}
                        data-testid="lead-first-response-target"
                        inputMode="numeric"
                        value={shown}
                        disabled={!canEdit || !settings.first_response_enabled}
                        onChange={(e) => setDraft(e.target.value)}
                        onBlur={commit}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") {
                            e.preventDefault();
                            commit();
                          }
                        }}
                      />
                      {targetError !== null && (
                        // `.field-error` is the catalog's spelling of "why
                        // this value was refused" — same ink, same size, same
                        // `role="alert"` as the one `Field` renders, so a
                        // refusal in a row reads exactly like a refusal in a
                        // form.
                        <p
                          className="field-error lead-handling-error"
                          id={targetErrorId}
                          role="alert"
                        >
                          {targetError}
                        </p>
                      )}
                    </div>
                  )}
                />
              </SettingList>
            );
          }}
        </QueryGate>
        {/* Under the rows it belongs to, in the card's own band — the same place
            the two vocabulary cards above report a refused write, so all three
            say it in one place. */}
        {update.isError && (
          <div className="lead-vocab-notices">
            <Callout tone="danger" live="alert">
              {problemMessageOf(update.error, t)}
            </Callout>
          </div>
        )}
      </PanelBody>
    </Panel>
  );
}
