import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { EntityKind } from "../app/entity";
import {
  Button,
  Modal,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { useT } from "../i18n";
import { entityTimelineKeys, taskWriteKeys } from "./activitykeys";
import { problemMessage, useSorMode } from "./common";

// Log a note or task from a 360 (person/company/deal/lead): the contract's
// logActivity POST, linked to the record being viewed, occurred_at stamped
// at submit, source=manual. On success every read that renders this record's
// timeline is invalidated (see activitykeys) so the fresh entry appears
// without a reload. Server-side validation is the truth — a 422 renders its
// RFC 7807 detail verbatim.

type ActivityDraft = {
  kind: "note" | "task";
  subject: string;
  body: string;
  // yyyy-mm-dd from the date input; only a task carries a due date.
  dueAt: string;
};

const EMPTY_DRAFT: ActivityDraft = {
  kind: "note",
  subject: "",
  body: "",
  dueAt: "",
};

/**
 * LogActivityForm is the composer itself, without a frame, so the same fields
 * serve the standing card on the person and deal screens and the modal the
 * company screen opens.
 */
export function LogActivityForm({
  entityType,
  entityId,
  onLogged,
}: Readonly<{
  entityType: EntityKind;
  entityId: string;
  onLogged?: () => void;
}>) {
  const t = useT();
  const formId = useId();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<ActivityDraft>(EMPTY_DRAFT);

  const log = useMutation({
    mutationFn: async (input: ActivityDraft) => {
      const { data, error } = await api.POST("/activities", {
        body: {
          kind: input.kind,
          subject: input.subject.trim(),
          body: input.body.trim() || null,
          occurred_at: new Date().toISOString(),
          ...(input.kind === "task" && input.dueAt
            ? { due_at: new Date(input.dueAt).toISOString() }
            : {}),
          links: [{ entity_type: entityType, entity_id: entityId }],
          source: "manual",
        },
      });
      if (error) {
        throw new Error(problemMessage(error, t));
      }
      return data;
    },
    onSuccess: (_data, input) => {
      const keys =
        input.kind === "task"
          ? taskWriteKeys(entityType, entityId)
          : entityTimelineKeys(entityType, entityId);
      for (const queryKey of keys) {
        queryClient.invalidateQueries({ queryKey });
      }
      setDraft(EMPTY_DRAFT);
      onLogged?.();
    },
  });

  const setField = (patch: Partial<ActivityDraft>) =>
    setDraft((current) => ({ ...current, ...patch }));

  return (
    <form
      className="form-stack"
      onSubmit={(event) => {
        event.preventDefault();
        log.mutate(draft);
      }}
    >
      <div className="form-row">
        <div className="field">
          <label className="t-label" htmlFor={`${formId}-kind`}>
            {t("log.kind")}
          </label>
          <select
            id={`${formId}-kind`}
            className="input"
            value={draft.kind}
            onChange={(event) =>
              setField({
                kind: event.target.value === "task" ? "task" : "note",
              })
            }
          >
            <option value="note">{t("log.kindNote")}</option>
            <option value="task">{t("log.kindTask")}</option>
          </select>
        </div>
        {/* Only a task carries a due date, but the field keeps its space in
            both states: mounting it on the kind switch moved every control
            below it down while the writer was reading them. */}
        <div className="field" hidden={draft.kind !== "task"}>
          <label className="t-label" htmlFor={`${formId}-due`}>
            {t("log.dueAt")}
          </label>
          <TextInput
            id={`${formId}-due`}
            type="date"
            value={draft.dueAt}
            disabled={draft.kind !== "task"}
            onChange={(event) => setField({ dueAt: event.target.value })}
          />
        </div>
      </div>
      <div className="field">
        <label className="t-label" htmlFor={`${formId}-subject`}>
          {t("log.subject")} *
        </label>
        <TextInput
          id={`${formId}-subject`}
          value={draft.subject}
          required
          onChange={(event) => setField({ subject: event.target.value })}
        />
      </div>
      <div className="field">
        <label className="t-label" htmlFor={`${formId}-body`}>
          {t("log.body")}
        </label>
        <textarea
          id={`${formId}-body`}
          className="textarea"
          rows={3}
          value={draft.body}
          onChange={(event) => setField({ body: event.target.value })}
        />
      </div>
      {log.isError && (
        <p className="t-caption form-error">{log.error.message}</p>
      )}
      <div className="form-actions">
        <Button
          small
          variant="primary"
          type="submit"
          disabled={log.isPending || !draft.subject.trim()}
        >
          {log.isPending ? t("log.saving") : t("log.save")}
        </Button>
      </div>
    </form>
  );
}

/**
 * LogActivity is the standing composer card the person and deal screens keep
 * open in their rail.
 */
export function LogActivity({
  entityType,
  entityId,
}: Readonly<{ entityType: EntityKind; entityId: string }>) {
  const t = useT();
  // Logging an activity writes to a mirrored record; in overlay every write
  // answers unsupported_by_sor, so the form would only fail on submit. Guarded
  // to render nothing rather than an affordance that can't work (P1/A107,
  // ADR-0018).
  const overlay = useSorMode() === "overlay";
  if (overlay) {
    return null;
  }
  return (
    <section className="card card-stack">
      <SectionHeader title={t("log.title")} sub={t("log.sub")} />
      <LogActivityForm entityType={entityType} entityId={entityId} />
    </section>
  );
}

/**
 * LogActivityAction is the same composer as a header button, for a screen
 * whose header strip is a row of actions rather than a column of cards.
 */
export function LogActivityAction({
  entityType,
  entityId,
}: Readonly<{ entityType: EntityKind; entityId: string }>) {
  const t = useT();
  const titleId = useId();
  const [open, setOpen] = useState(false);
  const overlay = useSorMode() === "overlay";
  if (overlay) {
    return null;
  }
  return (
    <>
      <Button small onClick={() => setOpen(true)}>
        {t("log.title")}
      </Button>
      <Modal open={open} onClose={() => setOpen(false)} labelledBy={titleId}>
        <h2 id={titleId} className="t-h2 modal-title">
          {t("log.title")}
        </h2>
        <LogActivityForm
          entityType={entityType}
          entityId={entityId}
          onLogged={() => setOpen(false)}
        />
      </Modal>
    </>
  );
}
