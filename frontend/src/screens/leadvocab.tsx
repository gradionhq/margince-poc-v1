import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import { useCanWrite } from "../app/capability";
import { isOption } from "../app/options";
import { Badge, Button, Field, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { Select } from "../design-system/select";
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

export function LeadSourcesCard() {
  const t = useT();
  const canCreate = useCanWrite("custom_field", "create");
  const canEdit = useCanWrite("custom_field", "update");
  const canRemove = useCanWrite("custom_field", "delete");
  const query = useLeadSources();
  const { create, update, remove } = useSourceMutations();
  const [label, setLabel] = useState("");
  const [intent, setIntent] = useState<LeadSourceIntent>("neutral");
  const [removing, setRemoving] = useState<LeadSource | null>(null);
  const denialId = useId();
  const failure = [create, update, remove].find((m) => m.isError);
  return (
    <Panel title={t("leadSources.title")}>
      <PanelBody className="form-stack">
        <p className="t-caption">{t("leadSources.sub")}</p>
        {!canEdit && (
          <p className="t-small" id={denialId}>
            {t("leadSources.readOnly")}
          </p>
        )}
        {failure && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(failure.error, t)}
          </Callout>
        )}
      </PanelBody>
      <QueryGate query={query}>
        {(list) => (
          <>
            <ul className="lead-vocab-list" data-testid="lead-source-list">
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
            {rowsOf(list.discovered).length > 0 && (
              <PanelRow>
                <p className="t-caption">{t("leadSources.discoveredSub")}</p>
                <ul
                  className="lead-vocab-list"
                  data-testid="lead-source-discovered"
                >
                  {rowsOf(list.discovered).map((found) => (
                    <li key={found.key} className="lead-vocab-row">
                      <span>
                        {sourceKeyLabel(found.key, rowsOf(list.data), t)}
                      </span>
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
                                  rowsOf(list.data),
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
              </PanelRow>
            )}
          </>
        )}
      </QueryGate>
      {canCreate && (
        <PanelRow>
          <form
            className="lead-vocab-add"
            onSubmit={(e) => {
              e.preventDefault();
              if (label.trim() === "") return;
              create.mutate(
                { label: label.trim(), intent },
                { onSuccess: () => setLabel("") },
              );
            }}
          >
            <Field label={t("leadSources.newLabel")}>
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
            <Field label={t("leadSources.intent")}>
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
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {t("leadSources.add")}
            </Button>
          </form>
          <p className="t-caption">{t("leadSources.intentHint")}</p>
        </PanelRow>
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
            remove.mutate(removing.id, { onSuccess: () => setRemoving(null) });
          }
        }}
      >
        <p className="t-small">
          {t("leadSources.removeBody", { label: removing?.label ?? "" })}
        </p>
      </ConfirmModal>
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
  const failure = [create, update, remove].find((m) => m.isError);
  return (
    <Panel title={t("leadReasons.title")}>
      <PanelBody className="form-stack">
        <p className="t-caption">{t("leadReasons.sub")}</p>
        {!canEdit && <p className="t-small">{t("leadSources.readOnly")}</p>}
        {failure && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(failure.error, t)}
          </Callout>
        )}
      </PanelBody>
      <QueryGate query={query}>
        {(reasons) => (
          <ul className="lead-vocab-list" data-testid="lead-reason-list">
            {rowsOf(reasons).map((reason) => {
              const count = reason.lead_count ?? 0;
              const builtIn = reason.system === true;
              const removable = canRemove && !builtIn && count === 0;
              return (
                <li
                  key={reason.id}
                  className="lead-vocab-row lead-vocab-row-reason"
                  data-testid={`lead-reason-${reason.id}`}
                >
                  <RenameField
                    label={t("leadReasons.labelFor", { label: reason.label })}
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
                    {builtIn && <Badge>{t("leadSources.builtIn")}</Badge>}
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
      {canCreate && (
        <PanelRow>
          <form
            className="lead-vocab-add"
            onSubmit={(e) => {
              e.preventDefault();
              if (label.trim() === "") return;
              create.mutate(
                { label: label.trim() },
                { onSuccess: () => setLabel("") },
              );
            }}
          >
            <Field label={t("leadReasons.newLabel")}>
              {(control) => (
                <TextInput
                  {...control}
                  data-testid="lead-reason-new-label"
                  value={label}
                  onChange={(e) => setLabel(e.target.value)}
                />
              )}
            </Field>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {t("leadReasons.add")}
            </Button>
          </form>
        </PanelRow>
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
            remove.mutate(removing.id, { onSuccess: () => setRemoving(null) });
          }
        }}
      >
        <p className="t-small">
          {t("leadReasons.removeBody", { label: removing?.label ?? "" })}
        </p>
      </ConfirmModal>
    </Panel>
  );
}

// The first-response target: off by default, and the number is the
// installation's own. The switch writes when flipped; the target commits on
// Enter or blur like a rename, only once the value is a whole number in range.
export function LeadHandlingCard() {
  const t = useT();
  const canEdit = useCanWrite("custom_field", "update");
  const query = useLeadSettings();
  const update = useUpdateLeadSettings();
  const [draft, setDraft] = useState<string | null>(null);
  const [targetError, setTargetError] = useState<string | null>(null);
  return (
    <Panel title={t("leadHandling.title")}>
      <PanelBody className="form-stack">
        <p className="t-caption">{t("leadHandling.sub")}</p>
        {update.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(update.error, t)}
          </Callout>
        )}
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
              <div className="lead-handling">
                <Switch
                  label={t("leadHandling.firstResponse")}
                  hint={t("leadHandling.firstResponseHint")}
                  checked={settings.first_response_enabled}
                  pending={update.isPending}
                  reason={canEdit ? undefined : t("leadSources.readOnly")}
                  testId="lead-first-response-switch"
                  onChange={(next) =>
                    update.mutate({ first_response_enabled: next })
                  }
                />
                <Field
                  label={t("leadHandling.targetMinutes")}
                  hint={t("leadHandling.targetHint")}
                  error={targetError ?? undefined}
                >
                  {(control) => (
                    <TextInput
                      {...control}
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
                  )}
                </Field>
              </div>
            );
          }}
        </QueryGate>
      </PanelBody>
    </Panel>
  );
}
