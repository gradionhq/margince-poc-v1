import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { EntityKind } from "../app/entity";
import {
  Button,
  Field,
  Modal,
  SectionHeader,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { entityTimelineKeys, taskWriteKeys } from "./activitykeys";
import { problemMessageOf, throwProblem, useSorMode } from "./common";

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
  initialKind,
}: Readonly<{
  entityType: EntityKind;
  entityId: string;
  onLogged?: () => void;
  // The kind the form starts on, when the caller already knows which one the
  // reader asked for. Absent means note, the ordinary case.
  initialKind?: "note" | "task";
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<ActivityDraft>(
    initialKind ? { ...EMPTY_DRAFT, kind: initialKind } : EMPTY_DRAFT,
  );

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
        throwProblem(error, t);
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
        <Field label={t("log.kind")}>
          {(control) => (
            <Select
              {...control}
              options={[
                { value: "note", label: t("log.kindNote") },
                { value: "task", label: t("log.kindTask") },
              ]}
              value={draft.kind}
              onChange={(value) =>
                setField({ kind: value === "task" ? "task" : "note" })
              }
            />
          )}
        </Field>
        {/* Only a task carries a due date, but the field stays in place for a
            note — disabled, not hidden. Mounting it on the kind switch moved
            every control below it down while the writer was reading them, and
            `hidden` would do the same thing by another name (it is
            display:none). Disabled keeps the row's height AND shows the reader
            that the field exists and why it is not theirs to fill yet. */}
        <Field label={t("log.dueAt")}>
          {(control) => (
            <TextInput
              {...control}
              type="date"
              value={draft.dueAt}
              disabled={draft.kind !== "task"}
              onChange={(event) => setField({ dueAt: event.target.value })}
            />
          )}
        </Field>
      </div>
      <Field label={t("log.subject")} required>
        {(control) => (
          <TextInput
            {...control}
            value={draft.subject}
            onChange={(event) => setField({ subject: event.target.value })}
          />
        )}
      </Field>
      <Field label={t("log.body")}>
        {(control) => (
          <Textarea
            {...control}
            rows={3}
            value={draft.body}
            onChange={(event) => setField({ body: event.target.value })}
          />
        )}
      </Field>
      {log.isError && (
        <p className="t-caption form-error">{problemMessageOf(log.error, t)}</p>
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
  initialKind,
  openOnMount,
  triggerLabel,
  onClose,
}: Readonly<{
  entityType: EntityKind;
  entityId: string;
  // The kind the form starts on. A suggestion that says "no task says what
  // happens next" opens straight onto a task rather than making the reader
  // pick the kind the advice already named.
  initialKind?: "note" | "task";
  // Rendered already open, with no trigger button — for a caller that IS the
  // trigger (a suggestion's action), rather than a toolbar offering the verb.
  openOnMount?: boolean;
  // What the trigger says. A header offering two ways into this form — log
  // what happened, and set what happens next — needs each button to name its
  // own verb; two buttons both reading "Log activity" is a toolbar that has
  // stopped telling the reader anything.
  triggerLabel?: MessageKey;
  onClose?: () => void;
}>) {
  const t = useT();
  const titleId = useId();
  const [open, setOpen] = useState(Boolean(openOnMount));
  const overlay = useSorMode() === "overlay";
  const close = () => {
    setOpen(false);
    onClose?.();
  };
  if (overlay) {
    return null;
  }
  return (
    <>
      {!openOnMount && (
        <Button small onClick={() => setOpen(true)}>
          {t(triggerLabel ?? "log.title")}
        </Button>
      )}
      <Modal open={open} onClose={close} labelledBy={titleId}>
        <h2 id={titleId} className="t-h2 modal-title">
          {t("log.title")}
        </h2>
        <LogActivityForm
          entityType={entityType}
          entityId={entityId}
          initialKind={initialKind}
          onLogged={close}
        />
      </Modal>
    </>
  );
}
